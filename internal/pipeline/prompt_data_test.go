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
	"github.com/decko/soda/schemas"
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

// reviewTemplateNames lists the three review templates for table-driven tests.
var reviewTemplateNames = []string{"review.md", "review-go.md", "review-harness.md"}

// loadReviewTemplate reads a review template from the embedded prompts directory.
func loadReviewTemplate(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	embedDir := filepath.Join(repoRoot, "cmd", "soda", "embeds", "prompts")
	raw, err := os.ReadFile(filepath.Join(embedDir, name))
	if err != nil {
		t.Fatalf("read template %s: %v", name, err)
	}
	return string(raw)
}

// baseReviewData returns a PromptData with enough fields populated to render
// review templates without errors.
func baseReviewData() PromptData {
	return PromptData{
		Ticket: TicketData{Key: "TEST-1", Summary: "Test"},
		Artifacts: ArtifactData{
			Plan:      "plan content",
			Implement: "impl content",
			Verify:    "verify content",
		},
		WorktreePath: "/tmp/test",
		Branch:       "test-branch",
	}
}

func TestReviewTemplates_DiffSection(t *testing.T) {
	for _, name := range reviewTemplateNames {
		name := name
		t.Run(name+"_present", func(t *testing.T) {
			tmpl := loadReviewTemplate(t, name)
			data := baseReviewData()
			data.Diff = "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new"

			rendered, err := RenderPrompt(tmpl, data)
			if err != nil {
				t.Fatalf("RenderPrompt: %v", err)
			}
			if !strings.Contains(rendered, "## Changed Files") {
				t.Error("expected '## Changed Files' section when Diff is non-empty")
			}
			if !strings.Contains(rendered, "+new") {
				t.Error("expected diff content to appear in rendered output")
			}
			if !strings.Contains(rendered, "Focus your review on the changed files") {
				t.Error("expected diff-scoped instruction when Diff is set")
			}
		})
		t.Run(name+"_absent", func(t *testing.T) {
			tmpl := loadReviewTemplate(t, name)
			data := baseReviewData()
			data.Diff = ""

			rendered, err := RenderPrompt(tmpl, data)
			if err != nil {
				t.Fatalf("RenderPrompt: %v", err)
			}
			if strings.Contains(rendered, "## Changed Files") {
				t.Error("'## Changed Files' section should be absent when Diff is empty")
			}
			if !strings.Contains(rendered, "Read the actual code in the worktree") {
				t.Error("expected fallback instruction when Diff is empty")
			}
		})
	}
}

func TestReviewTemplates_PriorFindingsSection(t *testing.T) {
	for _, name := range reviewTemplateNames {
		name := name
		t.Run(name+"_present", func(t *testing.T) {
			tmpl := loadReviewTemplate(t, name)
			data := baseReviewData()
			data.PriorFindings = []schemas.ReviewFinding{
				{Severity: "major", File: "handler.go", Line: 42, Issue: "nil pointer dereference"},
			}

			rendered, err := RenderPrompt(tmpl, data)
			if err != nil {
				t.Fatalf("RenderPrompt: %v", err)
			}
			if !strings.Contains(rendered, "## Previously Addressed Findings") {
				t.Error("expected '## Previously Addressed Findings' section")
			}
			if !strings.Contains(rendered, "nil pointer dereference") {
				t.Error("expected finding issue text in rendered output")
			}
			if !strings.Contains(rendered, "handler.go:42") {
				t.Error("expected file:line in rendered output")
			}
		})
		t.Run(name+"_absent", func(t *testing.T) {
			tmpl := loadReviewTemplate(t, name)
			data := baseReviewData()
			data.PriorFindings = nil

			rendered, err := RenderPrompt(tmpl, data)
			if err != nil {
				t.Fatalf("RenderPrompt: %v", err)
			}
			if strings.Contains(rendered, "## Previously Addressed Findings") {
				t.Error("'## Previously Addressed Findings' should be absent when PriorFindings is nil")
			}
		})
	}
}

func TestReviewTemplates_SeverityDefinitions(t *testing.T) {
	for _, name := range reviewTemplateNames {
		name := name
		t.Run(name, func(t *testing.T) {
			tmpl := loadReviewTemplate(t, name)
			data := baseReviewData()

			rendered, err := RenderPrompt(tmpl, data)
			if err != nil {
				t.Fatalf("RenderPrompt: %v", err)
			}
			if !strings.Contains(rendered, "## Severity definitions") {
				t.Error("expected '## Severity definitions' section")
			}
			for _, word := range []string{"critical", "major", "minor"} {
				if !strings.Contains(rendered, word) {
					t.Errorf("expected severity word %q in rendered output", word)
				}
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
