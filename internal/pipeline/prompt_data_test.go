package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/decko/soda/internal/runner"
)

func TestBuildPromptData_VerifyClean(t *testing.T) {
	t.Run("true_when_verify_passes", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:    "implement",
				Prompt:  "prompts/implement.md",
				Timeout: Duration{},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"ticket_key":"TEST-1"}`),
						RawText: "done",
					},
				}},
			},
		}

		engine, state := setupEngine(t, phases, mock)

		// Write a verify result with verdict=pass.
		verifyResult := json.RawMessage(`{"verdict":"pass","criteria_results":[]}`)
		if err := state.WriteResult("verify", verifyResult); err != nil {
			t.Fatalf("WriteResult verify: %v", err)
		}

		phase := PhaseConfig{Name: "review", DependsOn: []string{"verify"}}
		data, err := engine.buildPromptData(phase)
		if err != nil {
			t.Fatalf("buildPromptData: %v", err)
		}

		if !data.VerifyClean {
			t.Error("VerifyClean should be true when verify verdict is pass")
		}
	})

	t.Run("false_when_verify_fails", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:    "implement",
				Prompt:  "prompts/implement.md",
				Timeout: Duration{},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"ticket_key":"TEST-1"}`),
						RawText: "done",
					},
				}},
			},
		}

		engine, state := setupEngine(t, phases, mock)

		// Write a verify result with verdict=fail.
		verifyResult := json.RawMessage(`{"verdict":"fail","criteria_results":[]}`)
		if err := state.WriteResult("verify", verifyResult); err != nil {
			t.Fatalf("WriteResult verify: %v", err)
		}

		phase := PhaseConfig{Name: "review", DependsOn: []string{"verify"}}
		data, err := engine.buildPromptData(phase)
		if err != nil {
			t.Fatalf("buildPromptData: %v", err)
		}

		if data.VerifyClean {
			t.Error("VerifyClean should be false when verify verdict is fail")
		}
	})

	t.Run("false_when_no_verify_result", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:    "implement",
				Prompt:  "prompts/implement.md",
				Timeout: Duration{},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"ticket_key":"TEST-1"}`),
						RawText: "done",
					},
				}},
			},
		}

		engine, _ := setupEngine(t, phases, mock)

		phase := PhaseConfig{Name: "review", DependsOn: []string{}}
		data, err := engine.buildPromptData(phase)
		if err != nil {
			t.Fatalf("buildPromptData: %v", err)
		}

		if data.VerifyClean {
			t.Error("VerifyClean should be false when no verify result exists")
		}
	})
}

// Ensure buildPromptData is callable (basic smoke test to prevent import cycle issues).
func TestBuildPromptData_Smoke(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "prompts/triage.md",
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1"}`),
					RawText: "done",
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock)

	phase := PhaseConfig{Name: "triage", DependsOn: []string{}}
	data, err := engine.buildPromptData(phase)
	if err != nil {
		t.Fatalf("buildPromptData: %v", err)
	}
	if data.Ticket.Key != "TEST-1" {
		t.Errorf("Ticket.Key = %q, want %q", data.Ticket.Key, "TEST-1")
	}

	// Run context (to silence unused import warnings).
	_ = context.Background()
}
