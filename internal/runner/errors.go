package runner

import "fmt"

// TransientError represents a retryable infrastructure failure
// (API timeout, rate limit, process crash, OOM kill, signal death).
// Agent-agnostic: wraps backend-specific errors with a uniform type.
type TransientError struct {
	Reason string // "rate_limit", "timeout", "oom", "signal", "connection", "overloaded", "unknown"
	Err    error
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("runner: transient (%s): %s", e.Reason, e.Err)
}

func (e *TransientError) Unwrap() error { return e.Err }

// ParseError represents a failure to parse the agent response.
// Agent-agnostic: wraps backend-specific parse errors with a uniform type.
type ParseError struct {
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("runner: parse error: %s", e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// SemanticError represents a logically invalid agent response
// (e.g., subtype "error" from Claude, or equivalent from other backends).
type SemanticError struct {
	Message string
}

func (e *SemanticError) Error() string {
	return fmt.Sprintf("runner: semantic error: %s", e.Message)
}
