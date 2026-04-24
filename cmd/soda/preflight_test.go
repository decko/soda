package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/config"
)

func TestRunPreflight_AllPass(t *testing.T) {
	env := allPassEnv()
	err := runPreflight(env, false)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRunPreflight_AllPass_MockMode(t *testing.T) {
	// When mock mode is enabled, claude checks are skipped.
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	err := runPreflight(env, true)
	if err != nil {
		t.Fatalf("expected no error in mock mode even without claude, got: %v", err)
	}
}

func TestRunPreflight_GitMissing(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "git" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	err := runPreflight(env, false)
	if err == nil {
		t.Fatal("expected error when git is missing")
	}
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PreflightError, got: %T", err)
	}
	// git check should fail; git-repo should be skipped (so only 1 failure for git)
	found := false
	for _, f := range pe.Failures {
		if f.name == "git" {
			found = true
		}
		if f.name == "git-repo" {
			t.Error("git-repo should be skipped when git is missing, not listed as failure")
		}
	}
	if !found {
		t.Error("expected git failure in PreflightError")
	}
}

func TestRunPreflight_NotGitRepo(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "", errors.New("not a git repository")
		}
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return fmt.Sprintf("claude %s", claude.MinCLIVersion), nil
		}
		return "", nil
	}
	err := runPreflight(env, false)
	if err == nil {
		t.Fatal("expected error when not in a git repo")
	}
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PreflightError, got: %T", err)
	}
	found := false
	for _, f := range pe.Failures {
		if f.name == "git-repo" {
			found = true
		}
	}
	if !found {
		t.Error("expected git-repo failure in PreflightError")
	}
}

func TestRunPreflight_ClaudeMissing(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	err := runPreflight(env, false)
	if err == nil {
		t.Fatal("expected error when claude is missing")
	}
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PreflightError, got: %T", err)
	}
	found := false
	for _, f := range pe.Failures {
		if f.name == "claude" {
			found = true
		}
		// claude-version should be skipped, not a failure
		if f.name == "claude-version" {
			t.Error("claude-version should be skipped when claude is missing")
		}
	}
	if !found {
		t.Error("expected claude failure in PreflightError")
	}
}

func TestRunPreflight_ClaudeMissing_MockMode(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	// In mock mode, claude checks are entirely skipped.
	err := runPreflight(env, true)
	if err != nil {
		t.Fatalf("expected no error in mock mode, got: %v", err)
	}
}

func TestRunPreflight_ClaudeVersionTooOld(t *testing.T) {
	env := allPassEnv()
	env.RunCmd = func(name string, args ...string) (string, error) {
		if name == "claude" && len(args) > 0 && args[0] == "--version" {
			return "claude 1.0.0", nil
		}
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return ".git", nil
		}
		return "", nil
	}
	err := runPreflight(env, false)
	if err == nil {
		t.Fatal("expected error when claude version is too old")
	}
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PreflightError, got: %T", err)
	}
	found := false
	for _, f := range pe.Failures {
		if f.name == "claude-version" {
			found = true
			if !strings.Contains(f.detail, "minimum required") {
				t.Errorf("expected 'minimum required' in detail, got: %q", f.detail)
			}
		}
	}
	if !found {
		t.Error("expected claude-version failure in PreflightError")
	}
}

func TestRunPreflight_NoConfig(t *testing.T) {
	env := allPassEnv()
	env.Stat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	env.UserConfigDir = func() (string, error) {
		return "/home/testuser/.config", nil
	}
	err := runPreflight(env, false)
	if err == nil {
		t.Fatal("expected error when config is missing")
	}
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PreflightError, got: %T", err)
	}
	found := false
	for _, f := range pe.Failures {
		if f.name == "config" {
			found = true
		}
	}
	if !found {
		t.Error("expected config failure in PreflightError")
	}
}

func TestRunPreflight_InvalidConfig(t *testing.T) {
	env := allPassEnv()
	env.LoadConfig = func(path string) (*config.Config, error) {
		return nil, errors.New("invalid YAML syntax")
	}
	err := runPreflight(env, false)
	if err == nil {
		t.Fatal("expected error when config is invalid")
	}
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PreflightError, got: %T", err)
	}
	found := false
	for _, f := range pe.Failures {
		if f.name == "config-valid" {
			found = true
		}
	}
	if !found {
		t.Error("expected config-valid failure in PreflightError")
	}
}

func TestRunPreflight_MultipleFailures(t *testing.T) {
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	env.Stat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	err := runPreflight(env, false)
	if err == nil {
		t.Fatal("expected error with multiple failures")
	}
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PreflightError, got: %T", err)
	}
	if len(pe.Failures) < 3 {
		t.Errorf("expected at least 3 failures (git, claude, config), got %d", len(pe.Failures))
	}
}

func TestPreflightError_ErrorMessage(t *testing.T) {
	pe := &PreflightError{
		Failures: []checkResult{
			{name: "git", detail: "not found in PATH", fix: "install git"},
			{name: "claude", detail: "not found in PATH", fix: "install Claude Code"},
		},
	}
	msg := pe.Error()

	if !strings.Contains(msg, "preflight check failed") {
		t.Error("expected 'preflight check failed' header")
	}
	if !strings.Contains(msg, "✗ git: not found in PATH") {
		t.Error("expected git failure in message")
	}
	if !strings.Contains(msg, "fix: install git") {
		t.Error("expected git fix suggestion")
	}
	if !strings.Contains(msg, "✗ claude: not found in PATH") {
		t.Error("expected claude failure in message")
	}
	if !strings.Contains(msg, "fix: install Claude Code") {
		t.Error("expected claude fix suggestion")
	}
	if !strings.Contains(msg, "soda doctor") {
		t.Error("expected 'soda doctor' suggestion")
	}
}

func TestPreflightError_ErrorMessageNoFix(t *testing.T) {
	pe := &PreflightError{
		Failures: []checkResult{
			{name: "test-check", detail: "something wrong", fix: ""},
		},
	}
	msg := pe.Error()

	if !strings.Contains(msg, "✗ test-check: something wrong") {
		t.Error("expected failure line")
	}
	if strings.Contains(msg, "fix: \n") {
		t.Error("should not show empty fix line")
	}
}

func TestRunPreflight_OptionalFailuresIgnored(t *testing.T) {
	// gh and node are optional — they should not cause preflight to fail.
	// The current preflight does not include optional checks, so this
	// verifies that optional checks from doctor are not accidentally included.
	env := allPassEnv()
	env.LookPath = func(file string) (string, error) {
		if file == "gh" || file == "node" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	err := runPreflight(env, false)
	if err != nil {
		t.Fatalf("expected no error for optional-only failures, got: %v", err)
	}
}
