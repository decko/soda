package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newCostCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cost",
		Short: "Show cost breakdown from the persistent cost ledger",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			return runCost(cfg.StateDir)
		},
	}
}

func runCost(stateDir string) error {
	entries, err := pipeline.ReadCostLedger(stateDir)
	if err != nil {
		return fmt.Errorf("cost: read ledger: %w", err)
	}
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
