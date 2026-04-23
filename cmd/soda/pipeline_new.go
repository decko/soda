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
			phasesRaw, _ := cmd.Flags().GetString("phases")
			var phases []string
			if phasesRaw != "" {
				for _, p := range strings.Split(phasesRaw, ",") {
					if trimmed := strings.TrimSpace(p); trimmed != "" {
						phases = append(phases, trimmed)
					}
				}
			}
			return runPipelineNew(cmd.OutOrStdout(), args[0], pipelineNewOptions{
				Force:     force,
				DryRun:    dryRun,
				OutputDir: outputDir,
				From:      from,
				Phases:    phases,
			})
		},
	}

	cmd.Flags().Bool("force", false, "overwrite existing pipeline file")
	cmd.Flags().Bool("dry-run", false, "print generated pipeline to stdout without writing")
	cmd.Flags().String("dir", "", "output directory (default: current directory)")
	cmd.Flags().String("from", "", "load an existing pipeline as template (file path or embedded pipeline name)")
	cmd.Flags().String("phases", "", "comma-separated list of phases to include (filters the template)")

	return cmd
}

// pipelineNewOptions groups flag-driven parameters for runPipelineNew.
type pipelineNewOptions struct {
	Force     bool
	DryRun    bool
	OutputDir string
	From      string   // --from: load an existing pipeline as template
	Phases    []string // --phases: restrict output to these phase names
}

// runPipelineNew generates a scaffold pipeline definition for the given name.
// The generated file is written as phases-<name>.yaml in the output directory.
func runPipelineNew(w io.Writer, name string, opts pipelineNewOptions) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("pipeline new: name must not be empty")
	}

	if err := pipeline.ValidatePipelineName(name); err != nil {
		return fmt.Errorf("pipeline new: %w", err)
	}

	if name == "default" {
		return fmt.Errorf("pipeline new: %q is reserved; use 'soda init --phases' to scaffold the default pipeline", name)
	}

	var content string
	var err error
	if opts.From != "" || len(opts.Phases) > 0 {
		content, err = renderFromTemplate(name, opts.From, opts.Phases)
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

// renderFromTemplate builds pipeline content for the given name using either
// a template source (--from) or the built-in scaffold. An optional phase
// filter (--phases) retains only the named phases and rewrites depends_on.
func renderFromTemplate(name, from string, phaseFilter []string) (string, error) {
	var data []byte
	if from != "" {
		var err error
		data, err = loadFromSource(from)
		if err != nil {
			return "", err
		}
	} else {
		// Use the built-in scaffold as the base for phase filtering.
		scaffoldContent, err := renderPipelineScaffold(name)
		if err != nil {
			return "", err
		}
		data = []byte(scaffoldContent)
	}

	var src struct {
		Phases []map[string]interface{} `yaml:"phases"`
	}
	if err := yaml.Unmarshal(data, &src); err != nil {
		return "", fmt.Errorf("parse pipeline YAML: %w", err)
	}
	if len(src.Phases) == 0 {
		return "", fmt.Errorf("pipeline has no phases")
	}

	phases := src.Phases
	if len(phaseFilter) > 0 {
		phases = filterRawPhases(phases, phaseFilter)
		if len(phases) == 0 {
			return "", fmt.Errorf("no phases matched filter: %v", phaseFilter)
		}
	}

	out, err := yaml.Marshal(map[string]interface{}{"phases": phases})
	if err != nil {
		return "", fmt.Errorf("serialize pipeline: %w", err)
	}

	var header strings.Builder
	fmt.Fprintf(&header, "# SODA Pipeline: %s\n#\n", name)
	if from != "" {
		fmt.Fprintf(&header, "# Template: %s\n#\n", from)
	}
	fmt.Fprintf(&header, "# Usage:\n#   soda run <ticket> --pipeline %s\n#\n", name)
	header.WriteString("# Docs: https://github.com/decko/soda#pipelines\n\n")

	return header.String() + string(out), nil
}

// filterRawPhases returns the subset of phases whose name is in selected,
// with depends_on entries pruned to only reference retained phases.
func filterRawPhases(phases []map[string]interface{}, selected []string) []map[string]interface{} {
	keep := make(map[string]bool, len(selected))
	for _, s := range selected {
		keep[s] = true
	}

	var result []map[string]interface{}
	for _, ph := range phases {
		pname, _ := ph["name"].(string)
		if !keep[pname] {
			continue
		}
		// Clone the phase map to avoid mutating the caller's data.
		cloned := make(map[string]interface{}, len(ph))
		for k, v := range ph {
			cloned[k] = v
		}
		// Rewrite depends_on to only reference phases still in the set.
		if deps, ok := cloned["depends_on"].([]interface{}); ok {
			filtered := make([]interface{}, 0, len(deps))
			for _, dep := range deps {
				if depStr, ok := dep.(string); ok && keep[depStr] {
					filtered = append(filtered, dep)
				}
			}
			cloned["depends_on"] = filtered
		}
		result = append(result, cloned)
	}
	return result
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
