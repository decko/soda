package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// defaultConfigTemplate is the starter soda.config.yaml written by `soda init`.
// It mirrors config.example.yaml with placeholder values that users fill in.
const defaultConfigTemplate = `# soda.config.yaml — project-level SODA configuration
# See config.example.yaml for all available options.

# Ticket source: "github" or "jira"
ticket_source: github
github:
  owner: OWNER
  repo: REPO
  # fetch_comments: true
  # spec:
  #   start_marker: "<!-- spec:start -->"
  #   end_marker: "<!-- spec:end -->"
  # plan:
  #   start_marker: "<!-- plan:start -->"
  #   end_marker: "<!-- plan:end -->"

# Execution mode: "autonomous" or "checkpoint"
mode: autonomous

# Model (used for all phases)
model: claude-opus-4-6

# Sandbox (via agentic-orchestrator)
sandbox:
  enabled: true
  limits:
    memory_mb: 2048
    cpu_percent: 200
    max_pids: 256

# Budget
limits:
  max_cost_per_ticket: 15.00
  max_cost_per_phase: 8.00

# Worktrees
worktree_dir: .worktrees

# State directory
state_dir: .soda

# Project context — injected into every phase system prompt
context:
  - AGENTS.md

# Repos
repos:
  - name: REPO
    forge: github
    push_to: OWNER/REPO
    target: OWNER/REPO
    description: ""
    formatter: ""
    test_command: ""
    labels:
      - ai-assisted
    trailers:
      - "Assisted-by: SODA <noreply@soda.dev>"
`

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a soda.config.yaml in the current directory",
		Long: `Create a starter soda.config.yaml in the current directory.

The generated file contains sensible defaults and placeholder values
(OWNER, REPO) that you should replace with your project's details.

If soda.config.yaml already exists, the command exits with an error
unless --force is passed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			dir, _ := cmd.Flags().GetString("dir")
			return runInit(dir, force, cmd)
		},
	}

	cmd.Flags().Bool("force", false, "overwrite existing soda.config.yaml")
	cmd.Flags().String("dir", ".", "directory to create the config file in")

	return cmd
}

func runInit(dir string, force bool, cmd *cobra.Command) error {
	dest := filepath.Join(dir, "soda.config.yaml")

	if !force {
		if _, err := os.Stat(dest); err == nil {
			return fmt.Errorf("init: %s already exists (use --force to overwrite)", dest)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("init: check %s: %w", dest, err)
		}
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("init: create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(dest, []byte(defaultConfigTemplate), 0644); err != nil {
		return fmt.Errorf("init: write %s: %w", dest, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n", dest)
	fmt.Fprintf(cmd.OutOrStdout(), "Edit the file to set your project's owner, repo, and other settings.\n")
	return nil
}
