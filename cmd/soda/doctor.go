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
	UserHomeDir   func() (string, error)
	Getenv        func(key string) string // injectable os.Getenv for testable env checks

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
		UserHomeDir:   os.UserHomeDir,
		Getenv:        os.Getenv,
	}
}

// getenv returns the value of the environment variable key using the
// injected Getenv function, falling back to os.Getenv when not set.
func (e *doctorEnv) getenv(key string) string {
	if e.Getenv != nil {
		return e.Getenv(key)
	}
	return os.Getenv(key)
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
		checkConfig,
		checkConfigValid,
		checkClaudeAuth,
		checkGh,
		checkGhAuth,
		checkBranchProtection,
		checkNode,
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

	if compareSemver(ver, claude.MaxTestedCLIVersion) > 0 {
		return checkResult{
			name:   "claude-version",
			passed: true,
			detail: fmt.Sprintf("%s ⚠ newer than tested range (%s–%s); to pin: npm install -g @anthropic-ai/claude-code@%s", out, claude.MinCLIVersion, claude.MaxTestedCLIVersion, claude.MaxTestedCLIVersion),
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

// checkClaudeAuth verifies that Claude Code has a valid authentication
// method configured. The precedence chain mirrors how soda resolves
// credentials at runtime:
//
//  1. Proxy enabled in config → pass (proxy handles credentials)
//  2. ANTHROPIC_API_KEY env var set → pass
//  3. CLAUDE_CODE_USE_VERTEX env var set → pass (Vertex/GCP auth)
//  4. auth.api_key_helper in config → pass
//  5. None of the above → fail
//
// This check is optional (warning-only) because authentication can also
// be configured via Claude Code's own settings files or login flow.
func checkClaudeAuth(env *doctorEnv) checkResult {
	// 1. Proxy enabled — credentials are managed by the proxy.
	if env.ParsedConfig != nil && env.ParsedConfig.Sandbox.Proxy.Enabled {
		return checkResult{
			name:   "claude-auth",
			passed: true,
			detail: "proxy enabled (credentials managed by proxy)",
		}
	}

	// 2. ANTHROPIC_API_KEY env var.
	if env.getenv("ANTHROPIC_API_KEY") != "" {
		return checkResult{
			name:   "claude-auth",
			passed: true,
			detail: "ANTHROPIC_API_KEY is set",
		}
	}

	// 3. Vertex / GCP auth.
	if env.getenv("CLAUDE_CODE_USE_VERTEX") != "" {
		return checkResult{
			name:   "claude-auth",
			passed: true,
			detail: "CLAUDE_CODE_USE_VERTEX is set (Vertex AI auth)",
		}
	}

	// 4. api_key_helper in config.
	if env.ParsedConfig != nil && env.ParsedConfig.Auth.ApiKeyHelper != "" {
		return checkResult{
			name:   "claude-auth",
			passed: true,
			detail: fmt.Sprintf("api_key_helper configured: %s", env.ParsedConfig.Auth.ApiKeyHelper),
		}
	}

	// 5. No auth method found.
	return checkResult{
		name:   "claude-auth",
		passed: false,
		detail: "no authentication method detected",
		fix:    "set ANTHROPIC_API_KEY, configure auth.api_key_helper in soda.yaml, enable sandbox proxy, or run: claude login",
	}
}

// checkGh verifies that the GitHub CLI is available in PATH.
// Required when ticket_source is "github", optional otherwise.
func checkGh(env *doctorEnv) checkResult {
	required := env.isGitHubSource()
	path, err := env.LookPath("gh")
	if err != nil {
		detail := "not found in PATH (optional, needed for GitHub ticket source)"
		if required {
			detail = "not found in PATH (required by ticket_source: github)"
		}
		return checkResult{
			name:     "gh",
			passed:   false,
			required: required,
			detail:   detail,
			fix:      "install gh: https://cli.github.com",
		}
	}
	return checkResult{
		name:     "gh",
		passed:   true,
		required: required,
		detail:   path,
	}
}

// checkGhAuth verifies that the GitHub CLI is authenticated.
// Required when ticket_source is "github", optional otherwise.
// Skipped when gh is not installed.
func checkGhAuth(env *doctorEnv) checkResult {
	if _, err := env.LookPath("gh"); err != nil {
		return checkResult{
			name:    "gh-auth",
			skipped: true,
			detail:  "skipped (gh not found)",
		}
	}
	required := env.isGitHubSource()
	_, err := env.RunCmd("gh", "auth", "status")
	if err != nil {
		return checkResult{
			name:     "gh-auth",
			passed:   false,
			required: required,
			detail:   "gh is not authenticated",
			fix:      "run: gh auth login",
		}
	}
	return checkResult{
		name:     "gh-auth",
		passed:   true,
		required: required,
		detail:   "authenticated",
	}
}

// checkBranchProtection warns when the target repo's default branch has
// dismiss_stale_reviews enabled, which can cause auto-merge to fail after
// new pushes. Skipped when gh is not found or the config is unavailable.
// This is an optional (warning-only) check.
func checkBranchProtection(env *doctorEnv) checkResult {
	if _, err := env.LookPath("gh"); err != nil {
		return checkResult{
			name:    "branch-protection",
			skipped: true,
			detail:  "skipped (gh not found)",
		}
	}

	// Require a parsed config with GitHub repo info.
	if env.ParsedConfig == nil {
		return checkResult{
			name:    "branch-protection",
			skipped: true,
			detail:  "skipped (no config parsed)",
		}
	}

	owner := env.ParsedConfig.GitHub.Owner
	repo := env.ParsedConfig.GitHub.Repo
	if owner == "" || repo == "" {
		return checkResult{
			name:    "branch-protection",
			skipped: true,
			detail:  "skipped (github owner/repo not configured)",
		}
	}

	// Query branch protection for the default branch (main).
	// Use gh api to check the protection rules.
	out, err := env.RunCmd("gh", "api", fmt.Sprintf("repos/%s/%s/branches/main/protection", owner, repo))
	if err != nil {
		// 404 or error means no branch protection — that's fine.
		if strings.Contains(strings.ToLower(out), "not found") || strings.Contains(strings.ToLower(out), "404") {
			return checkResult{
				name:   "branch-protection",
				passed: true,
				detail: "no branch protection rules on main",
			}
		}
		return checkResult{
			name:    "branch-protection",
			skipped: true,
			detail:  fmt.Sprintf("skipped (could not query branch protection: %v)", err),
		}
	}

	if strings.Contains(out, "dismiss_stale_reviews") && strings.Contains(out, "true") {
		return checkResult{
			name:   "branch-protection",
			passed: false,
			detail: "dismiss_stale_reviews is enabled on main — auto-merge may fail after new pushes",
			fix:    "consider disabling dismiss_stale_reviews or using a merge queue",
		}
	}

	return checkResult{
		name:   "branch-protection",
		passed: true,
		detail: "no dismiss_stale_reviews on main",
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

// configLocation holds the resolved path and label for a config file.
type configLocation struct {
	path  string // absolute or relative path to the config file
	label string // human-readable label: "local" or "global"
}

// resolveConfigPath finds the best available config file using the same
// fallback chain as loadConfig and config.DefaultPath:
//
//  1. soda.yaml in CWD (project-local)
//  2. UserConfigDir()/soda/config.yaml
//  3. UserHomeDir()/.config/soda/config.yaml (fallback when UserConfigDir fails)
//
// Returns nil when no config file is found.
func resolveConfigPath(env *doctorEnv) *configLocation {
	// 1. Local config.
	if _, err := env.Stat("soda.yaml"); err == nil {
		return &configLocation{path: "soda.yaml", label: "local"}
	}

	// 2. Global config via UserConfigDir.
	configDir, err := env.UserConfigDir()
	if err == nil {
		path := filepath.Join(configDir, "soda", "config.yaml")
		if _, statErr := env.Stat(path); statErr == nil {
			return &configLocation{path: path, label: "global"}
		}
		// UserConfigDir succeeded but file not found — do NOT fall through
		// to UserHomeDir. This matches config.DefaultPath() which only uses
		// UserHomeDir when UserConfigDir() itself returns an error.
		return nil
	}

	// 3. Fallback: UserHomeDir + ".config" — only reached when UserConfigDir fails.
	if env.UserHomeDir != nil {
		home, homeErr := env.UserHomeDir()
		if homeErr == nil {
			path := filepath.Join(home, ".config", "soda", "config.yaml")
			if _, statErr := env.Stat(path); statErr == nil {
				return &configLocation{path: path, label: "global"}
			}
		}
	}

	return nil
}

// checkConfig verifies that at least one config file exists.
// Uses the shared resolveConfigPath fallback chain.
func checkConfig(env *doctorEnv) checkResult {
	loc := resolveConfigPath(env)
	if loc != nil {
		return checkResult{
			name:     "config",
			passed:   true,
			required: true,
			detail:   fmt.Sprintf("%s (%s)", loc.path, loc.label),
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
// and reports whether it is valid. Skipped if no config file was
// found by resolveConfigPath.
func checkConfigValid(env *doctorEnv) checkResult {
	loc := resolveConfigPath(env)
	if loc == nil {
		return checkResult{
			name:    "config-valid",
			skipped: true,
			detail:  "skipped (no config file found)",
		}
	}

	cfg, err := env.LoadConfig(loc.path)
	if err != nil {
		return checkResult{
			name:     "config-valid",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("%s: %v", loc.path, err),
			fix:      "fix syntax errors in " + loc.path,
		}
	}
	env.ParsedConfig = cfg
	return checkResult{
		name:     "config-valid",
		passed:   true,
		required: true,
		detail:   fmt.Sprintf("%s parses successfully", loc.path),
	}
}
