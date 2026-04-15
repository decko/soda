package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds all SODA configuration loaded from a YAML file.
type Config struct {
	TicketSource string              `yaml:"ticket_source"`
	Jira         JiraConfig          `yaml:"jira"`
	GitHub       GitHubTicketConfig  `yaml:"github"`
	Mode         string              `yaml:"mode"`
	Model        string              `yaml:"model"`
	Sandbox      SandboxConfig       `yaml:"sandbox"`
	Limits       LimitsConfig        `yaml:"limits"`
	WorktreeDir  string              `yaml:"worktree_dir"`
	StateDir     string              `yaml:"state_dir"`
	Context      []string            `yaml:"context"`
	PhaseContext map[string][]string `yaml:"phase_context"`
	Repos        []RepoConfig        `yaml:"repos"`
}

// JiraConfig holds Jira ticket source settings.
type JiraConfig struct {
	Command    string               `yaml:"command"`
	Project    string               `yaml:"project"`
	Query      string               `yaml:"query"`
	Extraction JiraExtractionConfig `yaml:"extraction"`
}

// JiraExtractionConfig configures how to extract spec/plan artifacts from
// Jira tickets. Multiple strategies can be configured; they are applied in
// order: description markers, custom fields, subtasks. The first strategy
// that finds content wins (later strategies do not overwrite).
type JiraExtractionConfig struct {
	Spec         ExtractionStrategy `yaml:"spec"`
	Plan         ExtractionStrategy `yaml:"plan"`
	SpecField    string             `yaml:"spec_field"`
	PlanField    string             `yaml:"plan_field"`
	SubtaskField string             `yaml:"subtask_field"`
}

// GitHubTicketConfig holds GitHub Issues ticket source settings.
type GitHubTicketConfig struct {
	Owner         string             `yaml:"owner"`
	Repo          string             `yaml:"repo"`
	FetchComments bool               `yaml:"fetch_comments"`
	Spec          ExtractionStrategy `yaml:"spec"`
	Plan          ExtractionStrategy `yaml:"plan"`
}

// ExtractionStrategy configures how to extract a named artifact from ticket
// comments. The extractor scans comments for lines matching StartMarker /
// EndMarker and captures the text between them.
type ExtractionStrategy struct {
	StartMarker string `yaml:"start_marker"`
	EndMarker   string `yaml:"end_marker"`
}

// SandboxConfig holds sandbox execution settings.
type SandboxConfig struct {
	Enabled bool          `yaml:"enabled"`
	Binary  string        `yaml:"binary"`
	Limits  SandboxLimits `yaml:"limits"`
}

// SandboxLimits holds resource limits for sandboxed execution.
type SandboxLimits struct {
	MemoryMB   int `yaml:"memory_mb"`
	CPUPercent int `yaml:"cpu_percent"`
	MaxPIDs    int `yaml:"max_pids"`
}

// LimitsConfig holds budget limits.
type LimitsConfig struct {
	MaxCostPerTicket float64 `yaml:"max_cost_per_ticket"`
	MaxCostPerPhase  float64 `yaml:"max_cost_per_phase"`
}

// RepoConfig holds per-repo configuration.
// Mirrors pipeline.RepoConfig — kept separate to avoid config importing pipeline.
type RepoConfig struct {
	Name        string   `yaml:"name"`
	Forge       string   `yaml:"forge"`
	PushTo      string   `yaml:"push_to"`
	Target      string   `yaml:"target"`
	Description string   `yaml:"description"`
	Formatter   string   `yaml:"formatter"`
	TestCommand string   `yaml:"test_command"`
	Labels      []string `yaml:"labels"`
	Trailers    []string `yaml:"trailers"`
}

// Load reads and parses a YAML config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	return &cfg, nil
}

// DefaultPath returns the default config file path: ~/.config/soda/config.yaml.
func DefaultPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("config: cannot determine config directory: %w", err)
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "soda", "config.yaml"), nil
}
