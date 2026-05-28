package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/internal/sandbox"
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
	LookPath           func(file string) (string, error)
	RunCmd             func(name string, args ...string) (string, error)
	Stat               func(name string) (os.FileInfo, error)
	LoadConfig         func(path string) (*config.Config, error)
	UserConfigDir      func() (string, error)
	UserHomeDir        func() (string, error)
	Getenv             func(key string) string // injectable os.Getenv for testable env checks
	ExecInstallCmd     func(cmd string) error  // runs an install command (sh -c); nil = default
	ArapucaWrapperPath func() string           // returns path to arapuca wrapper binary; "" = not found

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

// isGitLabSource reports whether the parsed config sets ticket_source to "gitlab".
// Returns false when ParsedConfig is nil (config missing or unparseable),
// making glab checks default to optional — a safe fallback.
func (e *doctorEnv) isGitLabSource() bool {
	return e.ParsedConfig != nil && e.ParsedConfig.TicketSource == "gitlab"
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
		ExecInstallCmd: func(cmd string) error {
			c := exec.Command("sh", "-c", cmd)
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
		ArapucaWrapperPath: sandbox.WrapperBinaryPath,
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
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check prerequisites and environment health",
		Long: `Run diagnostic checks to verify that required tools are installed,
configuration files are present and valid, and the environment is
ready for soda to operate.

Each check reports ✓ (pass), ✗ (fail), or ⚠ (optional) with a
suggested fix. Only required failures cause a non-zero exit code.

Use --install to be prompted to auto-install missing agent CLIs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env := defaultDoctorEnv()
			install, _ := cmd.Flags().GetBool("install")
			return runDoctorFull(cmd.OutOrStdout(), cmd.InOrStdin(), env, install)
		},
	}

	cmd.Flags().Bool("install", false, "prompt to install missing agent CLIs")

	return cmd
}

// runDoctor executes all diagnostic checks and prints results.
// Returns a non-nil error if any required check fails (exit 1).
// Preserved for backward compatibility with existing callers and tests.
func runDoctor(w io.Writer, env *doctorEnv) error {
	return runDoctorFull(w, strings.NewReader(""), env, false)
}

// installableAgents lists agent check functions whose failures can be
// auto-installed when --install is given.
var installableAgents = map[string]bool{
	"pi":       true,
	"opencode": true,
}

// runDoctorFull is the extended version of runDoctor that supports the
// --install flag. When install is true and an agent check fails, it
// prompts the user to install the missing CLI and runs the command on
// confirmation.
func runDoctorFull(w io.Writer, stdin io.Reader, env *doctorEnv, install bool) error {
	checks := []func(*doctorEnv) checkResult{
		checkGit,
		checkGitRepo,
		checkClaude,
		checkClaudeVersion,
		checkConfig,
		checkConfigValid,
		checkArapucaWrapper,
		checkArapucaWrapperVersion,
		checkClaudeAuth,
		checkGh,
		checkGhAuth,
		checkGlab,
		checkGlabAuth,
		checkBranchProtection,
		checkCommitSigning,
		checkNode,
		checkPi,
		checkOpencode,
	}

	reader := bufio.NewReader(stdin)

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

			// Offer auto-install for known agents when --install is set.
			if install && installableAgents[result.name] {
				installed := offerInstall(w, reader, env, result.name)
				if installed {
					// Re-run the check after installation.
					recheck := check(env)
					if recheck.passed {
						fmt.Fprintf(w, "✓ %s: %s\n", recheck.name, recheck.detail)
					} else {
						fmt.Fprintf(w, "⚠ %s: installation may have failed: %s\n", recheck.name, recheck.detail)
					}
				}
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

// offerInstall prompts the user to install a missing agent CLI. Returns
// true if installation was attempted and ExecInstallCmd did not error.
func offerInstall(w io.Writer, reader *bufio.Reader, env *doctorEnv, agentName string) bool {
	info := runner.AgentByName(agentName)
	if info == nil || len(info.InstallCmds) == 0 {
		return false
	}

	// Pick the best install method: prefer one whose package manager is
	// available, otherwise fall back to the first method.
	var chosen *runner.InstallMethod
	for idx := range info.InstallCmds {
		method := &info.InstallCmds[idx]
		// Check if the package manager (first word of the command) is available.
		parts := strings.Fields(method.Command)
		if len(parts) > 0 {
			if _, err := env.LookPath(parts[0]); err == nil {
				chosen = method
				break
			}
		}
	}
	if chosen == nil {
		chosen = &info.InstallCmds[0]
	}

	fmt.Fprintf(w, "  Auto-install %s via %s? (%s) [y/N] ", agentName, chosen.Label, chosen.Command)
	line, err := reader.ReadString('\n')
	if err != nil && err.Error() != "EOF" {
		return false
	}
	line = strings.TrimSpace(line)
	if !strings.EqualFold(line, "y") && !strings.EqualFold(line, "yes") {
		return false
	}

	execFn := env.ExecInstallCmd
	if execFn == nil {
		return false
	}

	fmt.Fprintf(w, "  Running: %s\n", chosen.Command)
	if installErr := execFn(chosen.Command); installErr != nil {
		fmt.Fprintf(w, "  Install failed: %v\n", installErr)
		return false
	}
	return true
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
			name:     "claude-version",
			passed:   true,
			required: true,
			detail:   fmt.Sprintf("%s ⚠ newer than tested range (%s–%s); to pin: npm install -g @anthropic-ai/claude-code@%s", out, claude.MinCLIVersion, claude.MaxTestedCLIVersion, claude.MaxTestedCLIVersion),
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

// checkGlab verifies that the GitLab CLI (glab) is available in PATH.
// Required when ticket_source is "gitlab", optional otherwise.
func checkGlab(env *doctorEnv) checkResult {
	required := env.isGitLabSource()
	path, err := env.LookPath("glab")
	if err != nil {
		detail := "not found in PATH (optional, needed for GitLab ticket source)"
		if required {
			detail = "not found in PATH (required by ticket_source: gitlab)"
		}
		return checkResult{
			name:     "glab",
			passed:   false,
			required: required,
			detail:   detail,
			fix:      "install glab: https://gitlab.com/gitlab-org/cli",
		}
	}
	return checkResult{
		name:     "glab",
		passed:   true,
		required: required,
		detail:   path,
	}
}

// checkGlabAuth verifies that the GitLab CLI is authenticated.
// Required when ticket_source is "gitlab", optional otherwise.
// Skipped when glab is not installed.
func checkGlabAuth(env *doctorEnv) checkResult {
	if _, err := env.LookPath("glab"); err != nil {
		return checkResult{
			name:    "glab-auth",
			skipped: true,
			detail:  "skipped (glab not found)",
		}
	}
	required := env.isGitLabSource()
	_, err := env.RunCmd("glab", "auth", "status")
	if err != nil {
		return checkResult{
			name:     "glab-auth",
			passed:   false,
			required: required,
			detail:   "glab is not authenticated",
			fix:      "run: glab auth login",
		}
	}
	return checkResult{
		name:     "glab-auth",
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

// checkCommitSigning verifies that git commit signing is configured and
// that the signing key is reachable. Supports both GPG and SSH formats.
//
// Not configured (commit.gpgsign != "true") → warn (optional).
// Configured but key unreachable → fail (required).
//
// Skipped when git is not found or not inside a git repository.
func checkCommitSigning(env *doctorEnv) checkResult {
	if _, err := env.LookPath("git"); err != nil {
		return checkResult{
			name:    "commit-signing",
			skipped: true,
			detail:  "skipped (git not found)",
		}
	}

	if _, err := env.RunCmd("git", "rev-parse", "--git-dir"); err != nil {
		return checkResult{
			name:    "commit-signing",
			skipped: true,
			detail:  "skipped (not a git repository)",
		}
	}

	// Check whether commit signing is enabled.
	gpgsign, err := env.RunCmd("git", "config", "commit.gpgsign")
	if err != nil || gpgsign != "true" {
		return checkResult{
			name:   "commit-signing",
			passed: false,
			detail: "commit signing is not enabled",
			fix:    "run: git config --global commit.gpgsign true",
		}
	}

	// Determine signing format (default is "gpg").
	format, _ := env.RunCmd("git", "config", "gpg.format")
	if format == "" {
		format = "gpg"
	}

	// Get the signing key.
	signingKey, err := env.RunCmd("git", "config", "user.signingkey")
	if err != nil || signingKey == "" {
		return checkResult{
			name:     "commit-signing",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("commit signing enabled (%s) but user.signingkey is not set", format),
			fix:      fmt.Sprintf("run: git config --global user.signingkey <your-%s-key>", format),
		}
	}

	// Verify key reachability based on format.
	if format == "ssh" {
		return checkCommitSigningSSH(env, signingKey)
	}
	return checkCommitSigningGPG(env, signingKey)
}

// checkCommitSigningSSH verifies that the configured SSH signing key is
// loaded in the ssh-agent. Handles both file paths and key:: inline keys.
func checkCommitSigningSSH(env *doctorEnv, signingKey string) checkResult {
	if _, err := env.LookPath("ssh-add"); err != nil {
		return checkResult{
			name:    "commit-signing",
			passed:  true,
			skipped: true,
			detail:  "ssh-add not found in PATH, skipping commit-signing check",
		}
	}
	// Handle key:: inline format — any loaded key is a pass.
	if strings.HasPrefix(signingKey, "key::") {
		out, err := env.RunCmd("ssh-add", "-l")
		if err != nil || strings.Contains(strings.ToLower(out), "no identities") {
			return checkResult{
				name:     "commit-signing",
				passed:   false,
				required: true,
				detail:   "commit signing enabled (ssh, inline key) but ssh-agent has no identities",
				fix:      "run: ssh-add",
			}
		}
		return checkResult{
			name:   "commit-signing",
			passed: true,
			detail: "ssh signing configured (inline key, agent has keys)",
		}
	}

	// Handle bare inline public key (e.g. "ssh-ed25519 AAAA... comment").
	// These look like file paths to the existing logic but are actually
	// public key literals. Match type+blob against ssh-add -L output.
	if isInlinePublicKey(signingKey) {
		out, err := env.RunCmd("ssh-add", "-L")
		if err == nil && sshBareKeyFoundInAgentOutput(signingKey, out) {
			return checkResult{
				name:   "commit-signing",
				passed: true,
				detail: "ssh signing configured (inline public key, agent has matching key)",
			}
		}
		return checkResult{
			name:     "commit-signing",
			passed:   false,
			required: true,
			detail:   "commit signing enabled (ssh, inline public key) but key not found in ssh-agent",
			fix:      "run: ssh-add",
		}
	}

	// File-based key: resolve path and match against ssh-add -l output.
	keyPath := resolveSSHKeyPath(env, signingKey)

	out, err := env.RunCmd("ssh-add", "-l")
	if err != nil {
		return checkResult{
			name:     "commit-signing",
			passed:   false,
			required: true,
			detail:   "commit signing enabled (ssh) but ssh-agent is not available",
			fix:      "run: eval $(ssh-agent) && ssh-add",
		}
	}

	// ssh-add -l outputs lines like:
	//   256 SHA256:abc... /home/user/.ssh/id_ed25519 (ED25519)
	// Match the resolved private key path (without .pub suffix).
	if strings.Contains(out, keyPath) {
		return checkResult{
			name:   "commit-signing",
			passed: true,
			detail: fmt.Sprintf("ssh signing configured (key: %s)", signingKey),
		}
	}

	return checkResult{
		name:     "commit-signing",
		passed:   false,
		required: true,
		detail:   fmt.Sprintf("commit signing enabled (ssh) but key %s not found in ssh-agent", signingKey),
		fix:      fmt.Sprintf("run: ssh-add %s", keyPath),
	}
}

// resolveSSHKeyPath expands ~ to the user's home directory and strips
// the .pub suffix so the path matches ssh-add -l output (which shows
// private key paths).
func resolveSSHKeyPath(env *doctorEnv, keyPath string) string {
	if strings.HasPrefix(keyPath, "~/") {
		if home, err := env.UserHomeDir(); err == nil {
			keyPath = filepath.Join(home, keyPath[2:])
		}
	}
	return strings.TrimSuffix(keyPath, ".pub")
}

// isInlinePublicKey reports whether signingKey looks like a bare inline
// SSH public key (e.g. "ssh-ed25519 AAAA... comment") as opposed to a
// file path, key:: prefixed key, or GPG key ID.
func isInlinePublicKey(signingKey string) bool {
	for _, prefix := range []string{"ssh-", "ecdsa-", "sk-"} {
		if strings.HasPrefix(signingKey, prefix) {
			return true
		}
	}
	return false
}

// sshBareKeyFoundInAgentOutput compares the type and blob fields of
// signingKey against each line of agentOutput (from ssh-add -L).
// The comment field (third+ field) is ignored so that keys match
// regardless of the label stored in the agent vs. git config.
func sshBareKeyFoundInAgentOutput(signingKey, agentOutput string) bool {
	keyFields := strings.Fields(signingKey)
	if len(keyFields) < 2 {
		return false
	}
	keyType := keyFields[0]
	keyBlob := keyFields[1]

	for _, line := range strings.Split(agentOutput, "\n") {
		lineFields := strings.Fields(line)
		if len(lineFields) < 2 {
			continue
		}
		if lineFields[0] == keyType && lineFields[1] == keyBlob {
			return true
		}
	}
	return false
}

// checkCommitSigningGPG verifies that the configured GPG signing key
// exists in the local secret keyring.
func checkCommitSigningGPG(env *doctorEnv, signingKey string) checkResult {
	if _, err := env.LookPath("gpg"); err != nil {
		return checkResult{
			name:    "commit-signing",
			passed:  true,
			skipped: true,
			detail:  "gpg not found in PATH, skipping commit-signing check",
		}
	}
	_, err := env.RunCmd("gpg", "--list-secret-keys", signingKey)
	if err != nil {
		return checkResult{
			name:     "commit-signing",
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("commit signing enabled (gpg) but key %s not found in keyring", signingKey),
			fix:      "ensure your GPG key is imported: gpg --list-secret-keys",
		}
	}
	return checkResult{
		name:   "commit-signing",
		passed: true,
		detail: fmt.Sprintf("gpg signing configured (key: %s)", signingKey),
	}
}

// checkAgentCLI verifies that a coding agent CLI is available in PATH
// and reports its version when available.
// This is always an optional (warning-only) check since the user may not
// be using that particular agent.
func checkAgentCLI(env *doctorEnv, agentName string) checkResult {
	info := runner.AgentByName(agentName)
	if info == nil {
		return checkResult{
			name:   agentName,
			passed: false,
			detail: "unknown agent",
		}
	}

	path, version, err := runner.DetectAgent(env.LookPath, env.RunCmd, agentName)
	if err != nil {
		hint := runner.InstallHint(info)
		return checkResult{
			name:     agentName,
			passed:   false,
			required: false,
			detail:   "not found in PATH (optional)",
			fix:      hint,
		}
	}
	detail := path
	if version != "" {
		detail = fmt.Sprintf("%s (%s)", path, version)
	}
	return checkResult{
		name:     agentName,
		passed:   true,
		required: false,
		detail:   detail,
	}
}

// checkPi verifies that the Pi coding agent CLI is available in PATH.
func checkPi(env *doctorEnv) checkResult {
	return checkAgentCLI(env, "pi")
}

// checkOpencode verifies that the Opencode coding agent CLI is available in PATH.
func checkOpencode(env *doctorEnv) checkResult {
	return checkAgentCLI(env, "opencode")
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
//  2. UserConfigDir()/soda/soda.yaml
//  3. UserHomeDir()/.config/soda/soda.yaml (fallback when UserConfigDir fails)
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
		path := filepath.Join(configDir, "soda", "soda.yaml")
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
			path := filepath.Join(home, ".config", "soda", "soda.yaml")
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
		detail:   "no config file found (checked soda.yaml and ~/.config/soda/soda.yaml)",
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

// arapucaWrapperCheck is the core check for the arapuca wrapper binary.
// It is nil-guarded on ArapucaWrapperPath and returns a required error
// with Landlock/seccomp impact when the wrapper is missing.
func arapucaWrapperCheck(env *doctorEnv) checkResult {
	if env.ArapucaWrapperPath == nil {
		return checkResult{
			name:     "arapuca-wrapper",
			passed:   false,
			required: true,
			detail:   "wrapper path function not available (cgo disabled?)",
			fix:      "rebuild with CGO_ENABLED=1 and install arapuca: sudo dnf install arapuca",
		}
	}
	path := env.ArapucaWrapperPath()
	if path == "" {
		return checkResult{
			name:     "arapuca-wrapper",
			passed:   false,
			required: true,
			detail:   "arapuca wrapper binary not found — Landlock/seccomp enforcement will not work",
			fix:      "install arapuca: sudo dnf install arapuca",
		}
	}
	return checkResult{
		name:     "arapuca-wrapper",
		passed:   true,
		required: true,
		detail:   path,
	}
}

// checkArapucaWrapper verifies the arapuca wrapper binary is present.
// Skipped when no config is parsed or sandbox is not enabled.
func checkArapucaWrapper(env *doctorEnv) checkResult {
	if env.ParsedConfig == nil || !env.ParsedConfig.Sandbox.Enabled {
		return checkResult{
			name:    "arapuca-wrapper",
			skipped: true,
			detail:  "skipped (sandbox not enabled)",
		}
	}
	return arapucaWrapperCheck(env)
}

// checkArapucaWrapperVersion warns when the installed arapuca wrapper binary
// is older than the go-arapuca library soda was linked against. Skipped when
// sandbox is disabled, the wrapper is absent, or versions cannot be determined.
func checkArapucaWrapperVersion(env *doctorEnv) checkResult {
	if env.ParsedConfig != nil && !env.ParsedConfig.Sandbox.Enabled {
		return checkResult{name: "arapuca-wrapper-version", skipped: true, detail: "skipped (sandbox not enabled)"}
	}
	if env.ArapucaWrapperPath == nil || env.ArapucaWrapperPath() == "" {
		return checkResult{name: "arapuca-wrapper-version", skipped: true, detail: "skipped (wrapper not found)"}
	}
	libVer := sandbox.ArapucaLibraryVersion
	out, _ := env.RunCmd("arapuca", "--version")
	wrapperVer := extractSemver(out)
	if wrapperVer == "" {
		return checkResult{name: "arapuca-wrapper-version", skipped: true, detail: "skipped (wrapper version unreadable)"}
	}
	if compareSemver(wrapperVer, libVer) < 0 {
		return checkResult{
			name:   "arapuca-wrapper-version",
			passed: false,
			detail: fmt.Sprintf("wrapper %s older than library %s — upgrade for compatibility", wrapperVer, libVer),
			fix:    "sudo dnf upgrade arapuca",
		}
	}
	return checkResult{name: "arapuca-wrapper-version", passed: true, detail: fmt.Sprintf("wrapper %s (library: %s)", wrapperVer, libVer)}
}
