package main

import (
	"context"
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
		Short: "Remove worktrees and branches, preserving session data (use --purge for full wipe)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			all, _ := cmd.Flags().GetBool("all")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			force, _ := cmd.Flags().GetBool("force")
			purge, _ := cmd.Flags().GetBool("purge")

			ctx := cmd.Context()
			if len(args) == 1 {
				return cleanTicket(ctx, cfg.StateDir, args[0], dryRun, force, purge)
			}
			if all {
				return cleanAll(ctx, cfg.StateDir, dryRun, force, purge)
			}
			return fmt.Errorf("specify a ticket key or use --all")
		},
	}

	cmd.Flags().Bool("all", false, "clean all tickets in terminal state")
	cmd.Flags().Bool("dry-run", false, "show what would be cleaned without doing it")
	cmd.Flags().Bool("force", false, "clean even if not in terminal state (does not override running pipeline lock)")
	cmd.Flags().Bool("purge", false, "remove all session data including state directory (default preserves meta, events, and artifacts)")

	return cmd
}

func cleanAll(ctx context.Context, stateDir string, dryRun, force, purge bool) error {
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
		if err := cleanTicket(ctx, stateDir, entry.Name(), dryRun, force, purge); err != nil {
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

func cleanTicket(ctx context.Context, stateDir, ticketKey string, dryRun, force, purge bool) error {
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

	// Track whether git resource cleanup succeeded so we only clear
	// references in meta.json for resources that were actually removed.
	var worktreeCleared, branchCleared bool

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
			out, wtErr := exec.CommandContext(ctx, "git", wtArgs...).CombinedOutput()
			if wtErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: git worktree remove %s: %s\n", meta.Worktree, string(out))
			} else {
				worktreeCleared = true
				fmt.Printf("Removed worktree: %s\n", meta.Worktree)
			}
		}
	} else {
		worktreeCleared = true // nothing to clean
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
					branchCleared = true
					fmt.Printf("Deleted branch: %s\n", meta.Branch)
				}
				if rmErr := sodagit.DeleteRemoteBranch(ctx, repoDir, "origin", meta.Branch); rmErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: git remote branch delete origin/%s: %v\n", meta.Branch, rmErr)
				} else {
					fmt.Printf("Deleted remote branch: origin/%s\n", meta.Branch)
				}
			}
		}
	} else {
		branchCleared = true // nothing to clean
	}

	if purge {
		// --purge: remove the entire state directory (full wipe).
		if dryRun {
			fmt.Printf("Would purge state: %s\n", ticketDir)
		} else {
			if rmErr := os.RemoveAll(ticketDir); rmErr != nil {
				return fmt.Errorf("remove state dir: %w", rmErr)
			}
			fmt.Printf("Purged state: %s\n", ticketDir)
		}
	} else {
		// Default: preserve session data (meta.json, events.jsonl, artifacts, logs)
		// but clear stale worktree/branch references and remove the lock file.
		if dryRun {
			fmt.Printf("Would preserve state: %s (clearing worktree/branch references)\n", ticketDir)
		} else {
			if err := clearCleanedRefs(metaPath, meta, worktreeCleared, branchCleared); err != nil {
				return fmt.Errorf("update meta after clean: %w", err)
			}
			// Remove the lock file since the pipeline is not running.
			os.Remove(lockPath)
			fmt.Printf("Cleaned %s (session data preserved)\n", ticketKey)
		}
	}

	return nil
}

// clearCleanedRefs updates meta.json to remove worktree and branch references
// that were successfully cleaned away. Only clears each reference if the
// corresponding cleanup operation succeeded, so that failed removals can be
// retried on a subsequent clean.
func clearCleanedRefs(metaPath string, meta *pipeline.PipelineMeta, worktreeCleared, branchCleared bool) error {
	if worktreeCleared {
		meta.Worktree = ""
	}
	if branchCleared {
		meta.Branch = ""
	}
	return pipeline.WriteMeta(metaPath, meta)
}

// tryLock attempts a non-blocking flock. Returns true if lock was acquired
// (meaning no other process holds it), and immediately releases it.
func tryLock(lockPath string) bool {
	fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true // no lock file means nothing is running
		}
		return false // fail closed on unexpected errors (e.g. permission denied)
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
