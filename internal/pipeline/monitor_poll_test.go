package pipeline

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
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

// setupMonitorEngine creates an engine configured for monitor testing.
// The submit phase is pre-completed with a PR URL.
func setupMonitorEngine(t *testing.T, poller PRPoller, pollingConfig *PollingConfig, opts ...func(*EngineConfig)) (*Engine, *State, *[]Event) {
	t.Helper()

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Write minimal prompt templates.
	submitPrompt := fmt.Sprintf("Phase: submit\nTicket: {{.Ticket.Key}}\n")
	monitorPrompt := fmt.Sprintf("Phase: monitor\nTicket: {{.Ticket.Key}}\n")
	os.MkdirAll(promptDir+"/prompts", 0755)
	os.WriteFile(promptDir+"/prompts/submit.md", []byte(submitPrompt), 0644)
	os.WriteFile(promptDir+"/prompts/monitor.md", []byte(monitorPrompt), 0644)

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

	engine := NewEngine(nil, state, cfg) // runner not needed for monitor
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
	if !contains(err.Error(), "cancel") {
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

func TestFormatCIFailure(t *testing.T) {
	tests := []struct {
		name string
		jobs []CIJobInfo
		want string
	}{
		{
			name: "single_failure",
			jobs: []CIJobInfo{
				{Name: "test", Conclusion: "failure"},
				{Name: "lint", Conclusion: "success"},
			},
			want: "CI failed: test (failure)",
		},
		{
			name: "multiple_failures",
			jobs: []CIJobInfo{
				{Name: "test", Conclusion: "failure"},
				{Name: "build", Conclusion: "timed_out"},
			},
			want: "CI failed: test (failure), build (timed_out)",
		},
		{
			name: "with_exit_code",
			jobs: []CIJobInfo{
				{Name: "test", Conclusion: "failure", ExitCode: 1},
			},
			want: "CI failed: test (exit 1)",
		},
		{
			name: "no_failures",
			jobs: []CIJobInfo{
				{Name: "test", Conclusion: "success"},
			},
			want: "CI failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCIFailure(tt.jobs)
			if got != tt.want {
				t.Errorf("formatCIFailure = %q, want %q", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
