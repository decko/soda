package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/decko/soda/internal/progress"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
)

// reviewerResult holds the outcome of a single reviewer subagent.
type reviewerResult struct {
	Name          string
	Findings      []schemas.ReviewFinding
	Cost          float64
	TokensIn      int64
	TokensOut     int64
	CacheTokensIn int64
	Err           error
}

// reviewerMsg is sent from reviewer goroutines to the parent via channel.
type reviewerMsg struct {
	Event  *Event          // emit this event (nil if not an event)
	Log    *reviewerLog    // write this log (nil if not a log)
	Result *reviewerResult // final result (nil if not done)
	Index  int
}

// reviewerLog holds data for a deferred WriteLog call.
type reviewerLog struct {
	Phase    string
	Name     string
	Content  []byte
	IsPrompt bool // true when this log carries a rendered prompt (used for PromptHash)
}

// loadPriorReview reads and parses the archived review result from the
// previous generation. Returns nil when there is no prior generation (first
// run) or the archived result cannot be read/parsed. On read/parse errors
// an event is emitted so operators can detect the failure; the caller should
// treat a nil return as "no prior data available" and run all reviewers
// (safe fallback).
func (e *Engine) loadPriorReview(phaseName string) *schemas.ReviewOutput {
	ps := e.state.Meta().Phases[phaseName]
	if ps == nil || ps.Generation <= 1 {
		return nil
	}

	raw, err := e.state.ReadArchivedResult(phaseName, ps.Generation-1)
	if err != nil {
		e.emit(Event{
			Phase: phaseName,
			Kind:  "prior_review_warning",
			Data:  map[string]any{"warning": "failed to read archived review result", "error": err.Error()},
		})
		return nil
	}

	var prevReview schemas.ReviewOutput
	if err := json.Unmarshal(raw, &prevReview); err != nil {
		e.emit(Event{
			Phase: phaseName,
			Kind:  "prior_review_warning",
			Data:  map[string]any{"warning": "failed to unmarshal archived review result", "error": err.Error()},
		})
		return nil
	}

	return &prevReview
}

// neededReviewersFromPrior derives the set of reviewer names that produced at
// least one critical or major finding from a prior review result. Returns nil
// when prev is nil (first run or load failure), meaning all reviewers should
// run.
func neededReviewersFromPrior(prev *schemas.ReviewOutput) map[string]bool {
	if prev == nil {
		return nil
	}
	needed := make(map[string]bool)
	for _, finding := range prev.Findings {
		sev := strings.ToLower(finding.Severity)
		if sev == "critical" || sev == "major" {
			// Source may be a comma-joined string from dedup merging
			// (e.g. "go-specialist, ai-harness"). Split to mark each
			// individual reviewer as needed.
			for _, src := range strings.Split(finding.Source, ", ") {
				needed[src] = true
			}
		}
	}
	return needed
}

// priorFindingsForReviewer returns the findings from a prior review result
// that belong to the given reviewer. This is used to carry forward minor
// findings when a reviewer is skipped on rework cycles, ensuring they are
// not silently dropped from the merged output.
func priorFindingsForReviewer(prev *schemas.ReviewOutput, reviewerName string) []schemas.ReviewFinding {
	if prev == nil {
		return nil
	}
	var findings []schemas.ReviewFinding
	for _, f := range prev.Findings {
		// Source may be a comma-joined string from dedup merging
		// (e.g. "go-specialist, ai-harness"). Check each component.
		for _, src := range strings.Split(f.Source, ", ") {
			if src == reviewerName {
				findings = append(findings, f)
				break
			}
		}
	}
	return findings
}

// runParallelReview dispatches specialist reviewer subagents in parallel,
// collects their findings, merges them into a single ReviewOutput, and
// computes a verdict.
func (e *Engine) runParallelReview(ctx context.Context, phase PhaseConfig) error {
	if len(phase.Reviewers) == 0 {
		return fmt.Errorf("engine: parallel-review phase %s has no reviewers configured", phase.Name)
	}

	// Check dependencies.
	for _, dep := range phase.DependsOn {
		if !e.state.IsCompleted(dep) {
			return &DependencyNotMetError{Phase: phase.Name, Dependency: dep}
		}
	}

	// Check budget.
	if err := e.checkBudget(phase); err != nil {
		return err
	}

	// Pre-run per-phase budget check: prevent starting a new generation
	// when cumulative cost already exceeds (or meets) the per-phase limit.
	if err := e.checkPhaseBudget(phase); err != nil {
		return err
	}

	// Mark phase running.
	if err := e.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("engine: mark running %s: %w", phase.Name, err)
	}
	e.emit(Event{Phase: phase.Name, Kind: EventPhaseStarted, Data: map[string]any{"generation": e.state.Meta().Phases[phase.Name].Generation}})

	// Build prompt data shared by all reviewers.
	promptData, err := e.buildPromptData(phase)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: build prompt data for %s: %w", phase.Name, err)
	}

	// Snapshot budget before launching goroutines — avoid concurrent Meta() reads.
	budgetRemaining := 0.0
	if e.config.MaxCostUSD > 0 {
		budgetRemaining = e.config.MaxCostUSD - e.state.Meta().TotalCost
	}
	// Cap with per-phase limit: use the remaining per-phase budget
	// (MaxCostPerPhase minus cumulative cost already spent) as the tighter bound.
	if e.config.MaxCostPerPhase > 0 {
		perPhaseRemaining := e.config.MaxCostPerPhase - e.state.Meta().Phases[phase.Name].CumulativeCost
		if budgetRemaining <= 0 || perPhaseRemaining < budgetRemaining {
			budgetRemaining = perPhaseRemaining
		}
	}
	// Cap with per-generation limit.
	if e.config.MaxCostPerGeneration > 0 {
		genRemaining := e.config.MaxCostPerGeneration - e.state.Meta().Phases[phase.Name].Cost
		if budgetRemaining <= 0 || genRemaining < budgetRemaining {
			budgetRemaining = genRemaining
		}
	}

	// On rework cycles, load the prior review result once and derive which
	// reviewers can be skipped. A reviewer is redundant if it had no
	// critical/major findings in the previous review — only reviewers that
	// flagged actionable issues need to re-verify the fixes.
	//
	// loadPriorReview returns nil on the first run or on load failure; in
	// both cases neededReviewers will be nil and all reviewers run (safe
	// fallback that avoids silently losing findings).
	priorReview := e.loadPriorReview(phase.Name)
	neededReviewers := neededReviewersFromPrior(priorReview)

	// Channel for reviewer goroutines to send messages to the parent.
	msgCh := make(chan reviewerMsg, len(phase.Reviewers)*10)

	// Dispatch reviewers in parallel, skipping redundant ones.
	var wg sync.WaitGroup
	results := make([]reviewerResult, len(phase.Reviewers))

	for idx, reviewer := range phase.Reviewers {
		if idx > 0 && phase.ReviewerStagger.Duration > 0 {
			e.config.SleepFunc(phase.ReviewerStagger.Duration)
		}

		// Skip reviewers that had no critical/major findings in the prior
		// cycle. neededReviewers is nil on the first run, so all run.
		if neededReviewers != nil && !neededReviewers[reviewer.Name] {
			// Carry forward minor findings from the prior cycle so they are
			// not silently dropped from the merged output. Without this, the
			// verdict could incorrectly become "pass" instead of
			// "pass-with-follow-ups", causing the post-submit follow-up
			// phase to be skipped.
			carried := priorFindingsForReviewer(priorReview, reviewer.Name)
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventReviewerSkipped,
				Data:  map[string]any{"reviewer": reviewer.Name, "reason": "no critical/major findings in prior cycle", "carried_findings": len(carried)},
			})
			results[idx] = reviewerResult{Name: reviewer.Name, Findings: carried}
			continue
		}
		wg.Add(1)
		go func(idx int, reviewer ReviewerConfig) {
			defer wg.Done()
			e.runReviewer(ctx, phase, reviewer, promptData, budgetRemaining, idx, msgCh)
		}(idx, reviewer)
	}

	// Close channel once all goroutines finish.
	go func() {
		wg.Wait()
		close(msgCh)
	}()

	// Drain channel in the parent goroutine — all State access is serialized here.
	// Collect rendered prompts keyed by reviewer name for composite PromptHash.
	renderedPrompts := make(map[string][]byte)
	for msg := range msgCh {
		if msg.Event != nil {
			if msg.Event.Kind == EventOutputChunk {
				e.emitChunk(*msg.Event)
			} else {
				e.emit(*msg.Event)
			}
		}
		if msg.Log != nil {
			_ = e.state.WriteLog(msg.Log.Phase, "prompt_"+msg.Log.Name, msg.Log.Content)
			if msg.Log.IsPrompt {
				renderedPrompts[msg.Log.Name] = msg.Log.Content
			}
		}
		if msg.Result != nil {
			results[msg.Index] = *msg.Result
		}
	}

	// Check for context cancellation first.
	if err := ctx.Err(); err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: context cancelled during %s: %w", phase.Name, err)
	}

	// Partition results into successes and failures.
	var successResults []reviewerResult
	var reviewErrors []string
	for _, result := range results {
		if result.Err != nil {
			reviewErrors = append(reviewErrors, fmt.Sprintf("%s: %v", result.Name, result.Err))
		} else {
			successResults = append(successResults, result)
		}
	}

	// Determine how many reviewers must succeed.
	// MinReviewers == 0 means all reviewers are required (backwards compatible).
	minRequired := phase.MinReviewers
	if minRequired <= 0 {
		minRequired = len(phase.Reviewers)
	}

	if len(successResults) < minRequired {
		combinedErr := fmt.Errorf("engine: reviewer failures in %s: %s", phase.Name, strings.Join(reviewErrors, "; "))
		_ = e.state.MarkFailed(phase.Name, combinedErr)
		e.emitPhaseFailed(phase.Name, combinedErr)
		return combinedErr
	}

	// Emit partial-failure warnings for tolerated reviewer errors.
	for _, result := range results {
		if result.Err != nil {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventReviewerPartialFailure,
				Data: map[string]any{
					"reviewer": result.Name,
					"error":    result.Err.Error(),
				},
			})
		}
	}

	// Merge findings from successful reviewers only.
	merged := e.mergeReviewFindings(phase, successResults)

	// Serialize and store results.
	output, err := json.Marshal(merged)
	if err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return fmt.Errorf("engine: marshal review output for %s: %w", phase.Name, err)
	}

	if err := e.state.WriteResult(phase.Name, json.RawMessage(output)); err != nil {
		return fmt.Errorf("engine: write result for %s: %w", phase.Name, err)
	}

	// Build a human-readable artifact from the merged findings.
	artifact := e.buildReviewArtifact(merged)
	if err := e.state.WriteArtifact(phase.Name, []byte(artifact)); err != nil {
		return fmt.Errorf("engine: write artifact for %s: %w", phase.Name, err)
	}

	// Accumulate cost and tokens from ALL reviewers (including failed ones,
	// since they may have incurred partial cost before failing).
	totalCost := 0.0
	var totalTokensIn, totalTokensOut, totalCacheTokensIn int64
	for _, result := range results {
		totalCost += result.Cost
		totalTokensIn += result.TokensIn
		totalTokensOut += result.TokensOut
		totalCacheTokensIn += result.CacheTokensIn
	}
	if err := e.state.AccumulateCost(phase.Name, totalCost); err != nil {
		return fmt.Errorf("engine: accumulate cost for %s: %w", phase.Name, err)
	}
	if err := e.state.AccumulateTokens(phase.Name, totalTokensIn, totalTokensOut, totalCacheTokensIn); err != nil {
		return fmt.Errorf("engine: accumulate tokens for %s: %w", phase.Name, err)
	}

	// Per-phase cost enforcement.
	if err := e.checkPhaseBudget(phase); err != nil {
		_ = e.state.MarkFailed(phase.Name, err)
		e.emitPhaseFailed(phase.Name, err)
		return err
	}

	// Compute composite PromptHash from all reviewer rendered prompts.
	// Sort by reviewer name for deterministic ordering.
	if len(renderedPrompts) > 0 {
		names := make([]string, 0, len(renderedPrompts))
		for name := range renderedPrompts {
			names = append(names, name)
		}
		sort.Strings(names)

		h := sha256.New()
		for _, name := range names {
			h.Write(renderedPrompts[name])
		}
		e.state.Meta().Phases[phase.Name].PromptHash = fmt.Sprintf("%x", h.Sum(nil))
	}

	// Mark completed.
	if err := e.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("engine: mark completed %s: %w", phase.Name, err)
	}
	ps := e.state.Meta().Phases[phase.Name]
	reviewCompletedData := map[string]any{
		"duration_ms": ps.DurationMs,
		"cost":        ps.Cost,
	}
	if ps.TokensIn > 0 {
		reviewCompletedData["tokens_in"] = ps.TokensIn
	}
	if ps.TokensOut > 0 {
		reviewCompletedData["tokens_out"] = ps.TokensOut
	}
	if ps.CacheTokensIn > 0 {
		reviewCompletedData["cache_tokens_in"] = ps.CacheTokensIn
	}
	if summary := progress.PhaseSummary(phase.Name, json.RawMessage(output)); summary != "" {
		reviewCompletedData["summary"] = summary
	}
	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventPhaseCompleted,
		Data:  reviewCompletedData,
	})

	// Domain gating.
	return e.gatePhase(phase)
}

// runReviewer executes a single specialist reviewer, sending events and results
// through msgCh to avoid concurrent State access. budgetRemaining is a snapshot
// taken before goroutines launch.
func (e *Engine) runReviewer(ctx context.Context, phase PhaseConfig, reviewer ReviewerConfig, promptData PromptData, budgetRemaining float64, idx int, msgCh chan<- reviewerMsg) {
	sendEvent := func(evt Event) {
		msgCh <- reviewerMsg{Event: &evt, Index: idx}
	}
	sendResult := func(res reviewerResult) {
		msgCh <- reviewerMsg{Result: &res, Index: idx}
	}

	sendEvent(Event{
		Phase: phase.Name,
		Kind:  EventReviewerStarted,
		Data:  map[string]any{"reviewer": reviewer.Name, "focus": reviewer.Focus},
	})

	// Load and render the reviewer's prompt template.
	loadResult, err := e.config.Loader.LoadWithSource(reviewer.Prompt)
	if err != nil {
		sendEvent(Event{
			Phase: phase.Name,
			Kind:  EventReviewerFailed,
			Data:  map[string]any{"reviewer": reviewer.Name, "error": err.Error()},
		})
		sendResult(reviewerResult{Name: reviewer.Name, Err: &PromptError{Phase: phase.Name, Operation: "load", Err: err}})
		return
	}

	rendered, err := RenderPrompt(loadResult.Content, promptData)
	if err != nil {
		sendEvent(Event{
			Phase: phase.Name,
			Kind:  EventReviewerFailed,
			Data:  map[string]any{"reviewer": reviewer.Name, "error": err.Error()},
		})
		sendResult(reviewerResult{Name: reviewer.Name, Err: &PromptError{Phase: phase.Name, Operation: "render", Err: err}})
		return
	}

	// Send log to parent for serialized WriteLog.
	msgCh <- reviewerMsg{Log: &reviewerLog{Phase: phase.Name, Name: reviewer.Name, Content: []byte(rendered), IsPrompt: true}, Index: idx}

	// Use a reviewer-specific OnChunk that routes events through msgCh
	// to maintain the serialization contract — all events flow through
	// the channel to avoid concurrent State/callback access.
	// The waitIfPaused error is deliberately discarded; see makeOnChunk comment.
	onChunk := func(line string) {
		msgCh <- reviewerMsg{Event: &Event{
			Phase: phase.Name,
			Kind:  EventOutputChunk,
			Data:  map[string]any{"line": line},
		}, Index: idx}
		_ = e.waitIfPaused(ctx) // error deliberately discarded; see makeOnChunk comment
	}

	// Use the parent phase's schema for the reviewer findings.
	// Prefer per-reviewer model if set, otherwise use the global model.
	model := e.config.Model
	if reviewer.Model != "" {
		model = reviewer.Model
	}

	opts := runner.RunOpts{
		Phase:        phase.Name + "/" + reviewer.Name,
		SystemPrompt: rendered,
		UserPrompt:   "Execute the review described in the system prompt.",
		OutputSchema: phase.Schema,
		AllowedTools: phase.Tools,
		MaxBudgetUSD: budgetRemaining,
		WorkDir:      e.workDir(phase),
		Model:        model,
		Timeout:      phase.Timeout.Duration,
		OnChunk:      onChunk,
	}

	// Run with per-reviewer retry logic using the phase's RetryConfig.
	result, err := e.runReviewerWithRetry(ctx, phase, reviewer, opts, idx, msgCh)
	if err != nil {
		sendEvent(Event{
			Phase: phase.Name,
			Kind:  EventReviewerFailed,
			Data:  map[string]any{"reviewer": reviewer.Name, "error": err.Error()},
		})
		sendResult(reviewerResult{Name: reviewer.Name, Err: fmt.Errorf("run %s: %w", reviewer.Name, err)})
		return
	}

	// Parse findings from the structured output.
	var findings []schemas.ReviewFinding
	if result.Output != nil {
		var parsed struct {
			Findings []schemas.ReviewFinding `json:"findings"`
		}
		if parseErr := json.Unmarshal(result.Output, &parsed); parseErr != nil {
			sendEvent(Event{
				Phase: phase.Name,
				Kind:  EventReviewerParseWarning,
				Data:  map[string]any{"reviewer": reviewer.Name, "error": parseErr.Error()},
			})
		} else {
			findings = parsed.Findings
		}
	}

	sendEvent(Event{
		Phase: phase.Name,
		Kind:  EventReviewerCompleted,
		Data: map[string]any{
			"reviewer":       reviewer.Name,
			"findings_count": len(findings),
			"cost":           result.CostUSD,
		},
	})

	sendResult(reviewerResult{
		Name:          reviewer.Name,
		Findings:      findings,
		Cost:          result.CostUSD,
		TokensIn:      result.TokensIn,
		TokensOut:     result.TokensOut,
		CacheTokensIn: result.CacheTokensIn,
	})
}

// runReviewerWithRetry runs the reviewer's runner call with per-category retry
// limits from the phase's RetryConfig. Only transient errors (429, timeout) are
// retried at the reviewer level with backoff. Parse, semantic, context, and
// unknown errors are immediately returned as failures without retry.
func (e *Engine) runReviewerWithRetry(ctx context.Context, phase PhaseConfig, reviewer ReviewerConfig, opts runner.RunOpts, idx int, msgCh chan<- reviewerMsg) (*runner.RunResult, error) {
	remaining := map[string]int{
		"transient": phase.Retry.Transient,
	}

	sendEvent := func(evt Event) {
		msgCh <- reviewerMsg{Event: &evt, Index: idx}
	}

	attempt := 0
	for {
		if err := e.apiSem.Acquire(ctx); err != nil {
			return nil, fmt.Errorf("reviewer %s semaphore acquire: %w", reviewer.Name, err)
		}
		result, err := e.runner.Run(ctx, opts)
		e.apiSem.Release()

		if err == nil {
			return result, nil
		}

		category := classifyError(err)

		left, tracked := remaining[category]
		if !tracked || left <= 0 {
			return nil, &RetriesExhaustedError{Phase: reviewer.Name, Category: category, Attempts: attempt + 1, Err: err}
		}
		remaining[category]--

		switch category {
		case "transient":
			delay := backoff(attempt, e.config.JitterFunc)
			sendEvent(Event{
				Phase: phase.Name,
				Kind:  EventReviewerRetrying,
				Data: map[string]any{
					"reviewer": reviewer.Name,
					"category": category,
					"attempt":  attempt + 1,
					"delay":    delay.String(),
				},
			})
			if err := e.sleepWithContext(ctx, delay); err != nil {
				return nil, fmt.Errorf("reviewer %s retry interrupted: %w", reviewer.Name, err)
			}
		}

		// Send retry log to parent for serialized WriteLog.
		msgCh <- reviewerMsg{
			Log: &reviewerLog{
				Phase:   phase.Name,
				Name:    fmt.Sprintf("%s_retry_%d", reviewer.Name, attempt+1),
				Content: []byte(fmt.Sprintf("category=%s err=%s", category, err)),
			},
			Index: idx,
		}

		attempt++
	}
}

// sourceContains checks whether the comma-separated combined source string
// contains an exact match for name. This avoids false positives when reviewer
// names are substrings of each other (e.g. "go" vs "go-specialist").
func sourceContains(combined, name string) bool {
	for _, s := range strings.Split(combined, ", ") {
		if s == name {
			return true
		}
	}
	return false
}

// deduplicateFindings removes duplicate findings from the merged list.
// Two findings are considered duplicates when they share the same File and
// Severity and either (a) they have the same positive Line number, or
// (b) both have Line == 0 and one's Issue text is a substring of the other's.
// When duplicates are found the longer Issue text is kept and Source names
// are combined with ", ".
func deduplicateFindings(findings []schemas.ReviewFinding) ([]schemas.ReviewFinding, int) {
	type lineKey struct {
		File     string
		Line     int
		Severity string
	}

	// Index for findings with Line > 0 — O(1) lookup.
	lineIndex := make(map[lineKey]int) // key → index in result slice

	// Separate list for findings with Line == 0, grouped by file+severity.
	type zeroKey struct {
		File     string
		Severity string
	}
	zeroIndex := make(map[zeroKey][]int) // key → indices in result slice

	var result []schemas.ReviewFinding
	removed := 0

	for _, f := range findings {
		sevLower := strings.ToLower(f.Severity)

		if f.Line > 0 {
			key := lineKey{File: f.File, Line: f.Line, Severity: sevLower}
			if idx, exists := lineIndex[key]; exists {
				// Duplicate: merge into existing entry.
				existing := &result[idx]
				if len(f.Issue) > len(existing.Issue) {
					existing.Issue = f.Issue
				}
				if f.Source != "" && !sourceContains(existing.Source, f.Source) {
					existing.Source = existing.Source + ", " + f.Source
				}
				removed++
			} else {
				lineIndex[key] = len(result)
				result = append(result, f)
			}
		} else {
			// Line == 0: check for substring match among existing zero-line
			// findings with the same file + severity.
			zk := zeroKey{File: f.File, Severity: sevLower}
			merged := false
			for _, idx := range zeroIndex[zk] {
				existing := &result[idx]
				if f.Issue != "" && existing.Issue != "" && (strings.Contains(existing.Issue, f.Issue) || strings.Contains(f.Issue, existing.Issue)) {
					// Keep the longer issue text.
					if len(f.Issue) > len(existing.Issue) {
						existing.Issue = f.Issue
					}
					if f.Source != "" && !sourceContains(existing.Source, f.Source) {
						existing.Source = existing.Source + ", " + f.Source
					}
					removed++
					merged = true
					break
				}
			}
			if !merged {
				zeroIndex[zk] = append(zeroIndex[zk], len(result))
				result = append(result, f)
			}
		}
	}

	return result, removed
}

// mergeReviewFindings combines findings from all reviewers and computes a verdict.
func (e *Engine) mergeReviewFindings(phase PhaseConfig, results []reviewerResult) schemas.ReviewOutput {
	var allFindings []schemas.ReviewFinding

	for _, result := range results {
		for _, finding := range result.Findings {
			finding.Source = result.Name
			allFindings = append(allFindings, finding)
		}
	}

	allFindings, duplicatesRemoved := deduplicateFindings(allFindings)

	verdict := computeReviewVerdict(allFindings)

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventReviewMerged,
		Data: map[string]any{
			"findings_count":     len(allFindings),
			"verdict":            verdict,
			"duplicates_removed": duplicatesRemoved,
		},
	})

	return schemas.ReviewOutput{
		TicketKey: e.config.Ticket.Key,
		Findings:  allFindings,
		Verdict:   verdict,
	}
}

// computeReviewVerdict determines the review verdict based on finding severities.
// Any critical or major finding → "rework"
// Only minor findings → "pass-with-follow-ups"
// No findings → "pass"
func computeReviewVerdict(findings []schemas.ReviewFinding) string {
	hasCriticalOrMajor := false
	hasMinor := false

	for _, finding := range findings {
		sev := strings.ToLower(finding.Severity)
		switch sev {
		case "critical", "major":
			hasCriticalOrMajor = true
		case "minor":
			hasMinor = true
		}
	}

	if hasCriticalOrMajor {
		return "rework"
	}
	if hasMinor {
		return "pass-with-follow-ups"
	}
	return "pass"
}

// buildReviewArtifact creates a human-readable markdown summary of the review.
func (e *Engine) buildReviewArtifact(merged schemas.ReviewOutput) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Review: %s\n\n", merged.Verdict))
	sb.WriteString(fmt.Sprintf("Ticket: %s\n", merged.TicketKey))
	sb.WriteString(fmt.Sprintf("Verdict: %s\n", merged.Verdict))
	sb.WriteString(fmt.Sprintf("Total findings: %d\n\n", len(merged.Findings)))

	if len(merged.Findings) == 0 {
		sb.WriteString("No issues found.\n")
		return sb.String()
	}

	for idx, finding := range merged.Findings {
		sb.WriteString(fmt.Sprintf("## Finding %d: %s (%s)\n\n", idx+1, finding.Severity, finding.Source))
		if finding.File != "" {
			if finding.Line > 0 {
				sb.WriteString(fmt.Sprintf("- **File**: %s:%d\n", finding.File, finding.Line))
			} else {
				sb.WriteString(fmt.Sprintf("- **File**: %s\n", finding.File))
			}
		}
		sb.WriteString(fmt.Sprintf("- **Issue**: %s\n", finding.Issue))
		if finding.Suggestion != "" {
			sb.WriteString(fmt.Sprintf("- **Suggestion**: %s\n", finding.Suggestion))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
