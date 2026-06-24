package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/runner"
)

func TestClaudeAdapterBuildArgsMCP(t *testing.T) {
	// Create a fake claude binary so NewClaudeAdapter can resolve it.
	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("NODE_PATH", "")

	adapter, err := NewClaudeAdapter(fakeClaude)
	if err != nil {
		t.Fatalf("NewClaudeAdapter: %v", err)
	}

	t.Run("writes_mcp_config_to_tmpdir", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			UserPrompt: "do the thing",
			MCPServers: map[string]runner.MCPServerConfig{
				"jira": {
					Command: "jira-mcp",
					Args:    []string{"--port", "8080"},
					Env:     map[string]string{"JIRA_URL": "https://jira.example.com"},
				},
			},
		}
		args, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		// Verify --mcp-config flag is present and points into tmpDir.
		mcpConfigPath := ""
		for idx, arg := range args {
			if arg == "--mcp-config" && idx+1 < len(args) {
				mcpConfigPath = args[idx+1]
				break
			}
		}
		if mcpConfigPath == "" {
			t.Fatal("--mcp-config flag not found in args")
		}
		if !strings.HasPrefix(mcpConfigPath, tmpDir) {
			t.Errorf("MCP config path %q should be inside tmpDir %q", mcpConfigPath, tmpDir)
		}

		// Verify --strict-mcp-config is present.
		assertContains(t, args, "--strict-mcp-config")

		// Verify the config file contains valid JSON with the expected server.
		data, readErr := os.ReadFile(mcpConfigPath)
		if readErr != nil {
			t.Fatalf("ReadFile(%s): %v", mcpConfigPath, readErr)
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
		jira, ok := envelope.MCPServers["jira"]
		if !ok {
			t.Fatal("jira server not found in MCP config JSON")
		}
		if jira.Command != "jira-mcp" {
			t.Errorf("jira.Command = %q, want %q", jira.Command, "jira-mcp")
		}
		if jira.Env["JIRA_URL"] != "https://jira.example.com" {
			t.Errorf("jira.Env[JIRA_URL] = %q, want %q", jira.Env["JIRA_URL"], "https://jira.example.com")
		}
	})

	t.Run("no_mcp_config_when_no_servers", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			UserPrompt: "do the thing",
		}
		args, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		for _, arg := range args {
			if arg == "--mcp-config" {
				t.Error("should not include --mcp-config when no MCP servers")
			}
			if arg == "--strict-mcp-config" {
				t.Error("should not include --strict-mcp-config when no MCP servers")
			}
		}
	})

	t.Run("appends_allowed_mcp_tools", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			UserPrompt:   "do the thing",
			AllowedTools: []string{"Read"},
			MCPServers: map[string]runner.MCPServerConfig{
				"jira": {Command: "jira-mcp"},
			},
			AllowedMCPTools: []string{"mcp__jira__search", "mcp__jira__create"},
		}
		args, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		// The allowed-tools list should contain both original and MCP tools.
		argsStr := strings.Join(args, " ")
		if !strings.Contains(argsStr, "mcp__jira__search") {
			t.Errorf("args should contain mcp__jira__search, got: %v", args)
		}
		if !strings.Contains(argsStr, "mcp__jira__create") {
			t.Errorf("args should contain mcp__jira__create, got: %v", args)
		}
	})
}

func TestOpencodeAdapterBuildArgsMCP(t *testing.T) {
	// Create a fake opencode binary so NewOpencodeAdapter can resolve it.
	binDir := t.TempDir()
	fakeOpencode := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(fakeOpencode, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewOpencodeAdapter(fakeOpencode)
	if err != nil {
		t.Fatalf("NewOpencodeAdapter: %v", err)
	}

	t.Run("writes_opencode_json_to_tmpdir", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			Phase:      "implement",
			UserPrompt: "do the thing",
			MCPServers: map[string]runner.MCPServerConfig{
				"github": {
					Command: "gh-mcp",
					Args:    []string{"--org", "decko"},
				},
			},
		}
		_, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		// Verify .opencode.json was written to tmpDir.
		configPath := filepath.Join(tmpDir, ".opencode.json")
		data, readErr := os.ReadFile(configPath)
		if readErr != nil {
			t.Fatalf("ReadFile(%s): %v", configPath, readErr)
		}

		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("JSON unmarshal: %v", err)
		}
		if _, ok := parsed["mcpServers"]; !ok {
			t.Fatal("mcpServers key not found in .opencode.json")
		}

		// Verify the server entry.
		var servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if err := json.Unmarshal(parsed["mcpServers"], &servers); err != nil {
			t.Fatalf("unmarshal mcpServers: %v", err)
		}
		gh, ok := servers["github"]
		if !ok {
			t.Fatal("github server not found in .opencode.json")
		}
		if gh.Command != "gh-mcp" {
			t.Errorf("github.Command = %q, want %q", gh.Command, "gh-mcp")
		}
	})

	t.Run("no_opencode_json_when_no_servers", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			Phase:      "implement",
			UserPrompt: "do the thing",
		}
		_, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		configPath := filepath.Join(tmpDir, ".opencode.json")
		if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
			t.Errorf(".opencode.json should not exist when no MCP servers, got err: %v", statErr)
		}
	})
}

func TestClaudeAdapterMCPExtraPaths(t *testing.T) {
	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("NODE_PATH", "")

	adapter, err := NewClaudeAdapter(fakeClaude)
	if err != nil {
		t.Fatalf("NewClaudeAdapter: %v", err)
	}

	t.Run("found_binary_in_read_paths", func(t *testing.T) {
		mcpDir := t.TempDir()
		fakeMCP := filepath.Join(mcpDir, "jira-mcp")
		if err := os.WriteFile(fakeMCP, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write fake mcp binary: %v", err)
		}
		t.Setenv("PATH", mcpDir+":"+binDir+":"+os.Getenv("PATH"))

		servers := map[string]runner.MCPServerConfig{
			"jira": {Command: "jira-mcp"},
		}
		read, write := adapter.MCPExtraPaths(servers)

		if !containsPath(read, mcpDir) {
			t.Errorf("read paths %v should contain MCP binary dir %q", read, mcpDir)
		}
		if write != nil {
			t.Errorf("write paths = %v, want nil", write)
		}
	})

	t.Run("nil_input_returns_empty", func(t *testing.T) {
		read, write := adapter.MCPExtraPaths(nil)

		if len(read) != 0 {
			t.Errorf("read paths = %v, want empty for nil input", read)
		}
		if write != nil {
			t.Errorf("write paths = %v, want nil", write)
		}
	})
}
