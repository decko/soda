package pipeline

import (
	"fmt"
	"strings"

	"github.com/decko/soda/schemas"
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

// DependencyNotMetError is returned when a phase's prerequisite has not completed.
type DependencyNotMetError struct {
	Phase      string
	Dependency string
}

func (e *DependencyNotMetError) Error() string {
	return fmt.Sprintf("pipeline: phase %s requires %s to be completed",
		e.Phase, e.Dependency)
}

// PhaseGateError is returned when domain gating fails after a phase succeeds.
type PhaseGateError struct {
	Phase  string
	Reason string
}

func (e *PhaseGateError) Error() string {
	return fmt.Sprintf("pipeline: phase %s gated: %s", e.Phase, e.Reason)
}

// reviewReworkSignal is an internal sentinel error used by gatePhase to
// signal the engine loop that the review phase produced a "rework" verdict
// and the pipeline should route back to implement. This is NOT a terminal
// error — the engine loop catches it and handles routing.
type reviewReworkSignal struct {
	findings []schemas.ReviewFinding
}

func (e *reviewReworkSignal) Error() string {
	var issues []string
	for _, finding := range e.findings {
		sev := strings.ToLower(finding.Severity)
		if sev == "critical" || sev == "major" {
			issues = append(issues, finding.Issue)
		}
	}
	if len(issues) > 0 {
		return "review rework signal: " + strings.Join(issues, "; ")
	}
	return "review rework signal"
}
