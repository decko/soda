package claude

import "strconv"

// BuildArgs constructs the CLI argument list from RunOpts and model.
func BuildArgs(opts RunOpts, model string) []string {
	args := []string{
		"--print",
		"--bare",
		"--output-format", "json",
		"--permission-mode", "bypassPermissions",
	}

	if opts.SystemPromptPath != "" {
		args = append(args, "--system-prompt-file", opts.SystemPromptPath)
	}
	if opts.OutputSchema != "" {
		args = append(args, "--json-schema", opts.OutputSchema)
	}
	if model != "" {
		args = append(args, "--model", model)
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
