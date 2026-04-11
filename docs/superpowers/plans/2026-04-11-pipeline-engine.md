# Pipeline Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the pipeline engine that runs phases sequentially via an abstract Runner interface, with classified retries, budget enforcement, checkpoint mode, and event emission.

**Architecture:** The engine loads phase config from `phases.yaml`, renders prompt templates, calls `runner.Run()` per phase, classifies errors for retry, persists state via the existing `State` type, and emits events via a callback. A separate `internal/runner/` package defines the Runner interface with its own `RunOpts`/`RunResult` types (abstraction boundary for the sandbox). Worktree management lives in `internal/git/`.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3` (first external dep), `text/template`, `os/exec` (for git)

**Spec:** `docs/superpowers/specs/2026-04-11-pipeline-engine-design.md`

---

### Task 1: Add yaml.v3 dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get gopkg.in/yaml.v3
```

- [ ] **Step 2: Verify go.mod updated**

Run:
```bash
cat go.mod
```
Expected: `require gopkg.in/yaml.v3 v3.x.x`

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add gopkg.in/yaml.v3 dependency"
```

---

### Task 2: Runner interface and MockRunner

**Files:**
- Create: `internal/runner/runner.go`
- Create: `internal/runner/mock.go`
- Create: `internal/runner/mock_test.go`

- [ ] **Step 1: Write the Runner interface, RunOpts, and RunResult**

Create `internal/runner/runner.go`:

```go
package runner

import (
	"context"
	"encoding/json"
	"time"
)

// Runner executes a single pipeline phase in an isolated session.
// The concrete implementation will be the sandbox runner (#2).
type Runner interface {
	Run(ctx context.Context, opts RunOpts) (*RunResult, error)
}

// RunOpts holds everything needed to execute one phase.
type RunOpts struct {
	Phase        string        // phase name (e.g., "triage", "plan")
	SystemPrompt string        // rendered system prompt content
	UserPrompt   string        // rendered user prompt (ticket + artifacts)
	OutputSchema string        // JSON schema for structured output
	AllowedTools []string      // tool scoping per phase
	MaxBudgetUSD float64       // cost cap for this phase
	WorkDir      string        // working directory for the agent
	Model        string        // model to use
	Timeout      time.Duration // phase timeout
}

// RunResult holds the parsed response from a phase execution.
type RunResult struct {
	Output     json.RawMessage // structured output matching the phase schema
	RawText    string          // freeform text output
	CostUSD    float64
	TokensIn   int64
	TokensOut  int64
	DurationMs int64
	Turns      int
}
```

- [ ] **Step 2: Write the MockRunner**

Create `internal/runner/mock.go`:

```go
package runner

import (
	"context"
	"fmt"
	"sync"
)

// MockRunner returns canned responses from fixture data for testing.
type MockRunner struct {
	mu        sync.Mutex
	Responses map[string]*RunResult // phase name -> canned response
	Errors    map[string]error      // phase name -> error to return
	Calls     []RunOpts             // recorded calls for assertions
}

// Run records the call and returns the configured response or error.
func (m *MockRunner) Run(ctx context.Context, opts RunOpts) (*RunResult, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, opts)
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err, ok := m.Errors[opts.Phase]; ok {
		return nil, err
	}
	if result, ok := m.Responses[opts.Phase]; ok {
		return result, nil
	}
	return nil, fmt.Errorf("mock: no response configured for phase %q", opts.Phase)
}
```

- [ ] **Step 3: Write the test for MockRunner**

Create `internal/runner/mock_test.go`:

```go
package runner

import (
	"context"
	"encoding/json"
	"testing"
)

var _ Runner = (*MockRunner)(nil) // compile-time interface check

func TestMockRunner(t *testing.T) {
	t.Run("returns_configured_response", func(t *testing.T) {
		mock := &MockRunner{
			Responses: map[string]*RunResult{
				"triage": {
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "triage done",
					CostUSD: 0.50,
				},
			},
		}

		result, err := mock.Run(context.Background(), RunOpts{Phase: "triage"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.CostUSD != 0.50 {
			t.Errorf("CostUSD = %v, want 0.50", result.CostUSD)
		}
		if len(mock.Calls) != 1 {
			t.Errorf("Calls = %d, want 1", len(mock.Calls))
		}
		if mock.Calls[0].Phase != "triage" {
			t.Errorf("Calls[0].Phase = %q, want %q", mock.Calls[0].Phase, "triage")
		}
	})

	t.Run("returns_configured_error", func(t *testing.T) {
		mock := &MockRunner{
			Errors: map[string]error{
				"plan": fmt.Errorf("test error"),
			},
		}

		_, err := mock.Run(context.Background(), RunOpts{Phase: "plan"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "test error" {
			t.Errorf("error = %q, want %q", err.Error(), "test error")
		}
	})

	t.Run("errors_on_unconfigured_phase", func(t *testing.T) {
		mock := &MockRunner{}

		_, err := mock.Run(context.Background(), RunOpts{Phase: "unknown"})
		if err == nil {
			t.Fatal("expected error for unconfigured phase")
		}
	})

	t.Run("respects_context_cancellation", func(t *testing.T) {
		mock := &MockRunner{
			Responses: map[string]*RunResult{
				"triage": {Output: json.RawMessage(`{}`)},
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := mock.Run(ctx, RunOpts{Phase: "triage"})
		if err == nil {
			t.Fatal("expected context error")
		}
	})
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/runner/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/runner/runner.go internal/runner/mock.go internal/runner/mock_test.go
git commit -m "feat(runner): add Runner interface and MockRunner for testing"
```

---

### Task 3: Phase config loading from YAML

**Files:**
- Create: `internal/pipeline/phase.go`
- Create: `internal/pipeline/phase_test.go`

- [ ] **Step 1: Write the test for LoadPipeline**

Create `internal/pipeline/phase_test.go`:

```go
package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDurationUnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"minutes", "3m", 3 * time.Minute, false},
		{"hours", "4h", 4 * time.Hour, false},
		{"seconds", "30s", 30 * time.Second, false},
		{"compound", "1h30m", 90 * time.Minute, false},
		{"invalid", "bogus", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dur Duration
			err := dur.UnmarshalYAML(func(v interface{}) error {
				ptr := v.(*string)
				*ptr = tt.input
				return nil
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && dur.Duration != tt.want {
				t.Errorf("Duration = %v, want %v", dur.Duration, tt.want)
			}
		})
	}
}

func TestLoadPipeline(t *testing.T) {
	t.Run("loads_real_phases_yaml", func(t *testing.T) {
		// Use the project's actual phases.yaml
		pipeline, err := LoadPipeline("../../phases.yaml")
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if len(pipeline.Phases) != 6 {
			t.Fatalf("got %d phases, want 6", len(pipeline.Phases))
		}

		// Verify first phase
		triage := pipeline.Phases[0]
		if triage.Name != "triage" {
			t.Errorf("first phase = %q, want %q", triage.Name, "triage")
		}
		if triage.Timeout.Duration != 3*time.Minute {
			t.Errorf("triage timeout = %v, want 3m", triage.Timeout.Duration)
		}
		if triage.Retry.Transient != 2 {
			t.Errorf("triage retry.transient = %d, want 2", triage.Retry.Transient)
		}
		if len(triage.DependsOn) != 0 {
			t.Errorf("triage depends_on = %v, want empty", triage.DependsOn)
		}

		// Verify dependency chain
		plan := pipeline.Phases[1]
		if len(plan.DependsOn) != 1 || plan.DependsOn[0] != "triage" {
			t.Errorf("plan depends_on = %v, want [triage]", plan.DependsOn)
		}

		// Verify monitor phase has polling config
		monitor := pipeline.Phases[5]
		if monitor.Name != "monitor" {
			t.Errorf("last phase = %q, want %q", monitor.Name, "monitor")
		}
		if monitor.Type != "polling" {
			t.Errorf("monitor type = %q, want %q", monitor.Type, "polling")
		}
		if monitor.Polling == nil {
			t.Fatal("monitor polling config should not be nil")
		}
		if monitor.Polling.MaxResponseRounds != 3 {
			t.Errorf("monitor max_response_rounds = %d, want 3", monitor.Polling.MaxResponseRounds)
		}
		if monitor.Polling.MaxDuration.Duration != 4*time.Hour {
			t.Errorf("monitor max_duration = %v, want 4h", monitor.Polling.MaxDuration.Duration)
		}
	})

	t.Run("errors_on_missing_file", func(t *testing.T) {
		_, err := LoadPipeline("/nonexistent/phases.yaml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("errors_on_invalid_yaml", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.yaml")
		os.WriteFile(path, []byte("not: [valid: yaml: {{{"), 0644)

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid yaml")
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/pipeline/ -run TestLoadPipeline -v`
Expected: FAIL (LoadPipeline not defined)

- [ ] **Step 3: Implement phase.go**

Create `internal/pipeline/phase.go`:

```go
package pipeline

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// PhasePipeline holds the ordered list of phases loaded from phases.yaml.
type PhasePipeline struct {
	Phases []PhaseConfig `yaml:"phases"`
}

// PhaseConfig holds the configuration for a single phase.
type PhaseConfig struct {
	Name      string         `yaml:"name"`
	Prompt    string         `yaml:"prompt"`
	Schema    string         `yaml:"schema"`
	Tools     []string       `yaml:"tools"`
	Timeout   Duration       `yaml:"timeout"`
	Type      string         `yaml:"type"`
	Retry     RetryConfig    `yaml:"retry"`
	DependsOn []string       `yaml:"depends_on"`
	Polling   *PollingConfig `yaml:"polling,omitempty"`
}

// RetryConfig holds per-category retry limits.
type RetryConfig struct {
	Transient int `yaml:"transient"`
	Parse     int `yaml:"parse"`
	Semantic  int `yaml:"semantic"`
}

// PollingConfig holds monitor-phase polling parameters.
type PollingConfig struct {
	InitialInterval   Duration `yaml:"initial_interval"`
	MaxInterval       Duration `yaml:"max_interval"`
	EscalateAfter     Duration `yaml:"escalate_after"`
	MaxDuration       Duration `yaml:"max_duration"`
	MaxResponseRounds int      `yaml:"max_response_rounds"`
}

// Duration wraps time.Duration for YAML unmarshaling.
type Duration struct {
	time.Duration
}

// UnmarshalYAML parses a Go duration string (e.g., "3m", "4h").
func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw string
	if err := unmarshal(&raw); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

// LoadPipeline reads and parses a phases.yaml file.
func LoadPipeline(path string) (*PhasePipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pipeline: read phases config %s: %w", path, err)
	}

	var pipeline PhasePipeline
	if err := yaml.Unmarshal(data, &pipeline); err != nil {
		return nil, fmt.Errorf("pipeline: parse phases config %s: %w", path, err)
	}

	if len(pipeline.Phases) == 0 {
		return nil, fmt.Errorf("pipeline: no phases defined in %s", path)
	}

	return &pipeline, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/pipeline/ -run "TestLoadPipeline|TestDuration" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/pipeline/phase.go internal/pipeline/phase_test.go
git commit -m "feat(pipeline): add phase config loading from YAML"
```

---

### Task 4: Pipeline error types

**Files:**
- Create: `internal/pipeline/errors.go`
- Create: `internal/pipeline/errors_test.go`

Note: the existing `events.go` file in `internal/pipeline/` is not the same as `errors.go`. We create a new file.

- [ ] **Step 1: Write tests for error types**

Create `internal/pipeline/errors_test.go`:

```go
package pipeline

import (
	"errors"
	"testing"
)

func TestBudgetExceededError(t *testing.T) {
	err := &BudgetExceededError{Limit: 15.00, Actual: 15.50, Phase: "verify"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	// Should be matchable with errors.As
	var target *BudgetExceededError
	if !errors.As(err, &target) {
		t.Error("errors.As should match BudgetExceededError")
	}
	if target.Phase != "verify" {
		t.Errorf("Phase = %q, want %q", target.Phase, "verify")
	}
}

func TestDependencyNotMetError(t *testing.T) {
	err := &DependencyNotMetError{Phase: "implement", Dependency: "plan"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	var target *DependencyNotMetError
	if !errors.As(err, &target) {
		t.Error("errors.As should match DependencyNotMetError")
	}
}

func TestPhaseGateError(t *testing.T) {
	err := &PhaseGateError{Phase: "triage", Reason: "not automatable"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	var target *PhaseGateError
	if !errors.As(err, &target) {
		t.Error("errors.As should match PhaseGateError")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/pipeline/ -run "TestBudget|TestDependency|TestPhaseGate" -v`
Expected: FAIL

- [ ] **Step 3: Implement errors.go**

Create `internal/pipeline/errors.go`:

```go
package pipeline

import "fmt"

// BudgetExceededError is returned when accumulated cost exceeds the configured limit.
type BudgetExceededError struct {
	Limit  float64
	Actual float64
	Phase  string
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("pipeline: budget exceeded in phase %s: limit $%.2f, actual $%.2f",
		e.Phase, e.Limit, e.Actual)
}

// DependencyNotMetError is returned when a phase's prerequisite has not completed.
type DependencyNotMetError struct {
	Phase      string
	Dependency string
}

func (e *DependencyNotMetError) Error() string {
	return fmt.Sprintf("pipeline: phase %s requires %s to be completed",
		e.Phase, e.Dependency)
}

// PhaseGateError is returned when domain gating fails after a phase succeeds.
type PhaseGateError struct {
	Phase  string
	Reason string
}

func (e *PhaseGateError) Error() string {
	return fmt.Sprintf("pipeline: phase %s gated: %s", e.Phase, e.Reason)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/pipeline/ -run "TestBudget|TestDependency|TestPhaseGate" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/pipeline/errors.go internal/pipeline/errors_test.go
git commit -m "feat(pipeline): add engine error types"
```

---

### Task 5: Event kind constants

**Files:**
- Modify: `internal/pipeline/events.go`

- [ ] **Step 1: Add event kind constants**

Add after the `Event` struct definition in `internal/pipeline/events.go`:

```go
// Event kinds emitted by the engine.
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

- [ ] **Step 2: Run existing tests to ensure nothing breaks**

Run: `go test ./internal/pipeline/ -v`
Expected: All existing tests PASS

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/events.go
git commit -m "feat(pipeline): add event kind constants for engine"
```

---

### Task 6: Git worktree management

**Files:**
- Create: `internal/git/worktree.go`
- Create: `internal/git/worktree_test.go`

- [ ] **Step 1: Write the test**

Create `internal/git/worktree_test.go`:

```go
package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepo initializes a bare-minimum git repo with one commit.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	commands := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %s: %v", args, out, err)
		}
	}
	return dir
}

func TestCreateWorktree(t *testing.T) {
	t.Run("creates_worktree_and_branch", func(t *testing.T) {
		repoDir := initGitRepo(t)
		worktreeBase := filepath.Join(repoDir, ".worktrees")

		path, err := CreateWorktree(context.Background(), repoDir, worktreeBase, "feat/test-123", "main")
		if err != nil {
			t.Fatalf("CreateWorktree: %v", err)
		}

		// Verify the worktree directory exists
		if _, err := os.Stat(path); err != nil {
			t.Errorf("worktree dir should exist: %v", err)
		}

		// Verify it's a valid git worktree (has .git file, not directory)
		gitFile := filepath.Join(path, ".git")
		info, err := os.Stat(gitFile)
		if err != nil {
			t.Fatalf(".git should exist in worktree: %v", err)
		}
		if info.IsDir() {
			t.Error(".git should be a file in worktree, not a directory")
		}

		// Verify the branch was created
		cmd := exec.Command("git", "branch", "--list", "feat/test-123")
		cmd.Dir = repoDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git branch: %v", err)
		}
		if len(out) == 0 {
			t.Error("branch feat/test-123 should exist")
		}
	})

	t.Run("returns_existing_worktree_path", func(t *testing.T) {
		repoDir := initGitRepo(t)
		worktreeBase := filepath.Join(repoDir, ".worktrees")

		path1, err := CreateWorktree(context.Background(), repoDir, worktreeBase, "feat/dup", "main")
		if err != nil {
			t.Fatalf("first CreateWorktree: %v", err)
		}

		path2, err := CreateWorktree(context.Background(), repoDir, worktreeBase, "feat/dup", "main")
		if err != nil {
			t.Fatalf("second CreateWorktree: %v", err)
		}

		if path1 != path2 {
			t.Errorf("paths differ: %q vs %q", path1, path2)
		}
	})

	t.Run("respects_context_cancellation", func(t *testing.T) {
		repoDir := initGitRepo(t)
		worktreeBase := filepath.Join(repoDir, ".worktrees")

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := CreateWorktree(ctx, repoDir, worktreeBase, "feat/cancel", "main")
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/git/ -v`
Expected: FAIL (package does not exist)

- [ ] **Step 3: Implement worktree.go**

Create `internal/git/worktree.go`:

```go
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CreateWorktree creates a git worktree for a new branch based on baseBranch.
// Returns the absolute path to the worktree directory.
// If the worktree already exists at the expected path, returns that path.
func CreateWorktree(ctx context.Context, repoDir, worktreeBase, branch, baseBranch string) (string, error) {
	worktreePath := filepath.Join(worktreeBase, branch)

	// If worktree already exists, return its path
	if _, err := os.Stat(filepath.Join(worktreePath, ".git")); err == nil {
		absPath, err := filepath.Abs(worktreePath)
		if err != nil {
			return "", fmt.Errorf("git: resolve worktree path: %w", err)
		}
		return absPath, nil
	}

	// Create parent directory
	if err := os.MkdirAll(worktreeBase, 0755); err != nil {
		return "", fmt.Errorf("git: create worktree base %s: %w", worktreeBase, err)
	}

	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branch, worktreePath, baseBranch)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git: worktree add: %s: %w", output, err)
	}

	absPath, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", fmt.Errorf("git: resolve worktree path: %w", err)
	}

	return absPath, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/git/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/git/worktree.go internal/git/worktree_test.go
git commit -m "feat(git): add worktree creation for pipeline phases"
```

---

### Task 7: Prompt loader and renderer

**Files:**
- Create: `internal/pipeline/prompt.go`
- Create: `internal/pipeline/prompt_test.go`

- [ ] **Step 1: Write the test**

Create `internal/pipeline/prompt_test.go`:

```go
package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptLoader(t *testing.T) {
	t.Run("loads_from_first_directory", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "triage.md"), []byte("triage prompt"), 0644)

		loader := NewPromptLoader(dir)
		content, err := loader.Load("triage.md")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if content != "triage prompt" {
			t.Errorf("content = %q, want %q", content, "triage prompt")
		}
	})

	t.Run("prefers_first_directory_override", func(t *testing.T) {
		override := t.TempDir()
		builtin := t.TempDir()
		os.WriteFile(filepath.Join(override, "plan.md"), []byte("custom plan"), 0644)
		os.WriteFile(filepath.Join(builtin, "plan.md"), []byte("default plan"), 0644)

		loader := NewPromptLoader(override, builtin)
		content, err := loader.Load("plan.md")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if content != "custom plan" {
			t.Errorf("content = %q, want %q", content, "custom plan")
		}
	})

	t.Run("falls_back_to_second_directory", func(t *testing.T) {
		override := t.TempDir() // empty
		builtin := t.TempDir()
		os.WriteFile(filepath.Join(builtin, "verify.md"), []byte("builtin verify"), 0644)

		loader := NewPromptLoader(override, builtin)
		content, err := loader.Load("verify.md")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if content != "builtin verify" {
			t.Errorf("content = %q, want %q", content, "builtin verify")
		}
	})

	t.Run("errors_on_not_found", func(t *testing.T) {
		loader := NewPromptLoader(t.TempDir())
		_, err := loader.Load("nonexistent.md")
		if err == nil {
			t.Fatal("expected error for missing template")
		}
	})

	t.Run("rejects_path_traversal", func(t *testing.T) {
		dir := t.TempDir()
		loader := NewPromptLoader(dir)
		_, err := loader.Load("../../../etc/passwd")
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
	})
}

func TestRenderPrompt(t *testing.T) {
	t.Run("renders_template_with_data", func(t *testing.T) {
		tmpl := "Key: {{.Ticket.Key}}\nSummary: {{.Ticket.Summary}}"
		data := PromptData{
			Ticket: TicketData{
				Key:     "PROJ-42",
				Summary: "Fix the thing",
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "PROJ-42") {
			t.Errorf("result should contain ticket key, got: %s", result)
		}
		if !strings.Contains(result, "Fix the thing") {
			t.Errorf("result should contain summary, got: %s", result)
		}
	})

	t.Run("renders_artifacts", func(t *testing.T) {
		tmpl := "Plan:\n{{.Artifacts.Plan}}"
		data := PromptData{
			Artifacts: ArtifactData{
				Plan: "Step 1: do the thing",
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "Step 1: do the thing") {
			t.Errorf("result should contain plan artifact, got: %s", result)
		}
	})

	t.Run("renders_submit_artifact_prurl", func(t *testing.T) {
		tmpl := "URL: {{.Artifacts.Submit.PRURL}}"
		data := PromptData{
			Artifacts: ArtifactData{
				Submit: SubmitArtifact{PRURL: "https://github.com/org/repo/pull/1"},
			},
		}

		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "https://github.com/org/repo/pull/1") {
			t.Errorf("result should contain PR URL, got: %s", result)
		}
	})

	t.Run("errors_on_invalid_template", func(t *testing.T) {
		_, err := RenderPrompt("{{.Invalid}", PromptData{})
		if err == nil {
			t.Fatal("expected error for invalid template syntax")
		}
	})

	t.Run("renders_conditional_sections", func(t *testing.T) {
		tmpl := `{{- if .Context.Gotchas}}Gotchas: {{.Context.Gotchas}}{{- end}}`
		data := PromptData{
			Context: ContextData{Gotchas: "watch out"},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "watch out") {
			t.Errorf("result should contain gotchas, got: %s", result)
		}

		// With empty gotchas, section should be omitted
		data.Context.Gotchas = ""
		result, err = RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if strings.Contains(result, "Gotchas") {
			t.Errorf("result should omit empty gotchas section, got: %s", result)
		}
	})

	t.Run("renders_range_over_criteria", func(t *testing.T) {
		tmpl := `{{range .Ticket.AcceptanceCriteria}}- {{.}}
{{end}}`
		data := PromptData{
			Ticket: TicketData{
				AcceptanceCriteria: []string{"AC1", "AC2"},
			},
		}
		result, err := RenderPrompt(tmpl, data)
		if err != nil {
			t.Fatalf("RenderPrompt: %v", err)
		}
		if !strings.Contains(result, "- AC1") || !strings.Contains(result, "- AC2") {
			t.Errorf("result should list criteria, got: %s", result)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/pipeline/ -run "TestPrompt|TestRender" -v`
Expected: FAIL

- [ ] **Step 3: Implement prompt.go**

Create `internal/pipeline/prompt.go`:

```go
package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// PromptData is the template context for phase prompts.
// This is a plain data struct with no methods to prevent
// side-effecting calls from templates.
type PromptData struct {
	Ticket         TicketData
	Config         PromptConfigData
	Artifacts      ArtifactData
	Context        ContextData
	WorktreePath   string
	Branch         string
	BaseBranch     string
	ReviewComments string
}

// TicketData holds ticket fields for prompt templates.
// Decoupled from ticket.Ticket to keep pipeline independent of ticket package.
type TicketData struct {
	Key                string
	Summary            string
	Description        string
	Type               string
	Priority           string
	AcceptanceCriteria []string
}

// PromptConfigData holds config fields accessible from templates.
type PromptConfigData struct {
	Repos          []RepoConfig
	Repo           RepoConfig
	Formatter      string
	TestCommand    string
	VerifyCommands []string
}

// RepoConfig holds per-repo configuration for prompts.
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
type ArtifactData struct {
	Triage    string
	Plan      string
	Implement string
	Verify    string
	Submit    SubmitArtifact
}

// SubmitArtifact holds parsed fields from the submit phase output.
type SubmitArtifact struct {
	PRURL string
}

// ContextData holds injected context content for prompts.
type ContextData struct {
	ProjectContext  string
	RepoConventions string
	Gotchas         string
}

// PromptLoader resolves prompt templates from the filesystem.
// Directories are searched in order; the first match wins.
type PromptLoader struct {
	dirs []string
}

// NewPromptLoader creates a loader that searches the given directories in order.
func NewPromptLoader(dirs ...string) *PromptLoader {
	return &PromptLoader{dirs: dirs}
}

// Load returns the template content for the given filename.
// Searches directories in order, returning the first match.
func (loader *PromptLoader) Load(name string) (string, error) {
	// Reject path traversal attempts
	cleaned := filepath.Clean(name)
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("prompt: path traversal rejected: %s", name)
	}

	for _, dir := range loader.dirs {
		path := filepath.Join(dir, cleaned)

		// Verify resolved path stays within the directory
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) && absPath != absDir {
			return "", fmt.Errorf("prompt: path traversal rejected: %s", name)
		}

		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("prompt: read %s: %w", path, err)
		}
		return string(data), nil
	}

	return "", fmt.Errorf("prompt: %s not found in %v", name, loader.dirs)
}

// RenderPrompt executes a Go text/template against the given data.
func RenderPrompt(tmpl string, data PromptData) (string, error) {
	parsed, err := template.New("prompt").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("prompt: parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("prompt: render template: %w", err)
	}

	return buf.String(), nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/pipeline/ -run "TestPrompt|TestRender" -v`
Expected: PASS

- [ ] **Step 5: Run all pipeline tests**

Run: `go test ./internal/pipeline/ -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/pipeline/prompt.go internal/pipeline/prompt_test.go
git commit -m "feat(pipeline): add prompt loader and renderer"
```

---

### Task 8: Engine core — Run, runPhase, retry, budget, checkpoint

**Files:**
- Create: `internal/pipeline/engine.go`
- Create: `internal/pipeline/engine_test.go`

This is the largest task. The engine ties everything together.

- [ ] **Step 1: Write the engine test file with the happy path test**

Create `internal/pipeline/engine_test.go`:

```go
package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/runner"
)

// minimalPipeline returns a pipeline with two phases for testing.
func minimalPipeline(promptDir string) *PhasePipeline {
	return &PhasePipeline{
		Phases: []PhaseConfig{
			{
				Name:      "triage",
				Prompt:    "triage.md",
				Timeout:   Duration{3 * time.Minute},
				Retry:     RetryConfig{Transient: 2, Parse: 1, Semantic: 1},
				DependsOn: []string{},
			},
			{
				Name:      "plan",
				Prompt:    "plan.md",
				Timeout:   Duration{5 * time.Minute},
				Retry:     RetryConfig{Transient: 2, Parse: 1, Semantic: 1},
				DependsOn: []string{"triage"},
			},
		},
	}
}

// setupEngine creates an engine with a MockRunner and temp state dir.
func setupEngine(t *testing.T, mock *runner.MockRunner, pipeline *PhasePipeline, opts ...func(*EngineConfig)) (*Engine, *State) {
	t.Helper()

	stateDir := t.TempDir()
	promptDir := t.TempDir()

	// Write minimal prompt templates
	os.WriteFile(filepath.Join(promptDir, "triage.md"), []byte("Triage: {{.Ticket.Key}}"), 0644)
	os.WriteFile(filepath.Join(promptDir, "plan.md"), []byte("Plan: {{.Ticket.Key}}\n{{.Artifacts.Triage}}"), 0644)
	os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte("Implement: {{.Ticket.Key}}"), 0644)
	os.WriteFile(filepath.Join(promptDir, "verify.md"), []byte("Verify: {{.Ticket.Key}}"), 0644)
	os.WriteFile(filepath.Join(promptDir, "submit.md"), []byte("Submit: {{.Ticket.Key}}"), 0644)
	os.WriteFile(filepath.Join(promptDir, "monitor.md"), []byte("Monitor: {{.Ticket.Key}}"), 0644)

	state, err := LoadOrCreate(stateDir, "TEST-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	config := EngineConfig{
		Pipeline: pipeline,
		Loader:   NewPromptLoader(promptDir),
		Ticket: TicketData{
			Key:     "TEST-1",
			Summary: "Test ticket",
		},
		Model:      "test-model",
		WorkDir:    t.TempDir(),
		BaseBranch: "main",
		MaxCostUSD: 15.00,
		Mode:       Autonomous,
		SleepFunc:  func(d time.Duration) {}, // no-op for tests
		JitterFunc: func(max time.Duration) time.Duration { return 0 },
	}

	for _, fn := range opts {
		fn(&config)
	}

	eng := NewEngine(mock, state, config)
	return eng, state
}

func TestEngineRun(t *testing.T) {
	t.Run("happy_path_all_phases_complete", func(t *testing.T) {
		triageOutput := json.RawMessage(`{"ticket_key":"TEST-1","automatable":true,"complexity":"small"}`)
		planOutput := json.RawMessage(`{"ticket_key":"TEST-1","tasks":[{"id":"T1","description":"do it","files":["f.go"],"done_when":"done"}]}`)

		mock := &runner.MockRunner{
			Responses: map[string]*runner.RunResult{
				"triage": {Output: triageOutput, RawText: "triage done", CostUSD: 0.50},
				"plan":   {Output: planOutput, RawText: "plan done", CostUSD: 0.75},
			},
		}

		promptDir := t.TempDir()
		eng, state := setupEngine(t, mock, minimalPipeline(promptDir))

		var events []Event
		eng.config.OnEvent = func(evt Event) {
			events = append(events, evt)
		}

		err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		// Both phases should be completed
		if !state.IsCompleted("triage") {
			t.Error("triage should be completed")
		}
		if !state.IsCompleted("plan") {
			t.Error("plan should be completed")
		}

		// Cost should be accumulated
		if state.Meta().TotalCost < 1.25 {
			t.Errorf("TotalCost = %.2f, want >= 1.25", state.Meta().TotalCost)
		}

		// Runner should have been called twice
		if len(mock.Calls) != 2 {
			t.Errorf("Calls = %d, want 2", len(mock.Calls))
		}
		if mock.Calls[0].Phase != "triage" {
			t.Errorf("first call phase = %q, want triage", mock.Calls[0].Phase)
		}
		if mock.Calls[1].Phase != "plan" {
			t.Errorf("second call phase = %q, want plan", mock.Calls[1].Phase)
		}

		// Events should include engine_started, phase_started/completed for each, engine_completed
		hasEvent := func(kind string) bool {
			for _, evt := range events {
				if evt.Kind == kind {
					return true
				}
			}
			return false
		}
		if !hasEvent(EventEngineStarted) {
			t.Error("missing engine_started event")
		}
		if !hasEvent(EventEngineCompleted) {
			t.Error("missing engine_completed event")
		}
	})

	t.Run("skips_completed_phases", func(t *testing.T) {
		mock := &runner.MockRunner{
			Responses: map[string]*runner.RunResult{
				"plan": {Output: json.RawMessage(`{"tasks":[{"id":"T1","description":"d","files":["f"],"done_when":"d"}]}`), RawText: "plan", CostUSD: 0.50},
			},
		}

		promptDir := t.TempDir()
		eng, state := setupEngine(t, mock, minimalPipeline(promptDir))

		// Pre-complete triage
		state.MarkRunning("triage")
		state.WriteResult("triage", json.RawMessage(`{"automatable":true}`))
		state.WriteArtifact("triage", []byte("triage done"))
		state.MarkCompleted("triage")

		err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		// Only plan should have been called
		if len(mock.Calls) != 1 {
			t.Errorf("Calls = %d, want 1 (triage skipped)", len(mock.Calls))
		}
		if mock.Calls[0].Phase != "plan" {
			t.Errorf("call phase = %q, want plan", mock.Calls[0].Phase)
		}
	})

	t.Run("dependency_not_met", func(t *testing.T) {
		// Pipeline where plan depends on triage, but triage has no mock response
		// and is not pre-completed. However, the engine runs phases in order,
		// so triage would run first. Let's make triage fail to test that plan
		// doesn't run when triage failed.
		mock := &runner.MockRunner{
			Errors: map[string]error{
				"triage": &claude.SemanticError{Message: "cannot triage"},
			},
		}

		promptDir := t.TempDir()
		pipeline := &PhasePipeline{
			Phases: []PhaseConfig{
				{
					Name:    "triage",
					Prompt:  "triage.md",
					Timeout: Duration{3 * time.Minute},
					Retry:   RetryConfig{Transient: 0, Parse: 0, Semantic: 0},
				},
				{
					Name:      "plan",
					Prompt:    "plan.md",
					Timeout:   Duration{5 * time.Minute},
					DependsOn: []string{"triage"},
				},
			},
		}

		eng, _ := setupEngine(t, mock, pipeline)

		err := eng.Run(context.Background())
		if err == nil {
			t.Fatal("expected error when triage fails")
		}
		// Plan should not have been called
		if len(mock.Calls) != 1 {
			t.Errorf("Calls = %d, want 1 (only triage attempted)", len(mock.Calls))
		}
	})

	t.Run("transient_retry_with_backoff", func(t *testing.T) {
		callCount := 0
		transientErr := &claude.TransientError{
			Reason: "rate_limit",
			Err:    fmt.Errorf("429 too many requests"),
		}

		mock := &runner.MockRunner{
			Responses: map[string]*runner.RunResult{
				"triage": {Output: json.RawMessage(`{"automatable":true}`), RawText: "ok", CostUSD: 0.25},
			},
			Errors: map[string]error{
				"triage": transientErr,
			},
		}

		// Override: fail twice, then succeed
		originalRun := mock.Run
		_ = originalRun
		// We need a more flexible mock for this test.
		// Use a custom runner that fails N times then succeeds.
		flexRunner := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {
					{err: transientErr},
					{err: transientErr},
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "ok",
						CostUSD: 0.25,
					}},
				},
			},
		}

		var sleepDurations []time.Duration
		promptDir := t.TempDir()
		pipeline := &PhasePipeline{
			Phases: []PhaseConfig{
				{
					Name:    "triage",
					Prompt:  "triage.md",
					Timeout: Duration{3 * time.Minute},
					Retry:   RetryConfig{Transient: 2, Parse: 0, Semantic: 0},
				},
			},
		}

		eng, state := setupEngine(t, nil, pipeline, func(cfg *EngineConfig) {
			cfg.SleepFunc = func(d time.Duration) {
				sleepDurations = append(sleepDurations, d)
			}
		})
		eng.runner = flexRunner
		_ = callCount

		err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		if !state.IsCompleted("triage") {
			t.Error("triage should be completed after retries")
		}

		// Should have slept twice (before retry 1 and retry 2)
		if len(sleepDurations) != 2 {
			t.Errorf("sleep calls = %d, want 2", len(sleepDurations))
		}

		// Second sleep should be >= first (exponential backoff)
		if len(sleepDurations) == 2 && sleepDurations[1] < sleepDurations[0] {
			t.Errorf("backoff not increasing: %v then %v", sleepDurations[0], sleepDurations[1])
		}
	})

	t.Run("parse_retry_appends_error", func(t *testing.T) {
		parseErr := &claude.ParseError{
			Raw: []byte("not json"),
			Err: fmt.Errorf("invalid character 'n'"),
		}

		flexRunner := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {
					{err: parseErr},
					{result: &runner.RunResult{
						Output:  json.RawMessage(`{"automatable":true}`),
						RawText: "ok",
						CostUSD: 0.25,
					}},
				},
			},
		}

		promptDir := t.TempDir()
		pipeline := &PhasePipeline{
			Phases: []PhaseConfig{
				{
					Name:    "triage",
					Prompt:  "triage.md",
					Timeout: Duration{3 * time.Minute},
					Retry:   RetryConfig{Transient: 0, Parse: 1, Semantic: 0},
				},
			},
		}

		eng, _ := setupEngine(t, nil, pipeline)
		eng.runner = flexRunner

		err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		// The retry call should have error context in the prompt
		if len(flexRunner.calls) < 2 {
			t.Fatalf("expected at least 2 calls, got %d", len(flexRunner.calls))
		}
		retryPrompt := flexRunner.calls[1].UserPrompt
		if !strings.Contains(retryPrompt, "could not be parsed") {
			t.Errorf("retry prompt should contain parse error context, got: %s", retryPrompt)
		}
	})

	t.Run("max_retries_exhausted", func(t *testing.T) {
		transientErr := &claude.TransientError{
			Reason: "timeout",
			Err:    fmt.Errorf("timed out"),
		}

		flexRunner := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {
					{err: transientErr},
					{err: transientErr},
					{err: transientErr}, // 3rd attempt, all retries consumed
				},
			},
		}

		promptDir := t.TempDir()
		pipeline := &PhasePipeline{
			Phases: []PhaseConfig{
				{
					Name:    "triage",
					Prompt:  "triage.md",
					Timeout: Duration{3 * time.Minute},
					Retry:   RetryConfig{Transient: 2, Parse: 0, Semantic: 0},
				},
			},
		}

		eng, state := setupEngine(t, nil, pipeline)
		eng.runner = flexRunner

		err := eng.Run(context.Background())
		if err == nil {
			t.Fatal("expected error after exhausting retries")
		}

		// Phase should be marked failed
		ps := state.Meta().Phases["triage"]
		if ps == nil || ps.Status != PhaseFailed {
			t.Errorf("triage status = %v, want failed", ps)
		}
	})

	t.Run("budget_exceeded", func(t *testing.T) {
		mock := &runner.MockRunner{
			Responses: map[string]*runner.RunResult{
				"triage": {Output: json.RawMessage(`{"automatable":true}`), RawText: "ok", CostUSD: 14.00},
				"plan":   {Output: json.RawMessage(`{"tasks":[{"id":"T1","description":"d","files":["f"],"done_when":"d"}]}`), RawText: "ok", CostUSD: 5.00},
			},
		}

		promptDir := t.TempDir()
		eng, _ := setupEngine(t, mock, minimalPipeline(promptDir), func(cfg *EngineConfig) {
			cfg.MaxCostUSD = 15.00
		})

		err := eng.Run(context.Background())
		if err == nil {
			t.Fatal("expected budget exceeded error")
		}

		var budgetErr *BudgetExceededError
		if !errors.As(err, &budgetErr) {
			t.Errorf("expected BudgetExceededError, got %T: %v", err, err)
		}
	})

	t.Run("checkpoint_mode", func(t *testing.T) {
		mock := &runner.MockRunner{
			Responses: map[string]*runner.RunResult{
				"triage": {Output: json.RawMessage(`{"automatable":true}`), RawText: "ok", CostUSD: 0.25},
				"plan":   {Output: json.RawMessage(`{"tasks":[{"id":"T1","description":"d","files":["f"],"done_when":"d"}]}`), RawText: "ok", CostUSD: 0.25},
			},
		}

		promptDir := t.TempDir()
		eng, _ := setupEngine(t, mock, minimalPipeline(promptDir), func(cfg *EngineConfig) {
			cfg.Mode = Checkpoint
		})

		var events []Event
		eng.config.OnEvent = func(evt Event) {
			events = append(events, evt)
		}

		// Auto-confirm from a goroutine
		go func() {
			for range 2 {
				eng.Confirm()
			}
		}()

		err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		// Should have checkpoint_pause events
		pauseCount := 0
		for _, evt := range events {
			if evt.Kind == EventCheckpointPause {
				pauseCount++
			}
		}
		if pauseCount != 2 {
			t.Errorf("checkpoint_pause events = %d, want 2", pauseCount)
		}
	})

	t.Run("context_cancellation", func(t *testing.T) {
		mock := &runner.MockRunner{
			Responses: map[string]*runner.RunResult{
				"triage": {Output: json.RawMessage(`{"automatable":true}`), RawText: "ok", CostUSD: 0.25},
			},
		}

		promptDir := t.TempDir()
		eng, _ := setupEngine(t, mock, minimalPipeline(promptDir))

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		err := eng.Run(ctx)
		if err == nil {
			t.Fatal("expected context error")
		}
	})

	t.Run("monitor_stub", func(t *testing.T) {
		mock := &runner.MockRunner{}

		promptDir := t.TempDir()
		pipeline := &PhasePipeline{
			Phases: []PhaseConfig{
				{
					Name:    "monitor",
					Prompt:  "monitor.md",
					Type:    "polling",
					Timeout: Duration{4 * time.Hour},
				},
			},
		}

		eng, state := setupEngine(t, mock, pipeline)

		// Pre-write submit result so monitor can extract PRURL
		state.WriteResult("submit", json.RawMessage(`{"pr_url":"https://github.com/org/repo/pull/1"}`))

		var events []Event
		eng.config.OnEvent = func(evt Event) {
			events = append(events, evt)
		}

		err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		if !state.IsCompleted("monitor") {
			t.Error("monitor should be completed (stub)")
		}

		hasSkipped := false
		for _, evt := range events {
			if evt.Kind == EventMonitorSkipped {
				hasSkipped = true
			}
		}
		if !hasSkipped {
			t.Error("missing monitor_skipped event")
		}

		// Runner should NOT have been called for monitor
		if len(mock.Calls) != 0 {
			t.Errorf("runner calls = %d, want 0 (monitor is stubbed)", len(mock.Calls))
		}
	})
}

// flexMockRunner allows per-call responses for retry testing.
type flexMockRunner struct {
	responses map[string][]flexResponse
	calls     []runner.RunOpts
	counters  map[string]int
}

type flexResponse struct {
	result *runner.RunResult
	err    error
}

func (f *flexMockRunner) Run(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
	f.calls = append(f.calls, opts)
	if f.counters == nil {
		f.counters = make(map[string]int)
	}
	idx := f.counters[opts.Phase]
	f.counters[opts.Phase]++

	resps, ok := f.responses[opts.Phase]
	if !ok || idx >= len(resps) {
		return nil, fmt.Errorf("flexmock: no response for phase %q call %d", opts.Phase, idx)
	}
	resp := resps[idx]
	return resp.result, resp.err
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/pipeline/ -run TestEngineRun -v`
Expected: FAIL (Engine not defined)

- [ ] **Step 3: Implement engine.go**

Create `internal/pipeline/engine.go`:

```go
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/git"
	"github.com/decko/soda/internal/runner"
)

// Mode determines how the engine runs between phases.
type Mode int

const (
	// Autonomous runs all phases without pausing.
	Autonomous Mode = iota
	// Checkpoint pauses between phases for user confirmation.
	Checkpoint
)

// EngineConfig holds all configuration for the engine.
type EngineConfig struct {
	Pipeline     *PhasePipeline
	Loader       *PromptLoader
	Ticket       TicketData
	Model        string
	WorkDir      string
	WorktreeBase string
	BaseBranch   string
	MaxCostUSD   float64
	Mode         Mode
	OnEvent      func(Event)
	SleepFunc    func(time.Duration)
	JitterFunc   func(max time.Duration) time.Duration
}

// Engine orchestrates the sequential execution of pipeline phases.
type Engine struct {
	runner    runner.Runner
	config    EngineConfig
	state     *State
	confirmCh chan struct{}
}

// NewEngine creates an engine ready to run.
func NewEngine(r runner.Runner, state *State, config EngineConfig) *Engine {
	if config.SleepFunc == nil {
		config.SleepFunc = time.Sleep
	}
	if config.JitterFunc == nil {
		config.JitterFunc = func(max time.Duration) time.Duration {
			return 0
		}
	}

	eng := &Engine{
		runner: r,
		config: config,
		state:  state,
	}

	if config.Mode == Checkpoint {
		eng.confirmCh = make(chan struct{}, 1)
	}

	return eng
}

// Confirm unblocks the engine from a checkpoint pause.
func (eng *Engine) Confirm() {
	if eng.confirmCh != nil {
		eng.confirmCh <- struct{}{}
	}
}

// Run executes all phases sequentially, skipping completed ones.
func (eng *Engine) Run(ctx context.Context) error {
	if err := eng.state.AcquireLock(); err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}
	defer eng.state.ReleaseLock()

	eng.emit(Event{Kind: EventEngineStarted, Data: map[string]any{
		"ticket": eng.config.Ticket.Key,
		"mode":   eng.config.Mode,
	}})

	for _, phase := range eng.config.Pipeline.Phases {
		if err := ctx.Err(); err != nil {
			return err
		}

		if eng.state.IsCompleted(phase.Name) {
			eng.emit(Event{Phase: phase.Name, Kind: EventPhaseSkipped})
			continue
		}

		if err := eng.runPhase(ctx, phase); err != nil {
			eng.emit(Event{Kind: EventEngineFailed, Data: map[string]any{
				"phase": phase.Name,
				"error": err.Error(),
			}})
			return err
		}

		if eng.config.Mode == Checkpoint {
			eng.emit(Event{Phase: phase.Name, Kind: EventCheckpointPause})
			select {
			case <-eng.confirmCh:
				// proceed
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	eng.emit(Event{Kind: EventEngineCompleted, Data: map[string]any{
		"total_cost": eng.state.Meta().TotalCost,
	}})

	return nil
}

// Resume restarts execution from a specific phase, re-running it
// even if it was previously completed.
func (eng *Engine) Resume(ctx context.Context, fromPhase string) error {
	// Find the phase index
	startIdx := -1
	for idx, phase := range eng.config.Pipeline.Phases {
		if phase.Name == fromPhase {
			startIdx = idx
			break
		}
	}
	if startIdx < 0 {
		return fmt.Errorf("pipeline: unknown phase %q", fromPhase)
	}

	if err := eng.state.AcquireLock(); err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}
	defer eng.state.ReleaseLock()

	eng.emit(Event{Kind: EventEngineStarted, Data: map[string]any{
		"ticket":     eng.config.Ticket.Key,
		"from_phase": fromPhase,
	}})

	for _, phase := range eng.config.Pipeline.Phases[startIdx:] {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := eng.runPhase(ctx, phase); err != nil {
			eng.emit(Event{Kind: EventEngineFailed, Data: map[string]any{
				"phase": phase.Name,
				"error": err.Error(),
			}})
			return err
		}

		if eng.config.Mode == Checkpoint {
			eng.emit(Event{Phase: phase.Name, Kind: EventCheckpointPause})
			select {
			case <-eng.confirmCh:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	eng.emit(Event{Kind: EventEngineCompleted})
	return nil
}

// runPhase orchestrates a single phase.
func (eng *Engine) runPhase(ctx context.Context, phase PhaseConfig) error {
	// Monitor phase stub
	if phase.Type == "polling" {
		return eng.runMonitorStub(phase)
	}

	// Check dependencies
	for _, dep := range phase.DependsOn {
		if !eng.state.IsCompleted(dep) {
			return &DependencyNotMetError{Phase: phase.Name, Dependency: dep}
		}
	}

	// Check budget
	if err := eng.checkBudget(phase.Name); err != nil {
		return err
	}

	// Create worktree at implement phase if needed
	if phase.Name == "implement" && eng.state.Meta().Worktree == "" && eng.config.WorktreeBase != "" {
		branch := fmt.Sprintf("feat/%s", strings.ToLower(eng.config.Ticket.Key))
		worktreePath, err := git.CreateWorktree(ctx, eng.config.WorkDir, eng.config.WorktreeBase, branch, eng.config.BaseBranch)
		if err != nil {
			return fmt.Errorf("pipeline: create worktree for %s: %w", phase.Name, err)
		}
		eng.state.Meta().Worktree = worktreePath
		eng.state.Meta().Branch = branch
		eng.emit(Event{Phase: phase.Name, Kind: EventWorktreeCreated, Data: map[string]any{
			"worktree": worktreePath,
			"branch":   branch,
		}})
	}

	// Mark running
	if err := eng.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("pipeline: mark running %s: %w", phase.Name, err)
	}

	// Build prompt data
	promptData, err := eng.buildPromptData(phase)
	if err != nil {
		eng.state.MarkFailed(phase.Name, err)
		return fmt.Errorf("pipeline: build prompt data for %s: %w", phase.Name, err)
	}

	// Load and render template
	tmplContent, err := eng.config.Loader.Load(phase.Prompt)
	if err != nil {
		eng.state.MarkFailed(phase.Name, err)
		return fmt.Errorf("pipeline: load prompt for %s: %w", phase.Name, err)
	}

	rendered, err := RenderPrompt(tmplContent, promptData)
	if err != nil {
		eng.state.MarkFailed(phase.Name, err)
		return fmt.Errorf("pipeline: render prompt for %s: %w", phase.Name, err)
	}

	// Log the rendered prompt
	eng.state.WriteLog(phase.Name, "prompt", []byte(rendered))

	// Build run opts
	remaining := eng.config.MaxCostUSD - eng.state.Meta().TotalCost
	opts := runner.RunOpts{
		Phase:        phase.Name,
		UserPrompt:   rendered,
		AllowedTools: phase.Tools,
		MaxBudgetUSD: remaining,
		WorkDir:      eng.workDir(phase),
		Model:        eng.config.Model,
		Timeout:      phase.Timeout.Duration,
	}

	// Run with retry
	result, err := eng.runWithRetry(ctx, phase, opts)
	if err != nil {
		eng.state.MarkFailed(phase.Name, err)
		return fmt.Errorf("pipeline: phase %s failed: %w", phase.Name, err)
	}

	// Write result and artifact
	if err := eng.state.WriteResult(phase.Name, result.Output); err != nil {
		eng.state.MarkFailed(phase.Name, err)
		return fmt.Errorf("pipeline: write result for %s: %w", phase.Name, err)
	}

	if err := eng.state.WriteArtifact(phase.Name, []byte(result.RawText)); err != nil {
		eng.state.MarkFailed(phase.Name, err)
		return fmt.Errorf("pipeline: write artifact for %s: %w", phase.Name, err)
	}

	// Accumulate cost
	if err := eng.state.AccumulateCost(phase.Name, result.CostUSD); err != nil {
		return fmt.Errorf("pipeline: accumulate cost for %s: %w", phase.Name, err)
	}

	// Mark completed
	if err := eng.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("pipeline: mark completed %s: %w", phase.Name, err)
	}

	// Domain gating
	if err := eng.gatePhase(phase.Name); err != nil {
		return err
	}

	return nil
}

// runWithRetry executes the runner with classified retry logic.
func (eng *Engine) runWithRetry(ctx context.Context, phase PhaseConfig, opts runner.RunOpts) (*runner.RunResult, error) {
	remaining := RetryConfig{
		Transient: phase.Retry.Transient,
		Parse:     phase.Retry.Parse,
		Semantic:  phase.Retry.Semantic,
	}

	attempt := 0
	for {
		result, err := eng.runner.Run(ctx, opts)
		if err == nil {
			return result, nil
		}

		category := classifyError(err)
		canRetry := false

		switch category {
		case "transient":
			if remaining.Transient > 0 {
				remaining.Transient--
				canRetry = true
				delay := eng.backoff(attempt)
				eng.emit(Event{
					Phase: phase.Name,
					Kind:  EventPhaseRetrying,
					Data: map[string]any{
						"category": category,
						"attempt":  attempt + 1,
						"delay":    delay.String(),
						"error":    err.Error(),
					},
				})
				eng.state.WriteLog(phase.Name, fmt.Sprintf("retry_%d", attempt+1), []byte(err.Error()))
				eng.config.SleepFunc(delay)
			}
		case "parse":
			if remaining.Parse > 0 {
				remaining.Parse--
				canRetry = true
				var parseErr *claude.ParseError
				errors.As(err, &parseErr)
				errMsg := parseErr.Err.Error()
				opts.UserPrompt = opts.UserPrompt + fmt.Sprintf(
					"\n\n[RETRY: Your previous response could not be parsed. Error: %s. "+
						"Respond with ONLY the JSON object matching the schema. No markdown, no commentary.]",
					errMsg,
				)
				eng.emit(Event{
					Phase: phase.Name,
					Kind:  EventPhaseRetrying,
					Data:  map[string]any{"category": category, "attempt": attempt + 1},
				})
				eng.state.WriteLog(phase.Name, fmt.Sprintf("retry_%d", attempt+1), []byte(err.Error()))
			}
		case "semantic":
			if remaining.Semantic > 0 {
				remaining.Semantic--
				canRetry = true
				var semErr *claude.SemanticError
				errors.As(err, &semErr)
				opts.UserPrompt = opts.UserPrompt + fmt.Sprintf(
					"\n\n[RETRY: Your previous response had error: %s. "+
						"Please address the issue and produce valid structured output.]",
					semErr.Message,
				)
				eng.emit(Event{
					Phase: phase.Name,
					Kind:  EventPhaseRetrying,
					Data:  map[string]any{"category": category, "attempt": attempt + 1},
				})
				eng.state.WriteLog(phase.Name, fmt.Sprintf("retry_%d", attempt+1), []byte(err.Error()))
			}
		}

		if !canRetry {
			return nil, err
		}

		attempt++
	}
}

// classifyError determines the error category for retry decisions.
func classifyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "context"
	}
	var transient *claude.TransientError
	if errors.As(err, &transient) {
		return "transient"
	}
	var parseErr *claude.ParseError
	if errors.As(err, &parseErr) {
		return "parse"
	}
	var semantic *claude.SemanticError
	if errors.As(err, &semantic) {
		return "semantic"
	}
	return "unknown"
}

// backoff computes exponential backoff with jitter.
func (eng *Engine) backoff(attempt int) time.Duration {
	base := 2 * time.Second
	maxDelay := 30 * time.Second
	delay := base * time.Duration(1<<uint(attempt))
	if delay > maxDelay {
		delay = maxDelay
	}
	jitter := eng.config.JitterFunc(time.Second)
	return delay + jitter
}

// checkBudget verifies accumulated cost hasn't exceeded the ceiling.
func (eng *Engine) checkBudget(phase string) error {
	if eng.config.MaxCostUSD <= 0 {
		return nil // no budget enforcement
	}
	actual := eng.state.Meta().TotalCost
	if actual >= eng.config.MaxCostUSD {
		return &BudgetExceededError{
			Limit:  eng.config.MaxCostUSD,
			Actual: actual,
			Phase:  phase,
		}
	}
	// Warn at 90%
	if actual >= eng.config.MaxCostUSD*0.9 {
		eng.emit(Event{
			Phase: phase,
			Kind:  EventBudgetWarning,
			Data: map[string]any{
				"total_cost": actual,
				"limit":      eng.config.MaxCostUSD,
			},
		})
	}
	return nil
}

// buildPromptData constructs template data for a phase.
func (eng *Engine) buildPromptData(phase PhaseConfig) (PromptData, error) {
	data := PromptData{
		Ticket:       eng.config.Ticket,
		Config:       PromptConfigData{},
		WorktreePath: eng.state.Meta().Worktree,
		Branch:       eng.state.Meta().Branch,
		BaseBranch:   eng.config.BaseBranch,
	}

	// Load artifacts from declared dependencies
	for _, dep := range phase.DependsOn {
		artifact, err := eng.state.ReadArtifact(dep)
		if err != nil {
			return data, fmt.Errorf("read artifact for dependency %s: %w", dep, err)
		}
		switch dep {
		case "triage":
			data.Artifacts.Triage = string(artifact)
		case "plan":
			data.Artifacts.Plan = string(artifact)
		case "implement":
			data.Artifacts.Implement = string(artifact)
		case "verify":
			data.Artifacts.Verify = string(artifact)
		case "submit":
			data.Artifacts.Submit.PRURL = eng.extractPRURL()
		}
	}

	return data, nil
}

// extractPRURL reads the submit result and extracts the PR URL.
func (eng *Engine) extractPRURL() string {
	result, err := eng.state.ReadResult("submit")
	if err != nil {
		return ""
	}
	var submit struct {
		PRURL string `json:"pr_url"`
	}
	if err := json.Unmarshal(result, &submit); err != nil {
		return ""
	}
	return submit.PRURL
}

// gatePhase performs domain-specific checks after a phase succeeds.
func (eng *Engine) gatePhase(phase string) error {
	result, err := eng.state.ReadResult(phase)
	if err != nil {
		return nil // no result to gate on
	}

	switch phase {
	case "triage":
		var output struct {
			Automatable bool   `json:"automatable"`
			BlockReason string `json:"block_reason"`
		}
		if err := json.Unmarshal(result, &output); err != nil {
			return nil
		}
		if !output.Automatable {
			reason := output.BlockReason
			if reason == "" {
				reason = "triage determined ticket is not automatable"
			}
			return &PhaseGateError{Phase: phase, Reason: reason}
		}

	case "plan":
		var output struct {
			Tasks []json.RawMessage `json:"tasks"`
		}
		if err := json.Unmarshal(result, &output); err != nil {
			return nil
		}
		if len(output.Tasks) == 0 {
			return &PhaseGateError{Phase: phase, Reason: "plan produced no tasks"}
		}

	case "verify":
		var output struct {
			Verdict       string   `json:"verdict"`
			FixesRequired []string `json:"fixes_required"`
		}
		if err := json.Unmarshal(result, &output); err != nil {
			return nil
		}
		if strings.EqualFold(output.Verdict, "FAIL") {
			reason := "verification failed"
			if len(output.FixesRequired) > 0 {
				reason = fmt.Sprintf("verification failed: %s", strings.Join(output.FixesRequired, "; "))
			}
			return &PhaseGateError{Phase: phase, Reason: reason}
		}

	case "submit":
		var output struct {
			PRURL string `json:"pr_url"`
		}
		if err := json.Unmarshal(result, &output); err != nil {
			return nil
		}
		if output.PRURL == "" {
			return &PhaseGateError{Phase: phase, Reason: "no PR URL produced"}
		}
	}

	return nil
}

// runMonitorStub handles the monitor phase as a v1 stub.
func (eng *Engine) runMonitorStub(phase PhaseConfig) error {
	prURL := eng.extractPRURL()

	if err := eng.state.MarkRunning(phase.Name); err != nil {
		return fmt.Errorf("pipeline: mark running %s: %w", phase.Name, err)
	}

	eng.emit(Event{
		Phase: phase.Name,
		Kind:  EventMonitorSkipped,
		Data: map[string]any{
			"reason": "not_implemented",
			"pr_url": prURL,
		},
	})

	if err := eng.state.MarkCompleted(phase.Name); err != nil {
		return fmt.Errorf("pipeline: mark completed %s: %w", phase.Name, err)
	}

	return nil
}

// workDir returns the working directory for a phase.
// Phases after implement use the worktree path if available.
func (eng *Engine) workDir(phase PhaseConfig) string {
	worktree := eng.state.Meta().Worktree
	if worktree != "" {
		return worktree
	}
	return eng.config.WorkDir
}

// emit sends an event to both the callback and disk.
func (eng *Engine) emit(event Event) {
	eng.state.LogEvent(event)
	if eng.config.OnEvent != nil {
		eng.config.OnEvent(event)
	}
}
```

- [ ] **Step 4: Run the engine tests**

Run: `go test ./internal/pipeline/ -run TestEngineRun -v`
Expected: PASS

- [ ] **Step 5: Run ALL tests in the project**

Run: `go test ./... -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/pipeline/engine.go internal/pipeline/engine_test.go
git commit -m "feat(pipeline): add engine with phase loop, retry, budget, and checkpoint"
```

---

### Task 9: Integration test — full pipeline lifecycle

**Files:**
- Modify: `internal/pipeline/engine_test.go` (add integration test)

- [ ] **Step 1: Add the full lifecycle integration test**

Append to `internal/pipeline/engine_test.go`:

```go
func TestEngineFullLifecycle(t *testing.T) {
	// This test exercises a realistic 4-phase pipeline:
	// triage → plan → implement → verify
	// with artifacts flowing between phases.

	triageResult := json.RawMessage(`{
		"ticket_key": "PROJ-99",
		"repo": "my-service",
		"code_area": "internal/auth",
		"files": ["auth.go", "auth_test.go"],
		"complexity": "small",
		"approach": "Add token validation",
		"risks": [],
		"automatable": true
	}`)

	planResult := json.RawMessage(`{
		"ticket_key": "PROJ-99",
		"approach": "Add JWT validation middleware",
		"tasks": [
			{"id": "T1", "description": "Add validator", "files": ["auth.go"], "done_when": "tests pass"}
		],
		"verification": {"commands": ["go test ./..."]}
	}`)

	implementResult := json.RawMessage(`{
		"ticket_key": "PROJ-99",
		"branch": "feat/PROJ-99",
		"commits": [{"hash": "abc123", "message": "feat: add validator", "task_id": "T1"}],
		"files_changed": [{"path": "auth.go", "action": "modified"}],
		"task_results": [{"task_id": "T1", "status": "completed"}],
		"tests_passed": true
	}`)

	verifyResult := json.RawMessage(`{
		"ticket_key": "PROJ-99",
		"verdict": "PASS",
		"criteria_results": [],
		"command_results": [{"command": "go test ./...", "exit_code": 0, "output": "ok", "passed": true}]
	}`)

	flexRunner := &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage":    {{result: &runner.RunResult{Output: triageResult, RawText: "Triage: small, automatable", CostUSD: 0.30}}},
			"plan":      {{result: &runner.RunResult{Output: planResult, RawText: "Plan: 1 task", CostUSD: 0.50}}},
			"implement": {{result: &runner.RunResult{Output: implementResult, RawText: "Implemented T1", CostUSD: 2.00}}},
			"verify":    {{result: &runner.RunResult{Output: verifyResult, RawText: "All checks pass", CostUSD: 0.40}}},
		},
	}

	promptDir := t.TempDir()
	os.WriteFile(filepath.Join(promptDir, "triage.md"), []byte("Triage {{.Ticket.Key}}"), 0644)
	os.WriteFile(filepath.Join(promptDir, "plan.md"), []byte("Plan {{.Ticket.Key}}\n{{.Artifacts.Triage}}"), 0644)
	os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte("Implement {{.Ticket.Key}}\n{{.Artifacts.Plan}}"), 0644)
	os.WriteFile(filepath.Join(promptDir, "verify.md"), []byte("Verify {{.Ticket.Key}}\n{{.Artifacts.Plan}}\n{{.Artifacts.Implement}}"), 0644)

	pipeline := &PhasePipeline{
		Phases: []PhaseConfig{
			{Name: "triage", Prompt: "triage.md", Timeout: Duration{3 * time.Minute}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}},
			{Name: "plan", Prompt: "plan.md", Timeout: Duration{5 * time.Minute}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}, DependsOn: []string{"triage"}},
			{Name: "implement", Prompt: "implement.md", Timeout: Duration{15 * time.Minute}, Retry: RetryConfig{Transient: 1, Parse: 1}, DependsOn: []string{"plan"}},
			{Name: "verify", Prompt: "verify.md", Timeout: Duration{5 * time.Minute}, Retry: RetryConfig{Transient: 1, Parse: 1, Semantic: 1}, DependsOn: []string{"plan", "implement"}},
		},
	}

	stateDir := t.TempDir()
	state, err := LoadOrCreate(stateDir, "PROJ-99")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	config := EngineConfig{
		Pipeline:   pipeline,
		Loader:     NewPromptLoader(promptDir),
		Ticket:     TicketData{Key: "PROJ-99", Summary: "Add token validation"},
		Model:      "test-model",
		WorkDir:    t.TempDir(),
		BaseBranch: "main",
		MaxCostUSD: 15.00,
		Mode:       Autonomous,
		SleepFunc:  func(d time.Duration) {},
		JitterFunc: func(max time.Duration) time.Duration { return 0 },
	}

	var events []Event
	config.OnEvent = func(evt Event) {
		events = append(events, evt)
	}

	eng := NewEngine(flexRunner, state, config)

	if err := eng.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// All 4 phases completed
	for _, name := range []string{"triage", "plan", "implement", "verify"} {
		if !state.IsCompleted(name) {
			t.Errorf("phase %s should be completed", name)
		}
	}

	// Total cost accumulated
	expectedCost := 0.30 + 0.50 + 2.00 + 0.40
	if state.Meta().TotalCost < expectedCost-0.01 {
		t.Errorf("TotalCost = %.2f, want >= %.2f", state.Meta().TotalCost, expectedCost)
	}

	// Artifacts persisted
	for _, name := range []string{"triage", "plan", "implement", "verify"} {
		artifact, err := state.ReadArtifact(name)
		if err != nil {
			t.Errorf("ReadArtifact(%s): %v", name, err)
		}
		if len(artifact) == 0 {
			t.Errorf("artifact for %s should not be empty", name)
		}
	}

	// Results persisted
	for _, name := range []string{"triage", "plan", "implement", "verify"} {
		result, err := state.ReadResult(name)
		if err != nil {
			t.Errorf("ReadResult(%s): %v", name, err)
		}
		if len(result) == 0 {
			t.Errorf("result for %s should not be empty", name)
		}
	}

	// Artifact flow: plan's prompt should contain triage artifact
	planCall := flexRunner.calls[1]
	if !strings.Contains(planCall.UserPrompt, "Triage: small, automatable") {
		t.Errorf("plan prompt should contain triage artifact, got: %s", planCall.UserPrompt)
	}

	// Verify prompt should contain both plan and implement artifacts
	verifyCall := flexRunner.calls[3]
	if !strings.Contains(verifyCall.UserPrompt, "Plan: 1 task") {
		t.Errorf("verify prompt should contain plan artifact")
	}
	if !strings.Contains(verifyCall.UserPrompt, "Implemented T1") {
		t.Errorf("verify prompt should contain implement artifact")
	}

	// Events should include engine lifecycle
	eventKinds := make(map[string]int)
	for _, evt := range events {
		eventKinds[evt.Kind]++
	}
	if eventKinds[EventEngineStarted] != 1 {
		t.Errorf("engine_started events = %d, want 1", eventKinds[EventEngineStarted])
	}
	if eventKinds[EventEngineCompleted] != 1 {
		t.Errorf("engine_completed events = %d, want 1", eventKinds[EventEngineCompleted])
	}
	if eventKinds[EventPhaseStarted] < 4 {
		t.Errorf("phase_started events = %d, want >= 4", eventKinds[EventPhaseStarted])
	}
}
```

- [ ] **Step 2: Run the integration test**

Run: `go test ./internal/pipeline/ -run TestEngineFullLifecycle -v`
Expected: PASS

- [ ] **Step 3: Run ALL project tests with race detector**

Run: `go test -race ./...`
Expected: All PASS, no races

- [ ] **Step 4: Run linting**

Run: `go vet ./... && gofmt -l .`
Expected: No issues, no files listed

- [ ] **Step 5: Commit**

```bash
git add internal/pipeline/engine_test.go
git commit -m "test(pipeline): add full lifecycle integration test"
```

---

### Task 10: Phase gating test

**Files:**
- Modify: `internal/pipeline/engine_test.go`

- [ ] **Step 1: Add phase gating tests**

Append to `internal/pipeline/engine_test.go`:

```go
func TestEnginePhaseGating(t *testing.T) {
	t.Run("triage_not_automatable", func(t *testing.T) {
		triageOutput := json.RawMessage(`{"automatable":false,"block_reason":"requires database migration"}`)
		mock := &runner.MockRunner{
			Responses: map[string]*runner.RunResult{
				"triage": {Output: triageOutput, RawText: "not automatable", CostUSD: 0.25},
			},
		}

		promptDir := t.TempDir()
		pipeline := &PhasePipeline{
			Phases: []PhaseConfig{
				{Name: "triage", Prompt: "triage.md", Timeout: Duration{3 * time.Minute}},
				{Name: "plan", Prompt: "plan.md", Timeout: Duration{5 * time.Minute}, DependsOn: []string{"triage"}},
			},
		}

		eng, _ := setupEngine(t, mock, pipeline)
		err := eng.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got %T: %v", err, err)
		}
		if gateErr.Phase != "triage" {
			t.Errorf("Phase = %q, want triage", gateErr.Phase)
		}
		if !strings.Contains(gateErr.Reason, "database migration") {
			t.Errorf("Reason should contain block reason, got: %s", gateErr.Reason)
		}

		// Plan should NOT have been called
		if len(mock.Calls) != 1 {
			t.Errorf("Calls = %d, want 1", len(mock.Calls))
		}
	})

	t.Run("plan_empty_tasks", func(t *testing.T) {
		flexRunner := &flexMockRunner{
			responses: map[string][]flexResponse{
				"triage": {{result: &runner.RunResult{Output: json.RawMessage(`{"automatable":true}`), RawText: "ok", CostUSD: 0.25}}},
				"plan":   {{result: &runner.RunResult{Output: json.RawMessage(`{"tasks":[]}`), RawText: "empty plan", CostUSD: 0.25}}},
			},
		}

		promptDir := t.TempDir()
		pipeline := &PhasePipeline{
			Phases: []PhaseConfig{
				{Name: "triage", Prompt: "triage.md", Timeout: Duration{3 * time.Minute}},
				{Name: "plan", Prompt: "plan.md", Timeout: Duration{5 * time.Minute}, DependsOn: []string{"triage"}},
			},
		}

		eng, _ := setupEngine(t, nil, pipeline)
		eng.runner = flexRunner

		err := eng.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError for empty tasks")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got %T: %v", err, err)
		}
		if gateErr.Phase != "plan" {
			t.Errorf("Phase = %q, want plan", gateErr.Phase)
		}
	})

	t.Run("verify_fail_verdict", func(t *testing.T) {
		flexRunner := &flexMockRunner{
			responses: map[string][]flexResponse{
				"verify": {{result: &runner.RunResult{
					Output:  json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix the test"]}`),
					RawText: "failed",
					CostUSD: 0.25,
				}}},
			},
		}

		promptDir := t.TempDir()
		pipeline := &PhasePipeline{
			Phases: []PhaseConfig{
				{Name: "verify", Prompt: "verify.md", Timeout: Duration{5 * time.Minute}},
			},
		}

		eng, _ := setupEngine(t, nil, pipeline)
		eng.runner = flexRunner

		err := eng.Run(context.Background())
		if err == nil {
			t.Fatal("expected PhaseGateError for FAIL verdict")
		}

		var gateErr *PhaseGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected PhaseGateError, got %T: %v", err, err)
		}
		if !strings.Contains(gateErr.Reason, "fix the test") {
			t.Errorf("Reason should contain fix, got: %s", gateErr.Reason)
		}
	})
}
```

- [ ] **Step 2: Run the gating tests**

Run: `go test ./internal/pipeline/ -run TestEnginePhaseGating -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/engine_test.go
git commit -m "test(pipeline): add phase gating tests"
```

---

### Task 11: Final verification

- [ ] **Step 1: Run all tests with race detector and coverage**

Run: `go test -race -cover ./...`
Expected: All PASS, coverage for new packages > 70%

- [ ] **Step 2: Run linting**

Run: `go vet ./... && gofmt -l .`
Expected: Clean

- [ ] **Step 3: Verify file structure**

Run: `find internal/runner internal/pipeline internal/git -type f | sort`
Expected:
```
internal/git/worktree.go
internal/git/worktree_test.go
internal/pipeline/atomic.go
internal/pipeline/atomic_test.go
internal/pipeline/engine.go
internal/pipeline/engine_test.go
internal/pipeline/errors.go
internal/pipeline/errors_test.go
internal/pipeline/events.go
internal/pipeline/events_test.go
internal/pipeline/lock.go
internal/pipeline/lock_test.go
internal/pipeline/meta.go
internal/pipeline/meta_test.go
internal/pipeline/phase.go
internal/pipeline/phase_test.go
internal/pipeline/prompt.go
internal/pipeline/prompt_test.go
internal/pipeline/state.go
internal/pipeline/state_test.go
internal/runner/mock.go
internal/runner/mock_test.go
internal/runner/runner.go
```
