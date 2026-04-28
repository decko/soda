package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSettingsFile(t *testing.T) {
	t.Run("writes_valid_json_with_apiKeyHelper", func(t *testing.T) {
		dir := t.TempDir()
		helper := "/usr/local/bin/get-api-key"

		path, err := writeSettingsFile(dir, helper)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer os.Remove(path)

		if !filepath.IsAbs(path) {
			t.Errorf("path is not absolute: %s", path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read settings file: %v", err)
		}

		var settings map[string]string
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatalf("failed to parse settings JSON: %v", err)
		}

		if got := settings["apiKeyHelper"]; got != helper {
			t.Errorf("apiKeyHelper = %q, want %q", got, helper)
		}
	})

	t.Run("file_is_removable", func(t *testing.T) {
		dir := t.TempDir()

		path, err := writeSettingsFile(dir, "/bin/helper")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if err := os.Remove(path); err != nil {
			t.Errorf("failed to remove settings file: %v", err)
		}

		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("settings file still exists after removal")
		}
	})

	t.Run("falls_back_to_os_tempdir_when_dir_empty", func(t *testing.T) {
		path, err := writeSettingsFile("", "/bin/helper")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer os.Remove(path)

		if !filepath.IsAbs(path) {
			t.Errorf("path is not absolute: %s", path)
		}
	})
}
