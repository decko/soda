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
				ReviewFindings: []schemas.ReviewFinding{
					{Severity: "critical", File: "x.go", Line: 1, Issue: "bad", Suggestion: "fix it", Source: "go-specialist"},
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
}
