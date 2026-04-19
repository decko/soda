package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed all:embeds/plugin
var embeddedPlugin embed.FS

// pluginSubdir is the directory name under .claude/plugins/ or ~/.claude/plugins/.
const pluginSubdir = "soda"

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage the SODA Claude Code plugin",
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
		Short: "Install the SODA plugin for Claude Code",
		Long: `Copies the embedded SODA plugin into the Claude Code plugin directory.

By default, installs to .claude/plugins/soda/ in the current directory (project-local).
Use --global to install to ~/.claude/plugins/soda/ instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			global, _ := cmd.Flags().GetBool("global")
			force, _ := cmd.Flags().GetBool("force")

			destDir, err := pluginDestDir(global)
			if err != nil {
				return err
			}

			return installPlugin(destDir, force)
		},
	}

	cmd.Flags().Bool("global", false, "install to ~/.claude/plugins/soda/ instead of .claude/plugins/soda/")
	cmd.Flags().Bool("force", false, "overwrite existing plugin installation")

	return cmd
}

func newPluginUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the SODA plugin from Claude Code",
		Long: `Removes the SODA plugin directory from the Claude Code plugin directory.

By default, removes .claude/plugins/soda/ in the current directory.
Use --global to remove ~/.claude/plugins/soda/ instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			global, _ := cmd.Flags().GetBool("global")

			destDir, err := pluginDestDir(global)
			if err != nil {
				return err
			}

			return uninstallPlugin(destDir)
		},
	}

	cmd.Flags().Bool("global", false, "remove from ~/.claude/plugins/soda/ instead of .claude/plugins/soda/")

	return cmd
}

// pluginDestDir returns the target directory for the plugin.
func pluginDestDir(global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("plugin: cannot determine home directory: %w", err)
		}
		return filepath.Join(home, ".claude", "plugins", pluginSubdir), nil
	}
	return filepath.Join(".claude", "plugins", pluginSubdir), nil
}

// installPlugin copies the embedded plugin files to destDir.
func installPlugin(destDir string, force bool) error {
	// Check if already installed
	if _, err := os.Stat(destDir); err == nil {
		if !force {
			return fmt.Errorf("plugin already installed at %s (use --force to overwrite)", destDir)
		}
		// Remove existing installation before overwriting
		if err := os.RemoveAll(destDir); err != nil {
			return fmt.Errorf("plugin: remove existing installation: %w", err)
		}
	}

	// Walk the embedded filesystem and copy files
	pluginFS, err := fs.Sub(embeddedPlugin, "embeds/plugin")
	if err != nil {
		return fmt.Errorf("plugin: embedded filesystem: %w", err)
	}

	err = fs.WalkDir(pluginFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		dest := filepath.Join(destDir, path)

		if entry.IsDir() {
			return os.MkdirAll(dest, 0755)
		}

		data, readErr := fs.ReadFile(pluginFS, path)
		if readErr != nil {
			return fmt.Errorf("read embedded %s: %w", path, readErr)
		}

		return os.WriteFile(dest, data, 0644)
	})
	if err != nil {
		return fmt.Errorf("plugin: install: %w", err)
	}

	fmt.Printf("Installed soda plugin to %s/\n", destDir)
	fmt.Println("  Skill:    soda-pipeline")
	fmt.Println("  Commands: /soda:run, /soda:status, /soda:sessions, /soda:init")
	fmt.Println("  Agent:    pipeline-architect")
	fmt.Println()
	fmt.Println("Enable in Claude Code: the plugin is auto-discovered from .claude/plugins/")

	return nil
}

// uninstallPlugin removes the plugin directory at destDir.
func uninstallPlugin(destDir string) error {
	info, err := os.Stat(destDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("plugin not installed at %s", destDir)
		}
		return fmt.Errorf("plugin: stat: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("plugin: %s is not a directory", destDir)
	}

	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("plugin: remove: %w", err)
	}

	fmt.Printf("Removed soda plugin from %s/\n", destDir)
	return nil
}
