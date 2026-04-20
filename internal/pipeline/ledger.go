package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// CostEntry records the cost of a single pipeline run in the persistent ledger.
type CostEntry struct {
	Ticket    string    `json:"ticket"`
	Timestamp time.Time `json:"timestamp"`
	Cost      float64   `json:"cost"`
	Success   bool      `json:"success"`
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
