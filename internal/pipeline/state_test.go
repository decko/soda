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
		if ps.CumulativeCost != 0.50 {
			t.Errorf("cumulative cost before rerun = %v, want 0.50", ps.CumulativeCost)
		}

		state.MarkRunning("plan")

		ps = state.meta.Phases["plan"]
		if ps.Cost != 0 {
			t.Errorf("cost after rerun = %v, want 0", ps.Cost)
		}
		if ps.CumulativeCost != 0.50 {
			t.Errorf("cumulative cost after rerun = %v, want 0.50 (should NOT be reset)", ps.CumulativeCost)
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

	t.Run("no_duplicate_events", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-3")

		state.MarkRunning("plan")
		state.AccumulateCost("plan", 0.42)
		if err := state.MarkFailed("plan", fmt.Errorf("budget exceeded")); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}

		events, err := ReadEvents(filepath.Join(dir, "T-3"))
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}

		// State methods no longer emit events to events.jsonl; the engine
		// is the single source of event logging via emit(). Verify that
		// MarkRunning/MarkFailed do not write phase events.
		for _, ev := range events {
			if ev.Kind == EventPhaseStarted || ev.Kind == EventPhaseFailed || ev.Kind == EventPhaseCompleted {
				t.Errorf("unexpected phase event %q in events.jsonl from state method", ev.Kind)
			}
		}
	})

	t.Run("error_on_unknown_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-4")

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
		if !approxEqual(ps.CumulativeCost, 0.15) {
			t.Errorf("phase cumulative cost = %v, want 0.15", ps.CumulativeCost)
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

func TestAccumulateTokens(t *testing.T) {
	t.Run("accumulates_token_counts", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-TOK-1")

		state.MarkRunning("triage")
		state.AccumulateTokens("triage", 10000, 2000, 3000)
		state.AccumulateTokens("triage", 5000, 1000, 1500)

		ps := state.meta.Phases["triage"]
		if ps.TokensIn != 15000 {
			t.Errorf("TokensIn = %d, want 15000", ps.TokensIn)
		}
		if ps.TokensOut != 3000 {
			t.Errorf("TokensOut = %d, want 3000", ps.TokensOut)
		}
		if ps.CacheTokensIn != 4500 {
			t.Errorf("CacheTokensIn = %d, want 4500", ps.CacheTokensIn)
		}
	})

	t.Run("zeroed_on_rerun", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-TOK-2")

		state.MarkRunning("plan")
		state.AccumulateTokens("plan", 10000, 2000, 3000)
		state.MarkCompleted("plan")

		// Re-run should zero token counts.
		state.MarkRunning("plan")

		ps := state.meta.Phases["plan"]
		if ps.TokensIn != 0 {
			t.Errorf("TokensIn after rerun = %d, want 0", ps.TokensIn)
		}
		if ps.TokensOut != 0 {
			t.Errorf("TokensOut after rerun = %d, want 0", ps.TokensOut)
		}
		if ps.CacheTokensIn != 0 {
			t.Errorf("CacheTokensIn after rerun = %d, want 0", ps.CacheTokensIn)
		}
	})

	t.Run("error_on_unstarted_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-TOK-3")

		err := state.AccumulateTokens("nonexistent", 100, 200, 300)
		if err == nil {
			t.Fatal("expected error for unstarted phase")
		}
	})

	t.Run("persists_to_disk", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-TOK-4")

		state.MarkRunning("verify")
		state.AccumulateTokens("verify", 8000, 1500, 2000)
		state.MarkCompleted("verify")

		// Reload from disk and verify tokens were persisted.
		state2, err := LoadOrCreate(dir, "T-TOK-4")
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		ps := state2.Meta().Phases["verify"]
		if ps == nil {
			t.Fatal("verify phase not found after reload")
		}
		if ps.TokensIn != 8000 {
			t.Errorf("TokensIn after reload = %d, want 8000", ps.TokensIn)
		}
		if ps.TokensOut != 1500 {
			t.Errorf("TokensOut after reload = %d, want 1500", ps.TokensOut)
		}
		if ps.CacheTokensIn != 2000 {
			t.Errorf("CacheTokensIn after reload = %d, want 2000", ps.CacheTokensIn)
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

func TestReadArchivedResult(t *testing.T) {
	t.Run("reads_archived_generation", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-ARC")

		// First generation
		state.MarkRunning("review")
		state.WriteResult("review", json.RawMessage(`{"verdict":"rework","findings":[{"severity":"major","issue":"bad pattern"}]}`))
		state.MarkCompleted("review")

		// Re-run archives generation 1
		state.MarkRunning("review")
		state.WriteResult("review", json.RawMessage(`{"verdict":"pass","findings":[]}`))
		state.MarkCompleted("review")

		// Read archived generation 1
		archived, err := state.ReadArchivedResult("review", 1)
		if err != nil {
			t.Fatalf("ReadArchivedResult: %v", err)
		}
		if !strings.Contains(string(archived), "bad pattern") {
			t.Errorf("archived result should contain first-gen findings, got: %s", archived)
		}

		// Current result should be the pass
		current, err := state.ReadResult("review")
		if err != nil {
			t.Fatalf("ReadResult: %v", err)
		}
		if !strings.Contains(string(current), `"pass"`) {
			t.Errorf("current result should contain pass verdict, got: %s", current)
		}
	})

	t.Run("returns_error_for_missing_generation", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-ARC2")

		_, err := state.ReadArchivedResult("review", 99)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expected os.ErrNotExist, got %v", err)
		}
	})

	t.Run("reads_multiple_archived_generations", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-ARC3")

		// Generation 1
		state.MarkRunning("verify")
		state.WriteResult("verify", json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix A"]}`))
		state.MarkCompleted("verify")

		// Generation 2 (archives gen 1)
		state.MarkRunning("verify")
		state.WriteResult("verify", json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix B"]}`))
		state.MarkCompleted("verify")

		// Generation 3 (archives gen 2)
		state.MarkRunning("verify")
		state.WriteResult("verify", json.RawMessage(`{"verdict":"PASS"}`))
		state.MarkCompleted("verify")

		// Read archived generations
		gen1, err := state.ReadArchivedResult("verify", 1)
		if err != nil {
			t.Fatalf("ReadArchivedResult gen 1: %v", err)
		}
		if !strings.Contains(string(gen1), "fix A") {
			t.Errorf("gen 1 should contain 'fix A', got: %s", gen1)
		}

		gen2, err := state.ReadArchivedResult("verify", 2)
		if err != nil {
			t.Fatalf("ReadArchivedResult gen 2: %v", err)
		}
		if !strings.Contains(string(gen2), "fix B") {
			t.Errorf("gen 2 should contain 'fix B', got: %s", gen2)
		}
	})
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

	// State methods no longer write phase events to events.jsonl (the engine
	// handles event logging via emit to avoid duplicates). Verify the file
	// is either absent or empty.
	eventsData, _ := os.ReadFile(filepath.Join(dir, "PROJ-100", "events.jsonl"))
	if len(eventsData) > 0 {
		t.Errorf("events.jsonl should be empty when using raw state methods, got %d bytes", len(eventsData))
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

func TestResetPhaseCosts(t *testing.T) {
	t.Run("zeroes_cumulative_cost_for_all_phases", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-RST")

		// Simulate a prior run that accumulated costs.
		state.MarkRunning("triage")
		state.AccumulateCost("triage", 0.10)
		state.MarkCompleted("triage")

		state.MarkRunning("implement")
		state.AccumulateCost("implement", 2.00)
		state.MarkCompleted("implement")

		// Verify costs exist before reset.
		if !approxEqual(state.Meta().Phases["triage"].CumulativeCost, 0.10) {
			t.Errorf("triage CumulativeCost before reset = %v, want 0.10",
				state.Meta().Phases["triage"].CumulativeCost)
		}
		if !approxEqual(state.Meta().Phases["implement"].CumulativeCost, 2.00) {
			t.Errorf("implement CumulativeCost before reset = %v, want 2.00",
				state.Meta().Phases["implement"].CumulativeCost)
		}

		// Reset.
		if err := state.ResetPhaseCosts(); err != nil {
			t.Fatalf("ResetPhaseCosts: %v", err)
		}

		// CumulativeCost should be zero for all phases.
		for name, ps := range state.Meta().Phases {
			if ps.CumulativeCost != 0 {
				t.Errorf("phase %q CumulativeCost after reset = %v, want 0", name, ps.CumulativeCost)
			}
		}
	})

	t.Run("preserves_total_cost", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-RST2")

		state.MarkRunning("plan")
		state.AccumulateCost("plan", 1.50)
		state.MarkCompleted("plan")

		totalBefore := state.Meta().TotalCost
		if err := state.ResetPhaseCosts(); err != nil {
			t.Fatalf("ResetPhaseCosts: %v", err)
		}

		// TotalCost must NOT be reset — it tracks overall ticket spend.
		if !approxEqual(state.Meta().TotalCost, totalBefore) {
			t.Errorf("TotalCost after reset = %v, want %v (should be preserved)",
				state.Meta().TotalCost, totalBefore)
		}
	})

	t.Run("preserves_phase_status_and_generation", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-RST3")

		state.MarkRunning("verify")
		state.AccumulateCost("verify", 0.30)
		state.MarkCompleted("verify")

		if err := state.ResetPhaseCosts(); err != nil {
			t.Fatalf("ResetPhaseCosts: %v", err)
		}

		ps := state.Meta().Phases["verify"]
		if ps.Status != PhaseCompleted {
			t.Errorf("status after reset = %q, want %q", ps.Status, PhaseCompleted)
		}
		if ps.Generation != 1 {
			t.Errorf("generation after reset = %d, want 1", ps.Generation)
		}
	})

	t.Run("no_phases_is_noop", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-RST4")

		if err := state.ResetPhaseCosts(); err != nil {
			t.Fatalf("ResetPhaseCosts on empty phases: %v", err)
		}
	})

	t.Run("persists_to_disk", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-RST5")

		state.MarkRunning("triage")
		state.AccumulateCost("triage", 0.50)
		state.MarkCompleted("triage")

		if err := state.ResetPhaseCosts(); err != nil {
			t.Fatalf("ResetPhaseCosts: %v", err)
		}

		// Reload from disk and verify CumulativeCost was persisted as 0.
		state2, err := LoadOrCreate(dir, "T-RST5")
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		ps := state2.Meta().Phases["triage"]
		if ps == nil {
			t.Fatal("triage phase not found after reload")
		}
		if ps.CumulativeCost != 0 {
			t.Errorf("CumulativeCost after reload = %v, want 0", ps.CumulativeCost)
		}
	})
}
