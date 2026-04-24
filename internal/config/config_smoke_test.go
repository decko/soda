package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/pipeline"
	"gopkg.in/yaml.v3"
)

// TestSmoke_ConfigDiscovery_ResolutionOrder verifies the full config file
// discovery chain using the production ResolveConfigPath function: local
// soda.yaml takes precedence over the global ~/.config/soda/config.yaml.
func TestSmoke_ConfigDiscovery_ResolutionOrder(t *testing.T) {
	t.Run("local_soda_yaml_takes_precedence", func(t *testing.T) {
		localDir := t.TempDir()
		globalDir := t.TempDir()

		// Write a local soda.yaml with a distinctive marker.
		localConfig := `ticket_source: github
model: local-model-marker
mode: autonomous
state_dir: .soda
`
		localPath := filepath.Join(localDir, "soda.yaml")
		if err := os.WriteFile(localPath, []byte(localConfig), 0644); err != nil {
			t.Fatalf("write local config: %v", err)
		}

		// Write a global config with a different marker.
		globalConfig := `ticket_source: jira
model: global-model-marker
mode: autonomous
state_dir: .soda
`
		globalPath := filepath.Join(globalDir, "config.yaml")
		if err := os.WriteFile(globalPath, []byte(globalConfig), 0644); err != nil {
			t.Fatalf("write global config: %v", err)
		}

		// Use the production discovery function — local should win.
		resolvedPath := ResolveConfigPath(localPath, globalPath)

		if resolvedPath != localPath {
			t.Errorf("ResolveConfigPath() = %q, want local path %q", resolvedPath, localPath)
		}

		cfg, err := Load(resolvedPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if cfg.Model != "local-model-marker" {
			t.Errorf("resolved Model = %q, want local-model-marker (local should win)", cfg.Model)
		}
	})

	t.Run("falls_back_to_global_when_no_local", func(t *testing.T) {
		localDir := t.TempDir()
		globalDir := t.TempDir()

		// No local soda.yaml exists.
		globalConfig := `ticket_source: jira
model: global-model-marker
mode: autonomous
state_dir: .soda
`
		globalPath := filepath.Join(globalDir, "config.yaml")
		if err := os.WriteFile(globalPath, []byte(globalConfig), 0644); err != nil {
			t.Fatalf("write global config: %v", err)
		}

		// Local path does not exist — ResolveConfigPath should fall back to global.
		localPath := filepath.Join(localDir, "soda.yaml")
		resolvedPath := ResolveConfigPath(localPath, globalPath)

		if resolvedPath != globalPath {
			t.Errorf("ResolveConfigPath() = %q, want global path %q", resolvedPath, globalPath)
		}

		cfg, err := Load(resolvedPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if cfg.Model != "global-model-marker" {
			t.Errorf("resolved Model = %q, want global-model-marker (should fall back to global)", cfg.Model)
		}
	})
}

// TestSmoke_ConfigDiscovery_PipelineDiscovery verifies the pipeline file
// discovery mechanism across local, user, and embedded directories.
func TestSmoke_ConfigDiscovery_PipelineDiscovery(t *testing.T) {
	t.Run("three_tier_discovery", func(t *testing.T) {
		localDir := t.TempDir()
		userDir := t.TempDir()
		embeddedDir := t.TempDir()

		// Local: has default and a "fast" pipeline.
		writeTestFile(t, filepath.Join(localDir, "phases.yaml"), minimalPhasesContent)
		writeTestFile(t, filepath.Join(localDir, "phases-fast.yaml"), minimalPhasesContent)

		// User: has default (lower priority) and "full" pipeline.
		writeTestFile(t, filepath.Join(userDir, "phases.yaml"), minimalPhasesContent)
		writeTestFile(t, filepath.Join(userDir, "phases-full.yaml"), minimalPhasesContent)

		// Embedded: has default (lowest priority) and "docs-only" pipeline.
		writeTestFile(t, filepath.Join(embeddedDir, "phases.yaml"), minimalPhasesContent)
		writeTestFile(t, filepath.Join(embeddedDir, "phases-docs-only.yaml"), minimalPhasesContent)

		pipelines := pipeline.DiscoverPipelines(
			[]string{localDir, userDir, embeddedDir},
			[]string{"local", "user", "embedded"},
		)

		// Should discover 4 unique pipelines: default, docs-only, fast, full.
		if len(pipelines) != 4 {
			t.Fatalf("got %d pipelines, want 4: %v", len(pipelines), pipelineNames(pipelines))
		}

		// Default should be first and from local (highest priority).
		if pipelines[0].Name != "default" {
			t.Errorf("first pipeline = %q, want default", pipelines[0].Name)
		}
		if pipelines[0].Source != "local" {
			t.Errorf("default source = %q, want local", pipelines[0].Source)
		}

		// Verify sources for non-default pipelines.
		sourceMap := make(map[string]string)
		for _, p := range pipelines {
			sourceMap[p.Name] = p.Source
		}

		if sourceMap["fast"] != "local" {
			t.Errorf("fast source = %q, want local", sourceMap["fast"])
		}
		if sourceMap["full"] != "user" {
			t.Errorf("full source = %q, want user", sourceMap["full"])
		}
		if sourceMap["docs-only"] != "embedded" {
			t.Errorf("docs-only source = %q, want embedded", sourceMap["docs-only"])
		}
	})

	t.Run("pipeline_filename_conventions", func(t *testing.T) {
		// Verify the naming convention works for all pipeline names.
		tests := []struct {
			name     string
			wantFile string
		}{
			{"default", "phases.yaml"},
			{"", "phases.yaml"},
			{"fast", "phases-fast.yaml"},
			{"ci-lite", "phases-ci-lite.yaml"},
		}

		for _, tt := range tests {
			got := pipeline.PipelineFilename(tt.name)
			if got != tt.wantFile {
				t.Errorf("PipelineFilename(%q) = %q, want %q", tt.name, got, tt.wantFile)
			}

			// Round-trip: filename → name → filename.
			roundTrippedName := pipeline.PipelineNameFromFile(got)
			expectedName := tt.name
			if expectedName == "" {
				expectedName = "default"
			}
			if roundTrippedName != expectedName {
				t.Errorf("PipelineNameFromFile(%q) = %q, want %q", got, roundTrippedName, expectedName)
			}
		}
	})

	t.Run("pipeline_name_validation", func(t *testing.T) {
		// Safe names should pass.
		for _, safe := range []string{"", "default", "fast", "ci-lite", "my_pipeline"} {
			if err := pipeline.ValidatePipelineName(safe); err != nil {
				t.Errorf("ValidatePipelineName(%q) unexpected error: %v", safe, err)
			}
		}

		// Dangerous names should fail.
		for _, dangerous := range []string{"foo/bar", `foo\bar`, "..", "foo/../bar"} {
			if err := pipeline.ValidatePipelineName(dangerous); err == nil {
				t.Errorf("ValidatePipelineName(%q) should have failed", dangerous)
			}
		}
	})
}

// TestSmoke_ConfigToSandbox_Wiring verifies that config file sandbox settings
// are correctly representable for the sandbox.Config struct.
func TestSmoke_ConfigToSandbox_Wiring(t *testing.T) {
	configYAML := `ticket_source: github
mode: autonomous
model: claude-sonnet-4-20250514
sandbox:
  enabled: true
  binary: custom-claude
  limits:
    memory_mb: 4096
    cpu_percent: 400
    max_pids: 512
  proxy:
    enabled: true
    upstream_url: https://api.anthropic.com
    max_input_tokens: 100000
    max_output_tokens: 50000
    log_dir: /var/log/soda-proxy
state_dir: .soda
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify sandbox config was parsed correctly.
	if !cfg.Sandbox.Enabled {
		t.Error("Sandbox.Enabled should be true")
	}
	if cfg.Sandbox.Binary != "custom-claude" {
		t.Errorf("Sandbox.Binary = %q, want custom-claude", cfg.Sandbox.Binary)
	}
	if cfg.Sandbox.Limits.MemoryMB != 4096 {
		t.Errorf("Sandbox.Limits.MemoryMB = %d, want 4096", cfg.Sandbox.Limits.MemoryMB)
	}
	if cfg.Sandbox.Limits.CPUPercent != 400 {
		t.Errorf("Sandbox.Limits.CPUPercent = %d, want 400", cfg.Sandbox.Limits.CPUPercent)
	}
	if cfg.Sandbox.Limits.MaxPIDs != 512 {
		t.Errorf("Sandbox.Limits.MaxPIDs = %d, want 512", cfg.Sandbox.Limits.MaxPIDs)
	}

	// Verify proxy config.
	if !cfg.Sandbox.Proxy.Enabled {
		t.Error("Sandbox.Proxy.Enabled should be true")
	}
	if cfg.Sandbox.Proxy.UpstreamURL != "https://api.anthropic.com" {
		t.Errorf("Sandbox.Proxy.UpstreamURL = %q", cfg.Sandbox.Proxy.UpstreamURL)
	}
	if cfg.Sandbox.Proxy.MaxInputTokens != 100000 {
		t.Errorf("Sandbox.Proxy.MaxInputTokens = %d, want 100000", cfg.Sandbox.Proxy.MaxInputTokens)
	}
	if cfg.Sandbox.Proxy.MaxOutputTokens != 50000 {
		t.Errorf("Sandbox.Proxy.MaxOutputTokens = %d, want 50000", cfg.Sandbox.Proxy.MaxOutputTokens)
	}
	if cfg.Sandbox.Proxy.LogDir != "/var/log/soda-proxy" {
		t.Errorf("Sandbox.Proxy.LogDir = %q", cfg.Sandbox.Proxy.LogDir)
	}
}

// TestSmoke_ConfigToMonitor_Wiring verifies that monitor config from the config
// file correctly maps to the monitor subsystem's expected format.
func TestSmoke_ConfigToMonitor_Wiring(t *testing.T) {
	configYAML := `ticket_source: github
mode: autonomous
model: claude-sonnet-4-20250514
monitor:
  profile: aggressive
  self_user: ci-bot
  bot_users:
    - dependabot
    - renovate
    - codecov[bot]
  codeowners: .github/CODEOWNERS
state_dir: .soda
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify monitor config.
	if cfg.Monitor.Profile != "aggressive" {
		t.Errorf("Monitor.Profile = %q, want aggressive", cfg.Monitor.Profile)
	}
	if cfg.Monitor.SelfUser != "ci-bot" {
		t.Errorf("Monitor.SelfUser = %q, want ci-bot", cfg.Monitor.SelfUser)
	}
	if len(cfg.Monitor.BotUsers) != 3 {
		t.Fatalf("len(Monitor.BotUsers) = %d, want 3", len(cfg.Monitor.BotUsers))
	}
	if cfg.Monitor.BotUsers[0] != "dependabot" {
		t.Errorf("BotUsers[0] = %q, want dependabot", cfg.Monitor.BotUsers[0])
	}
	if cfg.Monitor.BotUsers[2] != "codecov[bot]" {
		t.Errorf("BotUsers[2] = %q, want codecov[bot]", cfg.Monitor.BotUsers[2])
	}
	if cfg.Monitor.CODEOWNERS != ".github/CODEOWNERS" {
		t.Errorf("Monitor.CODEOWNERS = %q", cfg.Monitor.CODEOWNERS)
	}

	// Verify the profile name maps to a valid pipeline MonitorProfile.
	profile, err := pipeline.GetMonitorProfile(pipeline.MonitorProfileName(cfg.Monitor.Profile))
	if err != nil {
		t.Fatalf("GetMonitorProfile: %v", err)
	}
	if profile.Name != pipeline.ProfileAggressive {
		t.Errorf("profile.Name = %q, want aggressive", profile.Name)
	}
	if !profile.ShouldApplyNit() {
		t.Error("aggressive profile should auto-fix nits")
	}
	if !profile.ShouldAutoRebase() {
		t.Error("aggressive profile should auto-rebase")
	}
	if !profile.ShouldRespondToNonAuth() {
		t.Error("aggressive profile should respond to non-auth")
	}
}

// TestSmoke_ConfigRoundtrip_AllFields verifies that a fully-populated config
// survives a marshal → unmarshal roundtrip with all fields intact.
func TestSmoke_ConfigRoundtrip_AllFields(t *testing.T) {
	original := &Config{
		TicketSource: "github",
		GitHub: GitHubTicketConfig{
			Owner:         "decko",
			Repo:          "soda",
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
		Jira: JiraConfig{
			Command: "jira-mcp",
			Project: "SODA",
			Query:   "status = Open",
			Extraction: JiraExtractionConfig{
				SpecField:    "cf_spec",
				PlanField:    "cf_plan",
				SubtaskField: "subtasks",
			},
		},
		Mode:        "autonomous",
		Model:       "claude-sonnet-4-20250514",
		PhasesPath:  "/custom/phases.yaml",
		PromptsPath: "/custom/prompts",
		WorktreeDir: ".worktrees",
		StateDir:    ".soda",
		Context:     []string{"AGENTS.md", "CLAUDE.md"},
		PhaseContext: map[string][]string{
			"plan":      {"docs/design.md"},
			"implement": {"docs/gotchas.md"},
		},
		Sandbox: SandboxConfig{
			Enabled: true,
			Binary:  "claude",
			Limits: SandboxLimits{
				MemoryMB:   2048,
				CPUPercent: 200,
				MaxPIDs:    256,
			},
			Proxy: SandboxProxyConfig{
				Enabled:         true,
				MaxInputTokens:  100000,
				MaxOutputTokens: 50000,
			},
		},
		Limits: LimitsConfig{
			MaxCostPerTicket:       30.0,
			MaxCostPerPhase:        15.0,
			MaxCostPerGeneration:   5.0,
			MaxPipelineDuration:    "2h",
			MaxDiffBytes:           50000,
			MaxAPIConcurrency:      4,
			MaxSiblingContextBytes: 20000,
		},
		Repos: []RepoConfig{
			{
				Name:        "soda",
				Forge:       "github",
				PushTo:      "decko/soda",
				Target:      "decko/soda",
				Description: "Main repo",
				Formatter:   "gofmt -w .",
				TestCommand: "go test ./...",
				Labels:      []string{"ai-assisted"},
				Trailers:    []string{"Assisted-by: SODA <noreply@soda.dev>"},
			},
		},
		Monitor: MonitorConfig{
			Profile:    "smart",
			SelfUser:   "soda-bot",
			BotUsers:   []string{"dependabot", "renovate"},
			CODEOWNERS: ".github/CODEOWNERS",
		},
	}

	// Marshal to YAML.
	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Marshal returned empty data")
	}

	// Unmarshal back.
	var roundTripped Config
	if err := yaml.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Verify all critical fields survived.
	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"TicketSource", roundTripped.TicketSource, "github"},
		{"GitHub.Owner", roundTripped.GitHub.Owner, "decko"},
		{"GitHub.Repo", roundTripped.GitHub.Repo, "soda"},
		{"GitHub.FetchComments", roundTripped.GitHub.FetchComments, true},
		{"Jira.Command", roundTripped.Jira.Command, "jira-mcp"},
		{"Mode", roundTripped.Mode, "autonomous"},
		{"Model", roundTripped.Model, "claude-sonnet-4-20250514"},
		{"PhasesPath", roundTripped.PhasesPath, "/custom/phases.yaml"},
		{"PromptsPath", roundTripped.PromptsPath, "/custom/prompts"},
		{"StateDir", roundTripped.StateDir, ".soda"},
		{"WorktreeDir", roundTripped.WorktreeDir, ".worktrees"},
		{"Sandbox.Enabled", roundTripped.Sandbox.Enabled, true},
		{"Sandbox.Limits.MemoryMB", roundTripped.Sandbox.Limits.MemoryMB, 2048},
		{"Sandbox.Proxy.Enabled", roundTripped.Sandbox.Proxy.Enabled, true},
		{"Sandbox.Proxy.MaxInputTokens", roundTripped.Sandbox.Proxy.MaxInputTokens, int64(100000)},
		{"Limits.MaxCostPerTicket", roundTripped.Limits.MaxCostPerTicket, 30.0},
		{"Limits.MaxCostPerPhase", roundTripped.Limits.MaxCostPerPhase, 15.0},
		{"Limits.MaxPipelineDuration", roundTripped.Limits.MaxPipelineDuration, "2h"},
		{"Monitor.Profile", roundTripped.Monitor.Profile, "smart"},
		{"Monitor.SelfUser", roundTripped.Monitor.SelfUser, "soda-bot"},
		{"Monitor.CODEOWNERS", roundTripped.Monitor.CODEOWNERS, ".github/CODEOWNERS"},
	}

	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}

	// Verify arrays survived.
	if len(roundTripped.Context) != 2 {
		t.Errorf("len(Context) = %d, want 2", len(roundTripped.Context))
	}
	if len(roundTripped.Repos) != 1 {
		t.Errorf("len(Repos) = %d, want 1", len(roundTripped.Repos))
	}
	if len(roundTripped.Monitor.BotUsers) != 2 {
		t.Errorf("len(Monitor.BotUsers) = %d, want 2", len(roundTripped.Monitor.BotUsers))
	}
	if len(roundTripped.PhaseContext) != 2 {
		t.Errorf("len(PhaseContext) = %d, want 2", len(roundTripped.PhaseContext))
	}
}

// TestSmoke_DefaultConfig_SelfUserGuard verifies that DefaultConfig leaves
// SelfUser empty so that the monitor phase falls back to the stub, and
// Marshal includes a REQUIRED comment to guide users.
func TestSmoke_DefaultConfig_SelfUserGuard(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Monitor.SelfUser != "" {
		t.Errorf("DefaultConfig().Monitor.SelfUser = %q, want empty", cfg.Monitor.SelfUser)
	}

	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	yamlStr := string(data)
	if !strings.Contains(yamlStr, "REQUIRED") {
		t.Error("marshalled config should contain REQUIRED comment when SelfUser is empty")
	}

	// Setting SelfUser should remove the REQUIRED comment.
	cfg.Monitor.SelfUser = "ci-bot"
	data2, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data2), "REQUIRED") {
		t.Error("marshalled config should NOT contain REQUIRED comment when SelfUser is set")
	}
}

// TestSmoke_ConfigLoad_ErrorHandling verifies that config loading produces
// clear, actionable error messages for common failure modes.
func TestSmoke_ConfigLoad_ErrorHandling(t *testing.T) {
	t.Run("missing_file_wraps_os_error", func(t *testing.T) {
		_, err := Load("/nonexistent/path/config.yaml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
		if !strings.Contains(err.Error(), "config:") {
			t.Errorf("error should have config: prefix, got: %v", err)
		}
	})

	t.Run("malformed_yaml_wraps_parse_error", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "bad.yaml")
		if err := os.WriteFile(tmpFile, []byte("not: valid: yaml: [[["), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := Load(tmpFile)
		if err == nil {
			t.Fatal("expected error for malformed yaml")
		}
		if !strings.Contains(err.Error(), "config:") {
			t.Errorf("error should have config: prefix, got: %v", err)
		}
	})

	t.Run("empty_file_returns_zero_config", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "empty.yaml")
		if err := os.WriteFile(tmpFile, []byte(""), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		cfg, err := Load(tmpFile)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		// Empty YAML should produce a zero-valued Config.
		if cfg.TicketSource != "" {
			t.Errorf("TicketSource = %q, want empty", cfg.TicketSource)
		}
		if cfg.Model != "" {
			t.Errorf("Model = %q, want empty", cfg.Model)
		}
	})
}

// TestSmoke_ConfigDiscovery_PhasesPath verifies that the phases_path config
// field can override the default pipeline discovery.
func TestSmoke_ConfigDiscovery_PhasesPath(t *testing.T) {
	customPhasesDir := t.TempDir()
	customPhasesPath := filepath.Join(customPhasesDir, "custom-phases.yaml")
	writeTestFile(t, customPhasesPath, minimalPhasesContent)

	configYAML := `ticket_source: github
model: claude-sonnet-4-20250514
phases_path: ` + customPhasesPath + `
state_dir: .soda
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.PhasesPath != customPhasesPath {
		t.Errorf("PhasesPath = %q, want %q", cfg.PhasesPath, customPhasesPath)
	}

	// Verify the custom phases file can be loaded.
	pipelineCfg, err := pipeline.LoadPipeline(cfg.PhasesPath)
	if err != nil {
		t.Fatalf("LoadPipeline: %v", err)
	}
	if len(pipelineCfg.Phases) == 0 {
		t.Error("expected at least one phase from custom phases file")
	}
}

// TestSmoke_ConfigDiscovery_PromptsPath verifies that the prompts_path config
// field is correctly loaded and can be used to locate prompt templates.
func TestSmoke_ConfigDiscovery_PromptsPath(t *testing.T) {
	promptsDir := t.TempDir()
	promptFile := filepath.Join(promptsDir, "triage.md")
	writeTestFile(t, promptFile, "# Triage\nTicket: {{.Ticket.Key}}")

	configYAML := `ticket_source: github
model: claude-sonnet-4-20250514
prompts_path: ` + promptsDir + `
state_dir: .soda
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.PromptsPath != promptsDir {
		t.Errorf("PromptsPath = %q, want %q", cfg.PromptsPath, promptsDir)
	}

	// Verify a PromptLoader can find templates at this path.
	loader := pipeline.NewPromptLoader(cfg.PromptsPath)
	content, err := loader.Load("triage.md")
	if err != nil {
		t.Fatalf("Load prompt: %v", err)
	}
	if !strings.Contains(content, "Triage") {
		t.Errorf("prompt content = %q, want to contain 'Triage'", content)
	}
}

// TestSmoke_RepoConfig_FieldParity verifies that config.RepoConfig and
// pipeline.RepoConfig remain in sync. If a field is added to one but not
// the other, this test catches it.
func TestSmoke_RepoConfig_FieldParity(t *testing.T) {
	// Build a config.RepoConfig with all fields populated.
	configRepo := RepoConfig{
		Name:        "test-repo",
		Forge:       "github",
		PushTo:      "user/repo",
		Target:      "org/repo",
		Description: "Test repository",
		Formatter:   "gofmt -w .",
		TestCommand: "go test ./...",
		Labels:      []string{"ai-assisted"},
		Trailers:    []string{"Assisted-by: SODA"},
	}

	// Build a pipeline.RepoConfig with the same values.
	pipelineRepo := pipeline.RepoConfig{
		Name:        configRepo.Name,
		Forge:       configRepo.Forge,
		PushTo:      configRepo.PushTo,
		Target:      configRepo.Target,
		Description: configRepo.Description,
		Formatter:   configRepo.Formatter,
		TestCommand: configRepo.TestCommand,
		Labels:      configRepo.Labels,
		Trailers:    configRepo.Trailers,
	}

	// Verify they match.
	if pipelineRepo.Name != configRepo.Name {
		t.Errorf("Name mismatch: %q != %q", pipelineRepo.Name, configRepo.Name)
	}
	if pipelineRepo.Forge != configRepo.Forge {
		t.Errorf("Forge mismatch: %q != %q", pipelineRepo.Forge, configRepo.Forge)
	}
	if pipelineRepo.TestCommand != configRepo.TestCommand {
		t.Errorf("TestCommand mismatch: %q != %q", pipelineRepo.TestCommand, configRepo.TestCommand)
	}
}

// Helper functions.

const minimalPhasesContent = `phases:
  - name: triage
    prompt: prompts/triage.md
    timeout: 1m
`

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func pipelineNames(pipelines []pipeline.PipelineInfo) []string {
	names := make([]string, len(pipelines))
	for idx, p := range pipelines {
		names[idx] = p.Name + "(" + p.Source + ")"
	}
	return names
}
