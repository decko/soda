package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptLoader(t *testing.T) {
	t.Run("loads_from_first_directory", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "triage.md"), []byte("triage prompt"), 0644)

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
		os.WriteFile(filepath.Join(override, "plan.md"), []byte("custom plan"), 0644)
		os.WriteFile(filepath.Join(builtin, "plan.md"), []byte("default plan"), 0644)

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
		os.WriteFile(filepath.Join(builtin, "verify.md"), []byte("builtin verify"), 0644)

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

	t.Run("renders_verify_feedback_when_present", func(t *testing.T) {
		tmpl := `{{- if .VerifyFeedback}}
## Verification Feedback
{{.VerifyFeedback}}
{{- end}}`
		data := PromptData{
			VerifyFeedback: "The previous verification failed.\n\nVerdict: FAIL\n\nFixes required:\n- fix the test\n",
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Verification Feedback") {
			t.Errorf("result should contain feedback header, got: %s", result)
		}
		if !strings.Contains(result, "fix the test") {
			t.Errorf("result should contain fix message, got: %s", result)
		}
	})

	t.Run("omits_verify_feedback_when_empty", func(t *testing.T) {
		tmpl := `{{- if .VerifyFeedback}}
## Verification Feedback
{{.VerifyFeedback}}
{{- end}}`
		data := PromptData{}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Verification Feedback") {
			t.Errorf("result should not contain feedback section when empty, got: %s", result)
		}
	})
}
