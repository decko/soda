package sandbox

import (
	"os"
	"strings"
	"testing"

	"github.com/decko/soda/internal/runner"
)

func TestSystemReadPaths(t *testing.T) {
	paths := systemReadPaths()

	required := []string{"/usr", "/lib", "/bin", "/proc", "/dev", "/etc", "/tmp"}
	for _, req := range required {
		found := false
		for _, path := range paths {
			if path == req {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("systemReadPaths() missing %q", req)
		}
	}
}

func TestClaudeEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key-123")
	t.Setenv("LANG", "en_US.UTF-8")

	opts := runner.RunOpts{Phase: "triage", WorkDir: "/work"}
	env := claudeEnv("/tmp/sandbox", opts, "/usr/local/bin/claude")

	envMap := make(map[string]string)
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["HOME"] != "/tmp/sandbox" {
		t.Errorf("HOME = %q, want /tmp/sandbox", envMap["HOME"])
	}
	if envMap["TMPDIR"] != "/tmp/sandbox" {
		t.Errorf("TMPDIR = %q, want /tmp/sandbox", envMap["TMPDIR"])
	}
	if envMap["ANTHROPIC_API_KEY"] != "test-key-123" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want test-key-123", envMap["ANTHROPIC_API_KEY"])
	}
	if !strings.Contains(envMap["PATH"], "/usr/local/bin") {
		t.Errorf("PATH = %q, should contain /usr/local/bin", envMap["PATH"])
	}
}

func TestClaudeEnvVertexPassthrough(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	t.Setenv("VERTEXAI_PROJECT", "my-project")
	t.Setenv("VERTEXAI_LOCATION", "us-central1")

	opts := runner.RunOpts{Phase: "plan", WorkDir: "/work"}
	env := claudeEnv("/tmp/sb", opts, "/usr/bin/claude")

	envMap := make(map[string]string)
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["CLAUDE_CODE_USE_VERTEX"] != "1" {
		t.Errorf("CLAUDE_CODE_USE_VERTEX not passed through")
	}
	if envMap["VERTEXAI_PROJECT"] != "my-project" {
		t.Errorf("VERTEXAI_PROJECT not passed through")
	}
}

func TestClaudeEnvNoKeyMeansNoEntry(t *testing.T) {
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("CLAUDE_CODE_USE_VERTEX")

	opts := runner.RunOpts{Phase: "plan", WorkDir: "/work"}
	env := claudeEnv("/tmp/sb", opts, "/usr/bin/claude")

	for _, entry := range env {
		if strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") {
			t.Error("ANTHROPIC_API_KEY should not be present when unset")
		}
		if strings.HasPrefix(entry, "CLAUDE_CODE_USE_VERTEX=") {
			t.Error("CLAUDE_CODE_USE_VERTEX should not be present when unset")
		}
	}
}

func TestParseWrapperPaths(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "claude-wrapper-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.WriteString("#!/bin/bash\nexport NODE_PATH=\"/usr/lib/node_modules:/opt/claude/node_modules\"\nexec node \"$@\"\n")
	tmpFile.Close()

	paths := parseWrapperPaths(tmpFile.Name())

	if len(paths) == 0 {
		t.Fatal("expected paths from wrapper script, got none")
	}

	foundUsrLib := false
	foundOptClaude := false
	for _, path := range paths {
		if path == "/usr/lib/node_modules" {
			foundUsrLib = true
		}
		if path == "/opt/claude/node_modules" {
			foundOptClaude = true
		}
	}
	if !foundUsrLib {
		t.Errorf("missing /usr/lib/node_modules in parsed paths: %v", paths)
	}
	if !foundOptClaude {
		t.Errorf("missing /opt/claude/node_modules in parsed paths: %v", paths)
	}
}

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("SODA_EXISTS", "value")
	if got := envOrDefault("SODA_EXISTS", "fallback"); got != "value" {
		t.Errorf("envOrDefault = %q, want value", got)
	}

	os.Unsetenv("SODA_MISSING")
	if got := envOrDefault("SODA_MISSING", "fallback"); got != "fallback" {
		t.Errorf("envOrDefault = %q, want fallback", got)
	}
}
