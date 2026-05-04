package pipeline

import (
	"context"
	"errors"
	"time"
)

// sendNotification fires the configured notification hooks with a summary of
// the pipeline outcome. Notifications are best-effort: errors are emitted as
// events but do not affect the pipeline's return value.
func (e *Engine) sendNotification(runErr error) {
	if e.notifier == nil {
		return
	}

	result := e.buildPipelineResult(runErr)

	// Use a detached context so notifications are not cancelled by the
	// pipeline's context (which may already be done on failure/timeout).
	ctx, cancel := context.WithTimeout(context.Background(), defaultNotifyTimeout)
	defer cancel()

	if err := e.notifier.Notify(ctx, result); err != nil {
		e.emit(Event{
			Kind: EventNotifyFailed,
			Data: map[string]any{"error": err.Error()},
		})
		return
	}

	e.emit(Event{Kind: EventNotifySuccess})
}

// buildPipelineResult constructs a PipelineResult from the current engine state.
func (e *Engine) buildPipelineResult(runErr error) PipelineResult {
	meta := e.state.Meta()

	status := "success"
	var errMsg string
	if runErr != nil {
		errMsg = runErr.Error()
		var pte *PipelineTimeoutError
		if errors.As(runErr, &pte) {
			status = "timeout"
		} else {
			// Check for partial: some phases completed, some failed.
			hasCompleted := false
			hasFailed := false
			for _, ps := range meta.Phases {
				if ps.Status == PhaseCompleted {
					hasCompleted = true
				} else if ps.Status == PhaseFailed {
					hasFailed = true
				}
			}
			if hasCompleted && hasFailed {
				status = "partial"
			} else {
				status = "failed"
			}
		}
	}

	var duration string
	if !e.pipelineStart.IsZero() {
		duration = e.now().Sub(e.pipelineStart).Truncate(time.Second).String()
	}

	phases := make(map[string]any, len(meta.Phases))
	for name, ps := range meta.Phases {
		phases[name] = map[string]any{
			"status":          string(ps.Status),
			"cost":            ps.Cost,
			"duration_ms":     ps.DurationMs,
			"generation":      ps.Generation,
			"tokens_in":       ps.TokensIn,
			"tokens_out":      ps.TokensOut,
			"cache_tokens_in": ps.CacheTokensIn,
		}
	}

	return PipelineResult{
		Ticket:    meta.Ticket,
		Summary:   meta.Summary,
		Branch:    meta.Branch,
		Status:    status,
		Error:     errMsg,
		TotalCost: meta.TotalCost,
		Duration:  duration,
		Phases:    phases,
	}
}
