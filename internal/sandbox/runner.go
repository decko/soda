//go:build cgo

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/proxy"
	"github.com/decko/soda/internal/runner"
	arapuca "github.com/sergio-correia/go-arapuca"
)

// maxStdoutBytes caps stdout to prevent a runaway response from OOM-killing
// the orchestrator process. 50MB is generous — typical JSON responses are <1MB.
const maxStdoutBytes = 50 * 1024 * 1024

// Runner implements runner.Runner using go-arapuca for OS-level sandboxing.
type Runner struct {
	sandbox    *arapuca.Sandbox
	config     Config
	claudeBin  string   // resolved absolute path to claude binary
	claudeRead []string // read paths needed for claude binary + node
}

// compile-time interface check
var _ runner.Runner = (*Runner)(nil)

// New creates a sandbox runner. The arapuca.Sandbox is created once
// and reused across Run() calls. Call Close() when done.
func New(config Config) (*Runner, error) {
	sb, err := arapuca.New()
	if err != nil {
		return nil, fmt.Errorf("sandbox: create: %w", err)
	}

	claudeBin, claudeRead, err := resolveClaudePaths(config.ClaudeBinary)
	if err != nil {
		sb.Close()
		return nil, err
	}

	return &Runner{
		sandbox:    sb,
		config:     config,
		claudeBin:  claudeBin,
		claudeRead: claudeRead,
	}, nil
}

// Close releases the sandbox. Safe to call multiple times.
func (r *Runner) Close() {
	if r.sandbox != nil {
		r.sandbox.Close()
	}
}

// Run executes a single pipeline phase in the sandbox.
func (r *Runner) Run(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
	if opts.WorkDir == "" {
		return nil, fmt.Errorf("sandbox: WorkDir is required")
	}
	if !filepath.IsAbs(opts.WorkDir) {
		return nil, fmt.Errorf("sandbox: WorkDir must be absolute: %s", opts.WorkDir)
	}

	// Create sandbox temp dir first — used for system prompt file and HOME/TMPDIR.
	tmpPhase := sanitizePhase(opts.Phase)
	tmpDir, err := arapuca.MakeTmpDir(tmpPhase)
	if err != nil {
		return nil, fmt.Errorf("sandbox: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

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
	// Claude Code picks up the apiKeyHelper configuration. Also add the
	// helper script's parent directory to extra read paths so the sandbox
	// allows the CLI to execute it.
	var settingsPath string
	var extraReadForHelper []string
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

		// Allow the sandbox to read the helper script's parent directory.
		extraReadForHelper = append(extraReadForHelper, filepath.Dir(opts.ApiKeyHelper))
	}

	// Build Claude CLI args via exported BuildArgs.
	var budgetPtr *float64
	if opts.MaxBudgetUSD > 0 {
		budgetPtr = &opts.MaxBudgetUSD
	}
	claudeOpts := claude.RunOpts{
		SystemPromptPath: sysPromptPath,
		SettingsPath:     settingsPath,
		OutputSchema:     opts.OutputSchema,
		AllowedTools:     opts.AllowedTools,
		MaxBudgetUSD:     budgetPtr,
		Timeout:          opts.Timeout,
	}
	args := claude.BuildArgs(claudeOpts, opts.Model)

	// Append user prompt as positional arg (stdin workaround — see issue #2 Fix 4).
	args = append(args, "-p", opts.UserPrompt)

	// Build sandbox profile. Include helper script's parent dir in extra read paths.
	// Copy the slice to avoid mutating r.config.ExtraReadPaths across calls.
	combinedExtraRead := append([]string{}, r.config.ExtraReadPaths...)
	combinedExtraRead = append(combinedExtraRead, extraReadForHelper...)

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
				combinedExtraRead = append(combinedExtraRead, gcloudDir)
			}
			claudeDir := filepath.Join(home, ".claude")
			if _, err := os.Stat(claudeDir); err == nil {
				combinedExtraRead = append(combinedExtraRead, claudeDir)
			}
		}
	}
	sp := buildSandboxPaths(opts.WorkDir, tmpDir, r.claudeRead, combinedExtraRead, r.config.ExtraWritePaths)

	useNetNS := r.config.UseNetNS
	var llmProxy *proxy.Proxy
	var proxyBaseURL string

	// Start LLM proxy if configured. The proxy listens on TCP localhost
	// and the sandbox process connects to it via ANTHROPIC_BASE_URL or
	// ANTHROPIC_VERTEX_BASE_URL. Network namespace isolation is deferred
	// until go-arapuca implements the NetworkProxySocket bridge.
	if r.config.Proxy.Enabled {
		proxyCfg := proxy.Config{
			ListenAddr:      "127.0.0.1:0",
			MaxInputTokens:  r.config.Proxy.MaxInputTokens,
			MaxOutputTokens: r.config.Proxy.MaxOutputTokens,
			LogDir:          r.config.Proxy.LogDir,
		}

		if os.Getenv("CLAUDE_CODE_USE_VERTEX") != "" {
			// Vertex mode: resolve upstream URL and token source from ADC.
			// The "global" region is used for billing/routing but doesn't
			// host model endpoints. Fall back to a real region.
			region := os.Getenv("CLOUD_ML_REGION")
			if region == "" || region == "global" {
				region = os.Getenv("VERTEXAI_LOCATION")
			}
			if region == "" || region == "global" {
				region = "us-east5"
			}
			proxyCfg.UpstreamURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", region)

			tokenFunc, tokenErr := proxy.VertexTokenSource("")
			if tokenErr != nil {
				return nil, fmt.Errorf("sandbox: vertex token source: %w", tokenErr)
			}
			proxyCfg.TokenFunc = tokenFunc
		} else {
			// Direct Anthropic mode.
			proxyCfg.APIKey = r.config.Proxy.APIKey
			if proxyCfg.APIKey == "" {
				proxyCfg.APIKey = os.Getenv("ANTHROPIC_API_KEY")
			}
			proxyCfg.UpstreamURL = r.config.Proxy.UpstreamURL
			if proxyCfg.UpstreamURL == "" {
				proxyCfg.UpstreamURL = os.Getenv("ANTHROPIC_BASE_URL")
			}
			if proxyCfg.UpstreamURL == "" {
				proxyCfg.UpstreamURL = "https://api.anthropic.com"
			}
		}

		var proxyErr error
		llmProxy, proxyErr = proxy.New(proxyCfg)
		if proxyErr != nil {
			return nil, fmt.Errorf("sandbox: start LLM proxy: %w", proxyErr)
		}
		defer llmProxy.Close()

		proxyBaseURL = buildProxyURL(llmProxy.Addr().String())
	}

	profile := arapuca.Profile{
		ReadPaths:     sp.ReadPaths,
		WritePaths:    sp.WritePaths,
		MaxMemoryMB:   r.config.MemoryMB,
		MaxCPUPct:     r.config.CPUPercent,
		MaxPIDs:       r.config.MaxPIDs,
		MaxFileSizeMB: r.config.MaxFileSizeMB,
		UseNetNS:      useNetNS,
	}

	// Set up stdout/stderr pipes. Defer closing both ends as safety net
	// for early error paths — closing an already-closed *os.File is harmless.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("sandbox: stdout pipe: %w", err)
	}
	defer stdoutR.Close()
	defer stdoutW.Close()

	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("sandbox: stderr pipe: %w", err)
	}
	defer stderrR.Close()
	defer stderrW.Close()

	cfg := arapuca.Config{
		Profile: profile,
		TaskID:  tmpPhase,
		Phase:   tmpPhase,
		WorkDir: opts.WorkDir,
		Stdout:  stdoutW,
		Stderr:  stderrW,
	}

	// Build env vars for the sandboxed process. go-arapuca v0.1.1+ passes
	// these directly to the subprocess via Config.Env, avoiding the racy
	// setEnvForLaunch pattern that mutated the host process env.
	envSlice := claudeEnv(tmpDir, opts, r.claudeBin, proxyBaseURL)
	envMap := make(map[string]string, len(envSlice))
	for _, entry := range envSlice {
		k, v, _ := parseEnvEntry(entry)
		envMap[k] = v
	}
	cfg.Env = envMap

	proc, launchErr := r.sandbox.Launch(ctx, cfg, r.claudeBin, args, nil)

	if launchErr != nil {
		return nil, fmt.Errorf("sandbox: launch: %w", launchErr)
	}

	// Close write ends promptly so readers get EOF after process exits.
	// The deferred closes above are safety nets for the error path before this point.
	stdoutW.Close()
	stderrW.Close()

	// Drain stdout and stderr concurrently BEFORE Wait() to prevent pipe deadlock.
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var drainWg sync.WaitGroup
	var stdoutErr, stderrErr error

	drainWg.Add(2)
	go func() {
		defer drainWg.Done()
		// Limit stdout to prevent unbounded memory growth.
		lw := &limitWriter{writer: &stdout, remaining: maxStdoutBytes}
		_, stdoutErr = io.Copy(lw, stdoutR)
	}()
	go func() {
		defer drainWg.Done()
		// Limit stderr to 1MB to prevent memory bloat.
		lw := &limitWriter{writer: &stderr, remaining: 1024 * 1024}
		_, stderrErr = io.Copy(lw, stderrR)
	}()

	exitCode, waitErr := proc.Wait()
	drainWg.Wait()

	// Collect OOM count before cleanup.
	oomCount := proc.OOMCount()
	proc.Cleanup()

	_ = stderrErr // stderr drain errors are not actionable

	if stdoutErr != nil {
		return nil, fmt.Errorf("sandbox: drain stdout: %w", stdoutErr)
	}

	// Check for context cancellation.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Check for OOM kill.
	if oomCount > 0 {
		return nil, mapSandboxError(&ExitError{
			Code:    exitCode,
			Signal:  9, // SIGKILL from OOM
			OOMKill: true,
			Stderr:  stderr.Bytes(),
		})
	}

	// Handle non-zero exit or wait error.
	if waitErr != nil {
		// waitErr from arapuca indicates signal kill: "killed by signal N"
		sig := parseSignalFromError(waitErr)
		return nil, mapSandboxError(&ExitError{
			Code:   0,
			Signal: sig,
			Stderr: stderr.Bytes(),
		})
	}

	if exitCode != 0 {
		// Try parsing stdout even with non-zero exit (CLI may exit non-zero with valid output).
		if stdout.Len() > 0 {
			result, parseErr := claude.ParseResponse(stdout.Bytes())
			if parseErr == nil {
				return mapResult(result), nil
			}
		}
		return nil, mapSandboxError(&ExitError{
			Code:   exitCode,
			Stderr: stderr.Bytes(),
		})
	}

	// Parse response.
	result, err := claude.ParseResponse(stdout.Bytes())
	if err != nil {
		return nil, mapClaudeParseError(err)
	}

	return mapResult(result), nil
}

// mapResult converts a claude.RunResult to a runner.RunResult.
func mapResult(cr *claude.RunResult) *runner.RunResult {
	return &runner.RunResult{
		Output:        cr.Output,
		RawText:       cr.Result,
		CostUSD:       cr.CostUSD,
		TokensIn:      cr.Tokens.InputTokens,
		TokensOut:     cr.Tokens.OutputTokens,
		CacheTokensIn: cr.Tokens.CacheCreationInputTokens + cr.Tokens.CacheReadInputTokens,
		DurationMs:    cr.Duration.Milliseconds(),
		Turns:         cr.Turns,
	}
}

// mapSandboxError wraps ExitError into runner.TransientError so the pipeline
// engine can classify sandbox process failures uniformly. OOM kills and
// signal deaths are transient (retryable); non-zero exit codes with no
// signal are also transient with reason "exit_code".
func mapSandboxError(exitErr *ExitError) error {
	if exitErr.OOMKill {
		return &runner.TransientError{
			Reason: "oom",
			Err:    exitErr,
		}
	}
	if exitErr.Signal != 0 {
		return &runner.TransientError{
			Reason: "signal",
			Err:    exitErr,
		}
	}
	return &runner.TransientError{
		Reason: "exit_code",
		Err:    exitErr,
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
