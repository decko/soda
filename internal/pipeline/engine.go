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

// triageRequestsSkipPlan reads the triage result and returns true when the
// LLM set skip_plan=true and the ticket carries a non-empty ExistingPlan.
func (e *Engine) triageRequestsSkipPlan() bool {
	raw, err := e.state.ReadResult("triage")
	if err != nil {
		return false
	}
	var result struct {
		SkipPlan bool `json:"skip_plan"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false
	}
	return result.SkipPlan && e.config.Ticket.ExistingPlan != ""
}

// skipPlanFromTriage writes the ticket's ExistingPlan as the plan artifact,
// marks the plan phase as completed, and emits a skip event. This lets
// downstream phases (implement, verify, review) see a populated plan
// artifact without running the plan LLM call.
func (e *Engine) skipPlanFromTriage() error {
	plan := e.config.Ticket.ExistingPlan

	// Mark running so the PhaseState entry is created/archived.
	if err := e.state.MarkRunning("plan"); err != nil {
		return fmt.Errorf("engine: skip plan: mark running: %w", err)
	}
	e.emit(Event{Phase: "plan", Kind: EventPhaseStarted, Data: map[string]any{"generation": e.state.Meta().Phases["plan"].Generation}})

	// Write the existing plan as the plan artifact.
	if err := e.state.WriteArtifact("plan", []byte(plan)); err != nil {
		return fmt.Errorf("engine: skip plan: write artifact: %w", err)
	}

	// Mark completed so downstream dependency checks pass.
	if err := e.state.MarkCompleted("plan"); err != nil {
		return fmt.Errorf("engine: skip plan: mark completed: %w", err)
	}

	e.emit(Event{
		Phase: "plan",
		Kind:  EventPlanSkippedByTriage,
		Data: map[string]any{
			"reason":    "triage set skip_plan=true; using ExistingPlan from ticket",
			"plan_size": len(plan),
		},
	})

	e.emit(Event{
		Phase: "plan",
		Kind:  EventPhaseCompleted,
		Data: map[string]any{
			"duration_ms": e.state.Meta().Phases["plan"].DurationMs,
			"cost":        e.state.Meta().Phases["plan"].Cost,
		},
	})

	return nil
}

// shouldSkip returns true if a completed phase can be skipped because none
// of its dependencies were re-run in this execution.
// shouldSkipPostSubmit returns true if a post-submit phase should be skipped.
// Follow-up only runs when the review verdict is "pass-with-follow-ups".
func (e *Engine) shouldSkipPostSubmit(phase PhaseConfig) bool {
	raw, err := e.state.ReadResult("review")
	if err != nil {
		return true // no review result → nothing to follow up
	}
	var review schemas.ReviewOutput
	if err := json.Unmarshal(raw, &review); err != nil {
		return true
	}
	return review.Verdict != "pass-with-follow-ups"
}

func (e *Engine) shouldSkip(phase PhaseConfig) bool {
	if !e.state.IsCompleted(phase.Name) {
		return false
	}
	for _, dep := range phase.DependsOn {
		if e.reranPhases[dep] {
			return false
		}
	}
	return true
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
		return e.wrapTimeoutError(ctx, err)
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
		return e.wrapTimeoutError(ctx, err)
	}

	e.emit(Event{Kind: EventEngineCompleted})
	return nil
}

// reworkRoute holds the result of a successful routeRework call, providing
// the re-sliced phases and forceFirst flag for the outer executePhases loop.
type reworkRoute struct {
	phases     []PhaseConfig
	forceFirst bool
}

// routeRework handles a reworkSignal by validating the target phase exists,
// incrementing the rework cycle counter, emitting a routed event, flushing
// meta, and re-slicing the pipeline phases to start from the rework target.
// Returns the new route or an error.
//
// The target phase is validated before any state mutation so that an invalid
// target does not leave behind an incremented counter or a spurious event.
func (e *Engine) routeRework(phaseName string, sig *reworkSignal) (*reworkRoute, error) {
	// Validate the target phase exists before mutating any state.
	targetIdx := -1
	for i, p := range e.config.Pipeline.Phases {
		if p.Name == sig.target {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return nil, fmt.Errorf("engine: rework routing requires phase %q in the pipeline", sig.target)
	}

	// Increment the appropriate counter based on whether the target is
	// a corrective phase (patch) or a full rework (implement).
	isPatch := e.isCorrectivePhase(sig.target)
	if isPatch {
		e.state.Meta().PatchCycles++
	} else {
		e.state.Meta().ReworkCycles++
	}
	cycle := e.state.Meta().ReworkCycles
	if isPatch {
		cycle = e.state.Meta().PatchCycles
	}

	e.emit(Event{
		Phase: phaseName,
		Kind:  EventReworkRouted,
		Data: map[string]any{
			"rework_cycle":      cycle,
			"max_rework_cycles": e.config.maxReworkCycles(),
			"routing_to":        sig.target,
		},
	})

	if err := e.state.flushMeta(); err != nil {
		return nil, fmt.Errorf("engine: flush meta after rework routing: %w", err)
	}

	return &reworkRoute{
		phases:     e.config.Pipeline.Phases[targetIdx:],
		forceFirst: true,
	}, nil
}

// isCorrectivePhase returns true if the named phase has type "corrective"
// in the pipeline configuration.
func (e *Engine) isCorrectivePhase(name string) bool {
	for _, p := range e.config.Pipeline.Phases {
		if p.Name == name {
			return p.Type == "corrective"
		}
	}
	return false
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

// checkBudget verifies the pipeline has budget remaining before running a phase.
func (e *Engine) checkBudget(phase PhaseConfig) error {
	if e.config.MaxCostUSD <= 0 {
		return nil
	}
	total := e.state.Meta().TotalCost
	if total >= e.config.MaxCostUSD {
		return &BudgetExceededError{
			Limit:  e.config.MaxCostUSD,
			Actual: total,
			Phase:  phase.Name,
		}
	}
	// Warn at 90%.
	if total >= e.config.MaxCostUSD*0.9 {
		e.emit(Event{
			Phase: phase.Name,
			Kind:  EventBudgetWarning,
			Data:  map[string]any{"total_cost": total, "limit": e.config.MaxCostUSD},
		})
	}
	return nil
}

// checkPhaseBudget verifies a phase has not exceeded cost caps.
// It checks two limits in order:
//  1. Per-generation: ps.Cost against MaxCostPerGeneration (resets each generation).
//  2. Cumulative: ps.CumulativeCost against MaxCostPerPhase (spans all generations).
//
// Called after AccumulateCost so both Cost and CumulativeCost reflect the full run.
// Emits warnings at 90% and returns the appropriate error when exceeded.
func (e *Engine) checkPhaseBudget(phase PhaseConfig) error {
	ps := e.state.Meta().Phases[phase.Name]
	if ps == nil {
		return nil
	}

	// Per-generation check: ps.Cost resets on each MarkRunning call.
	if e.config.MaxCostPerGeneration > 0 {
		genCost := ps.Cost
		genLimit := e.config.MaxCostPerGeneration
		if genCost >= genLimit {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventGenerationBudgetExceeded,
				Data:  map[string]any{"generation_cost": genCost, "limit": genLimit},
			})
			return &GenerationBudgetExceededError{
				Limit:  genLimit,
				Actual: genCost,
				Phase:  phase.Name,
			}
		}
		if genCost >= genLimit*0.9 {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventGenerationBudgetWarning,
				Data:  map[string]any{"generation_cost": genCost, "limit": genLimit},
			})
		}
	}

	// Cumulative check: ps.CumulativeCost spans all generations.
	if e.config.MaxCostPerPhase > 0 {
		cost := ps.CumulativeCost
		limit := e.config.MaxCostPerPhase
		if cost >= limit {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseBudgetExceeded,
				Data:  map[string]any{"phase_cost": cost, "limit": limit},
			})
			return &PhaseBudgetExceededError{
				Limit:  limit,
				Actual: cost,
				Phase:  phase.Name,
			}
		}
		if cost >= limit*0.9 {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseBudgetWarning,
				Data:  map[string]any{"phase_cost": cost, "limit": limit},
			})
		}
	}

	return nil
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

// extractFeedbackFrom dispatches to the appropriate source-specific feedback
// extractor. Returns nil for unknown sources.
func (e *Engine) extractFeedbackFrom(source string) *ReworkFeedback {
	switch source {
	case "review":
		return e.extractReviewFeedback()
	case "verify":
		return e.extractVerifyFeedback()
	default:
		return nil
	}
}

// extractVerifyFeedback reads the verify result and returns structured
// feedback when the verdict is FAIL. Returns nil if no verify result
// exists, the verdict is not FAIL, or the plan has changed since verify
// ran (staleness guard).
//
// The top-level fields (Verdict, FixesRequired, etc.) always reflect
// only the most recent verify.json. PriorCycles is populated from
// archived results (verify.json.1, verify.json.2, ...) so the LLM
// has context about what was previously reported.
//
// Only critical/major code issues and failed criteria/commands are
// included to keep prompt context focused.
func (e *Engine) extractVerifyFeedback() *ReworkFeedback {
	raw, err := e.state.ReadResult("verify")
	if err != nil {
		return nil
	}

	var result struct {
		Verdict         string   `json:"verdict"`
		FixesRequired   []string `json:"fixes_required"`
		CriteriaResults []struct {
			Criterion string `json:"criterion"`
			Passed    bool   `json:"passed"`
			Evidence  string `json:"evidence"`
		} `json:"criteria_results"`
		CodeIssues []struct {
			File         string `json:"file"`
			Line         int    `json:"line"`
			Severity     string `json:"severity"`
			Issue        string `json:"issue"`
			SuggestedFix string `json:"suggested_fix"`
		} `json:"code_issues"`
		CommandResults []struct {
			Command  string `json:"command"`
			ExitCode int    `json:"exit_code"`
			Output   string `json:"output"`
			Passed   bool   `json:"passed"`
		} `json:"command_results"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	if !strings.EqualFold(result.Verdict, "FAIL") {
		return nil
	}

	// Staleness guard: skip if plan changed since verify ran.
	if verifyPS := e.state.Meta().Phases["verify"]; verifyPS != nil && verifyPS.PlanHash != "" {
		if currentHash := e.computePlanHash(); currentHash != "" && currentHash != verifyPS.PlanHash {
			e.emit(Event{
				Phase: "verify",
				Kind:  EventReworkFeedbackSkipped,
				Data: map[string]any{
					"reason":            "plan changed since verify ran",
					"verify_plan_hash":  verifyPS.PlanHash,
					"current_plan_hash": currentHash,
				},
			})
			return nil
		}
	}

	fb := &ReworkFeedback{
		Verdict:       result.Verdict,
		Source:        "verify",
		FixesRequired: result.FixesRequired,
	}

	// Failed criteria only.
	for _, cr := range result.CriteriaResults {
		if !cr.Passed {
			fb.FailedCriteria = append(fb.FailedCriteria, FailedCriterion{
				Criterion: cr.Criterion,
				Evidence:  cr.Evidence,
			})
		}
	}

	// Critical and major code issues only.
	for _, ci := range result.CodeIssues {
		sev := strings.ToLower(ci.Severity)
		if sev == "critical" || sev == "major" {
			fb.CodeIssues = append(fb.CodeIssues, ReworkCodeIssue{
				File:         ci.File,
				Line:         ci.Line,
				Severity:     ci.Severity,
				Issue:        ci.Issue,
				SuggestedFix: ci.SuggestedFix,
			})
		}
	}

	// Failed commands only, output truncated to 50 lines.
	for _, cmd := range result.CommandResults {
		if !cmd.Passed {
			fb.FailedCommands = append(fb.FailedCommands, FailedCommand{
				Command:  cmd.Command,
				ExitCode: cmd.ExitCode,
				Output:   truncateLines(cmd.Output, 50),
			})
		}
	}

	// Collect prior cycle context from archived verify results.
	fb.PriorCycles = e.collectPriorVerifyCycles()

	return fb
}

// extractReviewFeedback reads the review result and returns structured
// feedback when the verdict is "rework". Returns nil if no review result
// exists or the verdict is not "rework".
//
// The top-level fields (Verdict, ReviewFindings) reflect the most recent
// review.json. PriorCycles is populated from archived results
// (review.json.1, review.json.2, ...) so the LLM has context about
// what was previously reported. Only critical/major findings are
// included to keep prompt context focused.
func (e *Engine) extractReviewFeedback() *ReworkFeedback {
	raw, err := e.state.ReadResult("review")
	if err != nil {
		return nil
	}

	var result schemas.ReviewOutput
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	if !strings.EqualFold(result.Verdict, "rework") {
		return nil
	}

	fb := &ReworkFeedback{
		Verdict: result.Verdict,
		Source:  "review",
	}

	// Only include critical and major findings.
	for _, finding := range result.Findings {
		sev := strings.ToLower(finding.Severity)
		if sev == "critical" || sev == "major" {
			fb.ReviewFindings = append(fb.ReviewFindings, finding)
		}
	}

	// Collect prior cycle context from archived review results.
	fb.PriorCycles = e.collectPriorReviewCycles()

	return fb
}

// collectPriorReviewCycles reads archived review results (review.json.1,
// review.json.2, ...) and builds PriorCycle summaries. Only cycles with
// a "rework" verdict are included (pass cycles don't carry useful context
// for rework). Returns nil if no prior cycles exist.
func (e *Engine) collectPriorReviewCycles() []PriorCycle {
	reviewPS := e.state.Meta().Phases["review"]
	if reviewPS == nil || reviewPS.Generation <= 1 {
		return nil
	}

	var priors []PriorCycle
	for gen := 1; gen < reviewPS.Generation; gen++ {
		raw, err := e.state.ReadArchivedResult("review", gen)
		if err != nil {
			continue
		}

		var result schemas.ReviewOutput
		if err := json.Unmarshal(raw, &result); err != nil {
			continue
		}

		// Only include rework cycles — pass cycles don't carry useful context.
		if !strings.EqualFold(result.Verdict, "rework") {
			continue
		}

		summary := summarizeReviewFindings(result.Findings)
		if summary == "" {
			continue
		}

		priors = append(priors, PriorCycle{
			Cycle:   gen,
			Source:  "review",
			Verdict: result.Verdict,
			Summary: summary,
		})
	}
	return priors
}

// collectPriorVerifyCycles reads archived verify results (verify.json.1,
// verify.json.2, ...) and builds PriorCycle summaries. Only cycles with
// a FAIL verdict are included. Returns nil if no prior cycles exist.
func (e *Engine) collectPriorVerifyCycles() []PriorCycle {
	verifyPS := e.state.Meta().Phases["verify"]
	if verifyPS == nil || verifyPS.Generation <= 1 {
		return nil
	}

	var priors []PriorCycle
	for gen := 1; gen < verifyPS.Generation; gen++ {
		raw, err := e.state.ReadArchivedResult("verify", gen)
		if err != nil {
			continue
		}

		var result struct {
			Verdict       string   `json:"verdict"`
			FixesRequired []string `json:"fixes_required"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			continue
		}

		// Only include FAIL cycles.
		if !strings.EqualFold(result.Verdict, "FAIL") {
			continue
		}

		summary := summarizeVerifyFailures(result.FixesRequired)
		if summary == "" {
			continue
		}

		priors = append(priors, PriorCycle{
			Cycle:   gen,
			Source:  "verify",
			Verdict: result.Verdict,
			Summary: summary,
		})
	}
	return priors
}

// summarizeReviewFindings produces a concise summary of review findings
// for prior cycle context. Includes only critical/major findings.
func summarizeReviewFindings(findings []schemas.ReviewFinding) string {
	var parts []string
	for _, f := range findings {
		sev := strings.ToLower(f.Severity)
		if sev == "critical" || sev == "major" {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			if loc != "" {
				parts = append(parts, fmt.Sprintf("[%s] %s — %s", f.Severity, loc, f.Issue))
			} else {
				parts = append(parts, fmt.Sprintf("[%s] %s", f.Severity, f.Issue))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

// summarizeVerifyFailures produces a concise summary of verify failures
// for prior cycle context.
func summarizeVerifyFailures(fixesRequired []string) string {
	if len(fixesRequired) == 0 {
		return ""
	}
	return strings.Join(fixesRequired, "; ")
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

// truncateLines returns at most maxLines lines from s.
func truncateLines(s string, maxLines int) string {
	trimmed := strings.TrimRight(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + "\n... (truncated)"
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

// gatePhase checks domain-specific rules after a phase completes.
func (e *Engine) gatePhase(phase PhaseConfig) error {
	raw, err := e.state.ReadResult(phase.Name)
	if err != nil {
		// No result means no gating rules apply.
		return nil
	}

	switch phase.Name {
	case "triage":
		var result struct {
			Automatable bool   `json:"automatable"`
			BlockReason string `json:"block_reason"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil
		}
		if !result.Automatable {
			reason := result.BlockReason
			if reason == "" {
				reason = "ticket not automatable"
			}
			return &PhaseGateError{Phase: phase.Name, Reason: reason}
		}

	case "plan":
		var result struct {
			Tasks []json.RawMessage `json:"tasks"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil
		}
		if len(result.Tasks) == 0 {
			return &PhaseGateError{Phase: phase.Name, Reason: "no tasks in plan"}
		}

	case "implement":
		var result struct {
			TestsPassed bool `json:"tests_passed"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil
		}
		if !result.TestsPassed {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseRetrying,
				Data:  map[string]any{"warning": "tests did not pass during implementation"},
			})
		}
		// Proceed to verify regardless — verify will catch test failures.

	case "verify":
		var result struct {
			Verdict       string   `json:"verdict"`
			FixesRequired []string `json:"fixes_required"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil
		}
		if strings.EqualFold(result.Verdict, "FAIL") {
			if err := e.gateVerifyFail(phase, result.FixesRequired); err != nil {
				return err
			}
		}

	case "patch":
		if err := e.gatePatchResult(phase, raw); err != nil {
			return err
		}

	case "submit":
		var result struct {
			PRURL string `json:"pr_url"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil
		}
		if result.PRURL == "" {
			return &PhaseGateError{Phase: phase.Name, Reason: "no PR URL in submit result"}
		}
	}

	// Config-driven rework gating: when a phase has a Rework config, check
	// for a "rework" verdict and signal the engine loop accordingly.
	if phase.Rework != nil {
		if err := e.gateRework(phase, raw); err != nil {
			return err
		}
	}

	return nil
}

// reworkVerdict is a minimal struct for extracting rework-relevant fields
// from any phase result. Unlike schemas.ReviewOutput, this decouples the
// rework gate from any specific phase's full output shape — any phase that
// produces a JSON object with "verdict" (and optionally "findings") can
// participate in config-driven rework routing.
type reworkVerdict struct {
	Verdict  string `json:"verdict"`
	Findings []struct {
		Severity string `json:"severity"`
		Issue    string `json:"issue"`
	} `json:"findings"`
}

// gateRework checks for a "rework" verdict in the phase result and either
// signals rework routing or blocks when max cycles are exceeded. The rework
// target is read from the phase's ReworkConfig.
//
// The result is unmarshalled into a minimal reworkVerdict struct (verdict +
// findings) rather than a full phase-specific type, so any phase that emits
// a verdict field can use config-driven rework. On unmarshal failure, the
// gate silently skips (returns nil), consistent with all other gating cases.
func (e *Engine) gateRework(phase PhaseConfig, raw json.RawMessage) error {
	if phase.Rework == nil {
		return nil
	}

	var result reworkVerdict
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil // gracefully skip — consistent with other gating cases
	}
	if !strings.EqualFold(result.Verdict, "rework") {
		return nil
	}

	// Rework routing is handled by the engine loop, not the gate.
	// The gate only blocks when max rework cycles are exceeded.
	maxCycles := e.config.maxReworkCycles()
	if e.state.Meta().ReworkCycles >= maxCycles {
		var issues []string
		for _, finding := range result.Findings {
			sev := strings.ToLower(finding.Severity)
			if sev == "critical" || sev == "major" {
				issues = append(issues, finding.Issue)
			}
		}

		e.emit(Event{
			Phase: phase.Name,
			Kind:  EventReworkMaxCycles,
			Data: map[string]any{
				"rework_cycles":     e.state.Meta().ReworkCycles,
				"max_rework_cycles": maxCycles,
			},
		})

		// When no critical/major issues remain, downgrade the verdict to
		// "pass-with-follow-ups" so the pipeline proceeds to submit and
		// the remaining minors are handled by the follow-up phase.
		if len(issues) == 0 {
			if err := e.downgradeToFollowUps(phase, raw, result.Findings); err != nil {
				return err
			}
			return nil
		}

		reason := fmt.Sprintf("%s requires rework but max cycles (%d) reached: %s",
			phase.Name, maxCycles, strings.Join(issues, "; "))
		return &PhaseGateError{Phase: phase.Name, Reason: reason}
	}

	// Build findings for the rework signal from the minimal verdict struct.
	var findings []reworkFinding
	for _, f := range result.Findings {
		findings = append(findings, reworkFinding{
			Severity: f.Severity,
			Issue:    f.Issue,
		})
	}

	// Signal rework needed — the engine loop will handle routing.
	return &reworkSignal{target: phase.Rework.Target, findings: findings}
}

// downgradeToFollowUps rewrites the phase result on disk, changing the
// verdict from "rework" to "pass-with-follow-ups". This is called when
// max rework cycles are exhausted but the remaining findings are all
// minor — the pipeline can proceed to submit and handle them as follow-ups
// instead of blocking.
//
// The raw JSON is round-tripped through map[string]any (rather than a
// typed struct like schemas.ReviewOutput) so that fields belonging to
// non-review phases are preserved. The minor count is derived from the
// already-parsed reworkVerdict findings, keeping this function
// phase-agnostic — consistent with gateRework.
func (e *Engine) downgradeToFollowUps(phase PhaseConfig, raw json.RawMessage, findings []struct {
	Severity string `json:"severity"`
	Issue    string `json:"issue"`
}) error {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("engine: downgrade to follow-ups: unmarshal %s result: %w", phase.Name, err)
	}

	doc["verdict"] = "pass-with-follow-ups"

	updated, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("engine: downgrade to follow-ups: marshal %s result: %w", phase.Name, err)
	}
	if err := e.state.WriteResult(phase.Name, json.RawMessage(updated)); err != nil {
		return fmt.Errorf("engine: downgrade to follow-ups: write %s result: %w", phase.Name, err)
	}

	minorCount := 0
	for _, f := range findings {
		if strings.EqualFold(f.Severity, "minor") {
			minorCount++
		}
	}

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventReworkMinorsDowngraded,
		Data: map[string]any{
			"original_verdict":  "rework",
			"new_verdict":       "pass-with-follow-ups",
			"minor_count":       minorCount,
			"rework_cycles":     e.state.Meta().ReworkCycles,
			"max_rework_cycles": e.config.maxReworkCycles(),
		},
	})

	return nil
}

// regressionResult holds the outcome of a regression check between two
// sets of failing criteria.
type regressionResult struct {
	Regressions []string // criteria that were previously passing but now fail
}

// detectRegression compares the previous set of failing criteria against the
// current set. A "regression" is a criterion that was NOT in the previous
// failures (i.e., it was passing) but IS in the current failures.
func detectRegression(previous, current []string) regressionResult {
	prevSet := make(map[string]bool, len(previous))
	for _, f := range previous {
		prevSet[f] = true
	}

	var regressions []string
	for _, c := range current {
		if !prevSet[c] {
			regressions = append(regressions, c)
		}
	}

	return regressionResult{
		Regressions: regressions,
	}
}

// extractFailingCriteria reads the verify result from state and returns
// the criterion text for each failing criterion. Returns nil if the result
// cannot be read or parsed.
func (e *Engine) extractFailingCriteria() []string {
	raw, err := e.state.ReadResult("verify")
	if err != nil {
		return nil
	}

	var result struct {
		CriteriaResults []struct {
			Criterion string `json:"criterion"`
			Passed    bool   `json:"passed"`
		} `json:"criteria_results"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}

	var failures []string
	for _, cr := range result.CriteriaResults {
		if !cr.Passed {
			failures = append(failures, cr.Criterion)
		}
	}
	return failures
}

// gateVerifyFail handles a verify FAIL verdict. When the phase has a
// CorrectiveConfig, it routes to the corrective phase (e.g., patch) instead
// of stopping with a PhaseGateError. Respects max_attempts, on_exhausted
// policy, the EscalatedFromPatch one-shot flag, and regression detection.
func (e *Engine) gateVerifyFail(phase PhaseConfig, fixesRequired []string) error {
	reason := "verification failed"
	if len(fixesRequired) > 0 {
		reason = "verification failed: " + strings.Join(fixesRequired, "; ")
	}

	cc := phase.Corrective
	if cc == nil {
		return &PhaseGateError{Phase: phase.Name, Reason: reason}
	}

	// One-shot escalation flag: once set, subsequent verify FAILs stop.
	if e.state.Meta().EscalatedFromPatch {
		return &PhaseGateError{Phase: phase.Name, Reason: reason + " (escalated from patch, no re-entry)"}
	}

	// Check max_attempts before extracting failing criteria (lazy
	// evaluation). When cycles are exhausted the on_exhausted policy takes
	// over and the I/O from extractFailingCriteria (reading + parsing
	// verify.json) is avoided.
	maxAttempts := cc.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	if e.state.Meta().PatchCycles >= maxAttempts {
		e.emit(Event{
			Phase: phase.Name,
			Kind:  EventPatchExhausted,
			Data: map[string]any{
				"patch_cycles": e.state.Meta().PatchCycles,
				"on_exhausted": cc.OnExhausted,
			},
		})
		return e.handlePatchExhausted(phase, cc, reason)
	}

	// Extract failing criteria lazily — only when we know the result will
	// be used for regression detection or snapshotting for the next cycle.
	currentFailures := e.extractFailingCriteria()

	// Regression detection: when PatchCycles > 0, compare current failures
	// against PreviousFailures. A regression (previously-passing criterion
	// now fails) triggers immediate escalation. Note: criterion text from
	// the ticket's acceptance criteria should be stable across runs; if
	// criteria are rephrased, this may cause false negatives.
	if e.state.Meta().PatchCycles > 0 && len(e.state.Meta().PreviousFailures) > 0 {
		regResult := detectRegression(e.state.Meta().PreviousFailures, currentFailures)
		if len(regResult.Regressions) > 0 {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPatchRegression,
				Data: map[string]any{
					"previously_passed": regResult.Regressions,
					"now_failed":        currentFailures,
				},
			})
			return &PhaseGateError{
				Phase:  phase.Name,
				Reason: reason + " (regression: previously-passing criteria now fail: " + strings.Join(regResult.Regressions, "; ") + ")",
			}
		}
	}

	// Snapshot current failures for the next regression check.
	e.state.Meta().PreviousFailures = currentFailures

	// Route to the corrective phase.
	return &reworkSignal{target: cc.Phase}
}

// handlePatchExhausted applies the on_exhausted policy when patch attempts
// are depleted.
//   - "stop" returns a PhaseGateError.
//   - "escalate" routes to the escalation target (e.g., implement) with a budget check.
//   - "retry" allows one extra patch cycle by resetting PatchCycles, then stops.
func (e *Engine) handlePatchExhausted(phase PhaseConfig, cc *CorrectiveConfig, reason string) error {
	switch cc.OnExhausted {
	case "escalate":
		if cc.EscalateTo == "" {
			return &PhaseGateError{Phase: phase.Name, Reason: reason + " (escalation target not configured)"}
		}

		// Budget check: if remaining < $5, skip escalation.
		if e.config.MaxCostUSD > 0 {
			remaining := e.config.MaxCostUSD - e.state.Meta().TotalCost
			if remaining < 5.0 {
				e.emit(Event{
					Phase: phase.Name,
					Kind:  EventPatchEscalationSkipped,
					Data: map[string]any{
						"remaining_budget": remaining,
						"reason":           "insufficient budget for escalation",
					},
				})
				return &PhaseGateError{Phase: phase.Name, Reason: reason + " (insufficient budget to escalate)"}
			}
		}

		// Set one-shot flag so we don't re-enter the patch loop.
		e.state.Meta().EscalatedFromPatch = true

		patchCost := 0.0
		if ps := e.state.Meta().Phases[cc.Phase]; ps != nil {
			patchCost = ps.Cost
		}
		e.emit(Event{
			Phase: phase.Name,
			Kind:  EventPatchEscalated,
			Data: map[string]any{
				"escalating_to":    cc.EscalateTo,
				"total_patch_cost": patchCost,
			},
		})

		return &reworkSignal{target: cc.EscalateTo}

	case "retry":
		// Allow one extra patch cycle. If already used, stop.
		if e.state.Meta().PatchRetryUsed {
			return &PhaseGateError{Phase: phase.Name, Reason: reason + " (patch retry exhausted)"}
		}
		e.state.Meta().PatchRetryUsed = true
		e.state.Meta().PatchCycles = 0
		e.state.Meta().PreviousFailures = nil
		return &reworkSignal{target: cc.Phase}

	default: // "stop" or unrecognized
		return &PhaseGateError{Phase: phase.Name, Reason: reason + " (patch attempts exhausted)"}
	}
}

// gatePatchResult checks the patch phase result for the TooComplex flag.
// When set, the engine skips re-verify and either escalates or stops.
func (e *Engine) gatePatchResult(phase PhaseConfig, raw json.RawMessage) error {
	var result struct {
		TooComplex       bool   `json:"too_complex"`
		TooComplexReason string `json:"too_complex_reason"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	if !result.TooComplex {
		return nil
	}

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPatchTooComplex,
		Data:  map[string]any{"reason": result.TooComplexReason},
	})

	// Find the verify phase's corrective config to get escalation target.
	for _, p := range e.config.Pipeline.Phases {
		if p.Corrective != nil && p.Corrective.Phase == phase.Name {
			if p.Corrective.OnExhausted == "escalate" && p.Corrective.EscalateTo != "" {
				// Budget check before escalation.
				if e.config.MaxCostUSD > 0 {
					remaining := e.config.MaxCostUSD - e.state.Meta().TotalCost
					if remaining < 5.0 {
						e.emit(Event{
							Phase: phase.Name,
							Kind:  EventPatchEscalationSkipped,
							Data: map[string]any{
								"remaining_budget": remaining,
								"reason":           "insufficient budget for escalation",
							},
						})
						return &PhaseGateError{Phase: phase.Name, Reason: "patch too complex: " + result.TooComplexReason + " (insufficient budget to escalate)"}
					}
				}
				e.state.Meta().EscalatedFromPatch = true
				e.emit(Event{
					Phase: phase.Name,
					Kind:  EventPatchEscalated,
					Data: map[string]any{
						"escalating_to": p.Corrective.EscalateTo,
						"reason":        result.TooComplexReason,
					},
				})
				return &reworkSignal{target: p.Corrective.EscalateTo}
			}
			break
		}
	}

	return &PhaseGateError{Phase: phase.Name, Reason: "patch too complex: " + result.TooComplexReason}
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

	// Channel for reviewer goroutines to send messages to the parent.
	msgCh := make(chan reviewerMsg, len(phase.Reviewers)*10)

	// Dispatch reviewers in parallel.
	var wg sync.WaitGroup
	results := make([]reviewerResult, len(phase.Reviewers))

	for idx, reviewer := range phase.Reviewers {
		if idx > 0 && phase.ReviewerStagger.Duration > 0 {
			e.config.SleepFunc(phase.ReviewerStagger.Duration)
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

// runMonitorStub handles polling phases by marking them completed without
// actually running the runner. Full monitor polling is not yet implemented.
func (e *Engine) runMonitorStub(phase PhaseConfig) error {
	prURL := e.extractPRURL()

	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: mark running %s: %w", phase.Name, err)
	}
	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted, Data: map[string]any{"generation": e.state.Meta().Phases[phase.Name].Generation}})

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventMonitorSkipped,
		Data:  map[string]any{"pr_url": prURL, "reason": "monitor polling not yet implemented"},
	})

	if err := e.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("engine: mark completed %s: %w", phase.Name, err)
	}
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPhaseCompleted,
		Data:  map[string]any{"duration_ms": e.state.Meta().Phases[phase.Name].DurationMs, "cost": e.state.Meta().Phases[phase.Name].Cost},
	})

	return nil
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
