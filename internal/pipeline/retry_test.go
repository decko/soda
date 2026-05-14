package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/decko/soda/internal/runner"
)

func TestRetry_ParseSuccessOnFirstAttempt(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "prompts/triage.md", Retry: RetryConfig{Parse: 2}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {
				{result: &runner.RunResult{Output: json.RawMessage(`{}`)}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "triage", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["triage"]
	if !ps.ParseSuccessOnFirst {
		t.Error("ParseSuccessOnFirst should be true on first-attempt success")
	}
	if ps.ParseAttempts != 0 {
		t.Errorf("ParseAttempts = %d, want 0 (no parse failures)", ps.ParseAttempts)
	}
}

func TestRetry_ParseSuccessNotSetOnRetry(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "prompts/triage.md", Retry: RetryConfig{Parse: 2}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {
				{err: &runner.ParseError{Err: fmt.Errorf("bad json")}},
				{result: &runner.RunResult{Output: json.RawMessage(`{}`)}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "triage", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["triage"]
	if ps.ParseSuccessOnFirst {
		t.Error("ParseSuccessOnFirst should be false when first attempt fails")
	}
	if ps.ParseAttempts != 1 {
		t.Errorf("ParseAttempts = %d, want 1", ps.ParseAttempts)
	}
}

func TestRetry_ParseAttemptAccumulated(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "prompts/plan.md", Retry: RetryConfig{Parse: 3}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"plan": {
				{err: &runner.ParseError{Err: fmt.Errorf("error 1")}},
				{err: &runner.ParseError{Err: fmt.Errorf("error 2")}},
				{result: &runner.RunResult{Output: json.RawMessage(`{}`)}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("plan"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "plan", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["plan"]
	if ps.ParseAttempts != 2 {
		t.Errorf("ParseAttempts = %d, want 2 (two parse failures)", ps.ParseAttempts)
	}
	if ps.ParseSuccessOnFirst {
		t.Error("ParseSuccessOnFirst should be false")
	}
}

func TestRetry_ModelFallbackOnParseThreshold(t *testing.T) {
	pipeline := &PhasePipeline{
		Phases: []PhaseConfig{
			{Name: "impl", Prompt: "prompts/impl.md", Model: "phase-model", Retry: RetryConfig{Parse: 3}},
		},
		ModelRouting: ModelRoutingConfig{FallbackThreshold: 2},
	}

	var capturedModels []string
	mock := &funcRunner{
		fn: func(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
			capturedModels = append(capturedModels, opts.Model)
			if len(capturedModels) <= 2 {
				return nil, &runner.ParseError{Err: fmt.Errorf("bad output")}
			}
			return &runner.RunResult{Output: json.RawMessage(`{}`)}, nil
		},
	}

	var events []Event
	engine, state := setupEngine(t, pipeline.Phases, mock, func(cfg *EngineConfig) {
		cfg.Pipeline = pipeline
		cfg.Model = "global-model"
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	if err := state.MarkRunning("impl"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "impl", Model: "phase-model", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), pipeline.Phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	// First two calls use phase-model, third uses global-model after fallback.
	if len(capturedModels) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(capturedModels))
	}
	if capturedModels[0] != "phase-model" {
		t.Errorf("call 0 model = %q, want %q", capturedModels[0], "phase-model")
	}
	if capturedModels[1] != "phase-model" {
		t.Errorf("call 1 model = %q, want %q", capturedModels[1], "phase-model")
	}
	if capturedModels[2] != "global-model" {
		t.Errorf("call 2 model = %q, want %q", capturedModels[2], "global-model")
	}

	// Verify ModelUsed reflects the global model after fallback.
	ps := state.Meta().Phases["impl"]
	if ps.ModelUsed != "global-model" {
		t.Errorf("ModelUsed = %q, want %q after fallback", ps.ModelUsed, "global-model")
	}

	// Verify EventModelFallback was emitted.
	found := false
	for _, ev := range events {
		if ev.Kind == EventModelFallback {
			found = true
			if ev.Data["from"] != "phase-model" {
				t.Errorf("fallback event from = %v, want %q", ev.Data["from"], "phase-model")
			}
			if ev.Data["to"] != "global-model" {
				t.Errorf("fallback event to = %v, want %q", ev.Data["to"], "global-model")
			}
		}
	}
	if !found {
		t.Error("EventModelFallback not emitted")
	}
}

func TestRetry_NoFallbackWhenAlreadyGlobalModel(t *testing.T) {
	pipeline := &PhasePipeline{
		Phases: []PhaseConfig{
			{Name: "triage", Prompt: "prompts/triage.md", Retry: RetryConfig{Parse: 3}},
		},
		ModelRouting: ModelRoutingConfig{FallbackThreshold: 1},
	}

	callCount := 0
	mock := &funcRunner{
		fn: func(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
			callCount++
			if callCount == 1 {
				return nil, &runner.ParseError{Err: fmt.Errorf("bad json")}
			}
			return &runner.RunResult{Output: json.RawMessage(`{}`)}, nil
		},
	}

	var events []Event
	engine, state := setupEngine(t, pipeline.Phases, mock, func(cfg *EngineConfig) {
		cfg.Pipeline = pipeline
		cfg.Model = "global-model"
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	// Phase is already using the global model — no fallback should occur.
	opts := runner.RunOpts{Phase: "triage", Model: "global-model", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), pipeline.Phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	for _, ev := range events {
		if ev.Kind == EventModelFallback {
			t.Error("EventModelFallback should not be emitted when already using global model")
		}
	}
}

func TestRetry_TransientRetryCounterIncremented(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "prompts/triage.md", Retry: RetryConfig{Transient: 2, Parse: 1}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {
				{err: &runner.TransientError{Err: fmt.Errorf("503")}},
				{result: &runner.RunResult{Output: json.RawMessage(`{}`)}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "triage", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["triage"]
	if ps.TransientRetries != 1 {
		t.Errorf("TransientRetries = %d, want 1", ps.TransientRetries)
	}
	if ps.ParseRetries != 0 {
		t.Errorf("ParseRetries = %d, want 0 (no parse failures)", ps.ParseRetries)
	}
	if ps.SemanticRetries != 0 {
		t.Errorf("SemanticRetries = %d, want 0", ps.SemanticRetries)
	}
}

func TestRetry_ParseRetryCounterIncremented(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "prompts/plan.md", Retry: RetryConfig{Parse: 3}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"plan": {
				{err: &runner.ParseError{Err: fmt.Errorf("error 1")}},
				{err: &runner.ParseError{Err: fmt.Errorf("error 2")}},
				{result: &runner.RunResult{Output: json.RawMessage(`{}`)}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("plan"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "plan", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["plan"]
	if ps.ParseRetries != 2 {
		t.Errorf("ParseRetries = %d, want 2", ps.ParseRetries)
	}
	if ps.TransientRetries != 0 {
		t.Errorf("TransientRetries = %d, want 0", ps.TransientRetries)
	}
}

func TestRetry_SemanticRetryCounterIncremented(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "verify", Prompt: "prompts/verify.md", Retry: RetryConfig{Semantic: 2}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"verify": {
				{err: &runner.SemanticError{Message: "bad"}},
				{result: &runner.RunResult{Output: json.RawMessage(`{}`)}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("verify"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "verify", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["verify"]
	if ps.SemanticRetries != 1 {
		t.Errorf("SemanticRetries = %d, want 1", ps.SemanticRetries)
	}
	if ps.TransientRetries != 0 {
		t.Errorf("TransientRetries = %d, want 0", ps.TransientRetries)
	}
	if ps.ParseRetries != 0 {
		t.Errorf("ParseRetries = %d, want 0", ps.ParseRetries)
	}
}

func TestRetry_FailureCategoryOnExhaustion(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "prompts/triage.md", Retry: RetryConfig{Transient: 1}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {
				{err: &runner.TransientError{Err: fmt.Errorf("503")}},
				{err: &runner.TransientError{Err: fmt.Errorf("503 again")}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "triage", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err == nil {
		t.Fatal("expected error from exhausted retries")
	}

	ps := state.Meta().Phases["triage"]
	if ps.FailureCategory != "transient" {
		t.Errorf("FailureCategory = %q, want %q", ps.FailureCategory, "transient")
	}
	if ps.TransientRetries != 1 {
		t.Errorf("TransientRetries = %d, want 1 (one successful retry before exhaustion)", ps.TransientRetries)
	}
}

func TestRetry_MixedRetryCounters(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "impl", Prompt: "prompts/impl.md", Retry: RetryConfig{Transient: 2, Parse: 2, Semantic: 2}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"impl": {
				{err: &runner.TransientError{Err: fmt.Errorf("503")}},
				{err: &runner.ParseError{Err: fmt.Errorf("bad json")}},
				{err: &runner.SemanticError{Message: "bad logic"}},
				{result: &runner.RunResult{Output: json.RawMessage(`{}`)}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("impl"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "impl", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["impl"]
	if ps.TransientRetries != 1 {
		t.Errorf("TransientRetries = %d, want 1", ps.TransientRetries)
	}
	if ps.ParseRetries != 1 {
		t.Errorf("ParseRetries = %d, want 1", ps.ParseRetries)
	}
	if ps.SemanticRetries != 1 {
		t.Errorf("SemanticRetries = %d, want 1", ps.SemanticRetries)
	}
	if ps.FailureCategory != "" {
		t.Errorf("FailureCategory = %q, want empty (no exhaustion)", ps.FailureCategory)
	}
}

func TestRetry_CountersResetOnRerun(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "prompts/triage.md", Retry: RetryConfig{Transient: 2, Parse: 2}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {
				{err: &runner.TransientError{Err: fmt.Errorf("503")}},
				{result: &runner.RunResult{Output: json.RawMessage(`{}`)}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "triage", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["triage"]
	if ps.TransientRetries != 1 {
		t.Errorf("TransientRetries after first run = %d, want 1", ps.TransientRetries)
	}

	// Simulate a resume --from by marking running again; counters should reset.
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatalf("MarkRunning (rerun): %v", err)
	}

	ps = state.Meta().Phases["triage"]
	if ps.TransientRetries != 0 {
		t.Errorf("TransientRetries after rerun = %d, want 0", ps.TransientRetries)
	}
	if ps.ParseRetries != 0 {
		t.Errorf("ParseRetries after rerun = %d, want 0", ps.ParseRetries)
	}
	if ps.SemanticRetries != 0 {
		t.Errorf("SemanticRetries after rerun = %d, want 0", ps.SemanticRetries)
	}
	if ps.FailureCategory != "" {
		t.Errorf("FailureCategory after rerun = %q, want empty", ps.FailureCategory)
	}
}

func TestRetry_ModelUsedUpdatedAfterFallbackFailure(t *testing.T) {
	// Verify that when a parse failure occurs *after* fallback,
	// ModelUsed reflects the global model (not the original per-phase model).
	pipeline := &PhasePipeline{
		Phases: []PhaseConfig{
			{Name: "impl", Prompt: "prompts/impl.md", Model: "phase-model", Retry: RetryConfig{Parse: 4}},
		},
		ModelRouting: ModelRoutingConfig{FallbackThreshold: 1},
	}

	callCount := 0
	mock := &funcRunner{
		fn: func(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
			callCount++
			if callCount <= 3 {
				return nil, &runner.ParseError{Err: fmt.Errorf("bad output %d", callCount)}
			}
			return &runner.RunResult{Output: json.RawMessage(`{}`)}, nil
		},
	}

	engine, state := setupEngine(t, pipeline.Phases, mock, func(cfg *EngineConfig) {
		cfg.Pipeline = pipeline
		cfg.Model = "global-model"
	})

	if err := state.MarkRunning("impl"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	opts := runner.RunOpts{Phase: "impl", Model: "phase-model", UserPrompt: "test"}
	_, err := engine.runWithRetry(t.Context(), pipeline.Phases[0], opts)
	if err != nil {
		t.Fatalf("runWithRetry: %v", err)
	}

	ps := state.Meta().Phases["impl"]
	if ps.ModelUsed != "global-model" {
		t.Errorf("ModelUsed = %q, want %q — failures after fallback should be attributed to global model", ps.ModelUsed, "global-model")
	}
	// 3 parse failures total (1 before fallback + 2 after).
	if ps.ParseAttempts != 3 {
		t.Errorf("ParseAttempts = %d, want 3", ps.ParseAttempts)
	}
}
