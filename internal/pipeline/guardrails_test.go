package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
)

func TestEngine_BudgetExceeded(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	// Triage costs $14, budget is $15. After triage, total is $14 which is < $15,
	// but we also verify a budget warning is emitted at 90% ($13.50).
	// To actually exceed the budget, set cost to $16.
	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 16.0,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostUSD = 15.0
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected BudgetExceededError")
	}

	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got: %v", err)
	}
	if budgetErr.Phase != "plan" {
		t.Errorf("budget error phase = %q, want %q", budgetErr.Phase, "plan")
	}
}

func TestEngine_BudgetExceeded_AtLimit(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 15.0,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostUSD = 15.0
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected BudgetExceededError")
	}

	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got: %v", err)
	}
	if budgetErr.Phase != "plan" {
		t.Errorf("budget error phase = %q, want %q", budgetErr.Phase, "plan")
	}
}

func TestEngine_GatePhase_TriageNotAutomatable(t *testing.T) {
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"no","block_reason":"needs human design review"}`),
					RawText: "Not automatable",
					CostUSD: 0.05,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseGateError for non-automatable ticket")
	}

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if gateErr.Phase != "triage" {
		t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "triage")
	}
	if !strings.Contains(gateErr.Reason, "human design review") {
		t.Errorf("gate error reason should mention block_reason, got: %q", gateErr.Reason)
	}
}

func TestEngine_GatePhase_TriageSkipPlanDoesNotAffectGate(t *testing.T) {
	// Triage output with skip_plan=true should still pass the gate when
	// automatable=true. The gate only checks automatable.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes","skip_plan":true}`),
					RawText: "Automatable with existing plan",
					CostUSD: 0.05,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected no error when automatable=true with skip_plan, got: %v", err)
	}

	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
}

func TestEngine_GatePhase_TriageNotAutomatableWithSkipPlanStillBlocks(t *testing.T) {
	// Even if skip_plan=true, automatable=false should still gate.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"no","block_reason":"complex refactor","skip_plan":true}`),
					RawText: "Not automatable",
					CostUSD: 0.05,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseGateError when automatable=false, even with skip_plan=true")
	}

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if gateErr.Phase != "triage" {
		t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "triage")
	}
}

func TestEngine_GatePhase_ReviewUnmarshalError(t *testing.T) {
	// When a phase with Rework config produces output that doesn't unmarshal
	// as valid JSON, gateRework should gracefully skip (return nil), consistent
	// with all other gating cases in gatePhase.
	phases := []PhaseConfig{
		{
			Name:   "review",
			Prompt: "review.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework: &ReworkConfig{Target: "implement"},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"review": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`not valid json`),
					RawText: "corrupt output",
					CostUSD: 0.01,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected nil (graceful skip) for corrupt review result, got: %v", err)
	}
	// Phase should still complete — the rework gate is skipped on unmarshal failure.
	if !state.IsCompleted("review") {
		t.Error("review phase should be completed")
	}
}

func TestEngine_gateRework_nilReworkConfig(t *testing.T) {
	// Call gateRework directly with a PhaseConfig where Rework is nil.
	// This exercises the nil guard inside gateRework itself, bypassing
	// the caller guard in gatePhase (engine.go:1144).
	phases := []PhaseConfig{
		{
			Name:   "x",
			Prompt: "x.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	engine, _ := setupEngine(t, phases, &flexMockRunner{})

	phase := PhaseConfig{Name: "review"} // Rework is nil
	raw := json.RawMessage(`{"verdict":"rework","findings":[{"severity":"critical","issue":"missing tests"}]}`)

	if err := engine.gateRework(phase, raw); err != nil {
		t.Fatalf("expected nil when Rework config is nil, got: %v", err)
	}
}

func TestEngine_downgradeToFollowUps(t *testing.T) {
	// Direct unit test for downgradeToFollowUps: verify it rewrites the
	// verdict on disk, preserves unknown fields, and emits the correct event.
	phases := []PhaseConfig{
		{
			Name:   "review",
			Prompt: "review.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework: &ReworkConfig{Target: "implement"},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.MaxReworkCycles = 1
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})
	state.Meta().ReworkCycles = 1

	// Write a result with rework verdict, minor findings, and an extra
	// field ("custom_metric") that is not part of schemas.ReviewOutput.
	// downgradeToFollowUps must preserve it through the roundtrip.
	raw := json.RawMessage(`{"ticket_key":"TEST-1","custom_metric":42,"findings":[{"severity":"minor","file":"a.go","issue":"naming","suggestion":"rename"},{"severity":"minor","file":"b.go","issue":"style","suggestion":"fix"}],"verdict":"rework"}`)
	if err := state.WriteResult("review", raw); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}

	phase := PhaseConfig{
		Name:   "review",
		Rework: &ReworkConfig{Target: "implement"},
	}
	findings := []struct {
		Severity string `json:"severity"`
		Issue    string `json:"issue"`
	}{
		{Severity: "minor", Issue: "naming"},
		{Severity: "minor", Issue: "style"},
	}
	if err := engine.downgradeToFollowUps(phase, raw, findings); err != nil {
		t.Fatalf("downgradeToFollowUps: %v", err)
	}

	// Verify the result on disk was rewritten.
	updated, err := state.ReadResult("review")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}

	// Parse as map to check all fields including extras.
	var doc map[string]any
	if err := json.Unmarshal(updated, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, _ := doc["verdict"].(string); v != "pass-with-follow-ups" {
		t.Errorf("verdict = %q, want %q", v, "pass-with-follow-ups")
	}
	findingsSlice, _ := doc["findings"].([]any)
	if len(findingsSlice) != 2 {
		t.Errorf("findings count = %d, want 2", len(findingsSlice))
	}
	// Extra field must survive the roundtrip.
	if cm, _ := doc["custom_metric"].(float64); cm != 42 {
		t.Errorf("custom_metric = %v, want 42 (extra fields must be preserved)", cm)
	}

	// Verify event was emitted.
	hasDowngraded := false
	for _, e := range events {
		if e.Kind == EventReworkMinorsDowngraded {
			hasDowngraded = true
			if mc, _ := e.Data["minor_count"].(int); mc != 2 {
				t.Errorf("minor_count = %d, want 2", mc)
			}
		}
	}
	if !hasDowngraded {
		t.Error("rework_minors_downgraded event not emitted")
	}
}

func TestEngine_gateRework_maxCyclesMinorsOnly(t *testing.T) {
	// When max rework cycles exhausted with only minor findings,
	// gateRework should downgrade to pass-with-follow-ups (return nil).
	phases := []PhaseConfig{
		{
			Name:   "review",
			Prompt: "review.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework: &ReworkConfig{Target: "implement"},
		},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.MaxReworkCycles = 1
	})
	state.Meta().ReworkCycles = 1

	// Write the result to disk so downgradeToFollowUps can rewrite it.
	raw := json.RawMessage(`{"ticket_key":"TEST-1","findings":[{"severity":"minor","file":"x.go","issue":"naming","suggestion":"rename"}],"verdict":"rework"}`)
	if err := state.WriteResult("review", raw); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}

	phase := PhaseConfig{
		Name:   "review",
		Rework: &ReworkConfig{Target: "implement"},
	}
	err := engine.gateRework(phase, raw)
	if err != nil {
		t.Fatalf("expected nil (downgrade), got: %v", err)
	}

	// Verify the result was rewritten.
	updated, err := state.ReadResult("review")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	var output schemas.ReviewOutput
	if err := json.Unmarshal(updated, &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if output.Verdict != "pass-with-follow-ups" {
		t.Errorf("verdict = %q, want %q", output.Verdict, "pass-with-follow-ups")
	}
}

func TestEngine_gateRework_maxCyclesCriticalBlocks(t *testing.T) {
	// When max rework cycles exhausted with critical findings,
	// gateRework should still return PhaseGateError.
	phases := []PhaseConfig{
		{
			Name:   "review",
			Prompt: "review.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework: &ReworkConfig{Target: "implement"},
		},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.MaxReworkCycles = 1
	})
	state.Meta().ReworkCycles = 1

	phase := PhaseConfig{
		Name:   "review",
		Rework: &ReworkConfig{Target: "implement"},
	}
	raw := json.RawMessage(`{"verdict":"rework","findings":[{"severity":"critical","issue":"SQL injection"}]}`)

	err := engine.gateRework(phase, raw)
	if err == nil {
		t.Fatal("expected PhaseGateError when critical findings remain")
	}

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
	}
	if !strings.Contains(gateErr.Reason, "SQL injection") {
		t.Errorf("reason should mention the critical issue, got: %q", gateErr.Reason)
	}
}

func TestEngine_VerifyFailRoutesToPatchViaCorrective(t *testing.T) {
	// When verify fails and has CorrectiveConfig pointing to patch,
	// the engine should route to patch, then re-run verify.
	// Patch must come before verify in pipeline order so the forward
	// pass continues into verify after patch completes.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 2,
				OnExhausted: "stop",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"verify": {
				// First verify: FAIL
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix test"],"criteria_results":[],"command_results":[]}`),
						RawText: "fail",
						CostUSD: 0.05,
					},
				},
				// Second verify: PASS
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS","criteria_results":[],"command_results":[]}`),
						RawText: "pass",
						CostUSD: 0.05,
					},
				},
			},
			"patch": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[{"fix_index":0,"status":"fixed","description":"fixed test"}],"files_changed":[],"tests_passed":true,"too_complex":false}`),
					RawText: "patched",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// All phases should be completed.
	if !state.IsCompleted("implement") {
		t.Error("implement should be completed")
	}
	if !state.IsCompleted("verify") {
		t.Error("verify should be completed")
	}
	if !state.IsCompleted("patch") {
		t.Error("patch should be completed")
	}

	// PatchCycles should be 1 (not ReworkCycles).
	if state.Meta().PatchCycles != 1 {
		t.Errorf("PatchCycles = %d, want 1", state.Meta().PatchCycles)
	}
	if state.Meta().ReworkCycles != 0 {
		t.Errorf("ReworkCycles = %d, want 0", state.Meta().ReworkCycles)
	}

	// Runner should have been called: implement, verify(fail), patch, verify(pass).
	if len(mock.calls) != 4 {
		t.Errorf("runner called %d times, want 4", len(mock.calls))
	}
}

func TestEngine_VerifyFailNoCorrectiveConfigStops(t *testing.T) {
	// Without CorrectiveConfig, verify FAIL should return PhaseGateError.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			// No Corrective config.
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix X"],"criteria_results":[],"command_results":[]}`),
					RawText: "fail",
					CostUSD: 0.05,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock)
	err := engine.Run(context.Background())

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if gateErr.Phase != "verify" {
		t.Errorf("gate error phase = %q, want verify", gateErr.Phase)
	}
}

func TestEngine_PatchExhaustedStops(t *testing.T) {
	// When patch attempts are exhausted and on_exhausted is "stop",
	// the engine should return a PhaseGateError.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 1,
				OnExhausted: "stop",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"verify": {
				// First verify: FAIL → routes to patch.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix1"],"criteria_results":[],"command_results":[]}`),
						RawText: "fail1",
						CostUSD: 0.05,
					},
				},
				// Second verify (after patch): FAIL again → exhausted.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix2"],"criteria_results":[],"command_results":[]}`),
						RawText: "fail2",
						CostUSD: 0.05,
					},
				},
			},
			"patch": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":false,"too_complex":false}`),
					RawText: "patched",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	err := engine.Run(context.Background())
	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if !strings.Contains(gateErr.Reason, "patch attempts exhausted") {
		t.Errorf("gate error reason should mention patch attempts, got: %q", gateErr.Reason)
	}

	// PatchCycles should be 1.
	if state.Meta().PatchCycles != 1 {
		t.Errorf("PatchCycles = %d, want 1", state.Meta().PatchCycles)
	}

	// Should have patch_exhausted event.
	hasExhausted := false
	for _, ev := range events {
		if ev.Kind == EventPatchExhausted {
			hasExhausted = true
		}
	}
	if !hasExhausted {
		t.Error("patch_exhausted event not emitted")
	}
}

func TestEngine_PatchExhaustedEscalates(t *testing.T) {
	// When patch attempts are exhausted and on_exhausted is "escalate",
	// the engine should route to the escalation target.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 1,
				OnExhausted: "escalate",
				EscalateTo:  "implement",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				// First implement call.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":[{"hash":"a1","message":"impl","task_id":"T1"}],"files_changed":[{"path":"a.go","action":"modified"}]}`),
						RawText: "impl1",
						CostUSD: 0.10,
					},
				},
				// Second implement call (escalation).
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":[{"hash":"a2","message":"escalated","task_id":"T1"}],"files_changed":[{"path":"a.go","action":"modified"}]}`),
						RawText: "impl2",
						CostUSD: 3.00,
					},
				},
			},
			"verify": {
				// First verify: FAIL → routes to patch.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix1"],"criteria_results":[],"command_results":[]}`),
						RawText: "fail1",
						CostUSD: 0.05,
					},
				},
				// Second verify (after patch): FAIL → exhausted → escalate.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix2"],"criteria_results":[],"command_results":[]}`),
						RawText: "fail2",
						CostUSD: 0.05,
					},
				},
				// Third verify (after escalated implement): PASS.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS","criteria_results":[],"command_results":[]}`),
						RawText: "pass",
						CostUSD: 0.05,
					},
				},
			},
			"patch": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":false,"too_complex":false}`),
					RawText: "patched",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
		cfg.MaxCostUSD = 100.0 // plenty of budget
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// EscalatedFromPatch should be set.
	if !state.Meta().EscalatedFromPatch {
		t.Error("EscalatedFromPatch should be true after escalation")
	}

	// Should have emitted patch_escalated event.
	hasEscalated := false
	for _, ev := range events {
		if ev.Kind == EventPatchEscalated {
			hasEscalated = true
		}
	}
	if !hasEscalated {
		t.Error("patch_escalated event not emitted")
	}
}

func TestEngine_PatchTooComplexEscalates(t *testing.T) {
	// When patch reports too_complex, the engine should escalate immediately.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 2,
				OnExhausted: "escalate",
				EscalateTo:  "implement",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":[{"hash":"a1","message":"impl","task_id":"T1"}],"files_changed":[{"path":"a.go","action":"modified"}]}`),
						RawText: "impl1",
						CostUSD: 0.10,
					},
				},
				// Escalated implement.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"tests_passed":true,"commits":[{"hash":"a2","message":"escalated","task_id":"T1"}],"files_changed":[{"path":"a.go","action":"modified"}]}`),
						RawText: "impl2",
						CostUSD: 3.00,
					},
				},
			},
			"verify": {
				// First: FAIL → patch.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["complex fix"],"criteria_results":[],"command_results":[]}`),
						RawText: "fail",
						CostUSD: 0.05,
					},
				},
				// After escalated implement: PASS.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"PASS","criteria_results":[],"command_results":[]}`),
						RawText: "pass",
						CostUSD: 0.05,
					},
				},
			},
			"patch": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":false,"too_complex":true,"too_complex_reason":"requires refactoring multiple packages"}`),
					RawText: "too complex",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
		cfg.MaxCostUSD = 100.0
	})

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have patch_too_complex event.
	hasTooComplex := false
	for _, ev := range events {
		if ev.Kind == EventPatchTooComplex {
			hasTooComplex = true
		}
	}
	if !hasTooComplex {
		t.Error("patch_too_complex event not emitted")
	}

	if !state.Meta().EscalatedFromPatch {
		t.Error("EscalatedFromPatch should be true after too_complex escalation")
	}
}

func TestEngine_EscalatedFromPatchPreventsReentry(t *testing.T) {
	// Once EscalatedFromPatch is set, verify FAIL should return PhaseGateError.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 2,
				OnExhausted: "stop",
			},
		},
		{
			Name:   "patch",
			Type:   "corrective",
			Prompt: "patch.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix"],"criteria_results":[],"command_results":[]}`),
					RawText: "fail",
					CostUSD: 0.05,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	// Pre-set EscalatedFromPatch to simulate post-escalation state.
	state.Meta().EscalatedFromPatch = true

	err := engine.Run(context.Background())
	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if !strings.Contains(gateErr.Reason, "escalated from patch") {
		t.Errorf("gate error reason should mention escalation, got: %q", gateErr.Reason)
	}
}

func TestEngine_PatchEscalationSkippedLowBudget(t *testing.T) {
	// When remaining budget < $5, escalation should be skipped.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 1,
				OnExhausted: "escalate",
				EscalateTo:  "implement",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 7.0, // Leaves only $3 remaining
				},
			}},
			"verify": {
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix1"],"criteria_results":[],"command_results":[]}`),
						RawText: "fail1",
						CostUSD: 0.05,
					},
				},
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix2"],"criteria_results":[],"command_results":[]}`),
						RawText: "fail2",
						CostUSD: 0.05,
					},
				},
			},
			"patch": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":false,"too_complex":false}`),
					RawText: "patched",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostUSD = 10.0 // $10 budget, implement costs $7 → only $3 left
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	err := engine.Run(context.Background())
	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if !strings.Contains(gateErr.Reason, "insufficient budget") {
		t.Errorf("gate error reason should mention budget, got: %q", gateErr.Reason)
	}

	// Should have patch_escalation_skipped event.
	hasSkipped := false
	for _, ev := range events {
		if ev.Kind == EventPatchEscalationSkipped {
			hasSkipped = true
		}
	}
	if !hasSkipped {
		t.Error("patch_escalation_skipped event not emitted")
	}
}

func TestDetectRegression(t *testing.T) {
	tests := []struct {
		name            string
		previous        []string
		current         []string
		wantRegressions []string
	}{
		{
			name:            "no_regressions_same_failures",
			previous:        []string{"criterion A", "criterion B"},
			current:         []string{"criterion A", "criterion B"},
			wantRegressions: nil,
		},
		{
			name:            "progress_fewer_failures",
			previous:        []string{"criterion A", "criterion B", "criterion C"},
			current:         []string{"criterion A"},
			wantRegressions: nil,
		},
		{
			name:            "regression_new_failure",
			previous:        []string{"criterion A"},
			current:         []string{"criterion A", "criterion B"},
			wantRegressions: []string{"criterion B"},
		},
		{
			name:            "regression_different_failure",
			previous:        []string{"criterion A"},
			current:         []string{"criterion B"},
			wantRegressions: []string{"criterion B"},
		},
		{
			name:            "regression_with_progress",
			previous:        []string{"criterion A", "criterion B", "criterion C"},
			current:         []string{"criterion A", "criterion D"},
			wantRegressions: []string{"criterion D"},
		},
		{
			name:            "empty_previous",
			previous:        nil,
			current:         []string{"criterion A"},
			wantRegressions: []string{"criterion A"},
		},
		{
			name:            "empty_current",
			previous:        []string{"criterion A"},
			current:         nil,
			wantRegressions: nil,
		},
		{
			name:            "both_empty",
			previous:        nil,
			current:         nil,
			wantRegressions: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectRegression(tt.previous, tt.current)

			if len(tt.wantRegressions) == 0 && len(result.Regressions) == 0 {
				return // both empty/nil, OK
			}

			if len(result.Regressions) != len(tt.wantRegressions) {
				t.Fatalf("Regressions = %v, want %v", result.Regressions, tt.wantRegressions)
			}
			for idx, got := range result.Regressions {
				if got != tt.wantRegressions[idx] {
					t.Errorf("Regressions[%d] = %q, want %q", idx, got, tt.wantRegressions[idx])
				}
			}
		})
	}
}

func TestEngine_PatchExhaustedRetry(t *testing.T) {
	// When on_exhausted is "retry", the engine should allow one extra patch
	// cycle by resetting PatchCycles. After the retry, if still failing, stop.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 1,
				OnExhausted: "retry",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"verify": {
				// First verify: FAIL → routes to patch (cycle 1).
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix1"],"criteria_results":[{"criterion":"A","passed":false,"evidence":"e"}],"command_results":[]}`),
						RawText: "fail1",
						CostUSD: 0.05,
					},
				},
				// Second verify (after first patch): FAIL → exhausted → retry resets cycles.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix2"],"criteria_results":[{"criterion":"A","passed":false,"evidence":"e"}],"command_results":[]}`),
						RawText: "fail2",
						CostUSD: 0.05,
					},
				},
				// Third verify (after retry patch): FAIL → retry already used → stop.
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix3"],"criteria_results":[{"criterion":"A","passed":false,"evidence":"e"}],"command_results":[]}`),
						RawText: "fail3",
						CostUSD: 0.05,
					},
				},
			},
			"patch": {
				// First patch (cycle 1).
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":false,"too_complex":false}`),
						RawText: "patched1",
						CostUSD: 0.20,
					},
				},
				// Second patch (retry cycle).
				{
					result: &runner.RunResult{
						Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":false,"too_complex":false}`),
						RawText: "patched2",
						CostUSD: 0.20,
					},
				},
			},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	err := engine.Run(context.Background())
	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if !strings.Contains(gateErr.Reason, "patch retry exhausted") {
		t.Errorf("gate error reason should mention retry exhausted, got: %q", gateErr.Reason)
	}

	// PatchRetryUsed should be set.
	if !state.Meta().PatchRetryUsed {
		t.Error("PatchRetryUsed should be true after retry")
	}

	// Should have patch_exhausted event (emitted when first exhausted).
	hasExhausted := false
	for _, ev := range events {
		if ev.Kind == EventPatchExhausted {
			hasExhausted = true
		}
	}
	if !hasExhausted {
		t.Error("patch_exhausted event not emitted")
	}

	// Runner should have been called:
	// implement, verify(fail), patch, verify(fail), patch(retry), verify(fail) = 6 calls
	if len(mock.calls) != 6 {
		t.Errorf("runner called %d times, want 6", len(mock.calls))
	}
}

func TestEngine_PatchRegressionStopsImmediately(t *testing.T) {
	// When verify fails again after patch but a previously-passing criterion
	// now fails (regression), the engine should stop immediately with a
	// PhaseGateError and emit patch_regression.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 3,
				OnExhausted: "stop",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"verify": {
				// First verify: FAIL with criterion A failing.
				{
					result: &runner.RunResult{
						Output: json.RawMessage(`{
							"verdict":"FAIL",
							"fixes_required":["fix A"],
							"criteria_results":[
								{"criterion":"A","passed":false,"evidence":"fails"},
								{"criterion":"B","passed":true,"evidence":"ok"},
								{"criterion":"C","passed":true,"evidence":"ok"}
							],
							"command_results":[]
						}`),
						RawText: "fail1",
						CostUSD: 0.05,
					},
				},
				// Second verify (after patch): A still fails, but B now fails too (regression).
				{
					result: &runner.RunResult{
						Output: json.RawMessage(`{
							"verdict":"FAIL",
							"fixes_required":["fix A","fix B"],
							"criteria_results":[
								{"criterion":"A","passed":false,"evidence":"still fails"},
								{"criterion":"B","passed":false,"evidence":"now fails"},
								{"criterion":"C","passed":true,"evidence":"ok"}
							],
							"command_results":[]
						}`),
						RawText: "fail2",
						CostUSD: 0.05,
					},
				},
			},
			"patch": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[{"fix_index":0,"status":"fixed","description":"attempted fix"}],"files_changed":[],"tests_passed":false,"too_complex":false}`),
					RawText: "patched",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	err := engine.Run(context.Background())
	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if !strings.Contains(gateErr.Reason, "regression") {
		t.Errorf("gate error reason should mention regression, got: %q", gateErr.Reason)
	}
	if !strings.Contains(gateErr.Reason, "B") {
		t.Errorf("gate error reason should mention criterion B, got: %q", gateErr.Reason)
	}

	// Should have patch_regression event.
	hasRegression := false
	for _, ev := range events {
		if ev.Kind == EventPatchRegression {
			hasRegression = true
			// Verify event data contains the regressed criteria.
			prevPassed, _ := ev.Data["previously_passed"].([]string)
			if len(prevPassed) == 0 {
				// Try interface{} slice (JSON roundtrip)
				if prevArr, ok := ev.Data["previously_passed"].([]interface{}); ok {
					for _, item := range prevArr {
						if s, ok := item.(string); ok {
							prevPassed = append(prevPassed, s)
						}
					}
				}
			}
			found := false
			for _, p := range prevPassed {
				if p == "B" {
					found = true
				}
			}
			if !found {
				t.Errorf("patch_regression event should list B in previously_passed, got: %v", ev.Data["previously_passed"])
			}
		}
	}
	if !hasRegression {
		t.Error("patch_regression event not emitted")
	}

	// PatchCycles should be 1 (only one patch ran before regression).
	if state.Meta().PatchCycles != 1 {
		t.Errorf("PatchCycles = %d, want 1", state.Meta().PatchCycles)
	}

	// PreviousFailures should have been set before regression detected.
	// After routing to patch, PreviousFailures = ["A"].
	if len(state.Meta().PreviousFailures) != 1 || state.Meta().PreviousFailures[0] != "A" {
		t.Errorf("PreviousFailures = %v, want [A]", state.Meta().PreviousFailures)
	}

	// Runner should have been called: implement, verify(fail), patch, verify(regression) = 4.
	if len(mock.calls) != 4 {
		t.Errorf("runner called %d times, want 4", len(mock.calls))
	}
}

func TestEngine_PatchNoProgressRetry(t *testing.T) {
	// When verify fails with the same criteria (no progress, no regression),
	// the engine should still route to patch, respecting the cycle limit.
	// With on_exhausted=retry, this allows one extra attempt.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 1,
				OnExhausted: "retry",
			},
		},
	}

	// All verifies fail with the same criterion A.
	verifyFail := func() *runner.RunResult {
		return &runner.RunResult{
			Output: json.RawMessage(`{
				"verdict":"FAIL",
				"fixes_required":["fix A"],
				"criteria_results":[{"criterion":"A","passed":false,"evidence":"still fails"}],
				"command_results":[]
			}`),
			RawText: "fail",
			CostUSD: 0.05,
		}
	}

	patchResult := func() *runner.RunResult {
		return &runner.RunResult{
			Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":false,"too_complex":false}`),
			RawText: "patched",
			CostUSD: 0.20,
		}
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"verify": {
				{result: verifyFail()},
				{result: verifyFail()},
				{result: verifyFail()},
			},
			"patch": {
				{result: patchResult()},
				{result: patchResult()},
			},
		},
	}

	engine, state := setupEngine(t, phases, mock)
	err := engine.Run(context.Background())

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if !strings.Contains(gateErr.Reason, "patch retry exhausted") {
		t.Errorf("gate error should mention retry exhausted, got: %q", gateErr.Reason)
	}

	if !state.Meta().PatchRetryUsed {
		t.Error("PatchRetryUsed should be true")
	}

	// 6 calls: implement, verify, patch, verify, patch(retry), verify
	if len(mock.calls) != 6 {
		t.Errorf("runner called %d times, want 6", len(mock.calls))
	}
}

func TestEngine_PatchExhaustedSkipsExtractFailingCriteria(t *testing.T) {
	// When patch cycles are exhausted, gateVerifyFail should return via the
	// on_exhausted policy without calling extractFailingCriteria. This test
	// verifies that even when the verify result contains criteria data that
	// would trigger regression detection, exhaustion takes precedence and
	// the gate error reflects the exhaustion policy, not regression.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:         "patch",
			Type:         "corrective",
			Prompt:       "patch.md",
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 1,
				OnExhausted: "stop",
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "impl",
					CostUSD: 0.10,
				},
			}},
			"verify": {
				// First verify: FAIL with criterion A failing.
				{
					result: &runner.RunResult{
						Output: json.RawMessage(`{
							"verdict":"FAIL",
							"fixes_required":["fix A"],
							"criteria_results":[
								{"criterion":"A","passed":false,"evidence":"fails"},
								{"criterion":"B","passed":true,"evidence":"ok"}
							],
							"command_results":[]
						}`),
						RawText: "fail1",
						CostUSD: 0.05,
					},
				},
				// Second verify (after patch): A still fails and B now
				// fails too. Without lazy eval this would be a regression;
				// with lazy eval the exhaustion policy fires first.
				{
					result: &runner.RunResult{
						Output: json.RawMessage(`{
							"verdict":"FAIL",
							"fixes_required":["fix A","fix B"],
							"criteria_results":[
								{"criterion":"A","passed":false,"evidence":"still fails"},
								{"criterion":"B","passed":false,"evidence":"now fails"}
							],
							"command_results":[]
						}`),
						RawText: "fail2",
						CostUSD: 0.05,
					},
				},
			},
			"patch": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"ticket_key":"TEST-1","fix_results":[],"files_changed":[],"tests_passed":false,"too_complex":false}`),
					RawText: "patched",
					CostUSD: 0.20,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	err := engine.Run(context.Background())
	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}

	// The error should mention exhaustion, not regression — the max_attempts
	// check fires before extractFailingCriteria is called.
	if !strings.Contains(gateErr.Reason, "patch attempts exhausted") {
		t.Errorf("gate error reason should mention patch attempts exhausted, got: %q", gateErr.Reason)
	}
	if strings.Contains(gateErr.Reason, "regression") {
		t.Errorf("gate error reason should NOT mention regression (lazy eval skips it), got: %q", gateErr.Reason)
	}

	// Should have patch_exhausted event, NOT patch_regression.
	hasExhausted := false
	hasRegression := false
	for _, ev := range events {
		if ev.Kind == EventPatchExhausted {
			hasExhausted = true
		}
		if ev.Kind == EventPatchRegression {
			hasRegression = true
		}
	}
	if !hasExhausted {
		t.Error("patch_exhausted event not emitted")
	}
	if hasRegression {
		t.Error("patch_regression event should NOT be emitted when exhausted (lazy eval)")
	}

	// PatchCycles should be 1 (one patch ran before exhaustion).
	if state.Meta().PatchCycles != 1 {
		t.Errorf("PatchCycles = %d, want 1", state.Meta().PatchCycles)
	}

	// Runner should have been called: implement, verify(fail), patch, verify(exhausted) = 4.
	if len(mock.calls) != 4 {
		t.Errorf("runner called %d times, want 4", len(mock.calls))
	}
}

func TestEngine_PhaseBudgetExceeded(t *testing.T) {
	// Phase costs $10 but per-phase limit is $5 → PhaseBudgetExceededError.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 10.0,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 5.0
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseBudgetExceededError")
	}

	var phaseBudgetErr *PhaseBudgetExceededError
	if !errors.As(err, &phaseBudgetErr) {
		t.Fatalf("expected PhaseBudgetExceededError, got: %v", err)
	}
	if phaseBudgetErr.Phase != "triage" {
		t.Errorf("phase = %q, want %q", phaseBudgetErr.Phase, "triage")
	}
	if phaseBudgetErr.Limit != 5.0 {
		t.Errorf("limit = %f, want 5.0", phaseBudgetErr.Limit)
	}
	if phaseBudgetErr.Actual != 10.0 {
		t.Errorf("actual = %f, want 10.0", phaseBudgetErr.Actual)
	}
}

func TestEngine_PhaseBudgetExceeded_AtLimit(t *testing.T) {
	// Phase costs exactly the limit → PhaseBudgetExceededError (>= check).
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 5.0,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 5.0
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseBudgetExceededError")
	}

	var phaseBudgetErr *PhaseBudgetExceededError
	if !errors.As(err, &phaseBudgetErr) {
		t.Fatalf("expected PhaseBudgetExceededError, got: %v", err)
	}
	if phaseBudgetErr.Phase != "triage" {
		t.Errorf("phase = %q, want %q", phaseBudgetErr.Phase, "triage")
	}
}

func TestEngine_PhaseBudgetUnderLimit(t *testing.T) {
	// Phase costs $4 with per-phase limit $5 → succeeds.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 4.0,
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":[{"id":"1","description":"task"}]}`),
					RawText: "Plan done",
					CostUSD: 3.0,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 5.0
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Both phases should complete and each should be under the per-phase limit.
	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}
	if state.Meta().Phases["triage"].Cost != 4.0 {
		t.Errorf("triage cost = %f, want 4.0", state.Meta().Phases["triage"].Cost)
	}
	if state.Meta().Phases["plan"].Cost != 3.0 {
		t.Errorf("plan cost = %f, want 3.0", state.Meta().Phases["plan"].Cost)
	}
}

func TestEngine_PhaseBudgetWarning(t *testing.T) {
	// Phase costs $4.6 with per-phase limit $5 → warning at 90% ($4.50).
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 4.6,
				},
			}},
		},
	}

	var events []Event
	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 5.0
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	hasWarning := false
	for _, ev := range events {
		if ev.Kind == EventPhaseBudgetWarning {
			hasWarning = true
			if ev.Phase != "triage" {
				t.Errorf("warning phase = %q, want %q", ev.Phase, "triage")
			}
		}
	}
	if !hasWarning {
		t.Error("expected phase_budget_warning event")
	}
}

func TestEngine_PhaseBudgetExceededEmitsEvent(t *testing.T) {
	// Verify that the phase_budget_exceeded event is emitted.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 10.0,
				},
			}},
		},
	}

	var events []Event
	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 5.0
		cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	})

	_ = engine.Run(context.Background())

	hasExceeded := false
	for _, ev := range events {
		if ev.Kind == EventPhaseBudgetExceeded {
			hasExceeded = true
			if ev.Phase != "triage" {
				t.Errorf("exceeded phase = %q, want %q", ev.Phase, "triage")
			}
		}
	}
	if !hasExceeded {
		t.Error("expected phase_budget_exceeded event")
	}
}

func TestEngine_PhaseBudgetCapsRunnerOpts(t *testing.T) {
	// When MaxCostPerPhase < remaining pipeline budget, the runner
	// should receive MaxCostPerPhase as its MaxBudgetUSD.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 2.0,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostUSD = 100.0    // plenty of pipeline budget
		cfg.MaxCostPerPhase = 8.0 // per-phase limit is tighter
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	// Runner should have received the per-phase cap, not the pipeline remaining.
	if mock.calls[0].MaxBudgetUSD != 8.0 {
		t.Errorf("MaxBudgetUSD = %f, want 8.0", mock.calls[0].MaxBudgetUSD)
	}
}

func TestEngine_PhaseBudgetNoCap(t *testing.T) {
	// When MaxCostPerPhase is 0, no per-phase enforcement.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 50.0,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 0 // disabled
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected success (no per-phase cap), got: %v", err)
	}
}

func TestEngine_PhaseBudgetSecondPhaseExceeds(t *testing.T) {
	// First phase is under budget, second phase exceeds per-phase limit.
	// Each phase gets its own cost counter that resets on MarkRunning.
	phases := []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes"}`),
					RawText: "Triage done",
					CostUSD: 3.0, // under $5 per-phase limit
				},
			}},
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":[{"id":"1","description":"task"}]}`),
					RawText: "Plan done",
					CostUSD: 6.0, // over $5 per-phase limit
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 5.0
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseBudgetExceededError")
	}

	var phaseBudgetErr *PhaseBudgetExceededError
	if !errors.As(err, &phaseBudgetErr) {
		t.Fatalf("expected PhaseBudgetExceededError, got: %v", err)
	}
	if phaseBudgetErr.Phase != "plan" {
		t.Errorf("phase = %q, want %q", phaseBudgetErr.Phase, "plan")
	}

	// Triage should have completed successfully.
	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
	// Plan should be marked failed.
	ps := state.Meta().Phases["plan"]
	if ps == nil || ps.Status != PhaseFailed {
		t.Error("plan should be marked failed")
	}
}

func TestEngine_PhaseBudgetCumulativeAcrossRework(t *testing.T) {
	// A phase runs twice via rework. Each generation costs $3, which is
	// under the $5 per-phase limit individually. The cumulative cost ($6)
	// should exceed the limit and trigger PhaseBudgetExceededError on the
	// second generation.
	//
	// Pipeline: implement → verify → review (rework → implement)
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement", "verify"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				// First implement: costs $3 (under $5 per-phase limit).
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 3.0,
				}},
				// Second implement (rework): costs $3.
				// Cumulative = $6, exceeds $5 per-phase limit.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 3.0,
				}},
			},
			"verify": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v1",
					CostUSD: 0.10,
				}},
			},
			"review/go-specialist": {
				// First review: rework verdict → routes back to implement.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"handler.go","line":42,"issue":"nil deref","suggestion":"add nil check"}]}`),
					RawText: "Critical issue found",
					CostUSD: 0.20,
				}},
			},
		},
	}

	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 5.0
		cfg.MaxReworkCycles = 2
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseBudgetExceededError, got nil")
	}

	var phaseBudgetErr *PhaseBudgetExceededError
	if !errors.As(err, &phaseBudgetErr) {
		t.Fatalf("expected PhaseBudgetExceededError, got: %v", err)
	}
	if phaseBudgetErr.Phase != "implement" {
		t.Errorf("phase = %q, want %q", phaseBudgetErr.Phase, "implement")
	}
	if phaseBudgetErr.Limit != 5.0 {
		t.Errorf("limit = %f, want 5.0", phaseBudgetErr.Limit)
	}
	if phaseBudgetErr.Actual != 6.0 {
		t.Errorf("actual = %f, want 6.0", phaseBudgetErr.Actual)
	}

	// CumulativeCost should reflect the total across both generations.
	implState := state.Meta().Phases["implement"]
	if implState == nil {
		t.Fatal("implement phase state is nil")
	}
	if implState.CumulativeCost != 6.0 {
		t.Errorf("CumulativeCost = %f, want 6.0", implState.CumulativeCost)
	}
}

func TestEngine_PhaseBudgetCumulativePreRunCheck(t *testing.T) {
	// A phase runs via rework. First generation costs $4.50 (under $5 limit).
	// On the second generation, the pre-run check should detect that cumulative
	// cost already meets the limit (when exactly at the limit) and prevent
	// starting the phase, avoiding any token spend.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement", "verify"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				// First implement: costs exactly $5 (meets $5 per-phase limit).
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 5.0,
				}},
				// Second implement should never run — pre-run check blocks it.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 1.0,
				}},
			},
			"verify": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v1",
					CostUSD: 0.10,
				}},
			},
			"review/go-specialist": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"handler.go","line":42,"issue":"nil deref","suggestion":"add nil check"}]}`),
					RawText: "Critical issue found",
					CostUSD: 0.20,
				}},
			},
		},
	}

	engine, state := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 5.0
		cfg.MaxReworkCycles = 2
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseBudgetExceededError, got nil")
	}

	var phaseBudgetErr *PhaseBudgetExceededError
	if !errors.As(err, &phaseBudgetErr) {
		t.Fatalf("expected PhaseBudgetExceededError, got: %v", err)
	}
	if phaseBudgetErr.Phase != "implement" {
		t.Errorf("phase = %q, want %q", phaseBudgetErr.Phase, "implement")
	}

	// The first implement run should be the only one — the post-run check
	// catches it at exactly $5 == limit (>= check).
	implState := state.Meta().Phases["implement"]
	if implState == nil {
		t.Fatal("implement phase state is nil")
	}
	if implState.CumulativeCost != 5.0 {
		t.Errorf("CumulativeCost = %f, want 5.0", implState.CumulativeCost)
	}

	// The implement runner should only have been called once —
	// either the post-run check catches $5 == limit on the first gen,
	// or the pre-run check blocks the second gen.
	implCalls := 0
	for _, call := range mock.calls {
		if call.Phase == "implement" {
			implCalls++
		}
	}
	if implCalls != 1 {
		t.Errorf("implement runner called %d times, want 1", implCalls)
	}
}

func TestEngine_PhaseBudgetRunnerCapSubtractsCumulativeCost(t *testing.T) {
	// When a phase re-runs via rework, the runner's MaxBudgetUSD should
	// reflect the remaining per-phase budget (MaxCostPerPhase - CumulativeCost),
	// not the full MaxCostPerPhase.
	//
	// Pipeline: implement → verify → review (rework → implement)
	// MaxCostPerPhase = 10, first implement costs $3 → second implement
	// runner should see MaxBudgetUSD = 7 (10 - 3).
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "review",
			Type:      "parallel-review",
			DependsOn: []string{"implement", "verify"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			Rework:    &ReworkConfig{Target: "implement"},
			Reviewers: []ReviewerConfig{
				{Name: "go-specialist", Prompt: "prompts/review-go.md", Focus: "Go idioms"},
			},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {
				// First implement: costs $3.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 3.0,
				}},
				// Second implement (rework): costs $2.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2",
					CostUSD: 2.0,
				}},
			},
			"verify": {
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v1",
					CostUSD: 0.10,
				}},
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v2",
					CostUSD: 0.10,
				}},
			},
			"review/go-specialist": {
				// First review: rework.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"critical","file":"handler.go","line":42,"issue":"nil deref","suggestion":"add nil check"}]}`),
					RawText: "Critical issue found",
					CostUSD: 0.20,
				}},
				// Second review: pass.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[]}`),
					RawText: "No issues",
					CostUSD: 0.15,
				}},
			},
		},
	}

	engine, _ := setupReviewEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.MaxCostPerPhase = 10.0
		cfg.MaxReworkCycles = 2
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Find the second implement call and verify its MaxBudgetUSD.
	implCalls := 0
	for _, call := range mock.calls {
		if call.Phase == "implement" {
			implCalls++
			if implCalls == 2 {
				// Second call should have remaining = 10 - 3 = 7.
				if call.MaxBudgetUSD != 7.0 {
					t.Errorf("second implement MaxBudgetUSD = %f, want 7.0", call.MaxBudgetUSD)
				}
			}
		}
	}
	if implCalls != 2 {
		t.Errorf("implement called %d times, want 2", implCalls)
	}
}

func TestEngine_ImplementNoChanges_ReworkShortCircuit(t *testing.T) {
	// When implement re-runs during a rework cycle and produces zero commits
	// and zero files_changed, the gate should return a PhaseGateError.
	phases := []PhaseConfig{
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
			FeedbackFrom: []string{"review", "verify"},
		},
	}

	// First run: implement produces a normal result with commits.
	mock1 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{
						"tests_passed": true,
						"ticket_key": "TEST-1",
						"branch": "soda/TEST-1",
						"commits": [{"hash": "abc123", "message": "feat: initial impl", "task_id": "T1"}],
						"files_changed": [{"path": "handler.go", "action": "modified"}],
						"task_results": [{"task_id": "T1", "status": "completed"}]
					}`),
					RawText: "Implementation v1",
					CostUSD: 0.50,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock1, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	// First run succeeds.
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run: simulate rework — implement produces no changes (short-circuit).
	mock2 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{
						"tests_passed": true,
						"ticket_key": "TEST-1",
						"branch": "soda/TEST-1",
						"commits": [],
						"files_changed": [],
						"task_results": [{"task_id": "T1", "status": "skipped", "reason": "already implemented"}]
					}`),
					RawText: "No changes needed",
					CostUSD: 0.10,
				},
			}},
		},
	}

	events = nil
	engine2 := NewEngine(mock2, state, engine.config)

	err := engine2.Resume(context.Background(), "implement")
	if err == nil {
		t.Fatal("expected PhaseGateError for rework short-circuit")
	}

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %v", err)
	}
	if gateErr.Phase != "implement" {
		t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "implement")
	}
	if !strings.Contains(gateErr.Reason, "short-circuited") {
		t.Errorf("gate error reason should mention short-circuit, got: %q", gateErr.Reason)
	}

	// Verify the implement_no_changes event was emitted.
	hasNoChangesEvent := false
	for _, e := range events {
		if e.Kind == EventImplementNoChanges {
			hasNoChangesEvent = true
			if e.Phase != "implement" {
				t.Errorf("no_changes event phase = %q, want %q", e.Phase, "implement")
			}
			if gen, ok := e.Data["generation"].(int); ok && gen <= 1 {
				t.Errorf("no_changes event generation = %d, want > 1", gen)
			}
			if skipped, ok := e.Data["skipped_tasks"].(int); ok && skipped != 1 {
				t.Errorf("no_changes event skipped_tasks = %d, want 1", skipped)
			}
		}
	}
	if !hasNoChangesEvent {
		t.Error("implement_no_changes event not emitted")
	}
}

func TestEngine_ImplementNoChanges_FirstRunAllowed(t *testing.T) {
	// First-run implement with no changes should NOT trigger the short-circuit
	// gate — the guard only activates when generation > 1 (rework cycles).
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{
						"tests_passed": true,
						"ticket_key": "TEST-1",
						"branch": "soda/TEST-1",
						"commits": [],
						"files_changed": [],
						"task_results": []
					}`),
					RawText: "Nothing to change",
					CostUSD: 0.10,
				},
			}},
		},
	}

	engine, _ := setupEngine(t, phases, mock)

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("first-run implement with no changes should succeed, got: %v", err)
	}
}

func TestEngine_ImplementWithChanges_ReworkAllowed(t *testing.T) {
	// When implement re-runs during a rework cycle and produces actual changes,
	// the gate should NOT block — this is the normal rework flow.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	// First run: normal result.
	mock1 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{
						"tests_passed": true,
						"ticket_key": "TEST-1",
						"branch": "soda/TEST-1",
						"commits": [{"hash": "abc123", "message": "feat: initial", "task_id": "T1"}],
						"files_changed": [{"path": "a.go", "action": "modified"}],
						"task_results": [{"task_id": "T1", "status": "completed"}]
					}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock1)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run: rework with actual changes.
	mock2 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{
						"tests_passed": true,
						"ticket_key": "TEST-1",
						"branch": "soda/TEST-1",
						"commits": [{"hash": "def456", "message": "fix: address feedback", "task_id": "T1"}],
						"files_changed": [{"path": "a.go", "action": "modified"}],
						"task_results": [{"task_id": "T1", "status": "completed"}]
					}`),
					RawText: "Impl v2 with fixes",
					CostUSD: 0.60,
				},
			}},
		},
	}

	engine2 := NewEngine(mock2, state, engine.config)

	if err := engine2.Resume(context.Background(), "implement"); err != nil {
		t.Fatalf("rework with changes should succeed, got: %v", err)
	}
}

func TestEngine_ImplementNoChanges_CommitsOnlyAllowed(t *testing.T) {
	// When implement re-runs during rework and has commits but no files_changed
	// (edge case — shouldn't normally happen but let's be safe), the gate
	// should NOT block because commits indicate actual work.
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}

	mock1 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{
						"tests_passed": true,
						"ticket_key": "TEST-1",
						"branch": "soda/TEST-1",
						"commits": [{"hash": "abc", "message": "first", "task_id": "T1"}],
						"files_changed": [{"path": "a.go", "action": "modified"}],
						"task_results": []
					}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock1)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run: has commits but no files_changed.
	mock2 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output: json.RawMessage(`{
						"tests_passed": true,
						"ticket_key": "TEST-1",
						"branch": "soda/TEST-1",
						"commits": [{"hash": "def", "message": "fix: address rework", "task_id": "T1"}],
						"files_changed": [],
						"task_results": []
					}`),
					RawText: "Impl v2",
					CostUSD: 0.30,
				},
			}},
		},
	}

	engine2 := NewEngine(mock2, state, engine.config)

	if err := engine2.Resume(context.Background(), "implement"); err != nil {
		t.Fatalf("rework with commits-only should succeed, got: %v", err)
	}
}
