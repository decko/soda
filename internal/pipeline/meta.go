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
	Status         PhaseStatus `json:"status"`
	Cost           float64     `json:"cost,omitempty"`
	CumulativeCost float64     `json:"cumulative_cost,omitempty"`
	DurationMs     int64       `json:"duration_ms,omitempty"`
	Error          string      `json:"error,omitempty"`
	Generation     int         `json:"generation,omitempty"`
	PlanHash       string      `json:"plan_hash,omitempty"`
	startedAt      time.Time
}

// PipelineMeta is the top-level state stored in meta.json.
type PipelineMeta struct {
	Ticket             string                 `json:"ticket"`
	Summary            string                 `json:"summary,omitempty"`
	Branch             string                 `json:"branch,omitempty"`
	Worktree           string                 `json:"worktree,omitempty"`
	StartedAt          time.Time              `json:"started_at"`
	TotalCost          float64                `json:"total_cost"`
	ReworkCycles       int                    `json:"rework_cycles,omitempty"`
	PatchCycles        int                    `json:"patch_cycles,omitempty"`
	EscalatedFromPatch bool                   `json:"escalated_from_patch,omitempty"`
	PatchRetryUsed     bool                   `json:"patch_retry_used,omitempty"`
	PreviousFailures   []string               `json:"previous_failures,omitempty"`
	Phases             map[string]*PhaseState `json:"phases"`
}

// CumulativeCost scans all session directories under stateDir and returns the
// sum of TotalCost from every meta.json. Directories without a valid meta.json
// are silently skipped. Returns 0 if the directory does not exist or is empty.
func CumulativeCost(stateDir string) (float64, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("pipeline: read state dir for cumulative cost: %w", err)
	}

	var total float64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metaPath := filepath.Join(stateDir, entry.Name(), "meta.json")
		meta, readErr := ReadMeta(metaPath)
		if readErr != nil {
			continue
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

// writeMeta marshals and writes meta to path atomically.
func writeMeta(path string, meta *PipelineMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("pipeline: marshal meta: %w", err)
	}
	data = append(data, '\n')
	return atomicWrite(path, data)
}
