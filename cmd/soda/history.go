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
	ticketDir := filepath.Join(stateDir, ticketKey)
	metaPath := filepath.Join(ticketDir, "meta.json")
	meta, err := pipeline.ReadMeta(metaPath)
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

	// Try to read events for rich multi-generation history.
	events, eventsErr := pipeline.ReadEvents(ticketDir)
	if eventsErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read events: %v\n", eventsErr)
	}
	if len(events) > 0 {
		return renderEventsHistory(meta, events, ticketDir)
	}

	// Fallback: meta-only rendering for old state dirs without events.jsonl.
	return renderMetaHistory(meta)
}

// renderEventsHistory renders the full multi-generation history table
// reconstructed from events.jsonl.
func renderEventsHistory(meta *pipeline.PipelineMeta, events []pipeline.Event, stateDir string) error {
	h := pipeline.BuildHistory(events, stateDir)

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Phase\tGen\tStatus\tDuration\tCost\tDetails")

	var lastFailPhase string
	var lastFailError string

	for _, entry := range h.Entries {
		sym := statusSymbol(entry.Status, entry.Superseded)

		gen := fmt.Sprintf("%d", entry.Generation)
		if entry.Generation == 0 {
			gen = "-"
		}

		dur := "-"
		if entry.DurationMs > 0 {
			dur = pipeline.FormatDuration(entry.DurationMs)
		}

		cost := "-"
		if entry.Cost > 0 || entry.Status == pipeline.PhaseCompleted || entry.Status == pipeline.PhaseFailed {
			cost = fmt.Sprintf("$%.2f", entry.Cost)
		}

		details := entry.Details
		if entry.Superseded {
			details = "(superseded)"
		}
		if entry.Error != "" && !entry.Superseded {
			details = entry.Error
			if len(details) > 60 {
				details = details[:57] + "..."
			}
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.Phase, gen, sym, dur, cost, details)

		if entry.Status == pipeline.PhaseFailed && entry.Error != "" && !entry.Superseded {
			lastFailPhase = entry.Phase
			lastFailError = entry.Error
		}
	}

	// Total line using meta.TotalCost as the authoritative total.
	fmt.Fprintf(tw, "\t\t\t\t──────────\t\n")
	fmt.Fprintf(tw, "\t\t\tTotal:\t$%.2f\t\n", meta.TotalCost)
	if h.SupersededCost > 0 {
		fmt.Fprintf(tw, "\t\t\tSuperseded:\t$%.2f\t\n", h.SupersededCost)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("history: flush output: %w", err)
	}

	if lastFailPhase != "" {
		fmt.Printf("\nLast failure (%s):\n  Error: %s\n", lastFailPhase, lastFailError)
	}

	return nil
}

// renderMetaHistory renders a simple history table from meta.json only.
// Used as a fallback when events.jsonl does not exist.
func renderMetaHistory(meta *pipeline.PipelineMeta) error {
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

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Phase\tStatus\tDuration\tCost")

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

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", phase.Name, string(ps.Status), dur, cost)

		if ps.Status == pipeline.PhaseFailed && ps.Error != "" {
			lastFailPhase = phase.Name
			lastFailError = ps.Error
		}
	}

	// Use meta.TotalCost as the authoritative total.
	fmt.Fprintf(tw, "\t\t\t──────────\n")
	fmt.Fprintf(tw, "\t\tTotal:\t$%.2f\n", meta.TotalCost)
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("history: flush output: %w", err)
	}

	if lastFailPhase != "" {
		fmt.Printf("\nLast failure (%s):\n  Error: %s\n", lastFailPhase, lastFailError)
	}

	return nil
}

// statusSymbol returns a status symbol for display.
func statusSymbol(status pipeline.PhaseStatus, superseded bool) string {
	if superseded {
		switch status {
		case pipeline.PhaseCompleted:
			return "✓ ⏭"
		case pipeline.PhaseFailed:
			return "✗ ⏭"
		default:
			return "⏭"
		}
	}
	switch status {
	case pipeline.PhaseCompleted:
		return "✓"
	case pipeline.PhaseFailed:
		return "✗"
	case pipeline.PhaseRunning:
		return "⧗"
	case pipeline.PhaseSkipped:
		return "⏭"
	default:
		return string(status)
	}
}
