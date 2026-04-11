package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CreateWorktree creates a git worktree for a new branch based on baseBranch.
// Returns the absolute path to the worktree directory.
// If the worktree already exists at the expected path, returns that path.
func CreateWorktree(ctx context.Context, repoDir, worktreeBase, branch, baseBranch string) (string, error) {
	worktreePath := filepath.Join(worktreeBase, branch)

	// If worktree already exists, return its path
	if _, err := os.Stat(filepath.Join(worktreePath, ".git")); err == nil {
		absPath, err := filepath.Abs(worktreePath)
		if err != nil {
			return "", fmt.Errorf("git: resolve worktree path: %w", err)
		}
		return absPath, nil
	}

	// Create parent directory
	if err := os.MkdirAll(worktreeBase, 0755); err != nil {
		return "", fmt.Errorf("git: create worktree base %s: %w", worktreeBase, err)
	}

	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branch, worktreePath, baseBranch)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git: worktree add: %s: %w", output, err)
	}

	absPath, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", fmt.Errorf("git: resolve worktree path: %w", err)
	}

	return absPath, nil
}
