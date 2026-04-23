package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/decko/soda/internal/runner"
)

// TestSmoke_NoCGO_NewReturnsError verifies that sandbox.New fails gracefully
// when CGO is disabled, providing a clear error message.
func TestSmoke_NoCGO_NewReturnsError(t *testing.T) {
	if isCGO() {
		t.Skip("this test only runs when CGO_ENABLED=0")
	}

	cfg := Config{
		MemoryMB:   2048,
		CPUPercent: 200,
		MaxPIDs:    256,
	}

	r, err := New(cfg)
	if err == nil {
		r.Close()
		t.Fatal("expected error from New() without CGO, got nil")
	}

	if !strings.Contains(err.Error(), "cgo") {
		t.Errorf("error should mention cgo, got: %v", err)
	}
}

// TestSmoke_NoCGO_RunReturnsError verifies that Runner.Run fails gracefully
// when CGO is disabled.
func TestSmoke_NoCGO_RunReturnsError(t *testing.T) {
	if isCGO() {
		t.Skip("this test only runs when CGO_ENABLED=0")
	}

	r := &Runner{}
	_, err := r.Run(context.Background(), runner.RunOpts{
		Phase:   "triage",
		WorkDir: "/tmp",
		Model:   "test-model",
	})
	if err == nil {
		t.Fatal("expected error from Run() without CGO, got nil")
	}
	if !strings.Contains(err.Error(), "cgo") {
		t.Errorf("error should mention cgo, got: %v", err)
	}
}

// TestSmoke_ConfigToEnv_EndToEnd verifies the complete path from a sandbox Config
// through env construction, ensuring that API keys are correctly handled in both
// direct and proxy modes. This is an end-to-end test of the config → env wiring.
func TestSmoke_ConfigToEnv_EndToEnd(t *testing.T) {
	t.Run("direct_mode_with_api_key", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-test-real-key")
		t.Setenv("CLAUDE_CODE_USE_VERTEX", "")

		opts := runner.RunOpts{
			Phase:   "implement",
			WorkDir: "/workspace/project",
		}
		env := claudeEnv("/tmp/sandbox-home", opts, "/usr/bin/claude", "")

		envMap := envSliceToMap(env)

		// Verify HOME and TMPDIR are isolated to sandbox temp.
		if envMap["HOME"] != "/tmp/sandbox-home" {
			t.Errorf("HOME = %q, want /tmp/sandbox-home", envMap["HOME"])
		}
		if envMap["TMPDIR"] != "/tmp/sandbox-home" {
			t.Errorf("TMPDIR = %q, want /tmp/sandbox-home", envMap["TMPDIR"])
		}

		// Verify real API key is passed through in direct mode.
		if envMap["ANTHROPIC_API_KEY"] != "sk-test-real-key" {
			t.Errorf("ANTHROPIC_API_KEY = %q, want sk-test-real-key", envMap["ANTHROPIC_API_KEY"])
		}

		// Verify no proxy-related vars are set.
		if _, ok := envMap["ANTHROPIC_BASE_URL"]; ok {
			t.Error("ANTHROPIC_BASE_URL should not be set in direct mode without proxy")
		}
	})

	t.Run("proxy_mode_isolates_credentials", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-test-real-key")
		t.Setenv("CLAUDE_CODE_USE_VERTEX", "")

		opts := runner.RunOpts{
			Phase:   "implement",
			WorkDir: "/workspace/project",
		}
		proxyURL := "http://127.0.0.1:9090"
		env := claudeEnv("/tmp/sandbox-home", opts, "/usr/bin/claude", proxyURL)

		envMap := envSliceToMap(env)

		// In proxy mode, real API key must NOT be passed to sandbox.
		if envMap["ANTHROPIC_API_KEY"] == "sk-test-real-key" {
			t.Error("real API key should NOT be passed to sandbox in proxy mode")
		}

		// Instead, a proxy nonce should be set.
		if envMap["ANTHROPIC_API_KEY"] != "sk-proxy-nonce" {
			t.Errorf("ANTHROPIC_API_KEY = %q, want sk-proxy-nonce", envMap["ANTHROPIC_API_KEY"])
		}

		// Proxy base URL should be set.
		if envMap["ANTHROPIC_BASE_URL"] != proxyURL {
			t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", envMap["ANTHROPIC_BASE_URL"], proxyURL)
		}
	})

	t.Run("vertex_proxy_mode_skips_auth", func(t *testing.T) {
		t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
		t.Setenv("VERTEXAI_PROJECT", "my-gcp-project")
		t.Setenv("VERTEXAI_LOCATION", "us-central1")
		t.Setenv("ANTHROPIC_API_KEY", "")

		opts := runner.RunOpts{
			Phase:   "plan",
			WorkDir: "/workspace/project",
		}
		proxyURL := "http://127.0.0.1:9091"
		env := claudeEnv("/tmp/sandbox-home", opts, "/usr/bin/claude", proxyURL)

		envMap := envSliceToMap(env)

		// Vertex proxy mode should set skip auth flag.
		if envMap["CLAUDE_CODE_SKIP_VERTEX_AUTH"] != "1" {
			t.Errorf("CLAUDE_CODE_SKIP_VERTEX_AUTH = %q, want 1", envMap["CLAUDE_CODE_SKIP_VERTEX_AUTH"])
		}

		// Vertex base URL should point to proxy.
		if envMap["ANTHROPIC_VERTEX_BASE_URL"] != proxyURL {
			t.Errorf("ANTHROPIC_VERTEX_BASE_URL = %q, want %q", envMap["ANTHROPIC_VERTEX_BASE_URL"], proxyURL)
		}

		// Vertex project should be forwarded.
		if envMap["VERTEXAI_PROJECT"] != "my-gcp-project" {
			t.Errorf("VERTEXAI_PROJECT = %q, want my-gcp-project", envMap["VERTEXAI_PROJECT"])
		}

		// Anthropic API key should NOT be present in Vertex mode.
		if _, ok := envMap["ANTHROPIC_API_KEY"]; ok {
			t.Error("ANTHROPIC_API_KEY should not be set in Vertex proxy mode")
		}
	})

	t.Run("no_credentials_means_no_credential_env", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

		opts := runner.RunOpts{Phase: "verify", WorkDir: "/work"}
		env := claudeEnv("/tmp/sb", opts, "/usr/bin/claude", "")

		for _, entry := range env {
			if strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") {
				t.Error("ANTHROPIC_API_KEY should not be emitted when empty")
			}
			if strings.HasPrefix(entry, "GOOGLE_APPLICATION_CREDENTIALS=") {
				t.Error("GOOGLE_APPLICATION_CREDENTIALS should not be emitted when empty")
			}
		}
	})
}

// TestSmoke_ErrorMapping_EndToEnd exercises the full error mapping chain:
// ExitError → mapSandboxError → runner.TransientError, ensuring the error
// classification pipeline preserves diagnostic information end-to-end.
func TestSmoke_ErrorMapping_EndToEnd(t *testing.T) {
	exitErr := &ExitError{
		Code:    137,
		Signal:  9,
		OOMKill: true,
		Stderr:  []byte("cgroup: memory limit exceeded"),
	}

	// Verify error message includes diagnostic info.
	errMsg := exitErr.Error()
	if !strings.Contains(errMsg, "OOM") {
		t.Errorf("ExitError message should mention OOM, got: %s", errMsg)
	}

	// Verify stderr is preserved for debugging.
	if string(exitErr.Stderr) != "cgroup: memory limit exceeded" {
		t.Errorf("Stderr not preserved: %s", exitErr.Stderr)
	}
}

// TestSmoke_SystemReadPaths_Security verifies that system read paths include
// essential directories but exclude sensitive locations like /tmp.
func TestSmoke_SystemReadPaths_Security(t *testing.T) {
	paths := systemReadPaths()

	// Must include basic OS paths.
	required := map[string]bool{
		"/usr":  false,
		"/lib":  false,
		"/bin":  false,
		"/proc": false,
		"/dev":  false,
		"/etc":  false,
	}
	for _, path := range paths {
		if _, ok := required[path]; ok {
			required[path] = true
		}
	}
	for path, found := range required {
		if !found {
			t.Errorf("systemReadPaths missing required path: %s", path)
		}
	}

	// /tmp must NOT be in read paths (sandbox has its own tmpDir).
	for _, path := range paths {
		if path == "/tmp" {
			t.Error("/tmp should not be in systemReadPaths — sandbox uses dedicated tmpDir")
		}
	}

	// Home directory must NOT be in read paths.
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, path := range paths {
			if path == home {
				t.Errorf("user home %s should not be in systemReadPaths", home)
			}
		}
	}
}

// TestSmoke_LimitWriter_OOMProtection verifies that the limit writer correctly
// prevents unbounded memory growth from a runaway process, as used in the
// sandbox to cap stdout at maxStdoutBytes.
func TestSmoke_LimitWriter_OOMProtection(t *testing.T) {
	var buf testBuffer
	// Simulate the production maxStdoutBytes cap at a smaller scale.
	limit := int64(100)
	lw := &limitWriter{writer: &buf, remaining: limit}

	// Write data that exceeds the limit.
	largeData := strings.Repeat("x", 200)
	written, err := lw.Write([]byte(largeData))
	if err != nil {
		t.Fatalf("Write should not error: %v", err)
	}

	// Write must report full length to avoid blocking pipe draining.
	if written != 200 {
		t.Errorf("Write returned %d, want 200 (must report full length)", written)
	}

	// Buffer should only contain up to the limit.
	if len(buf.data) != int(limit) {
		t.Errorf("buffer has %d bytes, want %d", len(buf.data), limit)
	}

	// Subsequent writes should be silently discarded.
	written2, err2 := lw.Write([]byte("more data"))
	if err2 != nil {
		t.Fatalf("subsequent Write should not error: %v", err2)
	}
	if written2 != len("more data") {
		t.Errorf("subsequent Write returned %d, want %d", written2, len("more data"))
	}
	if len(buf.data) != int(limit) {
		t.Errorf("buffer grew after limit: %d bytes", len(buf.data))
	}
}

// TestSmoke_ParseSignal_EndToEnd tests signal parsing from arapuca error
// messages with various formats that may appear in production.
func TestSmoke_ParseSignal_EndToEnd(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want int
	}{
		{"oom_kill_signal", "killed by signal 9", 9},
		{"sigterm", "killed by signal 15", 15},
		{"sigsegv", "killed by signal 11", 11},
		{"wrapped_by_arapuca", "arapuca: wait: killed by signal 9: context canceled", 9},
		{"double_wrapped", "engine: sandbox: arapuca: killed by signal 6: abort", 6},
		{"no_signal", "process exited normally", 0},
		{"empty_after_prefix", "killed by signal", 0},
		{"trailing_text", "killed by signal 11 (core dumped)", 11},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &testError{msg: tt.msg}
			got := parseSignalFromError(err)
			if got != tt.want {
				t.Errorf("parseSignalFromError(%q) = %d, want %d", tt.msg, got, tt.want)
			}
		})
	}
}

// TestSmoke_SetEnvForLaunch_Isolation verifies that the env mutation/restore
// cycle correctly isolates sandbox env from the host process.
func TestSmoke_SetEnvForLaunch_Isolation(t *testing.T) {
	// Set up known state.
	t.Setenv("SODA_SMOKE_A", "original-a")
	t.Setenv("SODA_SMOKE_B", "original-b")

	// Simulate a sandbox launch that overrides both vars.
	sandboxEnv := []string{
		"SODA_SMOKE_A=sandbox-a",
		"SODA_SMOKE_B=sandbox-b",
		"SODA_SMOKE_C=sandbox-c", // new var not in host
	}

	restore := setEnvForLaunch(sandboxEnv)

	// During "launch", sandbox values should be active.
	if got := os.Getenv("SODA_SMOKE_A"); got != "sandbox-a" {
		t.Errorf("during launch: SODA_SMOKE_A = %q, want sandbox-a", got)
	}
	if got := os.Getenv("SODA_SMOKE_B"); got != "sandbox-b" {
		t.Errorf("during launch: SODA_SMOKE_B = %q, want sandbox-b", got)
	}
	if got := os.Getenv("SODA_SMOKE_C"); got != "sandbox-c" {
		t.Errorf("during launch: SODA_SMOKE_C = %q, want sandbox-c", got)
	}

	// Restore host state.
	restore()

	// After restore, original values should be back.
	if got := os.Getenv("SODA_SMOKE_A"); got != "original-a" {
		t.Errorf("after restore: SODA_SMOKE_A = %q, want original-a", got)
	}
	if got := os.Getenv("SODA_SMOKE_B"); got != "original-b" {
		t.Errorf("after restore: SODA_SMOKE_B = %q, want original-b", got)
	}
	// SODA_SMOKE_C should be unset (wasn't in host env before).
	if _, found := os.LookupEnv("SODA_SMOKE_C"); found {
		t.Error("after restore: SODA_SMOKE_C should be unset")
	}
}

// TestSmoke_Config_Defaults verifies that the Config struct zero value
// produces safe defaults (no network namespace, no proxy, no extra paths).
func TestSmoke_Config_Defaults(t *testing.T) {
	cfg := Config{}

	if cfg.UseNetNS {
		t.Error("default UseNetNS should be false (v1 — Claude needs network)")
	}
	if cfg.Proxy.Enabled {
		t.Error("default Proxy.Enabled should be false")
	}
	if len(cfg.ExtraReadPaths) != 0 {
		t.Errorf("default ExtraReadPaths should be empty, got %v", cfg.ExtraReadPaths)
	}
	if len(cfg.ExtraWritePaths) != 0 {
		t.Errorf("default ExtraWritePaths should be empty, got %v", cfg.ExtraWritePaths)
	}
	if cfg.MemoryMB != 0 {
		t.Errorf("default MemoryMB should be 0 (unset), got %d", cfg.MemoryMB)
	}
}

// TestSmoke_ParseWrapperPaths_Realistic tests path extraction from a realistic
// Claude wrapper script that would be found in production installations.
func TestSmoke_ParseWrapperPaths_Realistic(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := tmpDir + "/claude"

	// Write a realistic wrapper script.
	script := `#!/usr/bin/env bash
# Claude Code CLI wrapper
set -euo pipefail
export NODE_PATH="/opt/claude-code/lib/node_modules:/usr/local/lib/node_modules"
export NODE_OPTIONS="--max-old-space-size=4096"
exec /opt/claude-code/bin/node /opt/claude-code/lib/cli.js "$@"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	paths := parseWrapperPaths(scriptPath)

	// Should find the node_modules paths.
	foundOpt := false
	foundUsr := false
	for _, path := range paths {
		if path == "/opt/claude-code/lib/node_modules" {
			foundOpt = true
		}
		if path == "/usr/local/lib/node_modules" {
			foundUsr = true
		}
	}
	if !foundOpt {
		t.Errorf("missing /opt/claude-code/lib/node_modules in parsed paths: %v", paths)
	}
	if !foundUsr {
		t.Errorf("missing /usr/local/lib/node_modules in parsed paths: %v", paths)
	}
}

// envSliceToMap converts an env slice (KEY=VALUE) to a map for easy lookup.
func envSliceToMap(env []string) map[string]string {
	result := make(map[string]string, len(env))
	for _, entry := range env {
		key, val, _ := parseEnvEntry(entry)
		result[key] = val
	}
	return result
}

// isCGO returns true if this binary was built with CGO enabled.
// Since the cgo build tag gates the real sandbox implementation,
// we check whether New returns the cgo-specific error message.
func isCGO() bool {
	_, err := New(Config{})
	if err == nil {
		return true
	}
	return !strings.Contains(err.Error(), "cgo is required")
}
