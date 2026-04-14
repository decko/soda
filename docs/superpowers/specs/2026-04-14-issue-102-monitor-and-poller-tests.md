# Add monitorSummary Unit Tests and GetNewComments Test Coverage

**Issue:** decko/soda#102
**Date:** 2026-04-14
**Scope:** `internal/progress/summary_test.go`, `internal/pipeline/github_poller_test.go`, `internal/pipeline/monitor_poll_test.go`

## Problem

Four test gaps and two latent code bugs identified during review of #95:

### 1. `monitorSummary` has no test (summary.go:158-173)

Every other summary function (`triageSummary`, `planSummary`, `implementSummary`, `verifySummary`, `submitSummary`) has a dedicated table-driven test in `summary_test.go`. `monitorSummary` is the only one missing.

The function unmarshals JSON looking for `comments_handled` and returns:
- `""` on parse failure
- `"no comments handled"` when array is empty/absent
- `"1 comment handled"` for exactly one entry
- `"%d comments handled"` for 2+

### 2. `GetNewComments` has no test (github_poller.go:105-177)

`parsePRRef` is well tested, but `GetNewComments` — the most complex function in the poller — has zero test coverage. Its logic includes:
- Two separate API calls (pulls/comments + issues/comments)
- ID prefixing (`RC_` for review comments, `IC_` for issue comments)
- Positional afterID filtering (not numeric — walks the slice until it finds the matching ID)

### 3. Code bug: RC/IC ordering causes missed comments

Review comments (`RC_*`) always precede issue comments (`IC_*`) in the combined slice. If `afterID` is an `IC_*` ID, new `RC_*` comments posted after the tracked comment will be missed because they appear before `afterID` in the slice. This is a data-loss bug — file as a follow-up issue if not fixed in this ticket.

### 4. Code bug: `afterID` not found causes permanent blindness

When `afterID` is set but not found in the comment list (e.g., comment deleted), `pastAfter` never flips to `true` and the function returns an empty slice with nil error. The monitor loop at `monitor_poll.go:263-267` silently proceeds, permanently losing the ability to see any comments. File as a follow-up issue if not fixed in this ticket.

### 5. Latent bug: `gh api --paginate` multi-page concatenation

When `--paginate` spans multiple pages, `gh` concatenates JSON arrays (`[...][...]`). `json.Unmarshal` only parses the first array, silently discarding subsequent pages. This is a distinct defect — file as a separate issue.

### 6. Test helper discards errors (monitor_poll_test.go:94-96)

```go
os.MkdirAll(promptDir+"/prompts", 0755)
os.WriteFile(promptDir+"/prompts/submit.md", []byte(submitPrompt), 0644)
os.WriteFile(promptDir+"/prompts/monitor.md", []byte(monitorPrompt), 0644)
```

Errors are silently discarded. Should use `t.Fatal` on failure.

### 7. No-op `fmt.Sprintf` (monitor_poll_test.go:92-93)

```go
submitPrompt := fmt.Sprintf("Phase: submit\nTicket: {{.Ticket.Key}}\n")
monitorPrompt := fmt.Sprintf("Phase: monitor\nTicket: {{.Ticket.Key}}\n")
```

No format verbs — `fmt.Sprintf` is a no-op. Replace with string literals. Note: `go vet` will not flag this (the result is assigned); only `staticcheck` (SA1006) would catch it. This is a code cleanliness fix.

## Design

### A. `TestMonitorSummary` (summary_test.go)

Table-driven test matching the pattern of the other summary tests:

| Case | Input | Expected |
|------|-------|----------|
| `"multiple comments"` | `{"comments_handled":[{},{}]}` | `"2 comments handled"` |
| `"single comment"` | `{"comments_handled":[{}]}` | `"1 comment handled"` |
| `"no comments"` | `{"comments_handled":[]}` | `"no comments handled"` |
| `"field absent"` | `{"other":"data"}` | `"no comments handled"` |
| `"invalid json"` | `{broken` | `""` |
| `"nil result"` | `nil` | `""` |

Call via `PhaseSummary("monitor", ...)` to exercise the dispatcher path too.

### B. `TestGetNewComments` (github_poller_test.go)

Use a shell script in `t.TempDir()` as the fake `gh` binary. The script dispatches on the API endpoint argument (`$2`) to return different canned JSON for pulls/comments vs issues/comments. Point `p.command` at the script. This avoids the complexity of `TestMain` + `os.Getenv` and does not affect other tests in the file.

Test cases:

| Case | Setup | Assert |
|------|-------|--------|
| `"review and issue comments merged"` | 2 review + 1 issue comment | 3 comments returned, correct `RC_`/`IC_` prefixes |
| `"afterID filters correctly"` | 3 comments, `afterID` = second comment's ID | Only third comment returned |
| `"afterID empty returns all"` | 3 comments, `afterID = ""` | All 3 returned |
| `"afterID not found returns empty"` | 3 comments, `afterID = "RC_999"` | Empty slice (documents known limitation — see problem #4) |
| `"ordering: RC before IC"` | 1 review + 1 issue comment | `RC_*` appears before `IC_*` |
| `"afterID IC misses new RC"` | `afterID = "IC_1"`, new RC_99 | RC_99 NOT returned (documents known bug — see problem #3) |

### C. Error handling fix (monitor_poll_test.go)

```go
if err := os.MkdirAll(promptDir+"/prompts", 0755); err != nil {
    t.Fatalf("MkdirAll: %v", err)
}
if err := os.WriteFile(promptDir+"/prompts/submit.md", []byte(submitPrompt), 0644); err != nil {
    t.Fatalf("WriteFile submit: %v", err)
}
if err := os.WriteFile(promptDir+"/prompts/monitor.md", []byte(monitorPrompt), 0644); err != nil {
    t.Fatalf("WriteFile monitor: %v", err)
}
```

### D. String literal fix (monitor_poll_test.go)

```go
submitPrompt := "Phase: submit\nTicket: {{.Ticket.Key}}\n"
monitorPrompt := "Phase: monitor\nTicket: {{.Ticket.Key}}\n"
```

## Follow-up issues to file

These code bugs are out of scope for this test-coverage ticket but should be tracked:

1. **RC/IC ordering data loss** (problem #3) — fix by sorting the combined slice chronologically or tracking separate afterID cursors per namespace
2. **afterID-not-found silent failure** (problem #4) — fix by returning a sentinel error or resetting `LastCommentID` when the tracked comment is not found
3. **Pagination concatenation** (problem #5) — fix by using `--jq '.[]'` to flatten pages or a streaming JSON decoder

## Files changed

| File | Change |
|------|--------|
| `internal/progress/summary_test.go` | Add `TestMonitorSummary` (table-driven, ~30 lines) |
| `internal/pipeline/github_poller_test.go` | Add `TestGetNewComments` with shell-script fake (~90 lines) |
| `internal/pipeline/monitor_poll_test.go` | Check errors from `os.MkdirAll`/`os.WriteFile`; replace `fmt.Sprintf` with literals |

## Test plan

- `go test ./internal/progress/...`
- `go test ./internal/pipeline/...`
