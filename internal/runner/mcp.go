package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

// WriteMCPConfigFile writes a Claude Code MCP config JSON file to dir and
// returns the file path and a cleanup function. The cleanup function removes
// the file. The file is created with mode 0600 because env fields may
// contain API keys or other secrets.
func WriteMCPConfigFile(dir string, servers map[string]MCPServerConfig) (string, func(), error) {
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
		entry := mcpServerEntry(srv)
		entry.Command = resolveMCPCommand(srv.Command)
		envelope.MCPServers[name] = entry
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

// mcpServerEntry has the same fields as config.MCPServerConfig, enabling
// direct type conversion. The separate type exists for JSON tag control.

// WriteOpencodeMCPConfig writes (or merges) MCP server declarations into
// {workDir}/.opencode.json. If the file already exists, the mcpServers key
// is merged into the existing JSON, preserving other keys (providers, models,
// etc.). The file is created with mode 0600 because env fields may contain
// secrets. The returned cleanup function restores the original file content
// or removes it if it did not exist prior to the call.
func WriteOpencodeMCPConfig(workDir string, servers map[string]MCPServerConfig) (func(), error) {
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

	// Build the MCP servers map. Resolve bare command names to absolute
	// paths so the agent process can find them without relying on PATH.
	mcpEntries := make(map[string]mcpServerEntry, len(servers))
	for name, srv := range servers {
		entry := mcpServerEntry(srv)
		entry.Command = resolveMCPCommand(srv.Command)
		mcpEntries[name] = entry
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

// resolveMCPCommand resolves a bare MCP server command name to its absolute
// path via exec.LookPath + filepath.EvalSymlinks. This ensures the agent
// process inside the sandbox can execute the binary without relying on PATH
// (the sandbox PATH may not include the directory containing the binary).
// If the command is already absolute, empty, or cannot be resolved, the
// original value is returned unchanged.
func resolveMCPCommand(command string) string {
	if command == "" || filepath.IsAbs(command) {
		return command
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return command
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return command
	}
	return resolved
}
