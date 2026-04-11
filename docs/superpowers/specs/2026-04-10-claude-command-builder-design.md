# Claude Code Command Builder — Design Spec

**Issue:** decko/soda#1
**Date:** 2026-04-10
**Status:** Approved

## Summary

Build the Claude Code CLI wrapper (`internal/claude/`) that generates the correct
command for each pipeline phase, streams stdout to a callback, parses the JSON
response envelope, and classifies errors.

## Approach

Single `Runner` struct with a `Stream()` method (Approach A). Matches Go stdlib
patterns (`http.Client.Do()`, `exec.Cmd.Run()`). Parser extracted to its own file
with fixture-based tests per AGENTS.md warning about the unstable CLI output format.

## File Layout

```
internal/claude/
    types.go         # RunOpts, RunResult, TokenUsage
    errors.go        # TransientError, ParseError, SemanticError
    args.go          # buildArgs() private function
    parser.go        # ParseResponse() exported, extractJSON() private
    runner.go        # Runner struct, Stream(), DryRun()
    types_test.go
    args_test.go
    parser_test.go
    runner_test.go
    testdata/
        success.json
        error_response.json
        missing_usage.json
        extra_fields.json
        no_structured_output.json
        empty_output.json
        mixed_streaming.txt
```

## Types (`types.go`)

```go
package claude

import "encoding/json"

// Runner holds shared configuration for invoking the Claude Code CLI.
// Created once per SODA session, reused across phases.
type Runner struct {
    Binary  string // path to claude binary, default "claude"
    Model   string
    WorkDir string
}

// RunOpts holds per-invocation configuration for a single phase run.
type RunOpts struct {
    SystemPromptPath string
    OutputSchema     string   // JSON schema string passed to --json-schema
    AllowedTools     []string // tool names for --allowed-tools
    MaxBudgetUSD     float64
    Stdin            string   // piped to process stdin
}

// RunResult holds the parsed response from a Claude Code CLI invocation.
type RunResult struct {
    Output     json.RawMessage // raw structured_output, caller unmarshals into phase schema
    Result     string          // freeform text from "result" field
    CostUSD    float64
    Tokens     TokenUsage
    DurationMs int64
    Turns      int
}

// TokenUsage holds token counts from the CLI response.
type TokenUsage struct {
    InputTokens              int64 `json:"input_tokens"`
    CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
    CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
    OutputTokens             int64 `json:"output_tokens"`
}
```

## Errors (`errors.go`)

Three concrete error types, classified by source:

```go
// TransientError represents a retryable infrastructure failure
// (API timeout, rate limit, process crash).
type TransientError struct {
    Err error
}

// ParseError represents a failure to parse the CLI response.
type ParseError struct {
    Raw []byte // raw output for diagnostics
    Err error
}

// SemanticError represents a logically invalid response
// (subtype "error" from Claude).
type SemanticError struct {
    Message string
}
```

All implement `error`. `TransientError` and `ParseError` implement `Unwrap()`.

### Classification logic

| Source | Signal | Error type |
|--------|--------|-----------|
| Process exit + stderr | rate limit, timeout, 429, 529 | `TransientError` |
| Process exit + stderr | context cancelled/deadline | `context.Canceled` / `context.DeadlineExceeded` (not retryable) |
| JSON parsing | no JSON found, malformed, wrong envelope type | `ParseError` |
| Response envelope | `"subtype": "error"` | `SemanticError` |

Semantic validation of `structured_output` content (e.g. plan has no tasks) is
the pipeline engine's responsibility, not the wrapper's.

Consumer uses `errors.As()` for classification:

```go
var transient *claude.TransientError
if errors.As(err, &transient) { /* retry with backoff */ }
```

## Args (`args.go`)

Private function `buildArgs(opts RunOpts, model string) []string`.

Fixed flags (always present):
- `--print`
- `--bare`
- `--output-format json`
- `--permission-mode bypassPermissions`

Conditional flags (from `RunOpts`):
- `--system-prompt-file <path>` if `SystemPromptPath` is set
- `--json-schema <schema>` if `OutputSchema` is set
- `--model <model>` if model is non-empty
- `--max-budget-usd <amount>` if `MaxBudgetUSD > 0`
- `--allowed-tools <tool>` (one flag per tool) if `AllowedTools` is non-empty

Tested with table-driven tests — no process spawning needed.

## Parser (`parser.go`)

```go
// ParseResponse parses raw CLI output into a RunResult.
// Exported so the sandbox layer can reuse it independently of Stream().
func ParseResponse(output []byte) (*RunResult, error)
```

### Internal steps

1. **`extractJSON(output []byte) ([]byte, error)`** — scan backwards from end of
   buffer for last valid JSON object. Handles non-JSON streaming output that
   precedes the response envelope.

2. **Unmarshal into `rawEnvelope`** — generous intermediate struct:

```go
type rawEnvelope struct {
    Type       string          `json:"type"`
    Subtype    string          `json:"subtype"`
    Result     string          `json:"result"`
    Structured json.RawMessage `json:"structured_output"`
    Cost       *float64        `json:"total_cost_usd"`
    Usage      json.RawMessage `json:"usage"`
    NumTurns   *int            `json:"num_turns"`
    Duration   *int64          `json:"duration_ms"`
}
```

   Pointer fields for optional numerics (distinguish absent from zero).
   `json.RawMessage` for fields whose shape may change. Unknown fields accepted
   silently (no `DisallowUnknownFields`).

3. **Classify** — if `subtype == "error"`, return `&SemanticError{Message: envelope.Result}`.
   If `type != "result"`, return `&ParseError{...}`.

4. **Convert to `RunResult`** — zero-value missing fields. Parse `usage` JSON into
   `TokenUsage` separately, tolerating missing fields. Never fail because optional
   metadata is absent.

### Fixture tests

Parser tested via `testdata/` fixtures:

| Fixture | Tests |
|---------|-------|
| `success.json` | Normal response, all fields present |
| `error_response.json` | `subtype: "error"`, returns `SemanticError` |
| `missing_usage.json` | No usage field, zero-valued tokens |
| `extra_fields.json` | Unknown keys present, no error |
| `no_structured_output.json` | Missing structured_output, nil Output |
| `empty_output.json` | Process killed mid-output, returns `ParseError` |
| `mixed_streaming.txt` | Non-JSON lines before JSON envelope |

## Runner (`runner.go`)

### `Stream()`

```go
func (r *Runner) Stream(ctx context.Context, opts RunOpts, onChunk func(string)) (*RunResult, error)
```

Execution sequence:

1. `buildArgs(opts, r.Model)` to construct CLI args.
2. `exec.CommandContext(ctx, r.binary(), args...)` with `Dir = r.WorkDir`.
3. Process group isolation: `SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`.
4. Graceful cancellation: `cmd.Cancel` sends SIGTERM to process group,
   `cmd.WaitDelay = 5 * time.Second` before SIGKILL.
5. If `opts.Stdin != ""`, set `cmd.Stdin = strings.NewReader(opts.Stdin)`.
6. Pipe stdout and stderr.
7. `cmd.Start()`.
8. Two goroutines via `sync.WaitGroup`:
   - **stdout**: `bufio.Scanner` with 1MB buffer. Call `onChunk(line)` per line.
     Accumulate full output into `bytes.Buffer`.
   - **stderr**: `io.Copy` into separate `bytes.Buffer`.
9. `wg.Wait()` — drain both pipes before proceeding.
10. `cmd.Wait()` — check for errors:
    - If `ctx.Err() != nil`: return context error (not retryable).
    - If exit error: classify via stderr content, return `TransientError`.
11. `ParseResponse(outputBuf.Bytes())` — returns `*RunResult` or classified error.

### `DryRun()`

```go
func (r *Runner) DryRun(opts RunOpts) []string
```

Returns the full arg list without executing. For logging to `events.jsonl`
and debugging.

### `binary()` (private)

Returns `r.Binary` if set, otherwise `"claude"`. Allows test injection of a
mock script (`r.Binary = "testdata/mock_claude.sh"`).

## Consumer Interface

Per AGENTS.md convention ("interfaces: define at consumer, not producer"), the
`pipeline` package defines its own interface:

```go
// pipeline/engine.go
type PhaseRunner interface {
    Stream(ctx context.Context, opts claude.RunOpts, onChunk func(string)) (*claude.RunResult, error)
}
```

`claude.Runner` satisfies this implicitly via structural typing. No interface
is declared in the `claude` package.

## Key Design Decisions

1. **`json.RawMessage` for `Output`** — the wrapper doesn't know about phase
   schemas. The engine unmarshals into `TriageOutput`, `PlanOutput`, etc.

2. **Exported `ParseResponse()`** — the sandbox layer (`sandbox/runner.go`)
   will wrap `agent-node sandbox-run` around `claude`. It needs to parse the
   same response format without using `Stream()`.

3. **1MB scanner buffer** — default 64KB `bufio.Scanner` will silently truncate
   implement-phase responses that include long tool output.

4. **Process group kill** — Claude Code spawns child processes (Bash tool) that
   would become orphans without group-level signal delivery.

5. **Backward scan for JSON** — stdout may contain streaming progress output
   before the final JSON envelope. Scanning backwards for the last valid JSON
   object is more robust than assuming the entire output is JSON.

6. **Semantic validation upstream** — the wrapper classifies infrastructure
   and format errors. Content validation (plan has no tasks, verify finds no
   tests) is the engine's responsibility with phase-specific knowledge.
