package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
)

func TestPipelineTimeoutError(t *testing.T) {
	err := &PipelineTimeoutError{
		Limit:   2 * time.Hour,
		Elapsed: 2*time.Hour + 5*time.Minute,
		Phase:   "implement",
	}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}
	if !strings.Contains(msg, "implement") {
		t.Errorf("Error() should contain phase name, got: %s", msg)
	}
	if !strings.Contains(msg, "2h0m0s") {
		t.Errorf("Error() should contain limit, got: %s", msg)
	}
	if !strings.Contains(msg, "2h5m0s") {
		t.Errorf("Error() should contain elapsed, got: %s", msg)
	}

	var target *PipelineTimeoutError
	if !errors.As(err, &target) {
		t.Error("errors.As should match PipelineTimeoutError")
	}
	if target.Phase != "implement" {
		t.Errorf("Phase = %q, want %q", target.Phase, "implement")
	}
}

func TestBudgetExceededError(t *testing.T) {
	err := &BudgetExceededError{Limit: 15.00, Actual: 15.50, Phase: "verify"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	var target *BudgetExceededError
	if !errors.As(err, &target) {
		t.Error("errors.As should match BudgetExceededError")
	}
	if target.Phase != "verify" {
		t.Errorf("Phase = %q, want %q", target.Phase, "verify")
	}
}

func TestPhaseBudgetExceededError(t *testing.T) {
	err := &PhaseBudgetExceededError{Limit: 8.00, Actual: 10.50, Phase: "implement"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}
	if !strings.Contains(msg, "implement") {
		t.Errorf("Error() should contain phase name, got: %s", msg)
	}
	if !strings.Contains(msg, "8.00") {
		t.Errorf("Error() should contain limit, got: %s", msg)
	}
	if !strings.Contains(msg, "10.50") {
		t.Errorf("Error() should contain actual cost, got: %s", msg)
	}

	var target *PhaseBudgetExceededError
	if !errors.As(err, &target) {
		t.Error("errors.As should match PhaseBudgetExceededError")
	}
	if target.Phase != "implement" {
		t.Errorf("Phase = %q, want %q", target.Phase, "implement")
	}
}

func TestGenerationBudgetExceededError(t *testing.T) {
	err := &GenerationBudgetExceededError{Limit: 5.00, Actual: 6.50, Phase: "implement"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}
	if !strings.Contains(msg, "implement") {
		t.Errorf("Error() should contain phase name, got: %s", msg)
	}
	if !strings.Contains(msg, "5.00") {
		t.Errorf("Error() should contain limit, got: %s", msg)
	}
	if !strings.Contains(msg, "6.50") {
		t.Errorf("Error() should contain actual cost, got: %s", msg)
	}

	var target *GenerationBudgetExceededError
	if !errors.As(err, &target) {
		t.Error("errors.As should match GenerationBudgetExceededError")
	}
	if target.Phase != "implement" {
		t.Errorf("Phase = %q, want %q", target.Phase, "implement")
	}
}

func TestDependencyNotMetError(t *testing.T) {
	err := &DependencyNotMetError{Phase: "implement", Dependency: "plan"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	var target *DependencyNotMetError
	if !errors.As(err, &target) {
		t.Error("errors.As should match DependencyNotMetError")
	}
}

func TestPhaseGateError(t *testing.T) {
	err := &PhaseGateError{Phase: "triage", Reason: "not automatable"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	var target *PhaseGateError
	if !errors.As(err, &target) {
		t.Error("errors.As should match PhaseGateError")
	}
}

func TestReworkSignal(t *testing.T) {
	t.Run("with_findings", func(t *testing.T) {
		err := &reworkSignal{
			target: "implement",
			findings: []reworkFinding{
				{Severity: "critical", Issue: "nil deref"},
				{Severity: "minor", Issue: "naming"},
				{Severity: "major", Issue: "missing error check"},
			},
		}

		msg := err.Error()
		if !strings.Contains(msg, "target implement") {
			t.Errorf("Error() should contain target, got: %s", msg)
		}
		// Only critical and major issues should appear.
		if !strings.Contains(msg, "nil deref") {
			t.Errorf("Error() should contain critical issue, got: %s", msg)
		}
		if !strings.Contains(msg, "missing error check") {
			t.Errorf("Error() should contain major issue, got: %s", msg)
		}
		if strings.Contains(msg, "naming") {
			t.Errorf("Error() should NOT contain minor issue, got: %s", msg)
		}

		var target *reworkSignal
		if !errors.As(err, &target) {
			t.Error("errors.As should match reworkSignal")
		}
		if target.target != "implement" {
			t.Errorf("target = %q, want %q", target.target, "implement")
		}
	})

	t.Run("without_findings", func(t *testing.T) {
		err := &reworkSignal{target: "plan"}
		msg := err.Error()
		if !strings.Contains(msg, "target plan") {
			t.Errorf("Error() should contain target, got: %s", msg)
		}
	})
}

func TestPhaseNotFoundError(t *testing.T) {
	err := &PhaseNotFoundError{
		Phase:    "deploy",
		Pipeline: []string{"triage", "plan", "implement", "verify"},
	}
	msg := err.Error()
	if !strings.Contains(msg, "deploy") {
		t.Errorf("Error() should contain phase name, got: %s", msg)
	}
	if !strings.Contains(msg, "not found in pipeline") {
		t.Errorf("Error() should contain 'not found in pipeline', got: %s", msg)
	}
	if !strings.Contains(msg, "triage, plan, implement, verify") {
		t.Errorf("Error() should list available phases, got: %s", msg)
	}

	var target *PhaseNotFoundError
	if !errors.As(err, &target) {
		t.Error("errors.As should match PhaseNotFoundError")
	}
	if target.Phase != "deploy" {
		t.Errorf("Phase = %q, want %q", target.Phase, "deploy")
	}
	if len(target.Pipeline) != 4 {
		t.Errorf("Pipeline length = %d, want 4", len(target.Pipeline))
	}
}

func TestPhaseNotFoundError_EmptyPipeline(t *testing.T) {
	err := &PhaseNotFoundError{Phase: "unknown", Pipeline: nil}
	msg := err.Error()
	if !strings.Contains(msg, "unknown") {
		t.Errorf("Error() should contain phase name, got: %s", msg)
	}
	// With nil pipeline, available list should be empty string.
	if !strings.Contains(msg, "(available: )") {
		t.Errorf("Error() should handle nil pipeline, got: %s", msg)
	}
}

func TestRetriesExhaustedError(t *testing.T) {
	inner := fmt.Errorf("connection timeout")

	t.Run("phase_only", func(t *testing.T) {
		err := &RetriesExhaustedError{
			Phase:    "triage",
			Category: "transient",
			Attempts: 3,
			Err:      inner,
		}
		msg := err.Error()
		if !strings.Contains(msg, "triage") {
			t.Errorf("Error() should contain phase name, got: %s", msg)
		}
		if !strings.Contains(msg, "3 attempts") {
			t.Errorf("Error() should contain attempt count, got: %s", msg)
		}
		if !strings.Contains(msg, "transient") {
			t.Errorf("Error() should contain category, got: %s", msg)
		}
		if !strings.Contains(msg, "connection timeout") {
			t.Errorf("Error() should contain inner error, got: %s", msg)
		}
		if strings.Contains(msg, "reviewer") {
			t.Errorf("Error() should NOT contain reviewer when Reviewer is empty, got: %s", msg)
		}

		// Unwrap should return the inner error.
		if !errors.Is(err, inner) {
			t.Error("Unwrap should return inner error")
		}

		var target *RetriesExhaustedError
		if !errors.As(err, &target) {
			t.Error("errors.As should match RetriesExhaustedError")
		}
		if target.Phase != "triage" {
			t.Errorf("Phase = %q, want %q", target.Phase, "triage")
		}
		if target.Reviewer != "" {
			t.Errorf("Reviewer = %q, want empty", target.Reviewer)
		}
		if target.Category != "transient" {
			t.Errorf("Category = %q, want %q", target.Category, "transient")
		}
		if target.Attempts != 3 {
			t.Errorf("Attempts = %d, want 3", target.Attempts)
		}
	})

	t.Run("with_reviewer", func(t *testing.T) {
		err := &RetriesExhaustedError{
			Phase:    "review",
			Reviewer: "go-specialist",
			Category: "transient",
			Attempts: 2,
			Err:      inner,
		}
		msg := err.Error()
		if !strings.Contains(msg, "review") {
			t.Errorf("Error() should contain phase name, got: %s", msg)
		}
		if !strings.Contains(msg, "go-specialist") {
			t.Errorf("Error() should contain reviewer name, got: %s", msg)
		}
		if !strings.Contains(msg, "2 attempts") {
			t.Errorf("Error() should contain attempt count, got: %s", msg)
		}

		var target *RetriesExhaustedError
		if !errors.As(err, &target) {
			t.Error("errors.As should match RetriesExhaustedError")
		}
		if target.Phase != "review" {
			t.Errorf("Phase = %q, want %q", target.Phase, "review")
		}
		if target.Reviewer != "go-specialist" {
			t.Errorf("Reviewer = %q, want %q", target.Reviewer, "go-specialist")
		}
	})
}

func TestWorktreeError(t *testing.T) {
	inner := fmt.Errorf("worktree already exists")

	t.Run("with_path", func(t *testing.T) {
		err := &WorktreeError{
			Branch: "soda/42",
			Path:   "/tmp/worktrees/soda/42",
			Err:    inner,
		}
		msg := err.Error()
		if !strings.Contains(msg, "soda/42") {
			t.Errorf("Error() should contain branch, got: %s", msg)
		}
		if !strings.Contains(msg, "/tmp/worktrees/soda/42") {
			t.Errorf("Error() should contain path, got: %s", msg)
		}
		if !strings.Contains(msg, "worktree already exists") {
			t.Errorf("Error() should contain inner error, got: %s", msg)
		}
		if !errors.Is(err, inner) {
			t.Error("Unwrap should return inner error")
		}

		var target *WorktreeError
		if !errors.As(err, &target) {
			t.Error("errors.As should match WorktreeError")
		}
		if target.Branch != "soda/42" {
			t.Errorf("Branch = %q, want %q", target.Branch, "soda/42")
		}
	})

	t.Run("without_path", func(t *testing.T) {
		err := &WorktreeError{
			Branch: "soda/99",
			Err:    inner,
		}
		msg := err.Error()
		if !strings.Contains(msg, "soda/99") {
			t.Errorf("Error() should contain branch, got: %s", msg)
		}
		if strings.Contains(msg, " at ") {
			t.Errorf("Error() should not contain 'at' when path is empty, got: %s", msg)
		}
	})
}

func TestPromptError(t *testing.T) {
	inner := fmt.Errorf("template not found: triage.md")

	t.Run("load", func(t *testing.T) {
		err := &PromptError{
			Phase:     "triage",
			Operation: "load",
			Err:       inner,
		}
		msg := err.Error()
		if !strings.Contains(msg, "triage") {
			t.Errorf("Error() should contain phase name, got: %s", msg)
		}
		if !strings.Contains(msg, "load") {
			t.Errorf("Error() should contain operation, got: %s", msg)
		}
		if !strings.Contains(msg, "template not found") {
			t.Errorf("Error() should contain inner error, got: %s", msg)
		}
		if strings.Contains(msg, "reviewer") {
			t.Errorf("Error() should NOT contain reviewer when Reviewer is empty, got: %s", msg)
		}
		if !errors.Is(err, inner) {
			t.Error("Unwrap should return inner error")
		}

		var target *PromptError
		if !errors.As(err, &target) {
			t.Error("errors.As should match PromptError")
		}
		if target.Phase != "triage" {
			t.Errorf("Phase = %q, want %q", target.Phase, "triage")
		}
		if target.Operation != "load" {
			t.Errorf("Operation = %q, want %q", target.Operation, "load")
		}
		if target.Reviewer != "" {
			t.Errorf("Reviewer = %q, want empty", target.Reviewer)
		}
	})

	t.Run("render", func(t *testing.T) {
		renderErr := fmt.Errorf("invalid template syntax")
		err := &PromptError{
			Phase:     "plan",
			Operation: "render",
			Err:       renderErr,
		}
		msg := err.Error()
		if !strings.Contains(msg, "render") {
			t.Errorf("Error() should contain operation, got: %s", msg)
		}
		if !strings.Contains(msg, "plan") {
			t.Errorf("Error() should contain phase name, got: %s", msg)
		}
	})

	t.Run("with_reviewer", func(t *testing.T) {
		err := &PromptError{
			Phase:     "review",
			Reviewer:  "go-specialist",
			Operation: "load",
			Err:       inner,
		}
		msg := err.Error()
		if !strings.Contains(msg, "review") {
			t.Errorf("Error() should contain phase name, got: %s", msg)
		}
		if !strings.Contains(msg, "go-specialist") {
			t.Errorf("Error() should contain reviewer name, got: %s", msg)
		}
		if !strings.Contains(msg, "load") {
			t.Errorf("Error() should contain operation, got: %s", msg)
		}

		var target *PromptError
		if !errors.As(err, &target) {
			t.Error("errors.As should match PromptError")
		}
		if target.Reviewer != "go-specialist" {
			t.Errorf("Reviewer = %q, want %q", target.Reviewer, "go-specialist")
		}
		if target.Phase != "review" {
			t.Errorf("Phase = %q, want %q", target.Phase, "review")
		}
	})
}

func TestTransientSuggestionCatalog(t *testing.T) {
	// All 7 known reasons must have suggestions.
	knownReasons := []string{"oom", "signal", "rate_limit", "timeout", "overloaded", "connection", "unknown"}
	for _, reason := range knownReasons {
		te := &runner.TransientError{Reason: reason, Err: fmt.Errorf("test")}
		suggestion := transientSuggestion(te)
		if suggestion == "" {
			t.Errorf("transientSuggestion(%q) returned empty, want non-empty suggestion", reason)
		}
	}

	// Unknown reason not in catalog should return empty.
	te := &runner.TransientError{Reason: "never_seen_before", Err: fmt.Errorf("test")}
	if suggestion := transientSuggestion(te); suggestion != "" {
		t.Errorf("transientSuggestion for unknown reason returned %q, want empty", suggestion)
	}

	// Non-transient error should return empty.
	if suggestion := transientSuggestion(fmt.Errorf("plain error")); suggestion != "" {
		t.Errorf("transientSuggestion for non-transient error returned %q, want empty", suggestion)
	}
}

func TestEmitPhaseFailedEnrichment(t *testing.T) {
	t.Run("retries_exhausted_with_suggestion", func(t *testing.T) {
		var captured []Event
		phases := []PhaseConfig{{Name: "triage", Prompt: "triage.md"}}
		engine, _ := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) { captured = append(captured, e) }
		})

		// Mark running so MarkFailed has valid state.
		_ = engine.state.MarkRunning("triage")

		transientErr := &runner.TransientError{Reason: "rate_limit", Err: fmt.Errorf("429 too many requests")}
		reErr := &RetriesExhaustedError{Phase: "triage", Category: "transient", Attempts: 3, Err: transientErr}
		_ = engine.state.MarkFailed("triage", reErr)
		engine.emitPhaseFailed("triage", reErr)

		var found *Event
		for i := range captured {
			if captured[i].Kind == EventPhaseFailed {
				found = &captured[i]
				break
			}
		}
		if found == nil {
			t.Fatal("no phase_failed event emitted")
		}
		if found.Data["error_type"] != "retries_exhausted" {
			t.Errorf("error_type = %v, want retries_exhausted", found.Data["error_type"])
		}
		if found.Data["category"] != "transient" {
			t.Errorf("category = %v, want transient", found.Data["category"])
		}
		if found.Data["attempts"] != 3 {
			t.Errorf("attempts = %v, want 3", found.Data["attempts"])
		}
		suggestion, ok := found.Data["suggestion"].(string)
		if !ok || suggestion == "" {
			t.Error("suggestion should be a non-empty string for rate_limit transient errors")
		}
	})

	t.Run("retries_exhausted_with_reviewer", func(t *testing.T) {
		var captured []Event
		phases := []PhaseConfig{{Name: "review", Prompt: "review.md"}}
		engine, _ := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) { captured = append(captured, e) }
		})

		_ = engine.state.MarkRunning("review")
		reErr := &RetriesExhaustedError{Phase: "review", Reviewer: "go-specialist", Category: "transient", Attempts: 2, Err: fmt.Errorf("timeout")}
		_ = engine.state.MarkFailed("review", reErr)
		engine.emitPhaseFailed("review", reErr)

		var found *Event
		for i := range captured {
			if captured[i].Kind == EventPhaseFailed {
				found = &captured[i]
				break
			}
		}
		if found == nil {
			t.Fatal("no phase_failed event emitted")
		}
		if found.Data["reviewer"] != "go-specialist" {
			t.Errorf("reviewer = %v, want go-specialist", found.Data["reviewer"])
		}
	})

	t.Run("prompt_error", func(t *testing.T) {
		var captured []Event
		phases := []PhaseConfig{{Name: "triage", Prompt: "triage.md"}}
		engine, _ := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) { captured = append(captured, e) }
		})

		_ = engine.state.MarkRunning("triage")
		pErr := &PromptError{Phase: "triage", Operation: "load", Err: fmt.Errorf("not found")}
		_ = engine.state.MarkFailed("triage", pErr)
		engine.emitPhaseFailed("triage", pErr)

		var found *Event
		for i := range captured {
			if captured[i].Kind == EventPhaseFailed {
				found = &captured[i]
				break
			}
		}
		if found == nil {
			t.Fatal("no phase_failed event emitted")
		}
		if found.Data["error_type"] != "prompt_error" {
			t.Errorf("error_type = %v, want prompt_error", found.Data["error_type"])
		}
		if found.Data["operation"] != "load" {
			t.Errorf("operation = %v, want load", found.Data["operation"])
		}
	})

	t.Run("prompt_error_with_reviewer", func(t *testing.T) {
		var captured []Event
		phases := []PhaseConfig{{Name: "review", Prompt: "review.md"}}
		engine, _ := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) { captured = append(captured, e) }
		})

		_ = engine.state.MarkRunning("review")
		pErr := &PromptError{Phase: "review", Reviewer: "ai-harness", Operation: "render", Err: fmt.Errorf("template error")}
		_ = engine.state.MarkFailed("review", pErr)
		engine.emitPhaseFailed("review", pErr)

		var found *Event
		for i := range captured {
			if captured[i].Kind == EventPhaseFailed {
				found = &captured[i]
				break
			}
		}
		if found == nil {
			t.Fatal("no phase_failed event emitted")
		}
		if found.Data["reviewer"] != "ai-harness" {
			t.Errorf("reviewer = %v, want ai-harness", found.Data["reviewer"])
		}
		if found.Data["operation"] != "render" {
			t.Errorf("operation = %v, want render", found.Data["operation"])
		}
	})

	t.Run("budget_exceeded", func(t *testing.T) {
		var captured []Event
		phases := []PhaseConfig{{Name: "implement", Prompt: "implement.md"}}
		engine, _ := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) { captured = append(captured, e) }
		})

		_ = engine.state.MarkRunning("implement")
		bErr := &BudgetExceededError{Phase: "implement", Limit: 15.00, Actual: 16.50}
		_ = engine.state.MarkFailed("implement", bErr)
		engine.emitPhaseFailed("implement", bErr)

		var found *Event
		for i := range captured {
			if captured[i].Kind == EventPhaseFailed {
				found = &captured[i]
				break
			}
		}
		if found == nil {
			t.Fatal("no phase_failed event emitted")
		}
		if found.Data["error_type"] != "budget_exceeded" {
			t.Errorf("error_type = %v, want budget_exceeded", found.Data["error_type"])
		}
		if found.Data["limit"] != 15.0 {
			t.Errorf("limit = %v, want 15.0", found.Data["limit"])
		}
	})

	t.Run("dependency_not_met", func(t *testing.T) {
		var captured []Event
		phases := []PhaseConfig{{Name: "implement", Prompt: "implement.md"}}
		engine, _ := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) { captured = append(captured, e) }
		})

		_ = engine.state.MarkRunning("implement")
		dErr := &DependencyNotMetError{Phase: "implement", Dependency: "plan"}
		_ = engine.state.MarkFailed("implement", dErr)
		engine.emitPhaseFailed("implement", dErr)

		var found *Event
		for i := range captured {
			if captured[i].Kind == EventPhaseFailed {
				found = &captured[i]
				break
			}
		}
		if found == nil {
			t.Fatal("no phase_failed event emitted")
		}
		if found.Data["error_type"] != "dependency_not_met" {
			t.Errorf("error_type = %v, want dependency_not_met", found.Data["error_type"])
		}
		if found.Data["dependency"] != "plan" {
			t.Errorf("dependency = %v, want plan", found.Data["dependency"])
		}
	})

	t.Run("plain_error_no_enrichment", func(t *testing.T) {
		var captured []Event
		phases := []PhaseConfig{{Name: "triage", Prompt: "triage.md"}}
		engine, _ := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) { captured = append(captured, e) }
		})

		_ = engine.state.MarkRunning("triage")
		plainErr := fmt.Errorf("something went wrong")
		_ = engine.state.MarkFailed("triage", plainErr)
		engine.emitPhaseFailed("triage", plainErr)

		var found *Event
		for i := range captured {
			if captured[i].Kind == EventPhaseFailed {
				found = &captured[i]
				break
			}
		}
		if found == nil {
			t.Fatal("no phase_failed event emitted")
		}
		if _, ok := found.Data["error_type"]; ok {
			t.Error("plain errors should not have error_type in event data")
		}
	})
}
