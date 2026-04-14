# Fix: extractJSONByDepth extracts wrong JSON from truncated CLI output

**Issue:** #106
**Date:** 2026-04-14

## Problem

`extractJSONByDepth` in `internal/claude/parser.go:121` doesn't validate that extracted JSON is a result envelope. When Claude CLI output is truncated (missing closing `}`), Strategy 1 fails and Strategy 2 finds the inner `structured_output` JSON instead, producing: `unexpected response type: ""`.

This is a known limitation documented as a skipped test at `parser_test.go:239`.

## Root Cause

Strategy 1 (`extractJSON`) checks `isResultEnvelope`. Strategy 2 (`extractJSONByDepth`) does not. The asymmetry means truncated output extracts the wrong JSON object.

Trigger: resource contention from 3 concurrent pipeline runs causes truncated CLI stdout.

## Approach

**Approach B: prefer envelopes, fall back to any JSON for diagnostics.**

Both the Go Specialist and AI Harness Specialist independently recommended this approach:

- Makes Strategy 2 symmetric with Strategy 1 (both prefer envelopes)
- Preserves diagnostic value — fallback JSON appears in `ParseError.Raw` for operators
- No new error types needed — `ParseResponse` already handles `type != "result"`
- No context budget concern — `ParseError.Error()` excludes raw data
- Most resilient to future CLI format changes

## Implementation

### `extractJSONByDepth` (parser.go)

Scan all `{` candidates, prefer result envelopes, fall back to outermost valid JSON:

```go
func extractJSONByDepth(output []byte) ([]byte, error) {
    end := bytes.LastIndexByte(output, '}')
    if end == -1 {
        return nil, errNoJSON
    }

    var fallback []byte
    for i := end; i >= 0; i-- {
        if output[i] == '{' {
            candidate := output[i : end+1]
            if json.Valid(candidate) {
                if isResultEnvelope(candidate) {
                    return candidate, nil
                }
                fallback = candidate // keep overwriting; last is outermost
            }
        }
    }

    if fallback != nil {
        return fallback, nil
    }
    return nil, errNoJSON
}
```

Key detail: `fallback` is overwritten on each valid-but-non-envelope candidate. Since the scan moves leftward (larger candidates), the final `fallback` is the outermost (largest) valid JSON — most diagnostic value.

### Tests

1. **Un-skip `last_line_non_envelope_json`** — remove `t.Skip`, assert result envelope is extracted when both envelope and non-envelope JSON exist in output
2. **Add `truncated_envelope`** — outer envelope missing closing `}`, inner `structured_output` is valid JSON. Assert inner JSON is returned (non-envelope), and `ParseResponse` produces `ParseError`
3. **Add `envelope_after_non_envelope`** — result envelope appears before non-envelope JSON. Assert envelope is found and preferred

### Files changed

- `internal/claude/parser.go` — `extractJSONByDepth` function (~15 lines changed)
- `internal/claude/parser_test.go` — un-skip 1 test, add 2 new tests (~30 lines added)

## Follow-up

File a separate ticket for `sandbox.ExitError` classification gap: `classifyError` in `engine.go` doesn't handle `sandbox.ExitError`. OOM kills and signal kills fall into "unknown" category and are NOT retried. This likely contributes to concurrent pipeline failures.
