package runner

import (
	"strings"
)

// InstallMethod describes one way to install an agent CLI.
type InstallMethod struct {
	Label   string // human-readable label (e.g. "npm", "curl")
	Command string // shell command to install
}

// AgentInfo holds metadata about a known coding agent CLI.
type AgentInfo struct {
	Name        string          // canonical name used in soda.yaml runner field
	Binary      string          // default binary name in PATH
	InstallCmds []InstallMethod // ordered install methods (preferred first)
	VersionFlag string          // flag to query version (e.g. "--version")
}

// KnownAgents lists all coding agents that soda can drive.
var KnownAgents = []AgentInfo{
	{
		Name:   "claude",
		Binary: "claude",
		InstallCmds: []InstallMethod{
			{Label: "npm", Command: "npm install -g @anthropic-ai/claude-code"},
		},
		VersionFlag: "--version",
	},
	{
		Name:   "pi",
		Binary: "pi",
		InstallCmds: []InstallMethod{
			// curl URL is unverified — update when an official installer is published.
			{Label: "curl", Command: "curl -fsSL https://pi.dev/install | sh"},
			{Label: "npm", Command: "npm install -g @anthropic-ai/pi"},
		},
		VersionFlag: "--version",
	},
	{
		Name:   "opencode",
		Binary: "opencode",
		InstallCmds: []InstallMethod{
			{Label: "go", Command: "go install github.com/opencode-ai/opencode@latest"},
			{Label: "curl", Command: "curl -fsSL https://opencode.ai/install | sh"},
		},
		VersionFlag: "--version",
	},
}

// AgentByName returns the AgentInfo for the given runner name, or nil
// if the name is not a known agent.
func AgentByName(name string) *AgentInfo {
	for idx := range KnownAgents {
		if KnownAgents[idx].Name == name {
			return &KnownAgents[idx]
		}
	}
	return nil
}

// InstallHint returns the first install command for the agent, or an
// empty string if no install methods are defined.
func InstallHint(info *AgentInfo) string {
	if info == nil || len(info.InstallCmds) == 0 {
		return ""
	}
	return info.InstallCmds[0].Command
}

// DetectAgent checks whether a given agent binary is available in PATH
// and optionally extracts its version string.
//
// lookPath resolves the binary (typically exec.LookPath).
// runCmd executes a command and returns combined output (typically
// exec.Command(...).CombinedOutput wrapped in a string).
// name is the agent name used to look up metadata.
//
// Returns the resolved path, a semver version string (empty if
// extraction fails), and any error from lookPath.
func DetectAgent(
	lookPath func(string) (string, error),
	runCmd func(string, ...string) (string, error),
	name string,
) (path string, version string, err error) {
	info := AgentByName(name)
	if info == nil {
		return "", "", nil
	}

	path, err = lookPath(info.Binary)
	if err != nil {
		return "", "", err
	}

	if info.VersionFlag != "" {
		out, vErr := runCmd(info.Binary, info.VersionFlag)
		if vErr == nil {
			version = extractAgentSemver(out)
		}
	}

	return path, version, nil
}

// extractAgentSemver extracts the first semver-like version (X.Y.Z)
// from a string. Reuses the same logic as doctor's extractSemver.
func extractAgentSemver(s string) string {
	for _, field := range strings.Fields(s) {
		parts := strings.SplitN(field, ".", 3)
		if len(parts) == 3 {
			allDigits := true
			for _, part := range parts {
				if part == "" {
					allDigits = false
					break
				}
				for _, ch := range part {
					if ch < '0' || ch > '9' {
						allDigits = false
						break
					}
				}
			}
			if allDigits {
				return field
			}
		}
	}
	return ""
}
