package pipeline

import (
	"context"
	"testing"
	"time"
)

// --- missingLabels tests ---

func TestMissingLabels_AllPresent(t *testing.T) {
	got := missingLabels([]string{"ready", "approved"}, []string{"ready", "approved", "extra"})
	if len(got) != 0 {
		t.Errorf("expected no missing labels, got %v", got)
	}
}

func TestMissingLabels_SomeMissing(t *testing.T) {
	got := missingLabels([]string{"ready", "approved"}, []string{"ready"})
	if len(got) != 1 || got[0] != "approved" {
		t.Errorf("expected [approved], got %v", got)
	}
}

func TestMissingLabels_AllMissing(t *testing.T) {
	got := missingLabels([]string{"ready"}, nil)
	if len(got) != 1 || got[0] != "ready" {
		t.Errorf("expected [ready], got %v", got)
	}
}

// --- mergeMethod tests ---

func TestMergeMethod_DefaultSquash(t *testing.T) {
	engine, _ := setupEngine(t, nil, nil)
	got := engine.mergeMethod(nil)
	if got != "squash" {
		t.Errorf("expected squash, got %s", got)
	}
}

func TestMergeMethod_PollingOverrides(t *testing.T) {
	engine, _ := setupEngine(t, nil, nil)
	polling := &PollingConfig{MergeMethod: "rebase"}
	got := engine.mergeMethod(polling)
	if got != "rebase" {
		t.Errorf("expected rebase, got %s", got)
	}
}

func TestMergeMethod_EngineConfigFallback(t *testing.T) {
	engine, _ := setupEngine(t, nil, nil, func(cfg *EngineConfig) {
		cfg.MergeMethod = "merge"
	})
	got := engine.mergeMethod(&PollingConfig{}) // empty polling method
	if got != "merge" {
		t.Errorf("expected merge, got %s", got)
	}
}

// --- autoMergeTimeout tests ---

func TestAutoMergeTimeout_Default(t *testing.T) {
	engine, _ := setupEngine(t, nil, nil)
	got := engine.autoMergeTimeout(nil)
	if got != defaultAutoMergeTimeout {
		t.Errorf("expected %v, got %v", defaultAutoMergeTimeout, got)
	}
}

func TestAutoMergeTimeout_PollingOverrides(t *testing.T) {
	engine, _ := setupEngine(t, nil, nil)
	polling := &PollingConfig{AutoMergeTimeout: Duration{Duration: 10 * time.Minute}}
	got := engine.autoMergeTimeout(polling)
	if got != 10*time.Minute {
		t.Errorf("expected 10m, got %v", got)
	}
}

func TestAutoMergeTimeout_EngineConfigFallback(t *testing.T) {
	engine, _ := setupEngine(t, nil, nil, func(cfg *EngineConfig) {
		cfg.AutoMergeTimeout = 45 * time.Minute
	})
	got := engine.autoMergeTimeout(&PollingConfig{}) // zero timeout
	if got != 45*time.Minute {
		t.Errorf("expected 45m, got %v", got)
	}
}

// --- tryAutoMerge safeguard chain tests ---

func setupMergeEngine(t *testing.T, poller PRPoller, opts ...func(*EngineConfig)) (*Engine, *[]Event) {
	t.Helper()
	var events []Event
	engine, _ := setupEngine(t, nil, nil, func(cfg *EngineConfig) {
		cfg.PRPoller = poller
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
		cfg.SleepFunc = func(time.Duration) {}
		for _, opt := range opts {
			opt(cfg)
		}
	})
	return engine, &events
}

func TestTryAutoMerge_Timeout(t *testing.T) {
	poller := &mockPRPoller{}
	engine, events := setupMergeEngine(t, poller)

	past := time.Now().Add(-1 * time.Hour)
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &past,
	}
	prStatus := &PRStatus{State: "open", Approved: true}
	ciStatus := &CIStatus{Overall: "success"}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.TimedOut {
		t.Error("expected timeout")
	}
	if !result.Blocked {
		t.Error("expected blocked")
	}
	if !hasEventKind(*events, EventAutoMergeBlocked) {
		t.Error("expected auto_merge_blocked event")
	}
}

func TestTryAutoMerge_MissingLabels(t *testing.T) {
	poller := &mockPRPoller{}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, Labels: []string{"wip"}}
	ciStatus := &CIStatus{Overall: "success"}
	polling := &PollingConfig{
		AutoMerge:        true,
		MergeLabels:      []string{"ready-to-merge"},
		AutoMergeTimeout: Duration{Duration: 30 * time.Minute},
	}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Blocked {
		t.Error("expected blocked due to missing labels")
	}
	if !hasEventKind(*events, EventAutoMergeBlocked) {
		t.Error("expected auto_merge_blocked event")
	}
}

func TestTryAutoMerge_NotApproved(t *testing.T) {
	poller := &mockPRPoller{}
	engine, _ := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: false}
	ciStatus := &CIStatus{Overall: "success"}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Blocked {
		t.Error("expected blocked due to not approved")
	}
}

func TestTryAutoMerge_CINotGreen(t *testing.T) {
	poller := &mockPRPoller{}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "pending", CommitSHA: "abc123"}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Blocked {
		t.Error("expected blocked due to CI not green")
	}
	if !hasEventKind(*events, EventAutoMergeBlocked) {
		t.Error("expected auto_merge_blocked event")
	}
}

func TestTryAutoMerge_CISHAStale(t *testing.T) {
	poller := &mockPRPoller{}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "old999"}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Blocked {
		t.Error("expected blocked due to stale CI SHA")
	}
	found := false
	for _, ev := range *events {
		if ev.Kind == EventAutoMergeBlocked {
			if ev.Data["reason"] == "ci_sha_stale" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected auto_merge_blocked event with reason=ci_sha_stale")
	}
}

func TestTryAutoMerge_DryRun(t *testing.T) {
	poller := &mockPRPoller{}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "abc123"}
	polling := &PollingConfig{AutoMerge: false, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}} // auto_merge disabled

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.DryRun {
		t.Error("expected dry run when auto_merge is disabled")
	}
	if !hasEventKind(*events, EventAutoMergeDryRun) {
		t.Error("expected auto_merge_dry_run event")
	}
	if !monState.DryRunLogged {
		t.Error("expected DryRunLogged to be set")
	}
}

func TestTryAutoMerge_DryRunNotDuplicated(t *testing.T) {
	poller := &mockPRPoller{}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
		DryRunLogged: true, // already logged
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "abc123"}
	polling := &PollingConfig{AutoMerge: false, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.DryRun {
		t.Error("expected dry run")
	}
	if hasEventKind(*events, EventAutoMergeDryRun) {
		t.Error("expected no duplicate dry_run event when DryRunLogged is already true")
	}
}

func TestTryAutoMerge_MergeSuccess(t *testing.T) {
	poller := &mockPRPoller{}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "abc123"}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Merged {
		t.Error("expected merge success")
	}
	if !hasEventKind(*events, EventAutoMergeCompleted) {
		t.Error("expected auto_merge_completed event")
	}
	// Verify MergePR was called with correct method
	if len(poller.mergeCalls) != 1 {
		t.Fatalf("expected 1 merge call, got %d", len(poller.mergeCalls))
	}
	if poller.mergeCalls[0].Method != "squash" {
		t.Errorf("expected squash method, got %s", poller.mergeCalls[0].Method)
	}
}

func TestTryAutoMerge_AlreadyMerged(t *testing.T) {
	poller := &mockPRPoller{
		mergeErr: ErrPRAlreadyMerged,
	}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "abc123"}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Merged {
		t.Error("expected merged=true for already-merged PR")
	}
	if !hasEventKind(*events, EventAutoMergeCompleted) {
		t.Error("expected auto_merge_completed event")
	}
}

func TestTryAutoMerge_MergeConflict(t *testing.T) {
	poller := &mockPRPoller{
		mergeErr: ErrMergeConflict,
	}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "abc123"}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.RebaseConflict {
		t.Error("expected rebase conflict")
	}
	if !hasEventKind(*events, EventRebaseConflict) {
		t.Error("expected rebase_conflict event")
	}
}

func TestTryAutoMerge_PRClosed(t *testing.T) {
	poller := &mockPRPoller{
		mergeErr: ErrPRClosed,
	}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "abc123"}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Blocked {
		t.Error("expected blocked for closed PR")
	}
	if !hasEventKind(*events, EventAutoMergeBlocked) {
		t.Error("expected auto_merge_blocked event")
	}
}

func TestTryAutoMerge_CustomMergeMethod(t *testing.T) {
	poller := &mockPRPoller{}
	engine, _ := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true, HeadSHA: "abc123"}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "abc123"}
	polling := &PollingConfig{
		AutoMerge:        true,
		MergeMethod:      "rebase",
		AutoMergeTimeout: Duration{Duration: 30 * time.Minute},
	}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Merged {
		t.Error("expected merge success")
	}
	if len(poller.mergeCalls) != 1 {
		t.Fatalf("expected 1 merge call, got %d", len(poller.mergeCalls))
	}
	if poller.mergeCalls[0].Method != "rebase" {
		t.Errorf("expected rebase method, got %s", poller.mergeCalls[0].Method)
	}
}

func TestTryAutoMerge_LabelsPassWhenPresent(t *testing.T) {
	poller := &mockPRPoller{}
	engine, _ := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{
		State:    "open",
		Approved: true,
		HeadSHA:  "abc123",
		Labels:   []string{"ready-to-merge", "ci-green"},
	}
	ciStatus := &CIStatus{Overall: "success", CommitSHA: "abc123"}
	polling := &PollingConfig{
		AutoMerge:        true,
		MergeLabels:      []string{"ready-to-merge"},
		AutoMergeTimeout: Duration{Duration: 30 * time.Minute},
	}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, ciStatus, polling)
	if !result.Merged {
		t.Error("expected merge success when all required labels are present")
	}
}

func TestTryAutoMerge_NilCIStatus(t *testing.T) {
	poller := &mockPRPoller{}
	engine, events := setupMergeEngine(t, poller)

	now := time.Now()
	monState := &MonitorState{
		PRURL:        "https://github.com/o/r/pull/1",
		ApprovalTime: &now,
	}
	prStatus := &PRStatus{State: "open", Approved: true}
	polling := &PollingConfig{AutoMerge: true, AutoMergeTimeout: Duration{Duration: 30 * time.Minute}}

	result := engine.tryAutoMerge(context.Background(), "monitor", monState, prStatus, nil, polling)
	if !result.Blocked {
		t.Error("expected blocked when CI status is nil")
	}
	found := false
	for _, ev := range *events {
		if ev.Kind == EventAutoMergeBlocked && ev.Data["reason"] == "ci_not_green" {
			found = true
		}
	}
	if !found {
		t.Error("expected auto_merge_blocked with reason ci_not_green")
	}
}

// hasEventKind checks if any event in the slice has the given kind.
func hasEventKind(events []Event, kind string) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
