package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		data, err := engine.buildPromptData(context.Background(), phase)
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
		data, err := engine.buildPromptData(context.Background(), phase)
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
		data, err := engine.buildPromptData(context.Background(), phase)
		if err != nil {
			t.Fatalf("buildPromptData: %v", err)
		}

		if data.VerifyClean {
			t.Error("VerifyClean should be false when no verify result exists")
		}
	})
}

// TestVerifyCleanTemplateGating renders the real embedded review-go.md and
// review-harness.md templates with VerifyClean=true and VerifyClean=false,
// asserting that gated sections are absent/present accordingly. This catches
// logic inversions (e.g. {{if .VerifyClean}} instead of {{if not .VerifyClean}})
// that the unit-level VerifyClean population tests cannot detect.
func TestVerifyCleanTemplateGating(t *testing.T) {
	// Locate the embedded templates relative to this source file.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	embedDir := filepath.Join(repoRoot, "cmd", "soda", "embeds", "prompts")

	cases := []struct {
		template    string
		gatedPhrase string // text that must appear only when VerifyClean=false
	}{
		{"review-go.md", "Test quality and coverage"},
		{"review-harness.md", "Structured output schema alignment"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.template, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(embedDir, tc.template))
			if err != nil {
				t.Fatalf("read template %s: %v", tc.template, err)
			}
			tmpl := string(raw)

			// Provide enough data for the template to render without errors.
			baseData := PromptData{
				Ticket: TicketData{Key: "TEST-1", Summary: "Test"},
				Artifacts: ArtifactData{
					Plan:      "plan content",
					Implement: "impl content",
					Verify:    "verify content",
				},
				WorktreePath: "/tmp/test",
				Branch:       "test-branch",
			}

			// VerifyClean=false → gated section should appear.
			dataFalse := baseData
			dataFalse.VerifyClean = false
			rendered, err := RenderPrompt(tmpl, dataFalse)
			if err != nil {
				t.Fatalf("RenderPrompt(VerifyClean=false): %v", err)
			}
			if !strings.Contains(rendered, tc.gatedPhrase) {
				t.Errorf("VerifyClean=false: expected %q section to be present", tc.gatedPhrase)
			}

			// VerifyClean=true → gated section should be absent.
			dataTrue := baseData
			dataTrue.VerifyClean = true
			rendered, err = RenderPrompt(tmpl, dataTrue)
			if err != nil {
				t.Fatalf("RenderPrompt(VerifyClean=true): %v", err)
			}
			if strings.Contains(rendered, tc.gatedPhrase) {
				t.Errorf("VerifyClean=true: expected %q section to be absent", tc.gatedPhrase)
			}
		})
	}
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
	data, err := engine.buildPromptData(context.Background(), phase)
	if err != nil {
		t.Fatalf("buildPromptData: %v", err)
	}
	if data.Ticket.Key != "TEST-1" {
		t.Errorf("Ticket.Key = %q, want %q", data.Ticket.Key, "TEST-1")
	}

	// Run context (to silence unused import warnings).
	_ = context.Background()
}
