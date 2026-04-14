package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteMonitorState(t *testing.T) {
	stateDir := t.TempDir()
	state, err := LoadOrCreate(stateDir, "MON-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	now := time.Date(2026, 4, 13, 15, 0, 0, 0, time.UTC)
	ms := &MonitorState{
		PRURL:             "https://github.com/decko/soda/pull/49",
		PollCount:         12,
		ResponseRounds:    2,
		MaxResponseRounds: 3,
		LastCommentID:     "IC_abc123",
		LastCIStatus:      "success",
		LastPolledAt:      now,
		Status:            MonitorPolling,
	}

	if err := state.WriteMonitorState(ms); err != nil {
		t.Fatalf("WriteMonitorState: %v", err)
	}

	// Verify file exists and contains valid JSON.
	data, err := os.ReadFile(filepath.Join(stateDir, "MON-1", "monitor.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var parsed MonitorState
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if parsed.PRURL != ms.PRURL {
		t.Errorf("PRURL = %q, want %q", parsed.PRURL, ms.PRURL)
	}
	if parsed.PollCount != 12 {
		t.Errorf("PollCount = %d, want 12", parsed.PollCount)
	}
	if parsed.ResponseRounds != 2 {
		t.Errorf("ResponseRounds = %d, want 2", parsed.ResponseRounds)
	}
	if parsed.MaxResponseRounds != 3 {
		t.Errorf("MaxResponseRounds = %d, want 3", parsed.MaxResponseRounds)
	}
	if parsed.LastCommentID != "IC_abc123" {
		t.Errorf("LastCommentID = %q, want %q", parsed.LastCommentID, "IC_abc123")
	}
	if parsed.LastCIStatus != "success" {
		t.Errorf("LastCIStatus = %q, want %q", parsed.LastCIStatus, "success")
	}
	if parsed.Status != MonitorPolling {
		t.Errorf("Status = %q, want %q", parsed.Status, MonitorPolling)
	}
}

func TestReadMonitorState(t *testing.T) {
	t.Run("reads_existing_state", func(t *testing.T) {
		stateDir := t.TempDir()
		state, err := LoadOrCreate(stateDir, "MON-2")
		if err != nil {
			t.Fatalf("LoadOrCreate: %v", err)
		}

		now := time.Date(2026, 4, 13, 15, 0, 0, 0, time.UTC)
		original := &MonitorState{
			PRURL:             "https://github.com/decko/soda/pull/50",
			PollCount:         5,
			ResponseRounds:    1,
			MaxResponseRounds: 3,
			LastCommentID:     "IC_def456",
			LastCIStatus:      "failure",
			LastPolledAt:      now,
			Status:            MonitorPolling,
		}

		if err := state.WriteMonitorState(original); err != nil {
			t.Fatalf("WriteMonitorState: %v", err)
		}

		read, err := state.ReadMonitorState()
		if err != nil {
			t.Fatalf("ReadMonitorState: %v", err)
		}

		if read.PRURL != original.PRURL {
			t.Errorf("PRURL = %q, want %q", read.PRURL, original.PRURL)
		}
		if read.PollCount != original.PollCount {
			t.Errorf("PollCount = %d, want %d", read.PollCount, original.PollCount)
		}
		if read.ResponseRounds != original.ResponseRounds {
			t.Errorf("ResponseRounds = %d, want %d", read.ResponseRounds, original.ResponseRounds)
		}
		if read.Status != original.Status {
			t.Errorf("Status = %q, want %q", read.Status, original.Status)
		}
	})

	t.Run("returns_error_for_missing_file", func(t *testing.T) {
		stateDir := t.TempDir()
		state, err := LoadOrCreate(stateDir, "MON-3")
		if err != nil {
			t.Fatalf("LoadOrCreate: %v", err)
		}

		_, err = state.ReadMonitorState()
		if err == nil {
			t.Fatal("expected error for missing monitor.json")
		}
		if !os.IsNotExist(err) {
			t.Errorf("expected os.ErrNotExist, got: %v", err)
		}
	})
}

func TestMonitorState_Roundtrip(t *testing.T) {
	stateDir := t.TempDir()
	state, err := LoadOrCreate(stateDir, "MON-4")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	now := time.Date(2026, 4, 13, 16, 30, 0, 0, time.UTC)
	original := &MonitorState{
		PRURL:             "https://github.com/decko/soda/pull/99",
		PollCount:         0,
		ResponseRounds:    0,
		MaxResponseRounds: 5,
		LastPolledAt:      now,
		Status:            MonitorPolling,
	}

	if err := state.WriteMonitorState(original); err != nil {
		t.Fatalf("WriteMonitorState: %v", err)
	}

	// Update state
	original.PollCount = 3
	original.ResponseRounds = 1
	original.LastCommentID = "IC_newone"
	original.LastCIStatus = "success"
	original.Status = MonitorCompleted

	if err := state.WriteMonitorState(original); err != nil {
		t.Fatalf("WriteMonitorState (update): %v", err)
	}

	read, err := state.ReadMonitorState()
	if err != nil {
		t.Fatalf("ReadMonitorState: %v", err)
	}

	if read.PollCount != 3 {
		t.Errorf("PollCount = %d, want 3", read.PollCount)
	}
	if read.ResponseRounds != 1 {
		t.Errorf("ResponseRounds = %d, want 1", read.ResponseRounds)
	}
	if read.LastCommentID != "IC_newone" {
		t.Errorf("LastCommentID = %q, want %q", read.LastCommentID, "IC_newone")
	}
	if read.Status != MonitorCompleted {
		t.Errorf("Status = %q, want %q", read.Status, MonitorCompleted)
	}
}
