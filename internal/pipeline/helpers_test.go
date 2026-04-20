package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
)

// flexMockRunner returns per-call responses, allowing multi-call test scenarios
// (e.g., fail twice then succeed).
type flexMockRunner struct {
	mu        sync.Mutex
	responses map[string][]flexResponse
	calls     []runner.RunOpts
	counters  map[string]int
}

type flexResponse struct {
	result *runner.RunResult
	err    error
}

func (f *flexMockRunner) Run(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

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

// setupEngine creates temp directories, writes minimal prompt templates,
// creates State, and returns an Engine + State ready for testing.
func setupEngine(t *testing.T, phases []PhaseConfig, r runner.Runner, opts ...func(*EngineConfig)) (*Engine, *State) {
	t.Helper()

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Write a minimal prompt template for each phase.
	for _, p := range phases {
		tmplPath := filepath.Join(promptDir, p.Prompt)
		if err := os.MkdirAll(filepath.Dir(tmplPath), 0755); err != nil {
			t.Fatalf("mkdir for prompt %s: %v", p.Prompt, err)
		}
		content := fmt.Sprintf("Phase: %s\nTicket: {{.Ticket.Key}}\n", p.Name)
		if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
			t.Fatalf("write prompt %s: %v", p.Prompt, err)
		}
	}

	state, err := LoadOrCreate(stateDir, "TEST-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	pipeline := &PhasePipeline{Phases: phases}
	loader := NewPromptLoader(promptDir)

	cfg := EngineConfig{
		Pipeline:   pipeline,
		Loader:     loader,
		Ticket:     TicketData{Key: "TEST-1", Summary: "Test ticket"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0, // no budget limit by default
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {}, // no-op sleep for tests
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	engine := NewEngine(r, state, cfg)
	return engine, state
}

// initGitRepo initialises a bare git repository in the given directory for
// tests that exercise worktree creation.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s: %v", args, out, err)
		}
	}
}

// phaseNames extracts the phase name from each RunOpts call.
func phaseNames(calls []runner.RunOpts) []string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Phase
	}
	return names
}

// setupReviewEngine creates temp directories, writes prompt templates for
// reviewer-specific prompts, creates State, and returns an Engine + State
// for testing parallel-review phases.
func setupReviewEngine(t *testing.T, phases []PhaseConfig, r runner.Runner, opts ...func(*EngineConfig)) (*Engine, *State) {
	t.Helper()

	stateDir := t.TempDir()
	promptDir := t.TempDir()
	workDir := t.TempDir()

	// Write prompt templates for regular phases.
	for _, p := range phases {
		if p.Prompt != "" {
			tmplPath := filepath.Join(promptDir, p.Prompt)
			if err := os.MkdirAll(filepath.Dir(tmplPath), 0755); err != nil {
				t.Fatalf("mkdir for prompt %s: %v", p.Prompt, err)
			}
			content := fmt.Sprintf("Phase: %s\nTicket: {{.Ticket.Key}}\n", p.Name)
			if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
				t.Fatalf("write prompt %s: %v", p.Prompt, err)
			}
		}

		// Write reviewer prompt templates for parallel-review phases.
		for _, reviewer := range p.Reviewers {
			tmplPath := filepath.Join(promptDir, reviewer.Prompt)
			if err := os.MkdirAll(filepath.Dir(tmplPath), 0755); err != nil {
				t.Fatalf("mkdir for reviewer prompt %s: %v", reviewer.Prompt, err)
			}
			content := fmt.Sprintf("Reviewer: %s\nFocus: %s\nTicket: {{.Ticket.Key}}\n", reviewer.Name, reviewer.Focus)
			if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
				t.Fatalf("write reviewer prompt %s: %v", reviewer.Prompt, err)
			}
		}
	}

	state, err := LoadOrCreate(stateDir, "TEST-1")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	pipeline := &PhasePipeline{Phases: phases}
	loader := NewPromptLoader(promptDir)

	cfg := EngineConfig{
		Pipeline:   pipeline,
		Loader:     loader,
		Ticket:     TicketData{Key: "TEST-1", Summary: "Test ticket"},
		Model:      "test-model",
		WorkDir:    workDir,
		MaxCostUSD: 0,
		Mode:       Autonomous,
		SleepFunc:  func(time.Duration) {},
		JitterFunc: func(time.Duration) time.Duration { return 0 },
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	engine := NewEngine(r, state, cfg)
	return engine, state
}

// chunkMockRunner extends flexMockRunner with per-phase chunk emission.
type chunkMockRunner struct {
	flexMockRunner
	chunks map[string][]string // phase name → lines to emit via OnChunk
}

func (c *chunkMockRunner) Run(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
	// Emit chunks before returning the result.
	if lines, ok := c.chunks[opts.Phase]; ok && opts.OnChunk != nil {
		for _, line := range lines {
			opts.OnChunk(line)
		}
	}
	return c.flexMockRunner.Run(ctx, opts)
}

// blockingChunkRunner emits all chunks then returns its fixed result.
type blockingChunkRunner struct {
	result *runner.RunResult
	chunks []string
}

func (b *blockingChunkRunner) Run(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
	for _, line := range b.chunks {
		if opts.OnChunk != nil {
			opts.OnChunk(line)
		}
	}
	return b.result, nil
}

// capturingChunkRunner captures the OnChunk callback for external use.
type capturingChunkRunner struct {
	result         *runner.RunResult
	captureOnChunk func(func(string))
}

func (c *capturingChunkRunner) Run(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
	if c.captureOnChunk != nil {
		c.captureOnChunk(opts.OnChunk)
	}
	return c.result, nil
}

// funcRunner delegates to an arbitrary function, useful for one-off test
// behaviour.
type funcRunner struct {
	fn func(context.Context, runner.RunOpts) (*runner.RunResult, error)
}

func (f *funcRunner) Run(ctx context.Context, opts runner.RunOpts) (*runner.RunResult, error) {
	return f.fn(ctx, opts)
}
