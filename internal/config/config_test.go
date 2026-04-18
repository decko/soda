package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/decko/soda/internal/pipeline"
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
