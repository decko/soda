package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	EventEngineStarted   = "engine_started"
	EventEngineCompleted = "engine_completed"
	EventEngineFailed    = "engine_failed"
	EventPhaseStarted    = "phase_started"
	EventPhaseCompleted  = "phase_completed"
	EventPhaseFailed     = "phase_failed"
	EventPhaseRetrying   = "phase_retrying"
	EventPhaseSkipped    = "phase_skipped"
	EventOutputChunk     = "output_chunk"
	EventBudgetWarning   = "budget_warning"
	EventCheckpointPause = "checkpoint_pause"
	EventWorktreeCreated = "worktree_created"
	EventMonitorSkipped           = "monitor_skipped"
	EventReworkFeedbackInjected   = "rework_feedback_injected"
	EventReworkFeedbackSkipped    = "rework_feedback_skipped"
)

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
