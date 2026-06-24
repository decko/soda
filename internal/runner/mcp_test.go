package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteMCPConfigFile(t *testing.T) {
	// Use a command name that is guaranteed not to exist on PATH so
	// resolveMCPCommand returns it unchanged (tests for resolution
	// behaviour are in TestResolveMCPCommand / TestWriteMCPConfigFileResolvesCommands).
	servers := map[string]MCPServerConfig{
		"jira": {
			Command: "soda-test-fake-mcp-jira",
			Args:    []string{"jira"},
			Env:     map[string]string{"JIRA_URL": "https://jira.example.com"},
		},
		"github": {
			Command: "soda-test-fake-mcp-gh",
			Args:    []string{"github"},
		},
	}

	t.Run("creates_valid_json_file", func(t *testing.T) {
		dir := t.TempDir()
		path, cleanup, err := WriteMCPConfigFile(dir, servers)
		if err != nil {
			t.Fatalf("WriteMCPConfigFile: %v", err)
		}
		defer cleanup()

		if !filepath.IsAbs(path) {
			t.Errorf("path should be absolute, got %q", path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}

		var envelope struct {
			MCPServers map[string]struct {
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("JSON unmarshal: %v", err)
		}

		if len(envelope.MCPServers) != 2 {
			t.Fatalf("expected 2 servers, got %d", len(envelope.MCPServers))
		}

		jira, ok := envelope.MCPServers["jira"]
		if !ok {
			t.Fatal("jira server not found in JSON")
		}
		if jira.Command != "soda-test-fake-mcp-jira" {
			t.Errorf("jira.Command = %q, want %q", jira.Command, "soda-test-fake-mcp-jira")
		}
		if len(jira.Args) != 1 || jira.Args[0] != "jira" {
			t.Errorf("jira.Args = %v, want [jira]", jira.Args)
		}
		if jira.Env["JIRA_URL"] != "https://jira.example.com" {
			t.Errorf("jira.Env[JIRA_URL] = %q, want %q", jira.Env["JIRA_URL"], "https://jira.example.com")
		}

		gh, ok := envelope.MCPServers["github"]
		if !ok {
			t.Fatal("github server not found in JSON")
		}
		if gh.Command != "soda-test-fake-mcp-gh" {
			t.Errorf("github.Command = %q, want %q", gh.Command, "soda-test-fake-mcp-gh")
		}
	})

	t.Run("file_has_restricted_permissions", func(t *testing.T) {
		dir := t.TempDir()
		path, cleanup, err := WriteMCPConfigFile(dir, servers)
		if err != nil {
			t.Fatalf("WriteMCPConfigFile: %v", err)
		}
		defer cleanup()

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		perm := info.Mode().Perm()
		if perm != 0o600 {
			t.Errorf("file permissions = %o, want 0600", perm)
		}
	})

	t.Run("cleanup_removes_file", func(t *testing.T) {
		dir := t.TempDir()
		path, cleanup, err := WriteMCPConfigFile(dir, servers)
		if err != nil {
			t.Fatalf("WriteMCPConfigFile: %v", err)
		}

		// File should exist before cleanup.
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("file should exist before cleanup: %v", err)
		}

		cleanup()

		// File should be gone after cleanup.
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file should not exist after cleanup, got err: %v", err)
		}
	})

	t.Run("falls_back_to_os_tempdir_when_dir_empty", func(t *testing.T) {
		path, cleanup, err := WriteMCPConfigFile("", servers)
		if err != nil {
			t.Fatalf("WriteMCPConfigFile: %v", err)
		}
		defer cleanup()

		if !filepath.IsAbs(path) {
			t.Errorf("path should be absolute, got %q", path)
		}
	})
}

func TestWriteOpencodeMCPConfig(t *testing.T) {
	servers := map[string]MCPServerConfig{
		"jira": {
			Command: "soda-test-fake-mcp-jira",
			Args:    []string{"jira"},
			Env:     map[string]string{"JIRA_URL": "https://jira.example.com"},
		},
	}

	t.Run("creates_opencode_json", func(t *testing.T) {
		dir := t.TempDir()
		cleanup, err := WriteOpencodeMCPConfig(dir, servers)
		if err != nil {
			t.Fatalf("WriteOpencodeMCPConfig: %v", err)
		}
		defer cleanup()

		configPath := filepath.Join(dir, ".opencode.json")
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}

		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("JSON unmarshal: %v", err)
		}

		if _, ok := parsed["mcpServers"]; !ok {
			t.Fatal("mcpServers key not found in .opencode.json")
		}
	})

	t.Run("file_has_restricted_permissions", func(t *testing.T) {
		dir := t.TempDir()
		cleanup, err := WriteOpencodeMCPConfig(dir, servers)
		if err != nil {
			t.Fatalf("WriteOpencodeMCPConfig: %v", err)
		}
		defer cleanup()

		configPath := filepath.Join(dir, ".opencode.json")
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		perm := info.Mode().Perm()
		if perm != 0o600 {
			t.Errorf("file permissions = %o, want 0600", perm)
		}
	})

	t.Run("merges_into_existing_json", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, ".opencode.json")

		// Write pre-existing config with a "provider" key.
		existing := `{"provider":"anthropic","model":"claude-3"}`
		if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		cleanup, err := WriteOpencodeMCPConfig(dir, servers)
		if err != nil {
			t.Fatalf("WriteOpencodeMCPConfig: %v", err)
		}
		defer cleanup()

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}

		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("JSON unmarshal: %v", err)
		}

		// Both the original "provider" and new "mcpServers" should be present.
		if _, ok := parsed["provider"]; !ok {
			t.Error("existing 'provider' key was lost during merge")
		}
		if _, ok := parsed["model"]; !ok {
			t.Error("existing 'model' key was lost during merge")
		}
		if _, ok := parsed["mcpServers"]; !ok {
			t.Error("mcpServers key not found after merge")
		}
	})

	t.Run("cleanup_removes_file_when_none_existed", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, ".opencode.json")

		cleanup, err := WriteOpencodeMCPConfig(dir, servers)
		if err != nil {
			t.Fatalf("WriteOpencodeMCPConfig: %v", err)
		}

		// File should exist before cleanup.
		if _, err := os.Stat(configPath); err != nil {
			t.Fatalf("file should exist before cleanup: %v", err)
		}

		cleanup()

		// File should be gone after cleanup.
		if _, err := os.Stat(configPath); !os.IsNotExist(err) {
			t.Errorf("file should not exist after cleanup, got err: %v", err)
		}
	})

	t.Run("cleanup_restores_original_file", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, ".opencode.json")

		original := `{"provider":"anthropic"}`
		if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		cleanup, err := WriteOpencodeMCPConfig(dir, servers)
		if err != nil {
			t.Fatalf("WriteOpencodeMCPConfig: %v", err)
		}

		cleanup()

		restored, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("ReadFile after cleanup: %v", err)
		}
		if string(restored) != original {
			t.Errorf("restored content = %q, want %q", string(restored), original)
		}
	})
}

func TestResolveMCPCommand(t *testing.T) {
	t.Run("resolves_bare_command_to_absolute_path", func(t *testing.T) {
		binDir := t.TempDir()
		fakeBin := filepath.Join(binDir, "test-mcp-server")
		if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write fake binary: %v", err)
		}
		t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

		resolved := resolveMCPCommand("test-mcp-server")
		if !filepath.IsAbs(resolved) {
			t.Errorf("resolved = %q, want absolute path", resolved)
		}
		if resolved != fakeBin {
			t.Errorf("resolved = %q, want %q", resolved, fakeBin)
		}
	})

	t.Run("returns_original_when_not_found", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		resolved := resolveMCPCommand("nonexistent-mcp-binary")
		if resolved != "nonexistent-mcp-binary" {
			t.Errorf("resolved = %q, want %q", resolved, "nonexistent-mcp-binary")
		}
	})

	t.Run("returns_empty_for_empty_command", func(t *testing.T) {
		resolved := resolveMCPCommand("")
		if resolved != "" {
			t.Errorf("resolved = %q, want empty", resolved)
		}
	})

	t.Run("returns_absolute_path_unchanged", func(t *testing.T) {
		resolved := resolveMCPCommand("/usr/local/bin/mcp-server")
		if resolved != "/usr/local/bin/mcp-server" {
			t.Errorf("resolved = %q, want /usr/local/bin/mcp-server", resolved)
		}
	})
}

func TestWriteMCPConfigFileResolvesCommands(t *testing.T) {
	// Create a fake binary on PATH so resolveMCPCommand can find it.
	binDir := t.TempDir()
	fakeBin := filepath.Join(binDir, "resolvable-mcp")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	servers := map[string]MCPServerConfig{
		"resolvable": {Command: "resolvable-mcp"},
		"missing":    {Command: "unresolvable-mcp-xyz"},
	}

	dir := t.TempDir()
	path, cleanup, err := WriteMCPConfigFile(dir, servers)
	if err != nil {
		t.Fatalf("WriteMCPConfigFile: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var envelope struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}

	// Resolvable command should be written as an absolute path.
	resolvable := envelope.MCPServers["resolvable"]
	if !filepath.IsAbs(resolvable.Command) {
		t.Errorf("resolvable.Command = %q, want absolute path", resolvable.Command)
	}
	if !strings.HasSuffix(resolvable.Command, "resolvable-mcp") {
		t.Errorf("resolvable.Command = %q, should end with resolvable-mcp", resolvable.Command)
	}

	// Unresolvable command should remain as the bare name.
	missing := envelope.MCPServers["missing"]
	if missing.Command != "unresolvable-mcp-xyz" {
		t.Errorf("missing.Command = %q, want %q (unchanged)", missing.Command, "unresolvable-mcp-xyz")
	}
}

func TestWriteOpencodeMCPConfigResolvesCommands(t *testing.T) {
	// Create a fake binary on PATH so resolveMCPCommand can find it.
	binDir := t.TempDir()
	fakeBin := filepath.Join(binDir, "oc-mcp-server")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	servers := map[string]MCPServerConfig{
		"resolvable": {Command: "oc-mcp-server"},
	}

	dir := t.TempDir()
	cleanup, err := WriteOpencodeMCPConfig(dir, servers)
	if err != nil {
		t.Fatalf("WriteOpencodeMCPConfig: %v", err)
	}
	defer cleanup()

	configPath := filepath.Join(dir, ".opencode.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}

	var servers2 map[string]struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(parsed["mcpServers"], &servers2); err != nil {
		t.Fatalf("unmarshal mcpServers: %v", err)
	}

	srv := servers2["resolvable"]
	if !filepath.IsAbs(srv.Command) {
		t.Errorf("resolvable.Command = %q, want absolute path", srv.Command)
	}
	if !strings.HasSuffix(srv.Command, "oc-mcp-server") {
		t.Errorf("resolvable.Command = %q, should end with oc-mcp-server", srv.Command)
	}
}
