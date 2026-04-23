package main

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed all:embeds/claude-code
var embeddedClaudeCode embed.FS

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage SODA skills, commands, and agents for Claude Code",
	}

	cmd.AddCommand(
		newPluginInstallCmd(),
		newPluginUninstallCmd(),
	)

	return cmd
}

func newPluginInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install SODA commands, skills, and agents for Claude Code",
		Long: `Copies SODA commands, skills, and agents into the Claude Code directory
for auto-discovery.

By default, installs to .claude/ in the current directory (project-local).
Use --global to install to ~/.claude/ instead.

Installed components:
  Commands: /project:soda-run, /project:soda-resume, /project:soda-status, etc.
  Skills:   soda-pipeline (architecture + operational runbook)
  Agents:   pipeline-architect (design-only pipeline advisor)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			global, _ := cmd.Flags().GetBool("global")
			force, _ := cmd.Flags().GetBool("force")

			destDir, err := pluginDestDir(global)
			if err != nil {
				return err
			}

			return installPlugin(cmd.OutOrStdout(), destDir, force)
		},
	}

	cmd.Flags().Bool("global", false, "install to ~/.claude/ instead of .claude/")
	cmd.Flags().Bool("force", false, "overwrite existing files")

	return cmd
}

func newPluginUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove SODA commands, skills, and agents from Claude Code",
		Long: `Removes SODA-installed files from the Claude Code directory.

By default, removes from .claude/ in the current directory.
Use --global to remove from ~/.claude/ instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			global, _ := cmd.Flags().GetBool("global")

			destDir, err := pluginDestDir(global)
			if err != nil {
				return err
			}

			return uninstallPlugin(cmd.OutOrStdout(), destDir)
		},
	}

	cmd.Flags().Bool("global", false, "remove from ~/.claude/ instead of .claude/")

	return cmd
}

// pluginDestDir returns the target .claude/ directory.
func pluginDestDir(global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("plugin: cannot determine home directory: %w", err)
		}
		return filepath.Join(home, ".claude"), nil
	}
	return ".claude", nil
}

// installPlugin copies the embedded claude-code files to destDir/.
func installPlugin(w io.Writer, destDir string, force bool) error {
	claudeCodeFS, err := fs.Sub(embeddedClaudeCode, "embeds/claude-code")
	if err != nil {
		return fmt.Errorf("plugin: embedded filesystem: %w", err)
	}

	// Check for existing files if not forcing.
	if !force {
		existing, checkErr := findExistingSodaFiles(destDir, claudeCodeFS)
		if checkErr != nil && !errors.Is(checkErr, os.ErrNotExist) {
			return fmt.Errorf("plugin: check existing: %w", checkErr)
		}
		if len(existing) > 0 {
			return fmt.Errorf("SODA files already installed at %s (found %s; use --force to overwrite)",
				destDir, strings.Join(existing, ", "))
		}
	}

	// Walk the embedded filesystem and copy files.
	var installed []string
	err = fs.WalkDir(claudeCodeFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		dest := filepath.Join(destDir, path)

		if entry.IsDir() {
			return os.MkdirAll(dest, 0755)
		}

		data, readErr := fs.ReadFile(claudeCodeFS, path)
		if readErr != nil {
			return fmt.Errorf("read embedded %s: %w", path, readErr)
		}

		if writeErr := os.WriteFile(dest, data, 0644); writeErr != nil {
			return fmt.Errorf("write %s: %w", dest, writeErr)
		}

		installed = append(installed, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("plugin: install: %w", err)
	}

	// Count by type.
	var commands, skills, agents int
	for _, path := range installed {
		switch {
		case strings.HasPrefix(path, "commands/"):
			commands++
		case strings.HasPrefix(path, "skills/"):
			skills++
		case strings.HasPrefix(path, "agents/"):
			agents++
		}
	}

	fmt.Fprintf(w, "Installed SODA for Claude Code in %s/\n", destDir)
	fmt.Fprintf(w, "  Commands: %d slash commands (soda-run, soda-resume, soda-status, ...)\n", commands)
	fmt.Fprintf(w, "  Skills:   %d skill(s) (soda-pipeline)\n", skills)
	fmt.Fprintf(w, "  Agents:   %d agent(s) (pipeline-architect)\n", agents)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Auto-discovered by Claude Code from .claude/ — no further setup needed.")

	return nil
}

// uninstallPlugin removes SODA-installed files from destDir.
func uninstallPlugin(w io.Writer, destDir string) error {
	claudeCodeFS, err := fs.Sub(embeddedClaudeCode, "embeds/claude-code")
	if err != nil {
		return fmt.Errorf("plugin: embedded filesystem: %w", err)
	}

	// Find what's installed.
	existing, err := findExistingSodaFiles(destDir, claudeCodeFS)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("SODA not installed at %s", destDir)
		}
		return fmt.Errorf("plugin: check existing: %w", err)
	}
	if len(existing) == 0 {
		return fmt.Errorf("SODA not installed at %s", destDir)
	}

	// Remove individual files.
	var removed int
	err = fs.WalkDir(claudeCodeFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}

		target := filepath.Join(destDir, path)
		if removeErr := os.Remove(target); removeErr != nil {
			if !errors.Is(removeErr, os.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", target, removeErr)
			}
		} else {
			removed++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("plugin: uninstall: %w", err)
	}

	// Clean up the soda-pipeline skill directory if empty.
	skillDir := filepath.Join(destDir, "skills", "soda-pipeline")
	_ = os.Remove(skillDir) // only succeeds if empty

	fmt.Fprintf(w, "Removed SODA from %s/ (%d files)\n", destDir, removed)
	return nil
}

// findExistingSodaFiles returns paths of SODA files that already exist in destDir.
func findExistingSodaFiles(destDir string, claudeCodeFS fs.FS) ([]string, error) {
	var found []string

	err := fs.WalkDir(claudeCodeFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}

		target := filepath.Join(destDir, path)
		if _, statErr := os.Stat(target); statErr == nil {
			found = append(found, path)
		}
		return nil
	})

	return found, err
}
