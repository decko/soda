package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

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
// When MCP servers are configured, network isolation is disabled because MCP
// servers may need to make outbound connections (e.g. to Jira, GitHub APIs).
func effectiveUseNetNS(configured bool, servers map[string]runner.MCPServerConfig) bool {
	if len(servers) > 0 {
		return false
	}
	return configured
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
