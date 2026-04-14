# Issue #115: Handle reviewReworkSignal on Skipped-Phase Gate Path — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent the internal `reviewReworkSignal` sentinel from leaking to the caller when a skipped review phase's gate fires a rework signal.

**Architecture:** Add an `errors.As` check for `reviewReworkSignal` on the skipped-phase gate path in `executePhases`, matching the existing pattern on the non-skipped path. Includes two new tests: one for the rework routing and one for the max-cycles boundary.

**Tech Stack:** Go, `errors.As`

**Spec:** `docs/superpowers/specs/2026-04-14-issue-115-skipped-phase-rework-signal.md`

---

### Task 1: Write the failing test for rework signal on skipped-phase gate

**Files:**
- Modify: `internal/pipeline/engine_test.go` (append after `TestEngine_ReviewReworkCyclesPersisted`)

- [ ] **Step 1: Write the failing test**

This test pre-completes all phases including review (with a "rework" verdict), then calls `engine.Run()`. Since no deps re-ran, review is skipped, `gatePhase` fires `reviewReworkSignal`. Currently this leaks as an opaque error.

```go
func TestEngine_ReviewReworkSignalOnSkippedGate(t *testing.T) {
	t.Run("skipped_review_rework_routes_to_implement", func(t *testing.T) {
		// Pre-complete all phases. Review has "rework" verdict.
		// On Run(), all phases are skipped. gatePhase("review") fires
		// reviewReworkSignal. The fix catches it and routes to implement.
		phases := []PhaseConfig{
			{
				Name:   "implement",
				Prompt: "implement.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "review",
				Type:      "parallel-review",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{
				// Rework re-run of implement.
				"implement": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
						RawText: "Impl after rework",
						CostUSD: 0.50,
					},
				}},
				// Review re-run after rework — this time passes.
				"review/go-specialist": {{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"findings":[]}`),
						RawText: "All clear",
						CostUSD: 0.10,
					},
				}},
			},
		}

		var events []Event
		engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.OnEvent = func(e Event) {
				events = append(events, e)
			}
		})

		// Pre-complete implement.
		state.Meta().Phases["implement"] = &PhaseState{
			Status:     PhaseCompleted,
			Generation: 1,
		}
		_ = state.WriteResult("implement", json.RawMessage(`{"tests_passed":true,"commits":1}`))
		_ = state.WriteArtifact("implement", []byte("Impl v1"))

		// Pre-complete review with "rework" verdict and ReworkCycles = 0.
		state.Meta().Phases["review"] = &PhaseState{
			Status:     PhaseCompleted,
			Generation: 1,
		}
		_ = state.WriteResult("review", json.RawMessage(`{"ticket_key":"TEST-1","findings":[{"severity":"major","file":"x.go","line":1,"issue":"error not wrapped","suggestion":"use fmt.Errorf"}],"verdict":"rework"}`))

		err := engine.Run(context.Background())
		if err != nil {
			t.Fatalf("Run should succeed after rework routing, got: %v", err)
		}

		// Rework should have routed to implement.
		if state.Meta().ReworkCycles != 1 {
			t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
		}

		// Should have review_rework_routed event.
		hasRouted := false
		for _, e := range events {
			if e.Kind == EventReviewReworkRouted {
				hasRouted = true
			}
		}
		if !hasRouted {
			t.Error("review_rework_routed event not emitted")
		}

		// Implement should have been called (rework re-run).
		implCalls := 0
		for _, call := range mock.calls {
			if call.Phase == "implement" {
				implCalls++
			}
		}
		if implCalls != 1 {
			t.Errorf("implement called %d times, want 1 (rework re-run)", implCalls)
		}
	})
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /home/ddebrito/dev/soda && go test ./internal/pipeline/... -run TestEngine_ReviewReworkSignalOnSkippedGate -v -count=1`

Expected: FAIL — the `reviewReworkSignal` leaks as an error: `"review rework signal: error not wrapped"`

- [ ] **Step 3: Commit the failing test**

```bash
git add internal/pipeline/engine_test.go
git commit -m "test(pipeline): add failing test for reviewReworkSignal on skipped-phase gate

Exercises the edge case where a fresh Run() encounters a pre-completed
review with 'rework' verdict. All phases are skipped, gatePhase fires
reviewReworkSignal, which currently leaks as an opaque error.

Issue: #115"
```

---

### Task 2: Implement the fix

**Files:**
- Modify: `internal/pipeline/engine.go:216-221`

- [ ] **Step 4: Apply the fix**

Replace the skipped-phase gate error handling in `executePhases` (engine.go:216-221):

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

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd /home/ddebrito/dev/soda && go test ./internal/pipeline/... -run TestEngine_ReviewReworkSignalOnSkippedGate -v -count=1`

Expected: PASS

- [ ] **Step 6: Run full engine test suite**

Run: `cd /home/ddebrito/dev/soda && go test ./internal/pipeline/... -v -count=1`

Expected: All tests pass.

- [ ] **Step 7: Commit the fix**

```bash
git add internal/pipeline/engine.go
git commit -m "fix(pipeline): catch reviewReworkSignal on skipped-phase gate path

Add errors.As check for reviewReworkSignal on the skipped-phase gate
path, matching the existing pattern on the non-skipped path (line 228).
Without this, the internal sentinel leaks to the caller as an opaque
error on Run() re-entry after a crash mid-rework.

Fixes: #115"
```

---

### Task 3: Add max-cycles boundary test on the skip path

**Files:**
- Modify: `internal/pipeline/engine_test.go`

- [ ] **Step 8: Write the max-cycles boundary test**

Add as a second subtest inside `TestEngine_ReviewReworkSignalOnSkippedGate`:

```go
	t.Run("skipped_review_max_cycles_returns_gate_error", func(t *testing.T) {
		// Pre-complete review with "rework" verdict, ReworkCycles at max.
		// gatePhase should return PhaseGateError, not reviewReworkSignal.
		phases := []PhaseConfig{
			{
				Name:   "implement",
				Prompt: "implement.md",
				Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			},
			{
				Name:      "review",
				Type:      "parallel-review",
				DependsOn: []string{"implement"},
				Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
				Reviewers: []ReviewerConfig{
					{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
				},
			},
		}

		mock := &flexMockRunner{
			responses: map[string][]flexResponse{},
		}

		engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
			cfg.MaxReworkCycles = 1
		})

		// Pre-complete implement.
		state.Meta().Phases["implement"] = &PhaseState{
			Status:     PhaseCompleted,
			Generation: 1,
		}
		_ = state.WriteResult("implement", json.RawMessage(`{"tests_passed":true,"commits":1}`))

		// Pre-complete review with "rework" verdict.
		state.Meta().Phases["review"] = &PhaseState{
			Status:     PhaseCompleted,
			Generation: 1,
		}
		_ = state.WriteResult("review", json.RawMessage(`{"ticket_key":"TEST-1","findings":[{"severity":"major","file":"x.go","line":1,"issue":"bad","suggestion":"fix"}],"verdict":"rework"}`))

		// Set ReworkCycles at max.
		state.Meta().ReworkCycles = 1

		err := engine.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError when rework cycles exhausted")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
		}
		if gateErr.Phase != "review" {
			t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "review")
		}
		if !strings.Contains(gateErr.Reason, "max cycles") {
			t.Errorf("gate error should mention max cycles, got: %q", gateErr.Reason)
		}
	})
```

- [ ] **Step 9: Run both subtests**

Run: `cd /home/ddebrito/dev/soda && go test ./internal/pipeline/... -run TestEngine_ReviewReworkSignalOnSkippedGate -v -count=1`

Expected: Both subtests PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/pipeline/engine_test.go
git commit -m "test(pipeline): add max-cycles boundary test for skipped-phase rework gate

Confirms that when ReworkCycles >= maxReworkCycles on a skipped review
phase, gatePhase returns PhaseGateError (not reviewReworkSignal).

Issue: #115"
```
