package pipeline

import (
	"sort"
	"testing"
)

func TestExtractPromptVersion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{
			name: "version_1",
			raw:  "{{/* soda:prompt-version=1 */}}\nYou are a triage engineer.",
			want: 1,
		},
		{
			name: "version_42",
			raw:  "{{/* soda:prompt-version=42 */}}\nSome content.",
			want: 42,
		},
		{
			name: "no_version_header",
			raw:  "You are a triage engineer.",
			want: 0,
		},
		{
			name: "empty_string",
			raw:  "",
			want: 0,
		},
		{
			name: "version_in_middle",
			raw:  "# Header\n{{/* soda:prompt-version=3 */}}\nContent.",
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractPromptVersion(tt.raw)
			if got != tt.want {
				t.Errorf("ExtractPromptVersion() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEmbeddedPromptVersion(t *testing.T) {
	t.Run("known_prompt", func(t *testing.T) {
		version := EmbeddedPromptVersion("prompts/triage.md")
		if version != 1 {
			t.Errorf("EmbeddedPromptVersion(prompts/triage.md) = %d, want 1", version)
		}
	})

	t.Run("all_known_prompts_have_version_1", func(t *testing.T) {
		knownPrompts := []string{
			"prompts/triage.md",
			"prompts/plan.md",
			"prompts/implement.md",
			"prompts/patch.md",
			"prompts/verify.md",
			"prompts/review.md",
			"prompts/review-go.md",
			"prompts/review-harness.md",
			"prompts/submit.md",
			"prompts/follow-up.md",
			"prompts/monitor.md",
			"prompts/spec.md",
			"prompts/quick-fix-implement.md",
			"prompts/docs-plan.md",
			"prompts/docs-implement.md",
		}
		for _, prompt := range knownPrompts {
			version := EmbeddedPromptVersion(prompt)
			if version != 1 {
				t.Errorf("EmbeddedPromptVersion(%s) = %d, want 1", prompt, version)
			}
		}
	})

	t.Run("unknown_prompt_returns_zero", func(t *testing.T) {
		version := EmbeddedPromptVersion("prompts/custom-phase.md")
		if version != 0 {
			t.Errorf("EmbeddedPromptVersion(prompts/custom-phase.md) = %d, want 0", version)
		}
	})

	t.Run("registry_has_15_entries", func(t *testing.T) {
		if len(embeddedPromptVersions) != 15 {
			t.Errorf("embeddedPromptVersions has %d entries, want 15", len(embeddedPromptVersions))
		}
	})
}

func TestScanPromptDataFields(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		want []string
	}{
		{
			name: "simple_field",
			tmpl: "{{.Ticket.Key}}",
			want: []string{"Ticket"},
		},
		{
			name: "multiple_fields",
			tmpl: "{{.Ticket.Key}} {{.Config.Formatter}} {{.Artifacts.Plan}}",
			want: []string{"Ticket", "Config", "Artifacts"},
		},
		{
			name: "conditional_field",
			tmpl: "{{- if .ReworkFeedback}}Verdict: {{.ReworkFeedback.Verdict}}{{- end}}",
			want: []string{"ReworkFeedback"},
		},
		{
			name: "deduplicates",
			tmpl: "{{.Ticket.Key}} {{.Ticket.Summary}}",
			want: []string{"Ticket"},
		},
		{
			name: "no_fields",
			tmpl: "plain text with no template directives",
			want: nil,
		},
		{
			name: "range_variable_excluded",
			tmpl: "{{range $idx, $fix := .ReworkFeedback.FixesRequired}}{{$fix}}{{end}}",
			want: []string{"ReworkFeedback"},
		},
		{
			name: "range_variable_dot_access_excluded",
			tmpl: "{{range $idx, $finding := .ReworkFeedback.ReviewFindings}}{{$finding.Severity}} {{$finding.File}}{{if $finding.Line}}:{{$finding.Line}}{{end}} — {{$finding.Issue}}{{end}}",
			want: []string{"ReworkFeedback"},
		},
		{
			name: "rebound_dot_inside_range_excluded",
			tmpl: "{{- range .ReworkFeedback.PriorCycles}}{{.Cycle}} {{.Source}}{{- end}}",
			want: []string{"ReworkFeedback"},
		},
		{
			name: "mixed_top_level_and_range_subfields",
			tmpl: "{{.Ticket.Key}} {{range $idx, $ci := .ReworkFeedback.CodeIssues}}{{$ci.Severity}} {{$ci.File}}{{end}} {{.SiblingContext}}",
			want: []string{"Ticket", "ReworkFeedback", "SiblingContext"},
		},
		{
			name: "dollar_root_dot_access",
			tmpl: "{{range $idx, $finding := .ReworkFeedback.ReviewFindings}}{{len $.ReworkFeedback.ReviewFindings}}{{end}}",
			want: []string{"ReworkFeedback"},
		},
		{
			name: "sibling_context",
			tmpl: "{{- if .SiblingContext}}## Siblings\n{{.SiblingContext}}{{- end}}",
			want: []string{"SiblingContext"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanPromptDataFields(tt.tmpl)
			// Sort both for comparison.
			sort.Strings(got)
			sorted := make([]string, len(tt.want))
			copy(sorted, tt.want)
			sort.Strings(sorted)

			if len(got) != len(sorted) {
				t.Fatalf("ScanPromptDataFields() = %v, want %v", got, sorted)
			}
			for idx := range got {
				if got[idx] != sorted[idx] {
					t.Errorf("ScanPromptDataFields()[%d] = %q, want %q", idx, got[idx], sorted[idx])
				}
			}
		})
	}
}

func TestCheckPromptVersion(t *testing.T) {
	t.Run("matching_version_no_warning", func(t *testing.T) {
		raw := "{{/* soda:prompt-version=1 */}}\nYou are a triage engineer."
		warning := CheckPromptVersion("prompts/triage.md", raw)
		if warning != nil {
			t.Errorf("expected no warning for matching version, got: %+v", warning)
		}
	})

	t.Run("missing_version_header_warns", func(t *testing.T) {
		raw := "You are a triage engineer."
		warning := CheckPromptVersion("prompts/triage.md", raw)
		if warning == nil {
			t.Fatal("expected warning for missing version header")
		}
		if warning.ActualVersion != 0 {
			t.Errorf("ActualVersion = %d, want 0", warning.ActualVersion)
		}
		if warning.ExpectedVersion != 1 {
			t.Errorf("ExpectedVersion = %d, want 1", warning.ExpectedVersion)
		}
	})

	t.Run("mismatched_version_warns", func(t *testing.T) {
		raw := "{{/* soda:prompt-version=99 */}}\nYou are a triage engineer."
		warning := CheckPromptVersion("prompts/triage.md", raw)
		if warning == nil {
			t.Fatal("expected warning for mismatched version")
		}
		if warning.ActualVersion != 99 {
			t.Errorf("ActualVersion = %d, want 99", warning.ActualVersion)
		}
		if warning.ExpectedVersion != 1 {
			t.Errorf("ExpectedVersion = %d, want 1", warning.ExpectedVersion)
		}
	})

	t.Run("unknown_prompt_no_warning", func(t *testing.T) {
		raw := "Custom content without version."
		warning := CheckPromptVersion("prompts/custom.md", raw)
		if warning != nil {
			t.Errorf("expected no warning for unknown prompt, got: %+v", warning)
		}
	})

	t.Run("unknown_prompt_with_version_no_warning", func(t *testing.T) {
		raw := "{{/* soda:prompt-version=5 */}}\nCustom content."
		warning := CheckPromptVersion("prompts/custom.md", raw)
		if warning != nil {
			t.Errorf("expected no warning for unknown prompt even with version, got: %+v", warning)
		}
	})
}

func TestPromptVersionWarning(t *testing.T) {
	warning := &PromptVersionWarning{
		PromptPath:      "prompts/triage.md",
		ActualVersion:   0,
		ExpectedVersion: 1,
		Reason:          "template has no soda:prompt-version header",
	}
	if warning.PromptPath != "prompts/triage.md" {
		t.Errorf("PromptPath = %q, want prompts/triage.md", warning.PromptPath)
	}
	if warning.Reason == "" {
		t.Error("expected non-empty Reason")
	}
}
