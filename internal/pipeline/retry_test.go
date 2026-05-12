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
