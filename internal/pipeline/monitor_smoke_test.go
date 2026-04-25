package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
)

// smokeMonitorPipelinePhases returns a 3-phase pipeline:
//
//	implement → submit → monitor
//
// This is the minimal end-to-end pipeline that exercises the monitor
// phase through the engine's Run() path, including dependency resolution,
// PR URL extraction, polling, and terminal-status handling.
func smokeMonitorPipelinePhases(pollingConfig *PollingConfig) []PhaseConfig {
	if pollingConfig == nil {
		pollingConfig = &PollingConfig{
			InitialInterval:   Duration{Duration: 1 * time.Millisecond},
			MaxInterval:       Duration{Duration: 2 * time.Millisecond},
			EscalateAfter:     Duration{Duration: 10 * time.Millisecond},
			MaxDuration:       Duration{Duration: 100 * time.Millisecond},
			MaxResponseRounds: 3,
			RespondToComments: true,
		}
	}
	return []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Schema: `{"type":"object","properties":{"tests_passed":{"type":"boolean"}},"required":["tests_passed"]}`,
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:      "submit",
			Prompt:    "submit.md",
			Schema:    `{"type":"object","properties":{"pr_url":{"type":"string"}},"required":["pr_url"]}`,
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:      "monitor",
			Prompt:    "monitor.md",
			Type:      "polling",
			DependsOn: []string{"submit"},
			Polling:   pollingConfig,
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
	}
}

// smokeMonitorFixtures returns happy-path mock results for implement and submit.
func smokeMonitorFixtures() map[string]*runner.RunResult {
	return map[string]*runner.RunResult{
		"implement": {
			Output:  json.RawMessage(`{"tests_passed":true}`),
			RawText: "Implemented",
			CostUSD: 0.50,
		},
		"submit": {
			Output:  json.RawMessage(`{"pr_url":"https://github.com/test/repo/pull/42"}`),
			RawText: "PR #42 created",
			CostUSD: 0.03,
		},
	}
}

// writeMonitorSmokePrompts creates minimal prompt templates for the monitor
// smoke pipeline.
func writeMonitorSmokePrompts(t *testing.T, dir string) {
	t.Helper()
	templates := map[string]string{
		"implement.md": "Implement {{.Ticket.Key}}\n",
		"submit.md":    "Submit {{.Ticket.Key}}\n",
		"monitor.md":   "Monitor {{.Ticket.Key}}\n\n## Diff\n{{.DiffContext}}\n\n## Review Comments\n\n{{.ReviewComments}}\n",
	}
	for name, content := range templates {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write prompt %s: %v", name, err)
		}
	}
}

// TestSmokeMonitor_PRApproved runs the full implement → submit → monitor
// pipeline where the PR is approved on the first poll. Validates that all
// three phases complete, costs accumulate, events are emitted in order,
// and the monitor state file is written correctly.
func TestSmokeMonitor_PRApproved(t *testing.T) {
	fixtures := smokeMonitorFixtures()
	phases := smokeMonitorPipelinePhases(nil)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: true}},
		},
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-1", Summary: "Monitor smoke: PR approved"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "soda-bot",
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All phases completed ---
	for _, name := range []string{"implement", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// --- Cost accumulation (implement + submit; monitor has no runner cost) ---
	expectedCost := 0.50 + 0.03
	if !approxEqual(state.Meta().TotalCost, expectedCost) {
		t.Errorf("TotalCost = %v, want %v", state.Meta().TotalCost, expectedCost)
	}

	// --- Runner called only for implement and submit (monitor uses PRPoller) ---
	if len(mock.calls) != 2 {
		t.Errorf("runner called %d times, want 2; phases: %v",
			len(mock.calls), phaseNames(mock.calls))
	}

	// --- Phase ordering in runner calls ---
	wantOrder := []string{"implement", "submit"}
	gotOrder := phaseNames(mock.calls)
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("call count mismatch: got %v, want %v", gotOrder, wantOrder)
	}
	for i, want := range wantOrder {
		if gotOrder[i] != want {
			t.Errorf("runner call[%d] = %q, want %q", i, gotOrder[i], want)
		}
	}

	// --- Monitor state written correctly ---
	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorCompleted {
		t.Errorf("monitor status = %q, want %q", monState.Status, MonitorCompleted)
	}
	if monState.PRURL != "https://github.com/test/repo/pull/42" {
		t.Errorf("PRURL = %q, want %q", monState.PRURL, "https://github.com/test/repo/pull/42")
	}
	if monState.PollCount != 1 {
		t.Errorf("PollCount = %d, want 1", monState.PollCount)
	}

	// --- Verify key events ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventEngineStarted] != 1 {
		t.Errorf("engine_started events = %d, want 1", eventKinds[EventEngineStarted])
	}
	if eventKinds[EventEngineCompleted] != 1 {
		t.Errorf("engine_completed events = %d, want 1", eventKinds[EventEngineCompleted])
	}
	if eventKinds[EventPhaseStarted] != 3 {
		t.Errorf("phase_started events = %d, want 3", eventKinds[EventPhaseStarted])
	}
	if eventKinds[EventPhaseCompleted] != 3 {
		t.Errorf("phase_completed events = %d, want 3", eventKinds[EventPhaseCompleted])
	}
	if eventKinds[EventMonitorPRApproved] != 1 {
		t.Errorf("monitor_pr_approved events = %d, want 1", eventKinds[EventMonitorPRApproved])
	}
	if eventKinds[EventMonitorPolling] != 1 {
		t.Errorf("monitor_polling events = %d, want 1", eventKinds[EventMonitorPolling])
	}

	// --- events.jsonl written ---
	eventsPath := filepath.Join(state.Dir(), "events.jsonl")
	eventsData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if len(eventsData) == 0 {
		t.Error("events.jsonl should not be empty")
	}
}

// TestSmokeMonitor_PRClosed runs the full pipeline where the PR is closed
// (not merged). The monitor phase should detect this and mark itself failed.
// The engine reports completion because the monitor phase terminates cleanly
// (it marks itself failed and returns nil).
func TestSmokeMonitor_PRClosed(t *testing.T) {
	fixtures := smokeMonitorFixtures()
	phases := smokeMonitorPipelinePhases(nil)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "closed", Approved: false}},
		},
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-2")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-2", Summary: "Monitor smoke: PR closed"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "soda-bot",
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- implement and submit completed, monitor failed ---
	if !state.IsCompleted("implement") {
		t.Error("implement should be completed")
	}
	if !state.IsCompleted("submit") {
		t.Error("submit should be completed")
	}

	monPS := state.Meta().Phases["monitor"]
	if monPS == nil {
		t.Fatal("monitor phase state missing")
	}
	if monPS.Status != PhaseFailed {
		t.Errorf("monitor status = %q, want %q", monPS.Status, PhaseFailed)
	}

	// --- Monitor state reflects PR closed ---
	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorFailed {
		t.Errorf("monitor state status = %q, want %q", monState.Status, MonitorFailed)
	}

	// --- Events ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventMonitorPRClosed] != 1 {
		t.Errorf("monitor_pr_closed events = %d, want 1", eventKinds[EventMonitorPRClosed])
	}
	if eventKinds[EventPhaseFailed] != 1 {
		t.Errorf("phase_failed events = %d, want 1", eventKinds[EventPhaseFailed])
	}
}

// TestSmokeMonitor_PRMerged runs the full pipeline where the PR is already
// merged. The monitor phase should detect this and complete successfully.
func TestSmokeMonitor_PRMerged(t *testing.T) {
	fixtures := smokeMonitorFixtures()
	phases := smokeMonitorPipelinePhases(nil)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "merged", Approved: false}},
		},
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-3")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-3", Summary: "Monitor smoke: PR merged"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "soda-bot",
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All phases completed ---
	for _, name := range []string{"implement", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorCompleted {
		t.Errorf("monitor status = %q, want %q", monState.Status, MonitorCompleted)
	}

	// --- Events ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventMonitorPRApproved] != 1 {
		t.Errorf("monitor_pr_approved events = %d, want 1 (merged emits pr_approved)", eventKinds[EventMonitorPRApproved])
	}
}

// TestSmokeMonitor_Timeout runs the full pipeline where the monitor polls
// until max_duration is exceeded. Uses NowFunc to simulate time advancing
// past the deadline.
func TestSmokeMonitor_Timeout(t *testing.T) {
	fixtures := smokeMonitorFixtures()

	pollingCfg := &PollingConfig{
		InitialInterval:   Duration{Duration: 1 * time.Millisecond},
		MaxInterval:       Duration{Duration: 2 * time.Millisecond},
		EscalateAfter:     Duration{Duration: 10 * time.Minute},
		MaxDuration:       Duration{Duration: 1 * time.Hour},
		MaxResponseRounds: 3,
		RespondToComments: true,
	}
	phases := smokeMonitorPipelinePhases(pollingCfg)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			// Always open, never approved.
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
		},
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-4")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Time control: first call returns base time, subsequent calls jump past max_duration.
	baseTime := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	var callCount int64
	nowFunc := func() time.Time {
		n := atomic.AddInt64(&callCount, 1)
		if n <= 1 {
			return baseTime
		}
		return baseTime.Add(2 * time.Hour) // exceeds 1h max_duration
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-4", Summary: "Monitor smoke: timeout"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "soda-bot",
		NowFunc:    nowFunc,
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- implement and submit completed, monitor failed ---
	if !state.IsCompleted("implement") {
		t.Error("implement should be completed")
	}
	if !state.IsCompleted("submit") {
		t.Error("submit should be completed")
	}

	monPS := state.Meta().Phases["monitor"]
	if monPS == nil {
		t.Fatal("monitor phase state missing")
	}
	if monPS.Status != PhaseFailed {
		t.Errorf("monitor status = %q, want %q", monPS.Status, PhaseFailed)
	}

	// --- Monitor state reflects timeout ---
	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorFailed {
		t.Errorf("monitor state status = %q, want %q", monState.Status, MonitorFailed)
	}

	// --- Events ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventMonitorTimeout] != 1 {
		t.Errorf("monitor_timeout events = %d, want 1", eventKinds[EventMonitorTimeout])
	}
}

// TestSmokeMonitor_CommentsAndApproval runs a pipeline where the monitor
// detects new review comments, classifies them, and then the PR is approved
// on the next poll. Validates the full comment detection → classification →
// approval lifecycle through the engine.
func TestSmokeMonitor_CommentsAndApproval(t *testing.T) {
	fixtures := smokeMonitorFixtures()
	phases := smokeMonitorPipelinePhases(nil)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			// Poll 1: open, has comments.
			{status: &PRStatus{State: "open", Approved: false}},
			// Poll 2: approved.
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			// Poll 1: new review comment.
			{comments: []PRComment{
				{
					ID:     "IC_100",
					Author: "reviewer",
					Body:   "Please add error handling for nil input.",
					Path:   "main.go",
					Line:   42,
				},
			}},
			// Poll 2: no new comments.
			{comments: nil},
		},
		ciResponses: []mockCIResponse{
			{status: &CIStatus{Overall: "pending"}},
			{status: &CIStatus{Overall: "success"}},
		},
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-5")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-5", Summary: "Monitor smoke: comments + approval"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "soda-bot",
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All phases completed ---
	for _, name := range []string{"implement", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// --- Monitor polled twice ---
	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.PollCount != 2 {
		t.Errorf("PollCount = %d, want 2", monState.PollCount)
	}
	if monState.LastCommentID != "IC_100" {
		t.Errorf("LastCommentID = %q, want %q", monState.LastCommentID, "IC_100")
	}

	// --- Events: comment classified + new comments + CI change + approval ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventMonitorNewComments] < 1 {
		t.Errorf("monitor_new_comments events = %d, want >= 1", eventKinds[EventMonitorNewComments])
	}
	if eventKinds[EventMonitorCommentClassified] < 1 {
		t.Errorf("monitor_comment_classified events = %d, want >= 1", eventKinds[EventMonitorCommentClassified])
	}
	if eventKinds[EventMonitorPRApproved] != 1 {
		t.Errorf("monitor_pr_approved events = %d, want 1", eventKinds[EventMonitorPRApproved])
	}
	if eventKinds[EventMonitorCIChange] < 1 {
		t.Errorf("monitor_ci_change events = %d, want >= 1", eventKinds[EventMonitorCIChange])
	}
}

// TestSmokeMonitor_StubFallback runs a pipeline where PRPoller is nil.
// The monitor phase should fall back to the stub (mark completed without
// polling), validating graceful degradation.
func TestSmokeMonitor_StubFallback(t *testing.T) {
	fixtures := smokeMonitorFixtures()
	phases := smokeMonitorPipelinePhases(nil)

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-6")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-6", Summary: "Monitor smoke: stub fallback"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   nil, // no poller → stub
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All phases completed (monitor via stub) ---
	for _, name := range []string{"implement", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// --- Monitor skipped event ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventMonitorSkipped] != 1 {
		t.Errorf("monitor_skipped events = %d, want 1", eventKinds[EventMonitorSkipped])
	}
}

// TestSmokeMonitor_MaxRounds runs a pipeline where the monitor reaches
// max_response_rounds and terminates. The PR never gets approved but the
// monitor exits cleanly after exhausting response rounds. Uses mock runner
// responses for both regular phases and monitor response sessions.
func TestSmokeMonitor_MaxRounds(t *testing.T) {
	fixtures := smokeMonitorFixtures()

	pollingCfg := &PollingConfig{
		InitialInterval:   Duration{Duration: 1 * time.Millisecond},
		MaxInterval:       Duration{Duration: 2 * time.Millisecond},
		EscalateAfter:     Duration{Duration: 10 * time.Minute},
		MaxDuration:       Duration{Duration: 10 * time.Minute},
		MaxResponseRounds: 2,
		RespondToComments: true,
	}
	phases := smokeMonitorPipelinePhases(pollingCfg)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
		},
		commentResponses: []mockCommentResponse{
			// Poll 1: actionable comment.
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix the bug in handler.go.", Path: "handler.go", Line: 10},
			}},
			// Poll 2: another actionable comment.
			{comments: []PRComment{
				{ID: "IC_2", Author: "reviewer", Body: "Add tests for this.", Path: "handler_test.go", Line: 5},
			}},
			// Poll 3: shouldn't reach here.
			{comments: nil},
		},
	}

	// Monitor response sessions use phase "monitor/response_N".
	// Provide mock results so respondToComments succeeds and
	// ResponseRounds is incremented.
	monitorResponseResult := &runner.RunResult{
		Output:  json.RawMessage(`{"ticket_key":"MON-SMOKE-7","pr_url":"https://github.com/test/repo/pull/42","comments_handled":[{"comment_id":"IC_1","author":"reviewer","action":"fixed","response":"Done"}],"files_changed":[{"path":"handler.go","action":"modified"}],"tests_passed":true}`),
		RawText: "Fixed review comment",
		CostUSD: 0.15,
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	responses["monitor/response_0"] = []flexResponse{{result: monitorResponseResult}}
	responses["monitor/response_1"] = []flexResponse{{result: monitorResponseResult}}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-7")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-7", Summary: "Monitor smoke: max rounds"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "soda-bot",
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- Monitor should complete with max_rounds status ---
	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed (max rounds is a completed state)")
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorMaxRounds {
		t.Errorf("monitor status = %q, want %q", monState.Status, MonitorMaxRounds)
	}
	if monState.ResponseRounds != 2 {
		t.Errorf("ResponseRounds = %d, want 2", monState.ResponseRounds)
	}

	// --- Cost includes monitor response rounds ---
	// implement(0.50) + submit(0.03) + 2 × response(0.15) = 0.83
	expectedCost := 0.50 + 0.03 + 2*0.15
	if !approxEqual(state.Meta().TotalCost, expectedCost) {
		t.Errorf("TotalCost = %v, want %v", state.Meta().TotalCost, expectedCost)
	}

	// --- Events ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventMonitorMaxRounds] != 1 {
		t.Errorf("monitor_max_rounds events = %d, want 1", eventKinds[EventMonitorMaxRounds])
	}
	if eventKinds[EventMonitorResponseStarted] < 2 {
		t.Errorf("monitor_response_started events = %d, want >= 2", eventKinds[EventMonitorResponseStarted])
	}
	if eventKinds[EventMonitorResponseCompleted] < 2 {
		t.Errorf("monitor_response_completed events = %d, want >= 2", eventKinds[EventMonitorResponseCompleted])
	}
}

// TestSmokeMonitor_CIStatusChanges runs a pipeline where CI status changes
// across polls. Validates that CI change events are emitted and state tracks
// the latest CI status.
func TestSmokeMonitor_CIStatusChanges(t *testing.T) {
	fixtures := smokeMonitorFixtures()
	phases := smokeMonitorPipelinePhases(nil)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		ciResponses: []mockCIResponse{
			{status: &CIStatus{Overall: "pending"}},
			{status: &CIStatus{Overall: "failure", Jobs: []CIJobInfo{
				{Name: "test", Status: "failure", Conclusion: "failure"},
				{Name: "lint", Status: "success", Conclusion: "success"},
			}}},
			{status: &CIStatus{Overall: "success"}},
		},
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-8")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-8", Summary: "Monitor smoke: CI changes"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "soda-bot",
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All phases completed ---
	for _, name := range []string{"implement", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// --- Monitor state tracks latest CI status ---
	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	// The last CI check before approval could be "failure" or "success"
	// depending on poll ordering; just ensure it's tracked.
	if monState.LastCIStatus == "" {
		t.Error("LastCIStatus should be set")
	}

	// --- Events: CI changes detected ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventMonitorCIChange] < 1 {
		t.Errorf("monitor_ci_change events = %d, want >= 1", eventKinds[EventMonitorCIChange])
	}
	if eventKinds[EventMonitorCIFailure] < 1 {
		t.Errorf("monitor_ci_failure events = %d, want >= 1", eventKinds[EventMonitorCIFailure])
	}
}

// TestSmokeMonitor_StateFilesValid verifies that all state files from a
// monitor smoke run are well-formed: meta.json, result files, events.jsonl,
// and monitor_state.json.
func TestSmokeMonitor_StateFilesValid(t *testing.T) {
	fixtures := smokeMonitorFixtures()
	phases := smokeMonitorPipelinePhases(nil)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: true}},
		},
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-9")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-9", Summary: "Monitor smoke: state files"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "soda-bot",
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- meta.json ---
	metaPath := filepath.Join(state.Dir(), "meta.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}

	var meta PipelineMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("unmarshal meta.json: %v", err)
	}

	if meta.Ticket != "MON-SMOKE-9" {
		t.Errorf("meta.Ticket = %q, want %q", meta.Ticket, "MON-SMOKE-9")
	}
	if meta.TotalCost <= 0 {
		t.Error("meta.TotalCost should be positive")
	}

	// All three phases should have state entries.
	for _, name := range []string{"implement", "submit", "monitor"} {
		ps := meta.Phases[name]
		if ps == nil {
			t.Errorf("meta.Phases[%q] missing", name)
			continue
		}
		if name == "monitor" {
			if ps.Status != PhaseCompleted {
				t.Errorf("meta.Phases[%q].Status = %q, want %q", name, ps.Status, PhaseCompleted)
			}
		}
		if ps.Generation < 1 {
			t.Errorf("meta.Phases[%q].Generation = %d, want >= 1", name, ps.Generation)
		}
	}

	// --- Result files for implement and submit ---
	for _, name := range []string{"implement", "submit"} {
		resultPath := filepath.Join(state.Dir(), name+".json")
		resultData, err := os.ReadFile(resultPath)
		if err != nil {
			t.Errorf("read %s.json: %v", name, err)
			continue
		}
		if !json.Valid(resultData) {
			t.Errorf("%s.json is not valid JSON", name)
		}
	}

	// --- monitor_state.json ---
	monitorStatePath := filepath.Join(state.Dir(), "monitor_state.json")
	monitorStateData, err := os.ReadFile(monitorStatePath)
	if err != nil {
		t.Fatalf("read monitor_state.json: %v", err)
	}
	if !json.Valid(monitorStateData) {
		t.Fatal("monitor_state.json is not valid JSON")
	}

	var monState MonitorState
	if err := json.Unmarshal(monitorStateData, &monState); err != nil {
		t.Fatalf("unmarshal monitor_state.json: %v", err)
	}
	if monState.PRURL == "" {
		t.Error("monitor_state PRURL should not be empty")
	}
	if monState.Status != MonitorCompleted {
		t.Errorf("monitor_state status = %q, want %q", monState.Status, MonitorCompleted)
	}

	// --- events.jsonl ---
	eventsPath := filepath.Join(state.Dir(), "events.jsonl")
	eventsData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}

	lines := splitNonEmpty(string(eventsData))
	if len(lines) == 0 {
		t.Error("events.jsonl should have at least one line")
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("events.jsonl line %d is not valid JSON: %s", i+1, line)
		}
	}

	// Verify monitor-specific events are present in events.jsonl.
	allEventsText := string(eventsData)
	for _, expected := range []string{EventMonitorPolling, EventMonitorPRApproved} {
		if !strings.Contains(allEventsText, expected) {
			t.Errorf("events.jsonl should contain %q event", expected)
		}
	}
}

// TestSmokeMonitor_PassiveComments runs a pipeline where the monitor has
// no SelfUser configured (comment response disabled). New comments are
// detected and logged in passive mode with a notify_user event.
func TestSmokeMonitor_PassiveComments(t *testing.T) {
	fixtures := smokeMonitorFixtures()

	pollingCfg := &PollingConfig{
		InitialInterval:   Duration{Duration: 1 * time.Millisecond},
		MaxInterval:       Duration{Duration: 2 * time.Millisecond},
		EscalateAfter:     Duration{Duration: 10 * time.Millisecond},
		MaxDuration:       Duration{Duration: 100 * time.Millisecond},
		MaxResponseRounds: 3,
		RespondToComments: true, // enabled but SelfUser empty → passive mode
	}
	phases := smokeMonitorPipelinePhases(pollingCfg)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_200", Author: "human", Body: "Please look at this.", Path: "main.go"},
			}},
			{comments: nil},
		},
	}

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeMonitorSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "MON-SMOKE-10")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "MON-SMOKE-10", Summary: "Monitor smoke: passive comments"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		SelfUser:   "", // empty → passive mode
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All phases completed ---
	for _, name := range []string{"implement", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// --- Events: passive comments detected, notify_user emitted ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}

	// Should have warning about respond_to_comments with no self_user.
	if eventKinds[EventMonitorWarning] < 1 {
		t.Errorf("monitor_warning events = %d, want >= 1 (self_user not set)", eventKinds[EventMonitorWarning])
	}
	if eventKinds[EventMonitorNewComments] < 1 {
		t.Errorf("monitor_new_comments events = %d, want >= 1", eventKinds[EventMonitorNewComments])
	}
	if eventKinds[EventMonitorNotifyUser] < 1 {
		t.Errorf("monitor_notify_user events = %d, want >= 1", eventKinds[EventMonitorNotifyUser])
	}
}
