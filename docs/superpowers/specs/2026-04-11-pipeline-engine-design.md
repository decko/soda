# Pipeline Engine Design — `internal/pipeline/engine.go` (Issue #4)

## Summary

The engine orchestrates 6 pipeline phases sequentially (triage → plan → implement → verify → submit → monitor), calling an abstract Runner interface for each phase, handling errors with classified retries, persisting state to disk, and emitting events for a future TUI.

## Architecture

```
Engine.Run(ctx)
  │
  │  1. state.AcquireLock()
  │  2. emit(engine_started)
  │  3. For each phase in pipeline.Phases:
  │     │  a. Skip if state.IsCompleted(phase)
  │     │  b. Check depends_on — all must be completed
  │     │  c. Check budget — remaining > 0
  │     │  d. If phase is "implement" and no worktree: CreateWorktree()
  │     │  e. state.MarkRunning(phase)
  │     │  f. Build PromptData from ticket, config, state.ReadArtifact(dep)
  │     │  g. Render prompt template from filesystem
  │     │  h. Log rendered prompt to state.WriteLog(phase, "prompt")
  │     │  i. Build runner.RunOpts from phase config + rendered prompt
  │     │  j. Call runner.Run(ctx, opts) with retry loop
  │     │  k. On success: write result, write artifact, accumulate cost, mark completed
  │     │  l. On error: classify, retry or fail
  │     │  m. Domain gating on result (triage: Automatable, verify: Verdict, etc.)
  │     │  n. Checkpoint mode: emit pause, wait on confirmCh
  │  4. emit(engine_completed)
  │  5. state.ReleaseLock()
  │
  v
Success or first fatal error
```

## Design decisions

### Runner interface: separate `internal/runner/` package with its own types

The issue defines `runner.RunOpts` and `runner.RunResult` as types distinct from `claude.RunOpts` and `claude.RunResult`. All four specialists initially recommended collapsing these to reuse `claude` types directly. However, this collapses the abstraction boundary the sandbox (#2) needs.

The intended data flow is:

```
Engine → runner.RunOpts → SandboxRunner → claude.RunOpts → claude.Runner.Stream()
```

Key differences between the two RunOpts types:

| Field | `runner.RunOpts` | `claude.RunOpts` |
|-------|-----------------|-----------------|
| System prompt | `SystemPrompt string` (inline) | `SystemPromptPath string` (file path) |
| User prompt | `UserPrompt string` (separate) | `Prompt string` (single, via stdin) |
| Phase name | `Phase string` | not present |
| Model | `Model string` | not present (on Runner struct) |
| WorkDir | `WorkDir string` | not present (on Runner struct) |
| Budget | `MaxBudgetUSD float64` (value) | `MaxBudgetUSD *float64` (pointer) |

The sandbox runner needs this translation layer to: write the system prompt to a temp file inside the sandbox, configure the working directory within the sandbox, and set up Landlock/seccomp before invoking Claude.

**Decision:** Follow the issue spec. Define `Runner` interface, `RunOpts`, `RunResult`, and `MockRunner` in `internal/runner/`.

### Events: callback, not channel

The Go specialist argued against a channel for events. The callback pattern (`OnEvent func(Event)`) is simpler, consistent with the `onLine func(string)` pattern in `claude.Runner.Stream()`, and avoids buffer sizing, lifecycle, and dropped-event issues.

The engine calls `OnEvent` synchronously at each event point and also calls `state.LogEvent()` for disk persistence. If the TUI needs async dispatch, it handles that internally.

**Decision:** `OnEvent func(Event)` on `EngineConfig`. No channel.

### Artifact handoff: structured JSON rendered to deterministic markdown

Rather than passing the model's freeform text (`.md` file) as-is to downstream phases, render the structured JSON output through engine-controlled templates. Benefits:

- Deterministic, concise downstream context (saves tokens)
- No quality dependency on the model's prose
- Per-field truncation for context window management
- The `.md` file still written to disk for human readability

Exception: the freeform `.md` is still useful as a debug artifact and human-readable summary. Write both `.json` and `.md` to disk. For downstream prompt injection, render from `.json`.

For the monitor template's `.Artifacts.Submit.PRURL`, unmarshal `submit.json` into `schemas.SubmitOutput` and extract the field.

**Decision:** Engine renders artifacts from structured JSON. `ArtifactData` has string fields for most phases but `Submit` is a struct with `PRURL`.

### Worktree: `internal/git/worktree.go`

Worktree management is pure git operations with no dependency on pipeline types. Keeping it in its own package makes it independently testable and reusable.

**Decision:** `internal/git/worktree.go` with `CreateWorktree(ctx, repoDir, worktreeBase, branch, baseBranch) (string, error)`.

### Lock management: Engine.Run() acquires/releases

The lock protects the engine's invariant (only one engine runs per ticket). The engine is the only code that knows when to acquire and release. Pushing this to the caller creates a violable protocol.

**Decision:** `Engine.Run()` calls `state.AcquireLock()` at the start and defers `state.ReleaseLock()`.

### YAML parsing: `gopkg.in/yaml.v3`

First external dependency. AGENTS.md already lists yaml.v3 in the tech stack. Custom `Duration` type with `UnmarshalYAML` for `time.Duration` fields.

### Testing: inject sleepFunc for backoff

Rather than a full clock interface, inject `sleepFunc func(time.Duration)` into the retry config. Tests record durations without actually sleeping. Also inject `jitterFunc` for deterministic backoff verification.

## File structure

```
internal/
├── runner/
│   ├── runner.go         # Runner interface, RunOpts, RunResult
│   └── mock.go           # MockRunner for testing
├── pipeline/
│   ├── engine.go         # Engine, EngineConfig, Run(), runPhase(), retry
│   ├── engine_test.go    # All test cases against MockRunner
│   ├── phase.go          # PhaseConfig, PhasePipeline, LoadPipeline()
│   ├── phase_test.go
│   ├── prompt.go         # PromptLoader, PromptData, RenderPrompt()
│   ├── prompt_test.go
│   ├── errors.go         # BudgetExceededError, DependencyNotMetError
│   ├── events.go         # (existing, add Kind constants)
│   ├── state.go          # (existing, unchanged)
│   ├── meta.go           # (existing, unchanged)
│   ├── lock.go           # (existing, unchanged)
│   └── atomic.go         # (existing, unchanged)
├── git/
│   ├── worktree.go       # CreateWorktree()
│   └── worktree_test.go
```

## Type definitions

### `internal/runner/runner.go`

```go
package runner

import (
    "context"
    "encoding/json"
    "time"
)

// Runner executes a single pipeline phase in an isolated session.
// The concrete implementation will be the sandbox runner (#2).
// For now, use MockRunner for testing.
type Runner interface {
    Run(ctx context.Context, opts RunOpts) (*RunResult, error)
}

// RunOpts holds everything needed to execute one phase.
type RunOpts struct {
    Phase        string
    SystemPrompt string
    UserPrompt   string
    OutputSchema string
    AllowedTools []string
    MaxBudgetUSD float64
    WorkDir      string
    Model        string
    Timeout      time.Duration
}

// RunResult holds the parsed response from a phase execution.
type RunResult struct {
    Output     json.RawMessage
    RawText    string
    CostUSD    float64
    TokensIn   int64
    TokensOut  int64
    DurationMs int64
    Turns      int
}
```

### `internal/runner/mock.go`

```go
package runner

import (
    "context"
    "fmt"
)

// MockRunner returns canned responses for testing.
type MockRunner struct {
    Responses map[string]*RunResult // phase name -> response
    Errors    map[string]error      // phase name -> error
    Calls     []RunOpts             // recorded calls
}

func (m *MockRunner) Run(ctx context.Context, opts RunOpts) (*RunResult, error) {
    m.Calls = append(m.Calls, opts)
    if err, ok := m.Errors[opts.Phase]; ok {
        return nil, err
    }
    if result, ok := m.Responses[opts.Phase]; ok {
        return result, nil
    }
    return nil, fmt.Errorf("mock: no response configured for phase %q", opts.Phase)
}
```

### `internal/pipeline/engine.go`

```go
package pipeline

// Engine orchestrates the sequential execution of pipeline phases.
type Engine struct {
    runner    runner.Runner
    config    EngineConfig
    state     *State
    confirmCh chan struct{} // nil unless Checkpoint mode
}

// EngineConfig holds all configuration for the engine.
type EngineConfig struct {
    Pipeline     *PhasePipeline
    Loader       *PromptLoader
    Ticket       ticket.Ticket
    Model        string
    WorkDir      string        // project root
    WorktreeBase string        // e.g., ".worktrees"
    BaseBranch   string        // e.g., "main"
    MaxCostUSD   float64       // budget ceiling across all phases
    Mode         Mode
    OnEvent      func(Event)   // optional, called synchronously
    SleepFunc    func(time.Duration) // defaults to time.Sleep; tests inject no-op
    JitterFunc   func(max time.Duration) time.Duration // defaults to rand-based; tests inject deterministic
}

type Mode int
const (
    Autonomous Mode = iota
    Checkpoint
)

// NewEngine creates an engine ready to run.
func NewEngine(r runner.Runner, state *State, config EngineConfig) *Engine

// Run executes all phases sequentially, skipping completed ones.
func (eng *Engine) Run(ctx context.Context) error

// Resume restarts execution from a specific phase, re-running it
// even if it was previously completed.
func (eng *Engine) Resume(ctx context.Context, fromPhase string) error

// Confirm unblocks the engine from a checkpoint pause.
func (eng *Engine) Confirm()
```

Internal methods:

```go
// runPhase orchestrates a single phase: dependency check, prompt render,
// run with retry, write artifacts.
func (eng *Engine) runPhase(ctx context.Context, phase PhaseConfig) error

// runWithRetry calls runner.Run in a retry loop with error classification.
func (eng *Engine) runWithRetry(ctx context.Context, phase PhaseConfig, opts runner.RunOpts) (*runner.RunResult, error)

// emit sends an event to both OnEvent callback and state.LogEvent.
func (eng *Engine) emit(event Event)

// buildPromptData constructs the template data for a phase from state artifacts.
func (eng *Engine) buildPromptData(phase PhaseConfig) (PromptData, error)

// checkBudget verifies the total cost hasn't exceeded the ceiling.
func (eng *Engine) checkBudget(phase string) error
```

### `internal/pipeline/phase.go`

```go
package pipeline

// PhasePipeline holds the ordered list of phases from phases.yaml.
type PhasePipeline struct {
    Phases []PhaseConfig
}

type PhaseConfig struct {
    Name      string   `yaml:"name"`
    Prompt    string   `yaml:"prompt"`    // relative path to template
    Schema    string   `yaml:"schema"`    // relative path to schema (reference)
    Tools     []string `yaml:"tools"`
    Timeout   Duration `yaml:"timeout"`
    Type      string   `yaml:"type"`      // "" (one-shot) or "polling"
    Retry     RetryConfig   `yaml:"retry"`
    DependsOn []string      `yaml:"depends_on"`
    Polling   *PollingConfig `yaml:"polling,omitempty"`
}

type RetryConfig struct {
    Transient int `yaml:"transient"`
    Parse     int `yaml:"parse"`
    Semantic  int `yaml:"semantic"`
}

type PollingConfig struct {
    InitialInterval Duration `yaml:"initial_interval"`
    MaxInterval     Duration `yaml:"max_interval"`
    EscalateAfter   Duration `yaml:"escalate_after"`
    MaxDuration     Duration `yaml:"max_duration"`
    MaxResponseRounds int    `yaml:"max_response_rounds"`
}

// Duration wraps time.Duration for YAML unmarshaling.
type Duration struct {
    time.Duration
}

func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error

// LoadPipeline reads and parses a phases.yaml file.
func LoadPipeline(path string) (*PhasePipeline, error)
```

### `internal/pipeline/prompt.go`

```go
package pipeline

// PromptData is the template context for phase prompts.
type PromptData struct {
    Ticket       ticket.Ticket
    Config       PromptConfigData
    Artifacts    ArtifactData
    Context      ContextData
    WorktreePath string
    Branch       string
    BaseBranch   string
    ReviewComments string // monitor only
}

type PromptConfigData struct {
    Repos          []RepoConfig
    Repo           RepoConfig
    Formatter      string
    TestCommand    string
    VerifyCommands []string
}

type RepoConfig struct {
    Name        string   `yaml:"name"`
    Forge       string   `yaml:"forge"`
    PushTo      string   `yaml:"push_to"`
    Target      string   `yaml:"target"`
    Description string   `yaml:"description"`
    Formatter   string   `yaml:"formatter"`
    TestCommand string   `yaml:"test_command"`
    Labels      []string `yaml:"labels"`
    Trailers    []string `yaml:"trailers"`
}

// ArtifactData holds rendered artifacts from previous phases.
// Most fields are deterministic markdown rendered from structured JSON.
// Submit is a struct because monitor needs .Submit.PRURL.
type ArtifactData struct {
    Triage    string
    Plan      string
    Implement string
    Verify    string
    Submit    SubmitArtifact
}

type SubmitArtifact struct {
    PRURL string
}

type ContextData struct {
    ProjectContext  string
    RepoConventions string
    Gotchas         string
}

// PromptLoader resolves prompt templates from the filesystem.
type PromptLoader struct {
    dirs []string // ordered: user override dir first, then built-in dir
}

func NewPromptLoader(dirs ...string) *PromptLoader

// Load returns the template content for a phase, preferring overrides.
func (loader *PromptLoader) Load(name string) (string, error)

// RenderPrompt executes a Go text/template against PromptData.
func RenderPrompt(tmpl string, data PromptData) (string, error)
```

### `internal/pipeline/errors.go`

```go
package pipeline

// BudgetExceededError is returned when accumulated cost exceeds the limit.
type BudgetExceededError struct {
    Limit  float64
    Actual float64
    Phase  string
}

func (e *BudgetExceededError) Error() string

// DependencyNotMetError is returned when a prerequisite phase is not completed.
type DependencyNotMetError struct {
    Phase      string
    Dependency string
}

func (e *DependencyNotMetError) Error() string

// PhaseGateError is returned when domain gating fails (e.g., triage not automatable).
type PhaseGateError struct {
    Phase  string
    Reason string
}

func (e *PhaseGateError) Error() string
```

### Event kind constants (added to existing `events.go`)

```go
const (
    EventEngineStarted   = "engine_started"
    EventEngineCompleted = "engine_completed"
    EventEngineFailed    = "engine_failed"
    EventPhaseStarted    = "phase_started"
    EventPhaseCompleted  = "phase_completed"
    EventPhaseFailed     = "phase_failed"
    EventPhaseRetrying   = "phase_retrying"
    EventPhaseSkipped    = "phase_skipped"
    EventOutputChunk     = "output_chunk"
    EventBudgetWarning   = "budget_warning"
    EventCheckpointPause = "checkpoint_pause"
    EventWorktreeCreated = "worktree_created"
    EventMonitorSkipped  = "monitor_skipped"
)
```

### `internal/git/worktree.go`

```go
package git

// CreateWorktree creates a git worktree for a new branch.
// Returns the absolute path to the worktree directory.
func CreateWorktree(ctx context.Context, repoDir, worktreeBase, branch, baseBranch string) (string, error)
```

Uses `os/exec` to run `git worktree add`. The worktree path is `<repoDir>/<worktreeBase>/<branch>`. Creates the branch from baseBranch.

## Retry logic

### Error classification

```go
func classifyError(err error) (category string, retryable bool) {
    if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
        return "context", false
    }
    var transient *claude.TransientError
    if errors.As(err, &transient) {
        return "transient", true
    }
    var parseErr *claude.ParseError
    if errors.As(err, &parseErr) {
        return "parse", true
    }
    var semantic *claude.SemanticError
    if errors.As(err, &semantic) {
        return "semantic", true
    }
    return "unknown", false
}
```

### Retry behavior per category

| Category | Backoff | Prompt modification | Max retries |
|----------|---------|-------------------|-------------|
| Transient | Exponential: base 2s, max 30s, jitter | None (same prompt) | `phase.Retry.Transient` |
| Parse | Immediate | Append: "Previous response could not be parsed. Error: {err}. Respond with only the JSON object." | `phase.Retry.Parse` |
| Semantic | Immediate | Append: "Previous response had error: {message}. Please address and produce valid output." | `phase.Retry.Semantic` |
| Context | N/A | N/A | 0 (fail immediately) |
| Unknown | N/A | N/A | 0 (fail immediately) |

On each retry, the engine logs the full previous output to `logs/<phase>_retry_<attempt>.md` for post-mortem debugging.

### Backoff implementation

```go
func (eng *Engine) backoff(attempt int) time.Duration {
    base := 2 * time.Second
    maxDelay := 30 * time.Second
    delay := base * time.Duration(1<<uint(attempt)) // 2s, 4s, 8s, 16s, 30s
    if delay > maxDelay {
        delay = maxDelay
    }
    jitter := eng.config.JitterFunc(time.Second) // up to 1s random
    return delay + jitter
}
```

`SleepFunc` and `JitterFunc` are injected via `EngineConfig` for testing.

## Phase gating

Domain-specific checks after certain phases succeed, before proceeding to the next:

| After phase | Check | Action |
|-------------|-------|--------|
| triage | `Automatable == false` | Return `PhaseGateError` with `BlockReason` |
| plan | `len(Tasks) == 0` | Return `PhaseGateError` ("plan produced no tasks") |
| implement | `TestsPassed == false` | Log warning event; proceed to verify (verify will catch it) |
| verify | `Verdict == "FAIL"` | Return `PhaseGateError` with `FixesRequired` summary |
| submit | `PRURL == ""` | Return `PhaseGateError` ("no PR URL produced") |

These checks require unmarshaling the phase's structured JSON output into the corresponding `schemas.*Output` type. The engine unmarshals after writing the result to disk.

## Budget enforcement

1. Before each phase: `remaining = MaxCostUSD - state.Meta().TotalCost`. If `remaining <= 0`, return `BudgetExceededError`.
2. Set per-phase budget: `min(phaseBudgetFromConfig, remaining)` to tighten as the pipeline progresses.
3. After each phase: call `state.AccumulateCost(phase, result.CostUSD)`.
4. Budget warning event at 90% of `MaxCostUSD`.

Note: `TotalCost` is cumulative across resume cycles (never reset by State). If a phase crashes mid-run and is re-run, both runs' costs accumulate. This is correct — the money was spent.

## Checkpoint mode

```go
if eng.config.Mode == Checkpoint {
    eng.emit(Event{Phase: phase.Name, Kind: EventCheckpointPause})
    select {
    case <-eng.confirmCh:
        // proceed
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

The lock stays held across pauses — no other process can steal the ticket during human review.

## Worktree management

- Created at the start of the implement phase, if `state.Meta().Worktree == ""`
- Path stored in `state.Meta().Worktree` and `state.Meta().Branch`, flushed to `meta.json`
- Used by implement, verify, submit, and monitor phases as `WorktreePath` in prompt data
- NOT auto-removed on failure (human may need to inspect)
- On resume: check that `state.Meta().Worktree` still exists on disk. If deleted, log warning.
- Cleanup deferred to a future `soda clean` command

## Monitor phase (v1 stub)

The monitor phase has `type: "polling"` in phases.yaml. For v1:

1. Parse `submit.json` to get `PRURL`
2. Validate the PR exists (could call `gh pr view` or `glab mr view` via the runner, but for v1 stub, skip this)
3. Emit event: `{kind: "monitor_skipped", data: {reason: "not_implemented", pr_url: "..."}}`
4. Mark phase as completed (a skipped monitor is not a failure)

The full polling loop will be implemented when the monitor feature is built out.

## Prompt rendering

### Template loading

`PromptLoader` searches directories in order (user override first, built-in second):

```go
loader := NewPromptLoader(
    "~/.config/soda/prompts",  // user overrides
    "<project-root>/prompts",  // built-in
)
```

Path traversal validation: resolved path must stay within the searched directories.

### Template execution

Go `text/template` with the `PromptData` struct as context. The `PromptData` struct is a plain data struct with no methods — no side-effecting calls from templates.

### Artifact rendering for downstream injection

For each phase, the engine renders a deterministic markdown summary from the structured JSON output, not the model's freeform text. This summary is what downstream phases receive via `{{.Artifacts.Plan}}`, etc.

Example: rendering plan output for the implement phase:

```go
func renderPlanArtifact(plan *schemas.PlanOutput) string {
    var buf strings.Builder
    fmt.Fprintf(&buf, "Approach: %s\n\n", plan.Approach)
    for _, task := range plan.Tasks {
        fmt.Fprintf(&buf, "### Task %s: %s\n", task.ID, task.Description)
        fmt.Fprintf(&buf, "Files: %s\n", strings.Join(task.Files, ", "))
        fmt.Fprintf(&buf, "Done when: %s\n\n", task.DoneWhen)
    }
    return buf.String()
}
```

### Pre-render validation

Before rendering, verify that all declared dependency artifacts exist and are non-empty. Fail loudly rather than letting `text/template` silently render empty strings.

## Testing

All tests use `MockRunner`. Follow existing patterns: table-driven, `t.TempDir()`, `t.Run()` subtests.

### Test cases

1. **Happy path**: all 6 phases complete in order, artifacts flow between phases correctly
2. **Resume from phase**: mark triage+plan as completed, verify engine starts from implement
3. **Skip completed**: mark all phases as completed, verify Run() returns immediately
4. **Transient retry**: mock returns `claude.TransientError`, verify retry with backoff, verify sleepFunc called with increasing durations
5. **Parse retry**: mock returns `claude.ParseError`, verify retry with error context appended to prompt
6. **Semantic retry**: mock returns `claude.SemanticError`, verify retry with corrective feedback
7. **Max retries exhausted**: verify phase marked as failed after all retries consumed
8. **Budget exceeded**: set low `MaxCostUSD`, mock returns non-zero cost, verify `BudgetExceededError`
9. **Checkpoint mode**: verify engine emits `checkpoint_pause` and waits on `confirmCh`
10. **Phase dependencies**: remove a dependency's result, verify `DependencyNotMetError`
11. **Context cancellation**: cancel context mid-phase, verify clean return of `ctx.Err()`
12. **Phase gating**: mock triage returning `Automatable: false`, verify `PhaseGateError`
13. **Monitor stub**: verify monitor emits `monitor_skipped` and marks as completed

### Compile-time interface check

```go
var _ runner.Runner = (*runner.MockRunner)(nil)
```

## Dependencies

- #1 (claude/) — error types used for retry classification
- #3 (pipeline/state.go) — disk state management
- #2 (sandbox/) — NOT needed. Build against Runner interface with MockRunner.
- gopkg.in/yaml.v3 — first external dependency, for phases.yaml parsing

## Out of scope

- Schema-to-JSON-Schema conversion (needed for `RunOpts.OutputSchema`, but belongs in CLI wiring or a `schemas/` helper)
- Config loading from `~/.config/soda/config.yaml` (issue #7, CLI commands)
- TUI integration (issue #8)
- Full monitor polling loop (future issue)
- `soda clean` command for worktree cleanup
- `PhaseRetrying`/`PhasePaused` status methods on State (add when needed)
