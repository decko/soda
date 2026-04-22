package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/ticket"
	"github.com/decko/soda/internal/tui"
	"github.com/spf13/cobra"
)

func newPickCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pick",
		Short: "Select a ticket interactively and trigger pipeline",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			return runPick(cmd, cfg)
		},
	}

	cmd.Flags().String("query", "", "search filter for listing tickets")
	cmd.Flags().String("pipeline", "", "pipeline name (default: phases.yaml)")
	cmd.Flags().String("mode", "", "execution mode: checkpoint or autonomous")
	cmd.Flags().Bool("mock", false, "use mock runner for testing")

	return cmd
}

func runPick(cmd *cobra.Command, cfg *config.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Build ticket source from config.
	source, err := createTicketSource(cfg)
	if err != nil {
		return fmt.Errorf("pick: %w", err)
	}

	// Fetch ticket list.
	query, _ := cmd.Flags().GetString("query")
	tickets, err := source.List(ctx, query)
	if err != nil {
		return fmt.Errorf("pick: list tickets: %w", err)
	}

	// Convert to TUI model data.
	infos := ticketsToPickerInfo(tickets)

	// Launch the picker TUI.
	model := tui.NewPickerModel(infos)
	program := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return fmt.Errorf("pick: TUI error: %w", err)
	}

	pm, ok := finalModel.(tui.PickerModel)
	if !ok {
		return fmt.Errorf("pick: unexpected model type %T", finalModel)
	}
	result := pm.Result()
	if result == nil {
		// User quit without selecting.
		return nil
	}

	if result.Action != tui.PickerActionRun {
		return nil
	}

	// Trigger the pipeline for the selected ticket.
	fmt.Printf("Selected: %s — %s\n", result.Ticket.Key, result.Ticket.Summary)
	cancel() // release signal handler before runPipeline creates its own

	pipelineName, _ := cmd.Flags().GetString("pipeline")
	mode, _ := cmd.Flags().GetString("mode")
	useMock, _ := cmd.Flags().GetBool("mock")
	return runPipeline(cfg, pipelineOpts{
		ticketKey:       result.Ticket.Key,
		pipelineName:    pipelineName,
		pipelineChanged: cmd.Flags().Changed("pipeline"),
		mode:            mode,
		modeChanged:     cmd.Flags().Changed("mode"),
		useMock:         useMock,
	})
}

// ticketsToPickerInfo converts ticket.Ticket slices to tui.TicketInfo slices.
func ticketsToPickerInfo(tickets []ticket.Ticket) []tui.TicketInfo {
	infos := make([]tui.TicketInfo, 0, len(tickets))
	for _, t := range tickets {
		infos = append(infos, tui.TicketInfo{
			Key:      t.Key,
			Summary:  t.Summary,
			Type:     t.Type,
			Priority: t.Priority,
			Status:   t.Status,
			Labels:   t.Labels,
		})
	}
	return infos
}
