package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
)

func TestEngine_shouldSkip_CompletedNoDepsRerun(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "plan.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "implement", Prompt: "implement.md", DependsOn: []string{"plan"}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{})
	_ = state.MarkRunning("plan")
	_ = state.MarkCompleted("plan")
	_ = state.MarkRunning("implement")
	_ = state.MarkCompleted("implement")
	engine.reranPhases = map[string]bool{}

	// implement is completed and its dep (plan) was NOT re-run → skip
	if !engine.shouldSkip(phases[1]) {
		t.Error("expected shouldSkip=true for completed phase with no re-run deps")
	}
}

func TestEngine_shouldSkip_CompletedDepRerun(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "plan.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "implement", Prompt: "implement.md", DependsOn: []string{"plan"}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{})
	_ = state.MarkRunning("plan")
	_ = state.MarkCompleted("plan")
	_ = state.MarkRunning("implement")
	_ = state.MarkCompleted("implement")
	engine.reranPhases = map[string]bool{"plan": true}

	// implement is completed but its dep (plan) was re-run → don't skip
	if engine.shouldSkip(phases[1]) {
		t.Error("expected shouldSkip=false for completed phase with re-run dep")
	}
}

func TestEngine_shouldSkip_NotCompleted(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "plan.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, _ := setupEngine(t, phases, &flexMockRunner{})
	engine.reranPhases = map[string]bool{}

	// plan is not completed → don't skip
	if engine.shouldSkip(phases[0]) {
		t.Error("expected shouldSkip=false for non-completed phase")
	}
}

func TestEngine_shouldSkipPostSubmit_NoReviewResult(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "follow-up", Prompt: "follow-up.md", Type: "post-submit", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, _ := setupEngine(t, phases, &flexMockRunner{})

	// No review result on disk → skip
	if !engine.shouldSkipPostSubmit(phases[0]) {
		t.Error("expected shouldSkipPostSubmit=true when no review result exists")
	}
}

func TestEngine_shouldSkipPostSubmit_PassVerdict(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "review", Prompt: "review.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "follow-up", Prompt: "follow-up.md", Type: "post-submit", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{})
	_ = state.MarkRunning("review")
	_ = state.WriteResult("review", json.RawMessage(`{"verdict":"pass","findings":[]}`))
	_ = state.MarkCompleted("review")

	// review verdict is "pass" → skip follow-up
	if !engine.shouldSkipPostSubmit(phases[1]) {
		t.Error("expected shouldSkipPostSubmit=true when verdict is pass")
	}
}

func TestEngine_shouldSkipPostSubmit_PassWithFollowUps(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "review", Prompt: "review.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "follow-up", Prompt: "follow-up.md", Type: "post-submit", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{})
	_ = state.MarkRunning("review")
	_ = state.WriteResult("review", json.RawMessage(`{"verdict":"pass-with-follow-ups","findings":[{"severity":"minor","issue":"nit"}]}`))
	_ = state.MarkCompleted("review")

	// review verdict is "pass-with-follow-ups" → don't skip
	if engine.shouldSkipPostSubmit(phases[1]) {
		t.Error("expected shouldSkipPostSubmit=false when verdict is pass-with-follow-ups")
	}
}

func TestEngine_triageRequestsSkipPlan_True(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "triage.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Ticket.ExistingPlan = "some plan"
	})
	_ = state.MarkRunning("triage")
	_ = state.WriteResult("triage", json.RawMessage(`{"automatable":"yes","skip_plan":true}`))

	if !engine.triageRequestsSkipPlan() {
		t.Error("expected triageRequestsSkipPlan=true when skip_plan=true and ExistingPlan is set")
	}
}

func TestEngine_triageRequestsSkipPlan_FalseWhenNotSet(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "triage.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Ticket.ExistingPlan = "some plan"
	})
	_ = state.MarkRunning("triage")
	_ = state.WriteResult("triage", json.RawMessage(`{"automatable":"yes","skip_plan":false}`))

	if engine.triageRequestsSkipPlan() {
		t.Error("expected triageRequestsSkipPlan=false when skip_plan=false")
	}
}

func TestEngine_triageRequestsSkipPlan_FalseWhenNoPlan(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "triage.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{})
	_ = state.MarkRunning("triage")
	_ = state.WriteResult("triage", json.RawMessage(`{"automatable":"yes","skip_plan":true}`))

	// No ExistingPlan set → false
	if engine.triageRequestsSkipPlan() {
		t.Error("expected triageRequestsSkipPlan=false when ExistingPlan is empty")
	}
}

func TestEngine_triageRequestsSkipPlan_FalseWhenNoResult(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "triage.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, _ := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Ticket.ExistingPlan = "some plan"
	})

	// No triage result → false
	if engine.triageRequestsSkipPlan() {
		t.Error("expected triageRequestsSkipPlan=false when no triage result exists")
	}
}

func TestEngine_skipPlanFromTriage(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "triage.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "plan", Prompt: "plan.md", DependsOn: []string{"triage"}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	var events []Event
	engine, state := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Ticket.ExistingPlan = "## Tasks\n- implement feature X"
		cfg.OnEvent = func(e Event) { events = append(events, e) }
	})

	err := engine.skipPlanFromTriage()
	if err != nil {
		t.Fatalf("skipPlanFromTriage() = %v, want nil", err)
	}

	// Plan phase should be completed.
	if !state.IsCompleted("plan") {
		t.Error("plan phase should be completed after skipPlanFromTriage")
	}

	// Plan artifact should contain the existing plan.
	artifact, err := state.ReadArtifact("plan")
	if err != nil {
		t.Fatalf("ReadArtifact(plan) error: %v", err)
	}
	if string(artifact) != "## Tasks\n- implement feature X" {
		t.Errorf("plan artifact = %q, want existing plan", string(artifact))
	}

	// Should have emitted EventPlanSkippedByTriage.
	found := false
	for _, e := range events {
		if e.Kind == EventPlanSkippedByTriage {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected EventPlanSkippedByTriage event")
	}
}

func TestEngine_routeRework_ValidTarget(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "plan.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "implement", Prompt: "implement.md", DependsOn: []string{"plan"}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "review", Prompt: "review.md", DependsOn: []string{"implement"}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}, Rework: &ReworkConfig{Target: "implement"}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{})

	sig := &reworkSignal{target: "implement"}
	route, err := engine.routeRework("review", sig)
	if err != nil {
		t.Fatalf("routeRework() = %v, want nil", err)
	}

	if route == nil {
		t.Fatal("routeRework() returned nil route")
	}
	if !route.forceFirst {
		t.Error("expected forceFirst=true in rework route")
	}
	if len(route.phases) != 2 {
		t.Errorf("expected 2 phases in route (implement, review), got %d", len(route.phases))
	}
	if route.phases[0].Name != "implement" {
		t.Errorf("first phase in route = %q, want implement", route.phases[0].Name)
	}

	// ReworkCycles should be incremented (implement is not corrective).
	if state.Meta().ReworkCycles != 1 {
		t.Errorf("ReworkCycles = %d, want 1", state.Meta().ReworkCycles)
	}
}

func TestEngine_routeRework_InvalidTarget(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "plan.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, _ := setupEngine(t, phases, &flexMockRunner{})

	sig := &reworkSignal{target: "nonexistent"}
	_, err := engine.routeRework("review", sig)
	if err == nil {
		t.Fatal("expected error for invalid rework target")
	}
}

func TestEngine_routeRework_CorrectiveIncPatchCycles(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "verify", Prompt: "verify.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "patch", Prompt: "patch.md", Type: "corrective", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{})

	sig := &reworkSignal{target: "patch"}
	_, err := engine.routeRework("verify", sig)
	if err != nil {
		t.Fatalf("routeRework() = %v, want nil", err)
	}

	// PatchCycles should be incremented (patch is corrective).
	if state.Meta().PatchCycles != 1 {
		t.Errorf("PatchCycles = %d, want 1", state.Meta().PatchCycles)
	}
	if state.Meta().ReworkCycles != 0 {
		t.Errorf("ReworkCycles = %d, want 0 (corrective target)", state.Meta().ReworkCycles)
	}
}

func TestEngine_isCorrectivePhase(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "implement", Prompt: "implement.md"},
		{Name: "patch", Prompt: "patch.md", Type: "corrective"},
		{Name: "verify", Prompt: "verify.md"},
	}

	engine, _ := setupEngine(t, phases, &flexMockRunner{})

	if engine.isCorrectivePhase("implement") {
		t.Error("implement should not be corrective")
	}
	if !engine.isCorrectivePhase("patch") {
		t.Error("patch should be corrective")
	}
	if engine.isCorrectivePhase("verify") {
		t.Error("verify should not be corrective")
	}
	if engine.isCorrectivePhase("nonexistent") {
		t.Error("nonexistent should not be corrective")
	}
}

func TestEngine_skipPlanFromTriage_FullPipeline(t *testing.T) {
	// Integration-style test: run a pipeline where triage sets skip_plan=true
	// and verify that implement receives the plan artifact.
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "triage.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "plan", Prompt: "plan.md", DependsOn: []string{"triage"}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{Name: "implement", Prompt: "implement.md", DependsOn: []string{"plan"}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":"yes","skip_plan":true}`),
					RawText: "triage result",
					CostUSD: 0.01,
				},
			}},
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "implemented",
					CostUSD: 0.50,
				},
			}},
		},
	}

	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.Ticket.ExistingPlan = "## Tasks\n- do the thing"
		cfg.SleepFunc = func(time.Duration) {}
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}

	// plan should be completed via skip path
	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}

	// implement should have run (not plan)
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 runner calls (triage + implement), got %d", len(mock.calls))
	}
	names := phaseNames(mock.calls)
	if names[0] != "triage" || names[1] != "implement" {
		t.Errorf("runner phases = %v, want [triage implement]", names)
	}
}

// --- Phase condition evaluation tests ---

func TestEvalPhaseCondition_EmptyAlwaysRuns(t *testing.T) {
	shouldRun, err := evalPhaseCondition("", phaseConditionData{Complexity: "large"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !shouldRun {
		t.Error("expected empty condition to return true (always run)")
	}
}

func TestEvalPhaseCondition_FalseSkips(t *testing.T) {
	// Condition that evaluates to "false" when Complexity == "small".
	cond := `{{ eq .Complexity "small" }}`
	// When Complexity is "small", eq returns "true" → should run.
	shouldRun, err := evalPhaseCondition(cond, phaseConditionData{Complexity: "small"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !shouldRun {
		t.Error("expected condition to return true when Complexity=small")
	}

	// When Complexity is "large", eq returns "false" → should skip.
	shouldRun, err = evalPhaseCondition(cond, phaseConditionData{Complexity: "large"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shouldRun {
		t.Error("expected condition to return false when Complexity=large")
	}
}

func TestEvalPhaseCondition_CaseInsensitiveFalse(t *testing.T) {
	// Template that outputs "FALSE" in uppercase.
	cond := `{{ if eq .Complexity "low" }}FALSE{{ else }}true{{ end }}`
	shouldRun, err := evalPhaseCondition(cond, phaseConditionData{Complexity: "low"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shouldRun {
		t.Error("expected case-insensitive FALSE to skip")
	}
}

func TestEvalPhaseCondition_TemplateErrorFallsBack(t *testing.T) {
	// Use a template that will fail at execution time (accessing missing method).
	cond := `{{ .NonexistentMethod }}`
	shouldRun, err := evalPhaseCondition(cond, phaseConditionData{})
	if err == nil {
		t.Fatal("expected error from template execution")
	}
	if !shouldRun {
		t.Error("expected template error to fall back to true (run the phase)")
	}
}

func TestEngine_readPhaseConditionData(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "triage.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	engine, state := setupEngine(t, phases, &flexMockRunner{})
	_ = state.MarkRunning("triage")
	_ = state.WriteResult("triage", json.RawMessage(`{"complexity":"high","automatable":"yes"}`))
	state.Meta().ReworkCycles = 2

	data := engine.readPhaseConditionData()
	if data.Complexity != "high" {
		t.Errorf("Complexity = %q, want %q", data.Complexity, "high")
	}
	if data.Automatable != "yes" {
		t.Errorf("Automatable = %q, want %q", data.Automatable, "yes")
	}
	if data.ReworkCycle != 2 {
		t.Errorf("ReworkCycle = %d, want 2", data.ReworkCycle)
	}
}

func TestEngine_skipPhaseByCondition(t *testing.T) {
	phases := []PhaseConfig{
		{Name: "plan", Prompt: "plan.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	var events []Event
	engine, state := setupEngine(t, phases, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) { events = append(events, e) }
	})

	err := engine.skipPhaseByCondition(phases[0], "condition evaluated to false")
	if err != nil {
		t.Fatalf("skipPhaseByCondition() = %v, want nil", err)
	}

	// Phase should be completed.
	if !state.IsCompleted("plan") {
		t.Error("plan phase should be completed after skipPhaseByCondition")
	}

	// Artifact should exist (empty).
	artifact, err := state.ReadArtifact("plan")
	if err != nil {
		t.Fatalf("ReadArtifact(plan) error: %v", err)
	}
	if len(artifact) != 0 {
		t.Errorf("artifact length = %d, want 0 (empty)", len(artifact))
	}

	// Should have emitted EventPhaseConditionSkipped.
	foundSkipped := false
	foundCompleted := false
	for _, e := range events {
		if e.Kind == EventPhaseConditionSkipped {
			foundSkipped = true
			if reason, ok := e.Data["reason"].(string); !ok || reason != "condition evaluated to false" {
				t.Errorf("reason = %v, want %q", e.Data["reason"], "condition evaluated to false")
			}
		}
		if e.Kind == EventPhaseCompleted {
			foundCompleted = true
		}
	}
	if !foundSkipped {
		t.Error("expected EventPhaseConditionSkipped event")
	}
	if !foundCompleted {
		t.Error("expected EventPhaseCompleted event")
	}
}

func TestEngine_phaseCondition_FullPipeline(t *testing.T) {
	// Integration-style test: run a pipeline where a phase has a condition
	// that evaluates to false based on triage complexity.
	phases := []PhaseConfig{
		{Name: "triage", Prompt: "triage.md", Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Condition: `{{ ne .Complexity "small" }}`,
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{Name: "implement", Prompt: "implement.md", DependsOn: []string{"plan"}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
	}

	mock := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"complexity":"small","automatable":true}`),
					RawText: "triage result",
					CostUSD: 0.01,
				},
			}},
			"implement": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tests_passed":true}`),
					RawText: "implemented",
					CostUSD: 0.50,
				},
			}},
		},
	}

	var events []Event
	engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) { events = append(events, e) }
		cfg.SleepFunc = func(time.Duration) {}
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}

	// plan should be completed via condition skip path.
	if !state.IsCompleted("plan") {
		t.Error("plan should be completed")
	}

	// Only triage + implement should have been called (plan was skipped).
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 runner calls (triage + implement), got %d", len(mock.calls))
	}
	names := phaseNames(mock.calls)
	if names[0] != "triage" || names[1] != "implement" {
		t.Errorf("runner phases = %v, want [triage implement]", names)
	}

	// Verify condition-skipped event was emitted.
	foundSkipped := false
	for _, e := range events {
		if e.Kind == EventPhaseConditionSkipped && e.Phase == "plan" {
			foundSkipped = true
			break
		}
	}
	if !foundSkipped {
		t.Error("expected EventPhaseConditionSkipped event for plan phase")
	}
}
