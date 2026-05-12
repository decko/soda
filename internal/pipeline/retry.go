package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/decko/soda/internal/runner"
)

// runWithRetry runs the phase with per-category retry limits.
func (e *Engine) runWithRetry(ctx context.Context, phase PhaseConfig, opts runner.RunOpts) (*runner.RunResult, error) {
	remaining := map[string]int{
		"transient": phase.Retry.Transient,
		"parse":     phase.Retry.Parse,
		"semantic":  phase.Retry.Semantic,
	}

	parseFailures := 0
	attempt := 0
	for {
		if err := e.apiSem.Acquire(ctx); err != nil {
			return nil, fmt.Errorf("engine: phase %s semaphore acquire: %w", phase.Name, err)
		}
		result, err := e.runner.Run(ctx, opts)
		e.apiSem.Release()

		if err == nil {
			// Record first-attempt parse success when attempt is 0.
			if attempt == 0 {
				_ = e.state.RecordParseFirstSuccess(phase.Name)
			}
			return result, nil
		}

		category := classifyError(err)

		left, tracked := remaining[category]
		if !tracked || left <= 0 {
			return nil, &RetriesExhaustedError{
				Phase:    phase.Name,
				Category: category,
				Attempts: attempt + 1,
				Err:      err,
			}
		}
		remaining[category]--

		switch category {
		case "transient":
			delay := backoff(attempt, e.config.JitterFunc)
			e.config.SleepFunc(delay)
			retryData := map[string]any{"category": category, "attempt": attempt + 1, "delay": delay.String()}
			if suggestion := transientSuggestion(err); suggestion != "" {
				retryData["suggestion"] = suggestion
			}
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseRetrying,
				Data:  retryData,
			})

		case "parse":
			_ = e.state.AccumulateParseAttempt(phase.Name)
			parseFailures++

			// Model escalation: when parse failures exceed the threshold
			// and the phase is using a per-phase model (not already the
			// global model), switch to the global model mid-retry.
			threshold := e.config.Pipeline.ModelRouting.FallbackThreshold
			if threshold > 0 && parseFailures >= threshold && opts.Model != e.config.Model {
				previousModel := opts.Model
				opts.Model = e.config.Model
				e.emit(Event{
					Phase: phase.Name,
					Kind:  EventModelFallback,
					Data: map[string]any{
						"from":           previousModel,
						"to":             e.config.Model,
						"parse_failures": parseFailures,
					},
				})
			}

			var pe *runner.ParseError
			if errors.As(err, &pe) {
				opts.UserPrompt = opts.UserPrompt + "\n\n[RETRY] Previous attempt failed with parse error: " + pe.Error() + "\nPlease fix the output format."
			}
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseRetrying,
				Data:  map[string]any{"category": category, "attempt": attempt + 1},
			})

		case "semantic":
			var se *runner.SemanticError
			if errors.As(err, &se) {
				opts.UserPrompt = opts.UserPrompt + "\n\n[RETRY] Previous attempt returned a semantic error: " + se.Message + "\nPlease address this issue."
			}
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseRetrying,
				Data:  map[string]any{"category": category, "attempt": attempt + 1},
			})
		}

		_ = e.state.WriteLog(phase.Name, fmt.Sprintf("retry_%d", attempt+1),
			[]byte(fmt.Sprintf("category=%s err=%s", category, err)))

		attempt++
	}
}

// classifyError maps an error to a retry category using agent-agnostic
// runner error types. Backend runners (Claude, sandbox) are responsible
// for wrapping their specific errors into runner.* types before returning.
func classifyError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context"
	}
	var te *runner.TransientError
	if errors.As(err, &te) {
		return "transient"
	}
	var pe *runner.ParseError
	if errors.As(err, &pe) {
		return "parse"
	}
	var se *runner.SemanticError
	if errors.As(err, &se) {
		return "semantic"
	}
	return "unknown"
}

// backoff returns an exponential backoff duration capped at 30s, plus jitter.
func backoff(attempt int, jitterFunc func(time.Duration) time.Duration) time.Duration {
	base := 2 * time.Second
	exp := time.Duration(math.Pow(2, float64(attempt))) * base
	if exp > 30*time.Second {
		exp = 30 * time.Second
	}
	return exp + jitterFunc(time.Second)
}
