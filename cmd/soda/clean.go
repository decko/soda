package main

import "github.com/spf13/cobra"

func newCleanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clean [ticket]",
		Short: "Remove completed/failed pipeline state and worktrees",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
