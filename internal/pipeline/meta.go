package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
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
)

// PhaseState holds the status and metrics for a single phase.
type PhaseState struct {
	Status     PhaseStatus `json:"status"`
	Cost       float64     `json:"cost,omitempty"`
	DurationMs int64       `json:"duration_ms,omitempty"`
	Error      string      `json:"error,omitempty"`
	Generation int         `json:"generation,omitempty"`
	startedAt  time.Time
}

// PipelineMeta is the top-level state stored in meta.json.
type PipelineMeta struct {
	Ticket    string                 `json:"ticket"`
	Summary   string                 `json:"summary,omitempty"`
	Branch    string                 `json:"branch,omitempty"`
	Worktree  string                 `json:"worktree,omitempty"`
	StartedAt time.Time              `json:"started_at"`
	TotalCost float64                `json:"total_cost"`
	Phases    map[string]*PhaseState `json:"phases"`
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
