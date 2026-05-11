package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// TranscriptLevel controls which events are persisted in the transcript.
type TranscriptLevel string

const (
	// TranscriptOff disables transcript capture entirely.
	TranscriptOff TranscriptLevel = "off"
	// TranscriptTools captures tool_use and tool_result events only.
	TranscriptTools TranscriptLevel = "tools"
	// TranscriptFull captures all assistant/user events including text blocks.
	TranscriptFull TranscriptLevel = "full"
)

// TranscriptEntry is a single human-readable entry in a phase transcript.
type TranscriptEntry struct {
	Role    string `json:"role"`              // "assistant", "tool_use", "tool_result"
	Content string `json:"content"`           // display text
	Tool    string `json:"tool,omitempty"`    // tool name (tool_use/tool_result only)
	ToolID  string `json:"tool_id,omitempty"` // correlating tool_use_id
}

// streamEvent is the minimal structure needed to classify a stream-json line.
type streamEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
}

// messageEnvelope extracts message-level fields.
type messageEnvelope struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// contentBlock is a single content block inside an assistant or user message.
type contentBlock struct {
	Type      string          `json:"type"`                  // "text", "tool_use", "tool_result"
	Text      string          `json:"text,omitempty"`        // for type=text
	Name      string          `json:"name,omitempty"`        // for type=tool_use
	ID        string          `json:"id,omitempty"`          // for type=tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // for type=tool_result
	Input     json.RawMessage `json:"input,omitempty"`       // for type=tool_use
	Content   json.RawMessage `json:"content,omitempty"`     // for type=tool_result (string or array)
}

// extractDisplayText returns a human-readable summary of a stream-json line
// suitable for the TUI, or "" if the line has no displayable content
// (system init, user tool_result, result envelope, etc.).
func extractDisplayText(line []byte) string {
	var evt streamEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return ""
	}

	switch evt.Type {
	case "assistant":
		var msg messageEnvelope
		if err := json.Unmarshal(evt.Message, &msg); err != nil {
			return ""
		}
		var parts []string
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if text := strings.TrimSpace(block.Text); text != "" {
					parts = append(parts, text)
				}
			case "tool_use":
				parts = append(parts, "["+block.Name+"]")
			}
		}
		return strings.Join(parts, " ")

	default:
		return ""
	}
}

// shouldPersist returns true if the stream-json line should be included in the
// transcript at the given level. At TranscriptTools, only tool_use and
// tool_result events pass. At TranscriptFull, all assistant and user events
// pass. System and result events are always excluded.
func shouldPersist(line []byte, level TranscriptLevel) bool {
	if level == TranscriptOff {
		return false
	}

	var evt streamEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return false
	}

	switch evt.Type {
	case "system", "result":
		return false
	case "assistant":
		if level == TranscriptFull {
			return true
		}
		// TranscriptTools: only if the message contains a tool_use block.
		var msg messageEnvelope
		if err := json.Unmarshal(evt.Message, &msg); err != nil {
			return false
		}
		for _, block := range msg.Content {
			if block.Type == "tool_use" {
				return true
			}
		}
		return false
	case "user":
		if level == TranscriptFull {
			return true
		}
		// TranscriptTools: only if the message contains a tool_result block.
		var msg messageEnvelope
		if err := json.Unmarshal(evt.Message, &msg); err != nil {
			return false
		}
		for _, block := range msg.Content {
			if block.Type == "tool_result" {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// parseTranscriptEntry converts a stream-json line into transcript entries.
// A single line may produce multiple entries (e.g., text + tool_use in one message).
func parseTranscriptEntry(line []byte) []TranscriptEntry {
	var evt streamEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return nil
	}

	var msg messageEnvelope
	if err := json.Unmarshal(evt.Message, &msg); err != nil {
		return nil
	}

	var entries []TranscriptEntry
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				entries = append(entries, TranscriptEntry{
					Role:    "assistant",
					Content: text,
				})
			}
		case "tool_use":
			inputStr := string(block.Input)
			entries = append(entries, TranscriptEntry{
				Role:    "tool_use",
				Content: inputStr,
				Tool:    block.Name,
				ToolID:  block.ID,
			})
		case "tool_result":
			content := extractToolResultContent(block.Content)
			entries = append(entries, TranscriptEntry{
				Role:    "tool_result",
				Content: content,
				ToolID:  block.ToolUseID,
			})
		}
	}
	return entries
}

// extractToolResultContent extracts a string from tool_result content,
// which may be a JSON string or a JSON array of content blocks.
func extractToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try as a plain string first.
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}

	// Try as an array of content blocks (tool_result can wrap text blocks).
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, block := range blocks {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	// Fallback: return raw JSON string.
	return string(raw)
}

// FilterTranscript scans buffered stream-json output (JSONL) and returns
// transcript entries filtered by the given level. This is used by the sandbox
// runner which buffers stdout and processes it after the subprocess exits.
func FilterTranscript(output []byte, level TranscriptLevel) []TranscriptEntry {
	if level == TranscriptOff {
		return nil
	}

	var entries []TranscriptEntry
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if !shouldPersist(line, level) {
			continue
		}
		entries = append(entries, parseTranscriptEntry(line)...)
	}
	return entries
}
