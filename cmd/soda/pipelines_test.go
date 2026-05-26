package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/decko/soda/internal/pipeline"
)

func TestDiscoverDirs(t *testing.T) {
	dirs, sources := discoverDirs()

	// Should have at least the working directory.
	if len(dirs) == 0 {
		t.Fatal("expected at least one directory")
	}
	if len(dirs) != len(sources) {
		t.Fatalf("dirs (%d) and sources (%d) length mismatch", len(dirs), len(sources))
	}
	if sources[0] != "local" {
		t.Errorf("first source = %q, want %q", sources[0], "local")
	}
}

func TestRunPipelines_EmbeddedDefault(t *testing.T) {
	// Run from a temp dir with no pipeline files — should show embedded default.
	t.Chdir(t.TempDir())

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "default") {
		t.Error("expected 'default' pipeline in output")
	}
	if !strings.Contains(output, "embedded") {
		t.Error("expected 'embedded' source in output")
	}
}

func TestRunPipelines_LocalPipelines(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create local pipeline files.
	phases := "phases:\n  - name: triage\n    prompt: prompts/triage.md\n    timeout: 1m\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "phases.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "phases-fast.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "default") {
		t.Error("expected 'default' pipeline in output")
	}
	if !strings.Contains(output, "fast") {
		t.Error("expected 'fast' pipeline in output")
	}
	if !strings.Contains(output, "local") {
		t.Error("expected 'local' source in output")
	}
}

func TestRunPipelines_EmbeddedAlternativePipelines(t *testing.T) {
	// Run from a temp dir with no pipeline files — should show all embedded
	// pipelines including quick-fix and docs-only.
	t.Chdir(t.TempDir())

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	for _, name := range []string{"default", "quick-fix", "docs-only"} {
		if !strings.Contains(output, name) {
			t.Errorf("expected %q pipeline in output, got:\n%s", name, output)
		}
	}
}

func TestRunPipelines_LocalOverrideHidesEmbedded(t *testing.T) {
	// When a local phases-quick-fix.yaml exists, it should show "local"
	// source rather than "embedded".
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create a local quick-fix pipeline file.
	phases := "phases:\n  - name: implement\n    prompt: prompts/implement.md\n    timeout: 1m\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "phases-quick-fix.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "quick-fix") {
		t.Error("expected quick-fix in output")
	}
	// The quick-fix entry should show "local" source, not "embedded".
	lines := strings.Split(output, "\n")
	quickFixLines := 0
	for _, line := range lines {
		// Match lines where quick-fix appears as the pipeline name (first column).
		if strings.HasPrefix(strings.TrimSpace(line), "quick-fix") {
			quickFixLines++
			if !strings.Contains(line, "local") {
				t.Errorf("expected 'local' source for quick-fix, got: %s", line)
			}
		}
	}
	if quickFixLines != 1 {
		t.Errorf("expected exactly 1 quick-fix row, got %d", quickFixLines)
	}
}

func TestResolvePhasesPath_EmbeddedQuickFix(t *testing.T) {
	t.Chdir(t.TempDir())
	path, cleanup, err := resolvePhasesPath("quick-fix", "", "")
	if err != nil {
		t.Fatalf("resolvePhasesPath(quick-fix) failed: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	pl, err := pipeline.LoadPipeline(path)
	if err != nil {
		t.Fatalf("LoadPipeline failed: %v", err)
	}

	if len(pl.Phases) != 3 {
		t.Fatalf("quick-fix: expected 3 phases, got %d", len(pl.Phases))
	}

	// Verify phase names and order.
	wantNames := []string{"implement", "verify", "submit"}
	for i, want := range wantNames {
		if pl.Phases[i].Name != want {
			t.Errorf("phase[%d] = %q, want %q", i, pl.Phases[i].Name, want)
		}
	}
}

func TestResolvePhasesPath_EmbeddedDocsOnly(t *testing.T) {
	t.Chdir(t.TempDir())
	path, cleanup, err := resolvePhasesPath("docs-only", "", "")
	if err != nil {
		t.Fatalf("resolvePhasesPath(docs-only) failed: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	pl, err := pipeline.LoadPipeline(path)
	if err != nil {
		t.Fatalf("LoadPipeline failed: %v", err)
	}

	if len(pl.Phases) != 3 {
		t.Fatalf("docs-only: expected 3 phases, got %d", len(pl.Phases))
	}

	// Verify phase names and order.
	wantNames := []string{"plan", "implement", "submit"}
	for i, want := range wantNames {
		if pl.Phases[i].Name != want {
			t.Errorf("phase[%d] = %q, want %q", i, pl.Phases[i].Name, want)
		}
	}
}

func TestResolvePhasesPath_UnknownPipeline(t *testing.T) {
	t.Chdir(t.TempDir())
	_, _, err := resolvePhasesPath("nonexistent-pipeline", "", "")
	if err == nil {
		t.Fatal("expected error for unknown pipeline, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestResolvePhasesPath_DependsOnValidation(t *testing.T) {
	// Verify that both embedded pipelines pass depends_on cross-reference
	// validation (which happens inside LoadPipeline).
	for _, name := range []string{"quick-fix", "docs-only"} {
		t.Run(name, func(t *testing.T) {
			path, cleanup, err := resolvePhasesPath(name, "", "")
			if err != nil {
				t.Fatalf("resolvePhasesPath(%s) failed: %v", name, err)
			}
			if cleanup != nil {
				defer cleanup()
			}

			_, err = pipeline.LoadPipeline(path)
			if err != nil {
				t.Fatalf("LoadPipeline(%s) failed depends_on validation: %v", name, err)
			}
		})
	}
}

func TestKnownEmbeddedPipelines_AllDiscoverable(t *testing.T) {
	// Verify every entry in knownEmbeddedPipelines is resolvable.
	var names []string
	for name := range knownEmbeddedPipelines {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path, cleanup, err := resolvePhasesPath(name, "", "")
			if err != nil {
				t.Fatalf("resolvePhasesPath(%s) failed: %v", name, err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			if path == "" {
				t.Fatalf("resolvePhasesPath(%s) returned empty path", name)
			}
		})
	}
}

func TestRunPipelines_PipelinesDirDiscovery(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create a soda.yaml with pipelines_path set.
	cfgContent := "ticket_source: github\npipelines_path: .pipelines/\n"
	cfgPath := filepath.Join(tmpDir, "soda.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .pipelines/ with a named pipeline.
	pipelinesDir := filepath.Join(tmpDir, ".pipelines")
	if err := os.MkdirAll(pipelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	phases := "phases:\n  - name: triage\n    prompt: prompts/triage.md\n    timeout: 1m\n"
	if err := os.WriteFile(filepath.Join(pipelinesDir, "my-custom.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}

	rootCmd := newRootCmd()
	rootCmd.SetArgs([]string{"pipelines", "--config", cfgPath})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "my-custom") {
		t.Errorf("expected my-custom pipeline in output, got:\n%s", output)
	}
	if !strings.Contains(output, "pipelines-dir") {
		t.Errorf("expected pipelines-dir source in output, got:\n%s", output)
	}
}

func TestRunPipelines_PipelinesDirNoPipelinesPath(t *testing.T) {
	// When no pipelines_path is configured, only CWD and embedded pipelines
	// should appear.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	// Should NOT contain pipelines-dir source.
	if strings.Contains(output, "pipelines-dir") {
		t.Errorf("expected no pipelines-dir source without config, got:\n%s", output)
	}
	// Should still show embedded default.
	if !strings.Contains(output, "default") {
		t.Errorf("expected default pipeline in output, got:\n%s", output)
	}
}

func TestRunPipelines_PipelinesDirDoesNotDuplicate(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create a soda.yaml with pipelines_path set.
	cfgContent := "ticket_source: github\npipelines_path: .pipelines/\n"
	cfgPath := filepath.Join(tmpDir, "soda.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .pipelines/ with a pipeline that also exists in CWD.
	pipelinesDir := filepath.Join(tmpDir, ".pipelines")
	if err := os.MkdirAll(pipelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	phases := "phases:\n  - name: triage\n    prompt: prompts/triage.md\n    timeout: 1m\n"
	// Put a "fast" pipeline in both .pipelines/ and CWD (phases-fast.yaml).
	if err := os.WriteFile(filepath.Join(pipelinesDir, "fast.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "phases-fast.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}

	rootCmd := newRootCmd()
	rootCmd.SetArgs([]string{"pipelines", "--config", cfgPath})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	// "fast" should appear exactly once.
	fastCount := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "fast") {
			fastCount++
		}
	}
	if fastCount != 1 {
		t.Errorf("expected exactly 1 'fast' pipeline row, got %d:\n%s", fastCount, output)
	}
}

func TestResolvePhasesPath_PipelinesPathDefault(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .pipelines/default.yaml
	pipelinesDir := filepath.Join(tmpDir, ".pipelines")
	if err := os.MkdirAll(pipelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	phases := "phases:\n  - name: triage\n    prompt: prompts/triage.md\n    timeout: 1m\n"
	if err := os.WriteFile(filepath.Join(pipelinesDir, "default.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}

	path, cleanup, err := resolvePhasesPath("", "", pipelinesDir)
	if err != nil {
		t.Fatalf("resolvePhasesPath with pipelinesPath failed: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	wantPath := filepath.Join(pipelinesDir, "default.yaml")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}
}

func TestResolvePhasesPath_PipelinesPathNamed(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .pipelines/fast.yaml
	pipelinesDir := filepath.Join(tmpDir, ".pipelines")
	if err := os.MkdirAll(pipelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	phases := "phases:\n  - name: implement\n    prompt: prompts/implement.md\n    timeout: 15m\n"
	if err := os.WriteFile(filepath.Join(pipelinesDir, "fast.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}

	path, cleanup, err := resolvePhasesPath("fast", "", pipelinesDir)
	if err != nil {
		t.Fatalf("resolvePhasesPath with pipelinesPath named failed: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	wantPath := filepath.Join(pipelinesDir, "fast.yaml")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}
}

func TestResolvePhasesPath_PipelinesPathFallsThrough(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .pipelines/ dir but without the requested pipeline.
	pipelinesDir := filepath.Join(tmpDir, ".pipelines")
	if err := os.MkdirAll(pipelinesDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create phases.yaml in CWD — should fall through to CWD resolution.
	phases := "phases:\n  - name: triage\n    prompt: prompts/triage.md\n    timeout: 1m\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "phases.yaml"), []byte(phases), 0644); err != nil {
		t.Fatal(err)
	}

	path, cleanup, err := resolvePhasesPath("", "", pipelinesDir)
	if err != nil {
		t.Fatalf("resolvePhasesPath should fall through: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Should resolve to the CWD phases.yaml, not the pipelines dir.
	if !strings.HasSuffix(path, "phases.yaml") {
		t.Errorf("path = %q, want CWD phases.yaml", path)
	}
}

func TestRunPipelines_TableHeader(t *testing.T) {
	t.Chdir(t.TempDir())

	cmd := newPipelinesCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipelines command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "NAME") {
		t.Error("expected NAME header column")
	}
	if !strings.Contains(output, "SOURCE") {
		t.Error("expected SOURCE header column")
	}
	if !strings.Contains(output, "PATH") {
		t.Error("expected PATH header column")
	}
}
