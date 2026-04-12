package sandbox

import "fmt"

// ExitError represents a sandbox process exit failure.
// The caller maps these to domain errors (e.g., claude.TransientError).
type ExitError struct {
	Code    int    // exit code (0 if killed by signal)
	Signal  int    // signal number (0 if not signaled)
	OOMKill bool   // true if cgroup OOM killer fired
	Stderr  []byte // captured stderr for diagnostics
}

func (e *ExitError) Error() string {
	if e.OOMKill {
		return fmt.Sprintf("sandbox: process OOM killed (signal %d)", e.Signal)
	}
	if e.Signal != 0 {
		return fmt.Sprintf("sandbox: process killed by signal %d", e.Signal)
	}
	return fmt.Sprintf("sandbox: process exited with code %d", e.Code)
}
