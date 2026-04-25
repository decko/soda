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

// --- Notification validation tests ---

func TestValidateNotifications_NotConfigured(t *testing.T) {
	cfg := &config.Config{}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if result.hasErrors() {
		t.Error("expected no errors for unconfigured notifications")
	}
	if !strings.Contains(buf.String(), "not configured") {
		t.Errorf("expected 'not configured' message, got: %s", buf.String())
	}
}

func TestValidateNotifications_ValidWebhookURL(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			WebhookURL: "https://hooks.example.com/pipeline",
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if result.hasErrors() {
		t.Errorf("expected no errors, got: %v", result.errors)
	}
	if !strings.Contains(buf.String(), "webhook configured") {
		t.Errorf("expected 'webhook configured' message, got: %s", buf.String())
	}
}

func TestValidateNotifications_InvalidWebhookScheme(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			WebhookURL: "ftp://hooks.example.com/pipeline",
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if !result.hasErrors() {
		t.Error("expected error for non-HTTP scheme")
	}
	if !strings.Contains(result.errors[0], "http or https") {
		t.Errorf("error should mention http/https, got: %s", result.errors[0])
	}
}

func TestValidateNotifications_WebhookNoHost(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			WebhookURL: "https://",
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if !result.hasErrors() {
		t.Error("expected error for URL with no host")
	}
	if !strings.Contains(result.errors[0], "no host") {
		t.Errorf("error should mention 'no host', got: %s", result.errors[0])
	}
}

func TestValidateNotifications_ScriptExists(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "notify.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho ok\n"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Script: scriptPath,
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if result.hasErrors() {
		t.Errorf("expected no errors, got: %v", result.errors)
	}
	if !strings.Contains(buf.String(), "script configured") {
		t.Errorf("expected 'script configured' message, got: %s", buf.String())
	}
}

func TestValidateNotifications_ScriptNotFound(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Script: "/nonexistent/notify.sh",
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if !result.hasErrors() {
		t.Error("expected error for missing script")
	}
	if !strings.Contains(result.errors[0], "not found") {
		t.Errorf("error should mention 'not found', got: %s", result.errors[0])
	}
}

func TestValidateNotifications_ScriptNotExecutable(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "notify.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho ok\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Script: scriptPath,
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if result.hasErrors() {
		t.Errorf("not-executable should be a warning, not error: %v", result.errors)
	}
	if len(result.warnings) == 0 {
		t.Error("expected warning for non-executable script")
	}
	if len(result.warnings) > 0 && !strings.Contains(result.warnings[0], "not executable") {
		t.Errorf("warning should mention 'not executable', got: %s", result.warnings[0])
	}
}

func TestValidateNotifications_OnFailureWebhookValid(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			OnFailureWebhookURL: "https://alerts.example.com/fail",
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if result.hasErrors() {
		t.Errorf("expected no errors, got: %v", result.errors)
	}
	if !strings.Contains(buf.String(), "on_failure webhook configured") {
		t.Errorf("expected 'on_failure webhook configured' message, got: %s", buf.String())
	}
}

func TestValidateNotifications_OnFailureWebhookInvalidScheme(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			OnFailureWebhookURL: "ftp://alerts.example.com/fail",
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if !result.hasErrors() {
		t.Error("expected error for non-HTTP scheme in on_failure webhook")
	}
	if !strings.Contains(result.errors[0], "http or https") {
		t.Errorf("error should mention http/https, got: %s", result.errors[0])
	}
}

func TestValidateNotifications_OnFailureScriptNotFound(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			OnFailureScript: "/nonexistent/fail-notify.sh",
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if !result.hasErrors() {
		t.Error("expected error for missing on_failure script")
	}
	if !strings.Contains(result.errors[0], "not found") {
		t.Errorf("error should mention 'not found', got: %s", result.errors[0])
	}
}

func TestValidateNotifications_OnFailureScriptExists(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fail-notify.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho fail\n"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			OnFailureScript: scriptPath,
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if result.hasErrors() {
		t.Errorf("expected no errors, got: %v", result.errors)
	}
	if !strings.Contains(buf.String(), "on_failure script configured") {
		t.Errorf("expected 'on_failure script configured' message, got: %s", buf.String())
	}
}

func TestValidateNotifications_OnFailureScriptNotExecutable(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fail-notify.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho fail\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			OnFailureScript: scriptPath,
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if result.hasErrors() {
		t.Errorf("not-executable should be a warning, not error: %v", result.errors)
	}
	if len(result.warnings) == 0 {
		t.Error("expected warning for non-executable on_failure script")
	}
	if len(result.warnings) > 0 && !strings.Contains(result.warnings[0], "not executable") {
		t.Errorf("warning should mention 'not executable', got: %s", result.warnings[0])
	}
}

func TestValidateNotifications_AllHandlersConfigured(t *testing.T) {
	dir := t.TempDir()
	finishScript := filepath.Join(dir, "notify.sh")
	failScript := filepath.Join(dir, "fail-notify.sh")
	for _, p := range []string{finishScript, failScript} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho ok\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			WebhookURL:          "https://hooks.example.com/finish",
			Script:              finishScript,
			OnFailureWebhookURL: "https://hooks.example.com/fail",
			OnFailureScript:     failScript,
		},
	}

	result := &validationResult{}
	var buf bytes.Buffer
	validateNotifications(&buf, result, cfg)

	if result.hasErrors() {
		t.Errorf("expected no errors, got: %v", result.errors)
	}
	output := buf.String()
	for _, want := range []string{"webhook", "script", "on_failure webhook", "on_failure script"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q, got: %s", want, output)
		}
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
