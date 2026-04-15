package runner

import (
	"errors"
	"fmt"
	"testing"
)

func TestTransientError(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	err := &TransientError{
		Reason: "connection",
		Err:    inner,
	}

	if got := err.Error(); got != "runner: transient (connection): connection refused" {
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
		Err: inner,
	}

	if got := err.Error(); got != "runner: parse error: invalid JSON" {
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
	err := &SemanticError{Message: "output incomplete"}

	if got := err.Error(); got != "runner: semantic error: output incomplete" {
		t.Errorf("Error() = %q", got)
	}

	wrapped := fmt.Errorf("phase implement: %w", err)
	var target *SemanticError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As should find SemanticError in chain")
	}
}
