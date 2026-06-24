package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/decko/soda/internal/runner"
)

// OpencodeAdapter implements AgentAdapter for the Opencode agent CLI.
type OpencodeAdapter struct {
	binary    string   // resolved absolute path to opencode binary
	readPaths []string // read paths needed for opencode binary
}

// compile-time interface check
var _ AgentAdapter = (*OpencodeAdapter)(nil)

// NewOpencodeAdapter resolves the opencode binary and returns an OpencodeAdapter.
func NewOpencodeAdapter(binary string) (*OpencodeAdapter, error) {
	resolved, readPaths, err := resolveOpencodePaths(binary)
	if err != nil {
		return nil, err
	}
	return &OpencodeAdapter{
		binary:    resolved,
		readPaths: readPaths,
	}, nil
}

// Binary returns the resolved absolute path to the opencode binary.
func (a *OpencodeAdapter) Binary() string {
	return a.binary
}

// BuildArgs writes temporary files (agent file) into the workspace directory
// and returns the CLI argument list for an Opencode invocation.
//
// Opencode discovers agent files from {HOME}/.opencode/agent/{name}.md; the
// sandbox overrides HOME to tmpDir, so the agent file is written there. When
// opts.WorkDir is empty (unit tests), tmpDir is used as a safe fallback.
func (a *OpencodeAdapter) BuildArgs(opts runner.RunOpts, tmpDir string) ([]string, error) {
	// Write agent file to {tmpDir}/.opencode/agent/soda-{phase}.md so
	// Opencode can discover it via HOME=tmpDir.
	// When an output schema is provided, append it to the agent file so the
	// model can see the expected output format (opencode has no --json-schema flag).
	agentContent := opts.SystemPrompt
	if opts.OutputSchema != "" {
		schemaSection := "\n\n## Output Schema\n\nYou MUST produce a JSON object conforming to this schema:\n\n```json\n" + opts.OutputSchema + "\n```\n"
		agentContent += schemaSection
	}
	if agentContent != "" {
		promptDir := tmpDir
		phase := opts.Phase
		if phase == "" {
			phase = "default"
		}
		agentDir := filepath.Join(promptDir, ".opencode", "agent")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			return nil, fmt.Errorf("sandbox: create opencode agent directory: %w", err)
		}
		agentName := "soda-" + strings.ReplaceAll(phase, "/", "-")
		agentPath := filepath.Join(agentDir, agentName+".md")
		if err := os.WriteFile(agentPath, []byte(agentContent), 0o644); err != nil {
			return nil, fmt.Errorf("sandbox: write opencode agent file: %w", err)
		}
	}

	// When MCP servers are declared, resolve commands to absolute paths so
	// the agent spawns them without relying on the sandbox's restricted PATH,
	// then write them into {tmpDir}/.opencode.json so Opencode discovers them
	// via HOME=tmpDir. The file is cleaned up when tmpDir is removed, so no
	// explicit cleanup is needed.
	if len(opts.MCPServers) > 0 {
		resolvedServers := resolveMCPCommands(opts.MCPServers)
		_, mcpErr := runner.WriteOpencodeMCPConfig(tmpDir, resolvedServers)
		if mcpErr != nil {
			fmt.Fprintf(os.Stderr, "sandbox: warning: opencode MCP config write failed: %v; continuing without MCP\n", mcpErr)
		}
	}

	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	// Agent name derived from phase.
	phase := opts.Phase
	if phase == "" {
		phase = "default"
	}
	agentName := "soda-" + strings.ReplaceAll(phase, "/", "-")
	args = append(args, "--agent", agentName)

	// Map and add allowed tools.
	mapped := make([]string, 0, len(opts.AllowedTools))
	for _, tool := range opts.AllowedTools {
		mapped = append(mapped, runner.MapOpencodeToolName(tool))
	}
	mapped = runner.DeduplicateTools(mapped)
	if len(mapped) > 0 {
		args = append(args, "--permissions", strings.Join(mapped, ","))
	}

	// Append user prompt as positional arg.
	args = append(args, "-p", opts.UserPrompt)

	return args, nil
}

// BuildEnv returns the environment variables for a sandboxed Opencode process.
func (a *OpencodeAdapter) BuildEnv(opts runner.RunOpts, tmpDir string, proxyURL string) []string {
	return opencodeEnv(tmpDir, opts, a.binary, proxyURL)
}

// ParseOutput parses buffered stdout from an Opencode invocation into a RunResult.
func (a *OpencodeAdapter) ParseOutput(stdout []byte, opts runner.RunOpts) (*runner.RunResult, error) {
	result, err := runner.ParseOpencodeStream(stdout, nil)
	if err != nil {
		return nil, mapOpencodeParseError(err)
	}

	runResult := &runner.RunResult{
		Output:    result.Output,
		RawText:   result.RawText,
		CostUSD:   result.CostUSD,
		TokensIn:  result.TokensIn,
		TokensOut: result.TokensOut,
		Turns:     result.Turns,
	}

	// Validate output against schema if provided.
	if opts.OutputSchema != "" {
		if valErr := runner.ValidateOpencodeOutput(result.Output, opts.OutputSchema); valErr != nil {
			return nil, mapOpencodeParseError(valErr)
		}
	}

	return runResult, nil
}

// ExtraPaths returns additional read and write paths required by Opencode.
func (a *OpencodeAdapter) ExtraPaths(opts runner.RunOpts) (read []string, write []string) {
	read = append(read, a.readPaths...)

	// Allow the sandbox to read the helper script's parent directory.
	if opts.ApiKeyHelper != "" && filepath.IsAbs(opts.ApiKeyHelper) {
		read = append(read, filepath.Dir(opts.ApiKeyHelper))
	}

	return read, nil
}

// MCPExtraPaths returns additional read and write paths required for MCP
// server binaries. Write paths are nil — MCP servers that need temp files
// use tmpDir, which buildSandboxPaths already makes a write path.
func (a *OpencodeAdapter) MCPExtraPaths(servers map[string]runner.MCPServerConfig) (read []string, write []string) {
	return resolveMCPBinaryReadPaths(servers), nil
}

// resolveOpencodePaths finds the opencode binary and collects paths needed for
// the sandbox read profile.
func resolveOpencodePaths(binary string) (resolved string, readPaths []string, err error) {
	if binary == "" {
		binary = "opencode"
	}

	resolved, err = exec.LookPath(binary)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: opencode binary not found: %w", err)
	}

	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: resolve opencode symlink: %w", err)
	}

	// The opencode binary directory needs read access.
	readPaths = append(readPaths, filepath.Dir(resolved))

	return resolved, readPaths, nil
}

// opencodeEnv builds the environment for a sandboxed Opencode process.
// When proxyURL is non-empty, real credentials are NOT passed to the sandbox.
func opencodeEnv(tmpDir string, opts runner.RunOpts, opencodeBin, proxyURL string) []string {
	env := []string{
		"HOME=" + tmpDir,
		"TMPDIR=" + tmpDir,
		"LANG=" + envOrDefault("LANG", "en_US.UTF-8"),
	}

	// PATH: include opencode binary dir, standard paths.
	pathDirs := []string{filepath.Dir(opencodeBin)}
	pathDirs = append(pathDirs, "/usr/local/bin", "/usr/bin", "/bin")
	env = append(env, "PATH="+strings.Join(pathDirs, ":"))

	if proxyURL != "" {
		// Proxy mode: the proxy on the host side handles authentication.
		env = append(env,
			"ANTHROPIC_API_KEY=sk-proxy-nonce",
			"ANTHROPIC_BASE_URL="+proxyURL,
		)
	} else {
		// Direct mode: pass through real credentials.
		credentialVars := []string{
			"ANTHROPIC_API_KEY",
			"OPENCODE_API_KEY",
		}
		for _, key := range credentialVars {
			if val := os.Getenv(key); val != "" {
				env = append(env, key+"="+val)
			}
		}
	}

	// Git/GitHub credentials for submit, follow-up, and monitor phases.
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

	// If no GH_TOKEN is set, try to extract from gh CLI keyring.
	if os.Getenv("GH_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") == "" {
		if token, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			if tok := strings.TrimSpace(string(token)); tok != "" {
				env = append(env, "GH_TOKEN="+tok)
			}
		}
	}

	return env
}

// mapOpencodeParseError wraps Opencode errors into runner error types.
// Opencode errors from ParseOpencodeStream/ValidateOpencodeOutput are already
// runner.* types, so known types pass through unchanged. Unknown errors are
// wrapped in runner.ParseError.
func mapOpencodeParseError(err error) error {
	var pe *runner.ParseError
	if errors.As(err, &pe) {
		return err
	}
	var se *runner.SemanticError
	if errors.As(err, &se) {
		return err
	}
	var te *runner.TransientError
	if errors.As(err, &te) {
		return err
	}
	return &runner.ParseError{Err: fmt.Errorf("sandbox: %w", err)}
}
