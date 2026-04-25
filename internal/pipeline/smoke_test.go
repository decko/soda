package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
)

// smokePipelinePhases returns a 7-phase pipeline config with rework and
// corrective routing wired in:
//
//	triage → plan → implement → patch (corrective) → verify → review → submit
//
// In the happy path, patch is corrective-skipped. Review has a rework config
// targeting implement, and verify has a corrective config targeting patch.
func smokePipelinePhases() []PhaseConfig {
	return []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Schema: `{"type":"object","properties":{"automatable":{"type":"boolean"}},"required":["automatable"]}`,
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			Schema:    `{"type":"object","properties":{"tasks":{"type":"array"}},"required":["tasks"]}`,
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:         "implement",
			Prompt:       "implement.md",
			Schema:       `{"type":"object","properties":{"tests_passed":{"type":"boolean"}},"required":["tests_passed"]}`,
			DependsOn:    []string{"plan"},
			FeedbackFrom: []string{"review", "verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:         "patch",
			Prompt:       "patch.md",
			Type:         "corrective",
			Schema:       `{"type":"object","properties":{"patched":{"type":"boolean"}},"required":["patched"]}`,
			DependsOn:    []string{"implement"},
			FeedbackFrom: []string{"verify"},
			Retry:        RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			Schema:    `{"type":"object","properties":{"verdict":{"type":"string"}},"required":["verdict"]}`,
			DependsOn: []string{"plan", "implement"},
			Corrective: &CorrectiveConfig{
				Phase:       "patch",
				MaxAttempts: 2,
				OnExhausted: "stop",
			},
			Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:      "review",
			Prompt:    "review.md",
			Schema:    `{"type":"object","properties":{"findings":{"type":"array"},"verdict":{"type":"string"}},"required":["findings","verdict"]}`,
			DependsOn: []string{"plan", "implement", "verify"},
			Rework:    &ReworkConfig{Target: "implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:      "submit",
			Prompt:    "submit.md",
			Schema:    `{"type":"object","properties":{"pr_url":{"type":"string"}},"required":["pr_url"]}`,
			DependsOn: []string{"implement", "verify", "review"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
	}
}

// smokeFixtures returns happy-path mock results for the smoke pipeline.
// Patch is omitted because it is corrective-skipped in the happy path.
func smokeFixtures() map[string]*runner.RunResult {
	return map[string]*runner.RunResult{
		"triage": {
			Output:  json.RawMessage(`{"automatable":true}`),
			RawText: "Triage: automatable",
			CostUSD: 0.01,
		},
		"plan": {
			Output:  json.RawMessage(`{"tasks":[{"id":"T1","description":"do the thing"}]}`),
			RawText: "Plan: 1 task",
			CostUSD: 0.02,
		},
		"implement": {
			Output:  json.RawMessage(`{"tests_passed":true,"commits":[{"hash":"abc"}],"files_changed":[{"path":"main.go","action":"modified"}],"task_results":[{"task_id":"T1","status":"completed"}]}`),
			RawText: "Implemented T1",
			CostUSD: 0.50,
		},
		"verify": {
			Output:  json.RawMessage(`{"verdict":"PASS"}`),
			RawText: "All checks pass",
			CostUSD: 0.10,
		},
		"review": {
			Output:  json.RawMessage(`{"findings":[],"verdict":"pass"}`),
			RawText: "Review: no issues",
			CostUSD: 0.05,
		},
		"submit": {
			Output:  json.RawMessage(`{"pr_url":"https://github.com/test/repo/pull/1"}`),
			RawText: "PR #1 created",
			CostUSD: 0.03,
		},
	}
}

// writeSmokePrompts creates minimal prompt templates for the smoke pipeline.
func writeSmokePrompts(t *testing.T, dir string) {
	t.Helper()

	templates := map[string]string{
		"triage.md":    "Triage {{.Ticket.Key}}\n",
		"plan.md":      "Plan {{.Ticket.Key}}\nTriage: {{.Artifacts.Triage}}\n",
		"implement.md": "Implement {{.Ticket.Key}}\nPlan: {{.Artifacts.Plan}}\n{{- if .ReworkFeedback}}\nRework: {{.ReworkFeedback.Verdict}}\n{{end}}",
		"patch.md":     "Patch {{.Ticket.Key}}\n{{- if .ReworkFeedback}}\nFeedback: {{.ReworkFeedback.Verdict}}\n{{end}}",
		"verify.md":    "Verify {{.Ticket.Key}}\nImplement: {{.Artifacts.Implement}}\n",
		"review.md":    "Review {{.Ticket.Key}}\nImplement: {{.Artifacts.Implement}}\n",
		"submit.md":    "Submit {{.Ticket.Key}}\n",
	}

	for name, content := range templates {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write prompt %s: %v", name, err)
		}
	}
}

// TestSmoke_HappyPath runs the 7-phase smoke pipeline end-to-end with all
// phases succeeding. Patch is corrective-skipped, review passes without
// rework, and verify passes without corrective routing.
func TestSmoke_HappyPath(t *testing.T) {
	phases := smokePipelinePhases()
	fixtures := smokeFixtures()

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "SMOKE-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "SMOKE-1", Summary: "Smoke happy path"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All non-corrective phases completed ---
	for _, name := range []string{"triage", "plan", "implement", "verify", "review", "submit"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// Patch is corrective-skipped — not completed.
	if state.IsCompleted("patch") {
		t.Error("patch should NOT be completed (corrective-skipped)")
	}

	// --- Cost accumulation ---
	// triage(0.01) + plan(0.02) + implement(0.50) + verify(0.10) + review(0.05) + submit(0.03) = 0.71
	expectedCost := 0.01 + 0.02 + 0.50 + 0.10 + 0.05 + 0.03
	if !approxEqual(state.Meta().TotalCost, expectedCost) {
		t.Errorf("TotalCost = %v, want %v", state.Meta().TotalCost, expectedCost)
	}

	// --- Runner called 6 times (patch skipped) ---
	if len(mock.calls) != 6 {
		t.Errorf("runner called %d times, want 6; phases: %v",
			len(mock.calls), phaseNames(mock.calls))
	}

	// --- Phase ordering ---
	wantOrder := []string{"triage", "plan", "implement", "verify", "review", "submit"}
	gotOrder := phaseNames(mock.calls)
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("call count mismatch: got %v, want %v", gotOrder, wantOrder)
	}
	for i, want := range wantOrder {
		if gotOrder[i] != want {
			t.Errorf("runner call[%d] = %q, want %q", i, gotOrder[i], want)
		}
	}

	// --- Artifact flow: plan prompt contains triage RawText ---
	planPrompt := mock.calls[1].SystemPrompt
	if planPrompt == "" {
		t.Error("plan SystemPrompt should not be empty")
	}

	// --- Verify events ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventEngineStarted] != 1 {
		t.Errorf("engine_started events = %d, want 1", eventKinds[EventEngineStarted])
	}
	if eventKinds[EventEngineCompleted] != 1 {
		t.Errorf("engine_completed events = %d, want 1", eventKinds[EventEngineCompleted])
	}
	// 6 phases started (patch is corrective-skipped, no phase_started for it)
	if eventKinds[EventPhaseStarted] != 6 {
		t.Errorf("phase_started events = %d, want 6", eventKinds[EventPhaseStarted])
	}
	if eventKinds[EventPhaseCompleted] != 6 {
		t.Errorf("phase_completed events = %d, want 6", eventKinds[EventPhaseCompleted])
	}
	if eventKinds[EventCorrectiveSkipped] != 1 {
		t.Errorf("corrective_skipped events = %d, want 1", eventKinds[EventCorrectiveSkipped])
	}

	// --- No rework or patch cycles ---
	if state.Meta().ReworkCycles != 0 {
		t.Errorf("ReworkCycles = %d, want 0", state.Meta().ReworkCycles)
	}
	if state.Meta().PatchCycles != 0 {
		t.Errorf("PatchCycles = %d, want 0", state.Meta().PatchCycles)
	}

	// --- events.jsonl written ---
	eventsPath := filepath.Join(state.Dir(), "events.jsonl")
	eventsData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if len(eventsData) == 0 {
		t.Error("events.jsonl should not be empty")
	}
}

// TestSmoke_ReworkLoop tests the review→implement rework cycle:
// review returns "rework" → implement gen 2 → verify gen 2 → review gen 2
// passes → submit. Uses flexMockRunner's multi-response capability to return
// different results on the same phase's 2nd call.
func TestSmoke_ReworkLoop(t *testing.T) {
	phases := smokePipelinePhases()

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{result: smokeFixtures()["triage"]}},
			"plan":   {{result: smokeFixtures()["plan"]}},
			"implement": {
				// Gen 1: initial implementation.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":[{"hash":"aaa"}],"files_changed":[{"path":"main.go","action":"modified"}],"task_results":[{"task_id":"T1","status":"completed"}]}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				}},
				// Gen 2: rework with fixes.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":[{"hash":"bbb"}],"files_changed":[{"path":"main.go","action":"modified"}],"task_results":[{"task_id":"T1","status":"completed"}]}`),
					RawText: "Impl v2 with error handling",
					CostUSD: 0.60,
				}},
			},
			"verify": {
				// Gen 1: passes after initial implement.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v1 pass",
					CostUSD: 0.10,
				}},
				// Gen 2: passes after rework.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify v2 pass",
					CostUSD: 0.12,
				}},
			},
			"review": {
				// Gen 1: rework verdict with a major finding.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[{"severity":"major","issue":"missing error handling","file":"main.go","line":42,"suggestion":"add error check"}],"verdict":"rework"}`),
					RawText: "Review v1: rework needed",
					CostUSD: 0.05,
				}},
				// Gen 2: pass after rework.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"findings":[],"verdict":"pass"}`),
					RawText: "Review v2: pass",
					CostUSD: 0.06,
				}},
			},
			"submit": {{result: smokeFixtures()["submit"]}},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "REWORK-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "REWORK-1", Summary: "Rework loop test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 10.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All phases completed ---
	for _, name := range []string{"triage", "plan", "implement", "verify", "review", "submit"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// --- ReworkCycles incremented ---
	if state.Meta().ReworkCycles != 1 {
		t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
	}

	// --- Generation increments ---
	implPS := state.Meta().Phases["implement"]
	if implPS == nil {
		t.Fatal("implement phase state missing")
	}
	if implPS.Generation != 2 {
		t.Errorf("implement generation = %d, want 2", implPS.Generation)
	}

	verifyPS := state.Meta().Phases["verify"]
	if verifyPS == nil {
		t.Fatal("verify phase state missing")
	}
	if verifyPS.Generation != 2 {
		t.Errorf("verify generation = %d, want 2", verifyPS.Generation)
	}

	reviewPS := state.Meta().Phases["review"]
	if reviewPS == nil {
		t.Fatal("review phase state missing")
	}
	if reviewPS.Generation != 2 {
		t.Errorf("review generation = %d, want 2", reviewPS.Generation)
	}

	// --- Cost accumulation across both generations ---
	// triage(0.01) + plan(0.02) + impl1(0.50) + verify1(0.10) + review1(0.05)
	// + impl2(0.60) + verify2(0.12) + review2(0.06) + submit(0.03) = 1.49
	expectedCost := 0.01 + 0.02 + 0.50 + 0.10 + 0.05 + 0.60 + 0.12 + 0.06 + 0.03
	if !approxEqual(state.Meta().TotalCost, expectedCost) {
		t.Errorf("TotalCost = %v, want %v", state.Meta().TotalCost, expectedCost)
	}

	// --- Runner call count: 6 initial + 3 rework (impl + verify + review) + 1 submit = 9 ---
	// triage, plan, implement, verify, review, implement, verify, review, submit = 9
	if len(mock.calls) != 9 {
		t.Errorf("runner called %d times, want 9; phases: %v",
			len(mock.calls), phaseNames(mock.calls))
	}

	// --- Verify rework_routed event ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventReworkRouted] != 1 {
		t.Errorf("rework_routed events = %d, want 1", eventKinds[EventReworkRouted])
	}

	// --- Submit result is correct ---
	submitResult, err := state.ReadResult("submit")
	if err != nil {
		t.Fatalf("ReadResult(submit): %v", err)
	}
	var submitData struct {
		PRURL string `json:"pr_url"`
	}
	if err := json.Unmarshal(submitResult, &submitData); err != nil {
		t.Fatalf("unmarshal submit: %v", err)
	}
	if submitData.PRURL != "https://github.com/test/repo/pull/1" {
		t.Errorf("submit pr_url = %q, want %q", submitData.PRURL, "https://github.com/test/repo/pull/1")
	}
}

// TestSmoke_CorrectiveLoop tests the verify FAIL → patch → verify gen 2 PASS
// corrective cycle via the CorrectiveConfig on the verify phase.
func TestSmoke_CorrectiveLoop(t *testing.T) {
	phases := smokePipelinePhases()

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{result: smokeFixtures()["triage"]}},
			"plan":   {{result: smokeFixtures()["plan"]}},
			"implement": {{result: &runner.RunResult{
				Output:  json.RawMessage(`{"tests_passed":true,"commits":[{"hash":"aaa"}],"files_changed":[{"path":"main.go","action":"modified"}],"task_results":[{"task_id":"T1","status":"completed"}]}`),
				RawText: "Implemented T1",
				CostUSD: 0.50,
			}}},
			"verify": {
				// Gen 1: FAIL triggers corrective routing to patch.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix broken test"]}`),
					RawText: "Verify FAIL: broken test",
					CostUSD: 0.10,
				}},
				// Gen 2: PASS after patch.
				{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "Verify PASS after patch",
					CostUSD: 0.12,
				}},
			},
			"patch": {{result: &runner.RunResult{
				Output:  json.RawMessage(`{"patched":true}`),
				RawText: "Patch applied",
				CostUSD: 0.20,
			}}},
			"review": {{result: &runner.RunResult{
				Output:  json.RawMessage(`{"findings":[],"verdict":"pass"}`),
				RawText: "Review: pass",
				CostUSD: 0.05,
			}}},
			"submit": {{result: smokeFixtures()["submit"]}},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "CORRECTIVE-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "CORRECTIVE-1", Summary: "Corrective loop test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 10.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- All phases completed ---
	for _, name := range []string{"triage", "plan", "implement", "patch", "verify", "review", "submit"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// --- PatchCycles incremented ---
	if state.Meta().PatchCycles != 1 {
		t.Errorf("PatchCycles = %d, want 1", state.Meta().PatchCycles)
	}

	// --- ReworkCycles unchanged ---
	if state.Meta().ReworkCycles != 0 {
		t.Errorf("ReworkCycles = %d, want 0", state.Meta().ReworkCycles)
	}

	// --- Verify generation increments ---
	verifyPS := state.Meta().Phases["verify"]
	if verifyPS == nil {
		t.Fatal("verify phase state missing")
	}
	if verifyPS.Generation != 2 {
		t.Errorf("verify generation = %d, want 2", verifyPS.Generation)
	}

	patchPS := state.Meta().Phases["patch"]
	if patchPS == nil {
		t.Fatal("patch phase state missing")
	}
	if patchPS.Generation != 1 {
		t.Errorf("patch generation = %d, want 1", patchPS.Generation)
	}

	// --- Cost accumulation ---
	// triage(0.01) + plan(0.02) + implement(0.50) + verify1(0.10) +
	// patch(0.20) + verify2(0.12) + review(0.05) + submit(0.03) = 1.03
	expectedCost := 0.01 + 0.02 + 0.50 + 0.10 + 0.20 + 0.12 + 0.05 + 0.03
	if !approxEqual(state.Meta().TotalCost, expectedCost) {
		t.Errorf("TotalCost = %v, want %v", state.Meta().TotalCost, expectedCost)
	}

	// --- Runner call count ---
	// triage, plan, implement, verify(FAIL), patch, verify(PASS), review, submit = 8
	if len(mock.calls) != 8 {
		t.Errorf("runner called %d times, want 8; phases: %v",
			len(mock.calls), phaseNames(mock.calls))
	}

	// --- Phase ordering in runner calls ---
	wantOrder := []string{"triage", "plan", "implement", "verify", "patch", "verify", "review", "submit"}
	gotOrder := phaseNames(mock.calls)
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("call count mismatch: got %v, want %v", gotOrder, wantOrder)
	}
	for i, want := range wantOrder {
		if gotOrder[i] != want {
			t.Errorf("runner call[%d] = %q, want %q", i, gotOrder[i], want)
		}
	}

	// --- Verify rework_routed event ---
	eventKinds := make(map[string]int)
	for _, e := range events {
		eventKinds[e.Kind]++
	}
	if eventKinds[EventReworkRouted] != 1 {
		t.Errorf("rework_routed events = %d, want 1", eventKinds[EventReworkRouted])
	}
	// Corrective skip should NOT appear because patch was actually run.
	// The initial forward pass skips it (1 event), but after corrective
	// routing it runs. So we expect exactly 1 corrective_skipped event.
	if eventKinds[EventCorrectiveSkipped] != 1 {
		t.Errorf("corrective_skipped events = %d, want 1", eventKinds[EventCorrectiveSkipped])
	}
}

// TestSmoke_Resume tests the resume workflow: first run fails at the verify
// gate (no corrective config), second run uses Resume("verify"), validating
// that earlier phases are skipped, generations increment, and cost
// accumulates across both runs.
func TestSmoke_Resume(t *testing.T) {
	// Use smoke phases but remove corrective config from verify so that
	// verify FAIL produces a PhaseGateError instead of routing to patch.
	phases := smokePipelinePhases()
	for i := range phases {
		if phases[i].Name == "verify" {
			phases[i].Corrective = nil
		}
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "RESUME-S1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// --- First run: triage → plan → implement → verify FAIL → gate error ---
	mock1 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage":    {{result: smokeFixtures()["triage"]}},
			"plan":      {{result: smokeFixtures()["plan"]}},
			"implement": {{result: smokeFixtures()["implement"]}},
			"verify": {{result: &runner.RunResult{
				Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["missing edge case"]}`),
				RawText: "Verify FAIL",
				CostUSD: 0.15,
			}}},
		},
	}

	var events1 []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "RESUME-S1", Summary: "Resume smoke test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 10.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent:    func(e Event) { events1 = append(events1, e) },
	}

	engine1 := NewEngine(mock1, state, cfg)

	runErr := engine1.Run(context.Background())
	if runErr == nil {
		t.Fatal("expected PhaseGateError from verify FAIL")
	}

	// Verify the error is a PhaseGateError.
	var gateErr *PhaseGateError
	if !errors.As(runErr, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %T: %v", runErr, runErr)
	}

	// Implement and verify should be completed after first run.
	if !state.IsCompleted("implement") {
		t.Error("implement should be completed after first run")
	}
	if !state.IsCompleted("verify") {
		t.Error("verify should be completed (gate check happens after completion)")
	}

	costAfterFirstRun := state.Meta().TotalCost

	// --- Second run: Resume("verify") → verify PASS → review → submit ---
	mock2 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"verify": {{result: &runner.RunResult{
				Output:  json.RawMessage(`{"verdict":"PASS"}`),
				RawText: "Verify PASS on resume",
				CostUSD: 0.12,
			}}},
			"review": {{result: &runner.RunResult{
				Output:  json.RawMessage(`{"findings":[],"verdict":"pass"}`),
				RawText: "Review pass",
				CostUSD: 0.05,
			}}},
			"submit": {{result: smokeFixtures()["submit"]}},
		},
	}

	var events2 []Event
	cfg.OnEvent = func(e Event) { events2 = append(events2, e) }

	engine2 := NewEngine(mock2, state, cfg)

	if err := engine2.Resume(context.Background(), "verify"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// --- All phases completed ---
	for _, name := range []string{"triage", "plan", "implement", "verify", "review", "submit"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed after resume", name)
		}
	}

	// --- Verify generation incremented ---
	verifyPS := state.Meta().Phases["verify"]
	if verifyPS == nil {
		t.Fatal("verify phase state missing")
	}
	if verifyPS.Generation < 2 {
		t.Errorf("verify generation = %d, want >= 2", verifyPS.Generation)
	}

	// --- Cost accumulated across runs ---
	expectedAdditional := 0.12 + 0.05 + 0.03 // verify v2 + review + submit
	expectedTotal := costAfterFirstRun + expectedAdditional
	if !approxEqual(state.Meta().TotalCost, expectedTotal) {
		t.Errorf("TotalCost = %v, want %v", state.Meta().TotalCost, expectedTotal)
	}

	// --- Runner call count on second run ---
	// Only verify, review, submit should run (triage, plan, implement skipped).
	if len(mock2.calls) != 3 {
		t.Errorf("second run: runner called %d times, want 3; phases: %v",
			len(mock2.calls), phaseNames(mock2.calls))
	}

	// --- Verify runner call ordering on resume ---
	wantOrder := []string{"verify", "review", "submit"}
	gotOrder := phaseNames(mock2.calls)
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("call count mismatch: got %v, want %v", gotOrder, wantOrder)
	}
	for i, want := range wantOrder {
		if gotOrder[i] != want {
			t.Errorf("resume call[%d] = %q, want %q", i, gotOrder[i], want)
		}
	}

	// --- Engine completed event on resume ---
	hasCompleted := false
	for _, e := range events2 {
		if e.Kind == EventEngineCompleted {
			hasCompleted = true
		}
	}
	if !hasCompleted {
		t.Error("engine_completed event not emitted on resume")
	}
}

// TestSmoke_EventsOrdering verifies events.jsonl completeness and ordering
// after a happy-path run: monotonic timestamps, matching started/completed
// pairs, and no gaps in the lifecycle.
func TestSmoke_EventsOrdering(t *testing.T) {
	phases := smokePipelinePhases()
	fixtures := smokeFixtures()

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "EVENTS-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "EVENTS-1", Summary: "Events ordering test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Read events.jsonl.
	events, err := ReadEvents(state.Dir())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("events.jsonl should not be empty")
	}

	// --- Monotonic timestamps ---
	for i := 1; i < len(events); i++ {
		if events[i].Timestamp.Before(events[i-1].Timestamp) {
			t.Errorf("events[%d].Timestamp (%v) before events[%d].Timestamp (%v)",
				i, events[i].Timestamp, i-1, events[i-1].Timestamp)
		}
	}

	// --- Matching started/completed pairs ---
	// Track phase_started and phase_completed events by phase name.
	startedPhases := make(map[string]int)
	completedPhases := make(map[string]int)
	for _, e := range events {
		switch e.Kind {
		case EventPhaseStarted:
			startedPhases[e.Phase]++
		case EventPhaseCompleted:
			completedPhases[e.Phase]++
		}
	}

	// Every completed phase should have a matching started event.
	for phase, count := range completedPhases {
		if startedPhases[phase] != count {
			t.Errorf("phase %q: %d started, %d completed — mismatch",
				phase, startedPhases[phase], count)
		}
	}

	// --- Engine lifecycle: exactly one started and one completed ---
	engineStarted := 0
	engineCompleted := 0
	for _, e := range events {
		switch e.Kind {
		case EventEngineStarted:
			engineStarted++
		case EventEngineCompleted:
			engineCompleted++
		}
	}
	if engineStarted != 1 {
		t.Errorf("engine_started events = %d, want 1", engineStarted)
	}
	if engineCompleted != 1 {
		t.Errorf("engine_completed events = %d, want 1", engineCompleted)
	}

	// --- Engine started is first lifecycle event, completed is last ---
	firstLifecycle := ""
	lastLifecycle := ""
	for _, e := range events {
		switch e.Kind {
		case EventEngineStarted, EventEngineCompleted, EventPhaseStarted, EventPhaseCompleted:
			if firstLifecycle == "" {
				firstLifecycle = e.Kind
			}
			lastLifecycle = e.Kind
		}
	}
	if firstLifecycle != EventEngineStarted {
		t.Errorf("first lifecycle event = %q, want %q", firstLifecycle, EventEngineStarted)
	}
	if lastLifecycle != EventEngineCompleted {
		t.Errorf("last lifecycle event = %q, want %q", lastLifecycle, EventEngineCompleted)
	}

	// --- No phase_failed events in happy path ---
	for _, e := range events {
		if e.Kind == EventPhaseFailed {
			t.Errorf("unexpected phase_failed event for phase %q", e.Phase)
		}
	}
}

// TestSmoke_StateFileValidity verifies that the state files written during a
// happy-path run are valid: meta.json is well-formed JSON with expected
// fields, result files (<phase>.json) contain valid JSON, and artifact files
// (<phase>.md) are non-empty.
func TestSmoke_StateFileValidity(t *testing.T) {
	phases := smokePipelinePhases()
	fixtures := smokeFixtures()

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeSmokePrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "STATE-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "STATE-1", Summary: "State file validity test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- meta.json is valid JSON with expected structure ---
	metaPath := filepath.Join(state.Dir(), "meta.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}

	var meta PipelineMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("unmarshal meta.json: %v", err)
	}

	if meta.Ticket != "STATE-1" {
		t.Errorf("meta.Ticket = %q, want %q", meta.Ticket, "STATE-1")
	}
	if meta.Summary != "State file validity test" {
		t.Errorf("meta.Summary = %q, want %q", meta.Summary, "State file validity test")
	}
	if meta.TotalCost <= 0 {
		t.Error("meta.TotalCost should be positive")
	}
	if meta.StartedAt.IsZero() {
		t.Error("meta.StartedAt should not be zero")
	}
	if len(meta.Phases) == 0 {
		t.Error("meta.Phases should not be empty")
	}

	// Each completed phase should have status "completed" and generation >= 1.
	for _, name := range []string{"triage", "plan", "implement", "verify", "review", "submit"} {
		ps := meta.Phases[name]
		if ps == nil {
			t.Errorf("meta.Phases[%q] missing", name)
			continue
		}
		if ps.Status != PhaseCompleted {
			t.Errorf("meta.Phases[%q].Status = %q, want %q", name, ps.Status, PhaseCompleted)
		}
		if ps.Generation < 1 {
			t.Errorf("meta.Phases[%q].Generation = %d, want >= 1", name, ps.Generation)
		}
	}

	// --- Result files (<phase>.json) contain valid JSON ---
	for _, name := range []string{"triage", "plan", "implement", "verify", "review", "submit"} {
		resultPath := filepath.Join(state.Dir(), name+".json")
		resultData, err := os.ReadFile(resultPath)
		if err != nil {
			t.Errorf("read %s.json: %v", name, err)
			continue
		}
		if !json.Valid(resultData) {
			t.Errorf("%s.json is not valid JSON: %s", name, string(resultData))
		}
	}

	// --- Artifact files (<phase>.md) are non-empty ---
	for _, name := range []string{"triage", "plan", "implement", "verify", "review", "submit"} {
		artifactPath := filepath.Join(state.Dir(), name+".md")
		artifactData, err := os.ReadFile(artifactPath)
		if err != nil {
			t.Errorf("read %s.md: %v", name, err)
			continue
		}
		if len(artifactData) == 0 {
			t.Errorf("%s.md should not be empty", name)
		}
	}

	// --- events.jsonl each line is valid JSON ---
	eventsPath := filepath.Join(state.Dir(), "events.jsonl")
	eventsData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}

	lines := 0
	for _, line := range splitNonEmpty(string(eventsData)) {
		if !json.Valid([]byte(line)) {
			t.Errorf("events.jsonl line %d is not valid JSON: %s", lines+1, line)
		}
		lines++
	}
	if lines == 0 {
		t.Error("events.jsonl should have at least one line")
	}
}

// splitNonEmpty splits s by newline and returns non-empty trimmed lines.
func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
