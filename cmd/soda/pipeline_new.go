package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newPipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Manage pipeline configurations",
		Long:  `Create and manage custom pipeline configuration files.`,
	}

	cmd.AddCommand(newPipelineNewCmd())

	return cmd
}

func newPipelineNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Scaffold a custom pipeline definition",
		Long: `Generate a new pipeline definition file (phases-<name>.yaml) in the
current directory. The file contains a commented starter template
with implement, verify, and submit phases that you can customise.

Examples:
  soda pipeline new hotfix
  soda pipeline new ci-lite --force
  soda pipeline new experiment --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			outputDir, _ := cmd.Flags().GetString("dir")
			return runPipelineNew(cmd.OutOrStdout(), args[0], pipelineNewOptions{
				Force:     force,
				DryRun:    dryRun,
				OutputDir: outputDir,
			})
		},
	}

	cmd.Flags().Bool("force", false, "overwrite existing pipeline file")
	cmd.Flags().Bool("dry-run", false, "print generated pipeline to stdout without writing")
	cmd.Flags().String("dir", "", "output directory (default: current directory)")

	return cmd
}

// pipelineNewOptions groups flag-driven parameters for runPipelineNew.
type pipelineNewOptions struct {
	Force     bool
	DryRun    bool
	OutputDir string
}

// runPipelineNew generates a scaffold pipeline definition for the given name.
// The generated file is written as phases-<name>.yaml in the output directory.
func runPipelineNew(w io.Writer, name string, opts pipelineNewOptions) error {
	if err := pipeline.ValidatePipelineName(name); err != nil {
		return fmt.Errorf("pipeline new: %w", err)
	}

	if name == "default" {
		return fmt.Errorf("pipeline new: %q is reserved; use 'soda init --phases' to scaffold the default pipeline", name)
	}

	content, err := renderPipelineScaffold(name)
	if err != nil {
		return fmt.Errorf("pipeline new: render scaffold: %w", err)
	}

	if opts.DryRun {
		_, writeErr := w.Write([]byte(content))
		return writeErr
	}

	outputDir := opts.OutputDir
	if outputDir == "" {
		outputDir = "."
	}

	filename := pipeline.PipelineFilename(name)
	destPath := filepath.Join(outputDir, filename)

	absPath, err := filepath.Abs(destPath)
	if err != nil {
		return fmt.Errorf("pipeline new: resolve path: %w", err)
	}

	if !opts.Force {
		if _, err := os.Stat(absPath); err == nil {
			return fmt.Errorf("pipeline file already exists: %s (use --force to overwrite)", absPath)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("pipeline new: stat %s: %w", absPath, err)
		}
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("pipeline new: create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("pipeline new: write file: %w", err)
	}

	fmt.Fprintf(w, "Pipeline %q created: %s\n", name, absPath)
	fmt.Fprintf(w, "Run with: soda run <ticket> --pipeline %s\n", name)

	return nil
}

// pipelineScaffoldTemplate is the Go template used to generate a new pipeline
// definition file. It creates a three-phase pipeline (implement → verify →
// submit) as a starting point that users can customise.
const pipelineScaffoldTemplate = `# SODA Pipeline: {{ .Name }}
#
# Custom pipeline definition. Edit phases, tools, timeouts, and
# dependencies to match your workflow.
#
# Usage:
#   soda run <ticket> --pipeline {{ .Name }}
#
# Docs: https://github.com/decko/soda#pipelines

phases:
  - name: implement
    prompt: prompts/implement.md
    # schema: uses generated schema from schemas package
    tools:
      - Read
      - Write
      - Edit
      - Glob
      - Grep
      - Bash
    timeout: 15m
    retry:
      transient: 2
      parse: 1
      semantic: 0
    depends_on: []

  - name: verify
    prompt: prompts/verify.md
    # schema: uses generated schema from schemas package
    tools:
      - Read
      - Glob
      - Grep
      - Bash
    timeout: 5m
    retry:
      transient: 2
      parse: 1
      semantic: 1
    depends_on:
      - implement

  - name: submit
    prompt: prompts/submit.md
    # schema: uses generated schema from schemas package
    tools:
      - Bash(git:*)
      - Bash(gh:*)
      - Bash(glab:*)
    timeout: 3m
    retry:
      transient: 2
      parse: 1
      semantic: 0
    depends_on:
      - implement
      - verify
`

// renderPipelineScaffold executes the scaffold template with the given
// pipeline name and returns the rendered YAML content.
func renderPipelineScaffold(name string) (string, error) {
	tmpl, err := template.New("pipeline-scaffold").Parse(pipelineScaffoldTemplate)
	if err != nil {
		return "", fmt.Errorf("parse scaffold template: %w", err)
	}

	data := struct {
		Name string
	}{
		Name: name,
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute scaffold template: %w", err)
	}

	return buf.String(), nil
}
