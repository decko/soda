package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/pipeline"
)

// writeTestEvents writes events to events.jsonl in the given ticket directory.
func writeTestEvents(t *testing.T, ticketDir string, events []pipeline.Event) {
	t.Helper()
	var buf bytes.Buffer
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(ticketDir, "events.jsonl"), buf.Bytes(), 0644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
}

func TestNewLogCmd_Flags(t *testing.T) {
	cmd := newLogCmd()

	// Verify --follow/-f flag exists and defaults to false.
	followFlag := cmd.Flags().Lookup("follow")
	if followFlag == nil {
		t.Fatal("--follow flag not found")
	}
	if followFlag.DefValue != "false" {
		t.Errorf("--follow default = %q, want %q", followFlag.DefValue, "false")
	}
	if followFlag.Shorthand != "f" {
		t.Errorf("--follow shorthand = %q, want %q", followFlag.Shorthand, "f")
	}

	// Verify --since flag exists and defaults to empty.
	sinceFlag := cmd.Flags().Lookup("since")
	if sinceFlag == nil {
		t.Fatal("--since flag not found")
	}
	if sinceFlag.DefValue != "" {
		t.Errorf("--since default = %q, want empty", sinceFlag.DefValue)
	}

	// Verify --phase flag exists and defaults to empty.
	phaseFlag := cmd.Flags().Lookup("phase")
	if phaseFlag == nil {
		t.Fatal("--phase flag not found")
	}
	if phaseFlag.DefValue != "" {
		t.Errorf("--phase default = %q, want empty", phaseFlag.DefValue)
	}

	// Verify --n/-n flag exists and defaults to 0.
	nFlag := cmd.Flags().Lookup("n")
	if nFlag == nil {
		t.Fatal("--n flag not found")
	}
	if nFlag.DefValue != "0" {
		t.Errorf("--n default = %q, want %q", nFlag.DefValue, "0")
	}
	if nFlag.Shorthand != "n" {
		t.Errorf("--n shorthand = %q, want %q", nFlag.Shorthand, "n")
	}
}

func TestRunLog_PrintEvents(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-1")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []pipeline.Event{
		{Timestamp: ts, Kind: pipeline.EventEngineStarted},
		{Timestamp: ts.Add(time.Second), Phase: "triage", Kind: pipeline.EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Timestamp: ts.Add(2 * time.Second), Phase: "triage", Kind: pipeline.EventPhaseCompleted, Data: map[string]any{"cost": 0.12}},
	}
	writeTestEvents(t, ticketDir, events)

	var buf bytes.Buffer
	err := runLog(&buf, stateDir, "TEST-1", false, "", "", 0)
	if err != nil {
		t.Fatalf("runLog: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), output)
	}

	// Verify format: HH:MM:SS [phase] kind key=val
	if !strings.Contains(lines[0], "10:00:00 engine_started") {
		t.Errorf("line 0: unexpected format: %s", lines[0])
	}
	if !strings.Contains(lines[1], "[triage] phase_started") {
		t.Errorf("line 1: unexpected format: %s", lines[1])
	}
	if !strings.Contains(lines[2], "[triage] phase_completed") {
		t.Errorf("line 2: unexpected format: %s", lines[2])
	}
}

func TestRunLog_MissingFile(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "MISSING-1")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var buf bytes.Buffer
	err := runLog(&buf, stateDir, "MISSING-1", false, "", "", 0)
	if err == nil {
		t.Fatal("expected error for missing events file")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "no events file found") {
		t.Errorf("error should mention 'no events file found', got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "events.jsonl") {
		t.Errorf("error should mention events.jsonl path, got: %s", errMsg)
	}
	// Error should be single-line (no embedded newlines).
	if strings.Contains(errMsg, "\n") {
		t.Errorf("error message should be single-line, got: %s", errMsg)
	}
}

func TestRunLog_PhaseFilter(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-2")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []pipeline.Event{
		{Timestamp: ts, Kind: pipeline.EventEngineStarted},
		{Timestamp: ts.Add(time.Second), Phase: "triage", Kind: pipeline.EventPhaseStarted},
		{Timestamp: ts.Add(2 * time.Second), Phase: "triage", Kind: pipeline.EventPhaseCompleted},
		{Timestamp: ts.Add(3 * time.Second), Phase: "plan", Kind: pipeline.EventPhaseStarted},
		{Timestamp: ts.Add(4 * time.Second), Phase: "plan", Kind: pipeline.EventPhaseCompleted},
	}
	writeTestEvents(t, ticketDir, events)

	var buf bytes.Buffer
	err := runLog(&buf, stateDir, "TEST-2", false, "", "triage", 0)
	if err != nil {
		t.Fatalf("runLog: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 triage lines, got %d:\n%s", len(lines), output)
	}
	for _, line := range lines {
		if !strings.Contains(line, "[triage]") {
			t.Errorf("expected [triage] in every line, got: %s", line)
		}
	}
}

func TestRunLog_SinceFilter(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-3")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	now := time.Now()
	events := []pipeline.Event{
		{Timestamp: now.Add(-2 * time.Hour), Kind: pipeline.EventEngineStarted},
		{Timestamp: now.Add(-30 * time.Minute), Phase: "triage", Kind: pipeline.EventPhaseStarted},
		{Timestamp: now.Add(-5 * time.Minute), Phase: "triage", Kind: pipeline.EventPhaseCompleted},
		{Timestamp: now.Add(-1 * time.Minute), Phase: "plan", Kind: pipeline.EventPhaseStarted},
	}
	writeTestEvents(t, ticketDir, events)

	var buf bytes.Buffer
	err := runLog(&buf, stateDir, "TEST-3", false, "10m", "", 0)
	if err != nil {
		t.Fatalf("runLog: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 events within last 10m, got %d:\n%s", len(lines), output)
	}
}

func TestRunLog_LastN(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-4")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []pipeline.Event{
		{Timestamp: ts, Kind: pipeline.EventEngineStarted},
		{Timestamp: ts.Add(time.Second), Phase: "triage", Kind: pipeline.EventPhaseStarted},
		{Timestamp: ts.Add(2 * time.Second), Phase: "triage", Kind: pipeline.EventPhaseCompleted},
		{Timestamp: ts.Add(3 * time.Second), Phase: "plan", Kind: pipeline.EventPhaseStarted},
		{Timestamp: ts.Add(4 * time.Second), Phase: "plan", Kind: pipeline.EventPhaseCompleted},
	}
	writeTestEvents(t, ticketDir, events)

	var buf bytes.Buffer
	err := runLog(&buf, stateDir, "TEST-4", false, "", "", 2)
	if err != nil {
		t.Fatalf("runLog: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines with --n 2, got %d:\n%s", len(lines), output)
	}
	// Should be the last 2 events.
	if !strings.Contains(lines[0], "[plan] phase_started") {
		t.Errorf("line 0 should be plan phase_started, got: %s", lines[0])
	}
	if !strings.Contains(lines[1], "[plan] phase_completed") {
		t.Errorf("line 1 should be plan phase_completed, got: %s", lines[1])
	}
}

func TestFollowEvents_TerminalEvent(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-5")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []pipeline.Event{
		{Timestamp: ts, Kind: pipeline.EventEngineStarted},
		{Timestamp: ts.Add(time.Second), Phase: "triage", Kind: pipeline.EventPhaseStarted},
		{Timestamp: ts.Add(2 * time.Second), Kind: pipeline.EventEngineCompleted},
	}
	writeTestEvents(t, ticketDir, events)

	var buf bytes.Buffer
	eventsPath := filepath.Join(ticketDir, "events.jsonl")

	// followEvents should return immediately because events already contain
	// a terminal event (engine_completed).
	err := followEvents(&buf, eventsPath, time.Time{}, "")
	if err != nil {
		t.Fatalf("followEvents: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), output)
	}
	if !strings.Contains(lines[2], "engine_completed") {
		t.Errorf("last line should contain engine_completed, got: %s", lines[2])
	}
}

func TestFollowEvents_TerminalFailed(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-6")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []pipeline.Event{
		{Timestamp: ts, Kind: pipeline.EventEngineStarted},
		{Timestamp: ts.Add(time.Second), Kind: pipeline.EventEngineFailed, Data: map[string]any{"error": "budget exceeded"}},
	}
	writeTestEvents(t, ticketDir, events)

	var buf bytes.Buffer
	eventsPath := filepath.Join(ticketDir, "events.jsonl")

	err := followEvents(&buf, eventsPath, time.Time{}, "")
	if err != nil {
		t.Fatalf("followEvents: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "engine_failed") {
		t.Errorf("output should contain engine_failed, got: %s", output)
	}
}

func TestFollowEvents_PhaseFilterWithTerminalEvent(t *testing.T) {
	// Regression test: --phase filter must not prevent follow mode from
	// detecting terminal events (engine_completed has Phase="").
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-7")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []pipeline.Event{
		{Timestamp: ts, Kind: pipeline.EventEngineStarted},
		{Timestamp: ts.Add(time.Second), Phase: "triage", Kind: pipeline.EventPhaseStarted},
		{Timestamp: ts.Add(2 * time.Second), Phase: "triage", Kind: pipeline.EventPhaseCompleted},
		{Timestamp: ts.Add(3 * time.Second), Kind: pipeline.EventEngineCompleted},
	}
	writeTestEvents(t, ticketDir, events)

	var buf bytes.Buffer
	eventsPath := filepath.Join(ticketDir, "events.jsonl")

	// With --phase triage, engine_completed (Phase="") is filtered from
	// output but must still cause follow mode to exit.
	err := followEvents(&buf, eventsPath, time.Time{}, "triage")
	if err != nil {
		t.Fatalf("followEvents: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	// Only triage events should be printed (engine_completed is filtered).
	if len(lines) != 2 {
		t.Fatalf("expected 2 triage lines, got %d:\n%s", len(lines), output)
	}
	for _, line := range lines {
		if !strings.Contains(line, "[triage]") {
			t.Errorf("expected [triage] in every line, got: %s", line)
		}
	}
}

func TestFollowEvents_PipelineTimeoutTerminal(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-8")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []pipeline.Event{
		{Timestamp: ts, Kind: pipeline.EventEngineStarted},
		{Timestamp: ts.Add(time.Second), Kind: pipeline.EventPipelineTimeout, Data: map[string]any{"limit": "30m"}},
	}
	writeTestEvents(t, ticketDir, events)

	var buf bytes.Buffer
	eventsPath := filepath.Join(ticketDir, "events.jsonl")

	err := followEvents(&buf, eventsPath, time.Time{}, "")
	if err != nil {
		t.Fatalf("followEvents: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "pipeline_timeout") {
		t.Errorf("output should contain pipeline_timeout, got: %s", output)
	}
}

func TestIsTerminalEvent(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{pipeline.EventEngineCompleted, true},
		{pipeline.EventEngineFailed, true},
		{pipeline.EventPipelineTimeout, true},
		{pipeline.EventEngineStarted, false},
		{pipeline.EventPhaseStarted, false},
		{pipeline.EventPhaseCompleted, false},
		{pipeline.EventPhaseFailed, false},
	}
	for _, tc := range tests {
		ev := pipeline.Event{Kind: tc.kind}
		got := isTerminalEvent(ev)
		if got != tc.want {
			t.Errorf("isTerminalEvent(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestFilterEvents(t *testing.T) {
	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []pipeline.Event{
		{Timestamp: ts, Kind: pipeline.EventEngineStarted},
		{Timestamp: ts.Add(time.Second), Phase: "triage", Kind: pipeline.EventPhaseStarted},
		{Timestamp: ts.Add(2 * time.Second), Phase: "plan", Kind: pipeline.EventPhaseStarted},
	}

	t.Run("no filters", func(t *testing.T) {
		got := filterEvents(events, time.Time{}, "")
		if len(got) != 3 {
			t.Errorf("expected 3 events, got %d", len(got))
		}
	})

	t.Run("phase filter", func(t *testing.T) {
		got := filterEvents(events, time.Time{}, "triage")
		if len(got) != 1 {
			t.Errorf("expected 1 event, got %d", len(got))
		}
	})

	t.Run("since filter", func(t *testing.T) {
		got := filterEvents(events, ts.Add(500*time.Millisecond), "")
		if len(got) != 2 {
			t.Errorf("expected 2 events after cutoff, got %d", len(got))
		}
	})

	t.Run("phase and since combined", func(t *testing.T) {
		got := filterEvents(events, ts.Add(500*time.Millisecond), "plan")
		if len(got) != 1 {
			t.Errorf("expected 1 event, got %d", len(got))
		}
	})
}
