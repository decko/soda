# Pipeline State Design — `internal/pipeline/`

**Issue:** decko/soda#3
**Date:** 2026-04-11
**Status:** Approved

## Summary

File-based pipeline state management in `.soda/<ticket>/` with atomic writes, per-ticket flock-based locking, per-phase generation tracking, and structured event logging.

## Package structure

**Package:** `internal/pipeline`

| File | Responsibility |
|------|---------------|
| `state.go` | `State` struct, `LoadOrCreate`, directory lifecycle |
| `meta.go` | `PipelineMeta`, `PhaseState`, `PhaseStatus` types, meta.json read/write |
| `lock.go` | `flock`-based locking, stale PID detection |
| `events.go` | `Event` type, JSONL append |
| `atomic.go` | `atomicWrite` helper, artifact archival |

Tests mirror source files: `state_test.go`, `meta_test.go`, `lock_test.go`, `events_test.go`, `atomic_test.go`.

## Types

```go
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
    startedAt  time.Time   // unexported, not persisted — set by MarkRunning, used to compute DurationMs
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

// Event represents a single structured event in events.jsonl.
type Event struct {
    Timestamp time.Time      `json:"timestamp"`
    Phase     string         `json:"phase"`
    Kind      string         `json:"kind"`
    Data      map[string]any `json:"data,omitempty"`
}

// State manages the disk state for a single ticket's pipeline run.
type State struct {
    dir    string       // .soda/<ticket>/
    ticket string
    meta   *PipelineMeta
    lockFd *os.File     // held while lock is active, nil otherwise
}
```

### Key type decisions

- `PhaseState.startedAt` is unexported and not serialized. Set by `MarkRunning`, used by `MarkCompleted`/`MarkFailed` to compute `DurationMs`. On resume from crash, a `running` phase with no `startedAt` gets duration zero.
- `PipelineMeta.Phases` uses `map[string]*PhaseState` — pointer so mutations are reflected without re-assignment. The engine populates phase names from `phases.yaml`; the state package does not hardcode phase names.
- `PipelineMeta.TotalCost` is the sum across all phases, updated by `AccumulateCost` alongside per-phase cost. Convenience field so `soda status` doesn't iterate the map.
- `State.meta` is loaded once by `LoadOrCreate` and mutated in place. Every mutation flushes to disk atomically.
- `State` is not safe for concurrent use. The pipeline engine processes phases sequentially.
- `PhaseRetrying` and `PhasePaused` statuses are defined for use by `pipeline/engine.go` (issue #4), which will set them via the `State` methods or direct meta manipulation. This package only provides the status constants.

## State lifecycle

### `LoadOrCreate(stateDir, ticketKey string) (*State, error)`

- Validates `ticketKey`: rejects empty strings and keys containing `/`, `\`, or `..` (defense-in-depth against path traversal)
- Builds path: `filepath.Join(stateDir, ticketKey)`
- If `meta.json` exists: reads and unmarshals it (resume path)
- If not: creates the directory tree (including `logs/`), initializes `PipelineMeta` with `StartedAt: time.Now()` and empty `Phases` map, writes `meta.json` atomically
- Returns `*State` with meta loaded but lock **not** acquired (caller decides when to lock)

### `AcquireLock() error`

- Opens/creates `.soda/<ticket>/lock`
- Attempts `syscall.Flock(fd, LOCK_EX|LOCK_NB)` (non-blocking exclusive lock)
- On success: truncates and writes `{"pid": <pid>, "acquired_at": "<timestamp>"}` to the lock file, stores fd in `State.lockFd`. Note: the PID write is not atomic with flock acquisition -- if the process crashes between flock and PID write, the lock file may contain a stale PID from a previous holder. The kernel flock is the source of truth; the PID file is best-effort diagnostics.
- On `EWOULDBLOCK`: reads the lock file, checks if the PID is alive (`syscall.Kill(pid, 0)`)
  - PID alive: return error `"ticket %s is locked by PID %d (acquired %s)"`
  - PID dead: stale lock. Log a warning event, then retry the flock (the stale holder's fd is gone, so the kernel released it). If flock still fails, return error.
- Lock is advisory and per-machine only (documented in AGENTS.md)

### `ReleaseLock()`

- If `lockFd` is nil, no-op
- `syscall.Flock(fd, LOCK_UN)`, then `fd.Close()`, set `lockFd = nil`
- Does not remove the lock file (avoids race with another process checking it)

## Phase status management

### `MarkRunning(phase string) error`

1. If phase doesn't exist in `meta.Phases`, creates it with `Generation: 1`
2. If phase already has artifacts from a previous run (`<phase>.json`, `<phase>.md`):
   - Archives them: `verify.json` -> `verify.json.<old_generation>`, same for `.md`
   - Increments `Generation`
3. Sets `Status = PhaseRunning`, zeroes `Cost`, `DurationMs`, `Error`
4. Sets unexported `startedAt = time.Now()`
5. Flushes `meta.json` atomically
6. Logs event: `{kind: "phase_started", phase: "verify", data: {generation: 3}}`

### `MarkCompleted(phase string) error`

1. Computes `DurationMs` from `startedAt` (if zero, uses 0)
2. Sets `Status = PhaseCompleted`
3. Flushes `meta.json`
4. Logs event: `{kind: "phase_completed", phase: "verify", data: {duration_ms: 12000, cost: 0.45}}`

### `MarkFailed(phase string, err error) error`

1. Computes `DurationMs` from `startedAt`
2. Sets `Status = PhaseFailed`, `Error = err.Error()`
3. Flushes `meta.json`
4. Logs event: `{kind: "phase_failed", phase: "verify", data: {error: "...", duration_ms: 8000}}`

### `AccumulateCost(phase string, cost float64) error`

1. If phase doesn't exist in `meta.Phases`, returns an error (phase must be started via `MarkRunning` first)
2. Adds `cost` to `PhaseState.Cost`
3. Adds `cost` to `PipelineMeta.TotalCost`
4. Flushes `meta.json`

### `IsCompleted(phase string) bool`

- Returns false if phase is not in `meta.Phases` map (nil-safe: checks map entry before accessing `.Status`)
- Returns `true` only if `PhaseState.Status == PhaseCompleted`

### `Meta() *PipelineMeta`

- Returns the in-memory meta. Callers should treat as read-only.
- Simplified from the issue sketch's `(*PipelineMeta, error)` since meta is always valid after `LoadOrCreate`.

## Artifacts, results, and logs

### `WriteArtifact(phase string, content []byte) error`

- Writes `<phase>.md` (handoff content) via `atomicWrite`

### `ReadArtifact(phase string) ([]byte, error)`

- Reads `<phase>.md`. Returns `os.ErrNotExist` if not present.

### `WriteResult(phase string, result json.RawMessage) error`

- Writes `<phase>.json` (structured output) via `atomicWrite`

### `ReadResult(phase string) (json.RawMessage, error)`

- Reads `<phase>.json`, returns as `json.RawMessage`

### `WriteLog(phase, suffix string, content []byte) error`

- Writes to `logs/<phase>_<suffix>.md` (e.g., `logs/triage_prompt.md`)
- Plain write, no atomicity needed (debug artifacts, not resumable state)
- Creates `logs/` directory if missing

## Event logging

### `LogEvent(event Event) error`

- Sets `event.Timestamp = time.Now()` if zero
- Marshals to JSON (single line, no indent)
- Opens `events.jsonl` with `O_APPEND|O_CREATE|O_WRONLY`
- Writes marshaled JSON + `\n`
- Closes file after write (append frequency is low)

No atomicity needed. JSONL is append-only; a partial write from a crash produces a truncated last line, which consumers can skip.

## Atomic writes and archival

### `atomicWrite(path string, data []byte) error` (unexported)

- Writes to `<path>.tmp` with permissions `0644`
- Calls `fd.Sync()` before closing (ensures data is durable on disk, not just in page cache -- protects against power loss, not just process crash)
- `os.Rename("<path>.tmp", "<path>")`

### `archiveArtifact(path string, generation int) error` (unexported)

- Renames `<path>` to `<path>.<generation>` if the file exists
- Called by `MarkRunning` for both `.json` and `.md` artifacts

## Error handling

No custom error types. Errors are wrapped with context:

```go
fmt.Errorf("pipeline: load meta %s: %w", path, err)
fmt.Errorf("pipeline: acquire lock %s: %w", s.dir, err)
fmt.Errorf("pipeline: write artifact %s/%s.md: %w", s.dir, phase, err)
```

The consumer (`pipeline/engine.go`) treats any state error as fatal to the pipeline run. No error classification needed.

## Testing strategy

- All tests use `t.TempDir()` for isolation
- Table-driven tests for `PhaseStatus` transitions, `atomicWrite`, `archiveArtifact`
- `lock_test.go`: fork a subprocess (or second goroutine with a second fd) to test contention and stale lock detection
- `state_test.go`: integration tests covering the full lifecycle: `LoadOrCreate` -> `AcquireLock` -> `MarkRunning` -> `WriteArtifact` -> `WriteResult` -> `AccumulateCost` -> `MarkCompleted` -> `ReleaseLock` -> re-`LoadOrCreate` (resume)
- Crash simulation: write `meta.json.tmp` without renaming, then `LoadOrCreate` to verify it reads the old `meta.json`
- Archive test: run a phase twice, verify `<phase>.json.1` exists with the first generation's content

## Disk layout

```
.soda/<ticket>/
├── meta.json              # PipelineMeta
├── lock                   # {"pid": 12345, "acquired_at": "..."}
├── triage.json            # structured output
├── triage.md              # handoff content
├── plan.json
├── plan.md
├── implement.json
├── implement.md
├── verify.json
├── verify.json.1          # archived from generation 1
├── verify.json.2          # archived from generation 2
├── verify.md.1            # archived handoff (same pattern as .json)
├── verify.md.2
├── submit.json
├── events.jsonl           # append-only event log
└── logs/
    ├── triage_prompt.md
    ├── triage_response.md
    └── ...
```
