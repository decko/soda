package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/decko/soda/schemas"
)

func TestTruncateLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		contains string
		notLong  bool
	}{
		{"under_limit", "line1\nline2\nline3", 5, "line1", false},
		{"at_limit", "a\nb\nc", 3, "a\nb\nc", false},
		{"over_limit", "1\n2\n3\n4\n5", 3, "... (truncated)", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateLines(tt.input, tt.max)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("truncateLines() = %q, want to contain %q", got, tt.contains)
			}
			if tt.notLong {
				lines := strings.Split(got, "\n")
				// max lines + 1 for the truncation marker
				if len(lines) > tt.max+1 {
					t.Errorf("truncateLines() has %d lines, want <= %d", len(lines), tt.max+1)
				}
			}
		})
	}
}

func TestExtractReviewFeedback(t *testing.T) {
	t.Run("returns_nil_when_no_review_result", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")

		engine := &Engine{state: state, config: EngineConfig{}}
		if fb := engine.extractReviewFeedback(); fb != nil {
			t.Error("expected nil when no review result exists")
		}
	})

	t.Run("returns_nil_when_verdict_is_pass", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		_ = state.WriteResult("review", json.RawMessage(`{"verdict":"pass","findings":[]}`))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		if fb := engine.extractReviewFeedback(); fb != nil {
			t.Error("expected nil when verdict is pass")
		}
	})

	t.Run("returns_nil_when_verdict_is_pass_with_follow_ups", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		_ = state.WriteResult("review", json.RawMessage(`{"verdict":"pass-with-follow-ups","findings":[{"severity":"minor","issue":"style"}]}`))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		if fb := engine.extractReviewFeedback(); fb != nil {
			t.Error("expected nil when verdict is pass-with-follow-ups")
		}
	})

	t.Run("returns_feedback_when_verdict_is_rework", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		reviewResult := `{
			"verdict": "rework",
			"findings": [
				{"source":"go-specialist","severity":"critical","file":"a.go","line":10,"issue":"nil deref","suggestion":"check nil"},
				{"source":"ai-harness","severity":"major","file":"b.go","line":20,"issue":"missing guard","suggestion":"add guard"},
				{"source":"go-specialist","severity":"minor","file":"c.go","line":30,"issue":"naming","suggestion":"rename"}
			]
		}`
		_ = state.WriteResult("review", json.RawMessage(reviewResult))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractReviewFeedback()
		if fb == nil {
			t.Fatal("expected non-nil feedback for rework verdict")
		}

		if fb.Source != "review" {
			t.Errorf("Source = %q, want %q", fb.Source, "review")
		}
		if fb.Verdict != "rework" {
			t.Errorf("Verdict = %q, want %q", fb.Verdict, "rework")
		}

		// Only critical and major findings should be included (not minor).
		if len(fb.ReviewFindings) != 2 {
			t.Fatalf("ReviewFindings count = %d, want 2", len(fb.ReviewFindings))
		}

		if fb.ReviewFindings[0].Source != "go-specialist" || fb.ReviewFindings[0].Severity != "critical" {
			t.Errorf("first finding = %+v, want go-specialist/critical", fb.ReviewFindings[0])
		}
		if fb.ReviewFindings[1].Source != "ai-harness" || fb.ReviewFindings[1].Severity != "major" {
			t.Errorf("second finding = %+v, want ai-harness/major", fb.ReviewFindings[1])
		}
	})
}

func TestExtractFeedbackFrom(t *testing.T) {
	t.Run("review_source", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		_ = state.WriteResult("review", json.RawMessage(`{"verdict":"rework","findings":[{"severity":"critical","issue":"nil deref"}]}`))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractFeedbackFrom("review")
		if fb == nil {
			t.Fatal("expected non-nil feedback from review source")
		}
		if fb.Source != "review" {
			t.Errorf("Source = %q, want %q", fb.Source, "review")
		}
	})

	t.Run("verify_source", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("verify")
		_ = state.WriteResult("verify", json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix it"]}`))
		_ = state.MarkCompleted("verify")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractFeedbackFrom("verify")
		if fb == nil {
			t.Fatal("expected non-nil feedback from verify source")
		}
		if fb.Source != "verify" {
			t.Errorf("Source = %q, want %q", fb.Source, "verify")
		}
	})

	t.Run("unknown_source_returns_nil", func(t *testing.T) {
		engine := &Engine{config: EngineConfig{}}
		fb := engine.extractFeedbackFrom("unknown")
		if fb != nil {
			t.Errorf("expected nil for unknown source, got %+v", fb)
		}
	})
}

func TestSummarizeReviewFindings(t *testing.T) {
	t.Run("includes_critical_and_major", func(t *testing.T) {
		findings := []schemas.ReviewFinding{
			{Severity: "critical", File: "handler.go", Line: 42, Issue: "nil deref"},
			{Severity: "major", File: "util.go", Issue: "unchecked error"},
			{Severity: "minor", File: "style.go", Issue: "unused import"},
		}
		summary := summarizeReviewFindings(findings)
		if !strings.Contains(summary, "nil deref") {
			t.Errorf("summary should contain critical finding, got: %s", summary)
		}
		if !strings.Contains(summary, "unchecked error") {
			t.Errorf("summary should contain major finding, got: %s", summary)
		}
		if strings.Contains(summary, "unused import") {
			t.Errorf("summary should NOT contain minor finding, got: %s", summary)
		}
	})

	t.Run("empty_when_no_critical_or_major", func(t *testing.T) {
		findings := []schemas.ReviewFinding{
			{Severity: "minor", File: "style.go", Issue: "unused import"},
		}
		if summary := summarizeReviewFindings(findings); summary != "" {
			t.Errorf("summary should be empty for minor-only findings, got: %s", summary)
		}
	})

	t.Run("empty_when_no_findings", func(t *testing.T) {
		if summary := summarizeReviewFindings(nil); summary != "" {
			t.Errorf("summary should be empty for nil findings, got: %s", summary)
		}
	})

	t.Run("includes_line_numbers", func(t *testing.T) {
		findings := []schemas.ReviewFinding{
			{Severity: "critical", File: "x.go", Line: 10, Issue: "bad"},
		}
		summary := summarizeReviewFindings(findings)
		if !strings.Contains(summary, "x.go:10") {
			t.Errorf("summary should contain line number, got: %s", summary)
		}
	})
}

func TestSummarizeVerifyFailures(t *testing.T) {
	t.Run("joins_fixes", func(t *testing.T) {
		fixes := []string{"fix A", "fix B"}
		summary := summarizeVerifyFailures(fixes)
		if summary != "fix A; fix B" {
			t.Errorf("summary = %q, want %q", summary, "fix A; fix B")
		}
	})

	t.Run("empty_when_no_fixes", func(t *testing.T) {
		if summary := summarizeVerifyFailures(nil); summary != "" {
			t.Errorf("summary should be empty for nil fixes, got: %s", summary)
		}
	})
}
