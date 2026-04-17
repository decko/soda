package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
)

// mockPRPoller is a test double for PRPoller.
type mockPRPoller struct {
	mu sync.Mutex

	// statusResponses are returned in order for GetPRStatus calls.
	statusResponses []mockPRStatusResponse
	statusCallCount int

	// commentResponses are returned in order for GetNewComments calls.
	commentResponses []mockCommentResponse
	commentCallCount int

	// ciResponses are returned in order for GetCIStatus calls.
	ciResponses []mockCIResponse
	ciCallCount int

	// postedComments records bodies passed to PostComment.
	postedComments []string
	postCommentErr error
}

type mockPRStatusResponse struct {
	status *PRStatus
	err    error
}

type mockCommentResponse struct {
	comments []PRComment
	err      error
}

type mockCIResponse struct {
	status *CIStatus
	err    error
}

func (m *mockPRPoller) GetPRStatus(ctx context.Context, prURL string) (*PRStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.statusCallCount
	m.statusCallCount++
	if idx >= len(m.statusResponses) {
		// Default: PR is open, not approved.
		return &PRStatus{State: "open", Approved: false}, nil
	}
	resp := m.statusResponses[idx]
	return resp.status, resp.err
}

func (m *mockPRPoller) GetNewComments(ctx context.Context, prURL string, afterID string) ([]PRComment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.commentCallCount
	m.commentCallCount++
	if idx >= len(m.commentResponses) {
		return nil, nil
	}
	resp := m.commentResponses[idx]
	return resp.comments, resp.err
}

func (m *mockPRPoller) GetCIStatus(ctx context.Context, prURL string) (*CIStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.ciCallCount
	m.ciCallCount++
	if idx >= len(m.ciResponses) {
		return &CIStatus{Overall: "unknown"}, nil
	}
	resp := m.ciResponses[idx]
	return resp.status, resp.err
}

func (m *mockPRPoller) PostComment(ctx context.Context, prURL string, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.postedComments = append(m.postedComments, body)
	return m.postCommentErr
}

// setupMonitorEngine creates an engine configured for monitor testing.
// The submit phase is pre-completed with a PR URL.
func setupMonitorEngine(t *testing.T, poller PRPoller, pollingConfig *PollingConfig, opts ...func(*EngineConfig)) (*Engine, *State, *[]Event) {
	return setupMonitorEngineWithRunner(t, nil, poller, pollingConfig, opts...)
}

// setupMonitorEngineWithRunner is like setupMonitorEngine but accepts a runner
// for testing response execution.
func setupMonitorEngineWithRunner(t *testing.T, r runner.Runner, poller PRPoller, pollingConfig *PollingConfig, opts ...func(*EngineConfig)) (*Engine, *State, *[]Event) {
	t.Helper()

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Write minimal prompt templates.
	submitPrompt := "Phase: submit\nTicket: {{.Ticket.Key}}\n"
	monitorPrompt := "Phase: monitor\nTicket: {{.Ticket.Key}}\n\n## Diff\n{{.DiffContext}}\n\n## Review Comments\n\n{{.ReviewComments}}\n"
	if err := os.MkdirAll(promptDir+"/prompts", 0755); err != nil {
		t.Fatalf("MkdirAll prompts: %v", err)
	}
	if err := os.WriteFile(promptDir+"/prompts/submit.md", []byte(submitPrompt), 0644); err != nil {
		t.Fatalf("WriteFile submit.md: %v", err)
	}
	if err := os.WriteFile(promptDir+"/prompts/monitor.md", []byte(monitorPrompt), 0644); err != nil {
		t.Fatalf("WriteFile monitor.md: %v", err)
	}

	state, err := LoadOrCreate(stateDir, "TEST-MON")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Pre-complete the submit phase with a PR URL.
	state.Meta().Phases["submit"] = &PhaseState{
		Status:     PhaseCompleted,
		Generation: 1,
	}
	submitResult := []byte(`{"pr_url":"https://github.com/decko/soda/pull/49","ticket_key":"TEST-MON"}`)
	if err := state.WriteResult("submit", submitResult); err != nil {
		t.Fatalf("WriteResult submit: %v", err)
	}

	if pollingConfig == nil {
		pollingConfig = &PollingConfig{
			InitialInterval:   Duration{Duration: 1 * time.Millisecond},
			MaxInterval:       Duration{Duration: 2 * time.Millisecond},
			EscalateAfter:     Duration{Duration: 10 * time.Millisecond},
			MaxDuration:       Duration{Duration: 100 * time.Millisecond},
			MaxResponseRounds: 3,
		}
	}

	phases := []PhaseConfig{
		{
			Name:      "monitor",
			Prompt:    "prompts/monitor.md",
			Type:      "polling",
			DependsOn: []string{"submit"},
			Polling:   pollingConfig,
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
	}

	pipeline := &PhasePipeline{Phases: phases}
	loader := NewPromptLoader(promptDir)

	var events []Event

	cfg := EngineConfig{
		Pipeline:   pipeline,
		Loader:     loader,
		Ticket:     TicketData{Key: "TEST-MON", Summary: "Test monitor ticket"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {}, // no-op for tests
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		PRPoller:   poller,
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	engine := NewEngine(r, state, cfg)
	return engine, state, &events
}

func TestMonitor_PRApproved(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: true}},
		},
	}

	engine, state, events := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed when PR is approved")
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorCompleted {
		t.Errorf("monitor status = %q, want %q", monState.Status, MonitorCompleted)
	}

	// Should have monitor_pr_approved event.
	hasApproved := false
	for _, e := range *events {
		if e.Kind == EventMonitorPRApproved {
			hasApproved = true
		}
	}
	if !hasApproved {
		t.Error("monitor_pr_approved event not emitted")
	}
}

func TestMonitor_PRClosed(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "closed", Approved: false}},
		},
	}

	engine, state, events := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorFailed {
		t.Errorf("monitor status = %q, want %q", monState.Status, MonitorFailed)
	}

	hasClosed := false
	for _, e := range *events {
		if e.Kind == EventMonitorPRClosed {
			hasClosed = true
		}
	}
	if !hasClosed {
		t.Error("monitor_pr_closed event not emitted")
	}
}

func TestMonitor_PRMerged(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "merged", Approved: false}},
		},
	}

	engine, state, _ := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed when PR is merged")
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorCompleted {
		t.Errorf("monitor status = %q, want %q", monState.Status, MonitorCompleted)
	}
}

func TestMonitor_NewCommentsDetected(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}}, // approve on 2nd poll
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Please fix this"},
				{ID: "IC_2", Author: "reviewer", Body: "Also fix that"},
			}},
		},
	}

	engine, state, events := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.LastCommentID != "IC_2" {
		t.Errorf("LastCommentID = %q, want %q", monState.LastCommentID, "IC_2")
	}
	if monState.ResponseRounds != 1 {
		t.Errorf("ResponseRounds = %d, want 1", monState.ResponseRounds)
	}

	hasNewComments := false
	for _, e := range *events {
		if e.Kind == EventMonitorNewComments {
			hasNewComments = true
			count, _ := e.Data["count"].(int)
			if count != 2 {
				t.Errorf("comment count = %d, want 2", count)
			}
		}
	}
	if !hasNewComments {
		t.Error("monitor_new_comments event not emitted")
	}
}

func TestMonitor_MaxRoundsReached(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{{ID: "IC_1", Author: "r1", Body: "fix 1"}}},
			{comments: []PRComment{{ID: "IC_2", Author: "r1", Body: "fix 2"}}},
			{comments: []PRComment{{ID: "IC_3", Author: "r1", Body: "fix 3"}}},
		},
	}

	pollingCfg := &PollingConfig{
		InitialInterval:   Duration{Duration: 1 * time.Millisecond},
		MaxInterval:       Duration{Duration: 2 * time.Millisecond},
		EscalateAfter:     Duration{Duration: 10 * time.Millisecond},
		MaxDuration:       Duration{Duration: 1 * time.Second},
		MaxResponseRounds: 3,
	}

	engine, state, events := setupMonitorEngine(t, poller, pollingCfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorMaxRounds {
		t.Errorf("monitor status = %q, want %q", monState.Status, MonitorMaxRounds)
	}
	if monState.ResponseRounds != 3 {
		t.Errorf("ResponseRounds = %d, want 3", monState.ResponseRounds)
	}

	hasMaxRounds := false
	for _, e := range *events {
		if e.Kind == EventMonitorMaxRounds {
			hasMaxRounds = true
		}
	}
	if !hasMaxRounds {
		t.Error("monitor_max_rounds event not emitted")
	}
}

func TestMonitor_CIStatusChange(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		ciResponses: []mockCIResponse{
			{status: &CIStatus{
				Overall: "failure",
				Jobs: []CIJobInfo{
					{Name: "test", Status: "completed", Conclusion: "failure"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}},
		},
	}

	engine, _, events := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	hasCIChange := false
	hasCIFailure := false
	for _, e := range *events {
		if e.Kind == EventMonitorCIChange {
			hasCIChange = true
		}
		if e.Kind == EventMonitorCIFailure {
			hasCIFailure = true
			failedJobs, ok := e.Data["failed_jobs"].([]string)
			if !ok {
				t.Error("failed_jobs should be a string slice")
			} else if len(failedJobs) != 1 || failedJobs[0] != "test" {
				t.Errorf("failed_jobs = %v, want [test]", failedJobs)
			}
		}
	}
	if !hasCIChange {
		t.Error("monitor_ci_change event not emitted")
	}
	if !hasCIFailure {
		t.Error("monitor_ci_failure event not emitted")
	}
}

func TestMonitor_MaxDurationTimeout(t *testing.T) {
	// Use a time function that advances quickly.
	callCount := 0
	baseTime := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
		},
	}

	pollingCfg := &PollingConfig{
		InitialInterval:   Duration{Duration: 1 * time.Millisecond},
		MaxInterval:       Duration{Duration: 2 * time.Millisecond},
		EscalateAfter:     Duration{Duration: 10 * time.Minute},
		MaxDuration:       Duration{Duration: 1 * time.Hour},
		MaxResponseRounds: 3,
	}

	engine, state, events := setupMonitorEngine(t, poller, pollingCfg, func(cfg *EngineConfig) {
		cfg.NowFunc = func() time.Time {
			callCount++
			// First call: start time. Each subsequent call advances by 2 hours.
			if callCount <= 1 {
				return baseTime
			}
			return baseTime.Add(2 * time.Hour)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Monitor should be marked failed due to timeout.
	ps := state.Meta().Phases["monitor"]
	if ps == nil || ps.Status != PhaseFailed {
		t.Error("monitor should be failed on timeout")
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorFailed {
		t.Errorf("monitor status = %q, want %q", monState.Status, MonitorFailed)
	}

	hasTimeout := false
	for _, e := range *events {
		if e.Kind == EventMonitorTimeout {
			hasTimeout = true
		}
	}
	if !hasTimeout {
		t.Error("monitor_timeout event not emitted")
	}
}

func TestMonitor_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
		},
	}

	engine, _, _ := setupMonitorEngine(t, poller, nil, func(cfg *EngineConfig) {
		cfg.SleepFunc = func(d time.Duration) {
			cancel() // cancel during sleep
		}
	})

	err := engine.Run(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("expected cancellation error, got: %v", err)
	}
}

func TestMonitor_PollCountIncremented(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}}, // approve on 3rd poll
		},
	}

	engine, state, _ := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.PollCount != 3 {
		t.Errorf("PollCount = %d, want 3", monState.PollCount)
	}
}

func TestMonitor_FallbackToStubWithoutPoller(t *testing.T) {
	// When PRPoller is nil, should fall back to stub behavior.
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
		cfg.PRPoller = nil // explicitly no poller
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed via stub fallback")
	}

	hasMonitorSkipped := false
	for _, e := range events {
		if e.Kind == EventMonitorSkipped {
			hasMonitorSkipped = true
		}
	}
	if !hasMonitorSkipped {
		t.Error("monitor_skipped event not emitted for stub fallback")
	}
}

func TestMonitor_PollIntervalEscalation(t *testing.T) {
	polling := &PollingConfig{
		InitialInterval: Duration{Duration: 2 * time.Minute},
		MaxInterval:     Duration{Duration: 5 * time.Minute},
		EscalateAfter:   Duration{Duration: 30 * time.Minute},
		MaxDuration:     Duration{Duration: 4 * time.Hour},
	}

	engine := &Engine{config: EngineConfig{}}

	// Before escalation threshold.
	interval := engine.pollInterval(polling, 10*time.Minute)
	if interval != 2*time.Minute {
		t.Errorf("interval before escalation = %v, want 2m", interval)
	}

	// After escalation threshold.
	interval = engine.pollInterval(polling, 31*time.Minute)
	if interval != 5*time.Minute {
		t.Errorf("interval after escalation = %v, want 5m", interval)
	}

	// Exactly at escalation threshold.
	interval = engine.pollInterval(polling, 30*time.Minute)
	if interval != 5*time.Minute {
		t.Errorf("interval at escalation = %v, want 5m", interval)
	}
}

func TestMonitor_SelfCommentsSkipped(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}}, // approve on 2nd poll
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "soda-bot", Body: "I pushed a fix."},
				{ID: "IC_2", Author: "soda-bot", Body: "Updated the code."},
			}},
		},
	}

	engine, state, events := setupMonitorEngine(t, poller, nil, func(cfg *EngineConfig) {
		cfg.SelfUser = "soda-bot"
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}

	// Self-comments should NOT increment response rounds.
	if monState.ResponseRounds != 0 {
		t.Errorf("ResponseRounds = %d, want 0 (self-comments should not count)", monState.ResponseRounds)
	}

	// Should have skipped events.
	skippedCount := 0
	for _, e := range *events {
		if e.Kind == EventMonitorCommentSkipped {
			skippedCount++
		}
	}
	if skippedCount != 2 {
		t.Errorf("skipped events = %d, want 2", skippedCount)
	}
}

func TestMonitor_BotCommentsSkipped(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "dependabot", Body: "Bump version"},
				{ID: "IC_2", Author: "reviewer", Body: "Please fix this."},
			}},
		},
	}

	engine, state, events := setupMonitorEngine(t, poller, nil, func(cfg *EngineConfig) {
		cfg.SelfUser = "soda-bot"
		cfg.BotUsers = []string{"dependabot", "renovate"}
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}

	// Only the reviewer comment should be actionable.
	if monState.ResponseRounds != 1 {
		t.Errorf("ResponseRounds = %d, want 1 (only reviewer comment is actionable)", monState.ResponseRounds)
	}

	// Should have one classified and one skipped event.
	classifiedCount := 0
	skippedCount := 0
	for _, e := range *events {
		if e.Kind == EventMonitorCommentClassified {
			classifiedCount++
		}
		if e.Kind == EventMonitorCommentSkipped {
			skippedCount++
		}
	}
	if classifiedCount != 1 {
		t.Errorf("classified events = %d, want 1", classifiedCount)
	}
	if skippedCount != 1 {
		t.Errorf("skipped events = %d, want 1", skippedCount)
	}
}

func TestMonitor_ApprovalCommentsDoNotIncrementRounds(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "LGTM"},
			}},
		},
	}

	engine, state, _ := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}

	// Approval comments should NOT increment response rounds.
	if monState.ResponseRounds != 0 {
		t.Errorf("ResponseRounds = %d, want 0 (approval comments should not count)", monState.ResponseRounds)
	}
}

func TestMonitor_ProfileApplied(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: true}},
		},
	}

	profile, _ := GetMonitorProfile(ProfileAggressive)

	engine, state, events := setupMonitorEngine(t, poller, nil, func(cfg *EngineConfig) {
		cfg.MonitorProfile = profile
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed")
	}

	// Should have profile_applied event.
	hasProfileApplied := false
	for _, e := range *events {
		if e.Kind == EventMonitorProfileApplied {
			hasProfileApplied = true
			if name, _ := e.Data["profile"].(string); name != "aggressive" {
				t.Errorf("profile = %q, want %q", name, "aggressive")
			}
		}
	}
	if !hasProfileApplied {
		t.Error("monitor_profile_applied event not emitted")
	}
}

func TestMonitor_ProfileFromPollingConfig(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: true}},
		},
	}

	pollingCfg := &PollingConfig{
		Profile: ProfileConservative,
	}

	engine, state, events := setupMonitorEngine(t, poller, pollingCfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed")
	}

	hasProfileApplied := false
	for _, e := range *events {
		if e.Kind == EventMonitorProfileApplied {
			hasProfileApplied = true
			if source, _ := e.Data["source"].(string); source != "polling_config" {
				t.Errorf("source = %q, want %q", source, "polling_config")
			}
		}
	}
	if !hasProfileApplied {
		t.Error("monitor_profile_applied event not emitted for polling config profile")
	}
}

func TestMonitor_ClassificationWithAuthority(t *testing.T) {
	auth := NewCODEOWNERSAuthority([]CODEOWNERSRule{
		{Pattern: "*.go", Owners: []string{"go-owner"}},
	})

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "go-owner", Body: "Fix this bug.", Path: "main.go"},
				{ID: "IC_2", Author: "random-user", Body: "Fix this too.", Path: "main.go"},
			}},
		},
	}

	engine, state, events := setupMonitorEngine(t, poller, nil, func(cfg *EngineConfig) {
		cfg.SelfUser = "soda-bot"
		cfg.AuthorityResolver = auth
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}

	// Only go-owner's comment should be actionable.
	if monState.ResponseRounds != 1 {
		t.Errorf("ResponseRounds = %d, want 1 (only authoritative comment)", monState.ResponseRounds)
	}

	classifiedCount := 0
	skippedCount := 0
	for _, e := range *events {
		if e.Kind == EventMonitorCommentClassified {
			classifiedCount++
		}
		if e.Kind == EventMonitorCommentSkipped {
			skippedCount++
		}
	}
	if classifiedCount != 1 {
		t.Errorf("classified events = %d, want 1", classifiedCount)
	}
	if skippedCount != 1 {
		t.Errorf("skipped events = %d, want 1", skippedCount)
	}
}

func TestMonitor_CIStatusNoChangeDoesNotEmit(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		ciResponses: []mockCIResponse{
			{status: &CIStatus{Overall: "success"}},
			{status: &CIStatus{Overall: "success"}}, // same status, should not emit
		},
	}

	engine, _, events := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ciChangeCount := 0
	for _, e := range *events {
		if e.Kind == EventMonitorCIChange {
			ciChangeCount++
		}
	}
	// First transition from "" to "success" should emit, but second same status should not.
	if ciChangeCount != 1 {
		t.Errorf("CI change events = %d, want 1", ciChangeCount)
	}
}

func TestMonitor_ResponseExecution(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}}, // approve on 2nd poll
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Please fix this bug.", Path: "main.go", Line: 42},
			}},
		},
	}

	monitorOutput := json.RawMessage(`{
		"ticket_key":"TEST-MON",
		"pr_url":"https://github.com/decko/soda/pull/49",
		"comments_handled":[{"comment_id":"IC_1","author":"reviewer","content":"Please fix this bug.","action":"fixed","response":"Fixed the bug.","classification":"code_change","authoritative":true}],
		"files_changed":[{"path":"main.go","action":"modified"}],
		"commits":[{"hash":"abc123","message":"fix: address review feedback","task_id":"IC_1"}],
		"tests_passed":true
	}`)

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {{
				result: &runner.RunResult{
					Output:  monitorOutput,
					CostUSD: 0.15,
				},
			}},
		},
	}

	engine, state, events := setupMonitorEngineWithRunner(t, mock, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed")
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.ResponseRounds != 1 {
		t.Errorf("ResponseRounds = %d, want 1", monState.ResponseRounds)
	}

	// Verify runner was called.
	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("runner call count = %d, want 1", callCount)
	}

	mock.mu.Lock()
	call := mock.calls[0]
	mock.mu.Unlock()
	if call.Phase != "monitor/response_0" {
		t.Errorf("runner phase = %q, want %q", call.Phase, "monitor/response_0")
	}
	if !strings.Contains(call.SystemPrompt, "IC_1") {
		t.Error("system prompt should contain the comment ID")
	}
	if !strings.Contains(call.SystemPrompt, "Please fix this bug.") {
		t.Error("system prompt should contain the comment body")
	}

	// Verify cost was accumulated.
	if state.Meta().Phases["monitor"].Cost != 0.15 {
		t.Errorf("monitor cost = %f, want 0.15", state.Meta().Phases["monitor"].Cost)
	}
	if state.Meta().TotalCost != 0.15 {
		t.Errorf("total cost = %f, want 0.15", state.Meta().TotalCost)
	}

	// Verify response events were emitted.
	hasStarted := false
	hasCompleted := false
	for _, evt := range *events {
		if evt.Kind == EventMonitorResponseStarted {
			hasStarted = true
			if round, _ := evt.Data["response_round"].(int); round != 0 {
				t.Errorf("response_started round = %d, want 0", round)
			}
		}
		if evt.Kind == EventMonitorResponseCompleted {
			hasCompleted = true
			if handled, _ := evt.Data["comments_handled"].(int); handled != 1 {
				t.Errorf("comments_handled = %d, want 1", handled)
			}
			if cost, _ := evt.Data["cost"].(float64); cost != 0.15 {
				t.Errorf("cost = %f, want 0.15", cost)
			}
		}
	}
	if !hasStarted {
		t.Error("monitor_response_started event not emitted")
	}
	if !hasCompleted {
		t.Error("monitor_response_completed event not emitted")
	}

	// Verify result was written (for PhaseSummary consumption).
	result, err := state.ReadResult("monitor")
	if err != nil {
		t.Fatalf("ReadResult monitor: %v", err)
	}
	var parsed struct {
		CommentsHandled []json.RawMessage `json:"comments_handled"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Unmarshal monitor result: %v", err)
	}
	if len(parsed.CommentsHandled) != 1 {
		t.Errorf("result comments_handled count = %d, want 1", len(parsed.CommentsHandled))
	}
}

func TestMonitor_ResponseExecutionFailure(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this."},
			}},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {{
				err: fmt.Errorf("runner failed: API timeout"),
			}},
		},
	}

	engine, state, events := setupMonitorEngineWithRunner(t, mock, poller, nil)

	// Should not return error — response failure is non-fatal.
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed (PR approved despite response failure)")
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	// Response rounds should NOT be incremented on failure — transient failures
	// should not consume the response budget.
	if monState.ResponseRounds != 0 {
		t.Errorf("ResponseRounds = %d, want 0 (not incremented on failure)", monState.ResponseRounds)
	}

	// Should have response_failed event.
	hasFailed := false
	for _, evt := range *events {
		if evt.Kind == EventMonitorResponseFailed {
			hasFailed = true
			if errMsg, _ := evt.Data["error"].(string); !strings.Contains(errMsg, "API timeout") {
				t.Errorf("error = %q, should contain 'API timeout'", errMsg)
			}
		}
	}
	if !hasFailed {
		t.Error("monitor_response_failed event not emitted")
	}
}

func TestMonitor_ResponseRetriesTransientErrors(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this."},
			}},
		},
	}

	monitorOutput := json.RawMessage(`{
		"ticket_key":"TEST-MON",
		"pr_url":"https://github.com/decko/soda/pull/49",
		"comments_handled":[{"comment_id":"IC_1","author":"reviewer","content":"Fix this.","action":"fixed","response":"Done.","classification":"code_change","authoritative":true}],
		"tests_passed":true
	}`)

	// First call fails with transient error, second succeeds.
	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {
				{err: &runner.TransientError{Err: fmt.Errorf("API rate limit")}},
				{result: &runner.RunResult{Output: monitorOutput, CostUSD: 0.10}},
			},
		},
	}

	engine, state, events := setupMonitorEngineWithRunner(t, mock, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed after successful retry")
	}

	// Verify runner was called twice (initial + retry).
	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 2 {
		t.Errorf("runner call count = %d, want 2 (initial + retry)", callCount)
	}

	// Should have retry event.
	hasRetry := false
	for _, evt := range *events {
		if evt.Kind == EventPhaseRetrying {
			hasRetry = true
		}
	}
	if hasRetry == false {
		t.Error("phase_retrying event not emitted for transient error retry")
	}
}

func TestMonitor_NilRunnerSkipsResponse(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this."},
			}},
		},
	}

	// Use setupMonitorEngine (nil runner).
	engine, state, events := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	// Response rounds should be incremented even without a runner.
	if monState.ResponseRounds != 1 {
		t.Errorf("ResponseRounds = %d, want 1", monState.ResponseRounds)
	}

	// Should NOT have response_started event (runner is nil).
	for _, evt := range *events {
		if evt.Kind == EventMonitorResponseStarted {
			t.Error("monitor_response_started should not be emitted with nil runner")
		}
	}
}

func TestMonitor_ResponseBudgetExceeded(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this."},
			}},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{},
	}

	engine, state, events := setupMonitorEngineWithRunner(t, mock, poller, nil, func(cfg *EngineConfig) {
		cfg.MaxCostUSD = 1.0
	})

	// Set total cost to exceed budget.
	state.Meta().TotalCost = 1.5

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have budget_warning event with skipping=monitor_response.
	hasBudgetWarning := false
	for _, evt := range *events {
		if evt.Kind == EventBudgetWarning {
			if skipping, _ := evt.Data["skipping"].(string); skipping == "monitor_response" {
				hasBudgetWarning = true
			}
		}
	}
	if !hasBudgetWarning {
		t.Error("budget_warning event with skipping=monitor_response not emitted")
	}

	// Runner should NOT have been called.
	mock.mu.Lock()
	callCount := len(mock.calls)
	mock.mu.Unlock()
	if callCount != 0 {
		t.Errorf("runner call count = %d, want 0 (budget exceeded)", callCount)
	}
}

func TestMonitor_MultipleResponseRounds(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}}, // approve on 3rd poll
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this."},
			}},
			{comments: []PRComment{
				{ID: "IC_2", Author: "reviewer", Body: "Fix that too."},
			}},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-MON","pr_url":"https://github.com/decko/soda/pull/49","comments_handled":[{"comment_id":"IC_1","author":"reviewer","content":"Fix this.","action":"fixed","response":"Done.","classification":"code_change","authoritative":true}],"tests_passed":true}`),
					CostUSD: 0.10,
				},
			}},
			"monitor/response_1": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-MON","pr_url":"https://github.com/decko/soda/pull/49","comments_handled":[{"comment_id":"IC_2","author":"reviewer","content":"Fix that too.","action":"fixed","response":"Done.","classification":"code_change","authoritative":true}],"tests_passed":true}`),
					CostUSD: 0.12,
				},
			}},
		},
	}

	engine, state, events := setupMonitorEngineWithRunner(t, mock, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.ResponseRounds != 2 {
		t.Errorf("ResponseRounds = %d, want 2", monState.ResponseRounds)
	}

	// Verify costs accumulated correctly.
	expectedCost := 0.22 // 0.10 + 0.12
	if diff := state.Meta().Phases["monitor"].Cost - expectedCost; diff > 0.001 || diff < -0.001 {
		t.Errorf("monitor cost = %f, want %f", state.Meta().Phases["monitor"].Cost, expectedCost)
	}

	// Verify both response events were emitted.
	responseStarted := 0
	responseCompleted := 0
	for _, evt := range *events {
		if evt.Kind == EventMonitorResponseStarted {
			responseStarted++
		}
		if evt.Kind == EventMonitorResponseCompleted {
			responseCompleted++
		}
	}
	if responseStarted != 2 {
		t.Errorf("response_started events = %d, want 2", responseStarted)
	}
	if responseCompleted != 2 {
		t.Errorf("response_completed events = %d, want 2", responseCompleted)
	}
}

func TestMonitor_VerifyGatePassesWhenTestsPass(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this bug.", Path: "main.go"},
			}},
		},
	}

	monitorOutput := json.RawMessage(`{
		"ticket_key":"TEST-MON",
		"pr_url":"https://github.com/decko/soda/pull/49",
		"comments_handled":[{"comment_id":"IC_1","author":"reviewer","content":"Fix this bug.","action":"fixed","response":"Fixed.","classification":"code_change","authoritative":true}],
		"files_changed":[{"path":"main.go","action":"modified"}],
		"commits":[{"hash":"abc123","message":"fix: address review","task_id":"IC_1"}],
		"tests_passed":true
	}`)

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {{
				result: &runner.RunResult{Output: monitorOutput, CostUSD: 0.10},
			}},
		},
	}

	engine, _, events := setupMonitorEngineWithRunner(t, mock, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should NOT have verify_failed or notify_user events.
	for _, evt := range *events {
		if evt.Kind == EventMonitorVerifyFailed {
			t.Error("verify_failed event should not be emitted when tests pass")
		}
		if evt.Kind == EventMonitorNotifyUser {
			t.Error("notify_user event should not be emitted when tests pass")
		}
	}
}

func TestMonitor_VerifyGateFailsWhenTestsFail(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this bug.", Path: "main.go"},
			}},
		},
	}

	monitorOutput := json.RawMessage(`{
		"ticket_key":"TEST-MON",
		"pr_url":"https://github.com/decko/soda/pull/49",
		"comments_handled":[{"comment_id":"IC_1","author":"reviewer","content":"Fix this bug.","action":"fixed","response":"Fixed.","classification":"code_change","authoritative":true}],
		"files_changed":[{"path":"main.go","action":"modified"}],
		"commits":[{"hash":"abc123","message":"fix: address review","task_id":"IC_1"}],
		"tests_passed":false
	}`)

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {{
				result: &runner.RunResult{Output: monitorOutput, CostUSD: 0.10},
			}},
		},
	}

	engine, _, events := setupMonitorEngineWithRunner(t, mock, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have verify_failed and notify_user events.
	hasVerifyFailed := false
	hasNotifyUser := false
	for _, evt := range *events {
		if evt.Kind == EventMonitorVerifyFailed {
			hasVerifyFailed = true
		}
		if evt.Kind == EventMonitorNotifyUser {
			hasNotifyUser = true
			reason, _ := evt.Data["reason"].(string)
			if reason == "" {
				t.Error("notify_user event should have a reason")
			}
		}
	}
	if !hasVerifyFailed {
		t.Error("verify_failed event not emitted when tests fail")
	}
	if !hasNotifyUser {
		t.Error("notify_user event not emitted when tests fail")
	}
}

func TestMonitor_VerifyGateSkippedForReplyOnly(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Why did you do it this way?"},
			}},
		},
	}

	// No files_changed → reply only
	monitorOutput := json.RawMessage(`{
		"ticket_key":"TEST-MON",
		"pr_url":"https://github.com/decko/soda/pull/49",
		"comments_handled":[{"comment_id":"IC_1","author":"reviewer","content":"Why?","action":"explained","response":"Because...","classification":"question","authoritative":true}],
		"files_changed":[],
		"tests_passed":false
	}`)

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/reply_0": {{
				result: &runner.RunResult{Output: monitorOutput, CostUSD: 0.05},
			}},
		},
	}

	engine, _, events := setupMonitorEngineWithRunner(t, mock, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should NOT have verify_failed events for reply-only sessions.
	for _, evt := range *events {
		if evt.Kind == EventMonitorVerifyFailed {
			t.Error("verify_failed event should not be emitted for reply-only sessions")
		}
	}
}

func TestMonitor_NonAuthoritativeCannedAcknowledgment(t *testing.T) {
	auth := NewCODEOWNERSAuthority([]CODEOWNERSRule{
		{Pattern: "*.go", Owners: []string{"go-owner"}},
	})

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "random-user", Body: "Fix this please.", Path: "main.go"},
			}},
		},
	}

	engine, _, events := setupMonitorEngine(t, poller, nil, func(cfg *EngineConfig) {
		cfg.SelfUser = "soda-bot"
		cfg.AuthorityResolver = auth
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have posted a canned acknowledgment.
	poller.mu.Lock()
	posted := poller.postedComments
	poller.mu.Unlock()
	if len(posted) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(posted))
	}
	if !strings.Contains(posted[0], "@random-user") {
		t.Errorf("posted comment should mention @random-user, got: %q", posted[0])
	}

	// Should have acknowledge_posted event.
	hasAck := false
	for _, evt := range *events {
		if evt.Kind == EventMonitorAcknowledgePosted {
			hasAck = true
			if author, _ := evt.Data["author"].(string); author != "random-user" {
				t.Errorf("author = %q, want %q", author, "random-user")
			}
		}
	}
	if !hasAck {
		t.Error("monitor_acknowledge_posted event not emitted")
	}
}

func TestMonitor_ResponseSummaryPostedToPR(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this bug.", Path: "main.go"},
			}},
		},
	}

	monitorOutput := json.RawMessage(`{
		"ticket_key":"TEST-MON",
		"pr_url":"https://github.com/decko/soda/pull/49",
		"comments_handled":[{"comment_id":"IC_1","author":"reviewer","content":"Fix this bug.","action":"fixed","response":"Fixed the bug.","classification":"code_change","authoritative":true}],
		"files_changed":[{"path":"main.go","action":"modified"}],
		"commits":[{"hash":"abc123","message":"fix: address review","task_id":"IC_1"}],
		"tests_passed":true
	}`)

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {{
				result: &runner.RunResult{Output: monitorOutput, CostUSD: 0.10},
			}},
		},
	}

	engine, _, events := setupMonitorEngineWithRunner(t, mock, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have posted a reply summary.
	poller.mu.Lock()
	posted := poller.postedComments
	poller.mu.Unlock()
	if len(posted) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(posted))
	}
	if !strings.Contains(posted[0], "addressed") {
		t.Errorf("reply summary should mention 'addressed', got: %q", posted[0])
	}
	if !strings.Contains(posted[0], "tests passing") {
		t.Errorf("reply summary should mention tests passing, got: %q", posted[0])
	}

	// Should have reply_posted event.
	hasReply := false
	for _, evt := range *events {
		if evt.Kind == EventMonitorReplyPosted {
			hasReply = true
		}
	}
	if !hasReply {
		t.Error("monitor_reply_posted event not emitted")
	}
}

func TestFormatCommentsForPrompt(t *testing.T) {
	classified := []ClassifiedComment{
		{
			Comment: PRComment{
				ID:     "IC_1",
				Author: "reviewer",
				Body:   "Please fix this bug.",
				Path:   "main.go",
				Line:   42,
			},
			Type:       CommentCodeChange,
			Action:     ActionApplyFix,
			Actionable: true,
			Reason:     "code change requested",
		},
		{
			Comment: PRComment{
				ID:     "IC_2",
				Author: "reviewer",
				Body:   "LGTM",
			},
			Type:       CommentApproval,
			Action:     ActionAcknowledge,
			Actionable: false,
			Reason:     "approval/positive feedback",
		},
		{
			Comment: PRComment{
				ID:     "IC_3",
				Author: "reviewer",
				Body:   "Why did you do it this way?",
				Path:   "util.go",
			},
			Type:       CommentQuestion,
			Action:     ActionRespond,
			Actionable: true,
			Reason:     "question requiring response",
		},
	}

	result := formatCommentsForPrompt(classified)

	// Should contain all comment IDs.
	if !strings.Contains(result, "IC_1") {
		t.Error("result should contain IC_1")
	}
	if !strings.Contains(result, "IC_2") {
		t.Error("result should contain IC_2")
	}
	if !strings.Contains(result, "IC_3") {
		t.Error("result should contain IC_3")
	}

	// Should contain file paths with line numbers where applicable.
	if !strings.Contains(result, "main.go:42") {
		t.Error("result should contain main.go:42")
	}
	if !strings.Contains(result, "File: util.go") {
		t.Error("result should contain File: util.go")
	}

	// Should contain comment bodies.
	if !strings.Contains(result, "Please fix this bug.") {
		t.Error("result should contain comment body")
	}
	if !strings.Contains(result, "LGTM") {
		t.Error("result should contain LGTM")
	}

	// Should contain classifications.
	if !strings.Contains(result, "code_change") {
		t.Error("result should contain code_change classification")
	}
	if !strings.Contains(result, "approval") {
		t.Error("result should contain approval classification")
	}
	if !strings.Contains(result, "question") {
		t.Error("result should contain question classification")
	}
}

func TestIsReplyOnly(t *testing.T) {
	tests := []struct {
		name     string
		items    []ClassifiedComment
		expected bool
	}{
		{
			name:     "empty",
			items:    nil,
			expected: false,
		},
		{
			name: "all_questions",
			items: []ClassifiedComment{
				{Actionable: true, Action: ActionRespond},
				{Actionable: true, Action: ActionRespond},
			},
			expected: true,
		},
		{
			name: "mixed_fix_and_respond",
			items: []ClassifiedComment{
				{Actionable: true, Action: ActionApplyFix},
				{Actionable: true, Action: ActionRespond},
			},
			expected: false,
		},
		{
			name: "only_fixes",
			items: []ClassifiedComment{
				{Actionable: true, Action: ActionApplyFix},
			},
			expected: false,
		},
		{
			name: "non_actionable_skipped",
			items: []ClassifiedComment{
				{Actionable: false, Action: ActionSkip},
				{Actionable: true, Action: ActionRespond},
			},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isReplyOnly(tt.items)
			if got != tt.expected {
				t.Errorf("isReplyOnly = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestReplyOnlyTools(t *testing.T) {
	tools := []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash"}
	got := replyOnlyTools(tools)
	for _, tool := range got {
		if tool == "Write" || tool == "Edit" || tool == "Bash" {
			t.Errorf("replyOnlyTools should exclude %q", tool)
		}
	}
	// Should have: Read, Glob, Grep + Bash(git log:*), Bash(git diff:*),
	// Bash(git show:*), Bash(git status:*), Bash(go test:*), Bash(ls:*)
	if len(got) != 9 {
		t.Errorf("replyOnlyTools returned %d tools, want 9: %v", len(got), got)
	}
}

func TestHasCodeChanges(t *testing.T) {
	yes := []ClassifiedComment{
		{Actionable: true, Action: ActionApplyFix},
		{Actionable: true, Action: ActionRespond},
	}
	if !hasCodeChanges(yes) {
		t.Error("hasCodeChanges should return true")
	}
	no := []ClassifiedComment{
		{Actionable: true, Action: ActionRespond},
		{Actionable: false, Action: ActionApplyFix},
	}
	if hasCodeChanges(no) {
		t.Error("hasCodeChanges should return false when fix is non-actionable")
	}
}

func TestMonitor_ReplyOnlyToolRestriction(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Why did you do it this way?"},
			}},
		},
	}

	monitorOutput := json.RawMessage(`{
		"ticket_key":"TEST-MON",
		"pr_url":"https://github.com/decko/soda/pull/49",
		"comments_handled":[{"comment_id":"IC_1","author":"reviewer","content":"Why did you do it this way?","action":"explained","response":"Because...","classification":"question","authoritative":true}],
		"tests_passed":true
	}`)

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/reply_0": {{
				result: &runner.RunResult{
					Output:  monitorOutput,
					CostUSD: 0.10,
				},
			}},
		},
	}

	engine, _, _ := setupMonitorEngineWithRunner(t, mock, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify runner was called with restricted tools (no Write, no Edit).
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) != 1 {
		t.Fatalf("runner call count = %d, want 1", len(mock.calls))
	}
	call := mock.calls[0]
	for _, tool := range call.AllowedTools {
		if tool == "Write" || tool == "Edit" {
			t.Errorf("reply-only session should not have %q tool", tool)
		}
	}
}

func TestFormatCommentsForPrompt_Empty(t *testing.T) {
	result := formatCommentsForPrompt(nil)
	if result != "" {
		t.Errorf("empty classified should produce empty string, got %q", result)
	}
}

func TestCountActionable(t *testing.T) {
	classified := []ClassifiedComment{
		{Actionable: true},
		{Actionable: false},
		{Actionable: true},
		{Actionable: false},
		{Actionable: true},
	}
	if count := countActionable(classified); count != 3 {
		t.Errorf("countActionable = %d, want 3", count)
	}

	if count := countActionable(nil); count != 0 {
		t.Errorf("countActionable(nil) = %d, want 0", count)
	}
}

func TestMonitor_BudgetExceededDoesNotConsumeRound(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Fix this."},
			}},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{},
	}

	engine, state, _ := setupMonitorEngineWithRunner(t, mock, poller, nil, func(cfg *EngineConfig) {
		cfg.MaxCostUSD = 1.0
	})

	// Set total cost to exceed budget.
	state.Meta().TotalCost = 1.5

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	// Budget-skipped rounds should NOT consume response round budget.
	if monState.ResponseRounds != 0 {
		t.Errorf("ResponseRounds = %d, want 0 (budget-skipped rounds should not consume budget)", monState.ResponseRounds)
	}
}

func TestMonitor_GetNewCommentsErrorEmitsWarning(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}}, // approve on 2nd poll
		},
		commentResponses: []mockCommentResponse{
			{err: fmt.Errorf("network timeout fetching comments")},
		},
	}

	engine, _, events := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	hasWarning := false
	for _, evt := range *events {
		if evt.Kind == EventMonitorWarning {
			if w, _ := evt.Data["warning"].(string); strings.Contains(w, "get new comments") {
				hasWarning = true
			}
		}
	}
	if !hasWarning {
		t.Error("monitor_warning event not emitted when GetNewComments fails")
	}
}

func TestMonitor_GetCIStatusErrorEmitsWarning(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		ciResponses: []mockCIResponse{
			{err: fmt.Errorf("CI API unavailable")},
		},
	}

	engine, _, events := setupMonitorEngine(t, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	hasWarning := false
	for _, evt := range *events {
		if evt.Kind == EventMonitorWarning {
			if w, _ := evt.Data["warning"].(string); strings.Contains(w, "get CI status") {
				hasWarning = true
			}
		}
	}
	if !hasWarning {
		t.Error("monitor_warning event not emitted when GetCIStatus fails")
	}
}

func TestApplyProfileFilters_NilProfile(t *testing.T) {
	classified := []ClassifiedComment{
		{Type: CommentNit, Action: ActionApplyFix, Actionable: true},
	}
	result := applyProfileFilters(classified, nil)
	if len(result) != 1 || result[0].Action != ActionApplyFix {
		t.Error("nil profile should pass through unchanged")
	}
}

func TestApplyProfileFilters_DowngradeNits(t *testing.T) {
	profile := &MonitorProfile{
		Name:        ProfileConservative,
		AutoFixNits: false,
	}
	classified := []ClassifiedComment{
		{Type: CommentNit, Action: ActionApplyFix, Actionable: true, Reason: "nit"},
		{Type: CommentCodeChange, Action: ActionApplyFix, Actionable: true, Reason: "code change"},
	}
	result := applyProfileFilters(classified, profile)
	// Nit should be downgraded.
	if result[0].Action != ActionAcknowledge || result[0].Actionable {
		t.Errorf("nit should be downgraded: action=%s actionable=%v", result[0].Action, result[0].Actionable)
	}
	// Code change should remain unchanged.
	if result[1].Action != ActionApplyFix || !result[1].Actionable {
		t.Error("code change should remain unchanged")
	}
}

func TestApplyProfileFilters_SkipNonAuth(t *testing.T) {
	profile := &MonitorProfile{
		Name:             ProfileConservative,
		AutoFixNits:      true, // allow nits
		RespondToNonAuth: false,
	}
	classified := []ClassifiedComment{
		{
			Type: CommentCodeChange, Action: ActionAcknowledge,
			Actionable: false, NonAuthoritative: true, Reason: "non-auth",
		},
		{
			Type: CommentCodeChange, Action: ActionApplyFix,
			Actionable: true, NonAuthoritative: false, Reason: "authoritative",
		},
	}
	result := applyProfileFilters(classified, profile)
	// Non-auth acknowledge should be skipped.
	if result[0].Action != ActionSkip {
		t.Errorf("non-auth acknowledge should be skipped: action=%s", result[0].Action)
	}
	// Authoritative comment should remain unchanged.
	if result[1].Action != ActionApplyFix {
		t.Error("authoritative comment should remain unchanged")
	}
}

func TestApplyProfileFilters_AggressiveAllowsAll(t *testing.T) {
	profile := &MonitorProfile{
		Name:             ProfileAggressive,
		AutoFixNits:      true,
		RespondToNonAuth: true,
	}
	classified := []ClassifiedComment{
		{Type: CommentNit, Action: ActionApplyFix, Actionable: true},
		{
			Type: CommentCodeChange, Action: ActionAcknowledge,
			Actionable: false, NonAuthoritative: true,
		},
	}
	result := applyProfileFilters(classified, profile)
	if result[0].Action != ActionApplyFix || !result[0].Actionable {
		t.Error("aggressive profile should keep nit as apply_fix")
	}
	if result[1].Action != ActionAcknowledge {
		t.Error("aggressive profile should keep non-auth as acknowledge")
	}
}

func TestMonitor_ConservativeProfileDowngradesNits(t *testing.T) {
	profile, _ := GetMonitorProfile(ProfileConservative)

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "nit: rename this variable"},
			}},
		},
	}

	engine, state, _ := setupMonitorEngine(t, poller, nil, func(cfg *EngineConfig) {
		cfg.MonitorProfile = profile
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}

	// Conservative profile should NOT treat nits as actionable.
	if monState.ResponseRounds != 0 {
		t.Errorf("ResponseRounds = %d, want 0 (nits downgraded by conservative profile)", monState.ResponseRounds)
	}
}

func TestMonitor_ConservativeProfileSkipsNonAuthAcknowledgments(t *testing.T) {
	profile, _ := GetMonitorProfile(ProfileConservative)

	auth := NewCODEOWNERSAuthority([]CODEOWNERSRule{
		{Pattern: "*.go", Owners: []string{"go-owner"}},
	})

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}},
			{status: &PRStatus{State: "open", Approved: true}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{
				{ID: "IC_1", Author: "random-user", Body: "Fix this please.", Path: "main.go"},
			}},
		},
	}

	engine, _, _ := setupMonitorEngine(t, poller, nil, func(cfg *EngineConfig) {
		cfg.MonitorProfile = profile
		cfg.SelfUser = "soda-bot"
		cfg.AuthorityResolver = auth
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Conservative profile with respond_to_non_auth=false should NOT post
	// an acknowledgment for non-authoritative comments.
	poller.mu.Lock()
	posted := poller.postedComments
	poller.mu.Unlock()
	if len(posted) != 0 {
		t.Errorf("posted comments = %d, want 0 (non-auth skipped by profile)", len(posted))
	}
}

func TestMonitor_CorruptStateReturnsError(t *testing.T) {
	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: true}},
		},
	}

	engine, state, _ := setupMonitorEngine(t, poller, nil)

	// Write corrupt JSON to the monitor state file so ReadMonitorState
	// returns a parse error (not os.ErrNotExist).
	corruptPath := state.Dir() + "/monitor_state.json"
	if err := os.WriteFile(corruptPath, []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from corrupt monitor state")
	}
	if !strings.Contains(err.Error(), "read monitor state") {
		t.Errorf("expected 'read monitor state' error, got: %v", err)
	}
}
