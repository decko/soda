package runner

import (
	"context"
	"encoding/json"
	"time"
)

// Runner executes a single pipeline phase in an isolated session.
// The concrete implementation will be the sandbox runner (#2).
type Runner interface {
	Run(ctx context.Context, opts RunOpts) (*RunResult, error)
}

// RunOpts holds everything needed to execute one phase.
type RunOpts struct {
	Phase        string        // phase name (e.g., "triage", "plan")
	SystemPrompt string        // rendered system prompt content
	UserPrompt   string        // rendered user prompt (ticket + artifacts)
	OutputSchema string        // JSON schema for structured output
	AllowedTools []string      // tool scoping per phase
	MaxBudgetUSD float64       // cost cap for this phase
	WorkDir      string        // working directory for the agent
	Model        string        // model to use
	Timeout      time.Duration // phase timeout
}

// RunResult holds the parsed response from a phase execution.
type RunResult struct {
	Output        json.RawMessage // structured output matching the phase schema
	RawText       string          // freeform text output
	CostUSD       float64
	TokensIn      int64
	TokensOut     int64
	CacheTokensIn int64 // tokens served from prompt cache (0 if unsupported)
	DurationMs    int64
	Turns         int
}
