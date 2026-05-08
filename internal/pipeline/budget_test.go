package pipeline

import (
	"strings"
	"testing"
)

// simpleTmpl is a minimal template that renders enough fields to test
// fitToBudget's reduction logic.
const simpleTmpl = `Ticket: {{.Ticket.Key}}
Description: {{.Ticket.Description}}
{{- range .Ticket.AcceptanceCriteria}}
AC: {{.}}
{{- end}}
Plan: {{.Artifacts.Plan}}
{{- if .SiblingContext}}
Siblings: {{.SiblingContext}}
{{- end}}
{{- if .DiffContext}}
Diff: {{.DiffContext}}
{{- end}}
{{- if .ReviewComments}}
Comments: {{.ReviewComments}}
{{- end}}
{{- if .Context.ProjectContext}}
ProjectCtx: {{.Context.ProjectContext}}
{{- end}}
{{- if .Context.RepoConventions}}
Conventions: {{.Context.RepoConventions}}
{{- end}}
{{- if .Artifacts.Triage}}
Triage: {{.Artifacts.Triage}}
{{- end}}
{{- if .Artifacts.Implement}}
Implement: {{.Artifacts.Implement}}
{{- end}}
{{- if .Artifacts.Verify}}
Verify: {{.Artifacts.Verify}}
{{- end}}
{{- if .Artifacts.Review}}
Review: {{.Artifacts.Review}}
{{- end}}
{{- if .ReworkFeedback}}
ReworkSource: {{.ReworkFeedback.Source}}
{{- range .ReworkFeedback.ReviewFindings}}
Finding: {{.Issue}}
{{- if .CodeSnippet}}
Snippet: {{.CodeSnippet}}
{{- end}}
{{- end}}
{{- if .ReworkFeedback.ImplementDiff}}
ReworkDiff: {{.ReworkFeedback.ImplementDiff}}
{{- end}}
{{- range .ReworkFeedback.FailedCommands}}
CmdOutput: {{.Output}}
{{- end}}
{{- range .ReworkFeedback.PriorCycles}}
PriorCycle: {{.Summary}}
{{- end}}
{{- end}}
{{- if .ContextFitted}}
FITTED: true
{{- end}}
{{- if .ManifestNote}}
Manifest: {{.ManifestNote}}
{{- end}}
{{- if .Artifacts.Extras}}
{{- range $k, $v := .Artifacts.Extras}}
Extra-{{$k}}: {{$v}}
{{- end}}
{{- end}}
`

// makeData returns a PromptData with populated fields for testing.
func makeData() PromptData {
	return PromptData{
		Ticket: TicketData{
			Key:                "TEST-1",
			Description:        "A test ticket description that should never be removed.",
			AcceptanceCriteria: []string{"criterion-1", "criterion-2"},
		},
		Artifacts: ArtifactData{
			Plan:   "The plan content — always protected.",
			Extras: map[string]string{"custom": "custom artifact content"},
		},
		SiblingContext: strings.Repeat("sibling-ctx ", 100),
		DiffContext:    strings.Repeat("diff-line ", 100),
		ReviewComments: "some review comments",
		Context: ContextData{
			ProjectContext:  strings.Repeat("project-ctx ", 50),
			RepoConventions: strings.Repeat("conventions ", 50),
		},
	}
}

func TestFitToBudget_AlreadyFits(t *testing.T) {
	data := makeData()
	// Use a very large budget — should return immediately.
	fitted, reduced, err := fitToBudget(simpleTmpl, data, "implement", 1_000_000, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reduced) != 0 {
		t.Errorf("expected no reductions, got %v", reduced)
	}
	if fitted.ContextFitted {
		t.Error("ContextFitted should be false when no reduction needed")
	}
	if fitted.SiblingContext != data.SiblingContext {
		t.Error("SiblingContext should be unchanged")
	}
}

func TestFitToBudget_ReducesSiblingContextFirst_Implement(t *testing.T) {
	data := makeData()
	data.SiblingContext = strings.Repeat("x", 5000)

	// Compute the budget by rendering what the output should look like
	// after SiblingContext is removed and the manifest note is injected.
	expected := data
	expected.SiblingContext = ""
	expected.ContextFitted = true
	expected.ManifestNote = "[context-fitted] The following sections were reduced to fit the context window: SiblingContext. Use file-read and search tools to retrieve any missing context you need."
	renderedExpected, _ := RenderPrompt(simpleTmpl, expected)
	budget := estimateTokens(len(renderedExpected), 3.3) + 5

	rendered, _ := RenderPrompt(simpleTmpl, data)
	fullTokens := estimateTokens(len(rendered), 3.3)

	fitted, reduced, err := fitToBudget(simpleTmpl, data, "implement", budget, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fullTokens <= budget {
		t.Skip("budget too large for this test")
	}
	if fitted.SiblingContext != "" {
		t.Error("SiblingContext should be cleared")
	}
	if !fitted.ContextFitted {
		t.Error("ContextFitted should be true")
	}
	if fitted.ManifestNote == "" {
		t.Error("ManifestNote should be set")
	}
	if len(reduced) == 0 || reduced[0] != "SiblingContext" {
		t.Errorf("first reduction should be SiblingContext, got %v", reduced)
	}
	// Protected fields should remain.
	if fitted.Ticket.Description != data.Ticket.Description {
		t.Error("Description should be protected")
	}
	if fitted.Artifacts.Plan != data.Artifacts.Plan {
		t.Error("Plan should be protected")
	}
}

func TestFitToBudget_ProtectedFieldsNeverRemoved(t *testing.T) {
	data := PromptData{
		Ticket: TicketData{
			Key:                "TEST-1",
			Description:        strings.Repeat("desc ", 2000),
			AcceptanceCriteria: []string{"ac1", "ac2", "ac3"},
		},
		Artifacts: ArtifactData{
			Plan: strings.Repeat("plan ", 2000),
		},
	}
	// Tiny budget — can't fit even protected fields.
	_, reduced, err := fitToBudget(simpleTmpl, data, "implement", 10, 3.3)
	if err == nil {
		t.Fatal("expected ContextBudgetError, got nil")
	}
	var cbe *ContextBudgetError
	if !isContextBudgetError(err, &cbe) {
		t.Fatalf("expected ContextBudgetError, got %T: %v", err, err)
	}
	// No reductions should have been applied (nothing reducible present).
	if len(reduced) != 0 {
		t.Errorf("expected no reductions on protected-only data, got %v", reduced)
	}
}

func TestFitToBudget_ReductionOrderForPatch(t *testing.T) {
	data := makeData()
	// For patch phase, DiffContext should be reduced before SiblingContext.
	data.DiffContext = strings.Repeat("d", 3000)
	data.SiblingContext = strings.Repeat("s", 3000)

	// Compute budget with DiffContext removed + manifest overhead.
	// Add manifestReserveTokens headroom so the loop doesn't over-reduce
	// into SiblingContext while reserving space for the manifest note.
	expected := data
	expected.DiffContext = ""
	expected.ContextFitted = true
	expected.ManifestNote = "[context-fitted] The following sections were reduced to fit the context window: DiffContext. Use file-read and search tools to retrieve any missing context you need."
	renderedExpected, _ := RenderPrompt(simpleTmpl, expected)
	budget := estimateTokens(len(renderedExpected), 3.3) + manifestReserveTokens + 5

	fitted, reduced, err := fitToBudget(simpleTmpl, data, "patch", budget, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reduced) == 0 {
		t.Fatal("expected at least one reduction")
	}
	if reduced[0] != "DiffContext" {
		t.Errorf("patch should reduce DiffContext first, got %v", reduced)
	}
	if fitted.DiffContext != "" {
		t.Error("DiffContext should be cleared")
	}
	// SiblingContext should still be present if budget was met.
	if fitted.SiblingContext == "" {
		t.Error("SiblingContext should be preserved when DiffContext reduction suffices")
	}
}

func TestFitToBudget_ReworkFeedbackTrimOrder(t *testing.T) {
	data := makeData()
	data.ReworkFeedback = &ReworkFeedback{
		Source:  "review",
		Verdict: "rework",
		ReviewFindings: []EnrichedFinding{
			{CodeSnippet: strings.Repeat("snippet ", 500)},
		},
		ImplementDiff: strings.Repeat("rework-diff ", 500),
		FailedCommands: []FailedCommand{
			{Command: "go test", Output: strings.Repeat("output ", 500)},
		},
		PriorCycles: []PriorCycle{
			{Cycle: 1, Source: "review", Verdict: "rework", Summary: strings.Repeat("summary ", 200)},
		},
	}

	rendered, _ := RenderPrompt(simpleTmpl, data)
	fullTokens := estimateTokens(len(rendered), 3.3)

	// Budget that requires dropping snippets but nothing else.
	snippetBytes := len(strings.Repeat("snippet ", 500))
	budget := fullTokens - estimateTokens(snippetBytes, 3.3) + 20

	fitted, reduced, err := fitToBudget(simpleTmpl, data, "implement", budget, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find ReworkFeedback.Snippets in reduced list.
	foundSnippets := false
	for _, r := range reduced {
		if r == "ReworkFeedback.Snippets" {
			foundSnippets = true
		}
	}
	if !foundSnippets {
		t.Errorf("expected ReworkFeedback.Snippets in reduced, got %v", reduced)
	}

	// Snippets should be cleared.
	if fitted.ReworkFeedback.ReviewFindings[0].CodeSnippet != "" {
		t.Error("code snippet should be cleared")
	}
	// ImplementDiff should still be present if snippets sufficed.
	if fitted.ReworkFeedback.ImplementDiff == "" {
		t.Error("ImplementDiff should be preserved when snippet reduction suffices")
	}
}

func TestFitToBudget_DoesNotMutateOriginal(t *testing.T) {
	data := makeData()
	data.ReworkFeedback = &ReworkFeedback{
		Source: "review",
		ReviewFindings: []EnrichedFinding{
			{CodeSnippet: "original snippet"},
		},
		FailedCommands: []FailedCommand{
			{Output: "original output"},
		},
	}
	origSnippet := data.ReworkFeedback.ReviewFindings[0].CodeSnippet
	origOutput := data.ReworkFeedback.FailedCommands[0].Output
	origSiblings := data.SiblingContext
	origExtras := data.Artifacts.Extras["custom"]

	// Tiny budget to force all reductions.
	_, _, _ = fitToBudget(simpleTmpl, data, "implement", 50, 3.3)

	if data.SiblingContext != origSiblings {
		t.Error("original SiblingContext was mutated")
	}
	if data.ReworkFeedback.ReviewFindings[0].CodeSnippet != origSnippet {
		t.Error("original CodeSnippet was mutated")
	}
	if data.ReworkFeedback.FailedCommands[0].Output != origOutput {
		t.Error("original FailedCommands Output was mutated")
	}
	if data.Artifacts.Extras["custom"] != origExtras {
		t.Error("original Extras was mutated")
	}
}

func TestFitToBudget_ManifestNote(t *testing.T) {
	data := makeData()
	data.SiblingContext = strings.Repeat("x", 10000)

	rendered, _ := RenderPrompt(simpleTmpl, data)
	fullTokens := estimateTokens(len(rendered), 3.3)
	budget := fullTokens - estimateTokens(10000, 3.3) + 100

	fitted, _, err := fitToBudget(simpleTmpl, data, "implement", budget, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fitted.ContextFitted {
		t.Error("ContextFitted should be true")
	}
	if !strings.Contains(fitted.ManifestNote, "[context-fitted]") {
		t.Error("ManifestNote should contain [context-fitted] prefix")
	}
	if !strings.Contains(fitted.ManifestNote, "SiblingContext") {
		t.Error("ManifestNote should mention the reduced field")
	}
	if !strings.Contains(fitted.ManifestNote, "tools") {
		t.Error("ManifestNote should mention tools")
	}
}

func TestFitToBudget_ContextBudgetError(t *testing.T) {
	data := PromptData{
		Ticket: TicketData{
			Key:         "TEST-1",
			Description: strings.Repeat("desc ", 5000),
		},
		Artifacts: ArtifactData{
			Plan: strings.Repeat("plan ", 5000),
		},
	}
	_, _, err := fitToBudget(simpleTmpl, data, "implement", 10, 3.3)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cbe *ContextBudgetError
	if !isContextBudgetError(err, &cbe) {
		t.Fatalf("expected ContextBudgetError, got %T: %v", err, err)
	}
	if cbe.Phase != "implement" {
		t.Errorf("expected phase implement, got %s", cbe.Phase)
	}
	if cbe.BudgetTokens != 10 {
		t.Errorf("expected budget 10, got %d", cbe.BudgetTokens)
	}
}

func TestFitToBudget_ZeroBudgetUsesDefault(t *testing.T) {
	data := PromptData{
		Ticket: TicketData{Key: "TEST-1"},
	}
	fitted, reduced, err := fitToBudget(simpleTmpl, data, "implement", 0, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reduced) != 0 {
		t.Errorf("expected no reductions with large default budget, got %v", reduced)
	}
	if fitted.ContextFitted {
		t.Error("ContextFitted should be false")
	}
}

func TestFitToBudget_ArtifactTruncation(t *testing.T) {
	data := PromptData{
		Ticket: TicketData{Key: "TEST-1"},
		Artifacts: ArtifactData{
			Plan:      "protected plan",
			Implement: strings.Repeat("i", 5000), // large enough to truncate
		},
	}

	rendered, _ := RenderPrompt(simpleTmpl, data)
	fullTokens := estimateTokens(len(rendered), 3.3)
	// Budget requires truncating the implement artifact.
	budget := fullTokens - estimateTokens(3000, 3.3)

	fitted, reduced, err := fitToBudget(simpleTmpl, data, "review", budget, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundImpl := false
	for _, r := range reduced {
		if r == "Artifacts.Implement" {
			foundImpl = true
		}
	}
	if !foundImpl {
		t.Errorf("expected Artifacts.Implement in reduced, got %v", reduced)
	}
	if !strings.Contains(fitted.Artifacts.Implement, "[truncated]") {
		t.Error("implement artifact should contain truncation marker")
	}
	if len(fitted.Artifacts.Implement) >= len(data.Artifacts.Implement) {
		t.Error("implement artifact should be shorter after truncation")
	}
}

func TestFitToBudget_CustomPhaseDefaultOrder(t *testing.T) {
	data := makeData()
	data.SiblingContext = strings.Repeat("s", 5000)

	// Compute budget with SiblingContext removed + manifest overhead.
	expected := data
	expected.SiblingContext = ""
	expected.ContextFitted = true
	expected.ManifestNote = "[context-fitted] The following sections were reduced to fit the context window: SiblingContext. Use file-read and search tools to retrieve any missing context you need."
	renderedExpected, _ := RenderPrompt(simpleTmpl, expected)
	budget := estimateTokens(len(renderedExpected), 3.3) + 5

	fitted, reduced, err := fitToBudget(simpleTmpl, data, "custom-phase", budget, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reduced) == 0 {
		t.Fatal("expected at least one reduction")
	}
	// Custom phases should reduce SiblingContext first (default order).
	if reduced[0] != "SiblingContext" {
		t.Errorf("custom phase should reduce SiblingContext first, got %v", reduced)
	}
	if fitted.SiblingContext != "" {
		t.Error("SiblingContext should be cleared")
	}
}

func TestFitToBudget_MultipleReductions(t *testing.T) {
	data := makeData()
	data.SiblingContext = strings.Repeat("s", 3000)
	data.Context.ProjectContext = strings.Repeat("p", 3000)
	data.Artifacts.Extras = map[string]string{"big": strings.Repeat("e", 3000)}

	rendered, _ := RenderPrompt(simpleTmpl, data)
	fullTokens := estimateTokens(len(rendered), 3.3)
	// Budget that requires dropping all three.
	budget := fullTokens - estimateTokens(8500, 3.3)

	fitted, reduced, err := fitToBudget(simpleTmpl, data, "implement", budget, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reduced) < 2 {
		t.Errorf("expected multiple reductions, got %v", reduced)
	}
	if fitted.SiblingContext != "" {
		t.Error("SiblingContext should be cleared")
	}
	if fitted.ManifestNote == "" {
		t.Error("ManifestNote should be set")
	}
}

func TestFitToBudget_ManifestReservePreventsOvershoot(t *testing.T) {
	// Regression: when the last reduction step barely brings the prompt
	// under budget, the manifest note (~30 tokens) can push it back over.
	// The fix reserves headroom for the manifest during the loop, and
	// resumes reducing if the manifest still overshoots.
	data := makeData()
	data.SiblingContext = strings.Repeat("s", 2000)
	data.Context.ProjectContext = strings.Repeat("p", 2000)

	// Set budget so that removing SiblingContext alone brings us under the
	// *real* budget but NOT under (budget - manifestReserve). This means
	// the loop will also remove Context. Without the reserve fix, removing
	// only SiblingContext + injecting the manifest would overshoot.
	withoutSiblings := data
	withoutSiblings.SiblingContext = ""
	renderedWithout, _ := RenderPrompt(simpleTmpl, withoutSiblings)
	tokensWithout := estimateTokens(len(renderedWithout), 3.3)
	// Budget = exact fit without siblings (no manifest headroom).
	budget := tokensWithout + 5

	fitted, reduced, err := fitToBudget(simpleTmpl, data, "implement", budget, 3.3)
	if err != nil {
		t.Fatalf("expected no error (should continue reducing after manifest), got: %v", err)
	}
	if !fitted.ContextFitted {
		t.Error("ContextFitted should be true")
	}
	// Should have reduced at least SiblingContext, possibly Context too.
	if len(reduced) == 0 {
		t.Fatal("expected at least one reduction")
	}
	if reduced[0] != "SiblingContext" {
		t.Errorf("first reduction should be SiblingContext, got %v", reduced)
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		byteLen       int
		bytesPerToken float64
		want          int
	}{
		{0, 3.3, 0},
		{1, 3.3, 1},
		{33, 3.3, 10},
		{100, 0, 30}, // 0 defaults to 3.3
		{100, 4.0, 25},
	}

	for _, tt := range tests {
		got := estimateTokens(tt.byteLen, tt.bytesPerToken)
		if got != tt.want {
			t.Errorf("estimateTokens(%d, %f) = %d, want %d", tt.byteLen, tt.bytesPerToken, got, tt.want)
		}
	}
}

func TestTruncateArtifact(t *testing.T) {
	short := "short text"
	if truncateArtifact(short) != short {
		t.Error("short text should be returned unchanged")
	}

	long := strings.Repeat("a", 2000)
	result := truncateArtifact(long)
	if len(result) >= len(long) {
		t.Error("truncated artifact should be shorter")
	}
	if !strings.Contains(result, "[truncated]") {
		t.Error("truncated artifact should contain marker")
	}
	// First 500 bytes should be preserved.
	if !strings.HasPrefix(result, long[:truncateKeepBytes]) {
		t.Error("first bytes should be preserved")
	}
	// Last 500 bytes should be preserved.
	if !strings.HasSuffix(result, long[len(long)-truncateKeepBytes:]) {
		t.Error("last bytes should be preserved")
	}
}

func TestFitToBudget_ConventionsSurvivesLongerThanProjectContext_Implement(t *testing.T) {
	data := makeData()
	data.Context.ProjectContext = strings.Repeat("project-ctx ", 200)
	data.Context.RepoConventions = strings.Repeat("conventions ", 50)

	// Set a budget that requires dropping ProjectContext but not RepoConventions.
	// Remove ProjectContext+Gotchas worth of tokens from full render.
	withoutProject := data
	withoutProject.Context.ProjectContext = ""
	withoutProject.Context.Gotchas = ""
	withoutProject.SiblingContext = ""
	withoutProject.ContextFitted = true
	withoutProject.ManifestNote = "[context-fitted] The following sections were reduced to fit the context window: SiblingContext, ProjectContext. Use file-read and search tools to retrieve any missing context you need."
	renderedWithout, _ := RenderPrompt(simpleTmpl, withoutProject)
	budget := estimateTokens(len(renderedWithout), 3.3) + manifestReserveTokens + 5

	fitted, reduced, err := fitToBudget(simpleTmpl, data, "implement", budget, 3.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ProjectContext should be gone.
	if fitted.Context.ProjectContext != "" {
		t.Error("ProjectContext should be cleared")
	}
	// RepoConventions should survive.
	if fitted.Context.RepoConventions == "" {
		t.Error("RepoConventions should survive when ProjectContext reduction suffices")
	}

	// Verify reduction order: ProjectContext shed before RepoConventions.
	foundProject := false
	foundConventions := false
	for _, r := range reduced {
		if r == "ProjectContext" {
			foundProject = true
		}
		if r == "RepoConventions" {
			foundConventions = true
		}
	}
	if !foundProject {
		t.Errorf("expected ProjectContext in reduced, got %v", reduced)
	}
	if foundConventions {
		t.Error("RepoConventions should NOT be in reduced list when budget is met without it")
	}
}

func TestFitToBudget_ConventionsShedLast_AllPhases(t *testing.T) {
	// For phases whose templates render RepoConventions, verify that
	// conventionsStep appears after projectContextStep in the reduction order.
	// verify.md does NOT render RepoConventions, so it is excluded — see
	// TestFitToBudget_VerifyPhaseOmitsConventions for that assertion.
	phases := []string{"implement", "review", "patch", "custom-phase"}

	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			steps := phaseReductionOrder(phase)
			projectIdx := -1
			conventionsIdx := -1
			for i, s := range steps {
				if s.label == "ProjectContext" {
					projectIdx = i
				}
				if s.label == "RepoConventions" {
					conventionsIdx = i
				}
			}
			if projectIdx == -1 {
				t.Fatal("ProjectContext step not found")
			}
			if conventionsIdx == -1 {
				t.Fatal("RepoConventions step not found")
			}
			if conventionsIdx <= projectIdx {
				t.Errorf("RepoConventions (idx=%d) should be shed after ProjectContext (idx=%d)", conventionsIdx, projectIdx)
			}
		})
	}
}

func TestFitToBudget_VerifyPhaseOmitsConventions(t *testing.T) {
	// verify.md does not render {{.Context.RepoConventions}}, so the verify
	// phase must not include a conventionsStep. Including it would produce a
	// false manifest note claiming "RepoConventions" was reduced when the
	// model never saw the field.
	steps := phaseReductionOrder("verify")
	for _, s := range steps {
		if s.label == "RepoConventions" {
			t.Fatal("verify phase should not include RepoConventions reduction step — verify.md never renders it")
		}
	}
}

// isContextBudgetError is a helper to check for *ContextBudgetError,
// working around the fact that errors.As requires a pointer-to-pointer.
func isContextBudgetError(err error, target **ContextBudgetError) bool {
	if cbe, ok := err.(*ContextBudgetError); ok {
		*target = cbe
		return true
	}
	return false
}
