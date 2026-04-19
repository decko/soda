package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/detect"
)

func TestRunInit_WritesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader(""), false, initOptions{Output: dest, NoGitignore: true}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	// File must exist.
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("config file is empty")
	}

	// File must be valid YAML that round-trips.
	cfg, err := config.Load(dest)
	if err != nil {
		t.Fatalf("Load() written config: %v", err)
	}
	if cfg.TicketSource == "" {
		t.Error("loaded config has empty TicketSource")
	}
	if cfg.Mode == "" {
		t.Error("loaded config has empty Mode")
	}

	// Output message must mention the path.
	if !strings.Contains(buf.String(), dest) {
		t.Errorf("output = %q, want to mention %q", buf.String(), dest)
	}
}

func TestRunInit_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "deep", "nested", "soda.yaml")

	if err := runInit(io.Discard, strings.NewReader(""), false, initOptions{Output: dest, NoGitignore: true}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("config not created at nested path: %v", err)
	}
}

func TestRunInit_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	// Write a dummy file.
	if err := os.WriteFile(dest, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	err := runInit(io.Discard, strings.NewReader(""), false, initOptions{Output: dest, NoGitignore: true})
	if err == nil {
		t.Fatal("expected error when file exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "already exists")
	}

	// Original content must be preserved.
	data, _ := os.ReadFile(dest)
	if string(data) != "existing" {
		t.Errorf("file was modified: got %q", string(data))
	}
}

func TestRunInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	// Write a dummy file.
	if err := os.WriteFile(dest, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runInit(io.Discard, strings.NewReader(""), false, initOptions{Output: dest, Force: true, NoGitignore: true}); err != nil {
		t.Fatalf("runInit(force=true) error: %v", err)
	}

	// Content must be replaced with valid config.
	cfg, err := config.Load(dest)
	if err != nil {
		t.Fatalf("Load() after force overwrite: %v", err)
	}
	if cfg.TicketSource == "" {
		t.Error("overwritten config has empty TicketSource")
	}
}

func TestRunInit_StatErrorNotErrNotExist(t *testing.T) {
	dir := t.TempDir()
	// Create a parent dir with no read/execute permission so Stat fails
	// with a permission error, not ErrNotExist.
	noPerms := filepath.Join(dir, "noperm")
	if err := os.Mkdir(noPerms, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(noPerms, 0755) })

	dest := filepath.Join(noPerms, "soda.yaml")
	err := runInit(io.Discard, strings.NewReader(""), false, initOptions{Output: dest, NoGitignore: true})
	if err == nil {
		t.Fatal("expected error for inaccessible path, got nil")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Errorf("error = %q, want stat context", err)
	}
}

func TestResolveInitPath_DefaultPath(t *testing.T) {
	p, err := resolveInitPath("")
	if err != nil {
		t.Fatalf("resolveInitPath(\"\") error: %v", err)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("path %q is not absolute", p)
	}
	if filepath.Base(p) != "soda.yaml" {
		t.Errorf("base = %q, want soda.yaml", filepath.Base(p))
	}
}

func TestResolveInitPath_CustomPath(t *testing.T) {
	p, err := resolveInitPath("my-config.yaml")
	if err != nil {
		t.Fatalf("resolveInitPath() error: %v", err)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("path %q is not absolute", p)
	}
	if filepath.Base(p) != "my-config.yaml" {
		t.Errorf("base = %q, want my-config.yaml", filepath.Base(p))
	}
}

func TestRunInit_DryRun(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader(""), false, initOptions{Output: dest, DryRun: true, NoGitignore: true}); err != nil {
		t.Fatalf("runInit(dryRun=true) error: %v", err)
	}

	// File must NOT be written.
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("dry-run should not write a file")
	}

	// Output must contain valid YAML config.
	output := buf.String()
	if !strings.Contains(output, "ticket_source") {
		t.Errorf("dry-run output missing ticket_source, got: %s", output)
	}
	if !strings.Contains(output, "mode:") {
		t.Errorf("dry-run output missing mode field, got: %s", output)
	}
}

func TestRunInit_PhasesWritten(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader(""), false, initOptions{Output: dest, Phases: true, NoGitignore: true}); err != nil {
		t.Fatalf("runInit(phases=true) error: %v", err)
	}

	// Config must exist.
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("config not created: %v", err)
	}

	// phases.yaml must exist alongside the config.
	phasesPath := filepath.Join(dir, "phases.yaml")
	phasesData, err := os.ReadFile(phasesPath)
	if err != nil {
		t.Fatalf("phases.yaml not created: %v", err)
	}
	if len(phasesData) == 0 {
		t.Fatal("phases.yaml is empty")
	}

	// Output must mention both files.
	output := buf.String()
	if !strings.Contains(output, "Config written") {
		t.Errorf("output missing config confirmation: %s", output)
	}
	if !strings.Contains(output, "Phases written") {
		t.Errorf("output missing phases confirmation: %s", output)
	}
}

func TestRunInit_PhasesRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	// Pre-create phases.yaml.
	phasesPath := filepath.Join(dir, "phases.yaml")
	if err := os.WriteFile(phasesPath, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	err := runInit(io.Discard, strings.NewReader(""), false, initOptions{Output: dest, Phases: true, NoGitignore: true})
	if err == nil {
		t.Fatal("expected error when phases.yaml exists, got nil")
	}
	if !strings.Contains(err.Error(), "phases file already exists") {
		t.Errorf("error = %q, want phases file already exists", err.Error())
	}

	// Original phases content must be preserved.
	data, _ := os.ReadFile(phasesPath)
	if string(data) != "existing" {
		t.Errorf("phases.yaml was modified: got %q", string(data))
	}
}

func TestRunInit_PhasesForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	// Pre-create phases.yaml with dummy content.
	phasesPath := filepath.Join(dir, "phases.yaml")
	if err := os.WriteFile(phasesPath, []byte("old-content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runInit(io.Discard, strings.NewReader(""), false, initOptions{Output: dest, Force: true, Phases: true, NoGitignore: true}); err != nil {
		t.Fatalf("runInit(force=true, phases=true) error: %v", err)
	}

	// phases.yaml must be overwritten with embedded content.
	data, err := os.ReadFile(phasesPath)
	if err != nil {
		t.Fatalf("read phases.yaml: %v", err)
	}
	if string(data) == "old-content" {
		t.Error("phases.yaml was not overwritten")
	}
	if len(data) == 0 {
		t.Error("phases.yaml is empty after overwrite")
	}
}

func TestRunInit_GitignoreCreated(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	// NoGitignore=false → should create/update .gitignore
	if err := runInit(&buf, strings.NewReader(""), false, initOptions{Output: dest}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	gitignorePath := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, ".soda/") {
		t.Errorf(".gitignore missing .soda/ entry: %s", content)
	}
	if !strings.Contains(content, ".worktrees/") {
		t.Errorf(".gitignore missing .worktrees/ entry: %s", content)
	}

	// Output must mention the update.
	if !strings.Contains(buf.String(), "Updated .gitignore") {
		t.Errorf("output missing gitignore update message: %s", buf.String())
	}
}

func TestRunInit_GitignoreSkippedWithFlag(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	// NoGitignore=true → should NOT create .gitignore
	if err := runInit(io.Discard, strings.NewReader(""), false, initOptions{Output: dest, NoGitignore: true}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	gitignorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		t.Error("--no-gitignore should prevent .gitignore creation")
	}
}

func TestRunInit_GitignoreAppendsWithoutDuplication(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	// Pre-create .gitignore with .soda/ already present.
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("# existing\n.soda/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader(""), false, initOptions{Output: dest}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	content := string(data)

	// .soda/ should appear exactly once (not duplicated).
	count := strings.Count(content, ".soda/")
	if count != 1 {
		t.Errorf(".soda/ appears %d times, want 1: %s", count, content)
	}

	// .worktrees/ should be appended.
	if !strings.Contains(content, ".worktrees/") {
		t.Errorf(".gitignore missing .worktrees/ entry: %s", content)
	}
}

func TestEnsureGitignore_ExistingEntriesWithoutSlash(t *testing.T) {
	dir := t.TempDir()
	gitignorePath := filepath.Join(dir, ".gitignore")

	// Entries without trailing slash should still prevent duplication.
	if err := os.WriteFile(gitignorePath, []byte(".soda\n.worktrees\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{StateDir: ".soda", WorktreeDir: ".worktrees"}

	var buf bytes.Buffer
	if err := ensureGitignore(&buf, gitignorePath, cfg); err != nil {
		t.Fatalf("ensureGitignore() error: %v", err)
	}

	// Should not add anything since both entries already exist (without slash).
	if buf.Len() > 0 {
		t.Errorf("expected no output (nothing to add), got: %s", buf.String())
	}

	data, _ := os.ReadFile(gitignorePath)
	if strings.Contains(string(data), ".soda/") {
		t.Error("should not duplicate entries that exist without trailing slash")
	}
}

func TestNewInitCmd_YesFlagAccepted(t *testing.T) {
	cmd := newInitCmd()
	// Verify the --yes / -y flag is registered.
	flag := cmd.Flags().Lookup("yes")
	if flag == nil {
		t.Fatal("--yes flag not registered")
	}
	if flag.Shorthand != "y" {
		t.Errorf("--yes shorthand = %q, want %q", flag.Shorthand, "y")
	}
}

func TestConfigFromDetected_GitHub(t *testing.T) {
	info := &detect.ProjectInfo{
		Language:     "go",
		Forge:        "github",
		Owner:        "acme",
		Repo:         "myservice",
		Formatter:    "gofmt -w .",
		TestCommand:  "go test ./...",
		ContextFiles: []string{"AGENTS.md", "CLAUDE.md"},
	}

	cfg := configFromDetected(info)

	if cfg.TicketSource != "github" {
		t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "github")
	}
	if cfg.GitHub.Owner != "acme" {
		t.Errorf("GitHub.Owner = %q, want %q", cfg.GitHub.Owner, "acme")
	}
	if cfg.GitHub.Repo != "myservice" {
		t.Errorf("GitHub.Repo = %q, want %q", cfg.GitHub.Repo, "myservice")
	}
	if len(cfg.Context) != 2 || cfg.Context[0] != "AGENTS.md" || cfg.Context[1] != "CLAUDE.md" {
		t.Errorf("Context = %v, want [AGENTS.md CLAUDE.md]", cfg.Context)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("len(Repos) = %d, want 1", len(cfg.Repos))
	}
	repo := cfg.Repos[0]
	if repo.Name != "myservice" {
		t.Errorf("Repos[0].Name = %q, want %q", repo.Name, "myservice")
	}
	if repo.Forge != "github" {
		t.Errorf("Repos[0].Forge = %q, want %q", repo.Forge, "github")
	}
	if repo.PushTo != "acme/myservice" {
		t.Errorf("Repos[0].PushTo = %q, want %q", repo.PushTo, "acme/myservice")
	}
	if repo.Formatter != "gofmt -w ." {
		t.Errorf("Repos[0].Formatter = %q, want %q", repo.Formatter, "gofmt -w .")
	}
	if repo.TestCommand != "go test ./..." {
		t.Errorf("Repos[0].TestCommand = %q, want %q", repo.TestCommand, "go test ./...")
	}
}

func TestConfigFromDetected_NoForge(t *testing.T) {
	info := &detect.ProjectInfo{
		Language:    "python",
		Forge:       "",
		Owner:       "",
		Repo:        "",
		Formatter:   "black .",
		TestCommand: "pytest",
	}

	cfg := configFromDetected(info)

	// Should fall back to github ticket source with placeholders.
	if cfg.TicketSource != "github" {
		t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "github")
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("len(Repos) = %d, want 1", len(cfg.Repos))
	}
	repo := cfg.Repos[0]
	if repo.Name != "your-repo" {
		t.Errorf("Repos[0].Name = %q, want %q", repo.Name, "your-repo")
	}
	if repo.PushTo != "your-user/your-repo" {
		t.Errorf("Repos[0].PushTo = %q, want %q", repo.PushTo, "your-user/your-repo")
	}
	if repo.Formatter != "black ." {
		t.Errorf("Repos[0].Formatter = %q, want %q", repo.Formatter, "black .")
	}
	if repo.TestCommand != "pytest" {
		t.Errorf("Repos[0].TestCommand = %q, want %q", repo.TestCommand, "pytest")
	}
}

// TestRunInit_ConfirmationPromptYes verifies that when isTTY=true and the user
// types "y", the file is written and the prompt is shown.
func TestRunInit_ConfirmationPromptYes(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader("y\n"), true, initOptions{Output: dest, NoGitignore: true}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("file should be written after 'y' response: %v", err)
	}
	if !strings.Contains(buf.String(), "Write config to") {
		t.Errorf("output should show prompt, got: %s", buf.String())
	}
}

// TestRunInit_ConfirmationPromptNo verifies that when isTTY=true and the user
// types "n", the file is NOT written and "Aborted" is reported.
func TestRunInit_ConfirmationPromptNo(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader("n\n"), true, initOptions{Output: dest, NoGitignore: true}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	if _, err := os.Stat(dest); err == nil {
		t.Fatal("file should not be written after 'n' response")
	}
	if !strings.Contains(buf.String(), "Aborted") {
		t.Errorf("output should mention abort, got: %s", buf.String())
	}
}

// TestRunInit_YesSkipsPrompt verifies that --yes skips the confirmation prompt
// even when isTTY=true.
func TestRunInit_YesSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader(""), true, initOptions{Output: dest, NoGitignore: true, Yes: true}); err != nil {
		t.Fatalf("runInit(yes=true) error: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("file should be written with --yes: %v", err)
	}
	if strings.Contains(buf.String(), "Write config to") {
		t.Errorf("--yes should skip prompt, got: %s", buf.String())
	}
}

// TestRunInit_NonTTYAutoWrites verifies that in a non-TTY environment the file
// is written automatically without showing a confirmation prompt.
func TestRunInit_NonTTYAutoWrites(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader(""), false, initOptions{Output: dest, NoGitignore: true}); err != nil {
		t.Fatalf("runInit(isTTY=false) error: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("non-TTY should auto-write without prompt: %v", err)
	}
	if strings.Contains(buf.String(), "Write config to") {
		t.Errorf("non-TTY should not show prompt, got: %s", buf.String())
	}
}

// TestColorMsg_WithColor verifies that color codes are emitted when NO_COLOR is unset.
func TestColorMsg_WithColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	result := colorMsg("32", "hello")
	if !strings.Contains(result, "\033[32m") {
		t.Errorf("colorMsg should contain ANSI code, got: %q", result)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("colorMsg should contain message, got: %q", result)
	}
}

// TestColorMsg_NoColor verifies that ANSI codes are suppressed when NO_COLOR is set.
func TestColorMsg_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	result := colorMsg("32", "hello")
	if result != "hello" {
		t.Errorf("colorMsg with NO_COLOR should return plain text, got: %q", result)
	}
}

// TestRunInit_ColorOutput verifies that success messages include ANSI color codes
// when NO_COLOR is not set.
func TestRunInit_ColorOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader(""), false, initOptions{Output: dest, NoGitignore: true}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	if !strings.Contains(buf.String(), "\033[") {
		t.Errorf("output should contain ANSI color codes, got: %q", buf.String())
	}
}

// TestRunInit_NoColorEnv verifies that ANSI codes are absent when NO_COLOR is set.
func TestRunInit_NoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, strings.NewReader(""), false, initOptions{Output: dest, NoGitignore: true}); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	if strings.Contains(buf.String(), "\033[") {
		t.Errorf("NO_COLOR should suppress ANSI codes, got: %q", buf.String())
	}
}

func TestConfigFromDetected_GitLab(t *testing.T) {
	info := &detect.ProjectInfo{
		Language:    "rust",
		Forge:       "gitlab",
		Owner:       "team",
		Repo:        "backend",
		Formatter:   "cargo fmt",
		TestCommand: "cargo test",
	}

	cfg := configFromDetected(info)

	// GitLab forge should still use github ticket source (gitlab not yet supported).
	if cfg.TicketSource != "github" {
		t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "github")
	}

	// GitHub ticket config should be populated with detected owner/repo so
	// that the config is internally consistent (ticket source points at the
	// same repo as cfg.Repos[0]).
	if cfg.GitHub.Owner != "team" {
		t.Errorf("GitHub.Owner = %q, want %q", cfg.GitHub.Owner, "team")
	}
	if cfg.GitHub.Repo != "backend" {
		t.Errorf("GitHub.Repo = %q, want %q", cfg.GitHub.Repo, "backend")
	}

	if len(cfg.Repos) != 1 {
		t.Fatalf("len(Repos) = %d, want 1", len(cfg.Repos))
	}
	if cfg.Repos[0].Forge != "gitlab" {
		t.Errorf("Repos[0].Forge = %q, want %q", cfg.Repos[0].Forge, "gitlab")
	}
	if cfg.Repos[0].PushTo != "team/backend" {
		t.Errorf("Repos[0].PushTo = %q, want %q", cfg.Repos[0].PushTo, "team/backend")
	}
}
