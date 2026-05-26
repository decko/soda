package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// PiEvent represents a single JSONL event from the Pi coding agent.
type PiEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Content string          `json:"content,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	ToolID  string          `json:"tool_id,omitempty"`
	Error   string          `json:"error,omitempty"`
	Usage   *PiUsage        `json:"usage,omitempty"`
	Cost    *float64        `json:"cost_usd,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// PiUsage holds token usage data from a Pi message_end event.
type PiUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// PiStreamResult holds the accumulated result of parsing a Pi JSONL stream.
type PiStreamResult struct {
	Output    json.RawMessage // structured result from the final result event
	RawText   string          // accumulated assistant text
	CostUSD   float64
	TokensIn  int64
	TokensOut int64
	Turns     int
}

// ParsePiStream parses a JSONL byte stream from the Pi coding agent and
// accumulates usage, cost, and output data. onChunk is called for each
// displayable text chunk; it may be nil.
func ParsePiStream(data []byte, onChunk func(string)) (*PiStreamResult, error) {
	result := &PiStreamResult{}
	var textParts []string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	var lastErr error
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var event PiEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Skip non-JSON lines (e.g., debug output).
			continue
		}

		switch event.Type {
		case "assistant":
			if event.Content != "" {
				textParts = append(textParts, event.Content)
				if onChunk != nil {
					onChunk(event.Content)
				}
			}

		case "tool_use":
			result.Turns++

		case "message_end":
			if event.Usage != nil {
				result.TokensIn += event.Usage.InputTokens
				result.TokensOut += event.Usage.OutputTokens
			}
			if event.Cost != nil {
				result.CostUSD += *event.Cost
			}

		case "result":
			if event.Subtype == "error" {
				lastErr = &SemanticError{Message: event.Error}
				continue
			}
			if len(event.Result) > 0 && string(event.Result) != "null" {
				result.Output = event.Result
			}
			if event.Content != "" {
				textParts = append(textParts, event.Content)
			}

		case "error":
			// Classify Pi error events as transient errors.
			reason := classifyPiError(event.Error)
			lastErr = &TransientError{
				Reason: reason,
				Err:    fmt.Errorf("pi: %s", event.Error),
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, &ParseError{Err: fmt.Errorf("pi: scan stream: %w", err)}
	}

	// If the last event was an error, return it.
	if lastErr != nil {
		return nil, lastErr
	}

	result.RawText = strings.Join(textParts, "")
	return result, nil
}

// ValidatePiOutput checks that the structured output satisfies the required
// fields declared in the output schema. It parses the schema's "required"
// array and verifies each key exists in the output JSON object.
func ValidatePiOutput(output json.RawMessage, schemaJSON string) error {
	if len(output) == 0 {
		return &ParseError{Err: fmt.Errorf("pi: empty structured output")}
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
		return &ParseError{Err: fmt.Errorf("pi: output is not a JSON object: %w", err)}
	}

	var missing []string
	for _, key := range schema.Required {
		if _, ok := outputMap[key]; !ok {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		return &ParseError{
			Err: fmt.Errorf("pi: output missing required fields: %s", strings.Join(missing, ", ")),
		}
	}

	return nil
}

// MapPiToolName maps a runner-level tool name to the Pi-equivalent tool name.
// Pi uses a different naming convention than Claude Code for some tools.
// Unknown tool names are returned unchanged.
func MapPiToolName(tool string) string {
	mapping := map[string]string{
		"Read":   "read_file",
		"Write":  "write_file",
		"Edit":   "edit_file",
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

// classifyPiError maps Pi error messages to transient error reason categories.
func classifyPiError(msg string) string {
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
