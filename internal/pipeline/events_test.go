package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogEvent(t *testing.T) {
	t.Run("appends_event_to_jsonl", func(t *testing.T) {
		dir := t.TempDir()
		ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)

		event := Event{
			Timestamp: ts,
			Phase:     "triage",
			Kind:      "phase_started",
			Data:      map[string]any{"generation": 1},
		}

		if err := logEvent(dir, event); err != nil {
			t.Fatalf("logEvent: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 1 {
			t.Fatalf("expected 1 line, got %d", len(lines))
		}

		var parsed Event
		if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if parsed.Phase != "triage" {
			t.Errorf("Phase = %q, want %q", parsed.Phase, "triage")
		}
		if parsed.Kind != "phase_started" {
			t.Errorf("Kind = %q, want %q", parsed.Kind, "phase_started")
		}
	})

	t.Run("appends_multiple_events", func(t *testing.T) {
		dir := t.TempDir()

		logEvent(dir, Event{Phase: "triage", Kind: "phase_started"})
		logEvent(dir, Event{Phase: "triage", Kind: "phase_completed"})
		logEvent(dir, Event{Phase: "plan", Kind: "phase_started"})

		data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 3 {
			t.Fatalf("expected 3 lines, got %d", len(lines))
		}
	})

	t.Run("sets_timestamp_if_zero", func(t *testing.T) {
		dir := t.TempDir()
		before := time.Now()

		logEvent(dir, Event{Phase: "triage", Kind: "phase_started"})

		data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		var parsed Event
		json.Unmarshal([]byte(strings.TrimSpace(string(data))), &parsed)

		if parsed.Timestamp.Before(before) {
			t.Errorf("Timestamp %v should be >= %v", parsed.Timestamp, before)
		}
	})

	t.Run("omits_empty_data", func(t *testing.T) {
		dir := t.TempDir()

		logEvent(dir, Event{Phase: "triage", Kind: "phase_started"})

		data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		line := strings.TrimSpace(string(data))
		if strings.Contains(line, `"data"`) {
			t.Errorf("empty data should be omitted, got: %s", line)
		}
	})
}
