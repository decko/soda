package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

var _ Runner = (*MockRunner)(nil) // compile-time interface check

func TestMockRunner(t *testing.T) {
	t.Run("returns_configured_response", func(t *testing.T) {
		mock := &MockRunner{
			Responses: map[string]*RunResult{
				"triage": {
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "triage done",
					CostUSD: 0.50,
				},
			},
		}

		result, err := mock.Run(context.Background(), RunOpts{Phase: "triage"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.CostUSD != 0.50 {
			t.Errorf("CostUSD = %v, want 0.50", result.CostUSD)
		}
		if len(mock.Calls) != 1 {
			t.Errorf("Calls = %d, want 1", len(mock.Calls))
		}
		if mock.Calls[0].Phase != "triage" {
			t.Errorf("Calls[0].Phase = %q, want %q", mock.Calls[0].Phase, "triage")
		}
	})

	t.Run("returns_configured_error", func(t *testing.T) {
		mock := &MockRunner{
			Errors: map[string]error{
				"plan": fmt.Errorf("test error"),
			},
		}

		_, err := mock.Run(context.Background(), RunOpts{Phase: "plan"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "test error" {
			t.Errorf("error = %q, want %q", err.Error(), "test error")
		}
	})

	t.Run("errors_on_unconfigured_phase", func(t *testing.T) {
		mock := &MockRunner{}

		_, err := mock.Run(context.Background(), RunOpts{Phase: "unknown"})
		if err == nil {
			t.Fatal("expected error for unconfigured phase")
		}
	})

	t.Run("respects_context_cancellation", func(t *testing.T) {
		mock := &MockRunner{
			Responses: map[string]*RunResult{
				"triage": {Output: json.RawMessage(`{}`)},
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := mock.Run(ctx, RunOpts{Phase: "triage"})
		if err == nil {
			t.Fatal("expected context error")
		}
	})
}
