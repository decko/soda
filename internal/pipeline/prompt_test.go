package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/schemas"
)

func TestPromptLoader(t *testing.T) {
	t.Run("loads_from_first_directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "triage.md"), []byte("triage prompt"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(dir)
		content, err := loader.Load("triage.md")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if content != "triage prompt" {
			t.Errorf("content = %q, want %q", content, "triage prompt")
		}
	})

	t.Run("prefers_first_directory_override", func(t *testing.T) {
		override := t.TempDir()
		builtin := t.TempDir()
		if err := os.WriteFile(filepath.Join(override, "plan.md"), []byte("custom plan"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(builtin, "plan.md"), []byte("default plan"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(override, builtin)
		content, err := loader.Load("plan.md")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if content != "custom plan" {
			t.Errorf("content = %q, want %q", content, "custom plan")
		}
	})

	t.Run("falls_back_to_second_directory", func(t *testing.T) {
		override := t.TempDir() // empty
		builtin := t.TempDir()
		if err := os.WriteFile(filepath.Join(builtin, "verify.md"), []byte("builtin verify"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(override, builtin)
		content, err := loader.Load("verify.md")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if content != "builtin verify" {
			t.Errorf("content = %q, want %q", content, "builtin verify")
		}
	})

	t.Run("errors_on_not_found", func(t *testing.T) {
		loader := NewPromptLoader(t.TempDir())
		_, err := loader.Load("nonexistent.md")
		if err == nil {
			t.Fatal("expected error for missing template")
		}
	})

	t.Run("rejects_path_traversal", func(t *testing.T) {
		dir := t.TempDir()
		loader := NewPromptLoader(dir)
		_, err := loader.Load("../../../etc/passwd")
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
	})
}

func TestLoadWithSource(t *testing.T) {
	t.Run("returns_override_source", func(t *testing.T) {
		override := t.TempDir()
		builtin := t.TempDir()
		if err := os.WriteFile(filepath.Join(override, "plan.md"), []byte("custom {{.Ticket.Key}}"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(builtin, "plan.md"), []byte("default plan"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(override, builtin)
		result, err := loader.LoadWithSource("plan.md")
		if err != nil {
			t.Fatalf("LoadWithSource: %v", err)
		}
		if result.Content != "custom {{.Ticket.Key}}" {
			t.Errorf("content = %q, want custom override", result.Content)
		}
		if !result.IsOverride {
			t.Error("expected IsOverride = true for first-dir match")
		}
		if result.Fallback {
			t.Error("expected Fallback = false when override is valid")
		}
		if !strings.HasSuffix(result.Source, "plan.md") {
			t.Errorf("source = %q, want path ending in plan.md", result.Source)
		}
	})

	t.Run("returns_embedded_source", func(t *testing.T) {
		override := t.TempDir() // empty
		builtin := t.TempDir()
		if err := os.WriteFile(filepath.Join(builtin, "verify.md"), []byte("builtin verify"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(override, builtin)
		result, err := loader.LoadWithSource("verify.md")
		if err != nil {
			t.Fatalf("LoadWithSource: %v", err)
		}
		if result.Content != "builtin verify" {
			t.Errorf("content = %q, want builtin verify", result.Content)
		}
		if result.IsOverride {
			t.Error("expected IsOverride = false for last-dir match")
		}
	})

	t.Run("falls_back_on_invalid_override_syntax", func(t *testing.T) {
		override := t.TempDir()
		builtin := t.TempDir()
		if err := os.WriteFile(filepath.Join(override, "bad.md"), []byte("{{.Invalid}"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(builtin, "bad.md"), []byte("fallback content"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(override, builtin)
		result, err := loader.LoadWithSource("bad.md")
		if err != nil {
			t.Fatalf("LoadWithSource: %v", err)
		}
		if result.Content != "fallback content" {
			t.Errorf("content = %q, want fallback content", result.Content)
		}
		if result.IsOverride {
			t.Error("expected IsOverride = false after fallback to last dir")
		}
		if !result.Fallback {
			t.Error("expected Fallback = true when override was invalid")
		}
		if result.FallbackReason == "" {
			t.Error("expected non-empty FallbackReason")
		}
	})

	t.Run("falls_back_on_invalid_override_field", func(t *testing.T) {
		override := t.TempDir()
		builtin := t.TempDir()
		if err := os.WriteFile(filepath.Join(override, "plan.md"), []byte("{{.NonExistentField}}"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(builtin, "plan.md"), []byte("default plan"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(override, builtin)
		result, err := loader.LoadWithSource("plan.md")
		if err != nil {
			t.Fatalf("LoadWithSource: %v", err)
		}
		if result.Content != "default plan" {
			t.Errorf("content = %q, want default plan", result.Content)
		}
		if !result.Fallback {
			t.Error("expected Fallback = true for invalid field reference")
		}
		if !strings.Contains(result.FallbackReason, "invalid") {
			t.Errorf("FallbackReason = %q, want to contain 'invalid'", result.FallbackReason)
		}
	})

	t.Run("valid_override_with_three_dirs", func(t *testing.T) {
		configDir := t.TempDir()
		workDir := t.TempDir()
		embedDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(configDir, "t.md"), []byte("config override {{.Ticket.Key}}"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workDir, "t.md"), []byte("work override"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(embedDir, "t.md"), []byte("embedded"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(configDir, workDir, embedDir)
		result, err := loader.LoadWithSource("t.md")
		if err != nil {
			t.Fatalf("LoadWithSource: %v", err)
		}
		if result.Content != "config override {{.Ticket.Key}}" {
			t.Errorf("content = %q, want config override", result.Content)
		}
		if !result.IsOverride {
			t.Error("expected IsOverride = true")
		}
	})

	t.Run("cascading_fallback_through_multiple_invalid_overrides", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		dir3 := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir1, "p.md"), []byte("{{.Bad1}"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir2, "p.md"), []byte("{{.Bad2}"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir3, "p.md"), []byte("final good"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(dir1, dir2, dir3)
		result, err := loader.LoadWithSource("p.md")
		if err != nil {
			t.Fatalf("LoadWithSource: %v", err)
		}
		if result.Content != "final good" {
			t.Errorf("content = %q, want final good", result.Content)
		}
		if !result.Fallback {
			t.Error("expected Fallback = true")
		}
	})

	t.Run("single_dir_skips_validation", func(t *testing.T) {
		dir := t.TempDir()
		// Even an invalid template in the only (embedded) dir is returned as-is.
		if err := os.WriteFile(filepath.Join(dir, "only.md"), []byte("raw content no templates"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(dir)
		result, err := loader.LoadWithSource("only.md")
		if err != nil {
			t.Fatalf("LoadWithSource: %v", err)
		}
		if result.IsOverride {
			t.Error("expected IsOverride = false for single dir")
		}
		if result.Fallback {
			t.Error("expected Fallback = false for single dir")
		}
	})

	t.Run("load_delegates_to_load_with_source", func(t *testing.T) {
		override := t.TempDir()
		builtin := t.TempDir()
		if err := os.WriteFile(filepath.Join(override, "x.md"), []byte("{{.Bogus}"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(builtin, "x.md"), []byte("safe content"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		loader := NewPromptLoader(override, builtin)
		content, err := loader.Load("x.md")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if content != "safe content" {
			t.Errorf("Load should have fallen back, got %q", content)
		}
	})
}

func TestValidateTemplate(t *testing.T) {
	t.Run("valid_template_with_fields", func(t *testing.T) {
		tmpl := "Key: {{.Ticket.Key}}\nSummary: {{.Ticket.Summary}}"
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("valid_template_with_conditionals", func(t *testing.T) {
		tmpl := `{{- if .Context.Gotchas}}Gotchas: {{.Context.Gotchas}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("valid_template_with_range", func(t *testing.T) {
		tmpl := `{{range .Ticket.AcceptanceCriteria}}- {{.}}
{{end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("valid_template_with_optional_rework_feedback", func(t *testing.T) {
		tmpl := `{{- if .ReworkFeedback}}Verdict: {{.ReworkFeedback.Verdict}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("valid_template_with_prior_cycles", func(t *testing.T) {
		tmpl := `{{- if .ReworkFeedback}}{{- if .ReworkFeedback.PriorCycles}}{{range .ReworkFeedback.PriorCycles}}Cycle {{.Cycle}}: {{.Summary}}{{end}}{{- end}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("valid_template_with_existing_spec_and_plan", func(t *testing.T) {
		tmpl := `{{- if .Ticket.ExistingSpec}}Spec: {{.Ticket.ExistingSpec}}{{- end}}
{{- if .Ticket.ExistingPlan}}Plan: {{.Ticket.ExistingPlan}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("valid_template_with_detected_stack", func(t *testing.T) {
		tmpl := `{{- if .DetectedStack.Language}}Language: {{.DetectedStack.Language}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("errors_on_syntax_error", func(t *testing.T) {
		err := ValidateTemplate("{{.Invalid}")
		if err == nil {
			t.Fatal("expected error for invalid template syntax")
		}
	})

	t.Run("errors_on_nonexistent_field", func(t *testing.T) {
		err := ValidateTemplate("{{.NonExistentField}}")
		if err == nil {
			t.Fatal("expected error for non-existent field")
		}
	})

	t.Run("errors_on_nested_nonexistent_field", func(t *testing.T) {
		err := ValidateTemplate("{{.Ticket.FakeField}}")
		if err == nil {
			t.Fatal("expected error for non-existent nested field")
		}
	})

	t.Run("valid_empty_template", func(t *testing.T) {
		if err := ValidateTemplate(""); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("valid_plain_text", func(t *testing.T) {
		if err := ValidateTemplate("no template directives here"); err != nil {
			t.Fatalf("ValidateTemplate: %v", err)
		}
	})

	t.Run("valid_template_with_implement_diff", func(t *testing.T) {
		tmpl := `{{- if .ReworkFeedback}}{{- if .ReworkFeedback.ImplementDiff}}Diff: {{.ReworkFeedback.ImplementDiff}}{{- end}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate should accept ImplementDiff: %v", err)
		}
	})

	t.Run("valid_template_with_add_funcmap", func(t *testing.T) {
		tmpl := `{{- if .ReworkFeedback}}{{range $idx, $fix := .ReworkFeedback.FixesRequired}}Finding {{add $idx 1}} of {{len $.ReworkFeedback.FixesRequired}}{{end}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate should accept add FuncMap: %v", err)
		}
	})

	t.Run("valid_template_with_extras", func(t *testing.T) {
		tmpl := `{{- if .Artifacts.Extras}}{{range $name, $content := .Artifacts.Extras}}Phase {{$name}}: {{$content}}{{end}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate should accept Extras: %v", err)
		}
	})

	t.Run("valid_template_with_extras_index", func(t *testing.T) {
		tmpl := `{{- if .Artifacts.Extras}}{{index .Artifacts.Extras "lint"}}{{- end}}`
		if err := ValidateTemplate(tmpl); err != nil {
			t.Fatalf("ValidateTemplate should accept Extras index: %v", err)
		}
	})
}

func TestRenderPrompt(t *testing.T) {
	t.Run("renders_template_with_data", func(t *testing.T) {
		tmpl := "Key: {{.Ticket.Key}}\nSummary: {{.Ticket.Summary}}"
		data := PromptData{
			Ticket: TicketData{
				Key:     "PROJ-42",
				Summary: "Fix the thing",
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "PROJ-42") {
			t.Errorf("result should contain ticket key, got: %s", result)
		}
		if !strings.Contains(result, "Fix the thing") {
			t.Errorf("result should contain summary, got: %s", result)
		}
	})

	t.Run("renders_artifacts", func(t *testing.T) {
		tmpl := "Plan:\n{{.Artifacts.Plan}}"
		data := PromptData{
			Artifacts: ArtifactData{
				Plan: "Step 1: do the thing",
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Step 1: do the thing") {
			t.Errorf("result should contain plan artifact, got: %s", result)
		}
	})

	t.Run("renders_submit_artifact_prurl", func(t *testing.T) {
		tmpl := "URL: {{.Artifacts.Submit.PRURL}}"
		data := PromptData{
			Artifacts: ArtifactData{
				Submit: SubmitArtifact{PRURL: "https://github.com/org/repo/pull/1"},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "https://github.com/org/repo/pull/1") {
			t.Errorf("result should contain PR URL, got: %s", result)
		}
	})

	t.Run("errors_on_invalid_template", func(t *testing.T) {
		_, err := RenderPrompt("{{.Invalid}", PromptData{})
		if err == nil {
			t.Fatal("expected error for invalid template syntax")
		}
	})

	t.Run("renders_conditional_sections", func(t *testing.T) {
		tmpl := `{{- if .Context.Gotchas}}Gotchas: {{.Context.Gotchas}}{{- end}}`
		data := PromptData{
			Context: ContextData{Gotchas: "watch out"},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "watch out") {
			t.Errorf("result should contain gotchas, got: %s", result)
		}

		// With empty gotchas, section should be omitted
		data.Context.Gotchas = ""
		result, err = RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Gotchas") {
			t.Errorf("result should omit empty gotchas section, got: %s", result)
		}
	})

	t.Run("renders_range_over_criteria", func(t *testing.T) {
		tmpl := `{{range .Ticket.AcceptanceCriteria}}- {{.}}
{{end}}`
		data := PromptData{
			Ticket: TicketData{
				AcceptanceCriteria: []string{"AC1", "AC2"},
			},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "- AC1") || !strings.Contains(result, "- AC2") {
			t.Errorf("result should list criteria, got: %s", result)
		}
	})

	t.Run("renders_rework_feedback_when_present", func(t *testing.T) {
		tmpl := `{{- if .ReworkFeedback}}
## Rework Feedback
Verdict: {{.ReworkFeedback.Verdict}}
{{range .ReworkFeedback.FixesRequired}}- {{.}}
{{end}}
{{- end}}`
		data := PromptData{
			ReworkFeedback: &ReworkFeedback{
				Verdict:       "FAIL",
				FixesRequired: []string{"fix the test"},
			},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Rework Feedback") {
			t.Errorf("result should contain feedback header, got: %s", result)
		}
		if !strings.Contains(result, "fix the test") {
			t.Errorf("result should contain fix message, got: %s", result)
		}
	})

	t.Run("omits_rework_feedback_when_nil", func(t *testing.T) {
		tmpl := `{{- if .ReworkFeedback}}
## Rework Feedback
{{- end}}`
		data := PromptData{}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Rework Feedback") {
			t.Errorf("result should not contain feedback section when nil, got: %s", result)
		}
	})

	t.Run("rework_feedback_takes_precedence_over_plan_review_source", func(t *testing.T) {
		// Read the actual embedded implement.md template.
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-1", Summary: "test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-1",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			ReworkFeedback: &ReworkFeedback{
				Source:  "review",
				Verdict: "rework",
				ReviewFindings: []EnrichedFinding{
					{ReviewFinding: schemas.ReviewFinding{Severity: "critical", File: "x.go", Line: 1, Issue: "bad", Suggestion: "fix it", Source: "go-specialist"}},
				},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "the plan takes precedence") {
			t.Error("implement prompt should NOT say 'the plan takes precedence' for review rework feedback")
		}
		if !strings.Contains(result, "the feedback takes precedence") {
			t.Error("implement prompt should say 'the feedback takes precedence' for review rework feedback")
		}
		// Sequential finding format.
		if !strings.Contains(result, "Finding 1 of 1") {
			t.Error("implement prompt should contain sequential 'Finding 1 of 1' header for single review finding")
		}
		if !strings.Contains(result, "Fix this finding, then verify before proceeding") {
			t.Error("implement prompt should contain verify instruction for review finding")
		}
		if !strings.Contains(result, "Do NOT address multiple findings in a single edit") {
			t.Error("implement prompt should contain sequential fix instruction for review source")
		}
	})

	t.Run("rework_feedback_takes_precedence_over_plan_verify_source", func(t *testing.T) {
		// Read the actual embedded implement.md template.
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-1", Summary: "test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-1",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			ReworkFeedback: &ReworkFeedback{
				Source:        "verify",
				Verdict:       "FAIL",
				FixesRequired: []string{"fix the failing test"},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "the plan takes precedence") {
			t.Error("implement prompt should NOT say 'the plan takes precedence' for verify rework feedback")
		}
		if !strings.Contains(result, "the feedback takes precedence") {
			t.Error("implement prompt should say 'the feedback takes precedence' for verify rework feedback")
		}
		// Sequential finding format.
		if !strings.Contains(result, "Finding 1 of 1") {
			t.Error("implement prompt should contain sequential 'Finding 1 of 1' header for single verify fix")
		}
		if !strings.Contains(result, "Fix this finding, then verify before proceeding") {
			t.Error("implement prompt should contain verify instruction for verify finding")
		}
		if !strings.Contains(result, "Do NOT address multiple findings in a single edit") {
			t.Error("implement prompt should contain sequential fix instruction for verify source")
		}
	})

	t.Run("renders_existing_spec_when_present", func(t *testing.T) {
		tmpl := `{{- if .Ticket.ExistingSpec}}
## Existing Spec
{{.Ticket.ExistingSpec}}
{{- end}}`
		data := PromptData{
			Ticket: TicketData{ExistingSpec: "The spec content here"},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Existing Spec") {
			t.Errorf("result should contain Existing Spec header, got: %s", result)
		}
		if !strings.Contains(result, "The spec content here") {
			t.Errorf("result should contain spec content, got: %s", result)
		}
	})

	t.Run("omits_existing_spec_when_empty", func(t *testing.T) {
		tmpl := `{{- if .Ticket.ExistingSpec}}
## Existing Spec
{{.Ticket.ExistingSpec}}
{{- end}}`
		data := PromptData{}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Existing Spec") {
			t.Errorf("result should not contain Existing Spec when empty, got: %s", result)
		}
	})

	t.Run("renders_existing_plan_when_present", func(t *testing.T) {
		tmpl := `{{- if .Ticket.ExistingPlan}}
## Existing Plan
{{.Ticket.ExistingPlan}}
{{- end}}`
		data := PromptData{
			Ticket: TicketData{ExistingPlan: "Step 1: do stuff\nStep 2: more stuff"},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Existing Plan") {
			t.Errorf("result should contain Existing Plan header, got: %s", result)
		}
		if !strings.Contains(result, "Step 1: do stuff") {
			t.Errorf("result should contain plan content, got: %s", result)
		}
	})

	t.Run("omits_existing_plan_when_empty", func(t *testing.T) {
		tmpl := `{{- if .Ticket.ExistingPlan}}
## Existing Plan
{{.Ticket.ExistingPlan}}
{{- end}}`
		data := PromptData{}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Existing Plan") {
			t.Errorf("result should not contain Existing Plan when empty, got: %s", result)
		}
	})

	t.Run("renders_triage_prompt_with_existing_spec_and_plan", func(t *testing.T) {
		// Read the actual embedded triage.md template.
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "triage.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded triage.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket: TicketData{
				Key:          "TEST-134",
				Summary:      "Test plan routing",
				Type:         "feat",
				Description:  "A test ticket with existing plan",
				ExistingSpec: "The reviewed specification content",
				ExistingPlan: "Task 1: update schema\nTask 2: update prompt",
			},
			Config: PromptConfigData{
				Repos: []RepoConfig{{Name: "soda", Forge: "github", Description: "test repo"}},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Existing Spec (from issue)") {
			t.Errorf("triage prompt should contain Existing Spec section;\ngot: %s", result)
		}
		if !strings.Contains(result, "The reviewed specification content") {
			t.Errorf("triage prompt should contain spec content;\ngot: %s", result)
		}
		if !strings.Contains(result, "Existing Plan (from issue)") {
			t.Errorf("triage prompt should contain Existing Plan section;\ngot: %s", result)
		}
		if !strings.Contains(result, "Task 1: update schema") {
			t.Errorf("triage prompt should contain plan content;\ngot: %s", result)
		}
		if !strings.Contains(result, "skip_plan") {
			t.Errorf("triage prompt should contain plan routing instructions mentioning skip_plan;\ngot: %s", result)
		}
	})

	t.Run("renders_triage_prompt_without_spec_plan_when_absent", func(t *testing.T) {
		// Read the actual embedded triage.md template.
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "triage.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded triage.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket: TicketData{
				Key:         "TEST-134",
				Summary:     "Test no spec/plan",
				Type:        "feat",
				Description: "A ticket without existing spec or plan",
			},
			Config: PromptConfigData{
				Repos: []RepoConfig{{Name: "soda", Forge: "github", Description: "test repo"}},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Existing Spec (from issue)") {
			t.Errorf("triage prompt should NOT contain Existing Spec section when empty;\ngot: %s", result)
		}
		if strings.Contains(result, "Existing Plan (from issue)") {
			t.Errorf("triage prompt should NOT contain Existing Plan section when empty;\ngot: %s", result)
		}
		// Core triage content should still be present.
		if !strings.Contains(result, "TEST-134") {
			t.Errorf("triage prompt should still contain ticket key;\ngot: %s", result)
		}
	})

	t.Run("renders_detected_stack_when_present", func(t *testing.T) {
		tmpl := `{{- if .DetectedStack.Language}}Language: {{.DetectedStack.Language}}
Forge: {{.DetectedStack.Forge}}
Owner: {{.DetectedStack.Owner}}
Repo: {{.DetectedStack.Repo}}
{{- range .DetectedStack.ContextFiles}}
Context: {{.}}
{{- end}}
{{- end}}`
		data := PromptData{
			DetectedStack: DetectedStackData{
				Language:     "go",
				Forge:        "github",
				Owner:        "decko",
				Repo:         "soda",
				ContextFiles: []string{"AGENTS.md", "CLAUDE.md"},
			},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Language: go") {
			t.Errorf("result should contain language, got: %s", result)
		}
		if !strings.Contains(result, "Forge: github") {
			t.Errorf("result should contain forge, got: %s", result)
		}
		if !strings.Contains(result, "Owner: decko") {
			t.Errorf("result should contain owner, got: %s", result)
		}
		if !strings.Contains(result, "Repo: soda") {
			t.Errorf("result should contain repo, got: %s", result)
		}
		if !strings.Contains(result, "Context: AGENTS.md") {
			t.Errorf("result should contain AGENTS.md, got: %s", result)
		}
		if !strings.Contains(result, "Context: CLAUDE.md") {
			t.Errorf("result should contain CLAUDE.md, got: %s", result)
		}
	})

	t.Run("omits_detected_stack_when_empty", func(t *testing.T) {
		tmpl := `{{- if .DetectedStack.Language}}Language: {{.DetectedStack.Language}}{{- end}}`
		data := PromptData{} // zero-value DetectedStack
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Language") {
			t.Errorf("result should not contain Language when DetectedStack is zero-value, got: %s", result)
		}
	})

	t.Run("errors_on_nonexistent_field", func(t *testing.T) {
		_, err := RenderPrompt("{{.NonExistentField}}", PromptData{})
		if err == nil {
			t.Fatal("expected error for non-existent field on PromptData")
		}
	})

	t.Run("renders_patch_prompt_with_verify_feedback", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "patch.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded patch.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-167", Summary: "patch test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-167",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			DiffContext:  "--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,3 @@\n-old\n+new",
			ReworkFeedback: &ReworkFeedback{
				Source:        "verify",
				Verdict:       "FAIL",
				FixesRequired: []string{"fix the test assertion"},
				FailedCriteria: []FailedCriterion{
					{Criterion: "tests pass", Evidence: "exit code 1"},
				},
				CodeIssues: []ReworkCodeIssue{
					{File: "x.go", Line: 10, Severity: "major", Issue: "missing nil check", SuggestedFix: "add nil check before deref"},
				},
				FailedCommands: []FailedCommand{
					{Command: "go test ./...", ExitCode: 1, Output: "FAIL x_test.go"},
				},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		// Should contain key sections.
		checks := []struct {
			desc string
			want string
		}{
			{"ticket key", "TEST-167"},
			{"fix required", "fix the test assertion"},
			{"failed criterion", "tests pass"},
			{"code issue", "missing nil check"},
			{"suggested fix", "add nil check before deref"},
			{"failed command", "go test ./..."},
			{"diff context", "--- a/file.go"},
			{"anti-drift rule", "Do NOT modify files"},
			{"complexity escape", "too_complex"},
		}
		for _, check := range checks {
			if !strings.Contains(result, check.want) {
				t.Errorf("patch prompt should contain %s (%q);\ngot: %s", check.desc, check.want, result)
			}
		}

		// Should NOT contain plan artifact (anti-drift).
		if strings.Contains(result, "Implementation Plan") {
			t.Error("patch prompt should NOT contain Implementation Plan section")
		}
		// Sequential finding format.
		if !strings.Contains(result, "Finding 1 of 1") {
			t.Error("patch prompt should contain sequential 'Finding 1 of 1' header")
		}
		if !strings.Contains(result, "Fix this finding, then verify before proceeding") {
			t.Error("patch prompt should contain verify instruction")
		}
		if !strings.Contains(result, "Do NOT address multiple findings in a single edit") {
			t.Error("patch prompt should contain sequential fix instruction")
		}
	})

	t.Run("renders_patch_prompt_without_feedback", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "patch.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded patch.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-167", Summary: "patch test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-167",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		// Without feedback, should not have FIXES REQUIRED section.
		if strings.Contains(result, "FIXES REQUIRED") {
			t.Error("patch prompt should NOT contain FIXES REQUIRED when no feedback")
		}
	})

	t.Run("renders_implement_prompt_with_prior_review_cycles", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-241", Summary: "prior cycles test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-241",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			ReworkFeedback: &ReworkFeedback{
				Source:  "review",
				Verdict: "rework",
				ReviewFindings: []EnrichedFinding{
					{ReviewFinding: schemas.ReviewFinding{Severity: "major", File: "handler.go", Line: 20, Issue: "missing error check", Suggestion: "add error handling", Source: "go-specialist"}},
				},
				PriorCycles: []PriorCycle{
					{Cycle: 1, Source: "review", Verdict: "rework", Summary: "[critical] handler.go:42 — nil deref"},
				},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		// Should contain prior cycles section.
		if !strings.Contains(result, "Prior Review Cycles") {
			t.Errorf("implement prompt should contain 'Prior Review Cycles' header;\ngot: %s", result)
		}
		if !strings.Contains(result, "Cycle 1") {
			t.Errorf("implement prompt should contain 'Cycle 1';\ngot: %s", result)
		}
		if !strings.Contains(result, "nil deref") {
			t.Errorf("implement prompt should contain prior cycle summary;\ngot: %s", result)
		}
		// Should also contain current findings.
		if !strings.Contains(result, "missing error check") {
			t.Errorf("implement prompt should contain current finding;\ngot: %s", result)
		}
		// Sequential finding format.
		if !strings.Contains(result, "Finding 1 of 1") {
			t.Error("implement prompt should contain sequential 'Finding 1 of 1' header for single review finding with prior cycles")
		}
		if !strings.Contains(result, "Fix this finding, then verify before proceeding") {
			t.Error("implement prompt should contain verify instruction with prior cycles")
		}
	})

	t.Run("renders_implement_prompt_without_prior_cycles_on_first_rework", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-241", Summary: "no prior cycles"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-241",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			ReworkFeedback: &ReworkFeedback{
				Source:  "review",
				Verdict: "rework",
				ReviewFindings: []EnrichedFinding{
					{ReviewFinding: schemas.ReviewFinding{Severity: "critical", File: "x.go", Line: 1, Issue: "bad", Suggestion: "fix", Source: "go-specialist"}},
				},
				// No PriorCycles on first rework.
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		// Should NOT contain prior cycles section.
		if strings.Contains(result, "Prior Review Cycles") {
			t.Errorf("implement prompt should NOT contain 'Prior Review Cycles' on first rework;\ngot: %s", result)
		}
		// Should contain current findings.
		if !strings.Contains(result, "bad") {
			t.Errorf("implement prompt should contain current finding;\ngot: %s", result)
		}
	})

	t.Run("renders_patch_prompt_with_prior_verify_cycles", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "patch.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded patch.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-241", Summary: "patch prior cycles"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-241",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			DiffContext:  "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new",
			ReworkFeedback: &ReworkFeedback{
				Source:        "verify",
				Verdict:       "FAIL",
				FixesRequired: []string{"fix the remaining test"},
				PriorCycles: []PriorCycle{
					{Cycle: 1, Source: "verify", Verdict: "FAIL", Summary: "fix the original test"},
				},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		// Should contain prior cycles section.
		if !strings.Contains(result, "Prior Verification Cycles") {
			t.Errorf("patch prompt should contain 'Prior Verification Cycles' header;\ngot: %s", result)
		}
		if !strings.Contains(result, "fix the original test") {
			t.Errorf("patch prompt should contain prior cycle summary;\ngot: %s", result)
		}
		// Should also contain current fix.
		if !strings.Contains(result, "fix the remaining test") {
			t.Errorf("patch prompt should contain current fix;\ngot: %s", result)
		}
	})

	t.Run("renders_implement_prompt_with_implement_diff", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-282", Summary: "implement diff test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-282",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			ReworkFeedback: &ReworkFeedback{
				Source:  "review",
				Verdict: "rework",
				ReviewFindings: []EnrichedFinding{
					{ReviewFinding: schemas.ReviewFinding{Severity: "critical", File: "handler.go", Line: 42, Issue: "nil deref", Suggestion: "check nil", Source: "go-specialist"}},
				},
				ImplementDiff: "--- a/handler.go\n+++ b/handler.go\n@@ -40,3 +40,5 @@\n func handle() {\n+\treturn obj.Value\n }",
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		// Should contain the diff section.
		if !strings.Contains(result, "Current Implementation Diff") {
			t.Errorf("implement prompt should contain 'Current Implementation Diff' header;\ngot: %s", result)
		}
		if !strings.Contains(result, "--- a/handler.go") {
			t.Errorf("implement prompt should contain the diff content;\ngot: %s", result)
		}
		if !strings.Contains(result, "base...HEAD") {
			t.Errorf("implement prompt should describe the diff as base...HEAD;\ngot: %s", result)
		}
		// Should also contain the review findings.
		if !strings.Contains(result, "nil deref") {
			t.Errorf("implement prompt should still contain review finding;\ngot: %s", result)
		}
	})

	t.Run("omits_implement_diff_when_empty", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-282", Summary: "no diff test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-282",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			ReworkFeedback: &ReworkFeedback{
				Source:  "review",
				Verdict: "rework",
				ReviewFindings: []EnrichedFinding{
					{ReviewFinding: schemas.ReviewFinding{Severity: "critical", File: "x.go", Line: 1, Issue: "bad", Suggestion: "fix", Source: "go-specialist"}},
				},
				// ImplementDiff is empty — should be omitted from output.
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		if strings.Contains(result, "Current Implementation Diff") {
			t.Errorf("implement prompt should NOT contain diff section when ImplementDiff is empty;\ngot: %s", result)
		}
		// Findings should still render.
		if !strings.Contains(result, "bad") {
			t.Errorf("implement prompt should contain current finding;\ngot: %s", result)
		}
	})

	t.Run("omits_implement_diff_without_rework_feedback", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-282", Summary: "no feedback test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-282",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			// No ReworkFeedback at all.
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		if strings.Contains(result, "Current Implementation Diff") {
			t.Errorf("implement prompt should NOT contain diff section without rework feedback;\ngot: %s", result)
		}
	})

	t.Run("renders_implement_prompt_with_diff_and_prior_cycles", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-282", Summary: "diff with prior cycles"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-282",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			ReworkFeedback: &ReworkFeedback{
				Source:  "review",
				Verdict: "rework",
				ReviewFindings: []EnrichedFinding{
					{ReviewFinding: schemas.ReviewFinding{Severity: "major", File: "util.go", Line: 5, Issue: "unchecked error", Suggestion: "handle err", Source: "go-specialist"}},
				},
				ImplementDiff: "--- a/util.go\n+++ b/util.go\n@@ -1 +1 @@\n-old\n+new",
				PriorCycles: []PriorCycle{
					{Cycle: 1, Source: "review", Verdict: "rework", Summary: "[critical] handler.go:42 — nil deref"},
				},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		// Diff should appear before prior cycles.
		diffIdx := strings.Index(result, "Current Implementation Diff")
		priorIdx := strings.Index(result, "Prior Review Cycles")
		findingIdx := strings.Index(result, "Specialist Review Findings")

		if diffIdx < 0 {
			t.Fatal("implement prompt should contain 'Current Implementation Diff'")
		}
		if priorIdx < 0 {
			t.Fatal("implement prompt should contain 'Prior Review Cycles'")
		}
		if findingIdx < 0 {
			t.Fatal("implement prompt should contain 'Specialist Review Findings'")
		}
		if diffIdx > priorIdx {
			t.Error("diff section should appear before prior cycles section")
		}
		if priorIdx > findingIdx {
			t.Error("prior cycles section should appear before findings section")
		}
	})

	t.Run("renders_multi_finding_sequential_indexing", func(t *testing.T) {
		tmplBytes, err := os.ReadFile(filepath.Join("..", "..", "cmd", "soda", "embeds", "prompts", "implement.md"))
		if err != nil {
			t.Skipf("skipping: cannot read embedded implement.md: %v", err)
		}
		tmpl := string(tmplBytes)

		data := PromptData{
			Ticket:       TicketData{Key: "TEST-265", Summary: "multi-finding test"},
			WorktreePath: "/tmp/wt",
			Branch:       "soda/TEST-265",
			BaseBranch:   "main",
			Config:       PromptConfigData{Formatter: "gofmt -w .", TestCommand: "go test ./..."},
			ReworkFeedback: &ReworkFeedback{
				Source:  "review",
				Verdict: "rework",
				ReviewFindings: []EnrichedFinding{
					{ReviewFinding: schemas.ReviewFinding{Severity: "critical", File: "a.go", Line: 1, Issue: "issue-a", Suggestion: "fix-a", Source: "go-specialist"}},
					{ReviewFinding: schemas.ReviewFinding{Severity: "major", File: "b.go", Line: 2, Issue: "issue-b", Suggestion: "fix-b", Source: "go-specialist"}},
					{ReviewFinding: schemas.ReviewFinding{Severity: "minor", File: "c.go", Line: 3, Issue: "issue-c", Suggestion: "fix-c", Source: "go-specialist"}},
				},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}

		// All three sequential headers must be present.
		for _, want := range []string{"Finding 1 of 3", "Finding 2 of 3", "Finding 3 of 3"} {
			if !strings.Contains(result, want) {
				t.Errorf("implement prompt should contain %q;\ngot: %s", want, result)
			}
		}
		// Each finding should have the verify instruction.
		count := strings.Count(result, "Fix this finding, then verify before proceeding")
		if count != 3 {
			t.Errorf("expected 3 verify instructions, got %d;\nresult: %s", count, result)
		}
	})

	t.Run("renders_extras_artifact_with_range", func(t *testing.T) {
		tmpl := `{{- if .Artifacts.Extras}}{{range $name, $content := .Artifacts.Extras}}Custom({{$name}}): {{$content}}
{{end}}{{- end}}`
		data := PromptData{
			Artifacts: ArtifactData{
				Extras: map[string]string{
					"lint":     "All lint checks passed.",
					"security": "No vulnerabilities found.",
				},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Custom(lint): All lint checks passed.") {
			t.Errorf("result should contain lint extra, got: %s", result)
		}
		if !strings.Contains(result, "Custom(security): No vulnerabilities found.") {
			t.Errorf("result should contain security extra, got: %s", result)
		}
	})

	t.Run("renders_extras_artifact_with_index", func(t *testing.T) {
		tmpl := `{{- if .Artifacts.Extras}}Lint: {{index .Artifacts.Extras "lint"}}{{- end}}`
		data := PromptData{
			Artifacts: ArtifactData{
				Extras: map[string]string{
					"lint": "All checks passed.",
				},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Lint: All checks passed.") {
			t.Errorf("result should contain lint value via index, got: %s", result)
		}
	})

	t.Run("omits_extras_when_nil", func(t *testing.T) {
		tmpl := `{{- if .Artifacts.Extras}}Extras present{{- end}}`
		data := PromptData{}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Extras present") {
			t.Errorf("result should not contain extras when nil, got: %s", result)
		}
	})

	t.Run("omits_extras_when_empty_map", func(t *testing.T) {
		tmpl := `{{- if .Artifacts.Extras}}Extras present{{- end}}`
		data := PromptData{
			Artifacts: ArtifactData{
				Extras: map[string]string{},
			},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Extras present") {
			t.Errorf("result should not contain extras when empty map, got: %s", result)
		}
	})
}
