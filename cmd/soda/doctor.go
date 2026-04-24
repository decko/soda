package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/config"
	"github.com/spf13/cobra"
)

// checkResult holds the outcome of a single diagnostic check.
type checkResult struct {
	name     string
	passed   bool
	skipped  bool
	required bool
	detail   string
	fix      string
}

// doctorEnv provides dependency injection for doctor checks, enabling
// unit tests without requiring real binaries or filesystem state.
type doctorEnv struct {
	LookPath      func(file string) (string, error)
	RunCmd        func(name string, args ...string) (string, error)
	Stat          func(name string) (os.FileInfo, error)
	LoadConfig    func(path string) (*config.Config, error)
	UserConfigDir func() (string, error)

	// ParsedConfig is populated by checkConfigValid on success.
	// Downstream checks use it to adjust their required status.
	ParsedConfig *config.Config
}

// isGitHubSource reports whether the parsed config sets ticket_source to "github".
// Returns false when ParsedConfig is nil (config missing or unparseable),
// making gh checks default to optional — a safe fallback.
func (e *doctorEnv) isGitHubSource() bool {
	return e.ParsedConfig != nil && e.ParsedConfig.TicketSource == "github"
}

// defaultDoctorEnv returns a doctorEnv wired to the real OS.
func defaultDoctorEnv() *doctorEnv {
	return &doctorEnv{
		LookPath: exec.LookPath,
		RunCmd: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		Stat:          os.Stat,
		LoadConfig:    config.Load,
		UserConfigDir: os.UserConfigDir,
	}
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check prerequisites and environment health",
		Long: `Run diagnostic checks to verify that required tools are installed,
configuration files are present and valid, and the environment is
ready for soda to operate.

Each check reports ✓ (pass), ✗ (fail), or ⚠ (optional) with a
suggested fix. Only required failures cause a non-zero exit code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env := defaultDoctorEnv()
			return runDoctor(cmd.OutOrStdout(), env)
		},
	}
}

// runDoctor executes all diagnostic checks and prints results.
// Returns a non-nil error if any required check fails (exit 1).
func runDoctor(w io.Writer, env *doctorEnv) error {
	checks := []func(*doctorEnv) checkResult{
		checkGit,
		checkGitRepo,
		checkClaude,
		checkClaudeVersion,
		checkGh,
		checkGhAuth,
		checkNode,
		checkConfig,
		checkConfigValid,
	}

	var failed int
	for _, check := range checks {
		result := check(env)
		if result.skipped {
			fmt.Fprintf(w, "- %s: %s\n", result.name, result.detail)
		} else if result.passed {
			fmt.Fprintf(w, "✓ %s: %s\n", result.name, result.detail)
		} else if result.required {
			fmt.Fprintf(w, "✗ %s: %s\n", result.name, result.detail)
			if result.fix != "" {
				fmt.Fprintf(w, "  fix: %s\n", result.fix)
			}
			failed++
		} else {
			fmt.Fprintf(w, "⚠ %s: %s\n", result.name, result.detail)
			if result.fix != "" {
				fmt.Fprintf(w, "  fix: %s\n", result.fix)
			}
		}
	}

	fmt.Fprintln(w)
	if failed > 0 {
		fmt.Fprintf(w, "%d check(s) failed\n", failed)
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Fprintln(w, "All checks passed")
	return nil
}

// checkGit verifies that git is available in PATH.
func checkGit(env *doctorEnv) checkResult {
	path, err := env.LookPath("git")
	if err != nil {
		return checkResult{
			name:     "git",
			passed:   false,
			required: true,
			detail:   "not found in PATH",
			fix:      "install git: https://git-scm.com/downloads",
		}
	}
	return checkResult{
		name:     "git",
		passed:   true,
		required: true,
		detail:   path,
	}
}

// checkGitRepo verifies that the current directory is inside a git repository.
// Skipped when git itself is not installed to avoid misleading cascading failures.
func checkGitRepo(env *doctorEnv) checkResult {
	if _, err := env.LookPath("git"); err != nil {
		return checkResult{
			name:    "git-repo",
			skipped: true,
			detail:  "skipped (git not found)",
		}
	}
	_, err := env.RunCmd("git", "rev-parse", "--git-dir")
	if err != nil {
		return checkResult{
			name:     "git-repo",
			passed:   false,
			required: true,
			detail:   "not inside a git repository",
			fix:      "run from inside a git repository or run git init",
		}
	}
	return checkResult{
		name:     "git-repo",
		passed:   true,
		required: true,
		detail:   "inside a git repository",
	}
}

// checkClaude verifies that the Claude Code CLI is available in PATH.
func checkClaude(env *doctorEnv) checkResult {
	path, err := env.LookPath("claude")
	if err != nil {
		return checkResult{
			name:     "claude",
			passed:   false,
			required: true,
			detail:   "not found in PATH",
			fix:      "install Claude Code: https://docs.anthropic.com/en/docs/claude-code",
		}
	}
	return checkResult{
		name:     "claude",
		passed:   true,
		required: true,
		detail:   path,
	}
}

// checkClaudeVersion runs claude --version, reports the output, and verifies
// that the installed version meets the minimum required version.
// Skipped when claude itself is not installed to avoid cascading failures.
func checkClaudeVersion(env *doctorEnv) checkResult {
	if _, err := env.LookPath("claude"); err != nil {
		return checkResult{
			name:    "claude-version",
			skipped: true,
			detail:  "skipped (claude not found)",
		}
	}
	out, err := env.RunCmd("claude", "--version")
	if err != nil {
		return checkResult{
			name:     "claude-version",
			passed:   false,
			required: true,
			detail:   "failed to run claude --version",
			fix:      "ensure claude is installed correctly and executable",
		}
	}

	ver := extractSemver(out)
	if ver == "" {
		return checkResult{
			name:     "claude-version",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("could not parse version from: %s", out),
			fix:      "ensure claude is installed correctly",
		}
	}

	if compareSemver(ver, claude.MinCLIVersion) < 0 {
		return checkResult{
			name:     "claude-version",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("%s (minimum required: %s)", out, claude.MinCLIVersion),
			fix:      fmt.Sprintf("upgrade Claude Code to >= %s: npm update -g @anthropic-ai/claude-code", claude.MinCLIVersion),
		}
	}

	return checkResult{
		name:     "claude-version",
		passed:   true,
		required: true,
		detail:   out,
	}
}

// extractSemver extracts the first semver-like version (X.Y.Z) from a string.
// For example, "claude 2.1.81" returns "2.1.81".
func extractSemver(s string) string {
	for _, field := range strings.Fields(s) {
		parts := strings.SplitN(field, ".", 3)
		if len(parts) == 3 {
			allDigits := true
			for _, p := range parts {
				if p == "" {
					allDigits = false
					break
				}
				for _, c := range p {
					if c < '0' || c > '9' {
						allDigits = false
						break
					}
				}
			}
			if allDigits {
				return field
			}
		}
	}
	return ""
}

// compareSemver compares two semver strings (X.Y.Z).
// Returns -1 if a < b, 0 if a == b, +1 if a > b.
func compareSemver(a, b string) int {
	aParts := strings.SplitN(a, ".", 3)
	bParts := strings.SplitN(b, ".", 3)

	for i := 0; i < 3; i++ {
		ai, bi := 0, 0
		if i < len(aParts) {
			ai, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bi, _ = strconv.Atoi(bParts[i])
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// checkGh verifies that the GitHub CLI is available in PATH (optional).
func checkGh(env *doctorEnv) checkResult {
	path, err := env.LookPath("gh")
	if err != nil {
		return checkResult{
			name:     "gh",
			passed:   false,
			required: false,
			detail:   "not found in PATH (optional, needed for GitHub ticket source)",
			fix:      "install gh: https://cli.github.com",
		}
	}
	return checkResult{
		name:     "gh",
		passed:   true,
		required: false,
		detail:   path,
	}
}

// checkGhAuth verifies that the GitHub CLI is authenticated.
// Skipped when gh is not installed.
func checkGhAuth(env *doctorEnv) checkResult {
	if _, err := env.LookPath("gh"); err != nil {
		return checkResult{
			name:    "gh-auth",
			skipped: true,
			detail:  "skipped (gh not found)",
		}
	}
	_, err := env.RunCmd("gh", "auth", "status")
	if err != nil {
		return checkResult{
			name:     "gh-auth",
			passed:   false,
			required: false,
			detail:   "gh is not authenticated",
			fix:      "run: gh auth login",
		}
	}
	return checkResult{
		name:     "gh-auth",
		passed:   true,
		required: false,
		detail:   "authenticated",
	}
}

// checkNode verifies that Node.js is available in PATH (optional).
// Node.js is only needed for sandboxed execution, not for normal operation.
func checkNode(env *doctorEnv) checkResult {
	path, err := env.LookPath("node")
	if err != nil {
		return checkResult{
			name:     "node",
			passed:   false,
			required: false,
			detail:   "not found in PATH (optional, needed only for sandboxed execution)",
			fix:      "install Node.js: https://nodejs.org",
		}
	}
	return checkResult{
		name:     "node",
		passed:   true,
		required: false,
		detail:   path,
	}
}

// checkConfig verifies that at least one config file exists.
// SODA's loadConfig uses a fallback chain: soda.yaml in CWD → ~/.config/soda/config.yaml.
// Either one is sufficient.
func checkConfig(env *doctorEnv) checkResult {
	// Check local config first.
	if _, err := env.Stat("soda.yaml"); err == nil {
		return checkResult{
			name:     "config",
			passed:   true,
			required: true,
			detail:   "soda.yaml (local)",
		}
	}

	// Check global config.
	configDir, err := env.UserConfigDir()
	if err == nil {
		path := filepath.Join(configDir, "soda", "config.yaml")
		if _, statErr := env.Stat(path); statErr == nil {
			return checkResult{
				name:     "config",
				passed:   true,
				required: true,
				detail:   path + " (global)",
			}
		}
	}

	return checkResult{
		name:     "config",
		passed:   false,
		required: true,
		detail:   "no config file found (checked soda.yaml and ~/.config/soda/config.yaml)",
		fix:      "run: soda init",
	}
}

// checkConfigValid attempts to parse the best available config file
// (local soda.yaml first, then global config.yaml) and reports whether
// it is valid. Skipped if no config file was found by checkConfig.
func checkConfigValid(env *doctorEnv) checkResult {
	// Try local config first.
	if _, statErr := env.Stat("soda.yaml"); statErr == nil {
		cfg, err := env.LoadConfig("soda.yaml")
		if err != nil {
			return checkResult{
				name:     "config-valid",
				passed:   false,
				required: true,
				detail:   fmt.Sprintf("soda.yaml: %v", err),
				fix:      "fix syntax errors in soda.yaml",
			}
		}
		env.ParsedConfig = cfg
		return checkResult{
			name:     "config-valid",
			passed:   true,
			required: true,
			detail:   "soda.yaml parses successfully",
		}
	}

	// Fall back to global config.
	configDir, err := env.UserConfigDir()
	if err != nil {
		return checkResult{
			name:    "config-valid",
			skipped: true,
			detail:  "skipped (no config file found)",
		}
	}
	path := filepath.Join(configDir, "soda", "config.yaml")
	if _, statErr := env.Stat(path); statErr != nil {
		return checkResult{
			name:    "config-valid",
			skipped: true,
			detail:  "skipped (no config file found)",
		}
	}
	cfg, err := env.LoadConfig(path)
	if err != nil {
		return checkResult{
			name:     "config-valid",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("%s: %v", path, err),
			fix:      "fix syntax errors in config file",
		}
	}
	env.ParsedConfig = cfg
	return checkResult{
		name:     "config-valid",
		passed:   true,
		required: true,
		detail:   fmt.Sprintf("%s parses successfully", path),
	}
}
