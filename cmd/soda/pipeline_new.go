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

func newPipelineNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Scaffold a custom pipeline definition",
		Long: `Generate a new pipeline definition file (phases-<name>.yaml) in the
current directory. The file contains a commented starter template
with implement, verify, and submit phases that you can customise.

Examples:
  soda pipelines new hotfix
  soda pipelines new ci-lite --force
  soda pipelines new experiment --dry-run`,
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
//
// This function uses yaml.Node to preserve field ordering, indentation, and
// comments from the source pipeline.
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

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("parse pipeline YAML: %w", err)
	}

	phasesSeq := findPhasesSequence(&doc)
	if phasesSeq == nil || len(phasesSeq.Content) == 0 {
		return "", fmt.Errorf("pipeline has no phases")
	}

	if len(phaseFilter) > 0 {
		filterNodePhases(phasesSeq, phaseFilter)
		if len(phasesSeq.Content) == 0 {
			return "", fmt.Errorf("no phases matched filter: %v", phaseFilter)
		}
	}

	// Strip top-level comments from the source document — we replace
	// them with our own header below.
	stripDocumentComments(&doc)

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", fmt.Errorf("serialize pipeline: %w", err)
	}
	enc.Close()

	var header strings.Builder
	fmt.Fprintf(&header, "# SODA Pipeline: %s\n#\n", name)
	if from != "" {
		fmt.Fprintf(&header, "# Template: %s\n#\n", from)
	}
	fmt.Fprintf(&header, "# Usage:\n#   soda run <ticket> --pipeline %s\n#\n", name)
	header.WriteString("# Docs: https://github.com/decko/soda#pipelines\n\n")

	return header.String() + buf.String(), nil
}

// findPhasesSequence locates the "phases" sequence node in a parsed YAML
// document tree.
func findPhasesSequence(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	// Document node wraps the root content.
	root := doc
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	// Mapping content alternates key, value, key, value...
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "phases" && root.Content[i+1].Kind == yaml.SequenceNode {
			return root.Content[i+1]
		}
	}
	return nil
}

// stripDocumentComments removes head/foot/line comments from the document
// node and its root mapping node so the caller can prepend its own header.
func stripDocumentComments(doc *yaml.Node) {
	if doc == nil {
		return
	}
	doc.HeadComment = ""
	doc.LineComment = ""
	doc.FootComment = ""
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc.Content[0].HeadComment = ""
		doc.Content[0].LineComment = ""
		doc.Content[0].FootComment = ""
	}
}

// nodeMapValue returns the value node for a given key in a MappingNode,
// or nil if the key is not found.
func nodeMapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// phaseName extracts the "name" scalar value from a phase mapping node.
func phaseName(phase *yaml.Node) string {
	v := nodeMapValue(phase, "name")
	if v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// filterNodePhases modifies the phases sequence node in place, keeping only
// phases whose name is in selected and rewriting depends_on entries to
// reference only retained phases.
func filterNodePhases(seq *yaml.Node, selected []string) {
	keep := make(map[string]bool, len(selected))
	for _, s := range selected {
		keep[s] = true
	}

	// Filter phases.
	var kept []*yaml.Node
	for _, phase := range seq.Content {
		if keep[phaseName(phase)] {
			kept = append(kept, phase)
		}
	}
	seq.Content = kept

	// Rewrite depends_on in each retained phase.
	for _, phase := range seq.Content {
		depsNode := nodeMapValue(phase, "depends_on")
		if depsNode == nil || depsNode.Kind != yaml.SequenceNode {
			continue
		}
		var filtered []*yaml.Node
		for _, dep := range depsNode.Content {
			if dep.Kind == yaml.ScalarNode && keep[dep.Value] {
				filtered = append(filtered, dep)
			}
		}
		depsNode.Content = filtered
	}
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
