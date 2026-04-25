package config

import (
	"bytes"
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
	PhasesPath   string              `yaml:"phases_path"`  // explicit path to pipeline YAML; overrides CWD discovery
	PromptsPath  string              `yaml:"prompts_path"` // base directory for prompt templates; overrides CWD discovery
	WorktreeDir  string              `yaml:"worktree_dir"`
	StateDir     string              `yaml:"state_dir"`
	Context      []string            `yaml:"context"`
	PhaseContext map[string][]string `yaml:"phase_context"`
	Repos        []RepoConfig        `yaml:"repos"`
	Monitor      MonitorConfig       `yaml:"monitor"`
	Notify       NotifyConfig        `yaml:"notify"`
}

// MonitorConfig holds monitor phase settings loaded from the config file.
// These values are wired into EngineConfig at startup.
type MonitorConfig struct {
	Profile    string   `yaml:"profile"`    // monitor profile preset: conservative, smart, aggressive
	SelfUser   string   `yaml:"self_user"`  // PR author username for self-comment filtering
	BotUsers   []string `yaml:"bot_users"`  // known bot usernames to filter out
	CODEOWNERS string   `yaml:"codeowners"` // path to CODEOWNERS file for authority resolution
}

// NotifyConfig holds notification hook settings for pipeline completion.
// Both webhook and script may be configured; they fire independently.
type NotifyConfig struct {
	Webhook   *WebhookNotifyConfig `yaml:"webhook,omitempty"`    // HTTP POST notification (fires on any completion)
	Script    *ScriptNotifyConfig  `yaml:"script,omitempty"`     // script callback (fires on any completion)
	OnFinish  *NotifyHookConfig    `yaml:"on_finish,omitempty"`  // fires on any pipeline completion
	OnFailure *NotifyHookConfig    `yaml:"on_failure,omitempty"` // fires only on failed or timeout
}

// NotifyHookConfig groups a webhook and script for a single trigger condition.
type NotifyHookConfig struct {
	Webhook *WebhookNotifyConfig `yaml:"webhook,omitempty"`
	Script  *ScriptNotifyConfig  `yaml:"script,omitempty"`
}

// WebhookNotifyConfig configures an HTTP POST webhook fired on pipeline completion.
type WebhookNotifyConfig struct {
	URL     string            `yaml:"url"`               // target URL (required)
	Headers map[string]string `yaml:"headers,omitempty"` // extra HTTP headers (e.g., Authorization)
}

// ScriptNotifyConfig configures a script callback fired on pipeline completion.
type ScriptNotifyConfig struct {
	Command string `yaml:"command"` // shell command to execute; receives JSON on stdin
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
	Enabled bool               `yaml:"enabled"`
	Binary  string             `yaml:"binary"`
	Limits  SandboxLimits      `yaml:"limits"`
	Proxy   SandboxProxyConfig `yaml:"proxy"`
}

// SandboxProxyConfig holds LLM proxy settings for sandboxed execution.
// When enabled, API calls are routed through a host-side proxy for
// credential isolation, token metering, and budget enforcement.
type SandboxProxyConfig struct {
	Enabled         bool   `yaml:"enabled"`
	UpstreamURL     string `yaml:"upstream_url,omitempty"`      // defaults to ANTHROPIC_BASE_URL or https://api.anthropic.com
	MaxInputTokens  int64  `yaml:"max_input_tokens,omitempty"`  // per-session budget; 0 = unlimited
	MaxOutputTokens int64  `yaml:"max_output_tokens,omitempty"` // per-session budget; 0 = unlimited
	LogDir          string `yaml:"log_dir,omitempty"`           // request/response log directory; empty = no logging
}

// SandboxLimits holds resource limits for sandboxed execution.
type SandboxLimits struct {
	MemoryMB   int `yaml:"memory_mb"`
	CPUPercent int `yaml:"cpu_percent"`
	MaxPIDs    int `yaml:"max_pids"`
}

// LimitsConfig holds budget and duration limits.
type LimitsConfig struct {
	MaxCostPerTicket       float64           `yaml:"max_cost_per_ticket"`
	MaxCostPerPhase        float64           `yaml:"max_cost_per_phase"`
	MaxCostPerGeneration   float64           `yaml:"max_cost_per_generation,omitempty"`   // per-attempt cost cap; 0 means disabled
	MaxPipelineDuration    string            `yaml:"max_pipeline_duration,omitempty"`     // Go duration string (e.g., "2h", "90m"); 0 or empty means no limit
	MaxDiffBytes           int               `yaml:"max_diff_bytes,omitempty"`            // max bytes of git diff injected into rework prompts; 0 means use default (50000)
	MaxAPIConcurrency      int               `yaml:"max_api_concurrency,omitempty"`       // max concurrent API calls (runner.Run); 0 means unlimited
	MaxSiblingContextBytes int               `yaml:"max_sibling_context_bytes,omitempty"` // max bytes of sibling-function context injected into implement prompts; 0 means use default (20000)
	TokenBudget            TokenBudgetConfig `yaml:"token_budget,omitempty"`              // prompt token budget estimation; zero value disables checks
}

// TokenBudgetConfig configures the prompt-size estimation check that runs
// before each CLI invocation. When WarnTokens > 0, the engine estimates
// the rendered prompt's token count (bytes / BytesPerToken) and emits a
// warning event if the estimate exceeds the threshold. This is a warn-only
// check; it never blocks execution.
type TokenBudgetConfig struct {
	WarnTokens    int     `yaml:"warn_tokens,omitempty"`     // emit warning when estimated prompt tokens exceed this; 0 disables
	BytesPerToken float64 `yaml:"bytes_per_token,omitempty"` // bytes-per-token ratio for estimation; 0 defaults to 3.3
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

// DefaultConfig returns a Config populated with sensible starter defaults.
// The caller can modify the returned value before marshalling it to disk.
func DefaultConfig() *Config {
	return &Config{
		TicketSource: "github",
		GitHub: GitHubTicketConfig{
			Owner:         "your-org",
			Repo:          "your-repo",
			FetchComments: true,
			Spec: ExtractionStrategy{
				StartMarker: "<!-- spec:start -->",
				EndMarker:   "<!-- spec:end -->",
			},
			Plan: ExtractionStrategy{
				StartMarker: "<!-- plan:start -->",
				EndMarker:   "<!-- plan:end -->",
			},
		},
		Mode:        "autonomous",
		Model:       "claude-sonnet-4-20250514",
		WorktreeDir: ".worktrees",
		StateDir:    ".soda",
		Limits: LimitsConfig{
			MaxCostPerTicket: 30.00,
			MaxCostPerPhase:  15.00,
		},
		Context: []string{"AGENTS.md"},
		Repos: []RepoConfig{
			{
				Name:        "your-repo",
				Forge:       "github",
				PushTo:      "your-user/your-repo",
				Target:      "your-org/your-repo",
				Description: "Main repository",
				Formatter:   "gofmt -w .",
				TestCommand: "go test ./...",
				Labels:      []string{"ai-assisted"},
			},
		},
		// Monitor.SelfUser left empty so that the empty-string guard in
		// runMonitor triggers a warning and stub fallback. Users must set
		// self_user explicitly in their config for comment classification.
		Monitor: MonitorConfig{},
	}
}

// Marshal serialises a Config to YAML bytes.
// When SelfUser is empty the serialised output includes an inline comment
// reminding users that the field is required for comment classification.
func Marshal(cfg *Config) ([]byte, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("config: marshal: %w", err)
	}

	// When self_user is empty, replace the bare `self_user: ""` line with
	// a commented-out example so the requirement is obvious in the generated file.
	if cfg.Monitor.SelfUser == "" {
		data = bytes.Replace(
			data,
			[]byte("  self_user: \"\""),
			[]byte("  self_user: \"\" # REQUIRED — set to your bot's GitHub username for comment classification"),
			1,
		)
	}

	return data, nil
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
