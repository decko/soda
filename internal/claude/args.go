package claude

import "strconv"

// MinCLIVersion is the minimum Claude Code CLI version that supports all
// flags used by SODA. The bottleneck is --bare, introduced in v2.1.81.
//
// Flag introduction timeline:
//
//	--print                            pre-0.2.x
//	--output-format json               ≤ v0.2.66
//	--output-format stream-json        ≤ v0.2.66 (same flag, JSONL variant)
//	--allowed-tools                    pre-1.0.x
//	--system-prompt-file               v1.0.55
//	--permission-mode bypassPermissions ≤ v2.0.x
//	--max-budget-usd                   v2.0.28
//	--json-schema                      ≤ v2.1.21 (fix in v2.1.22)
//	--bare                             v2.1.81 ← bottleneck
const MinCLIVersion = "2.1.81"

// BuildArgs constructs the CLI argument list from RunOpts and model.
// When opts.Model is non-empty it takes precedence over the runner-level
// model, enabling per-phase and per-reviewer model overrides.
func BuildArgs(opts RunOpts, model string) []string {
	args := []string{
		"--print",
		"--bare",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	}

	if opts.SystemPromptPath != "" {
		args = append(args, "--system-prompt-file", opts.SystemPromptPath)
	}
	if opts.SettingsPath != "" {
		args = append(args, "--settings-path", opts.SettingsPath)
	}
	if opts.OutputSchema != "" {
		args = append(args, "--json-schema", opts.OutputSchema)
	}
	// Prefer per-invocation model over runner-level default.
	effectiveModel := model
	if opts.Model != "" {
		effectiveModel = opts.Model
	}
	if effectiveModel != "" {
		args = append(args, "--model", effectiveModel)
	}
	if opts.MaxBudgetUSD != nil {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(*opts.MaxBudgetUSD, 'f', -1, 64))
	}
	for _, tool := range opts.AllowedTools {
		args = append(args, "--allowed-tools", tool)
	}
	// TODO: validate AllowedTools entries against known tool names
	// (Read, Write, Edit, Glob, Grep, Bash, Bash(git:*), etc.)
	// and log warnings for unknown tools. Not blocking — the CLI
	// rejects unknown tools at runtime.

	return args
}
