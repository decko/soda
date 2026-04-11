package claude

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
