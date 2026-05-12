package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/git"
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
(warnings are OK), exits 1 if any validation error is found.

Use --session <ticket> to check schema version compatibility of stored
phase artifacts for a specific session.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			session, _ := cmd.Flags().GetString("session")
			pipelineName, _ := cmd.Flags().GetString("pipeline")
			if session != "" {
				return runValidateSession(cmd.OutOrStdout(), cfg, session, pipelineName)
			}
			return runValidate(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, pipelineName)
		},
	}

	cmd.Flags().String("pipeline", "", "pipeline name (default: phases.yaml)")
	cmd.Flags().String("session", "", "ticket key to check schema version compatibility")

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

	// Stage 6: Convention checklist
	validateConventionChecklist(w, result, cfg)

	// Stage 7: Notify hooks
	validateNotify(w, result, cfg)

	// Stage 8: Transcript config
	validateTranscript(w, result, cfg)

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

// validateConventionChecklist checks whether a convention checklist is
// configured. An empty checklist is a warning (conventions won't be injected
// into prompts); a populated checklist is reported as valid.
func validateConventionChecklist(w io.Writer, result *validationResult, cfg *config.Config) {
	if cfg.ConventionChecklist == "" {
		result.addWarning("convention_checklist: not set — prompts will not include repo conventions")
		fmt.Fprintln(w, "⚠ convention_checklist: not set")
		return
	}
	fmt.Fprintf(w, "✓ convention_checklist: %d bytes\n", len(cfg.ConventionChecklist))
}

// validateNotify checks notify hook configuration for obvious errors.
func validateNotify(w io.Writer, result *validationResult, cfg *config.Config) {
	if cfg.Notify.Webhook == nil && cfg.Notify.Script == nil &&
		cfg.Notify.OnFinish == nil && cfg.Notify.OnFailure == nil {
		fmt.Fprintln(w, "✓ notify: no hooks configured")
		return
	}

	hooks := 0
	if cfg.Notify.Webhook != nil {
		hooks++
		validateNotifyWebhook(result, "notify", cfg.Notify.Webhook)
	}

	if cfg.Notify.Script != nil {
		hooks++
		validateNotifyScript(result, "notify", cfg.Notify.Script)
	}

	if cfg.Notify.OnFinish != nil {
		hooks++
		if cfg.Notify.OnFinish.Webhook != nil {
			validateNotifyWebhook(result, "notify.on_finish", cfg.Notify.OnFinish.Webhook)
		}
		if cfg.Notify.OnFinish.Script != nil {
			validateNotifyScript(result, "notify.on_finish", cfg.Notify.OnFinish.Script)
		}
	}

	if cfg.Notify.OnFailure != nil {
		hooks++
		if cfg.Notify.OnFailure.Webhook != nil {
			validateNotifyWebhook(result, "notify.on_failure", cfg.Notify.OnFailure.Webhook)
		}
		if cfg.Notify.OnFailure.Script != nil {
			validateNotifyScript(result, "notify.on_failure", cfg.Notify.OnFailure.Script)
		}
	}

	fmt.Fprintf(w, "✓ notify: %d hook(s) configured\n", hooks)
}

// validateNotifyWebhook checks a webhook config for obvious errors.
func validateNotifyWebhook(result *validationResult, prefix string, wh *config.WebhookNotifyConfig) {
	if wh.URL == "" {
		result.addWarning("%s: webhook configured but URL is empty", prefix)
	} else if !strings.HasPrefix(wh.URL, "http://") && !strings.HasPrefix(wh.URL, "https://") {
		result.addWarning("%s: webhook URL %q does not start with http:// or https://", prefix, wh.URL)
	}
}

// validateNotifyScript checks a script config for obvious errors, including
// whether the binary exists on disk.
func validateNotifyScript(result *validationResult, prefix string, sc *config.ScriptNotifyConfig) {
	if sc.Command == "" {
		result.addWarning("%s: script configured but command is empty", prefix)
		return
	}
	parts := strings.Fields(sc.Command)
	binary := parts[0]
	if _, err := exec.LookPath(binary); err != nil {
		result.addWarning("%s: script binary %q not found in PATH: %v", prefix, binary, err)
	}
}

// runValidateSession checks schema version compatibility for a stored session.
// It loads the pipeline, reads each completed phase's artifact, extracts the
// _schema_version field, and compares it against the current schema hash.
func runValidateSession(w io.Writer, cfg *config.Config, ticketKey string, pipelineName string) error {
	// Load pipeline.
	phasesPath, cleanup, err := resolvePhasesPath(pipelineName, cfg.PhasesPath)
	if err != nil {
		return fmt.Errorf("validate session: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	pl, err := pipeline.LoadPipeline(phasesPath)
	if err != nil {
		return fmt.Errorf("validate session: %w", err)
	}

	// Resolve state directory relative to repo root, matching runPipeline.
	stateDir := cfg.StateDir
	if !filepath.IsAbs(stateDir) {
		repoRoot, rootErr := git.RepoRoot(context.Background(), ".")
		if rootErr != nil {
			return fmt.Errorf("validate session: resolve repo root: %w", rootErr)
		}
		stateDir = filepath.Join(repoRoot, stateDir)
	}
	sessionDir := filepath.Join(stateDir, ticketKey)
	if _, err := os.Stat(sessionDir); err != nil {
		return fmt.Errorf("validate session: state directory not found for %q: %w", ticketKey, err)
	}

	state, err := pipeline.LoadOrCreate(stateDir, ticketKey)
	if err != nil {
		return fmt.Errorf("validate session: %w", err)
	}

	fmt.Fprintf(w, "Session: %s\n\n", ticketKey)

	hasIssues := false
	for _, phase := range pl.Phases {
		currentVersion := schemas.SchemaVersionFor(phase.Name)

		if !state.IsCompleted(phase.Name) {
			fmt.Fprintf(w, "  %-15s  ⏭  not completed\n", phase.Name)
			continue
		}

		if currentVersion == "" {
			fmt.Fprintf(w, "  %-15s  ⏭  no schema defined\n", phase.Name)
			continue
		}

		storedVersion, err := extractSessionSchemaVersion(state, phase.Name)
		if err != nil {
			fmt.Fprintf(w, "  %-15s  ⚠  cannot read artifact: %v\n", phase.Name, err)
			hasIssues = true
			continue
		}

		if storedVersion == "" {
			fmt.Fprintf(w, "  %-15s  ⚠  no _schema_version (old artifact)\n", phase.Name)
			hasIssues = true
			continue
		}

		if storedVersion == currentVersion {
			fmt.Fprintf(w, "  %-15s  ✓  current (%s)\n", phase.Name, truncateVersion(storedVersion, 8))
		} else {
			fmt.Fprintf(w, "  %-15s  ✗  outdated (stored: %s, current: %s)\n", phase.Name, truncateVersion(storedVersion, 8), truncateVersion(currentVersion, 8))
			hasIssues = true
		}
	}

	fmt.Fprintln(w)
	if hasIssues {
		fmt.Fprintln(w, "Some phases have schema compatibility issues. Use --force on resume to override.")
		return fmt.Errorf("session %s has schema compatibility issues", ticketKey)
	}
	fmt.Fprintln(w, "All phase schemas are current.")
	return nil
}

// extractSessionSchemaVersion reads the _schema_version from a phase result file.
func extractSessionSchemaVersion(state *pipeline.State, phase string) (string, error) {
	data, err := state.ReadResult(phase)
	if err != nil {
		return "", err
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}

	versionRaw, ok := obj["_schema_version"]
	if !ok {
		return "", nil
	}

	var version string
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return "", fmt.Errorf("unmarshal _schema_version: %w", err)
	}
	return version, nil
}

// truncateVersion returns the first n characters of version, or the full
// string when it is shorter than n. This guards against panics on corrupted
// or manually edited _schema_version values.
func truncateVersion(version string, maxLen int) string {
	if len(version) <= maxLen {
		return version
	}
	return version[:maxLen]
}

// validateTranscript checks that the transcript level is a recognized value.
func validateTranscript(w io.Writer, result *validationResult, cfg *config.Config) {
	level := cfg.Transcript.Level
	switch level {
	case "", "off":
		fmt.Fprintln(w, "✓ transcript: off (default)")
	case "tools":
		fmt.Fprintln(w, "✓ transcript: tools")
	case "full":
		fmt.Fprintln(w, "✓ transcript: full")
	default:
		result.addError("transcript: unknown level %q (expected 'tools', 'full', or 'off')", level)
	}
}
