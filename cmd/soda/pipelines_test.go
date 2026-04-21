package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverDirs(t *testing.T) {
	dirs, sources := discoverDirs()

	// Should have at least the working directory.
	if len(dirs) == 0 {
		t.Fatal("expected at least one directory")
	}
	if len(dirs) != len(sources) {
		t.Fatalf("dirs (%d) and sources (%d) length mismatch", len(dirs), len(sources))
	}
	if sources[0] != "local" {
		t.Errorf("first source = %q, want %q", sources[0], "local")
	}
}

func TestRunPipelines_EmbeddedDefault(t *testing.T) {
	// Run from a temp dir with no pipeline files — should show embedded default.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "default") {
		t.Error("expected 'default' pipeline in output")
	}
	if !strings.Contains(output, "embedded") {
		t.Error("expected 'embedded' source in output")
	}
}

func TestRunPipelines_LocalPipelines(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Create local pipeline files.
	phases := "phases:\n  - name: triage\n    prompt: prompts/triage.md\n    timeout: 1m\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "phases.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "phases-fast.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "default") {
		t.Error("expected 'default' pipeline in output")
	}
	if !strings.Contains(output, "fast") {
		t.Error("expected 'fast' pipeline in output")
	}
	if !strings.Contains(output, "local") {
		t.Error("expected 'local' source in output")
	}
}

func TestRunPipelines_TableHeader(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "NAME") {
		t.Error("expected NAME header column")
	}
	if !strings.Contains(output, "SOURCE") {
		t.Error("expected SOURCE header column")
	}
	if !strings.Contains(output, "PATH") {
		t.Error("expected PATH header column")
	}
}
