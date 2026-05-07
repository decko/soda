package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/decko/soda/schemas"
)

// extractFeedbackFrom dispatches to the appropriate source-specific feedback
// extractor. Returns nil for unknown sources.
func (e *Engine) extractFeedbackFrom(source string) *ReworkFeedback {
	switch source {
	case "review":
		return e.extractReviewFeedback()
	case "verify":
		return e.extractVerifyFeedback()
	default:
		return nil
	}
}

// extractVerifyFeedback reads the verify result and returns structured
// feedback when the verdict is FAIL. Returns nil if no verify result
// exists, the verdict is not FAIL, or the plan has changed since verify
// ran (staleness guard).
//
// The top-level fields (Verdict, FixesRequired, etc.) always reflect
// only the most recent verify.json. PriorCycles is populated from
// archived results (verify.json.1, verify.json.2, ...) so the LLM
// has context about what was previously reported.
//
// Only critical/major code issues and failed criteria/commands are
// included to keep prompt context focused.
func (e *Engine) extractVerifyFeedback() *ReworkFeedback {
	raw, err := e.state.ReadResult("verify")
	if err != nil {
		return nil
	}

	var result struct {
		Verdict         string   `json:"verdict"`
		FixesRequired   []string `json:"fixes_required"`
		CriteriaResults []struct {
			Criterion string `json:"criterion"`
			Passed    bool   `json:"passed"`
			Evidence  string `json:"evidence"`
		} `json:"criteria_results"`
		CodeIssues []struct {
			File         string `json:"file"`
			Line         int    `json:"line"`
			Severity     string `json:"severity"`
			Issue        string `json:"issue"`
			SuggestedFix string `json:"suggested_fix"`
		} `json:"code_issues"`
		CommandResults []struct {
			Command  string `json:"command"`
			ExitCode int    `json:"exit_code"`
			Output   string `json:"output"`
			Passed   bool   `json:"passed"`
		} `json:"command_results"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	if !strings.EqualFold(result.Verdict, "FAIL") {
		return nil
	}

	// Staleness guard: skip if plan changed since verify ran.
	if verifyPS := e.state.Meta().Phases["verify"]; verifyPS != nil && verifyPS.PlanHash != "" {
		if currentHash := e.computePlanHash(); currentHash != "" && currentHash != verifyPS.PlanHash {
			e.emit(Event{
				Phase: "verify",
				Kind:  EventReworkFeedbackSkipped,
				Data: map[string]any{
					"reason":            "plan changed since verify ran",
					"verify_plan_hash":  verifyPS.PlanHash,
					"current_plan_hash": currentHash,
				},
			})
			return nil
		}
	}

	fb := &ReworkFeedback{
		Verdict:       result.Verdict,
		Source:        "verify",
		FixesRequired: result.FixesRequired,
	}

	// Failed criteria only.
	for _, cr := range result.CriteriaResults {
		if !cr.Passed {
			fb.FailedCriteria = append(fb.FailedCriteria, FailedCriterion{
				Criterion: cr.Criterion,
				Evidence:  cr.Evidence,
			})
		}
	}

	// Critical and major code issues only.
	for _, ci := range result.CodeIssues {
		sev := strings.ToLower(ci.Severity)
		if sev == "critical" || sev == "major" {
			fb.CodeIssues = append(fb.CodeIssues, ReworkCodeIssue{
				File:         ci.File,
				Line:         ci.Line,
				Severity:     ci.Severity,
				Issue:        ci.Issue,
				SuggestedFix: ci.SuggestedFix,
			})
		}
	}

	// Failed commands only, output truncated to 50 lines.
	for _, cmd := range result.CommandResults {
		if !cmd.Passed {
			fb.FailedCommands = append(fb.FailedCommands, FailedCommand{
				Command:  cmd.Command,
				ExitCode: cmd.ExitCode,
				Output:   truncateLines(cmd.Output, 50),
			})
		}
	}

	// Collect prior cycle context from archived verify results.
	fb.PriorCycles = e.collectPriorVerifyCycles()

	return fb
}

// maxFeedbackContextBytes is the total byte budget for file content injected
// into rework feedback across all findings. Prevents context bloat.
const maxFeedbackContextBytes = 30 * 1024 // 30KB

// Per-finding caps limit how much file content a single finding can inject.
// Critical findings get a larger window because they are highest priority.
const (
	criticalFindingCapBytes = 10 * 1024 // 10KB
	majorFindingCapBytes    = 5 * 1024  // 5KB
)

// severityRank returns a sort key for finding severity.
// Lower rank = higher priority. Critical findings are processed first
// so they consume budget before lower-severity findings.
func severityRank(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 0
	case "major":
		return 1
	default:
		return 2
	}
}

// contextLines returns the ± line window for snippet fallback by severity.
// Critical findings get a wider window for more surrounding context.
func contextLines(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 15
	case "major":
		return 10
	default:
		return 5
	}
}

// findingBudgetCap returns the maximum bytes a single finding may inject.
func findingBudgetCap(severity string) int {
	if strings.ToLower(severity) == "critical" {
		return criticalFindingCapBytes
	}
	return majorFindingCapBytes
}

// extractReviewFeedback reads the review result and returns structured
// feedback when the verdict is "rework". Returns nil if no review result
// exists or the verdict is not "rework".
//
// The top-level fields (Verdict, ReviewFindings) reflect the most recent
// review.json. PriorCycles is populated from archived results
// (review.json.1, review.json.2, ...) so the LLM has context about
// what was previously reported. Only critical/major findings are
// included to keep prompt context focused.
//
// Findings are sorted by severity (critical first) so higher-priority
// findings consume budget first. Each finding is enriched with file
// content up to a severity-dependent cap (10KB critical, 5KB major),
// falling back to a ±contextLines snippet when budget is exhausted.
// Same-file findings share the cached raw content to avoid duplicate reads.
func (e *Engine) extractReviewFeedback() *ReworkFeedback {
	raw, err := e.state.ReadResult("review")
	if err != nil {
		return nil
	}

	var result schemas.ReviewOutput
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	if !strings.EqualFold(result.Verdict, "rework") {
		return nil
	}

	fb := &ReworkFeedback{
		Verdict: result.Verdict,
		Source:  "review",
	}

	// Sort findings so critical issues are processed first and consume
	// budget before lower-severity findings (stable to preserve original
	// order within the same severity).
	sort.SliceStable(result.Findings, func(i, j int) bool {
		return severityRank(result.Findings[i].Severity) < severityRank(result.Findings[j].Severity)
	})

	workDir := e.workDir(PhaseConfig{})
	budgetRemaining := maxFeedbackContextBytes
	rawCache := make(map[string]string) // file path → raw file content

	// Only include critical and major findings, enriched with code context.
	for _, finding := range result.Findings {
		sev := strings.ToLower(finding.Severity)
		if sev != "critical" && sev != "major" {
			continue
		}

		ef := EnrichedFinding{ReviewFinding: finding}
		if finding.File != "" {
			ef.CodeSnippet = readFileForFinding(workDir, finding.File, finding.Line, finding.Severity, &budgetRemaining, rawCache)
		}
		fb.ReviewFindings = append(fb.ReviewFindings, ef)
	}

	// Collect prior cycle context from archived review results.
	fb.PriorCycles = e.collectPriorReviewCycles()

	return fb
}

// readFileForFinding returns file content for a review finding. The raw
// file is cached in rawCache only after a successful budget charge;
// subsequent calls for the same file (possibly with a different severity)
// extract a severity-appropriate window from the cached content without
// re-charging the budget.
//
// On a cache miss the full file is read. The returned content is capped
// to findingBudgetCap(severity) bytes (truncated to the last newline
// within the cap) and charged against budgetRemaining. If the charge
// succeeds, the raw content is stored in rawCache for future lookups.
// If the capped content exceeds the remaining budget, a snippet of
// ±contextLines(severity) around the finding line is returned instead
// (free — not charged, not cached).
//
// On a cache hit the same per-finding cap is applied to the already-
// cached raw content, but no budget is charged (the file's bytes were
// already counted on first access).
func readFileForFinding(workDir, file string, line int, severity string, budgetRemaining *int, rawCache map[string]string) string {
	raw, cached := rawCache[file]

	if !cached {
		resolved := filepath.Clean(filepath.Join(workDir, file))
		if !strings.HasPrefix(resolved, filepath.Clean(workDir)+string(filepath.Separator)) {
			return ""
		}

		data, err := os.ReadFile(resolved)
		if err != nil {
			return ""
		}

		raw = string(data)
	}

	budgetCap := findingBudgetCap(severity)

	if cached {
		// Cache hit — file was already paid for on first access.
		// If the file fits within the per-finding cap, return the full
		// content (consistent with cache-miss semantics for sub-cap files).
		if len(raw) <= budgetCap {
			return raw
		}
		// File exceeds cap — extract a window centered on the finding line
		// to avoid returning the head when the finding is beyond the boundary.
		if line > 0 {
			snippet := extractSnippet(raw, line, contextLines(severity))
			if len(snippet) > budgetCap {
				snippet = snippet[:budgetCap]
				if idx := strings.LastIndex(snippet, "\n"); idx > 0 {
					snippet = snippet[:idx+1]
				}
			}
			return snippet
		}
		// No line info — return head of file, capped.
		capped := raw[:budgetCap]
		if idx := strings.LastIndex(capped, "\n"); idx > 0 {
			capped = capped[:idx+1]
		}
		return capped
	}

	// Cache miss — extract a line-centered snippet capped to per-finding budget.
	// When the file exceeds the cap and we have a line number, center on
	// the finding line (same logic as cache-hit path) so the LLM sees the
	// relevant code regardless of where the finding is in the file.
	var effective string
	if len(raw) > budgetCap && line > 0 {
		effective = extractSnippet(raw, line, contextLines(severity))
		if len(effective) > budgetCap {
			effective = effective[:budgetCap]
			if idx := strings.LastIndex(effective, "\n"); idx > 0 {
				effective = effective[:idx+1]
			}
		}
	} else {
		effective = raw
		if len(effective) > budgetCap {
			effective = effective[:budgetCap]
			if idx := strings.LastIndex(effective, "\n"); idx > 0 {
				effective = effective[:idx+1]
			}
		}
	}

	// Cache miss — charge budget if the effective content fits.
	if len(effective) <= *budgetRemaining {
		*budgetRemaining -= len(effective)
		rawCache[file] = raw // store full content for future cache hits
		return effective
	}

	// Budget exhausted for capped content — fall back to snippet (free),
	// still respecting the per-finding cap.
	if line > 0 {
		snippet := extractSnippet(raw, line, contextLines(severity))
		if len(snippet) > budgetCap {
			snippet = snippet[:budgetCap]
			if idx := strings.LastIndex(snippet, "\n"); idx > 0 {
				snippet = snippet[:idx+1]
			}
		}
		return snippet
	}

	return ""
}

// extractSnippet returns ±context lines around the given 1-based line number
// from the provided content string.
func extractSnippet(content string, line, context int) string {
	lines := strings.Split(content, "\n")
	start := line - context - 1
	if start < 0 {
		start = 0
	}
	end := line + context
	if end > len(lines) {
		end = len(lines)
	}
	if start >= end {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

// readSnippet reads ±context lines around the given 1-based line number
// from file in workDir. Returns empty string on any error (missing file,
// invalid line, path outside workDir, etc.) — callers treat this as
// best-effort enrichment.
func readSnippet(workDir, file string, line, context int) string {
	resolved := filepath.Clean(filepath.Join(workDir, file))
	if !strings.HasPrefix(resolved, filepath.Clean(workDir)+string(filepath.Separator)) {
		return ""
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ""
	}
	return extractSnippet(string(data), line, context)
}

// collectPriorReviewCycles reads archived review results (review.json.1,
// review.json.2, ...) and builds PriorCycle summaries. Only cycles with
// a "rework" verdict are included (pass cycles don't carry useful context
// for rework). Returns nil if no prior cycles exist.
func (e *Engine) collectPriorReviewCycles() []PriorCycle {
	reviewPS := e.state.Meta().Phases["review"]
	if reviewPS == nil || reviewPS.Generation <= 1 {
		return nil
	}

	var priors []PriorCycle
	for gen := 1; gen < reviewPS.Generation; gen++ {
		raw, err := e.state.ReadArchivedResult("review", gen)
		if err != nil {
			continue
		}

		var result schemas.ReviewOutput
		if err := json.Unmarshal(raw, &result); err != nil {
			continue
		}

		// Only include rework cycles — pass cycles don't carry useful context.
		if !strings.EqualFold(result.Verdict, "rework") {
			continue
		}

		summary := summarizeReviewFindings(result.Findings)
		if summary == "" {
			continue
		}

		priors = append(priors, PriorCycle{
			Cycle:   gen,
			Source:  "review",
			Verdict: result.Verdict,
			Summary: summary,
		})
	}
	return priors
}

// collectPriorVerifyCycles reads archived verify results (verify.json.1,
// verify.json.2, ...) and builds PriorCycle summaries. Only cycles with
// a FAIL verdict are included. Returns nil if no prior cycles exist.
func (e *Engine) collectPriorVerifyCycles() []PriorCycle {
	verifyPS := e.state.Meta().Phases["verify"]
	if verifyPS == nil || verifyPS.Generation <= 1 {
		return nil
	}

	var priors []PriorCycle
	for gen := 1; gen < verifyPS.Generation; gen++ {
		raw, err := e.state.ReadArchivedResult("verify", gen)
		if err != nil {
			continue
		}

		var result struct {
			Verdict       string   `json:"verdict"`
			FixesRequired []string `json:"fixes_required"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			continue
		}

		// Only include FAIL cycles.
		if !strings.EqualFold(result.Verdict, "FAIL") {
			continue
		}

		summary := summarizeVerifyFailures(result.FixesRequired)
		if summary == "" {
			continue
		}

		priors = append(priors, PriorCycle{
			Cycle:   gen,
			Source:  "verify",
			Verdict: result.Verdict,
			Summary: summary,
		})
	}
	return priors
}

// summarizeReviewFindings produces a concise summary of review findings
// for prior cycle context. Includes only critical/major findings.
func summarizeReviewFindings(findings []schemas.ReviewFinding) string {
	var parts []string
	for _, f := range findings {
		sev := strings.ToLower(f.Severity)
		if sev == "critical" || sev == "major" {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			if loc != "" {
				parts = append(parts, fmt.Sprintf("[%s] %s — %s", f.Severity, loc, f.Issue))
			} else {
				parts = append(parts, fmt.Sprintf("[%s] %s", f.Severity, f.Issue))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

// summarizeVerifyFailures produces a concise summary of verify failures
// for prior cycle context.
func summarizeVerifyFailures(fixesRequired []string) string {
	if len(fixesRequired) == 0 {
		return ""
	}
	return strings.Join(fixesRequired, "; ")
}

// truncateLines returns at most maxLines lines from s.
func truncateLines(s string, maxLines int) string {
	trimmed := strings.TrimRight(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + "\n... (truncated)"
}
