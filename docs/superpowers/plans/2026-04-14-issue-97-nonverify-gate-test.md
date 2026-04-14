# Issue #97: Non-Verify PhaseGateError Test Coverage — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add test coverage for the generic `isPhaseGateError` branch in `formatNextSteps` (run.go:677-683), which currently has zero coverage.

**Architecture:** Single test function following the established `TestPrintSummary*` pattern — create state, construct `PhaseGateError`, call `fprintSummary`, assert output contains expected strings and does NOT contain verify-specific strings.

**Tech Stack:** Go, `strings.Contains`, `bytes.Buffer`

**Spec:** `docs/superpowers/specs/2026-04-14-issue-97-nonverify-gate-test.md`

---

### Task 1: Write the test and verify it passes

**Files:**
- Modify: `cmd/soda/run_test.go` (append after `TestPrintSummaryVerifyGateNoWorktree`)

- [ ] **Step 1: Write the test**

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

- [ ] **Step 2: Run the test**

Run: `cd /home/ddebrito/dev/soda && go test ./cmd/soda/... -run TestPrintSummaryPhaseGateGeneric -v -count=1`

Expected: PASS — the generic gate branch already works correctly, we're just adding coverage.

- [ ] **Step 3: Run the full run_test.go suite to check for regressions**

Run: `cd /home/ddebrito/dev/soda && go test ./cmd/soda/... -v -count=1`

Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/soda/run_test.go
git commit -m "test(cli): add coverage for non-verify PhaseGateError next-steps branch

Exercises the generic isPhaseGateError branch in formatNextSteps
(run.go:677-683) which had zero test coverage. Uses Phase: 'triage'
with positive + negative assertions to prove branch exclusivity.

Fixes: #97"
```
