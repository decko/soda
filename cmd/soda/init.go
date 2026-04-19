package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/detect"
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
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			phases, _ := cmd.Flags().GetBool("phases")
			return runInit(cmd.OutOrStdout(), output, force, dryRun, phases)
		},
	}

	cmd.Flags().StringP("output", "o", "", "output path (default: soda.yaml)")
	cmd.Flags().Bool("force", false, "overwrite existing config file")
	cmd.Flags().Bool("dry-run", false, "print generated config to stdout without writing")
	cmd.Flags().Bool("phases", false, "also write phases.yaml alongside the config")

	return cmd
}

// runInit generates a config (optionally auto-detected) and writes it to disk.
// When dryRun is true the generated YAML is printed to w without writing files.
// When phases is true the embedded phases.yaml is written alongside the config.
// Extracted for testability — accepts an io.Writer for output messages.
func runInit(w io.Writer, output string, force bool, dryRun bool, phases bool) error {
	// Auto-detect project stack. Detection is best-effort: if it fails
	// we fall back to DefaultConfig with placeholder values.
	cfg := config.DefaultConfig()
	info, detectErr := detect.Detect(context.Background(), ".")
	if detectErr != nil {
		fmt.Fprintf(w, "Warning: project detection failed: %v\n", detectErr)
	}
	if info != nil {
		cfg = configFromDetected(info)
	}

	data, err := config.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}

	// Dry-run: print config to writer and return without writing files.
	if dryRun {
		_, writeErr := w.Write(data)
		return writeErr
	}

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

	// Write phases.yaml alongside the config when --phases is set.
	if phases {
		phasesPath := filepath.Join(filepath.Dir(destPath), "phases.yaml")
		if err := writePhases(w, phasesPath, force); err != nil {
			return err
		}
	}

	return nil
}

// writePhases writes the embedded phases.yaml to phasesPath.
// It refuses to overwrite an existing file unless force is true.
func writePhases(w io.Writer, phasesPath string, force bool) error {
	if !force {
		if _, err := os.Stat(phasesPath); err == nil {
			return fmt.Errorf("phases file already exists: %s (use --force to overwrite)", phasesPath)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("init: stat %s: %w", phasesPath, err)
		}
	}

	if err := os.WriteFile(phasesPath, embeddedPhases, 0644); err != nil {
		return fmt.Errorf("init: write phases.yaml: %w", err)
	}

	fmt.Fprintf(w, "Phases written to %s\n", phasesPath)
	return nil
}

// configFromDetected creates a Config populated with values from auto-detection.
// Detected forge, owner, and repo are used to fill in ticket source and repo
// config. When detection finds nothing useful, the result falls back to
// DefaultConfig placeholder values for that field.
func configFromDetected(info *detect.ProjectInfo) *config.Config {
	cfg := config.DefaultConfig()

	// Forge → ticket source
	switch info.Forge {
	case "github":
		cfg.TicketSource = "github"
		cfg.GitHub.Owner = info.Owner
		cfg.GitHub.Repo = info.Repo
	case "gitlab":
		cfg.TicketSource = "github" // keep github as default; gitlab ticket source not yet supported
	}

	// Context files
	if len(info.ContextFiles) > 0 {
		cfg.Context = info.ContextFiles
	}

	// Repos
	repoName := info.Repo
	if repoName == "" {
		repoName = "your-repo"
	}
	ownerRepo := info.Owner + "/" + info.Repo
	if info.Owner == "" || info.Repo == "" {
		ownerRepo = "your-user/your-repo"
	}
	targetRepo := ownerRepo
	forge := info.Forge
	if forge == "" {
		forge = "github"
	}

	cfg.Repos = []config.RepoConfig{
		{
			Name:        repoName,
			Forge:       forge,
			PushTo:      ownerRepo,
			Target:      targetRepo,
			Description: "Main repository",
			Formatter:   info.Formatter,
			TestCommand: info.TestCommand,
			Labels:      []string{"ai-assisted"},
		},
	}

	return cfg
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
