package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/decko/soda/internal/git"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
)

// runMonitor handles polling phases: polls the PR for changes, detects new
// comments, CI status changes, merge conflicts, and manages response rounds.
// Replaces the old runMonitorStub.
func (e *Engine) runMonitor(ctx context.Context, phase PhaseConfig) error {
	prURL := e.extractPRURL()
	if prURL == "" {
		return e.runMonitorStub(phase)
	}

	// Require a PRPoller to be configured.
	if e.config.PRPoller == nil {
		return e.runMonitorStub(phase)
	}

	polling := phase.Polling

	// Apply monitor profile if configured (profile on PollingConfig or EngineConfig).
	var profile *MonitorProfile
	if e.config.MonitorProfile != nil {
		profile = e.config.MonitorProfile
		polling = profile.ToPollingConfig()
		e.emit(Event{
			Phase: phase.Name,
			Kind:  EventMonitorProfileApplied,
			Data: map[string]any{
				"profile": string(profile.Name),
				"source":  "engine_config",
			},
		})
	} else if polling != nil && polling.Profile != "" {
		p, err := GetMonitorProfile(polling.Profile)
		if err != nil {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventMonitorWarning,
				Data: map[string]any{
					"warning": fmt.Sprintf("invalid profile %q, using defaults: %v", polling.Profile, err),
				},
			})
		} else {
			profile = p
			polling = profile.ToPollingConfig()
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventMonitorProfileApplied,
				Data: map[string]any{
					"profile": string(profile.Name),
					"source":  "polling_config",
				},
			})
		}
	}

	if polling == nil {
		polling = &PollingConfig{
			InitialInterval:   Duration{Duration: 2 * time.Minute},
			MaxInterval:       Duration{Duration: 5 * time.Minute},
			EscalateAfter:     Duration{Duration: 30 * time.Minute},
			MaxDuration:       Duration{Duration: 4 * time.Hour},
			MaxResponseRounds: 3,
		}
	}

	// Check dependencies.
	for _, dep := range phase.DependsOn {
		if !e.state.IsCompleted(dep) {
			return &DependencyNotMetError{Phase: phase.Name, Dependency: dep}
		}
	}

	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: mark running %s: %w", phase.Name, err)
	}
	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted, Data: map[string]any{"generation": e.state.Meta().Phases[phase.Name].Generation}})

	// Initialize or resume monitor state.
	monState, err := e.state.ReadMonitorState()
	if err != nil {
		monState = &MonitorState{
			PRURL:             prURL,
			MaxResponseRounds: polling.MaxResponseRounds,
			Status:            MonitorPolling,
		}
	}
	monState.PRURL = prURL
	monState.Status = MonitorPolling

	if err := e.state.WriteMonitorState(monState); err != nil {
		return fmt.Errorf("engine: write initial monitor state: %w", err)
	}

	startTime := e.now()

	// Polling loop.
	for {
		// Check context cancellation.
		if err := ctx.Err(); err != nil {
			monState.Status = MonitorFailed
			_ = e.state.WriteMonitorState(monState)
			return fmt.Errorf("engine: monitor context cancelled: %w", err)
		}

		// Check max duration timeout.
		now := e.now()
		if now.Sub(startTime) >= polling.MaxDuration.Duration {
			monState.Status = MonitorFailed
			_ = e.state.WriteMonitorState(monState)
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventMonitorTimeout,
				Data: map[string]any{
					"duration":   now.Sub(startTime).String(),
					"poll_count": monState.PollCount,
				},
			})
			timeoutErr := fmt.Errorf("monitor: max duration %s exceeded", polling.MaxDuration.Duration)
			if err := e.state.MarkFailed(phase.Name, timeoutErr); err != nil {
				return fmt.Errorf("engine: mark failed %s: %w", phase.Name, err)
			}
			e.emitPhaseFailed(phase.Name, timeoutErr)
			return nil
		}

		monState.PollCount++
		monState.LastPolledAt = now
		e.emit(Event{
			Phase: phase.Name,
			Kind:  EventMonitorPolling,
			Data: map[string]any{
				"poll_count":      monState.PollCount,
				"response_rounds": monState.ResponseRounds,
			},
		})

		// 1. Check PR status (approved, closed, merged).
		terminal, err := e.checkPRStatus(ctx, phase.Name, monState)
		if err != nil {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventMonitorWarning,
				Data:  map[string]any{"warning": fmt.Sprintf("check PR status: %v", err)},
			})
			// Non-fatal — continue polling.
		}
		if terminal {
			_ = e.state.WriteMonitorState(monState)
			if monState.Status == MonitorCompleted {
				if err := e.state.MarkCompleted(phase.Name); err != nil {
					return fmt.Errorf("engine: mark completed %s: %w", phase.Name, err)
				}
				e.emit(Event{
					Phase: phase.Name,
					Kind:  EventPhaseCompleted,
					Data:  map[string]any{"duration_ms": e.state.Meta().Phases[phase.Name].DurationMs},
				})
			} else {
				failErr := fmt.Errorf("monitor: PR %s", monState.Status)
				if err := e.state.MarkFailed(phase.Name, failErr); err != nil {
					return fmt.Errorf("engine: mark failed %s: %w", phase.Name, err)
				}
				e.emitPhaseFailed(phase.Name, failErr)
			}
			return nil
		}

		// 2. Check for new comments.
		classified := e.checkNewComments(ctx, phase.Name, monState)

		// 2a. Post canned acknowledgment for non-authoritative comments.
		e.postAcknowledgments(ctx, phase.Name, classified, monState)

		// 2b. Response execution: run a Claude session for actionable comments.
		if HasActionable(classified) {
			monState.ResponseRounds++
			if e.runner != nil {
				// Check budget before each response round.
				if e.config.MaxCostUSD > 0 && e.state.Meta().TotalCost >= e.config.MaxCostUSD {
					e.emit(Event{
						Phase: phase.Name,
						Kind:  EventBudgetWarning,
						Data: map[string]any{
							"total_cost":     e.state.Meta().TotalCost,
							"limit":          e.config.MaxCostUSD,
							"skipping":       "monitor_response",
							"response_round": monState.ResponseRounds,
						},
					})
				} else {
					output, err := e.respondToComments(ctx, phase, classified, monState)
					if err != nil {
						// Non-fatal: log and continue polling.
						e.emit(Event{
							Phase: phase.Name,
							Kind:  EventMonitorResponseFailed,
							Data: map[string]any{
								"response_round": monState.ResponseRounds,
								"error":          err.Error(),
							},
						})
					} else if output != nil {
						// Programmatic verify gate: when files were changed,
						// check tests_passed to decide whether to allow push.
						e.gateMonitorResponse(ctx, phase, output, monState)

						// Post a summary reply to the PR.
						e.postResponseSummary(ctx, phase.Name, output, monState)
					}
				}
			}
		}

		// 3. Check CI status changes.
		e.checkCIStatus(ctx, phase.Name, monState)

		// 4. Check for merge conflicts and auto-rebase.
		autoRebase := profile == nil || profile.ShouldAutoRebase()
		e.checkMergeConflicts(ctx, phase.Name, monState, autoRebase)

		// 5. Check max response rounds.
		if monState.ResponseRounds >= monState.MaxResponseRounds {
			monState.Status = MonitorMaxRounds
			_ = e.state.WriteMonitorState(monState)
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventMonitorMaxRounds,
				Data: map[string]any{
					"response_rounds":     monState.ResponseRounds,
					"max_response_rounds": monState.MaxResponseRounds,
				},
			})
			if err := e.state.MarkCompleted(phase.Name); err != nil {
				return fmt.Errorf("engine: mark completed %s: %w", phase.Name, err)
			}
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventPhaseCompleted,
				Data: map[string]any{
					"duration_ms": e.state.Meta().Phases[phase.Name].DurationMs,
					"reason":      "max_rounds_reached",
				},
			})
			return nil
		}

		// Persist state after each poll cycle.
		if err := e.state.WriteMonitorState(monState); err != nil {
			return fmt.Errorf("engine: write monitor state: %w", err)
		}

		// Sleep until next poll.
		interval := e.pollInterval(polling, now.Sub(startTime))
		e.config.SleepFunc(interval)
	}
}

// checkPRStatus checks the PR for terminal states (approved, closed, merged).
// Returns true if the polling should stop.
func (e *Engine) checkPRStatus(ctx context.Context, phaseName string, monState *MonitorState) (bool, error) {
	status, err := e.config.PRPoller.GetPRStatus(ctx, monState.PRURL)
	if err != nil {
		return false, err
	}

	switch status.State {
	case "merged":
		monState.Status = MonitorCompleted
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorPRApproved,
			Data:  map[string]any{"state": "merged"},
		})
		return true, nil

	case "closed":
		monState.Status = MonitorFailed
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorPRClosed,
			Data:  map[string]any{"state": "closed"},
		})
		return true, nil
	}

	if status.Approved {
		monState.Status = MonitorCompleted
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorPRApproved,
			Data:  map[string]any{"state": "approved"},
		})
		return true, nil
	}

	return false, nil
}

// checkNewComments polls for new review comments and classifies them.
// Returns the classified comments so the caller can trigger response execution.
// ResponseRounds is NOT incremented here — the caller handles that after
// response execution.
func (e *Engine) checkNewComments(ctx context.Context, phaseName string, monState *MonitorState) []ClassifiedComment {
	comments, err := e.config.PRPoller.GetNewComments(ctx, monState.PRURL, monState.LastCommentID)
	if err != nil {
		// Non-fatal: emit warning and continue.
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorWarning,
			Data:  map[string]any{"warning": fmt.Sprintf("get new comments: %v", err)},
		})
		return nil
	}

	if len(comments) == 0 {
		return nil
	}

	// Update last comment ID to the latest one.
	lastComment := comments[len(comments)-1]
	monState.LastCommentID = lastComment.ID

	// Build classifier using engine config.
	classifier := NewCommentClassifier(
		e.config.SelfUser,
		e.config.BotUsers,
		e.config.AuthorityResolver,
	)

	classified := classifier.ClassifyAll(comments)

	// Emit per-comment classification events.
	for _, cc := range classified {
		if cc.Actionable {
			e.emit(Event{
				Phase: phaseName,
				Kind:  EventMonitorCommentClassified,
				Data: map[string]any{
					"comment_id": cc.Comment.ID,
					"author":     cc.Comment.Author,
					"type":       string(cc.Type),
					"action":     string(cc.Action),
					"reason":     cc.Reason,
				},
			})
		} else {
			e.emit(Event{
				Phase: phaseName,
				Kind:  EventMonitorCommentSkipped,
				Data: map[string]any{
					"comment_id": cc.Comment.ID,
					"author":     cc.Comment.Author,
					"type":       string(cc.Type),
					"reason":     cc.Reason,
				},
			})
		}
	}

	actionable := HasActionable(classified)

	commentSummaries := make([]map[string]any, 0, len(classified))
	for _, cc := range classified {
		commentSummaries = append(commentSummaries, map[string]any{
			"id":         cc.Comment.ID,
			"author":     cc.Comment.Author,
			"path":       cc.Comment.Path,
			"type":       string(cc.Type),
			"action":     string(cc.Action),
			"actionable": cc.Actionable,
		})
	}

	e.emit(Event{
		Phase: phaseName,
		Kind:  EventMonitorNewComments,
		Data: map[string]any{
			"count":           len(comments),
			"actionable":      actionable,
			"response_rounds": monState.ResponseRounds,
			"comments":        commentSummaries,
		},
	})

	return classified
}

// checkCIStatus polls CI status and emits events on status changes.
func (e *Engine) checkCIStatus(ctx context.Context, phaseName string, monState *MonitorState) {
	ciStatus, err := e.config.PRPoller.GetCIStatus(ctx, monState.PRURL)
	if err != nil {
		// Non-fatal: emit warning and continue.
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorWarning,
			Data:  map[string]any{"warning": fmt.Sprintf("get CI status: %v", err)},
		})
		return
	}

	if ciStatus.Overall == monState.LastCIStatus {
		return
	}

	previousStatus := monState.LastCIStatus
	monState.LastCIStatus = ciStatus.Overall

	e.emit(Event{
		Phase: phaseName,
		Kind:  EventMonitorCIChange,
		Data: map[string]any{
			"previous": previousStatus,
			"current":  ciStatus.Overall,
		},
	})

	// On CI failure, emit detailed notification with job names.
	if ciStatus.Overall == "failure" {
		var failedJobs []string
		for _, job := range ciStatus.Jobs {
			if job.Conclusion == "failure" || job.Conclusion == "timed_out" || job.Conclusion == "cancelled" {
				failedJobs = append(failedJobs, job.Name)
			}
		}
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorCIFailure,
			Data: map[string]any{
				"failed_jobs": failedJobs,
				"job_count":   len(ciStatus.Jobs),
			},
		})
	}
}

// checkMergeConflicts detects merge conflicts and attempts auto-rebase
// if the monitor profile allows it (or if no profile is configured).
func (e *Engine) checkMergeConflicts(ctx context.Context, phaseName string, monState *MonitorState, autoRebase bool) {
	workDir := e.state.Meta().Worktree
	if workDir == "" {
		workDir = e.config.WorkDir
	}

	baseBranch := e.config.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Fetch latest base branch before checking.
	_ = git.FetchBranch(ctx, workDir, "origin", baseBranch)

	hasConflicts, err := git.NeedsMergeWithBase(ctx, workDir, "origin/"+baseBranch)
	if err != nil {
		// Non-fatal: some setups may not have origin configured.
		return
	}
	if !hasConflicts {
		return
	}

	e.emit(Event{
		Phase: phaseName,
		Kind:  EventMonitorConflict,
		Data:  map[string]any{"base_branch": baseBranch},
	})

	// Skip auto-rebase if profile disallows it.
	if !autoRebase {
		return
	}

	// Attempt auto-rebase.
	if err := git.Rebase(ctx, workDir, "origin/"+baseBranch); err != nil {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorRebaseFailed,
			Data: map[string]any{
				"error":       err.Error(),
				"base_branch": baseBranch,
			},
		})
		return
	}

	// Push the rebased branch.
	if err := git.ForcePush(ctx, workDir, "origin"); err != nil {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorRebaseFailed,
			Data: map[string]any{
				"error":  "push after rebase failed: " + err.Error(),
				"action": "push",
			},
		})
		return
	}

	e.emit(Event{
		Phase: phaseName,
		Kind:  EventMonitorRebaseOK,
		Data:  map[string]any{"base_branch": baseBranch},
	})
}

// pollInterval computes the sleep duration between polls.
// Uses initial_interval until escalate_after elapsed, then max_interval.
func (e *Engine) pollInterval(polling *PollingConfig, elapsed time.Duration) time.Duration {
	if elapsed >= polling.EscalateAfter.Duration {
		return polling.MaxInterval.Duration
	}
	return polling.InitialInterval.Duration
}

// respondToComments runs a Claude session to address actionable review comments.
// It renders the monitor prompt with the classified comments, invokes the runner,
// and records the result. Returns the parsed MonitorOutput and any error.
func (e *Engine) respondToComments(ctx context.Context, phase PhaseConfig, classified []ClassifiedComment, monState *MonitorState) (*schemas.MonitorOutput, error) {
	actionableCount := countActionable(classified)
	replyOnly := isReplyOnly(classified)
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventMonitorResponseStarted,
		Data: map[string]any{
			"response_round":   monState.ResponseRounds,
			"comment_count":    len(classified),
			"actionable_count": actionableCount,
			"reply_only":       replyOnly,
		},
	})

	// Build prompt data from phase dependencies.
	promptData, err := e.buildPromptData(phase)
	if err != nil {
		return nil, fmt.Errorf("engine: build prompt data for monitor response: %w", err)
	}

	// Inject classified review comments into the prompt.
	promptData.ReviewComments = formatCommentsForPrompt(classified)

	// Inject diff context so the Claude session has the current changes.
	workDir := e.workDir(phase)
	baseBranch := e.config.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}
	diffCtx, err := git.Diff(ctx, workDir, "origin/"+baseBranch, 50000)
	if err != nil {
		// Non-fatal: the session can still read files.
		diffCtx = "(diff unavailable: " + err.Error() + ")"
	}
	if diffCtx == "" {
		diffCtx = "(no differences from base branch)"
	}
	promptData.DiffContext = diffCtx

	// Load and render the monitor prompt template.
	loadResult, err := e.config.Loader.LoadWithSource(phase.Prompt)
	if err != nil {
		return nil, fmt.Errorf("engine: load monitor prompt: %w", err)
	}

	rendered, err := RenderPrompt(loadResult.Content, promptData)
	if err != nil {
		return nil, fmt.Errorf("engine: render monitor prompt: %w", err)
	}

	_ = e.state.WriteLog(phase.Name, fmt.Sprintf("response_%d_prompt", monState.ResponseRounds), []byte(rendered))

	// Build runner opts with remaining budget.
	remaining := 0.0
	if e.config.MaxCostUSD > 0 {
		remaining = e.config.MaxCostUSD - e.state.Meta().TotalCost
	}

	// Restrict tools for reply-only sessions (questions only — no code changes).
	tools := phase.Tools
	if replyOnly {
		tools = replyOnlyTools(phase.Tools)
	}

	opts := runner.RunOpts{
		Phase:        fmt.Sprintf("%s/response_%d", phase.Name, monState.ResponseRounds),
		SystemPrompt: rendered,
		UserPrompt:   "Address the review comments described in the system prompt.",
		OutputSchema: phase.Schema,
		AllowedTools: tools,
		MaxBudgetUSD: remaining,
		WorkDir:      e.workDir(phase),
		Model:        e.config.Model,
		Timeout:      phase.Timeout.Duration,
	}

	result, err := e.runner.Run(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("engine: monitor response round %d: %w", monState.ResponseRounds, err)
	}

	// Record cost.
	if err := e.state.AccumulateCost(phase.Name, result.CostUSD); err != nil {
		return nil, fmt.Errorf("engine: accumulate monitor response cost: %w", err)
	}

	// Parse output.
	var output schemas.MonitorOutput
	if result.Output != nil {
		if parseErr := json.Unmarshal(result.Output, &output); parseErr != nil {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventMonitorWarning,
				Data: map[string]any{
					"warning": fmt.Sprintf("parse monitor response output: %v", parseErr),
				},
			})
		}
	}

	// Write response output as a log for debugging.
	if result.Output != nil {
		_ = e.state.WriteLog(phase.Name, fmt.Sprintf("response_%d_output", monState.ResponseRounds), result.Output)
	}

	// Write the result so PhaseSummary and other consumers can read it.
	// Each response round overwrites the previous result; the final round
	// represents the latest state.
	if result.Output != nil {
		_ = e.state.WriteResult(phase.Name, result.Output)
	}

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventMonitorResponseCompleted,
		Data: map[string]any{
			"response_round":   monState.ResponseRounds,
			"comments_handled": len(output.CommentsHandled),
			"files_changed":    len(output.FilesChanged),
			"commits":          len(output.Commits),
			"cost":             result.CostUSD,
		},
	})

	return &output, nil
}

// gateMonitorResponse enforces a programmatic verify gate after response
// execution. When files were changed (fix session), it checks tests_passed:
//   - true  → allow (commit+push was done by the session)
//   - false → emit notify_user event so the user is alerted; the session
//     should NOT have pushed (the prompt instructs it not to), but
//     the engine does not try to undo pushes.
//
// Reply-only sessions (no files changed) skip this gate entirely.
func (e *Engine) gateMonitorResponse(ctx context.Context, phase PhaseConfig, output *schemas.MonitorOutput, monState *MonitorState) {
	if len(output.FilesChanged) == 0 {
		// No code changes — nothing to gate.
		return
	}

	if output.TestsPassed {
		// Verification passed — commit+push is allowed.
		return
	}

	// Tests failed: notify the user.
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventMonitorVerifyFailed,
		Data: map[string]any{
			"response_round": monState.ResponseRounds,
			"files_changed":  len(output.FilesChanged),
			"commits":        len(output.Commits),
		},
	})
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventMonitorNotifyUser,
		Data: map[string]any{
			"reason":         "verification failed after monitor code changes",
			"response_round": monState.ResponseRounds,
			"files_changed":  len(output.FilesChanged),
		},
	})
}

// postAcknowledgments posts a canned acknowledgment reply to the PR for
// non-authoritative comments that were classified with ActionAcknowledge.
func (e *Engine) postAcknowledgments(ctx context.Context, phaseName string, classified []ClassifiedComment, monState *MonitorState) {
	if e.config.PRPoller == nil {
		return
	}

	for _, cc := range classified {
		if cc.Action != ActionAcknowledge || cc.Actionable {
			continue
		}
		// Only acknowledge non-authoritative users (skip approvals from auth users).
		if cc.Reason != "comment from non-authoritative user" {
			continue
		}

		body := fmt.Sprintf(
			"Thanks for the feedback, @%s! I've noted your comment. "+
				"A project maintainer will review and decide whether to act on it.",
			cc.Comment.Author,
		)

		if err := e.config.PRPoller.PostComment(ctx, monState.PRURL, body); err != nil {
			e.emit(Event{
				Phase: phaseName,
				Kind:  EventMonitorWarning,
				Data: map[string]any{
					"warning":    fmt.Sprintf("failed to post acknowledgment for %s: %v", cc.Comment.ID, err),
					"comment_id": cc.Comment.ID,
				},
			})
			continue
		}

		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorAcknowledgePosted,
			Data: map[string]any{
				"comment_id": cc.Comment.ID,
				"author":     cc.Comment.Author,
			},
		})
	}
}

// postResponseSummary posts a summary reply to the PR after a response
// session completes. This makes the response visible to reviewers on the PR.
func (e *Engine) postResponseSummary(ctx context.Context, phaseName string, output *schemas.MonitorOutput, monState *MonitorState) {
	if e.config.PRPoller == nil || output == nil {
		return
	}

	if len(output.CommentsHandled) == 0 {
		return
	}

	var sb strings.Builder
	sb.WriteString("I've addressed the review feedback:\n\n")

	for _, handled := range output.CommentsHandled {
		action := handled.Action
		if action == "" {
			action = "handled"
		}
		sb.WriteString(fmt.Sprintf("- **%s** %s's comment", action, handled.Author))
		if handled.CommentID != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", handled.CommentID))
		}
		if handled.Response != "" {
			sb.WriteString(": " + handled.Response)
		}
		sb.WriteString("\n")
	}

	if len(output.FilesChanged) > 0 {
		sb.WriteString(fmt.Sprintf("\n%d file(s) changed", len(output.FilesChanged)))
		if output.TestsPassed {
			sb.WriteString(", tests passing ✅")
		} else {
			sb.WriteString(", tests failing ⚠️")
		}
		sb.WriteString("\n")
	}

	if err := e.config.PRPoller.PostComment(ctx, monState.PRURL, sb.String()); err != nil {
		e.emit(Event{
			Phase: phaseName,
			Kind:  EventMonitorWarning,
			Data: map[string]any{
				"warning": fmt.Sprintf("failed to post response summary: %v", err),
			},
		})
		return
	}

	e.emit(Event{
		Phase: phaseName,
		Kind:  EventMonitorReplyPosted,
		Data: map[string]any{
			"response_round":   monState.ResponseRounds,
			"comments_handled": len(output.CommentsHandled),
		},
	})
}

// formatCommentsForPrompt converts classified comments into a human-readable
// format suitable for injection into the monitor prompt template's
// ReviewComments field.
func formatCommentsForPrompt(classified []ClassifiedComment) string {
	var sb strings.Builder
	for idx, cc := range classified {
		if idx > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("### Comment %s by %s\n", cc.Comment.ID, cc.Comment.Author))
		sb.WriteString(fmt.Sprintf("Classification: %s | Action: %s\n", cc.Type, cc.Action))
		if cc.Comment.Path != "" {
			if cc.Comment.Line > 0 {
				sb.WriteString(fmt.Sprintf("File: %s:%d\n", cc.Comment.Path, cc.Comment.Line))
			} else {
				sb.WriteString(fmt.Sprintf("File: %s\n", cc.Comment.Path))
			}
		}
		sb.WriteString("\n")
		sb.WriteString(cc.Comment.Body)
		sb.WriteString("\n")
	}
	return sb.String()
}

// countActionable returns the number of actionable comments in a classified slice.
func countActionable(classified []ClassifiedComment) int {
	count := 0
	for _, cc := range classified {
		if cc.Actionable {
			count++
		}
	}
	return count
}

// isReplyOnly returns true when every actionable comment in classified
// is a question (ActionRespond) and none require code changes (ActionApplyFix).
// Used to decide whether to restrict the Claude session's tool set.
func isReplyOnly(classified []ClassifiedComment) bool {
	hasActionable := false
	for _, cc := range classified {
		if !cc.Actionable {
			continue
		}
		hasActionable = true
		if cc.Action == ActionApplyFix {
			return false
		}
	}
	return hasActionable
}

// replyOnlyTools returns a restricted tool set that excludes Write and Edit,
// suitable for reply-only sessions that should not modify code.
func replyOnlyTools(tools []string) []string {
	var filtered []string
	for _, t := range tools {
		if t == "Write" || t == "Edit" {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// hasCodeChanges returns true when at least one actionable comment has
// ActionApplyFix, meaning the session may modify files.
func hasCodeChanges(classified []ClassifiedComment) bool {
	for _, cc := range classified {
		if cc.Actionable && cc.Action == ActionApplyFix {
			return true
		}
	}
	return false
}
