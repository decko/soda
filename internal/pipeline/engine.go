package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"time"

	"github.com/decko/soda/internal/git"
	"github.com/decko/soda/internal/progress"
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
	Pipeline               *PhasePipeline
	Loader                 *PromptLoader
	Ticket                 TicketData
	PromptConfig           PromptConfigData
	PromptContext          ContextData
	DetectedStack          DetectedStackData // auto-detected project stack info; zero value if detection was skipped
	Model                  string
	PipelineName           string // pipeline config name (e.g. "fast"); empty means "default"
	BinaryVersion          string // binary build identifier; recorded in meta on first run, checked for staleness on resume
	WorkDir                string
	WorktreeBase           string
	BaseBranch             string
	MaxCostUSD             float64
	MaxCostPerPhase        float64       // per-phase cost cap; 0 means no per-phase limit
	MaxCostPerGeneration   float64       // per-generation cost cap (ps.Cost); 0 means no per-generation limit
	MaxPipelineDuration    time.Duration // max wall-clock time for the entire pipeline; 0 means no limit
	MaxReworkCycles        int           // max review→implement rework loops; 0 means use default (2)
	MaxDiffBytes           int           // max bytes of git diff injected into rework prompts; 0 means use default (50000)
	MaxAPIConcurrency      int           // max concurrent runner.Run calls; 0 means unlimited
	MaxSiblingContextBytes int           // max bytes of sibling-function context injected into implement prompts; 0 means use default (20000)
	Mode                   Mode
	OnEvent                func(Event)
	PauseSignal            <-chan bool // receives true=pause, false=resume from TUI; nil disables
	SleepFunc              func(time.Duration)
	JitterFunc             func(max time.Duration) time.Duration
	PRPoller               PRPoller          // for monitor phase polling; nil disables monitor
	NowFunc                func() time.Time  // for testability; defaults to time.Now
	AuthorityResolver      AuthorityResolver // for comment authority checks; nil → all authoritative
	MonitorProfile         *MonitorProfile   // behavioral profile; nil → use polling config as-is
	SelfUser               string            // PR author username for self-comment filtering
	BotUsers               []string          // known bot usernames to filter
	Stderr                 io.Writer         // destination for warning messages; defaults to os.Stderr
	TokenBudget            TokenBudgetConfig // prompt token budget estimation; zero value disables checks
	ContextBudget          int               // global default context budget in tokens; 0 disables adaptive fitting
	Notify                 NotifyConfig      // notification hooks fired on pipeline completion; zero value disables
	ApiKeyHelper           string            // path to script that prints an API key; wired into runner.RunOpts
	MergeMethod            string            // merge method: "merge", "squash", "rebase"; defaults to "squash"
	MergeLabels            []string          // required PR labels before auto-merge proceeds
	AutoMergeTimeout       time.Duration     // max wait after approval before giving up; defaults to 30m
}

// TokenBudgetConfig configures the prompt-size estimation check.
// Mirrors config.TokenBudgetConfig — kept separate to avoid cross-package imports.
type TokenBudgetConfig struct {
	WarnTokens    int     // emit warning when estimated prompt tokens exceed this; 0 disables
	BytesPerToken float64 // bytes-per-token ratio for estimation; 0 defaults to 3.3
}

// maxReworkCycles returns the configured max rework cycles, defaulting to DefaultMaxReworkCycles.
func (c *EngineConfig) maxReworkCycles() int {
	if c.MaxReworkCycles > 0 {
		return c.MaxReworkCycles
	}
	return DefaultMaxReworkCycles
}

// contextBudgetDefault returns the global default context budget in tokens.
// If not configured (0), returns 0 (disabled). Per-phase ContextBudget
// takes precedence over this default.
func (e *Engine) contextBudgetDefault() int {
	return e.config.ContextBudget
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
	broadcaster      *Broadcaster
	apiSem           *Semaphore // limits concurrent runner.Run calls; nil means unlimited
	confirmCh        chan struct{}
	reranPhases      map[string]bool // phases that ran (not skipped) in this execution
	eventLogWarnOnce sync.Once       // ensures at most one LogEvent warning is printed
	pauseMu          sync.Mutex
	paused           bool
	pauseCond        *sync.Cond
	inCheckpoint     bool      // true while blocked on <-confirmCh; guarded by pauseMu
	pipelineStart    time.Time // wall-clock time when applyPipelineTimeout was called
	pipelineDeadline time.Time // deadline set by applyPipelineTimeout; zero if no timeout
	notifier         *Notifier // pipeline completion notifier; nil means no notifications
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
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}

	e := &Engine{
		runner:   r,
		config:   cfg,
		state:    state,
		apiSem:   NewSemaphore(cfg.MaxAPIConcurrency),
		notifier: NewNotifier(cfg.Notify),
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
		return &WorktreeError{Branch: branch, Err: err}
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

	// Start broadcast socket for live streaming to attach clients.
	e.startBroadcaster()
	defer e.stopBroadcaster()

	// Apply pipeline-level timeout if configured.
	ctx, cancel := e.applyPipelineTimeout(ctx)
	defer cancel()

	// Record or check binary version for staleness detection.
	e.checkBinaryVersion()

	// Cache ticket summary in meta for soda sessions/history display.
	if e.state.Meta().Summary == "" && e.config.Ticket.Summary != "" {
		e.state.Meta().Summary = e.config.Ticket.Summary
	}

	// Record pipeline name in meta for identification.
	// Always update when explicitly set so re-running with a different
	// --pipeline flag stores the correct name for future resumes.
	if e.config.PipelineName != "" {
		e.state.Meta().Pipeline = e.config.PipelineName
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

	// Recover any phases left in "running" status from a prior crash.
	// Must happen before executePhases so the event log records the failure
	// and MarkRunning starts a fresh generation instead of silently overwriting.
	e.recoverCrashedPhases()

	e.reranPhases = make(map[string]bool)
	startedData := map[string]any{}
	if e.config.PipelineName != "" {
		startedData["pipeline"] = e.config.PipelineName
	}
	if len(startedData) == 0 {
		e.emit(Event{Kind: EventEngineStarted})
	} else {
		e.emit(Event{Kind: EventEngineStarted, Data: startedData})
	}

	if err := e.executePhases(ctx, e.config.Pipeline.Phases, false); err != nil {
		wrapped := e.wrapTimeoutError(ctx, err)
		var pte *PipelineTimeoutError
		if !errors.As(wrapped, &pte) {
			e.emit(Event{Kind: EventEngineFailed, Data: map[string]any{"error": wrapped.Error()}})
		}
		e.sendNotification(wrapped)
		return wrapped
	}

	e.emit(Event{Kind: EventEngineCompleted})
	e.sendNotification(nil)
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
		names := make([]string, len(e.config.Pipeline.Phases))
		for i, p := range e.config.Pipeline.Phases {
			names[i] = p.Name
		}
		return &PhaseNotFoundError{Phase: fromPhase, Pipeline: names}
	}

	if err := e.state.AcquireLock(); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	defer e.state.ReleaseLock()

	// Start broadcast socket for live streaming to attach clients.
	e.startBroadcaster()
	defer e.stopBroadcaster()

	// Apply pipeline-level timeout if configured.
	ctx, cancel := e.applyPipelineTimeout(ctx)
	defer cancel()

	// Check binary version for staleness detection on resume.
	e.checkBinaryVersion()

	// Cache ticket summary in meta for soda sessions/history display.
	if e.state.Meta().Summary == "" && e.config.Ticket.Summary != "" {
		e.state.Meta().Summary = e.config.Ticket.Summary
	}

	// Record pipeline name in meta for identification.
	// Always update when explicitly set so re-running with a different
	// --pipeline flag stores the correct name for future resumes.
	if e.config.PipelineName != "" {
		e.state.Meta().Pipeline = e.config.PipelineName
	}

	if err := e.ensureWorktree(ctx); err != nil {
		return err
	}

	// Recover any phases left in "running" status from a prior crash.
	// Must happen before executePhases so the event log records the failure
	// and MarkRunning starts a fresh generation instead of silently overwriting.
	e.recoverCrashedPhases()

	e.reranPhases = make(map[string]bool)
	resumeData := map[string]any{"resumed_from": fromPhase}
	if e.config.PipelineName != "" {
		resumeData["pipeline"] = e.config.PipelineName
	}
	e.emit(Event{Kind: EventEngineStarted, Data: resumeData})

	// The fromPhase (first in slice) is always re-run, even if completed.
	// Mark it with forceFirst=true so executePhases skips the shouldSkip check.
	if err := e.executePhases(ctx, e.config.Pipeline.Phases[startIdx:], true); err != nil {
		wrapped := e.wrapTimeoutError(ctx, err)
		var pte *PipelineTimeoutError
		if !errors.As(wrapped, &pte) {
			e.emit(Event{Kind: EventEngineFailed, Data: map[string]any{"error": wrapped.Error()}})
		}
		e.sendNotification(wrapped)
		return wrapped
	}

	e.emit(Event{Kind: EventEngineCompleted})
	e.sendNotification(nil)
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

	// Inject implement diff into rework feedback so the LLM can see
	// what was previously implemented when addressing rework findings.
	if promptData.ReworkFeedback != nil {
		promptData.ReworkFeedback.ImplementDiff = e.computeDiffContext(ctx)
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
		promptErr := &PromptError{Phase: phase.Name, Operation: "load", Err: err}
		_ = e.state.MarkFailed(phase.Name, promptErr)
		e.emitPhaseFailed(phase.Name, promptErr)
		return promptErr
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

	// Adaptive context fitting: if a context budget is configured (per-phase
	// or global default), reduce the prompt data to fit within the token budget.
	// This must happen before the final render so the fitted data is used for
	// hash computation, token estimation, and the actual prompt sent to the LLM.
	contextBudget := phase.ContextBudget
	if contextBudget <= 0 {
		contextBudget = e.contextBudgetDefault()
	}
	if contextBudget > 0 {
		bytesPerToken := e.config.TokenBudget.BytesPerToken
		if bytesPerToken <= 0 {
			bytesPerToken = 3.3
		}
		fitted, reduced, fitErr := fitToBudget(loadResult.Content, promptData, phase.Name, contextBudget, bytesPerToken)
		if fitErr != nil {
			var cbe *ContextBudgetError
			if errors.As(fitErr, &cbe) {
				_ = e.state.MarkFailed(phase.Name, fitErr)
				e.emitPhaseFailed(phase.Name, fitErr)
				return fitErr
			}
			// Non-budget errors (e.g., template render failure) are fatal.
			_ = e.state.MarkFailed(phase.Name, fitErr)
			e.emitPhaseFailed(phase.Name, fitErr)
			return fmt.Errorf("engine: fit context for %s: %w", phase.Name, fitErr)
		}
		if len(reduced) > 0 {
			promptData = fitted
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventContextFitted,
				Data: map[string]any{
					"budget_tokens":  contextBudget,
					"reduced_fields": reduced,
				},
			})
		}
	}

	rendered, err := RenderPrompt(loadResult.Content, promptData)
	if err != nil {
		promptErr := &PromptError{Phase: phase.Name, Operation: "render", Err: err}
		_ = e.state.MarkFailed(phase.Name, promptErr)
		e.emitPhaseFailed(phase.Name, promptErr)
		return promptErr
	}

	// Persist prompt content hash for traceability so operators can verify
	// exactly which prompt was sent to the LLM for each phase execution.
	promptDigest := sha256.Sum256([]byte(rendered))
	e.state.Meta().Phases[phase.Name].PromptHash = fmt.Sprintf("%x", promptDigest)

	// Token budget estimation: estimate prompt tokens from rendered byte
	// length (bytes / ratio) and warn if above the configured threshold.
	// This is a warn-only check — it never blocks execution.
	bytesPerToken := e.config.TokenBudget.BytesPerToken
	if bytesPerToken <= 0 {
		bytesPerToken = 3.3
	}
	estimatedTokens := int64(float64(len(rendered)) / bytesPerToken)
	e.state.Meta().Phases[phase.Name].EstimatedPromptTokens = estimatedTokens
	if warnLimit := e.config.TokenBudget.WarnTokens; warnLimit > 0 && estimatedTokens > int64(warnLimit) {
		e.emit(Event{
			Phase: phase.Name,
			Kind:  EventTokenBudgetWarning,
			Data: map[string]any{
				"estimated_tokens": estimatedTokens,
				"warn_limit":       warnLimit,
				"prompt_bytes":     len(rendered),
			},
		})
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
		ApiKeyHelper: e.config.ApiKeyHelper,
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
	if err := e.state.AccumulateTokens(phase.Name, result.TokensIn, result.TokensOut, result.CacheTokensIn); err != nil {
		return fmt.Errorf("engine: accumulate tokens for %s: %w", phase.Name, err)
	}

	// Token budget calibration: after the LLM returns actual token counts,
	// emit a calibration event pairing the prompt byte length with the real
	// tokens_in so operators can tune the bytes-per-token ratio (default 3.3).
	if ps := e.state.Meta().Phases[phase.Name]; ps != nil && ps.TokensIn > 0 {
		actualRatio := float64(len(rendered)) / float64(ps.TokensIn)
		e.emit(Event{
			Phase: phase.Name,
			Kind:  EventTokenBudgetCalibration,
			Data: map[string]any{
				"prompt_bytes":     len(rendered),
				"estimated_tokens": estimatedTokens,
				"actual_tokens_in": ps.TokensIn,
				"bytes_per_token":  math.Round(actualRatio*100) / 100,
			},
		})
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
	ps := e.state.Meta().Phases[phase.Name]
	completedData := map[string]any{
		"duration_ms": ps.DurationMs,
		"cost":        ps.Cost,
	}
	if ps.TokensIn > 0 {
		completedData["tokens_in"] = ps.TokensIn
	}
	if ps.TokensOut > 0 {
		completedData["tokens_out"] = ps.TokensOut
	}
	if ps.CacheTokensIn > 0 {
		completedData["cache_tokens_in"] = ps.CacheTokensIn
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
// Also broadcasts to any attached clients via the Unix socket.
// If LogEvent fails (e.g., disk full), a warning is printed to stderr
// once to avoid flooding logs; subsequent failures are silently ignored.
func (e *Engine) emit(event Event) {
	if err := e.state.LogEvent(event); err != nil {
		e.eventLogWarnOnce.Do(func() {
			fmt.Fprintf(e.config.Stderr, "engine: warning: failed to write event log: %v\n", err)
		})
	}
	if e.config.OnEvent != nil {
		e.config.OnEvent(event)
	}
	if e.broadcaster != nil {
		e.broadcaster.Broadcast(event)
	}
}

// startBroadcaster creates the broadcast Unix socket for live streaming.
// Errors are silently ignored — broadcast is best-effort and must not
// prevent the pipeline from running.
func (e *Engine) startBroadcaster() {
	b, err := NewBroadcaster(e.state.SocketPath())
	if err != nil {
		return
	}
	e.broadcaster = b
}

// stopBroadcaster closes the broadcast socket and cleans up.
func (e *Engine) stopBroadcaster() {
	if e.broadcaster != nil {
		e.broadcaster.Close()
		e.broadcaster = nil
	}
}

// emitChunk forwards an event to the OnEvent callback without writing it
// to events.jsonl. Output chunks are high-frequency, transient streaming
// data that would inflate the durable log with no diagnostic value.
// Also broadcasts to any attached clients via the Unix socket.
func (e *Engine) emitChunk(event Event) {
	if e.config.OnEvent != nil {
		e.config.OnEvent(event)
	}
	if e.broadcaster != nil {
		e.broadcaster.Broadcast(event)
	}
}
