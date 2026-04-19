package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInit_CreatesConfigFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.config.yaml")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"init", "--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	content := string(data)

	// Verify key sections are present
	for _, expected := range []string{
		"ticket_source:",
		"mode: autonomous",
		"model: claude-opus-4-6",
		"worktree_dir:",
		"state_dir:",
		"repos:",
	} {
		if !strings.Contains(content, expected) {
			t.Errorf("config missing expected content %q", expected)
		}
	}
}

func TestRunInit_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.config.yaml")

	// Create an existing file
	if err := os.WriteFile(dest, []byte("existing"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"init", "--dir", dir})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error when config already exists, got nil")
	}

	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q does not mention 'already exists'", err.Error())
	}

	// Verify the original content was not changed
	data, _ := os.ReadFile(dest)
	if string(data) != "existing" {
		t.Error("existing config file was modified")
	}
}

func TestRunInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.config.yaml")

	// Create an existing file
	if err := os.WriteFile(dest, []byte("old content"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"init", "--force", "--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init --force failed: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("config file missing after --force: %v", err)
	}

	if string(data) == "old content" {
		t.Error("config file was not overwritten with --force")
	}

	if !strings.Contains(string(data), "ticket_source:") {
		t.Error("overwritten config missing expected content")
	}
}

func TestRunInit_CreatesParentDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"init", "--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init with nested dir failed: %v", err)
	}

	dest := filepath.Join(dir, "soda.config.yaml")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("config file not created in nested dir: %v", err)
	}
}

func TestRunInit_DefaultDir(t *testing.T) {
	// Run init with default dir (current directory).
	// Change to a temp directory to avoid polluting the repo.
	dir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init with default dir failed: %v", err)
	}

	dest := filepath.Join(dir, "soda.config.yaml")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("config file not created in default dir: %v", err)
	}
}
