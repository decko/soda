package sandbox

import (
	"strings"
	"testing"

	"github.com/decko/soda/internal/runner"
)

func TestSanitizePhase(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"slash_to_dash", "review/go-specialist", "review-go-specialist"},
		{"multiple_slashes", "a/b/c/d", "a-b-c-d"},
		{"empty_string", "", ""},
		{"clean_name", "triage", "triage"},
		{"leading_slash", "/leading", "-leading"},
		{"trailing_slash", "trailing/", "trailing-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizePhase(tt.input); got != tt.want {
				t.Errorf("sanitizePhase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestClaudeEnvGHTokenAbsentNoFallback(t *testing.T) {
	// Ensure GH_TOKEN and GITHUB_TOKEN are absent so the keyring fallback
	// path is exercised. With no `gh` binary on PATH (or if `gh auth token`
	// fails), GH_TOKEN should not appear in the env slice.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	// Hide `gh` from exec.LookPath by pointing PATH at an empty directory.
	// This makes the test deterministic regardless of host tooling.
	t.Setenv("PATH", t.TempDir())

	opts := runner.RunOpts{Phase: "submit", WorkDir: "/work"}
	env := claudeEnv("/tmp/sb", opts, "/usr/bin/claude", "")

	for _, entry := range env {
		if strings.HasPrefix(entry, "GH_TOKEN=") {
			t.Error("GH_TOKEN should not be present when gh CLI is absent")
		}
	}
}

func TestBuildSandboxPaths(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	sp := buildSandboxPaths(
		"/home/user/repo",
		"/tmp/soda-triage",
		[]string{"/opt/claude/bin", "/usr/lib/node"},
		nil, nil,
	)

	// Read paths should include system paths, claude read paths, workDir, tmpDir.
	wantRead := []string{
		"/usr", "/lib", "/bin", "/proc", "/dev", "/etc", // subset of systemReadPaths
		"/opt/claude/bin", "/usr/lib/node", // claudeRead
		"/home/user/repo",  // workDir
		"/tmp/soda-triage", // tmpDir
	}
	for _, want := range wantRead {
		if !containsPath(sp.ReadPaths, want) {
			t.Errorf("ReadPaths missing %q; got %v", want, sp.ReadPaths)
		}
	}

	// Write paths should include workDir and tmpDir.
	wantWrite := []string{"/home/user/repo", "/tmp/soda-triage"}
	for _, want := range wantWrite {
		if !containsPath(sp.WritePaths, want) {
			t.Errorf("WritePaths missing %q; got %v", want, sp.WritePaths)
		}
	}
}

func TestBuildSandboxPathsSSHAuthSock(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-XXXX/agent.1234")

	sp := buildSandboxPaths("/work", "/tmp/sb", nil, nil, nil)

	wantDir := "/tmp/ssh-XXXX"
	if !containsPath(sp.ReadPaths, wantDir) {
		t.Errorf("ReadPaths missing SSH_AUTH_SOCK dir %q; got %v", wantDir, sp.ReadPaths)
	}
}

func TestBuildSandboxPathsExtraPaths(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	extraRead := []string{"/data/models", "/opt/tools"}
	extraWrite := []string{"/var/output"}

	sp := buildSandboxPaths("/work", "/tmp/sb", nil, extraRead, extraWrite)

	for _, want := range extraRead {
		if !containsPath(sp.ReadPaths, want) {
			t.Errorf("ReadPaths missing extra read path %q; got %v", want, sp.ReadPaths)
		}
	}
	for _, want := range extraWrite {
		if !containsPath(sp.WritePaths, want) {
			t.Errorf("WritePaths missing extra write path %q; got %v", want, sp.WritePaths)
		}
	}
}

func TestBuildSandboxPathsWriteScopedToWorktree(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	sp := buildSandboxPaths("/home/user/repo", "/tmp/soda-impl", nil, nil, nil)

	// Write should have exactly workDir + tmpDir — no system paths.
	wantWrite := []string{"/home/user/repo", "/tmp/soda-impl"}
	if len(sp.WritePaths) != len(wantWrite) {
		t.Fatalf("WritePaths = %v (len %d), want exactly %v (len %d)",
			sp.WritePaths, len(sp.WritePaths), wantWrite, len(wantWrite))
	}
	for _, want := range wantWrite {
		if !containsPath(sp.WritePaths, want) {
			t.Errorf("WritePaths missing %q; got %v", want, sp.WritePaths)
		}
	}
}

func TestBuildProxyURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"localhost", "127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"ephemeral_port", "127.0.0.1:43210", "http://127.0.0.1:43210"},
		{"ipv6", "[::1]:9090", "http://[::1]:9090"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildProxyURL(tt.addr); got != tt.want {
				t.Errorf("buildProxyURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestProxyConfigFields(t *testing.T) {
	// Compile-time safety: ensure ProxyConfig struct fields exist and are
	// assignable. If the struct changes shape, this test fails at compile time.
	_ = ProxyConfig{
		Enabled:         true,
		UpstreamURL:     "https://api.anthropic.com",
		APIKey:          "sk-test",
		MaxInputTokens:  100_000,
		MaxOutputTokens: 16_000,
		LogDir:          "/var/log/proxy",
	}
}

// containsPath returns true if paths contains target.
func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}
