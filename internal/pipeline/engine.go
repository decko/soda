package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/decko/soda/internal/git"
	"github.com/decko/soda/internal/progress"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
)

// Mode controls whether the engine pauses between phases.
type Mode int

const (
	// Autonomous runs all phases without pausing.
	Autonomous Mode = iota
	// Checkpoint pauses after each phase and waits for Confirm().
	Checkpoint
)

// DefaultMaxReworkCycles is the default limit for review→implement rework loops.
const DefaultMaxReworkCycles = 2

// EngineConfig holds everything needed to construct an Engine.
type EngineConfig struct {
	Pipeline             *PhasePipeline
	Loader               *PromptLoader
	Ticket               TicketData
	PromptConfig         PromptConfigData
	PromptContext        ContextData
	DetectedStack        DetectedStackData // auto-detected project stack info; zero value if detection was skipped
	Model                string
	BinaryVersion        string // binary build identifier; recorded in meta on first run, checked for staleness on resume
	WorkDir              string
	WorktreeBase         string
	BaseBranch           string
	MaxCostUSD           float64
	MaxCostPerPhase      float64       // per-phase cost cap; 0 means no per-phase limit
	MaxCostPerGeneration float64       // per-generation cost cap (ps.Cost); 0 means no per-generation limit
	MaxPipelineDuration  time.Duration // max wall-clock time for the entire pipeline; 0 means no limit
	MaxReworkCycles      int           // max review→implement rework loops; 0 means use default (2)
	Mode                 Mode
	OnEvent              func(Event)
	PauseSignal          <-chan bool // receives true=pause, false=resume from TUI; nil disables
	SleepFunc            func(time.Duration)
	JitterFunc           func(max time.Duration) time.Duration
	PRPoller             PRPoller          // for monitor phase polling; nil disables monitor
	NowFunc              func() time.Time  // for testability; defaults to time.Now
	AuthorityResolver    AuthorityResolver // for comment authority checks; nil → all authoritative
	MonitorProfile       *MonitorProfile   // behavioral profile; nil → use polling config as-is
	SelfUser             string            // PR author username for self-comment filtering
	BotUsers             []string          // known bot usernames to filter
}

// maxReworkCycles returns the configured max rework cycles, defaulting to DefaultMaxReworkCycles.
func (c *EngineConfig) maxReworkCycles() int {
	if c.MaxReworkCycles > 0 {
		return c.MaxReworkCycles
	}
	return DefaultMaxReworkCycles
}

// remoteName returns the configured git remote name from PromptConfig.Repo.PushTo,
// defaulting to "origin" when not set.
func (e *Engine) remoteName() string {
	if r := e.config.PromptConfig.Repo.PushTo; r != "" {
		return r
	}
	return "origin"
}

// Engine orchestrates a pipeline run, tying together the runner,
// state management, prompt rendering, and retry logic.
type Engine struct {
	runner           runner.Runner
	config           EngineConfig
	state            *State
	confirmCh        chan struct{}
	reranPhases      map[string]bool // phases that ran (not skipped) in this execution
	pauseMu          sync.Mutex
	paused           bool
	pauseCond        *sync.Cond
	inCheckpoint     bool      // true while blocked on <-confirmCh; guarded by pauseMu
	pipelineStart    time.Time // wall-clock time when applyPipelineTimeout was called
	pipelineDeadline time.Time // deadline set by applyPipelineTimeout; zero if no timeout
}

// NewEngine creates an Engine with sensible defaults for sleep and jitter.
// confirmCh is only created in Checkpoint mode. If PauseSignal is set,
// a goroutine drains it and blocks output streaming while paused.
func NewEngine(r runner.Runner, state *State, cfg EngineConfig) *Engine {
	if cfg.SleepFunc == nil {
		cfg.SleepFunc = time.Sleep
	}
	if cfg.JitterFunc == nil {
		cfg.JitterFunc = func(time.Duration) time.Duration { return 0 }
	}

	e := &Engine{
		runner: r,
		config: cfg,
		state:  state,
	}
	e.pauseCond = sync.NewCond(&e.pauseMu)

	if cfg.Mode == Checkpoint {
		e.confirmCh = make(chan struct{}, 1)
	}

	if cfg.PauseSignal != nil {
		go e.drainPauseSignal(cfg.PauseSignal)
	}
	return e
}

// drainPauseSignal reads from the pause channel and updates the paused flag.
// When a resume signal (false) arrives while the engine is blocked on a
// checkpoint (inCheckpoint), the method also sends to confirmCh to unblock
// the checkpoint wait — without this, the engine deadlocks because it waits
// on confirmCh, not pauseCond.
// When the channel is closed (TUI exits), the goroutine force-unpauses to
// unblock any waiters, preventing deadlock.
func (e *Engine) drainPauseSignal(ch <-chan bool) {
	for p := range ch {
		e.pauseMu.Lock()
		e.paused = p
		if !p {
			e.pauseCond.Broadcast()
			// If the engine is blocked on a checkpoint, unblock it.
			if e.inCheckpoint && e.confirmCh != nil {
				select {
				case e.confirmCh <- struct{}{}:
				default:
				}
			}
		}
		e.pauseMu.Unlock()
	}
	// Channel closed: force-unpause to unblock any waiters.
	e.pauseMu.Lock()
	e.paused = false
	e.pauseCond.Broadcast()
	// Also unblock checkpoint if blocked.
	if e.inCheckpoint && e.confirmCh != nil {
		select {
		case e.confirmCh <- struct{}{}:
		default:
		}
	}
	e.pauseMu.Unlock()
}

// waitIfPaused blocks until the engine is unpaused or context is cancelled.
// Returns ctx.Err() if context was cancelled while paused, nil otherwise.
func (e *Engine) waitIfPaused(ctx context.Context) error {
	e.pauseMu.Lock()
	defer e.pauseMu.Unlock()
	for e.paused {
		// Check context before waiting
		if err := ctx.Err(); err != nil {
			return err
		}
		// Use a goroutine to wake on context cancellation
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				e.pauseMu.Lock()
				e.pauseCond.Broadcast()
				e.pauseMu.Unlock()
			case <-done:
			}
		}()
		e.pauseCond.Wait()
		close(done)
	}
	return ctx.Err()
}

// checkBinaryVersion records the binary version in meta on first run and
// emits a warning event if the current binary differs from the version
// that originally created the pipeline state.
func (e *Engine) checkBinaryVersion() {
	current := e.config.BinaryVersion
	if current == "" {
		return
	}

	stored := e.state.Meta().BinaryVersion
	if stored == "" {
		// First run: record the current binary version.
		e.state.Meta().BinaryVersion = current
		return
	}

	if stored != current {
		e.emit(Event{
			Kind: EventBinaryVersionMismatch,
			Data: map[string]any{
				"stored_version":  stored,
				"current_version": current,
			},
		})
		// Update to current so we don't warn again on the next phase.
		e.state.Meta().BinaryVersion = current
	}
}

// ensureWorktree creates a worktree if one hasn't been created yet and
// WorktreeBase is configured. Called at the start of Run and Resume so
// every phase executes inside the worktree.
func (e *Engine) ensureWorktree(ctx context.Context) error {
	if e.state.Meta().Worktree != "" || e.config.WorktreeBase == "" {
		return nil
	}

	branch := "soda/" + e.config.Ticket.Key
	baseBranch := e.config.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	wtPath, err := git.CreateWorktree(ctx, e.config.WorkDir, e.config.WorktreeBase, branch, baseBranch)
	if err != nil {
		return fmt.Errorf("engine: create worktree: %w", err)
	}

	e.state.Meta().Worktree = wtPath
	e.state.Meta().Branch = branch
	e.emit(Event{
		Kind: EventWorktreeCreated,
		Data: map[string]any{"worktree": wtPath, "branch": branch},
	})
	return nil
}

// Run executes the full pipeline from the beginning, skipping completed phases.
func (e *Engine) Run(ctx context.Context) error {
	if err := e.state.AcquireLock(); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	defer e.state.ReleaseLock()

	// Apply pipeline-level timeout if configured.
	ctx, cancel := e.applyPipelineTimeout(ctx)
	defer cancel()

	// Record or check binary version for staleness detection.
	e.checkBinaryVersion()

	// Cache ticket summary in meta for soda sessions/history display.
	if e.state.Meta().Summary == "" && e.config.Ticket.Summary != "" {
		e.state.Meta().Summary = e.config.Ticket.Summary
	}

	if err := e.ensureWorktree(ctx); err != nil {
		return err
	}

	// Reset per-phase CumulativeCost so that stale costs from a prior
	// pipeline execution do not block per-phase budget enforcement.
	// Within this execution, CumulativeCost accumulates correctly
	// across rework generations via MarkRunning / AccumulateCost.
	if err := e.state.ResetPhaseCosts(); err != nil {
		return fmt.Errorf("engine: reset phase costs: %w", err)
	}
	e.emit(Event{Kind: EventPhaseCostsReset})

	e.reranPhases = make(map[string]bool)
	e.emit(Event{Kind: EventEngineStarted})

	if err := e.executePhases(ctx, e.config.Pipeline.Phases, false); err != nil {
		wrapped := e.wrapTimeoutError(ctx, err)
		var pte *PipelineTimeoutError
		if !errors.As(wrapped, &pte) {
			e.emit(Event{Kind: EventEngineFailed, Data: map[string]any{"error": wrapped.Error()}})
		}
		return wrapped
	}

	e.emit(Event{Kind: EventEngineCompleted})
	return nil
}

// Resume restarts the pipeline from the named phase, skipping prior completed phases.
func (e *Engine) Resume(ctx context.Context, fromPhase string) error {
	startIdx := -1
	for i, phase := range e.config.Pipeline.Phases {
		if phase.Name == fromPhase {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return fmt.Errorf("engine: phase %q not found in pipeline", fromPhase)
	}

	if err := e.state.AcquireLock(); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	defer e.state.ReleaseLock()

	// Apply pipeline-level timeout if configured.
	ctx, cancel := e.applyPipelineTimeout(ctx)
	defer cancel()

	// Check binary version for staleness detection on resume.
	e.checkBinaryVersion()

	// Cache ticket summary in meta for soda sessions/history display.
	if e.state.Meta().Summary == "" && e.config.Ticket.Summary != "" {
		e.state.Meta().Summary = e.config.Ticket.Summary
	}

	if err := e.ensureWorktree(ctx); err != nil {
		return err
	}

	e.reranPhases = make(map[string]bool)
	e.emit(Event{Kind: EventEngineStarted, Data: map[string]any{"resumed_from": fromPhase}})

	// The fromPhase (first in slice) is always re-run, even if completed.
	// Mark it with forceFirst=true so executePhases skips the shouldSkip check.
	if err := e.executePhases(ctx, e.config.Pipeline.Phases[startIdx:], true); err != nil {
		wrapped := e.wrapTimeoutError(ctx, err)
		var pte *PipelineTimeoutError
		if !errors.As(wrapped, &pte) {
			e.emit(Event{Kind: EventEngineFailed, Data: map[string]any{"error": wrapped.Error()}})
		}
		return wrapped
	}

	e.emit(Event{Kind: EventEngineCompleted})
	return nil
}

// executePhases runs a slice of phases, handling skip logic, checkpoint
// pauses, and review rework routing. When forceFirst is true, the first
// phase in the slice is always re-run regardless of completion status.
//
// Rework routing is handled iteratively: when a review phase produces a
// "rework" verdict, the outer loop calls routeRework to increment the
// rework cycle counter, re-slice the phases from the target, set
// forceFirst, and restart the inner range loop — avoiding recursion.
func (e *Engine) executePhases(ctx context.Context, phases []PhaseConfig, forceFirst bool) error {
	for {
		rework := false

		for idx, phase := range phases {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("engine: context cancelled: %w", err)
			}

			// Block between phases while paused.
			if err := e.waitIfPaused(ctx); err != nil {
				return fmt.Errorf("engine: context cancelled while paused: %w", err)
			}

			skipCheck := !(forceFirst && idx == 0)
			if skipCheck && e.shouldSkip(phase) {
				if err := e.gatePhase(phase); err != nil {
					// Handle rework signal on skipped phases — can occur on
					// Run() re-entry when a prior rework crashed mid-cycle.
					var reworkSig *reworkSignal
					if errors.As(err, &reworkSig) {
						// When a skipped phase produces a stale corrective
						// rework signal (e.g., verify FAIL → patch), reset
						// the phase to PhasePending so it re-runs instead
						// of routing back to the corrective phase. Without
						// this, Resume paths create a wasteful loop: patch
						// runs → verify skipped (stale FAIL, deps not
						// re-run) → rework to patch → PatchCycles++,
						// burning LLM budget on patch calls whose fixes
						// are never verified.
						if e.isCorrectivePhase(reworkSig.target) {
							if ps := e.state.Meta().Phases[phase.Name]; ps != nil {
								ps.Status = PhasePending
							}
							forceFirst = false
							rework = true
							break
						}
						// Non-corrective rework (e.g., review → implement):
						// route to the configured target.
						route, routeErr := e.routeRework(phase.Name, reworkSig)
						if routeErr != nil {
							return routeErr
						}
						phases = route.phases
						forceFirst = route.forceFirst
						rework = true
						break
					}
					return err
				}
				e.emit(Event{Phase: phase.Name, Kind: EventPhaseSkipped})
				continue
			}

			// Corrective phases are skipped in the forward pass — they
			// only run when routed to via reworkSignal (forceFirst).
			if phase.Type == "corrective" && skipCheck {
				e.emit(Event{Phase: phase.Name, Kind: EventCorrectiveSkipped})
				continue
			}

			// Skip-plan routing: when triage set skip_plan=true and the
			// ticket carries an ExistingPlan, write the plan artifact from
			// ticket data and mark the phase completed — no LLM call needed.
			if phase.Name == "plan" && e.triageRequestsSkipPlan() {
				if err := e.skipPlanFromTriage(); err != nil {
					return err
				}
				e.reranPhases[phase.Name] = true

				if e.config.Mode == Checkpoint {
					e.emit(Event{Phase: phase.Name, Kind: EventCheckpointPause})
					e.pauseMu.Lock()
					e.inCheckpoint = true
					e.pauseMu.Unlock()
					select {
					case <-e.confirmCh:
					case <-ctx.Done():
						e.pauseMu.Lock()
						e.inCheckpoint = false
						e.pauseMu.Unlock()
						return fmt.Errorf("engine: context cancelled during checkpoint: %w", ctx.Err())
					}
					e.pauseMu.Lock()
					e.inCheckpoint = false
					e.pauseMu.Unlock()
				}
				continue
			}

			// Post-submit phases are best-effort: skip if not needed,
			// swallow errors on failure.
			if phase.Type == "post-submit" {
				if e.shouldSkipPostSubmit(phase) {
					e.emit(Event{Phase: phase.Name, Kind: EventFollowUpSkipped, Data: map[string]any{"reason": "no minor findings"}})
					continue
				}
				if err := e.runPhase(ctx, phase); err != nil {
					e.emit(Event{Phase: phase.Name, Kind: EventFollowUpFailed, Data: map[string]any{"error": err.Error()}})
					// Mark completed despite failure — best-effort.
					_ = e.state.MarkCompleted(phase.Name)
					e.reranPhases[phase.Name] = true
					continue
				}
				e.reranPhases[phase.Name] = true
				continue
			}

			if err := e.runPhase(ctx, phase); err != nil {
				// Check for rework signal — route to configured target.
				var reworkSig *reworkSignal
				if errors.As(err, &reworkSig) {
					route, routeErr := e.routeRework(phase.Name, reworkSig)
					if routeErr != nil {
						return routeErr
					}
					phases = route.phases
					forceFirst = route.forceFirst
					rework = true
					break
				}
				return err
			}
			e.reranPhases[phase.Name] = true

			if e.config.Mode == Checkpoint {
				e.emit(Event{Phase: phase.Name, Kind: EventCheckpointPause})
				e.pauseMu.Lock()
				e.inCheckpoint = true
				e.pauseMu.Unlock()
				select {
				case <-e.confirmCh:
				case <-ctx.Done():
					e.pauseMu.Lock()
					e.inCheckpoint = false
					e.pauseMu.Unlock()
					return fmt.Errorf("engine: context cancelled during checkpoint: %w", ctx.Err())
				}
				e.pauseMu.Lock()
				e.inCheckpoint = false
				e.pauseMu.Unlock()
			}
		}

		if !rework {
			return nil
		}
	}
}

// Confirm unblocks the engine in Checkpoint mode.
func (e *Engine) Confirm() {
	if e.confirmCh != nil {
		e.confirmCh <- struct{}{}
	}
}

// runPhase executes a single phase including dependency checks, budget checks,
// worktree creation, prompt rendering, runner invocation with retries, and gating.
func (e *Engine) runPhase(ctx context.Context, phase PhaseConfig) error {
	// Polling phases are handled separately.
	if phase.Type == "polling" {
		return e.runMonitor(ctx, phase)
	}

	// Parallel-review phases dispatch multiple reviewers concurrently.
	if phase.Type == "parallel-review" {
		return e.runParallelReview(ctx, phase)
	}

	// Check dependencies.
	for _, dep := range phase.DependsOn {
		if !e.state.IsCompleted(dep) {
			return &DependencyNotMetError{Phase: phase.Name, Dependency: dep}
		}
	}

	// Check budget.
	if err := e.checkBudget(phase); err != nil {
		return err
	}

	// Pre-run per-phase budget check: prevent starting a new generation
	// when cumulative cost already exceeds (or meets) the per-phase limit.
	if err := e.checkPhaseBudget(phase); err != nil {
		return err
	}

	// Mark phase running and notify callback.
	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: mark running %s: %w", phase.Name, err)
	}
	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted, Data: map[string]any{"generation": e.state.Meta().Phases[phase.Name].Generation}})

	// Build prompt data and render template.
	promptData, err := e.buildPromptData(phase)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: build prompt data for %s: %w", phase.Name, err)
	}

	// Inject diff context for corrective phases so the patch prompt
	// can see what was implemented without reading the plan artifact.
	if phase.Type == "corrective" {
		promptData.DiffContext = e.computeDiffContext(ctx)
	}

	// Store plan hash for staleness guard on phases that depend on plan.
	for _, dep := range phase.DependsOn {
		if dep == "plan" {
			if h := e.computePlanHash(); h != "" {
				e.state.Meta().Phases[phase.Name].PlanHash = h
			}
			break
		}
	}

	loadResult, err := e.config.Loader.LoadWithSource(phase.Prompt)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: load template for %s: %w", phase.Name, err)
	}

	// Emit source info so operators can see which template was used.
	promptEvent := Event{
		Phase: phase.Name,
		Kind:  EventPromptLoaded,
		Data: map[string]any{
			"source":      loadResult.Source,
			"is_override": loadResult.IsOverride,
		},
	}
	if loadResult.Fallback {
		promptEvent.Data["fallback"] = true
		promptEvent.Data["fallback_reason"] = loadResult.FallbackReason
	}
	e.emit(promptEvent)

	rendered, err := RenderPrompt(loadResult.Content, promptData)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: render prompt for %s: %w", phase.Name, err)
	}

	_ = e.state.WriteLog(phase.Name, "prompt", []byte(rendered))

	// Build runner opts. Tighten budget to the smallest remaining limit.
	remaining := e.config.MaxCostUSD - e.state.Meta().TotalCost
	if e.config.MaxCostUSD <= 0 {
		remaining = 0 // no budget enforcement
	}
	// Cap with per-phase limit: use the remaining per-phase budget
	// (MaxCostPerPhase minus cumulative cost already spent) as the tighter bound.
	if e.config.MaxCostPerPhase > 0 {
		perPhaseRemaining := e.config.MaxCostPerPhase - e.state.Meta().Phases[phase.Name].CumulativeCost
		if remaining <= 0 || perPhaseRemaining < remaining {
			remaining = perPhaseRemaining
		}
	}
	// Cap with per-generation limit: use the remaining generation budget
	// (MaxCostPerGeneration minus current generation cost) as the tighter bound.
	if e.config.MaxCostPerGeneration > 0 {
		genRemaining := e.config.MaxCostPerGeneration - e.state.Meta().Phases[phase.Name].Cost
		if remaining <= 0 || genRemaining < remaining {
			remaining = genRemaining
		}
	}
	// Prefer per-phase model if set, otherwise use the global model.
	model := e.config.Model
	if phase.Model != "" {
		model = phase.Model
	}
	opts := runner.RunOpts{
		Phase:        phase.Name,
		SystemPrompt: rendered,
		UserPrompt:   "Execute the task described in the system prompt.",
		OutputSchema: phase.Schema,
		AllowedTools: phase.Tools,
		MaxBudgetUSD: remaining,
		WorkDir:      e.workDir(phase),
		Model:        model,
		Timeout:      phase.Timeout.Duration,
		OnChunk:      e.makeOnChunk(ctx, phase.Name),
	}

	// Run with retry.
	result, err := e.runWithRetry(ctx, phase, opts)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return err
	}

	// Record results.
	if result.Output != nil {
		if err := e.state.WriteResult(phase.Name, result.Output); err != nil {
			return fmt.Errorf("engine: write result for %s: %w", phase.Name, err)
		}
	}
	if result.RawText != "" {
		if err := e.state.WriteArtifact(phase.Name, []byte(result.RawText)); err != nil {
			return fmt.Errorf("engine: write artifact for %s: %w", phase.Name, err)
		}
	}
	if err := e.state.AccumulateCost(phase.Name, result.CostUSD); err != nil {
		return fmt.Errorf("engine: accumulate cost for %s: %w", phase.Name, err)
	}

	// Per-phase cost enforcement: abort if this phase exceeded its budget.
	if err := e.checkPhaseBudget(phase); err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return err
	}

	// Mark completed and notify callback.
	if err := e.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("engine: mark completed %s: %w", phase.Name, err)
	}
	completedData := map[string]any{
		"duration_ms": e.state.Meta().Phases[phase.Name].DurationMs,
		"cost":        e.state.Meta().Phases[phase.Name].Cost,
	}
	if result.Output != nil {
		if summary := progress.PhaseSummary(phase.Name, result.Output); summary != "" {
			completedData["summary"] = summary
		}
	}
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPhaseCompleted,
		Data:  completedData,
	})

	// Domain gating.
	return e.gatePhase(phase)
}

// runWithRetry runs the phase with per-category retry limits.
func (e *Engine) runWithRetry(ctx context.Context, phase PhaseConfig, opts runner.RunOpts) (*runner.RunResult, error) {
	remaining := map[string]int{
		"transient": phase.Retry.Transient,
		"parse":     phase.Retry.Parse,
		"semantic":  phase.Retry.Semantic,
	}

	attempt := 0
	for {
		result, err := e.runner.Run(ctx, opts)
		if err == nil {
			return result, nil
		}

		category := classifyError(err)

		left, tracked := remaining[category]
		if !tracked || left <= 0 {
			return nil, fmt.Errorf("engine: phase %s failed (%s, no retries left): %w", phase.Name, category, err)
		}
		remaining[category]--

		switch category {
		case "transient":
			delay := backoff(attempt, e.config.JitterFunc)
			e.config.SleepFunc(delay)
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseRetrying,
				Data:  map[string]any{"category": category, "attempt": attempt + 1, "delay": delay.String()},
			})

		case "parse":
			var pe *runner.ParseError
			if errors.As(err, &pe) {
				opts.UserPrompt = opts.UserPrompt + "\n\n[RETRY] Previous attempt failed with parse error: " + pe.Error() + "\nPlease fix the output format."
			}
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseRetrying,
				Data:  map[string]any{"category": category, "attempt": attempt + 1},
			})

		case "semantic":
			var se *runner.SemanticError
			if errors.As(err, &se) {
				opts.UserPrompt = opts.UserPrompt + "\n\n[RETRY] Previous attempt returned a semantic error: " + se.Message + "\nPlease address this issue."
			}
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseRetrying,
				Data:  map[string]any{"category": category, "attempt": attempt + 1},
			})
		}

		_ = e.state.WriteLog(phase.Name, fmt.Sprintf("retry_%d", attempt+1),
			[]byte(fmt.Sprintf("category=%s err=%s", category, err)))

		attempt++
	}
}

// classifyError maps an error to a retry category using agent-agnostic
// runner error types. Backend runners (Claude, sandbox) are responsible
// for wrapping their specific errors into runner.* types before returning.
func classifyError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context"
	}
	var te *runner.TransientError
	if errors.As(err, &te) {
		return "transient"
	}
	var pe *runner.ParseError
	if errors.As(err, &pe) {
		return "parse"
	}
	var se *runner.SemanticError
	if errors.As(err, &se) {
		return "semantic"
	}
	return "unknown"
}

// backoff returns an exponential backoff duration capped at 30s, plus jitter.
func backoff(attempt int, jitterFunc func(time.Duration) time.Duration) time.Duration {
	base := 2 * time.Second
	exp := time.Duration(math.Pow(2, float64(attempt))) * base
	if exp > 30*time.Second {
		exp = 30 * time.Second
	}
	return exp + jitterFunc(time.Second)
}

// sleepWithContext runs SleepFunc(d) but returns early if ctx is cancelled.
// It delegates to e.config.SleepFunc in a goroutine so that test no-op sleeps
// complete instantly while production sleeps remain cancellable.
func (e *Engine) sleepWithContext(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		e.config.SleepFunc(d)
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// buildPromptData constructs the PromptData for a phase from its dependencies.
func (e *Engine) buildPromptData(phase PhaseConfig) (PromptData, error) {
	data := PromptData{
		Ticket:        e.config.Ticket,
		Config:        e.config.PromptConfig,
		Context:       e.config.PromptContext,
		DetectedStack: e.config.DetectedStack,
		WorktreePath:  e.state.Meta().Worktree,
		Branch:        e.state.Meta().Branch,
		BaseBranch:    e.config.BaseBranch,
	}

	for _, dep := range phase.DependsOn {
		artifact, err := e.state.ReadArtifact(dep)
		if err != nil {
			// Not all deps produce artifacts; skip if not found.
			continue
		}
		content := string(artifact)

		switch dep {
		case "triage":
			data.Artifacts.Triage = content
		case "plan":
			data.Artifacts.Plan = content
		case "implement":
			data.Artifacts.Implement = content
		case "verify":
			data.Artifacts.Verify = content
		case "review":
			data.Artifacts.Review = content
		case "patch":
			data.Artifacts.Patch = content
		case "submit":
			data.Artifacts.Submit.PRURL = e.extractPRURL()
		}
	}

	// Inject rework feedback from configured sources. The FeedbackFrom
	// list is read from the phase's own config. Sources are tried in
	// priority order; the first one that produces feedback wins.
	//
	// On patch retry (rework cycle), this block re-extracts feedback
	// from the latest verify/review result on disk — previous feedback
	// is NOT carried over. Each extractor reads the current result file,
	// so after verify/review re-run and overwrite their results, the
	// next rework cycle sees only the new failures. See ReworkFeedback
	// doc comment for the full reset lifecycle.
	if sources := phase.FeedbackFrom; len(sources) > 0 {
		for _, source := range sources {
			if fb := e.extractFeedbackFrom(source); fb != nil {
				data.ReworkFeedback = fb
				eventData := map[string]any{
					"source":  fb.Source,
					"verdict": fb.Verdict,
				}
				switch fb.Source {
				case "review":
					eventData["review_findings"] = len(fb.ReviewFindings)
				case "verify":
					eventData["fixes_count"] = len(fb.FixesRequired)
					eventData["failed_criteria"] = len(fb.FailedCriteria)
					eventData["code_issues"] = len(fb.CodeIssues)
					eventData["failed_commands"] = len(fb.FailedCommands)
				}
				e.emit(Event{
					Phase: phase.Name,
					Kind:  EventReworkFeedbackInjected,
					Data:  eventData,
				})
				break
			}
		}
	}

	return data, nil
}

// computeDiffContext returns the git diff of the current branch against the
// base branch. Used by corrective phases to see what was implemented.
// Returns an empty string on error (non-fatal).
func (e *Engine) computeDiffContext(ctx context.Context) string {
	workDir := e.workDir(PhaseConfig{})
	baseBranch := e.config.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	diffCtx, err := git.Diff(ctx, workDir, e.remoteName()+"/"+baseBranch, 50000)
	if err != nil {
		return ""
	}
	return diffCtx
}

// computePlanHash returns the SHA-256 hex digest of the plan artifact.
func (e *Engine) computePlanHash() string {
	artifact, err := e.state.ReadArtifact("plan")
	if err != nil {
		return ""
	}
	h := sha256.Sum256(artifact)
	return fmt.Sprintf("%x", h)
}

// extractPRURL reads the submit result and extracts the pr_url field.
func (e *Engine) extractPRURL() string {
	raw, err := e.state.ReadResult("submit")
	if err != nil {
		return ""
	}
	var result struct {
		PRURL string `json:"pr_url"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ""
	}
	return result.PRURL
}

// reviewerResult holds the outcome of a single reviewer subagent.
type reviewerResult struct {
	Name     string
	Findings []schemas.ReviewFinding
	Cost     float64
	Err      error
}

// reviewerMsg is sent from reviewer goroutines to the parent via channel.
type reviewerMsg struct {
	Event  *Event          // emit this event (nil if not an event)
	Log    *reviewerLog    // write this log (nil if not a log)
	Result *reviewerResult // final result (nil if not done)
	Index  int
}

// reviewerLog holds data for a deferred WriteLog call.
type reviewerLog struct {
	Phase   string
	Name    string
	Content []byte
}

// loadPriorReview reads and parses the archived review result from the
// previous generation. Returns nil when there is no prior generation (first
// run) or the archived result cannot be read/parsed. On read/parse errors
// an event is emitted so operators can detect the failure; the caller should
// treat a nil return as "no prior data available" and run all reviewers
// (safe fallback).
func (e *Engine) loadPriorReview(phaseName string) *schemas.ReviewOutput {
	ps := e.state.Meta().Phases[phaseName]
	if ps == nil || ps.Generation <= 1 {
		return nil
	}

	raw, err := e.state.ReadArchivedResult(phaseName, ps.Generation-1)
	if err != nil {
		e.emit(Event{
			Phase: phaseName,
			Kind:  "prior_review_warning",
			Data:  map[string]any{"warning": "failed to read archived review result", "error": err.Error()},
		})
		return nil
	}

	var prevReview schemas.ReviewOutput
	if err := json.Unmarshal(raw, &prevReview); err != nil {
		e.emit(Event{
			Phase: phaseName,
			Kind:  "prior_review_warning",
			Data:  map[string]any{"warning": "failed to unmarshal archived review result", "error": err.Error()},
		})
		return nil
	}

	return &prevReview
}

// neededReviewersFromPrior derives the set of reviewer names that produced at
// least one critical or major finding from a prior review result. Returns nil
// when prev is nil (first run or load failure), meaning all reviewers should
// run.
func neededReviewersFromPrior(prev *schemas.ReviewOutput) map[string]bool {
	if prev == nil {
		return nil
	}
	needed := make(map[string]bool)
	for _, finding := range prev.Findings {
		sev := strings.ToLower(finding.Severity)
		if sev == "critical" || sev == "major" {
			needed[finding.Source] = true
		}
	}
	return needed
}

// priorFindingsForReviewer returns the findings from a prior review result
// that belong to the given reviewer. This is used to carry forward minor
// findings when a reviewer is skipped on rework cycles, ensuring they are
// not silently dropped from the merged output.
func priorFindingsForReviewer(prev *schemas.ReviewOutput, reviewerName string) []schemas.ReviewFinding {
	if prev == nil {
		return nil
	}
	var findings []schemas.ReviewFinding
	for _, f := range prev.Findings {
		if f.Source == reviewerName {
			findings = append(findings, f)
		}
	}
	return findings
}

// runParallelReview dispatches specialist reviewer subagents in parallel,
// collects their findings, merges them into a single ReviewOutput, and
// computes a verdict.
func (e *Engine) runParallelReview(ctx context.Context, phase PhaseConfig) error {
	if len(phase.Reviewers) == 0 {
		return fmt.Errorf("engine: parallel-review phase %s has no reviewers configured", phase.Name)
	}

	// Check dependencies.
	for _, dep := range phase.DependsOn {
		if !e.state.IsCompleted(dep) {
			return &DependencyNotMetError{Phase: phase.Name, Dependency: dep}
		}
	}

	// Check budget.
	if err := e.checkBudget(phase); err != nil {
		return err
	}

	// Pre-run per-phase budget check: prevent starting a new generation
	// when cumulative cost already exceeds (or meets) the per-phase limit.
	if err := e.checkPhaseBudget(phase); err != nil {
		return err
	}

	// Mark phase running.
	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: mark running %s: %w", phase.Name, err)
	}
	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted, Data: map[string]any{"generation": e.state.Meta().Phases[phase.Name].Generation}})

	// Build prompt data shared by all reviewers.
	promptData, err := e.buildPromptData(phase)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: build prompt data for %s: %w", phase.Name, err)
	}

	// Snapshot budget before launching goroutines — avoid concurrent Meta() reads.
	budgetRemaining := 0.0
	if e.config.MaxCostUSD > 0 {
		budgetRemaining = e.config.MaxCostUSD - e.state.Meta().TotalCost
	}
	// Cap with per-phase limit: use the remaining per-phase budget
	// (MaxCostPerPhase minus cumulative cost already spent) as the tighter bound.
	if e.config.MaxCostPerPhase > 0 {
		perPhaseRemaining := e.config.MaxCostPerPhase - e.state.Meta().Phases[phase.Name].CumulativeCost
		if budgetRemaining <= 0 || perPhaseRemaining < budgetRemaining {
			budgetRemaining = perPhaseRemaining
		}
	}
	// Cap with per-generation limit.
	if e.config.MaxCostPerGeneration > 0 {
		genRemaining := e.config.MaxCostPerGeneration - e.state.Meta().Phases[phase.Name].Cost
		if budgetRemaining <= 0 || genRemaining < budgetRemaining {
			budgetRemaining = genRemaining
		}
	}

	// On rework cycles, load the prior review result once and derive which
	// reviewers can be skipped. A reviewer is redundant if it had no
	// critical/major findings in the previous review — only reviewers that
	// flagged actionable issues need to re-verify the fixes.
	//
	// loadPriorReview returns nil on the first run or on load failure; in
	// both cases neededReviewers will be nil and all reviewers run (safe
	// fallback that avoids silently losing findings).
	priorReview := e.loadPriorReview(phase.Name)
	neededReviewers := neededReviewersFromPrior(priorReview)

	// Channel for reviewer goroutines to send messages to the parent.
	msgCh := make(chan reviewerMsg, len(phase.Reviewers)*10)

	// Dispatch reviewers in parallel, skipping redundant ones.
	var wg sync.WaitGroup
	results := make([]reviewerResult, len(phase.Reviewers))

	for idx, reviewer := range phase.Reviewers {
		if idx > 0 && phase.ReviewerStagger.Duration > 0 {
			e.config.SleepFunc(phase.ReviewerStagger.Duration)
		}

		// Skip reviewers that had no critical/major findings in the prior
		// cycle. neededReviewers is nil on the first run, so all run.
		if neededReviewers != nil && !neededReviewers[reviewer.Name] {
			// Carry forward minor findings from the prior cycle so they are
			// not silently dropped from the merged output. Without this, the
			// verdict could incorrectly become "pass" instead of
			// "pass-with-follow-ups", causing the post-submit follow-up
			// phase to be skipped.
			carried := priorFindingsForReviewer(priorReview, reviewer.Name)
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventReviewerSkipped,
				Data:  map[string]any{"reviewer": reviewer.Name, "reason": "no critical/major findings in prior cycle", "carried_findings": len(carried)},
			})
			results[idx] = reviewerResult{Name: reviewer.Name, Findings: carried}
			continue
		}
		wg.Add(1)
		go func(idx int, reviewer ReviewerConfig) {
			defer wg.Done()
			e.runReviewer(ctx, phase, reviewer, promptData, budgetRemaining, idx, msgCh)
		}(idx, reviewer)
	}

	// Close channel once all goroutines finish.
	go func() {
		wg.Wait()
		close(msgCh)
	}()

	// Drain channel in the parent goroutine — all State access is serialized here.
	for msg := range msgCh {
		if msg.Event != nil {
			if msg.Event.Kind == EventOutputChunk {
				e.emitChunk(*msg.Event)
			} else {
				e.emit(*msg.Event)
			}
		}
		if msg.Log != nil {
			_ = e.state.WriteLog(msg.Log.Phase, "prompt_"+msg.Log.Name, msg.Log.Content)
		}
		if msg.Result != nil {
			results[msg.Index] = *msg.Result
		}
	}

	// Check for context cancellation first.
	if err := ctx.Err(); err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: context cancelled during %s: %w", phase.Name, err)
	}

	// Partition results into successes and failures.
	var successResults []reviewerResult
	var reviewErrors []string
	for _, result := range results {
		if result.Err != nil {
			reviewErrors = append(reviewErrors, fmt.Sprintf("%s: %v", result.Name, result.Err))
		} else {
			successResults = append(successResults, result)
		}
	}

	// Determine how many reviewers must succeed.
	// MinReviewers == 0 means all reviewers are required (backwards compatible).
	minRequired := phase.MinReviewers
	if minRequired <= 0 {
		minRequired = len(phase.Reviewers)
	}

	if len(successResults) < minRequired {
		combinedErr := fmt.Errorf("engine: reviewer failures in %s: %s", phase.Name, strings.Join(reviewErrors, "; "))
		_ = e.state.MarkFailed(phase.Name, combinedErr)
		e.emitPhaseFailed(phase.Name, combinedErr)
		return combinedErr
	}

	// Emit partial-failure warnings for tolerated reviewer errors.
	for _, result := range results {
		if result.Err != nil {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventReviewerPartialFailure,
				Data: map[string]any{
					"reviewer": result.Name,
					"error":    result.Err.Error(),
				},
			})
		}
	}

	// Merge findings from successful reviewers only.
	merged := e.mergeReviewFindings(phase, successResults)

	// Serialize and store results.
	output, err := json.Marshal(merged)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: marshal review output for %s: %w", phase.Name, err)
	}

	if err := e.state.WriteResult(phase.Name, json.RawMessage(output)); err != nil {
		return fmt.Errorf("engine: write result for %s: %w", phase.Name, err)
	}

	// Build a human-readable artifact from the merged findings.
	artifact := e.buildReviewArtifact(merged)
	if err := e.state.WriteArtifact(phase.Name, []byte(artifact)); err != nil {
		return fmt.Errorf("engine: write artifact for %s: %w", phase.Name, err)
	}

	// Accumulate cost from ALL reviewers (including failed ones,
	// since they may have incurred partial cost before failing).
	totalCost := 0.0
	for _, result := range results {
		totalCost += result.Cost
	}
	if err := e.state.AccumulateCost(phase.Name, totalCost); err != nil {
		return fmt.Errorf("engine: accumulate cost for %s: %w", phase.Name, err)
	}

	// Per-phase cost enforcement.
	if err := e.checkPhaseBudget(phase); err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return err
	}

	// Mark completed.
	if err := e.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("engine: mark completed %s: %w", phase.Name, err)
	}
	reviewCompletedData := map[string]any{
		"duration_ms": e.state.Meta().Phases[phase.Name].DurationMs,
		"cost":        e.state.Meta().Phases[phase.Name].Cost,
	}
	if summary := progress.PhaseSummary(phase.Name, json.RawMessage(output)); summary != "" {
		reviewCompletedData["summary"] = summary
	}
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPhaseCompleted,
		Data:  reviewCompletedData,
	})

	// Domain gating.
	return e.gatePhase(phase)
}

// runReviewer executes a single specialist reviewer, sending events and results
// through msgCh to avoid concurrent State access. budgetRemaining is a snapshot
// taken before goroutines launch.
func (e *Engine) runReviewer(ctx context.Context, phase PhaseConfig, reviewer ReviewerConfig, promptData PromptData, budgetRemaining float64, idx int, msgCh chan<- reviewerMsg) {
	sendEvent := func(evt Event) {
		msgCh <- reviewerMsg{Event: &evt, Index: idx}
	}
	sendResult := func(res reviewerResult) {
		msgCh <- reviewerMsg{Result: &res, Index: idx}
	}

	sendEvent(Event{
		Phase: phase.Name,
		Kind:  EventReviewerStarted,
		Data:  map[string]any{"reviewer": reviewer.Name, "focus": reviewer.Focus},
	})

	// Load and render the reviewer's prompt template.
	loadResult, err := e.config.Loader.LoadWithSource(reviewer.Prompt)
	if err != nil {
		sendEvent(Event{
			Phase: phase.Name,
			Kind:  EventReviewerFailed,
			Data:  map[string]any{"reviewer": reviewer.Name, "error": err.Error()},
		})
		sendResult(reviewerResult{Name: reviewer.Name, Err: fmt.Errorf("load template %s: %w", reviewer.Prompt, err)})
		return
	}

	rendered, err := RenderPrompt(loadResult.Content, promptData)
	if err != nil {
		sendEvent(Event{
			Phase: phase.Name,
			Kind:  EventReviewerFailed,
			Data:  map[string]any{"reviewer": reviewer.Name, "error": err.Error()},
		})
		sendResult(reviewerResult{Name: reviewer.Name, Err: fmt.Errorf("render prompt for %s: %w", reviewer.Name, err)})
		return
	}

	// Send log to parent for serialized WriteLog.
	msgCh <- reviewerMsg{Log: &reviewerLog{Phase: phase.Name, Name: reviewer.Name, Content: []byte(rendered)}, Index: idx}

	// Use a reviewer-specific OnChunk that routes events through msgCh
	// to maintain the serialization contract — all events flow through
	// the channel to avoid concurrent State/callback access.
	// The waitIfPaused error is deliberately discarded; see makeOnChunk comment.
	onChunk := func(line string) {
		msgCh <- reviewerMsg{Event: &Event{
			Phase: phase.Name,
			Kind:  EventOutputChunk,
			Data:  map[string]any{"line": line},
		}, Index: idx}
		_ = e.waitIfPaused(ctx) // error deliberately discarded; see makeOnChunk comment
	}

	// Use the parent phase's schema for the reviewer findings.
	// Prefer per-reviewer model if set, otherwise use the global model.
	model := e.config.Model
	if reviewer.Model != "" {
		model = reviewer.Model
	}

	opts := runner.RunOpts{
		Phase:        phase.Name + "/" + reviewer.Name,
		SystemPrompt: rendered,
		UserPrompt:   "Execute the review described in the system prompt.",
		OutputSchema: phase.Schema,
		AllowedTools: phase.Tools,
		MaxBudgetUSD: budgetRemaining,
		WorkDir:      e.workDir(phase),
		Model:        model,
		Timeout:      phase.Timeout.Duration,
		OnChunk:      onChunk,
	}

	// Run with per-reviewer retry logic using the phase's RetryConfig.
	result, err := e.runReviewerWithRetry(ctx, phase, reviewer, opts, idx, msgCh)
	if err != nil {
		sendEvent(Event{
			Phase: phase.Name,
			Kind:  EventReviewerFailed,
			Data:  map[string]any{"reviewer": reviewer.Name, "error": err.Error()},
		})
		sendResult(reviewerResult{Name: reviewer.Name, Err: fmt.Errorf("run %s: %w", reviewer.Name, err)})
		return
	}

	// Parse findings from the structured output.
	var findings []schemas.ReviewFinding
	if result.Output != nil {
		var parsed struct {
			Findings []schemas.ReviewFinding `json:"findings"`
		}
		if parseErr := json.Unmarshal(result.Output, &parsed); parseErr != nil {
			sendEvent(Event{
				Phase: phase.Name,
				Kind:  EventReviewerParseWarning,
				Data:  map[string]any{"reviewer": reviewer.Name, "error": parseErr.Error()},
			})
		} else {
			findings = parsed.Findings
		}
	}

	sendEvent(Event{
		Phase: phase.Name,
		Kind:  EventReviewerCompleted,
		Data: map[string]any{
			"reviewer":       reviewer.Name,
			"findings_count": len(findings),
			"cost":           result.CostUSD,
		},
	})

	sendResult(reviewerResult{
		Name:     reviewer.Name,
		Findings: findings,
		Cost:     result.CostUSD,
	})
}

// runReviewerWithRetry runs the reviewer's runner call with per-category retry
// limits from the phase's RetryConfig. Only transient errors (429, timeout) are
// retried at the reviewer level with backoff. Parse, semantic, context, and
// unknown errors are immediately returned as failures without retry.
func (e *Engine) runReviewerWithRetry(ctx context.Context, phase PhaseConfig, reviewer ReviewerConfig, opts runner.RunOpts, idx int, msgCh chan<- reviewerMsg) (*runner.RunResult, error) {
	remaining := map[string]int{
		"transient": phase.Retry.Transient,
	}

	sendEvent := func(evt Event) {
		msgCh <- reviewerMsg{Event: &evt, Index: idx}
	}

	attempt := 0
	for {
		result, err := e.runner.Run(ctx, opts)
		if err == nil {
			return result, nil
		}

		category := classifyError(err)

		left, tracked := remaining[category]
		if !tracked || left <= 0 {
			return nil, fmt.Errorf("reviewer %s failed (%s, no retries left): %w", reviewer.Name, category, err)
		}
		remaining[category]--

		switch category {
		case "transient":
			delay := backoff(attempt, e.config.JitterFunc)
			sendEvent(Event{
				Phase: phase.Name,
				Kind:  EventReviewerRetrying,
				Data: map[string]any{
					"reviewer": reviewer.Name,
					"category": category,
					"attempt":  attempt + 1,
					"delay":    delay.String(),
				},
			})
			if err := e.sleepWithContext(ctx, delay); err != nil {
				return nil, fmt.Errorf("reviewer %s retry interrupted: %w", reviewer.Name, err)
			}
		}

		// Send retry log to parent for serialized WriteLog.
		msgCh <- reviewerMsg{
			Log: &reviewerLog{
				Phase:   phase.Name,
				Name:    fmt.Sprintf("%s_retry_%d", reviewer.Name, attempt+1),
				Content: []byte(fmt.Sprintf("category=%s err=%s", category, err)),
			},
			Index: idx,
		}

		attempt++
	}
}

// mergeReviewFindings combines findings from all reviewers and computes a verdict.
func (e *Engine) mergeReviewFindings(phase PhaseConfig, results []reviewerResult) schemas.ReviewOutput {
	var allFindings []schemas.ReviewFinding

	for _, result := range results {
		for _, finding := range result.Findings {
			finding.Source = result.Name
			allFindings = append(allFindings, finding)
		}
	}

	verdict := computeReviewVerdict(allFindings)

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventReviewMerged,
		Data: map[string]any{
			"findings_count": len(allFindings),
			"verdict":        verdict,
		},
	})

	return schemas.ReviewOutput{
		TicketKey: e.config.Ticket.Key,
		Findings:  allFindings,
		Verdict:   verdict,
	}
}

// computeReviewVerdict determines the review verdict based on finding severities.
// Any critical or major finding → "rework"
// Only minor findings → "pass-with-follow-ups"
// No findings → "pass"
func computeReviewVerdict(findings []schemas.ReviewFinding) string {
	hasCriticalOrMajor := false
	hasMinor := false

	for _, finding := range findings {
		sev := strings.ToLower(finding.Severity)
		switch sev {
		case "critical", "major":
			hasCriticalOrMajor = true
		case "minor":
			hasMinor = true
		}
	}

	if hasCriticalOrMajor {
		return "rework"
	}
	if hasMinor {
		return "pass-with-follow-ups"
	}
	return "pass"
}

// buildReviewArtifact creates a human-readable markdown summary of the review.
func (e *Engine) buildReviewArtifact(merged schemas.ReviewOutput) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Review: %s\n\n", merged.Verdict))
	sb.WriteString(fmt.Sprintf("Ticket: %s\n", merged.TicketKey))
	sb.WriteString(fmt.Sprintf("Verdict: %s\n", merged.Verdict))
	sb.WriteString(fmt.Sprintf("Total findings: %d\n\n", len(merged.Findings)))

	if len(merged.Findings) == 0 {
		sb.WriteString("No issues found.\n")
		return sb.String()
	}

	for idx, finding := range merged.Findings {
		sb.WriteString(fmt.Sprintf("## Finding %d: %s (%s)\n\n", idx+1, finding.Severity, finding.Source))
		if finding.File != "" {
			if finding.Line > 0 {
				sb.WriteString(fmt.Sprintf("- **File**: %s:%d\n", finding.File, finding.Line))
			} else {
				sb.WriteString(fmt.Sprintf("- **File**: %s\n", finding.File))
			}
		}
		sb.WriteString(fmt.Sprintf("- **Issue**: %s\n", finding.Issue))
		if finding.Suggestion != "" {
			sb.WriteString(fmt.Sprintf("- **Suggestion**: %s\n", finding.Suggestion))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// now returns the current time, using NowFunc if configured for testability.
func (e *Engine) now() time.Time {
	if e.config.NowFunc != nil {
		return e.config.NowFunc()
	}
	return time.Now()
}

// workDir returns the working directory for a phase, preferring the worktree
// if one has been created.
func (e *Engine) workDir(phase PhaseConfig) string {
	if wt := e.state.Meta().Worktree; wt != "" {
		return wt
	}
	return e.config.WorkDir
}

// makeOnChunk returns a callback that emits EventOutputChunk events and
// blocks while the engine is paused. The ctx parameter allows the callback
// to unblock when the engine context is cancelled (e.g., Ctrl+C, timeout),
// preventing deadlocks when paused.
//
// NOTE: waitIfPaused may return a context-cancellation error, which is
// deliberately discarded here. The OnChunk signature (func(string)) cannot
// propagate errors, and the runner's own context check will detect the
// cancellation on its next iteration. The pause applies backpressure to
// the subprocess via the stdout pipe buffer (~64KB on Linux): when the
// callback blocks, the scanner blocks, and the subprocess blocks on write.
func (e *Engine) makeOnChunk(ctx context.Context, phase string) func(string) {
	return func(line string) {
		e.emitChunk(Event{
			Phase: phase,
			Kind:  EventOutputChunk,
			Data:  map[string]any{"line": line},
		})
		_ = e.waitIfPaused(ctx) // error deliberately discarded; see comment above
	}
}

// emit logs an event to state and calls the OnEvent callback if set.
func (e *Engine) emit(event Event) {
	_ = e.state.LogEvent(event)
	if e.config.OnEvent != nil {
		e.config.OnEvent(event)
	}
}

// emitChunk forwards an event to the OnEvent callback without writing it
// to events.jsonl. Output chunks are high-frequency, transient streaming
// data that would inflate the durable log with no diagnostic value.
func (e *Engine) emitChunk(event Event) {
	if e.config.OnEvent != nil {
		e.config.OnEvent(event)
	}
}

// applyPipelineTimeout wraps ctx with a deadline when MaxPipelineDuration is
// configured. It stores pipelineStart and pipelineDeadline so wrapTimeoutError
// can compute actual elapsed time and distinguish the pipeline's own deadline
// from an external parent context deadline. Returns the (possibly wrapped)
// context and a cancel function that must always be deferred.
func (e *Engine) applyPipelineTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.config.MaxPipelineDuration <= 0 {
		return ctx, func() {}
	}
	now := e.now()
	e.pipelineStart = now
	e.pipelineDeadline = now.Add(e.config.MaxPipelineDuration)
	return context.WithDeadline(ctx, e.pipelineDeadline)
}

// wrapTimeoutError checks whether err is a context deadline exceeded caused
// by the pipeline's own timeout (not an external parent context deadline).
// If so, it emits an EventPipelineTimeout event and returns a
// PipelineTimeoutError with actual elapsed time. Otherwise it returns err
// unchanged.
//
// To distinguish the pipeline's deadline from an external caller's deadline,
// the method compares ctx.Deadline() against e.pipelineDeadline. If they
// don't match (within a small tolerance), the deadline came from an external
// source (e.g., HTTP handler, CI timeout) and the error is returned as-is.
func (e *Engine) wrapTimeoutError(ctx context.Context, err error) error {
	if e.config.MaxPipelineDuration <= 0 {
		return err
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	// Guard: only wrap if the deadline that fired is the pipeline's own.
	// An external parent context with a shorter deadline would produce a
	// different deadline value, and wrapping that as PipelineTimeoutError
	// would produce misleading diagnostics. Per-phase timeouts create a
	// child context with an earlier deadline, so they also won't match.
	// An external parent context with a shorter deadline would produce a
	// different deadline value, and wrapping that as PipelineTimeoutError
	// would produce misleading diagnostics.
	if e.pipelineDeadline.IsZero() {
		return err
	}
	if ctxDeadline, ok := ctx.Deadline(); ok {
		// Allow 1s tolerance for clock jitter between WithDeadline creation
		// and this check.
		diff := ctxDeadline.Sub(e.pipelineDeadline)
		if diff < -time.Second || diff > time.Second {
			return err
		}
	}

	// Compute actual elapsed time from the stored start time.
	elapsed := e.now().Sub(e.pipelineStart)

	// Find the phase that was running when the timeout fired.
	phase := e.lastRunningPhase()

	e.emit(Event{
		Kind: EventPipelineTimeout,
		Data: map[string]any{
			"limit":   e.config.MaxPipelineDuration.String(),
			"elapsed": elapsed.String(),
			"phase":   phase,
		},
	})

	return &PipelineTimeoutError{
		Limit:   e.config.MaxPipelineDuration,
		Elapsed: elapsed,
		Phase:   phase,
	}
}

// lastRunningPhase returns the name of the phase that was active when the
// pipeline stopped. It checks for PhaseRunning first (preferred), then falls
// back to PhaseFailed — because runPhase calls MarkFailed before the error
// propagates to wrapTimeoutError, a timed-out phase will have PhaseFailed
// status by the time this method runs. Since phases execute sequentially and
// stop on first error, there will be at most one failed phase.
// Returns "unknown" if no running or failed phase is found.
func (e *Engine) lastRunningPhase() string {
	// Prefer PhaseRunning (e.g., parallel-review goroutines).
	for _, phase := range e.config.Pipeline.Phases {
		if ps := e.state.Meta().Phases[phase.Name]; ps != nil && ps.Status == PhaseRunning {
			return phase.Name
		}
	}
	// Fall back to PhaseFailed — the timed-out phase was marked failed
	// before the error propagated here. Iterate in reverse because phases
	// execute sequentially and stop on first error, so the LAST failed
	// phase in pipeline order is the one that just failed. Earlier phases
	// may retain stale PhaseFailed status from a prior run (e.g., when
	// Resume is called from a later phase).
	for i := len(e.config.Pipeline.Phases) - 1; i >= 0; i-- {
		phase := e.config.Pipeline.Phases[i]
		if ps := e.state.Meta().Phases[phase.Name]; ps != nil && ps.Status == PhaseFailed {
			return phase.Name
		}
	}
	return "unknown"
}

// emitPhaseFailed emits a phase_failed event with error, duration, and cost
// data from the phase state. Must be called after MarkFailed so the phase
// state contains the final duration and cost values.
func (e *Engine) emitPhaseFailed(phase string, phaseErr error) {
	data := map[string]any{"error": phaseErr.Error()}
	if ps := e.state.Meta().Phases[phase]; ps != nil {
		data["duration_ms"] = ps.DurationMs
		data["cost"] = ps.Cost
	}
	e.emit(Event{Phase: phase, Kind: EventPhaseFailed, Data: data})
}
