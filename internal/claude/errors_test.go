package claude

import (
	"errors"
	"fmt"
	"testing"
)

func TestTransientError(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	err := &TransientError{
		Stderr: []byte("connection refused to api.anthropic.com"),
		Reason: "connection",
		Err:    inner,
	}

	if got := err.Error(); got != "claude: transient (connection): connection refused" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}

	// errors.As from a wrapped error
	wrapped := fmt.Errorf("phase triage: %w", err)
	var target *TransientError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As should find TransientError in chain")
	}
	if target.Reason != "connection" {
		t.Errorf("Reason = %q, want %q", target.Reason, "connection")
	}
}

func TestParseError(t *testing.T) {
	inner := fmt.Errorf("invalid JSON")
	err := &ParseError{
		Raw: []byte("not json at all"),
		Err: inner,
	}

	if got := err.Error(); got != "claude: parse error: invalid JSON" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}

	wrapped := fmt.Errorf("phase plan: %w", err)
	var target *ParseError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As should find ParseError in chain")
	}
}

func TestSemanticError(t *testing.T) {
	err := &SemanticError{Message: "API key is invalid"}

	if got := err.Error(); got != "claude: semantic error: API key is invalid" {
		t.Errorf("Error() = %q", got)
	}

	wrapped := fmt.Errorf("phase implement: %w", err)
	var target *SemanticError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As should find SemanticError in chain")
	}
}
