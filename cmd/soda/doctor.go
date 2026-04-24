package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/decko/soda/internal/config"
	"github.com/spf13/cobra"
)

// checkResult holds the outcome of a single diagnostic check.
type checkResult struct {
	name     string
	passed   bool
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
		checkNode,
		checkGlobalConfig,
		checkLocalConfig,
		checkConfigValid,
		checkStateDir,
	}

	var failed int
	for _, check := range checks {
		result := check(env)
		if result.passed {
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
func checkGitRepo(env *doctorEnv) checkResult {
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

// checkClaudeVersion runs claude --version and reports the output.
func checkClaudeVersion(env *doctorEnv) checkResult {
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
	return checkResult{
		name:     "claude-version",
		passed:   true,
		required: true,
		detail:   out,
	}
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

// checkNode verifies that Node.js is available in PATH (optional).
func checkNode(env *doctorEnv) checkResult {
	path, err := env.LookPath("node")
	if err != nil {
		return checkResult{
			name:     "node",
			passed:   false,
			required: false,
			detail:   "not found in PATH (optional, used by claude sandbox)",
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

// checkGlobalConfig verifies that the global config file exists at
// ~/.config/soda/config.yaml.
func checkGlobalConfig(env *doctorEnv) checkResult {
	configDir, err := env.UserConfigDir()
	if err != nil {
		return checkResult{
			name:     "global-config",
			passed:   false,
			required: true,
			detail:   "cannot determine config directory",
			fix:      "set $XDG_CONFIG_HOME or $HOME",
		}
	}
	path := filepath.Join(configDir, "soda", "config.yaml")
	if _, err := env.Stat(path); err != nil {
		return checkResult{
			name:     "global-config",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("%s not found", path),
			fix:      "run: soda init",
		}
	}
	return checkResult{
		name:     "global-config",
		passed:   true,
		required: true,
		detail:   path,
	}
}

// checkLocalConfig verifies that a project-local soda.yaml exists in the
// current working directory.
func checkLocalConfig(env *doctorEnv) checkResult {
	if _, err := env.Stat("soda.yaml"); err != nil {
		return checkResult{
			name:     "local-config",
			passed:   false,
			required: true,
			detail:   "soda.yaml not found in current directory",
			fix:      "run: soda init",
		}
	}
	return checkResult{
		name:     "local-config",
		passed:   true,
		required: true,
		detail:   "soda.yaml",
	}
}

// checkConfigValid attempts to parse the best available config file
// (local soda.yaml first, then global config.yaml) and reports whether
// it is valid.
func checkConfigValid(env *doctorEnv) checkResult {
	// Try local config first.
	if _, statErr := env.Stat("soda.yaml"); statErr == nil {
		if _, err := env.LoadConfig("soda.yaml"); err != nil {
			return checkResult{
				name:     "config-valid",
				passed:   false,
				required: true,
				detail:   fmt.Sprintf("soda.yaml: %v", err),
				fix:      "fix syntax errors in soda.yaml",
			}
		}
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
			name:     "config-valid",
			passed:   false,
			required: true,
			detail:   "no config file found to validate",
			fix:      "run: soda init",
		}
	}
	path := filepath.Join(configDir, "soda", "config.yaml")
	if _, statErr := env.Stat(path); statErr != nil {
		return checkResult{
			name:     "config-valid",
			passed:   false,
			required: true,
			detail:   "no config file found to validate",
			fix:      "run: soda init",
		}
	}
	if _, err := env.LoadConfig(path); err != nil {
		return checkResult{
			name:     "config-valid",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("%s: %v", path, err),
			fix:      "fix syntax errors in config file",
		}
	}
	return checkResult{
		name:     "config-valid",
		passed:   true,
		required: true,
		detail:   fmt.Sprintf("%s parses successfully", path),
	}
}

// checkStateDir verifies that the state directory (~/.config/soda/) exists
// and is accessible.
func checkStateDir(env *doctorEnv) checkResult {
	configDir, err := env.UserConfigDir()
	if err != nil {
		return checkResult{
			name:     "state-dir",
			passed:   false,
			required: true,
			detail:   "cannot determine config directory",
			fix:      "set $XDG_CONFIG_HOME or $HOME",
		}
	}
	dir := filepath.Join(configDir, "soda")
	info, err := env.Stat(dir)
	if err != nil {
		return checkResult{
			name:     "state-dir",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("%s not found", dir),
			fix:      "run: soda init",
		}
	}
	if !info.IsDir() {
		return checkResult{
			name:     "state-dir",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("%s exists but is not a directory", dir),
			fix:      fmt.Sprintf("remove %s and run: soda init", dir),
		}
	}
	return checkResult{
		name:     "state-dir",
		passed:   true,
		required: true,
		detail:   dir,
	}
}
