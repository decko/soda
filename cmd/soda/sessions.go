package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/tui"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// sessionEntry holds data for one row in the sessions listing.
type sessionEntry struct {
	ticket    string
	summary   string
	status    string
	cost      string
	elapsed   string
	lastRun   string
	startedAt time.Time
}

func newSessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List previous and active pipeline sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			statusFilter, _ := cmd.Flags().GetString("status")
			sortBy, _ := cmd.Flags().GetString("sort")
			all, _ := cmd.Flags().GetBool("all")
			interactive, _ := cmd.Flags().GetBool("tui")
			limit := 20
			if all {
				limit = 0
			}
			if interactive {
				return runSessionsTUI(cfg.StateDir, statusFilter, sortBy, limit, time.Now())
			}
			return runSessions(cfg.StateDir, statusFilter, sortBy, limit, time.Now())
		},
	}

	cmd.Flags().String("status", "", "filter by status (completed, failed, running, stale)")
	cmd.Flags().String("sort", "date", "sort order: date, cost, elapsed")
	cmd.Flags().Bool("all", false, "list all sessions (default: most recent 20)")
	cmd.Flags().Bool("tui", false, "launch interactive session browser")

	return cmd
}

func runSessions(stateDir, statusFilter, sortBy string, limit int, now time.Time) error {
	rows, err := collectSessions(stateDir, now)
	if err != nil {
		return err
	}

	if len(rows) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	// Filter by status.
	if statusFilter != "" {
		rows = filterSessionsByStatus(rows, statusFilter)
		if len(rows) == 0 {
			fmt.Printf("No sessions with status %q.\n", statusFilter)
			return nil
		}
	}

	// Sort.
	sortSessions(rows, sortBy)

	// Apply limit.
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}

	// Render.
	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TICKET\tSUMMARY\tSTATUS\tCOST\tELAPSED\tLAST RUN")
	for _, row := range rows {
		status := colorizeStatus(row.status, isTTY)
		summary := truncate(row.summary, 40)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.ticket, summary, status, row.cost, row.elapsed, row.lastRun)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("sessions: flush output: %w", err)
	}

	// Summary footer.
	fmt.Println()
	fmt.Println(sessionsSummaryLine(rows))

	return nil
}

// collectSessions reads all meta.json files from stateDir and builds session entries.
func collectSessions(stateDir string, now time.Time) ([]sessionEntry, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sessions: read state dir: %w", err)
	}

	var rows []sessionEntry
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

		lockPath := filepath.Join(ticketDir, "lock")
		lockInfo, _ := pipeline.ReadLockInfo(lockPath)
		status := pipelineStatus(meta, lockInfo)

		elapsed := formatElapsed(meta)
		cost := fmt.Sprintf("$%.2f", meta.TotalCost)
		lastRun := formatLastRun(meta.StartedAt, now)

		rows = append(rows, sessionEntry{
			ticket:    meta.Ticket,
			summary:   meta.Summary,
			status:    status,
			cost:      cost,
			elapsed:   elapsed,
			lastRun:   lastRun,
			startedAt: meta.StartedAt,
		})
	}

	return rows, nil
}

// filterSessionsByStatus returns only rows matching the given status.
func filterSessionsByStatus(rows []sessionEntry, status string) []sessionEntry {
	var filtered []sessionEntry
	for _, row := range rows {
		if row.status == status {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// sortSessions sorts session entries by the specified field.
func sortSessions(rows []sessionEntry, sortBy string) {
	switch sortBy {
	case "cost":
		sort.SliceStable(rows, func(i, j int) bool {
			// Parse cost strings for comparison.
			ci := parseCost(rows[i].cost)
			cj := parseCost(rows[j].cost)
			return ci > cj // highest cost first
		})
	case "elapsed":
		sort.SliceStable(rows, func(i, j int) bool {
			// Use startedAt as a proxy — sessions that started earlier
			// have been running longer.
			return rows[i].startedAt.Before(rows[j].startedAt)
		})
	default: // "date" or unrecognized
		sort.SliceStable(rows, func(i, j int) bool {
			return rows[i].startedAt.After(rows[j].startedAt) // newest first
		})
	}
}

// parseCost extracts a float from a "$X.XX" string.
func parseCost(s string) float64 {
	var cost float64
	fmt.Sscanf(s, "$%f", &cost)
	return cost
}

// formatLastRun returns a human-friendly relative time string.
func formatLastRun(startedAt, now time.Time) string {
	diff := now.Sub(startedAt)
	if diff < 0 {
		return "now"
	}
	if diff < time.Minute {
		return "now"
	}
	if diff < time.Hour {
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	}
	if diff < 24*time.Hour {
		hours := int(diff.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	}
	days := int(diff.Hours() / 24)
	if days == 1 {
		return "1d ago"
	}
	return fmt.Sprintf("%dd ago", days)
}

// truncate shortens a string to maxLen, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// runSessionsTUI launches the interactive TUI session browser. When the user
// selects a session, it drills into the history for that ticket.
func runSessionsTUI(stateDir, statusFilter, sortBy string, limit int, now time.Time) error {
	rows, err := collectSessions(stateDir, now)
	if err != nil {
		return err
	}

	if statusFilter != "" {
		rows = filterSessionsByStatus(rows, statusFilter)
	}
	sortSessions(rows, sortBy)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}

	// Convert to TUI session infos.
	sessions := sessionsToTUI(rows)

	model := tui.NewSessionsModel(sessions)
	program := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return fmt.Errorf("sessions tui: %w", err)
	}

	result := finalModel.(tui.SessionsModel).Result()
	if result == nil {
		return nil
	}

	switch result.Action {
	case tui.SessionActionView:
		// Drill into session history.
		return runHistory(stateDir, result.Ticket, false, "")
	case tui.SessionActionResume:
		fmt.Printf("To resume: soda run %s --from last\n", result.Ticket)
	case tui.SessionActionDelete:
		fmt.Printf("To clean: soda clean %s\n", result.Ticket)
	}

	return nil
}

// sessionsToTUI converts sessionEntry rows to tui.SessionInfo for the TUI model.
func sessionsToTUI(rows []sessionEntry) []tui.SessionInfo {
	sessions := make([]tui.SessionInfo, len(rows))
	for idx, row := range rows {
		sessions[idx] = tui.SessionInfo{
			Ticket:    row.ticket,
			Summary:   row.summary,
			Status:    row.status,
			Cost:      parseCost(row.cost),
			Elapsed:   row.elapsed,
			StartedAt: row.startedAt,
		}
	}
	return sessions
}

// sessionsSummaryLine returns a summary like "4 sessions (3 completed, 1 running)".
func sessionsSummaryLine(rows []sessionEntry) string {
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.status]++
	}

	total := len(rows)
	var parts []string
	// Render in a deterministic order.
	for _, status := range []string{"running", "stale", "completed", "failed", "pending", "retrying"} {
		if count, ok := counts[status]; ok {
			parts = append(parts, fmt.Sprintf("%d %s", count, status))
		}
	}

	if len(parts) == 0 {
		return fmt.Sprintf("%d sessions", total)
	}

	noun := "sessions"
	if total == 1 {
		noun = "session"
	}
	return fmt.Sprintf("%d %s (%s)", total, noun, strings.Join(parts, ", "))
}
