package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/runner"
)

func TestPiAdapterBuildArgs(t *testing.T) {
	// Create a fake pi binary so NewPiAdapter can resolve it.
	binDir := t.TempDir()
	fakePi := filepath.Join(binDir, "pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewPiAdapter(fakePi)
	if err != nil {
		t.Fatalf("NewPiAdapter: %v", err)
	}

	t.Run("basic_args", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			Model:        "pi-model-1",
			AllowedTools: []string{"Read", "Bash(git:*)"},
			UserPrompt:   "do the thing",
		}
		args, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		assertContains(t, args, "--print")
		assertContains(t, args, "--output-format")
		assertContainsArgPair(t, args, "--model", "pi-model-1")
		assertContainsArgPair(t, args, "--allowed-tools", "read_file")
		assertContainsArgPair(t, args, "--allowed-tools", "bash(git:*)")
		assertContainsArgPair(t, args, "-p", "do the thing")
	})

	t.Run("writes_system_prompt_to_tmpdir", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			SystemPrompt: "You are a helpful assistant.",
			UserPrompt:   "hello",
		}
		_, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		promptPath := filepath.Join(tmpDir, ".pi", "SYSTEM.md")
		got, readErr := os.ReadFile(promptPath)
		if readErr != nil {
			t.Fatalf("system prompt file not written: %v", readErr)
		}
		if string(got) != opts.SystemPrompt {
			t.Errorf("system prompt content = %q, want %q", string(got), opts.SystemPrompt)
		}
	})

	t.Run("no_model_flag_when_empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{UserPrompt: "hello"}
		args, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}
		for _, arg := range args {
			if arg == "--model" {
				t.Error("should not include --model when model is empty")
			}
		}
	})
}

func TestPiAdapterBuildEnv(t *testing.T) {
	binDir := t.TempDir()
	fakePi := filepath.Join(binDir, "pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewPiAdapter(fakePi)
	if err != nil {
		t.Fatalf("NewPiAdapter: %v", err)
	}

	t.Run("direct_mode_passes_credentials", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
		t.Setenv("PI_API_KEY", "pi-key-123")
		t.Setenv("LANG", "en_US.UTF-8")

		opts := runner.RunOpts{Phase: "implement", WorkDir: "/work"}
		env := adapter.BuildEnv(opts, "/tmp/sandbox", "")

		envMap := toEnvMap(env)

		if envMap["HOME"] != "/tmp/sandbox" {
			t.Errorf("HOME = %q, want /tmp/sandbox", envMap["HOME"])
		}
		if envMap["TMPDIR"] != "/tmp/sandbox" {
			t.Errorf("TMPDIR = %q, want /tmp/sandbox", envMap["TMPDIR"])
		}
		if envMap["ANTHROPIC_API_KEY"] != "sk-test-key" {
			t.Errorf("ANTHROPIC_API_KEY = %q, want sk-test-key", envMap["ANTHROPIC_API_KEY"])
		}
		if envMap["PI_API_KEY"] != "pi-key-123" {
			t.Errorf("PI_API_KEY = %q, want pi-key-123", envMap["PI_API_KEY"])
		}
	})

	t.Run("proxy_mode_uses_fake_key", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-real-key")

		opts := runner.RunOpts{Phase: "implement", WorkDir: "/work"}
		env := adapter.BuildEnv(opts, "/tmp/sandbox", "http://127.0.0.1:8080")

		envMap := toEnvMap(env)

		if envMap["ANTHROPIC_API_KEY"] != "sk-proxy-nonce" {
			t.Errorf("ANTHROPIC_API_KEY = %q, want sk-proxy-nonce", envMap["ANTHROPIC_API_KEY"])
		}
		if envMap["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8080" {
			t.Errorf("ANTHROPIC_BASE_URL = %q, want http://127.0.0.1:8080", envMap["ANTHROPIC_BASE_URL"])
		}
	})

	t.Run("git_credentials_passed_through", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "ghp_test123")
		t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh/agent.1234")

		opts := runner.RunOpts{Phase: "submit", WorkDir: "/work"}
		env := adapter.BuildEnv(opts, "/tmp/sandbox", "")

		envMap := toEnvMap(env)

		if envMap["GH_TOKEN"] != "ghp_test123" {
			t.Errorf("GH_TOKEN = %q, want ghp_test123", envMap["GH_TOKEN"])
		}
		if envMap["SSH_AUTH_SOCK"] != "/tmp/ssh/agent.1234" {
			t.Errorf("SSH_AUTH_SOCK = %q, want /tmp/ssh/agent.1234", envMap["SSH_AUTH_SOCK"])
		}
	})

	t.Run("path_includes_pi_binary_dir", func(t *testing.T) {
		opts := runner.RunOpts{Phase: "implement", WorkDir: "/work"}
		env := adapter.BuildEnv(opts, "/tmp/sandbox", "")

		envMap := toEnvMap(env)
		pathVal := envMap["PATH"]
		piDir := filepath.Dir(adapter.Binary())
		if !strings.Contains(pathVal, piDir) {
			t.Errorf("PATH = %q, should contain %q", pathVal, piDir)
		}
	})
}

func TestPiAdapterParseOutput(t *testing.T) {
	binDir := t.TempDir()
	fakePi := filepath.Join(binDir, "pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewPiAdapter(fakePi)
	if err != nil {
		t.Fatalf("NewPiAdapter: %v", err)
	}

	t.Run("parses_valid_stream", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"assistant","content":"Hello"}`,
			`{"type":"message_end","usage":{"input_tokens":100,"output_tokens":50},"cost_usd":0.005}`,
			`{"type":"result","result":{"answer":"42"}}`,
		}, "\n")

		opts := runner.RunOpts{}
		result, err := adapter.ParseOutput([]byte(stream), opts)
		if err != nil {
			t.Fatalf("ParseOutput: %v", err)
		}

		if result.RawText != "Hello" {
			t.Errorf("RawText = %q, want %q", result.RawText, "Hello")
		}
		if result.TokensIn != 100 {
			t.Errorf("TokensIn = %d, want 100", result.TokensIn)
		}
		if string(result.Output) != `{"answer":"42"}` {
			t.Errorf("Output = %s, want %s", string(result.Output), `{"answer":"42"}`)
		}
	})

	t.Run("validates_output_schema", func(t *testing.T) {
		stream := `{"type":"result","result":{"ticket_key":"T-1"}}` + "\n"
		opts := runner.RunOpts{
			OutputSchema: `{"required":["ticket_key","verdict"]}`,
		}

		_, err := adapter.ParseOutput([]byte(stream), opts)
		if err == nil {
			t.Fatal("expected error for missing required field, got nil")
		}

		var pe *runner.ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected runner.ParseError, got %T: %v", err, err)
		}
		if !strings.Contains(pe.Error(), "verdict") {
			t.Errorf("error should mention missing field 'verdict', got: %v", pe)
		}
	})

	t.Run("maps_transient_error", func(t *testing.T) {
		stream := `{"type":"error","error":"rate limit exceeded"}` + "\n"
		opts := runner.RunOpts{}

		_, err := adapter.ParseOutput([]byte(stream), opts)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		var te *runner.TransientError
		if !errors.As(err, &te) {
			t.Fatalf("expected runner.TransientError, got %T: %v", err, err)
		}
	})
}

func TestPiAdapterExtraPaths(t *testing.T) {
	binDir := t.TempDir()
	fakePi := filepath.Join(binDir, "pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewPiAdapter(fakePi)
	if err != nil {
		t.Fatalf("NewPiAdapter: %v", err)
	}

	t.Run("includes_binary_directory", func(t *testing.T) {
		opts := runner.RunOpts{}
		read, write := adapter.ExtraPaths(opts)

		piDir := filepath.Dir(adapter.Binary())
		found := false
		for _, path := range read {
			if path == piDir {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("read paths %v should contain pi binary dir %q", read, piDir)
		}
		if len(write) != 0 {
			t.Errorf("write paths = %v, want empty", write)
		}
	})

	t.Run("includes_api_key_helper_dir", func(t *testing.T) {
		opts := runner.RunOpts{ApiKeyHelper: "/usr/local/bin/get-api-key"}
		read, _ := adapter.ExtraPaths(opts)

		found := false
		for _, path := range read {
			if path == "/usr/local/bin" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("read paths %v should contain /usr/local/bin for ApiKeyHelper", read)
		}
	})
}

func TestPiAdapterMCPExtraPaths(t *testing.T) {
	binDir := t.TempDir()
	fakePi := filepath.Join(binDir, "pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewPiAdapter(fakePi)
	if err != nil {
		t.Fatalf("NewPiAdapter: %v", err)
	}

	t.Run("always_returns_empty", func(t *testing.T) {
		servers := map[string]runner.MCPServerConfig{
			"jira":   {Command: "jira-mcp"},
			"github": {Command: "gh-mcp"},
		}
		read, write := adapter.MCPExtraPaths(servers)

		if len(read) != 0 {
			t.Errorf("read paths = %v, want empty", read)
		}
		if len(write) != 0 {
			t.Errorf("write paths = %v, want empty", write)
		}
	})

	t.Run("nil_input_returns_empty", func(t *testing.T) {
		read, write := adapter.MCPExtraPaths(nil)

		if len(read) != 0 {
			t.Errorf("read paths = %v, want empty for nil input", read)
		}
		if len(write) != 0 {
			t.Errorf("write paths = %v, want empty for nil input", write)
		}
	})
}

func TestMapPiParseError(t *testing.T) {
	t.Run("parse_error_passes_through", func(t *testing.T) {
		inner := fmt.Errorf("bad JSON")
		err := mapPiParseError(&runner.ParseError{Err: inner})

		var pe *runner.ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected runner.ParseError, got %T: %v", err, err)
		}
	})

	t.Run("semantic_error_passes_through", func(t *testing.T) {
		err := mapPiParseError(&runner.SemanticError{Message: "bad input"})

		var se *runner.SemanticError
		if !errors.As(err, &se) {
			t.Fatalf("expected runner.SemanticError, got %T: %v", err, err)
		}
	})

	t.Run("transient_error_passes_through", func(t *testing.T) {
		err := mapPiParseError(&runner.TransientError{Reason: "rate_limit", Err: fmt.Errorf("429")})

		var te *runner.TransientError
		if !errors.As(err, &te) {
			t.Fatalf("expected runner.TransientError, got %T: %v", err, err)
		}
	})

	t.Run("generic_error_becomes_parse_error", func(t *testing.T) {
		err := mapPiParseError(fmt.Errorf("something unexpected"))

		var pe *runner.ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected runner.ParseError, got %T: %v", err, err)
		}
	})
}

// toEnvMap converts a []string of "KEY=VALUE" entries to a map.
func toEnvMap(env []string) map[string]string {
	result := make(map[string]string, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

// assertContains checks that a string slice contains the given value.
func assertContains(t *testing.T, slice []string, value string) {
	t.Helper()
	for _, item := range slice {
		if item == value {
			return
		}
	}
	t.Errorf("slice %v does not contain %q", slice, value)
}

// assertContainsArgPair checks that args contains flag followed by value.
func assertContainsArgPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for idx, arg := range args {
		if arg == flag && idx+1 < len(args) && args[idx+1] == value {
			return
		}
	}
	t.Errorf("args %v does not contain %s %s", args, flag, value)
}
