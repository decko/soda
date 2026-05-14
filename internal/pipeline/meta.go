package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PhaseStatus represents the status of a pipeline phase.
type PhaseStatus string

const (
	PhasePending   PhaseStatus = "pending"
	PhaseRunning   PhaseStatus = "running"
	PhaseCompleted PhaseStatus = "completed"
	PhaseFailed    PhaseStatus = "failed"
	PhaseRetrying  PhaseStatus = "retrying"
	PhasePaused    PhaseStatus = "paused"
	PhaseSkipped   PhaseStatus = "skipped"
)

// PhaseState holds the status and metrics for a single phase.
type PhaseState struct {
	Status                PhaseStatus `json:"status"`
	Cost                  float64     `json:"cost,omitempty"`
	CumulativeCost        float64     `json:"cumulative_cost,omitempty"`
	DurationMs            int64       `json:"duration_ms,omitempty"`
	TokensIn              int64       `json:"tokens_in,omitempty"`
	TokensOut             int64       `json:"tokens_out,omitempty"`
	CacheTokensIn         int64       `json:"cache_tokens_in,omitempty"`
	Error                 string      `json:"error,omitempty"`
	Generation            int         `json:"generation,omitempty"`
	PlanHash              string      `json:"plan_hash,omitempty"`
	PromptHash            string      `json:"prompt_hash,omitempty"`             // SHA-256 hex digest of the rendered prompt sent to the LLM
	EstimatedPromptTokens int64       `json:"estimated_prompt_tokens,omitempty"` // pre-invocation estimate: len(rendered) / 3.3
	ModelUsed             string      `json:"model_used,omitempty"`              // resolved model name used for this phase execution
	ParseAttempts         int         `json:"parse_attempts,omitempty"`          // number of parse retry attempts during this execution
	ParseSuccessOnFirst   bool        `json:"parse_success_on_first,omitempty"`  // true if output parsed successfully on the first attempt
	TransientRetries      int         `json:"transient_retries,omitempty"`       // number of transient-error retries during this execution
	ParseRetries          int         `json:"parse_retries,omitempty"`           // number of parse-error retries during this execution
	SemanticRetries       int         `json:"semantic_retries,omitempty"`        // number of semantic-error retries during this execution
	FailureCategory       string      `json:"failure_category,omitempty"`        // error classification on terminal failure (e.g. "transient", "gate", "timeout")
	startedAt             time.Time
}

// PipelineMeta is the top-level state stored in meta.json.
type PipelineMeta struct {
	Ticket             string                 `json:"ticket"`
	Summary            string                 `json:"summary,omitempty"`
	Pipeline           string                 `json:"pipeline,omitempty"` // pipeline name used for this run; empty means "default"
	Branch             string                 `json:"branch,omitempty"`
	Worktree           string                 `json:"worktree,omitempty"`
	StartedAt          time.Time              `json:"started_at"`
	BinaryVersion      string                 `json:"binary_version,omitempty"`
	TotalCost          float64                `json:"total_cost"`
	ReworkCycles       int                    `json:"rework_cycles,omitempty"`
	PatchCycles        int                    `json:"patch_cycles,omitempty"`
	EscalatedFromPatch bool                   `json:"escalated_from_patch,omitempty"`
	PatchRetryUsed     bool                   `json:"patch_retry_used,omitempty"`
	Complexity         string                 `json:"complexity,omitempty"`
	PreviousFailures   []string               `json:"previous_failures,omitempty"`
	Phases             map[string]*PhaseState `json:"phases"`
}

// CumulativeCost returns the total spend across all pipeline runs. It first
// sums all entries from the persistent cost ledger (stateDir/cost.json), which
// survives soda clean. For backward compatibility it also adds TotalCost from
// meta.json files belonging to sessions that have no ledger entries yet.
func CumulativeCost(stateDir string) (float64, error) {
	// Sum all entries from the persistent ledger (survives soda clean).
	ledger, err := ReadCostLedger(stateDir)
	if err != nil {
		return 0, fmt.Errorf("pipeline: read cost ledger for cumulative cost: %w", err)
	}
	inLedger := make(map[string]bool, len(ledger))
	var total float64
	for _, e := range ledger {
		inLedger[e.Ticket] = true
		total += e.Cost
	}

	// Also sum from meta.json for sessions not yet tracked in the ledger
	// (legacy sessions that predate the ledger feature).
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return total, nil
		}
		return 0, fmt.Errorf("pipeline: read state dir for cumulative cost: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metaPath := filepath.Join(stateDir, entry.Name(), "meta.json")
		meta, readErr := ReadMeta(metaPath)
		if readErr != nil {
			continue
		}
		if inLedger[meta.Ticket] {
			continue // already counted via ledger; skip to avoid double-counting
		}
		total += meta.TotalCost
	}
	return total, nil
}

// ReadMeta reads and unmarshals a meta.json file.
func ReadMeta(path string) (*PipelineMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pipeline: read meta %s: %w", path, err)
	}
	var meta PipelineMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("pipeline: parse meta %s: %w", path, err)
	}
	if meta.Phases == nil {
		meta.Phases = make(map[string]*PhaseState)
	}
	return &meta, nil
}

// WriteMeta marshals and writes meta to path atomically.
func WriteMeta(path string, meta *PipelineMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("pipeline: marshal meta: %w", err)
	}
	data = append(data, '\n')
	return atomicWrite(path, data)
}
