package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newCostCmd() *cobra.Command {
	var byComplexity bool
	var byOutcomes bool

	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Show cost breakdown from the persistent cost ledger",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			entries, err := pipeline.ReadCostLedger(cfg.StateDir)
			if err != nil {
				return fmt.Errorf("cost: read ledger: %w", err)
			}
			if byOutcomes && byComplexity {
				return runCostByOutcomeAndComplexity(entries)
			}
			if byOutcomes {
				return runCostByOutcome(entries)
			}
			if byComplexity {
				return runCostByComplexity(entries)
			}
			return runCost(entries)
		},
	}

	cmd.Flags().BoolVar(&byComplexity, "by-complexity", false, "Show cost breakdown grouped by triage complexity band")
	cmd.Flags().BoolVar(&byOutcomes, "outcomes", false, "Show cost breakdown grouped by pipeline outcome")
	return cmd
}

func runCost(entries []pipeline.CostEntry) error {
	if len(entries) == 0 {
		fmt.Println("No cost entries found.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TICKET\tTIMESTAMP\tCOST\tSTATUS")

	var total float64
	for _, e := range entries {
		status := "success"
		if !e.Success {
			status = "failed"
		}
		fmt.Fprintf(tw, "%s\t%s\t$%.4f\t%s\n",
			e.Ticket,
			e.Timestamp.Format(time.RFC3339),
			e.Cost,
			status,
		)
		total += e.Cost
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("cost: flush output: %w", err)
	}

	fmt.Printf("\nTotal: $%.4f across %d run(s)\n", total, len(entries))
	return nil
}

// complexityBandOrder defines the canonical sort order for complexity bands.
// Bands not in this list are sorted alphabetically after the known ones.
var complexityBandOrder = map[string]int{
	"low":    0,
	"medium": 1,
	"high":   2,
}

// runCostByComplexity renders a cost breakdown grouped by triage complexity band.
// Uses manual column-width computation (not tabwriter) to support future ANSI coloring.
func runCostByComplexity(entries []pipeline.CostEntry) error {
	if len(entries) == 0 {
		fmt.Println("No cost entries found.")
		return nil
	}

	byBand := pipeline.CostByComplexity(entries)

	// Sort bands: low → medium → high → alphabetical rest (unknown last among unknowns).
	bands := make([]string, 0, len(byBand))
	for band := range byBand {
		bands = append(bands, band)
	}
	sort.Slice(bands, func(i, j int) bool {
		oi, oki := complexityBandOrder[bands[i]]
		oj, okj := complexityBandOrder[bands[j]]
		if oki && okj {
			return oi < oj
		}
		if oki {
			return true
		}
		if okj {
			return false
		}
		return bands[i] < bands[j]
	})

	// Build rows for column-width computation.
	type row struct {
		complexity string
		sessions   string
		mean       string
		median     string
		total      string
	}

	header := row{"COMPLEXITY", "SESSIONS", "MEAN", "MEDIAN", "TOTAL"}
	rows := make([]row, len(bands))
	var totalSessions int
	var totalCost float64
	for idx, band := range bands {
		stats := byBand[band]
		rows[idx] = row{
			complexity: strings.ToUpper(band),
			sessions:   fmt.Sprintf("%d", stats.Sessions),
			mean:       fmt.Sprintf("$%.2f", stats.Mean),
			median:     fmt.Sprintf("$%.2f", stats.Median),
			total:      fmt.Sprintf("$%.2f", stats.Total),
		}
		totalSessions += stats.Sessions
		totalCost += stats.Total
	}

	// Compute column widths from all rows including header.
	colW := [5]int{
		len(header.complexity),
		len(header.sessions),
		len(header.mean),
		len(header.median),
		len(header.total),
	}
	for _, r := range rows {
		if len(r.complexity) > colW[0] {
			colW[0] = len(r.complexity)
		}
		if len(r.sessions) > colW[1] {
			colW[1] = len(r.sessions)
		}
		if len(r.mean) > colW[2] {
			colW[2] = len(r.mean)
		}
		if len(r.median) > colW[3] {
			colW[3] = len(r.median)
		}
		if len(r.total) > colW[4] {
			colW[4] = len(r.total)
		}
	}

	// Also consider footer widths.
	footerSessions := fmt.Sprintf("%d", totalSessions)
	footerTotal := fmt.Sprintf("$%.2f", totalCost)
	if len("TOTAL") > colW[0] {
		colW[0] = len("TOTAL")
	}
	if len(footerSessions) > colW[1] {
		colW[1] = len(footerSessions)
	}
	if len(footerTotal) > colW[4] {
		colW[4] = len(footerTotal)
	}

	gap := 2
	fmtRow := func(r row) string {
		return fmt.Sprintf("%-*s%s%-*s%s%-*s%s%-*s%s%-*s",
			colW[0], r.complexity, strings.Repeat(" ", gap),
			colW[1], r.sessions, strings.Repeat(" ", gap),
			colW[2], r.mean, strings.Repeat(" ", gap),
			colW[3], r.median, strings.Repeat(" ", gap),
			colW[4], r.total,
		)
	}

	fmt.Println(fmtRow(header))
	for _, r := range rows {
		fmt.Println(fmtRow(r))
	}

	fmt.Printf("\nTotal: $%.2f across %d session(s)\n", totalCost, totalSessions)
	return nil
}

// outcomeOrder defines the canonical display order for pipeline outcome buckets.
var outcomeOrder = []string{"first_pass", "patched", "rework_1", "rework_2+", "failed"}

// runCostByOutcome renders a cost breakdown grouped by pipeline outcome.
func runCostByOutcome(entries []pipeline.CostEntry) error {
	if len(entries) == 0 {
		fmt.Println("No cost entries found.")
		return nil
	}

	byOutcome := pipeline.CostByOutcome(entries)

	// Build rows in canonical outcome order, skipping absent outcomes.
	type row struct {
		outcome  string
		sessions string
		mean     string
		median   string
		meanDur  string
		total    string
	}

	header := row{"OUTCOME", "SESSIONS", "MEAN", "MEDIAN", "MEAN DUR", "TOTAL"}
	var rows []row
	var totalSessions int
	var totalCost float64
	for _, outcome := range outcomeOrder {
		stats, ok := byOutcome[outcome]
		if !ok {
			continue
		}
		rows = append(rows, row{
			outcome:  strings.ToUpper(outcome),
			sessions: fmt.Sprintf("%d", stats.Sessions),
			mean:     fmt.Sprintf("$%.2f", stats.Mean),
			median:   fmt.Sprintf("$%.2f", stats.Median),
			meanDur:  formatDuration(stats.MeanDurMs),
			total:    fmt.Sprintf("$%.2f", stats.Total),
		})
		totalSessions += stats.Sessions
		totalCost += stats.Total
	}

	// Compute column widths from all rows including header.
	colW := [6]int{
		len(header.outcome),
		len(header.sessions),
		len(header.mean),
		len(header.median),
		len(header.meanDur),
		len(header.total),
	}
	for _, r := range rows {
		if len(r.outcome) > colW[0] {
			colW[0] = len(r.outcome)
		}
		if len(r.sessions) > colW[1] {
			colW[1] = len(r.sessions)
		}
		if len(r.mean) > colW[2] {
			colW[2] = len(r.mean)
		}
		if len(r.median) > colW[3] {
			colW[3] = len(r.median)
		}
		if len(r.meanDur) > colW[4] {
			colW[4] = len(r.meanDur)
		}
		if len(r.total) > colW[5] {
			colW[5] = len(r.total)
		}
	}

	// Consider footer widths.
	footerSessions := fmt.Sprintf("%d", totalSessions)
	footerTotal := fmt.Sprintf("$%.2f", totalCost)
	if len("TOTAL") > colW[0] {
		colW[0] = len("TOTAL")
	}
	if len(footerSessions) > colW[1] {
		colW[1] = len(footerSessions)
	}
	if len(footerTotal) > colW[5] {
		colW[5] = len(footerTotal)
	}

	gap := 2
	fmtRow := func(r row) string {
		return fmt.Sprintf("%-*s%s%-*s%s%-*s%s%-*s%s%-*s%s%-*s",
			colW[0], r.outcome, strings.Repeat(" ", gap),
			colW[1], r.sessions, strings.Repeat(" ", gap),
			colW[2], r.mean, strings.Repeat(" ", gap),
			colW[3], r.median, strings.Repeat(" ", gap),
			colW[4], r.meanDur, strings.Repeat(" ", gap),
			colW[5], r.total,
		)
	}

	fmt.Println(fmtRow(header))
	for _, r := range rows {
		fmt.Println(fmtRow(r))
	}

	fmt.Printf("\nTotal: $%.2f across %d session(s)\n", totalCost, totalSessions)

	// Rework-tax line: weighted mean of rework_1 + rework_2+ vs clean mean.
	cleanStats, hasClean := byOutcome["first_pass"]
	rework1Stats, hasR1 := byOutcome["rework_1"]
	rework2Stats, hasR2 := byOutcome["rework_2+"]
	if hasClean && (hasR1 || hasR2) {
		var reworkTotal float64
		var reworkSessions int
		if hasR1 {
			reworkTotal += rework1Stats.Total
			reworkSessions += rework1Stats.Sessions
		}
		if hasR2 {
			reworkTotal += rework2Stats.Total
			reworkSessions += rework2Stats.Sessions
		}
		reworkMean := reworkTotal / float64(reworkSessions)
		if cleanStats.Mean > 0 {
			tax := ((reworkMean - cleanStats.Mean) / cleanStats.Mean) * 100
			fmt.Printf("Rework tax: %.0f%% (rework mean $%.2f vs first_pass mean $%.2f)\n", tax, reworkMean, cleanStats.Mean)
		}
	}

	return nil
}

// runCostByOutcomeAndComplexity renders a 2D matrix of cost (mean) broken
// down by outcome (rows) and complexity (columns).
func runCostByOutcomeAndComplexity(entries []pipeline.CostEntry) error {
	if len(entries) == 0 {
		fmt.Println("No cost entries found.")
		return nil
	}

	// Group entries by (outcome, complexity).
	type cellKey struct {
		outcome    string
		complexity string
	}
	cells := make(map[cellKey][]float64)
	complexitySet := make(map[string]bool)
	outcomeSet := make(map[string]bool)
	for _, entry := range entries {
		outcome := pipeline.ClassifyOutcome(entry)
		complexity := entry.Complexity
		if complexity == "" {
			complexity = "unknown"
		}
		key := cellKey{outcome, complexity}
		cells[key] = append(cells[key], entry.Cost)
		complexitySet[complexity] = true
		outcomeSet[outcome] = true
	}

	// Sort complexity columns using the same band ordering as runCostByComplexity.
	complexities := make([]string, 0, len(complexitySet))
	for comp := range complexitySet {
		complexities = append(complexities, comp)
	}
	sort.Slice(complexities, func(i, j int) bool {
		oi, oki := complexityBandOrder[complexities[i]]
		oj, okj := complexityBandOrder[complexities[j]]
		if oki && okj {
			return oi < oj
		}
		if oki {
			return true
		}
		if okj {
			return false
		}
		return complexities[i] < complexities[j]
	})

	// Build header row.
	numCols := 1 + len(complexities) // outcome + one per complexity
	colW := make([]int, numCols)
	colW[0] = len("OUTCOME")
	headerCells := []string{"OUTCOME"}
	for idx, comp := range complexities {
		label := strings.ToUpper(comp)
		headerCells = append(headerCells, label)
		if len(label) > colW[idx+1] {
			colW[idx+1] = len(label)
		}
	}

	// Build data rows in canonical outcome order.
	type matrixRow struct {
		cells []string
	}
	var dataRows []matrixRow
	for _, outcome := range outcomeOrder {
		if !outcomeSet[outcome] {
			continue
		}
		rowCells := []string{strings.ToUpper(outcome)}
		if len(rowCells[0]) > colW[0] {
			colW[0] = len(rowCells[0])
		}
		for idx, comp := range complexities {
			key := cellKey{outcome, comp}
			costs, ok := cells[key]
			if !ok {
				rowCells = append(rowCells, "—")
			} else {
				var total float64
				for _, cost := range costs {
					total += cost
				}
				mean := total / float64(len(costs))
				cell := fmt.Sprintf("$%.2f (%d)", mean, len(costs))
				rowCells = append(rowCells, cell)
				if len(cell) > colW[idx+1] {
					colW[idx+1] = len(cell)
				}
			}
		}
		dataRows = append(dataRows, matrixRow{cells: rowCells})
	}

	// Ensure header label widths are respected.
	for idx, label := range headerCells {
		if len(label) > colW[idx] {
			colW[idx] = len(label)
		}
	}

	gap := 2
	printRow := func(cells []string) {
		var sb strings.Builder
		for idx, cell := range cells {
			if idx > 0 {
				sb.WriteString(strings.Repeat(" ", gap))
			}
			sb.WriteString(fmt.Sprintf("%-*s", colW[idx], cell))
		}
		fmt.Println(sb.String())
	}

	printRow(headerCells)
	for _, dr := range dataRows {
		printRow(dr.cells)
	}

	return nil
}
