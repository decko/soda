package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// mcpConfigEnvelope is the JSON structure expected by Claude Code's --mcp-config flag.
type mcpConfigEnvelope struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

// mcpServerEntry is the per-server JSON entry in the MCP config file.
type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// writeMCPConfigFile writes a Claude Code MCP config JSON file to dir and
// returns the file path and a cleanup function. The cleanup function removes
// the file. The file is created with mode 0600 because env fields may
// contain API keys or other secrets.
func writeMCPConfigFile(dir string, servers map[string]MCPServerConfig) (string, func(), error) {
	if dir == "" {
		dir = os.TempDir()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, fmt.Errorf("mcp: resolve dir: %w", err)
	}

	envelope := mcpConfigEnvelope{
		MCPServers: make(map[string]mcpServerEntry, len(servers)),
	}
	for name, srv := range servers {
		envelope.MCPServers[name] = mcpServerEntry{
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
		}
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		return "", nil, fmt.Errorf("mcp: marshal config: %w", err)
	}

	f, err := os.CreateTemp(abs, "soda-mcp-config-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("mcp: create temp file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("mcp: set file permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("mcp: write config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("mcp: close file: %w", err)
	}

	cleanup := func() {
		os.Remove(f.Name())
	}
	return f.Name(), cleanup, nil
}

// opencodeMCPEnvelope is the JSON structure for Opencode's .opencode.json
// with MCP server declarations.
type opencodeMCPEnvelope struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

// writeOpencodeMCPConfig writes (or merges) MCP server declarations into
// {workDir}/.opencode.json. If the file already exists, the mcpServers key
// is merged into the existing JSON, preserving other keys (providers, models,
// etc.). The file is created with mode 0600 because env fields may contain
// secrets. The returned cleanup function restores the original file content
// or removes it if it did not exist prior to the call.
func writeOpencodeMCPConfig(workDir string, servers map[string]MCPServerConfig) (func(), error) {
	if workDir == "" {
		workDir = os.TempDir()
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("mcp: resolve workdir: %w", err)
	}

	configPath := filepath.Join(abs, ".opencode.json")

	// Read existing file for backup/merge.
	existing, readErr := os.ReadFile(configPath)
	hadExisting := readErr == nil

	// Build the MCP servers map.
	mcpEntries := make(map[string]mcpServerEntry, len(servers))
	for name, srv := range servers {
		mcpEntries[name] = mcpServerEntry{
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
		}
	}

	// Merge into existing JSON or create fresh.
	var merged map[string]interface{}
	if hadExisting {
		if err := json.Unmarshal(existing, &merged); err != nil {
			// Existing file is not valid JSON; overwrite it.
			merged = make(map[string]interface{})
		}
	} else {
		merged = make(map[string]interface{})
	}
	merged["mcpServers"] = mcpEntries

	data, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal opencode config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("mcp: write opencode config: %w", err)
	}

	cleanup := func() {
		if hadExisting {
			_ = os.WriteFile(configPath, existing, 0o600)
		} else {
			_ = os.Remove(configPath)
		}
	}
	return cleanup, nil
}
