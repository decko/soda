package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// CostEntry records the cost of a single pipeline run in the persistent ledger.
type CostEntry struct {
	Ticket       string    `json:"ticket"`
	Timestamp    time.Time `json:"timestamp"`
	Cost         float64   `json:"cost"`
	Success      bool      `json:"success"`
	Complexity   string    `json:"complexity,omitempty"`
	ReworkCycles int       `json:"rework_cycles,omitempty"`
	PatchCycles  int       `json:"patch_cycles,omitempty"`
	Escalated    bool      `json:"escalated,omitempty"`
	DurationMs   int64     `json:"duration_ms,omitempty"`
}

// costLedgerFile is the filename of the cost ledger within the state directory.
const costLedgerFile = "cost.json"

// CostLedgerPath returns the path to the cost ledger file within stateDir.
// The ledger lives at the stateDir root (not inside a session subdirectory)
// so it persists across soda clean operations.
func CostLedgerPath(stateDir string) string {
	return filepath.Join(stateDir, costLedgerFile)
}

// ReadCostLedger reads all cost entries from the ledger at stateDir/cost.json.
// Returns an empty slice if the file does not exist.
func ReadCostLedger(stateDir string) ([]CostEntry, error) {
	path := CostLedgerPath(stateDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []CostEntry{}, nil
		}
		return nil, fmt.Errorf("pipeline: read cost ledger %s: %w", path, err)
	}
	var entries []CostEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("pipeline: parse cost ledger %s: %w", path, err)
	}
	return entries, nil
}

// CostTrendByTicket computes a per-ticket cost trend indicator from ledger
// entries. For each ticket with at least two entries, the latest cost is
// compared against the average of all prior entries:
//   - "▲" if the latest cost is more than 10% above the prior average
//   - "▼" if the latest cost is more than 10% below the prior average
//   - "─" otherwise (stable within ±10%)
//
// Tickets with fewer than two entries receive "─".
func CostTrendByTicket(entries []CostEntry) map[string]string {
	// Group entries by ticket, preserving insertion order (which matches
	// chronological order in the append-only ledger).
	type ticketEntries struct {
		costs []float64
	}
	byTicket := make(map[string]*ticketEntries)
	for _, entry := range entries {
		te, ok := byTicket[entry.Ticket]
		if !ok {
			te = &ticketEntries{}
			byTicket[entry.Ticket] = te
		}
		te.costs = append(te.costs, entry.Cost)
	}

	trends := make(map[string]string, len(byTicket))
	for ticket, te := range byTicket {
		if len(te.costs) < 2 {
			trends[ticket] = "─"
			continue
		}
		latest := te.costs[len(te.costs)-1]
		var priorSum float64
		for _, cost := range te.costs[:len(te.costs)-1] {
			priorSum += cost
		}
		priorAvg := priorSum / float64(len(te.costs)-1)

		if priorAvg == 0 {
			trends[ticket] = "─"
			continue
		}

		ratio := latest / priorAvg
		switch {
		case ratio > 1.10:
			trends[ticket] = "▲"
		case ratio < 0.90:
			trends[ticket] = "▼"
		default:
			trends[ticket] = "─"
		}
	}
	return trends
}

// AppendCostEntry appends a cost entry to the persistent ledger at stateDir/cost.json.
// The ledger file is created if it does not exist. The write is atomic.
// An exclusive file lock (flock) protects the read-modify-write sequence so that
// concurrent pipeline runs (e.g. two terminals running `soda run`) do not race.
func AppendCostEntry(stateDir string, entry CostEntry) error {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("pipeline: create state dir for cost ledger: %w", err)
	}

	// Acquire an exclusive flock on a dedicated lock file to serialize
	// concurrent read-modify-write operations on cost.json.
	lockPath := CostLedgerPath(stateDir) + ".lock"
	lockFd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("pipeline: open cost ledger lock %s: %w", lockPath, err)
	}
	defer lockFd.Close()
	if err := syscall.Flock(int(lockFd.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("pipeline: acquire cost ledger lock %s: %w", lockPath, err)
	}
	defer syscall.Flock(int(lockFd.Fd()), syscall.LOCK_UN)

	entries, err := ReadCostLedger(stateDir)
	if err != nil {
		return fmt.Errorf("pipeline: read cost ledger before append: %w", err)
	}
	entries = append(entries, entry)
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("pipeline: marshal cost ledger: %w", err)
	}
	data = append(data, '\n')
	return atomicWrite(CostLedgerPath(stateDir), data)
}

// ComplexityStats holds aggregated cost statistics for a single complexity band.
type ComplexityStats struct {
	Sessions int
	Mean     float64
	Median   float64
	Total    float64
}

// CostByComplexity groups cost entries by their complexity band and returns
// aggregated statistics per band. Entries with an empty complexity value are
// grouped under the key "unknown".
func CostByComplexity(entries []CostEntry) map[string]ComplexityStats {
	grouped := make(map[string][]float64)
	for _, entry := range entries {
		band := entry.Complexity
		if band == "" {
			band = "unknown"
		}
		grouped[band] = append(grouped[band], entry.Cost)
	}

	result := make(map[string]ComplexityStats, len(grouped))
	for band, costs := range grouped {
		var total float64
		for _, cost := range costs {
			total += cost
		}
		mean := total / float64(len(costs))

		sorted := make([]float64, len(costs))
		copy(sorted, costs)
		sort.Float64s(sorted)

		var median float64
		n := len(sorted)
		if n%2 == 0 {
			median = (sorted[n/2-1] + sorted[n/2]) / 2.0
		} else {
			median = sorted[n/2]
		}

		result[band] = ComplexityStats{
			Sessions: len(costs),
			Mean:     mean,
			Median:   median,
			Total:    total,
		}
	}
	return result
}

// OutcomeStats holds aggregated cost and duration statistics for a single
// pipeline outcome bucket (e.g. "first_pass", "patched", "rework_1").
type OutcomeStats struct {
	Sessions  int
	Mean      float64
	Median    float64
	Total     float64
	MeanDurMs int64
}

// ClassifyOutcome assigns a pipeline outcome label using a 5-level precedence:
//
//  1. !Success              → "failed"
//  2. ReworkCycles ≥ 2      → "rework_2+"
//  3. ReworkCycles == 1 OR Escalated with ReworkCycles == 0 → "rework_1"
//  4. PatchCycles > 0       → "patched"
//  5. otherwise             → "first_pass"
//
// Escalation with zero rework cycles is treated as a minimum of one rework
// cycle because the patch-to-rework escalation itself constitutes a rework.
func ClassifyOutcome(entry CostEntry) string {
	if !entry.Success {
		return "failed"
	}
	rework := entry.ReworkCycles
	if entry.Escalated && rework == 0 {
		rework = 1
	}
	switch {
	case rework >= 2:
		return "rework_2+"
	case rework == 1:
		return "rework_1"
	case entry.PatchCycles > 0:
		return "patched"
	default:
		return "first_pass"
	}
}

// CostByOutcome groups cost entries by their classified outcome and returns
// aggregated statistics per outcome bucket. The median uses "lower-median"
// semantics: sorted[n/2-1] for even n, sorted[n/2] for odd n. This is
// intentionally different from CostByComplexity's interpolated median.
func CostByOutcome(entries []CostEntry) map[string]OutcomeStats {
	type bucket struct {
		costs       []float64
		durationsMs []int64
	}
	grouped := make(map[string]*bucket)
	for _, entry := range entries {
		outcome := ClassifyOutcome(entry)
		bkt, ok := grouped[outcome]
		if !ok {
			bkt = &bucket{}
			grouped[outcome] = bkt
		}
		bkt.costs = append(bkt.costs, entry.Cost)
		bkt.durationsMs = append(bkt.durationsMs, entry.DurationMs)
	}

	result := make(map[string]OutcomeStats, len(grouped))
	for outcome, bkt := range grouped {
		var totalCost float64
		for _, cost := range bkt.costs {
			totalCost += cost
		}
		mean := totalCost / float64(len(bkt.costs))

		sorted := make([]float64, len(bkt.costs))
		copy(sorted, bkt.costs)
		sort.Float64s(sorted)

		// Lower-median: sorted[n/2-1] for even n, sorted[n/2] for odd n.
		var median float64
		n := len(sorted)
		if n%2 == 0 {
			median = sorted[n/2-1]
		} else {
			median = sorted[n/2]
		}

		var totalDurMs int64
		for _, dur := range bkt.durationsMs {
			totalDurMs += dur
		}
		meanDurMs := totalDurMs / int64(len(bkt.durationsMs))

		result[outcome] = OutcomeStats{
			Sessions:  len(bkt.costs),
			Mean:      mean,
			Median:    median,
			Total:     totalCost,
			MeanDurMs: meanDurMs,
		}
	}
	return result
}
