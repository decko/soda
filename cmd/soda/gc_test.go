package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/decko/soda/internal/pipeline"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{
			name:  "stdlib hours",
			input: "24h",
			want:  24 * time.Hour,
		},
		{
			name:  "days",
			input: "30d",
			want:  30 * 24 * time.Hour,
		},
		{
			name:  "weeks",
			input: "2w",
			want:  2 * 7 * 24 * time.Hour,
		},
		{
			name:  "compound weeks and days",
			input: "1w2d",
			want:  (7 + 2) * 24 * time.Hour,
		},
		{
			name:  "compound days and hours",
			input: "1d12h",
			want:  36 * time.Hour,
		},
		{
			name:  "zero",
			input: "0s",
			want:  0,
		},
		{
			name:  "zero days",
			input: "0d",
			want:  0,
		},
		{
			name:  "stdlib minutes",
			input: "30m",
			want:  30 * time.Minute,
		},
		{
			name:  "compound weeks days hours",
			input: "1w1d1h",
			want:  (8*24 + 1) * time.Hour,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid unit",
			input:   "10x",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDuration(%q) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDuration(%q) error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestGcIsTerminal(t *testing.T) {
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
			name: "has paused → not terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhasePaused},
			}},
			want: false,
		},
		{
			name: "has pending → not terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage": {Status: pipeline.PhasePending},
			}},
			want: false,
		},
		{
			name: "completed + paused → not terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhasePaused},
			}},
			want: false,
		},
		{
			name: "completed + pending → not terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhasePending},
			}},
			want: false,
		},
		{
			name: "completed + skipped → terminal",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"patch":     {Status: pipeline.PhaseSkipped},
				"implement": {Status: pipeline.PhaseCompleted},
			}},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := gcIsTerminal(tc.meta)
			if got != tc.want {
				t.Errorf("gcIsTerminal() = %v, want %v", got, tc.want)
			}
		})
	}
}

// writeGcMeta creates a session directory with a meta.json file.
func writeGcMeta(t *testing.T, dir string, meta *pipeline.PipelineMeta) {
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

// setDirMtime sets the modification time of a directory.
func setDirMtime(t *testing.T, dir string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(dir, mtime, mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

func TestGcSessions_RemovesOldTerminal(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	// Set mtime to 60 days ago.
	setDirMtime(t, ticketDir, time.Now().Add(-60*24*time.Hour))

	err := gcSessions(context.Background(), stateDir, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("gcSessions: %v", err)
	}

	if _, statErr := os.Stat(ticketDir); !os.IsNotExist(statErr) {
		t.Error("expected old terminal session to be removed")
	}
}

func TestGcSessions_SkipsRecentSession(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	// Leave mtime as now (recent).
	err := gcSessions(context.Background(), stateDir, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("gcSessions: %v", err)
	}

	if _, statErr := os.Stat(ticketDir); statErr != nil {
		t.Errorf("expected recent session to be preserved: %v", statErr)
	}
}

func TestGcSessions_SkipsNonTerminal(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhasePending},
		},
	})

	setDirMtime(t, ticketDir, time.Now().Add(-60*24*time.Hour))

	err := gcSessions(context.Background(), stateDir, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("gcSessions: %v", err)
	}

	if _, statErr := os.Stat(ticketDir); statErr != nil {
		t.Errorf("expected non-terminal session to be preserved: %v", statErr)
	}
}

func TestGcSessions_SkipsPaused(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage":    {Status: pipeline.PhaseCompleted},
			"implement": {Status: pipeline.PhasePaused},
		},
	})

	setDirMtime(t, ticketDir, time.Now().Add(-60*24*time.Hour))

	err := gcSessions(context.Background(), stateDir, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("gcSessions: %v", err)
	}

	if _, statErr := os.Stat(ticketDir); statErr != nil {
		t.Errorf("expected paused session to be preserved: %v", statErr)
	}
}

func TestGcSessions_SkipsLockedSession(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	setDirMtime(t, ticketDir, time.Now().Add(-60*24*time.Hour))

	// Acquire an exclusive flock to simulate a running pipeline.
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

	gcErr := gcSessions(context.Background(), stateDir, 30*24*time.Hour, false)
	if gcErr != nil {
		t.Fatalf("gcSessions: %v", gcErr)
	}

	if _, statErr := os.Stat(ticketDir); statErr != nil {
		t.Errorf("expected locked session to be preserved: %v", statErr)
	}
}

func TestGcSessions_SkipsWorktreeSession(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket:   "TICKET-1",
		Worktree: "/some/worktree/path",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	setDirMtime(t, ticketDir, time.Now().Add(-60*24*time.Hour))

	err := gcSessions(context.Background(), stateDir, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("gcSessions: %v", err)
	}

	if _, statErr := os.Stat(ticketDir); statErr != nil {
		t.Errorf("expected session with worktree to be preserved: %v", statErr)
	}
}

func TestGcSessions_DryRun(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	setDirMtime(t, ticketDir, time.Now().Add(-60*24*time.Hour))

	err := gcSessions(context.Background(), stateDir, 30*24*time.Hour, true)
	if err != nil {
		t.Fatalf("gcSessions dry-run: %v", err)
	}

	// Session should still exist after dry-run.
	if _, statErr := os.Stat(ticketDir); statErr != nil {
		t.Errorf("expected session to still exist after dry-run: %v", statErr)
	}
}

func TestGcSessions_ZeroDuration(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TICKET-1")

	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})

	// --older-than 0 should bypass age check and remove all eligible sessions.
	err := gcSessions(context.Background(), stateDir, 0, false)
	if err != nil {
		t.Fatalf("gcSessions: %v", err)
	}

	if _, statErr := os.Stat(ticketDir); !os.IsNotExist(statErr) {
		t.Error("expected session to be removed with olderThan=0")
	}
}

func TestGcSessions_NonexistentDir(t *testing.T) {
	err := gcSessions(context.Background(), "/tmp/nonexistent-soda-gc-test", 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("gcSessions should not error for nonexistent dir: %v", err)
	}
}

func TestGcSessions_PreservesCostJson(t *testing.T) {
	stateDir := t.TempDir()

	// Create a cost.json file at the stateDir root.
	costPath := filepath.Join(stateDir, "cost.json")
	if err := os.WriteFile(costPath, []byte(`[]`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create a session to collect.
	ticketDir := filepath.Join(stateDir, "TICKET-1")
	writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
		Ticket: "TICKET-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
	})
	setDirMtime(t, ticketDir, time.Now().Add(-60*24*time.Hour))

	err := gcSessions(context.Background(), stateDir, 30*24*time.Hour, false)
	if err != nil {
		t.Fatalf("gcSessions: %v", err)
	}

	// cost.json must survive.
	if _, statErr := os.Stat(costPath); statErr != nil {
		t.Errorf("expected cost.json to be preserved: %v", statErr)
	}
}

func TestGcSessions_ContextCancelled(t *testing.T) {
	stateDir := t.TempDir()

	// Create two sessions.
	for _, ticket := range []string{"TICKET-1", "TICKET-2"} {
		ticketDir := filepath.Join(stateDir, ticket)
		writeGcMeta(t, ticketDir, &pipeline.PipelineMeta{
			Ticket: ticket,
			Phases: map[string]*pipeline.PhaseState{
				"triage": {Status: pipeline.PhaseCompleted},
			},
		})
		setDirMtime(t, ticketDir, time.Now().Add(-60*24*time.Hour))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := gcSessions(ctx, stateDir, 30*24*time.Hour, false)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestNewGcCmd_Flags(t *testing.T) {
	cmd := newGcCmd()

	olderThanFlag := cmd.Flags().Lookup("older-than")
	if olderThanFlag == nil {
		t.Fatal("--older-than flag not found")
	}
	if olderThanFlag.DefValue != "30d" {
		t.Errorf("--older-than default = %q, want %q", olderThanFlag.DefValue, "30d")
	}

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	if dryRunFlag == nil {
		t.Fatal("--dry-run flag not found")
	}
	if dryRunFlag.DefValue != "false" {
		t.Errorf("--dry-run default = %q, want %q", dryRunFlag.DefValue, "false")
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()

	// Create some files with known sizes.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), make([]byte, 100), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "b.txt"), make([]byte, 200), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	size, err := dirSize(dir)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	if size != 300 {
		t.Errorf("dirSize = %d, want 300", size)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"},
	}
	for _, tc := range tests {
		got := formatBytes(tc.input)
		if got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
