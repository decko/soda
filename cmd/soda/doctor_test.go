package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

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
				return "claude 1.0.0", nil
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
	if !strings.Contains(r.detail, "1.0.0") {
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

// --- checkGlobalConfig tests ---

func TestCheckGlobalConfig_Exists(t *testing.T) {
	env := allPassEnv()
	r := checkGlobalConfig(env)
	if !r.passed {
		t.Error("expected global-config check to pass")
	}
}

func TestCheckGlobalConfig_Missing(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		if strings.Contains(name, "config.yaml") {
			return nil, os.ErrNotExist
		}
		return mockFileInfo{name: name}, nil
	}
	r := checkGlobalConfig(env)
	if r.passed {
		t.Error("expected global-config check to fail")
	}
}

func TestCheckGlobalConfig_NoConfigDir(t *testing.T) {
	env := allPassEnv()
	env.UserConfigDir = func() (string, error) {
		return "", errors.New("no config dir")
	}
	r := checkGlobalConfig(env)
	if r.passed {
		t.Error("expected global-config check to fail when config dir unknown")
	}
}

// --- checkLocalConfig tests ---

func TestCheckLocalConfig_Exists(t *testing.T) {
	env := allPassEnv()
	r := checkLocalConfig(env)
	if !r.passed {
		t.Error("expected local-config check to pass")
	}
}

func TestCheckLocalConfig_Missing(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		if name == "soda.yaml" {
			return nil, os.ErrNotExist
		}
		return mockFileInfo{name: name}, nil
	}
	r := checkLocalConfig(env)
	if r.passed {
		t.Error("expected local-config check to fail")
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
	if r.passed {
		t.Error("expected config-valid check to fail with no config files")
	}
}

// --- checkStateDir tests ---

func TestCheckStateDir_Exists(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		return mockFileInfo{name: "soda", isDir: true}, nil
	}
	r := checkStateDir(env)
	if !r.passed {
		t.Error("expected state-dir check to pass")
	}
}

func TestCheckStateDir_Missing(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		if strings.HasSuffix(name, "soda") && !strings.HasSuffix(name, ".yaml") {
			return nil, os.ErrNotExist
		}
		return mockFileInfo{name: name}, nil
	}
	r := checkStateDir(env)
	if r.passed {
		t.Error("expected state-dir check to fail")
	}
}

func TestCheckStateDir_NotADirectory(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		// Return a file (not a directory) for the soda path.
		return mockFileInfo{name: "soda", isDir: false}, nil
	}
	r := checkStateDir(env)
	if r.passed {
		t.Error("expected state-dir check to fail when path is not a directory")
	}
	if !strings.Contains(r.detail, "not a directory") {
		t.Errorf("expected 'not a directory' in detail, got: %q", r.detail)
	}
}

func TestCheckStateDir_NoConfigDir(t *testing.T) {
	env := allPassEnv()
	env.UserConfigDir = func() (string, error) {
		return "", errors.New("no config dir")
	}
	r := checkStateDir(env)
	if r.passed {
		t.Error("expected state-dir check to fail when config dir unknown")
	}
}
