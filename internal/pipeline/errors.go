package pipeline

import "fmt"

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
