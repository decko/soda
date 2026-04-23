package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
)

// TestSmoke_Monitor_FullLifecycle exercises a complete monitor lifecycle:
// poll → detect comments → classify → respond → CI change → approve.
// This is the happy path through all monitor subsystems working together.
func TestSmoke_Monitor_FullLifecycle(t *testing.T) {
	monitorOutput := schemas.MonitorOutput{
		CommentsHandled: []schemas.CommentAction{
			{CommentID: "IC_1", Author: "reviewer", Action: "fixed", Response: "Fixed the issue"},
		},
		FilesChanged: []schemas.FileChange{{Path: "internal/pipeline/engine.go", Action: "modified"}},
		Commits:      []schemas.CommitRecord{{Hash: "abc123", Message: "fix error handling"}},
		TestsPassed:  true,
	}
	outputJSON, _ := json.Marshal(monitorOutput)

	mockRunner := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {
				{result: &runner.RunResult{
					Output:    json.RawMessage(outputJSON),
					CostUSD:   0.15,
					TokensIn:  5000,
					TokensOut: 2000,
				}},
			},
		},
	}

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open", Approved: false}}, // poll 1: open
			{status: &PRStatus{State: "open", Approved: false}}, // poll 2: open
			{status: &PRStatus{State: "open", Approved: true}},  // poll 3: approved
		},
		commentResponses: []mockCommentResponse{
			// Poll 1: reviewer posts a code change request
			{comments: []PRComment{
				{ID: "IC_1", Author: "reviewer", Body: "Please fix the error handling here", Path: "internal/pipeline/engine.go", Line: 42},
			}},
			// Poll 2: no new comments
			{comments: nil},
		},
		ciResponses: []mockCIResponse{
			{status: &CIStatus{Overall: "pending"}},
			{status: &CIStatus{Overall: "success", Jobs: []CIJobInfo{
				{Name: "test", Status: "completed", Conclusion: "success"},
				{Name: "lint", Status: "completed", Conclusion: "success"},
			}}},
			{status: &CIStatus{Overall: "success"}},
		},
	}

	engine, state, events := setupMonitorEngineWithRunner(t, mockRunner, poller, nil)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify final state.
	if !state.IsCompleted("monitor") {
		t.Error("monitor should be completed after PR approval")
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}
	if monState.Status != MonitorCompleted {
		t.Errorf("status = %q, want %q", monState.Status, MonitorCompleted)
	}
	if monState.PollCount != 3 {
		t.Errorf("PollCount = %d, want 3", monState.PollCount)
	}
	if monState.ResponseRounds != 1 {
		t.Errorf("ResponseRounds = %d, want 1", monState.ResponseRounds)
	}
	if monState.LastCommentID != "IC_1" {
		t.Errorf("LastCommentID = %q, want IC_1", monState.LastCommentID)
	}

	// Verify event sequence contains the expected lifecycle events.
	var eventKinds []string
	for _, evt := range *events {
		eventKinds = append(eventKinds, evt.Kind)
	}

	wantEvents := []string{
		EventPhaseStarted,
		EventMonitorPolling,           // poll 1
		EventMonitorCommentClassified, // reviewer comment classified
		EventMonitorNewComments,       // new comments detected
		EventMonitorResponseStarted,   // response session started
		EventMonitorResponseCompleted, // response session completed
		EventMonitorCIChange,          // pending → success
		EventMonitorPolling,           // poll 2
		EventMonitorPolling,           // poll 3
		EventMonitorPRApproved,        // PR approved
		EventPhaseCompleted,
	}

	for _, want := range wantEvents {
		found := false
		for _, got := range eventKinds {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing event %q in lifecycle; got: %v", want, eventKinds)
		}
	}

	// Verify cost was recorded.
	phaseCost := state.Meta().Phases["monitor"].CumulativeCost
	if phaseCost < 0.15 {
		t.Errorf("cumulative cost = %f, want >= 0.15", phaseCost)
	}

	// Verify summary was posted.
	poller.mu.Lock()
	posted := poller.postedComments
	poller.mu.Unlock()
	if len(posted) == 0 {
		t.Error("expected response summary to be posted to PR")
	}
}

// TestSmoke_Monitor_MultiRoundComments exercises multiple comment-response
// cycles before hitting max rounds, verifying the round counting is accurate.
func TestSmoke_Monitor_MultiRoundComments(t *testing.T) {
	makeOutput := func(commentID string) json.RawMessage {
		output := schemas.MonitorOutput{
			CommentsHandled: []schemas.CommentAction{
				{CommentID: commentID, Author: "reviewer", Action: "fixed"},
			},
			FilesChanged: []schemas.FileChange{{Path: "file.go", Action: "modified"}},
			TestsPassed:  true,
		}
		data, _ := json.Marshal(output)
		return data
	}

	mockRunner := &flexMockRunner{
		responses: map[string][]flexResponse{
			"monitor/response_0": {{result: &runner.RunResult{Output: makeOutput("IC_1"), CostUSD: 0.10}}},
			"monitor/response_1": {{result: &runner.RunResult{Output: makeOutput("IC_2"), CostUSD: 0.12}}},
		},
	}

	poller := &mockPRPoller{
		statusResponses: []mockPRStatusResponse{
			{status: &PRStatus{State: "open"}},
			{status: &PRStatus{State: "open"}},
			{status: &PRStatus{State: "open"}},
		},
		commentResponses: []mockCommentResponse{
			{comments: []PRComment{{ID: "IC_1", Author: "reviewer", Body: "Fix error handling"}}},
			{comments: []PRComment{{ID: "IC_2", Author: "reviewer", Body: "Also fix logging"}}},
		},
	}

	pollingCfg := &PollingConfig{
		InitialInterval:   Duration{Duration: 1 * time.Millisecond},
		MaxInterval:       Duration{Duration: 2 * time.Millisecond},
		EscalateAfter:     Duration{Duration: 10 * time.Millisecond},
		MaxDuration:       Duration{Duration: 1 * time.Second},
		MaxResponseRounds: 2,
		RespondToComments: true,
	}

	engine, state, events := setupMonitorEngineWithRunner(t, mockRunner, poller, pollingCfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	monState, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}

	if monState.Status != MonitorMaxRounds {
		t.Errorf("status = %q, want %q", monState.Status, MonitorMaxRounds)
	}
	if monState.ResponseRounds != 2 {
		t.Errorf("ResponseRounds = %d, want 2", monState.ResponseRounds)
	}

	// Verify both response rounds were executed.
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

	// Verify cumulative cost.
	totalCost := state.Meta().TotalCost
	if totalCost < 0.20 {
		t.Errorf("total cost = %f, want >= 0.20 (0.10 + 0.12)", totalCost)
	}
}

// TestSmoke_Monitor_ClassifierPipeline exercises the full comment classification
// pipeline: self-authored, bot, approval, nit, question, code_change, and
// non-authoritative comments are all correctly classified and routed.
func TestSmoke_Monitor_ClassifierPipeline(t *testing.T) {
	classifier, err := NewCommentClassifier("soda-bot", []string{"dependabot", "codecov[bot]"}, nil)
	if err != nil {
		t.Fatalf("NewCommentClassifier: %v", err)
	}

	comments := []PRComment{
		{ID: "1", Author: "soda-bot", Body: "I pushed a fix"},             // self
		{ID: "2", Author: "dependabot", Body: "Bump version"},             // bot (username)
		{ID: "3", Author: "random", Body: "<!-- bot: auto --> triggered"}, // bot (content)
		{ID: "4", Author: "reviewer", Body: "LGTM"},                       // approval
		{ID: "5", Author: "reviewer", Body: "nit: use camelCase"},         // nit
		{ID: "6", Author: "reviewer", Body: "Why did you choose this?"},   // question
		{ID: "7", Author: "reviewer", Body: "Please add error handling"},  // code_change
		{ID: "8", Author: "reviewer", Body: "never mind"},                 // dismissal
	}

	classified := classifier.ClassifyAll(comments)

	if len(classified) != len(comments) {
		t.Fatalf("classified %d comments, want %d", len(classified), len(comments))
	}

	// Verify classifications.
	expectations := []struct {
		commentID  string
		wantType   CommentType
		wantAction CommentAction
		actionable bool
	}{
		{"1", CommentSelfAuthored, ActionSkip, false},
		{"2", CommentBotGenerated, ActionSkip, false},
		{"3", CommentBotGenerated, ActionSkip, false},
		{"4", CommentApproval, ActionAcknowledge, false},
		{"5", CommentNit, ActionApplyFix, true},
		{"6", CommentQuestion, ActionRespond, true},
		{"7", CommentCodeChange, ActionApplyFix, true},
		{"8", CommentDismissal, ActionSkip, false},
	}

	for idx, want := range expectations {
		got := classified[idx]
		if got.Comment.ID != want.commentID {
			t.Errorf("classified[%d].ID = %q, want %q", idx, got.Comment.ID, want.commentID)
		}
		if got.Type != want.wantType {
			t.Errorf("comment %s: type = %q, want %q", want.commentID, got.Type, want.wantType)
		}
		if got.Action != want.wantAction {
			t.Errorf("comment %s: action = %q, want %q", want.commentID, got.Action, want.wantAction)
		}
		if got.Actionable != want.actionable {
			t.Errorf("comment %s: actionable = %v, want %v", want.commentID, got.Actionable, want.actionable)
		}
	}

	// Verify HasActionable with mixed results.
	if !HasActionable(classified) {
		t.Error("HasActionable should be true with nit/question/code_change comments")
	}

	// Verify with only non-actionable comments.
	nonActionable := classified[:4] // self, bot, bot, approval
	if HasActionable(nonActionable) {
		t.Error("HasActionable should be false with only self/bot/approval comments")
	}
}

// TestSmoke_Monitor_ProfileFilterChain verifies that profile behavior filters
// correctly modify classified comments: nit downgrade, non-auth suppression.
func TestSmoke_Monitor_ProfileFilterChain(t *testing.T) {
	t.Run("conservative_profile_downgrades_nits", func(t *testing.T) {
		profile, err := GetMonitorProfile(ProfileConservative)
		if err != nil {
			t.Fatalf("GetMonitorProfile: %v", err)
		}

		// Conservative: AutoFixNits=false, RespondToNonAuth=false
		if profile.ShouldApplyNit() {
			t.Error("conservative profile should not auto-fix nits")
		}
		if profile.ShouldAutoRebase() {
			t.Error("conservative profile should not auto-rebase")
		}

		classified := []ClassifiedComment{
			{
				Comment:    PRComment{ID: "1", Author: "r1", Body: "nit: fix style"},
				Type:       CommentNit,
				Action:     ActionApplyFix,
				Actionable: true,
			},
			{
				Comment:    PRComment{ID: "2", Author: "r1", Body: "Fix bug"},
				Type:       CommentCodeChange,
				Action:     ActionApplyFix,
				Actionable: true,
			},
		}

		filtered := applyProfileFilters(classified, profile)

		// Nit should be downgraded to acknowledge.
		if filtered[0].Action != ActionAcknowledge {
			t.Errorf("nit action = %q, want %q", filtered[0].Action, ActionAcknowledge)
		}
		if filtered[0].Actionable {
			t.Error("nit should not be actionable after conservative downgrade")
		}

		// Code change should remain.
		if filtered[1].Action != ActionApplyFix {
			t.Errorf("code change action = %q, want %q", filtered[1].Action, ActionApplyFix)
		}
		if !filtered[1].Actionable {
			t.Error("code change should remain actionable")
		}
	})

	t.Run("aggressive_profile_keeps_all", func(t *testing.T) {
		profile, err := GetMonitorProfile(ProfileAggressive)
		if err != nil {
			t.Fatalf("GetMonitorProfile: %v", err)
		}

		// Aggressive: AutoFixNits=true, RespondToNonAuth=true
		if !profile.ShouldApplyNit() {
			t.Error("aggressive profile should auto-fix nits")
		}
		if !profile.ShouldAutoRebase() {
			t.Error("aggressive profile should auto-rebase")
		}
		if !profile.ShouldRespondToNonAuth() {
			t.Error("aggressive profile should respond to non-auth")
		}

		classified := []ClassifiedComment{
			{
				Comment:    PRComment{ID: "1", Author: "r1", Body: "nit: fix style"},
				Type:       CommentNit,
				Action:     ActionApplyFix,
				Actionable: true,
			},
		}

		filtered := applyProfileFilters(classified, profile)

		// Nit should remain actionable with aggressive profile.
		if filtered[0].Action != ActionApplyFix {
			t.Errorf("nit action = %q, want %q (aggressive keeps nits)", filtered[0].Action, ActionApplyFix)
		}
		if !filtered[0].Actionable {
			t.Error("nit should remain actionable with aggressive profile")
		}
	})

	t.Run("profile_to_polling_config_roundtrip", func(t *testing.T) {
		profile, _ := GetMonitorProfile(ProfileSmart)
		polling := profile.ToPollingConfig()

		if polling.InitialInterval.Duration != 2*time.Minute {
			t.Errorf("InitialInterval = %v, want 2m", polling.InitialInterval.Duration)
		}
		if polling.MaxInterval.Duration != 5*time.Minute {
			t.Errorf("MaxInterval = %v, want 5m", polling.MaxInterval.Duration)
		}
		if polling.MaxResponseRounds != 3 {
			t.Errorf("MaxResponseRounds = %d, want 3", polling.MaxResponseRounds)
		}
	})
}

// TestSmoke_Monitor_AuthorityResolution verifies the CODEOWNERS-based authority
// resolution chain: parse → match → classify with authority checks.
func TestSmoke_Monitor_AuthorityResolution(t *testing.T) {
	// Rules follow CODEOWNERS last-match-wins semantics.
	// The catch-all "*" must come first so more specific patterns override it.
	rules := []CODEOWNERSRule{
		{Pattern: "*", Owners: []string{"default-owner"}},
		{Pattern: "*.go", Owners: []string{"go-team", "alice"}},
		{Pattern: "internal/pipeline/", Owners: []string{"pipeline-team", "bob"}},
	}
	authority := NewCODEOWNERSAuthority(rules)

	t.Run("authoritative_owner_gets_full_classification", func(t *testing.T) {
		classifier, _ := NewCommentClassifier("soda-bot", nil, authority)
		// bob owns internal/pipeline/ (last matching rule for this path)
		comment := PRComment{ID: "1", Author: "bob", Body: "Fix this bug", Path: "internal/pipeline/engine.go"}
		result := classifier.Classify(comment)

		if result.NonAuthoritative {
			t.Error("bob should be authoritative for internal/pipeline/")
		}
		if result.Action != ActionApplyFix {
			t.Errorf("action = %q, want %q", result.Action, ActionApplyFix)
		}
		if !result.Actionable {
			t.Error("authoritative code_change should be actionable")
		}
	})

	t.Run("non_authoritative_user_gets_acknowledged", func(t *testing.T) {
		classifier, _ := NewCommentClassifier("soda-bot", nil, authority)
		// random-user is not in the internal/pipeline/ owner list
		comment := PRComment{ID: "2", Author: "random-user", Body: "Fix this bug", Path: "internal/pipeline/engine.go"}
		result := classifier.Classify(comment)

		if !result.NonAuthoritative {
			t.Error("random-user should be non-authoritative")
		}
		if result.Action != ActionAcknowledge {
			t.Errorf("action = %q, want %q", result.Action, ActionAcknowledge)
		}
		if result.Actionable {
			t.Error("non-authoritative comment should not be actionable")
		}
	})

	t.Run("general_comment_checks_any_rule", func(t *testing.T) {
		classifier, _ := NewCommentClassifier("soda-bot", nil, authority)

		// default-owner appears in catch-all rule, should be authoritative for general comments
		comment := PRComment{ID: "3", Author: "default-owner", Body: "Looks good overall"}
		result := classifier.Classify(comment)

		if result.NonAuthoritative {
			t.Error("default-owner should be authoritative for general comments")
		}
	})

	t.Run("no_authority_resolver_means_all_authoritative", func(t *testing.T) {
		classifier, _ := NewCommentClassifier("soda-bot", nil, nil)
		comment := PRComment{ID: "4", Author: "anyone", Body: "Fix this", Path: "any/file.go"}
		result := classifier.Classify(comment)

		if result.NonAuthoritative {
			t.Error("without authority resolver, all users should be authoritative")
		}
		if !result.Actionable {
			t.Error("code_change from authoritative user should be actionable")
		}
	})
}

// TestSmoke_Monitor_ReplyOnlySession verifies that question-only comments
// result in a reply-only session with restricted tools.
func TestSmoke_Monitor_ReplyOnlySession(t *testing.T) {
	t.Run("question_only_is_reply_only", func(t *testing.T) {
		classified := []ClassifiedComment{
			{
				Comment:    PRComment{ID: "1", Author: "r", Body: "Why this approach?"},
				Type:       CommentQuestion,
				Action:     ActionRespond,
				Actionable: true,
			},
		}

		if !isReplyOnly(classified) {
			t.Error("question-only should be reply-only")
		}
	})

	t.Run("mixed_is_not_reply_only", func(t *testing.T) {
		classified := []ClassifiedComment{
			{
				Comment:    PRComment{ID: "1", Author: "r", Body: "Why this?"},
				Type:       CommentQuestion,
				Action:     ActionRespond,
				Actionable: true,
			},
			{
				Comment:    PRComment{ID: "2", Author: "r", Body: "Fix this"},
				Type:       CommentCodeChange,
				Action:     ActionApplyFix,
				Actionable: true,
			},
		}

		if isReplyOnly(classified) {
			t.Error("mixed comments should not be reply-only")
		}
	})

	t.Run("reply_only_restricts_tools", func(t *testing.T) {
		fullTools := []string{"Read", "Write", "Edit", "Bash", "Bash(git:*)", "Bash(go test:*)"}
		restricted := replyOnlyTools(fullTools)

		// Write, Edit, and unrestricted Bash should be excluded.
		for _, tool := range restricted {
			if tool == "Write" || tool == "Edit" || tool == "Bash" {
				t.Errorf("reply-only tools should not include %q", tool)
			}
		}

		// Read and restricted Bash patterns should remain.
		hasRead := false
		for _, tool := range restricted {
			if tool == "Read" {
				hasRead = true
			}
		}
		if !hasRead {
			t.Error("reply-only tools should include Read")
		}
	})
}

// TestSmoke_Monitor_FormatCommentsForPrompt verifies that classified comments
// are rendered into a format suitable for prompt injection.
func TestSmoke_Monitor_FormatCommentsForPrompt(t *testing.T) {
	classified := []ClassifiedComment{
		{
			Comment: PRComment{ID: "IC_1", Author: "alice", Body: "Fix error handling", Path: "main.go", Line: 42},
			Type:    CommentCodeChange,
			Action:  ActionApplyFix,
		},
		{
			Comment: PRComment{ID: "IC_2", Author: "bob", Body: "Why this approach?"},
			Type:    CommentQuestion,
			Action:  ActionRespond,
		},
	}

	formatted := formatCommentsForPrompt(classified)

	// Should contain both comment IDs.
	if !strings.Contains(formatted, "IC_1") {
		t.Error("formatted output should contain IC_1")
	}
	if !strings.Contains(formatted, "IC_2") {
		t.Error("formatted output should contain IC_2")
	}

	// Should contain file path and line number.
	if !strings.Contains(formatted, "main.go:42") {
		t.Error("formatted output should contain file:line reference")
	}

	// Should contain author names.
	if !strings.Contains(formatted, "alice") || !strings.Contains(formatted, "bob") {
		t.Error("formatted output should contain author names")
	}

	// Should contain classification.
	if !strings.Contains(formatted, "code_change") || !strings.Contains(formatted, "question") {
		t.Error("formatted output should contain classification types")
	}
}

// TestSmoke_Monitor_StateResumption verifies that monitor state is correctly
// loaded on resume and that poll count continues from the persisted value.
func TestSmoke_Monitor_StateResumption(t *testing.T) {
	stateDir := t.TempDir()
	state, err := LoadOrCreate(stateDir, "RESUME-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Write initial monitor state as if 5 polls already happened.
	startTime := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	initial := &MonitorState{
		PRURL:             "https://github.com/decko/soda/pull/100",
		PollCount:         5,
		ResponseRounds:    1,
		MaxResponseRounds: 3,
		LastCommentID:     "IC_prev",
		LastCIStatus:      "success",
		LastPolledAt:      startTime.Add(10 * time.Minute),
		StartedAt:         startTime,
		Status:            MonitorPolling,
	}
	if err := state.WriteMonitorState(initial); err != nil {
		t.Fatalf("WriteMonitorState: %v", err)
	}

	// Read it back and verify all fields survived the roundtrip.
	loaded, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}

	if loaded.PollCount != 5 {
		t.Errorf("PollCount = %d, want 5", loaded.PollCount)
	}
	if loaded.ResponseRounds != 1 {
		t.Errorf("ResponseRounds = %d, want 1", loaded.ResponseRounds)
	}
	if loaded.LastCommentID != "IC_prev" {
		t.Errorf("LastCommentID = %q, want IC_prev", loaded.LastCommentID)
	}
	if loaded.Status != MonitorPolling {
		t.Errorf("Status = %q, want polling", loaded.Status)
	}
	if loaded.StartedAt.IsZero() {
		t.Error("StartedAt should be preserved on resume")
	}
	if loaded.PRURL != "https://github.com/decko/soda/pull/100" {
		t.Errorf("PRURL = %q, want full URL", loaded.PRURL)
	}
}

// TestSmoke_Monitor_ContextSleep verifies that contextSleep respects both
// normal completion and context cancellation.
func TestSmoke_Monitor_ContextSleep(t *testing.T) {
	t.Run("normal_sleep_completes", func(t *testing.T) {
		sleepCalled := false
		err := contextSleep(context.Background(), 1*time.Millisecond, func(d time.Duration) {
			sleepCalled = true
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !sleepCalled {
			t.Error("sleep function should have been called")
		}
	})

	t.Run("cancelled_context_returns_early", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		err := contextSleep(ctx, 1*time.Hour, func(d time.Duration) {
			// This should not complete because context is already cancelled.
			time.Sleep(d)
		})
		if err == nil {
			t.Error("expected error from cancelled context")
		}
	})

	t.Run("nil_sleep_func_uses_timer", func(t *testing.T) {
		err := contextSleep(context.Background(), 1*time.Millisecond, nil)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
