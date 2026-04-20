package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/pipeline"
)

func TestPipelineStatus(t *testing.T) {
	tests := []struct {
		name     string
		meta     *pipeline.PipelineMeta
		lockInfo *pipeline.LockInfo
		want     string
	}{
		{
			name:     "lock alive → running",
			meta:     &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{}},
			lockInfo: &pipeline.LockInfo{IsAlive: true},
			want:     "running",
		},
		{
			name:     "lock dead → stale",
			meta:     &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{}},
			lockInfo: &pipeline.LockInfo{IsAlive: false},
			want:     "stale",
		},
		{
			name: "no lock, all completed → completed",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhaseCompleted},
			}},
			lockInfo: nil,
			want:     "completed",
		},
		{
			name: "no lock, completed + skipped → completed",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhaseSkipped},
			}},
			lockInfo: nil,
			want:     "completed",
		},
		{
			name: "no lock, any failed → failed",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage":    {Status: pipeline.PhaseCompleted},
				"implement": {Status: pipeline.PhaseFailed},
			}},
			lockInfo: nil,
			want:     "failed",
		},
		{
			name: "no lock, mixed non-terminal → fallback to most advanced",
			meta: &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{
				"triage": {Status: pipeline.PhaseCompleted},
				"plan":   {Status: pipeline.PhaseRetrying},
			}},
			lockInfo: nil,
			want:     "retrying",
		},
		{
			name:     "no lock, no phases → pending",
			meta:     &pipeline.PipelineMeta{Phases: map[string]*pipeline.PhaseState{}},
			lockInfo: nil,
			want:     "pending",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pipelineStatus(tc.meta, tc.lockInfo)
			if got != tc.want {
				t.Errorf("pipelineStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSortEntries(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)

	rows := []pipelineEntry{
		{ticket: "OLD-1", status: "completed", startedAt: now.Add(-2 * time.Hour)},
		{ticket: "RUN-1", status: "running", startedAt: now.Add(-1 * time.Hour)},
		{ticket: "OLD-2", status: "failed", startedAt: now.Add(-30 * time.Minute)},
		{ticket: "RUN-2", status: "running", startedAt: now.Add(-10 * time.Minute)},
		{ticket: "STALE-1", status: "stale", startedAt: now.Add(-3 * time.Hour)},
	}

	sortEntries(rows)

	// Expected order: active group first (running, stale), newest-first within group;
	// then terminal group (completed, failed), newest-first within group.
	wantOrder := []string{"RUN-2", "RUN-1", "STALE-1", "OLD-2", "OLD-1"}
	for i, want := range wantOrder {
		if rows[i].ticket != want {
			t.Errorf("rows[%d].ticket = %q, want %q", i, rows[i].ticket, want)
		}
	}
}

func TestStatusGroup(t *testing.T) {
	tests := []struct {
		status string
		want   int
	}{
		{"running", 0},
		{"stale", 0},
		{"retrying", 0},
		{"pending", 0},
		{"completed", 1},
		{"failed", 1},
	}
	for _, tc := range tests {
		got := statusGroup(tc.status)
		if got != tc.want {
			t.Errorf("statusGroup(%q) = %d, want %d", tc.status, got, tc.want)
		}
	}
}

func TestFormatSubmitted(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name      string
		startedAt time.Time
		now       time.Time
		want      string
	}{
		{
			name:      "same day → time only",
			startedAt: time.Date(2025, 6, 15, 9, 5, 0, 0, time.UTC),
			now:       now,
			want:      "       09:05",
		},
		{
			name:      "yesterday → date + time",
			startedAt: time.Date(2025, 6, 14, 22, 0, 0, 0, time.UTC),
			now:       now,
			want:      "Jun 14 22:00",
		},
		{
			name:      "different year → date + time",
			startedAt: time.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC),
			now:       now,
			want:      "Dec 31 23:59",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatSubmitted(tc.startedAt, tc.now)
			if got != tc.want {
				t.Errorf("formatSubmitted() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunStatus_CumulativeCostFooter(t *testing.T) {
	dir := t.TempDir()

	// Create two sessions with known costs.
	writeStatusMeta(t, filepath.Join(dir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket:    "TICKET-1",
		StartedAt: time.Now().Add(-1 * time.Hour),
		TotalCost: 2.50,
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted, DurationMs: 5000, Cost: 2.50},
		},
	})
	writeStatusMeta(t, filepath.Join(dir, "TICKET-2"), &pipeline.PipelineMeta{
		Ticket:    "TICKET-2",
		StartedAt: time.Now().Add(-30 * time.Minute),
		TotalCost: 3.75,
		Phases: map[string]*pipeline.PhaseState{
			"triage":    {Status: pipeline.PhaseCompleted, DurationMs: 3000, Cost: 1.00},
			"implement": {Status: pipeline.PhaseCompleted, DurationMs: 10000, Cost: 2.75},
		},
	})

	// Capture stdout.
	oldStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writePipe

	runErr := runStatus(dir)

	writePipe.Close()
	os.Stdout = oldStdout

	var buf strings.Builder
	data := make([]byte, 4096)
	for {
		n, readErr := readPipe.Read(data)
		if n > 0 {
			buf.Write(data[:n])
		}
		if readErr != nil {
			break
		}
	}
	readPipe.Close()

	if runErr != nil {
		t.Fatalf("runStatus error: %v", runErr)
	}

	output := buf.String()
	wantFooter := "Total cost across all sessions: $6.25"
	if !strings.Contains(output, wantFooter) {
		t.Errorf("output does not contain cumulative cost footer.\nwant: %q\ngot:\n%s", wantFooter, output)
	}
}

func writeStatusMeta(t *testing.T, dir string, meta *pipeline.PipelineMeta) {
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

func TestRunStatus_ReworkAndTrendColumns(t *testing.T) {
	dir := t.TempDir()

	// Create a session with known rework cycles.
	writeStatusMeta(t, filepath.Join(dir, "TICKET-10"), &pipeline.PipelineMeta{
		Ticket:       "TICKET-10",
		StartedAt:    time.Now().Add(-1 * time.Hour),
		TotalCost:    5.00,
		ReworkCycles: 3,
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted, DurationMs: 5000, Cost: 5.00},
		},
	})

	// Add cost ledger entries so trend can be computed: increasing trend (▲).
	if err := pipeline.AppendCostEntry(dir, pipeline.CostEntry{
		Ticket: "TICKET-10", Cost: 1.00, Success: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.AppendCostEntry(dir, pipeline.CostEntry{
		Ticket: "TICKET-10", Cost: 5.00, Success: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Capture stdout.
	oldStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writePipe

	runErr := runStatus(dir)

	writePipe.Close()
	os.Stdout = oldStdout

	var buf strings.Builder
	data := make([]byte, 4096)
	for {
		n, readErr := readPipe.Read(data)
		if n > 0 {
			buf.Write(data[:n])
		}
		if readErr != nil {
			break
		}
	}
	readPipe.Close()

	if runErr != nil {
		t.Fatalf("runStatus error: %v", runErr)
	}

	output := buf.String()

	// Verify header contains new columns.
	if !strings.Contains(output, "REWORK") {
		t.Errorf("output missing REWORK column header.\ngot:\n%s", output)
	}
	if !strings.Contains(output, "TREND") {
		t.Errorf("output missing TREND column header.\ngot:\n%s", output)
	}

	// Verify rework count appears in output.
	if !strings.Contains(output, "3") {
		t.Errorf("output missing rework count 3.\ngot:\n%s", output)
	}

	// Verify trend indicator appears in output (▲ for increasing).
	if !strings.Contains(output, "▲") {
		t.Errorf("output missing trend indicator ▲.\ngot:\n%s", output)
	}
}

func TestRunStatus_DefaultTrendWhenNoLedger(t *testing.T) {
	dir := t.TempDir()

	// Create a session with no cost ledger entries — should get default trend ─.
	writeStatusMeta(t, filepath.Join(dir, "TICKET-20"), &pipeline.PipelineMeta{
		Ticket:    "TICKET-20",
		StartedAt: time.Now().Add(-1 * time.Hour),
		TotalCost: 1.00,
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted, DurationMs: 3000, Cost: 1.00},
		},
	})

	// Capture stdout.
	oldStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writePipe

	runErr := runStatus(dir)

	writePipe.Close()
	os.Stdout = oldStdout

	var buf strings.Builder
	data := make([]byte, 4096)
	for {
		n, readErr := readPipe.Read(data)
		if n > 0 {
			buf.Write(data[:n])
		}
		if readErr != nil {
			break
		}
	}
	readPipe.Close()

	if runErr != nil {
		t.Fatalf("runStatus error: %v", runErr)
	}

	output := buf.String()

	// ReworkCycles defaults to 0.
	if !strings.Contains(output, "0") {
		t.Errorf("output missing default rework count 0.\ngot:\n%s", output)
	}

	// Default trend should be ─.
	if !strings.Contains(output, "─") {
		t.Errorf("output missing default trend indicator ─.\ngot:\n%s", output)
	}
}

func TestColorizeStatus(t *testing.T) {
	tests := []struct {
		status string
		isTTY  bool
		want   string
	}{
		// Non-TTY: no colors.
		{"running", false, "running"},
		{"completed", false, "completed"},
		{"failed", false, "failed"},
		{"stale", false, "stale"},
		{"pending", false, "pending"},

		// TTY: wrapped in ANSI codes.
		{"running", true, statusColorGreen + "running" + statusColorReset},
		{"completed", true, statusColorGreen + "completed" + statusColorReset},
		{"failed", true, statusColorRed + "failed" + statusColorReset},
		{"stale", true, statusColorYellow + "stale" + statusColorReset},
		{"retrying", true, statusColorYellow + "retrying" + statusColorReset},
		{"pending", true, statusColorDim + "pending" + statusColorReset},
	}
	for _, tc := range tests {
		got := colorizeStatus(tc.status, tc.isTTY)
		if got != tc.want {
			t.Errorf("colorizeStatus(%q, %v) = %q, want %q", tc.status, tc.isTTY, got, tc.want)
		}
	}
}
