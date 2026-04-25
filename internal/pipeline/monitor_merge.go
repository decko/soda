package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// defaultAutoMergeTimeout is used when AutoMergeTimeout is not configured.
const defaultAutoMergeTimeout = 30 * time.Minute

// autoMergeResult describes the outcome of an auto-merge attempt.
type autoMergeResult struct {
	// Merged is true when the PR was successfully merged (or was already merged).
	Merged bool
	// Blocked is true when a safeguard prevented merging (labels, CI, etc.).
	Blocked bool
	// BlockReason describes why the merge was blocked, when Blocked is true.
	BlockReason string
	// RebaseConflict is true when a merge conflict was detected.
	RebaseConflict bool
	// DryRun is true when the merge would proceed but auto_merge is not enabled.
	DryRun bool
	// TimedOut is true when the auto-merge timeout expired.
	TimedOut bool
}

// tryAutoMerge runs the auto-merge safeguard chain. The chain is:
//
//  1. Timeout — reject if too long since approval
//  2. Labels — reject if required labels are missing
//  3. Approval — reject if PR is not approved
//  4. CI freshness — reject if CI commit SHA ≠ PR head SHA
//  5. Branch protection — warn if dismiss_stale_reviews is on
//  6. Dry-run — if auto_merge is not enabled, log what would happen
//  7. Merge — attempt the merge, mapping conflicts to rebase events
//
// Each step either returns a blocking result or falls through to the next.
func (e *Engine) tryAutoMerge(
	ctx context.Context,
	phaseName string,
	monState *MonitorState,
	prStatus *PRStatus,
	ciStatus *CIStatus,
	polling *PollingConfig,
) autoMergeResult {
	now := e.now()

	// --- 1. Timeout check ---
	timeout := e.autoMergeTimeout(polling)
	if monState.ApprovalTime != nil && now.Sub(*monState.ApprovalTime) >= timeout {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventAutoMergeBlocked,
			Data: map[string]any{
				"reason":  "timeout",
				"elapsed": now.Sub(*monState.ApprovalTime).String(),
				"timeout": timeout.String(),
			},
		})
		return autoMergeResult{Blocked: true, BlockReason: "auto-merge timeout exceeded", TimedOut: true}
	}

	// --- 2. Label check ---
	requiredLabels := e.mergeLabels(polling)
	if len(requiredLabels) > 0 && prStatus != nil {
		missing := missingLabels(requiredLabels, prStatus.Labels)
		if len(missing) > 0 {
			e.emit(Event{
				Phase: phaseName,
				Kind:  EventAutoMergeBlocked,
				Data: map[string]any{
					"reason":  "missing_labels",
					"missing": missing,
				},
			})
			return autoMergeResult{Blocked: true, BlockReason: fmt.Sprintf("missing required labels: %v", missing)}
		}
	}

	// --- 3. Approval check ---
	if prStatus == nil || !prStatus.Approved {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventAutoMergeBlocked,
			Data:  map[string]any{"reason": "not_approved"},
		})
		return autoMergeResult{Blocked: true, BlockReason: "PR not approved"}
	}

	// --- 4. CI freshness check ---
	if ciStatus == nil || ciStatus.Overall != "success" {
		reason := "ci_not_green"
		overall := "unknown"
		if ciStatus != nil {
			overall = ciStatus.Overall
		}
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventAutoMergeBlocked,
			Data: map[string]any{
				"reason":    reason,
				"ci_status": overall,
			},
		})
		return autoMergeResult{Blocked: true, BlockReason: fmt.Sprintf("CI status is %s, not success", overall)}
	}

	// CI SHA freshness: ensure CI ran against the current head commit.
	if prStatus.HeadSHA != "" && ciStatus.CommitSHA != "" && prStatus.HeadSHA != ciStatus.CommitSHA {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventAutoMergeBlocked,
			Data: map[string]any{
				"reason":   "ci_sha_stale",
				"head_sha": prStatus.HeadSHA,
				"ci_sha":   ciStatus.CommitSHA,
			},
		})
		return autoMergeResult{Blocked: true, BlockReason: "CI ran against stale commit"}
	}

	// --- 5. Branch protection check ---
	if validator, ok := e.config.PRPoller.(MergeValidator); ok {
		if err := validator.ValidateMergePrerequisites(ctx, monState.PRURL); err != nil {
			e.emit(Event{
				Phase: phaseName,
				Kind:  EventMonitorWarning,
				Data: map[string]any{
					"warning": fmt.Sprintf("branch protection: %v", err),
				},
			})
			// This is a warning, not a blocking condition — the merge may
			// still succeed if the protection rule doesn't apply to the actor.
		}
	}

	// --- 6. Dry-run check ---
	autoMergeEnabled := polling != nil && polling.AutoMerge
	if !autoMergeEnabled {
		if !monState.DryRunLogged {
			e.emit(Event{
				Phase: phaseName,
				Kind:  EventAutoMergeDryRun,
				Data: map[string]any{
					"merge_method": e.mergeMethod(polling),
					"message":      "all safeguards passed; would merge if auto_merge were enabled",
				},
			})
			monState.DryRunLogged = true
		}
		return autoMergeResult{DryRun: true}
	}

	// --- 7. Merge ---
	method := e.mergeMethod(polling)
	err := e.config.PRPoller.MergePR(ctx, monState.PRURL, method)
	if err == nil {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventAutoMergeCompleted,
			Data: map[string]any{
				"merge_method": method,
			},
		})
		return autoMergeResult{Merged: true}
	}

	// Handle sentinel errors.
	if errors.Is(err, ErrPRAlreadyMerged) {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventAutoMergeCompleted,
			Data: map[string]any{
				"merge_method": method,
				"note":         "already merged by someone else",
			},
		})
		return autoMergeResult{Merged: true}
	}

	if errors.Is(err, ErrMergeConflict) {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventRebaseConflict,
			Data: map[string]any{
				"error": err.Error(),
			},
		})
		return autoMergeResult{RebaseConflict: true}
	}

	if errors.Is(err, ErrPRClosed) {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventAutoMergeBlocked,
			Data: map[string]any{
				"reason": "pr_closed",
				"error":  err.Error(),
			},
		})
		return autoMergeResult{Blocked: true, BlockReason: "PR is closed"}
	}

	// Unknown merge error — emit warning and block.
	e.emit(Event{
		Phase: phaseName,
		Kind:  EventAutoMergeBlocked,
		Data: map[string]any{
			"reason": "merge_failed",
			"error":  err.Error(),
		},
	})
	return autoMergeResult{Blocked: true, BlockReason: err.Error()}
}

// mergeMethod returns the configured merge method, defaulting to "squash".
// Checks PollingConfig first, then EngineConfig.
func (e *Engine) mergeMethod(polling *PollingConfig) string {
	if polling != nil && polling.MergeMethod != "" {
		return polling.MergeMethod
	}
	if e.config.MergeMethod != "" {
		return e.config.MergeMethod
	}
	return "squash"
}

// mergeLabels returns the configured required merge labels.
// Checks PollingConfig first, then EngineConfig.
func (e *Engine) mergeLabels(polling *PollingConfig) []string {
	if polling != nil && len(polling.MergeLabels) > 0 {
		return polling.MergeLabels
	}
	return e.config.MergeLabels
}

// autoMergeTimeout returns the configured auto-merge timeout.
// Checks PollingConfig first, then EngineConfig, then defaults.
func (e *Engine) autoMergeTimeout(polling *PollingConfig) time.Duration {
	if polling != nil && polling.AutoMergeTimeout.Duration > 0 {
		return polling.AutoMergeTimeout.Duration
	}
	if e.config.AutoMergeTimeout > 0 {
		return e.config.AutoMergeTimeout
	}
	return defaultAutoMergeTimeout
}

// missingLabels returns elements from required that are not in actual.
func missingLabels(required, actual []string) []string {
	have := make(map[string]bool, len(actual))
	for _, l := range actual {
		have[l] = true
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}
