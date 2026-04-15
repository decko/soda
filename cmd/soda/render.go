package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/ticket"
	"github.com/spf13/cobra"
)

func newRenderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "render-prompt",
		Short: "Render a phase prompt template and print to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			phase, _ := cmd.Flags().GetString("phase")
			ticketKey, _ := cmd.Flags().GetString("ticket")

			return runRender(cmd, cfg, phase, ticketKey)
		},
	}

	cmd.Flags().String("phase", "", "phase to render (required)")
	cmd.Flags().String("ticket", "", "ticket key (required)")
	cmd.MarkFlagRequired("phase")
	cmd.MarkFlagRequired("ticket")

	return cmd
}

func runRender(cmd *cobra.Command, cfg *config.Config, phaseName, ticketKey string) error {
	ctx := cmd.Context()

	// Load pipeline config
	phasesPath, cleanup, err := resolvePhasesPath()
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	pl, err := pipeline.LoadPipeline(phasesPath)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	// Find phase config
	var phaseConfig *pipeline.PhaseConfig
	for idx := range pl.Phases {
		if pl.Phases[idx].Name == phaseName {
			phaseConfig = &pl.Phases[idx]
			break
		}
	}
	if phaseConfig == nil {
		return fmt.Errorf("render: phase %q not found in pipeline", phaseName)
	}

	// Fetch ticket
	source, err := createTicketSource(cfg)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	t, err := source.Fetch(ctx, ticketKey)
	if err != nil {
		return fmt.Errorf("render: fetch ticket: %w", err)
	}

	// Build prompt data
	promptData := pipeline.PromptData{
		Ticket: pipeline.TicketData{
			Key:                t.Key,
			Summary:            t.Summary,
			Description:        t.Description,
			Type:               t.Type,
			Priority:           t.Priority,
			AcceptanceCriteria: t.AcceptanceCriteria,
			Comments:           mapTicketComments(t.Comments),
		},
	}

	// Load artifacts from state if they exist
	stateDir := cfg.StateDir
	if stateDir != "" {
		state, stateErr := pipeline.LoadOrCreate(stateDir, ticketKey)
		if stateErr == nil {
			loadArtifacts(state, &promptData)
		}
	}

	// Set up prompt loader
	promptDir, extractErr := extractEmbeddedPrompts()
	if extractErr != nil {
		return fmt.Errorf("render: %w", extractErr)
	}
	defer os.RemoveAll(promptDir)

	loaderDirs := []string{"."}
	configDir, _ := os.UserConfigDir()
	if configDir != "" {
		loaderDirs = append([]string{filepath.Join(configDir, "soda")}, loaderDirs...)
	}
	loaderDirs = append(loaderDirs, promptDir)
	loader := pipeline.NewPromptLoader(loaderDirs...)

	// Load and render
	tmplContent, err := loader.Load(phaseConfig.Prompt)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	rendered, err := pipeline.RenderPrompt(tmplContent, promptData)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	// Output
	fmt.Printf("=== System Prompt (%s) ===\n\n", phaseName)
	fmt.Println(rendered)
	fmt.Printf("\n=== Tools ===\n%s\n", strings.Join(phaseConfig.Tools, ", "))
	fmt.Printf("\n=== Output Schema ===\n%s\n", phaseConfig.Schema)

	return nil
}

func createTicketSource(cfg *config.Config) (ticket.Source, error) {
	switch cfg.TicketSource {
	case "jira":
		return ticket.NewJiraSource(ticket.JiraConfig{
			Command: cfg.Jira.Command,
			Query:   cfg.Jira.Query,
		})
	case "github":
		return ticket.NewGitHubSource(ticket.GitHubConfig{
			Owner:         cfg.GitHub.Owner,
			Repo:          cfg.GitHub.Repo,
			FetchComments: cfg.GitHub.FetchComments,
		})
	default:
		return nil, fmt.Errorf("unsupported ticket source: %q", cfg.TicketSource)
	}
}

func mapTicketComments(comments []ticket.Comment) []pipeline.TicketComment {
	if len(comments) == 0 {
		return nil
	}
	out := make([]pipeline.TicketComment, len(comments))
	for i, c := range comments {
		out[i] = pipeline.TicketComment{
			Author:    c.Author,
			Body:      c.Body,
			CreatedAt: c.CreatedAt,
		}
	}
	return out
}

func loadArtifacts(state *pipeline.State, data *pipeline.PromptData) {
	if artifact, err := state.ReadArtifact("triage"); err == nil {
		data.Artifacts.Triage = string(artifact)
	}
	if artifact, err := state.ReadArtifact("plan"); err == nil {
		data.Artifacts.Plan = string(artifact)
	}
	if artifact, err := state.ReadArtifact("implement"); err == nil {
		data.Artifacts.Implement = string(artifact)
	}
	if artifact, err := state.ReadArtifact("verify"); err == nil {
		data.Artifacts.Verify = string(artifact)
	}
	if artifact, err := state.ReadArtifact("review"); err == nil {
		data.Artifacts.Review = string(artifact)
	}
}
