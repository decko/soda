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

// Silence unused import errors — these are used by later tasks.
var (
	_ = json.Marshal
	_ = errors.Is
	_ = strings.Contains
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
