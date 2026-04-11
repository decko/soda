# Claude Code Command Builder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/claude/` — the Claude Code CLI wrapper that generates commands, streams output, parses JSON responses, and classifies errors.

**Architecture:** Single `Runner` struct with `Stream()` method. Parser in own file with fixture tests. Three concrete error types (`TransientError`, `ParseError`, `SemanticError`) classified via `errors.As()`.

**Tech Stack:** Go 1.25, stdlib only (no external dependencies)

**Spec:** `docs/superpowers/specs/2026-04-10-claude-command-builder-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `go.mod` | Module definition |
| `internal/claude/types.go` | `RunOpts`, `RunResult`, `TokenUsage`, `DryRunResult` — pure data types |
| `internal/claude/errors.go` | `TransientError`, `ParseError`, `SemanticError` — error types with `Error()`, `Unwrap()` |
| `internal/claude/args.go` | `buildArgs()` — CLI flag construction from `RunOpts` |
| `internal/claude/parser.go` | `ParseResponse()`, `extractJSON()`, `parseTokenUsage()` — JSON response parsing |
| `internal/claude/runner.go` | `Runner`, `NewRunner()`, `Stream()`, `DryRun()`, `limitedBuffer`, `classifyExitError()` |
| `internal/claude/errors_test.go` | Error type behavior tests |
| `internal/claude/args_test.go` | Table-driven arg builder tests |
| `internal/claude/parser_test.go` | Fixture-based parser tests |
| `internal/claude/runner_test.go` | Runner tests using mock CLI script |
| `internal/claude/testdata/` | Fixtures + mock script |

---

### Task 1: Go module init + types

**Files:**
- Create: `go.mod`
- Create: `internal/claude/types.go`

- [ ] **Step 1: Initialize Go module**

```bash
cd /home/ddebrito/dev/soda
go mod init github.com/decko/soda
```

Verify `go.mod` contains:
```
module github.com/decko/soda

go 1.25
```

- [ ] **Step 2: Create types.go**

Create `internal/claude/types.go`:

```go
package claude

import (
	"encoding/json"
	"time"
)

// RunOpts holds per-invocation configuration for a single phase run.
type RunOpts struct {
	SystemPromptPath string        // path to system prompt file
	OutputSchema     string        // JSON schema string passed to --json-schema
	AllowedTools     []string      // tool names for --allowed-tools
	MaxBudgetUSD     *float64      // nil = omit flag; non-nil = emit value
	Prompt           string        // rendered template piped via stdin
	Timeout          time.Duration // fallback timeout if caller's context has no deadline
}

// RunResult holds the parsed response from a Claude Code CLI invocation.
type RunResult struct {
	Output   json.RawMessage // raw structured_output — caller unmarshals into phase schema
	Result   string          // freeform text from "result" field
	CostUSD  float64         // 0.0 if absent — per-invocation, not cumulative
	Tokens   TokenUsage
	Duration time.Duration // parsed from duration_ms
	Turns    int           // 0 if absent
}

// TokenUsage holds token counts from the CLI response.
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
	Prompt string
}
```

- [ ] **Step 3: Verify compilation**

```bash
cd /home/ddebrito/dev/soda && go vet ./internal/claude/
```

Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add go.mod internal/claude/types.go
git commit -m "feat: add Go module and claude package types

Initialize go.mod and define RunOpts, RunResult, TokenUsage,
DryRunResult data types for the Claude Code CLI wrapper."
```

---

### Task 2: Error types

**Files:**
- Create: `internal/claude/errors.go`
- Create: `internal/claude/errors_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/claude/errors_test.go`:

```go
package claude

import (
	"errors"
	"fmt"
	"testing"
)

func TestTransientError(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	err := &TransientError{
		Stderr: []byte("connection refused to api.anthropic.com"),
		Reason: "connection",
		Err:    inner,
	}

	if got := err.Error(); got != "claude: transient (connection): connection refused" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}

	// errors.As from a wrapped error
	wrapped := fmt.Errorf("phase triage: %w", err)
	var target *TransientError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As should find TransientError in chain")
	}
	if target.Reason != "connection" {
		t.Errorf("Reason = %q, want %q", target.Reason, "connection")
	}
}

func TestParseError(t *testing.T) {
	inner := fmt.Errorf("invalid JSON")
	err := &ParseError{
		Raw: []byte("not json at all"),
		Err: inner,
	}

	if got := err.Error(); got != "claude: parse error: invalid JSON" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}

	wrapped := fmt.Errorf("phase plan: %w", err)
	var target *ParseError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As should find ParseError in chain")
	}
}

func TestSemanticError(t *testing.T) {
	err := &SemanticError{Message: "API key is invalid"}

	if got := err.Error(); got != "claude: semantic error: API key is invalid" {
		t.Errorf("Error() = %q", got)
	}

	wrapped := fmt.Errorf("phase implement: %w", err)
	var target *SemanticError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As should find SemanticError in chain")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run TestTransientError -v
```

Expected: FAIL — `TransientError` not defined.

- [ ] **Step 3: Write errors.go**

Create `internal/claude/errors.go`:

```go
package claude

import "fmt"

// TransientError represents a retryable infrastructure failure
// (API timeout, rate limit, process crash, OOM kill).
type TransientError struct {
	Stderr []byte // raw stderr for diagnostics
	Reason string // "rate_limit", "timeout", "oom", "signal", "connection", "overloaded", "unknown"
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

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run "Test(Transient|Parse|Semantic)Error" -v
```

Expected: PASS — all three tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/errors.go internal/claude/errors_test.go
git commit -m "feat: add claude error types with classification support

TransientError (retryable), ParseError (bad output), SemanticError
(Claude reported error). All support errors.As() for consumer
classification."
```

---

### Task 3: Arg builder

**Files:**
- Create: `internal/claude/args.go`
- Create: `internal/claude/args_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/claude/args_test.go`:

```go
package claude

import (
	"slices"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	budget := 5.0

	tests := []struct {
		name     string
		opts     RunOpts
		model    string
		contains []string // flag-value pairs that must appear in order
		excludes []string // flags that must NOT appear
	}{
		{
			name:  "full_options",
			model: "claude-opus-4-6",
			opts: RunOpts{
				SystemPromptPath: "/tmp/prompt.md",
				OutputSchema:     `{"type":"object"}`,
				AllowedTools:     []string{"Read", "Glob"},
				MaxBudgetUSD:     &budget,
			},
			contains: []string{
				"--print",
				"--bare",
				"--output-format", "json",
				"--permission-mode", "bypassPermissions",
				"--system-prompt-file", "/tmp/prompt.md",
				"--json-schema", `{"type":"object"}`,
				"--model", "claude-opus-4-6",
				"--max-budget-usd",
				"--allowed-tools", "Read",
				"--allowed-tools", "Glob",
			},
		},
		{
			name:  "minimal_options",
			model: "",
			opts:  RunOpts{},
			contains: []string{
				"--print",
				"--bare",
				"--output-format", "json",
				"--permission-mode", "bypassPermissions",
			},
			excludes: []string{
				"--system-prompt-file",
				"--json-schema",
				"--model",
				"--max-budget-usd",
				"--allowed-tools",
			},
		},
		{
			name:  "nil_budget_omits_flag",
			model: "sonnet",
			opts: RunOpts{
				MaxBudgetUSD: nil,
			},
			excludes: []string{"--max-budget-usd"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildArgs(tt.opts, tt.model)

			for _, want := range tt.contains {
				if !slices.Contains(args, want) {
					t.Errorf("args missing %q\ngot: %v", want, args)
				}
			}
			for _, excluded := range tt.excludes {
				if slices.Contains(args, excluded) {
					t.Errorf("args should not contain %q\ngot: %v", excluded, args)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run TestBuildArgs -v
```

Expected: FAIL — `buildArgs` not defined.

- [ ] **Step 3: Write args.go**

Create `internal/claude/args.go`:

```go
package claude

import "strconv"

// buildArgs constructs the CLI argument list from RunOpts and model.
func buildArgs(opts RunOpts, model string) []string {
	args := []string{
		"--print",
		"--bare",
		"--output-format", "json",
		"--permission-mode", "bypassPermissions",
	}

	if opts.SystemPromptPath != "" {
		args = append(args, "--system-prompt-file", opts.SystemPromptPath)
	}
	if opts.OutputSchema != "" {
		args = append(args, "--json-schema", opts.OutputSchema)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if opts.MaxBudgetUSD != nil {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(*opts.MaxBudgetUSD, 'f', -1, 64))
	}
	for _, tool := range opts.AllowedTools {
		args = append(args, "--allowed-tools", tool)
	}

	return args
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run TestBuildArgs -v
```

Expected: PASS — all three subtests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/args.go internal/claude/args_test.go
git commit -m "feat: add buildArgs for Claude CLI flag construction

Generates --print --bare --output-format json and conditional flags
from RunOpts. Table-driven tests cover full, minimal, and nil-budget
cases."
```

---

### Task 4: Test fixtures + parser

**Files:**
- Create: `internal/claude/testdata/success.json`
- Create: `internal/claude/testdata/error_response.json`
- Create: `internal/claude/testdata/missing_usage.json`
- Create: `internal/claude/testdata/extra_fields.json`
- Create: `internal/claude/testdata/no_structured_output.json`
- Create: `internal/claude/testdata/empty_output.json`
- Create: `internal/claude/testdata/mixed_streaming.txt`
- Create: `internal/claude/testdata/wrong_type.json`
- Create: `internal/claude/testdata/fake_envelope_in_tool_output.txt`
- Create: `internal/claude/parser.go`
- Create: `internal/claude/parser_test.go`

- [ ] **Step 1: Create test fixtures**

Create `internal/claude/testdata/success.json`:
```json
{
  "type": "result",
  "subtype": "success",
  "result": "Implementation complete.",
  "structured_output": {"ticket_key": "PROJ-123", "complexity": "small"},
  "total_cost_usd": 0.097,
  "usage": {
    "input_tokens": 1174,
    "cache_creation_input_tokens": 7493,
    "cache_read_input_tokens": 38034,
    "output_tokens": 1020
  },
  "modelUsage": {
    "claude-opus-4-6": {
      "inputTokens": 1174,
      "outputTokens": 1020,
      "costUSD": 0.097
    }
  },
  "num_turns": 10,
  "duration_ms": 29689,
  "duration_api_ms": 27832
}
```

Create `internal/claude/testdata/error_response.json`:
```json
{
  "type": "result",
  "subtype": "error",
  "result": "API key is invalid or expired."
}
```

Create `internal/claude/testdata/missing_usage.json`:
```json
{
  "type": "result",
  "subtype": "success",
  "result": "Done.",
  "structured_output": {"key": "value"},
  "total_cost_usd": 0.01,
  "num_turns": 1,
  "duration_ms": 500
}
```

Create `internal/claude/testdata/extra_fields.json`:
```json
{
  "type": "result",
  "subtype": "success",
  "result": "Done.",
  "structured_output": {"key": "value"},
  "total_cost_usd": 0.01,
  "usage": {"input_tokens": 100, "output_tokens": 50},
  "num_turns": 1,
  "duration_ms": 500,
  "new_field_from_future_cli": "should be ignored",
  "another_new_field": 42
}
```

Create `internal/claude/testdata/no_structured_output.json`:
```json
{
  "type": "result",
  "subtype": "success",
  "result": "No structured output was produced."
}
```

Create `internal/claude/testdata/empty_output.json` — empty file (0 bytes).

Create `internal/claude/testdata/mixed_streaming.txt`:
```
Thinking about the problem...
Reading file: src/main.go
Running: go test ./...
PASS
{"type":"result","subtype":"success","result":"Tests pass.","structured_output":{"tests_passed":true},"total_cost_usd":0.05,"usage":{"input_tokens":500,"output_tokens":200},"num_turns":5,"duration_ms":12000}
```

Create `internal/claude/testdata/wrong_type.json`:
```json
{
  "type": "conversation",
  "subtype": "success",
  "result": "This is not a --print response."
}
```

Create `internal/claude/testdata/fake_envelope_in_tool_output.txt`:
```
Reading files...
Command output: {"type":"result","subtype":"success","structured_output":{"fake":true}}
More processing...
{"type":"result","subtype":"success","result":"Real response.","structured_output":{"real":true},"total_cost_usd":0.03,"usage":{"input_tokens":200,"output_tokens":100},"num_turns":2,"duration_ms":3000}
```

- [ ] **Step 2: Write the failing test**

Create `internal/claude/parser_test.go`:

```go
package claude

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantErr     bool
		errType     string // "parse", "semantic"
		checkResult func(t *testing.T, r *RunResult)
	}{
		{
			name:    "success",
			fixture: "testdata/success.json",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.CostUSD != 0.097 {
					t.Errorf("CostUSD = %v, want 0.097", r.CostUSD)
				}
				if r.Tokens.InputTokens != 1174 {
					t.Errorf("InputTokens = %d, want 1174", r.Tokens.InputTokens)
				}
				if r.Tokens.OutputTokens != 1020 {
					t.Errorf("OutputTokens = %d, want 1020", r.Tokens.OutputTokens)
				}
				if r.Tokens.CacheCreationInputTokens != 7493 {
					t.Errorf("CacheCreationInputTokens = %d, want 7493", r.Tokens.CacheCreationInputTokens)
				}
				if r.Tokens.CacheReadInputTokens != 38034 {
					t.Errorf("CacheReadInputTokens = %d, want 38034", r.Tokens.CacheReadInputTokens)
				}
				if r.Turns != 10 {
					t.Errorf("Turns = %d, want 10", r.Turns)
				}
				if r.Duration != 29689*time.Millisecond {
					t.Errorf("Duration = %v, want %v", r.Duration, 29689*time.Millisecond)
				}
				if r.Result != "Implementation complete." {
					t.Errorf("Result = %q", r.Result)
				}
				var so map[string]interface{}
				if err := json.Unmarshal(r.Output, &so); err != nil {
					t.Fatalf("Output unmarshal: %v", err)
				}
				if so["ticket_key"] != "PROJ-123" {
					t.Errorf("ticket_key = %v, want PROJ-123", so["ticket_key"])
				}
			},
		},
		{
			name:    "error_response",
			fixture: "testdata/error_response.json",
			wantErr: true,
			errType: "semantic",
		},
		{
			name:    "missing_usage",
			fixture: "testdata/missing_usage.json",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.Tokens.InputTokens != 0 {
					t.Errorf("InputTokens = %d, want 0", r.Tokens.InputTokens)
				}
				if r.CostUSD != 0.01 {
					t.Errorf("CostUSD = %v, want 0.01", r.CostUSD)
				}
			},
		},
		{
			name:    "extra_fields",
			fixture: "testdata/extra_fields.json",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.CostUSD != 0.01 {
					t.Errorf("CostUSD = %v, want 0.01", r.CostUSD)
				}
			},
		},
		{
			name:    "no_structured_output",
			fixture: "testdata/no_structured_output.json",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.Output != nil {
					t.Errorf("Output = %s, want nil", r.Output)
				}
				if r.Result != "No structured output was produced." {
					t.Errorf("Result = %q", r.Result)
				}
			},
		},
		{
			name:    "empty_output",
			fixture: "testdata/empty_output.json",
			wantErr: true,
			errType: "parse",
		},
		{
			name:    "mixed_streaming",
			fixture: "testdata/mixed_streaming.txt",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.Result != "Tests pass." {
					t.Errorf("Result = %q, want %q", r.Result, "Tests pass.")
				}
				if r.CostUSD != 0.05 {
					t.Errorf("CostUSD = %v, want 0.05", r.CostUSD)
				}
			},
		},
		{
			name:    "wrong_type",
			fixture: "testdata/wrong_type.json",
			wantErr: true,
			errType: "parse",
		},
		{
			name:    "fake_envelope_in_tool_output",
			fixture: "testdata/fake_envelope_in_tool_output.txt",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				var so map[string]interface{}
				if err := json.Unmarshal(r.Output, &so); err != nil {
					t.Fatalf("Output unmarshal: %v", err)
				}
				if so["real"] != true {
					t.Errorf("Expected real output, got %v", so)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(tt.fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			result, err := ParseResponse(data)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				switch tt.errType {
				case "parse":
					var pe *ParseError
					if !errors.As(err, &pe) {
						t.Errorf("expected ParseError, got %T: %v", err, err)
					}
				case "semantic":
					var se *SemanticError
					if !errors.As(err, &se) {
						t.Errorf("expected SemanticError, got %T: %v", err, err)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, raw []byte)
	}{
		{
			name:  "single_line_json",
			input: `{"type":"result","subtype":"success"}`,
			check: func(t *testing.T, raw []byte) {
				if string(raw) != `{"type":"result","subtype":"success"}` {
					t.Errorf("got %s", raw)
				}
			},
		},
		{
			name:  "json_after_text",
			input: "some text\n" + `{"type":"result"}`,
			check: func(t *testing.T, raw []byte) {
				if string(raw) != `{"type":"result"}` {
					t.Errorf("got %s", raw)
				}
			},
		},
		{
			name:    "no_json",
			input:   "just plain text\nno json here\n",
			wantErr: true,
		},
		{
			name:    "empty_input",
			input:   "",
			wantErr: true,
		},
		{
			name:  "trailing_whitespace",
			input: `{"type":"result"}` + "\n\n  \n",
			check: func(t *testing.T, raw []byte) {
				if string(raw) != `{"type":"result"}` {
					t.Errorf("got %s", raw)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := extractJSON([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, raw)
			}
		})
	}
}

func TestParseTokenUsage(t *testing.T) {
	t.Run("known_fields", func(t *testing.T) {
		raw := []byte(`{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":200}`)
		usage := parseTokenUsage(raw)
		if usage.InputTokens != 100 {
			t.Errorf("InputTokens = %d", usage.InputTokens)
		}
		if usage.OutputTokens != 50 {
			t.Errorf("OutputTokens = %d", usage.OutputTokens)
		}
		if usage.CacheReadInputTokens != 200 {
			t.Errorf("CacheReadInputTokens = %d", usage.CacheReadInputTokens)
		}
	})

	t.Run("extra_fields_collected", func(t *testing.T) {
		raw := []byte(`{"input_tokens":100,"output_tokens":50,"new_category":999}`)
		usage := parseTokenUsage(raw)
		if usage.InputTokens != 100 {
			t.Errorf("InputTokens = %d", usage.InputTokens)
		}
		if usage.Extra == nil || usage.Extra["new_category"] != 999 {
			t.Errorf("Extra = %v, want new_category=999", usage.Extra)
		}
	})

	t.Run("nil_input", func(t *testing.T) {
		usage := parseTokenUsage(nil)
		if usage.InputTokens != 0 {
			t.Errorf("InputTokens = %d, want 0", usage.InputTokens)
		}
	})
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run "TestParseResponse|TestExtractJSON|TestParseTokenUsage" -v
```

Expected: FAIL — `ParseResponse`, `extractJSON`, `parseTokenUsage` not defined.

- [ ] **Step 4: Write parser.go**

Create `internal/claude/parser.go`:

```go
package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var errNoJSON = errors.New("no JSON response envelope found in output")

// rawEnvelope is the intermediate representation of the Claude CLI JSON response.
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

// ParseResponse parses raw CLI output into a RunResult.
// Exported so the sandbox layer can reuse it independently of Stream().
func ParseResponse(output []byte) (*RunResult, error) {
	raw, err := extractJSON(output)
	if err != nil {
		return nil, &ParseError{
			Raw: truncateForLog(output, 4096),
			Err: err,
		}
	}

	var env rawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &ParseError{
			Raw: truncateForLog(raw, 4096),
			Err: fmt.Errorf("unmarshal response: %w", err),
		}
	}

	if env.Type != "result" {
		return nil, &ParseError{
			Raw: truncateForLog(raw, 4096),
			Err: fmt.Errorf("unexpected response type: %q", env.Type),
		}
	}

	if env.Subtype == "error" {
		return nil, &SemanticError{
			Message: env.Result,
		}
	}

	result := &RunResult{
		Result: env.Result,
		Tokens: parseTokenUsage(env.Usage),
	}

	// Normalize structured_output: nil and JSON "null" both become nil
	if len(env.Structured) > 0 && string(env.Structured) != "null" {
		result.Output = env.Structured
	}

	if env.Cost != nil {
		result.CostUSD = *env.Cost
	}
	if env.NumTurns != nil {
		result.Turns = *env.NumTurns
	}
	if env.Duration != nil {
		result.Duration = time.Duration(*env.Duration) * time.Millisecond
	}

	return result, nil
}

// extractJSON finds the JSON response envelope in the output.
// Strategy 1: try the last non-empty line (common case — envelope on final line).
// Strategy 2: backward brace scan for multi-line JSON.
func extractJSON(output []byte) ([]byte, error) {
	if len(output) == 0 {
		return nil, errNoJSON
	}

	// Strategy 1: last non-empty line
	trimmed := bytes.TrimRight(output, " \t\n\r")
	if len(trimmed) == 0 {
		return nil, errNoJSON
	}

	lastNewline := bytes.LastIndexByte(trimmed, '\n')
	var lastLine []byte
	if lastNewline == -1 {
		lastLine = trimmed
	} else {
		lastLine = trimmed[lastNewline+1:]
	}

	if len(lastLine) > 0 && lastLine[0] == '{' && json.Valid(lastLine) {
		return lastLine, nil
	}

	// Strategy 2: backward scan — find last '}', try '{' candidates with json.Valid
	return extractJSONByDepth(output)
}

// extractJSONByDepth scans backwards for the last valid JSON object.
func extractJSONByDepth(output []byte) ([]byte, error) {
	end := bytes.LastIndexByte(output, '}')
	if end == -1 {
		return nil, errNoJSON
	}

	for i := end; i >= 0; i-- {
		if output[i] == '{' {
			candidate := output[i : end+1]
			if json.Valid(candidate) {
				return candidate, nil
			}
		}
	}

	return nil, errNoJSON
}

// parseTokenUsage extracts token counts from the usage JSON.
// Known fields map to TokenUsage struct fields; unknown fields go to Extra.
func parseTokenUsage(raw json.RawMessage) TokenUsage {
	if len(raw) == 0 {
		return TokenUsage{}
	}

	var usage TokenUsage
	_ = json.Unmarshal(raw, &usage)

	// Collect unknown fields into Extra
	var all map[string]float64
	if err := json.Unmarshal(raw, &all); err != nil {
		return usage
	}

	known := map[string]bool{
		"input_tokens":                true,
		"cache_creation_input_tokens": true,
		"cache_read_input_tokens":     true,
		"output_tokens":               true,
	}

	for key, val := range all {
		if !known[key] {
			if usage.Extra == nil {
				usage.Extra = make(map[string]int64)
			}
			usage.Extra[key] = int64(val)
		}
	}

	return usage
}

// truncateForLog returns data truncated to maxLen bytes for log readability.
func truncateForLog(data []byte, maxLen int) []byte {
	if len(data) <= maxLen {
		return data
	}
	return data[:maxLen]
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run "TestParseResponse|TestExtractJSON|TestParseTokenUsage" -v
```

Expected: PASS — all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/claude/parser.go internal/claude/parser_test.go internal/claude/testdata/
git commit -m "feat: add Claude CLI response parser with fixture tests

ParseResponse extracts JSON from mixed streaming output, parses the
response envelope, and classifies errors. extractJSON uses last-line-
first strategy with brace-depth fallback. 9 fixture files cover
success, error, missing fields, extra fields, streaming, wrong type,
and fake envelope injection."
```

---

### Task 5: Runner — NewRunner + limitedBuffer + classifyExitError

**Files:**
- Create: `internal/claude/testdata/mock_claude.sh`
- Create: `internal/claude/runner.go`
- Create: `internal/claude/runner_test.go`

- [ ] **Step 1: Create mock CLI script**

Create `internal/claude/testdata/mock_claude.sh`:

```bash
#!/bin/bash
# Mock Claude CLI for testing. Behavior controlled by MOCK_CLAUDE_MODE env var.

# Handle --version
for arg in "$@"; do
    if [ "$arg" = "--version" ]; then
        echo "claude-code 1.0.0-test"
        exit 0
    fi
done

case "${MOCK_CLAUDE_MODE}" in
    success)
        echo "Thinking..."
        echo "Reading files..."
        echo '{"type":"result","subtype":"success","result":"Done.","structured_output":{"key":"value"},"total_cost_usd":0.05,"usage":{"input_tokens":100,"output_tokens":50},"num_turns":3,"duration_ms":5000}'
        ;;
    semantic_error)
        echo '{"type":"result","subtype":"error","result":"Something went wrong."}'
        ;;
    crash_rate_limit)
        echo "rate limit exceeded" >&2
        exit 1
        ;;
    crash_unknown)
        echo "unexpected internal error" >&2
        exit 1
        ;;
    slow)
        sleep 60
        ;;
    echo_stdin)
        # Read stdin and include it in the result for verification
        STDIN_CONTENT=$(cat)
        echo "{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"${STDIN_CONTENT}\",\"structured_output\":{}}"
        ;;
    *)
        signal_kill)
        kill -KILL $$
        ;;
    signal_term)
        kill -TERM $$
        ;;
    *)
        echo "unknown mode: ${MOCK_CLAUDE_MODE}" >&2
        exit 1
        ;;
esac
```

Make it executable:
```bash
chmod +x internal/claude/testdata/mock_claude.sh
```

- [ ] **Step 2: Write the failing tests**

Create `internal/claude/runner_test.go`:

```go
package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Ensure mock script is executable
	os.Chmod("testdata/mock_claude.sh", 0755)
	os.Exit(m.Run())
}

func mockBinaryPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/mock_claude.sh")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestNewRunner(t *testing.T) {
	t.Run("valid_binary", func(t *testing.T) {
		runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		if runner.version != "claude-code 1.0.0-test" {
			t.Errorf("version = %q, want %q", runner.version, "claude-code 1.0.0-test")
		}
	})

	t.Run("missing_binary", func(t *testing.T) {
		_, err := NewRunner("/nonexistent/path/claude", "model", t.TempDir())
		if err == nil {
			t.Fatal("expected error for missing binary")
		}
	})

	t.Run("relative_workdir_rejected", func(t *testing.T) {
		_, err := NewRunner(mockBinaryPath(t), "model", "relative/path")
		if err == nil {
			t.Fatal("expected error for relative workDir")
		}
	})
}

func TestLimitedBuffer(t *testing.T) {
	t.Run("within_limit", func(t *testing.T) {
		lb := &limitedBuffer{max: 100}
		lb.Write([]byte("hello"))
		lb.Write([]byte(" world"))
		if lb.Len() != 11 {
			t.Errorf("Len = %d, want 11", lb.Len())
		}
		if lb.overflow {
			t.Error("should not overflow")
		}
	})

	t.Run("exceeds_limit", func(t *testing.T) {
		lb := &limitedBuffer{max: 10}
		lb.Write([]byte("12345"))
		lb.Write([]byte("67890abc")) // would exceed 10
		if !lb.overflow {
			t.Error("should overflow")
		}
		// Buffer contains data up to the limit
		if lb.Len() > 10 {
			t.Errorf("Len = %d, should be <= 10", lb.Len())
		}
	})

	t.Run("write_returns_full_length", func(t *testing.T) {
		lb := &limitedBuffer{max: 5}
		n, err := lb.Write([]byte("1234567890"))
		if err != nil {
			t.Errorf("Write error: %v", err)
		}
		if n != 10 {
			t.Errorf("Write returned %d, want 10", n)
		}
	})
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run "TestNewRunner|TestLimitedBuffer" -v
```

Expected: FAIL — `NewRunner`, `limitedBuffer` not defined.

- [ ] **Step 4: Write runner.go with NewRunner, limitedBuffer, classifyExitError**

Create `internal/claude/runner.go`:

```go
package claude

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Runner holds shared configuration for invoking the Claude Code CLI.
// Safe for concurrent use — Stream() holds no mutable state between calls.
type Runner struct {
	binary  string // resolved absolute path to claude binary
	model   string
	workDir string
	version string // cached claude --version output
}

// NewRunner creates a Runner with validated configuration.
// binary defaults to "claude" if empty. workDir must be absolute.
// Resolves binary via exec.LookPath and caches CLI version.
func NewRunner(binary, model, workDir string) (*Runner, error) {
	if binary == "" {
		binary = "claude"
	}

	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("claude binary not found: %w", err)
	}

	if !filepath.IsAbs(workDir) {
		return nil, fmt.Errorf("workDir must be absolute: %s", workDir)
	}

	// Cache CLI version for diagnostics
	version := "unknown"
	cmd := exec.Command(resolved, "--version")
	if out, err := cmd.Output(); err == nil {
		version = strings.TrimSpace(string(out))
	}

	return &Runner{
		binary:  resolved,
		model:   model,
		workDir: workDir,
		version: version,
	}, nil
}

// limitedBuffer accumulates bytes up to a maximum, then silently discards excess.
// Write always reports success so pipe draining is never blocked.
type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	if lb.overflow {
		return len(p), nil
	}
	remaining := lb.max - lb.buf.Len()
	if len(p) > remaining {
		if remaining > 0 {
			lb.buf.Write(p[:remaining])
		}
		lb.overflow = true
		return len(p), nil
	}
	return lb.buf.Write(p)
}

func (lb *limitedBuffer) Bytes() []byte { return lb.buf.Bytes() }
func (lb *limitedBuffer) Len() int      { return lb.buf.Len() }

// classifyExitError categorizes a process exit failure.
func classifyExitError(exitErr *exec.ExitError, stderr []byte) error {
	// Check for signal-based kill (OOM, external SIGTERM, etc.)
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		sig := status.Signal()
		if sig == syscall.SIGKILL {
			return &TransientError{
				Stderr: stderr,
				Reason: "oom",
				Err:    fmt.Errorf("process killed by signal %s (possible OOM)", sig),
			}
		}
		return &TransientError{
			Stderr: stderr,
			Reason: "signal",
			Err:    fmt.Errorf("process killed by signal %s", sig),
		}
	}

	// Check stderr patterns
	stderrLower := strings.ToLower(string(stderr))

	patterns := []struct {
		substrings []string
		reason     string
	}{
		{[]string{"rate limit", "429"}, "rate_limit"},
		{[]string{"timeout", "504", "529"}, "timeout"},
		{[]string{"overloaded", "500", "502", "503", "server error"}, "overloaded"},
		{[]string{"connection refused", "econnreset", "connection reset"}, "connection"},
	}

	for _, p := range patterns {
		for _, s := range p.substrings {
			if strings.Contains(stderrLower, s) {
				return &TransientError{
					Stderr: stderr,
					Reason: p.reason,
					Err:    fmt.Errorf("claude exited with code %d", exitErr.ExitCode()),
				}
			}
		}
	}

	// Fallback: unknown transient error
	return &TransientError{
		Stderr: stderr,
		Reason: "unknown",
		Err:    fmt.Errorf("claude exited with code %d", exitErr.ExitCode()),
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run "TestNewRunner|TestLimitedBuffer" -v
```

Expected: PASS — all subtests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/claude/runner.go internal/claude/runner_test.go internal/claude/testdata/mock_claude.sh
git commit -m "feat: add Runner constructor, limitedBuffer, and error classifier

NewRunner validates binary (LookPath), workDir (absolute), and caches
CLI version. limitedBuffer caps accumulation with silent discard.
classifyExitError maps exit codes, signals, and stderr patterns to
TransientError reasons."
```

---

### Task 6: Stream + DryRun

**Files:**
- Modify: `internal/claude/runner.go`
- Modify: `internal/claude/runner_test.go`

- [ ] **Step 1: Add Stream and DryRun tests**

Append to `internal/claude/runner_test.go`:

```go
func TestStream_Success(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "success")

	var chunks []string
	result, err := runner.Stream(context.Background(), RunOpts{
		Timeout: 10 * time.Second,
	}, func(line string) {
		chunks = append(chunks, line)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if result.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v, want 0.05", result.CostUSD)
	}
	if result.Turns != 3 {
		t.Errorf("Turns = %d, want 3", result.Turns)
	}
	// Should have streaming lines: "Thinking...", "Reading files...", and the JSON line
	if len(chunks) < 3 {
		t.Errorf("expected at least 3 chunks, got %d: %v", len(chunks), chunks)
	}
}

func TestStream_SemanticError(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "semantic_error")

	_, err = runner.Stream(context.Background(), RunOpts{
		Timeout: 10 * time.Second,
	}, nil)

	var semantic *SemanticError
	if !errors.As(err, &semantic) {
		t.Fatalf("expected SemanticError, got %T: %v", err, err)
	}
	if semantic.Message != "Something went wrong." {
		t.Errorf("Message = %q", semantic.Message)
	}
}

func TestStream_TransientError(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "crash_rate_limit")

	_, err = runner.Stream(context.Background(), RunOpts{
		Timeout: 10 * time.Second,
	}, nil)

	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("expected TransientError, got %T: %v", err, err)
	}
	if transient.Reason != "rate_limit" {
		t.Errorf("Reason = %q, want %q", transient.Reason, "rate_limit")
	}
}

func TestStream_UnknownExitError(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "crash_unknown")

	_, err = runner.Stream(context.Background(), RunOpts{
		Timeout: 10 * time.Second,
	}, nil)

	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("expected TransientError, got %T: %v", err, err)
	}
	if transient.Reason != "unknown" {
		t.Errorf("Reason = %q, want %q", transient.Reason, "unknown")
	}
}

func TestStream_ContextCancel(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "slow")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = runner.Stream(ctx, RunOpts{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestStream_SignalKill(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "signal_kill")

	_, err = runner.Stream(context.Background(), RunOpts{
		Timeout: 10 * time.Second,
	}, nil)

	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("expected TransientError, got %T: %v", err, err)
	}
	if transient.Reason != "oom" {
		t.Errorf("Reason = %q, want %q", transient.Reason, "oom")
	}
}

func TestStream_SignalTerm(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "signal_term")

	_, err = runner.Stream(context.Background(), RunOpts{
		Timeout: 10 * time.Second,
	}, nil)

	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("expected TransientError, got %T: %v", err, err)
	}
	if transient.Reason != "signal" {
		t.Errorf("Reason = %q, want %q", transient.Reason, "signal")
	}
}

func TestStream_NilOnChunk(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "success")

	result, err := runner.Stream(context.Background(), RunOpts{
		Timeout: 10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if result.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v, want 0.05", result.CostUSD)
	}
}

func TestStream_StdinDelivery(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "echo_stdin")

	result, err := runner.Stream(context.Background(), RunOpts{
		Prompt:  "Hello from stdin",
		Timeout: 10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if result.Result != "Hello from stdin" {
		t.Errorf("Result = %q, want %q", result.Result, "Hello from stdin")
	}
}

func TestStream_RejectsRelativePromptPath(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	_, err = runner.Stream(context.Background(), RunOpts{
		SystemPromptPath: "relative/path/prompt.md",
		Timeout:          10 * time.Second,
	}, nil)
	if err == nil {
		t.Fatal("expected error for relative SystemPromptPath")
	}
}

func TestStream_RejectsInvalidSchema(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	_, err = runner.Stream(context.Background(), RunOpts{
		OutputSchema: "not valid json {{{",
		Timeout:      10 * time.Second,
	}, nil)
	if err == nil {
		t.Fatal("expected error for invalid OutputSchema JSON")
	}
}

func TestDryRun(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	budget := 5.0
	result := runner.DryRun(RunOpts{
		SystemPromptPath: "/tmp/prompt.md",
		OutputSchema:     `{"type":"object"}`,
		AllowedTools:     []string{"Read", "Glob"},
		MaxBudgetUSD:     &budget,
		Prompt:           "Implement the feature.",
	})

	// Check key flags are present
	wantFlags := map[string]bool{
		"--print": true, "--bare": true,
		"--output-format": true, "--permission-mode": true,
		"--system-prompt-file": true, "--json-schema": true,
		"--model": true, "--max-budget-usd": true,
		"--allowed-tools": true,
	}
	for _, arg := range result.Args {
		delete(wantFlags, arg)
	}
	for flag := range wantFlags {
		t.Errorf("missing flag: %s", flag)
	}

	if result.Prompt != "Implement the feature." {
		t.Errorf("Prompt = %q", result.Prompt)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run "TestStream|TestDryRun" -v
```

Expected: FAIL — `Stream`, `DryRun` not defined.

- [ ] **Step 3: Implement Stream and DryRun**

Append to `internal/claude/runner.go`:

Merge the imports at the top of runner.go to include all needed packages. The
final combined import block should be:

```go
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)
```

Add methods:

```go
// Stream invokes the Claude Code CLI, streams output line-by-line via onChunk,
// and returns the parsed response. Context cancellation kills the subprocess
// and its entire process group.
func (r *Runner) Stream(ctx context.Context, opts RunOpts, onChunk func(string)) (*RunResult, error) {
	// Validate opts
	if opts.SystemPromptPath != "" && !filepath.IsAbs(opts.SystemPromptPath) {
		return nil, fmt.Errorf("claude: system prompt path must be absolute: %s", opts.SystemPromptPath)
	}
	if opts.OutputSchema != "" {
		if len(opts.OutputSchema) > 256*1024 {
			return nil, fmt.Errorf("claude: output schema exceeds 256KB limit")
		}
		if !json.Valid([]byte(opts.OutputSchema)) {
			return nil, fmt.Errorf("claude: output schema is not valid JSON")
		}
	}

	args := buildArgs(opts, r.model)

	// Apply fallback timeout if caller's context has no deadline
	if opts.Timeout > 0 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
			defer cancel()
		}
	}

	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = r.workDir

	// Stdin: prompt via stdin, or /dev/null if empty
	if opts.Prompt != "" {
		cmd.Stdin = strings.NewReader(opts.Prompt)
	}
	// When Prompt is empty, cmd.Stdin stays nil → reads from /dev/null

	// Process group isolation — kill the entire group on cancel.
	// NOTE: Setpgid and syscall.Kill(-pid) are Linux/macOS only.
	// SODA requires Linux (Landlock, seccomp, cgroups), so this is acceptable.
	// Pdeathsig is not set — grandchild processes that setsid may escape group
	// kill. The sandbox layer's cgroup kill is the fallback.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, &TransientError{
			Reason: "unknown",
			Err:    fmt.Errorf("claude: start: %w", err),
		}
	}

	// Drain stdout and stderr concurrently
	var outputBuf limitedBuffer
	outputBuf.max = 50 * 1024 * 1024 // 50MB
	var stderrBuf limitedBuffer
	stderrBuf.max = 1024 * 1024 // 1MB

	var stdoutErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line
		for scanner.Scan() {
			line := scanner.Text()
			outputBuf.Write([]byte(line))
			outputBuf.Write([]byte("\n"))
			if onChunk != nil {
				func() {
					defer func() { recover() }()
					onChunk(line)
				}()
			}
		}
		if err := scanner.Err(); err != nil {
			stdoutErr = fmt.Errorf("claude: scan stdout: %w", err)
		}
	}()

	go func() {
		defer wg.Done()
		io.Copy(&stderrBuf, stderr)
	}()

	wg.Wait()

	// Check stdout drain error
	if stdoutErr != nil {
		cmd.Wait()
		return nil, stdoutErr
	}

	waitErr := cmd.Wait()

	if waitErr != nil {
		// Context cancellation — not retryable
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Non-zero exit: try parsing stdout first (some CLIs exit non-zero with valid output)
		if outputBuf.Len() > 0 {
			result, parseErr := ParseResponse(outputBuf.Bytes())
			if parseErr == nil {
				return result, nil
			}
		}

		// Classify the exit error
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return nil, classifyExitError(exitErr, stderrBuf.Bytes())
		}
		return nil, &TransientError{
			Stderr: stderrBuf.Bytes(),
			Reason: "unknown",
			Err:    fmt.Errorf("claude: wait: %w", waitErr),
		}
	}

	// Check buffer overflow
	if outputBuf.overflow {
		return nil, &ParseError{
			Raw: truncateForLog(outputBuf.Bytes(), 4096),
			Err: fmt.Errorf("stdout exceeded %d byte buffer limit", outputBuf.max),
		}
	}

	return ParseResponse(outputBuf.Bytes())
}

// DryRun returns the command that would be executed, without running it.
// For logging to events.jsonl and debugging.
func (r *Runner) DryRun(opts RunOpts) DryRunResult {
	return DryRunResult{
		Args:   buildArgs(opts, r.model),
		Prompt: opts.Prompt,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -run "TestStream|TestDryRun" -v
```

Expected: PASS — all Stream and DryRun tests pass.

- [ ] **Step 5: Run full test suite + quality checks**

```bash
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -v -count=1
```

Expected: PASS — all tests pass.

```bash
cd /home/ddebrito/dev/soda && go vet ./internal/claude/
```

Expected: no output (clean).

- [ ] **Step 6: Commit**

```bash
git add internal/claude/runner.go internal/claude/runner_test.go
git commit -m "feat: add Stream and DryRun methods to Runner

Stream invokes Claude CLI, streams stdout line-by-line via callback,
drains pipes concurrently, classifies exit errors (transient, parse,
semantic), handles context cancellation with process group SIGTERM,
and parses the JSON response envelope. DryRun returns args without
executing for logging/debugging. Tests use mock CLI script for
success, semantic error, transient error, context cancel, stdin
delivery, and nil callback scenarios."
```

---

## Post-Implementation Verification

After all tasks are complete, run:

```bash
# Full test suite
cd /home/ddebrito/dev/soda && go test ./internal/claude/ -v -count=1 -race

# Static analysis
go vet ./internal/claude/

# Build verification (types are importable)
go build ./internal/claude/
```

All must pass before considering this issue done.
