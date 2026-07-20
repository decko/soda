package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	arapuca "github.com/sergio-correia/go-arapuca"

	"github.com/decko/soda/internal/runner"
)

// sandboxPaths holds the computed read and write paths for a sandbox profile.
type sandboxPaths struct {
	ReadPaths  []string
	WritePaths []string
}

// buildSandboxPaths assembles the sandbox read/write path lists from the
// worktree directory, temp directory, and any extra paths (agent-specific
// read paths merged into extraRead by the caller). The result is used to
// populate arapuca.Profile.
func buildSandboxPaths(workDir, tmpDir string, extraRead, extraWrite []string) sandboxPaths {
	readPaths := systemReadPaths()
	readPaths = append(readPaths, workDir)
	readPaths = append(readPaths, extraRead...)

	// Allow SSH agent socket access for git push.
	if sshSock := os.Getenv("SSH_AUTH_SOCK"); sshSock != "" {
		readPaths = append(readPaths, filepath.Dir(sshSock))
	}

	writePaths := []string{workDir}
	writePaths = append(writePaths, extraWrite...)

	// Temp dir needs both read and write access.
	readPaths = append(readPaths, tmpDir)
	writePaths = append(writePaths, tmpDir)

	return sandboxPaths{ReadPaths: readPaths, WritePaths: writePaths}
}

// effectiveUseNetNS returns the effective network namespace isolation flag.
// When MCP servers are configured without allowed_hosts, network isolation
// is disabled because MCP servers may need outbound connections. When all
// MCP servers declare allowed_hosts, network isolation is preserved and
// traffic is routed through the sandbox CONNECT proxy.
func effectiveUseNetNS(configured bool, servers map[string]runner.MCPServerConfig) bool {
	if len(servers) == 0 {
		return configured
	}
	if allServersHaveAllowedHosts(servers) {
		return true
	}
	return false
}

// allServersHaveAllowedHosts returns true when every server in the map
// declares at least one AllowedHost entry.
func allServersHaveAllowedHosts(servers map[string]runner.MCPServerConfig) bool {
	for _, srv := range servers {
		if len(srv.AllowedHosts) == 0 {
			return false
		}
	}
	return true
}

// collectAllowedHosts aggregates AllowedHost entries from all MCP servers
// into a deduplicated list of arapuca.AllowedHost values for the sandbox
// CONNECT proxy.
func collectAllowedHosts(servers map[string]runner.MCPServerConfig) []arapuca.AllowedHost {
	type hostPort struct {
		host string
		port uint16
	}
	seen := make(map[hostPort]struct{})
	var result []arapuca.AllowedHost
	for _, srv := range servers {
		for _, ah := range srv.AllowedHosts {
			key := hostPort{host: ah.Host, port: ah.Port}
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				result = append(result, arapuca.AllowedHost{
					Host: ah.Host,
					Port: ah.Port,
				})
			}
		}
	}
	return result
}

// mcpNetworkWarning returns a warning message indicating that network
// isolation has been disabled for the given phase because MCP servers are
// configured.
func mcpNetworkWarning(phase string) string {
	return fmt.Sprintf("sandbox: warning: network isolation disabled for phase %q because MCP servers are configured\n", phase)
}

// buildProxyURL formats a proxy base URL from a listener address string
// (e.g. "127.0.0.1:43210" → "http://127.0.0.1:43210").
func buildProxyURL(addr string) string {
	return fmt.Sprintf("http://%s", addr)
}
