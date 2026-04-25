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

// PhaseNotFoundError is returned when Resume is called with a phase name
// that does not exist in the pipeline configuration.
type PhaseNotFoundError struct {
	Phase    string
	Pipeline []string // names of available phases for diagnostics
}

func (e *PhaseNotFoundError) Error() string {
	return fmt.Sprintf("engine: phase %q not found in pipeline (available: %s)",
		e.Phase, strings.Join(e.Pipeline, ", "))
}

// RetriesExhaustedError is returned when a phase fails after exhausting all
// retries for a given error category. It wraps the last error encountered
// so callers can inspect the root cause.
type RetriesExhaustedError struct {
	Phase    string
	Category string // "transient", "parse", "semantic", "unknown"
	Attempts int    // total attempts (initial + retries)
	Err      error  // last error encountered
}

func (e *RetriesExhaustedError) Error() string {
	return fmt.Sprintf("engine: phase %s failed after %d attempts (%s): %s",
		e.Phase, e.Attempts, e.Category, e.Err)
}

func (e *RetriesExhaustedError) Unwrap() error { return e.Err }

// WorktreeError is returned when worktree creation fails. It wraps the
// underlying git error so callers can inspect the root cause.
type WorktreeError struct {
	Branch string
	Path   string // attempted worktree path; may be empty on early failures
	Err    error
}

func (e *WorktreeError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("engine: create worktree for branch %s at %s: %s",
			e.Branch, e.Path, e.Err)
	}
	return fmt.Sprintf("engine: create worktree for branch %s: %s",
		e.Branch, e.Err)
}

func (e *WorktreeError) Unwrap() error { return e.Err }

// PromptError is returned when a phase prompt cannot be loaded or rendered.
// It wraps the underlying error and records the phase and operation for
// diagnostics.
type PromptError struct {
	Phase     string
	Operation string // "load" or "render"
	Err       error
}

func (e *PromptError) Error() string {
	return fmt.Sprintf("engine: prompt %s for phase %s: %s",
		e.Operation, e.Phase, e.Err)
}

func (e *PromptError) Unwrap() error { return e.Err }

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
