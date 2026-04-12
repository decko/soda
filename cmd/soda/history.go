package main

import "github.com/spf13/cobra"

func newHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history <ticket>",
		Short: "Show phase details for a ticket",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
