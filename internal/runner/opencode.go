package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// OpencodeRunner implements Runner by invoking the Opencode agent CLI.
type OpencodeRunner struct {
	binary string // resolved absolute path to opencode binary
	model  string
}

// compile-time interface check
var _ Runner = (*OpencodeRunner)(nil)

// NewOpencodeRunner creates an OpencodeRunner backed by the Opencode agent CLI.
func NewOpencodeRunner(binary, model, workDir string) (*OpencodeRunner, error) {
	if binary == "" {
		binary = "opencode"
	}

	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("opencode binary not found: %w", err)
	}

	return &OpencodeRunner{
		binary: resolved,
		model:  model,
	}, nil
}

// Run maps runner.RunOpts to Opencode CLI arguments, invokes the CLI, and maps the result back.
func (r *OpencodeRunner) Run(ctx context.Context, opts RunOpts) (*RunResult, error) {
	// Validate WorkDir — must be present and absolute.
	if opts.WorkDir == "" {
		return nil, fmt.Errorf("opencode runner: WorkDir is required")
	}
	if !filepath.IsAbs(opts.WorkDir) {
		return nil, fmt.Errorf("opencode runner: WorkDir must be absolute: %s", opts.WorkDir)
	}

	// Validate output schema.
	if opts.OutputSchema != "" {
		if len(opts.OutputSchema) > 256*1024 {
			return nil, fmt.Errorf("opencode runner: output schema exceeds 256KB limit")
		}
		if !json.Valid([]byte(opts.OutputSchema)) {
			return nil, fmt.Errorf("opencode runner: output schema is not valid JSON")
		}
	}

	// Normalize phase: default to "default" when empty so the agent file name
	// matches the --agent flag produced by buildOpencodeArgs.
	phase := opts.Phase
	if phase == "" {
		phase = "default"
	}

	// Write agent file for system prompt (and output schema, if provided).
	agentContent := opts.SystemPrompt
	if opts.OutputSchema != "" {
		schemaSection := "\n\n## Output Schema\n\nYou MUST produce a JSON object conforming to this schema:\n\n```json\n" + opts.OutputSchema + "\n```\n"
		agentContent += schemaSection
	}
	if agentContent != "" {
		cleanup, err := writeOpencodeAgentFile(opts.WorkDir, phase, agentContent)
		if err != nil {
			return nil, fmt.Errorf("opencode runner: write agent file: %w", err)
		}
		defer cleanup()
	}

	// When MCP servers are declared, write/merge them into .opencode.json.
	if len(opts.MCPServers) > 0 {
		mcpCleanup, mcpErr := WriteOpencodeMCPConfig(opts.WorkDir, opts.MCPServers)
		if mcpErr != nil {
			fmt.Fprintf(os.Stderr, "opencode runner: warning: MCP config write failed: %v; continuing without MCP\n", mcpErr)
		} else {
			defer mcpCleanup()
		}
	}

	args := buildOpencodeArgs(opts, r.model)

	// Apply per-phase timeout.
	if opts.Timeout > 0 {
		phaseDeadline := time.Now().Add(opts.Timeout)
		if existingDeadline, hasDeadline := ctx.Deadline(); !hasDeadline || phaseDeadline.Before(existingDeadline) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
			defer cancel()
		}
	}

	// Budget tracking: cancel if cost exceeds budget.
	budgetCtx := ctx
	var budgetCancel context.CancelFunc
	if opts.MaxBudgetUSD > 0 {
		budgetCtx, budgetCancel = context.WithCancel(ctx)
		defer budgetCancel()
	}

	// Build environment for the opencode process.
	env := buildOpencodeEnv(opts, r.binary)

	cmd := exec.CommandContext(budgetCtx, r.binary, args...)
	cmd.Dir = opts.WorkDir
	cmd.Env = env

	// Stdin: prompt via stdin, or /dev/null if empty.
	if opts.UserPrompt != "" {
		cmd.Stdin = strings.NewReader(opts.UserPrompt)
	}

	// Process group isolation — kill the entire group on cancel.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode runner: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode runner: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, &TransientError{
			Reason: "unknown",
			Err:    fmt.Errorf("opencode runner: start: %w", err),
		}
	}

	// Drain stdout and stderr concurrently.
	var outputBuf piLimitedBuffer
	outputBuf.max = 50 * 1024 * 1024 // 50MB
	var stderrBuf piLimitedBuffer
	stderrBuf.max = 1024 * 1024 // 1MB

	var stdoutErr error
	var wg sync.WaitGroup
	wg.Add(2)

	var costAccumulator float64
	var costMu sync.Mutex
	budgetExceeded := false

	go func() {
		defer wg.Done()
		scanner := newLineScanner(stdout)
		for scanner.Scan() {
			line := scanner.Bytes()
			outputBuf.Write(line)
			outputBuf.Write([]byte("\n"))

			// Budget enforcement: check cost from done events.
			if opts.MaxBudgetUSD > 0 {
				var event OpencodeEvent
				if json.Unmarshal(line, &event) == nil && event.Type == "done" && event.Cost != nil {
					costMu.Lock()
					costAccumulator += *event.Cost
					exceeded := costAccumulator >= opts.MaxBudgetUSD
					costMu.Unlock()
					if exceeded {
						budgetExceeded = true
						if budgetCancel != nil {
							budgetCancel()
						}
					}
				}
			}

			// Forward displayable text to onChunk.
			if opts.OnChunk != nil {
				var event OpencodeEvent
				if json.Unmarshal(line, &event) == nil && event.Type == "text" && event.Content != "" {
					func() {
						defer func() {
							if rec := recover(); rec != nil {
								fmt.Fprintf(os.Stderr, "opencode runner: onChunk panic: %v\n", rec)
							}
						}()
						opts.OnChunk(event.Content)
					}()
				}
			}
		}
		if err := scanner.Err(); err != nil {
			stdoutErr = fmt.Errorf("opencode runner: scan stdout: %w", err)
		}
	}()

	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			readN, readErr := stderr.Read(buf)
			if readN > 0 {
				stderrBuf.Write(buf[:readN])
			}
			if readErr != nil {
				break
			}
		}
	}()

	wg.Wait()

	// Budget exceeded takes precedence over stdout drain errors — we must
	// always revert the worktree when the budget is blown, even if the
	// stdout scanner also failed (e.g., line exceeded 1MB buffer).
	if budgetExceeded {
		revertWorktree(opts.WorkDir)
		cmd.Wait()
		return nil, &TransientError{
			Reason: "budget_exceeded",
			Err:    fmt.Errorf("opencode runner: budget exceeded ($%.2f >= $%.2f)", costAccumulator, opts.MaxBudgetUSD),
		}
	}

	// Check stdout drain error.
	if stdoutErr != nil {
		cmd.Wait()
		return nil, stdoutErr
	}

	waitErr := cmd.Wait()

	if waitErr != nil {
		// Context cancellation — not retryable.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if budgetCtx.Err() != nil {
			revertWorktree(opts.WorkDir)
			return nil, &TransientError{
				Reason: "budget_exceeded",
				Err:    fmt.Errorf("opencode runner: budget exceeded"),
			}
		}

		// Non-zero exit: try parsing stdout first.
		if outputBuf.Len() > 0 && !outputBuf.overflow {
			parsed, parseErr := ParseOpencodeStream(outputBuf.Bytes(), nil)
			if parseErr == nil && len(parsed.Output) > 0 {
				return opencodeStreamToResult(parsed), nil
			}
		}

		// Classify the exit error.
		stderrBytes := stderrBuf.Bytes()
		return nil, classifyOpencodeExitError(waitErr, stderrBytes)
	}

	// Check buffer overflow.
	if outputBuf.overflow {
		return nil, &ParseError{
			Err: fmt.Errorf("opencode runner: stdout exceeded %d byte buffer limit", outputBuf.max),
		}
	}

	parsed, err := ParseOpencodeStream(outputBuf.Bytes(), nil)
	if err != nil {
		return nil, err
	}

	// Validate output against schema.
	if opts.OutputSchema != "" {
		if valErr := ValidateOpencodeOutput(parsed.Output, opts.OutputSchema); valErr != nil {
			return nil, valErr
		}
	}

	return opencodeStreamToResult(parsed), nil
}

// opencodeStreamToResult converts an OpencodeStreamResult to a RunResult.
func opencodeStreamToResult(parsed *OpencodeStreamResult) *RunResult {
	return &RunResult{
		Output:    parsed.Output,
		RawText:   parsed.RawText,
		CostUSD:   parsed.CostUSD,
		TokensIn:  parsed.TokensIn,
		TokensOut: parsed.TokensOut,
		Turns:     parsed.Turns,
	}
}

// buildOpencodeArgs constructs the CLI argument list for an Opencode invocation.
func buildOpencodeArgs(opts RunOpts, defaultModel string) []string {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	}

	// Prefer per-invocation model over runner-level default.
	effectiveModel := defaultModel
	if opts.Model != "" {
		effectiveModel = opts.Model
	}
	if effectiveModel != "" {
		args = append(args, "--model", effectiveModel)
	}

	// Agent name derived from phase; default to "default" when empty so the
	// written agent file is always loaded (matches sandbox adapter behaviour).
	phase := opts.Phase
	if phase == "" {
		phase = "default"
	}
	args = append(args, "--agent", agentName(phase))

	// Map and add allowed tools.
	mapped := make([]string, 0, len(opts.AllowedTools))
	for _, tool := range opts.AllowedTools {
		mapped = append(mapped, MapOpencodeToolName(tool))
	}
	mapped = DeduplicateTools(mapped)
	if len(mapped) > 0 {
		args = append(args, "--permissions", strings.Join(mapped, ","))
	}

	return args
}

// writeOpencodeAgentFile writes the system prompt to
// {workDir}/.opencode/agent/{agentName}.md per Opencode's agent file convention.
//
// If an existing agent file is present it is backed up and restored by the
// returned cleanup function. If no prior file existed, cleanup removes the
// file (and the directories if we created them).
func writeOpencodeAgentFile(workDir, phase, content string) (cleanup func(), err error) {
	if workDir == "" {
		workDir = os.TempDir()
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("opencode: resolve workdir: %w", err)
	}

	ocDir := filepath.Join(abs, ".opencode")
	agentDir := filepath.Join(ocDir, "agent")
	agentFile := filepath.Join(agentDir, agentName(phase)+".md")

	// Track whether parent directories existed so we can clean up.
	_, ocStatErr := os.Stat(ocDir)
	ocDirExisted := ocStatErr == nil

	_, agentStatErr := os.Stat(agentDir)
	agentDirExisted := agentStatErr == nil

	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return nil, fmt.Errorf("opencode: create agent directory: %w", err)
	}

	// Back up existing agent file so we can restore it on cleanup.
	existing, readErr := os.ReadFile(agentFile)
	hadExisting := readErr == nil

	if err := os.WriteFile(agentFile, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("opencode: write agent file: %w", err)
	}

	cleanup = func() {
		if hadExisting {
			_ = os.WriteFile(agentFile, existing, 0o644)
		} else {
			_ = os.Remove(agentFile)
			if !agentDirExisted {
				_ = os.Remove(agentDir)
			}
			if !ocDirExisted {
				_ = os.Remove(ocDir)
			}
		}
	}

	return cleanup, nil
}

// agentName converts a phase name into an opencode agent name.
// Slashes are replaced with dashes (e.g., "review/go-specialist" → "soda-review-go-specialist").
func agentName(phase string) string {
	safe := strings.ReplaceAll(phase, "/", "-")
	return "soda-" + safe
}

// buildOpencodeEnv constructs the environment for a standalone Opencode runner.
// It inherits os.Environ() with HOME and isolation overrides applied.
func buildOpencodeEnv(opts RunOpts, opencodeBin string) []string {
	env := os.Environ()

	// Filter out vars we want to override.
	overrides := map[string]string{
		"HOME": opts.WorkDir,
	}

	var filtered []string
	for _, entry := range env {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		if _, skip := overrides[key]; skip {
			continue
		}
		filtered = append(filtered, entry)
	}

	for key, val := range overrides {
		filtered = append(filtered, key+"="+val)
	}

	return filtered
}
