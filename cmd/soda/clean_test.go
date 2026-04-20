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

func TestCleanTicket_TerminalState(t *testing.T) {
	stateDir := t.TempDir()

	writeCleanMeta(t, filepath.Join(stateDir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, false)
	if err != nil {
		t.Fatalf("cleanTicket: %v", err)
	}

	// Verify state directory was removed.
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

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, false)
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

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", false, true)
	if err != nil {
		t.Fatalf("cleanTicket with force: %v", err)
	}

	// Verify state directory was removed despite non-terminal state.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); !os.IsNotExist(statErr) {
		t.Error("expected state dir to be removed with --force")
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

	err := cleanTicket(context.Background(), stateDir, "TICKET-1", true, false)
	if err != nil {
		t.Fatalf("cleanTicket dry-run: %v", err)
	}

	// Verify state directory still exists after dry-run.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); statErr != nil {
		t.Errorf("expected state dir to still exist after dry-run: %v", statErr)
	}
}

func TestCleanAll_CleansTerminal(t *testing.T) {
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

	err := cleanAll(context.Background(), stateDir, false, false)
	if err != nil {
		t.Fatalf("cleanAll: %v", err)
	}

	// TICKET-1 (terminal) should be removed.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); !os.IsNotExist(statErr) {
		t.Error("expected TICKET-1 to be removed")
	}
	// TICKET-2 (non-terminal) should still exist.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-2")); statErr != nil {
		t.Errorf("expected TICKET-2 to still exist: %v", statErr)
	}
}

func TestCleanAll_ForceRemovesAll(t *testing.T) {
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

	err := cleanAll(context.Background(), stateDir, false, true)
	if err != nil {
		t.Fatalf("cleanAll with force: %v", err)
	}

	// Both should be removed when force is true.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); !os.IsNotExist(statErr) {
		t.Error("expected TICKET-1 to be removed")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-2")); !os.IsNotExist(statErr) {
		t.Error("expected TICKET-2 to be removed with --force")
	}
}

func TestCleanAll_NonexistentDir(t *testing.T) {
	err := cleanAll(context.Background(), "/tmp/nonexistent-soda-clean-test", false, false)
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
	err = cleanTicket(context.Background(), stateDir, "TICKET-1", false, true)
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
	err := cleanTicket(context.Background(), stateDir, "TICKET-1", true, false)
	if err != nil {
		t.Fatalf("cleanTicket dry-run with branch: %v", err)
	}

	// Verify state directory still exists after dry-run.
	if _, statErr := os.Stat(filepath.Join(stateDir, "TICKET-1")); statErr != nil {
		t.Errorf("expected state dir to still exist after dry-run: %v", statErr)
	}
}
