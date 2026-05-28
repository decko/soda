package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/runner"
)

// mockFileInfo implements os.FileInfo for tests.
type mockFileInfo struct {
	name  string
	isDir bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return 0 }
func (m mockFileInfo) Mode() os.FileMode  { return 0755 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return m.isDir }
func (m mockFileInfo) Sys() any           { return nil }

// allPassEnv returns a doctorEnv where all checks pass.
func allPassEnv() *doctorEnv {
	return &doctorEnv{
		LookPath: func(file string) (string, error) {
			return "/usr/bin/" + file, nil
		},
		RunCmd: func(name string, args ...string) (string, error) {
			if name == "claude" && len(args) > 0 && args[0] == "--version" {
				return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
			}
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return ".git", nil
			}
			if name == "git" && len(args) > 1 && args[0] == "config" {
				switch args[1] {
				case "commit.gpgsign":
					return "true", nil
				case "gpg.format":
					return "ssh", nil
				case "user.signingkey":
					return "~/.ssh/id_ed25519.pub", nil
				}
			}
			if name == "ssh-add" && len(args) > 0 && args[0] == "-l" {
				return "256 SHA256:abcdef /home/testuser/.ssh/id_ed25519 (ED25519)", nil
			}
			return "", nil
		},
		Stat: func(name string) (os.FileInfo, error) {
			return mockFileInfo{name: name, isDir: strings.HasSuffix(name, "soda") && !strings.HasSuffix(name, ".yaml")}, nil
		},
		LoadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				GitHub: config.GitHubTicketConfig{
					Owner: "test-org",
					Repo:  "test-repo",
				},
			}, nil
		},
		UserConfigDir: func() (string, error) {
			return "/home/testuser/.config", nil
		},
		UserHomeDir: func() (string, error) {
			return "/home/testuser", nil
		},
		Getenv: func(key string) string {
			// Default: ANTHROPIC_API_KEY is set so claude-auth passes.
			if key == "ANTHROPIC_API_KEY" {
				return "sk-test-key"
			}
			return ""
		},
	}
}

// --- runDoctor tests ---

func TestRunDoctor_AllPass(t *testing.T) {
	var buf bytes.Buffer
	err := runDoctor(&buf, allPassEnv())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "All checks passed") {
		t.Errorf("expected 'All checks passed', got:\n%s", out)
	}
	// Every line before the summary should start with ✓ or - (skipped)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || line == "All checks passed" {
			continue
		}
		if !strings.HasPrefix(line, "✓") && !strings.HasPrefix(line, "-") {
			t.Errorf("expected ✓ or - prefix, got: %s", line)
		}
	}
}

func TestRunDoctor_OptionalOnlyFailures_NoError(t *testing.T) {
	env := allPassEnv()
	// Only gh and node are missing — both are optional.
	env.LookPath = func(file string) (string, error) {
		if file == "gh" || file == "node" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err != nil {
		t.Fatalf("expected no error when only optional checks fail, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "All checks passed") {
		t.Errorf("expected 'All checks passed', got:\n%s", out)
	}
	if !strings.Contains(out, "⚠ gh:") {
		t.Errorf("expected ⚠ marker for gh, got:\n%s", out)
	}
	if !strings.Contains(out, "⚠ node:") {
		t.Errorf("expected ⚠ marker for node, got:\n%s", out)
	}
}

func TestRunDoctor_SomeFailures(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "git" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err == nil {
		t.Fatal("expected error when checks fail")
	}
	out := buf.String()
	if !strings.Contains(out, "✗ git:") {
		t.Errorf("expected failed git check, got:\n%s", out)
	}
	if !strings.Contains(out, "check(s) failed") {
		t.Errorf("expected failure summary, got:\n%s", out)
	}
}

func TestRunDoctor_FixSuggestionPrinted(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	_ = runDoctor(&buf, env)
	out := buf.String()
	if !strings.Contains(out, "fix:") {
		t.Errorf("expected fix suggestion, got:\n%s", out)
	}
}

// --- checkGit tests ---

func TestCheckGit_Found(t *testing.T) {
	env := allPassEnv()
	r := checkGit(env)
	if !r.passed {
		t.Error("expected git check to pass")
	}
	if r.name != "git" {
		t.Errorf("expected name 'git', got %q", r.name)
	}
}

func TestCheckGit_NotFound(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	r := checkGit(env)
	if r.passed {
		t.Error("expected git check to fail")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion")
	}
}

// --- checkGitRepo tests ---

func TestCheckGitRepo_Inside(t *testing.T) {
	env := allPassEnv()
	r := checkGitRepo(env)
	if !r.passed {
		t.Error("expected git-repo check to pass")
	}
}

func TestCheckGitRepo_Outside(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" {
			return "", errors.New("not a git repo")
		}
		return "", nil
	}
	r := checkGitRepo(env)
	if r.passed {
		t.Error("expected git-repo check to fail")
	}
}

func TestCheckGitRepo_SkippedWhenGitMissing(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "git" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGitRepo(env)
	if !r.skipped {
		t.Error("expected git-repo check to be skipped when git is missing")
	}
	if !strings.Contains(r.detail, "skipped") {
		t.Errorf("expected 'skipped' in detail, got: %q", r.detail)
	}
}

// --- checkClaude tests ---

func TestCheckClaude_Found(t *testing.T) {
	env := allPassEnv()
	r := checkClaude(env)
	if !r.passed {
		t.Error("expected claude check to pass")
	}
}

func TestCheckClaude_NotFound(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkClaude(env)
	if r.passed {
		t.Error("expected claude check to fail")
	}
	if !strings.Contains(r.fix, "Claude Code") {
		t.Errorf("expected fix to mention Claude Code, got: %q", r.fix)
	}
}

// --- checkClaudeVersion tests ---

func TestCheckClaudeVersion_Success(t *testing.T) {
	env := allPassEnv()
	r := checkClaudeVersion(env)
	if !r.passed {
		t.Error("expected claude-version check to pass")
	}
	if !strings.Contains(r.detail, claude.MinCLIVersion) {
		t.Errorf("expected version in detail, got: %q", r.detail)
	}
}

func TestCheckClaudeVersion_Failure(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "claude" {
			return "", errors.New("exec error")
		}
		return "", nil
	}
	r := checkClaudeVersion(env)
	if r.passed {
		t.Error("expected claude-version check to fail")
	}
}

func TestCheckClaudeVersion_SkippedWhenClaudeMissing(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkClaudeVersion(env)
	if !r.skipped {
		t.Error("expected claude-version check to be skipped when claude is missing")
	}
	if !strings.Contains(r.detail, "skipped") {
		t.Errorf("expected 'skipped' in detail, got: %q", r.detail)
	}
}

func TestCheckClaudeVersion_BelowMinimum(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return "claude 2.0.5", nil
		}
		return ".git", nil
	}
	r := checkClaudeVersion(env)
	if r.passed {
		t.Error("expected claude-version check to fail for version below minimum")
	}
	if !strings.Contains(r.detail, "minimum required") {
		t.Errorf("expected 'minimum required' in detail, got: %q", r.detail)
	}
	if !strings.Contains(r.fix, "upgrade") {
		t.Errorf("expected upgrade suggestion in fix, got: %q", r.fix)
	}
}

func TestCheckClaudeVersion_AboveMax(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return "claude 99.0.0", nil
		}
		return ".git", nil
	}
	r := checkClaudeVersion(env)
	if !r.passed {
		t.Error("expected claude-version check to pass (warning only) for version above max")
	}
	if !r.required {
		t.Error("expected claude-version check to be required for version above max")
	}
	if !strings.Contains(r.detail, "⚠") {
		t.Errorf("expected ⚠ in detail for untested version, got: %q", r.detail)
	}
	if !strings.Contains(r.detail, claude.MaxTestedCLIVersion) {
		t.Errorf("expected detail to mention MaxTestedCLIVersion, got: %q", r.detail)
	}
	if !strings.Contains(r.detail, "@anthropic-ai/claude-code@"+claude.MaxTestedCLIVersion) {
		t.Errorf("expected pin command in detail, got: %q", r.detail)
	}
}

func TestCheckClaudeVersion_AtMax(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MaxTestedCLIVersion), nil
		}
		return ".git", nil
	}
	r := checkClaudeVersion(env)
	if !r.passed {
		t.Error("expected claude-version check to pass at MaxTestedCLIVersion")
	}
	if strings.Contains(r.detail, "⚠") {
		t.Errorf("expected no warning at MaxTestedCLIVersion, got: %q", r.detail)
	}
}

func TestCheckClaudeVersion_BelowMax(t *testing.T) {
	env := allPassEnv()
	// MinCLIVersion is below MaxTestedCLIVersion, so no warning expected.
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
		}
		return ".git", nil
	}
	r := checkClaudeVersion(env)
	if !r.passed {
		t.Error("expected claude-version check to pass at MinCLIVersion")
	}
	if strings.Contains(r.detail, "⚠") {
		t.Errorf("expected no warning at MinCLIVersion, got: %q", r.detail)
	}
}

func TestCheckClaudeVersion_UnparseableVersion(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return "unknown-version", nil
		}
		return ".git", nil
	}
	r := checkClaudeVersion(env)
	if r.passed {
		t.Error("expected claude-version check to fail for unparseable version")
	}
	if !strings.Contains(r.detail, "could not parse") {
		t.Errorf("expected 'could not parse' in detail, got: %q", r.detail)
	}
}

// --- checkGh tests ---

func TestCheckGh_Found(t *testing.T) {
	env := allPassEnv()
	r := checkGh(env)
	if !r.passed {
		t.Error("expected gh check to pass")
	}
}

func TestCheckGh_NotFound(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "gh" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGh(env)
	if r.passed {
		t.Error("expected gh check to fail")
	}
	if !strings.Contains(r.detail, "optional") {
		t.Errorf("expected detail to mention optional, got: %q", r.detail)
	}
	if r.required {
		t.Error("expected gh check to be optional (required=false)")
	}
}

// --- checkNode tests ---

func TestCheckNode_Found(t *testing.T) {
	env := allPassEnv()
	r := checkNode(env)
	if !r.passed {
		t.Error("expected node check to pass")
	}
}

func TestCheckNode_NotFound(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "node" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkNode(env)
	if r.passed {
		t.Error("expected node check to fail")
	}
	if !strings.Contains(r.detail, "optional") {
		t.Errorf("expected detail to mention optional, got: %q", r.detail)
	}
	if r.required {
		t.Error("expected node check to be optional (required=false)")
	}
}

// --- checkConfig tests ---

func TestCheckConfig_LocalExists(t *testing.T) {
	env := allPassEnv()
	r := checkConfig(env)
	if !r.passed {
		t.Error("expected config check to pass")
	}
	if !strings.Contains(r.detail, "local") {
		t.Errorf("expected detail to mention local, got: %q", r.detail)
	}
}

func TestCheckConfig_OnlyGlobal(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		if name == "soda.yaml" {
			return nil, os.ErrNotExist
		}
		return mockFileInfo{name: name, isDir: !strings.HasSuffix(name, ".yaml")}, nil
	}
	r := checkConfig(env)
	if !r.passed {
		t.Error("expected config check to pass with global config only")
	}
	if !strings.Contains(r.detail, "global") {
		t.Errorf("expected detail to mention global, got: %q", r.detail)
	}
}

func TestCheckConfig_NoneFound(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	r := checkConfig(env)
	if r.passed {
		t.Error("expected config check to fail when no config exists")
	}
	if !r.required {
		t.Error("expected config check to be required")
	}
}

func TestCheckConfig_NoConfigDir(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		if name == "soda.yaml" {
			return nil, os.ErrNotExist
		}
		return nil, os.ErrNotExist
	}
	env.UserConfigDir = func() (string, error) {
		return "", errors.New("no config dir")
	}
	env.UserHomeDir = func() (string, error) {
		return "", errors.New("no home dir")
	}
	r := checkConfig(env)
	if r.passed {
		t.Error("expected config check to fail when no local config and no config/home dir")
	}
}

func TestCheckConfig_UserHomeDirFallback(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		// Only the UserHomeDir-based path exists.
		if name == "/home/testuser/.config/soda/soda.yaml" {
			return mockFileInfo{name: name}, nil
		}
		return nil, os.ErrNotExist
	}
	env.UserConfigDir = func() (string, error) {
		return "", errors.New("no config dir")
	}
	r := checkConfig(env)
	if !r.passed {
		t.Error("expected config check to pass via UserHomeDir fallback")
	}
	if !strings.Contains(r.detail, "global") {
		t.Errorf("expected detail to mention global, got: %q", r.detail)
	}
	if !strings.Contains(r.detail, "/home/testuser/.config/soda/soda.yaml") {
		t.Errorf("expected detail to include fallback path, got: %q", r.detail)
	}
}

// --- checkGhAuth tests ---

func TestCheckGhAuth_Authenticated(t *testing.T) {
	env := allPassEnv()
	r := checkGhAuth(env)
	if !r.passed {
		t.Error("expected gh-auth check to pass")
	}
}

func TestCheckGhAuth_NotAuthenticated(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 0 && args[0] == "auth" {
			return "", errors.New("not logged in")
		}
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
		}
		if name == "git" {
			return ".git", nil
		}
		return "", nil
	}
	r := checkGhAuth(env)
	if r.passed {
		t.Error("expected gh-auth check to fail")
	}
	if !strings.Contains(r.fix, "gh auth login") {
		t.Errorf("expected fix to suggest gh auth login, got: %q", r.fix)
	}
}

func TestCheckGhAuth_SkippedWhenGhMissing(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "gh" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGhAuth(env)
	if !r.skipped {
		t.Error("expected gh-auth check to be skipped when gh is missing")
	}
}

// --- checkConfigValid tests ---

func TestCheckConfigValid_LocalValid(t *testing.T) {
	env := allPassEnv()
	r := checkConfigValid(env)
	if !r.passed {
		t.Error("expected config-valid check to pass")
	}
	if !strings.Contains(r.detail, "soda.yaml") {
		t.Errorf("expected detail to mention soda.yaml, got: %q", r.detail)
	}
}

func TestCheckConfigValid_LocalInvalid(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return nil, errors.New("invalid YAML")
	}
	r := checkConfigValid(env)
	if r.passed {
		t.Error("expected config-valid check to fail with invalid config")
	}
}

func TestCheckConfigValid_FallbackToGlobal(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		if name == "soda.yaml" {
			return nil, os.ErrNotExist
		}
		return mockFileInfo{name: name, isDir: !strings.HasSuffix(name, ".yaml")}, nil
	}
	r := checkConfigValid(env)
	if !r.passed {
		t.Error("expected config-valid check to pass with global config")
	}
	if !strings.Contains(r.detail, "soda.yaml") {
		t.Errorf("expected detail to mention soda.yaml, got: %q", r.detail)
	}
}

func TestCheckConfigValid_NoConfigFound(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	r := checkConfigValid(env)
	if !r.skipped {
		t.Error("expected config-valid check to be skipped when no config files exist")
	}
}

// --- isGitHubSource tests ---

func TestIsGitHubSource_NilConfig(t *testing.T) {
	env := &doctorEnv{}
	if env.isGitHubSource() {
		t.Error("expected false when ParsedConfig is nil")
	}
}

func TestIsGitHubSource_GitHub(t *testing.T) {
	env := &doctorEnv{ParsedConfig: &config.Config{TicketSource: "github"}}
	if !env.isGitHubSource() {
		t.Error("expected true when ticket_source is github")
	}
}

func TestIsGitHubSource_Jira(t *testing.T) {
	env := &doctorEnv{ParsedConfig: &config.Config{TicketSource: "jira"}}
	if env.isGitHubSource() {
		t.Error("expected false when ticket_source is jira")
	}
}

func TestIsGitHubSource_Empty(t *testing.T) {
	env := &doctorEnv{ParsedConfig: &config.Config{TicketSource: ""}}
	if env.isGitHubSource() {
		t.Error("expected false when ticket_source is empty")
	}
}

// --- checkGh context-aware tests ---

func TestCheckGh_RequiredWhenGitHubSource(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{TicketSource: "github"}
	env.LookPath = func(file string) (string, error) {
		if file == "gh" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGh(env)
	if r.passed {
		t.Error("expected gh check to fail")
	}
	if !r.required {
		t.Error("expected gh check to be required when ticket_source is github")
	}
	if !strings.Contains(r.detail, "required") {
		t.Errorf("expected detail to mention required, got: %q", r.detail)
	}
}

func TestCheckGh_OptionalWhenJiraSource(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{TicketSource: "jira"}
	env.LookPath = func(file string) (string, error) {
		if file == "gh" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGh(env)
	if r.required {
		t.Error("expected gh check to be optional when ticket_source is jira")
	}
	if !strings.Contains(r.detail, "optional") {
		t.Errorf("expected detail to mention optional, got: %q", r.detail)
	}
}

func TestCheckGh_OptionalWhenNoParsedConfig(t *testing.T) {
	env := allPassEnv()
	// ParsedConfig is nil by default.
	env.LookPath = func(file string) (string, error) {
		if file == "gh" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGh(env)
	if r.required {
		t.Error("expected gh check to be optional when ParsedConfig is nil")
	}
}

// --- checkGhAuth context-aware tests ---

func TestCheckGhAuth_RequiredWhenGitHubSource(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{TicketSource: "github"}
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 0 && args[0] == "auth" {
			return "", errors.New("not logged in")
		}
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
		}
		if name == "git" {
			return ".git", nil
		}
		return "", nil
	}
	r := checkGhAuth(env)
	if r.passed {
		t.Error("expected gh-auth check to fail")
	}
	if !r.required {
		t.Error("expected gh-auth check to be required when ticket_source is github")
	}
}

func TestCheckGhAuth_OptionalWhenJiraSource(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{TicketSource: "jira"}
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 0 && args[0] == "auth" {
			return "", errors.New("not logged in")
		}
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
		}
		if name == "git" {
			return ".git", nil
		}
		return "", nil
	}
	r := checkGhAuth(env)
	if r.required {
		t.Error("expected gh-auth check to be optional when ticket_source is jira")
	}
}

// --- checkConfigValid stores ParsedConfig ---

func TestCheckConfigValid_StoresParsedConfig(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{TicketSource: "github"}, nil
	}
	r := checkConfigValid(env)
	if !r.passed {
		t.Fatal("expected config-valid check to pass")
	}
	if env.ParsedConfig == nil {
		t.Fatal("expected ParsedConfig to be populated")
	}
	if env.ParsedConfig.TicketSource != "github" {
		t.Errorf("expected TicketSource=github, got %q", env.ParsedConfig.TicketSource)
	}
}

func TestCheckConfigValid_DoesNotStoreParsedConfigOnError(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return nil, errors.New("invalid YAML")
	}
	r := checkConfigValid(env)
	if r.passed {
		t.Fatal("expected config-valid check to fail")
	}
	if env.ParsedConfig != nil {
		t.Error("expected ParsedConfig to remain nil on parse error")
	}
}

// --- runDoctor integration: gh required with github source ---

func TestRunDoctor_GhRequiredWhenGitHubSource(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{TicketSource: "github"}, nil
	}
	env.LookPath = func(file string) (string, error) {
		if file == "gh" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err == nil {
		t.Fatal("expected error when gh is missing and ticket_source is github")
	}
	out := buf.String()
	if !strings.Contains(out, "✗ gh:") {
		t.Errorf("expected ✗ marker for gh, got:\n%s", out)
	}
}

func TestRunDoctor_GhOptionalWhenJiraSource(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{TicketSource: "jira"}, nil
	}
	env.LookPath = func(file string) (string, error) {
		if file == "gh" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err != nil {
		t.Fatalf("expected no error when gh is missing and ticket_source is jira, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "⚠ gh:") {
		t.Errorf("expected ⚠ marker for gh, got:\n%s", out)
	}
}

func TestCheckConfigValid_UserHomeDirFallback(t *testing.T) {
	env := allPassEnv()
	homePath := "/home/testuser/.config/soda/soda.yaml"
	env.Stat = func(name string) (os.FileInfo, error) {
		if name == homePath {
			return mockFileInfo{name: name}, nil
		}
		return nil, os.ErrNotExist
	}
	env.UserConfigDir = func() (string, error) {
		return "", errors.New("no config dir")
	}
	r := checkConfigValid(env)
	if !r.passed {
		t.Error("expected config-valid check to pass via UserHomeDir fallback")
	}
	if !strings.Contains(r.detail, homePath) {
		t.Errorf("expected detail to mention fallback path, got: %q", r.detail)
	}
}

// --- resolveConfigPath tests ---

func TestResolveConfigPath_Local(t *testing.T) {
	env := allPassEnv()
	loc := resolveConfigPath(env)
	if loc == nil {
		t.Fatal("expected non-nil location")
	}
	if loc.path != "soda.yaml" {
		t.Errorf("expected path 'soda.yaml', got %q", loc.path)
	}
	if loc.label != "local" {
		t.Errorf("expected label 'local', got %q", loc.label)
	}
}

func TestResolveConfigPath_GlobalViaUserConfigDir(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		if name == "soda.yaml" {
			return nil, os.ErrNotExist
		}
		return mockFileInfo{name: name}, nil
	}
	loc := resolveConfigPath(env)
	if loc == nil {
		t.Fatal("expected non-nil location")
	}
	if loc.label != "global" {
		t.Errorf("expected label 'global', got %q", loc.label)
	}
	if !strings.Contains(loc.path, ".config/soda/soda.yaml") {
		t.Errorf("expected global config path, got %q", loc.path)
	}
}

func TestResolveConfigPath_GlobalViaUserHomeDir(t *testing.T) {
	env := allPassEnv()
	homePath := "/home/testuser/.config/soda/soda.yaml"
	env.Stat = func(name string) (os.FileInfo, error) {
		if name == homePath {
			return mockFileInfo{name: name}, nil
		}
		return nil, os.ErrNotExist
	}
	env.UserConfigDir = func() (string, error) {
		return "", errors.New("no config dir")
	}
	loc := resolveConfigPath(env)
	if loc == nil {
		t.Fatal("expected non-nil location via UserHomeDir fallback")
	}
	if loc.path != homePath {
		t.Errorf("expected path %q, got %q", homePath, loc.path)
	}
	if loc.label != "global" {
		t.Errorf("expected label 'global', got %q", loc.label)
	}
}

func TestResolveConfigPath_NoneFound(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	env.UserConfigDir = func() (string, error) {
		return "", errors.New("no config dir")
	}
	env.UserHomeDir = func() (string, error) {
		return "", errors.New("no home dir")
	}
	loc := resolveConfigPath(env)
	if loc != nil {
		t.Errorf("expected nil location, got %+v", loc)
	}
}

func TestResolveConfigPath_UserHomeDirNotCalled_WhenUserConfigDirSucceeds(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		if name == "soda.yaml" {
			return nil, os.ErrNotExist
		}
		return mockFileInfo{name: name}, nil
	}
	homeDirCalled := false
	env.UserHomeDir = func() (string, error) {
		homeDirCalled = true
		return "/home/testuser", nil
	}
	loc := resolveConfigPath(env)
	if loc == nil {
		t.Fatal("expected non-nil location")
	}
	if homeDirCalled {
		t.Error("UserHomeDir should not be called when UserConfigDir succeeds")
	}
}

// TestResolveConfigPath_NoFalsePositive_WhenUserConfigDirSucceeds verifies
// that on platforms where UserConfigDir differs from ~/.config (e.g. macOS
// returns ~/Library/Application Support), doctor does NOT fall through to
// the UserHomeDir ~/.config path. This would be a false positive: doctor
// says "config OK" but runtime config.DefaultPath() would look only at
// the UserConfigDir path and fail to find it.
func TestResolveConfigPath_NoFalsePositive_WhenUserConfigDirSucceeds(t *testing.T) {
	env := allPassEnv()
	// Simulate macOS: UserConfigDir returns ~/Library/Application Support
	env.UserConfigDir = func() (string, error) {
		return "/Users/testuser/Library/Application Support", nil
	}
	env.UserHomeDir = func() (string, error) {
		return "/Users/testuser", nil
	}
	env.Stat = func(name string) (os.FileInfo, error) {
		// Config exists at ~/.config/soda/soda.yaml but NOT at
		// ~/Library/Application Support/soda/soda.yaml
		if name == "/Users/testuser/.config/soda/soda.yaml" {
			return mockFileInfo{name: name}, nil
		}
		return nil, os.ErrNotExist
	}
	loc := resolveConfigPath(env)
	// Must return nil — the file at ~/.config is not where runtime would look.
	if loc != nil {
		t.Errorf("expected nil (no false positive), got %+v", loc)
	}
}

// --- extractSemver tests ---

func TestExtractSemver(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude 2.1.81", "2.1.81"},
		{"claude 10.20.300", "10.20.300"},
		{"2.1.81", "2.1.81"},
		{"2.1.111 (Claude Code)", "2.1.111"},
		{"no version here", ""},
		{"v2.1.81", ""},        // prefixed with 'v' — not pure digits
		{"claude abc.1.2", ""}, // non-numeric
		{"", ""},
	}
	for _, tt := range tests {
		got := extractSemver(tt.input)
		if got != tt.want {
			t.Errorf("extractSemver(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- compareSemver tests ---

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.1.81", "2.1.81", 0},
		{"2.1.80", "2.1.81", -1},
		{"2.1.82", "2.1.81", 1},
		{"2.0.100", "2.1.0", -1},
		{"3.0.0", "2.99.99", 1},
		{"1.0.0", "2.1.81", -1},
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// --- runDoctor skipped output tests ---

func TestRunDoctor_GitMissing_SkipsGitRepo(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "git" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	_ = runDoctor(&buf, env)
	out := buf.String()
	// git-repo should be skipped, not failed
	if !strings.Contains(out, "- git-repo: skipped") {
		t.Errorf("expected git-repo to be skipped, got:\n%s", out)
	}
	// git itself should fail
	if !strings.Contains(out, "✗ git:") {
		t.Errorf("expected git check to fail, got:\n%s", out)
	}
}

func TestRunDoctor_ClaudeMissing_SkipsClaudeVersion(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	_ = runDoctor(&buf, env)
	out := buf.String()
	// claude-version should be skipped, not failed
	if !strings.Contains(out, "- claude-version: skipped") {
		t.Errorf("expected claude-version to be skipped, got:\n%s", out)
	}
	// claude itself should fail
	if !strings.Contains(out, "✗ claude:") {
		t.Errorf("expected claude check to fail, got:\n%s", out)
	}
}

// --- checkBranchProtection tests ---

// --- checkClaudeAuth tests ---

func TestCheckClaudeAuth_ProxyEnabled(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{
		Sandbox: config.SandboxConfig{
			Proxy: config.SandboxProxyConfig{Enabled: true},
		},
	}
	env.Getenv = func(key string) string { return "" }
	r := checkClaudeAuth(env)
	if !r.passed {
		t.Error("expected claude-auth to pass when proxy is enabled")
	}
	if !strings.Contains(r.detail, "proxy") {
		t.Errorf("expected detail to mention proxy, got: %q", r.detail)
	}
}

func TestCheckClaudeAuth_APIKeySet(t *testing.T) {
	env := allPassEnv()
	env.Getenv = func(key string) string {
		if key == "ANTHROPIC_API_KEY" {
			return "sk-ant-test"
		}
		return ""
	}
	r := checkClaudeAuth(env)
	if !r.passed {
		t.Error("expected claude-auth to pass when ANTHROPIC_API_KEY is set")
	}
	if !strings.Contains(r.detail, "ANTHROPIC_API_KEY") {
		t.Errorf("expected detail to mention ANTHROPIC_API_KEY, got: %q", r.detail)
	}
}

func TestCheckClaudeAuth_VertexSet(t *testing.T) {
	env := allPassEnv()
	env.Getenv = func(key string) string {
		if key == "CLAUDE_CODE_USE_VERTEX" {
			return "1"
		}
		return ""
	}
	r := checkClaudeAuth(env)
	if !r.passed {
		t.Error("expected claude-auth to pass when CLAUDE_CODE_USE_VERTEX is set")
	}
	if !strings.Contains(r.detail, "Vertex") {
		t.Errorf("expected detail to mention Vertex, got: %q", r.detail)
	}
}

func TestCheckClaudeAuth_ApiKeyHelper(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{
		Auth: config.AuthConfig{ApiKeyHelper: "/usr/local/bin/get-key"},
	}
	env.Getenv = func(key string) string { return "" }
	r := checkClaudeAuth(env)
	if !r.passed {
		t.Error("expected claude-auth to pass when api_key_helper is configured")
	}
	if !strings.Contains(r.detail, "api_key_helper") {
		t.Errorf("expected detail to mention api_key_helper, got: %q", r.detail)
	}
}

func TestCheckClaudeAuth_NoAuth(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{}
	env.Getenv = func(key string) string { return "" }
	r := checkClaudeAuth(env)
	if r.passed {
		t.Error("expected claude-auth to fail when no auth method is configured")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion when auth fails")
	}
}

func TestCheckClaudeAuth_NilConfig(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = nil
	env.Getenv = func(key string) string { return "" }
	r := checkClaudeAuth(env)
	if r.passed {
		t.Error("expected claude-auth to fail when ParsedConfig is nil and no env vars set")
	}
}

func TestCheckClaudeAuth_PrecedenceProxyOverAPIKey(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{
		Sandbox: config.SandboxConfig{
			Proxy: config.SandboxProxyConfig{Enabled: true},
		},
	}
	env.Getenv = func(key string) string {
		if key == "ANTHROPIC_API_KEY" {
			return "sk-ant-test"
		}
		return ""
	}
	r := checkClaudeAuth(env)
	if !r.passed {
		t.Error("expected claude-auth to pass")
	}
	// Should report proxy, not API key — proxy takes precedence.
	if !strings.Contains(r.detail, "proxy") {
		t.Errorf("expected proxy to take precedence, got: %q", r.detail)
	}
}

func TestCheckClaudeAuth_PrecedenceAPIKeyOverHelper(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{
		Auth: config.AuthConfig{ApiKeyHelper: "/bin/helper"},
	}
	env.Getenv = func(key string) string {
		if key == "ANTHROPIC_API_KEY" {
			return "sk-ant-test"
		}
		return ""
	}
	r := checkClaudeAuth(env)
	if !r.passed {
		t.Error("expected claude-auth to pass")
	}
	// Should report API key, not helper — API key takes precedence.
	if !strings.Contains(r.detail, "ANTHROPIC_API_KEY") {
		t.Errorf("expected ANTHROPIC_API_KEY to take precedence, got: %q", r.detail)
	}
}

func TestCheckBranchProtection_SkippedWhenGhMissing(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "gh" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkBranchProtection(env)
	if !r.skipped {
		t.Error("expected branch-protection to be skipped when gh is missing")
	}
}

func TestCheckBranchProtection_SkippedWhenNoConfig(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = nil
	r := checkBranchProtection(env)
	if !r.skipped {
		t.Error("expected branch-protection to be skipped when config is nil")
	}
}

func TestCheckBranchProtection_SkippedWhenNoOwnerRepo(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{}
	r := checkBranchProtection(env)
	if !r.skipped {
		t.Error("expected branch-protection to be skipped when owner/repo is empty")
	}
}

func TestCheckBranchProtection_NoBranchProtection(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{
		GitHub: config.GitHubTicketConfig{Owner: "org", Repo: "repo"},
	}
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 1 && args[0] == "api" {
			return "Not Found", errors.New("exit status 1")
		}
		return "", nil
	}
	r := checkBranchProtection(env)
	if !r.passed {
		t.Errorf("expected branch-protection to pass when no protection rules, got: %+v", r)
	}
}

func TestCheckBranchProtection_DismissStaleReviewsEnabled(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{
		GitHub: config.GitHubTicketConfig{Owner: "org", Repo: "repo"},
	}
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 1 && args[0] == "api" {
			return `{"required_pull_request_reviews":{"dismiss_stale_reviews":true}}`, nil
		}
		return "", nil
	}
	r := checkBranchProtection(env)
	if r.passed {
		t.Error("expected branch-protection to warn when dismiss_stale_reviews is enabled")
	}
	if !strings.Contains(r.detail, "dismiss_stale_reviews") {
		t.Errorf("expected detail to mention dismiss_stale_reviews, got: %q", r.detail)
	}
	if r.fix == "" {
		t.Error("expected a fix suggestion")
	}
}

func TestCheckBranchProtection_NoStaleReviews(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{
		GitHub: config.GitHubTicketConfig{Owner: "org", Repo: "repo"},
	}
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 1 && args[0] == "api" {
			return `{"required_pull_request_reviews":{"dismiss_stale_reviews":false}}`, nil
		}
		return "", nil
	}
	r := checkBranchProtection(env)
	if !r.passed {
		t.Errorf("expected branch-protection to pass when dismiss_stale_reviews is false, got: %+v", r)
	}
}

// --- checkCommitSigning tests ---

func TestCheckCommitSigning_SkippedWhenGitMissing(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "git" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkCommitSigning(env)
	if !r.skipped {
		t.Error("expected commit-signing to be skipped when git is missing")
	}
	if !strings.Contains(r.detail, "skipped") {
		t.Errorf("expected 'skipped' in detail, got: %q", r.detail)
	}
}

func TestCheckCommitSigning_SkippedWhenNotInRepo(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "", errors.New("not a git repo")
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if !r.skipped {
		t.Error("expected commit-signing to be skipped when not in a git repo")
	}
	if !strings.Contains(r.detail, "skipped") {
		t.Errorf("expected 'skipped' in detail, got: %q", r.detail)
	}
}

func TestCheckCommitSigning_WarnWhenNotConfigured(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			if args[1] == "commit.gpgsign" {
				return "", errors.New("not set")
			}
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if r.passed {
		t.Error("expected commit-signing to not pass when gpgsign is not configured")
	}
	if r.required {
		t.Error("expected commit-signing to be optional (warn) when not configured")
	}
	if r.skipped {
		t.Error("expected commit-signing not to be skipped")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion")
	}
}

func TestCheckCommitSigning_GPGKeyFound(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "gpg", nil
			case "user.signingkey":
				return "ABCDEF1234567890", nil
			}
		}
		if name == "gpg" && len(args) > 0 && args[0] == "--list-secret-keys" {
			return "pub   ed25519 ABCDEF1234567890", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if !r.passed {
		t.Errorf("expected commit-signing to pass with GPG key, got: %+v", r)
	}
	if !strings.Contains(r.detail, "gpg") {
		t.Errorf("expected detail to mention gpg, got: %q", r.detail)
	}
}

func TestCheckCommitSigning_GPGKeyNotFound(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "gpg", nil
			case "user.signingkey":
				return "ABCDEF1234567890", nil
			}
		}
		if name == "gpg" && len(args) > 0 && args[0] == "--list-secret-keys" {
			return "", errors.New("no such key")
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if r.passed {
		t.Error("expected commit-signing to fail when GPG key is not found")
	}
	if !r.required {
		t.Error("expected commit-signing to be required (fail) when key is unreachable")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion")
	}
}

func TestCheckCommitSigning_GPGNoSigningKey(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "gpg", nil
			case "user.signingkey":
				return "", errors.New("not set")
			}
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if r.passed {
		t.Error("expected commit-signing to fail when no signing key is configured")
	}
	if !r.required {
		t.Error("expected commit-signing to be required (fail) when key is missing")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion")
	}
}

func TestCheckCommitSigning_SSHKeyFound(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "~/.ssh/id_ed25519.pub", nil
			}
		}
		if name == "ssh-add" && len(args) > 0 && args[0] == "-l" {
			return "256 SHA256:abcdef /home/testuser/.ssh/id_ed25519 (ED25519)", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if !r.passed {
		t.Errorf("expected commit-signing to pass with SSH key, got: %+v", r)
	}
	if !strings.Contains(r.detail, "ssh") {
		t.Errorf("expected detail to mention ssh, got: %q", r.detail)
	}
}

func TestCheckCommitSigning_SSHKeyNotFound(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "~/.ssh/id_ed25519.pub", nil
			}
		}
		if name == "ssh-add" && len(args) > 0 && args[0] == "-l" {
			return "256 SHA256:xyz /home/other/.ssh/id_rsa (RSA)", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if r.passed {
		t.Error("expected commit-signing to fail when SSH key is not in agent")
	}
	if !r.required {
		t.Error("expected commit-signing to be required (fail) when key is unreachable")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion")
	}
}

func TestCheckCommitSigning_SSHNoSigningKey(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "", errors.New("not set")
			}
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if r.passed {
		t.Error("expected commit-signing to fail when no signing key is configured")
	}
	if !r.required {
		t.Error("expected commit-signing to be required (fail) when key is missing")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion")
	}
}

func TestCheckCommitSigning_SSHInlineKeyLoaded(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "key::ssh-ed25519 AAAA...", nil
			}
		}
		if name == "ssh-add" && len(args) > 0 && args[0] == "-l" {
			return "256 SHA256:abcdef user@host (ED25519)", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if !r.passed {
		t.Errorf("expected commit-signing to pass with inline SSH key when agent has keys, got: %+v", r)
	}
	if !strings.Contains(r.detail, "inline") {
		t.Errorf("expected detail to mention inline, got: %q", r.detail)
	}
}

func TestCheckCommitSigning_SSHInlineKeyNotLoaded(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "key::ssh-ed25519 AAAA...", nil
			}
		}
		if name == "ssh-add" && len(args) > 0 && args[0] == "-l" {
			return "The agent has no identities.", errors.New("exit status 1")
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if r.passed {
		t.Error("expected commit-signing to fail when inline SSH key and no agent keys")
	}
	if !r.required {
		t.Error("expected commit-signing to be required (fail) when key is unreachable")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion")
	}
}

func TestCheckCommitSigning_DefaultFormatIsGPG(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "", errors.New("not set") // defaults to gpg
			case "user.signingkey":
				return "ABCDEF1234567890", nil
			}
		}
		if name == "gpg" && len(args) > 0 && args[0] == "--list-secret-keys" {
			return "pub   ed25519 ABCDEF1234567890", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if !r.passed {
		t.Errorf("expected commit-signing to pass with default GPG format, got: %+v", r)
	}
	if !strings.Contains(r.detail, "gpg") {
		t.Errorf("expected detail to mention gpg, got: %q", r.detail)
	}
}

func TestCheckCommitSigning_SkippedWhenGpgMissing(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "gpg", nil
			case "user.signingkey":
				return "ABCDEF1234567890", nil
			}
		}
		return "", nil
	}
	env.LookPath = func(file string) (string, error) {
		if file == "gpg" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkCommitSigning(env)
	if !r.skipped {
		t.Errorf("expected commit-signing to be skipped when gpg is missing, got: %+v", r)
	}
	if r.required {
		t.Error("expected skipped check to not be required (fail)")
	}
}

func TestCheckCommitSigning_SkippedWhenSSHAddMissing(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "~/.ssh/id_ed25519.pub", nil
			}
		}
		return "", nil
	}
	env.LookPath = func(file string) (string, error) {
		if file == "ssh-add" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkCommitSigning(env)
	if !r.skipped {
		t.Errorf("expected commit-signing to be skipped when ssh-add is missing, got: %+v", r)
	}
	if r.required {
		t.Error("expected skipped check to not be required (fail)")
	}
}

// --- isGitLabSource tests ---

func TestIsGitLabSource_NilConfig(t *testing.T) {
	env := &doctorEnv{}
	if env.isGitLabSource() {
		t.Error("expected false when ParsedConfig is nil")
	}
}

func TestIsGitLabSource_GitLab(t *testing.T) {
	env := &doctorEnv{ParsedConfig: &config.Config{TicketSource: "gitlab"}}
	if !env.isGitLabSource() {
		t.Error("expected true when ticket_source is gitlab")
	}
}

func TestIsGitLabSource_GitHub(t *testing.T) {
	env := &doctorEnv{ParsedConfig: &config.Config{TicketSource: "github"}}
	if env.isGitLabSource() {
		t.Error("expected false when ticket_source is github")
	}
}

func TestIsGitLabSource_Empty(t *testing.T) {
	env := &doctorEnv{ParsedConfig: &config.Config{TicketSource: ""}}
	if env.isGitLabSource() {
		t.Error("expected false when ticket_source is empty")
	}
}

// --- checkGlab tests ---

func TestCheckGlab_Found(t *testing.T) {
	env := allPassEnv()
	r := checkGlab(env)
	if !r.passed {
		t.Error("expected glab check to pass")
	}
}

func TestCheckGlab_NotFound(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "glab" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGlab(env)
	if r.passed {
		t.Error("expected glab check to fail")
	}
	if !strings.Contains(r.detail, "optional") {
		t.Errorf("expected detail to mention optional, got: %q", r.detail)
	}
	if r.required {
		t.Error("expected glab check to be optional (required=false)")
	}
}

func TestCheckGlab_RequiredWhenGitLabSource(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{TicketSource: "gitlab"}
	env.LookPath = func(file string) (string, error) {
		if file == "glab" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGlab(env)
	if r.passed {
		t.Error("expected glab check to fail")
	}
	if !r.required {
		t.Error("expected glab check to be required when ticket_source is gitlab")
	}
	if !strings.Contains(r.detail, "required") {
		t.Errorf("expected detail to mention required, got: %q", r.detail)
	}
}

func TestCheckGlab_OptionalWhenGitHubSource(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{TicketSource: "github"}
	env.LookPath = func(file string) (string, error) {
		if file == "glab" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGlab(env)
	if r.required {
		t.Error("expected glab check to be optional when ticket_source is github")
	}
	if !strings.Contains(r.detail, "optional") {
		t.Errorf("expected detail to mention optional, got: %q", r.detail)
	}
}

// --- checkGlabAuth tests ---

func TestCheckGlabAuth_Authenticated(t *testing.T) {
	env := allPassEnv()
	r := checkGlabAuth(env)
	if !r.passed {
		t.Error("expected glab-auth check to pass")
	}
}

func TestCheckGlabAuth_NotAuthenticated(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "glab" && len(args) > 0 && args[0] == "auth" {
			return "", errors.New("not logged in")
		}
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
		}
		if name == "git" {
			return ".git", nil
		}
		return "", nil
	}
	r := checkGlabAuth(env)
	if r.passed {
		t.Error("expected glab-auth check to fail")
	}
	if !strings.Contains(r.fix, "glab auth login") {
		t.Errorf("expected fix to suggest glab auth login, got: %q", r.fix)
	}
}

func TestCheckGlabAuth_SkippedWhenGlabMissing(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "glab" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkGlabAuth(env)
	if !r.skipped {
		t.Error("expected glab-auth check to be skipped when glab is missing")
	}
}

func TestCheckGlabAuth_RequiredWhenGitLabSource(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{TicketSource: "gitlab"}
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "glab" && len(args) > 0 && args[0] == "auth" {
			return "", errors.New("not logged in")
		}
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
		}
		if name == "git" {
			return ".git", nil
		}
		return "", nil
	}
	r := checkGlabAuth(env)
	if r.passed {
		t.Error("expected glab-auth check to fail")
	}
	if !r.required {
		t.Error("expected glab-auth check to be required when ticket_source is gitlab")
	}
}

func TestCheckGlabAuth_OptionalWhenGitHubSource(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{TicketSource: "github"}
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "glab" && len(args) > 0 && args[0] == "auth" {
			return "", errors.New("not logged in")
		}
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
		}
		if name == "git" {
			return ".git", nil
		}
		return "", nil
	}
	r := checkGlabAuth(env)
	if r.required {
		t.Error("expected glab-auth check to be optional when ticket_source is github")
	}
}

// --- runDoctor integration: glab required with gitlab source ---

func TestRunDoctor_GlabRequiredWhenGitLabSource(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{TicketSource: "gitlab"}, nil
	}
	env.LookPath = func(file string) (string, error) {
		if file == "glab" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err == nil {
		t.Fatal("expected error when glab is missing and ticket_source is gitlab")
	}
	out := buf.String()
	if !strings.Contains(out, "✗ glab:") {
		t.Errorf("expected ✗ marker for glab, got:\n%s", out)
	}
}

func TestRunDoctor_GlabOptionalWhenGitHubSource(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{TicketSource: "github"}, nil
	}
	env.LookPath = func(file string) (string, error) {
		if file == "glab" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err != nil {
		t.Fatalf("expected no error when glab is missing and ticket_source is github, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "⚠ glab:") {
		t.Errorf("expected ⚠ marker for glab, got:\n%s", out)
	}
}

// --- isInlinePublicKey tests ---

func TestIsInlinePublicKey(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Positive cases: bare inline public keys.
		{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... user@host", true},
		{"ssh-rsa AAAAB3NzaC1yc2EAAA... user@host", true},
		{"ssh-dss AAAAB3NzaC1kc3MAAA... user@host", true},
		{"ecdsa-sha2-nistp256 AAAAE2VjZHNh... user@host", true},
		{"sk-ssh-ed25519@openssh.com AAAAGnNr... user@host", true},
		{"sk-ecdsa-sha2-nistp256@openssh.com AAAA... user@host", true},

		// Negative cases.
		{"~/.ssh/id_ed25519.pub", false},
		{"/home/user/.ssh/id_ed25519", false},
		{"key::ssh-ed25519 AAAA...", false},
		{"ABCDEF1234567890", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isInlinePublicKey(tt.input)
		if got != tt.want {
			t.Errorf("isInlinePublicKey(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// --- sshBareKeyFoundInAgentOutput tests ---

func TestSSHBareKeyFoundInAgentOutput(t *testing.T) {
	tests := []struct {
		name        string
		signingKey  string
		agentOutput string
		want        bool
	}{
		{
			name:        "exact match with same comment",
			signingKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI user@host",
			agentOutput: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI user@host",
			want:        true,
		},
		{
			name:        "match with different comment",
			signingKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI My Key",
			agentOutput: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI agent-loaded-comment",
			want:        true,
		},
		{
			name:        "match among multiple agent lines",
			signingKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI user@host",
			agentOutput: "ssh-rsa AAAAB3Nza... other@host\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI loaded@host",
			want:        true,
		},
		{
			name:        "different blob",
			signingKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI user@host",
			agentOutput: "ssh-ed25519 BBBBC3NzaC1lZDI1NTE5BBBBB other@host",
			want:        false,
		},
		{
			name:        "different type",
			signingKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI user@host",
			agentOutput: "ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAI user@host",
			want:        false,
		},
		{
			name:        "empty agent output",
			signingKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI user@host",
			agentOutput: "",
			want:        false,
		},
		{
			name:        "signing key missing blob",
			signingKey:  "ssh-ed25519",
			agentOutput: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI user@host",
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sshBareKeyFoundInAgentOutput(tt.signingKey, tt.agentOutput)
			if got != tt.want {
				t.Errorf("sshBareKeyFoundInAgentOutput(%q, %q) = %v, want %v",
					tt.signingKey, tt.agentOutput, got, tt.want)
			}
		})
	}
}

// --- checkCommitSigning bare inline key tests ---

func TestCheckCommitSigning_SSHBareInlineKeyLoaded(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI My Key", nil
			}
		}
		if name == "ssh-add" && len(args) > 0 && args[0] == "-L" {
			return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI agent-comment", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if !r.passed {
		t.Errorf("expected commit-signing to pass with bare inline SSH key, got: %+v", r)
	}
	if !strings.Contains(r.detail, "inline") {
		t.Errorf("expected detail to mention inline, got: %q", r.detail)
	}
}

func TestCheckCommitSigning_SSHBareInlineKeyNotLoaded(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI My Key", nil
			}
		}
		if name == "ssh-add" && len(args) > 0 && args[0] == "-L" {
			return "ssh-rsa BBBBB3NzaC1yc2EAAA other@host", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if r.passed {
		t.Error("expected commit-signing to fail when bare inline SSH key is not loaded in agent")
	}
	if !r.required {
		t.Error("expected commit-signing to be required (fail) when key is unreachable")
	}
	if r.fix == "" {
		t.Error("expected fix suggestion")
	}
}

func TestCheckCommitSigning_SSHBareInlineKeyECDSA(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "ecdsa-sha2-nistp256 AAAAE2VjZHNh user@host", nil
			}
		}
		if name == "ssh-add" && len(args) > 0 && args[0] == "-L" {
			return "ecdsa-sha2-nistp256 AAAAE2VjZHNh loaded@host", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if !r.passed {
		t.Errorf("expected commit-signing to pass with bare inline ECDSA key, got: %+v", r)
	}
	if !strings.Contains(r.detail, "inline") {
		t.Errorf("expected detail to mention inline, got: %q", r.detail)
	}
}

func TestCheckCommitSigning_SSHBareInlineKeySK(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "config" {
			switch args[1] {
			case "commit.gpgsign":
				return "true", nil
			case "gpg.format":
				return "ssh", nil
			case "user.signingkey":
				return "sk-ssh-ed25519@openssh.com AAAAGnNr user@host", nil
			}
		}
		if name == "ssh-add" && len(args) > 0 && args[0] == "-L" {
			return "sk-ssh-ed25519@openssh.com AAAAGnNr loaded@host", nil
		}
		return "", nil
	}
	r := checkCommitSigning(env)
	if !r.passed {
		t.Errorf("expected commit-signing to pass with bare inline SK key, got: %+v", r)
	}
	if !strings.Contains(r.detail, "inline") {
		t.Errorf("expected detail to mention inline, got: %q", r.detail)
	}
}

// --- checkPi tests ---

func TestCheckPi_Found(t *testing.T) {
	env := allPassEnv()
	r := checkPi(env)
	if !r.passed {
		t.Error("expected pi check to pass")
	}
	if r.name != "pi" {
		t.Errorf("expected name 'pi', got %q", r.name)
	}
}

func TestCheckPi_NotFound(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "pi" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkPi(env)
	if r.passed {
		t.Error("expected pi check to fail")
	}
	if r.required {
		t.Error("expected pi check to be optional (required=false)")
	}
	if !strings.Contains(r.detail, "optional") {
		t.Errorf("expected detail to mention optional, got: %q", r.detail)
	}
	piInfo := runner.AgentByName("pi")
	expectedFix := runner.InstallHint(piInfo)
	if r.fix != expectedFix {
		t.Errorf("expected fix %q, got %q", expectedFix, r.fix)
	}
}

// --- checkOpencode tests ---

func TestCheckOpencode_Found(t *testing.T) {
	env := allPassEnv()
	r := checkOpencode(env)
	if !r.passed {
		t.Error("expected opencode check to pass")
	}
	if r.name != "opencode" {
		t.Errorf("expected name 'opencode', got %q", r.name)
	}
}

func TestCheckOpencode_NotFound(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "opencode" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	r := checkOpencode(env)
	if r.passed {
		t.Error("expected opencode check to fail")
	}
	if r.required {
		t.Error("expected opencode check to be optional (required=false)")
	}
	if !strings.Contains(r.detail, "optional") {
		t.Errorf("expected detail to mention optional, got: %q", r.detail)
	}
	ocInfo := runner.AgentByName("opencode")
	expectedFix := runner.InstallHint(ocInfo)
	if r.fix != expectedFix {
		t.Errorf("expected fix %q, got %q", expectedFix, r.fix)
	}
}

// --- runDoctor shows pi/opencode lines ---

func TestRunDoctor_ShowsPiAndOpencodeLines(t *testing.T) {
	env := allPassEnv()
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "pi:") {
		t.Errorf("expected pi line in doctor output, got:\n%s", out)
	}
	if !strings.Contains(out, "opencode:") {
		t.Errorf("expected opencode line in doctor output, got:\n%s", out)
	}
}

// TestCheckAgentCLI_IncludesVersionInDetail verifies that when the agent binary
// is found and returns a version string, checkAgentCLI includes the version
// in parentheses in the detail field (e.g. '/usr/bin/pi (1.2.3)').
func TestCheckAgentCLI_IncludesVersionInDetail(t *testing.T) {
	env := &doctorEnv{
		LookPath: func(file string) (string, error) {
			if file == "pi" {
				return "/usr/bin/pi", nil
			}
			return "", errors.New("not found")
		},
		RunCmd: func(name string, args ...string) (string, error) {
			if name == "pi" && len(args) > 0 && args[0] == "--version" {
				return "pi 1.2.3", nil
			}
			return "", nil
		},
	}
	r := checkPi(env)
	if !r.passed {
		t.Fatalf("expected pi check to pass, got detail: %q", r.detail)
	}
	if !strings.Contains(r.detail, "(1.2.3)") {
		t.Errorf("expected version in detail, got: %q", r.detail)
	}
	if !strings.Contains(r.detail, "/usr/bin/pi") {
		t.Errorf("expected path in detail, got: %q", r.detail)
	}
}

// --- runDoctorFull --install tests ---

func TestRunDoctorInstall_PromptsOnMissingAgent(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "pi" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	installCalled := false
	env.ExecInstallCmd = func(cmd string) error {
		installCalled = true
		return nil
	}

	// User answers "y" to install prompt.
	var buf bytes.Buffer
	err := runDoctorFull(&buf, strings.NewReader("y\n"), env, true)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !installCalled {
		t.Error("expected install command to be called when user answers 'y'")
	}
	out := buf.String()
	if !strings.Contains(out, "Auto-install pi") {
		t.Errorf("expected install prompt for pi, got:\n%s", out)
	}
}

func TestRunDoctorInstall_NoInstallOnDecline(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "opencode" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	installCalled := false
	env.ExecInstallCmd = func(cmd string) error {
		installCalled = true
		return nil
	}

	// User answers "n".
	var buf bytes.Buffer
	err := runDoctorFull(&buf, strings.NewReader("n\n"), env, true)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if installCalled {
		t.Error("expected install command NOT to be called when user answers 'n'")
	}
}

func TestRunDoctorInstall_NoPromptWithoutFlag(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "pi" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	installCalled := false
	env.ExecInstallCmd = func(cmd string) error {
		installCalled = true
		return nil
	}

	// install=false — should not prompt at all.
	var buf bytes.Buffer
	err := runDoctorFull(&buf, strings.NewReader("y\n"), env, false)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if installCalled {
		t.Error("expected install command NOT to be called when --install is not set")
	}
	out := buf.String()
	if strings.Contains(out, "Auto-install") {
		t.Errorf("expected no install prompt without --install flag, got:\n%s", out)
	}
}

func TestRunDoctorInstall_EmptyInputDeclines(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "pi" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	installCalled := false
	env.ExecInstallCmd = func(cmd string) error {
		installCalled = true
		return nil
	}

	// Empty input (just newline) should decline.
	var buf bytes.Buffer
	err := runDoctorFull(&buf, strings.NewReader("\n"), env, true)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if installCalled {
		t.Error("expected install command NOT to be called on empty input")
	}
}

// --- checkArapucaWrapper tests ---

func TestCheckArapucaWrapper_SkippedWhenNoConfig(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = nil
	r := checkArapucaWrapper(env)
	if !r.skipped {
		t.Error("expected skipped when ParsedConfig is nil")
	}
	if r.name != "arapuca-wrapper" {
		t.Errorf("expected name 'arapuca-wrapper', got %q", r.name)
	}
}

func TestCheckArapucaWrapper_SkippedWhenSandboxDisabled(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{Sandbox: config.SandboxConfig{Enabled: false}}
	r := checkArapucaWrapper(env)
	if !r.skipped {
		t.Error("expected skipped when sandbox is disabled")
	}
}

func TestCheckArapucaWrapper_FailsWhenMissing(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{Sandbox: config.SandboxConfig{Enabled: true}}
	env.ArapucaWrapperPath = func() string { return "" }
	r := checkArapucaWrapper(env)
	if r.passed {
		t.Error("expected check to fail when wrapper is missing")
	}
	if !r.required {
		t.Error("expected check to be required")
	}
	if !strings.Contains(r.detail, "Landlock/seccomp") {
		t.Errorf("expected detail to mention Landlock/seccomp, got: %q", r.detail)
	}
	if !strings.Contains(r.fix, "dnf install arapuca") {
		t.Errorf("expected fix to include install instructions, got: %q", r.fix)
	}
}

func TestCheckArapucaWrapper_PassesWhenPresent(t *testing.T) {
	env := allPassEnv()
	env.ParsedConfig = &config.Config{Sandbox: config.SandboxConfig{Enabled: true}}
	env.ArapucaWrapperPath = func() string { return "/usr/bin/arapuca" }
	r := checkArapucaWrapper(env)
	if !r.passed {
		t.Error("expected check to pass when wrapper is present")
	}
	if r.detail != "/usr/bin/arapuca" {
		t.Errorf("expected detail to be wrapper path, got: %q", r.detail)
	}
}

func TestRunDoctor_ArapucaWrapperSkippedBySandboxDisabled(t *testing.T) {
	env := allPassEnv()
	// allPassEnv returns a config without Sandbox.Enabled, so the
	// arapuca-wrapper check should be skipped (shown as "-").
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "- arapuca-wrapper:") {
		t.Errorf("expected skipped arapuca-wrapper line, got:\n%s", out)
	}
}

func TestRunDoctor_ArapucaWrapperFailsWhenSandboxEnabled(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			Sandbox: config.SandboxConfig{Enabled: true},
			GitHub: config.GitHubTicketConfig{
				Owner: "test-org",
				Repo:  "test-repo",
			},
		}, nil
	}
	env.ArapucaWrapperPath = func() string { return "" }
	var buf bytes.Buffer
	err := runDoctor(&buf, env)
	if err == nil {
		t.Fatal("expected error when sandbox enabled and wrapper missing")
	}
	out := buf.String()
	if !strings.Contains(out, "✗ arapuca-wrapper:") {
		t.Errorf("expected failed arapuca-wrapper line, got:\n%s", out)
	}
}
