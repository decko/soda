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

func TestOpencodeAdapterBuildArgs(t *testing.T) {
	// Create a fake opencode binary so NewOpencodeAdapter can resolve it.
	binDir := t.TempDir()
	fakeOpencode := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(fakeOpencode, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewOpencodeAdapter(fakeOpencode)
	if err != nil {
		t.Fatalf("NewOpencodeAdapter: %v", err)
	}

	t.Run("basic_args", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			Phase:        "implement",
			Model:        "opencode-model-1",
			AllowedTools: []string{"Read", "Bash(git:*)"},
			UserPrompt:   "do the thing",
		}
		args, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		assertContains(t, args, "--print")
		assertContains(t, args, "--output-format")
		assertContains(t, args, "--dangerously-skip-permissions")
		assertContainsArgPair(t, args, "--model", "opencode-model-1")
		assertContainsArgPair(t, args, "--agent", "soda-implement")
		assertContainsArgPair(t, args, "--permissions", "read,bash(git:*)")
		assertContainsArgPair(t, args, "-p", "do the thing")
	})

	t.Run("writes_agent_file_to_tmpdir", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{
			Phase:        "triage",
			SystemPrompt: "You are a helpful assistant.",
			UserPrompt:   "hello",
		}
		_, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}

		agentPath := filepath.Join(tmpDir, ".opencode", "agent", "soda-triage.md")
		got, readErr := os.ReadFile(agentPath)
		if readErr != nil {
			t.Fatalf("agent file not written: %v", readErr)
		}
		if string(got) != opts.SystemPrompt {
			t.Errorf("agent file content = %q, want %q", string(got), opts.SystemPrompt)
		}
	})

	t.Run("no_model_flag_when_empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{Phase: "triage", UserPrompt: "hello"}
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

	t.Run("no_permissions_when_no_tools", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{Phase: "triage", UserPrompt: "hello"}
		args, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}
		for _, arg := range args {
			if arg == "--permissions" {
				t.Error("should not include --permissions when no tools")
			}
		}
	})

	t.Run("phase_with_slash", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := runner.RunOpts{Phase: "review/go-specialist", UserPrompt: "hello"}
		args, err := adapter.BuildArgs(opts, tmpDir)
		if err != nil {
			t.Fatalf("BuildArgs: %v", err)
		}
		assertContainsArgPair(t, args, "--agent", "soda-review-go-specialist")
	})
}

func TestOpencodeAdapterBuildEnv(t *testing.T) {
	binDir := t.TempDir()
	fakeOpencode := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(fakeOpencode, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewOpencodeAdapter(fakeOpencode)
	if err != nil {
		t.Fatalf("NewOpencodeAdapter: %v", err)
	}

	t.Run("direct_mode_passes_credentials", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
		t.Setenv("OPENCODE_API_KEY", "oc-key-123")
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
		if envMap["OPENCODE_API_KEY"] != "oc-key-123" {
			t.Errorf("OPENCODE_API_KEY = %q, want oc-key-123", envMap["OPENCODE_API_KEY"])
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

	t.Run("path_includes_opencode_binary_dir", func(t *testing.T) {
		opts := runner.RunOpts{Phase: "implement", WorkDir: "/work"}
		env := adapter.BuildEnv(opts, "/tmp/sandbox", "")

		envMap := toEnvMap(env)
		pathVal := envMap["PATH"]
		ocDir := filepath.Dir(adapter.Binary())
		if !strings.Contains(pathVal, ocDir) {
			t.Errorf("PATH = %q, should contain %q", pathVal, ocDir)
		}
	})
}

func TestOpencodeAdapterParseOutput(t *testing.T) {
	binDir := t.TempDir()
	fakeOpencode := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(fakeOpencode, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewOpencodeAdapter(fakeOpencode)
	if err != nil {
		t.Fatalf("NewOpencodeAdapter: %v", err)
	}

	t.Run("parses_valid_stream", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"type":"text","content":"Hello"}`,
			`{"type":"done","usage":{"input_tokens":100,"output_tokens":50},"cost_usd":0.005}`,
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

func TestOpencodeAdapterExtraPaths(t *testing.T) {
	binDir := t.TempDir()
	fakeOpencode := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(fakeOpencode, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	adapter, err := NewOpencodeAdapter(fakeOpencode)
	if err != nil {
		t.Fatalf("NewOpencodeAdapter: %v", err)
	}

	t.Run("includes_binary_directory", func(t *testing.T) {
		opts := runner.RunOpts{}
		read, write := adapter.ExtraPaths(opts)

		ocDir := filepath.Dir(adapter.Binary())
		found := false
		for _, path := range read {
			if path == ocDir {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("read paths %v should contain opencode binary dir %q", read, ocDir)
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

func TestMapOpencodeParseError(t *testing.T) {
	t.Run("parse_error_passes_through", func(t *testing.T) {
		inner := fmt.Errorf("bad JSON")
		err := mapOpencodeParseError(&runner.ParseError{Err: inner})

		var pe *runner.ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected runner.ParseError, got %T: %v", err, err)
		}
	})

	t.Run("semantic_error_passes_through", func(t *testing.T) {
		err := mapOpencodeParseError(&runner.SemanticError{Message: "bad input"})

		var se *runner.SemanticError
		if !errors.As(err, &se) {
			t.Fatalf("expected runner.SemanticError, got %T: %v", err, err)
		}
	})

	t.Run("transient_error_passes_through", func(t *testing.T) {
		err := mapOpencodeParseError(&runner.TransientError{Reason: "rate_limit", Err: fmt.Errorf("429")})

		var te *runner.TransientError
		if !errors.As(err, &te) {
			t.Fatalf("expected runner.TransientError, got %T: %v", err, err)
		}
	})

	t.Run("generic_error_becomes_parse_error", func(t *testing.T) {
		err := mapOpencodeParseError(fmt.Errorf("something unexpected"))

		var pe *runner.ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected runner.ParseError, got %T: %v", err, err)
		}
	})
}
