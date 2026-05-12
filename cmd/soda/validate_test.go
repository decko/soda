package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/pipeline"
)

func TestValidationResult_AddErrorAndWarning(t *testing.T) {
	result := &validationResult{}

	if result.hasErrors() {
		t.Error("new validationResult should not have errors")
	}

	result.addWarning("warn %d", 1)
	if result.hasErrors() {
		t.Error("warnings should not count as errors")
	}
	if len(result.warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(result.warnings))
	}

	result.addError("err %s", "test")
	if !result.hasErrors() {
		t.Error("expected hasErrors() to be true after addError")
	}
	if len(result.errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(result.errors))
	}
}

func TestRunValidate_ValidConfig(t *testing.T) {
	// Use a minimal valid config — the real validation stages (phases,
	// prompts, schemas) exercise the embedded defaults.
	cfg := &config.Config{
		TicketSource: "github",
		Mode:         "autonomous",
		Model:        "claude-sonnet-4-20250514",
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")
	if err != nil {
		t.Fatalf("runValidate() returned error: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	output := stdout.String()

	// Should contain all validation stage outputs.
	if !strings.Contains(output, "✓ config: valid") {
		t.Error("expected config valid message")
	}
	if !strings.Contains(output, "✓ phases:") {
		t.Error("expected phases valid message")
	}
	if !strings.Contains(output, "✓ prompts:") {
		t.Error("expected prompts valid message")
	}
	if !strings.Contains(output, "✓ schemas:") {
		t.Error("expected schemas valid message")
	}
	if !strings.Contains(output, "Validation passed") {
		t.Error("expected validation passed message")
	}
}

func TestRunValidate_MissingContextFiles(t *testing.T) {
	cfg := &config.Config{
		Context: []string{"nonexistent-file.md", "also-missing.md"},
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")

	// Missing context files produce warnings, not errors, so validation passes.
	if err != nil {
		t.Fatalf("expected no error (warnings only), got: %v", err)
	}

	errOutput := stderr.String()
	if !strings.Contains(errOutput, "nonexistent-file.md") {
		t.Error("expected warning about nonexistent-file.md")
	}
	if !strings.Contains(errOutput, "also-missing.md") {
		t.Error("expected warning about also-missing.md")
	}
	if !strings.Contains(errOutput, "⚠ warning") {
		t.Error("expected warning prefix")
	}

	output := stdout.String()
	if !strings.Contains(output, "0 of 2") {
		t.Errorf("expected '0 of 2' in context line, got: %s", output)
	}
}

func TestRunValidate_ExistingContextFiles(t *testing.T) {
	dir := t.TempDir()
	ctxFile := filepath.Join(dir, "context.md")
	if err := os.WriteFile(ctxFile, []byte("# Context"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Context: []string{ctxFile},
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")
	if err != nil {
		t.Fatalf("runValidate() error: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "✓ context: 1 file(s) found") {
		t.Errorf("expected '1 file(s) found', got: %s", output)
	}
}

func TestRunValidate_NoContextFiles(t *testing.T) {
	cfg := &config.Config{}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")
	if err != nil {
		t.Fatalf("runValidate() error: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "no context files configured") {
		t.Errorf("expected 'no context files configured', got: %s", output)
	}
}

func TestValidateContextFiles_MixedExistAndMissing(t *testing.T) {
	dir := t.TempDir()
	existingFile := filepath.Join(dir, "exists.md")
	if err := os.WriteFile(existingFile, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Context: []string{existingFile, "missing.md"},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateContextFiles(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("context files should not produce errors, only warnings")
	}
	if len(result.warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(result.warnings))
	}

	output := buf.String()
	if !strings.Contains(output, "1 of 2") {
		t.Errorf("expected '1 of 2', got: %s", output)
	}
}

func TestValidateSchemas_AllPhasesHaveSchemas(t *testing.T) {
	pl := &pipeline.PhasePipeline{
		Phases: []pipeline.PhaseConfig{
			{Name: "triage", Schema: "{}"},
			{Name: "plan", Schema: "{}"},
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateSchemas(&buf, result, pl)

	if result.hasErrors() {
		t.Error("expected no errors")
	}
	if len(result.warnings) > 0 {
		t.Errorf("expected no warnings, got %v", result.warnings)
	}
	if !strings.Contains(buf.String(), "✓ schemas:") {
		t.Errorf("expected schemas valid message, got: %s", buf.String())
	}
}

func TestValidateSchemas_MissingSchemaWarning(t *testing.T) {
	pl := &pipeline.PhasePipeline{
		Phases: []pipeline.PhaseConfig{
			{Name: "triage", Schema: "{}"},
			{Name: "custom-phase", Schema: ""},
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateSchemas(&buf, result, pl)

	if result.hasErrors() {
		t.Error("missing schema should produce warning, not error")
	}
	if len(result.warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(result.warnings))
	}
	if !strings.Contains(result.warnings[0], "custom-phase") {
		t.Errorf("warning should mention custom-phase, got: %s", result.warnings[0])
	}

	output := buf.String()
	if !strings.Contains(output, "⚠ schemas: 1 phase(s) missing schemas") {
		t.Errorf("expected missing schemas summary, got: %s", output)
	}
	if strings.Contains(output, "all phases have schemas") {
		t.Error("should not claim all phases have schemas when some are missing")
	}
}

func TestValidateSinglePrompt_ValidTemplate(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "test.md")
	if err := os.WriteFile(tmplPath, []byte("Hello {{.Ticket.Key}}"), 0644); err != nil {
		t.Fatal(err)
	}

	loader := pipeline.NewPromptLoader(dir)
	result := &validationResult{}
	err := validateSinglePrompt(loader, "test.md", result)
	if err != nil {
		t.Fatalf("expected no error for valid template, got: %v", err)
	}
	if len(result.warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", result.warnings)
	}
}

func TestValidateSinglePrompt_InvalidTemplate(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(tmplPath, []byte("Hello {{.Invalid"), 0644); err != nil {
		t.Fatal(err)
	}

	loader := pipeline.NewPromptLoader(dir)
	result := &validationResult{}
	err := validateSinglePrompt(loader, "bad.md", result)
	if err == nil {
		t.Fatal("expected error for invalid template, got nil")
	}
}

func TestValidateSinglePrompt_MissingFile(t *testing.T) {
	dir := t.TempDir()
	loader := pipeline.NewPromptLoader(dir)
	result := &validationResult{}
	err := validateSinglePrompt(loader, "missing.md", result)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestValidateSinglePrompt_FallbackWarning(t *testing.T) {
	// Create two dirs: "override" with a broken template, "fallback" with a valid one.
	overrideDir := t.TempDir()
	fallbackDir := t.TempDir()

	// Broken override (invalid template syntax).
	if err := os.WriteFile(filepath.Join(overrideDir, "plan.md"), []byte("{{.Bad"), 0644); err != nil {
		t.Fatal(err)
	}
	// Valid fallback.
	if err := os.WriteFile(filepath.Join(fallbackDir, "plan.md"), []byte("Hello {{.Ticket.Key}}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Override dir first, fallback dir second — matches real loader search order.
	loader := pipeline.NewPromptLoader(overrideDir, fallbackDir)
	result := &validationResult{}
	err := validateSinglePrompt(loader, "plan.md", result)
	if err != nil {
		t.Fatalf("expected no error (should fall back), got: %v", err)
	}
	if len(result.warnings) == 0 {
		t.Error("expected a warning about fallback, got none")
	}
}

func TestNewValidateCmd_NoArgs(t *testing.T) {
	cmd := newValidateCmd()
	// Verify the command rejects args.
	cmd.SetArgs([]string{"extra-arg"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when args provided, got nil")
	}
}

func TestRunValidate_QuickFixPipeline(t *testing.T) {
	cfg := &config.Config{
		TicketSource: "github",
		Mode:         "autonomous",
		Model:        "claude-sonnet-4-20250514",
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "quick-fix")
	if err != nil {
		t.Fatalf("runValidate(quick-fix) returned error: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "✓ phases:") {
		t.Error("expected phases valid message for quick-fix")
	}
	if !strings.Contains(output, "3 phases loaded") {
		t.Errorf("expected '3 phases loaded' for quick-fix, got: %s", output)
	}
	if !strings.Contains(output, "Validation passed") {
		t.Error("expected validation passed for quick-fix")
	}
}

func TestRunValidate_DocsOnlyPipeline(t *testing.T) {
	cfg := &config.Config{
		TicketSource: "github",
		Mode:         "autonomous",
		Model:        "claude-sonnet-4-20250514",
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "docs-only")
	if err != nil {
		t.Fatalf("runValidate(docs-only) returned error: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "✓ phases:") {
		t.Error("expected phases valid message for docs-only")
	}
	if !strings.Contains(output, "3 phases loaded") {
		t.Errorf("expected '3 phases loaded' for docs-only, got: %s", output)
	}
	if !strings.Contains(output, "Validation passed") {
		t.Error("expected validation passed for docs-only")
	}
}

func TestRunValidate_ErrorOutput(t *testing.T) {
	// Test that errors go to stderr and success markers go to stdout.
	cfg := &config.Config{
		Context: []string{"absolutely-nonexistent-file-xyz.md"},
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")

	// Warnings don't cause failure.
	if err != nil {
		t.Fatalf("expected success (warnings only), got: %v", err)
	}

	// Warnings should go to stderr.
	if !strings.Contains(stderr.String(), "⚠ warning") {
		t.Error("expected warnings on stderr")
	}

	// Success markers should go to stdout.
	if !strings.Contains(stdout.String(), "✓ config: valid") {
		t.Error("expected config valid on stdout")
	}
}

func TestValidateConventionChecklist_Empty(t *testing.T) {
	cfg := &config.Config{}
	result := &validationResult{}
	var buf bytes.Buffer
	validateConventionChecklist(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("empty checklist should not produce errors")
	}
	if len(result.warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.warnings), result.warnings)
	}
	if !strings.Contains(result.warnings[0], "not set") {
		t.Errorf("warning should mention 'not set', got: %s", result.warnings[0])
	}
	output := buf.String()
	if !strings.Contains(output, "⚠ convention_checklist: not set") {
		t.Errorf("expected '⚠ convention_checklist: not set', got: %s", output)
	}
}

func TestValidateConventionChecklist_Populated(t *testing.T) {
	cfg := &config.Config{
		ConventionChecklist: "- Use table-driven tests\n- Wrap errors with fmt.Errorf\n",
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateConventionChecklist(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("populated checklist should not produce errors")
	}
	if len(result.warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", result.warnings)
	}
	output := buf.String()
	if !strings.Contains(output, "✓ convention_checklist:") {
		t.Errorf("expected '✓ convention_checklist:', got: %s", output)
	}
	if !strings.Contains(output, "bytes") {
		t.Errorf("expected byte count in output, got: %s", output)
	}
}

func TestRunValidate_WithConventionChecklist(t *testing.T) {
	cfg := &config.Config{
		TicketSource:        "github",
		Mode:                "autonomous",
		Model:               "claude-sonnet-4-20250514",
		ConventionChecklist: "- Always use gofmt\n",
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")
	if err != nil {
		t.Fatalf("runValidate() error: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "✓ convention_checklist:") {
		t.Errorf("expected convention_checklist valid message, got: %s", output)
	}
}

func TestRunValidate_WithoutConventionChecklist(t *testing.T) {
	cfg := &config.Config{
		TicketSource: "github",
		Mode:         "autonomous",
		Model:        "claude-sonnet-4-20250514",
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")
	if err != nil {
		t.Fatalf("runValidate() error: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "⚠ convention_checklist: not set") {
		t.Errorf("expected convention_checklist warning, got: %s", output)
	}
	errOutput := stderr.String()
	if !strings.Contains(errOutput, "convention_checklist") {
		t.Errorf("expected convention_checklist warning on stderr, got: %s", errOutput)
	}
}

func TestValidateNotify_NoHooksConfigured(t *testing.T) {
	cfg := &config.Config{}
	result := &validationResult{}
	var buf bytes.Buffer
	validateNotify(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("expected no errors")
	}
	if len(result.warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", result.warnings)
	}
	output := buf.String()
	if !strings.Contains(output, "no hooks configured") {
		t.Errorf("expected 'no hooks configured', got: %s", output)
	}
}

func TestValidateNotify_ValidWebhook(t *testing.T) {
	cfg := &config.Config{
		Notify: config.NotifyConfig{
			Webhook: &config.WebhookNotifyConfig{
				URL: "https://hooks.example.com/soda",
			},
		},
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateNotify(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("expected no errors")
	}
	if len(result.warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", result.warnings)
	}
	output := buf.String()
	if !strings.Contains(output, "1 hook(s) configured") {
		t.Errorf("expected '1 hook(s) configured', got: %s", output)
	}
}

func TestValidateNotify_EmptyWebhookURL(t *testing.T) {
	cfg := &config.Config{
		Notify: config.NotifyConfig{
			Webhook: &config.WebhookNotifyConfig{URL: ""},
		},
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateNotify(&buf, result, cfg)

	if len(result.warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.warnings), result.warnings)
	}
	if !strings.Contains(result.warnings[0], "URL is empty") {
		t.Errorf("warning should mention empty URL, got: %s", result.warnings[0])
	}
}

func TestValidateNotify_BadWebhookScheme(t *testing.T) {
	cfg := &config.Config{
		Notify: config.NotifyConfig{
			Webhook: &config.WebhookNotifyConfig{URL: "ftp://example.com"},
		},
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateNotify(&buf, result, cfg)

	if len(result.warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.warnings), result.warnings)
	}
	if !strings.Contains(result.warnings[0], "does not start with http") {
		t.Errorf("warning should mention scheme, got: %s", result.warnings[0])
	}
}

func TestValidateNotify_EmptyScriptCommand(t *testing.T) {
	cfg := &config.Config{
		Notify: config.NotifyConfig{
			Script: &config.ScriptNotifyConfig{Command: ""},
		},
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateNotify(&buf, result, cfg)

	if len(result.warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.warnings), result.warnings)
	}
	if !strings.Contains(result.warnings[0], "command is empty") {
		t.Errorf("warning should mention empty command, got: %s", result.warnings[0])
	}
}

func TestValidateNotify_BothHooks(t *testing.T) {
	cfg := &config.Config{
		Notify: config.NotifyConfig{
			Webhook: &config.WebhookNotifyConfig{URL: "https://example.com"},
			Script:  &config.ScriptNotifyConfig{Command: "echo done"},
		},
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateNotify(&buf, result, cfg)

	if len(result.warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", result.warnings)
	}
	output := buf.String()
	if !strings.Contains(output, "2 hook(s) configured") {
		t.Errorf("expected '2 hook(s) configured', got: %s", output)
	}
}

func TestRunValidate_WithNotifyHooks(t *testing.T) {
	cfg := &config.Config{
		TicketSource: "github",
		Mode:         "autonomous",
		Model:        "claude-sonnet-4-20250514",
		Notify: config.NotifyConfig{
			Webhook: &config.WebhookNotifyConfig{URL: "https://example.com/hook"},
		},
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")
	if err != nil {
		t.Fatalf("runValidate() error: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "✓ notify: 1 hook(s) configured") {
		t.Errorf("expected notify valid message, got: %s", output)
	}
}

func TestValidateTranscript_Default(t *testing.T) {
	cfg := &config.Config{}
	result := &validationResult{}
	var buf bytes.Buffer
	validateTranscript(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("expected no errors")
	}
	output := buf.String()
	if !strings.Contains(output, "transcript: off (default)") {
		t.Errorf("expected 'transcript: off (default)', got: %s", output)
	}
}

func TestValidateTranscript_Tools(t *testing.T) {
	cfg := &config.Config{
		Transcript: config.TranscriptConfig{Level: "tools"},
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateTranscript(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("expected no errors")
	}
	output := buf.String()
	if !strings.Contains(output, "transcript: tools") {
		t.Errorf("expected 'transcript: tools', got: %s", output)
	}
}

func TestValidateTranscript_Full(t *testing.T) {
	cfg := &config.Config{
		Transcript: config.TranscriptConfig{Level: "full"},
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateTranscript(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("expected no errors")
	}
	output := buf.String()
	if !strings.Contains(output, "transcript: full") {
		t.Errorf("expected 'transcript: full', got: %s", output)
	}
}

func TestValidateTranscript_InvalidLevel(t *testing.T) {
	cfg := &config.Config{
		Transcript: config.TranscriptConfig{Level: "invalid"},
	}
	result := &validationResult{}
	var buf bytes.Buffer
	validateTranscript(&buf, result, cfg)

	if !result.hasErrors() {
		t.Error("expected errors for invalid transcript level")
	}
	found := false
	for _, errMsg := range result.errors {
		if strings.Contains(errMsg, "unknown level") && strings.Contains(errMsg, "invalid") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about unknown level, got: %v", result.errors)
	}
}

func TestRunValidateSession_Current(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir}

	// Create state with current schema version (via WriteResult injection).
	state, err := pipeline.LoadOrCreate(dir, "T-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	_ = state.MarkRunning("triage")
	_ = state.WriteResult("triage", []byte(`{"ticket_key":"T-1","complexity":"low"}`))
	_ = state.MarkCompleted("triage")

	var buf bytes.Buffer
	err = runValidateSession(&buf, cfg, "T-1", "")
	if err != nil {
		t.Fatalf("runValidateSession: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "current") {
		t.Errorf("expected 'current' in output, got %q", output)
	}
	if !strings.Contains(output, "✓") {
		t.Errorf("expected ✓ in output, got %q", output)
	}
}

func TestRunValidateSession_Outdated(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir}

	state, err := pipeline.LoadOrCreate(dir, "T-2")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	_ = state.MarkRunning("triage")
	// Write with fake old schema version.
	resultPath := state.Dir() + "/triage.json"
	_ = os.WriteFile(resultPath, []byte(`{"ticket_key":"T-2","_schema_version":"deadbeef12345678"}`), 0644)
	_ = state.MarkCompleted("triage")

	var buf bytes.Buffer
	err = runValidateSession(&buf, cfg, "T-2", "")
	if err == nil {
		t.Fatal("expected error for outdated schema version")
	}

	output := buf.String()
	if !strings.Contains(output, "outdated") {
		t.Errorf("expected 'outdated' in output, got %q", output)
	}
	if !strings.Contains(output, "✗") {
		t.Errorf("expected ✗ in output, got %q", output)
	}
}

func TestRunValidateSession_NoVersion(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir}

	state, err := pipeline.LoadOrCreate(dir, "T-3")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	_ = state.MarkRunning("triage")
	// Write without _schema_version.
	resultPath := state.Dir() + "/triage.json"
	_ = os.WriteFile(resultPath, []byte(`{"ticket_key":"T-3"}`), 0644)
	_ = state.MarkCompleted("triage")

	var buf bytes.Buffer
	err = runValidateSession(&buf, cfg, "T-3", "")
	if err == nil {
		t.Fatal("expected error for missing schema version")
	}

	output := buf.String()
	if !strings.Contains(output, "no _schema_version") {
		t.Errorf("expected 'no _schema_version' in output, got %q", output)
	}
}

func TestRunValidateSession_MissingDir(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir}

	var buf bytes.Buffer
	err := runValidateSession(&buf, cfg, "NONEXISTENT-999", "")
	if err == nil {
		t.Fatal("expected error for missing session directory")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %q", err.Error())
	}
}

func TestTruncateVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		maxLen  int
		want    string
	}{
		{"full 16-char", "abcdef1234567890", 8, "abcdef12"},
		{"exact length", "abcdef12", 8, "abcdef12"},
		{"short value", "abc", 8, "abc"},
		{"empty string", "", 8, ""},
		{"single char", "x", 8, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateVersion(tt.version, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateVersion(%q, %d) = %q, want %q", tt.version, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestRunValidateSession_ShortVersion(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir}

	state, err := pipeline.LoadOrCreate(dir, "T-SHORT")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	_ = state.MarkRunning("triage")
	// Write with a very short _schema_version to test no panic.
	resultPath := state.Dir() + "/triage.json"
	_ = os.WriteFile(resultPath, []byte(`{"ticket_key":"T-SHORT","_schema_version":"abc"}`), 0644)
	_ = state.MarkCompleted("triage")

	var buf bytes.Buffer
	// Should not panic, even though stored version is shorter than 8 chars.
	err = runValidateSession(&buf, cfg, "T-SHORT", "")
	if err == nil {
		t.Fatal("expected error for mismatched schema version")
	}

	output := buf.String()
	if !strings.Contains(output, "outdated") {
		t.Errorf("expected 'outdated' in output, got %q", output)
	}
}

func TestNewValidateCmd_SessionFlag(t *testing.T) {
	cmd := newValidateCmd()
	flag := cmd.Flags().Lookup("session")
	if flag == nil {
		t.Fatal("--session flag should be registered on validate command")
	}
}

func TestRunValidate_WithTranscriptConfig(t *testing.T) {
	cfg := &config.Config{
		TicketSource: "github",
		Mode:         "autonomous",
		Model:        "claude-sonnet-4-20250514",
		Transcript:   config.TranscriptConfig{Level: "tools"},
	}

	var stdout, stderr bytes.Buffer
	err := runValidate(&stdout, &stderr, cfg, "")
	if err != nil {
		t.Fatalf("runValidate() error: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "✓ transcript: tools") {
		t.Errorf("expected transcript valid message, got: %s", output)
	}
}
