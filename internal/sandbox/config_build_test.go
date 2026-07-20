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
		nil,
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

	sp := buildSandboxPaths("/work", "/tmp/sb", nil, nil)

	wantDir := "/tmp/ssh-XXXX"
	if !containsPath(sp.ReadPaths, wantDir) {
		t.Errorf("ReadPaths missing SSH_AUTH_SOCK dir %q; got %v", wantDir, sp.ReadPaths)
	}
}

func TestBuildSandboxPathsExtraPaths(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	extraRead := []string{"/data/models", "/opt/tools"}
	extraWrite := []string{"/var/output"}

	sp := buildSandboxPaths("/work", "/tmp/sb", extraRead, extraWrite)

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

	sp := buildSandboxPaths("/home/user/repo", "/tmp/soda-impl", nil, nil)

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

func TestEffectiveUseNetNS(t *testing.T) {
	tests := []struct {
		name       string
		configured bool
		servers    map[string]runner.MCPServerConfig
		want       bool
	}{
		{
			name:       "nil_servers_returns_configured_true",
			configured: true,
			servers:    nil,
			want:       true,
		},
		{
			name:       "nil_servers_returns_configured_false",
			configured: false,
			servers:    nil,
			want:       false,
		},
		{
			name:       "empty_servers_returns_configured_true",
			configured: true,
			servers:    map[string]runner.MCPServerConfig{},
			want:       true,
		},
		{
			name:       "non_empty_servers_forces_false",
			configured: true,
			servers: map[string]runner.MCPServerConfig{
				"jira": {Command: "jira-mcp"},
			},
			want: false,
		},
		{
			name:       "non_empty_servers_configured_false",
			configured: false,
			servers: map[string]runner.MCPServerConfig{
				"github": {Command: "gh-mcp"},
			},
			want: false,
		},
		{
			name:       "servers_with_allowed_hosts_keeps_netns",
			configured: true,
			servers: map[string]runner.MCPServerConfig{
				"jira": {
					Command:      "jira-mcp",
					AllowedHosts: []runner.AllowedHost{{Host: "jira.example.com", Port: 443}},
				},
			},
			want: true,
		},
		{
			name:       "servers_with_allowed_hosts_enables_netns_even_if_configured_false",
			configured: false,
			servers: map[string]runner.MCPServerConfig{
				"jira": {
					Command:      "jira-mcp",
					AllowedHosts: []runner.AllowedHost{{Host: "jira.example.com", Port: 443}},
				},
			},
			want: true,
		},
		{
			name:       "mixed_servers_some_without_allowed_hosts_disables_netns",
			configured: true,
			servers: map[string]runner.MCPServerConfig{
				"jira": {
					Command:      "jira-mcp",
					AllowedHosts: []runner.AllowedHost{{Host: "jira.example.com", Port: 443}},
				},
				"github": {Command: "gh-mcp"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveUseNetNS(tt.configured, tt.servers)
			if got != tt.want {
				t.Errorf("effectiveUseNetNS(%v, %v) = %v, want %v",
					tt.configured, tt.servers, got, tt.want)
			}
		})
	}
}

func TestMCPNetworkWarning(t *testing.T) {
	warning := mcpNetworkWarning("implement")

	if !strings.Contains(warning, "implement") {
		t.Errorf("warning should contain phase name, got: %s", warning)
	}
	if !strings.Contains(warning, "network isolation") {
		t.Errorf("warning should contain 'network isolation', got: %s", warning)
	}
	if !strings.Contains(warning, "MCP") {
		t.Errorf("warning should contain 'MCP', got: %s", warning)
	}
}

func TestCollectAllowedHosts(t *testing.T) {
	tests := []struct {
		name    string
		servers map[string]runner.MCPServerConfig
		want    int
	}{
		{
			name:    "nil_servers",
			servers: nil,
			want:    0,
		},
		{
			name:    "empty_servers",
			servers: map[string]runner.MCPServerConfig{},
			want:    0,
		},
		{
			name: "servers_without_allowed_hosts",
			servers: map[string]runner.MCPServerConfig{
				"jira": {Command: "jira-mcp"},
			},
			want: 0,
		},
		{
			name: "single_server_single_host",
			servers: map[string]runner.MCPServerConfig{
				"jira": {
					Command:      "jira-mcp",
					AllowedHosts: []runner.AllowedHost{{Host: "jira.example.com", Port: 443}},
				},
			},
			want: 1,
		},
		{
			name: "multiple_servers_multiple_hosts",
			servers: map[string]runner.MCPServerConfig{
				"jira": {
					Command: "jira-mcp",
					AllowedHosts: []runner.AllowedHost{
						{Host: "jira.example.com", Port: 443},
					},
				},
				"github": {
					Command: "gh-mcp",
					AllowedHosts: []runner.AllowedHost{
						{Host: "api.github.com", Port: 443},
					},
				},
			},
			want: 2,
		},
		{
			name: "duplicate_hosts_deduplicated",
			servers: map[string]runner.MCPServerConfig{
				"jira": {
					Command: "jira-mcp",
					AllowedHosts: []runner.AllowedHost{
						{Host: "api.example.com", Port: 443},
					},
				},
				"github": {
					Command: "gh-mcp",
					AllowedHosts: []runner.AllowedHost{
						{Host: "api.example.com", Port: 443},
					},
				},
			},
			want: 1,
		},
		{
			name: "same_host_different_ports_kept",
			servers: map[string]runner.MCPServerConfig{
				"srv": {
					Command: "mcp",
					AllowedHosts: []runner.AllowedHost{
						{Host: "api.example.com", Port: 443},
						{Host: "api.example.com", Port: 8443},
					},
				},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectAllowedHosts(tt.servers)
			if len(got) != tt.want {
				t.Errorf("collectAllowedHosts() returned %d hosts, want %d", len(got), tt.want)
			}
		})
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
