package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

		// Create 10 small Go files, each individually small enough to fit
		// but collectively exceeding a tight budget.
		var fileNames []string
		for i := 0; i < 10; i++ {
			fname := fmt.Sprintf("f%d.go", i)
			fileNames = append(fileNames, fname)
			src := fmt.Sprintf("package x\nfunc Handler%d() error { return nil }\n", i)
			if err := os.WriteFile(filepath.Join(dir, fname), []byte(src), 0644); err != nil {
				t.Fatal(err)
			}
		}

		// Build a plan referencing all 10 files.
		var taskFiles []string
		for _, f := range fileNames {
			taskFiles = append(taskFiles, fmt.Sprintf("%q", f))
		}
		plan := fmt.Sprintf(`{
			"ticket_key": "TEST-1",
			"approach": "x",
			"tasks": [
				{"id":"T1","description":"x","files":[%s],"done_when":"x"}
			],
			"verification": {"commands":[]}
		}`, strings.Join(taskFiles, ","))

		const budget = 200
		ctx := BuildSiblingContext(dir, json.RawMessage(plan), budget)
		if ctx == "" {
			t.Fatal("expected some output under budget")
		}
		if len(ctx) > budget {
			t.Errorf("exceeded budget: %d > %d", len(ctx), budget)
		}
		// Not all 10 files should be present — the budget should have excluded some.
		if strings.Count(ctx, "### f") >= 10 {
			t.Error("budget should have excluded some files")
		}
	})
}

func TestExtractGoFunctionBodies(t *testing.T) {
	t.Run("extracts_function_bodies", func(t *testing.T) {
		dir := t.TempDir()
		src := `package main

func Add(a, b int) int {
	return a + b
}

func Multiply(a, b int) int {
	result := a * b
	return result
}
`
		path := filepath.Join(dir, "math.go")
		os.WriteFile(path, []byte(src), 0644)

		bodies, err := ExtractGoFunctionBodies(path)
		if err != nil {
			t.Fatalf("ExtractGoFunctionBodies: %v", err)
		}
		if len(bodies) != 2 {
			t.Fatalf("got %d bodies, want 2", len(bodies))
		}
		if !strings.Contains(bodies[0].Body, "return a + b") {
			t.Errorf("first body missing 'return a + b': %q", bodies[0].Body)
		}
		if !strings.Contains(bodies[1].Body, "result := a * b") {
			t.Errorf("second body missing 'result := a * b': %q", bodies[1].Body)
		}
	})

	t.Run("extracts_method_bodies", func(t *testing.T) {
		dir := t.TempDir()
		src := `package main

type Server struct{}

func (s *Server) Start() error {
	return nil
}
`
		path := filepath.Join(dir, "server.go")
		os.WriteFile(path, []byte(src), 0644)

		bodies, err := ExtractGoFunctionBodies(path)
		if err != nil {
			t.Fatalf("ExtractGoFunctionBodies: %v", err)
		}
		if len(bodies) != 1 {
			t.Fatalf("got %d bodies, want 1", len(bodies))
		}
		if !strings.Contains(bodies[0].Name, "Server") {
			t.Errorf("name should contain receiver: %q", bodies[0].Name)
		}
		if !strings.Contains(bodies[0].Body, "return nil") {
			t.Errorf("body missing 'return nil': %q", bodies[0].Body)
		}
	})

	t.Run("skips_test_files", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "math_test.go")
		os.WriteFile(path, []byte("package main\nfunc TestAdd(t *testing.T) {}\n"), 0644)

		bodies, err := ExtractGoFunctionBodies(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bodies != nil {
			t.Error("expected nil for test files")
		}
	})

	t.Run("skips_non_go_files", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "readme.md")
		os.WriteFile(path, []byte("# Readme"), 0644)

		bodies, err := ExtractGoFunctionBodies(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bodies != nil {
			t.Error("expected nil for non-Go files")
		}
	})
}

func TestBuildSiblingContext_BodiesInjected(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)

	src := `package pkg

func Process(data string) error {
	if data == "" {
		return fmt.Errorf("empty")
	}
	return nil
}

func helper() string {
	return "ok"
}
`
	os.WriteFile(filepath.Join(dir, "pkg", "process.go"), []byte(src), 0644)

	plan := `{
		"ticket_key": "TEST-1",
		"approach": "fix",
		"tasks": [{"id":"T1","description":"fix process","files":["pkg/process.go"],"done_when":"ok"}],
		"verification": {"commands":["go test"]}
	}`

	ctx := BuildSiblingContext(dir, json.RawMessage(plan), 0)
	if ctx == "" {
		t.Fatal("expected non-empty context")
	}
	// Should contain full function bodies, not just signatures.
	if !strings.Contains(ctx, "return fmt.Errorf") {
		t.Errorf("context should contain function body, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "return \"ok\"") {
		t.Errorf("context should contain helper body, got:\n%s", ctx)
	}
	// Should be wrapped in code fence.
	if !strings.Contains(ctx, "```go") {
		t.Errorf("context should contain code fence, got:\n%s", ctx)
	}
}

func TestBuildSiblingContext_FallsBackToSignatures(t *testing.T) {
	dir := t.TempDir()

	// Create a file with enough functions that bodies exceed a small budget.
	var src strings.Builder
	src.WriteString("package main\n\n")
	for i := 0; i < 20; i++ {
		src.WriteString(fmt.Sprintf("func Func%d() {\n\t// body line %d\n}\n\n", i, i))
	}
	os.WriteFile(filepath.Join(dir, "big.go"), []byte(src.String()), 0644)

	plan := `{
		"ticket_key": "TEST-1",
		"approach": "x",
		"tasks": [{"id":"T1","description":"x","files":["big.go"],"done_when":"x"}],
		"verification": {"commands":[]}
	}`

	// Budget that fits signatures but not full bodies.
	ctx := BuildSiblingContext(dir, json.RawMessage(plan), 500)
	if ctx == "" {
		t.Fatal("expected non-empty context (signatures fallback)")
	}
	// Should have signatures (backtick-quoted), not code fences.
	if strings.Contains(ctx, "```go") {
		t.Error("should have fallen back to signatures, but got code fence (bodies)")
	}
	if !strings.Contains(ctx, "Func0") {
		t.Errorf("context should contain function names: %s", ctx)
	}
}

func TestIsNewFile(t *testing.T) {
	t.Run("committed_file_returns_false", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		// Create and commit a file.
		fpath := filepath.Join(dir, "existing.go")
		os.WriteFile(fpath, []byte("package main\n"), 0644)
		gitAdd(t, dir, "existing.go")
		gitCommit(t, dir, "add existing.go")

		if isNewFile(context.Background(), dir, "existing.go", "main") {
			t.Error("committed file should return false")
		}
	})

	t.Run("uncommitted_file_returns_true", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		if !isNewFile(context.Background(), dir, "newfile.go", "main") {
			t.Error("uncommitted file should return true")
		}
	})

	t.Run("non_repo_workdir_returns_true", func(t *testing.T) {
		dir := t.TempDir()
		if !isNewFile(context.Background(), dir, "anything.go", "main") {
			t.Error("non-repo workdir should return true (graceful)")
		}
	})
}

func TestIsGeneratedGoFile(t *testing.T) {
	t.Run("generated_file_returns_true", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gen.go")
		os.WriteFile(path, []byte("// Code generated by tool; DO NOT EDIT.\npackage x\n"), 0644)

		if !isGeneratedGoFile(path) {
			t.Error("expected true for generated file")
		}
	})

	t.Run("normal_file_returns_false", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "normal.go")
		os.WriteFile(path, []byte("package x\n\nfunc Foo() {}\n"), 0644)

		if isGeneratedGoFile(path) {
			t.Error("expected false for normal file")
		}
	})

	t.Run("nonexistent_file_returns_false", func(t *testing.T) {
		if isGeneratedGoFile("/nonexistent/path.go") {
			t.Error("expected false for nonexistent file")
		}
	})
}

func TestFindExemplarFiles(t *testing.T) {
	t.Run("returns_go_files_sorted_by_mtime", func(t *testing.T) {
		dir := t.TempDir()

		// Create files with distinct mtimes.
		for i, name := range []string{"alpha.go", "beta.go", "gamma.go"} {
			path := filepath.Join(dir, name)
			os.WriteFile(path, []byte("package x\nfunc F() {}\n"), 0644)
			// Set mtime with increasing values.
			mtime := time.Now().Add(time.Duration(i) * time.Second)
			os.Chtimes(path, mtime, mtime)
		}

		files := findExemplarFiles(dir, 10)
		if len(files) != 3 {
			t.Fatalf("expected 3 files, got %d: %v", len(files), files)
		}
		// gamma.go has the newest mtime, should be first.
		if !strings.HasSuffix(files[0], "gamma.go") {
			t.Errorf("expected newest file first, got %s", files[0])
		}
	})

	t.Run("skips_test_files", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package x\nfunc H() {}\n"), 0644)
		os.WriteFile(filepath.Join(dir, "handler_test.go"), []byte("package x\nfunc TestH() {}\n"), 0644)

		files := findExemplarFiles(dir, 10)
		if len(files) != 1 {
			t.Fatalf("expected 1 file (test skipped), got %d", len(files))
		}
		if strings.Contains(files[0], "_test.go") {
			t.Error("should not include test files")
		}
	})

	t.Run("skips_generated_files", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "gen.go"), []byte("// Code generated\npackage x\n"), 0644)
		os.WriteFile(filepath.Join(dir, "real.go"), []byte("package x\nfunc R() {}\n"), 0644)

		files := findExemplarFiles(dir, 10)
		if len(files) != 1 {
			t.Fatalf("expected 1 file (generated skipped), got %d", len(files))
		}
		if strings.Contains(files[0], "gen.go") {
			t.Error("should not include generated files")
		}
	})

	t.Run("respects_maxFiles", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 10; i++ {
			name := fmt.Sprintf("f%d.go", i)
			os.WriteFile(filepath.Join(dir, name), []byte("package x\nfunc F() {}\n"), 0644)
		}

		files := findExemplarFiles(dir, 3)
		if len(files) != 3 {
			t.Fatalf("expected 3 files, got %d", len(files))
		}
	})

	t.Run("returns_nil_for_nonexistent_dir", func(t *testing.T) {
		files := findExemplarFiles("/nonexistent/dir", 10)
		if files != nil {
			t.Errorf("expected nil, got %v", files)
		}
	})
}

func TestBuildPackageExemplars(t *testing.T) {
	t.Run("new_file_in_package_with_siblings", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		// Create and commit an existing sibling file.
		os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
		os.WriteFile(filepath.Join(dir, "pkg", "existing.go"), []byte(`package pkg

func ExistingFunc(s string) error {
	return nil
}
`), 0644)
		gitAdd(t, dir, "pkg/existing.go")
		gitCommit(t, dir, "add existing.go")

		// Plan references a new file in the same package.
		plan := `{
			"ticket_key": "TEST-1",
			"approach": "add feature",
			"tasks": [
				{"id":"T1","description":"add new handler","files":["pkg/new_handler.go"],"done_when":"compiles"}
			],
			"verification": {"commands":["go test ./..."]}
		}`

		result := BuildPackageExemplars(context.Background(), dir, json.RawMessage(plan), "main", 0)
		if result == "" {
			t.Fatal("expected non-empty exemplar context")
		}
		if !strings.Contains(result, "ExistingFunc") {
			t.Errorf("should contain sibling signatures, got: %s", result)
		}
	})

	t.Run("all_modified_files_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		// Create and commit a file — it's modified, not new.
		os.WriteFile(filepath.Join(dir, "existing.go"), []byte("package main\nfunc F() {}\n"), 0644)
		gitAdd(t, dir, "existing.go")
		gitCommit(t, dir, "add existing.go")

		plan := `{
			"ticket_key": "TEST-1",
			"approach": "fix",
			"tasks": [
				{"id":"T1","description":"fix","files":["existing.go"],"done_when":"ok"}
			],
			"verification": {"commands":[]}
		}`

		result := BuildPackageExemplars(context.Background(), dir, json.RawMessage(plan), "main", 0)
		if result != "" {
			t.Errorf("expected empty for all-modified files, got: %s", result)
		}
	})

	t.Run("test_only_package_dir_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		// Package dir only has test files.
		os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
		os.WriteFile(filepath.Join(dir, "pkg", "handler_test.go"), []byte("package pkg\nfunc TestH() {}\n"), 0644)
		gitAdd(t, dir, "pkg/handler_test.go")
		gitCommit(t, dir, "add test file")

		plan := `{
			"ticket_key": "TEST-1",
			"approach": "add",
			"tasks": [
				{"id":"T1","description":"add","files":["pkg/new.go"],"done_when":"ok"}
			],
			"verification": {"commands":[]}
		}`

		result := BuildPackageExemplars(context.Background(), dir, json.RawMessage(plan), "main", 0)
		if result != "" {
			t.Errorf("expected empty for test-only dir, got: %s", result)
		}
	})

	t.Run("generated_only_dir_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
		os.WriteFile(filepath.Join(dir, "pkg", "gen.go"), []byte("// Code generated\npackage pkg\nfunc G() {}\n"), 0644)
		gitAdd(t, dir, "pkg/gen.go")
		gitCommit(t, dir, "add generated file")

		plan := `{
			"ticket_key": "TEST-1",
			"approach": "add",
			"tasks": [
				{"id":"T1","description":"add","files":["pkg/new.go"],"done_when":"ok"}
			],
			"verification": {"commands":[]}
		}`

		result := BuildPackageExemplars(context.Background(), dir, json.RawMessage(plan), "main", 0)
		if result != "" {
			t.Errorf("expected empty for generated-only dir, got: %s", result)
		}
	})

	t.Run("budget_cap_respected", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
		// Create a file with many functions.
		var src strings.Builder
		src.WriteString("package pkg\n\n")
		for i := 0; i < 50; i++ {
			src.WriteString(fmt.Sprintf("func Func%d() error { return nil }\n\n", i))
		}
		os.WriteFile(filepath.Join(dir, "pkg", "big.go"), []byte(src.String()), 0644)
		gitAdd(t, dir, "pkg/big.go")
		gitCommit(t, dir, "add big file")

		plan := `{
			"ticket_key": "TEST-1",
			"approach": "add",
			"tasks": [
				{"id":"T1","description":"add","files":["pkg/new.go"],"done_when":"ok"}
			],
			"verification": {"commands":[]}
		}`

		const budget = 200
		result := BuildPackageExemplars(context.Background(), dir, json.RawMessage(plan), "main", budget)
		if len(result) > budget {
			t.Errorf("exceeded budget: %d > %d", len(result), budget)
		}
	})

	t.Run("nil_plan_returns_empty", func(t *testing.T) {
		result := BuildPackageExemplars(context.Background(), t.TempDir(), nil, "main", 0)
		if result != "" {
			t.Errorf("expected empty for nil plan, got: %s", result)
		}
	})
}

// gitAdd stages a file in the git repo at dir.
func gitAdd(t *testing.T, dir, file string) {
	t.Helper()
	cmd := exec.Command("git", "add", file)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %s: %v", file, out, err)
	}
}

// gitCommit creates a commit in the git repo at dir.
func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %s: %v", out, err)
	}
}
