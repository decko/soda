package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoRoot returns the absolute path of the main repository root,
// even when called from inside a worktree. Uses --git-common-dir
// which always points to the shared .git directory.
func RepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git: rev-parse --git-common-dir in %s: %w", dir, err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	return filepath.Dir(gitDir), nil
}

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

// NeedsMergeWithBase returns true if the current branch in repoDir has
// merge conflicts with the given base branch. It uses "git merge-tree"
// (available in Git 2.38+) to perform a virtual merge without modifying
// the working tree. Returns false, nil if the merge would be clean.
func NeedsMergeWithBase(ctx context.Context, repoDir, baseBranch string) (bool, error) {
	// First check if there are any new commits on base to merge.
	mergeBaseCmd := exec.CommandContext(ctx, "git", "merge-base", "HEAD", baseBranch)
	mergeBaseCmd.Dir = repoDir
	mergeBaseOut, err := mergeBaseCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git: merge-base: %w", err)
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOut))

	// Get the current tip of the base branch.
	revParseCmd := exec.CommandContext(ctx, "git", "rev-parse", baseBranch)
	revParseCmd.Dir = repoDir
	baseTipOut, err := revParseCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git: rev-parse %s: %w", baseBranch, err)
	}
	baseTip := strings.TrimSpace(string(baseTipOut))

	// If merge-base equals base tip, base hasn't diverged — no conflicts possible.
	if mergeBase == baseTip {
		return false, nil
	}

	// Use merge-tree to simulate the merge.
	mergeTreeCmd := exec.CommandContext(ctx, "git", "merge-tree", "--write-tree", "--no-messages", "HEAD", baseBranch)
	mergeTreeCmd.Dir = repoDir
	_, err = mergeTreeCmd.Output()
	if err != nil {
		// Non-zero exit means conflicts exist.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return true, nil
		}
		return false, fmt.Errorf("git: merge-tree: %w", err)
	}

	return false, nil
}

// Rebase attempts to rebase the current branch onto baseBranch.
// Returns nil if the rebase succeeds cleanly.
// If the rebase has conflicts, it aborts the rebase and returns an error.
func Rebase(ctx context.Context, repoDir, baseBranch string) error {
	rebaseCmd := exec.CommandContext(ctx, "git", "rebase", baseBranch)
	rebaseCmd.Dir = repoDir
	output, err := rebaseCmd.CombinedOutput()
	if err != nil {
		// Abort the failed rebase to restore clean state.
		abortCmd := exec.CommandContext(ctx, "git", "rebase", "--abort")
		abortCmd.Dir = repoDir
		_ = abortCmd.Run()
		return fmt.Errorf("git: rebase onto %s: %s: %w", baseBranch, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// Push pushes the current branch to the given remote.
func Push(ctx context.Context, repoDir, remote string) error {
	cmd := exec.CommandContext(ctx, "git", "push", remote)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git: push to %s: %s: %w", remote, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// ForcePush pushes the current branch with --force-with-lease for safety
// after rebase. Uses --force-with-lease instead of --force to avoid
// overwriting work pushed from another source.
func ForcePush(ctx context.Context, repoDir, remote string) error {
	cmd := exec.CommandContext(ctx, "git", "push", "--force-with-lease", remote)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git: force push to %s: %s: %w", remote, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// FetchBranch fetches a specific branch from the remote.
func FetchBranch(ctx context.Context, repoDir, remote, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "fetch", remote, branch)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git: fetch %s %s: %s: %w", remote, branch, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// DeleteBranch deletes a local git branch. It runs "git branch -D <branch>"
// from the given repoDir. Returns nil if the branch was deleted or did not exist.
func DeleteBranch(repoDir, branch string) error {
	cmd := exec.Command("git", "branch", "-D", branch)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If the branch doesn't exist, treat as success.
		outStr := string(output)
		if strings.Contains(outStr, "not found") || strings.Contains(outStr, "not a valid branch") {
			return nil
		}
		return fmt.Errorf("git: branch delete %s: %s: %w", branch, outStr, err)
	}
	return nil
}

// Diff returns the output of "git diff <base>...HEAD" for the given
// repoDir. The three-dot syntax shows changes introduced on the current
// branch since it diverged from base. Returns an empty string and nil
// error when there are no differences. maxBytes limits the output size;
// use 0 for no limit.
func Diff(ctx context.Context, repoDir, base string, maxBytes int) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", base+"...HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git: diff %s...HEAD: %w", base, err)
	}
	s := string(out)
	if maxBytes > 0 && len(s) > maxBytes {
		s = s[:maxBytes] + "\n... (diff truncated)"
	}
	return s, nil
}
