package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadCostLedger_Missing(t *testing.T) {
	dir := t.TempDir()
	entries, err := ReadCostLedger(dir)
	if err != nil {
		t.Fatalf("unexpected error for missing ledger: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(entries))
	}
}

func TestAppendCostEntry_CreatesAndAccumulates(t *testing.T) {
	dir := t.TempDir()

	e1 := CostEntry{Ticket: "PROJ-1", Timestamp: time.Now(), Cost: 1.23, Success: true}
	e2 := CostEntry{Ticket: "PROJ-2", Timestamp: time.Now(), Cost: 4.56, Success: false}

	if err := AppendCostEntry(dir, e1); err != nil {
		t.Fatalf("AppendCostEntry first: %v", err)
	}
	if err := AppendCostEntry(dir, e2); err != nil {
		t.Fatalf("AppendCostEntry second: %v", err)
	}

	entries, err := ReadCostLedger(dir)
	if err != nil {
		t.Fatalf("ReadCostLedger: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Ticket != "PROJ-1" || entries[0].Cost != 1.23 || !entries[0].Success {
		t.Errorf("entry[0] = %+v, want {PROJ-1 1.23 true}", entries[0])
	}
	if entries[1].Ticket != "PROJ-2" || entries[1].Cost != 4.56 || entries[1].Success {
		t.Errorf("entry[1] = %+v, want {PROJ-2 4.56 false}", entries[1])
	}
}

// TestCostLedgerSurvivesClean verifies that cost.json lives at the stateDir
// root (not inside a session subdirectory) and therefore survives when a
// ticket's session directory is removed (as soda clean does).
func TestCostLedgerSurvivesClean(t *testing.T) {
	dir := t.TempDir()

	// Write a cost entry to the ledger at the stateDir root.
	if err := AppendCostEntry(dir, CostEntry{
		Ticket:    "TICKET-1",
		Timestamp: time.Now(),
		Cost:      1.00,
		Success:   true,
	}); err != nil {
		t.Fatalf("AppendCostEntry: %v", err)
	}

	// Verify cost.json is at stateDir root, not inside any session subdir.
	ledgerPath := CostLedgerPath(dir)
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("cost.json not found at stateDir root: %v", err)
	}
	if filepath.Dir(ledgerPath) != dir {
		t.Errorf("cost.json dir = %q, want %q", filepath.Dir(ledgerPath), dir)
	}

	// Simulate soda clean: remove the ticket session directory.
	ticketDir := filepath.Join(dir, "TICKET-1")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ticketDir); err != nil {
		t.Fatalf("RemoveAll ticket dir: %v", err)
	}

	// cost.json at stateDir root must still exist and contain the entry.
	entries, err := ReadCostLedger(dir)
	if err != nil {
		t.Fatalf("ReadCostLedger after simulated clean: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after clean, got %d", len(entries))
	}
	if len(entries) > 0 && entries[0].Ticket != "TICKET-1" {
		t.Errorf("entry[0].Ticket = %q, want TICKET-1", entries[0].Ticket)
	}
}

func TestCumulativeCost_WithLedgerOnly(t *testing.T) {
	dir := t.TempDir()

	// Two runs for the same ticket recorded in the ledger.
	if err := AppendCostEntry(dir, CostEntry{Ticket: "T-1", Timestamp: time.Now(), Cost: 1.00, Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := AppendCostEntry(dir, CostEntry{Ticket: "T-1", Timestamp: time.Now(), Cost: 2.00, Success: false}); err != nil {
		t.Fatal(err)
	}

	cost, err := CumulativeCost(dir)
	if err != nil {
		t.Fatalf("CumulativeCost: %v", err)
	}
	if cost != 3.00 {
		t.Errorf("CumulativeCost = %f, want 3.00", cost)
	}
}

func TestCumulativeCost_LedgerAndLegacyMeta(t *testing.T) {
	dir := t.TempDir()

	// T-1 is in the ledger (represents a cleaned session — no meta.json).
	if err := AppendCostEntry(dir, CostEntry{Ticket: "T-1", Timestamp: time.Now(), Cost: 1.00, Success: true}); err != nil {
		t.Fatal(err)
	}

	// T-2 is a legacy session not in the ledger but with a meta.json.
	writeTestMeta(t, filepath.Join(dir, "T-2"), &PipelineMeta{
		Ticket:    "T-2",
		TotalCost: 2.00,
		StartedAt: time.Now(),
		Phases:    map[string]*PhaseState{},
	})

	cost, err := CumulativeCost(dir)
	if err != nil {
		t.Fatalf("CumulativeCost: %v", err)
	}
	// Ledger (1.00) + legacy meta (2.00) = 3.00, no double-counting.
	if cost != 3.00 {
		t.Errorf("CumulativeCost = %f, want 3.00", cost)
	}
}

func TestCumulativeCost_NoDoubleCounting(t *testing.T) {
	dir := t.TempDir()

	// T-1 is in the ledger AND has a meta.json (active session, not yet cleaned).
	if err := AppendCostEntry(dir, CostEntry{Ticket: "T-1", Timestamp: time.Now(), Cost: 1.00, Success: true}); err != nil {
		t.Fatal(err)
	}
	writeTestMeta(t, filepath.Join(dir, "T-1"), &PipelineMeta{
		Ticket:    "T-1",
		TotalCost: 1.00,
		StartedAt: time.Now(),
		Phases:    map[string]*PhaseState{},
	})

	cost, err := CumulativeCost(dir)
	if err != nil {
		t.Fatalf("CumulativeCost: %v", err)
	}
	// Only ledger is used for T-1; meta.json is skipped to avoid double-counting.
	if cost != 1.00 {
		t.Errorf("CumulativeCost = %f, want 1.00 (no double-count)", cost)
	}
}

func TestCumulativeCost_NoDoubleCountingSlugifiedDir(t *testing.T) {
	dir := t.TempDir()

	// Ledger entry uses the canonical ticket key "PROJ-42".
	if err := AppendCostEntry(dir, CostEntry{Ticket: "PROJ-42", Timestamp: time.Now(), Cost: 3.00, Success: true}); err != nil {
		t.Fatal(err)
	}

	// On-disk directory is a slugified variant ("proj-42-slugified"), but
	// meta.json inside it records the canonical ticket key "PROJ-42".
	// CumulativeCost must match on meta.Ticket (not the dir name) to avoid
	// double-counting.
	writeTestMeta(t, filepath.Join(dir, "proj-42-slugified"), &PipelineMeta{
		Ticket:    "PROJ-42",
		TotalCost: 3.00,
		StartedAt: time.Now(),
		Phases:    map[string]*PhaseState{},
	})

	cost, err := CumulativeCost(dir)
	if err != nil {
		t.Fatalf("CumulativeCost: %v", err)
	}
	// The session should NOT be double-counted; only the ledger entry counts.
	if cost != 3.00 {
		t.Errorf("CumulativeCost = %f, want 3.00 (no double-count for slugified dir)", cost)
	}
}
