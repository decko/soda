package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/progress"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
	"github.com/mattn/go-isatty"
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
				return err
			}
			return runPipeline(cmd, cfg, args[0])
		},
	}

	cmd.Flags().String("mode", "", "execution mode: checkpoint or autonomous")
	cmd.Flags().String("from", "", "resume from phase (or 'last')")
	cmd.Flags().Bool("dry-run", false, "render prompts without executing")
	cmd.Flags().Bool("mock", false, "use mock runner for testing")

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

	// Search order: user config dir > working dir > embedded.
	// phases.yaml references prompts with the "prompts/" prefix
	// (e.g. "prompts/triage.md"), so the base dirs should NOT
	// include "prompts/" — the loader joins base + name.
	loaderDirs := []string{"."}
	configDir, _ := os.UserConfigDir()
	if configDir != "" {
		loaderDirs = append([]string{filepath.Join(configDir, "soda")}, loaderDirs...)
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
	switch modeStr {
	case "checkpoint":
		mode = pipeline.Checkpoint
	case "autonomous", "":
		// default
	default:
		return fmt.Errorf("run: unknown mode %q (expected 'checkpoint' or 'autonomous')", modeStr)
	}

	// Build runner
	var r runner.Runner
	useMock, _ := cmd.Flags().GetBool("mock")
	if useMock {
		r = buildMockRunner()
	} else {
		workDir, err := filepath.Abs(".")
		if err != nil {
			return fmt.Errorf("run: resolve workdir: %w", err)
		}
		claudeRunner, err := runner.NewClaudeRunner("claude", cfg.Model, workDir)
		if err != nil {
			return fmt.Errorf("run: create claude runner: %w", err)
		}
		r = claudeRunner
	}

	// Build prompt config from repos
	promptConfig := buildPromptConfig(cfg)

	// Load project context files
	promptContext := buildPromptContext(cfg)

	// Set up progress display
	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	prog := progress.New(os.Stdout, isTTY)

	// Build engine config — use a closure that captures the engine pointer
	var engine *pipeline.Engine
	skippedPhases := map[string]bool{}

	engineCfg := pipeline.EngineConfig{
		Pipeline:      pl,
		Loader:        loader,
		Ticket:        ticketData,
		PromptConfig:  promptConfig,
		PromptContext: promptContext,
		Model:         cfg.Model,
		WorkDir:       ".",
		WorktreeBase:  cfg.WorktreeDir,
		BaseBranch:    "main",
		MaxCostUSD:    cfg.Limits.MaxCostPerTicket,
		Mode:          mode,
		OnEvent: func(event pipeline.Event) {
			if event.Kind == pipeline.EventPhaseSkipped || event.Kind == pipeline.EventMonitorSkipped {
				skippedPhases[event.Phase] = true
			}
			handleEvent(ctx, cancel, engine, state, prog, event)
		},
	}

	engine = pipeline.NewEngine(r, state, engineCfg)

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
	printSummary(state, pl.Phases, t.Summary, time.Since(startTime), runErr, skippedPhases)

	return runErr
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
			"review/go-specialist": {
				Output:  json.RawMessage(`{"findings":[],"verdict":"pass","ticket_key":"MOCK"}`),
				RawText: "Mock go-specialist review: no issues",
			},
			"review/ai-harness": {
				Output:  json.RawMessage(`{"findings":[],"verdict":"pass","ticket_key":"MOCK"}`),
				RawText: "Mock ai-harness review: no issues",
			},
			"submit": {
				Output:  json.RawMessage(`{"pr_url":"https://github.com/mock/repo/pull/0","ticket_key":"MOCK"}`),
				RawText: "Mock submit: PR created",
			},
		},
	}
}

func handleEvent(ctx context.Context, cancel context.CancelFunc, engine *pipeline.Engine, state *pipeline.State, prog *progress.Progress, event pipeline.Event) {
	switch event.Kind {
	case pipeline.EventEngineStarted:
		prog.Message("Pipeline started")
		prog.Message("")

	case pipeline.EventEngineCompleted:
		prog.Message("")
		prog.Message("Pipeline completed")

	case pipeline.EventPhaseStarted:
		prog.PhaseStarted(event.Phase)

	case pipeline.EventPhaseCompleted:
		durationMs, _ := event.Data["duration_ms"].(int64)
		if durationMs == 0 {
			// Try float64 (JSON numbers default to float64)
			if dFloat, ok := event.Data["duration_ms"].(float64); ok {
				durationMs = int64(dFloat)
			}
		}
		elapsed := time.Duration(durationMs) * time.Millisecond
		cost, _ := event.Data["cost"].(float64)

		// Extract summary from structured output
		summary := ""
		if result, err := state.ReadResult(event.Phase); err == nil {
			summary = progress.PhaseSummary(event.Phase, result)
		}

		prog.PhaseCompleted(event.Phase, summary, elapsed, cost)

	case pipeline.EventPhaseFailed:
		errMsg, _ := event.Data["error"].(string)
		durationMs, _ := event.Data["duration_ms"].(int64)
		if durationMs == 0 {
			if dFloat, ok := event.Data["duration_ms"].(float64); ok {
				durationMs = int64(dFloat)
			}
		}
		elapsed := time.Duration(durationMs) * time.Millisecond
		prog.PhaseFailed(event.Phase, errMsg, elapsed)

	case pipeline.EventPhaseSkipped:
		prog.PhaseSkipped(event.Phase)

	case pipeline.EventPhaseRetrying:
		category, _ := event.Data["category"].(string)
		attempt, _ := event.Data["attempt"].(int)
		if attempt == 0 {
			if aFloat, ok := event.Data["attempt"].(float64); ok {
				attempt = int(aFloat)
			}
		}
		prog.PhaseRetrying(event.Phase, category, attempt)

	case pipeline.EventBudgetWarning:
		total, _ := event.Data["total_cost"].(float64)
		limit, _ := event.Data["limit"].(float64)
		prog.BudgetWarning(total, limit)

	case pipeline.EventCheckpointPause:
		prog.Message(fmt.Sprintf("⏸ %s completed. Continue? [y/N] ", event.Phase))
		if engine != nil && promptConfirm(ctx) {
			engine.Confirm()
		} else {
			cancel()
		}

	case pipeline.EventWorktreeCreated:
		wt, _ := event.Data["worktree"].(string)
		branch, _ := event.Data["branch"].(string)
		prog.Message(fmt.Sprintf("Created worktree: %s (%s)", wt, branch))

	case pipeline.EventMonitorSkipped:
		prog.PhaseSkipped("monitor")
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
		Ticket:  ticketData,
		Config:  buildPromptConfig(cfg),
		Context: buildPromptContext(cfg),
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

// formatDuration formats a millisecond duration into a human-readable string.
// Returns "—" for zero/negative values.
func formatDuration(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", ms)
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}

// formatPhaseDetails reads a phase's structured JSON result from state and
// returns a one-line detail string. Returns "" on missing or unparseable data.
func formatPhaseDetails(state *pipeline.State, phase string) string {
	raw, err := state.ReadResult(phase)
	if err != nil {
		return ""
	}

	switch phase {
	case "triage":
		var out schemas.TriageOutput
		if json.Unmarshal(raw, &out) != nil {
			return ""
		}
		parts := []string{}
		if out.Repo != "" {
			parts = append(parts, "repo="+out.Repo)
		}
		if out.Complexity != "" {
			parts = append(parts, "complexity="+out.Complexity)
		}
		if !out.Automatable {
			reason := out.BlockReason
			if reason == "" {
				reason = "not automatable"
			}
			parts = append(parts, "BLOCKED: "+reason)
		}
		return strings.Join(parts, ", ")

	case "plan":
		var out schemas.PlanOutput
		if json.Unmarshal(raw, &out) != nil {
			return ""
		}
		return fmt.Sprintf("%d tasks", len(out.Tasks))

	case "implement":
		var out schemas.ImplementOutput
		if json.Unmarshal(raw, &out) != nil {
			return ""
		}
		return fmt.Sprintf("%d files changed, %d commits", len(out.FilesChanged), len(out.Commits))

	case "verify":
		var out schemas.VerifyOutput
		if json.Unmarshal(raw, &out) != nil {
			return ""
		}
		if strings.EqualFold(out.Verdict, "PASS") {
			return "PASS — all criteria met"
		}
		fails := 0
		for _, cr := range out.CriteriaResults {
			if !cr.Passed {
				fails++
			}
		}
		if fails > 0 {
			return fmt.Sprintf("FAIL — %d criteria not met", fails)
		}
		return "FAIL"

	case "review":
		var out schemas.ReviewOutput
		if json.Unmarshal(raw, &out) != nil {
			return ""
		}
		if len(out.Findings) == 0 {
			return out.Verdict
		}
		return fmt.Sprintf("%s — %d findings", out.Verdict, len(out.Findings))

	case "submit":
		var out schemas.SubmitOutput
		if json.Unmarshal(raw, &out) != nil {
			return ""
		}
		if out.PRURL != "" {
			return out.PRURL
		}
		return ""

	case "monitor":
		var out schemas.MonitorOutput
		if json.Unmarshal(raw, &out) != nil {
			return ""
		}
		return fmt.Sprintf("%d comments handled", len(out.CommentsHandled))
	}

	return ""
}

func printSummary(state *pipeline.State, phases []pipeline.PhaseConfig, summary string, elapsed time.Duration, runErr error, skippedPhases map[string]bool) {
	fprintSummary(os.Stdout, state, phases, summary, elapsed, runErr, skippedPhases)
}

// fprintSummary writes the detailed pipeline outcome report to w.
// Extracted so tests can capture output without os.Pipe.
func fprintSummary(w io.Writer, state *pipeline.State, phases []pipeline.PhaseConfig, summary string, elapsed time.Duration, runErr error, skippedPhases map[string]bool) {
	meta := state.Meta()
	success := runErr == nil

	// Header
	fmt.Fprintln(w)
	if success {
		fmt.Fprintln(w, "✅ Pipeline completed successfully")
	} else {
		fmt.Fprintln(w, "❌ Pipeline failed")
	}
	fmt.Fprintln(w)

	// Ticket / branch / worktree info
	fmt.Fprintf(w, "Ticket:   %s", meta.Ticket)
	if summary != "" {
		fmt.Fprintf(w, " — %s", summary)
	}
	fmt.Fprintln(w)
	if meta.Branch != "" {
		fmt.Fprintf(w, "Branch:   %s\n", meta.Branch)
	}
	if meta.Worktree != "" {
		fmt.Fprintf(w, "Worktree: %s\n", meta.Worktree)
	}
	fmt.Fprintln(w)

	// Phase table
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "PHASE\tSTATUS\tDURATION\tCOST\tDETAILS\n")
	fmt.Fprintf(tw, "─────\t──────\t────────\t────\t───────\n")

	failedPhase := ""
	failedIdx := -1
	for i, phase := range phases {
		ps := meta.Phases[phase.Name]

		status := "·"
		dur := "—"
		cost := "—"
		details := ""

		if skippedPhases[phase.Name] {
			status = "⏭"
		} else if ps != nil {
			switch ps.Status {
			case pipeline.PhaseCompleted:
				status = "✓"
			case pipeline.PhaseFailed:
				status = "✗"
				failedPhase = phase.Name
				failedIdx = i
			case pipeline.PhaseRunning:
				status = "▸"
			default:
				status = "·"
			}
			dur = formatDuration(ps.DurationMs)
			if ps.Cost > 0 {
				cost = fmt.Sprintf("$%.2f", ps.Cost)
			}
		}

		// Details from structured output
		if ps != nil && ps.Status == pipeline.PhaseCompleted {
			details = formatPhaseDetails(state, phase.Name)
		} else if ps != nil && ps.Status == pipeline.PhaseFailed {
			details = formatPhaseDetails(state, phase.Name)
			if details == "" && ps.Error != "" {
				details = ps.Error
			}
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", phase.Name, status, dur, cost, details)
	}
	tw.Flush()

	// Totals
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Total cost: $%.2f\n", meta.TotalCost)
	fmt.Fprintf(w, "Elapsed:    %s\n", elapsed.Truncate(time.Second))

	// PR URL on success
	if success {
		prDetails := formatPhaseDetails(state, "submit")
		if prDetails != "" && strings.HasPrefix(prDetails, "http") {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "PR: %s\n", prDetails)
		}
	}

	// Actionable next steps on failure
	if !success && failedPhase != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Next steps:")
		// Suggest resuming from the predecessor phase (whose output feeds the failed phase)
		resumeFrom := failedPhase
		if failedIdx > 0 {
			resumeFrom = phases[failedIdx-1].Name
		}
		fmt.Fprintf(w, "  soda run %s --from %s\n", meta.Ticket, resumeFrom)
	}
}

func buildPromptConfig(cfg *config.Config) pipeline.PromptConfigData {
	repos := make([]pipeline.RepoConfig, len(cfg.Repos))
	for i, r := range cfg.Repos {
		repos[i] = pipeline.RepoConfig{
			Name:        r.Name,
			Forge:       r.Forge,
			PushTo:      r.PushTo,
			Target:      r.Target,
			Description: r.Description,
			Formatter:   r.Formatter,
			TestCommand: r.TestCommand,
			Labels:      r.Labels,
			Trailers:    r.Trailers,
		}
	}

	pc := pipeline.PromptConfigData{
		Repos: repos,
	}

	// Set single Repo, Formatter, TestCommand from the first repo if available.
	if len(repos) > 0 {
		pc.Repo = repos[0]
		pc.Formatter = repos[0].Formatter
		pc.TestCommand = repos[0].TestCommand
	}

	return pc
}

func buildPromptContext(cfg *config.Config) pipeline.ContextData {
	var ctx pipeline.ContextData

	// Load project-wide context files
	var projectParts []string
	for _, path := range cfg.Context {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		projectParts = append(projectParts, string(data))
	}
	if len(projectParts) > 0 {
		ctx.ProjectContext = strings.Join(projectParts, "\n\n---\n\n")
	}

	// Load gotchas if referenced in any phase context
	if paths, ok := cfg.PhaseContext["plan"]; ok {
		for _, path := range paths {
			if strings.Contains(path, "gotchas") {
				data, err := os.ReadFile(path)
				if err == nil {
					ctx.Gotchas = string(data)
				}
				break
			}
		}
	}

	return ctx
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
