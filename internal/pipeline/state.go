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

	if err := WriteMeta(metaPath, meta); err != nil {
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

// SocketPath returns the path to the broadcast Unix socket.
func (s *State) SocketPath() string {
	return filepath.Join(s.dir, "stream.sock")
}

// Meta returns the in-memory pipeline metadata. Callers should treat as read-only.
func (s *State) Meta() *PipelineMeta {
	return s.meta
}

// flushMeta writes the current meta to disk atomically.
func (s *State) flushMeta() error {
	return WriteMeta(filepath.Join(s.dir, "meta.json"), s.meta)
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
// Previous result and artifact files are renamed to <phase>.json.<gen> and <phase>.md.<gen>,
// preserving them for history/debugging while clearing the current slot for new output.
// This archival is what enables the ReworkFeedback reset on patch retry: when implement
// re-runs, its old result is archived, but the verify/review results that SOURCE the
// feedback remain current until those phases re-run and overwrite them.
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
	ps.TokensIn = 0
	ps.TokensOut = 0
	ps.CacheTokensIn = 0
	ps.EstimatedPromptTokens = 0
	ps.Error = ""
	ps.PlanHash = ""
	ps.PromptHash = ""
	ps.startedAt = time.Now()

	return s.flushMeta()
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

	return s.flushMeta()
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

	return s.flushMeta()
}

// ResetPhaseCosts zeroes the CumulativeCost for every phase in the pipeline
// metadata. This is called at the start of a fresh Run() so that per-phase
// budget enforcement (checkPhaseBudget) is not blocked by stale costs
// accumulated during a prior pipeline execution. Within a single execution,
// CumulativeCost is preserved across rework generations by MarkRunning/AccumulateCost.
func (s *State) ResetPhaseCosts() error {
	for _, ps := range s.meta.Phases {
		ps.CumulativeCost = 0
	}
	return s.flushMeta()
}

// AccumulateCost adds cost to the phase and total. Phase must exist (via MarkRunning).
// Both the per-generation Cost and the cross-generation CumulativeCost are updated.
func (s *State) AccumulateCost(phase string, cost float64) error {
	ps := s.meta.Phases[phase]
	if ps == nil {
		return fmt.Errorf("pipeline: accumulate cost: phase %q not started", phase)
	}
	ps.Cost += cost
	ps.CumulativeCost += cost
	s.meta.TotalCost += cost
	return s.flushMeta()
}

// AccumulateTokens adds token counts to the phase. Phase must exist (via MarkRunning).
// Counts are per-generation and zeroed on re-run by MarkRunning.
func (s *State) AccumulateTokens(phase string, tokensIn, tokensOut, cacheTokensIn int64) error {
	ps := s.meta.Phases[phase]
	if ps == nil {
		return fmt.Errorf("pipeline: accumulate tokens: phase %q not started", phase)
	}
	ps.TokensIn += tokensIn
	ps.TokensOut += tokensOut
	ps.CacheTokensIn += cacheTokensIn
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

// ReadArchivedResult reads an archived result file (<phase>.json.<generation>).
// Archived results are created by MarkRunning when a phase is re-run,
// preserving the previous generation's output for history and context.
func (s *State) ReadArchivedResult(phase string, generation int) (json.RawMessage, error) {
	path := fmt.Sprintf("%s.%d", filepath.Join(s.dir, phase+".json"), generation)
	data, err := os.ReadFile(path)
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
