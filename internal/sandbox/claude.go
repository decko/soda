package sandbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/runner"
)

// ClaudeAdapter implements AgentAdapter for the Claude Code CLI.
type ClaudeAdapter struct {
	binary    string   // resolved absolute path to claude binary
	readPaths []string // read paths needed for claude binary + node
}

// compile-time interface check
var _ AgentAdapter = (*ClaudeAdapter)(nil)

// NewClaudeAdapter resolves the claude binary and returns a ClaudeAdapter.
func NewClaudeAdapter(binary string) (*ClaudeAdapter, error) {
	resolved, readPaths, err := resolveClaudePaths(binary)
	if err != nil {
		return nil, err
	}
	return &ClaudeAdapter{
		binary:    resolved,
		readPaths: readPaths,
	}, nil
}

// Binary returns the resolved absolute path to the claude binary.
func (a *ClaudeAdapter) Binary() string {
	return a.binary
}

// BuildArgs writes temporary files (system prompt, settings) into tmpDir
// and returns the CLI argument list for a Claude Code invocation.
func (a *ClaudeAdapter) BuildArgs(opts runner.RunOpts, tmpDir string) ([]string, error) {
	// Write system prompt to temp file in tmpDir (not WorkDir) so it's not
	// visible to Claude Code's Read tool browsing the workspace.
	var sysPromptPath string
	if opts.SystemPrompt != "" {
		tmpFile, err := os.CreateTemp(tmpDir, ".soda-prompt-*.md")
		if err != nil {
			return nil, fmt.Errorf("sandbox: create system prompt file: %w", err)
		}
		sysPromptPath = tmpFile.Name()
		if _, err := tmpFile.WriteString(opts.SystemPrompt); err != nil {
			tmpFile.Close()
			os.Remove(sysPromptPath)
			return nil, fmt.Errorf("sandbox: write system prompt: %w", err)
		}
		tmpFile.Close()
		// No need for deferred Remove — tmpDir cleanup handles it.
	}

	// When ApiKeyHelper is set, write a settings JSON file in tmpDir so
	// Claude Code picks up the apiKeyHelper configuration.
	var settingsPath string
	if opts.ApiKeyHelper != "" {
		if !filepath.IsAbs(opts.ApiKeyHelper) {
			return nil, fmt.Errorf("sandbox: ApiKeyHelper must be an absolute path, got %q", opts.ApiKeyHelper)
		}

		settingsData, jsonErr := json.Marshal(map[string]string{
			"apiKeyHelper": opts.ApiKeyHelper,
		})
		if jsonErr != nil {
			return nil, fmt.Errorf("sandbox: marshal settings JSON: %w", jsonErr)
		}
		sf, sfErr := os.CreateTemp(tmpDir, ".soda-settings-*.json")
		if sfErr != nil {
			return nil, fmt.Errorf("sandbox: create settings file: %w", sfErr)
		}
		settingsPath = sf.Name()
		if _, wErr := sf.Write(settingsData); wErr != nil {
			sf.Close()
			return nil, fmt.Errorf("sandbox: write settings file: %w", wErr)
		}
		sf.Close()
	}

	// When MCP servers are declared, write a temp config file into tmpDir
	// and pass its path via --mcp-config + --strict-mcp-config. The file
	// is cleaned up when tmpDir is removed, so no explicit defer is needed.
	var mcpConfigPath string
	if len(opts.MCPServers) > 0 {
		mcpPath, _, mcpErr := runner.WriteMCPConfigFile(tmpDir, opts.MCPServers)
		if mcpErr != nil {
			fmt.Fprintf(os.Stderr, "sandbox: warning: MCP config write failed: %v; continuing without MCP\n", mcpErr)
		} else {
			mcpConfigPath = mcpPath
		}
	}

	// Build Claude CLI args via exported BuildArgs.
	var budgetPtr *float64
	if opts.MaxBudgetUSD > 0 {
		budgetPtr = &opts.MaxBudgetUSD
	}
	claudeOpts := claude.RunOpts{
		SystemPromptPath: sysPromptPath,
		SettingsPath:     settingsPath,
		MCPConfigPath:    mcpConfigPath,
		StrictMCPConfig:  mcpConfigPath != "",
		OutputSchema:     opts.OutputSchema,
		AllowedTools:     opts.AllowedTools,
		MaxBudgetUSD:     budgetPtr,
		Timeout:          opts.Timeout,
		TranscriptLevel:  opts.TranscriptLevel,
	}

	// When MCP servers provide extra tool declarations, append them so
	// Claude Code's allowlist permits MCP tool calls.
	if len(opts.AllowedMCPTools) > 0 {
		claudeOpts.AllowedTools = append(claudeOpts.AllowedTools, opts.AllowedMCPTools...)
	}

	args := claude.BuildArgs(claudeOpts, opts.Model)

	// Append user prompt as positional arg (stdin workaround — see issue #2 Fix 4).
	args = append(args, "-p", opts.UserPrompt)

	return args, nil
}

// BuildEnv returns the environment variables for a sandboxed Claude Code process.
func (a *ClaudeAdapter) BuildEnv(opts runner.RunOpts, tmpDir string, proxyURL string) []string {
	return claudeEnv(tmpDir, opts, a.binary, proxyURL)
}

// ParseOutput parses buffered stdout from a Claude Code invocation into a RunResult.
func (a *ClaudeAdapter) ParseOutput(stdout []byte, opts runner.RunOpts) (*runner.RunResult, error) {
	result, err := claude.ParseResponse(stdout)
	if err != nil {
		return nil, mapClaudeParseError(err)
	}
	transcript := claude.FilterTranscript(stdout, opts.TranscriptLevel)
	return mapResult(result, transcript), nil
}

// ExtraPaths returns additional read and write paths required by Claude Code.
// This includes the claude binary directory, node paths, the API key helper
// script directory (if configured), and Vertex-specific paths.
func (a *ClaudeAdapter) ExtraPaths(opts runner.RunOpts) (read []string, write []string) {
	read = append(read, a.readPaths...)

	// Allow the sandbox to read the helper script's parent directory.
	if opts.ApiKeyHelper != "" && filepath.IsAbs(opts.ApiKeyHelper) {
		read = append(read, filepath.Dir(opts.ApiKeyHelper))
	}

	// Vertex mode: Claude Code reads ~/.claude/claude.settings for Vertex
	// env vars (project, region) and validates model availability against
	// the Vertex model catalog using ADC. Since HOME is overridden to
	// tmpDir inside the sandbox, both paths are unreachable without
	// explicit read access. The proxy handles inference auth, but the
	// model catalog pre-check and settings need filesystem access.
	if os.Getenv("CLAUDE_CODE_USE_VERTEX") != "" {
		if home, err := os.UserHomeDir(); err == nil {
			gcloudDir := filepath.Join(home, ".config", "gcloud")
			if _, err := os.Stat(gcloudDir); err == nil {
				read = append(read, gcloudDir)
			}
			claudeDir := filepath.Join(home, ".claude")
			if _, err := os.Stat(claudeDir); err == nil {
				read = append(read, claudeDir)
			}
		}
	}

	return read, nil
}

// MCPExtraPaths returns additional read and write paths required for MCP
// server binaries. Write paths are nil — MCP servers that need temp files
// use tmpDir, which buildSandboxPaths already makes a write path.
func (a *ClaudeAdapter) MCPExtraPaths(servers map[string]runner.MCPServerConfig) (read []string, write []string) {
	return resolveMCPBinaryReadPaths(servers), nil
}

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
			// Resolve the Vertex region. Claude Code validates model
			// availability directly (bypassing the proxy) using
			// CLOUD_ML_REGION.
			region := os.Getenv("CLOUD_ML_REGION")
			if region == "" {
				region = os.Getenv("VERTEXAI_LOCATION")
			}
			if region == "" {
				region = "us-east5"
			}
			for _, key := range []string{"VERTEXAI_PROJECT", "ANTHROPIC_VERTEX_PROJECT_ID"} {
				if val := os.Getenv(key); val != "" {
					env = append(env, key+"="+val)
				}
			}
			env = append(env,
				"VERTEXAI_LOCATION="+region,
				"CLOUD_ML_REGION="+region,
			)
			// Claude Code validates model availability against the Vertex
			// model catalog using ADC. Since HOME is overridden to tmpDir,
			// the default ADC path (~/.config/gcloud/application_default_credentials.json)
			// is unreachable. Point GOOGLE_APPLICATION_CREDENTIALS at the real file.
			if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
				if realHome, err := os.UserHomeDir(); err == nil {
					adcPath := filepath.Join(realHome, ".config", "gcloud", "application_default_credentials.json")
					if _, err := os.Stat(adcPath); err == nil {
						env = append(env, "GOOGLE_APPLICATION_CREDENTIALS="+adcPath)
					}
				}
			} else {
				env = append(env, "GOOGLE_APPLICATION_CREDENTIALS="+os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
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

	// Git/GitHub credentials for submit, follow-up, and monitor phases.
	// gh CLI uses GH_TOKEN or GITHUB_TOKEN; git push uses SSH_AUTH_SOCK.
	gitVars := []string{
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"GH_HOST",
		"SSH_AUTH_SOCK",
		"GIT_AUTHOR_NAME",
		"GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME",
		"GIT_COMMITTER_EMAIL",
	}
	for _, key := range gitVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}

	// If no GH_TOKEN is set, try to extract from gh CLI keyring so that
	// gh commands work inside the sandbox (no keyring access there).
	if os.Getenv("GH_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") == "" {
		if token, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			if t := strings.TrimSpace(string(token)); t != "" {
				env = append(env, "GH_TOKEN="+t)
			}
		}
	}

	return env
}

// mapResult converts a claude.RunResult to a runner.RunResult.
// transcript is computed post-run by FilterTranscript on buffered stdout.
func mapResult(cr *claude.RunResult, transcript []claude.TranscriptEntry) *runner.RunResult {
	return &runner.RunResult{
		Output:        cr.Output,
		RawText:       cr.Result,
		CostUSD:       cr.CostUSD,
		TokensIn:      cr.Tokens.InputTokens,
		TokensOut:     cr.Tokens.OutputTokens,
		CacheTokensIn: cr.Tokens.CacheCreationInputTokens + cr.Tokens.CacheReadInputTokens,
		DurationMs:    cr.Duration.Milliseconds(),
		Turns:         cr.Turns,
		Transcript:    transcript,
	}
}

// mapClaudeParseError wraps claude parse/semantic errors from ParseResponse
// into runner error types. Falls back to runner.ParseError for unrecognized
// error types.
func mapClaudeParseError(err error) error {
	var pe *claude.ParseError
	if errors.As(err, &pe) {
		return &runner.ParseError{Err: pe.Err}
	}
	var se *claude.SemanticError
	if errors.As(err, &se) {
		return &runner.SemanticError{Message: se.Message}
	}
	return &runner.ParseError{Err: fmt.Errorf("sandbox: %w", err)}
}
