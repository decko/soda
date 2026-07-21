package sandbox

import (
	"strings"
	"testing"

	"github.com/decko/soda/internal/runner"
)

func TestEffectiveUseNetNS(t *testing.T) {
	srv := func(allowedHosts ...string) runner.MCPServerConfig {
		return runner.MCPServerConfig{
			Command:      "some-mcp",
			AllowedHosts: allowedHosts,
		}
	}

	tests := []struct {
		name       string
		configured bool
		servers    map[string]runner.MCPServerConfig
		want       bool
	}{
		// === configured=false cases ===
		// The core fix: when the user explicitly disabled netns, we must never
		// upgrade it — regardless of whether MCP servers declare allowed_hosts.
		{
			name:       "configured_false_no_servers",
			configured: false,
			servers:    nil,
			want:       false,
		},
		{
			name:       "configured_false_servers_no_allowed_hosts",
			configured: false,
			servers:    map[string]runner.MCPServerConfig{"jira": srv()},
			want:       false,
		},
		{
			name:       "configured_false_servers_with_allowed_hosts",
			configured: false,
			servers:    map[string]runner.MCPServerConfig{"jira": srv("jira.example.com")},
			want:       false, // BUG in PR #679: this used to return true
		},
		{
			name:       "configured_false_all_servers_have_allowed_hosts",
			configured: false,
			servers: map[string]runner.MCPServerConfig{
				"jira":   srv("jira.example.com"),
				"github": srv("api.github.com"),
			},
			want: false, // BUG in PR #679: this used to return true
		},
		// === configured=true cases ===
		{
			name:       "configured_true_no_servers",
			configured: true,
			servers:    nil,
			want:       true,
		},
		{
			name:       "configured_true_server_no_allowed_hosts",
			configured: true,
			servers:    map[string]runner.MCPServerConfig{"jira": srv()},
			want:       false, // needs open network
		},
		{
			name:       "configured_true_server_with_allowed_hosts",
			configured: true,
			servers:    map[string]runner.MCPServerConfig{"jira": srv("jira.example.com")},
			want:       true,
		},
		{
			name:       "configured_true_mixed_servers",
			configured: true,
			servers: map[string]runner.MCPServerConfig{
				"jira":   srv("jira.example.com"),
				"github": srv(), // no allowed_hosts → open network needed
			},
			want: false,
		},
		{
			name:       "configured_true_all_servers_have_allowed_hosts",
			configured: true,
			servers: map[string]runner.MCPServerConfig{
				"jira":   srv("jira.example.com"),
				"github": srv("api.github.com"),
			},
			want: true,
		},
		{
			name:       "configured_true_empty_servers_map",
			configured: true,
			servers:    map[string]runner.MCPServerConfig{},
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveUseNetNS(tt.configured, tt.servers)
			if got != tt.want {
				t.Errorf("effectiveUseNetNS(configured=%v, servers=%v) = %v, want %v",
					tt.configured, tt.servers, got, tt.want)
			}
		})
	}
}

func TestBuildSandboxPaths(t *testing.T) {
	sp := buildSandboxPaths("/work", "/tmp/sandbox", []string{"/extra/read"}, []string{"/extra/write"})

	// WorkDir and tmpDir must appear in read paths.
	if !containsString(sp.ReadPaths, "/work") {
		t.Errorf("ReadPaths missing /work: %v", sp.ReadPaths)
	}
	if !containsString(sp.ReadPaths, "/tmp/sandbox") {
		t.Errorf("ReadPaths missing /tmp/sandbox: %v", sp.ReadPaths)
	}
	if !containsString(sp.ReadPaths, "/extra/read") {
		t.Errorf("ReadPaths missing /extra/read: %v", sp.ReadPaths)
	}

	// WorkDir and tmpDir must appear in write paths.
	if !containsString(sp.WritePaths, "/work") {
		t.Errorf("WritePaths missing /work: %v", sp.WritePaths)
	}
	if !containsString(sp.WritePaths, "/tmp/sandbox") {
		t.Errorf("WritePaths missing /tmp/sandbox: %v", sp.WritePaths)
	}
	if !containsString(sp.WritePaths, "/extra/write") {
		t.Errorf("WritePaths missing /extra/write: %v", sp.WritePaths)
	}

	// Standard system paths must appear in ReadPaths.
	for _, sys := range systemReadPaths() {
		if !containsString(sp.ReadPaths, sys) {
			t.Errorf("ReadPaths missing system path %q: %v", sys, sp.ReadPaths)
		}
	}
}

func TestMCPNetworkWarning(t *testing.T) {
	msg := mcpNetworkWarning("implement")
	if !strings.Contains(msg, "implement") {
		t.Errorf("mcpNetworkWarning should contain phase name, got: %q", msg)
	}
	if !strings.HasSuffix(msg, "\n") {
		t.Errorf("mcpNetworkWarning should end with newline, got: %q", msg)
	}
}

func TestBuildProxyURL(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"localhost:0", "http://localhost:0"},
	}
	for _, tt := range tests {
		got := buildProxyURL(tt.addr)
		if got != tt.want {
			t.Errorf("buildProxyURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

// containsPath is an alias for containsString used by other test files
// in this package.
func containsPath(paths []string, target string) bool {
	return containsString(paths, target)
}
