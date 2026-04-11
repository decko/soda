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
