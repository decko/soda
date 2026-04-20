package pipeline

import (
	"fmt"
	"strings"
	"time"
)

// BudgetExceededError is returned when accumulated cost exceeds the configured limit.
type BudgetExceededError struct {
	Limit  float64
	Actual float64
	Phase  string
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("pipeline: budget exceeded in phase %s: limit $%.2f, actual $%.2f",
		e.Phase, e.Limit, e.Actual)
}

// PhaseBudgetExceededError is returned when a single phase's cost exceeds the
// configured per-phase limit (MaxCostPerPhase).
type PhaseBudgetExceededError struct {
	Limit  float64
	Actual float64
	Phase  string
}

func (e *PhaseBudgetExceededError) Error() string {
	return fmt.Sprintf("pipeline: phase budget exceeded in phase %s: limit $%.2f, actual $%.2f",
		e.Phase, e.Limit, e.Actual)
}

// GenerationBudgetExceededError is returned when a single phase generation's
// cost (ps.Cost) exceeds the configured per-generation limit.
type GenerationBudgetExceededError struct {
	Limit  float64
	Actual float64
	Phase  string
}

func (e *GenerationBudgetExceededError) Error() string {
	return fmt.Sprintf("pipeline: generation budget exceeded in phase %s: limit $%.2f, actual $%.2f",
		e.Phase, e.Limit, e.Actual)
}

// DependencyNotMetError is returned when a phase's prerequisite has not completed.
type DependencyNotMetError struct {
	Phase      string
	Dependency string
}

func (e *DependencyNotMetError) Error() string {
	return fmt.Sprintf("pipeline: phase %s requires %s to be completed",
		e.Phase, e.Dependency)
}

// PipelineTimeoutError is returned when the total pipeline duration exceeds
// the configured max_pipeline_duration limit.
type PipelineTimeoutError struct {
	Limit   time.Duration
	Elapsed time.Duration
	Phase   string // phase that was running (or about to start) when timeout fired
}

func (e *PipelineTimeoutError) Error() string {
	return fmt.Sprintf("pipeline: timeout after %s (limit %s) during phase %s",
		e.Elapsed.Truncate(time.Second), e.Limit.Truncate(time.Second), e.Phase)
}

// PhaseGateError is returned when domain gating fails after a phase succeeds.
type PhaseGateError struct {
	Phase  string
	Reason string
}

func (e *PhaseGateError) Error() string {
	return fmt.Sprintf("pipeline: phase %s gated: %s", e.Phase, e.Reason)
}

// reworkFinding is a minimal finding type used by reworkSignal for error
// message context. It carries only the fields needed to describe rework
// issues (severity + issue text), decoupled from schemas.ReviewFinding
// which carries additional review-specific fields (file, line, source, etc.).
type reworkFinding struct {
	Severity string
	Issue    string
}

// reworkSignal is an internal sentinel error used by gatePhase to signal the
// engine loop that a phase produced a "rework" verdict and the pipeline should
// route back to the configured target phase. This is NOT a terminal error —
// the engine loop catches it and handles routing.
type reworkSignal struct {
	target   string          // phase to route back to
	findings []reworkFinding // findings for error message context
}

func (e *reworkSignal) Error() string {
	var issues []string
	for _, finding := range e.findings {
		sev := strings.ToLower(finding.Severity)
		if sev == "critical" || sev == "major" {
			issues = append(issues, finding.Issue)
		}
	}
	if len(issues) > 0 {
		return "rework signal (target " + e.target + "): " + strings.Join(issues, "; ")
	}
	return "rework signal (target " + e.target + ")"
}
