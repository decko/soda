# Handle `reviewReworkSignal` on Skipped-Phase Gate Path

**Issue:** decko/soda#115
**Date:** 2026-04-14
**Scope:** `internal/pipeline/engine.go`

## Problem

In `executePhases` (engine.go:216-221), when a phase is skipped, `gatePhase` is still called to re-evaluate domain rules against the stored result. If the stored review result has verdict `"rework"` and `ReworkCycles < maxReworkCycles`, `gatePhase` returns a `reviewReworkSignal`. On the skipped-phase path, this signal is returned as-is:

```go
if skipCheck && e.shouldSkip(phase) {
    if err := e.gatePhase(phase); err != nil {
        // reviewReworkSignal on a skipped phase shouldn't happen
        // in practice, but treat it as a normal gate error.
        return err  // ← leaks reviewReworkSignal to caller
    }
    ...
}
```

The comment says "treat it as a normal gate error," but the code just returns it. `reviewReworkSignal` is an internal sentinel — it's not a `PhaseGateError`. When it reaches `Run()` (line 158), it propagates to the user as an opaque error: `"review rework signal: ..."`.

By contrast, the non-skipped path (line 228-231) properly catches this signal via `errors.As` and routes to `handleReviewRework`.

### When this can happen

The triggering scenario is a fresh `Run()` (not `Resume`) where all phases through review are completed from a prior execution and none re-run in the current execution:

1. Pipeline runs: review completes with verdict `"rework"`, `handleReviewRework` increments `ReworkCycles` and persists via `flushMeta`
2. The recursive `executePhases` call starts implement, but implement crashes (OOM, signal kill, budget exhaustion)
3. User re-runs with `soda run TICKET` (a fresh `Run()`)
4. All phases through review are completed from the prior run. Nothing has re-run yet in this execution, so `reranPhases` is empty. `shouldSkip` returns true for each phase including review
5. `gatePhase("review")` reads the persisted `"rework"` result, sees `ReworkCycles < maxReworkCycles`, returns `reviewReworkSignal`
6. Current code: leaks the signal as an opaque error. Proposed fix: catches it and routes to rework

Note: `Resume` from a phase *after* review (e.g., `--from submit`) does not trigger this because `Resume` slices `Phases[startIdx:]`, excluding review from iteration entirely.

In practice this is unlikely but not impossible — the comment acknowledges it. The fix is cheap and makes the code correct regardless of edge cases.

## Design

Add an `errors.As` check for `reviewReworkSignal` on the skipped-phase gate path, matching the pattern already used on the non-skipped path (line 228-231). Update the stale comment to describe the new behavior.

### Change

```go
if skipCheck && e.shouldSkip(phase) {
    if err := e.gatePhase(phase); err != nil {
        // Handle reviewReworkSignal on skipped phases — can occur on
        // Run() re-entry when a prior rework crashed mid-implement.
        var reworkSig *reviewReworkSignal
        if errors.As(err, &reworkSig) {
            return e.handleReviewRework(ctx, phase)
        }
        return err
    }
    e.emit(Event{Phase: phase.Name, Kind: EventPhaseSkipped})
    continue
}
```

If #116 (iterative rework) lands first, the rework handling will be inline instead of calling `handleReviewRework`, but the fix is the same: catch `reviewReworkSignal` and route to rework instead of leaking it.

## Files changed

| File | Change |
|------|--------|
| `internal/pipeline/engine.go` | Add `reviewReworkSignal` check on skipped-phase gate path (~5 lines including updated comment) |

## Test plan

- Add a test that exercises the skipped-phase gate path with a stored `"rework"` review result:
  1. Pre-complete the review phase with a `{"verdict":"rework","findings":[...]}` result
  2. Set `ReworkCycles = 0` (below max) so gatePhase returns `reviewReworkSignal`
  3. Run `executePhases` with review in the skip set
  4. Assert: pipeline routes to implement (not an error returned to caller)
- Add a complementary test for the max-cycles boundary on the skip path:
  1. Pre-complete review with `"rework"` verdict
  2. Set `ReworkCycles = maxReworkCycles` so gatePhase returns `PhaseGateError`
  3. Assert: `PhaseGateError` is returned (not `reviewReworkSignal`)
- All existing engine tests pass
- `go test ./internal/pipeline/...`
