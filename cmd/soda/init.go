package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/decko/soda/internal/config"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a starter config file",
		Long: `Generate a starter soda config file with sensible defaults.

By default the config is written to the standard location
(~/.config/soda/config.yaml). Use --output to choose a
different path. The command refuses to overwrite an existing
file unless --force is given.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			output, _ := cmd.Flags().GetString("output")
			force, _ := cmd.Flags().GetBool("force")
			return runInit(output, force)
		},
	}

	cmd.Flags().StringP("output", "o", "", "output path (default: ~/.config/soda/config.yaml)")
	cmd.Flags().Bool("force", false, "overwrite existing config file")

	return cmd
}

// runInit generates a default config and writes it to disk.
// Extracted for testability.
func runInit(output string, force bool) error {
	// Resolve output path.
	destPath, err := resolveInitPath(output)
	if err != nil {
		return err
	}

	// Check for existing file unless --force.
	if !force {
		if _, err := os.Stat(destPath); err == nil {
			return fmt.Errorf("config file already exists: %s (use --force to overwrite)", destPath)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("init: stat %s: %w", destPath, err)
		}
	}

	// Generate default config.
	cfg := config.DefaultConfig()
	data, err := config.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("init: create directory %s: %w", dir, err)
	}

	// Write the file.
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return fmt.Errorf("init: write config: %w", err)
	}

	fmt.Printf("Config written to %s\n", destPath)
	return nil
}

// resolveInitPath returns the destination path for the generated config.
// If output is empty, the default config path is used.
func resolveInitPath(output string) (string, error) {
	if output != "" {
		abs, err := filepath.Abs(output)
		if err != nil {
			return "", fmt.Errorf("init: resolve path: %w", err)
		}
		return abs, nil
	}
	p, err := config.DefaultPath()
	if err != nil {
		return "", fmt.Errorf("init: %w", err)
	}
	return p, nil
}
