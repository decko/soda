package main

import "github.com/spf13/cobra"

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show active and recent pipelines",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
