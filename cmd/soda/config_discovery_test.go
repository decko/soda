package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/config"
	"github.com/spf13/cobra"
)

// configDiscoveryCmd builds a parent + child cobra.Command pair that mirrors
// how newRootCmd registers the persistent "config" flag. The provided RunE
// callback receives the child command, so tests can call loadConfig inside
// it (persistent flags are only fully resolved after command execution).
func configDiscoveryCmd(defaultPath string, fn func(*cobra.Command) error) *cobra.Command {
	root := &cobra.Command{Use: "root"}
	root.PersistentFlags().String("config", defaultPath, "config file path")
	root.SilenceUsage = true
	root.SilenceErrors = true

	child := &cobra.Command{
		Use:           "child",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fn(cmd)
		},
	}
	root.AddCommand(child)

	return root
}

// writeMinimalConfig writes a valid YAML config file that loadConfig can parse.
func writeMinimalConfig(t *testing.T, path, ticketSource string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.TicketSource = ticketSource
	cfg.Mode = "autonomous"
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoadConfig_ExplicitFlag(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "custom.yaml")
	writeMinimalConfig(t, cfgFile, "github")

	var cfg *config.Config
	root := configDiscoveryCmd("", func(cmd *cobra.Command) error {
		var err error
		cfg, err = loadConfig(cmd)
		return err
	})
	root.SetArgs([]string{"child", "--config", cfgFile})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if cfg.TicketSource != "github" {
		t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "github")
	}
	if cfg.Mode != "autonomous" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "autonomous")
	}
}

func TestLoadConfig_ExplicitFlag_MissingFile(t *testing.T) {
	root := configDiscoveryCmd("", func(cmd *cobra.Command) error {
		_, err := loadConfig(cmd)
		return err
	})
	root.SetArgs([]string{"child", "--config", filepath.Join(t.TempDir(), "nonexistent.yaml")})

	if err := root.Execute(); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_LocalSodaYAML(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Write soda.yaml in CWD.
	writeMinimalConfig(t, filepath.Join(dir, "soda.yaml"), "github")

	// Default flag points to a nonexistent global path — loadConfig should
	// discover the local soda.yaml instead.
	var cfg *config.Config
	root := configDiscoveryCmd(filepath.Join(t.TempDir(), "global-does-not-exist.yaml"), func(cmd *cobra.Command) error {
		var err error
		cfg, err = loadConfig(cmd)
		return err
	})
	root.SetArgs([]string{"child"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if cfg.TicketSource != "github" {
		t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "github")
	}
}

func TestLoadConfig_LocalSodaYAML_OverridesGlobal(t *testing.T) {
	localDir := t.TempDir()
	globalDir := t.TempDir()
	t.Chdir(localDir)

	// Write local soda.yaml.
	writeMinimalConfig(t, filepath.Join(localDir, "soda.yaml"), "github")

	// Write global config with a different ticket_source.
	globalPath := filepath.Join(globalDir, "config.yaml")
	writeMinimalConfig(t, globalPath, "jira")

	// Default flag points to global config; local should still win.
	var cfg *config.Config
	root := configDiscoveryCmd(globalPath, func(cmd *cobra.Command) error {
		var err error
		cfg, err = loadConfig(cmd)
		return err
	})
	root.SetArgs([]string{"child"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if cfg.TicketSource != "github" {
		t.Errorf("TicketSource = %q, want %q (local should override global)", cfg.TicketSource, "github")
	}
}

func TestLoadConfig_FallbackToDefaultPath(t *testing.T) {
	t.Chdir(t.TempDir())

	// No soda.yaml in CWD — loadConfig should fall back to the default path.
	globalDir := t.TempDir()
	globalPath := filepath.Join(globalDir, "config.yaml")
	writeMinimalConfig(t, globalPath, "github")

	var cfg *config.Config
	root := configDiscoveryCmd(globalPath, func(cmd *cobra.Command) error {
		var err error
		cfg, err = loadConfig(cmd)
		return err
	})
	root.SetArgs([]string{"child"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if cfg.TicketSource != "github" {
		t.Errorf("TicketSource = %q, want %q", cfg.TicketSource, "github")
	}
}

func TestLoadConfig_NoConfigAnywhere(t *testing.T) {
	t.Chdir(t.TempDir())

	// Default flag points to nonexistent path, no soda.yaml in CWD.
	root := configDiscoveryCmd(filepath.Join(t.TempDir(), "nonexistent.yaml"), func(cmd *cobra.Command) error {
		_, err := loadConfig(cmd)
		return err
	})
	root.SetArgs([]string{"child"})

	if err := root.Execute(); err == nil {
		t.Fatal("expected error when no config exists, got nil")
	}
}

func TestLoadConfig_ExplicitFlagTakesPrecedence(t *testing.T) {
	localDir := t.TempDir()
	explicitDir := t.TempDir()
	t.Chdir(localDir)

	// Write local soda.yaml.
	writeMinimalConfig(t, filepath.Join(localDir, "soda.yaml"), "github")

	// Write explicit config with different ticket_source.
	explicitPath := filepath.Join(explicitDir, "explicit.yaml")
	writeMinimalConfig(t, explicitPath, "jira")

	// Explicitly set --config flag — should win over local soda.yaml.
	var cfg *config.Config
	root := configDiscoveryCmd("", func(cmd *cobra.Command) error {
		var err error
		cfg, err = loadConfig(cmd)
		return err
	})
	root.SetArgs([]string{"child", "--config", explicitPath})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if cfg.TicketSource != "jira" {
		t.Errorf("TicketSource = %q, want %q (explicit flag should take precedence)", cfg.TicketSource, "jira")
	}
}

func TestLoadConfig_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	malformed := filepath.Join(dir, "soda.yaml")
	if err := os.WriteFile(malformed, []byte("{{invalid yaml["), 0644); err != nil {
		t.Fatal(err)
	}

	root := configDiscoveryCmd(filepath.Join(t.TempDir(), "fallback.yaml"), func(cmd *cobra.Command) error {
		_, err := loadConfig(cmd)
		return err
	})
	root.SetArgs([]string{"child"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error = %q, want to contain 'parse'", err.Error())
	}
}
