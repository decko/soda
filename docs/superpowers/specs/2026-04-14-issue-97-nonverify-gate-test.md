# Add Test Coverage for Non-Verify `PhaseGateError` Next-Steps

**Issue:** decko/soda#97
**Date:** 2026-04-14
**Scope:** `cmd/soda/run_test.go`

## Problem

`formatNextSteps` (run.go:664-727) has a `switch` that checks error types in order:

1. `isVerifyGateError` (line 665) — `PhaseGateError` with `Phase == "verify"`
2. `isPhaseGateError` (line 677) — any `PhaseGateError` (generic)
3. `isBudgetExceededError`, `isTransientError`, `isParseError`, default

The ordering is correct (`isVerifyGateError` is a strict subset of `isPhaseGateError`), but the generic `isPhaseGateError` branch (lines 677-683) has **zero test coverage**. Both existing verify-gate tests (`TestPrintSummaryVerifyGate`, `TestPrintSummaryVerifyGateNoWorktree`) use `Phase: "verify"`, which always hits the verify-specific branch.

One test is sufficient because the generic branch has no internal conditionals — it uses `ge.Phase` and `ge.Reason` directly with no sub-branches.

The generic branch outputs:
```
  Phase "plan" was gated: no tasks in plan
  • Re-run from that phase after addressing the issue:
    soda run TICKET --from plan  (retry after fixing the gate condition)
```

This path is exercised for triage gates ("ticket not automatable"), plan gates ("no tasks in plan"), and submit gates ("no PR URL").

## Design

Add `TestPrintSummaryPhaseGateGeneric` following the exact pattern of the existing verify-gate tests. Uses `strings.Contains` / `!strings.Contains` with `t.Error` to match the established test style in `run_test.go`.

### Test case

```go
func TestPrintSummaryPhaseGateGeneric(t *testing.T) {
    dir := t.TempDir()
    state, _ := pipeline.LoadOrCreate(dir, "PROJ-30")
    meta := state.Meta()
    meta.Branch = "soda/PROJ-30"
    meta.Worktree = "/tmp/worktrees/PROJ-30"
    meta.TotalCost = 0.50

    meta.Phases["triage"] = &pipeline.PhaseState{
        Status: pipeline.PhaseFailed, DurationMs: 8000, Cost: 0.50,
        Error: "ticket not automatable",
    }

    gateErr := &pipeline.PhaseGateError{Phase: "triage", Reason: "ticket not automatable"}
    var buf bytes.Buffer
    fprintSummary(&buf, state, testPhases(), "Generic gate test", 30*time.Second, gateErr, nil)
    output := buf.String()

    // Verify generic gate output — NOT the verify-specific advice.
    if !strings.Contains(output, `Phase "triage" was gated`) {
        t.Error("expected generic gate message for triage phase")
    }
    if !strings.Contains(output, "ticket not automatable") {
        t.Error("expected gate reason in output")
    }
    if !strings.Contains(output, "--from triage") {
        t.Error("expected --from triage suggestion")
    }
    if !strings.Contains(output, "retry after fixing the gate condition") {
        t.Error("expected retry guidance")
    }

    // Must NOT contain verify-specific advice.
    if strings.Contains(output, "Review the verify output") {
        t.Error("should not contain verify-specific advice for non-verify gate")
    }
    if strings.Contains(output, "--from implement") {
        t.Error("should not contain --from implement for non-verify gate")
    }
    if strings.Contains(output, "--from verify") {
        t.Error("should not contain --from verify for non-verify gate")
    }
}
```

### Why `Phase: "triage"` not `"plan"`

Either works. Triage is the first phase, so the test state is minimal (no need to pre-complete earlier phases). It also exercises a common real-world scenario — the ticket is flagged as not automatable.

## Files changed

| File | Change |
|------|--------|
| `cmd/soda/run_test.go` | Add `TestPrintSummaryPhaseGateGeneric` (~30 lines) |

## Test plan

- `go test ./cmd/soda/...`
- Confirm the new test exercises lines 677-683 of `run.go` (the `isPhaseGateError` branch)
