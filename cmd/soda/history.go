package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history <ticket>",
		Short: "Show phase details for a ticket",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			return runHistory(cfg.StateDir, args[0])
		},
	}
}

func runHistory(stateDir, ticketKey string) error {
	metaPath := filepath.Join(stateDir, ticketKey, "meta.json")
	meta, err := pipeline.ReadMeta(metaPath)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}

	phasesPath, cleanup, err := resolvePhasesPath()
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	pl, err := pipeline.LoadPipeline(phasesPath)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}

	// Header
	fmt.Printf("%s", meta.Ticket)
	if meta.Branch != "" {
		fmt.Printf("\nBranch: %s", meta.Branch)
	}
	if meta.Worktree != "" {
		fmt.Printf("\nWorktree: %s", meta.Worktree)
	}
	fmt.Println()
	fmt.Println()

	// Phase table
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Phase\tStatus\tDuration\tCost")

	var totalCost float64
	var lastFailPhase string
	var lastFailError string

	for _, phase := range pl.Phases {
		ps, ok := meta.Phases[phase.Name]
		if !ok {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", phase.Name, "pending", "-", "-")
			continue
		}

		dur := (time.Duration(ps.DurationMs) * time.Millisecond).Truncate(time.Second).String()
		if ps.DurationMs == 0 {
			dur = "-"
		}
		cost := fmt.Sprintf("$%.2f", ps.Cost)
		totalCost += ps.Cost

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", phase.Name, string(ps.Status), dur, cost)

		if ps.Status == pipeline.PhaseFailed && ps.Error != "" {
			lastFailPhase = phase.Name
			lastFailError = ps.Error
		}
	}

	fmt.Fprintf(tw, "\t\t\t──────────\n")
	fmt.Fprintf(tw, "\t\tTotal:\t$%.2f\n", totalCost)
	tw.Flush()

	if lastFailPhase != "" {
		fmt.Printf("\nLast failure (%s):\n  Error: %s\n", lastFailPhase, lastFailError)
	}

	return nil
}
