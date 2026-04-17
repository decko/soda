package claude

import (
	"slices"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	budget := 5.0

	tests := []struct {
		name     string
		opts     RunOpts
		model    string
		contains []string // flag-value pairs that must appear in order
		excludes []string // flags that must NOT appear
	}{
		{
			name:  "full_options",
			model: "claude-opus-4-6",
			opts: RunOpts{
				SystemPromptPath: "/tmp/prompt.md",
				OutputSchema:     `{"type":"object"}`,
				AllowedTools:     []string{"Read", "Glob"},
				MaxBudgetUSD:     &budget,
			},
			contains: []string{
				"--print",
				"--bare",
				"--output-format", "json",
				"--permission-mode", "bypassPermissions",
				"--system-prompt-file", "/tmp/prompt.md",
				"--json-schema", `{"type":"object"}`,
				"--model", "claude-opus-4-6",
				"--max-budget-usd",
				"--allowed-tools", "Read",
				"--allowed-tools", "Glob",
			},
		},
		{
			name:  "minimal_options",
			model: "",
			opts:  RunOpts{},
			contains: []string{
				"--print",
				"--bare",
				"--output-format", "json",
				"--permission-mode", "bypassPermissions",
			},
			excludes: []string{
				"--system-prompt-file",
				"--json-schema",
				"--model",
				"--max-budget-usd",
				"--allowed-tools",
			},
		},
		{
			name:  "nil_budget_omits_flag",
			model: "sonnet",
			opts: RunOpts{
				MaxBudgetUSD: nil,
			},
			excludes: []string{"--max-budget-usd"},
		},
		{
			name:  "per_invocation_model_overrides_runner_model",
			model: "runner-default",
			opts: RunOpts{
				Model: "phase-specific-model",
			},
			contains: []string{
				"--model", "phase-specific-model",
			},
			excludes: []string{
				"runner-default", // runner-level model must not appear
			},
		},
		{
			name:  "empty_opts_model_uses_runner_model",
			model: "runner-default",
			opts: RunOpts{
				Model: "",
			},
			contains: []string{
				"--model", "runner-default",
			},
		},
		{
			name:  "both_empty_omits_model",
			model: "",
			opts:  RunOpts{Model: ""},
			excludes: []string{
				"--model",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := BuildArgs(tt.opts, tt.model)

			for _, want := range tt.contains {
				if !slices.Contains(args, want) {
					t.Errorf("args missing %q\ngot: %v", want, args)
				}
			}
			for _, excluded := range tt.excludes {
				if slices.Contains(args, excluded) {
					t.Errorf("args should not contain %q\ngot: %v", excluded, args)
				}
			}
		})
	}
}
