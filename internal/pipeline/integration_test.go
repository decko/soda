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

// fullPipelinePhases returns the production 6-phase pipeline config
// (triage → plan → implement → verify → submit → monitor) matching
// the dependency graph in embeds/phases.yaml.
func fullPipelinePhases() []PhaseConfig {
	return []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Schema: `{"type":"object","properties":{"automatable":{"type":"boolean"}},"required":["automatable"]}`,
			Retry:  RetryConfig{Transient: 2, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			Schema:    `{"type":"object","properties":{"tasks":{"type":"array"}},"required":["tasks"]}`,
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 2, Parse: 1, Semantic: 1},
		},
		{
			Name:      "implement",
			Prompt:    "implement.md",
			Schema:    `{"type":"object","properties":{"tests_passed":{"type":"boolean"}},"required":["tests_passed"]}`,
			DependsOn: []string{"plan"},
			Retry:     RetryConfig{Transient: 2, Parse: 1, Semantic: 0},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			Schema:    `{"type":"object","properties":{"verdict":{"type":"string"}},"required":["verdict"]}`,
			DependsOn: []string{"plan", "implement"},
			Retry:     RetryConfig{Transient: 2, Parse: 1, Semantic: 1},
		},
		{
			Name:      "submit",
			Prompt:    "submit.md",
			Schema:    `{"type":"object","properties":{"pr_url":{"type":"string"}},"required":["pr_url"]}`,
			DependsOn: []string{"implement", "verify"},
			Retry:     RetryConfig{Transient: 2, Parse: 1, Semantic: 0},
		},
		{
			Name:      "monitor",
			Prompt:    "monitor.md",
			Type:      "polling",
			DependsOn: []string{"submit"},
			Retry:     RetryConfig{Transient: 2, Parse: 1, Semantic: 0},
		},
	}
}

// mockFixtures returns realistic structured outputs for the full pipeline.
func mockFixtures() map[string]*runner.RunResult {
	return map[string]*runner.RunResult{
		"triage": {
			Output: json.RawMessage(`{
				"ticket_key": "INT-1",
				"repo": "soda",
				"code_area": "internal/pipeline",
				"files": ["internal/pipeline/engine.go", "internal/pipeline/engine_test.go"],
				"complexity": "medium",
				"approach": "Add integration tests for the full pipeline lifecycle",
				"risks": ["test flakiness", "mock coverage gaps"],
				"automatable": true
			}`),
			RawText: "Triage: medium complexity, automatable, affects internal/pipeline",
			CostUSD: 0.05,
		},
		"plan": {
			Output: json.RawMessage(`{
				"ticket_key": "INT-1",
				"approach": "Create integration tests with mock runner covering the full 6-phase pipeline",
				"tasks": [
					{"id": "T1", "description": "Create mock runner integration test", "files": ["internal/pipeline/integration_test.go"], "done_when": "Test passes with mock runner"},
					{"id": "T2", "description": "Create dry-run integration test", "files": ["internal/pipeline/integration_test.go"], "done_when": "Dry-run renders all prompts"},
					{"id": "T3", "description": "Create optional API test", "files": ["internal/pipeline/integration_test.go"], "done_when": "Test gated behind env var"}
				],
				"verification": {"commands": ["go test ./internal/pipeline/ -run Integration"]},
				"deviations": []
			}`),
			RawText: "Plan: 3 tasks — mock integration, dry-run, optional API test",
			CostUSD: 0.12,
		},
		"implement": {
			Output: json.RawMessage(`{
				"ticket_key": "INT-1",
				"branch": "soda/INT-1",
				"commits": [
					{"hash": "abc123", "message": "feat: add pipeline integration tests", "task_id": "T1"},
					{"hash": "def456", "message": "feat: add dry-run integration test", "task_id": "T2"},
					{"hash": "ghi789", "message": "feat: add optional API integration test", "task_id": "T3"}
				],
				"files_changed": [
					{"path": "internal/pipeline/integration_test.go", "action": "created"}
				],
				"task_results": [
					{"task_id": "T1", "status": "completed"},
					{"task_id": "T2", "status": "completed"},
					{"task_id": "T3", "status": "completed"}
				],
				"tests_passed": true,
				"test_output": "ok  \tgithub.com/decko/soda/internal/pipeline\t1.234s"
			}`),
			RawText: "Implementation complete: 3 commits, 1 file created, all tests passing",
			CostUSD: 1.50,
		},
		"verify": {
			Output: json.RawMessage(`{
				"ticket_key": "INT-1",
				"verdict": "PASS",
				"criteria_results": [
					{"criterion": "mock integration test exists", "passed": true, "evidence": "TestIntegration_MockFullPipeline present"},
					{"criterion": "dry-run test exists", "passed": true, "evidence": "TestIntegration_DryRun present"},
					{"criterion": "optional API test gated", "passed": true, "evidence": "TestIntegration_RealAPI skipped without env var"}
				],
				"command_results": [
					{"command": "go test ./internal/pipeline/ -run Integration", "exit_code": 0, "output": "PASS", "passed": true},
					{"command": "go vet ./...", "exit_code": 0, "output": "ok", "passed": true}
				],
				"code_issues": [],
				"fixes_required": []
			}`),
			RawText: "Verification passed: all criteria met, all commands pass",
			CostUSD: 0.30,
		},
		"submit": {
			Output: json.RawMessage(`{
				"ticket_key": "INT-1",
				"pr_url": "https://github.com/decko/soda/pull/51",
				"pr_number": 51,
				"title": "feat: add pipeline integration tests",
				"branch": "soda/INT-1",
				"target": "main",
				"forge": "github"
			}`),
			RawText: "PR created: https://github.com/decko/soda/pull/51",
			CostUSD: 0.08,
		},
	}
}

// writeIntegrationPrompts creates prompt templates that reference artifacts from
// upstream phases, exercising the artifact handoff chain.
func writeIntegrationPrompts(t *testing.T, dir string) {
	t.Helper()

	templates := map[string]string{
		"triage.md": `Triage phase for {{.Ticket.Key}}
Summary: {{.Ticket.Summary}}
Type: {{.Ticket.Type}}
Priority: {{.Ticket.Priority}}
{{- if .Ticket.AcceptanceCriteria}}
Acceptance Criteria:
{{range .Ticket.AcceptanceCriteria}}- {{.}}
{{end}}{{end}}
{{- if .Config.Formatter}}Formatter: {{.Config.Formatter}}{{end}}
{{- if .Context.ProjectContext}}
Project: {{.Context.ProjectContext}}{{end}}
`,
		"plan.md": `Plan phase for {{.Ticket.Key}}
Triage output:
{{.Artifacts.Triage}}
`,
		"implement.md": `Implement phase for {{.Ticket.Key}}
Plan:
{{.Artifacts.Plan}}
{{- if .ReworkFeedback}}
REWORK FEEDBACK:
Verdict: {{.ReworkFeedback.Verdict}}
{{range .ReworkFeedback.FixesRequired}}Fix: {{.}}
{{end}}{{end}}
`,
		"verify.md": `Verify phase for {{.Ticket.Key}}
Plan:
{{.Artifacts.Plan}}
Implementation:
{{.Artifacts.Implement}}
`,
		"submit.md": `Submit phase for {{.Ticket.Key}}
WorktreePath: {{.WorktreePath}}
Branch: {{.Branch}}
BaseBranch: {{.BaseBranch}}
`,
		"monitor.md": `Monitor phase for {{.Ticket.Key}}
`,
	}

	for name, content := range templates {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write prompt %s: %v", name, err)
		}
	}
}

// TestIntegration_MockFullPipeline runs the complete 6-phase pipeline with a
// mock runner, verifying event lifecycle, artifact flow, state transitions,
// cost accumulation, and gating behaviour.
func TestIntegration_MockFullPipeline(t *testing.T) {
	phases := fullPipelinePhases()
	fixtures := mockFixtures()

	// Build flexMockRunner from fixtures.
	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeIntegrationPrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "INT-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline: &PhasePipeline{Phases: phases},
		Loader:   NewPromptLoader(promptDir),
		Ticket: TicketData{
			Key:         "INT-1",
			Summary:     "Add pipeline integration tests",
			Description: "Create CI pipeline integration tests with mock, dry-run, and optional real API.",
			Type:        "task",
			Priority:    "high",
			AcceptanceCriteria: []string{
				"Mock integration test covers full pipeline",
				"Dry-run test renders all prompts",
				"Real API test gated behind env var",
			},
		},
		PromptConfig: PromptConfigData{
			Formatter:   "gofmt",
			TestCommand: "go test ./...",
			Repo: RepoConfig{
				Name:   "soda",
				Forge:  "github",
				PushTo: "origin",
				Target: "main",
			},
		},
		PromptContext: ContextData{
			ProjectContext: "Go CLI for automated software development",
		},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 10.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	}

	engine := NewEngine(mock, state, cfg)

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --- Verify all phases completed ---
	for _, name := range []string{"triage", "plan", "implement", "verify", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed", name)
		}
	}

	// --- Verify cost accumulation ---
	// triage(0.05) + plan(0.12) + implement(1.50) + verify(0.30) + submit(0.08) = 2.05
	// monitor is a polling stub with no runner cost.
	expectedCost := 0.05 + 0.12 + 1.50 + 0.30 + 0.08
	if !approxEqual(state.Meta().TotalCost, expectedCost) {
		t.Errorf("TotalCost = %v, want %v", state.Meta().TotalCost, expectedCost)
	}

	// --- Verify per-phase costs ---
	phaseCosts := map[string]float64{
		"triage":    0.05,
		"plan":      0.12,
		"implement": 1.50,
		"verify":    0.30,
		"submit":    0.08,
	}
	for name, wantCost := range phaseCosts {
		ps := state.Meta().Phases[name]
		if ps == nil {
			t.Errorf("phase %q state missing", name)
			continue
		}
		if !approxEqual(ps.Cost, wantCost) {
			t.Errorf("phase %q cost = %v, want %v", name, ps.Cost, wantCost)
		}
	}

	// --- Verify runner was called for non-polling phases (5 calls, not 6) ---
	if len(mock.calls) != 5 {
		t.Errorf("runner called %d times, want 5 (monitor is polling stub); phases: %v",
			len(mock.calls), phaseNames(mock.calls))
	}

	// --- Verify correct phase ordering in runner calls ---
	wantOrder := []string{"triage", "plan", "implement", "verify", "submit"}
	gotOrder := phaseNames(mock.calls)
	for i, want := range wantOrder {
		if i >= len(gotOrder) {
			t.Errorf("missing runner call for phase %q at index %d", want, i)
			break
		}
		if gotOrder[i] != want {
			t.Errorf("runner call[%d] = %q, want %q", i, gotOrder[i], want)
		}
	}

	// --- Verify artifact flow: plan prompt contains triage output ---
	planPrompt := mock.calls[1].SystemPrompt
	if !strings.Contains(planPrompt, "Triage: medium complexity") {
		t.Errorf("plan prompt should contain triage RawText artifact;\ngot: %s", planPrompt)
	}

	// --- Verify artifact flow: implement prompt contains plan output ---
	implPrompt := mock.calls[2].SystemPrompt
	if !strings.Contains(implPrompt, "Plan: 3 tasks") {
		t.Errorf("implement prompt should contain plan RawText artifact;\ngot: %s", implPrompt)
	}

	// --- Verify artifact flow: verify prompt contains both plan and implement ---
	verifyPrompt := mock.calls[3].SystemPrompt
	if !strings.Contains(verifyPrompt, "Plan: 3 tasks") {
		t.Errorf("verify prompt should contain plan artifact;\ngot: %s", verifyPrompt)
	}
	if !strings.Contains(verifyPrompt, "Implementation complete") {
		t.Errorf("verify prompt should contain implement artifact;\ngot: %s", verifyPrompt)
	}

	// --- Verify config/context rendered in triage prompt ---
	triagePrompt := mock.calls[0].SystemPrompt
	if !strings.Contains(triagePrompt, "Formatter: gofmt") {
		t.Errorf("triage prompt should contain formatter;\ngot: %s", triagePrompt)
	}
	if !strings.Contains(triagePrompt, "Go CLI for automated software development") {
		t.Errorf("triage prompt should contain project context;\ngot: %s", triagePrompt)
	}

	// --- Verify ticket data rendered ---
	if !strings.Contains(triagePrompt, "Add pipeline integration tests") {
		t.Errorf("triage prompt should contain ticket summary;\ngot: %s", triagePrompt)
	}
	if !strings.Contains(triagePrompt, "Mock integration test covers full pipeline") {
		t.Errorf("triage prompt should contain acceptance criteria;\ngot: %s", triagePrompt)
	}

	// --- Verify engine lifecycle events ---
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

	// 5 non-polling phases started + 1 monitor = 6 phase_started events
	if eventKinds[EventPhaseStarted] != 6 {
		t.Errorf("phase_started events = %d, want 6", eventKinds[EventPhaseStarted])
	}
	if eventKinds[EventPhaseCompleted] != 6 {
		t.Errorf("phase_completed events = %d, want 6", eventKinds[EventPhaseCompleted])
	}

	// Monitor should emit monitor_skipped.
	if eventKinds[EventMonitorSkipped] != 1 {
		t.Errorf("monitor_skipped events = %d, want 1", eventKinds[EventMonitorSkipped])
	}

	// --- Verify artifacts persisted to disk ---
	for _, name := range []string{"triage", "plan", "implement", "verify", "submit"} {
		artifact, err := state.ReadArtifact(name)
		if err != nil {
			t.Errorf("ReadArtifact(%q): %v", name, err)
			continue
		}
		if len(artifact) == 0 {
			t.Errorf("artifact for %q should not be empty", name)
		}
	}

	// --- Verify structured results persisted to disk ---
	for _, name := range []string{"triage", "plan", "implement", "verify", "submit"} {
		result, err := state.ReadResult(name)
		if err != nil {
			t.Errorf("ReadResult(%q): %v", name, err)
			continue
		}
		if len(result) == 0 {
			t.Errorf("result for %q should not be empty", name)
		}
	}

	// --- Verify submit result contains PR URL ---
	submitResult, err := state.ReadResult("submit")
	if err != nil {
		t.Fatalf("ReadResult(submit): %v", err)
	}
	var submitData struct {
		PRURL string `json:"pr_url"`
	}
	if err := json.Unmarshal(submitResult, &submitData); err != nil {
		t.Fatalf("unmarshal submit result: %v", err)
	}
	if submitData.PRURL != "https://github.com/decko/soda/pull/51" {
		t.Errorf("submit pr_url = %q, want %q", submitData.PRURL, "https://github.com/decko/soda/pull/51")
	}

	// --- Verify runner opts carry schema and model ---
	for _, call := range mock.calls {
		if call.Model != "test-model" {
			t.Errorf("phase %s model = %q, want %q", call.Phase, call.Model, "test-model")
		}
		if call.SystemPrompt == "" {
			t.Errorf("phase %s SystemPrompt should not be empty", call.Phase)
		}
	}

	// --- Verify ticket summary cached in meta ---
	if state.Meta().Summary != "Add pipeline integration tests" {
		t.Errorf("meta summary = %q, want %q", state.Meta().Summary, "Add pipeline integration tests")
	}

	// --- Verify events.jsonl was written ---
	eventsPath := filepath.Join(state.Dir(), "events.jsonl")
	eventsData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if len(eventsData) == 0 {
		t.Error("events.jsonl should not be empty")
	}
}

// TestIntegration_MockGateBlocksDownstream verifies that triage gating
// (automatable=false) stops the pipeline and prevents downstream phases
// from running.
func TestIntegration_MockGateBlocksDownstream(t *testing.T) {
	phases := fullPipelinePhases()

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":false,"block_reason":"requires architecture review"}`),
					RawText: "Blocked: architecture review needed",
					CostUSD: 0.03,
				},
			}},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeIntegrationPrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "GATE-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "GATE-1", Summary: "Gating test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	err = engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected PhaseGateError")
	}

	var gateErr *PhaseGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PhaseGateError, got: %T: %v", err, err)
	}
	if gateErr.Phase != "triage" {
		t.Errorf("gate error phase = %q, want %q", gateErr.Phase, "triage")
	}
	if !strings.Contains(gateErr.Reason, "architecture review") {
		t.Errorf("gate reason = %q, want to contain 'architecture review'", gateErr.Reason)
	}

	// Only triage should have been called.
	if len(mock.calls) != 1 {
		t.Errorf("runner called %d times, want 1; phases: %v",
			len(mock.calls), phaseNames(mock.calls))
	}

	// Downstream phases should not be started.
	for _, name := range []string{"plan", "implement", "verify", "submit", "monitor"} {
		if state.IsCompleted(name) {
			t.Errorf("phase %q should NOT be completed", name)
		}
	}
}

// TestIntegration_MockResumeAfterFailure tests the resume-from-failure
// workflow: verify FAILs → resume from implement → implement gen 2 →
// verify passes → submit → monitor.
func TestIntegration_MockResumeAfterFailure(t *testing.T) {
	// Phases: implement → verify → submit → monitor
	// (skipping triage/plan to focus on resume behaviour).
	phases := []PhaseConfig{
		{
			Name:   "implement",
			Prompt: "implement.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "submit",
			Prompt:    "submit.md",
			DependsOn: []string{"verify"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
		{
			Name:      "monitor",
			Prompt:    "monitor.md",
			Type:      "polling",
			DependsOn: []string{"submit"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeIntegrationPrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "RESUME-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// --- First run: implement OK, verify FAIL ---
	mock1 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":1}`),
					RawText: "Impl v1",
					CostUSD: 0.50,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["missing edge case test"]}`),
					RawText: "Verify v1 FAIL",
					CostUSD: 0.15,
				},
			}},
		},
	}

	var events1 []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "RESUME-1", Summary: "Resume test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
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

	// Verify implement is completed, verify is completed (but gate blocked).
	if !state.IsCompleted("implement") {
		t.Error("implement should be completed after first run")
	}
	if !state.IsCompleted("verify") {
		t.Error("verify should be completed (gate check happens after completion)")
	}

	costAfterFirstRun := state.Meta().TotalCost

	// --- Second run: resume from implement → everything passes ---
	mock2 := &flexMockRunner{
		responses: map[string][]flexResponse{
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true,"commits":2}`),
					RawText: "Impl v2 with edge case fix",
					CostUSD: 0.60,
				},
			}},
			"verify": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"PASS"}`),
					RawText: "All checks pass",
					CostUSD: 0.20,
				},
			}},
			"submit": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"pr_url":"https://github.com/decko/soda/pull/99"}`),
					RawText: "PR created",
					CostUSD: 0.05,
				},
			}},
		},
	}

	var events2 []Event
	cfg.OnEvent = func(e Event) { events2 = append(events2, e) }

	engine2 := NewEngine(mock2, state, cfg)

	if err := engine2.Resume(context.Background(), "implement"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// All phases should now be completed.
	for _, name := range []string{"implement", "verify", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed after resume", name)
		}
	}

	// Implement should be gen 2.
	implPS := state.Meta().Phases["implement"]
	if implPS == nil {
		t.Fatal("implement phase state missing")
	}
	if implPS.Generation < 2 {
		t.Errorf("implement generation = %d, want >= 2", implPS.Generation)
	}

	// Verify should also be gen 2 (dependency re-ran).
	verifyPS := state.Meta().Phases["verify"]
	if verifyPS == nil {
		t.Fatal("verify phase state missing")
	}
	if verifyPS.Generation < 2 {
		t.Errorf("verify generation = %d, want >= 2", verifyPS.Generation)
	}

	// Cost should have accumulated from both runs.
	expectedAdditional := 0.60 + 0.20 + 0.05 // impl v2 + verify v2 + submit
	expectedTotal := costAfterFirstRun + expectedAdditional
	if !approxEqual(state.Meta().TotalCost, expectedTotal) {
		t.Errorf("TotalCost = %v, want %v", state.Meta().TotalCost, expectedTotal)
	}

	// Engine completed event should be present in second run.
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

// TestIntegration_DryRunRendersAllPrompts verifies that prompts can be
// rendered for every phase without running the runner, simulating the
// --dry-run CLI flag behaviour at the engine/prompt level.
func TestIntegration_DryRunRendersAllPrompts(t *testing.T) {
	phases := fullPipelinePhases()

	promptDir := t.TempDir()
	writeIntegrationPrompts(t, promptDir)

	loader := NewPromptLoader(promptDir)
	ticket := TicketData{
		Key:         "DRY-1",
		Summary:     "Dry run test",
		Description: "Verify prompt rendering in dry-run mode",
		Type:        "task",
		Priority:    "medium",
		AcceptanceCriteria: []string{
			"All prompts render without error",
		},
	}

	promptConfig := PromptConfigData{
		Formatter:   "gofmt",
		TestCommand: "go test ./...",
		Repo: RepoConfig{
			Name:  "soda",
			Forge: "github",
		},
	}

	promptContext := ContextData{
		ProjectContext: "Test project context",
	}

	// Build PromptData with synthetic artifacts to simulate
	// what would be available at each stage.
	promptData := PromptData{
		Ticket:       ticket,
		Config:       promptConfig,
		Context:      promptContext,
		WorktreePath: "/tmp/worktrees/soda/DRY-1",
		Branch:       "soda/DRY-1",
		BaseBranch:   "main",
		Artifacts: ArtifactData{
			Triage:    "Triage: automatable, medium complexity",
			Plan:      "Plan: 2 tasks identified",
			Implement: "Implementation: 3 files changed, tests pass",
			Verify:    "Verification: PASS",
			Submit:    SubmitArtifact{PRURL: "https://github.com/example/pull/1"},
		},
	}

	renderedPrompts := make(map[string]string)

	for _, phase := range phases {
		tmplContent, err := loader.Load(phase.Prompt)
		if err != nil {
			t.Errorf("Load(%q): %v", phase.Prompt, err)
			continue
		}

		rendered, err := RenderPrompt(tmplContent, promptData)
		if err != nil {
			t.Errorf("RenderPrompt(%q): %v", phase.Name, err)
			continue
		}

		if rendered == "" {
			t.Errorf("phase %q rendered to empty string", phase.Name)
		}

		renderedPrompts[phase.Name] = rendered
	}

	// Verify all 6 phases were rendered.
	if len(renderedPrompts) != 6 {
		t.Errorf("rendered %d prompts, want 6", len(renderedPrompts))
	}

	// Verify ticket data appears in triage prompt.
	if triage, ok := renderedPrompts["triage"]; ok {
		for _, want := range []string{"DRY-1", "Dry run test", "gofmt", "Test project context"} {
			if !strings.Contains(triage, want) {
				t.Errorf("triage prompt missing %q", want)
			}
		}
	}

	// Verify artifact handoff: plan references triage artifact.
	if plan, ok := renderedPrompts["plan"]; ok {
		if !strings.Contains(plan, "Triage: automatable") {
			t.Errorf("plan prompt should contain triage artifact;\ngot: %s", plan)
		}
	}

	// Verify artifact handoff: implement references plan artifact.
	if impl, ok := renderedPrompts["implement"]; ok {
		if !strings.Contains(impl, "Plan: 2 tasks") {
			t.Errorf("implement prompt should contain plan artifact;\ngot: %s", impl)
		}
	}

	// Verify artifact handoff: verify references both plan and implement.
	if verify, ok := renderedPrompts["verify"]; ok {
		if !strings.Contains(verify, "Plan: 2 tasks") {
			t.Errorf("verify prompt should contain plan artifact;\ngot: %s", verify)
		}
		if !strings.Contains(verify, "Implementation: 3 files") {
			t.Errorf("verify prompt should contain implement artifact;\ngot: %s", verify)
		}
	}

	// Verify submit prompt includes worktree/branch info.
	if submit, ok := renderedPrompts["submit"]; ok {
		for _, want := range []string{"soda/DRY-1", "main", "/tmp/worktrees/soda/DRY-1"} {
			if !strings.Contains(submit, want) {
				t.Errorf("submit prompt missing %q;\ngot: %s", want, submit)
			}
		}
	}
}

// TestIntegration_MockCheckpointMode verifies the full pipeline works
// in checkpoint mode with auto-confirm for all phases.
func TestIntegration_MockCheckpointMode(t *testing.T) {
	phases := fullPipelinePhases()
	fixtures := mockFixtures()

	responses := make(map[string][]flexResponse)
	for name, result := range fixtures {
		responses[name] = []flexResponse{{result: result}}
	}
	mock := &flexMockRunner{responses: responses}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeIntegrationPrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "CKPT-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	checkpointReached := make(chan struct{}, len(phases))
	var events []Event

	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "CKPT-1", Summary: "Checkpoint mode test"},
		Model:      "test-model",
		WorkDir:    workDir,
		Mode:       Checkpoint,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent: func(e Event) {
			events = append(events, e)
			if e.Kind == EventCheckpointPause {
				checkpointReached <- struct{}{}
			}
		},
	}

	engine := NewEngine(mock, state, cfg)

	// Auto-confirm checkpoints from a goroutine.
	go func() {
		for i := 0; i < len(phases); i++ {
			<-checkpointReached
			engine.Confirm()
		}
	}()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// All phases completed.
	for _, name := range []string{"triage", "plan", "implement", "verify", "submit", "monitor"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %q should be completed in checkpoint mode", name)
		}
	}

	// Should have 6 checkpoint_pause events (one per phase).
	checkpointCount := 0
	for _, e := range events {
		if e.Kind == EventCheckpointPause {
			checkpointCount++
		}
	}
	if checkpointCount != 6 {
		t.Errorf("checkpoint_pause events = %d, want 6", checkpointCount)
	}
}

// TestIntegration_MockBudgetEnforcement verifies that the engine enforces
// budget limits across the full pipeline and emits budget warnings at 90%.
func TestIntegration_MockBudgetEnforcement(t *testing.T) {
	phases := fullPipelinePhases()

	// Triage costs $5.10 which exceeds the $5 budget.
	// After triage completes and accumulates cost, the budget check for
	// plan should trigger BudgetExceededError.
	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage done",
					CostUSD: 5.10,
				},
			}},
		},
	}

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	writeIntegrationPrompts(t, promptDir)

	state, err := LoadOrCreate(stateDir, "BUDGET-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline:   &PhasePipeline{Phases: phases},
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "BUDGET-1", Summary: "Budget test"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 5.0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(mock, state, cfg)

	err = engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected BudgetExceededError")
	}

	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got: %T: %v", err, err)
	}
	if budgetErr.Phase != "plan" {
		t.Errorf("budget error phase = %q, want %q", budgetErr.Phase, "plan")
	}

	// Only triage should have completed.
	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}
	if state.IsCompleted("plan") {
		t.Error("plan should NOT be completed (budget exceeded)")
	}
}

// TestIntegration_RealAPI is an optional test that runs the pipeline
// with the real Claude API. It is skipped unless the SODA_API_TEST
// environment variable is set. Requires the 'claude' binary in PATH.
func TestIntegration_RealAPI(t *testing.T) {
	if os.Getenv("SODA_API_TEST") == "" {
		t.Skip("skipping real API test: set SODA_API_TEST=1 to enable")
	}

	// Verify claude binary is available.
	claudeBin := os.Getenv("CLAUDE_BIN")
	if claudeBin == "" {
		claudeBin = "claude"
	}

	workDir := t.TempDir()
	claudeRunner, err := runner.NewClaudeRunner(claudeBin, "", workDir)
	if err != nil {
		t.Skipf("skipping real API test: claude binary not found: %v", err)
	}

	// Use a minimal single-phase pipeline for the real API test
	// to minimize cost and API calls.
	phases := []PhaseConfig{
		{
			Name:    "triage",
			Prompt:  "triage.md",
			Schema:  `{"type":"object","properties":{"automatable":{"type":"boolean"},"complexity":{"type":"string"}},"required":["automatable","complexity"]}`,
			Retry:   RetryConfig{Transient: 1, Parse: 1, Semantic: 0},
			Timeout: Duration{Duration: 2 * time.Minute},
		},
	}

	promptDir := t.TempDir()
	triageTmpl := `You are assessing a ticket for automated implementation.

Ticket: {{.Ticket.Key}}
Summary: {{.Ticket.Summary}}
Description: {{.Ticket.Description}}

Respond with a JSON object containing:
- "automatable": true (this is a simple test ticket)
- "complexity": "small"
`
	if err := os.WriteFile(filepath.Join(promptDir, "triage.md"), []byte(triageTmpl), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	stateDir := t.TempDir()
	state, err := LoadOrCreate(stateDir, "API-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	var events []Event
	cfg := EngineConfig{
		Pipeline: &PhasePipeline{Phases: phases},
		Loader:   NewPromptLoader(promptDir),
		Ticket: TicketData{
			Key:         "API-1",
			Summary:     "Test ticket for API integration",
			Description: "A simple test ticket to verify the real API works.",
		},
		Model:      os.Getenv("SODA_API_MODEL"),
		WorkDir:    workDir,
		MaxCostUSD: 1.0,
		Mode:       Autonomous,
		OnEvent:    func(e Event) { events = append(events, e) },
	}

	engine := NewEngine(claudeRunner, state, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := engine.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Phase should be completed.
	if !state.IsCompleted("triage") {
		t.Error("triage should be completed")
	}

	// Result should be parseable JSON.
	result, err := state.ReadResult("triage")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}

	var triageResult struct {
		Automatable bool   `json:"automatable"`
		Complexity  string `json:"complexity"`
	}
	if err := json.Unmarshal(result, &triageResult); err != nil {
		t.Fatalf("unmarshal triage result: %v", err)
	}

	// We expect the model to classify this as automatable/small,
	// but we don't hard-assert on specific values since model
	// outputs are non-deterministic.
	t.Logf("Real API result: automatable=%v, complexity=%q", triageResult.Automatable, triageResult.Complexity)

	// Cost should be non-zero.
	if state.Meta().TotalCost <= 0 {
		t.Error("TotalCost should be positive for real API call")
	}
	t.Logf("Real API cost: $%.4f", state.Meta().TotalCost)
}
