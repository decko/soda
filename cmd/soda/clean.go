package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	sodagit "github.com/decko/soda/internal/git"
	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

var errSkipped = errors.New("skipped")

func newCleanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clean [ticket]",
		Short: "Remove completed/failed pipeline state and worktrees",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			all, _ := cmd.Flags().GetBool("all")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			force, _ := cmd.Flags().GetBool("force")

			if len(args) == 1 {
				return cleanTicket(cfg.StateDir, args[0], dryRun, force)
			}
			if all {
				return cleanAll(cfg.StateDir, dryRun, force)
			}
			return fmt.Errorf("specify a ticket key or use --all")
		},
	}

	cmd.Flags().Bool("all", false, "clean all tickets in terminal state")
	cmd.Flags().Bool("dry-run", false, "show what would be cleaned without doing it")
	cmd.Flags().Bool("force", false, "clean even if not in terminal state (does not override running pipeline lock)")

	return cmd
}

func cleanAll(stateDir string, dryRun, force bool) error {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("No pipelines found.")
			return nil
		}
		return fmt.Errorf("clean: read state dir: %w", err)
	}

	cleaned := 0
	for _, entry := range entries {
		// Skip non-directories. This preserves stateDir-level files such as
		// cost.json (the persistent cost ledger) which must survive clean runs.
		if !entry.IsDir() {
			continue
		}
		if err := cleanTicket(stateDir, entry.Name(), dryRun, force); err != nil {
			if !errors.Is(err, errSkipped) {
				fmt.Fprintf(os.Stderr, "Warning: %s: %v\n", entry.Name(), err)
			}
		} else {
			cleaned++
		}
	}

	if cleaned == 0 {
		fmt.Println("Nothing to clean.")
	}
	return nil
}

func cleanTicket(stateDir, ticketKey string, dryRun, force bool) error {
	ticketDir := filepath.Join(stateDir, ticketKey)
	metaPath := filepath.Join(ticketDir, "meta.json")

	meta, err := pipeline.ReadMeta(metaPath)
	if err != nil {
		return fmt.Errorf("read meta: %w", err)
	}

	// Try to acquire flock to ensure pipeline is not running.
	// Always checked, even with --force, to prevent cleaning running pipelines.
	lockPath := filepath.Join(ticketDir, "lock")
	if !tryLock(lockPath) {
		fmt.Fprintf(os.Stderr, "Skipping %s: pipeline is running\n", ticketKey)
		return errSkipped
	}

	// Check terminal state
	if !force && !isTerminal(meta) {
		fmt.Fprintf(os.Stderr, "Skipping %s: not in terminal state (use --force to override)\n", ticketKey)
		return errSkipped
	}

	// Remove worktree
	if meta.Worktree != "" {
		if dryRun {
			fmt.Printf("Would remove worktree: %s\n", meta.Worktree)
		} else {
			wtArgs := []string{"worktree", "remove"}
			if force {
				wtArgs = append(wtArgs, "--force")
			}
			wtArgs = append(wtArgs, meta.Worktree)
			out, wtErr := exec.Command("git", wtArgs...).CombinedOutput()
			if wtErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: git worktree remove %s: %s\n", meta.Worktree, string(out))
			} else {
				fmt.Printf("Removed worktree: %s\n", meta.Worktree)
			}
		}
	}

	// Delete branch (local + remote)
	if meta.Branch != "" {
		if dryRun {
			fmt.Printf("Would delete branch: %s\n", meta.Branch)
			fmt.Printf("Would delete remote branch: origin/%s\n", meta.Branch)
		} else {
			repoDir, err := resolveRepoDir()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: cannot resolve repo dir to delete branch %s: %v\n", meta.Branch, err)
			} else {
				if brErr := sodagit.DeleteBranch(repoDir, meta.Branch); brErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: git branch delete %s: %v\n", meta.Branch, brErr)
				} else {
					fmt.Printf("Deleted branch: %s\n", meta.Branch)
				}
				if rmErr := sodagit.DeleteRemoteBranch(repoDir, "origin", meta.Branch); rmErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: git remote branch delete origin/%s: %v\n", meta.Branch, rmErr)
				} else {
					fmt.Printf("Deleted remote branch: origin/%s\n", meta.Branch)
				}
			}
		}
	}

	// Remove state directory
	if dryRun {
		fmt.Printf("Would remove state: %s\n", ticketDir)
	} else {
		if rmErr := os.RemoveAll(ticketDir); rmErr != nil {
			return fmt.Errorf("remove state dir: %w", rmErr)
		}
		fmt.Printf("Removed state: %s\n", ticketDir)
	}

	return nil
}

// tryLock attempts a non-blocking flock. Returns true if lock was acquired
// (meaning no other process holds it), and immediately releases it.
func tryLock(lockPath string) bool {
	fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return true // no lock file means nothing is running
	}
	defer fd.Close()

	err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		return false // lock is held
	}
	syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	return true
}

// resolveRepoDir returns the top-level directory of the current git repository.
func resolveRepoDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	dir := string(out)
	// Trim trailing newline
	if len(dir) > 0 && dir[len(dir)-1] == '\n' {
		dir = dir[:len(dir)-1]
	}
	return dir, nil
}

// isTerminal returns true if the pipeline is in a cleanable state.
func isTerminal(meta *pipeline.PipelineMeta) bool {
	if len(meta.Phases) == 0 {
		return true
	}
	for _, ps := range meta.Phases {
		if ps.Status == pipeline.PhaseRunning || ps.Status == pipeline.PhaseRetrying {
			return false
		}
	}
	// At least one phase must have completed or failed
	for _, ps := range meta.Phases {
		if ps.Status == pipeline.PhaseCompleted || ps.Status == pipeline.PhaseFailed {
			return true
		}
	}
	return false
}
