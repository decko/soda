package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/decko/soda/internal/git"
	"github.com/decko/soda/internal/runner"
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
