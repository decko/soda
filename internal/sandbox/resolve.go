package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/decko/soda/internal/runner"
)

// resolveClaudePaths finds the claude binary and collects paths
// needed for the sandbox read profile (node binary, node_modules, etc.).
func resolveClaudePaths(binary string) (resolved string, readPaths []string, err error) {
	if binary == "" {
		binary = "claude"
	}

	resolved, err = exec.LookPath(binary)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: claude binary not found: %w", err)
	}

	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: resolve claude symlink: %w", err)
	}

	// The claude wrapper directory needs read access.
	readPaths = append(readPaths, filepath.Dir(resolved))

	// Claude Code is a Node.js app. The wrapper script typically invokes
	// node. Try to find node and add its directory + NODE_PATH.
	if nodePath, nodeErr := exec.LookPath("node"); nodeErr == nil {
		if realNode, symErr := filepath.EvalSymlinks(nodePath); symErr == nil {
			readPaths = append(readPaths, filepath.Dir(realNode))
		}
	}

	// Parse the wrapper script to find additional paths (NODE_PATH, etc.)
	readPaths = append(readPaths, parseWrapperPaths(resolved)...)

	// NODE_PATH from environment
	if nodePath := os.Getenv("NODE_PATH"); nodePath != "" {
		readPaths = append(readPaths, filepath.SplitList(nodePath)...)
	}

	return resolved, readPaths, nil
}

// parseWrapperPaths reads a shell wrapper script and extracts paths
// from NODE_PATH exports and node_modules references.
func parseWrapperPaths(scriptPath string) []string {
	file, err := os.Open(scriptPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var paths []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Look for NODE_PATH= or similar path assignments
		if strings.Contains(line, "NODE_PATH=") || strings.Contains(line, "node_modules") {
			// Extract quoted paths
			for _, part := range strings.Fields(line) {
				if idx := strings.Index(part, "="); idx >= 0 {
					val := strings.Trim(part[idx+1:], `"'`)
					for _, entry := range filepath.SplitList(val) {
						if filepath.IsAbs(entry) {
							paths = append(paths, entry)
						}
					}
				}
			}
		}
	}
	return paths
}

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

// claudeEnv builds the environment for a sandboxed Claude Code process.
// When proxyURL is non-empty, real credentials are NOT passed to the sandbox.
// Instead, a fake API key is set and the base URL points to the proxy.
// The proxy injects real credentials on the host side.
func claudeEnv(tmpDir string, opts runner.RunOpts, claudeBin, proxyURL string) []string {
	env := []string{
		"HOME=" + tmpDir,
		"TMPDIR=" + tmpDir,
		"LANG=" + envOrDefault("LANG", "en_US.UTF-8"),
	}

	// PATH: include claude binary dir, node binary dir, standard paths
	pathDirs := []string{filepath.Dir(claudeBin)}
	if nodePath, err := exec.LookPath("node"); err == nil {
		if realNode, symErr := filepath.EvalSymlinks(nodePath); symErr == nil {
			pathDirs = append(pathDirs, filepath.Dir(realNode))
		}
	}
	pathDirs = append(pathDirs, "/usr/local/bin", "/usr/bin", "/bin")
	env = append(env, "PATH="+strings.Join(pathDirs, ":"))

	// NODE_PATH passthrough
	if nodePath := os.Getenv("NODE_PATH"); nodePath != "" {
		env = append(env, "NODE_PATH="+nodePath)
	}

	if proxyURL != "" {
		// Proxy mode: the proxy on the host side handles authentication.
		if os.Getenv("CLAUDE_CODE_USE_VERTEX") != "" {
			// Vertex mode: pass through Vertex config (project, location)
			// but route API calls through the proxy and skip Vertex auth
			// (the proxy injects Google OAuth tokens on the host side).
			env = append(env,
				"CLAUDE_CODE_USE_VERTEX=1",
				"ANTHROPIC_VERTEX_BASE_URL="+proxyURL,
				"CLAUDE_CODE_SKIP_VERTEX_AUTH=1",
			)
			for _, key := range []string{"VERTEXAI_PROJECT", "VERTEXAI_LOCATION", "CLOUD_ML_REGION", "ANTHROPIC_VERTEX_PROJECT_ID"} {
				if val := os.Getenv(key); val != "" {
					env = append(env, key+"="+val)
				}
			}
		} else {
			// Direct Anthropic mode: fake API key + base URL.
			env = append(env,
				"ANTHROPIC_API_KEY=sk-proxy-nonce",
				"ANTHROPIC_BASE_URL="+proxyURL,
			)
		}
	} else {
		// Direct mode: pass through real credentials.
		credentialVars := []string{
			"ANTHROPIC_API_KEY",
			"CLAUDE_CODE_USE_VERTEX",
			"VERTEXAI_PROJECT",
			"VERTEXAI_LOCATION",
			"CLOUD_ML_REGION",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_CLOUD_PROJECT",
		}
		for _, key := range credentialVars {
			if val := os.Getenv(key); val != "" {
				env = append(env, key+"="+val)
			}
		}
	}

	return env
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
