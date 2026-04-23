package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallPlugin(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	var buf bytes.Buffer
	if err := installPlugin(&buf, destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	output := buf.String()
	if !contains(output, "Installed SODA for Claude Code") {
		t.Errorf("expected output to contain install message, got %q", output)
	}
	if !contains(output, "soda-pipeline") {
		t.Errorf("expected output to mention soda-pipeline skill")
	}
	if !contains(output, "pipeline-architect") {
		t.Errorf("expected output to mention pipeline-architect agent")
	}

	// Verify key files exist in the correct locations.
	expectedFiles := []string{
		"commands/soda-run.md",
		"commands/soda-resume.md",
		"commands/soda-status.md",
		"commands/soda-sessions.md",
		"commands/soda-clean.md",
		"commands/soda-history.md",
		"commands/soda-render.md",
		"commands/soda-spec.md",
		"commands/soda-log.md",
		"commands/soda-validate.md",
		"commands/soda-cost.md",
		"commands/soda-pipelines.md",
		"commands/soda-attach.md",
		"commands/soda-pick.md",
		"skills/soda-pipeline/SKILL.md",
		"skills/soda-pipeline/RUNBOOK.md",
		"agents/pipeline-architect.md",
	}

	for _, relPath := range expectedFiles {
		fullPath := filepath.Join(destDir, relPath)
		info, err := os.Stat(fullPath)
		if err != nil {
			t.Errorf("expected file %s to exist: %v", relPath, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("expected file %s to be non-empty", relPath)
		}
	}
}

func TestInstallPluginMatchesEmbedded(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Walk the embedded filesystem and verify each file matches.
	claudeCodeFS, err := fs.Sub(embeddedClaudeCode, "embeds/claude-code")
	if err != nil {
		t.Fatalf("fs.Sub() error: %v", err)
	}

	fileCount := 0
	walkErr := fs.WalkDir(claudeCodeFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		fileCount++

		embeddedData, readErr := fs.ReadFile(claudeCodeFS, path)
		if readErr != nil {
			return readErr
		}

		installedData, readErr := os.ReadFile(filepath.Join(destDir, path))
		if readErr != nil {
			t.Errorf("installed file %s not readable: %v", path, readErr)
			return nil
		}

		if string(embeddedData) != string(installedData) {
			t.Errorf("file %s content mismatch: embedded %d bytes, installed %d bytes",
				path, len(embeddedData), len(installedData))
		}

		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk error: %v", walkErr)
	}

	if fileCount == 0 {
		t.Fatal("no embedded files found")
	}
}

func TestInstallPluginAlreadyExistsNoForce(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	// First install.
	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("first installPlugin() error: %v", err)
	}

	// Second install without force should fail.
	err := installPlugin(&bytes.Buffer{}, destDir, false)
	if err == nil {
		t.Fatal("expected error when installing without --force, got nil")
	}

	expectedMsg := "already installed"
	if got := err.Error(); !contains(got, expectedMsg) {
		t.Errorf("error message %q does not contain %q", got, expectedMsg)
	}
}

func TestInstallPluginForceOverwrites(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	// First install.
	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("first installPlugin() error: %v", err)
	}

	// Modify a file to verify overwrite.
	target := filepath.Join(destDir, "commands", "soda-run.md")
	if err := os.WriteFile(target, []byte("modified"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Install with force.
	if err := installPlugin(&bytes.Buffer{}, destDir, true); err != nil {
		t.Fatalf("force installPlugin() error: %v", err)
	}

	// File should be restored to embedded content.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read file after force install: %v", err)
	}
	if string(data) == "modified" {
		t.Error("expected file to be overwritten after force install")
	}
}

func TestUninstallPlugin(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	// Install first.
	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Verify a file exists.
	runCmd := filepath.Join(destDir, "commands", "soda-run.md")
	if _, err := os.Stat(runCmd); err != nil {
		t.Fatalf("soda-run.md should exist before uninstall: %v", err)
	}

	// Uninstall.
	var buf bytes.Buffer
	if err := uninstallPlugin(&buf, destDir); err != nil {
		t.Fatalf("uninstallPlugin() error: %v", err)
	}

	if !contains(buf.String(), "Removed SODA") {
		t.Errorf("expected output to contain 'Removed SODA', got %q", buf.String())
	}

	// SODA files should be gone.
	if _, err := os.Stat(runCmd); !os.IsNotExist(err) {
		t.Error("soda-run.md should not exist after uninstall")
	}

	// The .claude/commands/ directory should still exist (may have non-SODA files).
	if _, err := os.Stat(filepath.Join(destDir, "commands")); os.IsNotExist(err) {
		t.Error("commands/ directory should still exist after uninstall")
	}
}

func TestUninstallPluginNotInstalled(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	err := uninstallPlugin(&bytes.Buffer{}, destDir)
	if err == nil {
		t.Fatal("expected error when uninstalling non-existent install, got nil")
	}

	expectedMsg := "not installed"
	if got := err.Error(); !contains(got, expectedMsg) {
		t.Errorf("error message %q does not contain %q", got, expectedMsg)
	}
}

func TestUninstallPreservesNonSodaFiles(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	// Install SODA.
	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Create a non-SODA command file.
	otherCmd := filepath.Join(destDir, "commands", "my-custom-command.md")
	if err := os.WriteFile(otherCmd, []byte("custom"), 0644); err != nil {
		t.Fatalf("write custom command: %v", err)
	}

	// Uninstall.
	if err := uninstallPlugin(&bytes.Buffer{}, destDir); err != nil {
		t.Fatalf("uninstallPlugin() error: %v", err)
	}

	// Non-SODA file should still exist.
	if _, err := os.Stat(otherCmd); err != nil {
		t.Errorf("non-SODA file should be preserved after uninstall: %v", err)
	}
}

func TestPluginDestDirLocal(t *testing.T) {
	dir, err := pluginDestDir(false)
	if err != nil {
		t.Fatalf("pluginDestDir(false) error: %v", err)
	}

	expected := ".claude"
	if dir != expected {
		t.Errorf("pluginDestDir(false) = %q, want %q", dir, expected)
	}
}

func TestPluginDestDirGlobal(t *testing.T) {
	dir, err := pluginDestDir(true)
	if err != nil {
		t.Fatalf("pluginDestDir(true) error: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error: %v", err)
	}

	expected := filepath.Join(home, ".claude")
	if dir != expected {
		t.Errorf("pluginDestDir(true) = %q, want %q", dir, expected)
	}
}

func TestUpdatePluginAlreadyUpToDate(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	// Install first.
	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Update should report up to date.
	var buf bytes.Buffer
	if err := updatePlugin(&buf, destDir, false); err != nil {
		t.Fatalf("updatePlugin() error: %v", err)
	}

	if !contains(buf.String(), "Already up to date") {
		t.Errorf("expected 'Already up to date', got %q", buf.String())
	}
}

func TestUpdatePluginModifiedFile(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	// Install first.
	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Modify a file to simulate a stale version.
	target := filepath.Join(destDir, "commands", "soda-run.md")
	if err := os.WriteFile(target, []byte("old version"), 0644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	// Update should detect and fix the changed file.
	var buf bytes.Buffer
	if err := updatePlugin(&buf, destDir, false); err != nil {
		t.Fatalf("updatePlugin() error: %v", err)
	}

	output := buf.String()
	if !contains(output, "soda-run.md") {
		t.Errorf("expected output to mention soda-run.md, got %q", output)
	}
	if !contains(output, "updated") {
		t.Errorf("expected output to mention 'updated', got %q", output)
	}
	if !contains(output, "1 updated") {
		t.Errorf("expected '1 updated' in summary, got %q", output)
	}

	// File should now match the embedded version.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if string(data) == "old version" {
		t.Error("file should have been updated to embedded version")
	}
}

func TestUpdatePluginNewFile(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	// Install first.
	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Delete one file to simulate a missing file.
	target := filepath.Join(destDir, "commands", "soda-log.md")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove file: %v", err)
	}

	// Update should add the missing file.
	var buf bytes.Buffer
	if err := updatePlugin(&buf, destDir, false); err != nil {
		t.Fatalf("updatePlugin() error: %v", err)
	}

	output := buf.String()
	if !contains(output, "soda-log.md") {
		t.Errorf("expected output to mention soda-log.md, got %q", output)
	}
	if !contains(output, "new") {
		t.Errorf("expected output to mention 'new', got %q", output)
	}

	// File should now exist.
	if _, err := os.Stat(target); err != nil {
		t.Errorf("soda-log.md should exist after update: %v", err)
	}
}

func TestUpdatePluginDryRun(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	// Install first.
	if err := installPlugin(&bytes.Buffer{}, destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Modify a file.
	target := filepath.Join(destDir, "commands", "soda-run.md")
	original, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if err := os.WriteFile(target, []byte("modified"), 0644); err != nil {
		t.Fatalf("write modified: %v", err)
	}

	// Dry run should report changes but not write.
	var buf bytes.Buffer
	if err := updatePlugin(&buf, destDir, true); err != nil {
		t.Fatalf("updatePlugin(dry-run) error: %v", err)
	}

	output := buf.String()
	if !contains(output, "Dry run") {
		t.Errorf("expected 'Dry run' in output, got %q", output)
	}
	if !contains(output, "1 would be updated") {
		t.Errorf("expected '1 would be updated', got %q", output)
	}

	// File should NOT have been changed.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read file after dry run: %v", err)
	}
	if string(data) != "modified" {
		t.Errorf("dry run should not modify files; got %q, want %q", string(data), "modified")
	}
	_ = original // verify we read it (suppress unused warning)
}

func TestUpdatePluginNotInstalled(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude")

	err := updatePlugin(&bytes.Buffer{}, destDir, false)
	if err == nil {
		t.Fatal("expected error when updating non-existent install, got nil")
	}

	if !contains(err.Error(), "not installed") {
		t.Errorf("error %q should mention 'not installed'", err.Error())
	}
}

// contains checks if s contains substr (avoids importing strings for a test helper).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for idx := 0; idx <= len(s)-len(substr); idx++ {
		if s[idx:idx+len(substr)] == substr {
			return true
		}
	}
	return false
}
