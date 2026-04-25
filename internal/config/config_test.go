package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/decko/soda/internal/pipeline"
	"gopkg.in/yaml.v3"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr bool
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "valid config parses all fields",
			file: "testdata/valid.yaml",
			check: func(t *testing.T, cfg *Config) {
				if cfg.TicketSource != "jira" {
					t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "jira")
				}
				if cfg.Jira.Command != "wtmcp" {
					t.Errorf("Jira.Command = %q, want %q", cfg.Jira.Command, "wtmcp")
				}
				if cfg.Mode != "autonomous" {
					t.Errorf("Mode = %q, want %q", cfg.Mode, "autonomous")
				}
				if cfg.Model != "claude-opus-4-6" {
					t.Errorf("Model = %q, want %q", cfg.Model, "claude-opus-4-6")
				}
				if cfg.Limits.MaxCostPerTicket != 15.0 {
					t.Errorf("MaxCostPerTicket = %f, want 15.0", cfg.Limits.MaxCostPerTicket)
				}
				if cfg.Limits.MaxCostPerPhase != 8.0 {
					t.Errorf("MaxCostPerPhase = %f, want 8.0", cfg.Limits.MaxCostPerPhase)
				}
				if cfg.Limits.MaxCostPerGeneration != 5.0 {
					t.Errorf("MaxCostPerGeneration = %f, want 5.0", cfg.Limits.MaxCostPerGeneration)
				}
				if cfg.Limits.MaxPipelineDuration != "2h" {
					t.Errorf("MaxPipelineDuration = %q, want %q", cfg.Limits.MaxPipelineDuration, "2h")
				}
				if cfg.Limits.TokenBudget.WarnTokens != 100000 {
					t.Errorf("TokenBudget.WarnTokens = %d, want 100000", cfg.Limits.TokenBudget.WarnTokens)
				}
				if cfg.StateDir != ".soda" {
					t.Errorf("StateDir = %q, want %q", cfg.StateDir, ".soda")
				}
				if len(cfg.Repos) != 1 {
					t.Fatalf("len(Repos) = %d, want 1", len(cfg.Repos))
				}
				if cfg.Repos[0].Name != "my-service" {
					t.Errorf("Repos[0].Name = %q, want %q", cfg.Repos[0].Name, "my-service")
				}
				if cfg.Repos[0].Forge != "github" {
					t.Errorf("Repos[0].Forge = %q, want %q", cfg.Repos[0].Forge, "github")
				}
				if len(cfg.Context) != 1 || cfg.Context[0] != "AGENTS.md" {
					t.Errorf("Context = %v, want [AGENTS.md]", cfg.Context)
				}
				if len(cfg.PhaseContext["plan"]) != 1 {
					t.Errorf("PhaseContext[plan] = %v, want 1 entry", cfg.PhaseContext["plan"])
				}
				if cfg.Jira.Extraction.Spec.StartMarker != "<!-- spec:start -->" {
					t.Errorf("Jira.Extraction.Spec.StartMarker = %q, want %q", cfg.Jira.Extraction.Spec.StartMarker, "<!-- spec:start -->")
				}
				if cfg.Jira.Extraction.Spec.EndMarker != "<!-- spec:end -->" {
					t.Errorf("Jira.Extraction.Spec.EndMarker = %q, want %q", cfg.Jira.Extraction.Spec.EndMarker, "<!-- spec:end -->")
				}
				if cfg.Jira.Extraction.Plan.StartMarker != "<!-- plan:start -->" {
					t.Errorf("Jira.Extraction.Plan.StartMarker = %q, want %q", cfg.Jira.Extraction.Plan.StartMarker, "<!-- plan:start -->")
				}
				if cfg.Jira.Extraction.Plan.EndMarker != "<!-- plan:end -->" {
					t.Errorf("Jira.Extraction.Plan.EndMarker = %q, want %q", cfg.Jira.Extraction.Plan.EndMarker, "<!-- plan:end -->")
				}
				if cfg.Jira.Extraction.SpecField != "customfield_10050" {
					t.Errorf("Jira.Extraction.SpecField = %q, want %q", cfg.Jira.Extraction.SpecField, "customfield_10050")
				}
				if cfg.Jira.Extraction.PlanField != "customfield_10051" {
					t.Errorf("Jira.Extraction.PlanField = %q, want %q", cfg.Jira.Extraction.PlanField, "customfield_10051")
				}
				if cfg.Jira.Extraction.SubtaskField != "subtasks" {
					t.Errorf("Jira.Extraction.SubtaskField = %q, want %q", cfg.Jira.Extraction.SubtaskField, "subtasks")
				}
				if cfg.GitHub.Owner != "myorg" {
					t.Errorf("GitHub.Owner = %q, want %q", cfg.GitHub.Owner, "myorg")
				}
				if cfg.GitHub.Repo != "my-service" {
					t.Errorf("GitHub.Repo = %q, want %q", cfg.GitHub.Repo, "my-service")
				}
				if !cfg.GitHub.FetchComments {
					t.Error("GitHub.FetchComments = false, want true")
				}
				if cfg.GitHub.Spec.StartMarker != "<!-- spec:start -->" {
					t.Errorf("GitHub.Spec.StartMarker = %q, want %q", cfg.GitHub.Spec.StartMarker, "<!-- spec:start -->")
				}
				if cfg.GitHub.Spec.EndMarker != "<!-- spec:end -->" {
					t.Errorf("GitHub.Spec.EndMarker = %q, want %q", cfg.GitHub.Spec.EndMarker, "<!-- spec:end -->")
				}
				if cfg.GitHub.Plan.StartMarker != "<!-- plan:start -->" {
					t.Errorf("GitHub.Plan.StartMarker = %q, want %q", cfg.GitHub.Plan.StartMarker, "<!-- plan:start -->")
				}
				if cfg.GitHub.Plan.EndMarker != "<!-- plan:end -->" {
					t.Errorf("GitHub.Plan.EndMarker = %q, want %q", cfg.GitHub.Plan.EndMarker, "<!-- plan:end -->")
				}
				// Monitor config
				if cfg.Monitor.Profile != "smart" {
					t.Errorf("Monitor.Profile = %q, want %q", cfg.Monitor.Profile, "smart")
				}
				if cfg.Monitor.SelfUser != "soda-bot" {
					t.Errorf("Monitor.SelfUser = %q, want %q", cfg.Monitor.SelfUser, "soda-bot")
				}
				if len(cfg.Monitor.BotUsers) != 2 {
					t.Fatalf("len(Monitor.BotUsers) = %d, want 2", len(cfg.Monitor.BotUsers))
				}
				if cfg.Monitor.BotUsers[0] != "dependabot" {
					t.Errorf("Monitor.BotUsers[0] = %q, want %q", cfg.Monitor.BotUsers[0], "dependabot")
				}
				if cfg.Monitor.BotUsers[1] != "renovate" {
					t.Errorf("Monitor.BotUsers[1] = %q, want %q", cfg.Monitor.BotUsers[1], "renovate")
				}
				if cfg.Monitor.CODEOWNERS != ".github/CODEOWNERS" {
					t.Errorf("Monitor.CODEOWNERS = %q, want %q", cfg.Monitor.CODEOWNERS, ".github/CODEOWNERS")
				}
			},
		},
		{
			name: "minimal config",
			file: "testdata/minimal.yaml",
			check: func(t *testing.T, cfg *Config) {
				if cfg.TicketSource != "jira" {
					t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "jira")
				}
				if len(cfg.Repos) != 0 {
					t.Errorf("len(Repos) = %d, want 0", len(cfg.Repos))
				}
				// Monitor config should be zero-valued when not in file.
				if cfg.Monitor.SelfUser != "" {
					t.Errorf("Monitor.SelfUser = %q, want empty", cfg.Monitor.SelfUser)
				}
				if cfg.Monitor.Profile != "" {
					t.Errorf("Monitor.Profile = %q, want empty", cfg.Monitor.Profile)
				}
				if len(cfg.Monitor.BotUsers) != 0 {
					t.Errorf("len(Monitor.BotUsers) = %d, want 0", len(cfg.Monitor.BotUsers))
				}
			},
		},
		{
			name:    "missing file",
			file:    "testdata/nonexistent.yaml",
			wantErr: true,
			check: func(t *testing.T, _ *Config) {
				// Error check is below
			},
		},
		{
			name:    "malformed yaml",
			file:    "testdata/malformed.yaml",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.file)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.name == "missing file" && !errors.Is(err, os.ErrNotExist) {
					t.Errorf("expected os.ErrNotExist, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("DefaultPath() = %q, want absolute path", path)
	}
	if filepath.Base(path) != "config.yaml" {
		t.Errorf("DefaultPath() base = %q, want config.yaml", filepath.Base(path))
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.TicketSource != "github" {
		t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "github")
	}
	if cfg.Mode != "autonomous" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "autonomous")
	}
	if cfg.Model == "" {
		t.Error("Model is empty")
	}
	if cfg.StateDir != ".soda" {
		t.Errorf("StateDir = %q, want %q", cfg.StateDir, ".soda")
	}
	if cfg.WorktreeDir != ".worktrees" {
		t.Errorf("WorktreeDir = %q, want %q", cfg.WorktreeDir, ".worktrees")
	}
	if cfg.Limits.MaxCostPerTicket <= 0 {
		t.Errorf("MaxCostPerTicket = %f, want > 0", cfg.Limits.MaxCostPerTicket)
	}
	if cfg.Limits.MaxCostPerPhase <= 0 {
		t.Errorf("MaxCostPerPhase = %f, want > 0", cfg.Limits.MaxCostPerPhase)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("len(Repos) = %d, want 1", len(cfg.Repos))
	}
	if cfg.Repos[0].Forge != "github" {
		t.Errorf("Repos[0].Forge = %q, want %q", cfg.Repos[0].Forge, "github")
	}
	if cfg.GitHub.Owner != "your-org" {
		t.Errorf("GitHub.Owner = %q, want %q", cfg.GitHub.Owner, "your-org")
	}
	if cfg.Monitor.SelfUser != "" {
		t.Errorf("Monitor.SelfUser = %q, want empty (so runMonitor falls back to stub)", cfg.Monitor.SelfUser)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	original := DefaultConfig()

	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Marshal() returned empty data")
	}

	var roundTripped Config
	if err := yaml.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal round-tripped data: %v", err)
	}

	if roundTripped.TicketSource != original.TicketSource {
		t.Errorf("TicketSource = %q, want %q", roundTripped.TicketSource, original.TicketSource)
	}
	if roundTripped.Mode != original.Mode {
		t.Errorf("Mode = %q, want %q", roundTripped.Mode, original.Mode)
	}
	if roundTripped.Model != original.Model {
		t.Errorf("Model = %q, want %q", roundTripped.Model, original.Model)
	}
	if roundTripped.StateDir != original.StateDir {
		t.Errorf("StateDir = %q, want %q", roundTripped.StateDir, original.StateDir)
	}
	if len(roundTripped.Repos) != len(original.Repos) {
		t.Fatalf("len(Repos) = %d, want %d", len(roundTripped.Repos), len(original.Repos))
	}
	if roundTripped.Repos[0].Name != original.Repos[0].Name {
		t.Errorf("Repos[0].Name = %q, want %q", roundTripped.Repos[0].Name, original.Repos[0].Name)
	}
}

func TestMarshal_EmptySelfUserComment(t *testing.T) {
	cfg := DefaultConfig()
	// SelfUser should be empty by default.
	if cfg.Monitor.SelfUser != "" {
		t.Fatalf("DefaultConfig().Monitor.SelfUser = %q, want empty", cfg.Monitor.SelfUser)
	}

	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	yaml := string(data)
	if !strings.Contains(yaml, "REQUIRED") {
		t.Errorf("expected REQUIRED comment in marshalled output when SelfUser is empty, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "self_user") {
		t.Errorf("expected self_user field in marshalled output, got:\n%s", yaml)
	}

	// When SelfUser is set, the comment should NOT appear.
	cfg.Monitor.SelfUser = "my-bot"
	data, err = Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	yamlSet := string(data)
	if strings.Contains(yamlSet, "REQUIRED") {
		t.Errorf("REQUIRED comment should not appear when SelfUser is set, got:\n%s", yamlSet)
	}
}

func TestMarshalRoundTrip_MonitorConfig(t *testing.T) {
	original := DefaultConfig()
	original.Monitor = MonitorConfig{
		Profile:    "smart",
		SelfUser:   "soda-bot",
		BotUsers:   []string{"dependabot", "renovate"},
		CODEOWNERS: ".github/CODEOWNERS",
	}

	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var roundTripped Config
	if err := yaml.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if roundTripped.Monitor.Profile != "smart" {
		t.Errorf("Monitor.Profile = %q, want %q", roundTripped.Monitor.Profile, "smart")
	}
	if roundTripped.Monitor.SelfUser != "soda-bot" {
		t.Errorf("Monitor.SelfUser = %q, want %q", roundTripped.Monitor.SelfUser, "soda-bot")
	}
	if len(roundTripped.Monitor.BotUsers) != 2 {
		t.Fatalf("len(Monitor.BotUsers) = %d, want 2", len(roundTripped.Monitor.BotUsers))
	}
	if roundTripped.Monitor.BotUsers[0] != "dependabot" {
		t.Errorf("Monitor.BotUsers[0] = %q, want %q", roundTripped.Monitor.BotUsers[0], "dependabot")
	}
	if roundTripped.Monitor.CODEOWNERS != ".github/CODEOWNERS" {
		t.Errorf("Monitor.CODEOWNERS = %q, want %q", roundTripped.Monitor.CODEOWNERS, ".github/CODEOWNERS")
	}
}

func TestRepoConfigFieldParity(t *testing.T) {
	configType := reflect.TypeOf(RepoConfig{})
	pipelineType := reflect.TypeOf(pipeline.RepoConfig{})

	configFields := make(map[string]reflect.StructField)
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		configFields[field.Name] = field
	}

	for i := 0; i < pipelineType.NumField(); i++ {
		pipeField := pipelineType.Field(i)
		configField, ok := configFields[pipeField.Name]
		if !ok {
			t.Errorf("pipeline.RepoConfig has field %q not found in config.RepoConfig", pipeField.Name)
			continue
		}
		if configField.Type != pipeField.Type {
			t.Errorf("field %q type mismatch: config=%v, pipeline=%v", pipeField.Name, configField.Type, pipeField.Type)
		}
		delete(configFields, pipeField.Name)
	}

	for name := range configFields {
		t.Errorf("config.RepoConfig has field %q not found in pipeline.RepoConfig", name)
	}
}
