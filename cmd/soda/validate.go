package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/schemas"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check config and phases without running",
		Long: `Validate configuration, phases, prompts, schemas, and context files
without executing the pipeline. Exits 0 if everything is valid
(warnings are OK), exits 1 if any validation error is found.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			pipelineName, _ := cmd.Flags().GetString("pipeline")
			return runValidate(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, pipelineName)
		},
	}

	cmd.Flags().String("pipeline", "", "pipeline name (default: phases.yaml)")

	return cmd
}

// validationResult accumulates errors and warnings across validation stages.
type validationResult struct {
	errors   []string
	warnings []string
}

func (v *validationResult) addError(format string, args ...any) {
	v.errors = append(v.errors, fmt.Sprintf(format, args...))
}

func (v *validationResult) addWarning(format string, args ...any) {
	v.warnings = append(v.warnings, fmt.Sprintf(format, args...))
}

func (v *validationResult) hasErrors() bool {
	return len(v.errors) > 0
}

// runValidate runs all validation stages in order and prints results.
// Returns nil on success (exit 0), non-nil error on validation failure (exit 1).
func runValidate(w io.Writer, errW io.Writer, cfg *config.Config, pipelineName string) error {
	result := &validationResult{}

	// Stage 1: Config is already loaded (loadConfig succeeded).
	fmt.Fprintln(w, "✓ config: valid")

	// Stage 2: Phases
	pl := validatePhases(w, result, pipelineName)

	// Stage 3: Prompts (only if phases loaded)
	if pl != nil {
		validatePrompts(w, result, pl)
	}

	// Stage 4: Schemas (only if phases loaded)
	if pl != nil {
		validateSchemas(w, result, pl)
	}

	// Stage 5: Context files
	validateContextFiles(w, result, cfg)

	// Print summary
	fmt.Fprintln(w)
	for _, warn := range result.warnings {
		fmt.Fprintf(errW, "⚠ warning: %s\n", warn)
	}
	for _, e := range result.errors {
		fmt.Fprintf(errW, "✗ error: %s\n", e)
	}

	if result.hasErrors() {
		fmt.Fprintf(w, "Validation failed: %d error(s), %d warning(s)\n", len(result.errors), len(result.warnings))
		return fmt.Errorf("validation failed with %d error(s)", len(result.errors))
	}

	fmt.Fprintf(w, "Validation passed: %d warning(s)\n", len(result.warnings))
	return nil
}

// validatePhases loads and validates the pipeline config (cross-references, structure).
func validatePhases(w io.Writer, result *validationResult, pipelineName string) *pipeline.PhasePipeline {
	phasesPath, cleanup, err := resolvePhasesPath(pipelineName, "")
	if err != nil {
		result.addError("phases: %v", err)
		return nil
	}
	if cleanup != nil {
		defer cleanup()
	}

	pl, err := pipeline.LoadPipeline(phasesPath)
	if err != nil {
		result.addError("phases: %v", err)
		return nil
	}

	fmt.Fprintf(w, "✓ phases: %d phases loaded\n", len(pl.Phases))
	return pl
}

// validatePrompts loads and validates each phase prompt and reviewer prompt.
func validatePrompts(w io.Writer, result *validationResult, pl *pipeline.PhasePipeline) {
	promptDir, err := extractEmbeddedPrompts()
	if err != nil {
		result.addError("prompts: extract embedded prompts: %v", err)
		return
	}
	defer os.RemoveAll(promptDir)

	// Build the loader with the same search order as the run command.
	loaderDirs := []string{"."}
	configDir, _ := os.UserConfigDir()
	if configDir != "" {
		loaderDirs = append([]string{filepath.Join(configDir, "soda")}, loaderDirs...)
	}
	loaderDirs = append(loaderDirs, promptDir)
	loader := pipeline.NewPromptLoader(loaderDirs...)

	promptErrors := 0
	for _, phase := range pl.Phases {
		// Phase prompt (skip parallel-review phases that have no top-level prompt).
		if phase.Prompt != "" {
			if err := validateSinglePrompt(loader, phase.Prompt, result); err != nil {
				result.addError("prompts: phase %q: %v", phase.Name, err)
				promptErrors++
			}
		}

		// Reviewer prompts.
		for _, reviewer := range phase.Reviewers {
			if reviewer.Prompt != "" {
				label := fmt.Sprintf("%s/%s", phase.Name, reviewer.Name)
				if err := validateSinglePrompt(loader, reviewer.Prompt, result); err != nil {
					result.addError("prompts: reviewer %q: %v", label, err)
					promptErrors++
				}
			}
		}
	}

	if promptErrors == 0 {
		fmt.Fprintln(w, "✓ prompts: all templates valid")
	}
}

// validateSinglePrompt loads a prompt file and validates it as a Go template.
// If the loader fell back from a broken override to the embedded default,
// it records a warning so the user knows their custom file was rejected.
func validateSinglePrompt(loader *pipeline.PromptLoader, name string, result *validationResult) error {
	lr, err := loader.LoadWithSource(name)
	if err != nil {
		return fmt.Errorf("load %s: %w", name, err)
	}

	if lr.Fallback {
		result.addWarning("prompts: %s", lr.FallbackReason)
	}

	if err := pipeline.ValidateTemplate(lr.Content); err != nil {
		return fmt.Errorf("template %s: %w", name, err)
	}

	return nil
}

// validateSchemas checks that each phase has a non-empty schema after
// the resolution done in LoadPipeline (inline or generated).
func validateSchemas(w io.Writer, result *validationResult, pl *pipeline.PhasePipeline) {
	missingSchemas := 0
	for _, phase := range pl.Phases {
		if strings.TrimSpace(phase.Schema) == "" {
			// Check if a generated schema exists.
			if schemas.SchemaFor(phase.Name) == "" {
				result.addWarning("schemas: phase %q has no schema", phase.Name)
				missingSchemas++
			}
		}
	}

	if missingSchemas == 0 {
		fmt.Fprintln(w, "✓ schemas: all phases have schemas")
	} else {
		fmt.Fprintf(w, "⚠ schemas: %d phase(s) missing schemas\n", missingSchemas)
	}
}

// validateContextFiles checks that each configured context file exists.
func validateContextFiles(w io.Writer, result *validationResult, cfg *config.Config) {
	if len(cfg.Context) == 0 {
		fmt.Fprintln(w, "✓ context: no context files configured")
		return
	}

	missing := 0
	for _, path := range cfg.Context {
		if _, err := os.Stat(path); err != nil {
			result.addWarning("context: file %q not found", path)
			missing++
		}
	}

	if missing == 0 {
		fmt.Fprintf(w, "✓ context: %d file(s) found\n", len(cfg.Context))
	} else {
		fmt.Fprintf(w, "✓ context: %d of %d file(s) found (%d missing)\n",
			len(cfg.Context)-missing, len(cfg.Context), missing)
	}
}
