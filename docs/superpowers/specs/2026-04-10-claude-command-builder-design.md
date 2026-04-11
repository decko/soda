# Claude Code Command Builder — Design Spec

**Issue:** decko/soda#1
**Date:** 2026-04-10
**Status:** Approved
**Platform:** Linux (Landlock, seccomp, cgroups — `Setpgid`/`syscall.Kill` are Linux/macOS only)

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
    runner.go        # Runner struct, NewRunner(), Stream(), DryRun()
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
        wrong_type.json
        fake_envelope_in_tool_output.txt
```

## Types (`types.go`)

```go
package claude

import (
    "encoding/json"
    "time"
)

// Runner holds shared configuration for invoking the Claude Code CLI.
// Created once per SODA session, reused across phases.
// Safe for concurrent use — Stream() holds no mutable state between calls.
type Runner struct {
    binary  string // resolved path to claude binary
    model   string
    workDir string
    version string // cached output of claude --version, for diagnostics
}

// NewRunner creates a Runner with validated configuration.
// binary is the path to the claude CLI (empty string defaults to "claude").
// workDir must be an absolute path within the project root or worktree directory.
// Resolves the binary via exec.LookPath at construction time.
func NewRunner(binary, model, workDir string) (*Runner, error)

// RunOpts holds per-invocation configuration for a single phase run.
type RunOpts struct {
    SystemPromptPath string    // path to system prompt file (validated against allowed dirs)
    OutputSchema     string    // JSON schema string passed to --json-schema
    AllowedTools     []string  // tool names for --allowed-tools
    MaxBudgetUSD     *float64  // nil = omit flag; non-nil = emit value (pointer distinguishes unset from zero)
    Prompt           string    // rendered template — the user prompt, piped via stdin
    Timeout          time.Duration // fallback timeout if caller's context has no deadline
}

// RunResult holds the parsed response from a Claude Code CLI invocation.
type RunResult struct {
    Output   json.RawMessage // raw structured_output, caller unmarshals into phase schema
    Result   string          // freeform text from "result" field
    CostUSD  float64         // 0.0 if absent — per-invocation, not cumulative across retries
    Tokens   TokenUsage
    Duration time.Duration   // parsed from duration_ms, zero if absent
    Turns    int             // 0 if absent (indistinguishable from actual zero — acceptable)
}

// TokenUsage holds token counts from the CLI response.
// New token categories added by the CLI will appear in Extra.
type TokenUsage struct {
    InputTokens              int64 `json:"input_tokens"`
    CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
    CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
    OutputTokens             int64 `json:"output_tokens"`
    Extra                    map[string]int64 `json:"-"` // overflow for unknown token categories
}

// DryRunResult holds the command that would be executed, for logging.
type DryRunResult struct {
    Args   []string
    Prompt string // the stdin content (rendered template)
}
```

### Prompt delivery

The rendered Go template (ticket data + handoff artifacts from prior phases) is
the **user prompt**. It is piped to Claude Code via stdin. The `--system-prompt-file`
flag provides the phase role/instructions. This means:

- `RunOpts.Prompt` = rendered template content → `cmd.Stdin`
- `RunOpts.SystemPromptPath` = phase prompt file → `--system-prompt-file`

The prompt template rendering is upstream of this package (in the pipeline engine).
`RunOpts.Prompt` is an **untrusted input boundary** — ticket descriptions, plan
content, and PR comments originate from external sources (Jira, GitHub) and may
contain prompt injection payloads. Defense against prompt injection is the
responsibility of the prompt template layer, not this package. This package ensures
that `Prompt` content cannot influence CLI argument parsing (guaranteed by
`exec.Command` separating argv from stdin).

## Errors (`errors.go`)

Three concrete error types, classified by source:

```go
// TransientError represents a retryable infrastructure failure
// (API timeout, rate limit, process crash, OOM kill).
type TransientError struct {
    Stderr []byte // raw stderr for diagnostics
    Reason string // "rate_limit", "timeout", "oom", "signal", "unknown"
    Err    error
}

func (e *TransientError) Error() string {
    return fmt.Sprintf("claude: transient (%s): %s", e.Reason, e.Err)
}
func (e *TransientError) Unwrap() error { return e.Err }

// ParseError represents a failure to parse the CLI response.
type ParseError struct {
    Raw []byte // raw output (truncated to 4KB for log readability)
    Err error
}

func (e *ParseError) Error() string {
    return fmt.Sprintf("claude: parse error: %s", e.Err)
}
func (e *ParseError) Unwrap() error { return e.Err }

// SemanticError represents a logically invalid response
// (subtype "error" from Claude).
type SemanticError struct {
    Message string
}

func (e *SemanticError) Error() string {
    return fmt.Sprintf("claude: semantic error: %s", e.Message)
}
```

All error messages use `claude:` prefix for clean wrapping chains per AGENTS.md
convention (`fmt.Errorf("context: %w", err)`).

### Classification logic

| Source | Signal | Error type | Reason |
|--------|--------|-----------|--------|
| Process exit + stderr | "rate limit", "429" | `TransientError` | `rate_limit` |
| Process exit + stderr | "timeout", "504", "529" | `TransientError` | `timeout` |
| Process exit + stderr | "overloaded", "500", "502", "503", "server error" | `TransientError` | `overloaded` |
| Process exit + stderr | "connection refused", "ECONNRESET", "connection reset" | `TransientError` | `connection` |
| Process exit + signal | SIGKILL (exit via signal, no context cancel) — e.g. OOM killer | `TransientError` | `oom` |
| Process exit + signal | SIGTERM, SIGPIPE, other signals (no context cancel) | `TransientError` | `signal` |
| Process exit | non-zero exit, unrecognized stderr, no context cancel | `TransientError` | `unknown` |
| Process exit | context cancelled/deadline exceeded | `context.Canceled` / `context.DeadlineExceeded` (not retryable) | — |
| JSON parsing | no JSON found, malformed, wrong envelope type | `ParseError` | — |
| Response envelope | `"subtype": "error"` | `SemanticError` | — |

**Fallback rule:** any non-zero exit code that is not context cancellation defaults
to `TransientError` with reason `"unknown"`. This ensures OOM kills, unexpected
signals, and unrecognized errors are retried rather than silently dropped.

**Non-zero exit with valid output:** if exit code is non-zero AND stderr does not
match known transient patterns, still attempt `ParseResponse` on stdout. Only
return `TransientError` if parsing also fails. Some CLI versions may exit non-zero
with warnings but still produce valid output.

**Signal detection:** use `exec.ExitError` → `ProcessState.Sys().(syscall.WaitStatus)`
to distinguish signal kills from normal exits.

Semantic validation of `structured_output` content (e.g. plan has no tasks) is
the pipeline engine's responsibility, not the wrapper's.

Consumer uses `errors.As()` for classification. The `Reason` field enables
differentiated backoff (longer for rate limits, immediate for connection resets):

```go
var transient *claude.TransientError
if errors.As(err, &transient) {
    switch transient.Reason {
    case "rate_limit":
        // longer backoff
    default:
        // standard exponential backoff
    }
}
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
- `--max-budget-usd <amount>` if `MaxBudgetUSD` is non-nil
- `--allowed-tools <tool>` (one flag per tool) if `AllowedTools` is non-empty

Tested with table-driven tests — no process spawning needed.

### Validation (in `NewRunner` and `buildArgs`)

- `SystemPromptPath`: after resolving symlinks (`filepath.EvalSymlinks`) and
  converting to absolute path, verify it falls within the project root, the
  embedded prompts directory, or `~/.config/soda/prompts/`. Reject escapes.
- `AllowedTools`: validate each entry against known tool names/patterns
  (`Read`, `Write`, `Edit`, `Glob`, `Grep`, `Bash`, `Bash(git:*)`, etc.)
  to catch config typos early. Log a warning for unknown tools but do not
  reject (the CLI may add new tools).
- `OutputSchema`: validate JSON syntax and check size < 256KB (well under
  Linux `ARG_MAX` of ~2MB).

## Parser (`parser.go`)

```go
// ParseResponse parses raw CLI output into a RunResult.
// Exported so the sandbox layer can reuse it independently of Stream().
func ParseResponse(output []byte) (*RunResult, error)
```

### JSON extraction strategy

Claude Code with `--print --output-format json` outputs streaming progress
(tool use notifications, file reads) to stdout, followed by the JSON response
envelope as the **final content**. The extraction approach:

1. **Last-line-first strategy**: scan backwards for the last non-empty line.
   Try `json.Unmarshal` on it. If it's valid JSON with `"type": "result"`,
   use it. This handles the common case (envelope on last line) cheaply.

2. **Fallback: brace-depth scan**: if last-line fails (envelope spans multiple
   lines), scan backwards from end of buffer using brace/bracket depth tracking
   that ignores braces inside quoted strings (accounting for escaped quotes).
   Try `json.Valid()` on each candidate, then verify `"type": "result"` after
   unmarshal.

3. **Envelope validation**: after extraction, verify the parsed JSON contains
   `"type": "result"`. Never accept arbitrary JSON — this prevents tool output
   poisoning where a Bash command outputs a fake envelope structure.

**Security note:** during implement/verify phases, Claude Code has Bash access.
A malicious repo file or command could output a crafted JSON object matching the
envelope structure. The `"type": "result"` validation after extraction, combined
with the last-content-first strategy (real envelope is always last), mitigates
this. Test explicitly with `testdata/fake_envelope_in_tool_output.txt`.

### Internal parsing

Unmarshal into `rawEnvelope` — generous intermediate struct:

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

### Classification

- If `type` is not `"result"`: return `&ParseError{...}`. Include actual type
  value in error message for diagnostics (covers unknown types like `"progress"`,
  `"partial"`, etc.).
- If `subtype == "error"`: return `&SemanticError{Message: envelope.Result}`.
- Otherwise: convert to `RunResult`.

### Conversion to RunResult

- Zero-value missing fields. Cost = 0.0 if absent.
- Parse `usage` JSON into `TokenUsage` separately, tolerating missing fields.
  Unknown token categories go into `TokenUsage.Extra`.
- Convert `duration_ms` int64 → `time.Duration` once here.
- Never fail because optional metadata is absent.

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
| `wrong_type.json` | `type: "conversation"`, returns `ParseError` with type in message |
| `fake_envelope_in_tool_output.txt` | Tool output contains fake envelope before real one — must extract real envelope |

## Runner (`runner.go`)

### `NewRunner()`

```go
func NewRunner(binary, model, workDir string) (*Runner, error)
```

- If `binary` is empty, defaults to `"claude"`.
- Resolves binary via `exec.LookPath` at construction time. Returns error if
  not found.
- Validates `workDir` is an absolute path.
- Captures `claude --version` output and caches in `runner.version` for
  diagnostic inclusion in `ParseError` and logs.
- Logs resolved binary path for audit purposes.

### `Stream()`

```go
func (r *Runner) Stream(ctx context.Context, opts RunOpts, onChunk func(string)) (*RunResult, error)
```

Execution sequence:

1. `buildArgs(opts, r.model)` to construct CLI args.
2. Apply fallback timeout: if `opts.Timeout > 0` and `ctx` has no deadline,
   wrap ctx with `context.WithTimeout(ctx, opts.Timeout)`.
3. `exec.CommandContext(ctx, r.binary, args...)` with `Dir = r.workDir`.
4. **Stdin**: if `opts.Prompt != ""`, set `cmd.Stdin = strings.NewReader(opts.Prompt)`.
   Otherwise, explicitly set `cmd.Stdin = nil` (maps to `/dev/null` — prevents
   reading from parent's stdin in unattended execution).
5. Process group isolation: `SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`.
6. Graceful cancellation:
   ```go
   cmd.Cancel = func() error {
       return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
   }
   cmd.WaitDelay = 5 * time.Second
   ```
   This sends SIGTERM to the entire process group (not just the main process),
   then SIGKILL after 5s. Required because Claude Code's Bash tool spawns child
   processes that would become orphans with default `Process.Kill()`.
7. Pipe stdout and stderr via `cmd.StdoutPipe()` and `cmd.StderrPipe()`.
8. `cmd.Start()`.
9. Two goroutines via `errgroup.Group` (preferred over `sync.WaitGroup` to
   propagate errors):
   - **stdout**: `bufio.Scanner` with 1MB buffer. Call `onChunk(line)` per line
     (wrapped in `recover()` — panic in callback must not deadlock pipe draining).
     Accumulate into `bytes.Buffer` capped at 50MB (`limitedBuffer`). After scan
     loop, **check `scanner.Err()`** — if `bufio.ErrTooLong`, return descriptive
     error ("stdout line exceeded 1MB scanner buffer"). If buffer cap exceeded,
     return descriptive error.
   - **stderr**: `io.Copy` into `bytes.Buffer` capped at 1MB. Excess silently
     truncated (stderr is for diagnostics, not data).
10. `eg.Wait()` — drain both pipes and collect errors before proceeding.
11. `cmd.Wait()` — check for errors:
    - If `ctx.Err() != nil`: return context error (not retryable).
    - If exit error with valid stdout: attempt `ParseResponse(outputBuf.Bytes())`
      first — some CLI versions exit non-zero with warnings but produce valid output.
    - If exit error with signal (via `syscall.WaitStatus`): classify signal type
      (SIGKILL → `oom`, others → `signal`), return `TransientError` with stderr.
    - If exit error with stderr matching known patterns: return `TransientError`
      with appropriate reason.
    - If exit error, no match: return `TransientError{Reason: "unknown"}` with
      stderr attached.
12. `ParseResponse(outputBuf.Bytes())` — returns `*RunResult` or classified error.

### `DryRun()`

```go
func (r *Runner) DryRun(opts RunOpts) DryRunResult
```

Returns the full arg list and prompt content without executing. For logging to
`events.jsonl` and debugging. Note: `events.jsonl` should have 0600 permissions
as it may contain filesystem paths and prompt content.

The `DryRun()` method is accessed directly on `*Runner` for CLI `--dry-run` mode
and pre-execution logging. It is NOT part of the `PhaseRunner` interface — the
engine calls it directly on the concrete `Runner` before invoking `Stream()`.

### `binary()` removed

No longer needed. `NewRunner()` resolves and stores the binary path at
construction time. The field is private, eliminating the exported-field-with-
private-default-accessor inconsistency.

## Consumer Interface

Per AGENTS.md convention ("interfaces: define at consumer, not producer"), the
`pipeline` package defines its own interface:

```go
// pipeline/engine.go
type PhaseRunner interface {
    Stream(ctx context.Context, opts claude.RunOpts, onChunk func(string)) (*claude.RunResult, error)
}
```

`*claude.Runner` satisfies this implicitly via structural typing. No interface
is declared in the `claude` package.

`DryRun()` is not on the interface — it's used directly on `*Runner` for
logging before execution. The engine holds a concrete `*Runner` reference
alongside the `PhaseRunner` interface (or just uses `*Runner` directly).

## Key Design Decisions

1. **`json.RawMessage` for `Output`** — the wrapper doesn't know about phase
   schemas. The engine unmarshals into `TriageOutput`, `PlanOutput`, etc.

2. **Exported `ParseResponse()`** — the sandbox layer (`sandbox/runner.go`)
   will wrap `agent-node sandbox-run` around `claude`. It needs to parse the
   same response format without using `Stream()`.

3. **1MB scanner buffer** — default 64KB `bufio.Scanner` will silently truncate
   implement-phase responses that include long tool output.

4. **Process group kill** — Claude Code spawns child processes (Bash tool) that
   would become orphans without group-level signal delivery. `cmd.Cancel` is
   explicitly set to SIGTERM the process group — the default `Process.Kill()`
   only kills the main process.

5. **Last-line-first JSON extraction** — stdout may contain streaming progress
   output before the final JSON envelope. Reading the last line first is simpler
   and more secure than scanning for JSON objects in mixed content. Fallback to
   brace-depth scan only if last-line approach fails.

6. **Envelope validation after extraction** — extracted JSON must contain
   `"type": "result"` to prevent tool output poisoning (Bash commands outputting
   fake envelope structures).

7. **Semantic validation upstream** — the wrapper classifies infrastructure
   and format errors. Content validation (plan has no tasks, verify finds no
   tests) is the engine's responsibility with phase-specific knowledge.

8. **`NewRunner()` constructor** — validates config, resolves binary via
   `exec.LookPath`, and caches CLI version at construction time rather than
   at first `Stream()` call. Private fields prevent misconfiguration.

9. **`*float64` for `MaxBudgetUSD`** — distinguishes "not set" (nil, omit flag)
   from "zero" (explicit value). Matches the pointer-for-optional pattern used
   in `rawEnvelope`.

10. **`time.Duration` for `RunResult.Duration`** — parsed once in the parser,
    avoids unit confusion downstream. Raw `int64` stays in `rawEnvelope`.

11. **Fallback timeout** — `RunOpts.Timeout` applies when the caller's context
    has no deadline. Prevents `Stream()` from blocking forever if called with
    `context.Background()`.

12. **`TransientError.Reason`** — enables differentiated backoff at the engine
    level (longer for rate limits, immediate for connection resets).

13. **Buffer caps** — 50MB stdout, 1MB stderr. Prevents OOM in the SODA process
    from runaway Claude Code output. The cgroup limits protect the child process;
    these caps protect the parent.

14. **`errgroup` over `sync.WaitGroup`** — goroutine errors (scanner overflow,
    io.Copy failure) are propagated to `Stream()` rather than silently dropped.

15. **`onChunk` panic recovery** — callback is user-provided (TUI layer). A
    panic must not deadlock pipe draining. Wrapped in `recover()`.

16. **Prompt via stdin** — the rendered template is the user prompt, piped via
    `cmd.Stdin`. The system prompt (phase role/instructions) is a file via
    `--system-prompt-file`. This package documents `RunOpts.Prompt` as an
    untrusted input boundary.

17. **CLI version caching** — captured at `NewRunner()` time for inclusion in
    error diagnostics. Helps debug format changes when the CLI updates.

18. **`TokenUsage.Extra`** — overflow map for unknown token categories, so new
    CLI fields don't require code changes.

## Platform Constraints

- `SysProcAttr{Setpgid: true}` and `syscall.Kill(-pid, ...)` are Linux/macOS
  only. SODA requires Linux (Landlock, seccomp, cgroups) so this is acceptable.
  Add a comment noting the platform constraint.
- `Pdeathsig` is not set — grandchild processes that create their own process
  groups (via `setsid`) may escape the group kill. The sandbox layer's cgroup
  kill is the fallback. Without the sandbox layer (dev/testing), grandchild
  leaks are possible. Document this limitation.
