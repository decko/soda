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

var _ Runner = (*OpencodeRunner)(nil) // compile-time interface check

func TestParseOpencodeStream(t *testing.T) {
	t.Run("parses_text_and_result_events", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"text","content":"Hello "}`,
			`{"type":"text","content":"world"}`,
			`{"type":"tool_use","tool":"bash"}`,
			`{"type":"done","usage":{"input_tokens":100,"output_tokens":50},"cost_usd":0.005}`,
			`{"type":"result","result":{"answer":"42"}}`,
		}, "\n")

		result, err := ParseOpencodeStream([]byte(stream), nil)
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

	t.Run("accumulates_multiple_done_costs", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"done","usage":{"input_tokens":50,"output_tokens":25},"cost_usd":0.003}`,
			`{"type":"done","usage":{"input_tokens":75,"output_tokens":30},"cost_usd":0.004}`,
			`{"type":"result","result":{"ok":true}}`,
		}, "\n")

		result, err := ParseOpencodeStream([]byte(stream), nil)
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

	t.Run("returns_transient_error_on_error_event", func(t *testing.T) {
		stream := `{"type":"error","error":"rate limit exceeded"}`

		_, err := ParseOpencodeStream([]byte(stream), nil)
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

	t.Run("returns_parse_error_on_unknown_error_event", func(t *testing.T) {
		stream := `{"type":"error","error":"invalid API key"}`

		_, err := ParseOpencodeStream([]byte(stream), nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected ParseError for non-transient error, got %T: %v", err, err)
		}
		if !strings.Contains(pe.Error(), "invalid API key") {
			t.Errorf("error should contain original message, got: %v", pe)
		}
	})

	t.Run("calls_onChunk_for_text_content", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"text","content":"chunk1"}`,
			`{"type":"text","content":"chunk2"}`,
			`{"type":"result","result":{}}`,
		}, "\n")

		var chunks []string
		onChunk := func(text string) {
			chunks = append(chunks, text)
		}

		_, err := ParseOpencodeStream([]byte(stream), onChunk)
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

		result, err := ParseOpencodeStream([]byte(stream), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if string(result.Output) != `{"ok":true}` {
			t.Errorf("Output = %s, want %s", string(result.Output), `{"ok":true}`)
		}
	})

	t.Run("empty_stream_returns_empty_result", func(t *testing.T) {
		result, err := ParseOpencodeStream([]byte(""), nil)
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

	t.Run("extracts_json_from_text_when_no_result_event", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"text","content":"Here is the output:\n"}`,
			"{\"type\":\"text\",\"content\":\"```json\\n{\\\"answer\\\":\\\"42\\\"}\\n```\"}",
			`{"type":"done","usage":{"input_tokens":10,"output_tokens":5},"cost_usd":0.001}`,
		}, "\n")

		result, err := ParseOpencodeStream([]byte(stream), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if string(result.Output) != `{"answer":"42"}` {
			t.Errorf("Output = %s, want %s", string(result.Output), `{"answer":"42"}`)
		}
	})

	t.Run("result_event_clears_prior_error", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"error","error":"rate limit exceeded"}`,
			`{"type":"text","content":"Hello"}`,
			`{"type":"result","result":{"answer":"42"}}`,
		}, "\n")
		result, err := ParseOpencodeStream([]byte(stream), nil)
		if err != nil {
			t.Fatalf("expected recovery after valid result, got: %v", err)
		}
		if string(result.Output) != `{"answer":"42"}` {
			t.Errorf("Output = %s, want %s", string(result.Output), `{"answer":"42"}`)
		}
	})
}

func TestExtractJSONFromText(t *testing.T) {
	t.Run("extracts_fenced_json_block", func(t *testing.T) {
		text := "Some text\n```json\n{\"key\":\"value\"}\n```\nmore text"
		got := extractJSONFromText(text)
		if string(got) != `{"key":"value"}` {
			t.Errorf("got %s, want %s", string(got), `{"key":"value"}`)
		}
	})

	t.Run("extracts_last_fenced_json_block", func(t *testing.T) {
		text := "```json\n{\"first\":true}\n```\n\n```json\n{\"second\":true}\n```"
		got := extractJSONFromText(text)
		if string(got) != `{"second":true}` {
			t.Errorf("got %s, want %s", string(got), `{"second":true}`)
		}
	})

	t.Run("falls_back_to_raw_json_object", func(t *testing.T) {
		text := "Here is the result: {\"answer\":42} done."
		got := extractJSONFromText(text)
		if string(got) != `{"answer":42}` {
			t.Errorf("got %s, want %s", string(got), `{"answer":42}`)
		}
	})

	t.Run("returns_nil_for_no_json", func(t *testing.T) {
		text := "No JSON here at all"
		got := extractJSONFromText(text)
		if got != nil {
			t.Errorf("got %s, want nil", string(got))
		}
	})

	t.Run("returns_nil_for_empty_text", func(t *testing.T) {
		got := extractJSONFromText("")
		if got != nil {
			t.Errorf("got %s, want nil", string(got))
		}
	})
}

func TestValidateOpencodeOutput(t *testing.T) {
	t.Run("passes_with_all_required_fields", func(t *testing.T) {
		output := json.RawMessage(`{"ticket_key":"TEST-1","verdict":"PASS"}`)
		schema := `{"required":["ticket_key","verdict"]}`

		err := ValidateOpencodeOutput(output, schema)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("fails_with_missing_required_field", func(t *testing.T) {
		output := json.RawMessage(`{"ticket_key":"TEST-1"}`)
		schema := `{"required":["ticket_key","verdict"]}`

		err := ValidateOpencodeOutput(output, schema)
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
		err := ValidateOpencodeOutput(output, "")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("passes_with_no_required_fields_in_schema", func(t *testing.T) {
		output := json.RawMessage(`{"anything":"goes"}`)
		schema := `{"type":"object","properties":{}}`
		err := ValidateOpencodeOutput(output, schema)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("fails_with_empty_output", func(t *testing.T) {
		err := ValidateOpencodeOutput(nil, `{"required":["key"]}`)
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
		err := ValidateOpencodeOutput(output, "not valid json")
		if err != nil {
			t.Errorf("invalid schema should not block output: %v", err)
		}
	})
}

func TestMapOpencodeToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Read", "read"},
		{"Write", "write"},
		{"Edit", "edit"},
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
			got := MapOpencodeToolName(tt.input)
			if got != tt.want {
				t.Errorf("MapOpencodeToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDeduplicateTools(t *testing.T) {
	t.Run("removes_duplicates", func(t *testing.T) {
		input := []string{"bash", "read", "bash", "write", "read"}
		got := deduplicateTools(input)
		want := []string{"bash", "read", "write"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for idx := range want {
			if got[idx] != want[idx] {
				t.Errorf("got[%d] = %q, want %q", idx, got[idx], want[idx])
			}
		}
	})

	t.Run("preserves_order", func(t *testing.T) {
		input := []string{"write", "read", "bash"}
		got := deduplicateTools(input)
		for idx := range input {
			if got[idx] != input[idx] {
				t.Errorf("got[%d] = %q, want %q", idx, got[idx], input[idx])
			}
		}
	})

	t.Run("handles_empty", func(t *testing.T) {
		got := deduplicateTools(nil)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})
}

func TestClassifyOpencodeError(t *testing.T) {
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
			got := classifyOpencodeError(tt.msg)
			if got != tt.reason {
				t.Errorf("classifyOpencodeError(%q) = %q, want %q", tt.msg, got, tt.reason)
			}
		})
	}
}

func TestBuildOpencodeArgs(t *testing.T) {
	t.Run("basic_args", func(t *testing.T) {
		opts := RunOpts{
			Phase:        "implement",
			Model:        "opencode-model-1",
			OutputSchema: `{"type":"object"}`,
			AllowedTools: []string{"Read", "Bash(git:*)"},
		}
		args := buildOpencodeArgs(opts, "default-model")

		// Model should be per-invocation override.
		assertContainsArg(t, args, "--model", "opencode-model-1")
		// Should have --dangerously-skip-permissions.
		found := false
		for _, arg := range args {
			if arg == "--dangerously-skip-permissions" {
				found = true
				break
			}
		}
		if !found {
			t.Error("args should contain --dangerously-skip-permissions")
		}
		// Agent name should be derived from phase.
		assertContainsArg(t, args, "--agent", "soda-implement")
		// Tools should be mapped and joined.
		assertContainsArg(t, args, "--permissions", "read,bash(git:*)")
	})

	t.Run("default_model_when_not_overridden", func(t *testing.T) {
		opts := RunOpts{Phase: "triage"}
		args := buildOpencodeArgs(opts, "default-model")
		assertContainsArg(t, args, "--model", "default-model")
	})

	t.Run("no_model_when_both_empty", func(t *testing.T) {
		opts := RunOpts{Phase: "triage"}
		args := buildOpencodeArgs(opts, "")
		for _, arg := range args {
			if arg == "--model" {
				t.Error("should not include --model when both are empty")
			}
		}
	})

	t.Run("no_permissions_when_no_tools", func(t *testing.T) {
		opts := RunOpts{Phase: "triage"}
		args := buildOpencodeArgs(opts, "model")
		for _, arg := range args {
			if arg == "--permissions" {
				t.Error("should not include --permissions when no tools")
			}
		}
	})

	t.Run("phase_with_slash", func(t *testing.T) {
		opts := RunOpts{Phase: "review/go-specialist"}
		args := buildOpencodeArgs(opts, "model")
		assertContainsArg(t, args, "--agent", "soda-review-go-specialist")
	})
}

func TestWriteOpencodeAgentFile(t *testing.T) {
	t.Run("writes_to_opencode_agent_directory", func(t *testing.T) {
		dir := t.TempDir()
		content := "You are a helpful assistant."

		cleanup, err := writeOpencodeAgentFile(dir, "implement", content)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer cleanup()

		agentPath := filepath.Join(dir, ".opencode", "agent", "soda-implement.md")
		got, readErr := os.ReadFile(agentPath)
		if readErr != nil {
			t.Fatalf("failed to read file: %v", readErr)
		}
		if string(got) != content {
			t.Errorf("content = %q, want %q", string(got), content)
		}
	})

	t.Run("restores_existing_file_on_cleanup", func(t *testing.T) {
		dir := t.TempDir()
		agentDir := filepath.Join(dir, ".opencode", "agent")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatalf("create agent dir: %v", err)
		}
		agentPath := filepath.Join(agentDir, "soda-implement.md")
		original := "user-owned content"
		if err := os.WriteFile(agentPath, []byte(original), 0o644); err != nil {
			t.Fatalf("write original: %v", err)
		}

		cleanup, err := writeOpencodeAgentFile(dir, "implement", "soda override")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got, _ := os.ReadFile(agentPath)
		if string(got) != "soda override" {
			t.Errorf("during run: content = %q, want %q", string(got), "soda override")
		}

		cleanup()

		got, _ = os.ReadFile(agentPath)
		if string(got) != original {
			t.Errorf("after cleanup: content = %q, want %q", string(got), original)
		}
	})

	t.Run("removes_file_and_dirs_when_none_existed", func(t *testing.T) {
		dir := t.TempDir()
		ocDir := filepath.Join(dir, ".opencode")

		cleanup, err := writeOpencodeAgentFile(dir, "triage", "test content")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		agentPath := filepath.Join(ocDir, "agent", "soda-triage.md")
		if _, statErr := os.Stat(agentPath); statErr != nil {
			t.Fatalf("agent file should exist during run: %v", statErr)
		}

		cleanup()

		if _, statErr := os.Stat(agentPath); statErr == nil {
			t.Error("agent file should be removed after cleanup")
		}
		if _, statErr := os.Stat(ocDir); statErr == nil {
			t.Error(".opencode directory should be removed after cleanup when we created it")
		}
	})
}

func TestClassifyOpencodeExitError(t *testing.T) {
	t.Run("rate_limit_in_stderr", func(t *testing.T) {
		err := classifyOpencodeExitError(
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
		err := classifyOpencodeExitError(
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
		err := classifyOpencodeExitError(
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

func TestOpencodeAgentName(t *testing.T) {
	tests := []struct {
		phase string
		want  string
	}{
		{"implement", "soda-implement"},
		{"triage", "soda-triage"},
		{"review/go-specialist", "soda-review-go-specialist"},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			got := agentName(tt.phase)
			if got != tt.want {
				t.Errorf("agentName(%q) = %q, want %q", tt.phase, got, tt.want)
			}
		})
	}
}

func TestOpencodeBudgetEnforcement(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a fake opencode binary (shell script) that emits a cost-exceeding event.
	binDir := t.TempDir()
	fakeOpencode := filepath.Join(binDir, "opencode")
	script := "#!/bin/sh\n" +
		`echo '{"type":"done","usage":{"input_tokens":100,"output_tokens":50},"cost_usd":99.99}'` + "\n" +
		`echo '{"type":"result","result":{"ok":true}}'` + "\n"
	if err := os.WriteFile(fakeOpencode, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
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

	ocRunner, err := NewOpencodeRunner(fakeOpencode, "test-model", workDir)
	if err != nil {
		t.Fatalf("NewOpencodeRunner: %v", err)
	}

	opts := RunOpts{
		MaxBudgetUSD: 0.01, // well below the 99.99 emitted by the fake binary
		WorkDir:      workDir,
		Phase:        "implement",
	}

	_, runErr := ocRunner.Run(context.Background(), opts)
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

func TestBuildOpencodeEnv(t *testing.T) {
	t.Run("sets_home_to_workdir", func(t *testing.T) {
		opts := RunOpts{WorkDir: "/test/workdir"}
		env := buildOpencodeEnv(opts, "/usr/bin/opencode")

		envMap := make(map[string]string)
		for _, entry := range env {
			parts := strings.SplitN(entry, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		if envMap["HOME"] != "/test/workdir" {
			t.Errorf("HOME = %q, want /test/workdir", envMap["HOME"])
		}
	})

	t.Run("preserves_existing_env", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "test-key")
		opts := RunOpts{WorkDir: "/test/workdir"}
		env := buildOpencodeEnv(opts, "/usr/bin/opencode")

		envMap := make(map[string]string)
		for _, entry := range env {
			parts := strings.SplitN(entry, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		if envMap["ANTHROPIC_API_KEY"] != "test-key" {
			t.Errorf("ANTHROPIC_API_KEY = %q, want test-key", envMap["ANTHROPIC_API_KEY"])
		}
	})
}
