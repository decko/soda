package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
			TestsPassed  bool       `json:"tests_passed"`
			Commits      []struct{} `json:"commits"`
			FilesChanged []struct{} `json:"files_changed"`
			TaskResults  []struct {
				Status string `json:"status"`
			} `json:"task_results"`
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

		// Short-circuit detection: when implement re-runs during a rework
		// cycle (generation > 1) and produces zero commits and zero
		// files_changed, the LLM likely saw existing code matching plan
		// tasks and short-circuited without addressing the rework feedback.
		// Gate with a clear error so the operator can see what happened.
		if ps := e.state.Meta().Phases[phase.Name]; ps != nil && ps.Generation > 1 {
			if len(result.Commits) == 0 && len(result.FilesChanged) == 0 {
				skippedTasks := 0
				for _, tr := range result.TaskResults {
					if strings.EqualFold(tr.Status, "skipped") {
						skippedTasks++
					}
				}
				e.emit(Event{
					Phase: phase.Name,
					Kind:  EventImplementNoChanges,
					Data: map[string]any{
						"generation":    ps.Generation,
						"commits":       len(result.Commits),
						"files_changed": len(result.FilesChanged),
						"skipped_tasks": skippedTasks,
					},
				})
				return &PhaseGateError{
					Phase:  phase.Name,
					Reason: "rework produced no changes — implement short-circuited without addressing feedback",
				}
			}
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
