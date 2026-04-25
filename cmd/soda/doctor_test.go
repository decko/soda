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
			return "", nil
		},
		Stat: func(name string) (os.FileInfo, error) {
			return mockFileInfo{name: name, isDir: strings.HasSuffix(name, "soda") && !strings.HasSuffix(name, ".yaml")}, nil
		},
		LoadConfig: func(path string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		UserConfigDir: func() (string, error) {
			return "/home/testuser/.config", nil
		},
		UserHomeDir: func() (string, error) {
			return "/home/testuser", nil
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
	// Every line before the summary should start with ✓
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || line == "All checks passed" {
			continue
		}
		if !strings.HasPrefix(line, "✓") {
			t.Errorf("expected ✓ prefix, got: %s", line)
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
	r := checkConfig(env)
	if r.passed {
		t.Error("expected config check to fail when no local config and no config dir")
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
	if !strings.Contains(r.detail, "config.yaml") {
		t.Errorf("expected detail to mention config.yaml, got: %q", r.detail)
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
