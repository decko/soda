//go:build cgo

package sandbox

import (
	"errors"
	"fmt"
	"testing"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/runner"
)

func TestMapSandboxError(t *testing.T) {
	tests := []struct {
		name       string
		exitErr    *ExitError
		wantReason string
	}{
		{
			name:       "oom_kill",
			exitErr:    &ExitError{OOMKill: true, Signal: 9, Stderr: []byte("oom")},
			wantReason: "oom",
		},
		{
			name:       "signal_kill",
			exitErr:    &ExitError{Signal: 15, Stderr: []byte("sigterm")},
			wantReason: "signal",
		},
		{
			name:       "non_zero_exit",
			exitErr:    &ExitError{Code: 1, Stderr: []byte("error")},
			wantReason: "exit_code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapSandboxError(tt.exitErr)

			var runnerTE *runner.TransientError
			if !errors.As(result, &runnerTE) {
				t.Fatalf("expected runner.TransientError, got %T: %v", result, result)
			}
			if runnerTE.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", runnerTE.Reason, tt.wantReason)
			}

			// Original ExitError should be available via Unwrap.
			var origEE *ExitError
			if !errors.As(result, &origEE) {
				t.Error("original ExitError should be in error chain")
			}
		})
	}
}

func TestMapClaudeParseError(t *testing.T) {
	t.Run("claude_parse_error", func(t *testing.T) {
		inner := fmt.Errorf("invalid JSON")
		claudeErr := &claude.ParseError{Raw: []byte("bad"), Err: inner}
		result := mapClaudeParseError(claudeErr)

		var runnerPE *runner.ParseError
		if !errors.As(result, &runnerPE) {
			t.Fatalf("expected runner.ParseError, got %T: %v", result, result)
		}
		if !errors.Is(runnerPE, inner) {
			t.Error("inner error should be preserved")
		}
	})

	t.Run("claude_semantic_error", func(t *testing.T) {
		claudeErr := &claude.SemanticError{Message: "API key invalid"}
		result := mapClaudeParseError(claudeErr)

		var runnerSE *runner.SemanticError
		if !errors.As(result, &runnerSE) {
			t.Fatalf("expected runner.SemanticError, got %T: %v", result, result)
		}
		if runnerSE.Message != "API key invalid" {
			t.Errorf("Message = %q, want %q", runnerSE.Message, "API key invalid")
		}
	})

	t.Run("generic_error_becomes_parse_error", func(t *testing.T) {
		genericErr := fmt.Errorf("something unexpected")
		result := mapClaudeParseError(genericErr)

		var runnerPE *runner.ParseError
		if !errors.As(result, &runnerPE) {
			t.Fatalf("expected runner.ParseError, got %T: %v", result, result)
		}
	})
}
