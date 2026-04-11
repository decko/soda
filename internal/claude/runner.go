package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Runner holds shared configuration for invoking the Claude Code CLI.
// Safe for concurrent use — Stream() holds no mutable state between calls.
type Runner struct {
	binary  string // resolved absolute path to claude binary
	model   string
	workDir string
	version string // cached claude --version output
}

// NewRunner creates a Runner with validated configuration.
// binary defaults to "claude" if empty. workDir must be absolute.
// Resolves binary via exec.LookPath and caches CLI version.
func NewRunner(binary, model, workDir string) (*Runner, error) {
	if binary == "" {
		binary = "claude"
	}

	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("claude binary not found: %w", err)
	}

	if !filepath.IsAbs(workDir) {
		return nil, fmt.Errorf("workDir must be absolute: %s", workDir)
	}

	// Cache CLI version for diagnostics
	version := "unknown"
	cmd := exec.Command(resolved, "--version")
	if out, err := cmd.Output(); err == nil {
		version = strings.TrimSpace(string(out))
	}

	return &Runner{
		binary:  resolved,
		model:   model,
		workDir: workDir,
		version: version,
	}, nil
}

// limitedBuffer accumulates bytes up to a maximum, then silently discards excess.
// Write always reports success so pipe draining is never blocked.
type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	if lb.overflow {
		return len(p), nil
	}
	remaining := lb.max - lb.buf.Len()
	if len(p) > remaining {
		if remaining > 0 {
			lb.buf.Write(p[:remaining])
		}
		lb.overflow = true
		return len(p), nil
	}
	return lb.buf.Write(p)
}

func (lb *limitedBuffer) Bytes() []byte { return lb.buf.Bytes() }
func (lb *limitedBuffer) Len() int      { return lb.buf.Len() }

// classifyExitError categorizes a process exit failure.
func classifyExitError(exitErr *exec.ExitError, stderr []byte) error {
	// Check for signal-based kill (OOM, external SIGTERM, etc.)
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		sig := status.Signal()
		if sig == syscall.SIGKILL {
			return &TransientError{
				Stderr: stderr,
				Reason: "oom",
				Err:    fmt.Errorf("process killed by signal %s (possible OOM)", sig),
			}
		}
		return &TransientError{
			Stderr: stderr,
			Reason: "signal",
			Err:    fmt.Errorf("process killed by signal %s", sig),
		}
	}

	// Check stderr patterns
	stderrLower := strings.ToLower(string(stderr))

	patterns := []struct {
		substrings []string
		reason     string
	}{
		{[]string{"rate limit", "429"}, "rate_limit"},
		{[]string{"timeout", "504", "529"}, "timeout"},
		{[]string{"overloaded", "500", "502", "503", "server error"}, "overloaded"},
		{[]string{"connection refused", "econnreset", "connection reset"}, "connection"},
	}

	for _, p := range patterns {
		for _, s := range p.substrings {
			if strings.Contains(stderrLower, s) {
				return &TransientError{
					Stderr: stderr,
					Reason: p.reason,
					Err:    fmt.Errorf("claude exited with code %d", exitErr.ExitCode()),
				}
			}
		}
	}

	// Fallback: unknown transient error
	return &TransientError{
		Stderr: stderr,
		Reason: "unknown",
		Err:    fmt.Errorf("claude exited with code %d", exitErr.ExitCode()),
	}
}

// Stream invokes the Claude Code CLI, streams output line-by-line via onChunk,
// and returns the parsed response. Context cancellation kills the subprocess
// and its entire process group.
func (r *Runner) Stream(ctx context.Context, opts RunOpts, onChunk func(string)) (*RunResult, error) {
	// Validate opts
	if opts.SystemPromptPath != "" && !filepath.IsAbs(opts.SystemPromptPath) {
		return nil, fmt.Errorf("claude: system prompt path must be absolute: %s", opts.SystemPromptPath)
	}
	if opts.OutputSchema != "" {
		if len(opts.OutputSchema) > 256*1024 {
			return nil, fmt.Errorf("claude: output schema exceeds 256KB limit")
		}
		if !json.Valid([]byte(opts.OutputSchema)) {
			return nil, fmt.Errorf("claude: output schema is not valid JSON")
		}
	}

	args := buildArgs(opts, r.model)

	// Apply fallback timeout if caller's context has no deadline
	if opts.Timeout > 0 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
			defer cancel()
		}
	}

	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = r.workDir

	// Stdin: prompt via stdin, or /dev/null if empty
	if opts.Prompt != "" {
		cmd.Stdin = strings.NewReader(opts.Prompt)
	}
	// When Prompt is empty, cmd.Stdin stays nil → reads from /dev/null

	// Process group isolation — kill the entire group on cancel.
	// NOTE: Setpgid and syscall.Kill(-pid) are Linux/macOS only.
	// SODA requires Linux (Landlock, seccomp, cgroups), so this is acceptable.
	// Pdeathsig is not set — grandchild processes that setsid may escape group
	// kill. The sandbox layer's cgroup kill is the fallback.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, &TransientError{
			Reason: "unknown",
			Err:    fmt.Errorf("claude: start: %w", err),
		}
	}

	// Drain stdout and stderr concurrently
	var outputBuf limitedBuffer
	outputBuf.max = 50 * 1024 * 1024 // 50MB
	var stderrBuf limitedBuffer
	stderrBuf.max = 1024 * 1024 // 1MB

	var stdoutErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line
		for scanner.Scan() {
			line := scanner.Text()
			outputBuf.Write([]byte(line))
			outputBuf.Write([]byte("\n"))
			if onChunk != nil {
				func() {
					defer func() { recover() }()
					onChunk(line)
				}()
			}
		}
		if err := scanner.Err(); err != nil {
			stdoutErr = fmt.Errorf("claude: scan stdout: %w", err)
		}
	}()

	go func() {
		defer wg.Done()
		io.Copy(&stderrBuf, stderr)
	}()

	wg.Wait()

	// Check stdout drain error
	if stdoutErr != nil {
		cmd.Wait()
		return nil, stdoutErr
	}

	waitErr := cmd.Wait()

	if waitErr != nil {
		// Context cancellation — not retryable
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Non-zero exit: try parsing stdout first (some CLIs exit non-zero with valid output)
		if outputBuf.Len() > 0 {
			result, parseErr := ParseResponse(outputBuf.Bytes())
			if parseErr == nil {
				return result, nil
			}
		}

		// Classify the exit error
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return nil, classifyExitError(exitErr, stderrBuf.Bytes())
		}
		return nil, &TransientError{
			Stderr: stderrBuf.Bytes(),
			Reason: "unknown",
			Err:    fmt.Errorf("claude: wait: %w", waitErr),
		}
	}

	// Check buffer overflow
	if outputBuf.overflow {
		return nil, &ParseError{
			Raw: truncateForLog(outputBuf.Bytes(), 4096),
			Err: fmt.Errorf("stdout exceeded %d byte buffer limit", outputBuf.max),
		}
	}

	return ParseResponse(outputBuf.Bytes())
}

// DryRun returns the command that would be executed, without running it.
// For logging to events.jsonl and debugging.
func (r *Runner) DryRun(opts RunOpts) DryRunResult {
	return DryRunResult{
		Args:   buildArgs(opts, r.model),
		Prompt: opts.Prompt,
	}
}
