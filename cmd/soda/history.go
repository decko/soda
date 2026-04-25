package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history <ticket>",
		Short: "Show phase details for a ticket",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			detail, _ := cmd.Flags().GetBool("detail")
			phase, _ := cmd.Flags().GetString("phase")
			return runHistory(cfg.StateDir, args[0], detail, phase)
		},
	}

	cmd.Flags().Bool("detail", false, "show full structured output for all phases")
	cmd.Flags().String("phase", "", "filter to a specific phase and show full details")

	return cmd
}

func runHistory(stateDir, ticketKey string, detail bool, phaseFilter string) error {
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
		return renderEventsHistory(meta, events, ticketDir, detail, phaseFilter)
	}

	// Fallback: meta-only rendering for old state dirs without events.jsonl.
	return renderMetaHistory(meta)
}

// renderEventsHistory renders the full multi-generation history table
// reconstructed from events.jsonl. When detail is true, the full structured
// output JSON is printed after each phase row. When phaseFilter is non-empty,
// only entries for that phase are shown with their full output.
func renderEventsHistory(meta *pipeline.PipelineMeta, events []pipeline.Event, stateDir string, detail bool, phaseFilter string) error {
	h := pipeline.BuildHistory(events, stateDir)

	// Populate PromptHash and EstimatedPromptTokens on non-superseded entries
	// only. The PhaseState in meta stores the values for the latest generation;
	// applying them to superseded entries would show misleading data that
	// doesn't match the prompt actually sent for that generation.
	for i := range h.Entries {
		if h.Entries[i].Superseded {
			continue
		}
		if ps, ok := meta.Phases[h.Entries[i].Phase]; ok {
			h.Entries[i].PromptHash = ps.PromptHash
			h.Entries[i].EstimatedPromptTokens = ps.EstimatedPromptTokens
		}
	}

	// When --detail or --phase is used, load full outputs.
	if detail || phaseFilter != "" {
		h.LoadFullOutputs(stateDir, phaseFilter)
	}

	// Filter entries when --phase is specified.
	entries := h.Entries
	if phaseFilter != "" {
		var filtered []pipeline.PhaseGeneration
		for _, e := range entries {
			if e.Phase == phaseFilter {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
		if len(entries) == 0 {
			return fmt.Errorf("history: no entries found for phase %q", phaseFilter)
		}
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Phase\tGen\tStatus\tDuration\tCost\tDetails")

	var lastFailPhase string
	var lastFailError string

	// Collect full outputs and prompt hashes to print after the table (to avoid
	// breaking tabwriter alignment).
	type outputBlock struct {
		phase                 string
		generation            int
		promptHash            string
		estimatedPromptTokens int64
		data                  json.RawMessage
	}
	var outputs []outputBlock

	for _, entry := range entries {
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

		details := formatDetails(entry.Details, entry.Error, entry.Superseded)

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.Phase, gen, sym, dur, cost, details)

		if (detail || phaseFilter != "") && (len(entry.FullOutput) > 0 || entry.PromptHash != "" || entry.EstimatedPromptTokens > 0) {
			outputs = append(outputs, outputBlock{
				phase:                 entry.Phase,
				generation:            entry.Generation,
				promptHash:            entry.PromptHash,
				estimatedPromptTokens: entry.EstimatedPromptTokens,
				data:                  entry.FullOutput,
			})
		}

		if entry.Status == pipeline.PhaseFailed && entry.Error != "" && !entry.Superseded {
			lastFailPhase = entry.Phase
			lastFailError = entry.Error
		}
	}

	// Total line using meta.TotalCost as the authoritative total.
	if phaseFilter == "" {
		fmt.Fprintf(tw, "\t\t\t\t──────────\t\n")
		fmt.Fprintf(tw, "\t\t\tTotal:\t$%.2f\t\n", meta.TotalCost)
		if h.SupersededCost > 0 {
			fmt.Fprintf(tw, "\t\t\tSuperseded:\t$%.2f\t\n", h.SupersededCost)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("history: flush output: %w", err)
	}

	if lastFailPhase != "" {
		fmt.Printf("\nLast failure (%s):\n  Error: %s\n", lastFailPhase, lastFailError)
	}

	// Print full structured outputs and prompt hashes after the table.
	for _, ob := range outputs {
		fmt.Printf("\n--- %s (gen %d) ---\n", ob.phase, ob.generation)
		if ob.promptHash != "" {
			fmt.Printf("Prompt Hash: %s\n", ob.promptHash)
		}
		if ob.estimatedPromptTokens > 0 {
			fmt.Printf("Estimated Prompt Tokens: %d\n", ob.estimatedPromptTokens)
		}
		if len(ob.data) > 0 {
			fmt.Println(prettyJSON(ob.data))
		}
	}

	return nil
}

// prettyJSON formats raw JSON with indentation. Falls back to the raw
// content if the data cannot be parsed.
func prettyJSON(data json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return string(data)
	}
	return buf.String()
}

// renderMetaHistory renders a simple history table from meta.json only.
// Used as a fallback when events.jsonl does not exist.
func renderMetaHistory(meta *pipeline.PipelineMeta) error {
	phasesPath, cleanup, err := resolvePhasesPath(meta.Pipeline, "")
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

// formatDetails builds the details column text for a history entry.
// When both outcome details and an error are present, they are joined
// with " — " so the reader sees both at a glance.
func formatDetails(details, errMsg string, superseded bool) string {
	if superseded {
		return "(superseded)"
	}
	if errMsg != "" {
		errText := errMsg
		if len(errText) > 60 {
			errText = errText[:57] + "..."
		}
		if details != "" {
			return details + " — " + errText
		}
		return errText
	}
	return details
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
