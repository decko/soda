package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractPlanFiles(t *testing.T) {
	t.Run("extracts_unique_sorted_files", func(t *testing.T) {
		plan := `{
			"ticket_key": "TEST-1",
			"approach": "add feature",
			"tasks": [
				{"id":"T1","description":"add handler","files":["internal/handler.go","internal/handler_test.go"],"done_when":"compiles"},
				{"id":"T2","description":"add route","files":["internal/handler.go","cmd/main.go"],"done_when":"works"}
			],
			"verification": {"commands":["go test ./..."]}
		}`
		files, err := ExtractPlanFiles(json.RawMessage(plan))
		if err != nil {
			t.Fatalf("ExtractPlanFiles: %v", err)
		}
		want := []string{"cmd/main.go", "internal/handler.go", "internal/handler_test.go"}
		if len(files) != len(want) {
			t.Fatalf("got %d files, want %d: %v", len(files), len(want), files)
		}
		for i, f := range files {
			if f != want[i] {
				t.Errorf("files[%d] = %q, want %q", i, f, want[i])
			}
		}
	})

	t.Run("returns_nil_for_empty_input", func(t *testing.T) {
		files, err := ExtractPlanFiles(nil)
		if err != nil {
			t.Fatalf("ExtractPlanFiles: %v", err)
		}
		if files != nil {
			t.Errorf("expected nil, got %v", files)
		}
	})

	t.Run("returns_nil_for_no_tasks", func(t *testing.T) {
		plan := `{"ticket_key":"T","approach":"x","tasks":[],"verification":{"commands":[]}}`
		files, err := ExtractPlanFiles(json.RawMessage(plan))
		if err != nil {
			t.Fatalf("ExtractPlanFiles: %v", err)
		}
		if files != nil {
			t.Errorf("expected nil for empty tasks, got %v", files)
		}
	})

	t.Run("errors_on_invalid_json", func(t *testing.T) {
		_, err := ExtractPlanFiles(json.RawMessage(`{invalid`))
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}

func TestExtractGoSignatures(t *testing.T) {
	t.Run("extracts_function_signatures", func(t *testing.T) {
		dir := t.TempDir()
		src := `package example

import "context"

// Run executes the thing.
func Run(ctx context.Context, name string) error {
	return nil
}

func helper() {
}
`
		path := filepath.Join(dir, "example.go")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if len(sigs) != 2 {
			t.Fatalf("got %d signatures, want 2: %v", len(sigs), sigs)
		}
		if sigs[0] != "func Run(ctx context.Context, name string) error" {
			t.Errorf("sigs[0] = %q", sigs[0])
		}
		if sigs[1] != "func helper()" {
			t.Errorf("sigs[1] = %q", sigs[1])
		}
	})

	t.Run("extracts_method_signatures", func(t *testing.T) {
		dir := t.TempDir()
		src := `package example

type Engine struct{}

func (e *Engine) Start() error {
	return nil
}

func (e Engine) Name() string {
	return ""
}
`
		path := filepath.Join(dir, "engine.go")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if len(sigs) != 2 {
			t.Fatalf("got %d signatures, want 2: %v", len(sigs), sigs)
		}
		if sigs[0] != "func (e *Engine) Start() error" {
			t.Errorf("sigs[0] = %q", sigs[0])
		}
		if sigs[1] != "func (e Engine) Name() string" {
			t.Errorf("sigs[1] = %q", sigs[1])
		}
	})

	t.Run("handles_multiple_return_values", func(t *testing.T) {
		dir := t.TempDir()
		src := `package example

func Fetch(url string) ([]byte, error) {
	return nil, nil
}
`
		path := filepath.Join(dir, "fetch.go")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if len(sigs) != 1 {
			t.Fatalf("got %d signatures, want 1", len(sigs))
		}
		if sigs[0] != "func Fetch(url string) ([]byte, error)" {
			t.Errorf("sigs[0] = %q", sigs[0])
		}
	})

	t.Run("returns_nil_for_non_go_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "readme.md")
		if err := os.WriteFile(path, []byte("# readme"), 0644); err != nil {
			t.Fatal(err)
		}
		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if sigs != nil {
			t.Errorf("expected nil for non-Go file, got %v", sigs)
		}
	})

	t.Run("returns_nil_for_test_files", func(t *testing.T) {
		dir := t.TempDir()
		src := `package example

import "testing"

func TestFoo(t *testing.T) {}
`
		path := filepath.Join(dir, "foo_test.go")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if sigs != nil {
			t.Errorf("expected nil for test file, got %v", sigs)
		}
	})

	t.Run("handles_variadic_params", func(t *testing.T) {
		dir := t.TempDir()
		src := `package example

func Log(format string, args ...interface{}) {
}
`
		path := filepath.Join(dir, "log.go")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if len(sigs) != 1 {
			t.Fatalf("got %d signatures, want 1", len(sigs))
		}
		if sigs[0] != "func Log(format string, args ...interface{})" {
			t.Errorf("sigs[0] = %q", sigs[0])
		}
	})

	t.Run("handles_map_and_slice_types", func(t *testing.T) {
		dir := t.TempDir()
		src := `package example

func Process(items []string, lookup map[string]int) map[string][]int {
	return nil
}
`
		path := filepath.Join(dir, "types.go")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if len(sigs) != 1 {
			t.Fatalf("got %d signatures, want 1", len(sigs))
		}
		if sigs[0] != "func Process(items []string, lookup map[string]int) map[string][]int" {
			t.Errorf("sigs[0] = %q", sigs[0])
		}
	})

	t.Run("handles_func_type_params", func(t *testing.T) {
		dir := t.TempDir()
		src := `package example

func WithCallback(fn func(string) error) {
}
`
		path := filepath.Join(dir, "callback.go")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if len(sigs) != 1 {
			t.Fatalf("got %d signatures, want 1", len(sigs))
		}
		if sigs[0] != "func WithCallback(fn func(string) error)" {
			t.Errorf("sigs[0] = %q", sigs[0])
		}
	})

	t.Run("handles_channel_types", func(t *testing.T) {
		dir := t.TempDir()
		src := `package example

func Listen(ch <-chan string) chan int {
	return nil
}
`
		path := filepath.Join(dir, "chan.go")
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		sigs, err := ExtractGoSignatures(path)
		if err != nil {
			t.Fatalf("ExtractGoSignatures: %v", err)
		}
		if len(sigs) != 1 {
			t.Fatalf("got %d signatures, want 1", len(sigs))
		}
		if sigs[0] != "func Listen(ch <-chan string) chan int" {
			t.Errorf("sigs[0] = %q", sigs[0])
		}
	})
}

func TestBuildSiblingContext(t *testing.T) {
	t.Run("builds_context_from_plan_files", func(t *testing.T) {
		dir := t.TempDir()

		// Create Go source files in the temp dir.
		if err := os.MkdirAll(filepath.Join(dir, "internal"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "internal", "handler.go"), []byte(`package internal

func HandleRequest(w Writer, r *Request) error {
	return nil
}

func validateInput(input string) bool {
	return true
}
`), 0644); err != nil {
			t.Fatal(err)
		}

		plan := `{
			"ticket_key": "TEST-1",
			"approach": "add endpoint",
			"tasks": [
				{"id":"T1","description":"add handler","files":["internal/handler.go"],"done_when":"compiles"}
			],
			"verification": {"commands":["go test ./..."]}
		}`

		ctx := BuildSiblingContext(dir, json.RawMessage(plan), 0)
		if ctx == "" {
			t.Fatal("expected non-empty sibling context")
		}
		if !strings.Contains(ctx, "internal/handler.go") {
			t.Errorf("context should contain file path, got: %s", ctx)
		}
		if !strings.Contains(ctx, "HandleRequest") {
			t.Errorf("context should contain function name, got: %s", ctx)
		}
		if !strings.Contains(ctx, "validateInput") {
			t.Errorf("context should contain helper function, got: %s", ctx)
		}
	})

	t.Run("returns_empty_for_nonexistent_files", func(t *testing.T) {
		dir := t.TempDir()
		plan := `{
			"ticket_key": "TEST-1",
			"approach": "x",
			"tasks": [
				{"id":"T1","description":"x","files":["doesnotexist.go"],"done_when":"x"}
			],
			"verification": {"commands":[]}
		}`
		ctx := BuildSiblingContext(dir, json.RawMessage(plan), 0)
		if ctx != "" {
			t.Errorf("expected empty context for nonexistent files, got: %s", ctx)
		}
	})

	t.Run("returns_empty_for_nil_plan", func(t *testing.T) {
		ctx := BuildSiblingContext(t.TempDir(), nil, 0)
		if ctx != "" {
			t.Errorf("expected empty context for nil plan, got: %s", ctx)
		}
	})

	t.Run("skips_non_go_files", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Readme"), 0644); err != nil {
			t.Fatal(err)
		}
		plan := `{
			"ticket_key": "TEST-1",
			"approach": "x",
			"tasks": [
				{"id":"T1","description":"x","files":["readme.md"],"done_when":"x"}
			],
			"verification": {"commands":[]}
		}`
		ctx := BuildSiblingContext(dir, json.RawMessage(plan), 0)
		if ctx != "" {
			t.Errorf("expected empty context for non-Go files, got: %s", ctx)
		}
	})

	t.Run("includes_test_file_patterns", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "handler_test.go"), []byte(`package x
import "testing"
func TestFoo(t *testing.T) {}
`), 0644); err != nil {
			t.Fatal(err)
		}
		plan := `{
			"ticket_key": "TEST-1",
			"approach": "x",
			"tasks": [
				{"id":"T1","description":"x","files":["handler_test.go"],"done_when":"x"}
			],
			"verification": {"commands":[]}
		}`
		ctx := BuildSiblingContext(dir, json.RawMessage(plan), 0)
		if ctx == "" {
			t.Errorf("expected non-empty context for test files, got empty")
		}
		if !strings.Contains(ctx, "TestFoo") {
			t.Errorf("expected test function in context, got: %s", ctx)
		}
	})

	t.Run("respects_max_size_limit", func(t *testing.T) {
		dir := t.TempDir()

		// Create a Go file with many functions to exceed the limit.
		var b strings.Builder
		b.WriteString("package big\n\n")
		for i := 0; i < 500; i++ {
			b.WriteString("func VeryLongFunctionName")
			b.WriteString(strings.Repeat("X", 50))
			b.WriteString("Number")
			b.WriteString(string(rune('A' + (i % 26))))
			// Use index to make names unique.
			b.WriteString(strings.Repeat("Y", i%20))
			b.WriteString("() {}\n\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(b.String()), 0644); err != nil {
			t.Fatal(err)
		}

		plan := `{
			"ticket_key": "TEST-1",
			"approach": "x",
			"tasks": [
				{"id":"T1","description":"x","files":["big.go"],"done_when":"x"}
			],
			"verification": {"commands":[]}
		}`
		ctx := BuildSiblingContext(dir, json.RawMessage(plan), 0)
		if len(ctx) > maxSiblingContextBytes+500 {
			t.Errorf("context too large: %d bytes, limit is %d", len(ctx), maxSiblingContextBytes)
		}
	})
}
