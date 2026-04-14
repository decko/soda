# Issue #103: Unify Review Finding Types — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate duplicate review finding types in the engine by replacing them with the canonical `schemas.ReviewOutput` and `schemas.ReviewFinding` types.

**Architecture:** Delete `mergedReviewOutput`, `mergedReviewFinding`, and `ReviewReworkFinding` — all structurally identical to the schema types. Replace usages with `schemas.*`. Rename `reviewFinding` → `rawReviewFinding` (genuinely distinct — no `Source` field). Keep intentionally narrow anonymous structs in `gatePhase` and `reviewReworkSignal` unchanged.

**Tech Stack:** Go

**Spec:** `docs/superpowers/specs/2026-04-14-issue-103-unify-review-types.md`

---

### Task 1: Run existing tests to establish baseline

**Files:**
- None (verification only)

- [ ] **Step 1: Run the full engine and schema test suites**

Run: `cd /home/ddebrito/dev/soda && go test ./internal/pipeline/... ./schemas/... -count=1`

Expected: All tests pass.

---

### Task 2: Replace `mergedReviewOutput` and `mergedReviewFinding` with schema types

**Files:**
- Modify: `internal/pipeline/engine.go:1-18` (imports)
- Modify: `internal/pipeline/engine.go:1202-1217` (type definitions — delete)
- Modify: `internal/pipeline/engine.go:1220-1252` (`mergeReviewFindings`)
- Modify: `internal/pipeline/engine.go:1258-1279` (`computeReviewVerdict`)
- Modify: `internal/pipeline/engine.go:1281-1312` (`buildReviewArtifact`)
- Modify: `internal/pipeline/engine.go:1059` (`runParallelReview` local var)

- [ ] **Step 2: Add the `schemas` import**

Add to the import block in `engine.go`:

```go
"github.com/decko/soda/schemas"
```

- [ ] **Step 3: Delete `mergedReviewOutput` and `mergedReviewFinding` type definitions**

Delete these two type definitions from engine.go (lines 1203-1217):

```go
// mergedReviewOutput is the combined output from all reviewers.
type mergedReviewOutput struct {
	TicketKey string                `json:"ticket_key"`
	Findings  []mergedReviewFinding `json:"findings"`
	Verdict   string                `json:"verdict"`
}

// mergedReviewFinding is a single finding with its source reviewer.
type mergedReviewFinding struct {
	Source     string `json:"source"`
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
}
```

- [ ] **Step 4: Update `mergeReviewFindings` signature and body**

Change the return type and internal slice type:

```go
func (e *Engine) mergeReviewFindings(phase PhaseConfig, results []reviewerResult) schemas.ReviewOutput {
	var allFindings []schemas.ReviewFinding

	for _, result := range results {
		for _, finding := range result.Findings {
			allFindings = append(allFindings, schemas.ReviewFinding{
				Source:     result.Name,
				Severity:  finding.Severity,
				File:      finding.File,
				Line:      finding.Line,
				Issue:     finding.Issue,
				Suggestion: finding.Suggestion,
			})
		}
	}

	verdict := computeReviewVerdict(allFindings)

	e.emit(Event{
		Phase: phase.Name,
		Kind:  EventReviewMerged,
		Data: map[string]any{
			"findings_count": len(allFindings),
			"verdict":        verdict,
		},
	})

	return schemas.ReviewOutput{
		TicketKey: e.config.Ticket.Key,
		Findings:  allFindings,
		Verdict:   verdict,
	}
}
```

- [ ] **Step 5: Update `computeReviewVerdict` parameter type**

```go
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
```

- [ ] **Step 6: Update `buildReviewArtifact` parameter type**

```go
func (e *Engine) buildReviewArtifact(merged schemas.ReviewOutput) string {
```

The function body references `merged.TicketKey`, `merged.Findings`, `merged.Verdict`, `finding.Source`, `finding.Severity`, `finding.File`, `finding.Line`, `finding.Issue`, `finding.Suggestion` — all field names are identical in `schemas.ReviewOutput`/`schemas.ReviewFinding`. No body changes needed.

- [ ] **Step 7: Update `runParallelReview` local variable type**

The `merged` variable at engine.go:1059 is assigned from `mergeReviewFindings`, which now returns `schemas.ReviewOutput`. No explicit type annotation needed — Go infers it. But verify the `json.Marshal(merged)` call still works (it does — same JSON tags).

- [ ] **Step 8: Verify compilation**

Run: `cd /home/ddebrito/dev/soda && go build ./internal/pipeline/...`

Expected: Compiles cleanly. If `engine_test.go` references `mergedReviewFinding`, it will fail — that's fixed in Task 4.

---

### Task 3: Rename `reviewFinding` → `rawReviewFinding`

**Files:**
- Modify: `internal/pipeline/engine.go:940-946` (type definition)
- Modify: `internal/pipeline/engine.go:932-937` (`reviewerResult`)
- Modify: `internal/pipeline/engine.go:1169` (`runReviewer`)

- [ ] **Step 9: Rename the type and all references**

Change the type definition:

```go
// rawReviewFinding is the structured output a single reviewer returns,
// before the source reviewer name is attached during merging.
type rawReviewFinding struct {
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
}
```

Update `reviewerResult`:

```go
type reviewerResult struct {
	Name     string
	Findings []rawReviewFinding
	Cost     float64
	Err      error
}
```

Update the parsed struct in `runReviewer`:

```go
var findings []rawReviewFinding
if result.Output != nil {
	var parsed struct {
		Findings []rawReviewFinding `json:"findings"`
	}
```

---

### Task 4: Replace `ReviewReworkFinding` with `schemas.ReviewFinding`

**Files:**
- Modify: `internal/pipeline/prompt.go:37` (field type)
- Modify: `internal/pipeline/prompt.go:40-49` (delete type definition)
- Modify: `internal/pipeline/engine.go:725-770` (`extractReviewReworkFeedback`)

- [ ] **Step 10: Update `ReworkFeedback.ReviewFindings` field type in prompt.go**

Change line 37:

```go
ReviewFindings []schemas.ReviewFinding
```

- [ ] **Step 11: Delete `ReviewReworkFinding` type definition from prompt.go**

Delete lines 40-49:

```go
// ReviewReworkFinding holds a critical or major finding from a specialist
// reviewer, injected into the implement prompt for rework.
type ReviewReworkFinding struct {
	Source     string // reviewer name, e.g. "go-specialist"
	Severity   string // "critical" or "major"
	File       string
	Line       int
	Issue      string
	Suggestion string
}
```

- [ ] **Step 12: Add `schemas` import to prompt.go**

Add to the import block:

```go
"github.com/decko/soda/schemas"
```

- [ ] **Step 13: Update `extractReviewReworkFeedback` in engine.go**

Replace the anonymous struct and the findings loop:

```go
func (e *Engine) extractReviewReworkFeedback() *ReworkFeedback {
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

	// Only include critical and major findings.
	for _, finding := range result.Findings {
		sev := strings.ToLower(finding.Severity)
		if sev == "critical" || sev == "major" {
			fb.ReviewFindings = append(fb.ReviewFindings, schemas.ReviewFinding{
				Source:     finding.Source,
				Severity:  finding.Severity,
				File:      finding.File,
				Line:      finding.Line,
				Issue:     finding.Issue,
				Suggestion: finding.Suggestion,
			})
		}
	}

	return fb
}
```

---

### Task 5: Update test file

**Files:**
- Modify: `internal/pipeline/engine_test.go` (~8 occurrences of `mergedReviewFinding`)

- [ ] **Step 14: Replace `mergedReviewFinding` with `schemas.ReviewFinding` in tests**

In `TestComputeReviewVerdict` (engine_test.go:3452-3515), replace all `mergedReviewFinding` with `schemas.ReviewFinding`. There are 8 occurrences across the test table.

Add the `schemas` import to the test file's import block:

```go
"github.com/decko/soda/schemas"
```

Example — change:

```go
findings: []mergedReviewFinding{
    {Severity: "minor", Issue: "style"},
},
```

to:

```go
findings: []schemas.ReviewFinding{
    {Severity: "minor", Issue: "style"},
},
```

- [ ] **Step 15: Run the full test suite**

Run: `cd /home/ddebrito/dev/soda && go test ./internal/pipeline/... ./schemas/... -v -count=1`

Expected: All tests pass.

- [ ] **Step 16: Run vet**

Run: `cd /home/ddebrito/dev/soda && go vet ./internal/pipeline/... ./schemas/...`

Expected: No issues.

- [ ] **Step 17: Commit**

```bash
git add internal/pipeline/engine.go internal/pipeline/prompt.go internal/pipeline/engine_test.go
git commit -m "refactor(pipeline): unify review finding types with schemas package

Delete mergedReviewOutput, mergedReviewFinding, and ReviewReworkFinding
— all structurally identical to schemas.ReviewOutput and
schemas.ReviewFinding. Replace with canonical schema types.

Rename reviewFinding -> rawReviewFinding (genuinely distinct: no Source
field, used for individual reviewer output before merge).

Keep intentionally narrow anonymous structs in gatePhase and
reviewReworkSignal unchanged — they document that only Severity/Issue
are inspected.

Fixes: #103"
```
