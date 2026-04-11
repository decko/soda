package claude

import "strconv"

// buildArgs constructs the CLI argument list from RunOpts and model.
func buildArgs(opts RunOpts, model string) []string {
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

	return args
}
