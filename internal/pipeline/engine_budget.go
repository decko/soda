package pipeline

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
