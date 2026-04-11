package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/runner"
)

// flexMockRunner returns per-call responses, allowing multi-call test scenarios
// (e.g., fail twice then succeed).
type flexMockRunner struct {
	mu        sync.Mutex
	responses map[string][]flexResponse
	calls     []runner.RunOpts
	counters  map[string]int
}

type flexResponse struct {
	result *runner.RunResult
	err    error
}

func (f *flexMockRunner) Run(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, opts)
	if f.counters == nil {
		f.counters = make(map[string]int)
	}
	idx := f.counters[opts.Phase]
	f.counters[opts.Phase]++
	resps, ok := f.responses[opts.Phase]
	if !ok || idx >= len(resps) {
		return nil, fmt.Errorf("flexmock: no response for phase %q call %d", opts.Phase, idx)
	}
	resp := resps[idx]
	return resp.result, resp.err
}

// setupEngine creates temp directories, writes minimal prompt templates,
// creates State, and returns an Engine + State ready for testing.
func setupEngine(t *testing.T, phases []PhaseConfig, r runner.Runner, opts ...func(*EngineConfig)) (*Engine, *State) {
	t.Helper()

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Write a minimal prompt template for each phase.
	for _, p := range phases {
		tmplPath := filepath.Join(promptDir, p.Prompt)
		if err := os.MkdirAll(filepath.Dir(tmplPath), 0755); err != nil {
			t.Fatalf("mkdir for prompt %s: %v", p.Prompt, err)
		}
		content := fmt.Sprintf("Phase: %s\nTicket: {{.Ticket.Key}}\n", p.Name)
		if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
			t.Fatalf("write prompt %s: %v", p.Prompt, err)
		}
	}

	state, err := LoadOrCreate(stateDir, "TEST-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	pipeline := &PhasePipeline{Phases: phases}
	loader := NewPromptLoader(promptDir)

	cfg := EngineConfig{
		Pipeline:   pipeline,
		Loader:     loader,
		Ticket:     TicketData{Key: "TEST-1", Summary: "Test ticket"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0, // no budget limit by default
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {}, // no-op sleep for tests
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	engine := NewEngine(r, state, cfg)
	return engine, state
}

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
				err: &claude.TransientError{Reason: "timeout", Err: fmt.Errorf("connection reset")},
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
				{err: &claude.TransientError{Reason: "timeout", Err: fmt.Errorf("fail1")}},
				{err: &claude.TransientError{Reason: "timeout", Err: fmt.Errorf("fail2")}},
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

	parseErr := &claude.ParseError{
		Raw: []byte("bad output"),
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
				{err: &claude.TransientError{Reason: "timeout", Err: fmt.Errorf("fail1")}},
				{err: &claude.TransientError{Reason: "timeout", Err: fmt.Errorf("fail2")}},
				{err: &claude.TransientError{Reason: "timeout", Err: fmt.Errorf("fail3")}},
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

func TestEngine_BudgetExceeded(t *testing.T) {
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

	// Triage costs $14, budget is $15. After triage, total is $14 which is < $15,
	// but we also verify a budget warning is emitted at 90% ($13.50).
	// To actually exceed the budget, set cost to $16.
	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 16.0,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostUSD = 15.0
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected BudgetExceededError")
	}

	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got: %v", err)
	}
	if budgetErr.Phase != "plan" {
		t.Errorf("budget error phase = %q, want %q", budgetErr.Phase, "plan")
	}
}

func TestEngine_BudgetExceeded_AtLimit(t *testing.T) {
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
					CostUSD: 15.0,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostUSD = 15.0
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected BudgetExceededError")
	}

	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got: %v", err)
	}
	if budgetErr.Phase != "plan" {
		t.Errorf("budget error phase = %q, want %q", budgetErr.Phase, "plan")
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

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Mode = Checkpoint
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	// Auto-confirm from a goroutine.
	go func() {
		// Wait for each checkpoint_pause, then confirm.
		for i := 0; i < len(phases); i++ {
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
		{"transient", &claude.TransientError{Reason: "timeout", Err: fmt.Errorf("x")}, "transient"},
		{"parse", &claude.ParseError{Err: fmt.Errorf("x")}, "parse"},
		{"semantic", &claude.SemanticError{Message: "bad"}, "semantic"},
		{"unknown", fmt.Errorf("something else"), "unknown"},
		{"wrapped_transient", fmt.Errorf("wrap: %w", &claude.TransientError{Reason: "r", Err: fmt.Errorf("x")}), "transient"},
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

func TestEngine_GatePhase_TriageNotAutomatable(t *testing.T) {
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
					Output:  json.RawMessage(`{"automatable":false,"block_reason":"needs human design review"}`),
					RawText: "Not automatable",
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
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if gateErr.Phase != "triage" {
		t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "triage")
	}
	if !strings.Contains(gateErr.Reason, "human design review") {
		t.Errorf("gate error reason should mention block_reason, got: %q", gateErr.Reason)
	}
}

