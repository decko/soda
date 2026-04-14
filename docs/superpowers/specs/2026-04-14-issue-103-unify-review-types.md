# Unify Review Finding Types Between Engine and Schemas

**Issue:** decko/soda#103
**Date:** 2026-04-14
**Scope:** `internal/pipeline/engine.go`, `internal/pipeline/prompt.go`, `schemas/review.go`

## Problem

The engine defines multiple structs for review findings that duplicate the exported types in `schemas/`:

| Engine type | Location | Schema equivalent | Structural difference |
|-------------|----------|-------------------|-----------------------|
| `reviewFinding` | engine.go:940-946 | `schemas.ReviewFinding` | Missing `Source` field |
| `mergedReviewFinding` | engine.go:1210-1217 | `schemas.ReviewFinding` | **Identical** |
| `mergedReviewOutput` | engine.go:1203-1207 | `schemas.ReviewOutput` | **Identical** |
| `ReviewReworkFinding` | prompt.go:42-49 | `schemas.ReviewFinding` | Same fields, no JSON tags |
| anonymous struct in `extractReviewReworkFeedback` | engine.go:731-741 | `schemas.ReviewOutput` | **Identical** |

Additionally, two intentionally minimal structs exist that are **not** duplicates:

| Type | Location | Fields | Rationale for keeping |
|------|----------|--------|-----------------------|
| anonymous struct in `gatePhase` review case | engine.go:876-882 | `Severity`, `Issue` only | Intentionally narrow — documents that the gate only inspects these two fields |
| `reviewReworkSignal.findings` | errors.go:46-49 | `Severity`, `Issue` only | Minimal subset for `Error()` message — coupled to `gatePhase` output |

The full duplicates are a maintenance risk — changes to the review output schema require updates in multiple places.

## Design

1. **Delete `mergedReviewOutput` and `mergedReviewFinding`** from engine.go — replace all usages with `schemas.ReviewOutput` and `schemas.ReviewFinding`.

2. **Delete `ReviewReworkFinding`** from prompt.go — replace with `schemas.ReviewFinding`. JSON tags on `schemas.ReviewFinding` are harmless when the struct is used as template data (Go templates use field names, not JSON tags).

3. **Replace the anonymous struct in `extractReviewReworkFeedback`** with `schemas.ReviewOutput` — the fields and JSON tags are identical.

4. **Keep `reviewFinding`** — it represents the output of a single reviewer *before* the source is known. Rename to `rawReviewFinding` for clarity.

5. **Keep the `gatePhase` anonymous struct and `reviewReworkSignal.findings`** — these intentionally narrow the deserialized fields. The `gatePhase` struct documents that only `Severity` and `Issue` are inspected. No change needed.

### Import addition

```go
import "github.com/decko/soda/schemas"
```

The `schemas` package has no internal dependencies, so this import creates no cycle risk. The dependency direction (`internal/pipeline` → `schemas`) is safe — `schemas` is a leaf package defining canonical structured-output types.

### Affected functions

| Function | Current type | New type |
|----------|-------------|----------|
| `mergeReviewFindings` return type | `mergedReviewOutput` | `schemas.ReviewOutput` |
| `mergeReviewFindings` internal slice | `[]mergedReviewFinding` | `[]schemas.ReviewFinding` |
| `computeReviewVerdict` parameter | `[]mergedReviewFinding` | `[]schemas.ReviewFinding` |
| `buildReviewArtifact` parameter | `mergedReviewOutput` | `schemas.ReviewOutput` |
| `runParallelReview` local var `merged` | `mergedReviewOutput` | `schemas.ReviewOutput` |
| `runReviewer` findings parsing | `[]reviewFinding` | `[]rawReviewFinding` (rename only) |
| `reviewerResult.Findings` | `[]reviewFinding` | `[]rawReviewFinding` (rename only) |
| `extractReviewReworkFeedback` local var | anonymous struct | `schemas.ReviewOutput` |
| `extractReviewReworkFeedback` findings loop | populates `ReviewReworkFinding` | populates `schemas.ReviewFinding` |
| `ReworkFeedback.ReviewFindings` field type | `[]ReviewReworkFinding` | `[]schemas.ReviewFinding` |

### Merge conversion (in `mergeReviewFindings`)

The loop that converts `reviewFinding` → `mergedReviewFinding` becomes `rawReviewFinding` → `schemas.ReviewFinding`:

```go
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
```

No behavioral change — field names and JSON tags are identical.

## Files changed

| File | Change |
|------|--------|
| `internal/pipeline/engine.go` | Delete `mergedReviewOutput`, `mergedReviewFinding`; rename `reviewFinding` → `rawReviewFinding`; replace anonymous struct in `extractReviewReworkFeedback`; import `schemas`; update function signatures |
| `internal/pipeline/prompt.go` | Delete `ReviewReworkFinding`; change `ReworkFeedback.ReviewFindings` to `[]schemas.ReviewFinding` |
| `internal/pipeline/engine_test.go` | Replace `mergedReviewFinding` with `schemas.ReviewFinding` in test tables (~8 occurrences) |

## Test plan

- All existing engine and review tests pass after type-name substitutions in test files
- `go test ./internal/pipeline/... ./schemas/...`
