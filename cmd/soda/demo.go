package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/progress"
	"github.com/decko/soda/internal/runner"
	"github.com/decko/soda/internal/ticket"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

//go:embed all:embeds/demo
var embeddedDemoFS embed.FS

func newDemoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Run a guided first-run demo with an embedded buggy Go project",
		Long: `Run a self-contained demo that fixes a trivial HTTP handler bug.

The demo creates a temporary directory with a small Go project, initialises
a git repository, and runs a 4-phase pipeline (triage → plan → implement →
verify) to fix the bug automatically.

No configuration, credentials, or existing repository are required — only
git and the Claude CLI must be available.

The temporary directory is cleaned up automatically on exit.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			return runDemo(dryRun)
		},
	}

	cmd.Flags().Bool("dry-run", false, "render prompts without executing")

	return cmd
}

// runDemo orchestrates the complete demo experience.
func runDemo(dryRun bool) error {
	// Preflight: check git + claude (skip git-repo and claude-version).
	if !dryRun {
		if err := runDemoPreflightChecks(); err != nil {
			return err
		}
	}

	// Create temp directory for the demo project.
	tmpDir, err := os.MkdirTemp("", "soda-demo-*")
	if err != nil {
		return fmt.Errorf("demo: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("📁 Demo project: %s\n", tmpDir)

	// Write embedded project files.
	if err := writeDemoFiles(tmpDir); err != nil {
		return fmt.Errorf("demo: %w", err)
	}

	// Initialise git repo.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := initDemoRepo(ctx, tmpDir); err != nil {
		return fmt.Errorf("demo: %w", err)
	}

	// Load the demo ticket.
	demoTicket, err := loadDemoTicket()
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}

	// Load the demo pipeline.
	demoPipeline, pipelineCleanup, err := loadDemoPipeline()
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	defer pipelineCleanup()

	// Build demo config.
	cfg := buildDemoConfig(tmpDir)

	// Set up prompt loader with embedded prompts.
	promptDir, err := extractEmbeddedPrompts()
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	defer os.RemoveAll(promptDir)

	loader := pipeline.NewPromptLoader(tmpDir, promptDir)

	ticketData := pipeline.TicketData{
		Key:                demoTicket.Key,
		Summary:            demoTicket.Summary,
		Description:        demoTicket.Description,
		Type:               demoTicket.Type,
		Priority:           demoTicket.Priority,
		AcceptanceCriteria: demoTicket.AcceptanceCriteria,
	}

	// Dry-run mode: render prompts and exit.
	if dryRun {
		return runDryRun(cfg, demoPipeline, loader, ticketData, false)
	}

	// Create state directory.
	stateDir := filepath.Join(tmpDir, ".soda")
	state, err := pipeline.LoadOrCreate(stateDir, demoTicket.Key)
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}

	// Build runner.
	claudeRunner, err := runner.NewClaudeRunner("claude", cfg.Model, tmpDir)
	if err != nil {
		return fmt.Errorf("demo: create claude runner: %w", err)
	}

	// Progress display.
	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	prog := progress.New(os.Stdout, isTTY)

	skippedPhases := map[string]bool{}
	var engine *pipeline.Engine

	engineCfg := pipeline.EngineConfig{
		Pipeline:        demoPipeline,
		Loader:          loader,
		Ticket:          ticketData,
		PromptConfig:    buildDemoPromptConfig(),
		Model:           cfg.Model,
		BinaryVersion:   binaryVersionID(),
		WorkDir:         tmpDir,
		WorktreeBase:    "", // run in-place; no worktree needed
		BaseBranch:      "main",
		MaxCostUSD:      cfg.Limits.MaxCostPerTicket,
		MaxCostPerPhase: cfg.Limits.MaxCostPerPhase,
		Mode:            pipeline.Autonomous,
		OnEvent: func(event pipeline.Event) {
			if event.Kind == pipeline.EventPhaseSkipped {
				skippedPhases[event.Phase] = true
			}
			handleEvent(ctx, cancel, engine, state, prog, event)
		},
	}

	engine = pipeline.NewEngine(claudeRunner, state, engineCfg)

	// Run the pipeline.
	startTime := time.Now()
	runErr := engine.Run(ctx)

	// Print summary.
	printSummary(state, demoPipeline.Phases, demoTicket.Summary, time.Since(startTime), runErr, skippedPhases)

	// Print "what's next" guide.
	if runErr == nil {
		printDemoNextSteps(os.Stdout, state.Meta())
	}

	return runErr
}

// runDemoPreflightChecks runs a targeted subset of preflight checks
// suitable for the demo: git and claude must be present, but we skip
// checkGitRepo (the demo creates its own) and checkClaudeVersion
// (avoid blocking a first-run experience on version checks).
func runDemoPreflightChecks() error {
	env := defaultDoctorEnv()

	checks := []func(*doctorEnv) checkResult{
		checkGit,
		checkClaude,
	}

	var failures []checkResult
	for _, check := range checks {
		result := check(env)
		if result.skipped {
			continue
		}
		if !result.passed && result.required {
			failures = append(failures, result)
		}
	}

	if len(failures) > 0 {
		return &PreflightError{Failures: failures}
	}
	return nil
}

// writeDemoFiles extracts the embedded demo project files to destDir.
// Files with a .go.txt or .mod.txt suffix are renamed to .go / .mod.
// Pipeline and ticket files are skipped (they are loaded separately).
func writeDemoFiles(destDir string) error {
	demoFS, err := fs.Sub(embeddedDemoFS, "embeds/demo")
	if err != nil {
		return fmt.Errorf("embedded demo: %w", err)
	}

	return fs.WalkDir(demoFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		// Skip pipeline and ticket files — they are loaded directly from embed.
		if path == "pipeline.yaml" || path == "ticket.json" {
			return nil
		}

		data, readErr := fs.ReadFile(demoFS, path)
		if readErr != nil {
			return fmt.Errorf("read embedded %s: %w", path, readErr)
		}

		// Rename .go.txt → .go and .mod.txt → .mod
		destName := path
		if strings.HasSuffix(destName, ".go.txt") {
			destName = strings.TrimSuffix(destName, ".txt")
		} else if strings.HasSuffix(destName, ".mod.txt") {
			destName = strings.TrimSuffix(destName, ".txt")
		}

		destPath := filepath.Join(destDir, destName)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("create dir for %s: %w", destName, err)
		}
		return os.WriteFile(destPath, data, 0644)
	})
}

// initDemoRepo initialises a git repository in dir with an initial commit.
func initDemoRepo(ctx context.Context, dir string) error {
	commands := []struct {
		args []string
	}{
		{[]string{"git", "init", "-b", "main"}},
		{[]string{"git", "config", "user.email", "demo@soda.dev"}},
		{[]string{"git", "config", "user.name", "soda-demo"}},
		{[]string{"git", "add", "."}},
		{[]string{"git", "-c", "commit.gpgsign=false", "commit", "-m", "initial commit"}},
	}

	for _, c := range commands {
		cmd := exec.CommandContext(ctx, c.args[0], c.args[1:]...)
		cmd.Dir = dir
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("init repo (%s): %w", strings.Join(c.args, " "), err)
		}
	}
	return nil
}

// loadDemoTicket reads and unmarshals the embedded demo ticket.
func loadDemoTicket() (*ticket.Ticket, error) {
	data, err := fs.ReadFile(embeddedDemoFS, "embeds/demo/ticket.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded ticket: %w", err)
	}

	var t ticket.Ticket
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse embedded ticket: %w", err)
	}
	return &t, nil
}

// loadDemoPipeline writes the embedded demo pipeline to a temp file
// and loads it. The caller must invoke the returned cleanup function.
func loadDemoPipeline() (*pipeline.PhasePipeline, func(), error) {
	data, err := fs.ReadFile(embeddedDemoFS, "embeds/demo/pipeline.yaml")
	if err != nil {
		return nil, nil, fmt.Errorf("read embedded pipeline: %w", err)
	}

	tmpFile, err := os.CreateTemp("", "soda-demo-pipeline-*.yaml")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp pipeline file: %w", err)
	}

	if _, writeErr := tmpFile.Write(data); writeErr != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, nil, fmt.Errorf("write demo pipeline: %w", writeErr)
	}
	tmpFile.Close()

	pl, err := pipeline.LoadPipeline(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		return nil, nil, fmt.Errorf("load demo pipeline: %w", err)
	}

	cleanup := func() { os.Remove(tmpFile.Name()) }
	return pl, cleanup, nil
}

// buildDemoConfig returns an in-memory config suitable for the demo.
// It uses conservative cost limits and hardcoded Go tool commands.
func buildDemoConfig(tmpDir string) *config.Config {
	return &config.Config{
		Model:    "claude-sonnet-4-20250514",
		StateDir: filepath.Join(tmpDir, ".soda"),
		Repos: []config.RepoConfig{{
			Formatter:   "gofmt -w .",
			TestCommand: "go test ./...",
		}},
		Limits: config.LimitsConfig{
			MaxCostPerTicket: 3.00,
			MaxCostPerPhase:  1.50,
		},
	}
}

// buildDemoPromptConfig returns a PromptConfigData with hardcoded Go
// formatter and test commands suitable for the demo project.
func buildDemoPromptConfig() pipeline.PromptConfigData {
	return pipeline.PromptConfigData{
		Formatter:   "gofmt -w .",
		TestCommand: "go test ./...",
	}
}

// printDemoNextSteps prints a "what to do next" guide after a
// successful demo run.
func printDemoNextSteps(w io.Writer, meta *pipeline.PipelineMeta) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "🎉 Demo completed successfully!")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  Total cost: $%.2f\n", meta.TotalCost)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "What's next:")
	fmt.Fprintln(w, "  1. Run 'soda init' to set up your own project")
	fmt.Fprintln(w, "  2. Run 'soda doctor' to verify your environment")
	fmt.Fprintln(w, "  3. Run 'soda run <ticket>' to fix a real bug")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Documentation: https://github.com/decko/soda")
}
