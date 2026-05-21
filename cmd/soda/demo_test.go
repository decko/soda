package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/pipeline"
)

func TestDemoCommandRegistered(t *testing.T) {
	rootCmd := newRootCmd()
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "demo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'demo' subcommand to be registered")
	}
}

func TestDemoCommandHasDryRunFlag(t *testing.T) {
	cmd := newDemoCmd()
	flag := cmd.Flags().Lookup("dry-run")
	if flag == nil {
		t.Fatal("expected --dry-run flag to be registered")
	}
	if flag.DefValue != "false" {
		t.Errorf("--dry-run default = %q, want %q", flag.DefValue, "false")
	}
}

func TestEmbeddedDemoFiles(t *testing.T) {
	// Verify all expected files are present in the embedded FS.
	expectedFiles := []string{
		"embeds/demo/main.go.txt",
		"embeds/demo/main_test.go.txt",
		"embeds/demo/go.mod.txt",
		"embeds/demo/ticket.json",
		"embeds/demo/pipeline.yaml",
	}

	for _, path := range expectedFiles {
		data, err := fs.ReadFile(embeddedDemoFS, path)
		if err != nil {
			t.Errorf("embedded file %q not found: %v", path, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("embedded file %q is empty", path)
		}
	}
}

func TestEmbeddedDemoMainContainsBug(t *testing.T) {
	data, err := fs.ReadFile(embeddedDemoFS, "embeds/demo/main.go.txt")
	if err != nil {
		t.Fatalf("read main.go.txt: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "StatusInternalServerError") {
		t.Error("main.go.txt should contain the StatusInternalServerError bug")
	}
}

func TestLoadDemoTicket(t *testing.T) {
	ticket, err := loadDemoTicket()
	if err != nil {
		t.Fatalf("loadDemoTicket: %v", err)
	}

	if ticket.Key != "DEMO-1" {
		t.Errorf("Key = %q, want %q", ticket.Key, "DEMO-1")
	}
	if ticket.Type != "bug" {
		t.Errorf("Type = %q, want %q", ticket.Type, "bug")
	}
	if ticket.Priority != "high" {
		t.Errorf("Priority = %q, want %q", ticket.Priority, "high")
	}
	if len(ticket.AcceptanceCriteria) != 1 {
		t.Fatalf("AcceptanceCriteria len = %d, want 1", len(ticket.AcceptanceCriteria))
	}
	if ticket.AcceptanceCriteria[0] != "TestHandler passes with go test ./..." {
		t.Errorf("AcceptanceCriteria[0] = %q, want %q",
			ticket.AcceptanceCriteria[0], "TestHandler passes with go test ./...")
	}
}

func TestLoadDemoPipeline(t *testing.T) {
	pl, cleanup, err := loadDemoPipeline()
	if err != nil {
		t.Fatalf("loadDemoPipeline: %v", err)
	}
	defer cleanup()

	if len(pl.Phases) != 4 {
		t.Fatalf("expected 4 phases, got %d", len(pl.Phases))
	}

	expectedPhases := []string{"triage", "plan", "implement", "verify"}
	for idx, want := range expectedPhases {
		if pl.Phases[idx].Name != want {
			t.Errorf("phase[%d].Name = %q, want %q", idx, pl.Phases[idx].Name, want)
		}
	}
}

func TestBuildDemoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := buildDemoConfig(tmpDir)

	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-sonnet-4-20250514")
	}
	if cfg.Limits.MaxCostPerTicket != 3.00 {
		t.Errorf("MaxCostPerTicket = %.2f, want 3.00", cfg.Limits.MaxCostPerTicket)
	}
	if cfg.Limits.MaxCostPerPhase != 1.50 {
		t.Errorf("MaxCostPerPhase = %.2f, want 1.50", cfg.Limits.MaxCostPerPhase)
	}
	expectedStateDir := filepath.Join(tmpDir, ".soda")
	if cfg.StateDir != expectedStateDir {
		t.Errorf("StateDir = %q, want %q", cfg.StateDir, expectedStateDir)
	}

	// Repos must be populated so dry-run picks up Formatter/TestCommand
	// via buildPromptConfig(cfg).
	if len(cfg.Repos) != 1 {
		t.Fatalf("Repos len = %d, want 1", len(cfg.Repos))
	}
	if cfg.Repos[0].Formatter != "gofmt -w ." {
		t.Errorf("Repos[0].Formatter = %q, want %q", cfg.Repos[0].Formatter, "gofmt -w .")
	}
	if cfg.Repos[0].TestCommand != "go test ./..." {
		t.Errorf("Repos[0].TestCommand = %q, want %q", cfg.Repos[0].TestCommand, "go test ./...")
	}
}

func TestWriteDemoFiles(t *testing.T) {
	destDir := t.TempDir()

	if err := writeDemoFiles(destDir); err != nil {
		t.Fatalf("writeDemoFiles: %v", err)
	}

	// .go.txt should be renamed to .go
	if _, err := os.Stat(filepath.Join(destDir, "main.go")); err != nil {
		t.Error("main.go not found (expected .go.txt → .go rename)")
	}
	if _, err := os.Stat(filepath.Join(destDir, "main_test.go")); err != nil {
		t.Error("main_test.go not found (expected .go.txt → .go rename)")
	}

	// .mod.txt should be renamed to .mod
	if _, err := os.Stat(filepath.Join(destDir, "go.mod")); err != nil {
		t.Error("go.mod not found (expected .mod.txt → .mod rename)")
	}

	// pipeline.yaml and ticket.json should NOT be written
	if _, err := os.Stat(filepath.Join(destDir, "pipeline.yaml")); err == nil {
		t.Error("pipeline.yaml should not be written by writeDemoFiles")
	}
	if _, err := os.Stat(filepath.Join(destDir, "ticket.json")); err == nil {
		t.Error("ticket.json should not be written by writeDemoFiles")
	}

	// .go.txt originals should not exist
	if _, err := os.Stat(filepath.Join(destDir, "main.go.txt")); err == nil {
		t.Error("main.go.txt should not exist after rename")
	}
}

func TestInitDemoRepo(t *testing.T) {
	destDir := t.TempDir()

	// Write a file so there's something to commit.
	if err := os.WriteFile(filepath.Join(destDir, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	ctx := context.Background()
	if err := initDemoRepo(ctx, destDir); err != nil {
		t.Fatalf("initDemoRepo: %v", err)
	}

	// Verify that the directory is a git repository with at least one commit.
	gitDir := filepath.Join(destDir, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		t.Fatalf(".git directory not found: %v", err)
	}
	if !info.IsDir() {
		t.Error(".git is not a directory")
	}

	// Verify branch is "main" (matches BaseBranch in engine config).
	branchCmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	branchCmd.Dir = destDir
	branchOut, branchErr := branchCmd.Output()
	if branchErr != nil {
		t.Fatalf("git branch: %v", branchErr)
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch != "main" {
		t.Errorf("branch = %q, want %q", branch, "main")
	}
}

func TestRunDemoPreflightChecks_SkipsGitRepo(t *testing.T) {
	// The demo preflight should NOT check for an existing git repo.
	// We verify by ensuring there's no "git-repo" failure even when
	// we're not inside a git repository.
	//
	// We can't easily mock runDemoPreflightChecks since it uses
	// defaultDoctorEnv(), but we can verify that its check list
	// does not include checkGitRepo by inspecting the behavior:
	// if git and claude are found, no error should occur even
	// outside a git repo.
	//
	// This test verifies the check list composition indirectly.
	// The actual checks use exec.LookPath, so we just verify the
	// function signature and behavior are correct.

	// Verify the function exists and returns the right type.
	err := runDemoPreflightChecks()
	if err != nil {
		// Expected when git or claude isn't on PATH in CI,
		// but the error should NOT mention "git-repo".
		var pe *PreflightError
		if errors.As(err, &pe) {
			for _, failure := range pe.Failures {
				if failure.name == "git-repo" {
					t.Error("demo preflight should not check for git-repo")
				}
				if failure.name == "claude-version" {
					t.Error("demo preflight should not check claude-version")
				}
			}
		}
	}
}

func TestPrintDemoNextSteps(t *testing.T) {
	var buf bytes.Buffer
	meta := &pipeline.PipelineMeta{
		TotalCost: 0.42,
	}

	printDemoNextSteps(&buf, meta)

	output := buf.String()
	if !strings.Contains(output, "Demo completed successfully") {
		t.Error("expected success message")
	}
	if !strings.Contains(output, "$0.42") {
		t.Error("expected cost to be displayed")
	}
	if !strings.Contains(output, "soda init") {
		t.Error("expected 'soda init' suggestion")
	}
	if !strings.Contains(output, "soda doctor") {
		t.Error("expected 'soda doctor' suggestion")
	}
	if !strings.Contains(output, "soda run") {
		t.Error("expected 'soda run' suggestion")
	}
}
