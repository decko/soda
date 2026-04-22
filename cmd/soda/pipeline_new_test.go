package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/pipeline"
)

func TestRunPipelineNew_CreatesFile(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "hotfix", pipelineNewOptions{OutputDir: dir})
	if err != nil {
		t.Fatalf("runPipelineNew() error: %v", err)
	}

	destPath := filepath.Join(dir, "phases-hotfix.yaml")
	info, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat created file: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("created file is empty")
	}

	// Output should mention the file and the run command.
	output := buf.String()
	if !strings.Contains(output, "hotfix") {
		t.Errorf("output should mention pipeline name, got: %s", output)
	}
	if !strings.Contains(output, "--pipeline hotfix") {
		t.Errorf("output should show run command, got: %s", output)
	}
}

func TestRunPipelineNew_ValidYAML(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "ci-lite", pipelineNewOptions{OutputDir: dir})
	if err != nil {
		t.Fatalf("runPipelineNew() error: %v", err)
	}

	// The generated file should be loadable as a valid pipeline.
	destPath := filepath.Join(dir, "phases-ci-lite.yaml")
	pl, err := pipeline.LoadPipeline(destPath)
	if err != nil {
		t.Fatalf("LoadPipeline() on scaffold: %v", err)
	}

	if len(pl.Phases) != 3 {
		t.Fatalf("expected 3 phases, got %d", len(pl.Phases))
	}

	wantPhases := []string{"implement", "verify", "submit"}
	for idx, want := range wantPhases {
		if pl.Phases[idx].Name != want {
			t.Errorf("phase[%d] = %q, want %q", idx, pl.Phases[idx].Name, want)
		}
	}
}

func TestRunPipelineNew_NameInHeader(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "experiment", pipelineNewOptions{OutputDir: dir})
	if err != nil {
		t.Fatalf("runPipelineNew() error: %v", err)
	}

	destPath := filepath.Join(dir, "phases-experiment.yaml")
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "# SODA Pipeline: experiment") {
		t.Errorf("header should contain pipeline name, got:\n%s", content)
	}
	if !strings.Contains(content, "--pipeline experiment") {
		t.Errorf("usage comment should contain pipeline name, got:\n%s", content)
	}
}

func TestRunPipelineNew_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "phases-hotfix.yaml")
	if err := os.WriteFile(existing, []byte("existing content"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "hotfix", pipelineNewOptions{OutputDir: dir})
	if err == nil {
		t.Fatal("expected error when file exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want 'already exists'", err.Error())
	}

	// Original content must be preserved.
	data, _ := os.ReadFile(existing)
	if string(data) != "existing content" {
		t.Errorf("file was modified: got %q", string(data))
	}
}

func TestRunPipelineNew_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "phases-hotfix.yaml")
	if err := os.WriteFile(existing, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "hotfix", pipelineNewOptions{OutputDir: dir, Force: true})
	if err != nil {
		t.Fatalf("runPipelineNew(force=true) error: %v", err)
	}

	// File should be overwritten with valid pipeline content.
	pl, err := pipeline.LoadPipeline(existing)
	if err != nil {
		t.Fatalf("LoadPipeline() after force overwrite: %v", err)
	}
	if len(pl.Phases) != 3 {
		t.Errorf("expected 3 phases after overwrite, got %d", len(pl.Phases))
	}
}

func TestRunPipelineNew_DryRun(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "experiment", pipelineNewOptions{OutputDir: dir, DryRun: true})
	if err != nil {
		t.Fatalf("runPipelineNew(dryRun=true) error: %v", err)
	}

	// File must NOT be written.
	destPath := filepath.Join(dir, "phases-experiment.yaml")
	if _, err := os.Stat(destPath); err == nil {
		t.Fatal("dry-run should not write a file")
	}

	// Output should contain the scaffold YAML.
	output := buf.String()
	if !strings.Contains(output, "phases:") {
		t.Errorf("dry-run output missing phases key, got: %s", output)
	}
	if !strings.Contains(output, "experiment") {
		t.Errorf("dry-run output missing pipeline name, got: %s", output)
	}
}

func TestRunPipelineNew_RejectsDefault(t *testing.T) {
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "default", pipelineNewOptions{})
	if err == nil {
		t.Fatal("expected error for 'default' name, got nil")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error = %q, want 'reserved'", err.Error())
	}
}

func TestRunPipelineNew_RejectsInvalidName(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"foo/bar"},
		{"foo\\bar"},
		{"../etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := runPipelineNew(&buf, tt.name, pipelineNewOptions{})
			if err == nil {
				t.Fatalf("expected error for invalid name %q, got nil", tt.name)
			}
		})
	}
}

func TestRunPipelineNew_CustomDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "config", "pipelines")

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "fast", pipelineNewOptions{OutputDir: subdir})
	if err != nil {
		t.Fatalf("runPipelineNew() error: %v", err)
	}

	destPath := filepath.Join(subdir, "phases-fast.yaml")
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("file not created in custom dir: %v", err)
	}
}

func TestNewPipelineCmd_Structure(t *testing.T) {
	cmd := newPipelineCmd()

	if cmd.Use != "pipeline" {
		t.Errorf("Use = %q, want %q", cmd.Use, "pipeline")
	}

	// Should have a "new" subcommand.
	subCmds := cmd.Commands()
	found := false
	for _, sub := range subCmds {
		if sub.Use == "new <name>" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'new' subcommand")
	}
}

func TestNewPipelineNewCmd_RequiresName(t *testing.T) {
	cmd := newPipelineNewCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no name provided, got nil")
	}
}

func TestRenderPipelineScaffold_ContainsPhases(t *testing.T) {
	content, err := renderPipelineScaffold("test-pipeline")
	if err != nil {
		t.Fatalf("renderPipelineScaffold() error: %v", err)
	}

	for _, want := range []string{"implement", "verify", "submit", "test-pipeline"} {
		if !strings.Contains(content, want) {
			t.Errorf("scaffold should contain %q, got:\n%s", want, content)
		}
	}
}

func TestRunPipelineNew_StatErrorNotErrNotExist(t *testing.T) {
	dir := t.TempDir()
	// Create a parent dir with no read/execute permission so Stat fails
	// with a permission error, not ErrNotExist.
	noPerms := filepath.Join(dir, "noperm")
	if err := os.Mkdir(noPerms, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(noPerms, 0755) })

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "test", pipelineNewOptions{OutputDir: noPerms})
	if err == nil {
		t.Fatal("expected error for inaccessible path, got nil")
	}
}
