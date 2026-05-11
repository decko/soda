package claude

import (
	"encoding/json"
	"time"
)

// RunOpts holds per-invocation configuration for a single phase run.
type RunOpts struct {
	SystemPromptPath string          // path to system prompt file
	SettingsPath     string          // path to Claude Code settings JSON (--settings-path)
	OutputSchema     string          // JSON schema string passed to --json-schema
	AllowedTools     []string        // tool names for --allowed-tools
	MaxBudgetUSD     *float64        // nil = omit flag; non-nil = emit value
	Prompt           string          // rendered template piped via stdin
	Model            string          // per-phase model override; empty uses runner-level default
	Timeout          time.Duration   // fallback timeout if caller's context has no deadline
	TranscriptLevel  TranscriptLevel // transcript capture level; empty/"off" disables
}

// RunResult holds the parsed response from a Claude Code CLI invocation.
type RunResult struct {
	Output     json.RawMessage // raw structured_output — caller unmarshals into phase schema
	Result     string          // freeform text from "result" field
	CostUSD    float64         // 0.0 if absent — per-invocation, not cumulative
	Tokens     TokenUsage
	Duration   time.Duration     // parsed from duration_ms
	Turns      int               // 0 if absent
	Transcript []TranscriptEntry // filtered transcript entries; nil when capture is disabled
}

// TokenUsage holds token counts from the CLI response.
type TokenUsage struct {
	InputTokens              int64            `json:"input_tokens"`
	CacheCreationInputTokens int64            `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64            `json:"cache_read_input_tokens"`
	OutputTokens             int64            `json:"output_tokens"`
	Extra                    map[string]int64 `json:"-"` // overflow for unknown token categories
}

// DryRunResult holds the command that would be executed, for logging.
type DryRunResult struct {
	Args   []string
	Prompt string
}
