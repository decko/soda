package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// OpencodeEvent represents a single JSONL event from the Opencode agent.
type OpencodeEvent struct {
	Type    string          `json:"type"`
	Content string          `json:"content,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Error   string          `json:"error,omitempty"`
	Usage   *OpencodeUsage  `json:"usage,omitempty"`
	Cost    *float64        `json:"cost_usd,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// OpencodeUsage holds token usage data from an Opencode event.
type OpencodeUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// OpencodeStreamResult holds the accumulated result of parsing an Opencode JSONL stream.
type OpencodeStreamResult struct {
	Output    json.RawMessage // structured result extracted from text
	RawText   string          // accumulated assistant text
	CostUSD   float64
	TokensIn  int64
	TokensOut int64
	Turns     int
}

// ParseOpencodeStream parses a JSONL byte stream from the Opencode agent and
// accumulates usage, cost, and output data. onChunk is called for each
// displayable text chunk; it may be nil.
func ParseOpencodeStream(data []byte, onChunk func(string)) (*OpencodeStreamResult, error) {
	result := &OpencodeStreamResult{}
	var textParts []string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	var lastErr error
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var event OpencodeEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Skip non-JSON lines (e.g., debug output).
			continue
		}

		switch event.Type {
		case "text":
			if event.Content != "" {
				textParts = append(textParts, event.Content)
				if onChunk != nil {
					onChunk(event.Content)
				}
			}

		case "tool_use":
			result.Turns++

		case "done":
			if event.Usage != nil {
				result.TokensIn += event.Usage.InputTokens
				result.TokensOut += event.Usage.OutputTokens
			}
			if event.Cost != nil {
				result.CostUSD += *event.Cost
			}

		case "result":
			if len(event.Result) > 0 && string(event.Result) != "null" {
				lastErr = nil
				result.Output = event.Result
			}
			if event.Content != "" {
				textParts = append(textParts, event.Content)
			}

		case "error":
			reason := classifyOpencodeError(event.Error)
			if reason != "unknown" {
				lastErr = &TransientError{
					Reason: reason,
					Err:    fmt.Errorf("opencode: %s", event.Error),
				}
			} else {
				lastErr = &ParseError{
					Err: fmt.Errorf("opencode: %s", event.Error),
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, &ParseError{Err: fmt.Errorf("opencode: scan stream: %w", err)}
	}

	// If the last event was an error, return it.
	if lastErr != nil {
		return nil, lastErr
	}

	result.RawText = strings.Join(textParts, "")

	// If no explicit result event was found, try extracting structured JSON
	// from the accumulated text.
	if result.Output == nil && result.RawText != "" {
		if extracted := extractJSONFromText(result.RawText); extracted != nil {
			result.Output = extracted
		}
	}

	return result, nil
}

// extractJSONFromText scans text for the last valid JSON object. It first
// looks for fenced ```json blocks, then falls back to the last substring
// that parses as a JSON object.
func extractJSONFromText(text string) json.RawMessage {
	// Try fenced JSON blocks — use the last one found.
	const startFence = "```json"
	const endFence = "```"

	var lastFenced json.RawMessage
	searchText := text
	for {
		startIdx := strings.Index(searchText, startFence)
		if startIdx < 0 {
			break
		}
		after := searchText[startIdx+len(startFence):]
		endIdx := strings.Index(after, endFence)
		if endIdx < 0 {
			break
		}
		candidate := strings.TrimSpace(after[:endIdx])
		if json.Valid([]byte(candidate)) {
			// Verify it's an object (starts with '{').
			trimmed := strings.TrimSpace(candidate)
			if len(trimmed) > 0 && trimmed[0] == '{' {
				lastFenced = json.RawMessage(trimmed)
			}
		}
		searchText = after[endIdx+len(endFence):]
	}
	if lastFenced != nil {
		return lastFenced
	}

	// Fallback: find the last valid JSON object in the text by scanning
	// for '{' and matching '}' from the end.
	for idx := len(text) - 1; idx >= 0; idx-- {
		if text[idx] == '}' {
			// Walk backwards to find a matching '{'.
			depth := 0
			for jdx := idx; jdx >= 0; jdx-- {
				switch text[jdx] {
				case '}':
					depth++
				case '{':
					depth--
				}
				if depth == 0 {
					candidate := text[jdx : idx+1]
					if json.Valid([]byte(candidate)) {
						return json.RawMessage(candidate)
					}
					break
				}
			}
		}
	}

	return nil
}

// ValidateOpencodeOutput checks that the structured output satisfies the
// required fields declared in the output schema. It parses the schema's
// "required" array and verifies each key exists in the output JSON object.
func ValidateOpencodeOutput(output json.RawMessage, schemaJSON string) error {
	if len(output) == 0 {
		return &ParseError{Err: fmt.Errorf("opencode: empty structured output")}
	}

	if schemaJSON == "" {
		return nil
	}

	// Extract the "required" array from the schema.
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		// Schema itself is invalid — not the agent's fault.
		return nil
	}

	if len(schema.Required) == 0 {
		return nil
	}

	// Parse the output as a generic map to check for required keys.
	var outputMap map[string]json.RawMessage
	if err := json.Unmarshal(output, &outputMap); err != nil {
		return &ParseError{Err: fmt.Errorf("opencode: output is not a JSON object: %w", err)}
	}

	var missing []string
	for _, key := range schema.Required {
		if _, ok := outputMap[key]; !ok {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		return &ParseError{
			Err: fmt.Errorf("opencode: output missing required fields: %s", strings.Join(missing, ", ")),
		}
	}

	return nil
}

// MapOpencodeToolName maps a runner-level tool name to the Opencode-equivalent
// tool name. Unknown tool names are returned unchanged.
func MapOpencodeToolName(tool string) string {
	mapping := map[string]string{
		"Read":   "read",
		"Write":  "write",
		"Edit":   "edit",
		"Glob":   "glob",
		"Grep":   "grep",
		"Bash":   "bash",
		"Search": "search",
	}

	// Handle parameterized tools like "Bash(git:*)" → "bash(git:*)"
	baseTool := tool
	param := ""
	if idx := strings.Index(tool, "("); idx >= 0 {
		baseTool = tool[:idx]
		param = tool[idx:]
	}

	if mapped, ok := mapping[baseTool]; ok {
		return mapped + param
	}

	return tool
}

// DeduplicateTools returns a copy of tools with duplicates removed,
// preserving the original order.
func DeduplicateTools(tools []string) []string {
	seen := make(map[string]bool, len(tools))
	var result []string
	for _, tool := range tools {
		if !seen[tool] {
			seen[tool] = true
			result = append(result, tool)
		}
	}
	return result
}

// classifyOpencodeError maps Opencode error messages to transient error reason categories.
func classifyOpencodeError(msg string) string {
	lower := strings.ToLower(msg)

	patterns := []struct {
		substrings []string
		reason     string
	}{
		{[]string{"rate limit", " 429", "too many requests"}, "rate_limit"},
		{[]string{"timeout", " 504", " 529"}, "timeout"},
		{[]string{"overloaded", " 500", " 502", " 503", "server error", "internal error"}, "overloaded"},
		{[]string{"connection refused", "econnreset", "connection reset"}, "connection"},
	}

	for _, pattern := range patterns {
		for _, sub := range pattern.substrings {
			if strings.Contains(lower, sub) {
				return pattern.reason
			}
		}
	}

	return "unknown"
}

// classifyOpencodeExitError categorizes an Opencode process exit failure.
func classifyOpencodeExitError(waitErr error, stderr []byte) error {
	stderrLower := strings.ToLower(string(stderr))

	patterns := []struct {
		substrings []string
		reason     string
	}{
		{[]string{"rate limit", " 429", "too many requests"}, "rate_limit"},
		{[]string{"timeout", " 504", " 529"}, "timeout"},
		{[]string{"overloaded", " 500", " 502", " 503", "server error", "internal error"}, "overloaded"},
		{[]string{"connection refused", "econnreset", "connection reset"}, "connection"},
	}

	for _, pattern := range patterns {
		for _, sub := range pattern.substrings {
			if strings.Contains(stderrLower, sub) {
				return &TransientError{
					Reason: pattern.reason,
					Err:    fmt.Errorf("opencode exited: %w", waitErr),
				}
			}
		}
	}

	return &TransientError{
		Reason: "unknown",
		Err:    fmt.Errorf("opencode exited: %w", waitErr),
	}
}
