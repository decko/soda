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
				"--output-format", "stream-json",
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
				"--output-format", "stream-json",
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
		{
			name:  "settings_path_included",
			model: "",
			opts: RunOpts{
				SettingsPath: "/tmp/settings.json",
			},
			contains: []string{
				"--settings-path", "/tmp/settings.json",
			},
		},
		{
			name:  "empty_settings_path_omits_flag",
			model: "",
			opts:  RunOpts{SettingsPath: ""},
			excludes: []string{
				"--settings-path",
			},
		},
		{
			name:  "mcp_config_path_emits_flag",
			model: "",
			opts: RunOpts{
				MCPConfigPath: "/tmp/mcp-config.json",
			},
			contains: []string{
				"--mcp-config", "/tmp/mcp-config.json",
			},
		},
		{
			name:  "strict_mcp_config_emits_flag",
			model: "",
			opts: RunOpts{
				MCPConfigPath:   "/tmp/mcp-config.json",
				StrictMCPConfig: true,
			},
			contains: []string{
				"--mcp-config", "/tmp/mcp-config.json",
				"--strict-mcp-config",
			},
		},
		{
			name:  "empty_mcp_config_omits_flags",
			model: "",
			opts:  RunOpts{},
			excludes: []string{
				"--mcp-config",
				"--strict-mcp-config",
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

func TestCompareCLIVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.1.81", "2.1.81", 0},  // equal versions
		{"2.1.80", "2.1.81", -1}, // patch-level less
		{"2.1.82", "2.1.81", 1},  // patch-level greater
		{"2.0.100", "2.1.0", -1}, // minor boundary crossing
		{"3.0.0", "2.99.99", 1},  // major boundary crossing
		{"1.0.0", "2.1.81", -1},  // large version gap
	}
	for _, tt := range tests {
		got := compareCLIVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareCLIVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
