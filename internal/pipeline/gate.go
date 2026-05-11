package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/decko/soda/schemas"
)

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

// phaseConditionData is the minimal context passed to phase condition
// templates. It mirrors reviewerConditionData but adds Automatable for
// triage-driven phase gating.
type phaseConditionData struct {
	Complexity  string // "low", "medium", "high" from triage
	Automatable string // "yes", "no", "partial" from triage
	ReworkCycle int    // current rework cycle count from pipeline meta
}

// readPhaseConditionData builds a phaseConditionData from pipeline state.
// Triage fields are extracted from the triage result JSON; on read/parse
// failure the fields are left at their zero values (conditions should
// handle the defaults).
func (e *Engine) readPhaseConditionData() phaseConditionData {
	data := phaseConditionData{
		ReworkCycle: e.state.Meta().ReworkCycles,
	}
	raw, err := e.state.ReadResult("triage")
	if err != nil {
		return data
	}
	// Unmarshal each field independently so that a type mismatch on one
	// field (e.g. automatable is a JSON boolean in the embedded schema
	// but a string in the generated schema) does not prevent the other
	// field from being read.
	var comp struct {
		Complexity string `json:"complexity"`
	}
	if err := json.Unmarshal(raw, &comp); err == nil {
		data.Complexity = comp.Complexity
	}
	var auto struct {
		Automatable string `json:"automatable"`
	}
	if err := json.Unmarshal(raw, &auto); err == nil {
		data.Automatable = auto.Automatable
	}
	return data
}

// evalPhaseCondition evaluates a phase's condition template against the
// given data. Returns true if the phase should run. When the condition is
// empty the phase always runs. The rendered output is trimmed and compared
// case-insensitively to "false"; only an exact "false" skips the phase.
// On template errors the function returns (true, err) so the caller can
// fall back to running the phase (fail-safe).
func evalPhaseCondition(condition string, data phaseConditionData) (bool, error) {
	if condition == "" {
		return true, nil
	}
	tmpl, err := template.New("condition").Parse(condition)
	if err != nil {
		return true, fmt.Errorf("parse condition: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return true, fmt.Errorf("execute condition: %w", err)
	}
	result := strings.TrimSpace(buf.String())
	if strings.EqualFold(result, "false") {
		return false, nil
	}
	return true, nil
}

// resolvePhaseTimeout evaluates a phase's timeout overrides against the
// current triage metadata. Overrides are evaluated in declaration order;
// the first match wins. When no override matches (or TimeoutOverrides is
// empty), the phase's base Timeout is returned. Template evaluation errors
// are fail-safe: the erroring override is skipped and evaluation continues
// to the next one. An EventPhaseTimeoutResolved event is emitted when an
// override matches so operators can see which timeout was applied.
func (e *Engine) resolvePhaseTimeout(phase PhaseConfig) time.Duration {
	if len(phase.TimeoutOverrides) == 0 {
		return phase.Timeout.Duration
	}
	condData := e.readPhaseConditionData()
	for _, override := range phase.TimeoutOverrides {
		matches, err := evalPhaseCondition(override.Condition, condData)
		if err != nil {
			e.emit(Event{Phase: phase.Name, Kind: EventConditionEvalFallback,
				Data: map[string]any{"error": err.Error()}})
			continue // skip erroring override, try next
		}
		if matches {
			data := map[string]any{
				"resolved_timeout":  override.Timeout.Duration.String(),
				"matched_condition": override.Condition,
			}
			if override.Label != "" {
				data["label"] = override.Label
			}
			e.emit(Event{Phase: phase.Name, Kind: EventPhaseTimeoutResolved,
				Data: data})
			return override.Timeout.Duration
		}
	}
	return phase.Timeout.Duration // base fallback
}

// skipPhaseByCondition marks a phase as completed with an empty artifact
// so downstream dependency checks and shouldSkip work correctly, then
// emits condition-skipped and phase-completed events. The empty artifact
// prevents ReadArtifact ErrNotExist errors on dependent phases.
func (e *Engine) skipPhaseByCondition(phase PhaseConfig, reason string) error {
	// Mark running so the PhaseState entry is created/archived.
	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: skip phase by condition: mark running %s: %w", phase.Name, err)
	}
	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted, Data: map[string]any{"generation": e.state.Meta().Phases[phase.Name].Generation}})

	// Write an empty artifact so downstream ReadArtifact calls don't get
	// ErrNotExist and shouldSkip works correctly on resume.
	if err := e.state.WriteArtifact(phase.Name, []byte{}); err != nil {
		return fmt.Errorf("engine: skip phase by condition: write artifact %s: %w", phase.Name, err)
	}

	// Mark completed so downstream dependency checks pass.
	if err := e.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("engine: skip phase by condition: mark completed %s: %w", phase.Name, err)
	}

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPhaseConditionSkipped,
		Data: map[string]any{
			"reason": reason,
		},
	})

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPhaseCompleted,
		Data: map[string]any{
			"duration_ms": e.state.Meta().Phases[phase.Name].DurationMs,
			"cost":        e.state.Meta().Phases[phase.Name].Cost,
		},
	})

	return nil
}
