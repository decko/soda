package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var errNoJSON = errors.New("no JSON response envelope found in output")

// rawEnvelope is the intermediate representation of the Claude CLI JSON response.
type rawEnvelope struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	Result     string          `json:"result"`
	Structured json.RawMessage `json:"structured_output"`
	Cost       *float64        `json:"total_cost_usd"`
	Usage      json.RawMessage `json:"usage"`
	NumTurns   *int            `json:"num_turns"`
	Duration   *int64          `json:"duration_ms"`
}

// ParseResponse parses raw CLI output into a RunResult.
// Exported so the sandbox layer can reuse it independently of Stream().
func ParseResponse(output []byte) (*RunResult, error) {
	raw, err := extractJSON(output)
	if err != nil {
		return nil, &ParseError{
			Raw: truncateForLog(output, 4096),
			Err: err,
		}
	}

	var env rawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &ParseError{
			Raw: truncateForLog(raw, 4096),
			Err: fmt.Errorf("unmarshal response: %w", err),
		}
	}

	if env.Type != "result" {
		return nil, &ParseError{
			Raw: truncateForLog(raw, 4096),
			Err: fmt.Errorf("unexpected response type: %q", env.Type),
		}
	}

	if env.Subtype == "error" {
		return nil, &SemanticError{
			Message: env.Result,
		}
	}

	result := &RunResult{
		Result: env.Result,
		Tokens: parseTokenUsage(env.Usage),
	}

	// Normalize structured_output: nil and JSON "null" both become nil
	if len(env.Structured) > 0 && string(env.Structured) != "null" {
		result.Output = env.Structured
	}

	if env.Cost != nil {
		result.CostUSD = *env.Cost
	}
	if env.NumTurns != nil {
		result.Turns = *env.NumTurns
	}
	if env.Duration != nil {
		result.Duration = time.Duration(*env.Duration) * time.Millisecond
	}

	return result, nil
}

// extractJSON finds the JSON response envelope in the output.
// Strategy 1: try the last non-empty line (common case — envelope on final line).
// Strategy 2: backward brace scan for multi-line JSON.
func extractJSON(output []byte) ([]byte, error) {
	if len(output) == 0 {
		return nil, errNoJSON
	}

	// Strategy 1: last non-empty line
	trimmed := bytes.TrimRight(output, " \t\n\r")
	if len(trimmed) == 0 {
		return nil, errNoJSON
	}

	lastNewline := bytes.LastIndexByte(trimmed, '\n')
	var lastLine []byte
	if lastNewline == -1 {
		lastLine = trimmed
	} else {
		lastLine = trimmed[lastNewline+1:]
	}

	if len(lastLine) > 0 && lastLine[0] == '{' && json.Valid(lastLine) && isResultEnvelope(lastLine) {
		return lastLine, nil
	}

	// Strategy 2: backward scan — find last '}', try '{' candidates with json.Valid
	return extractJSONByDepth(output)
}

// isResultEnvelope checks if JSON data contains "type":"result".
func isResultEnvelope(data []byte) bool {
	var check struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(data, &check) == nil && check.Type == "result"
}

// extractJSONByDepth scans backwards for valid JSON objects in the output,
// preferring result envelopes over other valid JSON. Tries each closing '}'
// as a potential endpoint to handle multiple top-level JSON objects.
// Falls back to the outermost valid non-envelope JSON for diagnostics.
func extractJSONByDepth(output []byte) ([]byte, error) {
	var fallback []byte

	// Scan each '}' as a potential JSON endpoint, from last to first.
	for end := len(output) - 1; end >= 0; end-- {
		if output[end] != '}' {
			continue
		}

		for i := end; i >= 0; i-- {
			if output[i] == '{' {
				candidate := output[i : end+1]
				if json.Valid(candidate) {
					if isResultEnvelope(candidate) {
						return candidate, nil
					}
					if fallback == nil {
						fallback = candidate
					}
					break // found valid JSON for this end; try next '}'
				}
			}
		}
	}

	if fallback != nil {
		return fallback, nil
	}
	return nil, errNoJSON
}

// parseTokenUsage extracts token counts from the usage JSON.
// Known fields map to TokenUsage struct fields; unknown fields go to Extra.
func parseTokenUsage(raw json.RawMessage) TokenUsage {
	if len(raw) == 0 {
		return TokenUsage{}
	}

	var usage TokenUsage
	_ = json.Unmarshal(raw, &usage)

	// Collect unknown fields into Extra
	var all map[string]float64
	if err := json.Unmarshal(raw, &all); err != nil {
		return usage
	}

	known := map[string]bool{
		"input_tokens":                true,
		"cache_creation_input_tokens": true,
		"cache_read_input_tokens":     true,
		"output_tokens":               true,
	}

	for key, val := range all {
		if !known[key] {
			if usage.Extra == nil {
				usage.Extra = make(map[string]int64)
			}
			usage.Extra[key] = int64(val)
		}
	}

	return usage
}

// truncateForLog returns data truncated to maxLen bytes for log readability.
func truncateForLog(data []byte, maxLen int) []byte {
	if len(data) <= maxLen {
		return data
	}
	truncated := make([]byte, maxLen+3)
	copy(truncated, data[:maxLen])
	copy(truncated[maxLen:], "...")
	return truncated
}
