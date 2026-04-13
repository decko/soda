package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
					{err: &claude.SemanticError{Message: "output incomplete"}},
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

// initGitRepo creates a bare-minimum git repo in dir with one commit on "main".
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s: %v", args, out, err)
		}
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
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
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
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
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
			Name:      "implement",
			Prompt:    "implement.md",
			DependsOn: []string{"plan"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
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

func TestEngine_TruncateLines(t *testing.T) {
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

// phaseNames extracts phase names from runner calls for test error messages.
func phaseNames(calls []runner.RunOpts) []string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Phase
	}
	return names
}
