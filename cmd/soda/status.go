package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

// pipelineEntry holds collected data for a single pipeline row.
type pipelineEntry struct {
	ticket    string
	phase     string
	status    string
	elapsed   string
	cost      string
	startedAt time.Time
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

	// Load pipeline config for deterministic phase ordering.
	phasesPath, cleanup, phasesErr := resolvePhasesPath()
	if phasesErr != nil {
		return fmt.Errorf("status: %w", phasesErr)
	}
	if cleanup != nil {
		defer cleanup()
	}
	pl, phasesErr := pipeline.LoadPipeline(phasesPath)
	if phasesErr != nil {
		return fmt.Errorf("status: %w", phasesErr)
	}

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

		phase, status := currentPhaseStatus(meta, pl.Phases)
		lockPath := filepath.Join(ticketDir, "lock")
		lockInfo, lockErr := pipeline.ReadLockInfo(lockPath)
		if lockErr == nil {
			if lockInfo.IsAlive {
				status = "running"
			} else {
				status = "stale"
			}
		}

		elapsed := formatElapsed(meta)
		cost := fmt.Sprintf("$%.2f", meta.TotalCost)

		rows = append(rows, pipelineEntry{
			ticket:    meta.Ticket,
			phase:     phase,
			status:    status,
			elapsed:   elapsed,
			cost:      cost,
			startedAt: meta.StartedAt,
		})
	}

	if len(rows) == 0 {
		fmt.Println("No pipelines found.")
		return nil
	}

	// Render collected entries.
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TICKET\tPHASE\tSTATUS\tELAPSED\tCOST")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ticket, r.phase, r.status, r.elapsed, r.cost)
	}

	return tw.Flush()
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
