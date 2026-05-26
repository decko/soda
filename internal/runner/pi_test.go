package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var _ Runner = (*PiRunner)(nil) // compile-time interface check

func TestParsePiStream(t *testing.T) {
	t.Run("parses_assistant_and_result_events", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"assistant","content":"Hello "}`,
			`{"type":"assistant","content":"world"}`,
			`{"type":"tool_use","tool":"bash","tool_id":"t1"}`,
			`{"type":"message_end","usage":{"input_tokens":100,"output_tokens":50},"cost_usd":0.005}`,
			`{"type":"result","result":{"answer":"42"}}`,
		}, "\n")

		result, err := ParsePiStream([]byte(stream), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.RawText != "Hello world" {
			t.Errorf("RawText = %q, want %q", result.RawText, "Hello world")
		}
		if result.TokensIn != 100 {
			t.Errorf("TokensIn = %d, want 100", result.TokensIn)
		}
		if result.TokensOut != 50 {
			t.Errorf("TokensOut = %d, want 50", result.TokensOut)
		}
		if result.CostUSD != 0.005 {
			t.Errorf("CostUSD = %f, want 0.005", result.CostUSD)
		}
		if result.Turns != 1 {
			t.Errorf("Turns = %d, want 1", result.Turns)
		}
		if string(result.Output) != `{"answer":"42"}` {
			t.Errorf("Output = %s, want %s", string(result.Output), `{"answer":"42"}`)
		}
	})

	t.Run("accumulates_multiple_message_end_costs", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"message_end","usage":{"input_tokens":50,"output_tokens":25},"cost_usd":0.003}`,
			`{"type":"message_end","usage":{"input_tokens":75,"output_tokens":30},"cost_usd":0.004}`,
			`{"type":"result","result":{"ok":true}}`,
		}, "\n")

		result, err := ParsePiStream([]byte(stream), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.TokensIn != 125 {
			t.Errorf("TokensIn = %d, want 125", result.TokensIn)
		}
		if result.TokensOut != 55 {
			t.Errorf("TokensOut = %d, want 55", result.TokensOut)
		}
		wantCost := 0.007
		if result.CostUSD < wantCost-0.0001 || result.CostUSD > wantCost+0.0001 {
			t.Errorf("CostUSD = %f, want %f", result.CostUSD, wantCost)
		}
	})

	t.Run("returns_semantic_error_on_result_error", func(t *testing.T) {
		stream := `{"type":"result","subtype":"error","error":"something went wrong"}`

		_, err := ParsePiStream([]byte(stream), nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		var se *SemanticError
		if !errors.As(err, &se) {
			t.Fatalf("expected SemanticError, got %T: %v", err, err)
		}
		if se.Message != "something went wrong" {
			t.Errorf("Message = %q, want %q", se.Message, "something went wrong")
		}
	})

	t.Run("returns_transient_error_on_error_event", func(t *testing.T) {
		stream := `{"type":"error","error":"rate limit exceeded"}`

		_, err := ParsePiStream([]byte(stream), nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		var te *TransientError
		if !errors.As(err, &te) {
			t.Fatalf("expected TransientError, got %T: %v", err, err)
		}
		if te.Reason != "rate_limit" {
			t.Errorf("Reason = %q, want %q", te.Reason, "rate_limit")
		}
	})

	t.Run("calls_onChunk_for_assistant_content", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"assistant","content":"chunk1"}`,
			`{"type":"assistant","content":"chunk2"}`,
			`{"type":"result","result":{}}`,
		}, "\n")

		var chunks []string
		onChunk := func(text string) {
			chunks = append(chunks, text)
		}

		_, err := ParsePiStream([]byte(stream), onChunk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(chunks) != 2 {
			t.Fatalf("got %d chunks, want 2", len(chunks))
		}
		if chunks[0] != "chunk1" || chunks[1] != "chunk2" {
			t.Errorf("chunks = %v, want [chunk1, chunk2]", chunks)
		}
	})

	t.Run("skips_non_json_lines", func(t *testing.T) {
		stream := strings.Join([]string{
			"debug: starting up",
			`{"type":"result","result":{"ok":true}}`,
			"",
		}, "\n")

		result, err := ParsePiStream([]byte(stream), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if string(result.Output) != `{"ok":true}` {
			t.Errorf("Output = %s, want %s", string(result.Output), `{"ok":true}`)
		}
	})

	t.Run("empty_stream_returns_empty_result", func(t *testing.T) {
		result, err := ParsePiStream([]byte(""), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.RawText != "" {
			t.Errorf("RawText = %q, want empty", result.RawText)
		}
		if result.Output != nil {
			t.Errorf("Output = %s, want nil", string(result.Output))
		}
	})

	t.Run("null_result_treated_as_nil", func(t *testing.T) {
		stream := `{"type":"result","result":null}`
		result, err := ParsePiStream([]byte(stream), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Output != nil {
			t.Errorf("Output = %s, want nil", string(result.Output))
		}
	})
}

func TestValidatePiOutput(t *testing.T) {
	t.Run("passes_with_all_required_fields", func(t *testing.T) {
		output := json.RawMessage(`{"ticket_key":"TEST-1","verdict":"PASS"}`)
		schema := `{"required":["ticket_key","verdict"]}`

		err := ValidatePiOutput(output, schema)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("fails_with_missing_required_field", func(t *testing.T) {
		output := json.RawMessage(`{"ticket_key":"TEST-1"}`)
		schema := `{"required":["ticket_key","verdict"]}`

		err := ValidatePiOutput(output, schema)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected ParseError, got %T: %v", err, err)
		}
		if !strings.Contains(pe.Error(), "verdict") {
			t.Errorf("error should mention missing field 'verdict', got: %v", pe)
		}
	})

	t.Run("passes_with_empty_schema", func(t *testing.T) {
		output := json.RawMessage(`{"anything":"goes"}`)
		err := ValidatePiOutput(output, "")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("passes_with_no_required_fields_in_schema", func(t *testing.T) {
		output := json.RawMessage(`{"anything":"goes"}`)
		schema := `{"type":"object","properties":{}}`
		err := ValidatePiOutput(output, schema)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("fails_with_empty_output", func(t *testing.T) {
		err := ValidatePiOutput(nil, `{"required":["key"]}`)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected ParseError, got %T: %v", err, err)
		}
	})

	t.Run("passes_with_invalid_schema_json", func(t *testing.T) {
		output := json.RawMessage(`{"key":"val"}`)
		err := ValidatePiOutput(output, "not valid json")
		if err != nil {
			t.Errorf("invalid schema should not block output: %v", err)
		}
	})
}

func TestMapPiToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Read", "read_file"},
		{"Write", "write_file"},
		{"Edit", "edit_file"},
		{"Glob", "glob"},
		{"Grep", "grep"},
		{"Bash", "bash"},
		{"Search", "search"},
		{"Bash(git:*)", "bash(git:*)"},
		{"UnknownTool", "UnknownTool"},
		{"custom_tool", "custom_tool"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := MapPiToolName(tt.input)
			if got != tt.want {
				t.Errorf("MapPiToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestClassifyPiError(t *testing.T) {
	tests := []struct {
		msg    string
		reason string
	}{
		{"rate limit exceeded", "rate_limit"},
		{"HTTP 429 Too Many Requests", "rate_limit"},
		{"request timeout", "timeout"},
		{"server overloaded 503", "overloaded"},
		{"connection refused", "connection"},
		{"something unknown happened", "unknown"},
		{"used 1500 tokens", "unknown"}, // bare "500" must NOT match
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			got := classifyPiError(tt.msg)
			if got != tt.reason {
				t.Errorf("classifyPiError(%q) = %q, want %q", tt.msg, got, tt.reason)
			}
		})
	}
}

func TestBuildPiArgs(t *testing.T) {
	t.Run("basic_args", func(t *testing.T) {
		opts := RunOpts{
			Model:        "pi-model-1",
			OutputSchema: `{"type":"object"}`,
			AllowedTools: []string{"Read", "Bash(git:*)"},
		}
		args := buildPiArgs(opts, "default-model")

		// Model should be per-invocation override.
		assertContainsArg(t, args, "--model", "pi-model-1")
		// Pi has no --json-schema flag; schema validation is done in software.
		for idx, arg := range args {
			if arg == "--json-schema" {
				t.Errorf("args must not contain --json-schema (at index %d)", idx)
			}
		}
		// Tools should be mapped.
		assertContainsArg(t, args, "--allowed-tools", "read_file")
		assertContainsArg(t, args, "--allowed-tools", "bash(git:*)")
	})

	t.Run("default_model_when_not_overridden", func(t *testing.T) {
		opts := RunOpts{}
		args := buildPiArgs(opts, "default-model")
		assertContainsArg(t, args, "--model", "default-model")
	})

	t.Run("no_model_when_both_empty", func(t *testing.T) {
		opts := RunOpts{}
		args := buildPiArgs(opts, "")
		for _, arg := range args {
			if arg == "--model" {
				t.Error("should not include --model when both are empty")
			}
		}
	})
}

func TestPiLimitedBuffer(t *testing.T) {
	t.Run("truncates_at_limit", func(t *testing.T) {
		lb := &piLimitedBuffer{max: 10}
		lb.Write([]byte("hello"))
		lb.Write([]byte("world!"))

		if lb.Len() != 10 {
			t.Errorf("Len() = %d, want 10", lb.Len())
		}
		if !lb.overflow {
			t.Error("expected overflow to be true")
		}
		if string(lb.Bytes()) != "helloworld" {
			t.Errorf("Bytes() = %q, want %q", string(lb.Bytes()), "helloworld")
		}
	})

	t.Run("write_returns_full_length", func(t *testing.T) {
		lb := &piLimitedBuffer{max: 5}
		n, err := lb.Write([]byte("hello world"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 11 {
			t.Errorf("Write returned %d, want 11", n)
		}
		// Second write after overflow still returns full length.
		n, err = lb.Write([]byte("more data"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 9 {
			t.Errorf("Write returned %d, want 9", n)
		}
	})
}

func TestClassifyPiExitError(t *testing.T) {
	t.Run("rate_limit_in_stderr", func(t *testing.T) {
		err := classifyPiExitError(
			fmt.Errorf("exit status 1"),
			[]byte("Error: rate limit exceeded"),
		)
		var te *TransientError
		if !errors.As(err, &te) {
			t.Fatalf("expected TransientError, got %T: %v", err, err)
		}
		if te.Reason != "rate_limit" {
			t.Errorf("Reason = %q, want %q", te.Reason, "rate_limit")
		}
	})

	t.Run("unknown_stderr", func(t *testing.T) {
		err := classifyPiExitError(
			fmt.Errorf("exit status 1"),
			[]byte("something else happened"),
		)
		var te *TransientError
		if !errors.As(err, &te) {
			t.Fatalf("expected TransientError, got %T: %v", err, err)
		}
		if te.Reason != "unknown" {
			t.Errorf("Reason = %q, want %q", te.Reason, "unknown")
		}
	})

	t.Run("no_false_positive_on_bare_numbers", func(t *testing.T) {
		err := classifyPiExitError(
			fmt.Errorf("exit status 1"),
			[]byte("used 1500 tokens in 2529 ms"),
		)
		var te *TransientError
		if !errors.As(err, &te) {
			t.Fatalf("expected TransientError, got %T: %v", err, err)
		}
		if te.Reason != "unknown" {
			t.Errorf("Reason = %q, want %q (bare numbers should not match)", te.Reason, "unknown")
		}
	})
}

func TestWritePiSystemPrompt(t *testing.T) {
	t.Run("writes_to_pi_directory", func(t *testing.T) {
		dir := t.TempDir()
		content := "You are a helpful assistant."

		cleanup, err := writePiSystemPrompt(dir, content)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer cleanup()

		promptPath := filepath.Join(dir, ".pi", "SYSTEM.md")
		got, readErr := os.ReadFile(promptPath)
		if readErr != nil {
			t.Fatalf("failed to read file: %v", readErr)
		}
		if string(got) != content {
			t.Errorf("content = %q, want %q", string(got), content)
		}
	})

	t.Run("restores_existing_file_on_cleanup", func(t *testing.T) {
		dir := t.TempDir()
		piDir := filepath.Join(dir, ".pi")
		if err := os.MkdirAll(piDir, 0o755); err != nil {
			t.Fatalf("create .pi dir: %v", err)
		}
		promptPath := filepath.Join(piDir, "SYSTEM.md")
		original := "user-owned content"
		if err := os.WriteFile(promptPath, []byte(original), 0o644); err != nil {
			t.Fatalf("write original: %v", err)
		}

		cleanup, err := writePiSystemPrompt(dir, "soda override")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// During run, file should have soda content.
		got, _ := os.ReadFile(promptPath)
		if string(got) != "soda override" {
			t.Errorf("during run: content = %q, want %q", string(got), "soda override")
		}

		cleanup()

		// After cleanup, original content should be restored.
		got, _ = os.ReadFile(promptPath)
		if string(got) != original {
			t.Errorf("after cleanup: content = %q, want %q", string(got), original)
		}
	})

	t.Run("removes_file_and_dir_when_none_existed", func(t *testing.T) {
		dir := t.TempDir()
		piDir := filepath.Join(dir, ".pi")

		cleanup, err := writePiSystemPrompt(dir, "test content")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// File should exist during run.
		promptPath := filepath.Join(piDir, "SYSTEM.md")
		if _, statErr := os.Stat(promptPath); statErr != nil {
			t.Fatalf("SYSTEM.md should exist during run: %v", statErr)
		}

		cleanup()

		// After cleanup, both file and directory should be gone.
		if _, statErr := os.Stat(promptPath); statErr == nil {
			t.Error("SYSTEM.md should be removed after cleanup")
		}
		if _, statErr := os.Stat(piDir); statErr == nil {
			t.Error(".pi directory should be removed after cleanup when we created it")
		}
	})
}

func TestRevertWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()

	// Initialise a bare git repo with one committed file.
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	testFile := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(testFile, []byte("original"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Modify the file.
	if err := os.WriteFile(testFile, []byte("modified"), 0o644); err != nil {
		t.Fatalf("modify file: %v", err)
	}

	// Revert the worktree.
	revertWorktree(dir)

	// Verify the file is back to original.
	got, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read file after revert: %v", err)
	}
	if string(got) != "original" {
		t.Errorf("content after revert = %q, want %q", string(got), "original")
	}
}

func TestBudgetEnforcement(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a fake pi binary (shell script) that emits a cost-exceeding event.
	binDir := t.TempDir()
	fakePi := filepath.Join(binDir, "pi")
	script := "#!/bin/sh\n" +
		`echo '{"type":"message_end","usage":{"input_tokens":100,"output_tokens":50},"cost_usd":99.99}'` + "\n" +
		`echo '{"type":"result","result":{"ok":true}}'` + "\n"
	if err := os.WriteFile(fakePi, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}

	// Create a git worktree with a committed file.
	workDir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	agentFile := filepath.Join(workDir, "output.txt")
	if err := os.WriteFile(agentFile, []byte("clean"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Simulate agent modifying a file before budget exceeded.
	if err := os.WriteFile(agentFile, []byte("dirty"), 0o644); err != nil {
		t.Fatalf("dirty agent file: %v", err)
	}

	piRunner, err := NewPiRunner(fakePi, "test-model", workDir)
	if err != nil {
		t.Fatalf("NewPiRunner: %v", err)
	}

	opts := RunOpts{
		MaxBudgetUSD: 0.01, // well below the 99.99 emitted by the fake binary
		WorkDir:      workDir,
	}

	_, runErr := piRunner.Run(context.Background(), opts)
	if runErr == nil {
		t.Fatal("expected budget_exceeded error, got nil")
	}

	var te *TransientError
	if !errors.As(runErr, &te) {
		t.Fatalf("expected TransientError, got %T: %v", runErr, runErr)
	}
	if te.Reason != "budget_exceeded" {
		t.Errorf("Reason = %q, want %q", te.Reason, "budget_exceeded")
	}

	// Verify revertWorktree was called: the dirty file should be restored.
	got, err := os.ReadFile(agentFile)
	if err != nil {
		t.Fatalf("read agent file after budget exceeded: %v", err)
	}
	if string(got) != "clean" {
		t.Errorf("worktree not reverted: content = %q, want %q", string(got), "clean")
	}
}

// assertContainsArg checks that args contains a flag followed by a specific value.
func assertContainsArg(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for idx, arg := range args {
		if arg == flag && idx+1 < len(args) && args[idx+1] == value {
			return
		}
	}
	t.Errorf("args %v does not contain %s %s", args, flag, value)
}
