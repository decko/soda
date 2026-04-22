package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/detect"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/schemas"
	"github.com/spf13/cobra"
)

func newSpecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec <description>",
		Short: "Generate a ticket specification from a description",
		Long:  "Scan the codebase and generate a well-structured ticket specification with context scoping, budget estimation, and acceptance criteria.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}

			fromFile, _ := cmd.Flags().GetString("from-file")
			autoConfirm, _ := cmd.Flags().GetBool("yes")
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			var description string
			switch {
			case fromFile != "":
				data, readErr := os.ReadFile(fromFile)
				if readErr != nil {
					return fmt.Errorf("spec: read file: %w", readErr)
				}
				description = string(data)
			case len(args) > 0:
				description = args[0]
			default:
				return fmt.Errorf("spec: provide a description as argument or use --from-file")
			}

			return runSpec(cmd, cfg, description, autoConfirm, dryRun)
		},
	}

	cmd.Flags().String("from-file", "", "read description from a file")
	cmd.Flags().Bool("yes", false, "skip confirmation and create issue immediately")
	cmd.Flags().Bool("dry-run", false, "generate spec without creating an issue")

	return cmd
}

func runSpec(cmd *cobra.Command, cfg *config.Config, description string, autoConfirm, dryRun bool) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("spec: working directory: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Scanning codebase...\n")

	prompt, err := renderSpecPrompt(ctx, workDir, description)
	if err != nil {
		return fmt.Errorf("spec: render prompt: %w", err)
	}

	schema := schemas.SchemaFor("spec")
	if schema == "" {
		return fmt.Errorf("spec: schema not found for 'spec'")
	}

	if dryRun {
		fmt.Printf("=== System Prompt ===\n\n%s\n", prompt)
		fmt.Printf("\n=== Schema ===\n%s\n", schema)
		return nil
	}

	claudeRunner, err := runner.NewClaudeRunner("claude", cfg.Model, workDir)
	if err != nil {
		return fmt.Errorf("spec: create runner: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Generating spec with Claude...\n")

	result, err := claudeRunner.Run(ctx, runner.RunOpts{
		Phase:        "spec",
		SystemPrompt: prompt,
		UserPrompt:   description,
		OutputSchema: schema,
		AllowedTools: []string{"Read", "Glob", "Grep", "Bash(ls:*)", "Bash(wc:*)", "Bash(head:*)", "Bash(git:*)"},
		WorkDir:      workDir,
		Model:        cfg.Model,
	})
	if err != nil {
		return fmt.Errorf("spec: claude run: %w", err)
	}

	var specOutput schemas.SpecOutput
	if err := json.Unmarshal(result.Output, &specOutput); err != nil {
		return fmt.Errorf("spec: parse output: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\nCost: $%.2f | Tokens: %d in, %d out\n\n", result.CostUSD, result.TokensIn, result.TokensOut)

	fmt.Println(specOutput.TicketBody)

	if dryRun {
		return nil
	}

	if !autoConfirm {
		fmt.Fprintf(os.Stderr, "\nCreate this issue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintf(os.Stderr, "Aborted.\n")
			return nil
		}
	}

	issueURL, err := createGitHubIssue(ctx, specOutput.Title, specOutput.TicketBody, specOutput.SuggestedLabels)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create issue: %v\n", err)
		fmt.Fprintf(os.Stderr, "Spec output printed above — you can create the issue manually.\n")
		return nil
	}

	fmt.Fprintf(os.Stderr, "Created: %s\n", issueURL)
	return nil
}

func renderSpecPrompt(ctx context.Context, workDir, description string) (string, error) {
	promptDir, err := extractEmbeddedPrompts()
	if err != nil {
		return "", fmt.Errorf("extract prompts: %w", err)
	}
	defer os.RemoveAll(promptDir)

	promptPath := filepath.Join(promptDir, "prompts", "spec.md")
	promptContent, err := os.ReadFile(promptPath)
	if err != nil {
		return "", fmt.Errorf("read spec prompt: %w", err)
	}

	tmpl, err := template.New("spec").Parse(string(promptContent))
	if err != nil {
		return "", fmt.Errorf("parse spec prompt: %w", err)
	}

	info, _ := detect.Detect(ctx, workDir)

	data := struct {
		Description     string
		DetectedStack   string
		RepoConventions string
	}{
		Description: description,
	}

	if info != nil {
		var parts []string
		if info.Language != "" {
			parts = append(parts, "Language: "+info.Language)
		}
		if info.Formatter != "" {
			parts = append(parts, "Formatter: "+info.Formatter)
		}
		if info.TestCommand != "" {
			parts = append(parts, "Test: "+info.TestCommand)
		}
		data.DetectedStack = strings.Join(parts, "\n")

		for _, cf := range info.ContextFiles {
			content, readErr := os.ReadFile(filepath.Join(workDir, cf))
			if readErr == nil && len(content) > 0 {
				maxBytes := 4000
				text := string(content)
				if len(text) > maxBytes {
					text = text[:maxBytes] + "\n... (truncated)"
				}
				data.RepoConventions += fmt.Sprintf("\n### %s\n%s\n", cf, text)
			}
		}
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute spec prompt: %w", err)
	}

	return buf.String(), nil
}

func createGitHubIssue(ctx context.Context, title, body string, labels []string) (string, error) {
	args := []string{"issue", "create", "--title", title, "--body", body}
	for _, label := range labels {
		args = append(args, "--label", label)
	}

	cmd := exec.CommandContext(ctx, "gh", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh issue create: %w: %s", err, output)
	}
	return strings.TrimSpace(string(output)), nil
}
