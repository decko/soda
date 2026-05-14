package pipeline

import (
	"errors"
	"fmt"
)

// emitPhaseFailed emits a phase_failed event with error, duration, and cost
// data from the phase state. Must be called after MarkFailed so the phase
// state contains the final duration and cost values.
//
// When the error is a structured error type, its machine-readable fields are
// propagated into the event Data map so consumers of events.jsonl can inspect
// failure metadata without parsing the error string.
func (e *Engine) emitPhaseFailed(phase string, phaseErr error) {
	data := map[string]any{"error": phaseErr.Error()}
	if ps := e.state.Meta().Phases[phase]; ps != nil {
		data["duration_ms"] = ps.DurationMs
		data["cost"] = ps.Cost
	}

	// Enrich with structured error metadata when available.
	var re *RetriesExhaustedError
	var pe *PromptError
	var be *BudgetExceededError
	var pbe *PhaseBudgetExceededError
	var gbe *GenerationBudgetExceededError
	var dne *DependencyNotMetError
	var cbe *ContextBudgetError

	var failureCategory string

	switch {
	case errors.As(phaseErr, &re):
		data["error_type"] = "retries_exhausted"
		data["category"] = re.Category
		data["attempts"] = re.Attempts
		failureCategory = re.Category
		if re.Reviewer != "" {
			data["reviewer"] = re.Reviewer
		}
		// Enrich with suggestion from the transient error catalog if available.
		if suggestion := transientSuggestion(re.Err); suggestion != "" {
			data["suggestion"] = suggestion
		}
	case errors.As(phaseErr, &pe):
		data["error_type"] = "prompt_error"
		data["operation"] = pe.Operation
		failureCategory = "prompt"
		if pe.Reviewer != "" {
			data["reviewer"] = pe.Reviewer
		}
	case errors.As(phaseErr, &be):
		data["error_type"] = "budget_exceeded"
		data["limit"] = be.Limit
		data["actual"] = be.Actual
		failureCategory = "budget"
	case errors.As(phaseErr, &pbe):
		data["error_type"] = "phase_budget_exceeded"
		data["limit"] = pbe.Limit
		data["actual"] = pbe.Actual
		failureCategory = "budget"
	case errors.As(phaseErr, &gbe):
		data["error_type"] = "generation_budget_exceeded"
		data["limit"] = gbe.Limit
		data["actual"] = gbe.Actual
		failureCategory = "budget"
	case errors.As(phaseErr, &dne):
		data["error_type"] = "dependency_not_met"
		data["dependency"] = dne.Dependency
		failureCategory = "dependency"
	case errors.As(phaseErr, &cbe):
		data["error_type"] = "context_budget_exceeded"
		data["budget_tokens"] = cbe.BudgetTokens
		data["current_tokens"] = cbe.CurrentTokens
		failureCategory = "context"
	}

	// Persist and emit failure_category for downstream consumers.
	if failureCategory != "" {
		data["failure_category"] = failureCategory
		_ = e.state.SetFailureCategory(phase, failureCategory)
	}

	e.emit(Event{Phase: phase, Kind: EventPhaseFailed, Data: data})
}

// recoverCrashedPhases scans pipeline phases for any left in "running" status
// from a prior process crash. For each such phase, it marks the phase as
// failed and emits a phase_failed event so the event log is consistent.
//
// This covers the window between prompt_loaded and the Claude subprocess
// spawn: if the process dies in that window, meta.json records "running"
// but events.jsonl has no corresponding phase_failed entry. Without this
// recovery, the stale "running" status is silently overwritten when the
// phase re-runs, leaving an observability gap.
func (e *Engine) recoverCrashedPhases() {
	for _, phase := range e.config.Pipeline.Phases {
		ps := e.state.Meta().Phases[phase.Name]
		if ps == nil || ps.Status != PhaseRunning {
			continue
		}
		crashErr := fmt.Errorf("process crashed while phase was running (recovered on restart)")
		_ = e.state.MarkFailed(phase.Name, crashErr)
		e.emitPhaseFailed(phase.Name, crashErr)
	}
}
