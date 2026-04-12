package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/runner"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <ticket>",
		Short: "Run the pipeline for a ticket",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				os.Exit(2)
			}
			return runPipeline(cmd, cfg, args[0])
		},
	}

	cmd.Flags().String("mode", "", "execution mode: checkpoint or autonomous")
	cmd.Flags().String("from", "", "resume from phase (or 'last')")
	cmd.Flags().Bool("dry-run", false, "render prompts without executing")

	return cmd
}

func runPipeline(cmd *cobra.Command, cfg *config.Config, ticketKey string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Fetch ticket
	source, err := createTicketSource(cfg)
	if err != nil {
		return err
	}
	t, err := source.Fetch(ctx, ticketKey)
	if err != nil {
		return fmt.Errorf("run: fetch ticket: %w", err)
	}

	ticketData := pipeline.TicketData{
		Key:                t.Key,
		Summary:            t.Summary,
		Description:        t.Description,
		Type:               t.Type,
		Priority:           t.Priority,
		AcceptanceCriteria: t.AcceptanceCriteria,
	}

	// Load pipeline config
	phasesPath, phasesCleanup, err := resolvePhasesPath()
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	if phasesCleanup != nil {
		defer phasesCleanup()
	}
	pl, err := pipeline.LoadPipeline(phasesPath)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	// Set up prompt loader
	promptDir, err := extractEmbeddedPrompts()
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	defer os.RemoveAll(promptDir)

	loaderDirs := []string{"prompts"}
	configDir, _ := os.UserConfigDir()
	if configDir != "" {
		loaderDirs = append([]string{filepath.Join(configDir, "soda", "prompts")}, loaderDirs...)
	}
	loaderDirs = append(loaderDirs, promptDir)
	loader := pipeline.NewPromptLoader(loaderDirs...)

	// Dry-run mode
	if dryRun {
		return runDryRun(cfg, pl, loader, ticketData)
	}

	// Load or create state
	state, err := pipeline.LoadOrCreate(cfg.StateDir, ticketKey)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	// Resolve mode
	mode := pipeline.Autonomous
	modeStr := cfg.Mode
	if cmd.Flags().Changed("mode") {
		modeStr, _ = cmd.Flags().GetString("mode")
	}
	if modeStr == "checkpoint" {
		mode = pipeline.Checkpoint
	}

	// Build mock runner
	mockRunner := buildMockRunner()

	// Build engine config — use a closure that captures the engine pointer
	var engine *pipeline.Engine

	engineCfg := pipeline.EngineConfig{
		Pipeline:     pl,
		Loader:       loader,
		Ticket:       ticketData,
		Model:        cfg.Model,
		WorkDir:      ".",
		WorktreeBase: cfg.WorktreeDir,
		BaseBranch:   "main",
		MaxCostUSD:   cfg.Limits.MaxCostPerTicket,
		Mode:         mode,
		OnEvent: func(event pipeline.Event) {
			handleEvent(ctx, cancel, engine, event)
		},
	}

	engine = pipeline.NewEngine(mockRunner, state, engineCfg)

	// Run or resume
	fromPhase, _ := cmd.Flags().GetString("from")
	startTime := time.Now()

	var runErr error
	if fromPhase != "" {
		if fromPhase == "last" {
			fromPhase, err = resolveLastPhase(state.Meta(), pl.Phases)
			if err != nil {
				return fmt.Errorf("run: %w", err)
			}
		}
		fmt.Printf("Resuming from phase: %s\n", fromPhase)
		runErr = engine.Resume(ctx, fromPhase)
	} else {
		runErr = engine.Run(ctx)
	}

	// Print summary
	printSummary(state.Meta(), time.Since(startTime))

	if runErr != nil {
		os.Exit(4)
	}
	return nil
}

func buildMockRunner() *runner.MockRunner {
	return &runner.MockRunner{
		Responses: map[string]*runner.RunResult{
			"triage": {
				Output:  json.RawMessage(`{"automatable":true,"ticket_key":"MOCK","complexity":"low","approach":"mock approach"}`),
				RawText: "Mock triage: ticket is automatable",
			},
			"plan": {
				Output:  json.RawMessage(`{"tasks":[{"id":"1","description":"mock task"}],"ticket_key":"MOCK"}`),
				RawText: "Mock plan: one task defined",
			},
			"implement": {
				Output:  json.RawMessage(`{"tests_passed":true,"ticket_key":"MOCK","branch":"mock-branch","commits":[],"files_changed":[]}`),
				RawText: "Mock implement: tests passed",
			},
			"verify": {
				Output:  json.RawMessage(`{"verdict":"PASS","ticket_key":"MOCK"}`),
				RawText: "Mock verify: all checks passed",
			},
			"submit": {
				Output:  json.RawMessage(`{"pr_url":"https://github.com/mock/repo/pull/0","ticket_key":"MOCK"}`),
				RawText: "Mock submit: PR created",
			},
		},
	}
}

func handleEvent(ctx context.Context, cancel context.CancelFunc, engine *pipeline.Engine, event pipeline.Event) {
	switch event.Kind {
	case pipeline.EventEngineStarted:
		fmt.Println("Pipeline started")
	case pipeline.EventEngineCompleted:
		fmt.Println("Pipeline completed")
	case pipeline.EventPhaseSkipped:
		fmt.Printf("⏭ %s  skipped\n", event.Phase)
	case pipeline.EventPhaseRetrying:
		category, _ := event.Data["category"].(string)
		attempt, _ := event.Data["attempt"].(int)
		fmt.Printf("↻ %s  retrying (%s, attempt %d)\n", event.Phase, category, attempt)
	case pipeline.EventBudgetWarning:
		total, _ := event.Data["total_cost"].(float64)
		limit, _ := event.Data["limit"].(float64)
		fmt.Printf("⚠ Budget warning: $%.2f / $%.2f\n", total, limit)
	case pipeline.EventCheckpointPause:
		fmt.Printf("⏸ %s completed. Continue? [y/N] ", event.Phase)
		if promptConfirm(ctx) {
			engine.Confirm()
		} else {
			cancel()
		}
	case pipeline.EventWorktreeCreated:
		wt, _ := event.Data["worktree"].(string)
		branch, _ := event.Data["branch"].(string)
		fmt.Printf("Created worktree: %s (%s)\n", wt, branch)
	case pipeline.EventMonitorSkipped:
		fmt.Printf("⏭ monitor  skipped (not yet implemented)\n")
	}
}

func promptConfirm(ctx context.Context) bool {
	ch := make(chan bool, 1)
	go func() {
		var input string
		fmt.Scanln(&input)
		ch <- strings.EqualFold(strings.TrimSpace(input), "y")
	}()
	select {
	case confirmed := <-ch:
		return confirmed
	case <-ctx.Done():
		return false
	}
}

func runDryRun(cfg *config.Config, pl *pipeline.PhasePipeline, loader *pipeline.PromptLoader, ticketData pipeline.TicketData) error {
	promptData := pipeline.PromptData{
		Ticket: ticketData,
	}

	// Load artifacts from state if available
	if cfg.StateDir != "" {
		state, err := pipeline.LoadOrCreate(cfg.StateDir, ticketData.Key)
		if err == nil {
			loadArtifacts(state, &promptData)
		}
	}

	for _, phase := range pl.Phases {
		tmplContent, err := loader.Load(phase.Prompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not load template for %s: %v\n", phase.Name, err)
			continue
		}

		rendered, err := pipeline.RenderPrompt(tmplContent, promptData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not render %s: %v\n", phase.Name, err)
			continue
		}

		fmt.Printf("=== System Prompt (%s) ===\n\n", phase.Name)
		fmt.Println(rendered)
		fmt.Printf("\n=== Tools ===\n%s\n", strings.Join(phase.Tools, ", "))
		fmt.Printf("\n=== Output Schema ===\n%s\n", phase.Schema)
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
	}

	return nil
}

func printSummary(meta *pipeline.PipelineMeta, elapsed time.Duration) {
	fmt.Println()
	fmt.Println("--- Summary ---")
	fmt.Printf("Ticket:  %s\n", meta.Ticket)
	fmt.Printf("Cost:    $%.2f\n", meta.TotalCost)
	fmt.Printf("Elapsed: %s\n", elapsed.Truncate(time.Second))

	for name, ps := range meta.Phases {
		if ps.Status == pipeline.PhaseFailed {
			fmt.Printf("Failed:  %s — %s\n", name, ps.Error)
		}
	}
}

// resolveLastPhase finds the last running or failed phase in pipeline order.
func resolveLastPhase(meta *pipeline.PipelineMeta, phases []pipeline.PhaseConfig) (string, error) {
	lastPhase := ""
	for _, phase := range phases {
		ps, ok := meta.Phases[phase.Name]
		if !ok {
			continue
		}
		if ps.Status == pipeline.PhaseRunning || ps.Status == pipeline.PhaseFailed {
			lastPhase = phase.Name
		}
	}
	if lastPhase == "" {
		return "", fmt.Errorf("no running or failed phase found")
	}
	return lastPhase, nil
}
