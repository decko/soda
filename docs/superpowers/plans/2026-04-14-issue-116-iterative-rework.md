# Issue #116: Iterative Rework Routing — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert the recursive `handleReviewRework` → `executePhases` call chain into an iterative outer loop, making the rework routing more idiomatic Go.

**Architecture:** Wrap the phase-iteration loop in `executePhases` with an outer `for` that restarts from the implement phase on rework. Inline `handleReviewRework`'s 4 responsibilities (increment cycles, emit event, flush meta, restart) and delete the function entirely.

**Tech Stack:** Go

**Spec:** `docs/superpowers/specs/2026-04-14-issue-116-iterative-rework.md`

---

### Task 1: Run existing tests to establish baseline

**Files:**
- None (verification only)

- [ ] **Step 1: Run the full engine test suite**

Run: `cd /home/ddebrito/dev/soda && go test ./internal/pipeline/... -v -count=1`

Expected: All tests pass. Note the rework-related tests:
- `TestEngine_ReviewReworkRouting/rework_routes_back_to_implement`
- `TestEngine_ReviewReworkRouting/max_rework_cycles_blocks`
- `TestEngine_ReviewReworkRouting/pass_with_follow_ups_proceeds`
- `TestEngine_ReviewReworkRouting/missing_implement_phase_errors`
- `TestEngine_ReviewReworkFeedbackInjected`
- `TestEngine_ReviewReworkDefaultMaxCycles`
- `TestEngine_ReviewReworkCyclesPersisted`

These must all continue to pass after the refactor.

---

### Task 2: Rewrite `executePhases` with iterative rework loop

**Files:**
- Modify: `internal/pipeline/engine.go:206-288`

- [ ] **Step 2: Replace `executePhases` and delete `handleReviewRework`**

Replace `executePhases` (lines 206-247) and delete `handleReviewRework` (lines 249-288) with this single function:

```go
// executePhases runs a slice of phases, handling skip logic, checkpoint
// pauses, and review rework routing. When forceFirst is true, the first
// phase in the slice is always re-run regardless of completion status.
//
// Rework routing is handled iteratively: when a reviewReworkSignal is
// caught, the loop restarts from the implement phase instead of
// recursing through handleReviewRework.
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
						return fmt.Errorf("engine: review rework routing requires an implement phase in the pipeline")
					}

					currentPhases = e.config.Pipeline.Phases[implIdx:]
					currentForceFirst = true
					rework = true
					break
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

- [ ] **Step 3: Verify the `handleReviewRework` function is fully deleted**

Search for any remaining references:

Run: `cd /home/ddebrito/dev/soda && grep -n 'handleReviewRework' internal/pipeline/engine.go`

Expected: No output (zero references remain).

- [ ] **Step 4: Run the full engine test suite**

Run: `cd /home/ddebrito/dev/soda && go test ./internal/pipeline/... -v -count=1`

Expected: All tests pass, including all rework-related tests listed in Task 1.

- [ ] **Step 5: Commit**

```bash
git add internal/pipeline/engine.go
git commit -m "refactor(pipeline): convert recursive rework routing to iterative loop

Replace the recursive handleReviewRework -> executePhases call chain
with an outer for loop that restarts from the implement phase on
rework signal. Delete handleReviewRework entirely — its 4
responsibilities (increment cycles, emit event, flush meta, restart)
are inlined in the rework branch.

Behavioral equivalence verified — all existing rework tests pass
unchanged. flushMeta error propagation preserved.

Fixes: #116"
```

---

### Task 3: Verify no regressions across the full codebase

**Files:**
- None (verification only)

- [ ] **Step 6: Run the full project test suite**

Run: `cd /home/ddebrito/dev/soda && go test ./... -count=1`

Expected: All tests pass.

- [ ] **Step 7: Run vet**

Run: `cd /home/ddebrito/dev/soda && go vet ./internal/pipeline/...`

Expected: No issues.
