package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
)

func TestEngine_HappyPathAllPhasesComplete(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage: automatable",
					CostUSD: 0.10,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["task1","task2"]}`),
					RawText: "Plan: two tasks",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Both phases should be completed.
	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}

	// Costs should be accumulated.
	if !approxEqual(state.Meta().TotalCost, 0.30) {
		t.Errorf("TotalCost = %v, want 0.30", state.Meta().TotalCost)
	}

	// Check events: engine_started, engine_completed should be present.
	hasStarted := false
	hasCompleted := false
	for _, e := range events {
		if e.Kind == EventEngineStarted {
			hasStarted = true
		}
		if e.Kind == EventEngineCompleted {
			hasCompleted = true
		}
	}
	if !hasStarted {
		t.Error("engine_started event not emitted")
	}
	if !hasCompleted {
		t.Error("engine_completed event not emitted")
	}

	// Verify runner was called twice.
	if len(mock.calls) != 2 {
		t.Errorf("runner called %d times, want 2", len(mock.calls))
	}
}

func TestEngine_SkipsCompletedPhases(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["task1"]}`),
					RawText: "Plan output",
					CostUSD: 0.15,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock)

	// Pre-complete triage.
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteResult("triage", json.RawMessage(`{"automatable":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteArtifact("triage", []byte("Triage done")); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkCompleted("triage"); err != nil {
		t.Fatal(err)
	}

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Only plan should have been called.
	if len(mock.calls) != 1 {
		t.Errorf("runner called %d times, want 1", len(mock.calls))
	}
	if mock.calls[0].Phase != "plan" {
		t.Errorf("runner called for %q, want %q", mock.calls[0].Phase, "plan")
	}
}

func TestEngine_DependencyNotMet(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				err: &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("connection reset")},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from failed triage")
	}

	// Triage should be failed, plan should never have run.
	if state.IsCompleted("triage") {
		t.Error("triage should NOT be completed")
	}
	if state.IsCompleted("plan") {
		t.Error("plan should NOT be completed")
	}

	// Runner should have been called only for triage.
	if len(mock.calls) != 1 {
		t.Errorf("runner called %d times, want 1", len(mock.calls))
	}
}

func TestEngine_TransientRetryWithBackoff(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 3, Parse: 0, Semantic: 0},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {
				{err: &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("fail1")}},
				{err: &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("fail2")}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "success",
					CostUSD: 0.05,
				}},
			},
		},
	}

	var sleepDurations []time.Duration
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.SleepFunc = func(d time.Duration) {
			sleepDurations = append(sleepDurations, d)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed after retries")
	}

	// Should have slept twice (before retry 1 and retry 2).
	if len(sleepDurations) != 2 {
		t.Fatalf("sleepFunc called %d times, want 2", len(sleepDurations))
	}

	// Second sleep should be >= first (exponential backoff).
	if sleepDurations[1] < sleepDurations[0] {
		t.Errorf("backoff not increasing: %v then %v", sleepDurations[0], sleepDurations[1])
	}

	// Runner should have been called 3 times.
	if len(mock.calls) != 3 {
		t.Errorf("runner called %d times, want 3", len(mock.calls))
	}
}

func TestEngine_ParseRetryAppendsError(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 0, Parse: 1, Semantic: 0},
		},
	}

	parseErr := &runner.ParseError{
		Err: fmt.Errorf("expected JSON object"),
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {
				{err: parseErr},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "success",
					CostUSD: 0.05,
				}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed after parse retry")
	}

	// The second call should have the error context appended to UserPrompt.
	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(mock.calls))
	}
	retryPrompt := mock.calls[1].UserPrompt
	if retryPrompt == "" {
		t.Error("retry call should have error context in UserPrompt")
	}
	if !strings.Contains(retryPrompt, "RETRY") {
		t.Errorf("retry prompt should contain RETRY marker, got: %q", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "parse error") {
		t.Errorf("retry prompt should mention parse error, got: %q", retryPrompt)
	}
}

func TestEngine_MaxRetriesExhausted(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 2, Parse: 0, Semantic: 0},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {
				{err: &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("fail1")}},
				{err: &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("fail2")}},
				{err: &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("fail3")}},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error after max retries exhausted")
	}

	// Phase should be marked failed.
	ps := state.Meta().Phases["triage"]
	if ps == nil {
		t.Fatal("triage phase state should exist")
	}
	if ps.Status != PhaseFailed {
		t.Errorf("triage status = %q, want %q", ps.Status, PhaseFailed)
	}

	// Runner should have been called 3 times (initial + 2 retries).
	if len(mock.calls) != 3 {
		t.Errorf("runner called %d times, want 3", len(mock.calls))
	}
}

func TestEngine_CheckpointMode(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.10,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["t1"]}`),
					RawText: "Plan done",
					CostUSD: 0.20,
				},
			}},
		},
	}

	// checkpointReached signals the confirming goroutine that the engine
	// has emitted a checkpoint_pause and is now blocked waiting for Confirm().
	// Buffered so the synchronous OnEvent callback never blocks.
	checkpointReached := make(chan struct{}, len(phases))

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Mode = Checkpoint
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
			if e.Kind == EventCheckpointPause {
				checkpointReached <- struct{}{}
			}
		}
	})

	// Auto-confirm from a goroutine, waiting for each checkpoint_pause
	// before calling Confirm() to avoid timing-fragile fire-and-forget sends.
	go func() {
		for i := 0; i < len(phases); i++ {
			<-checkpointReached
			engine.Confirm()
		}
	}()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}

	// Count checkpoint_pause events.
	checkpointCount := 0
	for _, e := range events {
		if e.Kind == EventCheckpointPause {
			checkpointCount++
		}
	}
	if checkpointCount != 2 {
		t.Errorf("checkpoint_pause events = %d, want 2", checkpointCount)
	}
}

func TestEngine_ContextCancellation(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{}`),
					RawText: "ok",
					CostUSD: 0.01,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := engine.Run(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("error should mention context, got: %v", err)
	}
}

func TestEngine_MonitorStub(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "monitor",
			Prompt: "monitor.md",
			Type:   "polling",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Monitor should be completed.
	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed")
	}

	// Runner should NOT have been called.
	if len(mock.calls) != 0 {
		t.Errorf("runner called %d times, want 0 for monitor stub", len(mock.calls))
	}

	// Should have monitor_skipped event.
	hasMonitorSkipped := false
	for _, e := range events {
		if e.Kind == EventMonitorSkipped {
			hasMonitorSkipped = true
		}
	}
	if !hasMonitorSkipped {
		t.Error("monitor_skipped event not emitted")
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"context_canceled", context.Canceled, "context"},
		{"context_deadline", context.DeadlineExceeded, "context"},
		{"transient", &runner.TransientError{Reason: "timeout", Err: fmt.Errorf("x")}, "transient"},
		{"parse", &runner.ParseError{Err: fmt.Errorf("x")}, "parse"},
		{"semantic", &runner.SemanticError{Message: "bad"}, "semantic"},
		{"unknown", fmt.Errorf("something else"), "unknown"},
		{"wrapped_transient", fmt.Errorf("wrap: %w", &runner.TransientError{Reason: "r", Err: fmt.Errorf("x")}), "transient"},
		{"sandbox_oom_as_transient", &runner.TransientError{Reason: "oom", Err: fmt.Errorf("sandbox: OOM killed")}, "transient"},
		{"sandbox_signal_as_transient", &runner.TransientError{Reason: "signal", Err: fmt.Errorf("sandbox: killed by signal 15")}, "transient"},
		{"sandbox_exit_as_transient", &runner.TransientError{Reason: "exit_code", Err: fmt.Errorf("sandbox: exited with code 1")}, "transient"},
		{"wrapped_sandbox_transient", fmt.Errorf("wrap: %w", &runner.TransientError{Reason: "oom", Err: fmt.Errorf("sandbox: OOM")}), "transient"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyError(tt.err)
			if got != tt.want {
				t.Errorf("classifyError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	noJitter := func(time.Duration) time.Duration { return 0 }

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 2 * time.Second},  // 2^0 * 2s = 2s
		{1, 4 * time.Second},  // 2^1 * 2s = 4s
		{2, 8 * time.Second},  // 2^2 * 2s = 8s
		{3, 16 * time.Second}, // 2^3 * 2s = 16s
		{4, 30 * time.Second}, // 2^4 * 2s = 32s, capped at 30s
		{5, 30 * time.Second}, // capped
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tt.attempt), func(t *testing.T) {
			got := backoff(tt.attempt, noJitter)
			if got != tt.want {
				t.Errorf("backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestEngine_Resume(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "implement",
			Prompt:    "implement.md",
			DependsOn: []string{"plan"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["t1"]}`),
					RawText: "Plan done",
					CostUSD: 0.10,
				},
			}},
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"commits":1}`),
					RawText: "Impl done",
					CostUSD: 0.50,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock)

	// Pre-complete triage.
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteResult("triage", json.RawMessage(`{"automatable":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteArtifact("triage", []byte("Triage done")); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkCompleted("triage"); err != nil {
		t.Fatal(err)
	}

	// Resume from plan.
	if err := engine.Resume(context.Background(), "plan"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}
	if !state.IsCompleted("implement") {
		t.Error("implement should be completed")
	}

	// Triage should not have been called.
	for _, call := range mock.calls {
		if call.Phase == "triage" {
			t.Error("triage should not have been called on Resume from plan")
		}
	}
}

func TestEngine_SkipPlanRouting_SkipsPlanPhaseWhenTriageSetSkipPlan(t *testing.T) {
	// When triage sets skip_plan=true and the ticket has an ExistingPlan,
	// the engine should skip the plan LLM call, write the ExistingPlan as
	// the plan artifact, and proceed to implement.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			DependsOn: []string{"triage"},
		},
		{
			Name:      "implement",
			Prompt:    "implement.md",
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			DependsOn: []string{"plan"},
		},
	}

	existingPlan := "## Tasks\n\n1. Task one\n2. Task two\n\n## Verification\n\nRun tests."

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true,"skip_plan":true}`),
					RawText: "Triage with skip_plan",
					CostUSD: 0.05,
				},
			}},
			// No "plan" response — plan phase should not run.
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "Implementation done",
					CostUSD: 0.10,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Ticket.ExistingPlan = existingPlan
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Plan phase should be completed.
	if !state.IsCompleted("plan") {
		t.Error("plan phase should be completed")
	}

	// Plan artifact should contain the ExistingPlan.
	artifact, err := state.ReadArtifact("plan")
	if err != nil {
		t.Fatalf("reading plan artifact: %v", err)
	}
	if string(artifact) != existingPlan {
		t.Errorf("plan artifact = %q, want %q", string(artifact), existingPlan)
	}

	// Runner should NOT have been called for "plan" phase.
	for _, call := range mock.calls {
		if call.Phase == "plan" {
			t.Error("plan phase runner should not have been called when skip_plan=true")
		}
	}

	// Implement should have been called and completed.
	if !state.IsCompleted("implement") {
		t.Error("implement should be completed")
	}

	// Check that plan_skipped_by_triage event was emitted.
	foundSkipEvent := false
	for _, ev := range events {
		if ev.Kind == EventPlanSkippedByTriage {
			foundSkipEvent = true
			if ev.Phase != "plan" {
				t.Errorf("skip event phase = %q, want %q", ev.Phase, "plan")
			}
			break
		}
	}
	if !foundSkipEvent {
		t.Error("expected plan_skipped_by_triage event")
	}
}

func TestEngine_SkipPlanRouting_NoSkipWhenSkipPlanFalse(t *testing.T) {
	// When triage does not set skip_plan (or sets it to false), the plan
	// phase should run normally.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			DependsOn: []string{"triage"},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage without skip_plan",
					CostUSD: 0.05,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":[{"id":"1","description":"do it"}]}`),
					RawText: "Plan from LLM",
					CostUSD: 0.08,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Ticket.ExistingPlan = "some plan content"
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Plan phase should have run via the runner.
	planCalled := false
	for _, call := range mock.calls {
		if call.Phase == "plan" {
			planCalled = true
		}
	}
	if !planCalled {
		t.Error("plan phase runner should have been called when skip_plan is not set")
	}

	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}
}

func TestEngine_SkipPlanRouting_NoSkipWhenExistingPlanEmpty(t *testing.T) {
	// When triage sets skip_plan=true but the ticket has no ExistingPlan,
	// the plan phase should run normally (skip_plan is ignored).
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			DependsOn: []string{"triage"},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true,"skip_plan":true}`),
					RawText: "Triage with skip_plan but no plan",
					CostUSD: 0.05,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":[{"id":"1","description":"do it"}]}`),
					RawText: "Plan from LLM",
					CostUSD: 0.08,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Ticket.ExistingPlan = "" // empty plan
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Plan phase should have run via the runner since ExistingPlan is empty.
	planCalled := false
	for _, call := range mock.calls {
		if call.Phase == "plan" {
			planCalled = true
		}
	}
	if !planCalled {
		t.Error("plan phase runner should have been called when ExistingPlan is empty")
	}

	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}
}

func TestEngine_SkipPlanRouting_PlanArtifactAvailableToImplement(t *testing.T) {
	// When skip_plan routes the plan, the implement phase should see the
	// plan content in its prompt (via Artifacts.Plan).
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			DependsOn: []string{"triage"},
		},
		{
			Name:      "implement",
			Prompt:    "implement.md",
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			DependsOn: []string{"plan"},
		},
	}

	existingPlan := "## Tasks\n\n1. Update engine.go\n2. Add tests"

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true,"skip_plan":true}`),
					RawText: "Triage output",
					CostUSD: 0.05,
				},
			}},
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "Implementation done",
					CostUSD: 0.10,
				},
			}},
		},
	}

	// Set up with custom implement template that renders {{.Artifacts.Plan}}.
	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	templates := map[string]string{
		"triage.md":    "Phase: triage\nTicket: {{.Ticket.Key}}\n",
		"plan.md":      "Phase: plan\nTicket: {{.Ticket.Key}}\n",
		"implement.md": "Phase: implement\nTicket: {{.Ticket.Key}}\nPlan:\n{{.Artifacts.Plan}}\n",
	}
	for name, content := range templates {
		if err := os.WriteFile(filepath.Join(promptDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write template %s: %v", name, err)
		}
	}

	state, err := LoadOrCreate(stateDir, "TEST-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	engine := NewEngine(mock, state, EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "TEST-1", Summary: "Test ticket", ExistingPlan: existingPlan},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// The implement phase should have received a prompt containing the plan.
	var implementOpts *runner.RunOpts
	for i, call := range mock.calls {
		if call.Phase == "implement" {
			implementOpts = &mock.calls[i]
			break
		}
	}
	if implementOpts == nil {
		t.Fatal("implement phase was not called")
	}

	if !strings.Contains(implementOpts.SystemPrompt, existingPlan) {
		t.Errorf("implement prompt should contain the existing plan, got: %s", implementOpts.SystemPrompt[:min(200, len(implementOpts.SystemPrompt))])
	}
}

func TestEngineFullLifecycle(t *testing.T) {
	// A realistic 4-phase pipeline: triage -> plan -> implement -> verify.
	// Each phase depends on the previous one. Prompt templates reference
	// upstream artifacts so we can verify artifact flow through the pipeline.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "implement",
			Prompt:    "implement.md",
			DependsOn: []string{"plan"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"plan", "implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	triageOutput := `{"automatable":true,"complexity":"medium","estimated_hours":4,"components":["api","auth"]}`
	planOutput := `{"tasks":[{"id":"T1","description":"Add auth middleware"},{"id":"T2","description":"Update API routes"}],"approach":"incremental"}`
	implementOutput := `{"tests_passed":true,"commits":2,"files_changed":["middleware.go","routes.go"]}`
	verifyOutput := `{"verdict":"PASS","test_results":{"passed":12,"failed":0},"coverage":85.5}`

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(triageOutput),
					RawText: "Triage analysis: medium complexity, auth and api components affected",
					CostUSD: 0.05,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(planOutput),
					RawText: "Plan: add auth middleware first, then update routes",
					CostUSD: 0.12,
				},
			}},
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(implementOutput),
					RawText: "Implementation complete: 2 commits, all tests passing",
					CostUSD: 1.50,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(verifyOutput),
					RawText: "Verification passed: 12/12 tests, 85.5% coverage",
					CostUSD: 0.30,
				},
			}},
		},
	}

	var events []Event
	// We use setupEngine but override prompt templates to include artifact references.
	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Write prompt templates that reference upstream artifacts.
	templates := map[string]string{
		"triage.md":    "Phase: triage\nTicket: {{.Ticket.Key}}\nSummary: {{.Ticket.Summary}}\n",
		"plan.md":      "Phase: plan\nTicket: {{.Ticket.Key}}\nTriage output:\n{{.Artifacts.Triage}}\n",
		"implement.md": "Phase: implement\nTicket: {{.Ticket.Key}}\nPlan:\n{{.Artifacts.Plan}}\n",
		"verify.md":    "Phase: verify\nTicket: {{.Ticket.Key}}\nPlan:\n{{.Artifacts.Plan}}\nImplementation:\n{{.Artifacts.Implement}}\n",
	}
	for name, content := range templates {
		if err := os.WriteFile(filepath.Join(promptDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write prompt %s: %v", name, err)
		}
	}

	state, err := LoadOrCreate(stateDir, "LIFECYCLE-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	pipeline := &PhasePipeline{Phases: phases}
	loader := NewPromptLoader(promptDir)
	cfg := EngineConfig{
		Pipeline:   pipeline,
		Loader:     loader,
		Ticket:     TicketData{Key: "LIFECYCLE-1", Summary: "Full lifecycle test"},
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

	// All 4 phases should be completed.
	for _, phaseName := range []string{"triage", "plan", "implement", "verify"} {
		if !state.IsCompleted(phaseName) {
			t.Errorf("phase %q should be completed", phaseName)
		}
	}

	// Total cost: 0.05 + 0.12 + 1.50 + 0.30 = 1.97
	if !approxEqual(state.Meta().TotalCost, 1.97) {
		t.Errorf("TotalCost = %v, want 1.97", state.Meta().TotalCost)
	}

	// Verify artifact flow: plan's SystemPrompt should contain triage's RawText.
	if len(mock.calls) != 4 {
		t.Fatalf("runner called %d times, want 4", len(mock.calls))
	}

	planPrompt := mock.calls[1].SystemPrompt
	triageRawText := "Triage analysis: medium complexity, auth and api components affected"
	if !strings.Contains(planPrompt, triageRawText) {
		t.Errorf("plan's prompt should contain triage RawText;\nprompt: %q\nwanted substring: %q", planPrompt, triageRawText)
	}

	// verify's SystemPrompt should contain both plan and implement RawTexts.
	verifyPrompt := mock.calls[3].SystemPrompt
	planRawText := "Plan: add auth middleware first, then update routes"
	implRawText := "Implementation complete: 2 commits, all tests passing"
	if !strings.Contains(verifyPrompt, planRawText) {
		t.Errorf("verify's prompt should contain plan RawText;\nprompt: %q\nwanted substring: %q", verifyPrompt, planRawText)
	}
	if !strings.Contains(verifyPrompt, implRawText) {
		t.Errorf("verify's prompt should contain implement RawText;\nprompt: %q\nwanted substring: %q", verifyPrompt, implRawText)
	}

	// Artifacts should be persisted to disk.
	for _, phaseName := range []string{"triage", "plan", "implement", "verify"} {
		artifact, err := state.ReadArtifact(phaseName)
		if err != nil {
			t.Errorf("ReadArtifact(%q): %v", phaseName, err)
			continue
		}
		if len(artifact) == 0 {
			t.Errorf("artifact for %q should not be empty", phaseName)
		}
	}

	// Results should be persisted to disk.
	for _, phaseName := range []string{"triage", "plan", "implement", "verify"} {
		result, err := state.ReadResult(phaseName)
		if err != nil {
			t.Errorf("ReadResult(%q): %v", phaseName, err)
			continue
		}
		if len(result) == 0 {
			t.Errorf("result for %q should not be empty", phaseName)
		}
	}

	// Events should include engine lifecycle events.
	hasStarted := false
	hasCompleted := false
	for _, e := range events {
		if e.Kind == EventEngineStarted {
			hasStarted = true
		}
		if e.Kind == EventEngineCompleted {
			hasCompleted = true
		}
	}
	if !hasStarted {
		t.Error("engine_started event not emitted")
	}
	if !hasCompleted {
		t.Error("engine_completed event not emitted")
	}
}

func TestEnginePhaseGating(t *testing.T) {
	t.Run("triage_not_automatable", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:   "triage",
				Prompt: "triage.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "plan",
				Prompt:    "plan.md",
				DependsOn: []string{"triage"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":false,"block_reason":"requires database migration"}`),
						RawText: "Not automatable: database migration needed",
						CostUSD: 0.05,
					},
				}},
			},
		}

		engine, _ := setupEngine(t, phases, mock)

		err := engine.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError for non-automatable ticket")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
		}
		if gateErr.Phase != "triage" {
			t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "triage")
		}
		if !strings.Contains(gateErr.Reason, "requires database migration") {
			t.Errorf("gate error reason should contain block_reason, got: %q", gateErr.Reason)
		}

		// Plan should NOT have been called.
		for _, call := range mock.calls {
			if call.Phase == "plan" {
				t.Error("plan should not have run when triage is not automatable")
			}
		}
	})

	t.Run("plan_empty_tasks", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:   "triage",
				Prompt: "triage.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "plan",
				Prompt:    "plan.md",
				DependsOn: []string{"triage"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Automatable",
						CostUSD: 0.05,
					},
				}},
				"plan": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tasks":[]}`),
						RawText: "No tasks identified",
						CostUSD: 0.08,
					},
				}},
			},
		}

		engine, _ := setupEngine(t, phases, mock)

		err := engine.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError for empty tasks")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
		}
		if gateErr.Phase != "plan" {
			t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "plan")
		}
		if !strings.Contains(gateErr.Reason, "no tasks") {
			t.Errorf("gate error reason should mention 'no tasks', got: %q", gateErr.Reason)
		}
	})

	t.Run("verify_fail_verdict", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:   "triage",
				Prompt: "triage.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "plan",
				Prompt:    "plan.md",
				DependsOn: []string{"triage"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "implement",
				Prompt:    "implement.md",
				DependsOn: []string{"plan"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "verify",
				Prompt:    "verify.md",
				DependsOn: []string{"plan", "implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Automatable",
						CostUSD: 0.05,
					},
				}},
				"plan": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tasks":[{"id":"T1","description":"fix it"}]}`),
						RawText: "Plan: fix the issue",
						CostUSD: 0.10,
					},
				}},
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Implementation done",
						CostUSD: 0.80,
					},
				}},
				"verify": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix the test"]}`),
						RawText: "Verification failed",
						CostUSD: 0.15,
					},
				}},
			},
		}

		engine, _ := setupEngine(t, phases, mock)

		err := engine.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError for verify FAIL verdict")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
		}
		if gateErr.Phase != "verify" {
			t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "verify")
		}
		if !strings.Contains(gateErr.Reason, "fix the test") {
			t.Errorf("gate error reason should contain fix message, got: %q", gateErr.Reason)
		}
	})

	t.Run("semantic_retry_appends_message", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:   "triage",
				Prompt: "triage.md",
				Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {
					{err: &runner.SemanticError{Message: "output incomplete"}},
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Triage complete",
						CostUSD: 0.05,
					}},
				},
			},
		}

		engine, state := setupEngine(t, phases, mock)

		if err := engine.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}

		if !state.IsCompleted("triage") {
			t.Error("triage should be completed after semantic retry")
		}

		// Runner should have been called twice.
		if len(mock.calls) != 2 {
			t.Fatalf("runner called %d times, want 2", len(mock.calls))
		}

		// The retry call's UserPrompt should contain the semantic error message.
		retryPrompt := mock.calls[1].UserPrompt
		if !strings.Contains(retryPrompt, "output incomplete") {
			t.Errorf("retry UserPrompt should contain semantic error message;\ngot: %q", retryPrompt)
		}
		if !strings.Contains(retryPrompt, "RETRY") {
			t.Errorf("retry UserPrompt should contain RETRY marker;\ngot: %q", retryPrompt)
		}
	})
}

func TestEngine_ResumeRerunsCompletedPhase(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage v2",
					CostUSD: 0.30,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":[{"id":"T1"}]}`),
					RawText: "Plan v2",
					CostUSD: 0.40,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock)

	// Pre-complete both triage and plan.
	state.MarkRunning("triage")
	state.WriteResult("triage", json.RawMessage(`{"automatable":true}`))
	state.WriteArtifact("triage", []byte("Triage v1"))
	state.MarkCompleted("triage")

	state.MarkRunning("plan")
	state.WriteResult("plan", json.RawMessage(`{"tasks":[{"id":"old"}]}`))
	state.WriteArtifact("plan", []byte("Plan v1"))
	state.MarkCompleted("plan")

	// Resume from triage — should re-run triage even though completed.
	if err := engine.Resume(context.Background(), "triage"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// Triage should have been called (re-run).
	triageCalled := false
	for _, call := range mock.calls {
		if call.Phase == "triage" {
			triageCalled = true
		}
	}
	if !triageCalled {
		t.Error("Resume should re-run the fromPhase even if completed")
	}

	// Artifact should be updated to v2.
	artifact, err := state.ReadArtifact("triage")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(artifact) != "Triage v2" {
		t.Errorf("triage artifact = %q, want %q", string(artifact), "Triage v2")
	}
}

func TestEngine_WorktreeCreatedBeforeFirstPhase(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.05,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["t1"]}`),
					RawText: "Plan done",
					CostUSD: 0.10,
				},
			}},
		},
	}

	// Set up a real git repo so CreateWorktree works.
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	wtBase := filepath.Join(repoDir, ".worktrees")

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.WorkDir = repoDir
		cfg.WorktreeBase = wtBase
		cfg.BaseBranch = "main"
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Worktree should be recorded in state.
	if state.Meta().Worktree == "" {
		t.Fatal("worktree path should be set in state")
	}
	if state.Meta().Branch != "soda/TEST-1" {
		t.Errorf("branch = %q, want %q", state.Meta().Branch, "soda/TEST-1")
	}

	// EventWorktreeCreated must come before EventEngineStarted.
	wtIdx := -1
	startIdx := -1
	for i, e := range events {
		if e.Kind == EventWorktreeCreated && wtIdx == -1 {
			wtIdx = i
		}
		if e.Kind == EventEngineStarted && startIdx == -1 {
			startIdx = i
		}
	}
	if wtIdx == -1 {
		t.Fatal("worktree_created event not emitted")
	}
	if startIdx == -1 {
		t.Fatal("engine_started event not emitted")
	}
	if wtIdx >= startIdx {
		t.Errorf("worktree_created (idx %d) should come before engine_started (idx %d)", wtIdx, startIdx)
	}

	// All phases should have received the worktree as WorkDir.
	for _, call := range mock.calls {
		if call.WorkDir != state.Meta().Worktree {
			t.Errorf("phase %s WorkDir = %q, want %q", call.Phase, call.WorkDir, state.Meta().Worktree)
		}
	}
}

func TestEngine_ResumeCreatesWorktreeIfMissing(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.05,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["t1"]}`),
					RawText: "Plan done",
					CostUSD: 0.10,
				},
			}},
		},
	}

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	wtBase := filepath.Join(repoDir, ".worktrees")

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.WorkDir = repoDir
		cfg.WorktreeBase = wtBase
		cfg.BaseBranch = "main"
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Resume(context.Background(), "triage"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// Worktree should have been created.
	if state.Meta().Worktree == "" {
		t.Fatal("worktree path should be set in state after Resume")
	}

	hasWT := false
	for _, e := range events {
		if e.Kind == EventWorktreeCreated {
			hasWT = true
		}
	}
	if !hasWT {
		t.Error("worktree_created event not emitted during Resume")
	}
}

func TestEngine_ResumeWithExistingWorktreeSkipsCreation(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.05,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.WorktreeBase = "/some/base"
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	// Pre-set worktree in state to simulate a previous run.
	state.Meta().Worktree = "/existing/worktree"
	state.Meta().Branch = "soda/TEST-1"

	if err := engine.Resume(context.Background(), "triage"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// Should NOT have emitted worktree_created (already exists).
	for _, e := range events {
		if e.Kind == EventWorktreeCreated {
			t.Error("worktree_created should not be emitted when worktree already exists")
		}
	}

	// WorkDir for triage should be the existing worktree.
	if len(mock.calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(mock.calls))
	}
	if mock.calls[0].WorkDir != "/existing/worktree" {
		t.Errorf("WorkDir = %q, want %q", mock.calls[0].WorkDir, "/existing/worktree")
	}
}

func TestEngine_BuildPromptDataIncludesConfigAndContext(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.05,
				},
			}},
		},
	}

	// Write a prompt template that renders Config and Context fields.
	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	tmpl := `Formatter: {{.Config.Formatter}}
TestCommand: {{.Config.TestCommand}}
Forge: {{.Config.Repo.Forge}}
PushTo: {{.Config.Repo.PushTo}}
Target: {{.Config.Repo.Target}}
{{range .Config.VerifyCommands}}Verify: {{.}}
{{end}}ProjectContext: {{.Context.ProjectContext}}
RepoConventions: {{.Context.RepoConventions}}
`
	if err := os.WriteFile(filepath.Join(promptDir, "triage.md"), []byte(tmpl), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	state, err := LoadOrCreate(stateDir, "CFG-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	cfg := EngineConfig{
		Pipeline: &PhasePipeline{Phases: phases},
		Loader:   NewPromptLoader(promptDir),
		Ticket:   TicketData{Key: "CFG-1", Summary: "Config context test"},
		PromptConfig: PromptConfigData{
			Formatter:      "gofmt",
			TestCommand:    "go test ./...",
			VerifyCommands: []string{"make lint", "make test"},
			Repo: RepoConfig{
				Forge:  "github",
				PushTo: "origin",
				Target: "main",
			},
		},
		PromptContext: ContextData{
			ProjectContext:  "Go CLI tool for automated development",
			RepoConventions: "Use table-driven tests",
		},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(mock.calls))
	}

	rendered := mock.calls[0].SystemPrompt

	for _, want := range []string{
		"Formatter: gofmt",
		"TestCommand: go test ./...",
		"Forge: github",
		"PushTo: origin",
		"Target: main",
		"Verify: make lint",
		"Verify: make test",
		"ProjectContext: Go CLI tool for automated development",
		"RepoConventions: Use table-driven tests",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered prompt missing %q;\ngot: %s", want, rendered)
		}
	}
}

func TestEngine_BuildPromptDataIncludesDetectedStack(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.05,
				},
			}},
		},
	}

	// Write a prompt template that renders DetectedStack fields.
	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	tmpl := `Language: {{.DetectedStack.Language}}
Forge: {{.DetectedStack.Forge}}
Owner: {{.DetectedStack.Owner}}
Repo: {{.DetectedStack.Repo}}
{{range .DetectedStack.ContextFiles}}Context: {{.}}
{{end}}`
	if err := os.WriteFile(filepath.Join(promptDir, "triage.md"), []byte(tmpl), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	state, err := LoadOrCreate(stateDir, "DETECT-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	cfg := EngineConfig{
		Pipeline: &PhasePipeline{Phases: phases},
		Loader:   NewPromptLoader(promptDir),
		Ticket:   TicketData{Key: "DETECT-1", Summary: "Detect stack test"},
		DetectedStack: DetectedStackData{
			Language:     "go",
			Forge:        "github",
			Owner:        "decko",
			Repo:         "soda",
			ContextFiles: []string{"AGENTS.md"},
		},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(mock.calls))
	}

	rendered := mock.calls[0].SystemPrompt

	for _, want := range []string{
		"Language: go",
		"Forge: github",
		"Owner: decko",
		"Repo: soda",
		"Context: AGENTS.md",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered prompt missing %q;\ngot: %s", want, rendered)
		}
	}
}

func TestEngine_BuildPromptDataOmitsDetectedStackWhenEmpty(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.05,
				},
			}},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	tmpl := `{{- if .DetectedStack.Language}}HAS_LANG{{- end}}REST`
	if err := os.WriteFile(filepath.Join(promptDir, "triage.md"), []byte(tmpl), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	state, err := LoadOrCreate(stateDir, "DETECT-2")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	cfg := EngineConfig{
		Pipeline: &PhasePipeline{Phases: phases},
		Loader:   NewPromptLoader(promptDir),
		Ticket:   TicketData{Key: "DETECT-2", Summary: "No detect test"},
		// DetectedStack intentionally left as zero value
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rendered := mock.calls[0].SystemPrompt
	if strings.Contains(rendered, "HAS_LANG") {
		t.Errorf("rendered prompt should not contain HAS_LANG when DetectedStack is empty;\ngot: %s", rendered)
	}
	if !strings.Contains(rendered, "REST") {
		t.Errorf("rendered prompt should contain REST;\ngot: %s", rendered)
	}
}

func TestEngine_ResumeInvalidatesDownstreamPhases(t *testing.T) {
	// Pipeline: implement -> verify -> submit (linear dependency chain).
	// Bug: --from implement re-runs implement but skips verify/submit
	// if they were previously completed, even though their dependency changed.

	t.Run("from_implement_reruns_verify", func(t *testing.T) {
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
				Name:      "submit",
				Prompt:    "submit.md",
				DependsOn: []string{"verify"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
						RawText: "Impl v2",
						CostUSD: 0.50,
					},
				}},
				"verify": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS"}`),
						RawText: "Verify v2",
						CostUSD: 0.20,
					},
				}},
				"submit": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`),
						RawText: "Submit done",
						CostUSD: 0.10,
					},
				}},
			},
		}

		engine, state := setupEngine(t, phases, mock)

		// Pre-complete all three phases (simulate a previous run).
		for _, name := range []string{"implement", "verify", "submit"} {
			if err := state.MarkRunning(name); err != nil {
				t.Fatal(err)
			}
			if err := state.MarkCompleted(name); err != nil {
				t.Fatal(err)
			}
		}
		// Write verify result with PASS so gate doesn't block.
		if err := state.WriteResult("verify", json.RawMessage(`{"verdict":"PASS"}`)); err != nil {
			t.Fatal(err)
		}
		if err := state.WriteResult("submit", json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`)); err != nil {
			t.Fatal(err)
		}

		// Resume from implement — verify and submit should be re-run
		// because their dependency (implement) was re-run.
		if err := engine.Resume(context.Background(), "implement"); err != nil {
			t.Fatalf("Resume: %v", err)
		}

		// All three phases should have been called.
		if len(mock.calls) != 3 {
			t.Errorf("runner called %d times, want 3 (implement+verify+submit); got phases: %v",
				len(mock.calls), phaseNames(mock.calls))
		}

		verifyCalled := false
		submitCalled := false
		for _, call := range mock.calls {
			if call.Phase == "verify" {
				verifyCalled = true
			}
			if call.Phase == "submit" {
				submitCalled = true
			}
		}
		if !verifyCalled {
			t.Error("verify should be re-run when dependency (implement) was re-run")
		}
		if !submitCalled {
			t.Error("submit should be re-run when transitive dependency (implement) was re-run")
		}
	})

	t.Run("from_verify_reruns_submit_because_dep_reran", func(t *testing.T) {
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
				Name:      "submit",
				Prompt:    "submit.md",
				DependsOn: []string{"verify"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"verify": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS"}`),
						RawText: "Verify v2",
						CostUSD: 0.20,
					},
				}},
				"submit": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/2"}`),
						RawText: "Submit v2",
						CostUSD: 0.10,
					},
				}},
			},
		}

		engine, state := setupEngine(t, phases, mock)

		// Pre-complete all three phases.
		for _, name := range []string{"implement", "verify", "submit"} {
			if err := state.MarkRunning(name); err != nil {
				t.Fatal(err)
			}
			if err := state.MarkCompleted(name); err != nil {
				t.Fatal(err)
			}
		}
		if err := state.WriteResult("verify", json.RawMessage(`{"verdict":"PASS"}`)); err != nil {
			t.Fatal(err)
		}
		if err := state.WriteResult("submit", json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`)); err != nil {
			t.Fatal(err)
		}

		// Resume from verify — submit should also re-run because verify (its dep) was re-run.
		if err := engine.Resume(context.Background(), "verify"); err != nil {
			t.Fatalf("Resume: %v", err)
		}

		// verify (fromPhase) + submit (dependency re-ran) = 2 calls
		if len(mock.calls) != 2 {
			t.Errorf("runner called %d times, want 2 (verify+submit); got phases: %v",
				len(mock.calls), phaseNames(mock.calls))
		}
	})

	t.Run("skipped_phase_with_fail_gate_blocks", func(t *testing.T) {
		// Even when a phase is skipped (deps unchanged), its existing gate
		// result should be re-checked. A FAIL verify should block submit.
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
				Name:      "submit",
				Prompt:    "submit.md",
				DependsOn: []string{"verify"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true}`),
						RawText: "Impl done",
						CostUSD: 0.50,
					},
				}},
				"verify": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS"}`),
						RawText: "Verify done",
						CostUSD: 0.20,
					},
				}},
				"submit": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`),
						RawText: "Submit done",
						CostUSD: 0.10,
					},
				}},
			},
		}

		engine, state := setupEngine(t, phases, mock)

		// Run the full pipeline — verify will PASS, all phases complete.
		if err := engine.Run(context.Background()); err != nil {
			t.Fatalf("initial Run: %v", err)
		}

		// Now overwrite verify's result with a FAIL verdict (simulating
		// external corruption or a previous run that left stale state).
		if err := state.WriteResult("verify", json.RawMessage(`{"verdict":"FAIL","fixes_required":["tests broken"]}`)); err != nil {
			t.Fatal(err)
		}

		// Create a new engine with fresh mock for the resume.
		mock2 := &flexMockRunner{
			responses: map[string][]flexResponse{},
		}
		engine2 := NewEngine(mock2, state, engine.config)

		// Run() should re-check verify's gate even though it's "completed",
		// and block because the stored result is FAIL.
		err := engine2.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError for skipped phase with FAIL gate")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
		}
		if gateErr.Phase != "verify" {
			t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "verify")
		}
	})

	t.Run("verify_fail_then_resume_implement_reruns_verify", func(t *testing.T) {
		// Full scenario: verify FAIL → --from implement → implement gen 2 →
		// verify re-runs → PASS → submit runs.
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
				Name:      "submit",
				Prompt:    "submit.md",
				DependsOn: []string{"verify"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		// First run: implement succeeds, verify FAILs (gate blocks).
		mock1 := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl v1",
						CostUSD: 0.50,
					},
				}},
				"verify": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix the test"]}`),
						RawText: "Verify v1 FAIL",
						CostUSD: 0.15,
					},
				}},
			},
		}

		engine, state := setupEngine(t, phases, mock1)

		err := engine.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError from verify FAIL")
		}
		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got: %v", err)
		}

		// Second run: resume from implement → implement gen 2 → verify PASS → submit.
		mock2 := &flexMockRunner{
			responses: map[string][]flexResponse{
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
						RawText: "Impl v2",
						CostUSD: 0.60,
					},
				}},
				"verify": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS"}`),
						RawText: "Verify v2 PASS",
						CostUSD: 0.20,
					},
				}},
				"submit": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/2"}`),
						RawText: "Submit done",
						CostUSD: 0.10,
					},
				}},
			},
		}

		engine2 := NewEngine(mock2, state, engine.config)

		if err := engine2.Resume(context.Background(), "implement"); err != nil {
			t.Fatalf("Resume: %v", err)
		}

		// All three should have been called.
		if len(mock2.calls) != 3 {
			t.Errorf("runner called %d times, want 3; got phases: %v",
				len(mock2.calls), phaseNames(mock2.calls))
		}

		// verify should have gen 2 (re-run).
		verifyPS := state.Meta().Phases["verify"]
		if verifyPS == nil {
			t.Fatal("verify phase state should exist")
		}
		if verifyPS.Generation < 2 {
			t.Errorf("verify generation = %d, want >= 2", verifyPS.Generation)
		}
	})
}

func TestEngine_RunChecksGateOnSkippedPhases(t *testing.T) {
	// In Run(), completed phases should still have their gate checked.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{},
	}

	engine, state := setupEngine(t, phases, mock)

	// Pre-complete triage with a non-automatable result.
	if err := state.MarkRunning("triage"); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteResult("triage", json.RawMessage(`{"automatable":false,"block_reason":"needs human"}`)); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkCompleted("triage"); err != nil {
		t.Fatal(err)
	}

	// Run should check the gate on skipped triage and block.
	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseGateError for skipped phase with failing gate")
	}

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
	}
	if gateErr.Phase != "triage" {
		t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "triage")
	}

	// Runner should NOT have been called (blocked at triage gate).
	if len(mock.calls) != 0 {
		t.Errorf("runner called %d times, want 0", len(mock.calls))
	}
}

func TestEngine_ResumeImplementInjectsReworkFeedback(t *testing.T) {
	// When verify FAILs and we resume from implement, the implement prompt
	// should contain structured rework feedback with selective extraction.
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
	}

	verifyOutput := `{
		"verdict": "FAIL",
		"fixes_required": ["add missing test for edge case", "fix nil pointer in handler"],
		"criteria_results": [
			{"criterion": "handles valid input", "passed": true, "evidence": "test passes"},
			{"criterion": "handles nil input", "passed": false, "evidence": "panics on nil"}
		],
		"code_issues": [
			{"file": "handler.go", "line": 42, "severity": "critical", "issue": "nil deref"},
			{"file": "handler.go", "line": 100, "severity": "minor", "issue": "unused var"},
			{"file": "util.go", "line": 10, "severity": "major", "issue": "unchecked error"}
		],
		"command_results": [
			{"command": "go test ./...", "exit_code": 1, "output": "FAIL handler_test.go", "passed": false},
			{"command": "go vet ./...", "exit_code": 0, "output": "ok", "passed": true}
		]
	}`

	mock1 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(verifyOutput),
					RawText: "Verify report",
					CostUSD: 0.15,
				},
			}},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Template that renders ReworkFeedback fields.
	implTmpl := `Phase: implement
Ticket: {{.Ticket.Key}}
{{- if .ReworkFeedback}}
REWORK:
Verdict: {{.ReworkFeedback.Verdict}}
{{range .ReworkFeedback.FixesRequired}}Fix: {{.}}
{{end}}
{{- range .ReworkFeedback.FailedCriteria}}FailedCrit: {{.Criterion}} | {{.Evidence}}
{{end}}
{{- range .ReworkFeedback.CodeIssues}}Issue: {{.Severity}} {{.File}}:{{.Line}} {{.Issue}}
{{end}}
{{- range .ReworkFeedback.FailedCommands}}Cmd: {{.Command}} exit={{.ExitCode}}
{{end}}
{{- end}}
`
	verifyTmpl := "Phase: verify\nTicket: {{.Ticket.Key}}\n"

	if err := os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte(implTmpl), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "verify.md"), []byte(verifyTmpl), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadOrCreate(stateDir, "REWORK-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "REWORK-1", Summary: "Rework feedback test"},
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

	engine1 := NewEngine(mock1, state, cfg)

	// First run should fail at verify gate.
	runErr := engine1.Run(context.Background())
	if runErr == nil {
		t.Fatal("expected PhaseGateError from verify FAIL")
	}
	var gateErr *PhaseGateError
	if !errors.As(runErr, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", runErr)
	}

	// Resume from implement — rework feedback should be injected.
	events = nil
	mock2 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2 with fixes",
					CostUSD: 0.60,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "All checks pass",
					CostUSD: 0.20,
				},
			}},
		},
	}

	engine2 := NewEngine(mock2, state, cfg)

	if err := engine2.Resume(context.Background(), "implement"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if len(mock2.calls) < 1 {
		t.Fatal("expected at least 1 runner call")
	}

	implPrompt := mock2.calls[0].SystemPrompt

	// Fixes required should be present.
	if !strings.Contains(implPrompt, "Fix: add missing test for edge case") {
		t.Errorf("prompt should contain first fix;\ngot: %s", implPrompt)
	}
	if !strings.Contains(implPrompt, "Fix: fix nil pointer in handler") {
		t.Errorf("prompt should contain second fix;\ngot: %s", implPrompt)
	}

	// Only failed criteria should appear (not the passed one).
	if !strings.Contains(implPrompt, "FailedCrit: handles nil input | panics on nil") {
		t.Errorf("prompt should contain failed criterion;\ngot: %s", implPrompt)
	}
	if strings.Contains(implPrompt, "handles valid input") {
		t.Errorf("prompt should NOT contain passed criterion;\ngot: %s", implPrompt)
	}

	// Only critical/major code issues (not minor).
	if !strings.Contains(implPrompt, "Issue: critical handler.go:42 nil deref") {
		t.Errorf("prompt should contain critical issue;\ngot: %s", implPrompt)
	}
	if !strings.Contains(implPrompt, "Issue: major util.go:10 unchecked error") {
		t.Errorf("prompt should contain major issue;\ngot: %s", implPrompt)
	}
	if strings.Contains(implPrompt, "unused var") {
		t.Errorf("prompt should NOT contain minor issue;\ngot: %s", implPrompt)
	}

	// Only failed commands (not the passing go vet).
	if !strings.Contains(implPrompt, "Cmd: go test ./... exit=1") {
		t.Errorf("prompt should contain failed command;\ngot: %s", implPrompt)
	}
	if strings.Contains(implPrompt, "go vet") {
		t.Errorf("prompt should NOT contain passing command;\ngot: %s", implPrompt)
	}

	// Verdict should be present.
	if !strings.Contains(implPrompt, "Verdict: FAIL") {
		t.Errorf("prompt should contain FAIL verdict;\ngot: %s", implPrompt)
	}

	// Injection event should have been logged.
	hasInjectionEvent := false
	for _, e := range events {
		if e.Kind == EventReworkFeedbackInjected {
			hasInjectionEvent = true
			if e.Phase != "implement" {
				t.Errorf("injection event phase = %q, want %q", e.Phase, "implement")
			}
		}
	}
	if !hasInjectionEvent {
		t.Error("rework_feedback_injected event not emitted")
	}
}

func TestEngine_ImplementPromptNoReworkFeedbackOnFirstRun(t *testing.T) {
	// On the very first run (no verify result exists), ReworkFeedback should be nil.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
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
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	implTmpl := "Phase: implement\n{{if .ReworkFeedback}}FEEDBACK:yes{{end}}\n"
	if err := os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte(implTmpl), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadOrCreate(stateDir, "NOFB-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "NOFB-1"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(mock.calls))
	}

	implPrompt := mock.calls[0].SystemPrompt
	if strings.Contains(implPrompt, "FEEDBACK:") {
		t.Errorf("implement prompt should NOT contain rework feedback on first run;\ngot: %s", implPrompt)
	}
}

func TestEngine_ImplementPromptNoReworkFeedbackOnPass(t *testing.T) {
	// When verify passed previously, ReworkFeedback should be nil on resume.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
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
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	implTmpl := "Phase: implement\n{{if .ReworkFeedback}}FEEDBACK:yes{{end}}\n"
	if err := os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte(implTmpl), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadOrCreate(stateDir, "PASSFB-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Pre-populate a PASS verify result.
	if err := state.MarkRunning("verify"); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteResult("verify", json.RawMessage(`{"verdict":"PASS"}`)); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteArtifact("verify", []byte("All tests pass")); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkCompleted("verify"); err != nil {
		t.Fatal(err)
	}

	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "PASSFB-1"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Resume(context.Background(), "implement"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(mock.calls))
	}

	implPrompt := mock.calls[0].SystemPrompt
	if strings.Contains(implPrompt, "FEEDBACK:") {
		t.Errorf("implement prompt should NOT contain rework feedback when verdict was PASS;\ngot: %s", implPrompt)
	}
}

func TestEngine_ReworkFeedbackStalePlanSkipped(t *testing.T) {
	// When the plan has changed since verify ran, rework feedback should
	// be skipped and a warning event emitted.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			DependsOn:    []string{"plan"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v2",
					CostUSD: 0.50,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "All pass",
					CostUSD: 0.20,
				},
			}},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	implTmpl := "Phase: implement\n{{if .ReworkFeedback}}FEEDBACK:yes{{end}}\n"
	verifyTmpl := "Phase: verify\n"

	if err := os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte(implTmpl), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "verify.md"), []byte(verifyTmpl), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadOrCreate(stateDir, "STALE-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Write a plan artifact (version 1).
	if err := state.MarkRunning("plan"); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteResult("plan", json.RawMessage(`{"tasks":["t1"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteArtifact("plan", []byte("Plan version 1")); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkCompleted("plan"); err != nil {
		t.Fatal(err)
	}

	// Pre-populate a FAIL verify result with a plan_hash from the OLD plan.
	if err := state.MarkRunning("verify"); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteResult("verify", json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix it"]}`)); err != nil {
		t.Fatal(err)
	}
	// Store plan hash from "Plan version 1".
	state.Meta().Phases["verify"].PlanHash = fmt.Sprintf("%x", sha256.Sum256([]byte("Plan version 1")))
	if err := state.MarkCompleted("verify"); err != nil {
		t.Fatal(err)
	}

	// Now change the plan artifact (version 2) — simulates re-running plan.
	if err := state.WriteArtifact("plan", []byte("Plan version 2 - different")); err != nil {
		t.Fatal(err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "STALE-1"},
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

	if err := engine.Resume(context.Background(), "implement"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// Implement prompt should NOT contain feedback (stale plan).
	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 runner call")
	}
	implPrompt := mock.calls[0].SystemPrompt
	if strings.Contains(implPrompt, "FEEDBACK:") {
		t.Errorf("prompt should NOT contain rework feedback when plan is stale;\ngot: %s", implPrompt)
	}

	// Should have emitted a rework_feedback_skipped event.
	hasSkipEvent := false
	for _, e := range events {
		if e.Kind == EventReworkFeedbackSkipped {
			hasSkipEvent = true
			if reason, ok := e.Data["reason"].(string); !ok || !strings.Contains(reason, "plan changed") {
				t.Errorf("skip event reason should mention plan changed, got: %v", e.Data["reason"])
			}
		}
	}
	if !hasSkipEvent {
		t.Error("rework_feedback_skipped event not emitted for stale plan")
	}
}

func TestEngine_PromptLoadedEvents(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.01,
				},
			}},
		},
	}

	var events []Event
	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Find the prompt_loaded event for triage.
	var promptEvent *Event
	for i := range events {
		if events[i].Kind == EventPromptLoaded && events[i].Phase == "triage" {
			promptEvent = &events[i]
			break
		}
	}
	if promptEvent == nil {
		t.Fatal("expected prompt_loaded event for triage phase")
	}

	source, _ := promptEvent.Data["source"].(string)
	if !strings.HasSuffix(source, "triage.md") {
		t.Errorf("prompt source = %q, want path ending in triage.md", source)
	}

	// Single dir means not an override (it's the embedded default).
	isOverride, _ := promptEvent.Data["is_override"].(bool)
	if isOverride {
		t.Error("expected is_override = false for single-dir loader")
	}
}

func TestEngine_PromptLoadedFallbackEvent(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.01,
				},
			}},
		},
	}

	// Create override dir with an invalid template, and embedded dir with a good one.
	overrideDir := t.TempDir()
	embeddedDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(overrideDir, "triage.md"), []byte("{{.BogusField}}"), 0644); err != nil {
		t.Fatalf("WriteFile override triage.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(embeddedDir, "triage.md"), []byte("Phase: triage\nTicket: {{.Ticket.Key}}\n"), 0644); err != nil {
		t.Fatalf("WriteFile embedded triage.md: %v", err)
	}

	stateDir := t.TempDir()
	state, err := LoadOrCreate(stateDir, "FALLBACK-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(overrideDir, embeddedDir),
		Ticket:     TicketData{Key: "FALLBACK-1", Summary: "Fallback test"},
		Model:      "test-model",
		WorkDir:    t.TempDir(),
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Find the prompt_loaded event.
	var promptEvent *Event
	for i := range events {
		if events[i].Kind == EventPromptLoaded && events[i].Phase == "triage" {
			promptEvent = &events[i]
			break
		}
	}
	if promptEvent == nil {
		t.Fatal("expected prompt_loaded event for triage phase")
	}

	// Should indicate fallback happened.
	fallback, _ := promptEvent.Data["fallback"].(bool)
	if !fallback {
		t.Error("expected fallback = true in prompt_loaded event")
	}

	reason, _ := promptEvent.Data["fallback_reason"].(string)
	if reason == "" {
		t.Error("expected non-empty fallback_reason")
	}

	// Source should be the embedded dir, not the override.
	source, _ := promptEvent.Data["source"].(string)
	if !strings.Contains(source, embeddedDir) {
		t.Errorf("source = %q, want path in embedded dir %q", source, embeddedDir)
	}

	// Should NOT be marked as override (it fell back to embedded).
	isOverride, _ := promptEvent.Data["is_override"].(bool)
	if isOverride {
		t.Error("expected is_override = false after fallback to embedded dir")
	}

	// Phase should still complete successfully.
	if !state.IsCompleted("triage") {
		t.Error("triage should be completed after fallback")
	}
}

func TestEngine_PerPhaseModel(t *testing.T) {
	t.Run("phase_model_overrides_global", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:   "triage",
				Prompt: "triage.md",
				Model:  "claude-sonnet-4-6", // per-phase override
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "plan",
				Prompt:    "plan.md",
				DependsOn: []string{"triage"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				// no per-phase model — should use global
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Triage output",
						CostUSD: 0.10,
					},
				}},
				"plan": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tasks":["task1"]}`),
						RawText: "Plan output",
						CostUSD: 0.20,
					},
				}},
			},
		}

		engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
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

		models := map[string]string{}
		for _, c := range mock.calls {
			models[c.Phase] = c.Model
		}

		// triage has per-phase model override.
		if models["triage"] != "claude-sonnet-4-6" {
			t.Errorf("triage model = %q, want %q", models["triage"], "claude-sonnet-4-6")
		}
		// plan has no per-phase model — should fall back to global.
		if models["plan"] != "claude-opus-4-6" {
			t.Errorf("plan model = %q, want %q (global fallback)", models["plan"], "claude-opus-4-6")
		}
	})

	t.Run("empty_phase_model_uses_global", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:   "triage",
				Prompt: "triage.md",
				Model:  "", // explicitly empty
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Triage output",
						CostUSD: 0.10,
					},
				}},
			},
		}

		engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.Model = "claude-opus-4-6"
		})

		if err := engine.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}

		mock.mu.Lock()
		defer mock.mu.Unlock()

		if len(mock.calls) != 1 {
			t.Fatalf("runner called %d times, want 1", len(mock.calls))
		}
		if mock.calls[0].Model != "claude-opus-4-6" {
			t.Errorf("model = %q, want %q (global)", mock.calls[0].Model, "claude-opus-4-6")
		}
	})
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

func TestEngine_ReviewReworkDefaultMaxCycles(t *testing.T) {
	// Verify that the default max rework cycles is 2.
	cfg := EngineConfig{}
	if cfg.maxReworkCycles() != DefaultMaxReworkCycles {
		t.Errorf("default maxReworkCycles() = %d, want %d", cfg.maxReworkCycles(), DefaultMaxReworkCycles)
	}
	if DefaultMaxReworkCycles != 2 {
		t.Errorf("DefaultMaxReworkCycles = %d, want 2", DefaultMaxReworkCycles)
	}
}

func TestEngine_remoteName(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		// When PushTo is empty, remoteName() should return "origin".
		stateDir := t.TempDir()
		state, err := LoadOrCreate(stateDir, "REMOTE-1")
		if err != nil {
			t.Fatalf("LoadOrCreate: %v", err)
		}
		e := &Engine{config: EngineConfig{}, state: state}
		if got := e.remoteName(); got != "origin" {
			t.Errorf("remoteName() = %q, want %q", got, "origin")
		}
	})

	t.Run("configured", func(t *testing.T) {
		// When PushTo is set, remoteName() should return the configured value.
		stateDir := t.TempDir()
		state, err := LoadOrCreate(stateDir, "REMOTE-2")
		if err != nil {
			t.Fatalf("LoadOrCreate: %v", err)
		}
		e := &Engine{
			config: EngineConfig{
				PromptConfig: PromptConfigData{
					Repo: RepoConfig{PushTo: "upstream"},
				},
			},
			state: state,
		}
		if got := e.remoteName(); got != "upstream" {
			t.Errorf("remoteName() = %q, want %q", got, "upstream")
		}
	})
}

func TestEngine_ReviewReworkCyclesPersisted(t *testing.T) {
	// Verify rework cycles are persisted to meta.json across engine restarts.
	stateDir := t.TempDir()
	state, err := LoadOrCreate(stateDir, "PERSIST-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	state.Meta().ReworkCycles = 3
	if err := state.flushMeta(); err != nil {
		t.Fatalf("flushMeta: %v", err)
	}

	// Re-read state from disk.
	state2, err := LoadOrCreate(stateDir, "PERSIST-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	if state2.Meta().ReworkCycles != 3 {
		t.Errorf("ReworkCycles after reload = %d, want 3", state2.Meta().ReworkCycles)
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

// TestEngine_PhaseLifecycleEvents verifies that the engine emits exactly one
// phase_started and one phase_completed (or phase_failed) event per phase, and
// that those events carry the expected data fields. This is the core acceptance
// criterion for ticket #152: the engine is the single source of phase lifecycle
// event logging.
func TestEngine_PhaseLifecycleEvents(t *testing.T) {
	t.Run("completed_phase_emits_started_and_completed", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:   "triage",
				Prompt: "triage.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Triage output",
						CostUSD: 0.25,
					},
				}},
			},
		}

		var events []Event
		engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) {
				events = append(events, e)
			}
		})

		if err := engine.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}

		// Count phase lifecycle events for "triage".
		var started, completed, failed int
		var startedEv, completedEv Event
		for _, ev := range events {
			if ev.Phase != "triage" {
				continue
			}
			switch ev.Kind {
			case EventPhaseStarted:
				started++
				startedEv = ev
			case EventPhaseCompleted:
				completed++
				completedEv = ev
			case EventPhaseFailed:
				failed++
			}
		}

		if started != 1 {
			t.Errorf("phase_started count = %d, want 1", started)
		}
		if completed != 1 {
			t.Errorf("phase_completed count = %d, want 1", completed)
		}
		if failed != 0 {
			t.Errorf("phase_failed count = %d, want 0", failed)
		}

		// phase_started must include generation.
		if gen, ok := startedEv.Data["generation"]; !ok {
			t.Error("phase_started missing 'generation' field")
		} else if toFloat64(gen) != 1 {
			t.Errorf("phase_started generation = %v, want 1", gen)
		}

		// phase_completed must include duration_ms and cost.
		if _, ok := completedEv.Data["duration_ms"]; !ok {
			t.Error("phase_completed missing 'duration_ms' field")
		}
		if cost, ok := completedEv.Data["cost"]; !ok {
			t.Error("phase_completed missing 'cost' field")
		} else if !approxEqual(toFloat64(cost), 0.25) {
			t.Errorf("phase_completed cost = %v, want 0.25", cost)
		}
	})

	t.Run("failed_phase_emits_started_and_failed", func(t *testing.T) {
		phases := []PhaseConfig{
			{
				Name:   "plan",
				Prompt: "plan.md",
				Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				"plan": {{
					err: fmt.Errorf("LLM call failed"),
				}},
			},
		}

		var events []Event
		engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) {
				events = append(events, e)
			}
		})

		_ = engine.Run(context.Background()) // expected to fail

		// Count phase lifecycle events for "plan".
		var started, completed, failed int
		var startedEv, failedEv Event
		for _, ev := range events {
			if ev.Phase != "plan" {
				continue
			}
			switch ev.Kind {
			case EventPhaseStarted:
				started++
				startedEv = ev
			case EventPhaseCompleted:
				completed++
			case EventPhaseFailed:
				failed++
				failedEv = ev
			}
		}

		if started != 1 {
			t.Errorf("phase_started count = %d, want 1", started)
		}
		if completed != 0 {
			t.Errorf("phase_completed count = %d, want 0", completed)
		}
		if failed != 1 {
			t.Errorf("phase_failed count = %d, want 1", failed)
		}

		// phase_started must include generation.
		if _, ok := startedEv.Data["generation"]; !ok {
			t.Error("phase_started missing 'generation' field")
		}

		// phase_failed must include error, duration_ms, and cost.
		if _, ok := failedEv.Data["error"]; !ok {
			t.Error("phase_failed missing 'error' field")
		}
		if _, ok := failedEv.Data["duration_ms"]; !ok {
			t.Error("phase_failed missing 'duration_ms' field")
		}
		if _, ok := failedEv.Data["cost"]; !ok {
			t.Error("phase_failed missing 'cost' field")
		}
	})
}

func TestEngine_OutputChunkEvents(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &chunkMockRunner{
		flexMockRunner: flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Triage done",
						CostUSD: 0.10,
					},
				}},
			},
		},
		chunks: map[string][]string{
			"triage": {"Analyzing ticket...", "Classification: small"},
		},
	}

	var events []Event
	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify output_chunk events were emitted.
	var chunkLines []string
	for _, ev := range events {
		if ev.Kind == EventOutputChunk {
			if line, ok := ev.Data["line"].(string); ok {
				chunkLines = append(chunkLines, line)
			}
			if ev.Phase != "triage" {
				t.Errorf("output_chunk event has phase %q, want %q", ev.Phase, "triage")
			}
		}
	}

	if len(chunkLines) != 2 {
		t.Fatalf("got %d output_chunk events, want 2", len(chunkLines))
	}
	if chunkLines[0] != "Analyzing ticket..." {
		t.Errorf("chunk[0] = %q, want %q", chunkLines[0], "Analyzing ticket...")
	}
	if chunkLines[1] != "Classification: small" {
		t.Errorf("chunk[1] = %q, want %q", chunkLines[1], "Classification: small")
	}
}

func TestEngine_PauseSignalBlocksBetweenPhases(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.10,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["task1"]}`),
					RawText: "Plan done",
					CostUSD: 0.20,
				},
			}},
		},
	}

	pauseCh := make(chan bool, 10)
	var events []Event
	var mu sync.Mutex

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.PauseSignal = pauseCh
		cfg.OnEvent = func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	})

	// Send pause signal before running
	pauseCh <- true

	// Start engine in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(context.Background())
	}()

	// Give the engine time to run triage and hit the pause point.
	time.Sleep(100 * time.Millisecond)

	// Unpause
	pauseCh <- false

	// Engine should complete
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not complete after unpause")
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}
}

func TestEngine_PauseSignalContextCancel(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.10,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["task1"]}`),
					RawText: "Plan done",
					CostUSD: 0.20,
				},
			}},
		},
	}

	pauseCh := make(chan bool, 10)
	ctx, cancel := context.WithCancel(context.Background())

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.PauseSignal = pauseCh
	})

	// Pause before run
	pauseCh <- true

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	// Wait briefly, then cancel context while paused
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from context cancellation")
		}
		if !strings.Contains(err.Error(), "context cancelled") && !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("expected context-related error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not exit after context cancel")
	}
}

func TestEngine_PauseBlocksOutputStreaming(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	// Custom runner that calls OnChunk and checks pause behavior.
	blockingRunner := &blockingChunkRunner{
		result: &runner.RunResult{
			Output:  json.RawMessage(`{"automatable":true}`),
			RawText: "Triage done",
			CostUSD: 0.10,
		},
		chunks: []string{"line1", "line2", "line3"},
	}

	var chunksMu sync.Mutex
	var receivedChunks []string

	engine, _ := setupEngine(t, phases, blockingRunner, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			if e.Kind == EventOutputChunk {
				if line, ok := e.Data["line"].(string); ok {
					chunksMu.Lock()
					receivedChunks = append(receivedChunks, line)
					chunksMu.Unlock()
				}
			}
		}
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(context.Background())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not complete")
	}

	chunksMu.Lock()
	defer chunksMu.Unlock()
	if len(receivedChunks) != 3 {
		t.Errorf("got %d chunks, want 3: %v", len(receivedChunks), receivedChunks)
	}
}

func TestEngine_NilPauseSignalNoOp(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &chunkMockRunner{
		flexMockRunner: flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Triage done",
						CostUSD: 0.10,
					},
				}},
			},
		},
		chunks: map[string][]string{
			"triage": {"line1"},
		},
	}

	// No PauseSignal configured — should work without issue.
	engine, state := setupEngine(t, phases, mock)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
}

func TestEngine_OnChunkPassedToRunner(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	// Verify that RunOpts.OnChunk is set when the runner is invoked.
	var capturedOnChunk func(string)
	capturingRunner := &capturingChunkRunner{
		result: &runner.RunResult{
			Output:  json.RawMessage(`{"automatable":true}`),
			RawText: "Triage done",
			CostUSD: 0.10,
		},
		captureOnChunk: func(fn func(string)) {
			capturedOnChunk = fn
		},
	}

	engine, _ := setupEngine(t, phases, capturingRunner)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if capturedOnChunk == nil {
		t.Fatal("expected OnChunk to be set in RunOpts")
	}
}

func TestEngine_DrainPauseSignalUnpausesOnClose(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.10,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["task1"]}`),
					RawText: "Plan done",
					CostUSD: 0.20,
				},
			}},
		},
	}

	pauseCh := make(chan bool, 10)

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.PauseSignal = pauseCh
	})

	// Pause the engine.
	pauseCh <- true

	// Close the channel (simulating TUI exit) — should force-unpause.
	close(pauseCh)

	// Engine should complete without deadlock.
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(context.Background())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine deadlocked after pause channel close")
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}
}

func TestEngine_OnChunkContextCancel(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	// Runner that pauses the engine, calls OnChunk (which will block), then
	// waits for context cancellation.
	pauseCh := make(chan bool, 10)
	ctx, cancel := context.WithCancel(context.Background())

	chunkStarted := make(chan struct{})
	slowRunner := &funcRunner{
		fn: func(rctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
			// Pause the engine.
			pauseCh <- true
			// Give drainPauseSignal time to process.
			time.Sleep(50 * time.Millisecond)

			// Call OnChunk in a goroutine — it will block because engine is paused.
			go func() {
				close(chunkStarted)
				if opts.OnChunk != nil {
					opts.OnChunk("blocked line")
				}
			}()

			// Wait for context cancellation.
			<-rctx.Done()
			return nil, rctx.Err()
		},
	}

	engine, _ := setupEngine(t, phases, slowRunner, func(cfg *EngineConfig) {
		cfg.PauseSignal = pauseCh
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	// Wait for OnChunk to be called (blocking in waitIfPaused).
	select {
	case <-chunkStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("OnChunk was not called")
	}

	// Give OnChunk time to enter waitIfPaused.
	time.Sleep(50 * time.Millisecond)

	// Cancel context — should unblock OnChunk's waitIfPaused.
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine deadlocked — OnChunk did not unblock on context cancel")
	}
}

func TestEngine_OutputChunkNotLoggedToFile(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &chunkMockRunner{
		flexMockRunner: flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "Triage done",
						CostUSD: 0.10,
					},
				}},
			},
		},
		chunks: map[string][]string{
			"triage": {"line1", "line2", "line3"},
		},
	}

	var callbackChunks []string
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			if e.Kind == EventOutputChunk {
				if line, ok := e.Data["line"].(string); ok {
					callbackChunks = append(callbackChunks, line)
				}
			}
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Chunks should still arrive via OnEvent callback.
	if len(callbackChunks) != 3 {
		t.Errorf("got %d callback chunks, want 3", len(callbackChunks))
	}

	// Read the events.jsonl file — output_chunk events should NOT be present.
	events, err := ReadEvents(state.Dir())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	for _, ev := range events {
		if ev.Kind == EventOutputChunk {
			t.Errorf("output_chunk event found in events.jsonl — should be excluded from disk log")
		}
	}
}

func TestEngine_CheckpointWithPauseSignal(t *testing.T) {
	// Verify that sending false on PauseSignal after EventCheckpointPause
	// unblocks the engine. Without the inCheckpoint fix, this test deadlocks.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.10,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["task1"]}`),
					RawText: "Plan done",
					CostUSD: 0.20,
				},
			}},
		},
	}

	pauseCh := make(chan bool, 8)
	var events []Event
	var mu sync.Mutex

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Mode = Checkpoint
		cfg.PauseSignal = pauseCh
		cfg.OnEvent = func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()

			// When the TUI receives a checkpoint pause, it sets paused=true
			// and waits for Enter. When Enter is pressed, it sends false on
			// the pause channel. Simulate this behavior.
			if e.Kind == EventCheckpointPause {
				go func() {
					// Small delay to simulate user pressing Enter.
					time.Sleep(50 * time.Millisecond)
					pauseCh <- false
				}()
			}
		}
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(context.Background())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("engine deadlocked — Checkpoint + PauseSignal interaction is broken")
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}

	// Verify that checkpoint pause events were emitted.
	mu.Lock()
	defer mu.Unlock()
	checkpointCount := 0
	for _, ev := range events {
		if ev.Kind == EventCheckpointPause {
			checkpointCount++
		}
	}
	if checkpointCount != 2 {
		t.Errorf("expected 2 checkpoint pause events, got %d", checkpointCount)
	}
}

func TestEngine_CheckpointWithPauseSignal_ChannelClose(t *testing.T) {
	// Verify that closing the pause channel while the engine is blocked on a
	// checkpoint unblocks it (TUI exit scenario).
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 0.10,
				},
			}},
		},
	}

	pauseCh := make(chan bool, 8)

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Mode = Checkpoint
		cfg.PauseSignal = pauseCh
		cfg.OnEvent = func(e Event) {
			// Close channel on checkpoint to simulate TUI exit.
			if e.Kind == EventCheckpointPause {
				go func() {
					time.Sleep(50 * time.Millisecond)
					close(pauseCh)
				}()
			}
		}
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(context.Background())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("engine deadlocked — channel close did not unblock checkpoint")
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
}

func TestEngine_CheckpointWithPauseSignal_PauseResumeBeforeCheckpoint(t *testing.T) {
	// Verify that a regular p→p pause/resume cycle does not interfere with
	// subsequent checkpoint pauses. The inCheckpoint flag should only be set
	// when actually in a checkpoint wait.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	pauseCh := make(chan bool, 8)
	checkpointReached := make(chan struct{}, 1)

	mock := &funcRunner{
		fn: func(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
			// Simulate a p→p pause/resume cycle during the phase.
			pauseCh <- true
			time.Sleep(20 * time.Millisecond)
			pauseCh <- false
			time.Sleep(20 * time.Millisecond)
			return &runner.RunResult{
				Output:  json.RawMessage(`{"automatable":true}`),
				RawText: "Triage done",
				CostUSD: 0.10,
			}, nil
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Mode = Checkpoint
		cfg.PauseSignal = pauseCh
		cfg.OnEvent = func(e Event) {
			if e.Kind == EventCheckpointPause {
				checkpointReached <- struct{}{}
				go func() {
					time.Sleep(50 * time.Millisecond)
					// Simulate TUI Enter: resume from checkpoint via pause signal.
					pauseCh <- false
				}()
			}
		}
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(context.Background())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("engine deadlocked")
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}

	// Verify checkpoint was actually reached.
	select {
	case <-checkpointReached:
		// OK
	default:
		t.Error("checkpoint was never reached")
	}
}

// --- Follow-up phase (post-submit) tests ---

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

func TestEngine_FollowUpPhase_SkippedOnPass(t *testing.T) {
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
				Output: json.RawMessage(`{"ticket_key":"TEST-1","findings":[],"verdict":"pass"}`),
			}}},
			"submit": {{result: &runner.RunResult{
				Output: json.RawMessage(`{"ticket_key":"TEST-1","pr_url":"https://github.com/test/repo/pull/1","pr_number":1,"title":"test","branch":"test","target":"main","forge":"github"}`),
			}}},
		},
	}

	var events []Event
	engine, _ := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) { events = append(events, e) }
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Follow-up should have been skipped.
	skipped := false
	for _, ev := range events {
		if ev.Kind == EventFollowUpSkipped {
			skipped = true
		}
	}
	if !skipped {
		t.Error("follow-up should emit follow_up_skipped event when review verdict is 'pass'")
	}

	// Runner should NOT have been called for follow-up.
	mock.mu.Lock()
	for _, c := range mock.calls {
		if c.Phase == "follow-up" {
			t.Error("runner should not be called for skipped follow-up phase")
		}
	}
	mock.mu.Unlock()
}

func TestEngine_FollowUpPhase_FailureIsNonFatal(t *testing.T) {
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
			"follow-up": {{err: fmt.Errorf("gh: rate limit exceeded")}},
		},
	}

	var events []Event
	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) { events = append(events, e) }
	})

	// Pipeline should succeed despite follow-up failure.
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run should succeed despite follow-up failure: %v", err)
	}

	// Follow-up should be marked completed (best-effort).
	if !state.IsCompleted("follow-up") {
		t.Error("follow-up should be marked completed even on failure")
	}

	// Should have follow_up_failed event.
	hasFailed := false
	for _, ev := range events {
		if ev.Kind == EventFollowUpFailed {
			hasFailed = true
		}
	}
	if !hasFailed {
		t.Error("follow_up_failed event should be emitted on failure")
	}
}

func TestEngine_CorrectivePhaseSkippedInForwardPass(t *testing.T) {
	// Corrective phases (type: corrective) should be skipped during the
	// forward pass — they only run when routed via reworkSignal.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "implement",
			Prompt:    "implement.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:   "patch",
			Type:   "corrective",
			Prompt: "patch.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "auto",
					CostUSD: 0.05,
				},
			}},
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Patch should NOT have been run.
	if state.IsCompleted("patch") {
		t.Error("patch should NOT be completed in forward pass")
	}

	// Should emit corrective_skipped event.
	hasSkipped := false
	for _, ev := range events {
		if ev.Kind == EventCorrectiveSkipped && ev.Phase == "patch" {
			hasSkipped = true
		}
	}
	if !hasSkipped {
		t.Error("corrective_skipped event not emitted for patch phase")
	}

	// Runner should only have been called for triage and implement.
	if len(mock.calls) != 2 {
		t.Errorf("runner called %d times, want 2", len(mock.calls))
	}
}

func TestEngine_ResumePatchRerunsVerify(t *testing.T) {
	// When Resume('patch') is called, after patch completes the engine
	// must re-run verify — not skip it because of a stale FAIL result.
	// Before the fix, shouldSkip would gate verify (patch is not in
	// verify's depends_on), gatePhase would read the stale FAIL, and
	// rework back to patch, creating a wasteful loop.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 2,
				OnExhausted: "stop",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"patch": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[{"fix_index":0,"status":"fixed","description":"fixed"}],"files_changed":[],"tests_passed":true,"too_complex":false}`),
					RawText: "patched",
					CostUSD: 0.20,
				},
			}},
			"verify": {{
				// After patch, verify should PASS.
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS","criteria_results":[],"command_results":[]}`),
					RawText: "pass",
					CostUSD: 0.05,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	// Simulate prior state: implement completed, verify completed with FAIL
	// (as if the previous run crashed after verify FAIL routed to patch).
	_ = state.MarkRunning("implement")
	_ = state.MarkCompleted("implement")
	_ = state.WriteResult("implement", json.RawMessage(`{"tests_passed":true}`))

	_ = state.MarkRunning("verify")
	_ = state.MarkCompleted("verify")
	_ = state.WriteResult("verify", json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix X"],"criteria_results":[],"command_results":[]}`))

	// Resume from patch — this is the crash-recovery path.
	if err := engine.Resume(context.Background(), "patch"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// Verify should have been re-run (not skipped).
	verifyRan := false
	for _, call := range mock.calls {
		if call.Phase == "verify" {
			verifyRan = true
		}
	}
	if !verifyRan {
		t.Error("verify should have been re-run after Resume('patch'), not skipped")
	}

	// PatchCycles should be 0 — the stale verify FAIL from the prior
	// crash should NOT increment PatchCycles. In a real crash-recovery
	// scenario, PatchCycles would already reflect the pre-crash count.
	if state.Meta().PatchCycles != 0 {
		t.Errorf("PatchCycles = %d, want 0 (stale signal should not increment)", state.Meta().PatchCycles)
	}

	// Should have exactly 2 runner calls: patch + verify.
	if len(mock.calls) != 2 {
		t.Errorf("runner called %d times, want 2 (patch + verify)", len(mock.calls))
	}

	// Verify should be completed with PASS.
	if !state.IsCompleted("verify") {
		t.Error("verify should be completed after Resume")
	}
}

func TestEngine_ReviewReworkAndPatchIndependent(t *testing.T) {
	// Both review rework and patch cycles should work independently in the
	// same pipeline run. Review rework increments ReworkCycles, patch
	// increments PatchCycles — they don't interfere with each other.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 2,
				OnExhausted: "stop",
			},
		},
		{
			Name:      "review",
			Prompt:    "review.md",
			DependsOn: []string{"implement", "verify"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework: &ReworkConfig{
				Target: "implement",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				// First implement.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true}`),
						RawText: "impl1",
						CostUSD: 0.10,
					},
				},
				// Second implement (after review rework).
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true}`),
						RawText: "impl2",
						CostUSD: 0.10,
					},
				},
			},
			"patch": {
				// First patch (after first verify FAIL).
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[{"fix_index":0,"status":"fixed","description":"fixed"}],"files_changed":[],"tests_passed":true,"too_complex":false}`),
						RawText: "patched",
						CostUSD: 0.20,
					},
				},
			},
			"verify": {
				// First verify: FAIL → routes to patch.
				{
					result: &runner.RunResult{
						Output: json.RawMessage(`{
							"verdict":"FAIL",
							"fixes_required":["fix test"],
							"criteria_results":[{"criterion":"tests pass","passed":false,"evidence":"1 failure"}],
							"command_results":[]
						}`),
						RawText: "fail",
						CostUSD: 0.05,
					},
				},
				// Second verify (after patch): PASS → continues to review.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS","criteria_results":[],"command_results":[]}`),
						RawText: "pass",
						CostUSD: 0.05,
					},
				},
				// Third verify (after review rework → implement): PASS.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS","criteria_results":[],"command_results":[]}`),
						RawText: "pass2",
						CostUSD: 0.05,
					},
				},
			},
			"review": {
				// First review: rework verdict → routes back to implement.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"rework","findings":[{"severity":"major","issue":"needs refactor"}],"ticket_key":"TEST-1"}`),
						RawText: "rework",
						CostUSD: 0.15,
					},
				},
				// Second review: pass → pipeline completes.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"pass","findings":[],"ticket_key":"TEST-1"}`),
						RawText: "pass",
						CostUSD: 0.15,
					},
				},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxReworkCycles = 2
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// PatchCycles and ReworkCycles should be independent.
	if state.Meta().PatchCycles != 1 {
		t.Errorf("PatchCycles = %d, want 1", state.Meta().PatchCycles)
	}
	if state.Meta().ReworkCycles != 1 {
		t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
	}

	// All phases should be completed.
	for _, name := range []string{"implement", "patch", "verify", "review"} {
		if !state.IsCompleted(name) {
			t.Errorf("%s should be completed", name)
		}
	}

	// Flow: implement, verify(fail), patch, verify(pass), review(rework),
	// implement, verify(pass), review(pass) = 8 calls
	if len(mock.calls) != 8 {
		t.Errorf("runner called %d times, want 8", len(mock.calls))
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

func TestEngine_ReworkFeedbackIncludesPriorVerifyCycles(t *testing.T) {
	// When verify produces "FAIL" and routes to patch (corrective),
	// the patch prompt should include prior cycle context from archived
	// verify results.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 3,
				OnExhausted: "stop",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"patch": {
				// First patch.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":true,"too_complex":false}`),
					RawText: "patched1",
					CostUSD: 0.10,
				}},
				// Second patch.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":true,"too_complex":false}`),
					RawText: "patched2",
					CostUSD: 0.10,
				}},
			},
			"verify": {
				// First verify: FAIL with test A.
				{result: &runner.RunResult{
					Output: json.RawMessage(`{
						"verdict":"FAIL",
						"fixes_required":["fix test A"],
						"criteria_results":[{"criterion":"tests pass","passed":false,"evidence":"exit code 1"}],
						"command_results":[]
					}`),
					RawText: "fail",
					CostUSD: 0.05,
				}},
				// Second verify (after first patch): FAIL with same criterion but new fix.
				{result: &runner.RunResult{
					Output: json.RawMessage(`{
						"verdict":"FAIL",
						"fixes_required":["fix test B"],
						"criteria_results":[{"criterion":"tests pass","passed":false,"evidence":"assertion error"}],
						"command_results":[]
					}`),
					RawText: "fail2",
					CostUSD: 0.05,
				}},
				// Third verify (after second patch): PASS.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS","criteria_results":[],"command_results":[]}`),
					RawText: "pass",
					CostUSD: 0.05,
				}},
			},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	implTmpl := "Phase: implement\nTicket: {{.Ticket.Key}}\n"
	verifyTmpl := "Phase: verify\nTicket: {{.Ticket.Key}}\n"
	patchTmpl := `Phase: patch
Ticket: {{.Ticket.Key}}
{{- if .ReworkFeedback}}
REWORK:
Source: {{.ReworkFeedback.Source}}
Verdict: {{.ReworkFeedback.Verdict}}
{{- range .ReworkFeedback.FixesRequired}}
Fix: {{.}}
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
	if err := os.WriteFile(filepath.Join(promptDir, "verify.md"), []byte(verifyTmpl), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "patch.md"), []byte(patchTmpl), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadOrCreate(stateDir, "VPRIOR-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "VPRIOR-1", Summary: "Verify prior cycles test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Find the patch runner calls.
	var patchPrompts []string
	for _, call := range mock.calls {
		if call.Phase == "patch" {
			patchPrompts = append(patchPrompts, call.SystemPrompt)
		}
	}

	if len(patchPrompts) < 2 {
		t.Fatalf("expected 2 patch calls, got %d", len(patchPrompts))
	}

	// First patch: rework feedback from first verify, no prior cycles.
	if !strings.Contains(patchPrompts[0], "fix test A") {
		t.Error("first patch should reference 'fix test A'")
	}
	if strings.Contains(patchPrompts[0], "PRIOR_CYCLES:") {
		t.Error("first patch should NOT have prior cycles")
	}

	// Second patch: rework feedback from second verify, WITH prior cycle context.
	if !strings.Contains(patchPrompts[1], "fix test B") {
		t.Error("second patch should reference 'fix test B'")
	}
	if !strings.Contains(patchPrompts[1], "PRIOR_CYCLES:") {
		t.Error("second patch should have prior cycles")
	}
	if !strings.Contains(patchPrompts[1], "fix test A") {
		t.Error("second patch prior cycles should reference 'fix test A' from cycle 1")
	}
}

func TestEngine_PipelineTimeout_Run(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
	}

	// The plan phase blocks until context is cancelled, simulating a slow phase
	// that exceeds the pipeline timeout.
	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "triage ok",
					CostUSD: 0.01,
				},
			}},
			"plan": {{
				result: nil,
				err:    context.DeadlineExceeded,
			}},
		},
	}

	var events []Event
	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxPipelineDuration = 50 * time.Millisecond
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from pipeline timeout")
	}

	var timeoutErr *PipelineTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected PipelineTimeoutError, got: %T: %v", err, err)
	}
	if timeoutErr.Limit != 50*time.Millisecond {
		t.Errorf("Limit = %v, want 50ms", timeoutErr.Limit)
	}
	// Phase should be "plan" — that's the phase that was running when the timeout fired.
	if timeoutErr.Phase != "plan" {
		t.Errorf("Phase = %q, want %q", timeoutErr.Phase, "plan")
	}
	// Elapsed should be > 0 (actual wall-clock time).
	if timeoutErr.Elapsed <= 0 {
		t.Errorf("Elapsed = %v, want > 0", timeoutErr.Elapsed)
	}

	// Verify pipeline_timeout event was emitted.
	hasTimeoutEvent := false
	for _, ev := range events {
		if ev.Kind == EventPipelineTimeout {
			hasTimeoutEvent = true
		}
	}
	if !hasTimeoutEvent {
		t.Error("pipeline_timeout event not emitted")
	}
}

func TestEngine_PipelineTimeout_Resume(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"plan": {{
				result: nil,
				err:    context.DeadlineExceeded,
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxPipelineDuration = 50 * time.Millisecond
		cfg.OnEvent = func(ev Event) {
			events = append(events, ev)
		}
	})

	// Pre-complete triage so Resume from plan works.
	_ = state.MarkRunning("triage")
	_ = state.WriteResult("triage", json.RawMessage(`{"automatable":true}`))
	_ = state.WriteArtifact("triage", []byte("done"))
	_ = state.MarkCompleted("triage")

	err := engine.Resume(context.Background(), "plan")
	if err == nil {
		t.Fatal("expected error from pipeline timeout")
	}

	var timeoutErr *PipelineTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected PipelineTimeoutError, got: %T: %v", err, err)
	}
	// Phase should be "plan" — that's the phase that was running when the timeout fired.
	if timeoutErr.Phase != "plan" {
		t.Errorf("Phase = %q, want %q", timeoutErr.Phase, "plan")
	}
	// Elapsed should be > 0 (actual wall-clock time).
	if timeoutErr.Elapsed <= 0 {
		t.Errorf("Elapsed = %v, want > 0", timeoutErr.Elapsed)
	}
}

func TestEngine_PipelineTimeout_ResumeWithStaleFailed(t *testing.T) {
	// Regression test: when resuming from a later phase, earlier phases may
	// retain stale PhaseFailed status from a prior run. lastRunningPhase must
	// return the LAST failed phase (the one that just timed out), not the
	// first failed phase (which is stale).
	//
	// Scenario: 3-phase pipeline [triage → implement → review].
	// Prior run: triage succeeded, implement failed. Now we resume from
	// implement, which succeeds, but review times out. The stale PhaseFailed
	// status on implement from the prior run should NOT be returned — review
	// (the last failed phase) should be returned.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
		{
			Name:      "implement",
			Prompt:    "implement.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
		{
			Name:      "review",
			Prompt:    "review.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ok":true}`),
					RawText: "implement ok",
					CostUSD: 0.01,
				},
			}},
			"review": {{
				result: nil,
				err:    context.DeadlineExceeded,
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxPipelineDuration = 200 * time.Millisecond
	})

	// Pre-complete triage from a prior run.
	_ = state.MarkRunning("triage")
	_ = state.WriteResult("triage", json.RawMessage(`{"automatable":true}`))
	_ = state.WriteArtifact("triage", []byte("done"))
	_ = state.MarkCompleted("triage")

	// Simulate implement having failed in a prior run (stale PhaseFailed).
	// MarkRunning creates the phase entry, then MarkFailed sets status=failed.
	_ = state.MarkRunning("implement")
	_ = state.MarkFailed("implement", fmt.Errorf("prior run failure"))

	// Resume from implement: implement will succeed (re-run), then review
	// will return DeadlineExceeded, triggering the pipeline timeout.
	err := engine.Resume(context.Background(), "implement")
	if err == nil {
		t.Fatal("expected error from pipeline timeout")
	}

	var timeoutErr *PipelineTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected PipelineTimeoutError, got: %T: %v", err, err)
	}
	// Phase should be "review" — the phase that actually timed out — not
	// "implement" which had stale PhaseFailed status from the prior run.
	if timeoutErr.Phase != "review" {
		t.Errorf("Phase = %q, want %q (lastRunningPhase returned stale failed phase)", timeoutErr.Phase, "review")
	}
}

func TestEngine_PipelineTimeout_ZeroMeansNoLimit(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "ok",
					CostUSD: 0.01,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxPipelineDuration = 0 // no limit
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
}

func TestEngine_PipelineTimeout_NonDeadlineErrorNotWrapped(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
	}

	// Return a non-timeout error.
	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: nil,
				err:    fmt.Errorf("some other error"),
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxPipelineDuration = 5 * time.Minute
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}

	// Should NOT be wrapped as PipelineTimeoutError since the context
	// deadline was not exceeded.
	var timeoutErr *PipelineTimeoutError
	if errors.As(err, &timeoutErr) {
		t.Error("non-deadline error should not be wrapped as PipelineTimeoutError")
	}
}

func TestEngine_PipelineTimeout_ExternalDeadlineNotWrapped(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
		},
	}

	// The phase returns DeadlineExceeded, simulating an external context timeout.
	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: nil,
				err:    context.DeadlineExceeded,
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxPipelineDuration = 10 * time.Minute // large pipeline timeout
	})

	// Use an external context with a very short deadline (shorter than the pipeline's).
	externalCtx, externalCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer externalCancel()

	err := engine.Run(externalCtx)
	if err == nil {
		t.Fatal("expected error")
	}

	// Should NOT be wrapped as PipelineTimeoutError because the deadline
	// that fired belongs to the external parent context, not the pipeline.
	var timeoutErr *PipelineTimeoutError
	if errors.As(err, &timeoutErr) {
		t.Error("external context deadline should not be wrapped as PipelineTimeoutError")
	}
}
