package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/decko/soda/internal/pipeline"
)

func writeMeta(t *testing.T, dir string, meta *pipeline.PipelineMeta) {
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

func TestCollectSessions_Empty(t *testing.T) {
	dir := t.TempDir()
	rows, err := collectSessions(dir, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestCollectSessions_NonexistentDir(t *testing.T) {
	rows, err := collectSessions("/tmp/nonexistent-soda-sessions-test", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestCollectSessions_ReadsMeta(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)

	writeMeta(t, filepath.Join(dir, "TICKET-1"), &pipeline.PipelineMeta{
		Ticket:    "TICKET-1",
		Summary:   "First ticket",
		StartedAt: now.Add(-2 * time.Hour),
		TotalCost: 1.23,
		Phases: map[string]*pipeline.PhaseState{
			"triage":    {Status: pipeline.PhaseCompleted},
			"implement": {Status: pipeline.PhaseCompleted},
		},
	})
	writeMeta(t, filepath.Join(dir, "TICKET-2"), &pipeline.PipelineMeta{
		Ticket:    "TICKET-2",
		Summary:   "Second ticket",
		StartedAt: now.Add(-30 * time.Minute),
		TotalCost: 5.58,
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseFailed, Error: "timeout"},
		},
	})

	rows, err := collectSessions(dir, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	// Verify fields populated
	found := map[string]bool{}
	for _, row := range rows {
		found[row.ticket] = true
		if row.cost == "" || row.elapsed == "" || row.status == "" {
			t.Errorf("row %s: missing fields (cost=%q, elapsed=%q, status=%q)", row.ticket, row.cost, row.elapsed, row.status)
		}
	}
	if !found["TICKET-1"] || !found["TICKET-2"] {
		t.Errorf("expected both tickets, found: %v", found)
	}
}

func TestFilterSessionsByStatus(t *testing.T) {
	rows := []sessionEntry{
		{ticket: "A", status: "completed"},
		{ticket: "B", status: "failed"},
		{ticket: "C", status: "completed"},
		{ticket: "D", status: "running"},
	}

	completed := filterSessionsByStatus(rows, "completed")
	if len(completed) != 2 {
		t.Errorf("expected 2 completed, got %d", len(completed))
	}
	for _, row := range completed {
		if row.status != "completed" {
			t.Errorf("expected completed, got %q", row.status)
		}
	}

	running := filterSessionsByStatus(rows, "running")
	if len(running) != 1 {
		t.Errorf("expected 1 running, got %d", len(running))
	}

	none := filterSessionsByStatus(rows, "stale")
	if len(none) != 0 {
		t.Errorf("expected 0 stale, got %d", len(none))
	}
}

func TestSortSessions(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)

	rows := []sessionEntry{
		{ticket: "A", cost: "$1.00", startedAt: now.Add(-3 * time.Hour)},
		{ticket: "B", cost: "$5.00", startedAt: now.Add(-1 * time.Hour)},
		{ticket: "C", cost: "$0.50", startedAt: now.Add(-2 * time.Hour)},
	}

	t.Run("sort by date (default)", func(t *testing.T) {
		r := make([]sessionEntry, len(rows))
		copy(r, rows)
		sortSessions(r, "date")
		wantOrder := []string{"B", "C", "A"} // newest first
		for i, want := range wantOrder {
			if r[i].ticket != want {
				t.Errorf("rows[%d].ticket = %q, want %q", i, r[i].ticket, want)
			}
		}
	})

	t.Run("sort by cost", func(t *testing.T) {
		r := make([]sessionEntry, len(rows))
		copy(r, rows)
		sortSessions(r, "cost")
		wantOrder := []string{"B", "A", "C"} // highest cost first
		for i, want := range wantOrder {
			if r[i].ticket != want {
				t.Errorf("rows[%d].ticket = %q, want %q", i, r[i].ticket, want)
			}
		}
	})

	t.Run("sort by elapsed", func(t *testing.T) {
		r := make([]sessionEntry, len(rows))
		copy(r, rows)
		sortSessions(r, "elapsed")
		wantOrder := []string{"A", "C", "B"} // longest running first
		for i, want := range wantOrder {
			if r[i].ticket != want {
				t.Errorf("rows[%d].ticket = %q, want %q", i, r[i].ticket, want)
			}
		}
	})
}

func TestFormatLastRun(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		startedAt time.Time
		want      string
	}{
		{"just now", now, "now"},
		{"30 seconds ago", now.Add(-30 * time.Second), "now"},
		{"1 minute ago", now.Add(-90 * time.Second), "1m ago"},
		{"5 minutes ago", now.Add(-5 * time.Minute), "5m ago"},
		{"1 hour ago", now.Add(-time.Hour), "1h ago"},
		{"3 hours ago", now.Add(-3 * time.Hour), "3h ago"},
		{"1 day ago", now.Add(-24 * time.Hour), "1d ago"},
		{"3 days ago", now.Add(-72 * time.Hour), "3d ago"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatLastRun(tc.startedAt, now)
			if got != tc.want {
				t.Errorf("formatLastRun() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"this is a long string", 10, "this is..."},
		{"ab", 3, "ab"},
		{"abcd", 3, "abc"},
	}
	for _, tc := range tests {
		got := truncate(tc.input, tc.maxLen)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
		}
	}
}

func TestSessionsSummaryLine(t *testing.T) {
	tests := []struct {
		name string
		rows []sessionEntry
		want string
	}{
		{
			name: "mixed statuses",
			rows: []sessionEntry{
				{status: "completed"},
				{status: "completed"},
				{status: "running"},
				{status: "failed"},
			},
			want: "4 sessions (1 running, 2 completed, 1 failed)",
		},
		{
			name: "all completed",
			rows: []sessionEntry{
				{status: "completed"},
				{status: "completed"},
			},
			want: "2 sessions (2 completed)",
		},
		{
			name: "single session",
			rows: []sessionEntry{
				{status: "running"},
			},
			want: "1 session (1 running)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionsSummaryLine(tc.rows)
			if got != tc.want {
				t.Errorf("sessionsSummaryLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseCost(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"$1.23", 1.23},
		{"$0.00", 0.00},
		{"$10.50", 10.50},
	}
	for _, tc := range tests {
		got := parseCost(tc.input)
		if got != tc.want {
			t.Errorf("parseCost(%q) = %f, want %f", tc.input, got, tc.want)
		}
	}
}

func TestNewSessionsCmd_Flags(t *testing.T) {
	cmd := newSessionsCmd()

	statusFlag := cmd.Flags().Lookup("status")
	if statusFlag == nil {
		t.Fatal("--status flag not found")
	}
	if statusFlag.DefValue != "" {
		t.Errorf("--status default = %q, want empty", statusFlag.DefValue)
	}

	sortFlag := cmd.Flags().Lookup("sort")
	if sortFlag == nil {
		t.Fatal("--sort flag not found")
	}
	if sortFlag.DefValue != "date" {
		t.Errorf("--sort default = %q, want %q", sortFlag.DefValue, "date")
	}

	allFlag := cmd.Flags().Lookup("all")
	if allFlag == nil {
		t.Fatal("--all flag not found")
	}
	if allFlag.DefValue != "false" {
		t.Errorf("--all default = %q, want %q", allFlag.DefValue, "false")
	}
}
