package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
)

func TestEngine_ParallelReview_HappyPath(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:  "review",
			Type:  "parallel-review",
			Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[],"verdict":"pass"}`),
					RawText: "No issues found",
					CostUSD: 0.15,
				},
			}},
			"review/ai-harness": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[],"verdict":"pass"}`),
					RawText: "No issues found",
					CostUSD: 0.10,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Phase should be completed.
	if !state.IsCompleted("review") {
		t.Error("review should be completed")
	}

	// Cost should be accumulated from both reviewers.
	ps := state.Meta().Phases["review"]
	if ps == nil {
		t.Fatal("review phase state missing")
	}
	if !approxEqual(ps.Cost, 0.25) {
		t.Errorf("review cost = %v, want 0.25", ps.Cost)
	}

	// Both reviewers should have been called.
	if len(mock.calls) != 2 {
		t.Errorf("runner called %d times, want 2; phases: %v", len(mock.calls), phaseNames(mock.calls))
	}

	// Verify events: reviewer_started, reviewer_completed, review_merged.
	eventCounts := make(map[string]int)
	for _, e := range events {
		eventCounts[e.Kind]++
	}
	if eventCounts[EventReviewerStarted] != 2 {
		t.Errorf("reviewer_started events = %d, want 2", eventCounts[EventReviewerStarted])
	}
	if eventCounts[EventReviewerCompleted] != 2 {
		t.Errorf("reviewer_completed events = %d, want 2", eventCounts[EventReviewerCompleted])
	}
	if eventCounts[EventReviewMerged] != 1 {
		t.Errorf("review_merged events = %d, want 1", eventCounts[EventReviewMerged])
	}

	// Verify the merged result.
	result, err := state.ReadResult("review")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	var reviewOutput struct {
		TicketKey string `json:"ticket_key"`
		Verdict   string `json:"verdict"`
		Findings  []struct {
			Source string `json:"source"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(result, &reviewOutput); err != nil {
		t.Fatalf("unmarshal review output: %v", err)
	}
	if reviewOutput.TicketKey != "TEST-1" {
		t.Errorf("ticket_key = %q, want %q", reviewOutput.TicketKey, "TEST-1")
	}
	if reviewOutput.Verdict != "pass" {
		t.Errorf("verdict = %q, want %q", reviewOutput.Verdict, "pass")
	}

	// Verify artifact was written.
	artifact, err := state.ReadArtifact("review")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(artifact) == 0 {
		t.Error("review artifact should not be empty")
	}
}

func TestEngine_ParallelReview_PerReviewerModel(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:  "review",
			Type:  "parallel-review",
			Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms", Model: "claude-sonnet-4-6"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"}, // no model — should use global
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{"findings":[],"verdict":"pass"}`),
				},
			}},
			"review/ai-harness": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{"findings":[],"verdict":"pass"}`),
				},
			}},
		},
	}

	engine, _ := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Model = "claude-opus-4-6"
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()

	if len(mock.calls) != 2 {
		t.Fatalf("runner called %d times, want 2", len(mock.calls))
	}

	// Find each reviewer's call by phase name.
	models := map[string]string{}
	for _, c := range mock.calls {
		models[c.Phase] = c.Model
	}

	if models["review/go-specialist"] != "claude-sonnet-4-6" {
		t.Errorf("go-specialist model = %q, want %q", models["review/go-specialist"], "claude-sonnet-4-6")
	}
	if models["review/ai-harness"] != "claude-opus-4-6" {
		t.Errorf("ai-harness model = %q, want %q (global fallback)", models["review/ai-harness"], "claude-opus-4-6")
	}
}

func TestEngine_ParallelReview_MergedFindings(t *testing.T) {
	// When max rework cycles is reached (set to 0), review with
	// critical/major findings should gate with a PhaseGateError.
	phases := []PhaseConfig{
		{
			Name:   "review",
			Type:   "parallel-review",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework: &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	goFindings := `{"findings":[
		{"severity":"critical","file":"handler.go","line":42,"issue":"nil pointer dereference","suggestion":"add nil check"},
		{"severity":"minor","file":"util.go","line":10,"issue":"unused import","suggestion":"remove it"}
	]}`

	harnessFindings := `{"findings":[
		{"severity":"major","file":"prompts/plan.md","line":0,"issue":"missing template guard","suggestion":"add {{if}} block"}
	]}`

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(goFindings),
					RawText: "Found 2 issues",
					CostUSD: 0.20,
				},
			}},
			"review/ai-harness": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(harnessFindings),
					RawText: "Found 1 issue",
					CostUSD: 0.15,
				},
			}},
		},
	}

	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		// Pre-exhaust rework cycles so the gate blocks immediately.
		cfg.MaxReworkCycles = 1
	})
	state.Meta().ReworkCycles = 1

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseGateError for review with critical/major findings at max cycles")
	}

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
	}
	if gateErr.Phase != "review" {
		t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "review")
	}
	if !strings.Contains(gateErr.Reason, "rework") {
		t.Errorf("gate error reason should contain 'rework', got: %q", gateErr.Reason)
	}
	if !strings.Contains(gateErr.Reason, "max cycles") {
		t.Errorf("gate error reason should mention max cycles, got: %q", gateErr.Reason)
	}

	// Verify the merged result contains findings from both reviewers.
	result, err := state.ReadResult("review")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	var reviewOutput struct {
		Verdict  string `json:"verdict"`
		Findings []struct {
			Source   string `json:"source"`
			Severity string `json:"severity"`
			Issue    string `json:"issue"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(result, &reviewOutput); err != nil {
		t.Fatalf("unmarshal review output: %v", err)
	}

	// Should have 3 total findings (2 from go-specialist + 1 from ai-harness).
	if len(reviewOutput.Findings) != 3 {
		t.Errorf("findings count = %d, want 3", len(reviewOutput.Findings))
	}

	// Verdict should be "rework" due to critical/major findings.
	if reviewOutput.Verdict != "rework" {
		t.Errorf("verdict = %q, want %q", reviewOutput.Verdict, "rework")
	}

	// Each finding should track its source reviewer.
	goCount := 0
	harnessCount := 0
	for _, finding := range reviewOutput.Findings {
		switch finding.Source {
		case "go-specialist":
			goCount++
		case "ai-harness":
			harnessCount++
		}
	}
	if goCount != 2 {
		t.Errorf("go-specialist findings = %d, want 2", goCount)
	}
	if harnessCount != 1 {
		t.Errorf("ai-harness findings = %d, want 1", harnessCount)
	}
}

func TestEngine_ParallelReview_MinorOnlyPassWithFollowUps(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:  "review",
			Type:  "parallel-review",
			Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"minor","file":"util.go","line":5,"issue":"could use shorter var name","suggestion":"rename"}]}`),
					RawText: "Minor issue",
					CostUSD: 0.10,
				},
			}},
			"review/ai-harness": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "No issues",
					CostUSD: 0.08,
				},
			}},
		},
	}

	engine, state := setupReviewEngine(t, phases, mock)

	// Should pass (minor issues don't block).
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("review") {
		t.Error("review should be completed")
	}

	result, err := state.ReadResult("review")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	var reviewOutput struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(result, &reviewOutput); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if reviewOutput.Verdict != "pass-with-follow-ups" {
		t.Errorf("verdict = %q, want %q", reviewOutput.Verdict, "pass-with-follow-ups")
	}
}

func TestEngine_ParallelReview_ReviewerFails(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:  "review",
			Type:  "parallel-review",
			Retry: RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "OK",
					CostUSD: 0.10,
				},
			}},
			"review/ai-harness": {{
				err: &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("connection reset")},
			}},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error when a reviewer fails")
	}

	if !strings.Contains(err.Error(), "ai-harness") {
		t.Errorf("error should mention failing reviewer, got: %v", err)
	}

	// Phase should be marked failed.
	ps := state.Meta().Phases["review"]
	if ps == nil {
		t.Fatal("review phase state should exist")
	}
	if ps.Status != PhaseFailed {
		t.Errorf("review status = %q, want %q", ps.Status, PhaseFailed)
	}

	// Should have reviewer_failed event.
	hasReviewerFailed := false
	for _, e := range events {
		if e.Kind == EventReviewerFailed {
			hasReviewerFailed = true
			reviewer, _ := e.Data["reviewer"].(string)
			if reviewer != "ai-harness" {
				t.Errorf("reviewer_failed event for %q, want %q", reviewer, "ai-harness")
			}
		}
	}
	if !hasReviewerFailed {
		t.Error("reviewer_failed event not emitted")
	}
}

func TestEngine_ParallelReview_NoReviewersConfigured(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:      "review",
			Type:      "parallel-review",
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{}, // empty
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{},
	}

	engine, _ := setupReviewEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error for review phase with no reviewers")
	}
	if !strings.Contains(err.Error(), "no reviewers") {
		t.Errorf("error should mention 'no reviewers', got: %v", err)
	}
}

func TestEngine_ParallelReview_DependencyCheck(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				err: &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("fail")},
			}},
		},
	}

	engine, state := setupReviewEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from failed implement")
	}

	// Review should not be completed.
	if state.IsCompleted("review") {
		t.Error("review should NOT be completed when dependency failed")
	}
}

func TestEngine_ParallelReview_CostTrackedPerReviewer(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:  "review",
			Type:  "parallel-review",
			Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "OK",
					CostUSD: 0.30,
				},
			}},
			"review/ai-harness": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "OK",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Total cost should be sum of both reviewers.
	if !approxEqual(state.Meta().TotalCost, 0.50) {
		t.Errorf("TotalCost = %v, want 0.50", state.Meta().TotalCost)
	}

	// Phase cost should also be the sum.
	ps := state.Meta().Phases["review"]
	if ps == nil {
		t.Fatal("review phase state missing")
	}
	if !approxEqual(ps.Cost, 0.50) {
		t.Errorf("review phase cost = %v, want 0.50", ps.Cost)
	}

	// Reviewer_completed events should include per-reviewer cost.
	var goCost, harnessCost float64
	for _, e := range events {
		if e.Kind == EventReviewerCompleted {
			reviewer, _ := e.Data["reviewer"].(string)
			cost, _ := e.Data["cost"].(float64)
			switch reviewer {
			case "go-specialist":
				goCost = cost
			case "ai-harness":
				harnessCost = cost
			}
		}
	}
	if !approxEqual(goCost, 0.30) {
		t.Errorf("go-specialist cost event = %v, want 0.30", goCost)
	}
	if !approxEqual(harnessCost, 0.20) {
		t.Errorf("ai-harness cost event = %v, want 0.20", harnessCost)
	}
}

func TestEngine_ParallelReview_InPipeline(t *testing.T) {
	// Full pipeline with review between verify and submit.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement", "verify"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
		{
			Name:      "submit",
			Prompt:    "submit.md",
			DependsOn: []string{"review"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl done",
					CostUSD: 0.50,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify pass",
					CostUSD: 0.20,
				},
			}},
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "No Go issues",
					CostUSD: 0.15,
				},
			}},
			"review/ai-harness": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "No harness issues",
					CostUSD: 0.10,
				},
			}},
			"submit": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`),
					RawText: "PR created",
					CostUSD: 0.05,
				},
			}},
		},
	}

	engine, state := setupReviewEngine(t, phases, mock)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// All phases should be completed.
	for _, name := range []string{"implement", "verify", "review", "submit"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// Total cost: 0.50 + 0.20 + 0.15 + 0.10 + 0.05 = 1.00
	if !approxEqual(state.Meta().TotalCost, 1.00) {
		t.Errorf("TotalCost = %v, want 1.00", state.Meta().TotalCost)
	}

	// Runner should have been called 4 times (implement, verify, 2 reviewers, submit).
	if len(mock.calls) != 5 {
		t.Errorf("runner called %d times, want 5; phases: %v", len(mock.calls), phaseNames(mock.calls))
	}
}

func TestComputeReviewVerdict(t *testing.T) {
	tests := []struct {
		name     string
		findings []schemas.ReviewFinding
		want     string
	}{
		{
			name:     "no_findings",
			findings: nil,
			want:     "pass",
		},
		{
			name:     "empty_findings",
			findings: []schemas.ReviewFinding{},
			want:     "pass",
		},
		{
			name: "minor_only",
			findings: []schemas.ReviewFinding{
				{Severity: "minor", Issue: "style"},
			},
			want: "pass-with-follow-ups",
		},
		{
			name: "major_triggers_rework",
			findings: []schemas.ReviewFinding{
				{Severity: "major", Issue: "missing error check"},
			},
			want: "rework",
		},
		{
			name: "critical_triggers_rework",
			findings: []schemas.ReviewFinding{
				{Severity: "critical", Issue: "nil deref"},
			},
			want: "rework",
		},
		{
			name: "mixed_severities",
			findings: []schemas.ReviewFinding{
				{Severity: "minor", Issue: "style"},
				{Severity: "major", Issue: "missing error check"},
				{Severity: "minor", Issue: "naming"},
			},
			want: "rework",
		},
		{
			name: "case_insensitive",
			findings: []schemas.ReviewFinding{
				{Severity: "Critical", Issue: "nil deref"},
			},
			want: "rework",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeReviewVerdict(tt.findings)
			if got != tt.want {
				t.Errorf("computeReviewVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEngine_LoadPriorReview(t *testing.T) {
	t.Run("nil_on_first_generation", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name: "review",
				Type: "parallel-review",
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
		}
		engine, _ := setupReviewEngine(t, phases, &flexMockRunner{})

		got := engine.loadPriorReview("review")
		if got != nil {
			t.Errorf("expected nil on first generation, got %v", got)
		}
	})

	t.Run("nil_when_phase_not_started", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name: "review",
				Type: "parallel-review",
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
		}
		engine, _ := setupReviewEngine(t, phases, &flexMockRunner{})

		got := engine.loadPriorReview("nonexistent")
		if got != nil {
			t.Errorf("expected nil for unstarted phase, got %v", got)
		}
	})

	t.Run("returns_prior_review", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name: "review",
				Type: "parallel-review",
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
					{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
				},
			},
		}
		engine, state := setupReviewEngine(t, phases, &flexMockRunner{})

		// Simulate a review phase at generation 2 with an archived generation-1 result.
		state.Meta().Phases["review"] = &PhaseState{Generation: 2}
		prevReview := schemas.ReviewOutput{
			TicketKey: "TEST-1",
			Verdict:   "rework",
			Findings: []schemas.ReviewFinding{
				{Source: "go-specialist", Severity: "critical", File: "x.go", Issue: "nil deref"},
				{Source: "ai-harness", Severity: "minor", File: "p.md", Issue: "style"},
			},
		}
		prevData, _ := json.Marshal(prevReview)
		archivedPath := filepath.Join(state.Dir(), "review.json.1")
		if err := os.WriteFile(archivedPath, prevData, 0644); err != nil {
			t.Fatalf("write archived result: %v", err)
		}

		got := engine.loadPriorReview("review")
		if got == nil {
			t.Fatal("expected non-nil review")
		}
		if len(got.Findings) != 2 {
			t.Errorf("expected 2 findings, got %d", len(got.Findings))
		}
	})
}

func TestNeededReviewersFromPrior(t *testing.T) {
	t.Run("nil_when_no_prior", func(t *testing.T) {
		got := neededReviewersFromPrior(nil)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("returns_critical_sources", func(t *testing.T) {
		prev := &schemas.ReviewOutput{
			Findings: []schemas.ReviewFinding{
				{Source: "go-specialist", Severity: "critical", File: "x.go", Issue: "nil deref"},
				{Source: "ai-harness", Severity: "minor", File: "p.md", Issue: "style"},
			},
		}
		got := neededReviewersFromPrior(prev)
		if got == nil {
			t.Fatal("expected non-nil set")
		}
		if !got["go-specialist"] {
			t.Error("expected go-specialist in critical set")
		}
		if got["ai-harness"] {
			t.Error("ai-harness should NOT be in critical set (only minor findings)")
		}
	})

	t.Run("returns_empty_map_all_minor", func(t *testing.T) {
		prev := &schemas.ReviewOutput{
			Findings: []schemas.ReviewFinding{
				{Source: "go-specialist", Severity: "minor", File: "x.go", Issue: "naming"},
			},
		}
		got := neededReviewersFromPrior(prev)
		if got == nil {
			t.Fatal("expected non-nil set (empty map)")
		}
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})
}

func TestEngine_ParallelReview_SkipsRedundantReviewerOnRework(t *testing.T) {
	// Pipeline: implement → review (with rework → implement).
	// Cycle 1: go-specialist finds critical issue, ai-harness finds no issues.
	// Cycle 2: only go-specialist re-runs; ai-harness is skipped.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review"},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 0.50,
				}},
			},
			// Cycle 1: go-specialist finds critical, ai-harness clean.
			"review/go-specialist": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"x.go","line":1,"issue":"nil deref","suggestion":"add nil check"}]}`),
					RawText: "Critical issue",
					CostUSD: 0.15,
				}},
				// Cycle 2: re-runs and passes.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				}},
			},
			// Cycle 1 only — should NOT be called in cycle 2.
			"review/ai-harness": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "No issues",
					CostUSD: 0.10,
				}},
			},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Both phases should complete.
	for _, name := range []string{"implement", "review"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// Verify runner calls: implement(2) + go-specialist(2) + ai-harness(1) = 5.
	goSpecCalls := 0
	aiHarnessCalls := 0
	for _, call := range mock.calls {
		switch call.Phase {
		case "review/go-specialist":
			goSpecCalls++
		case "review/ai-harness":
			aiHarnessCalls++
		}
	}
	if goSpecCalls != 2 {
		t.Errorf("go-specialist called %d times, want 2 (both cycles)", goSpecCalls)
	}
	if aiHarnessCalls != 1 {
		t.Errorf("ai-harness called %d times, want 1 (skipped in cycle 2)", aiHarnessCalls)
	}

	// Verify reviewer_skipped event was emitted for ai-harness.
	skippedCount := 0
	skippedReviewer := ""
	for _, ev := range events {
		if ev.Kind == EventReviewerSkipped {
			skippedCount++
			skippedReviewer, _ = ev.Data["reviewer"].(string)
		}
	}
	if skippedCount != 1 {
		t.Errorf("reviewer_skipped events = %d, want 1", skippedCount)
	}
	if skippedReviewer != "ai-harness" {
		t.Errorf("skipped reviewer = %q, want %q", skippedReviewer, "ai-harness")
	}

	// Verify rework cycle counter.
	if state.Meta().ReworkCycles != 1 {
		t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
	}
}

func TestEngine_ParallelReview_AllReviewersRunOnFirstCycle(t *testing.T) {
	// On the first review run (no prior generation), all reviewers should execute.
	phases := []PhaseConfig{
		{
			Name:  "review",
			Type:  "parallel-review",
			Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "No issues",
					CostUSD: 0.15,
				},
			}},
			"review/ai-harness": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "No issues",
					CostUSD: 0.10,
				},
			}},
		},
	}

	var events []Event
	engine, _ := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Both reviewers should run.
	if len(mock.calls) != 2 {
		t.Errorf("runner called %d times, want 2", len(mock.calls))
	}

	// No reviewer_skipped events.
	for _, ev := range events {
		if ev.Kind == EventReviewerSkipped {
			t.Error("no reviewer should be skipped on the first cycle")
		}
	}
}

func TestEngine_ParallelReview_AllCriticalReviewersRerun(t *testing.T) {
	// When ALL reviewers had critical findings, none should be skipped on rework.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review"},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 0.50,
				}},
			},
			"review/go-specialist": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"x.go","line":1,"issue":"nil deref","suggestion":"fix"}]}`),
					RawText: "Critical issue",
					CostUSD: 0.15,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				}},
			},
			"review/ai-harness": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"major","file":"p.md","line":0,"issue":"template error","suggestion":"fix template"}]}`),
					RawText: "Major issue",
					CostUSD: 0.15,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				}},
			},
		},
	}

	var events []Event
	engine, _ := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Both reviewers should run in both cycles: 2 implement + 2 go-specialist + 2 ai-harness = 6.
	goSpecCalls := 0
	aiHarnessCalls := 0
	for _, call := range mock.calls {
		switch call.Phase {
		case "review/go-specialist":
			goSpecCalls++
		case "review/ai-harness":
			aiHarnessCalls++
		}
	}
	if goSpecCalls != 2 {
		t.Errorf("go-specialist called %d times, want 2", goSpecCalls)
	}
	if aiHarnessCalls != 2 {
		t.Errorf("ai-harness called %d times, want 2", aiHarnessCalls)
	}

	// No reviewer_skipped events.
	for _, ev := range events {
		if ev.Kind == EventReviewerSkipped {
			t.Error("no reviewer should be skipped when all had critical findings")
		}
	}
}

func TestEngine_ParallelReview_SkipReviewerWithMinorFindings(t *testing.T) {
	// Reviewer with only minor findings should be skipped on rework.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review"},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 0.50,
				}},
			},
			// go-specialist: critical finding in cycle 1 → triggers rework.
			"review/go-specialist": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"x.go","line":1,"issue":"nil deref","suggestion":"fix"}]}`),
					RawText: "Critical issue",
					CostUSD: 0.15,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				}},
			},
			// ai-harness: only minor findings → should be skipped in cycle 2.
			"review/ai-harness": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"minor","file":"p.md","line":0,"issue":"naming","suggestion":"rename"}]}`),
					RawText: "Minor issue",
					CostUSD: 0.10,
				}},
			},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("review") {
		t.Error("review should be completed")
	}

	// ai-harness should be called only once (cycle 1), skipped in cycle 2.
	aiHarnessCalls := 0
	for _, call := range mock.calls {
		if call.Phase == "review/ai-harness" {
			aiHarnessCalls++
		}
	}
	if aiHarnessCalls != 1 {
		t.Errorf("ai-harness called %d times, want 1", aiHarnessCalls)
	}

	// Verify reviewer_skipped event for ai-harness.
	skipped := false
	for _, ev := range events {
		if ev.Kind == EventReviewerSkipped {
			reviewer, _ := ev.Data["reviewer"].(string)
			if reviewer == "ai-harness" {
				skipped = true
			}
		}
	}
	if !skipped {
		t.Error("reviewer_skipped event not emitted for ai-harness")
	}

	// The final review result should include the carried-forward minor finding
	// from ai-harness, producing a "pass-with-follow-ups" verdict.
	raw, err := state.ReadResult("review")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	var reviewOut schemas.ReviewOutput
	if err := json.Unmarshal(raw, &reviewOut); err != nil {
		t.Fatalf("Unmarshal review result: %v", err)
	}
	if reviewOut.Verdict != "pass-with-follow-ups" {
		t.Errorf("verdict = %q, want %q", reviewOut.Verdict, "pass-with-follow-ups")
	}
	if len(reviewOut.Findings) != 1 {
		t.Errorf("findings count = %d, want 1 (the carried minor)", len(reviewOut.Findings))
	} else {
		f := reviewOut.Findings[0]
		if f.Source != "ai-harness" || f.Severity != "minor" {
			t.Errorf("carried finding = %v, want ai-harness minor", f)
		}
	}
}

func TestPriorFindingsForReviewer(t *testing.T) {
	t.Run("nil_when_no_prior", func(t *testing.T) {
		got := priorFindingsForReviewer(nil, "go-specialist")
		if got != nil {
			t.Errorf("expected nil when prior is nil, got %v", got)
		}
	})

	t.Run("returns_findings_for_reviewer", func(t *testing.T) {
		prev := &schemas.ReviewOutput{
			TicketKey: "TEST-1",
			Verdict:   "rework",
			Findings: []schemas.ReviewFinding{
				{Source: "go-specialist", Severity: "critical", File: "x.go", Issue: "nil deref"},
				{Source: "ai-harness", Severity: "minor", File: "p.md", Issue: "naming"},
				{Source: "ai-harness", Severity: "minor", File: "q.md", Issue: "style"},
			},
		}

		got := priorFindingsForReviewer(prev, "ai-harness")
		if len(got) != 2 {
			t.Fatalf("expected 2 findings for ai-harness, got %d", len(got))
		}
		for _, f := range got {
			if f.Source != "ai-harness" {
				t.Errorf("finding source = %q, want %q", f.Source, "ai-harness")
			}
		}

		// go-specialist should only have its own findings.
		goFindings := priorFindingsForReviewer(prev, "go-specialist")
		if len(goFindings) != 1 {
			t.Fatalf("expected 1 finding for go-specialist, got %d", len(goFindings))
		}
	})

	t.Run("nil_for_unknown_reviewer", func(t *testing.T) {
		prev := &schemas.ReviewOutput{
			TicketKey: "TEST-1",
			Verdict:   "rework",
			Findings: []schemas.ReviewFinding{
				{Source: "go-specialist", Severity: "minor", File: "x.go", Issue: "naming"},
			},
		}

		got := priorFindingsForReviewer(prev, "nonexistent")
		if len(got) != 0 {
			t.Errorf("expected no findings for unknown reviewer, got %d", len(got))
		}
	})
}

func TestEngine_ParallelReview_CarriesMinorFindingsOnSkip(t *testing.T) {
	// When a reviewer is skipped on a rework cycle because it had no
	// critical/major findings, its minor findings from the prior cycle
	// should be carried forward into the merged output. This ensures
	// the verdict is "pass-with-follow-ups" (not "pass"), so the
	// post-submit follow-up phase is not skipped.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review"},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 0.50,
				}},
			},
			// go-specialist: critical in cycle 1, clean in cycle 2.
			"review/go-specialist": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"x.go","line":1,"issue":"nil deref","suggestion":"fix"}]}`),
					RawText: "Critical issue",
					CostUSD: 0.15,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				}},
			},
			// ai-harness: minor finding in cycle 1. Skipped in cycle 2, but
			// its minor finding must be carried forward.
			"review/ai-harness": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"minor","file":"p.md","line":5,"issue":"naming convention","suggestion":"use camelCase"}]}`),
					RawText: "Minor issue",
					CostUSD: 0.10,
				}},
			},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("review") {
		t.Fatal("review should be completed")
	}

	// Read final review result.
	raw, err := state.ReadResult("review")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	var reviewOut schemas.ReviewOutput
	if err := json.Unmarshal(raw, &reviewOut); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Verdict MUST be "pass-with-follow-ups" because the carried minor
	// finding should be present. Previously this was incorrectly "pass".
	if reviewOut.Verdict != "pass-with-follow-ups" {
		t.Errorf("verdict = %q, want %q", reviewOut.Verdict, "pass-with-follow-ups")
	}

	// The merged findings should include the carried minor finding.
	if len(reviewOut.Findings) != 1 {
		t.Fatalf("findings count = %d, want 1", len(reviewOut.Findings))
	}
	f := reviewOut.Findings[0]
	if f.Source != "ai-harness" {
		t.Errorf("finding source = %q, want %q", f.Source, "ai-harness")
	}
	if f.Severity != "minor" {
		t.Errorf("finding severity = %q, want %q", f.Severity, "minor")
	}
	if f.Issue != "naming convention" {
		t.Errorf("finding issue = %q, want %q", f.Issue, "naming convention")
	}

	// The reviewer_skipped event should include carried_findings count.
	for _, ev := range events {
		if ev.Kind == EventReviewerSkipped {
			reviewer, _ := ev.Data["reviewer"].(string)
			if reviewer == "ai-harness" {
				carried, _ := ev.Data["carried_findings"].(int)
				if carried != 1 {
					t.Errorf("carried_findings = %d, want 1", carried)
				}
			}
		}
	}
}

func TestEngine_ReviewReworkRouting(t *testing.T) {
	t.Run("rework_routes_back_to_implement", func(t *testing.T) {
		// Pipeline: implement → verify → review → submit
		// Review produces "rework" verdict → engine routes back to implement.
		// Second cycle: review passes → submit proceeds.
		phases := []PhaseConfig{
			{
				Name:         "implement",
				Prompt:       "implement.md",
				Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				FeedbackFrom: []string{"review", "verify"},
			},
			{
				Name:      "verify",
				Prompt:    "verify.md",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "review",
				Type:      "parallel-review",
				DependsOn: []string{"implement", "verify"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Rework:    &ReworkConfig{Target: "implement"},
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
			{
				Name:      "submit",
				Prompt:    "submit.md",
				DependsOn: []string{"review"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				// First implement run.
				"implement": {
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl v1",
						CostUSD: 0.50,
					}},
					// Second implement run (rework).
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
						RawText: "Impl v2 with fixes",
						CostUSD: 0.60,
					}},
				},
				// Verify runs twice (once per cycle).
				"verify": {
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS"}`),
						RawText: "Verify v1",
						CostUSD: 0.15,
					}},
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS"}`),
						RawText: "Verify v2",
						CostUSD: 0.15,
					}},
				},
				// First review: rework.
				"review/go-specialist": {
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"handler.go","line":42,"issue":"nil deref","suggestion":"add nil check"}]}`),
						RawText: "Critical issue found",
						CostUSD: 0.20,
					}},
					// Second review: pass.
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"findings":[]}`),
						RawText: "No issues",
						CostUSD: 0.15,
					}},
				},
				"submit": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`),
						RawText: "PR created",
						CostUSD: 0.05,
					},
				}},
			},
		}

		var events []Event
		engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) {
				events = append(events, e)
			}
		})

		if err := engine.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}

		// All phases should be completed.
		for _, name := range []string{"implement", "verify", "review", "submit"} {
			if !state.IsCompleted(name) {
				t.Errorf("phase %q should be completed", name)
			}
		}

		// Rework cycle counter should be 1.
		if state.Meta().ReworkCycles != 1 {
			t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
		}

		// Should have rework_routed event.
		hasRouted := false
		for _, e := range events {
			if e.Kind == EventReworkRouted {
				hasRouted = true
				routingTo, _ := e.Data["routing_to"].(string)
				if routingTo != "implement" {
					t.Errorf("routing_to = %q, want %q", routingTo, "implement")
				}
			}
		}
		if !hasRouted {
			t.Error("rework_routed event not emitted")
		}

		// Implement should have been called twice (original + rework).
		implCalls := 0
		for _, call := range mock.calls {
			if call.Phase == "implement" {
				implCalls++
			}
		}
		if implCalls != 2 {
			t.Errorf("implement called %d times, want 2", implCalls)
		}
	})

	t.Run("max_rework_cycles_blocks", func(t *testing.T) {
		// Pipeline: implement → review
		// Review always returns "rework", max cycles = 1.
		// First cycle routes back, second cycle gates.
		phases := []PhaseConfig{
			{
				Name:         "implement",
				Prompt:       "implement.md",
				Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				FeedbackFrom: []string{"review"},
			},
			{
				Name:      "review",
				Type:      "parallel-review",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Rework:    &ReworkConfig{Target: "implement"},
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
		}

		reworkFindings := `{"findings":[{"severity":"major","file":"x.go","line":1,"issue":"error not wrapped","suggestion":"use fmt.Errorf"}]}`

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl v1",
						CostUSD: 0.50,
					}},
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
						RawText: "Impl v2",
						CostUSD: 0.50,
					}},
				},
				"review/go-specialist": {
					{result: &runner.RunResult{
						Output:  json.RawMessage(reworkFindings),
						RawText: "Rework needed",
						CostUSD: 0.15,
					}},
					{result: &runner.RunResult{
						Output:  json.RawMessage(reworkFindings),
						RawText: "Still needs rework",
						CostUSD: 0.15,
					}},
				},
			},
		}

		var events []Event
		engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.MaxReworkCycles = 1
			cfg.OnEvent = func(e Event) {
				events = append(events, e)
			}
		})

		err := engine.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError after max rework cycles")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
		}
		if gateErr.Phase != "review" {
			t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "review")
		}
		if !strings.Contains(gateErr.Reason, "max cycles") {
			t.Errorf("gate error should mention max cycles, got: %q", gateErr.Reason)
		}

		// Should have 1 rework cycle.
		if state.Meta().ReworkCycles != 1 {
			t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
		}

		// Should have both routing and max cycles events.
		hasRouted := false
		hasMaxCycles := false
		for _, e := range events {
			if e.Kind == EventReworkRouted {
				hasRouted = true
			}
			if e.Kind == EventReworkMaxCycles {
				hasMaxCycles = true
			}
		}
		if !hasRouted {
			t.Error("rework_routed event not emitted")
		}
		if !hasMaxCycles {
			t.Error("rework_max_cycles event not emitted")
		}
	})

	t.Run("max_rework_cycles_downgrades_minors", func(t *testing.T) {
		// Pipeline: implement → review → submit
		// Uses a regular (non-parallel) review phase where the runner
		// controls the verdict directly. First review returns "rework" with
		// major findings, second review returns "rework" with only minor
		// findings. After max cycles, the engine should downgrade the
		// verdict to "pass-with-follow-ups" and proceed to submit.
		phases := []PhaseConfig{
			{
				Name:         "implement",
				Prompt:       "implement.md",
				Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				FeedbackFrom: []string{"review"},
			},
			{
				Name:      "review",
				Prompt:    "review.md",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Rework:    &ReworkConfig{Target: "implement"},
			},
			{
				Name:      "submit",
				Prompt:    "submit.md",
				DependsOn: []string{"implement", "review"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl v1",
						CostUSD: 0.50,
					}},
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
						RawText: "Impl v2",
						CostUSD: 0.50,
					}},
				},
				"review": {
					// First review: rework with major finding → triggers rework.
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"ticket_key":"TEST-1","findings":[{"severity":"major","file":"x.go","line":1,"issue":"error not wrapped","suggestion":"use fmt.Errorf"}],"verdict":"rework"}`),
						RawText: "Major issues found",
						CostUSD: 0.15,
					}},
					// Second review: rework with only minor findings → downgraded.
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"ticket_key":"TEST-1","findings":[{"severity":"minor","file":"x.go","line":5,"issue":"naming style","suggestion":"use camelCase"}],"verdict":"rework"}`),
						RawText: "Minor issue only",
						CostUSD: 0.15,
					}},
				},
				"submit": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/42"}`),
						RawText: "PR created",
						CostUSD: 0.05,
					},
				}},
			},
		}

		var events []Event
		engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.MaxReworkCycles = 1
			cfg.OnEvent = func(e Event) {
				events = append(events, e)
			}
		})

		err := engine.Run(context.Background())
		if err != nil {
			t.Fatalf("expected pipeline to proceed after downgrade, got: %v", err)
		}

		// All phases should be completed.
		for _, name := range []string{"implement", "review", "submit"} {
			if !state.IsCompleted(name) {
				t.Errorf("phase %q should be completed", name)
			}
		}

		// Should have 1 rework cycle.
		if state.Meta().ReworkCycles != 1 {
			t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
		}

		// Review result should have been rewritten with pass-with-follow-ups.
		result, err := state.ReadResult("review")
		if err != nil {
			t.Fatalf("ReadResult: %v", err)
		}
		var reviewOutput schemas.ReviewOutput
		if err := json.Unmarshal(result, &reviewOutput); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if reviewOutput.Verdict != "pass-with-follow-ups" {
			t.Errorf("verdict = %q, want %q", reviewOutput.Verdict, "pass-with-follow-ups")
		}
		// Findings should still be present.
		if len(reviewOutput.Findings) != 1 {
			t.Errorf("findings count = %d, want 1", len(reviewOutput.Findings))
		}

		// Should have rework_routed, rework_max_cycles, and rework_minors_downgraded events.
		hasRouted := false
		hasDowngraded := false
		hasMaxCycles := false
		for _, e := range events {
			if e.Kind == EventReworkRouted {
				hasRouted = true
			}
			if e.Kind == EventReworkMinorsDowngraded {
				hasDowngraded = true
				if orig, _ := e.Data["original_verdict"].(string); orig != "rework" {
					t.Errorf("original_verdict = %q, want %q", orig, "rework")
				}
				if newV, _ := e.Data["new_verdict"].(string); newV != "pass-with-follow-ups" {
					t.Errorf("new_verdict = %q, want %q", newV, "pass-with-follow-ups")
				}
			}
			if e.Kind == EventReworkMaxCycles {
				hasMaxCycles = true
			}
		}
		if !hasRouted {
			t.Error("rework_routed event not emitted")
		}
		if !hasDowngraded {
			t.Error("rework_minors_downgraded event not emitted")
		}
		if !hasMaxCycles {
			t.Error("rework_max_cycles event not emitted")
		}
	})

	t.Run("max_rework_cycles_blocks_with_mixed_findings", func(t *testing.T) {
		// Pipeline: implement → review
		// Uses a regular (non-parallel) review where the runner controls
		// the verdict. Both reviews return "rework" with major+minor findings.
		// The engine should block because critical/major findings remain.
		phases := []PhaseConfig{
			{
				Name:         "implement",
				Prompt:       "implement.md",
				Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				FeedbackFrom: []string{"review"},
			},
			{
				Name:      "review",
				Prompt:    "review.md",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Rework:    &ReworkConfig{Target: "implement"},
			},
		}

		mixedFindings := `{"ticket_key":"TEST-1","findings":[{"severity":"major","file":"x.go","line":1,"issue":"error not wrapped","suggestion":"use fmt.Errorf"},{"severity":"minor","file":"y.go","line":10,"issue":"naming style","suggestion":"rename"}],"verdict":"rework"}`

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl v1",
						CostUSD: 0.50,
					}},
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
						RawText: "Impl v2",
						CostUSD: 0.50,
					}},
				},
				"review": {
					{result: &runner.RunResult{
						Output:  json.RawMessage(mixedFindings),
						RawText: "Mixed issues",
						CostUSD: 0.15,
					}},
					{result: &runner.RunResult{
						Output:  json.RawMessage(mixedFindings),
						RawText: "Still mixed issues",
						CostUSD: 0.15,
					}},
				},
			},
		}

		engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.MaxReworkCycles = 1
		})

		err := engine.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError after max rework cycles with major findings")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
		}
		if gateErr.Phase != "review" {
			t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "review")
		}
		if !strings.Contains(gateErr.Reason, "max cycles") {
			t.Errorf("gate error should mention max cycles, got: %q", gateErr.Reason)
		}
		if !strings.Contains(gateErr.Reason, "error not wrapped") {
			t.Errorf("gate error should mention the major issue, got: %q", gateErr.Reason)
		}

		// Should have 1 rework cycle.
		if state.Meta().ReworkCycles != 1 {
			t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
		}
	})

	t.Run("pass_with_follow_ups_proceeds", func(t *testing.T) {
		// Minor-only findings should not block and should proceed to submit.
		phases := []PhaseConfig{
			{
				Name:   "implement",
				Prompt: "implement.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "review",
				Type:      "parallel-review",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Rework:    &ReworkConfig{Target: "implement"},
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
			{
				Name:      "submit",
				Prompt:    "submit.md",
				DependsOn: []string{"review"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl done",
						CostUSD: 0.50,
					},
				}},
				"review/go-specialist": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"findings":[{"severity":"minor","file":"util.go","issue":"naming style","suggestion":"rename var"}]}`),
						RawText: "Minor issues only",
						CostUSD: 0.10,
					},
				}},
				"submit": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`),
						RawText: "PR created",
						CostUSD: 0.05,
					},
				}},
			},
		}

		engine, state := setupReviewEngine(t, phases, mock)

		if err := engine.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}

		// All phases including submit should complete.
		for _, name := range []string{"implement", "review", "submit"} {
			if !state.IsCompleted(name) {
				t.Errorf("phase %q should be completed", name)
			}
		}

		// No rework cycles should have occurred.
		if state.Meta().ReworkCycles != 0 {
			t.Errorf("ReworkCycles = %d, want 0", state.Meta().ReworkCycles)
		}

		// Verify verdict is pass-with-follow-ups.
		result, err := state.ReadResult("review")
		if err != nil {
			t.Fatalf("ReadResult: %v", err)
		}
		var reviewOutput struct {
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal(result, &reviewOutput); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if reviewOutput.Verdict != "pass-with-follow-ups" {
			t.Errorf("verdict = %q, want %q", reviewOutput.Verdict, "pass-with-follow-ups")
		}
	})

	t.Run("no_findings_passes", func(t *testing.T) {
		// No findings → proceed to submit without rework.
		phases := []PhaseConfig{
			{
				Name:   "implement",
				Prompt: "implement.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "review",
				Type:      "parallel-review",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Rework:    &ReworkConfig{Target: "implement"},
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
			{
				Name:      "submit",
				Prompt:    "submit.md",
				DependsOn: []string{"review"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl done",
						CostUSD: 0.50,
					},
				}},
				"review/go-specialist": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"findings":[]}`),
						RawText: "No issues",
						CostUSD: 0.10,
					},
				}},
				"submit": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`),
						RawText: "PR created",
						CostUSD: 0.05,
					},
				}},
			},
		}

		engine, state := setupReviewEngine(t, phases, mock)

		if err := engine.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}

		for _, name := range []string{"implement", "review", "submit"} {
			if !state.IsCompleted(name) {
				t.Errorf("phase %q should be completed", name)
			}
		}

		if state.Meta().ReworkCycles != 0 {
			t.Errorf("ReworkCycles = %d, want 0", state.Meta().ReworkCycles)
		}
	})

	t.Run("invalid_target_does_not_mutate_state", func(t *testing.T) {
		// When the rework target refers to a phase that doesn't exist in
		// the pipeline, routeRework should return an error WITHOUT
		// incrementing ReworkCycles or emitting a routed event.
		phases := []PhaseConfig{
			{
				Name:   "implement",
				Prompt: "implement.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "review",
				Type:      "parallel-review",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Rework:    &ReworkConfig{Target: "nonexistent-phase"},
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl v1",
						CostUSD: 0.50,
					},
				}},
				"review/go-specialist": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"x.go","line":1,"issue":"bug","suggestion":"fix it"}]}`),
						RawText: "Critical issue",
						CostUSD: 0.15,
					},
				}},
			},
		}

		var events []Event
		engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) {
				events = append(events, e)
			}
		})

		err := engine.Run(context.Background())
		if err == nil {
			t.Fatal("expected error for invalid rework target")
		}
		if !strings.Contains(err.Error(), "nonexistent-phase") {
			t.Errorf("error should mention invalid target, got: %v", err)
		}

		// ReworkCycles must NOT have been incremented.
		if state.Meta().ReworkCycles != 0 {
			t.Errorf("ReworkCycles = %d, want 0 (state mutated before validation)", state.Meta().ReworkCycles)
		}

		// No review_rework_routed event should have been emitted.
		for _, ev := range events {
			if ev.Kind == EventReworkRouted {
				t.Error("review_rework_routed event should not be emitted for invalid target")
			}
		}
	})
}

func TestEngine_ReviewReworkFeedbackInjected(t *testing.T) {
	// When review rework routes back to implement, the implement prompt
	// should contain the review findings.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				{Name: "ai-harness", Prompt: "prompts/review-harness.md", Focus: "AI harness"},
			},
		},
	}

	goFindings := `{"findings":[
		{"severity":"critical","file":"handler.go","line":42,"issue":"nil pointer dereference","suggestion":"add nil check"}
	]}`

	harnessFindings := `{"findings":[
		{"severity":"major","file":"prompts/plan.md","line":0,"issue":"missing template guard","suggestion":"add if block"}
	]}`

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 0.60,
				}},
			},
			"review/go-specialist": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(goFindings),
					RawText: "Critical issue",
					CostUSD: 0.15,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				}},
			},
			"review/ai-harness": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(harnessFindings),
					RawText: "Major issue",
					CostUSD: 0.15,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				}},
			},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Write a prompt template that renders review rework feedback.
	implTmpl := `Phase: implement
Ticket: {{.Ticket.Key}}
{{- if .ReworkFeedback}}
REWORK_SOURCE: {{.ReworkFeedback.Source}}
REWORK_VERDICT: {{.ReworkFeedback.Verdict}}
{{- range .ReworkFeedback.ReviewFindings}}
FINDING: {{.Source}} {{.Severity}} {{.File}}:{{.Line}} {{.Issue}} -> {{.Suggestion}}
{{- end}}
{{- end}}
`
	for _, name := range []string{"implement.md", "prompts/review-go.md", "prompts/review-harness.md"} {
		tmplPath := filepath.Join(promptDir, name)
		if err := os.MkdirAll(filepath.Dir(tmplPath), 0755); err != nil {
			t.Fatal(err)
		}
		content := implTmpl
		if strings.Contains(name, "review") {
			content = fmt.Sprintf("Reviewer: %s\nTicket: {{.Ticket.Key}}\n", name)
		}
		if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	state, err := LoadOrCreate(stateDir, "REVFB-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "REVFB-1", Summary: "Review feedback test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Find the second implement call (the rework run).
	implCalls := 0
	var reworkPrompt string
	for _, call := range mock.calls {
		if call.Phase == "implement" {
			implCalls++
			if implCalls == 2 {
				reworkPrompt = call.SystemPrompt
			}
		}
	}
	if implCalls != 2 {
		t.Fatalf("implement called %d times, want 2", implCalls)
	}

	// The rework prompt should contain review findings.
	if !strings.Contains(reworkPrompt, "REWORK_SOURCE: review") {
		t.Errorf("rework prompt should contain REWORK_SOURCE: review;\ngot: %s", reworkPrompt)
	}
	if !strings.Contains(reworkPrompt, "REWORK_VERDICT: rework") {
		t.Errorf("rework prompt should contain REWORK_VERDICT: rework;\ngot: %s", reworkPrompt)
	}
	if !strings.Contains(reworkPrompt, "FINDING: go-specialist critical handler.go:42 nil pointer dereference -> add nil check") {
		t.Errorf("rework prompt should contain go-specialist finding;\ngot: %s", reworkPrompt)
	}
	if !strings.Contains(reworkPrompt, "FINDING: ai-harness major prompts/plan.md:0 missing template guard -> add if block") {
		t.Errorf("rework prompt should contain ai-harness finding;\ngot: %s", reworkPrompt)
	}

	// Should have rework_feedback_injected event.
	hasInjection := false
	for _, e := range events {
		if e.Kind == EventReworkFeedbackInjected {
			hasInjection = true
			source, _ := e.Data["source"].(string)
			if source != "review" {
				t.Errorf("injection event source = %q, want %q", source, "review")
			}
		}
	}
	if !hasInjection {
		t.Error("rework_feedback_injected event not emitted for review rework")
	}
}

func TestEngine_SkippedReviewPhaseReworkSignalRoutesToImplement(t *testing.T) {
	// Scenario: review phase completed with "rework" verdict in a prior run.
	// On re-run, review is skipped (deps unchanged), but its stored gate
	// result still contains the rework verdict. The engine should handle the
	// reworkSignal by routing back to implement, NOT returning a
	// terminal error.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement", "verify"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
			},
		},
		{
			Name:      "submit",
			Prompt:    "submit.md",
			DependsOn: []string{"review"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	// First run: all phases complete, review returns rework → routed,
	// second pass review returns pass → submit.
	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				}},
				// Rework cycle implement.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 0.50,
				}},
			},
			"verify": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v1",
					CostUSD: 0.10,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v2",
					CostUSD: 0.10,
				}},
			},
			"review/go-specialist": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"x.go","line":1,"issue":"nil deref","suggestion":"add nil check"}]}`),
					RawText: "Critical issue",
					CostUSD: 0.15,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				}},
			},
			"submit": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`),
					RawText: "PR created",
					CostUSD: 0.05,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Sanity: all phases completed, rework cycle = 1.
	for _, name := range []string{"implement", "verify", "review", "submit"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed after first run", name)
		}
	}
	if state.Meta().ReworkCycles != 1 {
		t.Fatalf("ReworkCycles after first run = %d, want 1", state.Meta().ReworkCycles)
	}

	// --- Second run with the same state ---
	// Overwrite review result back to "rework" verdict to simulate stale
	// state from a prior incomplete rework cycle.
	reworkResult := json.RawMessage(`{"verdict":"rework","findings":[{"severity":"major","file":"y.go","line":5,"issue":"missing error check","suggestion":"handle err"}]}`)
	if err := state.WriteResult("review", reworkResult); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}

	// Set up mock for the second run: implement, verify, review, submit
	// will all need to run again due to the rework routing.
	mock2 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":3}`),
					RawText: "Impl v3",
					CostUSD: 0.50,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v3",
					CostUSD: 0.10,
				},
			}},
			"review/go-specialist": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "All clear",
					CostUSD: 0.10,
				},
			}},
			"submit": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/2"}`),
					RawText: "PR created v2",
					CostUSD: 0.05,
				},
			}},
		},
	}

	events = nil
	engine2 := NewEngine(mock2, state, engine.config)
	engine2.config.OnEvent = func(e Event) {
		events = append(events, e)
	}

	// Run() should: skip implement (deps unchanged) → skip verify (deps
	// unchanged) → skip-gate review → detect rework signal → route to
	// implement → re-run implement, verify, review, submit.
	if err := engine2.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	// ReworkCycles should have incremented.
	if state.Meta().ReworkCycles != 2 {
		t.Errorf("ReworkCycles after second run = %d, want 2", state.Meta().ReworkCycles)
	}

	// Should have emitted a rework_routed event.
	hasRouted := false
	for _, e := range events {
		if e.Kind == EventReworkRouted {
			hasRouted = true
			break
		}
	}
	if !hasRouted {
		t.Error("rework_routed event not emitted on skipped-phase gate path")
	}

	// Implement should have been called in the second run (via rework routing).
	implCalls := 0
	for _, call := range mock2.calls {
		if call.Phase == "implement" {
			implCalls++
		}
	}
	if implCalls != 1 {
		t.Errorf("implement called %d times in second run, want 1", implCalls)
	}

	// All phases should be completed.
	for _, name := range []string{"implement", "verify", "review", "submit"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed after second run", name)
		}
	}
}

func TestEngine_FollowUpPhase_RunsOnMinorFindings(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "review", Type: "parallel-review", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Reviewers: []ReviewerConfig{
				{Name: "test-reviewer", Prompt: "prompts/review-test.md", Focus: "test"},
			},
		},
		{Name: "submit", Prompt: "prompts/submit.md", DependsOn: []string{"review"}},
		{Name: "follow-up", Type: "post-submit", Prompt: "prompts/follow-up.md", DependsOn: []string{"review", "submit"}, Tools: []string{"Bash(gh:*)"}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review/test-reviewer": {{result: &runner.RunResult{
				Output: json.RawMessage(`{"ticket_key":"TEST-1","findings":[{"severity":"minor","file":"main.go","issue":"nit","suggestion":"fix","source":"test-reviewer"}],"verdict":"pass-with-follow-ups"}`),
			}}},
			"submit": {{result: &runner.RunResult{
				Output: json.RawMessage(`{"ticket_key":"TEST-1","pr_url":"https://github.com/test/repo/pull/1","pr_number":1,"title":"test","branch":"test","target":"main","forge":"github"}`),
			}}},
			"follow-up": {{result: &runner.RunResult{
				Output: json.RawMessage(`{"ticket_key":"TEST-1","actions":[{"finding":"nit","action":"created","ticket_url":"https://github.com/test/repo/issues/99","ticket_number":99}]}`),
			}}},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) { events = append(events, e) }
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("follow-up") {
		t.Error("follow-up should be completed")
	}

	// Verify runner was called for follow-up.
	mock.mu.Lock()
	var followUpCalled bool
	for _, c := range mock.calls {
		if c.Phase == "follow-up" {
			followUpCalled = true
		}
	}
	mock.mu.Unlock()
	if !followUpCalled {
		t.Error("runner should have been called for follow-up phase")
	}
}

func TestEngine_ReworkFeedbackIncludesPriorReviewCycles(t *testing.T) {
	// When review produces "rework" and routes back to implement, the
	// implement prompt should include prior cycle context from archived
	// review results. This test simulates two review cycles: the first
	// produces a rework, and the second (after re-implement) also produces
	// a rework. The second implement should see prior cycle context from
	// the first review.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review"},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				// First implement.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl1",
					CostUSD: 0.10,
				}},
				// Second implement (after first rework).
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl2",
					CostUSD: 0.10,
				}},
				// Third implement (after second rework).
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl3",
					CostUSD: 0.10,
				}},
			},
			"review/go-specialist": {
				// First review: rework with critical finding.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"handler.go","line":42,"issue":"nil deref","suggestion":"add nil check"}]}`),
					RawText: "critical issue",
					CostUSD: 0.15,
				}},
				// Second review: rework with different finding.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"major","file":"util.go","line":10,"issue":"unchecked error","suggestion":"check return value"}]}`),
					RawText: "major issue",
					CostUSD: 0.15,
				}},
				// Third review: pass.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "no issues",
					CostUSD: 0.15,
				}},
			},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Template that renders PriorCycles.
	implTmpl := `Phase: implement
Ticket: {{.Ticket.Key}}
{{- if .ReworkFeedback}}
REWORK:
Source: {{.ReworkFeedback.Source}}
Verdict: {{.ReworkFeedback.Verdict}}
{{- range .ReworkFeedback.ReviewFindings}}
Finding: [{{.Severity}}] {{.File}}:{{.Line}} — {{.Issue}}
{{- end}}
{{- if .ReworkFeedback.PriorCycles}}
PRIOR_CYCLES:
{{- range .ReworkFeedback.PriorCycles}}
Cycle{{.Cycle}}: [{{.Source}}] {{.Verdict}} — {{.Summary}}
{{- end}}
{{- end}}
{{- end}}
`
	if err := os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte(implTmpl), 0644); err != nil {
		t.Fatal(err)
	}
	reviewPromptDir := filepath.Join(promptDir, "prompts")
	if err := os.MkdirAll(reviewPromptDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reviewPromptDir, "review-go.md"), []byte("Reviewer: go-specialist\nTicket: {{.Ticket.Key}}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadOrCreate(stateDir, "PRIOR-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:        &PhasePipeline{Phases: phases},
		Loader:          NewPromptLoader(promptDir),
		Ticket:          TicketData{Key: "PRIOR-1", Summary: "Prior cycles test"},
		Model:           "test-model",
		WorkDir:         workDir,
		MaxCostUSD:      0,
		MaxReworkCycles: 3,
		Mode:            Autonomous,
		SleepFunc:       func(time.Duration) {},
		JitterFunc:      func(time.Duration) time.Duration { return 0 },
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Find the implement runner calls.
	var implPrompts []string
	for _, call := range mock.calls {
		if call.Phase == "implement" {
			implPrompts = append(implPrompts, call.SystemPrompt)
		}
	}

	if len(implPrompts) < 3 {
		t.Fatalf("expected 3 implement calls, got %d", len(implPrompts))
	}

	// First implement: no rework feedback.
	if strings.Contains(implPrompts[0], "REWORK:") {
		t.Error("first implement should not have rework feedback")
	}

	// Second implement: rework feedback from first review, no prior cycles.
	if !strings.Contains(implPrompts[1], "REWORK:") {
		t.Error("second implement should have rework feedback")
	}
	if !strings.Contains(implPrompts[1], "nil deref") {
		t.Error("second implement should reference nil deref finding")
	}
	if strings.Contains(implPrompts[1], "PRIOR_CYCLES:") {
		t.Error("second implement should NOT have prior cycles (first rework)")
	}

	// Third implement: rework feedback from second review, WITH prior cycle context.
	if !strings.Contains(implPrompts[2], "REWORK:") {
		t.Error("third implement should have rework feedback")
	}
	if !strings.Contains(implPrompts[2], "unchecked error") {
		t.Error("third implement should reference unchecked error finding")
	}
	if !strings.Contains(implPrompts[2], "PRIOR_CYCLES:") {
		t.Error("third implement should have prior cycles")
	}
	if !strings.Contains(implPrompts[2], "nil deref") {
		t.Error("third implement prior cycles should reference nil deref from cycle 1")
	}
}
