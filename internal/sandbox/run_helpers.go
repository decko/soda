package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/decko/soda/internal/runner"
)

// sandboxPaths holds the filesystem access paths for a single sandbox run.
type sandboxPaths struct {
	ReadPaths  []string
	WritePaths []string
}

// buildSandboxPaths constructs the read and write path lists for a sandbox run.
//
// Standard OS read paths (from systemReadPaths) are always included so the
// sandboxed process can load libraries and execute binaries. WorkDir and tmpDir
// are always both readable and writable. Extra paths provided by adapters and
// the static Config are appended last.
func buildSandboxPaths(workDir, tmpDir string, extraRead, extraWrite []string) sandboxPaths {
	sys := systemReadPaths()
	read := make([]string, 0, len(sys)+2+len(extraRead))
	read = append(read, sys...)
	read = append(read, workDir, tmpDir)
	read = append(read, extraRead...)

	// Allow SSH agent socket access for git push.
	if sshSock := os.Getenv("SSH_AUTH_SOCK"); sshSock != "" {
		read = append(read, filepath.Dir(sshSock))
	}

	write := make([]string, 0, 2+len(extraWrite))
	write = append(write, workDir, tmpDir)
	write = append(write, extraWrite...)

	return sandboxPaths{
		ReadPaths:  read,
		WritePaths: write,
	}
}

// effectiveUseNetNS determines whether network namespace isolation should be
// active for this sandbox run.
//
// Policy:
//
//   - configured=false → always return false.
//     The user explicitly opted out of netns (e.g. because their kernel lacks
//     unprivileged user namespaces). We never silently override that decision,
//     even when all MCP servers declare allowed_hosts. Forcing netns on in that
//     case would cause a hard launch failure with a confusing error — the user
//     set use_net_ns: false precisely to avoid this.
//
//   - configured=true → return true only when netns is compatible with all
//     declared MCP servers. A server that declares AllowedHosts restricts its
//     own outbound traffic to those hosts, so the network namespace can stay
//     on. A server that omits AllowedHosts may need arbitrary internet access,
//     so netns is disabled to avoid breaking it.
func effectiveUseNetNS(configured bool, servers map[string]runner.MCPServerConfig) bool {
	if !configured {
		// Respect the user's explicit opt-out; never upgrade silently.
		return false
	}
	// User enabled netns. Keep it on only when every MCP server has declared
	// its allowed hosts — otherwise at least one server needs open network access.
	for _, srv := range servers {
		if len(srv.AllowedHosts) == 0 {
			return false
		}
	}
	return true
}

// mcpNetworkWarning returns a one-line warning message emitted to stderr when a
// phase is launched with MCP servers configured. MCP servers that do not declare
// allowed_hosts require open outbound network access, which is incompatible with
// network namespace isolation (use_net_ns). Users who rely on netns should add
// allowed_hosts to each MCP server so isolation can remain enabled.
func mcpNetworkWarning(phase string) string {
	return fmt.Sprintf(
		"sandbox: warning: phase %q uses MCP servers; "+
			"add allowed_hosts to each server to preserve network namespace isolation\n",
		phase,
	)
}

// buildProxyURL converts a TCP host:port address returned by net.Addr.String()
// into an HTTP base URL suitable for use as ANTHROPIC_BASE_URL or
// ANTHROPIC_VERTEX_BASE_URL inside the sandboxed process.
func buildProxyURL(addr string) string {
	return "http://" + addr
}
