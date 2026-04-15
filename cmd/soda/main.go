package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/decko/soda/internal/config"
	"github.com/spf13/cobra"
)

//go:embed all:embeds/prompts
var embeddedPrompts embed.FS

//go:embed embeds/phases.yaml
var embeddedPhases []byte

var version = "dev"

func main() {
	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "soda",
		Short:         "Session-Orchestrated Development Agent",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	defaultPath, _ := config.DefaultPath()
	cmd.PersistentFlags().String("config", defaultPath, "config file path")

	cmd.AddCommand(
		newRunCmd(),
		newStatusCmd(),
		newSessionsCmd(),
		newHistoryCmd(),
		newCleanCmd(),
		newRenderCmd(),
		newVersionCmd(),
	)

	return cmd
}

// loadConfig reads the config file specified by the --config flag.
func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	cfgPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return nil, fmt.Errorf("config flag: %w", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// extractEmbeddedPrompts writes embedded prompt files to a temp dir
// and returns the path. Caller must clean up with os.RemoveAll.
// The extracted tree preserves the prompts/ subdirectory so that
// phases.yaml references (e.g. "prompts/triage.md") resolve correctly.
func extractEmbeddedPrompts() (string, error) {
	tmpDir, err := os.MkdirTemp("", "soda-prompts-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	// Use "embeds" (not "embeds/prompts") to preserve the prompts/ prefix.
	promptsFS, err := fs.Sub(embeddedPrompts, "embeds")
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("embedded prompts: %w", err)
	}

	err = fs.WalkDir(promptsFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(tmpDir, path), 0755)
		}
		data, readErr := fs.ReadFile(promptsFS, path)
		if readErr != nil {
			return readErr
		}
		dest := filepath.Join(tmpDir, path)
		return os.WriteFile(dest, data, 0644)
	})
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("extract prompts: %w", err)
	}

	return tmpDir, nil
}

// resolvePhasesPath returns a path to phases.yaml.
// Prefers a local file in the working directory; falls back to extracting
// the embedded copy to a temp file. Caller must remove the temp file
// if cleanup is non-nil.
func resolvePhasesPath() (path string, cleanup func(), err error) {
	if _, statErr := os.Stat("phases.yaml"); statErr == nil {
		return "phases.yaml", nil, nil
	}

	tmpFile, tmpErr := os.CreateTemp("", "soda-phases-*.yaml")
	if tmpErr != nil {
		return "", nil, fmt.Errorf("create temp phases file: %w", tmpErr)
	}
	if _, writeErr := tmpFile.Write(embeddedPhases); writeErr != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", nil, fmt.Errorf("write embedded phases: %w", writeErr)
	}
	tmpFile.Close()
	return tmpFile.Name(), func() { os.Remove(tmpFile.Name()) }, nil
}
