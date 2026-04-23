package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/pipeline"
)

func TestRunPipelineNew_CreatesFile(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "hotfix", pipelineNewOptions{OutputDir: dir})
	if err != nil {
		t.Fatalf("runPipelineNew() error: %v", err)
	}

	destPath := filepath.Join(dir, "phases-hotfix.yaml")
	info, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat created file: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("created file is empty")
	}

	// Output should mention the file and the run command.
	output := buf.String()
	if !strings.Contains(output, "hotfix") {
		t.Errorf("output should mention pipeline name, got: %s", output)
	}
	if !strings.Contains(output, "--pipeline hotfix") {
		t.Errorf("output should show run command, got: %s", output)
	}
}

func TestRunPipelineNew_ValidYAML(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "ci-lite", pipelineNewOptions{OutputDir: dir})
	if err != nil {
		t.Fatalf("runPipelineNew() error: %v", err)
	}

	// The generated file should be loadable as a valid pipeline.
	destPath := filepath.Join(dir, "phases-ci-lite.yaml")
	pl, err := pipeline.LoadPipeline(destPath)
	if err != nil {
		t.Fatalf("LoadPipeline() on scaffold: %v", err)
	}

	if len(pl.Phases) != 3 {
		t.Fatalf("expected 3 phases, got %d", len(pl.Phases))
	}

	wantPhases := []string{"implement", "verify", "submit"}
	for idx, want := range wantPhases {
		if pl.Phases[idx].Name != want {
			t.Errorf("phase[%d] = %q, want %q", idx, pl.Phases[idx].Name, want)
		}
	}
}

func TestRunPipelineNew_NameInHeader(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "experiment", pipelineNewOptions{OutputDir: dir})
	if err != nil {
		t.Fatalf("runPipelineNew() error: %v", err)
	}

	destPath := filepath.Join(dir, "phases-experiment.yaml")
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "# SODA Pipeline: experiment") {
		t.Errorf("header should contain pipeline name, got:\n%s", content)
	}
	if !strings.Contains(content, "--pipeline experiment") {
		t.Errorf("usage comment should contain pipeline name, got:\n%s", content)
	}
}

func TestRunPipelineNew_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "phases-hotfix.yaml")
	if err := os.WriteFile(existing, []byte("existing content"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "hotfix", pipelineNewOptions{OutputDir: dir})
	if err == nil {
		t.Fatal("expected error when file exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want 'already exists'", err.Error())
	}

	// Original content must be preserved.
	data, _ := os.ReadFile(existing)
	if string(data) != "existing content" {
		t.Errorf("file was modified: got %q", string(data))
	}
}

func TestRunPipelineNew_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "phases-hotfix.yaml")
	if err := os.WriteFile(existing, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "hotfix", pipelineNewOptions{OutputDir: dir, Force: true})
	if err != nil {
		t.Fatalf("runPipelineNew(force=true) error: %v", err)
	}

	// File should be overwritten with valid pipeline content.
	pl, err := pipeline.LoadPipeline(existing)
	if err != nil {
		t.Fatalf("LoadPipeline() after force overwrite: %v", err)
	}
	if len(pl.Phases) != 3 {
		t.Errorf("expected 3 phases after overwrite, got %d", len(pl.Phases))
	}
}

func TestRunPipelineNew_DryRun(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "experiment", pipelineNewOptions{OutputDir: dir, DryRun: true})
	if err != nil {
		t.Fatalf("runPipelineNew(dryRun=true) error: %v", err)
	}

	// File must NOT be written.
	destPath := filepath.Join(dir, "phases-experiment.yaml")
	if _, err := os.Stat(destPath); err == nil {
		t.Fatal("dry-run should not write a file")
	}

	// Output should contain the scaffold YAML.
	output := buf.String()
	if !strings.Contains(output, "phases:") {
		t.Errorf("dry-run output missing phases key, got: %s", output)
	}
	if !strings.Contains(output, "experiment") {
		t.Errorf("dry-run output missing pipeline name, got: %s", output)
	}
}

func TestRunPipelineNew_RejectsEmptyName(t *testing.T) {
	tests := []struct {
		name string
	}{
		{""},
		{" "},
		{"  \t "},
	}
	for _, tt := range tests {
		t.Run("empty_"+tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := runPipelineNew(&buf, tt.name, pipelineNewOptions{})
			if err == nil {
				t.Fatalf("expected error for empty name %q, got nil", tt.name)
			}
			if !strings.Contains(err.Error(), "name must not be empty") {
				t.Errorf("error = %q, want 'name must not be empty'", err.Error())
			}
		})
	}
}

func TestRunPipelineNew_RejectsDefault(t *testing.T) {
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "default", pipelineNewOptions{})
	if err == nil {
		t.Fatal("expected error for 'default' name, got nil")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error = %q, want 'reserved'", err.Error())
	}
}

func TestRunPipelineNew_RejectsInvalidName(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"foo/bar"},
		{"foo\\bar"},
		{"../etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := runPipelineNew(&buf, tt.name, pipelineNewOptions{})
			if err == nil {
				t.Fatalf("expected error for invalid name %q, got nil", tt.name)
			}
		})
	}
}

func TestRunPipelineNew_CustomDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "config", "pipelines")

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "fast", pipelineNewOptions{OutputDir: subdir})
	if err != nil {
		t.Fatalf("runPipelineNew() error: %v", err)
	}

	destPath := filepath.Join(subdir, "phases-fast.yaml")
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("file not created in custom dir: %v", err)
	}
}

func TestNewPipelineCmd_Structure(t *testing.T) {
	cmd := newPipelineCmd()

	if cmd.Use != "pipeline" {
		t.Errorf("Use = %q, want %q", cmd.Use, "pipeline")
	}

	// Should have a "new" subcommand.
	subCmds := cmd.Commands()
	found := false
	for _, sub := range subCmds {
		if sub.Use == "new <name>" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'new' subcommand")
	}
}

func TestNewPipelineNewCmd_RequiresName(t *testing.T) {
	cmd := newPipelineNewCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no name provided, got nil")
	}
}

func TestRenderPipelineScaffold_ContainsPhases(t *testing.T) {
	content, err := renderPipelineScaffold("test-pipeline")
	if err != nil {
		t.Fatalf("renderPipelineScaffold() error: %v", err)
	}

	for _, want := range []string{"implement", "verify", "submit", "test-pipeline"} {
		if !strings.Contains(content, want) {
			t.Errorf("scaffold should contain %q, got:\n%s", want, content)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests for --from flag
// ---------------------------------------------------------------------------

func TestRunPipelineNew_FromFilePath(t *testing.T) {
	// Write a minimal source pipeline to a temp file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "phases-src.yaml")
	srcContent := `phases:
  - name: build
    prompt: prompts/build.md
    tools:
      - Bash
    timeout: 5m
    retry:
      transient: 1
      parse: 0
      semantic: 0
    depends_on: []
`
	if err := os.WriteFile(srcFile, []byte(srcContent), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "mybuild", pipelineNewOptions{
		OutputDir: outDir,
		From:      srcFile,
	})
	if err != nil {
		t.Fatalf("runPipelineNew(from=file) error: %v", err)
	}

	dest := filepath.Join(outDir, "phases-mybuild.yaml")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	content := string(data)

	// Header should use the new pipeline name.
	if !strings.Contains(content, "# SODA Pipeline: mybuild") {
		t.Errorf("header missing new name, got:\n%s", content)
	}
	// Should reference the from source.
	if !strings.Contains(content, srcFile) {
		t.Errorf("header should reference source file, got:\n%s", content)
	}
	// The build phase from the source should appear.
	if !strings.Contains(content, "build") {
		t.Errorf("generated content should include 'build' phase, got:\n%s", content)
	}
}

func TestRunPipelineNew_FromEmbeddedName(t *testing.T) {
	outDir := t.TempDir()
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "myquick", pipelineNewOptions{
		OutputDir: outDir,
		From:      "quick-fix",
	})
	if err != nil {
		t.Fatalf("runPipelineNew(from=quick-fix) error: %v", err)
	}

	dest := filepath.Join(outDir, "phases-myquick.yaml")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "# SODA Pipeline: myquick") {
		t.Errorf("header missing new name, got:\n%s", content)
	}
	// quick-fix has implement, verify, submit phases.
	for _, phase := range []string{"implement", "verify", "submit"} {
		if !strings.Contains(content, phase) {
			t.Errorf("expected phase %q in output, got:\n%s", phase, content)
		}
	}
}

func TestRunPipelineNew_FromDefaultEmbedded(t *testing.T) {
	outDir := t.TempDir()
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "custom-default", pipelineNewOptions{
		OutputDir: outDir,
		From:      "default",
	})
	if err != nil {
		t.Fatalf("runPipelineNew(from=default) error: %v", err)
	}

	dest := filepath.Join(outDir, "phases-custom-default.yaml")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if !strings.Contains(string(data), "# SODA Pipeline: custom-default") {
		t.Errorf("header missing new name")
	}
}

func TestRunPipelineNew_FromInvalidSource(t *testing.T) {
	var buf bytes.Buffer
	// Non-existent file path.
	err := runPipelineNew(&buf, "test", pipelineNewOptions{
		From: "/nonexistent/path/phases-missing.yaml",
	})
	if err == nil {
		t.Fatal("expected error for missing source file, got nil")
	}
}

func TestRunPipelineNew_FromUnknownEmbeddedName(t *testing.T) {
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "test", pipelineNewOptions{
		From: "no-such-pipeline",
	})
	if err == nil {
		t.Fatal("expected error for unknown embedded name, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestRunPipelineNew_FromDryRun(t *testing.T) {
	outDir := t.TempDir()
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "drytest", pipelineNewOptions{
		OutputDir: outDir,
		From:      "quick-fix",
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("runPipelineNew(from=quick-fix,dry-run) error: %v", err)
	}

	// No file should be written.
	if _, err := os.Stat(filepath.Join(outDir, "phases-drytest.yaml")); err == nil {
		t.Fatal("dry-run should not write a file")
	}
	// Output should contain pipeline content.
	if !strings.Contains(buf.String(), "phases:") {
		t.Errorf("dry-run output missing 'phases:', got: %s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Tests for --phases flag
// ---------------------------------------------------------------------------

func TestRunPipelineNew_PhasesFiltersScaffold(t *testing.T) {
	outDir := t.TempDir()
	var buf bytes.Buffer
	// Request only "implement" and "verify" from the default scaffold.
	err := runPipelineNew(&buf, "lite", pipelineNewOptions{
		OutputDir: outDir,
		Phases:    []string{"implement", "verify"},
	})
	if err != nil {
		t.Fatalf("runPipelineNew(phases=implement,verify) error: %v", err)
	}

	dest := filepath.Join(outDir, "phases-lite.yaml")
	pl, err := pipeline.LoadPipeline(dest)
	if err != nil {
		t.Fatalf("LoadPipeline() on filtered output: %v", err)
	}
	if len(pl.Phases) != 2 {
		t.Fatalf("expected 2 phases, got %d", len(pl.Phases))
	}
	if pl.Phases[0].Name != "implement" || pl.Phases[1].Name != "verify" {
		t.Errorf("unexpected phase names: %v", pl.Phases)
	}
}

func TestRunPipelineNew_PhasesRewritesDependsOn(t *testing.T) {
	// Build a source file where "submit" depends on both "implement" and
	// "verify". When "verify" is dropped via --phases, submit's depends_on
	// should only contain "implement".
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "phases-multi.yaml")
	srcContent := `phases:
  - name: implement
    prompt: prompts/implement.md
    tools:
      - Bash
    timeout: 5m
    retry:
      transient: 1
      parse: 0
      semantic: 0
    depends_on: []
  - name: verify
    prompt: prompts/verify.md
    tools:
      - Bash
    timeout: 5m
    retry:
      transient: 1
      parse: 0
      semantic: 0
    depends_on:
      - implement
  - name: submit
    prompt: prompts/submit.md
    tools:
      - Bash
    timeout: 3m
    retry:
      transient: 1
      parse: 0
      semantic: 0
    depends_on:
      - implement
      - verify
`
	if err := os.WriteFile(srcFile, []byte(srcContent), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "noverify", pipelineNewOptions{
		OutputDir: outDir,
		From:      srcFile,
		Phases:    []string{"implement", "submit"},
	})
	if err != nil {
		t.Fatalf("runPipelineNew(phases=implement,submit) error: %v", err)
	}

	dest := filepath.Join(outDir, "phases-noverify.yaml")
	pl, err := pipeline.LoadPipeline(dest)
	if err != nil {
		t.Fatalf("LoadPipeline() on filtered output: %v", err)
	}
	if len(pl.Phases) != 2 {
		t.Fatalf("expected 2 phases, got %d: %v", len(pl.Phases), pl.Phases)
	}

	// submit should only depend on implement (verify was dropped).
	var submitPhase *pipeline.PhaseConfig
	for i := range pl.Phases {
		if pl.Phases[i].Name == "submit" {
			submitPhase = &pl.Phases[i]
		}
	}
	if submitPhase == nil {
		t.Fatal("submit phase not found in output")
	}
	if len(submitPhase.DependsOn) != 1 || submitPhase.DependsOn[0] != "implement" {
		t.Errorf("submit.depends_on = %v, want [implement]", submitPhase.DependsOn)
	}
}

func TestRunPipelineNew_PhasesNoMatchError(t *testing.T) {
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "test", pipelineNewOptions{
		Phases: []string{"nonexistent-phase"},
	})
	if err == nil {
		t.Fatal("expected error when no phases match filter, got nil")
	}
}

func TestRunPipelineNew_FromWithPhases(t *testing.T) {
	outDir := t.TempDir()
	var buf bytes.Buffer
	// Use the embedded quick-fix pipeline and keep only implement.
	err := runPipelineNew(&buf, "impl-only", pipelineNewOptions{
		OutputDir: outDir,
		From:      "quick-fix",
		Phases:    []string{"implement"},
	})
	if err != nil {
		t.Fatalf("runPipelineNew(from=quick-fix,phases=implement) error: %v", err)
	}

	dest := filepath.Join(outDir, "phases-impl-only.yaml")
	pl, err := pipeline.LoadPipeline(dest)
	if err != nil {
		t.Fatalf("LoadPipeline() on filtered output: %v", err)
	}
	if len(pl.Phases) != 1 {
		t.Fatalf("expected 1 phase, got %d", len(pl.Phases))
	}
	if pl.Phases[0].Name != "implement" {
		t.Errorf("expected 'implement', got %q", pl.Phases[0].Name)
	}
	// No depends_on should reference removed phases.
	if len(pl.Phases[0].DependsOn) != 0 {
		t.Errorf("implement.depends_on should be empty, got %v", pl.Phases[0].DependsOn)
	}
}

func TestRunPipelineNew_PhasesDryRun(t *testing.T) {
	outDir := t.TempDir()
	var buf bytes.Buffer
	err := runPipelineNew(&buf, "dryfilter", pipelineNewOptions{
		OutputDir: outDir,
		Phases:    []string{"implement"},
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("runPipelineNew(phases=implement,dry-run) error: %v", err)
	}
	// No file should be written.
	if _, err := os.Stat(filepath.Join(outDir, "phases-dryfilter.yaml")); err == nil {
		t.Fatal("dry-run should not write a file")
	}
	out := buf.String()
	if !strings.Contains(out, "implement") {
		t.Errorf("dry-run output should contain 'implement', got: %s", out)
	}
	// verify and submit must not appear (they were filtered out).
	if strings.Contains(out, "verify") || strings.Contains(out, "submit") {
		t.Errorf("filtered phases must not appear in output, got: %s", out)
	}
}

func TestFilterRawPhases_DoesNotMutateInput(t *testing.T) {
	// Build input phases with depends_on that references a phase we'll filter out.
	phases := []map[string]interface{}{
		{
			"name":       "implement",
			"depends_on": []interface{}{},
		},
		{
			"name":       "verify",
			"depends_on": []interface{}{"implement"},
		},
		{
			"name":       "submit",
			"depends_on": []interface{}{"implement", "verify"},
		},
	}

	// Keep only implement and submit — verify is dropped.
	result := filterRawPhases(phases, []string{"implement", "submit"})
	if len(result) != 2 {
		t.Fatalf("expected 2 phases, got %d", len(result))
	}

	// The original submit phase must still reference both implement and verify.
	origDeps := phases[2]["depends_on"].([]interface{})
	if len(origDeps) != 2 {
		t.Errorf("original submit.depends_on was mutated: got %v, want [implement verify]", origDeps)
	}

	// The filtered submit should only reference implement.
	var filteredSubmit map[string]interface{}
	for _, r := range result {
		if r["name"] == "submit" {
			filteredSubmit = r
		}
	}
	if filteredSubmit == nil {
		t.Fatal("submit not found in result")
	}
	filteredDeps := filteredSubmit["depends_on"].([]interface{})
	if len(filteredDeps) != 1 || filteredDeps[0] != "implement" {
		t.Errorf("filtered submit.depends_on = %v, want [implement]", filteredDeps)
	}
}

func TestRunPipelineNew_StatErrorNotErrNotExist(t *testing.T) {
	dir := t.TempDir()
	// Create a parent dir with no read/execute permission so Stat fails
	// with a permission error, not ErrNotExist.
	noPerms := filepath.Join(dir, "noperm")
	if err := os.Mkdir(noPerms, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(noPerms, 0755) })

	var buf bytes.Buffer
	err := runPipelineNew(&buf, "test", pipelineNewOptions{OutputDir: noPerms})
	if err == nil {
		t.Fatal("expected error for inaccessible path, got nil")
	}
}
