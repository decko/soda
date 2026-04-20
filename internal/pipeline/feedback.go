package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// extractReviewFeedback reads the review result and returns structured
// feedback when the verdict is "rework". Returns nil if no review result
// exists or the verdict is not "rework".
//
// The top-level fields (Verdict, ReviewFindings) reflect the most recent
// review.json. PriorCycles is populated from archived results
// (review.json.1, review.json.2, ...) so the LLM has context about
// what was previously reported. Only critical/major findings are
// included to keep prompt context focused.
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

	// Only include critical and major findings, enriched with code snippets.
	for _, finding := range result.Findings {
		sev := strings.ToLower(finding.Severity)
		if sev == "critical" || sev == "major" {
			ef := EnrichedFinding{ReviewFinding: finding}
			if finding.File != "" && finding.Line > 0 {
				ef.CodeSnippet = readSnippet(e.workDir(PhaseConfig{}), finding.File, finding.Line, 5)
			}
			fb.ReviewFindings = append(fb.ReviewFindings, ef)
		}
	}

	// Collect prior cycle context from archived review results.
	fb.PriorCycles = e.collectPriorReviewCycles()

	return fb
}

// readSnippet reads ±context lines around the given 1-based line number
// from file in workDir. Returns empty string on any error (missing file,
// invalid line, etc.) — callers treat this as best-effort enrichment.
func readSnippet(workDir, file string, line, context int) string {
	path := filepath.Join(workDir, file)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
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
