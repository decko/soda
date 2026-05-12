// Package transcript defines shared types for agent transcript capture.
// These types are intentionally in a separate package from internal/claude
// to keep the runner interface agent-agnostic.
package transcript

// Level controls which events are persisted in the transcript.
type Level string

const (
	// Off disables transcript capture entirely.
	Off Level = "off"
	// Tools captures tool_use and tool_result events only.
	Tools Level = "tools"
	// Full captures all assistant/user events including text blocks.
	Full Level = "full"
)

// Entry is a single human-readable entry in a phase transcript.
type Entry struct {
	Role    string `json:"role"`              // "assistant", "tool_use", "tool_result"
	Content string `json:"content"`           // display text
	Tool    string `json:"tool,omitempty"`    // tool name (tool_use/tool_result only)
	ToolID  string `json:"tool_id,omitempty"` // correlating tool_use_id
}
