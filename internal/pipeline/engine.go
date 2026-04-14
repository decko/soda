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

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/git"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/internal/sandbox"
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
	Pipeline          *PhasePipeline
	Loader            *PromptLoader
	Ticket            TicketData
	PromptConfig      PromptConfigData
	PromptContext     ContextData
	Model             string
	WorkDir           string
	WorktreeBase      string
	BaseBranch        string
	MaxCostUSD        float64
	MaxReworkCycles   int // max review→implement rework loops; 0 means use default (2)
	Mode              Mode
	OnEvent           func(Event)
	SleepFunc         func(time.Duration)
	JitterFunc        func(max time.Duration) time.Duration
	PRPoller          PRPoller          // for monitor phase polling; nil disables monitor
	NowFunc           func() time.Time  // for testability; defaults to time.Now
	AuthorityResolver AuthorityResolver // for comment authority checks; nil → all authoritative
	MonitorProfile    *MonitorProfile   // behavioral profile; nil → use polling config as-is
	SelfUser          string            // PR author username for self-comment filtering
	BotUsers          []string          // known bot usernames to filter
}

// maxReworkCycles returns the configured max rework cycles, defaulting to DefaultMaxReworkCycles.
func (c *EngineConfig) maxReworkCycles() int {
	if c.MaxReworkCycles > 0 {
		return c.MaxReworkCycles
	}
	return DefaultMaxReworkCycles
}

// Engine orchestrates a pipeline run, tying together the runner,
// state management, prompt rendering, and retry logic.
type Engine struct {
	runner      runner.Runner
	config      EngineConfig
	state       *State
	confirmCh   chan struct{}
	reranPhases map[string]bool // phases that ran (not skipped) in this execution
}

// NewEngine creates an Engine with sensible defaults for sleep and jitter.
// confirmCh is only created in Checkpoint mode.
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
	if cfg.Mode == Checkpoint {
		e.confirmCh = make(chan struct{}, 1)
	}
	return e
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

// shouldSkip returns true if a completed phase can be skipped because none
// of its dependencies were re-run in this execution.
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

	// Cache ticket summary in meta for soda sessions/history display.
	if e.state.Meta().Summary == "" && e.config.Ticket.Summary != "" {
		e.state.Meta().Summary = e.config.Ticket.Summary
	}

	if err := e.ensureWorktree(ctx); err != nil {
		return err
	}

	e.reranPhases = make(map[string]bool)
	e.emit(Event{Kind: EventEngineStarted})

	if err := e.executePhases(ctx, e.config.Pipeline.Phases, false); err != nil {
		return err
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
		return err
	}

	e.emit(Event{Kind: EventEngineCompleted})
	return nil
}

// executePhases runs a slice of phases, handling skip logic, checkpoint
// pauses, and review rework routing. When forceFirst is true, the first
// phase in the slice is always re-run regardless of completion status.
//
// Rework routing is handled iteratively: when a review phase produces a
// "rework" verdict, the outer loop increments the rework cycle counter,
// re-slices the phases from "implement", sets forceFirst, and restarts
// the inner range loop — avoiding recursion entirely.
func (e *Engine) executePhases(ctx context.Context, phases []PhaseConfig, forceFirst bool) error {
	for {
		rework := false

		for idx, phase := range phases {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("engine: context cancelled: %w", err)
			}

			skipCheck := !(forceFirst && idx == 0)
			if skipCheck && e.shouldSkip(phase) {
				if err := e.gatePhase(phase); err != nil {
					// Handle review rework signal on skipped phases — can
					// occur on Run() re-entry when a prior rework crashed
					// mid-implement. Route to rework instead of leaking.
					var reworkSig *reviewReworkSignal
					if errors.As(err, &reworkSig) {
						e.state.Meta().ReworkCycles++
						cycle := e.state.Meta().ReworkCycles

						e.emit(Event{
							Phase: phase.Name,
							Kind:  EventReviewReworkRouted,
							Data: map[string]any{
								"rework_cycle":      cycle,
								"max_rework_cycles": e.config.maxReworkCycles(),
								"routing_to":        "implement",
							},
						})

						if err := e.state.flushMeta(); err != nil {
							return fmt.Errorf("engine: flush meta after rework routing: %w", err)
						}

						implIdx := -1
						for i, p := range e.config.Pipeline.Phases {
							if p.Name == "implement" {
								implIdx = i
								break
							}
						}
						if implIdx < 0 {
							return fmt.Errorf("engine: review rework routing requires an implement phase in the pipeline")
						}

						phases = e.config.Pipeline.Phases[implIdx:]
						forceFirst = true
						rework = true
						break
					}
					return err
				}
				e.emit(Event{Phase: phase.Name, Kind: EventPhaseSkipped})
				continue
			}

			if err := e.runPhase(ctx, phase); err != nil {
				// Check for review rework signal — route back to implement.
				var reworkSig *reviewReworkSignal
				if errors.As(err, &reworkSig) {
					e.state.Meta().ReworkCycles++
					cycle := e.state.Meta().ReworkCycles

					e.emit(Event{
						Phase: phase.Name,
						Kind:  EventReviewReworkRouted,
						Data: map[string]any{
							"rework_cycle":      cycle,
							"max_rework_cycles": e.config.maxReworkCycles(),
							"routing_to":        "implement",
						},
					})

					// Flush meta to persist the rework cycle count.
					if err := e.state.flushMeta(); err != nil {
						return fmt.Errorf("engine: flush meta after rework routing: %w", err)
					}

					// Find implement's index and re-slice from there.
					implIdx := -1
					for i, p := range e.config.Pipeline.Phases {
						if p.Name == "implement" {
							implIdx = i
							break
						}
					}
					if implIdx < 0 {
						return fmt.Errorf("engine: review rework routing requires an implement phase in the pipeline")
					}

					phases = e.config.Pipeline.Phases[implIdx:]
					forceFirst = true
					rework = true
					break
				}
				return err
			}
			e.reranPhases[phase.Name] = true

			if e.config.Mode == Checkpoint {
				e.emit(Event{Phase: phase.Name, Kind: EventCheckpointPause})
				select {
				case <-e.confirmCh:
				case <-ctx.Done():
					return fmt.Errorf("engine: context cancelled during checkpoint: %w", ctx.Err())
				}
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

	// Mark phase running and notify callback.
	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted})
	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: mark running %s: %w", phase.Name, err)
	}

	// Build prompt data and render template.
	promptData, err := e.buildPromptData(phase)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emit(Event{Phase: phase.Name, Kind: EventPhaseFailed, Data: map[string]any{"error": err.Error()}})
		return fmt.Errorf("engine: build prompt data for %s: %w", phase.Name, err)
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
		e.emit(Event{Phase: phase.Name, Kind: EventPhaseFailed, Data: map[string]any{"error": err.Error()}})
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
		e.emit(Event{Phase: phase.Name, Kind: EventPhaseFailed, Data: map[string]any{"error": err.Error()}})
		return fmt.Errorf("engine: render prompt for %s: %w", phase.Name, err)
	}

	_ = e.state.WriteLog(phase.Name, "prompt", []byte(rendered))

	// Build runner opts. Tighten per-phase budget to remaining amount.
	remaining := e.config.MaxCostUSD - e.state.Meta().TotalCost
	if e.config.MaxCostUSD <= 0 {
		remaining = 0 // no budget enforcement
	}
	opts := runner.RunOpts{
		Phase:        phase.Name,
		SystemPrompt: rendered,
		UserPrompt:   "Execute the task described in the system prompt.",
		OutputSchema: phase.Schema,
		AllowedTools: phase.Tools,
		MaxBudgetUSD: remaining,
		WorkDir:      e.workDir(phase),
		Model:        e.config.Model,
		Timeout:      phase.Timeout.Duration,
	}

	// Run with retry.
	result, err := e.runWithRetry(ctx, phase, opts)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emit(Event{Phase: phase.Name, Kind: EventPhaseFailed, Data: map[string]any{"error": err.Error()}})
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

	// Mark completed and notify callback.
	if err := e.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("engine: mark completed %s: %w", phase.Name, err)
	}
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPhaseCompleted,
		Data:  map[string]any{"duration_ms": e.state.Meta().Phases[phase.Name].DurationMs, "cost": e.state.Meta().Phases[phase.Name].Cost},
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
			var pe *claude.ParseError
			if errors.As(err, &pe) {
				opts.UserPrompt = opts.UserPrompt + "\n\n[RETRY] Previous attempt failed with parse error: " + pe.Error() + "\nPlease fix the output format."
			}
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseRetrying,
				Data:  map[string]any{"category": category, "attempt": attempt + 1},
			})

		case "semantic":
			var se *claude.SemanticError
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

// classifyError maps an error to a retry category.
func classifyError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context"
	}
	var te *claude.TransientError
	if errors.As(err, &te) {
		return "transient"
	}
	var pe *claude.ParseError
	if errors.As(err, &pe) {
		return "parse"
	}
	var se *claude.SemanticError
	if errors.As(err, &se) {
		return "semantic"
	}
	var ee *sandbox.ExitError
	if errors.As(err, &ee) {
		if ee.OOMKill || ee.Signal != 0 {
			return "transient"
		}
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

// buildPromptData constructs the PromptData for a phase from its dependencies.
func (e *Engine) buildPromptData(phase PhaseConfig) (PromptData, error) {
	data := PromptData{
		Ticket:       e.config.Ticket,
		Config:       e.config.PromptConfig,
		Context:      e.config.PromptContext,
		WorktreePath: e.state.Meta().Worktree,
		Branch:       e.state.Meta().Branch,
		BaseBranch:   e.config.BaseBranch,
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
		case "submit":
			data.Artifacts.Submit.PRURL = e.extractPRURL()
		}
	}

	// When building the implement prompt, inject rework feedback from
	// either a failed verify or a review-rework verdict. Review feedback
	// takes precedence since it comes later in the pipeline and is more
	// specific.
	if phase.Name == "implement" {
		if fb := e.extractReviewReworkFeedback(); fb != nil {
			data.ReworkFeedback = fb
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventReworkFeedbackInjected,
				Data: map[string]any{
					"source":          fb.Source,
					"verdict":         fb.Verdict,
					"review_findings": len(fb.ReviewFindings),
				},
			})
		} else if fb := e.extractReworkFeedback(); fb != nil {
			data.ReworkFeedback = fb
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventReworkFeedbackInjected,
				Data: map[string]any{
					"source":          fb.Source,
					"verdict":         fb.Verdict,
					"fixes_count":     len(fb.FixesRequired),
					"failed_criteria": len(fb.FailedCriteria),
					"code_issues":     len(fb.CodeIssues),
					"failed_commands": len(fb.FailedCommands),
				},
			})
		}
	}

	return data, nil
}

// extractReworkFeedback reads the verify result and returns structured
// feedback when the verdict is FAIL. Returns nil if no verify result
// exists, the verdict is not FAIL, or the plan has changed since verify
// ran (staleness guard).
func (e *Engine) extractReworkFeedback() *ReworkFeedback {
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
			File     string `json:"file"`
			Line     int    `json:"line"`
			Severity string `json:"severity"`
			Issue    string `json:"issue"`
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
				Phase: "implement",
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
				File:     ci.File,
				Line:     ci.Line,
				Severity: ci.Severity,
				Issue:    ci.Issue,
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

	return fb
}

// extractReviewReworkFeedback reads the review result and returns
// structured feedback when the verdict is "rework". Returns nil if no
// review result exists or the verdict is not "rework".
func (e *Engine) extractReviewReworkFeedback() *ReworkFeedback {
	raw, err := e.state.ReadResult("review")
	if err != nil {
		return nil
	}

	var result struct {
		Verdict  string `json:"verdict"`
		Findings []struct {
			Source     string `json:"source"`
			Severity   string `json:"severity"`
			File       string `json:"file"`
			Line       int    `json:"line,omitempty"`
			Issue      string `json:"issue"`
			Suggestion string `json:"suggestion"`
		} `json:"findings"`
	}
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
			fb.ReviewFindings = append(fb.ReviewFindings, ReviewReworkFinding{
				Source:     finding.Source,
				Severity:   finding.Severity,
				File:       finding.File,
				Line:       finding.Line,
				Issue:      finding.Issue,
				Suggestion: finding.Suggestion,
			})
		}
	}

	return fb
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
			reason := "verification failed"
			if len(result.FixesRequired) > 0 {
				reason = "verification failed: " + strings.Join(result.FixesRequired, "; ")
			}
			return &PhaseGateError{Phase: phase.Name, Reason: reason}
		}

	case "review":
		var result struct {
			Verdict  string `json:"verdict"`
			Findings []struct {
				Severity string `json:"severity"`
				Issue    string `json:"issue"`
			} `json:"findings"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return fmt.Errorf("engine: review gate: failed to unmarshal result: %w", err)
		}
		if strings.EqualFold(result.Verdict, "rework") {
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
				reason := fmt.Sprintf("review requires rework but max cycles (%d) reached", maxCycles)
				if len(issues) > 0 {
					reason += ": " + strings.Join(issues, "; ")
				}
				e.emit(Event{
					Phase: phase.Name,
					Kind:  EventReviewReworkMaxCycles,
					Data: map[string]any{
						"rework_cycles":     e.state.Meta().ReworkCycles,
						"max_rework_cycles": maxCycles,
					},
				})
				return &PhaseGateError{Phase: phase.Name, Reason: reason}
			}
			// Signal rework needed — the engine loop will handle routing.
			return &reviewReworkSignal{findings: result.Findings}
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

	return nil
}

// reviewerResult holds the outcome of a single reviewer subagent.
type reviewerResult struct {
	Name     string
	Findings []reviewFinding
	Cost     float64
	Err      error
}

// reviewFinding mirrors the structured output each reviewer returns.
type reviewFinding struct {
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
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

	// Mark phase running.
	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted})
	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: mark running %s: %w", phase.Name, err)
	}

	// Build prompt data shared by all reviewers.
	promptData, err := e.buildPromptData(phase)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emit(Event{Phase: phase.Name, Kind: EventPhaseFailed, Data: map[string]any{"error": err.Error()}})
		return fmt.Errorf("engine: build prompt data for %s: %w", phase.Name, err)
	}

	// Snapshot budget before launching goroutines — avoid concurrent Meta() reads.
	budgetRemaining := 0.0
	if e.config.MaxCostUSD > 0 {
		budgetRemaining = e.config.MaxCostUSD - e.state.Meta().TotalCost
	}

	// Channel for reviewer goroutines to send messages to the parent.
	msgCh := make(chan reviewerMsg, len(phase.Reviewers)*10)

	// Dispatch reviewers in parallel.
	var wg sync.WaitGroup
	results := make([]reviewerResult, len(phase.Reviewers))

	for idx, reviewer := range phase.Reviewers {
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
			e.emit(*msg.Event)
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
		e.emit(Event{Phase: phase.Name, Kind: EventPhaseFailed, Data: map[string]any{"error": err.Error()}})
		return fmt.Errorf("engine: context cancelled during %s: %w", phase.Name, err)
	}

	// Collect errors from reviewers.
	var reviewErrors []string
	for _, result := range results {
		if result.Err != nil {
			reviewErrors = append(reviewErrors, fmt.Sprintf("%s: %v", result.Name, result.Err))
		}
	}
	if len(reviewErrors) > 0 {
		combinedErr := fmt.Errorf("engine: reviewer failures in %s: %s", phase.Name, strings.Join(reviewErrors, "; "))
		_ = e.state.MarkFailed(phase.Name, combinedErr)
		e.emit(Event{Phase: phase.Name, Kind: EventPhaseFailed, Data: map[string]any{"error": combinedErr.Error()}})
		return combinedErr
	}

	// Merge findings from all reviewers.
	merged := e.mergeReviewFindings(phase, results)

	// Serialize and store results.
	output, err := json.Marshal(merged)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
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

	// Accumulate cost from all reviewers.
	totalCost := 0.0
	for _, result := range results {
		totalCost += result.Cost
	}
	if err := e.state.AccumulateCost(phase.Name, totalCost); err != nil {
		return fmt.Errorf("engine: accumulate cost for %s: %w", phase.Name, err)
	}

	// Mark completed.
	if err := e.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("engine: mark completed %s: %w", phase.Name, err)
	}
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPhaseCompleted,
		Data:  map[string]any{"duration_ms": e.state.Meta().Phases[phase.Name].DurationMs, "cost": e.state.Meta().Phases[phase.Name].Cost},
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

	// Use the parent phase's schema for the reviewer findings.
	opts := runner.RunOpts{
		Phase:        phase.Name + "/" + reviewer.Name,
		SystemPrompt: rendered,
		UserPrompt:   "Execute the review described in the system prompt.",
		OutputSchema: phase.Schema,
		AllowedTools: phase.Tools,
		MaxBudgetUSD: budgetRemaining,
		WorkDir:      e.workDir(phase),
		Model:        e.config.Model,
		Timeout:      phase.Timeout.Duration,
	}

	result, err := e.runner.Run(ctx, opts)
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
	var findings []reviewFinding
	if result.Output != nil {
		var parsed struct {
			Findings []reviewFinding `json:"findings"`
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

// mergedReviewOutput is the combined output from all reviewers.
type mergedReviewOutput struct {
	TicketKey string                `json:"ticket_key"`
	Findings  []mergedReviewFinding `json:"findings"`
	Verdict   string                `json:"verdict"`
}

// mergedReviewFinding is a single finding with its source reviewer.
type mergedReviewFinding struct {
	Source     string `json:"source"`
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
}

// mergeReviewFindings combines findings from all reviewers and computes a verdict.
func (e *Engine) mergeReviewFindings(phase PhaseConfig, results []reviewerResult) mergedReviewOutput {
	var allFindings []mergedReviewFinding

	for _, result := range results {
		for _, finding := range result.Findings {
			allFindings = append(allFindings, mergedReviewFinding{
				Source:     result.Name,
				Severity:   finding.Severity,
				File:       finding.File,
				Line:       finding.Line,
				Issue:      finding.Issue,
				Suggestion: finding.Suggestion,
			})
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

	return mergedReviewOutput{
		TicketKey: e.config.Ticket.Key,
		Findings:  allFindings,
		Verdict:   verdict,
	}
}

// computeReviewVerdict determines the review verdict based on finding severities.
// Any critical or major finding → "rework"
// Only minor findings → "pass-with-follow-ups"
// No findings → "pass"
func computeReviewVerdict(findings []mergedReviewFinding) string {
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
func (e *Engine) buildReviewArtifact(merged mergedReviewOutput) string {
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

	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted})
	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: mark running %s: %w", phase.Name, err)
	}

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

// emit logs an event to state and calls the OnEvent callback if set.
func (e *Engine) emit(event Event) {
	_ = e.state.LogEvent(event)
	if e.config.OnEvent != nil {
		e.config.OnEvent(event)
	}
}
