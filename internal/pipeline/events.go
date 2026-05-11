package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Event represents a single structured event in events.jsonl.
type Event struct {
	Timestamp time.Time      `json:"timestamp"`
	Phase     string         `json:"phase"`
	Kind      string         `json:"kind"`
	Data      map[string]any `json:"data,omitempty"`
}

// Event kinds emitted by the engine.
const (
	EventEngineStarted            = "engine_started"
	EventEngineCompleted          = "engine_completed"
	EventEngineFailed             = "engine_failed"
	EventPhaseStarted             = "phase_started"
	EventPhaseCompleted           = "phase_completed"
	EventPhaseFailed              = "phase_failed"
	EventPhaseRetrying            = "phase_retrying"
	EventPhaseSkipped             = "phase_skipped"
	EventOutputChunk              = "output_chunk"
	EventBudgetWarning            = "budget_warning"
	EventPhaseBudgetWarning       = "phase_budget_warning"
	EventPhaseBudgetExceeded      = "phase_budget_exceeded"
	EventGenerationBudgetWarning  = "generation_budget_warning"
	EventGenerationBudgetExceeded = "generation_budget_exceeded"
	EventCheckpointPause          = "checkpoint_pause"
	EventWorktreeCreated          = "worktree_created"
	EventMonitorSkipped           = "monitor_skipped"
	EventMonitorPolling           = "monitor_polling"
	EventMonitorNewComments       = "monitor_new_comments"
	EventMonitorCIChange          = "monitor_ci_change"
	EventMonitorCIFailure         = "monitor_ci_failure"
	EventMonitorConflict          = "monitor_conflict"
	EventMonitorRebaseOK          = "monitor_rebase_ok"
	EventMonitorRebaseFailed      = "monitor_rebase_failed"
	EventMonitorPRApproved        = "monitor_pr_approved"
	EventMonitorPRClosed          = "monitor_pr_closed"
	EventMonitorMaxRounds         = "monitor_max_rounds"
	EventMonitorTimeout           = "monitor_timeout"
	EventMonitorCompleted         = "monitor_completed"
	EventMonitorCommentClassified = "monitor_comment_classified"
	EventMonitorCommentSkipped    = "monitor_comment_skipped"
	EventMonitorProfileApplied    = "monitor_profile_applied"
	EventMonitorWarning           = "monitor_warning"
	EventMonitorResponseStarted   = "monitor_response_started"
	EventMonitorResponseCompleted = "monitor_response_completed"
	EventMonitorResponseFailed    = "monitor_response_failed"
	EventMonitorVerifyFailed      = "monitor_verify_failed"
	EventMonitorAcknowledgePosted = "monitor_acknowledge_posted"
	EventMonitorReplyPosted       = "monitor_reply_posted"
	EventMonitorNotifyUser        = "monitor_notify_user"
	EventPlanSkippedByTriage      = "plan_skipped_by_triage"
	EventReworkFeedbackInjected   = "rework_feedback_injected"
	EventReworkFeedbackSkipped    = "rework_feedback_skipped"
	EventPromptLoaded             = "prompt_loaded"
	EventReviewerStarted          = "reviewer_started"
	EventReviewerCompleted        = "reviewer_completed"
	EventReviewerFailed           = "reviewer_failed"
	EventReviewerRetrying         = "reviewer_retrying"
	EventReviewerPartialFailure   = "reviewer_partial_failure"
	EventReviewerSkipped          = "reviewer_skipped"
	EventReviewMerged             = "review_merged"
	EventReworkRouted             = "rework_routed"
	EventReworkMaxCycles          = "rework_max_cycles"
	EventReviewerParseWarning     = "reviewer_parse_warning"
	EventFollowUpSkipped          = "follow_up_skipped"
	EventFollowUpFailed           = "follow_up_failed"
	EventCorrectiveSkipped        = "corrective_skipped"
	EventPatchExhausted           = "patch_exhausted"
	EventPatchEscalated           = "patch_escalated"
	EventPatchTooComplex          = "patch_too_complex"
	EventPatchEscalationSkipped   = "patch_escalation_skipped"
	EventPatchRegression          = "patch_regression"
	EventReworkMinorsDowngraded   = "rework_minors_downgraded"
	EventPipelineTimeout          = "pipeline_timeout"
	EventPhaseCostsReset          = "phase_costs_reset"
	EventBinaryVersionMismatch    = "binary_version_mismatch"
	EventAPISemaphoreWait         = "api_semaphore_wait"
	EventImplementNoChanges       = "implement_no_changes"
	EventTokenBudgetWarning       = "token_budget_warning"
	EventTokenBudgetCalibration   = "token_budget_calibration"
	EventNotifySuccess            = "notify_success"
	EventNotifyFailed             = "notify_failed"
	EventAutoMergeCompleted       = "auto_merge_completed"
	EventAutoMergeBlocked         = "auto_merge_blocked"
	EventAutoMergeDryRun          = "auto_merge_dry_run"
	EventRebaseConflict           = "rebase_conflict"
	EventContextFitted            = "context_fitted"
	EventConditionEvalFallback    = "condition_eval_fallback"
	EventPhaseConditionSkipped    = "phase_condition_skipped"
	EventTimeoutResolved          = "timeout_resolved"
)

// FormatEvent formats an event as a compact, human-readable line:
//
//	HH:MM:SS [phase] kind key=val key=val
//
// Data keys are sorted for stable output.
func FormatEvent(e Event) string {
	ts := e.Timestamp.Format("15:04:05")
	var b strings.Builder
	b.WriteString(ts)
	if e.Phase != "" {
		b.WriteString(" [")
		b.WriteString(e.Phase)
		b.WriteByte(']')
	}
	b.WriteByte(' ')
	b.WriteString(e.Kind)

	if len(e.Data) > 0 {
		keys := make([]string, 0, len(e.Data))
		for k := range e.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteByte(' ')
			b.WriteString(k)
			b.WriteByte('=')
			v := fmt.Sprintf("%v", e.Data[k])
			if strings.ContainsAny(v, " \t\n") {
				b.WriteString(fmt.Sprintf("%q", v))
			} else {
				b.WriteString(v)
			}
		}
	}
	return b.String()
}

// ReadEvents reads and parses all events from the events.jsonl file in dir.
// Returns an empty slice if the file does not exist.
// Malformed lines are silently skipped.
func ReadEvents(dir string) ([]Event, error) {
	path := filepath.Join(dir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("pipeline: read events %s: %w", path, err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	var events []Event
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	return events, nil
}

// logEvent appends an event to the events.jsonl file in dir.
func logEvent(dir string, event Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("pipeline: marshal event: %w", err)
	}
	data = append(data, '\n')

	path := filepath.Join(dir, "events.jsonl")
	fd, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("pipeline: open events log %s: %w", path, err)
	}
	defer fd.Close()

	if _, err := fd.Write(data); err != nil {
		return fmt.Errorf("pipeline: write event to %s: %w", path, err)
	}

	return nil
}
