package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/decko/soda/internal/runner"
)

// systemReadPaths returns the standard OS paths required for process execution.
// Note: /tmp is intentionally excluded — the sandbox has its own tmpDir with
// read+write access. Including /tmp globally would expose other processes' temp files.
func systemReadPaths() []string {
	return []string{
		"/usr",
		"/lib",
		"/lib64",
		"/bin",
		"/sbin",
		"/etc",
		"/dev",
		"/proc",
	}
}

// sanitizePhase replaces slashes with dashes so the phase name is safe
// for use in filesystem paths (e.g. "review/go-specialist" → "review-go-specialist").
func sanitizePhase(phase string) string {
	return strings.ReplaceAll(phase, "/", "-")
}

// resolveMCPBinaryReadPaths iterates MCP server configs, resolves each
// command via exec.LookPath + filepath.EvalSymlinks, and returns the parent
// directories as read paths. Missing binaries emit a warning to stderr and
// are skipped — no hard failure.
func resolveMCPBinaryReadPaths(servers map[string]runner.MCPServerConfig) []string {
	var paths []string
	for name, srv := range servers {
		if srv.Command == "" {
			continue
		}
		resolved, err := exec.LookPath(srv.Command)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sandbox: warning: MCP server %q binary %q not found, skipping\n", name, srv.Command)
			continue
		}
		resolved, err = filepath.EvalSymlinks(resolved)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sandbox: warning: MCP server %q binary %q symlink resolve failed, skipping\n", name, srv.Command)
			continue
		}
		paths = append(paths, filepath.Dir(resolved))
	}
	return paths
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
