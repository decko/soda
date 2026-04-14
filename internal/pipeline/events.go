package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	EventReworkFeedbackInjected   = "rework_feedback_injected"
	EventReworkFeedbackSkipped    = "rework_feedback_skipped"
	EventPromptLoaded             = "prompt_loaded"
	EventReviewerStarted          = "reviewer_started"
	EventReviewerCompleted        = "reviewer_completed"
	EventReviewerFailed           = "reviewer_failed"
	EventReviewMerged             = "review_merged"
	EventReviewReworkRouted       = "review_rework_routed"
	EventReviewReworkMaxCycles    = "review_rework_max_cycles"
	EventReviewerParseWarning     = "reviewer_parse_warning"
)

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
