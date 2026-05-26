package runner

import (
	"bufio"
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

// PiRunner implements Runner by invoking the Pi coding agent CLI.
type PiRunner struct {
	binary  string // resolved absolute path to pi binary
	model   string
	workDir string
}

// compile-time interface check
var _ Runner = (*PiRunner)(nil)

// NewPiRunner creates a PiRunner backed by the Pi coding agent CLI.
func NewPiRunner(binary, model, workDir string) (*PiRunner, error) {
	if binary == "" {
		binary = "pi"
	}

	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("pi binary not found: %w", err)
	}

	if !filepath.IsAbs(workDir) {
		return nil, fmt.Errorf("workDir must be absolute: %s", workDir)
	}

	return &PiRunner{
		binary:  resolved,
		model:   model,
		workDir: workDir,
	}, nil
}

// Run maps runner.RunOpts to Pi CLI arguments, invokes the CLI, and maps the result back.
func (r *PiRunner) Run(ctx context.Context, opts RunOpts) (*RunResult, error) {
	// Validate WorkDir — must be present and absolute.
	if opts.WorkDir == "" {
		return nil, fmt.Errorf("pi runner: WorkDir is required")
	}
	if !filepath.IsAbs(opts.WorkDir) {
		return nil, fmt.Errorf("pi runner: WorkDir must be absolute: %s", opts.WorkDir)
	}

	// Validate output schema.
	if opts.OutputSchema != "" {
		if len(opts.OutputSchema) > 256*1024 {
			return nil, fmt.Errorf("pi runner: output schema exceeds 256KB limit")
		}
		if !json.Valid([]byte(opts.OutputSchema)) {
			return nil, fmt.Errorf("pi runner: output schema is not valid JSON")
		}
	}

	// Write system prompt to workspace-level .pi/SYSTEM.md.
	if opts.SystemPrompt != "" {
		cleanup, err := writePiSystemPrompt(opts.WorkDir, opts.SystemPrompt)
		if err != nil {
			return nil, fmt.Errorf("pi runner: write system prompt: %w", err)
		}
		defer cleanup()
	}

	args := buildPiArgs(opts, r.model)

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

	cmd := exec.CommandContext(budgetCtx, r.binary, args...)
	cmd.Dir = opts.WorkDir

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
		return nil, fmt.Errorf("pi runner: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pi runner: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, &TransientError{
			Reason: "unknown",
			Err:    fmt.Errorf("pi runner: start: %w", err),
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

			// Budget enforcement: check cost from message_end events.
			if opts.MaxBudgetUSD > 0 {
				var event PiEvent
				if json.Unmarshal(line, &event) == nil && event.Type == "message_end" && event.Cost != nil {
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
				var event PiEvent
				if json.Unmarshal(line, &event) == nil && event.Type == "assistant" && event.Content != "" {
					func() {
						defer func() {
							if rec := recover(); rec != nil {
								fmt.Fprintf(os.Stderr, "pi runner: onChunk panic: %v\n", rec)
							}
						}()
						opts.OnChunk(event.Content)
					}()
				}
			}
		}
		if err := scanner.Err(); err != nil {
			stdoutErr = fmt.Errorf("pi runner: scan stdout: %w", err)
		}
	}()

	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stderr.Read(buf)
			if n > 0 {
				stderrBuf.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
	}()

	wg.Wait()

	// Check stdout drain error.
	if stdoutErr != nil {
		cmd.Wait()
		return nil, stdoutErr
	}

	waitErr := cmd.Wait()

	// Budget exceeded: revert worktree and return budget error.
	if budgetExceeded {
		revertWorktree(opts.WorkDir)
		return nil, &TransientError{
			Reason: "budget_exceeded",
			Err:    fmt.Errorf("pi runner: budget exceeded ($%.2f >= $%.2f)", costAccumulator, opts.MaxBudgetUSD),
		}
	}

	if waitErr != nil {
		// Context cancellation — not retryable.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if budgetCtx.Err() != nil {
			revertWorktree(opts.WorkDir)
			return nil, &TransientError{
				Reason: "budget_exceeded",
				Err:    fmt.Errorf("pi runner: budget exceeded"),
			}
		}

		// Non-zero exit: try parsing stdout first.
		if outputBuf.Len() > 0 && !outputBuf.overflow {
			parsed, parseErr := ParsePiStream(outputBuf.Bytes(), nil)
			if parseErr == nil && len(parsed.Output) > 0 {
				return piStreamToResult(parsed), nil
			}
		}

		// Classify the exit error.
		stderrBytes := stderrBuf.Bytes()
		return nil, classifyPiExitError(waitErr, stderrBytes)
	}

	// Check buffer overflow.
	if outputBuf.overflow {
		return nil, &ParseError{
			Err: fmt.Errorf("pi runner: stdout exceeded %d byte buffer limit", outputBuf.max),
		}
	}

	parsed, err := ParsePiStream(outputBuf.Bytes(), nil)
	if err != nil {
		return nil, err
	}

	// Validate output against schema.
	if opts.OutputSchema != "" {
		if valErr := ValidatePiOutput(parsed.Output, opts.OutputSchema); valErr != nil {
			return nil, valErr
		}
	}

	return piStreamToResult(parsed), nil
}

// piStreamToResult converts a PiStreamResult to a RunResult.
func piStreamToResult(parsed *PiStreamResult) *RunResult {
	return &RunResult{
		Output:    parsed.Output,
		RawText:   parsed.RawText,
		CostUSD:   parsed.CostUSD,
		TokensIn:  parsed.TokensIn,
		TokensOut: parsed.TokensOut,
		Turns:     parsed.Turns,
	}
}

// buildPiArgs constructs the CLI argument list for a Pi invocation.
func buildPiArgs(opts RunOpts, defaultModel string) []string {
	args := []string{
		"--print",
		"--output-format", "stream-json",
	}

	// Prefer per-invocation model over runner-level default.
	effectiveModel := defaultModel
	if opts.Model != "" {
		effectiveModel = opts.Model
	}
	if effectiveModel != "" {
		args = append(args, "--model", effectiveModel)
	}

	// Map and add allowed tools.
	for _, tool := range opts.AllowedTools {
		args = append(args, "--allowed-tools", MapPiToolName(tool))
	}

	return args
}

// writePiSystemPrompt writes the system prompt to {workDir}/.pi/SYSTEM.md
// per Pi's workspace-level system prompt convention.
//
// If an existing SYSTEM.md is present it is backed up and restored by the
// returned cleanup function. If no prior file existed, cleanup removes the
// file (and the .pi/ directory if we created it).
func writePiSystemPrompt(workDir, content string) (cleanup func(), err error) {
	if workDir == "" {
		workDir = os.TempDir()
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("pi: resolve workdir: %w", err)
	}

	piDir := filepath.Join(abs, ".pi")
	promptPath := filepath.Join(piDir, "SYSTEM.md")

	// Track whether the .pi directory already existed so we can clean up
	// if we created it.
	_, statErr := os.Stat(piDir)
	piDirExisted := statErr == nil

	if err := os.MkdirAll(piDir, 0o755); err != nil {
		return nil, fmt.Errorf("pi: create .pi directory: %w", err)
	}

	// Back up existing SYSTEM.md so we can restore it on cleanup.
	existing, readErr := os.ReadFile(promptPath)
	hadExisting := readErr == nil

	if err := os.WriteFile(promptPath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("pi: write SYSTEM.md: %w", err)
	}

	cleanup = func() {
		if hadExisting {
			// Restore the original file.
			_ = os.WriteFile(promptPath, existing, 0o644)
		} else {
			// Remove the file we created.
			_ = os.Remove(promptPath)
			// Remove the directory only if we created it AND it is now empty.
			if !piDirExisted {
				_ = os.Remove(piDir) // fails silently if non-empty
			}
		}
	}

	return cleanup, nil
}

// classifyPiExitError categorizes a Pi process exit failure.
func classifyPiExitError(waitErr error, stderr []byte) error {
	stderrLower := strings.ToLower(string(stderr))

	patterns := []struct {
		substrings []string
		reason     string
	}{
		{[]string{"rate limit", " 429", "too many requests"}, "rate_limit"},
		{[]string{"timeout", " 504", " 529"}, "timeout"},
		{[]string{"overloaded", " 500", " 502", " 503", "server error", "internal error"}, "overloaded"},
		{[]string{"connection refused", "econnreset", "connection reset"}, "connection"},
	}

	for _, pattern := range patterns {
		for _, sub := range pattern.substrings {
			if strings.Contains(stderrLower, sub) {
				return &TransientError{
					Reason: pattern.reason,
					Err:    fmt.Errorf("pi exited: %w", waitErr),
				}
			}
		}
	}

	return &TransientError{
		Reason: "unknown",
		Err:    fmt.Errorf("pi exited: %w", waitErr),
	}
}

// revertWorktree runs `git checkout -- .` in workDir to discard uncommitted
// changes made by the agent after a budget overrun.
func revertWorktree(workDir string) {
	if workDir == "" {
		return
	}
	cmd := exec.Command("git", "checkout", "--", ".")
	cmd.Dir = workDir
	_ = cmd.Run()
}

// piLimitedBuffer accumulates bytes up to a maximum, then silently discards excess.
// Write always reports success so pipe draining is never blocked.
type piLimitedBuffer struct {
	buf      []byte
	max      int
	overflow bool
}

func (lb *piLimitedBuffer) Write(p []byte) (int, error) {
	if lb.overflow {
		return len(p), nil
	}
	remaining := lb.max - len(lb.buf)
	if len(p) > remaining {
		if remaining > 0 {
			lb.buf = append(lb.buf, p[:remaining]...)
		}
		lb.overflow = true
		return len(p), nil
	}
	lb.buf = append(lb.buf, p...)
	return len(p), nil
}

func (lb *piLimitedBuffer) Bytes() []byte { return lb.buf }
func (lb *piLimitedBuffer) Len() int      { return len(lb.buf) }

// newLineScanner creates a bufio.Scanner configured for JSONL parsing with
// a 1MB max line size.
func newLineScanner(r interface{ Read([]byte) (int, error) }) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return scanner
}
