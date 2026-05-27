package runner

import (
	"errors"
	"testing"
)

func TestAgentByName_Claude(t *testing.T) {
	info := AgentByName("claude")
	if info == nil {
		t.Fatal("expected non-nil AgentInfo for claude")
	}
	if info.Binary != "claude" {
		t.Errorf("expected binary 'claude', got %q", info.Binary)
	}
	if len(info.InstallCmds) == 0 {
		t.Error("expected at least one install method for claude")
	}
}

func TestAgentByName_Pi(t *testing.T) {
	info := AgentByName("pi")
	if info == nil {
		t.Fatal("expected non-nil AgentInfo for pi")
	}
	if info.Binary != "pi" {
		t.Errorf("expected binary 'pi', got %q", info.Binary)
	}
	if len(info.InstallCmds) < 2 {
		t.Errorf("expected at least 2 install methods for pi, got %d", len(info.InstallCmds))
	}
}

func TestAgentByName_Opencode(t *testing.T) {
	info := AgentByName("opencode")
	if info == nil {
		t.Fatal("expected non-nil AgentInfo for opencode")
	}
	if info.Binary != "opencode" {
		t.Errorf("expected binary 'opencode', got %q", info.Binary)
	}
	if len(info.InstallCmds) < 2 {
		t.Errorf("expected at least 2 install methods for opencode, got %d", len(info.InstallCmds))
	}
}

func TestAgentByName_Unknown(t *testing.T) {
	info := AgentByName("nonexistent")
	if info != nil {
		t.Errorf("expected nil for unknown agent, got %+v", info)
	}
}

func TestInstallHint_WithMethods(t *testing.T) {
	info := AgentByName("claude")
	hint := InstallHint(info)
	if hint == "" {
		t.Error("expected non-empty install hint for claude")
	}
	if hint != info.InstallCmds[0].Command {
		t.Errorf("expected first install command, got %q", hint)
	}
}

func TestInstallHint_NilAgent(t *testing.T) {
	hint := InstallHint(nil)
	if hint != "" {
		t.Errorf("expected empty hint for nil agent, got %q", hint)
	}
}

func TestInstallHint_EmptyMethods(t *testing.T) {
	info := &AgentInfo{Name: "test", Binary: "test"}
	hint := InstallHint(info)
	if hint != "" {
		t.Errorf("expected empty hint when no install methods, got %q", hint)
	}
}

func TestDetectAgent_Found(t *testing.T) {
	lookPath := func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	runCmd := func(name string, args ...string) (string, error) {
		return "pi 1.2.3", nil
	}

	path, version, err := DetectAgent(lookPath, runCmd, "pi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/usr/bin/pi" {
		t.Errorf("expected path '/usr/bin/pi', got %q", path)
	}
	if version != "1.2.3" {
		t.Errorf("expected version '1.2.3', got %q", version)
	}
}

func TestDetectAgent_NotFound(t *testing.T) {
	lookPath := func(file string) (string, error) {
		return "", errors.New("not found")
	}
	runCmd := func(name string, args ...string) (string, error) {
		return "", nil
	}

	path, version, err := DetectAgent(lookPath, runCmd, "opencode")
	if err == nil {
		t.Fatal("expected error when agent not found")
	}
	if path != "" {
		t.Errorf("expected empty path, got %q", path)
	}
	if version != "" {
		t.Errorf("expected empty version, got %q", version)
	}
}

func TestDetectAgent_UnknownName(t *testing.T) {
	lookPath := func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	runCmd := func(name string, args ...string) (string, error) {
		return "", nil
	}

	path, version, err := DetectAgent(lookPath, runCmd, "unknown-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" {
		t.Errorf("expected empty path for unknown agent, got %q", path)
	}
	if version != "" {
		t.Errorf("expected empty version for unknown agent, got %q", version)
	}
}

func TestDetectAgent_VersionExtractionFails(t *testing.T) {
	lookPath := func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	runCmd := func(name string, args ...string) (string, error) {
		return "", errors.New("command failed")
	}

	path, version, err := DetectAgent(lookPath, runCmd, "claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/usr/bin/claude" {
		t.Errorf("expected path '/usr/bin/claude', got %q", path)
	}
	if version != "" {
		t.Errorf("expected empty version when --version fails, got %q", version)
	}
}

func TestDetectAgent_VersionUnparseable(t *testing.T) {
	lookPath := func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	runCmd := func(name string, args ...string) (string, error) {
		return "no version info here", nil
	}

	path, version, err := DetectAgent(lookPath, runCmd, "pi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/usr/bin/pi" {
		t.Errorf("expected path '/usr/bin/pi', got %q", path)
	}
	if version != "" {
		t.Errorf("expected empty version for unparseable output, got %q", version)
	}
}

func TestExtractAgentSemver(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"pi 1.2.3", "1.2.3"},
		{"opencode 10.20.300", "10.20.300"},
		{"1.0.0", "1.0.0"},
		{"no version", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractAgentSemver(tt.input)
		if got != tt.want {
			t.Errorf("extractAgentSemver(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
