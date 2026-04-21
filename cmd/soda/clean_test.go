package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/decko/soda/internal/pipeline"
)

func writeCleanMeta(t *testing.T, dir string, meta *pipeline.PipelineMeta) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		name string
		meta *pipeline.PipelineMeta
		want bool
	}{
		{
			name: "no phases → terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{}},
			want: true,
		},
		{
			name: "all completed → terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhaseCompleted},
			}},
			want: true,
		},
		{
			name: "has failed → terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhaseFailed},
			}},
			want: true,
		},
		{
			name: "has running → not terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhaseRunning},
			}},
			want: false,
		},
		{
			name: "has retrying → not terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhaseRetrying},
			}},
			want: false,
		},
		{
			name: "only pending → not terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage": {Status: pipeline.PhasePending},
			}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isTerminal(tc.meta)
			if got != tc.want {
				t.Errorf("isTerminal() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCleanTicket_TerminalState_Purge(t *testing.T) {
	stateDir := t.TempDir()

	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, false, true)
	if err != nil {
		t.Fatalf("cleanTicket: %v", err)
	}

	// Verify state directory was removed with --purge.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); !os.IsNotExist(statErr) {
		t.Error("expected state dir to be removed")
	}
}

func TestCleanTicket_SkipsNonTerminal(t *testing.T) {
	stateDir := t.TempDir()

	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhasePending},
		},
	})

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, false, false)
	if err != errSkipped {
		t.Fatalf("expected errSkipped, got %v", err)
	}

	// Verify state directory still exists.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); statErr != nil {
		t.Errorf("expected state dir to still exist: %v", statErr)
	}
}

func TestCleanTicket_ForceBypassesTerminalCheck(t *testing.T) {
	stateDir := t.TempDir()

	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhasePending},
		},
	})

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, true, true)
	if err != nil {
		t.Fatalf("cleanTicket with force+purge: %v", err)
	}

	// Verify state directory was removed with --force --purge despite non-terminal state.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); !os.IsNotExist(statErr) {
		t.Error("expected state dir to be removed with --force --purge")
	}
}

func TestCleanTicket_DryRun(t *testing.T) {
	stateDir := t.TempDir()

	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", true, false, true)
	if err != nil {
		t.Fatalf("cleanTicket dry-run: %v", err)
	}

	// Verify state directory still exists after dry-run.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); statErr != nil {
		t.Errorf("expected state dir to still exist after dry-run: %v", statErr)
	}
}

func TestCleanAll_CleansTerminal_Purge(t *testing.T) {
	stateDir := t.TempDir()

	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})
	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-2"), &pipeline.PipelineMeta{
		Ticket: "TICKET-2",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhasePending},
		},
	})

	err := cleanAll(context.Background(), stateDir, false, false, true)
	if err != nil {
		t.Fatalf("cleanAll: %v", err)
	}

	// TICKET-1 (terminal) should be removed with --purge.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); !os.IsNotExist(statErr) {
		t.Error("expected TICKET-1 to be removed")
	}
	// TICKET-2 (non-terminal) should still exist.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-2")); statErr != nil {
		t.Errorf("expected TICKET-2 to still exist: %v", statErr)
	}
}

func TestCleanAll_ForceRemovesAll_Purge(t *testing.T) {
	stateDir := t.TempDir()

	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})
	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-2"), &pipeline.PipelineMeta{
		Ticket: "TICKET-2",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhasePending},
		},
	})

	err := cleanAll(context.Background(), stateDir, false, true, true)
	if err != nil {
		t.Fatalf("cleanAll with force+purge: %v", err)
	}

	// Both should be removed when force+purge is true.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); !os.IsNotExist(statErr) {
		t.Error("expected TICKET-1 to be removed")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-2")); !os.IsNotExist(statErr) {
		t.Error("expected TICKET-2 to be removed with --force --purge")
	}
}

func TestCleanAll_NonexistentDir(t *testing.T) {
	err := cleanAll(context.Background(), "/tmp/nonexistent-soda-clean-test", false, false, false)
	if err != nil {
		t.Fatalf("cleanAll should not error for nonexistent dir: %v", err)
	}
}

func TestNewCleanCmd_Flags(t *testing.T) {
	cmd := newCleanCmd()

	allFlag := cmd.Flags().Lookup("all")
	if allFlag == nil {
		t.Fatal("--all flag not found")
	}

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	if dryRunFlag == nil {
		t.Fatal("--dry-run flag not found")
	}

	forceFlag := cmd.Flags().Lookup("force")
	if forceFlag == nil {
		t.Fatal("--force flag not found")
	}
	if forceFlag.DefValue != "false" {
		t.Errorf("--force default = %q, want %q", forceFlag.DefValue, "false")
	}

	purgeFlag := cmd.Flags().Lookup("purge")
	if purgeFlag == nil {
		t.Fatal("--purge flag not found")
	}
	if purgeFlag.DefValue != "false" {
		t.Errorf("--purge default = %q, want %q", purgeFlag.DefValue, "false")
	}
}

// TestCleanTicket_ForceStillChecksFlockWhenLockHeld verifies that --force does
// NOT bypass the flock safety check. If a pipeline is actively running (lock
// held), cleanTicket must return errSkipped even when force=true.
func TestCleanTicket_ForceStillChecksFlockWhenLockHeld(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeCleanMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseRunning},
		},
	})

	// Acquire an exclusive flock on the lock file to simulate a running pipeline.
	lockPath := filepath.Join(ticketDir, "lock")
	fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	defer fd.Close()
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	defer syscall.Flock(int(fd.Fd()), syscall.LOCK_UN) //nolint:errcheck

	// Even with force=true, cleanTicket must refuse because the lock is held.
	err = cleanTicket(context.Background(), stateDir, "TICKET-1", false, true, true)
	if !errors.Is(err, errSkipped) {
		t.Fatalf("expected errSkipped when lock is held with force=true, got %v", err)
	}

	// State directory must still exist — nothing was cleaned.
	if _, statErr := os.Stat(ticketDir); statErr != nil {
		t.Errorf("expected state dir to still exist: %v", statErr)
	}
}

func TestCleanTicket_DryRunWithBranchShowsRemote(t *testing.T) {
	stateDir := t.TempDir()

	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Branch: "soda/TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	// Dry-run should not error even without a git repo context —
	// it only prints what it would do.
	err := cleanTicket(context.Background(), stateDir, "TICKET-1", true, false, false)
	if err != nil {
		t.Fatalf("cleanTicket dry-run with branch: %v", err)
	}

	// Verify state directory still exists after dry-run.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); statErr != nil {
		t.Errorf("expected state dir to still exist after dry-run: %v", statErr)
	}
}

func TestCleanTicket_PreservesSessionData(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	// No Branch/Worktree set — refs are already empty so clearCleanedRefs
	// will mark both as cleared.
	writeCleanMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	// Write additional session files that should be preserved.
	if err := os.WriteFile(filepath.Join(ticketDir, "events.jsonl"), []byte(`{"kind":"test"}`+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile events.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ticketDir, "triage.md"), []byte("artifact"), 0644); err != nil {
		t.Fatalf("WriteFile triage.md: %v", err)
	}

	// Default clean (no --purge) should preserve session data.
	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, false, false)
	if err != nil {
		t.Fatalf("cleanTicket preserve: %v", err)
	}

	// State directory should still exist.
	if _, statErr := os.Stat(ticketDir); statErr != nil {
		t.Fatalf("expected state dir to still exist: %v", statErr)
	}

	// meta.json should still exist.
	if _, readErr := pipeline.ReadMeta(filepath.Join(ticketDir, "meta.json")); readErr != nil {
		t.Fatalf("ReadMeta after clean: %v", readErr)
	}

	// events.jsonl should be preserved.
	if _, statErr := os.Stat(filepath.Join(ticketDir, "events.jsonl")); statErr != nil {
		t.Errorf("expected events.jsonl to be preserved: %v", statErr)
	}

	// Phase artifacts should be preserved.
	if _, statErr := os.Stat(filepath.Join(ticketDir, "triage.md")); statErr != nil {
		t.Errorf("expected triage.md to be preserved: %v", statErr)
	}

	// Lock file should be removed.
	if _, statErr := os.Stat(filepath.Join(ticketDir, "lock")); !os.IsNotExist(statErr) {
		t.Error("expected lock file to be removed after clean")
	}
}

func TestCleanTicket_PreserveClearsRefsInMeta(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	// No Branch/Worktree — both are already empty so clearCleanedRefs will
	// set worktreeCleared=true and branchCleared=true immediately.
	writeCleanMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket:    "TICKET-1",
		Summary:   "test summary",
		TotalCost: 1.50,
		Phases: map[string]*pipeline.PhaseState{
			"triage":    {Status: pipeline.PhaseCompleted, Cost: 0.50},
			"implement": {Status: pipeline.PhaseCompleted, Cost: 1.00},
		},
	})

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, false, false)
	if err != nil {
		t.Fatalf("cleanTicket preserve: %v", err)
	}

	meta, err := pipeline.ReadMeta(filepath.Join(ticketDir, "meta.json"))
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	// Non-ref fields should be preserved.
	if meta.Ticket != "TICKET-1" {
		t.Errorf("Ticket = %q, want TICKET-1", meta.Ticket)
	}
	if meta.Summary != "test summary" {
		t.Errorf("Summary = %q, want 'test summary'", meta.Summary)
	}
	if meta.TotalCost != 1.50 {
		t.Errorf("TotalCost = %f, want 1.50", meta.TotalCost)
	}
	if len(meta.Phases) != 2 {
		t.Errorf("Phases count = %d, want 2", len(meta.Phases))
	}

	// Ref fields should be cleared (both were empty, so both marked as cleared).
	if meta.Branch != "" {
		t.Errorf("Branch = %q, want empty", meta.Branch)
	}
	if meta.Worktree != "" {
		t.Errorf("Worktree = %q, want empty", meta.Worktree)
	}
}

func TestCleanTicket_FailedWorktreeRemovePreservesRef(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	// Set a fake worktree path that git cannot remove — the worktree ref
	// should be preserved in meta.json since the removal failed.
	writeCleanMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket:   "TICKET-1",
		Worktree: "/tmp/nonexistent-worktree-for-clean-test",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, false, false)
	if err != nil {
		t.Fatalf("cleanTicket: %v", err)
	}

	meta, err := pipeline.ReadMeta(filepath.Join(ticketDir, "meta.json"))
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	// Worktree reference should still be set since removal failed.
	if meta.Worktree == "" {
		t.Error("Worktree ref should be preserved when worktree removal fails")
	}
}

func TestCleanAll_PreservesSessionData(t *testing.T) {
	stateDir := t.TempDir()

	// No Branch set so the ref is treated as already clean.
	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})
	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-2"), &pipeline.PipelineMeta{
		Ticket: "TICKET-2",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhasePending},
		},
	})

	// Default clean (no --purge) preserves session data.
	err := cleanAll(context.Background(), stateDir, false, false, false)
	if err != nil {
		t.Fatalf("cleanAll preserve: %v", err)
	}

	// TICKET-1 (terminal) state dir should still exist with meta preserved.
	_, readErr := pipeline.ReadMeta(filepath.Join(stateDir, "TICKET-1", "meta.json"))
	if readErr != nil {
		t.Fatalf("expected TICKET-1 meta.json to be preserved: %v", readErr)
	}

	// TICKET-2 (non-terminal) should still exist untouched.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-2")); statErr != nil {
		t.Errorf("expected TICKET-2 to still exist: %v", statErr)
	}
}
