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
	"gopkg.in/yaml.v3"
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
			from, _ := cmd.Flags().GetString("from")
			return runPipelineNew(cmd.OutOrStdout(), args[0], pipelineNewOptions{
				Force:     force,
				DryRun:    dryRun,
				OutputDir: outputDir,
				From:      from,
			})
		},
	}

	cmd.Flags().Bool("force", false, "overwrite existing pipeline file")
	cmd.Flags().Bool("dry-run", false, "print generated pipeline to stdout without writing")
	cmd.Flags().String("dir", "", "output directory (default: current directory)")
	cmd.Flags().String("from", "", "load an existing pipeline as template (file path or embedded pipeline name)")

	return cmd
}

// pipelineNewOptions groups flag-driven parameters for runPipelineNew.
type pipelineNewOptions struct {
	Force     bool
	DryRun    bool
	OutputDir string
	From      string // --from: load an existing pipeline as template
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

	var content string
	var err error
	if opts.From != "" {
		content, err = renderFromTemplate(name, opts.From)
	} else {
		content, err = renderPipelineScaffold(name)
	}
	if err != nil {
		return fmt.Errorf("pipeline new: %w", err)
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

// loadFromSource returns the raw YAML bytes for the given --from source.
// If the source contains a path separator or ends in ".yaml" it is treated
// as a file path and read from disk; otherwise it is looked up as a known
// embedded pipeline name (or "default").
func loadFromSource(from string) ([]byte, error) {
	if strings.ContainsAny(from, `/\`) || strings.HasSuffix(from, ".yaml") {
		data, err := os.ReadFile(from)
		if err != nil {
			return nil, fmt.Errorf("read pipeline file %q: %w", from, err)
		}
		return data, nil
	}
	if from == "default" {
		return embeddedPhases, nil
	}
	embeddedPath, ok := knownEmbeddedPipelines[from]
	if !ok {
		return nil, fmt.Errorf("pipeline %q not found: not a file path or known embedded pipeline name", from)
	}
	data, err := fs.ReadFile(embeddedPipelines, embeddedPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded pipeline %q: %w", from, err)
	}
	return data, nil
}

// renderFromTemplate loads a source pipeline and re-serializes it with a
// new header for the given name. The source may be a file path or an
// embedded pipeline name (see loadFromSource).
func renderFromTemplate(name, from string) (string, error) {
	data, err := loadFromSource(from)
	if err != nil {
		return "", err
	}

	var src struct {
		Phases []map[string]interface{} `yaml:"phases"`
	}
	if err := yaml.Unmarshal(data, &src); err != nil {
		return "", fmt.Errorf("parse source pipeline %q: %w", from, err)
	}
	if len(src.Phases) == 0 {
		return "", fmt.Errorf("source pipeline %q has no phases", from)
	}

	out, err := yaml.Marshal(map[string]interface{}{"phases": src.Phases})
	if err != nil {
		return "", fmt.Errorf("serialize pipeline: %w", err)
	}

	var header strings.Builder
	fmt.Fprintf(&header, "# SODA Pipeline: %s\n#\n# Template: %s\n#\n", name, from)
	fmt.Fprintf(&header, "# Usage:\n#   soda run <ticket> --pipeline %s\n#\n", name)
	header.WriteString("# Docs: https://github.com/decko/soda#pipelines\n\n")

	return header.String() + string(out), nil
}

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
