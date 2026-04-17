package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/git"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/progress"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/internal/ticket"
	"github.com/decko/soda/internal/tui"
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
	cmd.Flags().Bool("tui", false, "use interactive TUI display")

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

	// Extract artifacts from comments (spec/plan) if configured.
	extractArtifacts(cfg, t)

	ticketData := pipeline.TicketData{
		Key:                t.Key,
		Summary:            t.Summary,
		Description:        t.Description,
		Type:               t.Type,
		Priority:           t.Priority,
		AcceptanceCriteria: t.AcceptanceCriteria,
		Comments:           mapTicketComments(t.Comments),
		ExistingSpec:       t.ExistingSpec,
		ExistingPlan:       t.ExistingPlan,
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

	// Resolve working directory to the main repo root so worktrees,
	// state, and all paths are always relative to the root, even when
	// soda is invoked from inside an existing worktree.
	workDir, err := git.RepoRoot(".")
	if err != nil {
		return fmt.Errorf("run: resolve repo root: %w", err)
	}

	// Resolve StateDir relative to repo root.
	stateDir := cfg.StateDir
	if !filepath.IsAbs(stateDir) {
		stateDir = filepath.Join(workDir, stateDir)
	}

	// Load or create state
	state, err := pipeline.LoadOrCreate(stateDir, ticketKey)
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

	// Check if TUI mode is requested.
	useTUI, _ := cmd.Flags().GetBool("tui")

	// Set up progress display (used in non-TUI mode).
	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	prog := progress.New(os.Stdout, isTTY)

	// Build engine config — use a closure that captures the engine pointer
	var engine *pipeline.Engine
	skippedPhases := map[string]bool{}

	// Set up PR poller for monitor phase.
	prPoller := pipeline.NewGitHubPRPoller("")

	// When TUI mode is active, create a bidirectional pause channel and an
	// event channel. The send end of the pause channel goes to the TUI, the
	// receive end to the engine. The event channel bridges engine events to
	// the TUI's bubbletea event loop.
	var pauseSignal chan bool
	var tuiEventCh chan pipeline.Event
	if useTUI {
		pauseSignal = make(chan bool, 8)
		tuiEventCh = make(chan pipeline.Event, 64)
	}

	engineCfg := pipeline.EngineConfig{
		Pipeline:      pl,
		Loader:        loader,
		Ticket:        ticketData,
		PromptConfig:  promptConfig,
		PromptContext: promptContext,
		Model:         cfg.Model,
		WorkDir:       workDir,
		WorktreeBase:  filepath.Join(workDir, cfg.WorktreeDir),
		BaseBranch:    "main",
		MaxCostUSD:    cfg.Limits.MaxCostPerTicket,
		Mode:          mode,
		PRPoller:      prPoller,
		OnEvent: func(event pipeline.Event) {
			if event.Kind == pipeline.EventPhaseSkipped || event.Kind == pipeline.EventMonitorSkipped {
				skippedPhases[event.Phase] = true
			}
			if useTUI {
				// Forward events to the TUI via channel.
				select {
				case tuiEventCh <- event:
				default:
					// Drop if buffer is full; TUI will still see subsequent events.
				}
			} else {
				handleEvent(ctx, cancel, engine, state, prog, event)
			}
		},
	}

	if useTUI {
		engineCfg.PauseSignal = pauseSignal
	}

	engine = pipeline.NewEngine(r, state, engineCfg)

	// Run or resume
	fromPhase, _ := cmd.Flags().GetString("from")
	startTime := time.Now()

	if useTUI {
		return runWithTUI(ctx, engine, state, pl, t, fromPhase, pauseSignal, tuiEventCh, startTime, skippedPhases)
	}

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

// runWithTUI launches the pipeline engine in a background goroutine and
// drives the bubbletea TUI in the foreground. The pause channel connects
// the TUI's pause/resume keys to the engine's PauseSignal. The event
// channel bridges engine events into the TUI's bubbletea event loop.
func runWithTUI(ctx context.Context, engine *pipeline.Engine, state *pipeline.State, pl *pipeline.PhasePipeline, t *ticket.Ticket, fromPhase string, pauseSignal chan bool, tuiEventCh chan pipeline.Event, startTime time.Time, skippedPhases map[string]bool) error {
	// Extract phase names for the TUI.
	phaseNames := make([]string, len(pl.Phases))
	for i, p := range pl.Phases {
		phaseNames[i] = p.Name
	}

	// Build ticket for TUI.
	tuiTicket := ticket.Ticket{
		Key:                t.Key,
		Summary:            t.Summary,
		Description:        t.Description,
		Type:               t.Type,
		Priority:           t.Priority,
		Status:             t.Status,
		Labels:             t.Labels,
		AcceptanceCriteria: t.AcceptanceCriteria,
	}

	model := tui.New(tuiTicket, phaseNames, tuiEventCh, pauseSignal)

	// Create a cancellable context so TUI exit stops the engine.
	engineCtx, engineCancel := context.WithCancel(ctx)
	defer engineCancel()

	// Run engine in background.
	engineDone := make(chan error, 1)
	go func() {
		defer close(tuiEventCh)
		defer func() {
			model.MarkPauseClosed()
			close(pauseSignal)
		}()
		var runErr error
		if fromPhase != "" {
			if fromPhase == "last" {
				resolved, err := resolveLastPhase(state.Meta(), pl.Phases)
				if err != nil {
					engineDone <- fmt.Errorf("run: %w", err)
					return
				}
				fromPhase = resolved
			}
			runErr = engine.Resume(engineCtx, fromPhase)
		} else {
			runErr = engine.Run(engineCtx)
		}
		engineDone <- runErr
	}()

	// Run TUI in foreground.
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		engineCancel()
		return fmt.Errorf("run: TUI error: %w", err)
	}

	// Cancel engine if TUI exited before engine finished.
	engineCancel()

	// Wait for engine to finish.
	runErr := <-engineDone

	// Print summary after TUI exits.
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

	case pipeline.EventMonitorPolling:
		pollCount, _ := event.Data["poll_count"].(int)
		rounds, _ := event.Data["response_rounds"].(int)
		prog.Message(fmt.Sprintf("  📡 poll #%d (rounds: %d)", pollCount, rounds))

	case pipeline.EventMonitorNewComments:
		count, _ := event.Data["count"].(int)
		rounds, _ := event.Data["response_rounds"].(int)
		prog.Message(fmt.Sprintf("  💬 %d new comment(s) — round %d", count, rounds))

	case pipeline.EventMonitorCIChange:
		prev, _ := event.Data["previous"].(string)
		curr, _ := event.Data["current"].(string)
		prog.Message(fmt.Sprintf("  🔄 CI status: %s → %s", prev, curr))

	case pipeline.EventMonitorCIFailure:
		failedJobs, _ := event.Data["failed_jobs"].([]string)
		if len(failedJobs) > 0 {
			prog.Message(fmt.Sprintf("  ❌ CI failed: %s", strings.Join(failedJobs, ", ")))
		} else {
			prog.Message("  ❌ CI failed")
		}

	case pipeline.EventMonitorConflict:
		baseBranch, _ := event.Data["base_branch"].(string)
		prog.Message(fmt.Sprintf("  ⚠️  Merge conflict detected with %s", baseBranch))

	case pipeline.EventMonitorRebaseOK:
		prog.Message("  ✅ Auto-rebase succeeded, pushed")

	case pipeline.EventMonitorRebaseFailed:
		errMsg, _ := event.Data["error"].(string)
		prog.Message(fmt.Sprintf("  ❌ Auto-rebase failed: %s", errMsg))

	case pipeline.EventMonitorPRApproved:
		state, _ := event.Data["state"].(string)
		prog.Message(fmt.Sprintf("  ✅ PR %s — pipeline complete", state))

	case pipeline.EventMonitorPRClosed:
		prog.Message("  ❌ PR closed/rejected — pipeline failed")

	case pipeline.EventMonitorMaxRounds:
		rounds, _ := event.Data["response_rounds"].(int)
		maxRounds, _ := event.Data["max_response_rounds"].(int)
		prog.Message(fmt.Sprintf("  ⏹  Max response rounds reached (%d/%d)", rounds, maxRounds))

	case pipeline.EventMonitorTimeout:
		duration, _ := event.Data["duration"].(string)
		prog.Message(fmt.Sprintf("  ⏹  Monitor timeout after %s", duration))

	case pipeline.EventCorrectiveSkipped:
		prog.PhaseSkipped(event.Phase)

	case pipeline.EventPatchExhausted:
		patchCycles, _ := event.Data["patch_cycles"].(int)
		if patchCycles == 0 {
			if pf, ok := event.Data["patch_cycles"].(float64); ok {
				patchCycles = int(pf)
			}
		}
		onExhausted, _ := event.Data["on_exhausted"].(string)
		prog.Message(fmt.Sprintf("  ⏹  Patch exhausted after %d cycles (policy: %s)", patchCycles, onExhausted))

	case pipeline.EventPatchEscalated:
		escalatingTo, _ := event.Data["escalating_to"].(string)
		prog.Message(fmt.Sprintf("  ⬆️  Escalating to %s", escalatingTo))

	case pipeline.EventPatchRegression:
		prog.Message("  ⚠️  Regression detected: previously-passing criteria now fail")

	case pipeline.EventPatchTooComplex:
		reason, _ := event.Data["reason"].(string)
		prog.Message(fmt.Sprintf("  ⚠️  Patch too complex: %s", reason))

	case pipeline.EventPatchEscalationSkipped:
		reason, _ := event.Data["reason"].(string)
		prog.Message(fmt.Sprintf("  ⏭  Escalation skipped: %s", reason))
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

	case "patch":
		var out schemas.PatchOutput
		if json.Unmarshal(raw, &out) != nil {
			return ""
		}
		if out.TooComplex {
			return "too complex"
		}
		fixed := 0
		for _, fr := range out.FixResults {
			if fr.Status == "fixed" {
				fixed++
			}
		}
		total := len(out.FixResults)
		if total == 0 {
			return ""
		}
		return fmt.Sprintf("%d/%d fixed", fixed, total)

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
	if !success {
		formatNextSteps(w, meta, phases, failedPhase, failedIdx, runErr)
	}
}

// formatNextSteps writes context-aware recovery suggestions based on the error
// type. It uses errors.As to classify the failure and tailors the advice
// accordingly.
func formatNextSteps(w io.Writer, meta *pipeline.PipelineMeta, phases []pipeline.PhaseConfig, failedPhase string, failedIdx int, runErr error) {
	if runErr == nil {
		return
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")

	ticket := meta.Ticket
	worktree := meta.Worktree

	switch {
	case isVerifyGateError(runErr):
		// Verify gate: the implementation didn't pass verification.
		fmt.Fprintf(w, "  1. Review the verify output above for failing criteria\n")
		if worktree != "" {
			fmt.Fprintf(w, "  2. cd %s\n", worktree)
			fmt.Fprintf(w, "  3. Fix the issues manually, then re-run:\n")
		} else {
			fmt.Fprintf(w, "  2. Fix the issues manually, then re-run:\n")
		}
		fmt.Fprintf(w, "     soda run %s --from implement  (re-implement with updated context)\n", ticket)
		fmt.Fprintf(w, "     soda run %s --from verify     (re-verify after manual fixes)\n", ticket)

	case isPhaseGateError(runErr):
		var ge *pipeline.PhaseGateError
		errors.As(runErr, &ge)
		fmt.Fprintf(w, "  Phase %q was gated: %s\n", ge.Phase, ge.Reason)
		fmt.Fprintf(w, "  • Re-run from that phase after addressing the issue:\n")
		fmt.Fprintf(w, "    soda run %s --from %s  (retry after fixing the gate condition)\n", ticket, ge.Phase)

	case isBudgetExceededError(runErr):
		var be *pipeline.BudgetExceededError
		errors.As(runErr, &be)
		fmt.Fprintf(w, "  Budget limit ($%.2f) reached at $%.2f in phase %q.\n", be.Limit, be.Actual, be.Phase)
		fmt.Fprintf(w, "  • Increase the limit in soda.yaml (limits.max_cost_per_ticket) and resume:\n")
		fmt.Fprintf(w, "    soda run %s --from %s  (resume with higher budget)\n", ticket, be.Phase)

	case isTransientError(runErr):
		fmt.Fprintf(w, "  A transient error occurred (network, rate-limit, or timeout).\n")
		fmt.Fprintf(w, "  • Wait a moment and retry:\n")
		if failedPhase != "" {
			fmt.Fprintf(w, "    soda run %s --from %s  (retry the failed phase)\n", ticket, failedPhase)
		} else {
			fmt.Fprintf(w, "    soda run %s\n", ticket)
		}

	case isParseError(runErr):
		fmt.Fprintf(w, "  The model returned output that could not be parsed.\n")
		fmt.Fprintf(w, "  • Retry (the model may produce valid output on the next attempt):\n")
		if failedPhase != "" {
			fmt.Fprintf(w, "    soda run %s --from %s  (retry with a fresh attempt)\n", ticket, failedPhase)
		} else {
			fmt.Fprintf(w, "    soda run %s\n", ticket)
		}

	default:
		// Generic fallback: suggest resuming from predecessor or failed phase.
		resumeFrom := failedPhase
		if failedIdx > 0 {
			resumeFrom = phases[failedIdx-1].Name
		}
		if failedPhase != "" {
			fmt.Fprintf(w, "  • Resume the pipeline:\n")
			fmt.Fprintf(w, "    soda run %s --from %s\n", ticket, resumeFrom)
			if worktree != "" {
				fmt.Fprintf(w, "  • Inspect the worktree:\n")
				fmt.Fprintf(w, "    cd %s\n", worktree)
			}
		} else {
			fmt.Fprintf(w, "  • Re-run the pipeline:\n")
			fmt.Fprintf(w, "    soda run %s\n", ticket)
		}
	}
}

func isVerifyGateError(err error) bool {
	var ge *pipeline.PhaseGateError
	return errors.As(err, &ge) && ge.Phase == "verify"
}

func isPhaseGateError(err error) bool {
	var ge *pipeline.PhaseGateError
	return errors.As(err, &ge)
}

func isBudgetExceededError(err error) bool {
	var be *pipeline.BudgetExceededError
	return errors.As(err, &be)
}

func isTransientError(err error) bool {
	var te *claude.TransientError
	return errors.As(err, &te)
}

func isParseError(err error) bool {
	var pe *claude.ParseError
	return errors.As(err, &pe)
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
