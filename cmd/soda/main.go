package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

//go:embed all:embeds/prompts
var embeddedPrompts embed.FS

//go:embed embeds/phases.yaml
var embeddedPhases []byte

//go:embed all:embeds/pipelines
var embeddedPipelines embed.FS

// knownEmbeddedPipelines maps pipeline names to their embedded file paths
// within the embeddedPipelines filesystem.
var knownEmbeddedPipelines = map[string]string{
	"quick-fix": "embeds/pipelines/quick-fix.yaml",
	"docs-only": "embeds/pipelines/docs-only.yaml",
}

var version = "dev"

func main() {
	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
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
		newInitCmd(),
		newRunCmd(),
		newPickCmd(),
		newStatusCmd(),
		newSessionsCmd(),
		newHistoryCmd(),
		newLogCmd(),
		newCleanCmd(),
		newCostCmd(),
		newRenderCmd(),
		newValidateCmd(),
		newPipelinesCmd(),
		newSpecCmd(),
		newPluginCmd(),
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

// resolvePhasesPath returns a path to a pipeline configuration file.
// When pipelineName is empty or "default", it resolves to phases.yaml;
// otherwise it resolves to phases-<name>.yaml. The search order is:
//
//  1. Working directory
//  2. User config directory (~/.config/soda/)
//  3. Embedded default (only for the "default" pipeline)
//
// Caller must remove the temp file if cleanup is non-nil.
func resolvePhasesPath(pipelineName string) (path string, cleanup func(), err error) {
	if err := pipeline.ValidatePipelineName(pipelineName); err != nil {
		return "", nil, err
	}
	filename := pipeline.PipelineFilename(pipelineName)

	// Check working directory.
	if _, statErr := os.Stat(filename); statErr == nil {
		return filename, nil, nil
	}

	// Check user config directory.
	configDir, _ := os.UserConfigDir()
	if configDir != "" {
		userPath := filepath.Join(configDir, "soda", filename)
		if _, statErr := os.Stat(userPath); statErr == nil {
			return userPath, nil, nil
		}
	}

	// Check known embedded pipelines.
	if pipelineName != "" && pipelineName != "default" {
		embeddedPath, ok := knownEmbeddedPipelines[pipelineName]
		if !ok {
			return "", nil, fmt.Errorf("pipeline %q not found (looked for %s in . and %s)",
				pipelineName, filename, filepath.Join(configDir, "soda"))
		}
		data, readErr := fs.ReadFile(embeddedPipelines, embeddedPath)
		if readErr != nil {
			return "", nil, fmt.Errorf("read embedded pipeline %q: %w", pipelineName, readErr)
		}
		tmpFile, tmpErr := os.CreateTemp("", "soda-phases-*.yaml")
		if tmpErr != nil {
			return "", nil, fmt.Errorf("create temp phases file: %w", tmpErr)
		}
		if _, writeErr := tmpFile.Write(data); writeErr != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return "", nil, fmt.Errorf("write embedded phases: %w", writeErr)
		}
		tmpFile.Close()
		return tmpFile.Name(), func() { os.Remove(tmpFile.Name()) }, nil
	}

	// Fall back to embedded default.
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
