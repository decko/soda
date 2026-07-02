//go:build cgo

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/decko/soda/internal/proxy"
	"github.com/decko/soda/internal/runner"
	arapuca "github.com/sergio-correia/go-arapuca"
)

// maxStdoutBytes caps stdout to prevent a runaway response from OOM-killing
// the orchestrator process. 50MB is generous — typical JSON responses are <1MB.
const maxStdoutBytes = 50 * 1024 * 1024

// Runner implements runner.Runner using go-arapuca for OS-level sandboxing.
type Runner struct {
	sandbox *arapuca.Sandbox
	config  Config
	adapter AgentAdapter
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

	adapter, err := NewClaudeAdapter(config.ClaudeBinary)
	if err != nil {
		sb.Close()
		return nil, err
	}

	return &Runner{
		sandbox: sb,
		config:  config,
		adapter: adapter,
	}, nil
}

// NewWithAdapter creates a sandbox runner using a caller-provided AgentAdapter.
// This allows the runner-type decision to live in cmd/soda/run.go rather than
// being hardcoded here. Call Close() when done.
func NewWithAdapter(config Config, adapter AgentAdapter) (*Runner, error) {
	sb, err := arapuca.New()
	if err != nil {
		return nil, fmt.Errorf("sandbox: create: %w", err)
	}

	return &Runner{
		sandbox: sb,
		config:  config,
		adapter: adapter,
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

	// Create sandbox temp dir first — used for temp files and HOME/TMPDIR.
	tmpPhase := sanitizePhase(opts.Phase)
	tmpDir, err := arapuca.MakeTmpDir(tmpPhase)
	if err != nil {
		return nil, fmt.Errorf("sandbox: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Build agent CLI args (writes temp files into tmpDir).
	args, err := r.adapter.BuildArgs(opts, tmpDir)
	if err != nil {
		return nil, err
	}

	// Collect agent-specific extra paths and merge with config paths.
	// Copy the config slices to avoid mutating r.config across calls.
	adapterRead, adapterWrite := r.adapter.ExtraPaths(opts)
	combinedExtraRead := append([]string{}, r.config.ExtraReadPaths...)
	combinedExtraRead = append(combinedExtraRead, adapterRead...)
	combinedExtraWrite := append([]string{}, r.config.ExtraWritePaths...)
	combinedExtraWrite = append(combinedExtraWrite, adapterWrite...)

	// Add MCP server binary directories to the read path list so the
	// sandbox can execute them.
	mcpRead, mcpWrite := r.adapter.MCPExtraPaths(opts.MCPServers)
	combinedExtraRead = append(combinedExtraRead, mcpRead...)
	combinedExtraWrite = append(combinedExtraWrite, mcpWrite...)

	sp := buildSandboxPaths(opts.WorkDir, tmpDir, combinedExtraRead, combinedExtraWrite)

	// Disable network isolation when MCP servers are configured — they
	// may need outbound connectivity (e.g. Jira, GitHub APIs).
	useNetNS := effectiveUseNetNS(r.config.UseNetNS, opts.MCPServers)
	if len(opts.MCPServers) > 0 {
		fmt.Fprintf(os.Stderr, "%s", mcpNetworkWarning(opts.Phase))
	}
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
			region := os.Getenv("CLOUD_ML_REGION")
			if region == "" {
				region = os.Getenv("VERTEXAI_LOCATION")
			}
			if region == "" {
				region = "us-east5"
			}
			if region == "global" {
				proxyCfg.UpstreamURL = "https://aiplatform.googleapis.com/v1"
			} else {
				proxyCfg.UpstreamURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", region)
			}

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
		ReadPaths:      sp.ReadPaths,
		WritePaths:     sp.WritePaths,
		MaxMemoryMB:    r.config.MemoryMB,
		MaxCPUPct:      r.config.CPUPercent,
		MaxPIDs:        r.config.MaxPIDs,
		MaxFileSizeMB:  r.config.MaxFileSizeMB,
		UseNetNS:       useNetNS,
		SeccompProfile: arapuca.SeccompProfileBaseline,
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
	envSlice := r.adapter.BuildEnv(opts, tmpDir, proxyBaseURL)
	envMap := make(map[string]string, len(envSlice))
	for _, entry := range envSlice {
		k, v, _ := parseEnvEntry(entry)
		envMap[k] = v
	}
	cfg.Env = envMap

	proc, launchErr := r.sandbox.Launch(ctx, cfg, r.adapter.Binary(), args, nil)

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
			result, parseErr := r.adapter.ParseOutput(stdout.Bytes(), opts)
			if parseErr == nil {
				return result, nil
			}
		}
		return nil, mapSandboxError(&ExitError{
			Code:   exitCode,
			Stderr: stderr.Bytes(),
		})
	}

	// Parse response.
	return r.adapter.ParseOutput(stdout.Bytes(), opts)
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
