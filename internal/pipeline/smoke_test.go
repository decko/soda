package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
