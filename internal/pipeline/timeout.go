package pipeline

import (
	"context"
	"errors"
	"time"
)

// applyPipelineTimeout wraps ctx with a deadline when MaxPipelineDuration is
// configured. It stores pipelineStart and pipelineDeadline so wrapTimeoutError
// can compute actual elapsed time and distinguish the pipeline's own deadline
// from an external parent context deadline. Returns the (possibly wrapped)
// context and a cancel function that must always be deferred.
func (e *Engine) applyPipelineTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.config.MaxPipelineDuration <= 0 {
		return ctx, func() {}
	}
	now := e.now()
	e.pipelineStart = now
	e.pipelineDeadline = now.Add(e.config.MaxPipelineDuration)
	return context.WithDeadline(ctx, e.pipelineDeadline)
}

// wrapTimeoutError checks whether err is a context deadline exceeded caused
// by the pipeline's own timeout (not an external parent context deadline).
// If so, it emits an EventPipelineTimeout event and returns a
// PipelineTimeoutError with actual elapsed time. Otherwise it returns err
// unchanged.
//
// To distinguish the pipeline's deadline from an external caller's deadline,
// the method compares ctx.Deadline() against e.pipelineDeadline. If they
// don't match (within a small tolerance), the deadline came from an external
// source (e.g., HTTP handler, CI timeout) and the error is returned as-is.
func (e *Engine) wrapTimeoutError(ctx context.Context, err error) error {
	if e.config.MaxPipelineDuration <= 0 {
		return err
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	// Guard: only wrap if the deadline that fired is the pipeline's own.
	// An external parent context with a shorter deadline would produce a
	// different deadline value, and wrapping that as PipelineTimeoutError
	// would produce misleading diagnostics. Per-phase timeouts create a
	// child context with an earlier deadline, so they also won't match.
	// An external parent context with a shorter deadline would produce a
	// different deadline value, and wrapping that as PipelineTimeoutError
	// would produce misleading diagnostics.
	if e.pipelineDeadline.IsZero() {
		return err
	}
	if ctxDeadline, ok := ctx.Deadline(); ok {
		// Allow 1s tolerance for clock jitter between WithDeadline creation
		// and this check.
		diff := ctxDeadline.Sub(e.pipelineDeadline)
		if diff < -time.Second || diff > time.Second {
			return err
		}
	}

	// Compute actual elapsed time from the stored start time.
	elapsed := e.now().Sub(e.pipelineStart)

	// Find the phase that was running when the timeout fired.
	phase := e.lastRunningPhase()

	// Persist failure category as "timeout" on the phase that was running.
	if phase != "unknown" {
		_ = e.state.SetFailureCategory(phase, "timeout")
	}

	e.emit(Event{
		Kind: EventPipelineTimeout,
		Data: map[string]any{
			"limit":   e.config.MaxPipelineDuration.String(),
			"elapsed": elapsed.String(),
			"phase":   phase,
		},
	})

	return &PipelineTimeoutError{
		Limit:   e.config.MaxPipelineDuration,
		Elapsed: elapsed,
		Phase:   phase,
	}
}

// lastRunningPhase returns the name of the phase that was active when the
// pipeline stopped. It checks for PhaseRunning first (preferred), then falls
// back to PhaseFailed — because runPhase calls MarkFailed before the error
// propagates to wrapTimeoutError, a timed-out phase will have PhaseFailed
// status by the time this method runs. Since phases execute sequentially and
// stop on first error, there will be at most one failed phase.
// Returns "unknown" if no running or failed phase is found.
func (e *Engine) lastRunningPhase() string {
	// Prefer PhaseRunning (e.g., parallel-review goroutines).
	for _, phase := range e.config.Pipeline.Phases {
		if ps := e.state.Meta().Phases[phase.Name]; ps != nil && ps.Status == PhaseRunning {
			return phase.Name
		}
	}
	// Fall back to PhaseFailed — the timed-out phase was marked failed
	// before the error propagated here. Iterate in reverse because phases
	// execute sequentially and stop on first error, so the LAST failed
	// phase in pipeline order is the one that just failed. Earlier phases
	// may retain stale PhaseFailed status from a prior run (e.g., when
	// Resume is called from a later phase).
	for i := len(e.config.Pipeline.Phases) - 1; i >= 0; i-- {
		phase := e.config.Pipeline.Phases[i]
		if ps := e.state.Meta().Phases[phase.Name]; ps != nil && ps.Status == PhaseFailed {
			return phase.Name
		}
	}
	return "unknown"
}
