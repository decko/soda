# Pipeline State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement file-based pipeline state management in `internal/pipeline/` with atomic writes, flock-based locking, per-phase generation tracking, and structured event logging (decko/soda#3).

**Architecture:** A single `State` struct owns a `.soda/<ticket>/` directory and exposes all operations. Meta is loaded once and flushed atomically on every mutation. File-level separation by concern: `atomic.go`, `meta.go`, `events.go`, `lock.go`, `state.go`.

**Tech Stack:** Go 1.25 stdlib only — `encoding/json`, `os`, `syscall`, `path/filepath`, `time`, `fmt`, `errors`, `strings`

---

## File Structure

| File | Creates/Modifies | Responsibility |
|------|-----------------|----------------|
| `internal/pipeline/atomic.go` | Create | `atomicWrite`, `archiveArtifact` helpers |
| `internal/pipeline/atomic_test.go` | Create | Tests for atomic write + archival |
| `internal/pipeline/meta.go` | Create | `PhaseStatus`, `PhaseState`, `PipelineMeta`, `readMeta`, `writeMeta` |
| `internal/pipeline/meta_test.go` | Create | Tests for meta serialization round-trip |
| `internal/pipeline/events.go` | Create | `Event` type, `logEvent` JSONL appender |
| `internal/pipeline/events_test.go` | Create | Tests for event logging |
| `internal/pipeline/lock.go` | Create | `acquireLock`, `releaseLock`, stale PID detection |
| `internal/pipeline/lock_test.go` | Create | Tests for locking and contention |
| `internal/pipeline/state.go` | Create | `State` struct, `LoadOrCreate`, phase management, artifacts, logs |
| `internal/pipeline/state_test.go` | Create | Lifecycle, phase management, artifact, and integration tests |

---

### Task 1: Atomic Write and Archive Helpers

**Files:**
- Create: `internal/pipeline/atomic.go`
- Create: `internal/pipeline/atomic_test.go`

- [ ] **Step 1: Write failing tests for atomicWrite and archiveArtifact**

```go
// internal/pipeline/atomic_test.go
package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	t.Run("writes_file_atomically", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.json")

		if err := atomicWrite(path, []byte(`{"key":"value"}`)); err != nil {
			t.Fatalf("atomicWrite: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != `{"key":"value"}` {
			t.Errorf("content = %q, want %q", got, `{"key":"value"}`)
		}

		// Verify no .tmp file left behind
		tmpPath := path + ".tmp"
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Errorf("temp file should not exist, got err: %v", err)
		}
	})

	t.Run("overwrites_existing_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.json")

		if err := atomicWrite(path, []byte("first")); err != nil {
			t.Fatalf("first write: %v", err)
		}
		if err := atomicWrite(path, []byte("second")); err != nil {
			t.Fatalf("second write: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "second" {
			t.Errorf("content = %q, want %q", got, "second")
		}
	})

	t.Run("orphaned_tmp_does_not_corrupt", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.json")

		// Write the real file
		if err := atomicWrite(path, []byte("real")); err != nil {
			t.Fatalf("atomicWrite: %v", err)
		}

		// Simulate a crash: leave an orphaned .tmp file
		tmpPath := path + ".tmp"
		if err := os.WriteFile(tmpPath, []byte("orphaned"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		// A new atomicWrite should overwrite the orphaned .tmp and succeed
		if err := atomicWrite(path, []byte("updated")); err != nil {
			t.Fatalf("atomicWrite after orphan: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "updated" {
			t.Errorf("content = %q, want %q", got, "updated")
		}
	})
}

func TestArchiveArtifact(t *testing.T) {
	t.Run("renames_existing_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "verify.json")

		if err := os.WriteFile(path, []byte("gen1"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := archiveArtifact(path, 1); err != nil {
			t.Fatalf("archiveArtifact: %v", err)
		}

		// Original should be gone
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("original file should not exist after archive")
		}

		// Archived file should exist
		archived, err := os.ReadFile(path + ".1")
		if err != nil {
			t.Fatalf("archived file: %v", err)
		}
		if string(archived) != "gen1" {
			t.Errorf("archived content = %q, want %q", archived, "gen1")
		}
	})

	t.Run("noop_when_file_missing", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "nonexistent.json")

		if err := archiveArtifact(path, 1); err != nil {
			t.Fatalf("archiveArtifact on missing file: %v", err)
		}
	})

	t.Run("multiple_generations", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "verify.json")

		// Create and archive generation 1
		os.WriteFile(path, []byte("gen1"), 0644)
		archiveArtifact(path, 1)

		// Create and archive generation 2
		os.WriteFile(path, []byte("gen2"), 0644)
		archiveArtifact(path, 2)

		// Both archives should exist with correct content
		got1, _ := os.ReadFile(path + ".1")
		got2, _ := os.ReadFile(path + ".2")
		if string(got1) != "gen1" {
			t.Errorf("gen1 content = %q", got1)
		}
		if string(got2) != "gen2" {
			t.Errorf("gen2 content = %q", got2)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestAtomicWrite|TestArchiveArtifact"`
Expected: Compilation error — `atomicWrite` and `archiveArtifact` not defined

- [ ] **Step 3: Implement atomicWrite and archiveArtifact**

```go
// internal/pipeline/atomic.go
package pipeline

import (
	"fmt"
	"os"
)

// atomicWrite writes data to path atomically: write to .tmp, fsync, rename.
// Protects against both process crash and power loss.
func atomicWrite(path string, data []byte) error {
	tmpPath := path + ".tmp"

	fd, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("pipeline: create temp file %s: %w", tmpPath, err)
	}

	if _, err := fd.Write(data); err != nil {
		fd.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("pipeline: write temp file %s: %w", tmpPath, err)
	}

	if err := fd.Sync(); err != nil {
		fd.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("pipeline: sync temp file %s: %w", tmpPath, err)
	}

	if err := fd.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("pipeline: close temp file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("pipeline: rename %s to %s: %w", tmpPath, path, err)
	}

	return nil
}

// archiveArtifact renames path to path.<generation> if the file exists.
// Returns nil if the file does not exist (nothing to archive).
func archiveArtifact(path string, generation int) error {
	archivePath := fmt.Sprintf("%s.%d", path, generation)
	err := os.Rename(path, archivePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pipeline: archive %s to %s: %w", path, archivePath, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestAtomicWrite|TestArchiveArtifact"`
Expected: All 6 tests PASS

- [ ] **Step 5: Commit**

```bash
cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state
git add internal/pipeline/atomic.go internal/pipeline/atomic_test.go
git commit -m "feat(pipeline): add atomicWrite and archiveArtifact helpers"
```

---

### Task 2: Types and Meta Serialization

**Files:**
- Create: `internal/pipeline/meta.go`
- Create: `internal/pipeline/meta_test.go`

- [ ] **Step 1: Write failing tests for meta round-trip**

```go
// internal/pipeline/meta_test.go
package pipeline

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	now := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	original := &PipelineMeta{
		Ticket:    "PROJ-123",
		Branch:    "feat/thing",
		Worktree:  "/tmp/wt",
		StartedAt: now,
		TotalCost: 1.23,
		Phases: map[string]*PhaseState{
			"triage": {
				Status:     PhaseCompleted,
				Cost:       0.12,
				DurationMs: 8000,
				Generation: 1,
			},
			"plan": {
				Status:     PhaseRunning,
				Cost:       0.31,
				DurationMs: 0,
				Generation: 2,
			},
			"implement": {
				Status: PhaseFailed,
				Error:  "test failure",
			},
		},
	}

	if err := writeMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	loaded, err := readMeta(path)
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}

	if loaded.Ticket != original.Ticket {
		t.Errorf("Ticket = %q, want %q", loaded.Ticket, original.Ticket)
	}
	if loaded.Branch != original.Branch {
		t.Errorf("Branch = %q, want %q", loaded.Branch, original.Branch)
	}
	if loaded.Worktree != original.Worktree {
		t.Errorf("Worktree = %q, want %q", loaded.Worktree, original.Worktree)
	}
	if !loaded.StartedAt.Equal(original.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", loaded.StartedAt, original.StartedAt)
	}
	if loaded.TotalCost != original.TotalCost {
		t.Errorf("TotalCost = %v, want %v", loaded.TotalCost, original.TotalCost)
	}

	// Verify phase states
	triagePhase := loaded.Phases["triage"]
	if triagePhase == nil {
		t.Fatal("triage phase missing")
	}
	if triagePhase.Status != PhaseCompleted {
		t.Errorf("triage status = %q, want %q", triagePhase.Status, PhaseCompleted)
	}
	if triagePhase.Cost != 0.12 {
		t.Errorf("triage cost = %v, want 0.12", triagePhase.Cost)
	}
	if triagePhase.DurationMs != 8000 {
		t.Errorf("triage duration = %d, want 8000", triagePhase.DurationMs)
	}

	planPhase := loaded.Phases["plan"]
	if planPhase == nil {
		t.Fatal("plan phase missing")
	}
	if planPhase.Generation != 2 {
		t.Errorf("plan generation = %d, want 2", planPhase.Generation)
	}

	implPhase := loaded.Phases["implement"]
	if implPhase == nil {
		t.Fatal("implement phase missing")
	}
	if implPhase.Error != "test failure" {
		t.Errorf("implement error = %q, want %q", implPhase.Error, "test failure")
	}
}

func TestReadMeta_InitializesNilPhasesMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	// Write JSON without a phases key
	if err := atomicWrite(path, []byte(`{"ticket":"T-1","started_at":"2026-04-11T10:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}

	meta, err := readMeta(path)
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if meta.Phases == nil {
		t.Error("Phases should be initialized to non-nil map")
	}
}

func TestReadMeta_FileNotExist(t *testing.T) {
	_, err := readMeta("/nonexistent/path/meta.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestPhaseStatusConstants(t *testing.T) {
	// Verify JSON serialization matches expected strings
	tests := []struct {
		status PhaseStatus
		want   string
	}{
		{PhasePending, "pending"},
		{PhaseRunning, "running"},
		{PhaseCompleted, "completed"},
		{PhaseFailed, "failed"},
		{PhaseRetrying, "retrying"},
		{PhasePaused, "paused"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("PhaseStatus %v = %q, want %q", tt.status, tt.status, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestMeta|TestPhaseStatus|TestReadMeta"`
Expected: Compilation error — types not defined

- [ ] **Step 3: Implement types, readMeta, and writeMeta**

```go
// internal/pipeline/meta.go
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
	Branch    string                 `json:"branch,omitempty"`
	Worktree  string                 `json:"worktree,omitempty"`
	StartedAt time.Time              `json:"started_at"`
	TotalCost float64                `json:"total_cost"`
	Phases    map[string]*PhaseState `json:"phases"`
}

// readMeta reads and unmarshals a meta.json file.
func readMeta(path string) (*PipelineMeta, error) {
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestMeta|TestPhaseStatus|TestReadMeta"`
Expected: All 4 tests PASS

- [ ] **Step 5: Commit**

```bash
cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state
git add internal/pipeline/meta.go internal/pipeline/meta_test.go
git commit -m "feat(pipeline): add types and meta.json serialization"
```

---

### Task 3: Event Logging

**Files:**
- Create: `internal/pipeline/events.go`
- Create: `internal/pipeline/events_test.go`

- [ ] **Step 1: Write failing tests for logEvent**

```go
// internal/pipeline/events_test.go
package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogEvent(t *testing.T) {
	t.Run("appends_event_to_jsonl", func(t *testing.T) {
		dir := t.TempDir()
		ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)

		event := Event{
			Timestamp: ts,
			Phase:     "triage",
			Kind:      "phase_started",
			Data:      map[string]any{"generation": 1},
		}

		if err := logEvent(dir, event); err != nil {
			t.Fatalf("logEvent: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 1 {
			t.Fatalf("expected 1 line, got %d", len(lines))
		}

		var parsed Event
		if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if parsed.Phase != "triage" {
			t.Errorf("Phase = %q, want %q", parsed.Phase, "triage")
		}
		if parsed.Kind != "phase_started" {
			t.Errorf("Kind = %q, want %q", parsed.Kind, "phase_started")
		}
	})

	t.Run("appends_multiple_events", func(t *testing.T) {
		dir := t.TempDir()

		logEvent(dir, Event{Phase: "triage", Kind: "phase_started"})
		logEvent(dir, Event{Phase: "triage", Kind: "phase_completed"})
		logEvent(dir, Event{Phase: "plan", Kind: "phase_started"})

		data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 3 {
			t.Fatalf("expected 3 lines, got %d", len(lines))
		}
	})

	t.Run("sets_timestamp_if_zero", func(t *testing.T) {
		dir := t.TempDir()
		before := time.Now()

		logEvent(dir, Event{Phase: "triage", Kind: "phase_started"})

		data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		var parsed Event
		json.Unmarshal([]byte(strings.TrimSpace(string(data))), &parsed)

		if parsed.Timestamp.Before(before) {
			t.Errorf("Timestamp %v should be >= %v", parsed.Timestamp, before)
		}
	})

	t.Run("omits_empty_data", func(t *testing.T) {
		dir := t.TempDir()

		logEvent(dir, Event{Phase: "triage", Kind: "phase_started"})

		data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		line := strings.TrimSpace(string(data))
		if strings.Contains(line, `"data"`) {
			t.Errorf("empty data should be omitted, got: %s", line)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestLogEvent"`
Expected: Compilation error — `Event` and `logEvent` not defined

- [ ] **Step 3: Implement Event type and logEvent**

```go
// internal/pipeline/events.go
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Event represents a single structured event in events.jsonl.
type Event struct {
	Timestamp time.Time      `json:"timestamp"`
	Phase     string         `json:"phase"`
	Kind      string         `json:"kind"`
	Data      map[string]any `json:"data,omitempty"`
}

// logEvent appends an event to the events.jsonl file in dir.
func logEvent(dir string, event Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("pipeline: marshal event: %w", err)
	}
	data = append(data, '\n')

	path := filepath.Join(dir, "events.jsonl")
	fd, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("pipeline: open events log %s: %w", path, err)
	}
	defer fd.Close()

	if _, err := fd.Write(data); err != nil {
		return fmt.Errorf("pipeline: write event to %s: %w", path, err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestLogEvent"`
Expected: All 4 tests PASS

- [ ] **Step 5: Commit**

```bash
cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state
git add internal/pipeline/events.go internal/pipeline/events_test.go
git commit -m "feat(pipeline): add Event type and JSONL event logging"
```

---

### Task 4: File Locking

**Files:**
- Create: `internal/pipeline/lock.go`
- Create: `internal/pipeline/lock_test.go`

- [ ] **Step 1: Write failing tests for acquire, release, and contention**

```go
// internal/pipeline/lock_test.go
package pipeline

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestAcquireLock(t *testing.T) {
	t.Run("acquire_and_release", func(t *testing.T) {
		dir := t.TempDir()

		fd, err := acquireLock(dir)
		if err != nil {
			t.Fatalf("acquireLock: %v", err)
		}
		if fd == nil {
			t.Fatal("expected non-nil fd")
		}

		// Lock file should exist with PID info
		info, err := readLockInfo(fd)
		if err != nil {
			t.Fatalf("readLockInfo: %v", err)
		}
		if info.PID != os.Getpid() {
			t.Errorf("PID = %d, want %d", info.PID, os.Getpid())
		}

		releaseLock(fd)

		// Should be able to acquire again after release
		fd2, err := acquireLock(dir)
		if err != nil {
			t.Fatalf("re-acquire after release: %v", err)
		}
		releaseLock(fd2)
	})

	t.Run("contention_returns_error", func(t *testing.T) {
		dir := t.TempDir()

		fd1, err := acquireLock(dir)
		if err != nil {
			t.Fatalf("first acquireLock: %v", err)
		}
		defer releaseLock(fd1)

		// Second acquire from same process (different fd) should fail
		_, err = acquireLock(dir)
		if err == nil {
			t.Fatal("expected error for contention")
		}
		if !strings.Contains(err.Error(), "locked by PID") {
			t.Errorf("error should mention PID, got: %v", err)
		}
	})

	t.Run("release_nil_is_noop", func(t *testing.T) {
		releaseLock(nil) // should not panic
	})
}

func TestIsPIDAlive(t *testing.T) {
	t.Run("current_process_is_alive", func(t *testing.T) {
		if !isPIDAlive(os.Getpid()) {
			t.Error("current process should be alive")
		}
	})

	t.Run("invalid_pid_is_not_alive", func(t *testing.T) {
		if isPIDAlive(0) {
			t.Error("PID 0 should not be considered alive")
		}
		if isPIDAlive(-1) {
			t.Error("PID -1 should not be considered alive")
		}
	})

	t.Run("nonexistent_pid_is_not_alive", func(t *testing.T) {
		// PID 4194304 is above Linux's default max PID (4194304 is the kernel max)
		// Use a very high but valid PID that is almost certainly unused
		if isPIDAlive(4194300) {
			t.Skip("PID 4194300 unexpectedly exists")
		}
	})
}

func TestStaleDetection(t *testing.T) {
	t.Run("stale_lock_file_with_no_flock", func(t *testing.T) {
		dir := t.TempDir()

		// Simulate a stale lock: write lock file with a dead PID but no flock held.
		// This simulates a process that crashed after writing the lock file.
		lockPath := dir + "/lock"
		os.WriteFile(lockPath, []byte(`{"pid":4194300,"acquired_at":"2026-01-01T00:00:00Z"}`), 0644)

		// acquireLock should succeed because no flock is actually held
		fd, err := acquireLock(dir)
		if err != nil {
			t.Fatalf("acquireLock on stale lock: %v", err)
		}
		defer releaseLock(fd)

		// Lock file should now have our PID
		info, _ := readLockInfo(fd)
		if info.PID != os.Getpid() {
			t.Errorf("PID = %d, want %d", info.PID, os.Getpid())
		}
	})
}

func TestAcquireLock_AfterHolderCloses(t *testing.T) {
	dir := t.TempDir()

	// Simulate holder: open and flock, then close (simulating crash)
	lockPath := dir + "/lock"
	holderFd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(holderFd.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	holderFd.Write([]byte(`{"pid":99999,"acquired_at":"2026-01-01T00:00:00Z"}`))
	holderFd.Close() // simulates crash — kernel releases flock

	// Now acquireLock should succeed
	fd, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("acquireLock after holder closed: %v", err)
	}
	releaseLock(fd)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestAcquireLock|TestIsPID|TestStale"`
Expected: Compilation error — lock functions not defined

- [ ] **Step 3: Implement locking**

```go
// internal/pipeline/lock.go
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockInfo holds the PID and acquisition time written to the lock file.
type lockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// acquireLock acquires an exclusive flock on the lock file in dir.
// Returns the open file descriptor (caller must hold it to maintain the lock).
func acquireLock(dir string) (*os.File, error) {
	lockPath := filepath.Join(dir, "lock")

	fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("pipeline: open lock %s: %w", lockPath, err)
	}

	err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		writeLockInfo(fd)
		return fd, nil
	}

	if err != syscall.EWOULDBLOCK {
		fd.Close()
		return nil, fmt.Errorf("pipeline: flock %s: %w", lockPath, err)
	}

	// Lock is held — check if the holder is alive
	info, readErr := readLockInfo(fd)
	if readErr == nil && !isPIDAlive(info.PID) {
		// Stale lock: holder is dead. Retry — kernel should have released flock.
		err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			writeLockInfo(fd)
			return fd, nil
		}
	}

	fd.Close()
	if info != nil && info.PID > 0 {
		return nil, fmt.Errorf("pipeline: locked by PID %d (acquired %s)",
			info.PID, info.AcquiredAt.Format(time.RFC3339))
	}
	return nil, fmt.Errorf("pipeline: lock %s is held by another process", lockPath)
}

// releaseLock releases the flock and closes the file descriptor.
func releaseLock(fd *os.File) {
	if fd == nil {
		return
	}
	syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	fd.Close()
}

// writeLockInfo truncates and writes PID + timestamp to the lock file.
func writeLockInfo(fd *os.File) {
	info := lockInfo{
		PID:        os.Getpid(),
		AcquiredAt: time.Now(),
	}
	data, _ := json.Marshal(info)
	fd.Truncate(0)
	fd.Seek(0, 0)
	fd.Write(data)
	fd.Sync()
}

// readLockInfo reads and parses the lock file contents.
func readLockInfo(fd *os.File) (*lockInfo, error) {
	fd.Seek(0, 0)
	data := make([]byte, 1024)
	n, err := fd.Read(data)
	if err != nil || n == 0 {
		return nil, fmt.Errorf("pipeline: read lock info: %w", err)
	}
	var info lockInfo
	if err := json.Unmarshal(data[:n], &info); err != nil {
		return nil, fmt.Errorf("pipeline: parse lock info: %w", err)
	}
	return &info, nil
}

// isPIDAlive checks if a process with the given PID is running.
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestAcquireLock|TestIsPID|TestStale"`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state
git add internal/pipeline/lock.go internal/pipeline/lock_test.go
git commit -m "feat(pipeline): add flock-based locking with stale detection"
```

---

### Task 5: State Lifecycle — LoadOrCreate

**Files:**
- Create: `internal/pipeline/state.go`
- Create: `internal/pipeline/state_test.go`

- [ ] **Step 1: Write failing tests for LoadOrCreate and validation**

```go
// internal/pipeline/state_test.go
package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreate(t *testing.T) {
	t.Run("creates_new_state", func(t *testing.T) {
		dir := t.TempDir()

		state, err := LoadOrCreate(dir, "PROJ-123")
		if err != nil {
			t.Fatalf("LoadOrCreate: %v", err)
		}

		if state.ticket != "PROJ-123" {
			t.Errorf("ticket = %q, want %q", state.ticket, "PROJ-123")
		}
		if state.meta.Ticket != "PROJ-123" {
			t.Errorf("meta.Ticket = %q, want %q", state.meta.Ticket, "PROJ-123")
		}
		if state.meta.StartedAt.IsZero() {
			t.Error("StartedAt should be set")
		}
		if state.meta.Phases == nil {
			t.Error("Phases should be initialized")
		}

		// Directory structure should exist
		stateDir := filepath.Join(dir, "PROJ-123")
		if _, err := os.Stat(stateDir); err != nil {
			t.Errorf("state dir should exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "logs")); err != nil {
			t.Errorf("logs dir should exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "meta.json")); err != nil {
			t.Errorf("meta.json should exist: %v", err)
		}
	})

	t.Run("resumes_existing_state", func(t *testing.T) {
		dir := t.TempDir()

		// Create initial state
		state1, err := LoadOrCreate(dir, "PROJ-456")
		if err != nil {
			t.Fatal(err)
		}
		originalStartedAt := state1.meta.StartedAt

		// Load again — should resume, not overwrite
		state2, err := LoadOrCreate(dir, "PROJ-456")
		if err != nil {
			t.Fatalf("resume LoadOrCreate: %v", err)
		}
		if !state2.meta.StartedAt.Equal(originalStartedAt) {
			t.Errorf("StartedAt changed on resume: got %v, want %v",
				state2.meta.StartedAt, originalStartedAt)
		}
	})

	t.Run("lock_not_acquired", func(t *testing.T) {
		dir := t.TempDir()

		state, err := LoadOrCreate(dir, "PROJ-789")
		if err != nil {
			t.Fatal(err)
		}
		if state.lockFd != nil {
			t.Error("lockFd should be nil after LoadOrCreate")
		}
	})
}

func TestValidateTicketKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid_key", "PROJ-123", false},
		{"valid_with_underscores", "MY_PROJECT-42", false},
		{"empty_string", "", true},
		{"contains_slash", "PROJ/123", true},
		{"contains_backslash", "PROJ\\123", true},
		{"contains_dotdot", "PROJ..123", true},
		{"path_traversal", "../etc/passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTicketKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTicketKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestLoadOrCreate_InvalidKey(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadOrCreate(dir, "../evil")
	if err == nil {
		t.Fatal("expected error for invalid ticket key")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestLoadOrCreate|TestValidateTicketKey"`
Expected: Compilation error — `State`, `LoadOrCreate`, `validateTicketKey` not defined

- [ ] **Step 3: Implement State struct, LoadOrCreate, and validateTicketKey**

```go
// internal/pipeline/state.go
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

	meta, err := readMeta(metaPath)
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestLoadOrCreate|TestValidateTicketKey"`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state
git add internal/pipeline/state.go internal/pipeline/state_test.go
git commit -m "feat(pipeline): add State struct with LoadOrCreate and full API"
```

---

### Task 6: Phase Management Tests

**Files:**
- Modify: `internal/pipeline/state_test.go`

- [ ] **Step 1: Write tests for MarkRunning, MarkCompleted, MarkFailed, IsCompleted, AccumulateCost**

Append to `internal/pipeline/state_test.go`:

```go
func TestMarkRunning(t *testing.T) {
	t.Run("first_run_creates_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-1")

		if err := state.MarkRunning("triage"); err != nil {
			t.Fatalf("MarkRunning: %v", err)
		}

		ps := state.meta.Phases["triage"]
		if ps == nil {
			t.Fatal("triage phase should exist")
		}
		if ps.Status != PhaseRunning {
			t.Errorf("status = %q, want %q", ps.Status, PhaseRunning)
		}
		if ps.Generation != 1 {
			t.Errorf("generation = %d, want 1", ps.Generation)
		}
		if ps.Cost != 0 {
			t.Errorf("cost = %v, want 0", ps.Cost)
		}
	})

	t.Run("rerun_archives_and_increments_generation", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-2")

		// First run
		state.MarkRunning("verify")
		state.WriteResult("verify", []byte(`{"verdict":"pass"}`))
		state.WriteArtifact("verify", []byte("# Verify handoff"))
		state.MarkCompleted("verify")

		// Re-run
		if err := state.MarkRunning("verify"); err != nil {
			t.Fatalf("re-run MarkRunning: %v", err)
		}

		ps := state.meta.Phases["verify"]
		if ps.Generation != 2 {
			t.Errorf("generation = %d, want 2", ps.Generation)
		}
		if ps.Status != PhaseRunning {
			t.Errorf("status = %q, want %q", ps.Status, PhaseRunning)
		}

		// Archived files should exist
		stateDir := filepath.Join(dir, "T-2")
		archived, err := os.ReadFile(filepath.Join(stateDir, "verify.json.1"))
		if err != nil {
			t.Fatalf("archived .json: %v", err)
		}
		if string(archived) != `{"verdict":"pass"}` {
			t.Errorf("archived content = %q", archived)
		}

		archivedMd, err := os.ReadFile(filepath.Join(stateDir, "verify.md.1"))
		if err != nil {
			t.Fatalf("archived .md: %v", err)
		}
		if string(archivedMd) != "# Verify handoff" {
			t.Errorf("archived md content = %q", archivedMd)
		}
	})

	t.Run("rerun_zeroes_cost_and_error", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-3")

		state.MarkRunning("plan")
		state.AccumulateCost("plan", 0.50)
		state.MarkFailed("plan", fmt.Errorf("something broke"))

		// Verify state before re-run
		ps := state.meta.Phases["plan"]
		if ps.Cost != 0.50 {
			t.Errorf("cost before rerun = %v", ps.Cost)
		}

		state.MarkRunning("plan")

		ps = state.meta.Phases["plan"]
		if ps.Cost != 0 {
			t.Errorf("cost after rerun = %v, want 0", ps.Cost)
		}
		if ps.Error != "" {
			t.Errorf("error after rerun = %q, want empty", ps.Error)
		}
		if ps.DurationMs != 0 {
			t.Errorf("duration after rerun = %d, want 0", ps.DurationMs)
		}
	})
}

func TestMarkCompleted(t *testing.T) {
	t.Run("sets_completed_status", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-1")

		state.MarkRunning("triage")
		if err := state.MarkCompleted("triage"); err != nil {
			t.Fatalf("MarkCompleted: %v", err)
		}

		ps := state.meta.Phases["triage"]
		if ps.Status != PhaseCompleted {
			t.Errorf("status = %q, want %q", ps.Status, PhaseCompleted)
		}
		if ps.DurationMs < 0 {
			t.Errorf("DurationMs = %d, should be >= 0", ps.DurationMs)
		}
	})

	t.Run("error_on_unknown_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-2")

		err := state.MarkCompleted("nonexistent")
		if err == nil {
			t.Fatal("expected error for unknown phase")
		}
	})
}

func TestMarkFailed(t *testing.T) {
	t.Run("sets_failed_status_with_error", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-1")

		state.MarkRunning("implement")
		if err := state.MarkFailed("implement", fmt.Errorf("tests failed")); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}

		ps := state.meta.Phases["implement"]
		if ps.Status != PhaseFailed {
			t.Errorf("status = %q, want %q", ps.Status, PhaseFailed)
		}
		if ps.Error != "tests failed" {
			t.Errorf("error = %q, want %q", ps.Error, "tests failed")
		}
	})

	t.Run("error_on_unknown_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-2")

		err := state.MarkFailed("nonexistent", fmt.Errorf("err"))
		if err == nil {
			t.Fatal("expected error for unknown phase")
		}
	})
}

func TestIsCompleted(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	if state.IsCompleted("triage") {
		t.Error("should be false for unknown phase")
	}

	state.MarkRunning("triage")
	if state.IsCompleted("triage") {
		t.Error("should be false for running phase")
	}

	state.MarkCompleted("triage")
	if !state.IsCompleted("triage") {
		t.Error("should be true for completed phase")
	}
}

func TestAccumulateCost(t *testing.T) {
	t.Run("accumulates_phase_and_total", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-1")

		state.MarkRunning("triage")
		state.AccumulateCost("triage", 0.10)
		state.AccumulateCost("triage", 0.05)

		ps := state.meta.Phases["triage"]
		if ps.Cost != 0.15 {
			t.Errorf("phase cost = %v, want 0.15", ps.Cost)
		}
		if state.meta.TotalCost != 0.15 {
			t.Errorf("total cost = %v, want 0.15", state.meta.TotalCost)
		}

		state.MarkCompleted("triage")
		state.MarkRunning("plan")
		state.AccumulateCost("plan", 0.20)

		if state.meta.TotalCost != 0.35 {
			t.Errorf("total cost = %v, want 0.35", state.meta.TotalCost)
		}
	})

	t.Run("error_on_unstarted_phase", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := LoadOrCreate(dir, "T-2")

		err := state.AccumulateCost("nonexistent", 1.0)
		if err == nil {
			t.Fatal("expected error for unstarted phase")
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestMark|TestIsCompleted|TestAccumulateCost"`
Expected: All tests PASS (implementation was included in Task 5)

- [ ] **Step 3: Commit**

```bash
cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state
git add internal/pipeline/state_test.go
git commit -m "test(pipeline): add phase management tests"
```

---

### Task 7: Artifacts, Results, Logs, and Locking Tests

**Files:**
- Modify: `internal/pipeline/state_test.go`

- [ ] **Step 1: Write tests for artifact/result/log operations and State-level locking**

Append to `internal/pipeline/state_test.go`:

```go
func TestWriteReadArtifact(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	content := []byte("# Triage handoff\n\nThis ticket is about X.")
	if err := state.WriteArtifact("triage", content); err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}

	got, err := state.ReadArtifact("triage")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestReadArtifact_NotExist(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	_, err := state.ReadArtifact("nonexistent")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestWriteReadResult(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	result := json.RawMessage(`{"verdict":"pass","confidence":0.95}`)
	if err := state.WriteResult("verify", result); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}

	got, err := state.ReadResult("verify")
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	if string(got) != string(result) {
		t.Errorf("result = %q, want %q", got, result)
	}
}

func TestReadResult_NotExist(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	_, err := state.ReadResult("nonexistent")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestWriteLog(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	prompt := []byte("You are a triage agent...")
	if err := state.WriteLog("triage", "prompt", prompt); err != nil {
		t.Fatalf("WriteLog: %v", err)
	}

	logPath := filepath.Join(dir, "T-1", "logs", "triage_prompt.md")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(prompt) {
		t.Errorf("log content = %q, want %q", got, prompt)
	}
}

func TestStateAcquireReleaseLock(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	if err := state.AcquireLock(); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	// Second state for same ticket should fail to lock
	state2, _ := LoadOrCreate(dir, "T-1")
	err := state2.AcquireLock()
	if err == nil {
		t.Fatal("expected error for contention")
	}

	state.ReleaseLock()

	// Now second state should succeed
	if err := state2.AcquireLock(); err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	state2.ReleaseLock()
}

func TestStateLogEvent(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	if err := state.LogEvent(Event{Phase: "triage", Kind: "custom_event"}); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "T-1", "events.jsonl"))
	if len(data) == 0 {
		t.Error("events.jsonl should not be empty")
	}
}

func TestDir(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	want := filepath.Join(dir, "T-1")
	if state.Dir() != want {
		t.Errorf("Dir() = %q, want %q", state.Dir(), want)
	}
}

func TestMeta(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-1")

	meta := state.Meta()
	if meta == nil {
		t.Fatal("Meta() should not be nil")
	}
	if meta.Ticket != "T-1" {
		t.Errorf("Ticket = %q, want %q", meta.Ticket, "T-1")
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -run "TestWriteRead|TestWriteLog|TestStateAcquire|TestStateLog|TestDir|TestMeta"`
Expected: All tests PASS

- [ ] **Step 3: Commit**

```bash
cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state
git add internal/pipeline/state_test.go
git commit -m "test(pipeline): add artifact, result, log, and locking tests"
```

---

### Task 8: Integration Test — Full Lifecycle

**Files:**
- Modify: `internal/pipeline/state_test.go`

- [ ] **Step 1: Write integration test covering all acceptance criteria**

Append to `internal/pipeline/state_test.go`:

```go
func TestFullLifecycle(t *testing.T) {
	dir := t.TempDir()

	// === First run ===
	state, err := LoadOrCreate(dir, "PROJ-100")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	if err := state.AcquireLock(); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer state.ReleaseLock()

	// Run triage
	state.MarkRunning("triage")
	state.AccumulateCost("triage", 0.08)
	state.WriteResult("triage", json.RawMessage(`{"complexity":"medium"}`))
	state.WriteArtifact("triage", []byte("Triage: medium complexity"))
	state.WriteLog("triage", "prompt", []byte("system prompt"))
	state.WriteLog("triage", "response", []byte("raw response"))
	state.MarkCompleted("triage")

	// Run plan
	state.MarkRunning("plan")
	state.AccumulateCost("plan", 0.20)
	state.WriteResult("plan", json.RawMessage(`{"tasks":["task1"]}`))
	state.WriteArtifact("plan", []byte("Plan: one task"))
	state.MarkCompleted("plan")

	// Run implement — fails
	state.MarkRunning("implement")
	state.AccumulateCost("implement", 1.50)
	state.MarkFailed("implement", fmt.Errorf("test suite failed"))

	// Verify accumulated cost
	meta := state.Meta()
	if meta.TotalCost != 1.78 {
		t.Errorf("TotalCost = %v, want 1.78", meta.TotalCost)
	}

	state.ReleaseLock()

	// === Resume after crash ===
	state2, err := LoadOrCreate(dir, "PROJ-100")
	if err != nil {
		t.Fatalf("resume LoadOrCreate: %v", err)
	}

	if err := state2.AcquireLock(); err != nil {
		t.Fatalf("resume AcquireLock: %v", err)
	}
	defer state2.ReleaseLock()

	// Completed phases should be skippable
	if !state2.IsCompleted("triage") {
		t.Error("triage should be completed on resume")
	}
	if !state2.IsCompleted("plan") {
		t.Error("plan should be completed on resume")
	}
	if state2.IsCompleted("implement") {
		t.Error("implement should NOT be completed (it failed)")
	}

	// Artifacts should be readable
	triageArtifact, _ := state2.ReadArtifact("triage")
	if string(triageArtifact) != "Triage: medium complexity" {
		t.Errorf("triage artifact = %q", triageArtifact)
	}
	triageResult, _ := state2.ReadResult("triage")
	if string(triageResult) != `{"complexity":"medium"}` {
		t.Errorf("triage result = %q", triageResult)
	}

	// Re-run implement (generation 2)
	state2.MarkRunning("implement")

	ps := state2.Meta().Phases["implement"]
	if ps.Generation != 2 {
		t.Errorf("implement generation = %d, want 2", ps.Generation)
	}
	if ps.Error != "" {
		t.Errorf("implement error should be cleared, got %q", ps.Error)
	}

	// Budget preserved from first run's triage + plan
	if state2.Meta().TotalCost != 1.78 {
		t.Errorf("resumed TotalCost = %v, want 1.78", state2.Meta().TotalCost)
	}

	state2.AccumulateCost("implement", 2.00)
	state2.WriteResult("implement", json.RawMessage(`{"commits":1}`))
	state2.MarkCompleted("implement")

	if state2.Meta().TotalCost != 3.78 {
		t.Errorf("final TotalCost = %v, want 3.78", state2.Meta().TotalCost)
	}

	// Verify events.jsonl has entries
	eventsData, _ := os.ReadFile(filepath.Join(dir, "PROJ-100", "events.jsonl"))
	eventLines := strings.Split(strings.TrimSpace(string(eventsData)), "\n")
	if len(eventLines) < 8 {
		t.Errorf("expected >= 8 events, got %d", len(eventLines))
	}

	// Verify log files exist
	logDir := filepath.Join(dir, "PROJ-100", "logs")
	if _, err := os.Stat(filepath.Join(logDir, "triage_prompt.md")); err != nil {
		t.Errorf("triage prompt log missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "triage_response.md")); err != nil {
		t.Errorf("triage response log missing: %v", err)
	}
}

func TestCrashSafety_OrphanedTmp(t *testing.T) {
	dir := t.TempDir()

	// Create state normally
	state, _ := LoadOrCreate(dir, "T-1")
	state.MarkRunning("triage")
	state.MarkCompleted("triage")

	// Simulate crash: leave orphaned meta.json.tmp with corrupt data
	metaTmp := filepath.Join(dir, "T-1", "meta.json.tmp")
	os.WriteFile(metaTmp, []byte("corrupt"), 0644)

	// Resume should read the real meta.json, ignoring the orphaned .tmp
	state2, err := LoadOrCreate(dir, "T-1")
	if err != nil {
		t.Fatalf("resume after crash: %v", err)
	}
	if !state2.IsCompleted("triage") {
		t.Error("triage should still be completed after crash resume")
	}
}
```

- [ ] **Step 2: Run all tests**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -v -count=1`
Expected: All tests PASS

- [ ] **Step 3: Run tests with race detector**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./internal/pipeline/ -race -count=1`
Expected: PASS, no race conditions detected

- [ ] **Step 4: Run go vet and check formatting**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go vet ./internal/pipeline/ && gofmt -l internal/pipeline/`
Expected: No issues

- [ ] **Step 5: Commit**

```bash
cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state
git add internal/pipeline/state_test.go
git commit -m "test(pipeline): add integration and crash safety tests"
```

- [ ] **Step 6: Run full project test suite**

Run: `cd /home/ddebrito/dev/soda/.worktrees/soda/feat-pipeline-state && go test ./... -race -count=1`
Expected: All tests PASS across both `internal/claude/` and `internal/pipeline/`
