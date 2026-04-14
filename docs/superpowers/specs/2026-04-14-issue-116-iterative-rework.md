# Iterative Rework Routing in `executePhases`

**Issue:** decko/soda#116
**Date:** 2026-04-14
**Scope:** `internal/pipeline/engine.go`

## Problem

`handleReviewRework` (engine.go:252-288) calls `executePhases` recursively. When a review phase returns a `reviewReworkSignal`, `executePhases` catches it and calls `handleReviewRework`, which finds the implement phase index and calls `executePhases` again. Recursion depth is bounded by `MaxReworkCycles` (default 2), so stack overflow isn't a risk, but an iterative approach is more idiomatic Go and easier to reason about.

### Current call chain

```
Run() → executePhases(allPhases)
  → runPhase("review") → gatePhase → reviewReworkSignal
  → handleReviewRework()
    → executePhases(phases[implIdx:])         ← recursive
      → runPhase("review") → gatePhase → reviewReworkSignal
      → handleReviewRework()
        → executePhases(phases[implIdx:])     ← recursive (cycle 2)
```

## Design

Convert the recursion into a `for` loop inside `executePhases`. When a `reviewReworkSignal` is caught after `runPhase`, increment the rework counter, emit the routing event, flush meta, and restart the inner loop from the implement phase — instead of calling `handleReviewRework` → `executePhases` recursively.

### Pseudocode

All load-bearing lines are shown explicitly — no "unchanged" elisions for logic that changes variable references.

```go
func (e *Engine) executePhases(ctx context.Context, phases []PhaseConfig, forceFirst bool) error {
    // Resolve implement index once — Pipeline.Phases is immutable during
    // execution, so implIdx is stable for the lifetime of this call.
    implIdx := -1
    for i, p := range e.config.Pipeline.Phases {
        if p.Name == "implement" {
            implIdx = i
            break
        }
    }

    currentPhases := phases
    currentForceFirst := forceFirst

    for {
        rework := false
        for idx, phase := range currentPhases {
            if err := ctx.Err(); err != nil {
                return fmt.Errorf("engine: context cancelled: %w", err)
            }

            // NOTE: uses currentForceFirst (not the parameter forceFirst)
            // so that rework restarts force-run the implement phase.
            skipCheck := !(currentForceFirst && idx == 0)
            if skipCheck && e.shouldSkip(phase) {
                if err := e.gatePhase(phase); err != nil {
                    return err
                }
                e.emit(Event{Phase: phase.Name, Kind: EventPhaseSkipped})
                continue
            }

            if err := e.runPhase(ctx, phase); err != nil {
                var reworkSig *reviewReworkSignal
                if errors.As(err, &reworkSig) {
                    // --- inlined handleReviewRework logic ---
                    e.state.Meta().ReworkCycles++
                    cycle := e.state.Meta().ReworkCycles
                    e.emit(Event{
                        Phase: phase.Name,
                        Kind:  EventReviewReworkRouted,
                        Data: map[string]any{
                            "rework_cycle":      cycle,
                            "max_rework_cycles": e.config.maxReworkCycles(),
                            "routing_to":        "implement",
                        },
                    })
                    if err := e.state.flushMeta(); err != nil {
                        return fmt.Errorf("engine: flush meta after rework routing: %w", err)
                    }
                    if implIdx < 0 {
                        return fmt.Errorf("engine: review rework routing requires an implement phase")
                    }
                    currentPhases = e.config.Pipeline.Phases[implIdx:]
                    currentForceFirst = true
                    rework = true
                    break // restart outer loop
                }
                return err
            }
            e.reranPhases[phase.Name] = true

            if e.config.Mode == Checkpoint {
                e.emit(Event{Phase: phase.Name, Kind: EventCheckpointPause})
                select {
                case <-e.confirmCh:
                case <-ctx.Done():
                    return fmt.Errorf("engine: context cancelled during checkpoint: %w", ctx.Err())
                }
            }
        }
        if !rework {
            return nil
        }
    }
}
```

### What gets deleted

`handleReviewRework` (engine.go:252-288) is deleted entirely. Its four responsibilities move inline:

1. Increment `ReworkCycles` and emit `EventReviewReworkRouted` → inline in rework branch
2. Flush meta (with error propagation) → inline in rework branch
3. Find implement index → once at top of `executePhases`
4. Call `executePhases` recursively → outer `for` loop restart

### Behavioral equivalence

| Aspect | Before (recursive) | After (iterative) |
|--------|--------------------|--------------------|
| Rework cycle counter | Incremented in `handleReviewRework` | Incremented inline |
| Event emission | `EventReviewReworkRouted` emitted in `handleReviewRework` | Same event, inline |
| Meta flush | In `handleReviewRework`, error propagated | Inline, error propagated |
| `flushMeta` failure | Returns error, aborts pipeline | Returns error, aborts pipeline |
| Implement index lookup | Each recursive call | Once at top of `executePhases` |
| `forceFirst` for implement | Passed to recursive `executePhases` | Set on `currentForceFirst` |
| Checkpoint after rework-triggering review | Skipped (returns before checkpoint) | Skipped (breaks before checkpoint) |
| `reranPhases` map | Shared across recursive frames | Shared across loop iterations |

## Files changed

| File | Change |
|------|--------|
| `internal/pipeline/engine.go` | Rewrite `executePhases` with outer loop; delete `handleReviewRework` |

## Test plan

- All existing engine tests pass unchanged (behavior is identical)
- No new tests required — rework routing is already covered by existing tests
- `go test ./internal/pipeline/...`
