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
