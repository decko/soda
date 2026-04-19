package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/decko/soda/internal/config"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Auto-detect project stack and generate soda.yaml",
		Long: `Auto-detect the project stack and generate a soda config file.

By default the config is written to soda.yaml in the current
directory. Use --output to choose a different path. The command
refuses to overwrite an existing file unless --force is given.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			output, _ := cmd.Flags().GetString("output")
			force, _ := cmd.Flags().GetBool("force")
			return runInit(cmd.OutOrStdout(), output, force)
		},
	}

	cmd.Flags().StringP("output", "o", "", "output path (default: soda.yaml)")
	cmd.Flags().Bool("force", false, "overwrite existing config file")

	return cmd
}

// runInit generates a default config and writes it to disk.
// Extracted for testability — accepts an io.Writer for output messages.
func runInit(w io.Writer, output string, force bool) error {
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

	fmt.Fprintf(w, "Config written to %s\n", destPath)
	return nil
}

// resolveInitPath returns the destination path for the generated config.
// If output is empty, defaults to soda.yaml in the current directory.
func resolveInitPath(output string) (string, error) {
	if output != "" {
		abs, err := filepath.Abs(output)
		if err != nil {
			return "", fmt.Errorf("init: resolve path: %w", err)
		}
		return abs, nil
	}
	abs, err := filepath.Abs("soda.yaml")
	if err != nil {
		return "", fmt.Errorf("init: resolve path: %w", err)
	}
	return abs, nil
}
