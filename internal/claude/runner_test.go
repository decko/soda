package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestStream_OnChunkPanic(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	t.Setenv("MOCK_CLAUDE_MODE", "success")

	result, err := runner.Stream(context.Background(), RunOpts{
		Timeout: 10 * time.Second,
	}, func(line string) {
		panic("callback panic should not crash Stream")
	})
	if err != nil {
		t.Fatalf("Stream should succeed despite onChunk panic: %v", err)
	}
	if result.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v, want 0.05", result.CostUSD)
	}
}

func TestStream_RejectsOversizedSchema(t *testing.T) {
	runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// Create a schema larger than 256KB
	bigSchema := `{"type":"object","properties":{` + strings.Repeat(`"field":"string",`, 20000) + `"last":"string"}}`

	_, err = runner.Stream(context.Background(), RunOpts{
		OutputSchema: bigSchema,
		Timeout:      10 * time.Second,
	}, nil)
	if err == nil {
		t.Fatal("expected error for oversized OutputSchema")
	}
}
