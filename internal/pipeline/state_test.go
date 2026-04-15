package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestLoadOrCreate(t *testing.T) {
	t.Run("creates_new_state", func(t *testing.T) {
		dir := t.TempDir()

		state, err := LoadOrCreate(dir, "PROJ-123")
		if err != nil {
			t.Fatalf("LoadOrCreate: %v", err)
		}

		if state.ticket != "PROJ-123" {
			t.Errorf("ticket = %q, want %q", state.ticket, "PROJ-123")
		}
		if state.meta.Ticket != "PROJ-123" {
			t.Errorf("meta.Ticket = %q, want %q", state.meta.Ticket, "PROJ-123")
		}
		if state.meta.StartedAt.IsZero() {
			t.Error("StartedAt should be set")
		}
		if state.meta.Phases == nil {
			t.Error("Phases should be initialized")
		}

		// Directory structure should exist
		stateDir := filepath.Join(dir, "PROJ-123")
		if _, err := os.Stat(stateDir); err != nil {
			t.Errorf("state dir should exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "logs")); err != nil {
			t.Errorf("logs dir should exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "meta.json")); err != nil {
			t.Errorf("meta.json should exist: %v", err)
		}
	})

	t.Run("resumes_existing_state", func(t *testing.T) {
		dir := t.TempDir()

		// Create initial state
		state1, err := LoadOrCreate(dir, "PROJ-456")
		if err != nil {
			t.Fatal(err)
		}
		originalStartedAt := state1.meta.StartedAt

		// Load again — should resume, not overwrite
		state2, err := LoadOrCreate(dir, "PROJ-456")
		if err != nil {
			t.Fatalf("resume LoadOrCreate: %v", err)
		}
		if !state2.meta.StartedAt.Equal(originalStartedAt) {
			t.Errorf("StartedAt changed on resume: got %v, want %v",
				state2.meta.StartedAt, originalStartedAt)
		}
	})

	t.Run("lock_not_acquired", func(t *testing.T) {
		dir := t.TempDir()

		state, err := LoadOrCreate(dir, "PROJ-789")
		if err != nil {
			t.Fatal(err)
		}
		if state.lockFd != nil {
			t.Error("lockFd should be nil after LoadOrCreate")
		}
	})
}

func TestValidateTicketKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid_key", "PROJ-123", false},
		{"valid_with_underscores", "MY_PROJECT-42", false},
		{"empty_string", "", true},
		{"contains_slash", "PROJ/123", true},
		{"contains_backslash", "PROJ\\123", true},
		{"contains_dotdot", "PROJ..123", true},
		{"path_traversal", "../etc/passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTicketKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTicketKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestLoadOrCreate_InvalidKey(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadOrCreate(dir, "../evil")
	if err == nil {
		t.Fatal("expected error for invalid ticket key")
	}
}

func TestMarkRunning(t *testing.T) {
	t.Run("first_run_creates_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-1")

		if err := state.MarkRunning("triage"); err != nil {
			t.Fatalf("MarkRunning: %v", err)
		}

		ps := state.meta.Phases["triage"]
		if ps == nil {
			t.Fatal("triage phase should exist")
		}
		if ps.Status != PhaseRunning {
			t.Errorf("status = %q, want %q", ps.Status, PhaseRunning)
		}
		if ps.Generation != 1 {
			t.Errorf("generation = %d, want 1", ps.Generation)
		}
		if ps.Cost != 0 {
			t.Errorf("cost = %v, want 0", ps.Cost)
		}
	})

	t.Run("rerun_archives_and_increments_generation", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-2")

		// First run
		state.MarkRunning("verify")
		state.WriteResult("verify", []byte(`{"verdict":"pass"}`))
		state.WriteArtifact("verify", []byte("# Verify handoff"))
		state.MarkCompleted("verify")

		// Re-run
		if err := state.MarkRunning("verify"); err != nil {
			t.Fatalf("re-run MarkRunning: %v", err)
		}

		ps := state.meta.Phases["verify"]
		if ps.Generation != 2 {
			t.Errorf("generation = %d, want 2", ps.Generation)
		}
		if ps.Status != PhaseRunning {
			t.Errorf("status = %q, want %q", ps.Status, PhaseRunning)
		}

		// Archived files should exist
		stateDir := filepath.Join(dir, "T-2")
		archived, err := os.ReadFile(filepath.Join(stateDir, "verify.json.1"))
		if err != nil {
			t.Fatalf("archived .json: %v", err)
		}
		if string(archived) != `{"verdict":"pass"}` {
			t.Errorf("archived content = %q", archived)
		}

		archivedMd, err := os.ReadFile(filepath.Join(stateDir, "verify.md.1"))
		if err != nil {
			t.Fatalf("archived .md: %v", err)
		}
		if string(archivedMd) != "# Verify handoff" {
			t.Errorf("archived md content = %q", archivedMd)
		}
	})

	t.Run("rerun_zeroes_cost_and_error", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-3")

		state.MarkRunning("plan")
		state.AccumulateCost("plan", 0.50)
		state.MarkFailed("plan", fmt.Errorf("something broke"))

		// Verify state before re-run
		ps := state.meta.Phases["plan"]
		if ps.Cost != 0.50 {
			t.Errorf("cost before rerun = %v", ps.Cost)
		}

		state.MarkRunning("plan")

		ps = state.meta.Phases["plan"]
		if ps.Cost != 0 {
			t.Errorf("cost after rerun = %v, want 0", ps.Cost)
		}
		if ps.Error != "" {
			t.Errorf("error after rerun = %q, want empty", ps.Error)
		}
		if ps.DurationMs != 0 {
			t.Errorf("duration after rerun = %d, want 0", ps.DurationMs)
		}
	})
}

func TestMarkCompleted(t *testing.T) {
	t.Run("sets_completed_status", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-1")

		state.MarkRunning("triage")
		if err := state.MarkCompleted("triage"); err != nil {
			t.Fatalf("MarkCompleted: %v", err)
		}

		ps := state.meta.Phases["triage"]
		if ps.Status != PhaseCompleted {
			t.Errorf("status = %q, want %q", ps.Status, PhaseCompleted)
		}
		if ps.DurationMs < 0 {
			t.Errorf("DurationMs = %d, should be >= 0", ps.DurationMs)
		}
	})

	t.Run("error_on_unknown_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-2")

		err := state.MarkCompleted("nonexistent")
		if err == nil {
			t.Fatal("expected error for unknown phase")
		}
	})
}

func TestMarkFailed(t *testing.T) {
	t.Run("sets_failed_status_with_error", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-1")

		state.MarkRunning("implement")
		if err := state.MarkFailed("implement", fmt.Errorf("tests failed")); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}

		ps := state.meta.Phases["implement"]
		if ps.Status != PhaseFailed {
			t.Errorf("status = %q, want %q", ps.Status, PhaseFailed)
		}
		if ps.Error != "tests failed" {
			t.Errorf("error = %q, want %q", ps.Error, "tests failed")
		}
	})

	t.Run("error_on_unknown_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-2")

		err := state.MarkFailed("nonexistent", fmt.Errorf("err"))
		if err == nil {
			t.Fatal("expected error for unknown phase")
		}
	})
}

func TestIsCompleted(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	if state.IsCompleted("triage") {
		t.Error("should be false for unknown phase")
	}

	state.MarkRunning("triage")
	if state.IsCompleted("triage") {
		t.Error("should be false for running phase")
	}

	state.MarkCompleted("triage")
	if !state.IsCompleted("triage") {
		t.Error("should be true for completed phase")
	}
}

func TestAccumulateCost(t *testing.T) {
	t.Run("accumulates_phase_and_total", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-1")

		state.MarkRunning("triage")
		state.AccumulateCost("triage", 0.10)
		state.AccumulateCost("triage", 0.05)

		ps := state.meta.Phases["triage"]
		if !approxEqual(ps.Cost, 0.15) {
			t.Errorf("phase cost = %v, want 0.15", ps.Cost)
		}
		if !approxEqual(state.meta.TotalCost, 0.15) {
			t.Errorf("total cost = %v, want 0.15", state.meta.TotalCost)
		}

		state.MarkCompleted("triage")
		state.MarkRunning("plan")
		state.AccumulateCost("plan", 0.20)

		if !approxEqual(state.meta.TotalCost, 0.35) {
			t.Errorf("total cost = %v, want 0.35", state.meta.TotalCost)
		}
	})

	t.Run("error_on_unstarted_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-2")

		err := state.AccumulateCost("nonexistent", 1.0)
		if err == nil {
			t.Fatal("expected error for unstarted phase")
		}
	})
}

func TestWriteReadArtifact(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	content := []byte("# Triage handoff\n\nThis ticket is about X.")
	if err := state.WriteArtifact("triage", content); err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}

	got, err := state.ReadArtifact("triage")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestReadArtifact_NotExist(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	_, err := state.ReadArtifact("nonexistent")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestWriteReadResult(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	result := json.RawMessage(`{"verdict":"pass","confidence":0.95}`)
	if err := state.WriteResult("verify", result); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}

	got, err := state.ReadResult("verify")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	if string(got) != string(result) {
		t.Errorf("result = %q, want %q", got, result)
	}
}

func TestReadResult_NotExist(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	_, err := state.ReadResult("nonexistent")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestWriteLog(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	prompt := []byte("You are a triage agent...")
	if err := state.WriteLog("triage", "prompt", prompt); err != nil {
		t.Fatalf("WriteLog: %v", err)
	}

	logPath := filepath.Join(dir, "T-1", "logs", "triage_prompt.md")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(prompt) {
		t.Errorf("log content = %q, want %q", got, prompt)
	}
}

func TestStateAcquireReleaseLock(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	if err := state.AcquireLock(); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	// Second state for same ticket should fail to lock
	state2, _ := LoadOrCreate(dir, "T-1")
	err := state2.AcquireLock()
	if err == nil {
		t.Fatal("expected error for contention")
	}

	state.ReleaseLock()

	// Now second state should succeed
	if err := state2.AcquireLock(); err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	state2.ReleaseLock()
}

func TestStateLogEvent(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	if err := state.LogEvent(Event{Phase: "triage", Kind: "custom_event"}); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "T-1", "events.jsonl"))
	if len(data) == 0 {
		t.Error("events.jsonl should not be empty")
	}
}

func TestDir(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	want := filepath.Join(dir, "T-1")
	if state.Dir() != want {
		t.Errorf("Dir() = %q, want %q", state.Dir(), want)
	}
}

func TestMeta(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	meta := state.Meta()
	if meta == nil {
		t.Fatal("Meta() should not be nil")
	}
	if meta.Ticket != "T-1" {
		t.Errorf("Ticket = %q, want %q", meta.Ticket, "T-1")
	}
}

func TestFullLifecycle(t *testing.T) {
	dir := t.TempDir()

	// === First run ===
	state, err := LoadOrCreate(dir, "PROJ-100")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	if err := state.AcquireLock(); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer state.ReleaseLock()

	// Run triage
	state.MarkRunning("triage")
	state.AccumulateCost("triage", 0.08)
	state.WriteResult("triage", json.RawMessage(`{"complexity":"medium"}`))
	state.WriteArtifact("triage", []byte("Triage: medium complexity"))
	state.WriteLog("triage", "prompt", []byte("system prompt"))
	state.WriteLog("triage", "response", []byte("raw response"))
	state.MarkCompleted("triage")

	// Run plan
	state.MarkRunning("plan")
	state.AccumulateCost("plan", 0.20)
	state.WriteResult("plan", json.RawMessage(`{"tasks":["task1"]}`))
	state.WriteArtifact("plan", []byte("Plan: one task"))
	state.MarkCompleted("plan")

	// Run implement — fails
	state.MarkRunning("implement")
	state.AccumulateCost("implement", 1.50)
	state.MarkFailed("implement", fmt.Errorf("test suite failed"))

	// Verify accumulated cost
	meta := state.Meta()
	if !approxEqual(meta.TotalCost, 1.78) {
		t.Errorf("TotalCost = %v, want 1.78", meta.TotalCost)
	}

	state.ReleaseLock()

	// === Resume after crash ===
	state2, err := LoadOrCreate(dir, "PROJ-100")
	if err != nil {
		t.Fatalf("resume LoadOrCreate: %v", err)
	}

	if err := state2.AcquireLock(); err != nil {
		t.Fatalf("resume AcquireLock: %v", err)
	}
	defer state2.ReleaseLock()

	// Completed phases should be skippable
	if !state2.IsCompleted("triage") {
		t.Error("triage should be completed on resume")
	}
	if !state2.IsCompleted("plan") {
		t.Error("plan should be completed on resume")
	}
	if state2.IsCompleted("implement") {
		t.Error("implement should NOT be completed (it failed)")
	}

	// Artifacts should be readable
	triageArtifact, _ := state2.ReadArtifact("triage")
	if string(triageArtifact) != "Triage: medium complexity" {
		t.Errorf("triage artifact = %q", triageArtifact)
	}
	triageResult, _ := state2.ReadResult("triage")
	if string(triageResult) != `{"complexity":"medium"}` {
		t.Errorf("triage result = %q", triageResult)
	}

	// Re-run implement (generation 2)
	state2.MarkRunning("implement")

	ps := state2.Meta().Phases["implement"]
	if ps.Generation != 2 {
		t.Errorf("implement generation = %d, want 2", ps.Generation)
	}
	if ps.Error != "" {
		t.Errorf("implement error should be cleared, got %q", ps.Error)
	}

	// Budget preserved from first run's triage + plan
	if !approxEqual(state2.Meta().TotalCost, 1.78) {
		t.Errorf("resumed TotalCost = %v, want 1.78", state2.Meta().TotalCost)
	}

	state2.AccumulateCost("implement", 2.00)
	state2.WriteResult("implement", json.RawMessage(`{"commits":1}`))
	state2.MarkCompleted("implement")

	if !approxEqual(state2.Meta().TotalCost, 3.78) {
		t.Errorf("final TotalCost = %v, want 3.78", state2.Meta().TotalCost)
	}

	// Verify events.jsonl has entries
	eventsData, _ := os.ReadFile(filepath.Join(dir, "PROJ-100", "events.jsonl"))
	eventLines := strings.Split(strings.TrimSpace(string(eventsData)), "\n")
	if len(eventLines) < 8 {
		t.Errorf("expected >= 8 events, got %d", len(eventLines))
	}

	// Verify log files exist
	logDir := filepath.Join(dir, "PROJ-100", "logs")
	if _, err := os.Stat(filepath.Join(logDir, "triage_prompt.md")); err != nil {
		t.Errorf("triage prompt log missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "triage_response.md")); err != nil {
		t.Errorf("triage response log missing: %v", err)
	}
}

func TestCrashSafety_OrphanedTmp(t *testing.T) {
	dir := t.TempDir()

	// Create state normally
	state, _ := LoadOrCreate(dir, "T-1")
	state.MarkRunning("triage")
	state.MarkCompleted("triage")

	// Simulate crash: leave orphaned meta.json.tmp with corrupt data
	metaTmp := filepath.Join(dir, "T-1", "meta.json.tmp")
	if err := os.WriteFile(metaTmp, []byte("corrupt"), 0644); err != nil {
		t.Fatalf("WriteFile meta.json.tmp: %v", err)
	}

	// Resume should read the real meta.json, ignoring the orphaned .tmp
	state2, err := LoadOrCreate(dir, "T-1")
	if err != nil {
		t.Fatalf("resume after crash: %v", err)
	}
	if !state2.IsCompleted("triage") {
		t.Error("triage should still be completed after crash resume")
	}
}
