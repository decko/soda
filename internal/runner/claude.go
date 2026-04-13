package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/decko/soda/internal/claude"
)

// ClaudeRunner implements Runner by delegating to a claude.Runner.
type ClaudeRunner struct {
	inner *claude.Runner
}

// NewClaudeRunner creates a ClaudeRunner backed by the Claude Code CLI.
func NewClaudeRunner(binary, model, workDir string) (*ClaudeRunner, error) {
	inner, err := claude.NewRunner(binary, model, workDir)
	if err != nil {
		return nil, err
	}
	return &ClaudeRunner{inner: inner}, nil
}

// Run maps runner.RunOpts to claude.RunOpts, invokes the CLI, and maps the result back.
func (r *ClaudeRunner) Run(ctx context.Context, opts RunOpts) (*RunResult, error) {
	claudeOpts := claude.RunOpts{
		OutputSchema: opts.OutputSchema,
		AllowedTools: opts.AllowedTools,
		Prompt:       opts.UserPrompt,
		Timeout:      opts.Timeout,
	}

	if opts.MaxBudgetUSD > 0 {
		budget := opts.MaxBudgetUSD
		claudeOpts.MaxBudgetUSD = &budget
	}

	// claude.Runner expects a file path for the system prompt, not content.
	// Write content to a temp file and clean up after.
	if opts.SystemPrompt != "" {
		tmpPath, err := writeSystemPromptFile(opts.WorkDir, opts.SystemPrompt)
		if err != nil {
			return nil, fmt.Errorf("claude runner: write system prompt: %w", err)
		}
		defer os.Remove(tmpPath)
		claudeOpts.SystemPromptPath = tmpPath
	}

	result, err := r.inner.Stream(ctx, claudeOpts, nil)
	if err != nil {
		return nil, err
	}

	return &RunResult{
		Output:     result.Output,
		RawText:    result.Result,
		CostUSD:    result.CostUSD,
		TokensIn:   result.Tokens.InputTokens,
		TokensOut:  result.Tokens.OutputTokens,
		DurationMs: result.Duration.Milliseconds(),
		Turns:      result.Turns,
	}, nil
}

// writeSystemPromptFile writes content to a temp file in dir and returns its absolute path.
func writeSystemPromptFile(dir, content string) (string, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(abs, "soda-system-prompt-*.md")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
