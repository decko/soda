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

// PiAdapter implements AgentAdapter for the Pi coding agent CLI.
type PiAdapter struct {
	binary    string   // resolved absolute path to pi binary
	readPaths []string // read paths needed for pi binary
}

// compile-time interface check
var _ AgentAdapter = (*PiAdapter)(nil)

// NewPiAdapter resolves the pi binary and returns a PiAdapter.
func NewPiAdapter(binary string) (*PiAdapter, error) {
	resolved, readPaths, err := resolvePiPaths(binary)
	if err != nil {
		return nil, err
	}
	return &PiAdapter{
		binary:    resolved,
		readPaths: readPaths,
	}, nil
}

// Binary returns the resolved absolute path to the pi binary.
func (a *PiAdapter) Binary() string {
	return a.binary
}

// BuildArgs writes temporary files (system prompt) into tmpDir
// and returns the CLI argument list for a Pi invocation.
func (a *PiAdapter) BuildArgs(opts runner.RunOpts, tmpDir string) ([]string, error) {
	// Write system prompt to {tmpDir}/.pi/SYSTEM.md to avoid polluting the worktree.
	if opts.SystemPrompt != "" {
		piDir := filepath.Join(tmpDir, ".pi")
		if err := os.MkdirAll(piDir, 0o755); err != nil {
			return nil, fmt.Errorf("sandbox: create .pi directory: %w", err)
		}
		promptPath := filepath.Join(piDir, "SYSTEM.md")
		if err := os.WriteFile(promptPath, []byte(opts.SystemPrompt), 0o644); err != nil {
			return nil, fmt.Errorf("sandbox: write pi system prompt: %w", err)
		}
		// No need for deferred Remove — tmpDir cleanup handles it.
	}

	args := []string{
		"--print",
		"--output-format", "stream-json",
	}

	if opts.OutputSchema != "" {
		args = append(args, "--json-schema", opts.OutputSchema)
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	// Map and add allowed tools.
	for _, tool := range opts.AllowedTools {
		args = append(args, "--allowed-tools", runner.MapPiToolName(tool))
	}

	// Append user prompt as positional arg.
	args = append(args, "-p", opts.UserPrompt)

	return args, nil
}

// BuildEnv returns the environment variables for a sandboxed Pi process.
func (a *PiAdapter) BuildEnv(opts runner.RunOpts, tmpDir string, proxyURL string) []string {
	return piEnv(tmpDir, opts, a.binary, proxyURL)
}

// ParseOutput parses buffered stdout from a Pi invocation into a RunResult.
func (a *PiAdapter) ParseOutput(stdout []byte, opts runner.RunOpts) (*runner.RunResult, error) {
	result, err := runner.ParsePiStream(stdout, nil)
	if err != nil {
		return nil, mapPiParseError(err)
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
	if opts.OutputSchema != "" && len(result.Output) > 0 {
		if valErr := runner.ValidatePiOutput(result.Output, opts.OutputSchema); valErr != nil {
			return nil, mapPiParseError(valErr)
		}
	}

	return runResult, nil
}

// ExtraPaths returns additional read and write paths required by Pi.
func (a *PiAdapter) ExtraPaths(opts runner.RunOpts) (read []string, write []string) {
	read = append(read, a.readPaths...)

	// Allow the sandbox to read the helper script's parent directory.
	if opts.ApiKeyHelper != "" && filepath.IsAbs(opts.ApiKeyHelper) {
		read = append(read, filepath.Dir(opts.ApiKeyHelper))
	}

	return read, nil
}

// resolvePiPaths finds the pi binary and collects paths needed for the
// sandbox read profile.
func resolvePiPaths(binary string) (resolved string, readPaths []string, err error) {
	if binary == "" {
		binary = "pi"
	}

	resolved, err = exec.LookPath(binary)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: pi binary not found: %w", err)
	}

	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: resolve pi symlink: %w", err)
	}

	// The pi binary directory needs read access.
	readPaths = append(readPaths, filepath.Dir(resolved))

	return resolved, readPaths, nil
}

// piEnv builds the environment for a sandboxed Pi process.
// When proxyURL is non-empty, real credentials are NOT passed to the sandbox.
func piEnv(tmpDir string, opts runner.RunOpts, piBin, proxyURL string) []string {
	env := []string{
		"HOME=" + tmpDir,
		"TMPDIR=" + tmpDir,
		"LANG=" + envOrDefault("LANG", "en_US.UTF-8"),
	}

	// PATH: include pi binary dir, standard paths.
	pathDirs := []string{filepath.Dir(piBin)}
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
			"PI_API_KEY",
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

// mapPiParseError wraps Pi errors into runner error types.
// Pi errors from ParsePiStream/ValidatePiOutput are already runner.* types,
// so known types pass through unchanged. Unknown errors are wrapped in
// runner.ParseError.
func mapPiParseError(err error) error {
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
