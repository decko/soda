package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestCostTrendByTicket_Empty(t *testing.T) {
	trends := CostTrendByTicket(nil)
	if len(trends) != 0 {
		t.Errorf("expected empty map, got %v", trends)
	}
}

func TestCostTrendByTicket_SingleEntry(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 5.00},
	}
	trends := CostTrendByTicket(entries)
	if got := trends["T-1"]; got != "─" {
		t.Errorf("single entry trend = %q, want \"─\"", got)
	}
}

func TestCostTrendByTicket_Stable(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 1.00},
		{Ticket: "T-1", Cost: 1.05}, // 5% above average → stable
	}
	trends := CostTrendByTicket(entries)
	if got := trends["T-1"]; got != "─" {
		t.Errorf("stable trend = %q, want \"─\"", got)
	}
}

func TestCostTrendByTicket_Increasing(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 1.00},
		{Ticket: "T-1", Cost: 1.00},
		{Ticket: "T-1", Cost: 1.50}, // 50% above prior avg → ▲
	}
	trends := CostTrendByTicket(entries)
	if got := trends["T-1"]; got != "▲" {
		t.Errorf("increasing trend = %q, want \"▲\"", got)
	}
}

func TestCostTrendByTicket_Decreasing(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 2.00},
		{Ticket: "T-1", Cost: 2.00},
		{Ticket: "T-1", Cost: 1.00}, // 50% below prior avg → ▼
	}
	trends := CostTrendByTicket(entries)
	if got := trends["T-1"]; got != "▼" {
		t.Errorf("decreasing trend = %q, want \"▼\"", got)
	}
}

func TestCostTrendByTicket_MultipleTickets(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 1.00},
		{Ticket: "T-2", Cost: 5.00},
		{Ticket: "T-1", Cost: 2.00}, // ▲ (100% above prior)
		{Ticket: "T-2", Cost: 2.00}, // ▼ (60% below prior)
		{Ticket: "T-3", Cost: 3.00}, // single entry → ─
	}
	trends := CostTrendByTicket(entries)

	if got := trends["T-1"]; got != "▲" {
		t.Errorf("T-1 trend = %q, want \"▲\"", got)
	}
	if got := trends["T-2"]; got != "▼" {
		t.Errorf("T-2 trend = %q, want \"▼\"", got)
	}
	if got := trends["T-3"]; got != "─" {
		t.Errorf("T-3 trend = %q, want \"─\"", got)
	}
}

func TestCostTrendByTicket_BoundaryAt10Percent(t *testing.T) {
	// Exactly at the 10% boundary — should be stable.
	entries := []CostEntry{
		{Ticket: "T-UP", Cost: 1.00},
		{Ticket: "T-UP", Cost: 1.10}, // ratio = 1.10, not > 1.10 → ─

		{Ticket: "T-DOWN", Cost: 1.00},
		{Ticket: "T-DOWN", Cost: 0.90}, // ratio = 0.90, not < 0.90 → ─
	}
	trends := CostTrendByTicket(entries)
	if got := trends["T-UP"]; got != "─" {
		t.Errorf("T-UP at boundary = %q, want \"─\"", got)
	}
	if got := trends["T-DOWN"]; got != "─" {
		t.Errorf("T-DOWN at boundary = %q, want \"─\"", got)
	}
}

func TestCostTrendByTicket_ZeroPriorAverage(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 0.00},
		{Ticket: "T-1", Cost: 1.00}, // prior avg is 0 → ─
	}
	trends := CostTrendByTicket(entries)
	if got := trends["T-1"]; got != "─" {
		t.Errorf("zero prior avg trend = %q, want \"─\"", got)
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

// TestAppendCostEntry_ConcurrentWrites verifies that the flock-based
// serialization in AppendCostEntry prevents lost updates when multiple
// goroutines write concurrently.
func TestAppendCostEntry_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := AppendCostEntry(dir, CostEntry{
				Ticket:    "T-CONCURRENT",
				Timestamp: time.Now(),
				Cost:      1.00,
				Success:   true,
			}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("AppendCostEntry failed during concurrent writes: %v", err)
	}

	entries, err := ReadCostLedger(dir)
	if err != nil {
		t.Fatalf("ReadCostLedger: %v", err)
	}
	if len(entries) != n {
		t.Errorf("len(entries) = %d, want %d (lost updates under concurrency)", len(entries), n)
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

func TestCostByComplexity_Empty(t *testing.T) {
	result := CostByComplexity(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestCostByComplexity_BandGrouping(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 2.00, Complexity: "low"},
		{Ticket: "T-2", Cost: 4.00, Complexity: "low"},
		{Ticket: "T-3", Cost: 10.00, Complexity: "high"},
	}
	result := CostByComplexity(entries)

	low, ok := result["low"]
	if !ok {
		t.Fatal("missing 'low' band")
	}
	if low.Sessions != 2 {
		t.Errorf("low.Sessions = %d, want 2", low.Sessions)
	}
	if low.Mean != 3.00 {
		t.Errorf("low.Mean = %f, want 3.00", low.Mean)
	}
	if low.Median != 3.00 {
		t.Errorf("low.Median = %f, want 3.00", low.Median)
	}
	if low.Total != 6.00 {
		t.Errorf("low.Total = %f, want 6.00", low.Total)
	}

	high, ok := result["high"]
	if !ok {
		t.Fatal("missing 'high' band")
	}
	if high.Sessions != 1 {
		t.Errorf("high.Sessions = %d, want 1", high.Sessions)
	}
	if high.Mean != 10.00 {
		t.Errorf("high.Mean = %f, want 10.00", high.Mean)
	}
	if high.Median != 10.00 {
		t.Errorf("high.Median = %f, want 10.00", high.Median)
	}
	if high.Total != 10.00 {
		t.Errorf("high.Total = %f, want 10.00", high.Total)
	}
}

func TestCostByComplexity_UnknownBand(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 5.00},
		{Ticket: "T-2", Cost: 3.00, Complexity: ""},
	}
	result := CostByComplexity(entries)

	unknown, ok := result["unknown"]
	if !ok {
		t.Fatal("missing 'unknown' band")
	}
	if unknown.Sessions != 2 {
		t.Errorf("unknown.Sessions = %d, want 2", unknown.Sessions)
	}
	if unknown.Total != 8.00 {
		t.Errorf("unknown.Total = %f, want 8.00", unknown.Total)
	}
}

func TestCostByComplexity_EvenMedian(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 1.00, Complexity: "medium"},
		{Ticket: "T-2", Cost: 3.00, Complexity: "medium"},
		{Ticket: "T-3", Cost: 5.00, Complexity: "medium"},
		{Ticket: "T-4", Cost: 7.00, Complexity: "medium"},
	}
	result := CostByComplexity(entries)

	medium := result["medium"]
	// Sorted: 1,3,5,7 → median = (3+5)/2 = 4
	if medium.Median != 4.00 {
		t.Errorf("medium.Median = %f, want 4.00", medium.Median)
	}
}

func TestCostByComplexity_OddMedian(t *testing.T) {
	entries := []CostEntry{
		{Ticket: "T-1", Cost: 1.00, Complexity: "low"},
		{Ticket: "T-2", Cost: 5.00, Complexity: "low"},
		{Ticket: "T-3", Cost: 3.00, Complexity: "low"},
	}
	result := CostByComplexity(entries)

	low := result["low"]
	// Sorted: 1,3,5 → median = 3
	if low.Median != 3.00 {
		t.Errorf("low.Median = %f, want 3.00", low.Median)
	}
}

func TestCostEntryComplexityRoundTrip(t *testing.T) {
	dir := t.TempDir()

	entry := CostEntry{
		Ticket:     "T-1",
		Timestamp:  time.Now().Truncate(time.Second),
		Cost:       5.00,
		Success:    true,
		Complexity: "high",
	}
	if err := AppendCostEntry(dir, entry); err != nil {
		t.Fatalf("AppendCostEntry: %v", err)
	}

	entries, err := ReadCostLedger(dir)
	if err != nil {
		t.Fatalf("ReadCostLedger: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Complexity != "high" {
		t.Errorf("Complexity = %q, want %q", entries[0].Complexity, "high")
	}
}

func TestCostEntryComplexityOmitEmpty(t *testing.T) {
	dir := t.TempDir()

	entry := CostEntry{
		Ticket:    "T-1",
		Timestamp: time.Now().Truncate(time.Second),
		Cost:      5.00,
		Success:   true,
	}
	if err := AppendCostEntry(dir, entry); err != nil {
		t.Fatalf("AppendCostEntry: %v", err)
	}

	data, err := os.ReadFile(CostLedgerPath(dir))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "complexity") {
		t.Errorf("cost.json should omit complexity when empty, got:\n%s", data)
	}
}
