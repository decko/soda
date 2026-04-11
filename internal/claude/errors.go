package claude

import "fmt"

// TransientError represents a retryable infrastructure failure
// (API timeout, rate limit, process crash, OOM kill).
type TransientError struct {
	Stderr []byte // raw stderr for diagnostics
	Reason string // "rate_limit", "timeout", "oom", "signal", "connection", "overloaded", "unknown"
	Err    error
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("claude: transient (%s): %s", e.Reason, e.Err)
}

func (e *TransientError) Unwrap() error { return e.Err }

// ParseError represents a failure to parse the CLI response.
type ParseError struct {
	Raw []byte // raw output (truncated to 4KB for log readability)
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("claude: parse error: %s", e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// SemanticError represents a logically invalid response
// (subtype "error" from Claude).
type SemanticError struct {
	Message string
}

func (e *SemanticError) Error() string {
	return fmt.Sprintf("claude: semantic error: %s", e.Message)
}
