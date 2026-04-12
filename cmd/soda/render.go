package main

import "github.com/spf13/cobra"

func newRenderCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "render-prompt",
		Short: "Render a phase prompt template and print to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}
