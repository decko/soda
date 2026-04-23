package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/decko/soda/internal/pipeline"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// ANSI escape codes for status column colors (matching internal/progress).
const (
	statusColorReset  = "\033[0m"
	statusColorGreen  = "\033[32m"
	statusColorRed    = "\033[31m"
	statusColorYellow = "\033[33m"
	statusColorDim    = "\033[2m"
)

// pipelineEntry holds collected data for a single pipeline row.
type pipelineEntry struct {
	ticket    string
	phase     string
	status    string
	elapsed   string
	cost      string
	submitted string
	startedAt time.Time
	rework    int
	costTrend string
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show active and recent pipelines",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			return runStatus(cfg.StateDir)
		},
	}
}

func runStatus(stateDir string) error {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("No pipelines found.")
			return nil
		}
		return fmt.Errorf("status: read state dir: %w", err)
	}

	// Cache loaded pipeline configs by name to avoid redundant I/O.
	type pipelineCache struct {
		pl      *pipeline.PhasePipeline
		cleanup func()
	}
	plCache := map[string]*pipelineCache{}
	defer func() {
		for _, c := range plCache {
			if c.cleanup != nil {
				c.cleanup()
			}
		}
	}()
	loadPipelineFor := func(name string) (*pipeline.PhasePipeline, error) {
		if cached, ok := plCache[name]; ok {
			return cached.pl, nil
		}
		phasesPath, cleanup, err := resolvePhasesPath(name, "")
		if err != nil {
			return nil, err
		}
		pl, err := pipeline.LoadPipeline(phasesPath)
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			return nil, err
		}
		plCache[name] = &pipelineCache{pl: pl, cleanup: cleanup}
		return pl, nil
	}

	// Read the cost ledger once and compute per-ticket cost trends.
	costEntries, costErr := pipeline.ReadCostLedger(stateDir)
	if costErr != nil {
		return fmt.Errorf("status: read cost ledger: %w", costErr)
	}
	trendMap := pipeline.CostTrendByTicket(costEntries)

	// Collect pipeline entries.
	var rows []pipelineEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ticketDir := filepath.Join(stateDir, entry.Name())
		metaPath := filepath.Join(ticketDir, "meta.json")

		meta, metaErr := pipeline.ReadMeta(metaPath)
		if metaErr != nil {
			continue
		}

		// Use the pipeline name stored in meta to load the correct phase config.
		pl, plErr := loadPipelineFor(meta.Pipeline)
		if plErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not load pipeline %q for %s: %v\n", meta.Pipeline, meta.Ticket, plErr)
			continue
		}

		phase, _ := currentPhaseStatus(meta, pl.Phases)
		lockPath := filepath.Join(ticketDir, "lock")
		lockInfo, _ := pipeline.ReadLockInfo(lockPath)
		status := pipelineStatus(meta, lockInfo)

		elapsed := formatElapsed(meta)
		cost := fmt.Sprintf("$%.2f", meta.TotalCost)

		trend, ok := trendMap[meta.Ticket]
		if !ok {
			trend = "─"
		}

		rows = append(rows, pipelineEntry{
			ticket:    meta.Ticket,
			phase:     phase,
			status:    status,
			elapsed:   elapsed,
			cost:      cost,
			submitted: formatSubmitted(meta.StartedAt, time.Now()),
			startedAt: meta.StartedAt,
			rework:    meta.ReworkCycles,
			costTrend: trend,
		})
	}

	if len(rows) == 0 {
		fmt.Println("No pipelines found.")
		return nil
	}

	// Sort: running/stale first (group 0), then completed/failed (group 1);
	// within each group, most recently started first.
	sortEntries(rows)

	// Render collected entries.
	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', tabwriter.StripEscape)
	fmt.Fprintln(tw, "TICKET\tPHASE\tSTATUS\tSUBMITTED\tELAPSED\tCOST\tREWORK\tTREND")
	for _, r := range rows {
		status := colorizeStatus(r.status, isTTY)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", r.ticket, r.phase, status, r.submitted, r.elapsed, r.cost, r.rework, r.costTrend)
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("status: flush output: %w", err)
	}

	// Cumulative cost footer.
	cumulativeCost, costErr := pipeline.CumulativeCost(stateDir)
	if costErr != nil {
		return fmt.Errorf("status: compute cumulative cost: %w", costErr)
	}
	fmt.Println()
	fmt.Printf("Total cost across all sessions: $%.2f\n", cumulativeCost)

	return nil
}

// currentPhaseStatus returns the most advanced phase name and its status string.
// Uses pipeline phase order for deterministic results when ranks are tied.
func currentPhaseStatus(meta *pipeline.PipelineMeta, phases []pipeline.PhaseConfig) (string, string) {
	latestPhase := ""
	latestRank := -1
	latestStatus := ""
	for _, phase := range phases {
		ps, ok := meta.Phases[phase.Name]
		if !ok {
			continue
		}
		rank := phaseRank(ps.Status)
		if rank > latestRank {
			latestPhase = phase.Name
			latestRank = rank
			latestStatus = string(ps.Status)
		}
	}
	if latestPhase == "" {
		return "-", "pending"
	}
	return latestPhase, latestStatus
}

// pipelineStatus computes a pipeline-level status from lock info and phase state.
// Phase states take priority over lock state: a pipeline with all phases
// completed/failed is terminal even if a stale lock file remains on disk.
func pipelineStatus(meta *pipeline.PipelineMeta, lockInfo *pipeline.LockInfo) string {
	// Derive terminal status from phase states first.
	hasFailed := false
	hasNonCompleted := false
	hasAny := false
	for _, ps := range meta.Phases {
		hasAny = true
		if ps.Status == pipeline.PhaseFailed {
			hasFailed = true
		}
		if ps.Status != pipeline.PhaseCompleted && ps.Status != pipeline.PhaseSkipped {
			hasNonCompleted = true
		}
	}
	if hasFailed {
		return "failed"
	}
	if hasAny && !hasNonCompleted {
		return "completed"
	}

	// Not terminal — check lock state for active/stale.
	if lockInfo != nil {
		if lockInfo.IsAlive {
			return "running"
		}
		return "stale"
	}

	// Fallback: use the most advanced phase's status.
	status := ""
	bestRank := -1
	for _, ps := range meta.Phases {
		r := phaseRank(ps.Status)
		if r > bestRank {
			bestRank = r
			status = string(ps.Status)
		}
	}
	if status == "" {
		return "pending"
	}
	return status
}

// sortEntries sorts pipeline entries: running/stale pipelines first, then
// completed/failed. Within each group, entries are sorted by StartedAt
// descending (newest first).
func sortEntries(rows []pipelineEntry) {
	sort.SliceStable(rows, func(i, j int) bool {
		gi, gj := statusGroup(rows[i].status), statusGroup(rows[j].status)
		if gi != gj {
			return gi < gj
		}
		return rows[i].startedAt.After(rows[j].startedAt)
	})
}

// statusGroup returns 0 for active/in-progress pipelines (shown first)
// and 1 for terminal states (shown after).
func statusGroup(status string) int {
	switch status {
	case "running", "stale", "retrying", "pending":
		return 0
	default:
		return 1
	}
}

// colorizeStatus wraps the status string in ANSI color codes when isTTY is true.
// ANSI escape sequences are wrapped in \xff delimiters so that tabwriter
// (with StripEscape) ignores them for column width calculation.
func colorizeStatus(status string, isTTY bool) string {
	if !isTTY {
		return status
	}
	var color string
	switch status {
	case "running", "completed":
		color = statusColorGreen
	case "failed":
		color = statusColorRed
	case "stale", "retrying":
		color = statusColorYellow
	default:
		color = statusColorDim
	}
	return "\xff" + color + "\xff" + status + "\xff" + statusColorReset + "\xff"
}

func phaseRank(status pipeline.PhaseStatus) int {
	switch status {
	case pipeline.PhaseRunning:
		return 4
	case pipeline.PhaseFailed:
		return 3
	case pipeline.PhaseRetrying:
		return 2
	case pipeline.PhaseCompleted:
		return 1
	default:
		return 0
	}
}

// formatSubmitted returns a human-friendly timestamp: time-only for today,
// date+time for older entries.
func formatSubmitted(startedAt, now time.Time) string {
	sy, sm, sd := startedAt.Date()
	ny, nm, nd := now.Date()
	if sy == ny && sm == nm && sd == nd {
		return startedAt.Format("15:04")
	}
	return startedAt.Format("Jan 02 15:04")
}

func formatElapsed(meta *pipeline.PipelineMeta) string {
	var totalMs int64
	hasRunning := false
	for _, ps := range meta.Phases {
		if ps.Status == pipeline.PhaseRunning {
			hasRunning = true
		}
		totalMs += ps.DurationMs
	}
	if hasRunning {
		return time.Since(meta.StartedAt).Truncate(time.Second).String()
	}
	return (time.Duration(totalMs) * time.Millisecond).Truncate(time.Second).String()
}
