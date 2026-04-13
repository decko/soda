package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// State manages the disk state for a single ticket's pipeline run.
// Not safe for concurrent use.
type State struct {
	dir    string
	ticket string
	meta   *PipelineMeta
	lockFd *os.File
}

// LoadOrCreate loads existing state from stateDir/ticketKey, or creates a new one.
// Returns the State with meta loaded but lock not acquired.
func LoadOrCreate(stateDir, ticketKey string) (*State, error) {
	if err := validateTicketKey(ticketKey); err != nil {
		return nil, err
	}

	dir := filepath.Join(stateDir, ticketKey)
	metaPath := filepath.Join(dir, "meta.json")

	meta, err := ReadMeta(metaPath)
	if err == nil {
		return &State{dir: dir, ticket: ticketKey, meta: meta}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0755); err != nil {
		return nil, fmt.Errorf("pipeline: create state dir %s: %w", dir, err)
	}

	meta = &PipelineMeta{
		Ticket:    ticketKey,
		StartedAt: time.Now(),
		Phases:    make(map[string]*PhaseState),
	}

	if err := writeMeta(metaPath, meta); err != nil {
		return nil, err
	}

	return &State{dir: dir, ticket: ticketKey, meta: meta}, nil
}

// AcquireLock acquires an exclusive file lock for this ticket.
func (s *State) AcquireLock() error {
	fd, err := acquireLock(s.dir)
	if err != nil {
		return fmt.Errorf("pipeline: acquire lock %s: %w", s.dir, err)
	}
	s.lockFd = fd
	return nil
}

// ReleaseLock releases the file lock. Safe to call if lock is not held.
func (s *State) ReleaseLock() {
	releaseLock(s.lockFd)
	s.lockFd = nil
}

// Dir returns the state directory path.
func (s *State) Dir() string {
	return s.dir
}

// Meta returns the in-memory pipeline metadata. Callers should treat as read-only.
func (s *State) Meta() *PipelineMeta {
	return s.meta
}

// flushMeta writes the current meta to disk atomically.
func (s *State) flushMeta() error {
	return writeMeta(filepath.Join(s.dir, "meta.json"), s.meta)
}

// validateTicketKey checks for empty strings and path traversal.
func validateTicketKey(key string) error {
	if key == "" {
		return fmt.Errorf("pipeline: ticket key must not be empty")
	}
	if strings.Contains(key, "/") || strings.Contains(key, "\\") || strings.Contains(key, "..") {
		return fmt.Errorf("pipeline: invalid ticket key %q: must not contain path separators or '..'", key)
	}
	return nil
}

// IsCompleted returns true if the given phase has completed.
func (s *State) IsCompleted(phase string) bool {
	ps := s.meta.Phases[phase]
	return ps != nil && ps.Status == PhaseCompleted
}

// MarkRunning marks a phase as running, archives previous artifacts, and increments generation.
func (s *State) MarkRunning(phase string) error {
	ps := s.meta.Phases[phase]
	if ps == nil {
		ps = &PhaseState{Generation: 1}
		s.meta.Phases[phase] = ps
	} else {
		resultPath := filepath.Join(s.dir, phase+".json")
		artifactPath := filepath.Join(s.dir, phase+".md")
		if err := archiveArtifact(resultPath, ps.Generation); err != nil {
			return err
		}
		if err := archiveArtifact(artifactPath, ps.Generation); err != nil {
			return err
		}
		ps.Generation++
	}

	ps.Status = PhaseRunning
	ps.Cost = 0
	ps.DurationMs = 0
	ps.Error = ""
	ps.PlanHash = ""
	ps.startedAt = time.Now()

	if err := s.flushMeta(); err != nil {
		return err
	}

	return s.LogEvent(Event{
		Phase: phase,
		Kind:  "phase_started",
		Data:  map[string]any{"generation": ps.Generation},
	})
}

// MarkCompleted marks a phase as completed with its duration.
func (s *State) MarkCompleted(phase string) error {
	ps := s.meta.Phases[phase]
	if ps == nil {
		return fmt.Errorf("pipeline: mark completed: phase %q not found", phase)
	}

	ps.Status = PhaseCompleted
	if !ps.startedAt.IsZero() {
		ps.DurationMs = time.Since(ps.startedAt).Milliseconds()
	}

	if err := s.flushMeta(); err != nil {
		return err
	}

	return s.LogEvent(Event{
		Phase: phase,
		Kind:  "phase_completed",
		Data: map[string]any{
			"duration_ms": ps.DurationMs,
			"cost":        ps.Cost,
		},
	})
}

// MarkFailed marks a phase as failed with the error and duration.
func (s *State) MarkFailed(phase string, phaseErr error) error {
	ps := s.meta.Phases[phase]
	if ps == nil {
		return fmt.Errorf("pipeline: mark failed: phase %q not found", phase)
	}

	ps.Status = PhaseFailed
	ps.Error = phaseErr.Error()
	if !ps.startedAt.IsZero() {
		ps.DurationMs = time.Since(ps.startedAt).Milliseconds()
	}

	if err := s.flushMeta(); err != nil {
		return err
	}

	return s.LogEvent(Event{
		Phase: phase,
		Kind:  "phase_failed",
		Data: map[string]any{
			"error":       ps.Error,
			"duration_ms": ps.DurationMs,
		},
	})
}

// AccumulateCost adds cost to the phase and total. Phase must exist (via MarkRunning).
func (s *State) AccumulateCost(phase string, cost float64) error {
	ps := s.meta.Phases[phase]
	if ps == nil {
		return fmt.Errorf("pipeline: accumulate cost: phase %q not started", phase)
	}
	ps.Cost += cost
	s.meta.TotalCost += cost
	return s.flushMeta()
}

// WriteArtifact writes handoff content (<phase>.md) atomically.
func (s *State) WriteArtifact(phase string, content []byte) error {
	path := filepath.Join(s.dir, phase+".md")
	if err := atomicWrite(path, content); err != nil {
		return fmt.Errorf("pipeline: write artifact %s: %w", path, err)
	}
	return nil
}

// ReadArtifact reads handoff content (<phase>.md).
func (s *State) ReadArtifact(phase string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.dir, phase+".md"))
}

// WriteResult writes structured output (<phase>.json) atomically.
func (s *State) WriteResult(phase string, result json.RawMessage) error {
	path := filepath.Join(s.dir, phase+".json")
	if err := atomicWrite(path, result); err != nil {
		return fmt.Errorf("pipeline: write result %s: %w", path, err)
	}
	return nil
}

// ReadResult reads structured output (<phase>.json).
func (s *State) ReadResult(phase string) (json.RawMessage, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, phase+".json"))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// WriteLog writes a debug log file (logs/<phase>_<suffix>.md).
func (s *State) WriteLog(phase, suffix string, content []byte) error {
	logsDir := filepath.Join(s.dir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("pipeline: create logs dir: %w", err)
	}
	path := filepath.Join(logsDir, phase+"_"+suffix+".md")
	if err := os.WriteFile(path, content, 0644); err != nil {
		return fmt.Errorf("pipeline: write log %s: %w", path, err)
	}
	return nil
}

// LogEvent appends a structured event to events.jsonl.
func (s *State) LogEvent(event Event) error {
	return logEvent(s.dir, event)
}
