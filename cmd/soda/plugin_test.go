package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallPlugin(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude", "plugins", "soda")

	if err := installPlugin(destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Verify key files exist
	expectedFiles := []string{
		".claude-plugin/plugin.json",
		"skills/soda-pipeline/SKILL.md",
		"commands/run.md",
		"commands/status.md",
		"commands/sessions.md",
		"commands/init.md",
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
	destDir := filepath.Join(t.TempDir(), ".claude", "plugins", "soda")

	if err := installPlugin(destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Walk the embedded filesystem and verify each file matches
	pluginFS, err := fs.Sub(embeddedPlugin, "embeds/plugin")
	if err != nil {
		t.Fatalf("fs.Sub() error: %v", err)
	}

	fileCount := 0
	walkErr := fs.WalkDir(pluginFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		fileCount++

		embeddedData, readErr := fs.ReadFile(pluginFS, path)
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
		t.Fatal("no embedded plugin files found")
	}
}

func TestInstallPluginAlreadyExistsNoForce(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude", "plugins", "soda")

	// First install
	if err := installPlugin(destDir, false); err != nil {
		t.Fatalf("first installPlugin() error: %v", err)
	}

	// Second install without force should fail
	err := installPlugin(destDir, false)
	if err == nil {
		t.Fatal("expected error when installing without --force, got nil")
	}

	expectedMsg := "plugin already installed"
	if got := err.Error(); !contains(got, expectedMsg) {
		t.Errorf("error message %q does not contain %q", got, expectedMsg)
	}
}

func TestInstallPluginForceOverwrites(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude", "plugins", "soda")

	// First install
	if err := installPlugin(destDir, false); err != nil {
		t.Fatalf("first installPlugin() error: %v", err)
	}

	// Write a marker file to verify overwrite
	markerPath := filepath.Join(destDir, "marker.txt")
	if err := os.WriteFile(markerPath, []byte("marker"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Install with force
	if err := installPlugin(destDir, true); err != nil {
		t.Fatalf("force installPlugin() error: %v", err)
	}

	// Marker should be gone (directory was replaced)
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Error("expected marker file to be removed after force install")
	}

	// Plugin files should still exist
	pluginJSON := filepath.Join(destDir, ".claude-plugin", "plugin.json")
	if _, err := os.Stat(pluginJSON); err != nil {
		t.Errorf("plugin.json should exist after force install: %v", err)
	}
}

func TestUninstallPlugin(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude", "plugins", "soda")

	// Install first
	if err := installPlugin(destDir, false); err != nil {
		t.Fatalf("installPlugin() error: %v", err)
	}

	// Verify directory exists
	if _, err := os.Stat(destDir); err != nil {
		t.Fatalf("plugin dir should exist before uninstall: %v", err)
	}

	// Uninstall
	if err := uninstallPlugin(destDir); err != nil {
		t.Fatalf("uninstallPlugin() error: %v", err)
	}

	// Verify directory is gone
	if _, err := os.Stat(destDir); !os.IsNotExist(err) {
		t.Error("plugin directory should not exist after uninstall")
	}
}

func TestUninstallPluginNotInstalled(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), ".claude", "plugins", "soda")

	err := uninstallPlugin(destDir)
	if err == nil {
		t.Fatal("expected error when uninstalling non-existent plugin, got nil")
	}

	expectedMsg := "plugin not installed"
	if got := err.Error(); !contains(got, expectedMsg) {
		t.Errorf("error message %q does not contain %q", got, expectedMsg)
	}
}

func TestPluginDestDirLocal(t *testing.T) {
	dir, err := pluginDestDir(false)
	if err != nil {
		t.Fatalf("pluginDestDir(false) error: %v", err)
	}

	expected := filepath.Join(".claude", "plugins", "soda")
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

	expected := filepath.Join(home, ".claude", "plugins", "soda")
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
