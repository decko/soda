package pipeline

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/decko/soda/internal/runner"
)

func TestEmitPhaseFailed_RetriesExhausted_SetsFailureCategory(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "prompts/triage.md"},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{result: &runner.RunResult{Output: json.RawMessage(`{}`)}}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	phaseErr := &RetriesExhaustedError{
		Phase:    "triage",
		Category: "transient",
		Attempts: 3,
		Err:      &runner.TransientError{Err: fmt.Errorf("503")},
	}
	engine.emitPhaseFailed("triage", phaseErr)

	// Verify failure_category is set in phase state.
	ps := state.Meta().Phases["triage"]
	if ps.FailureCategory != "transient" {
		t.Errorf("FailureCategory = %q, want %q", ps.FailureCategory, "transient")
	}

	// Verify failure_category is in the emitted event.
	var failedEvent *Event
	for idx := range events {
		if events[idx].Kind == EventPhaseFailed {
			failedEvent = &events[idx]
			break
		}
	}
	if failedEvent == nil {
		t.Fatal("no phase_failed event emitted")
	}
	if failedEvent.Data["failure_category"] != "transient" {
		t.Errorf("event failure_category = %v, want %q", failedEvent.Data["failure_category"], "transient")
	}
	if failedEvent.Data["category"] != "transient" {
		t.Errorf("event category = %v, want %q (backward compat)", failedEvent.Data["category"], "transient")
	}
}

func TestEmitPhaseFailed_BudgetExceeded_SetsFailureCategory(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "prompts/plan.md"},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"plan": {{result: &runner.RunResult{Output: json.RawMessage(`{}`)}}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	if err := state.MarkRunning("plan"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	phaseErr := &BudgetExceededError{Phase: "plan", Limit: 5.0, Actual: 5.5}
	engine.emitPhaseFailed("plan", phaseErr)

	ps := state.Meta().Phases["plan"]
	if ps.FailureCategory != "budget" {
		t.Errorf("FailureCategory = %q, want %q", ps.FailureCategory, "budget")
	}

	var failedEvent *Event
	for idx := range events {
		if events[idx].Kind == EventPhaseFailed {
			failedEvent = &events[idx]
			break
		}
	}
	if failedEvent == nil {
		t.Fatal("no phase_failed event emitted")
	}
	if failedEvent.Data["failure_category"] != "budget" {
		t.Errorf("event failure_category = %v, want %q", failedEvent.Data["failure_category"], "budget")
	}
}

func TestEmitPhaseFailed_PromptError_SetsFailureCategory(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "impl", Prompt: "prompts/impl.md"},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"impl": {{result: &runner.RunResult{Output: json.RawMessage(`{}`)}}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	if err := state.MarkRunning("impl"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	phaseErr := &PromptError{Phase: "impl", Operation: "render", Err: fmt.Errorf("bad template")}
	engine.emitPhaseFailed("impl", phaseErr)

	ps := state.Meta().Phases["impl"]
	if ps.FailureCategory != "prompt" {
		t.Errorf("FailureCategory = %q, want %q", ps.FailureCategory, "prompt")
	}

	var failedEvent *Event
	for idx := range events {
		if events[idx].Kind == EventPhaseFailed {
			failedEvent = &events[idx]
			break
		}
	}
	if failedEvent == nil {
		t.Fatal("no phase_failed event emitted")
	}
	if failedEvent.Data["failure_category"] != "prompt" {
		t.Errorf("event failure_category = %v, want %q", failedEvent.Data["failure_category"], "prompt")
	}
}

func TestEmitPhaseFailed_ContextBudgetError_SetsFailureCategory(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "impl", Prompt: "prompts/impl.md"},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"impl": {{result: &runner.RunResult{Output: json.RawMessage(`{}`)}}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	if err := state.MarkRunning("impl"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	phaseErr := &ContextBudgetError{Phase: "impl", BudgetTokens: 10000, CurrentTokens: 25000}
	engine.emitPhaseFailed("impl", phaseErr)

	ps := state.Meta().Phases["impl"]
	if ps.FailureCategory != "context" {
		t.Errorf("FailureCategory = %q, want %q", ps.FailureCategory, "context")
	}

	var failedEvent *Event
	for idx := range events {
		if events[idx].Kind == EventPhaseFailed {
			failedEvent = &events[idx]
			break
		}
	}
	if failedEvent == nil {
		t.Fatal("no phase_failed event emitted")
	}
	if failedEvent.Data["failure_category"] != "context" {
		t.Errorf("event failure_category = %v, want %q", failedEvent.Data["failure_category"], "context")
	}
	if failedEvent.Data["error_type"] != "context_budget_exceeded" {
		t.Errorf("event error_type = %v, want %q", failedEvent.Data["error_type"], "context_budget_exceeded")
	}
}

func TestEmitPhaseFailed_UnstructuredError_NoFailureCategory(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "prompts/triage.md"},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{result: &runner.RunResult{Output: json.RawMessage(`{}`)}}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	// Plain error — should not set failure_category.
	phaseErr := fmt.Errorf("some generic error")
	engine.emitPhaseFailed("triage", phaseErr)

	ps := state.Meta().Phases["triage"]
	if ps.FailureCategory != "" {
		t.Errorf("FailureCategory = %q, want empty for unstructured error", ps.FailureCategory)
	}

	var failedEvent *Event
	for idx := range events {
		if events[idx].Kind == EventPhaseFailed {
			failedEvent = &events[idx]
			break
		}
	}
	if failedEvent == nil {
		t.Fatal("no phase_failed event emitted")
	}
	if _, ok := failedEvent.Data["failure_category"]; ok {
		t.Errorf("failure_category should not be present for unstructured errors, got %v", failedEvent.Data["failure_category"])
	}
}

func TestEmitPhaseFailed_DependencyNotMet_SetsFailureCategory(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "impl", Prompt: "prompts/impl.md"},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"impl": {{result: &runner.RunResult{Output: json.RawMessage(`{}`)}}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	if err := state.MarkRunning("impl"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	phaseErr := &DependencyNotMetError{Phase: "impl", Dependency: "plan"}
	engine.emitPhaseFailed("impl", phaseErr)

	ps := state.Meta().Phases["impl"]
	if ps.FailureCategory != "dependency" {
		t.Errorf("FailureCategory = %q, want %q", ps.FailureCategory, "dependency")
	}

	var failedEvent *Event
	for idx := range events {
		if events[idx].Kind == EventPhaseFailed {
			failedEvent = &events[idx]
			break
		}
	}
	if failedEvent == nil {
		t.Fatal("no phase_failed event emitted")
	}
	if failedEvent.Data["failure_category"] != "dependency" {
		t.Errorf("event failure_category = %v, want %q", failedEvent.Data["failure_category"], "dependency")
	}
}
