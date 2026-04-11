package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepo initializes a bare-minimum git repo with one commit.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	commands := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %s: %v", args, out, err)
		}
	}
	return dir
}

func TestCreateWorktree(t *testing.T) {
	t.Run("creates_worktree_and_branch", func(t *testing.T) {
		repoDir := initGitRepo(t)
		worktreeBase := filepath.Join(repoDir, ".worktrees")

		path, err := CreateWorktree(context.Background(), repoDir, worktreeBase, "feat/test-123", "main")
		if err != nil {
			t.Fatalf("CreateWorktree: %v", err)
		}

		// Verify the worktree directory exists
		if _, err := os.Stat(path); err != nil {
			t.Errorf("worktree dir should exist: %v", err)
		}

		// Verify it's a valid git worktree (has .git file, not directory)
		gitFile := filepath.Join(path, ".git")
		info, err := os.Stat(gitFile)
		if err != nil {
			t.Fatalf(".git should exist in worktree: %v", err)
		}
		if info.IsDir() {
			t.Error(".git should be a file in worktree, not a directory")
		}

		// Verify the branch was created
		cmd := exec.Command("git", "branch", "--list", "feat/test-123")
		cmd.Dir = repoDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git branch: %v", err)
		}
		if len(out) == 0 {
			t.Error("branch feat/test-123 should exist")
		}
	})

	t.Run("returns_existing_worktree_path", func(t *testing.T) {
		repoDir := initGitRepo(t)
		worktreeBase := filepath.Join(repoDir, ".worktrees")

		path1, err := CreateWorktree(context.Background(), repoDir, worktreeBase, "feat/dup", "main")
		if err != nil {
			t.Fatalf("first CreateWorktree: %v", err)
		}

		path2, err := CreateWorktree(context.Background(), repoDir, worktreeBase, "feat/dup", "main")
		if err != nil {
			t.Fatalf("second CreateWorktree: %v", err)
		}

		if path1 != path2 {
			t.Errorf("paths differ: %q vs %q", path1, path2)
		}
	})

	t.Run("respects_context_cancellation", func(t *testing.T) {
		repoDir := initGitRepo(t)
		worktreeBase := filepath.Join(repoDir, ".worktrees")

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := CreateWorktree(ctx, repoDir, worktreeBase, "feat/cancel", "main")
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
	})
}
