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

func TestRepoRoot(t *testing.T) {
	t.Run("returns_repo_root", func(t *testing.T) {
		repoDir := initGitRepo(t)

		got, err := RepoRoot(repoDir)
		if err != nil {
			t.Fatalf("RepoRoot: %v", err)
		}
		if got != repoDir {
			t.Errorf("RepoRoot = %q, want %q", got, repoDir)
		}
	})

	t.Run("resolves_from_subdirectory", func(t *testing.T) {
		repoDir := initGitRepo(t)

		subDir := filepath.Join(repoDir, "sub", "deep")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		got, err := RepoRoot(subDir)
		if err != nil {
			t.Fatalf("RepoRoot: %v", err)
		}
		if got != repoDir {
			t.Errorf("RepoRoot = %q, want %q", got, repoDir)
		}
	})

	t.Run("returns_main_repo_from_worktree", func(t *testing.T) {
		repoDir := initGitRepo(t)
		worktreeBase := filepath.Join(repoDir, ".worktrees")

		wtPath, err := CreateWorktree(context.Background(), repoDir, worktreeBase, "feat/reporoot-test", "main")
		if err != nil {
			t.Fatalf("CreateWorktree: %v", err)
		}

		got, err := RepoRoot(wtPath)
		if err != nil {
			t.Fatalf("RepoRoot: %v", err)
		}
		// RepoRoot must return the MAIN repo root, not the worktree.
		if got != repoDir {
			t.Errorf("RepoRoot from worktree = %q, want %q (main repo)", got, repoDir)
		}
	})

	t.Run("errors_outside_git_repo", func(t *testing.T) {
		dir := t.TempDir()
		_, err := RepoRoot(dir)
		if err == nil {
			t.Fatal("expected error outside git repo")
		}
	})
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

func TestDeleteBranch(t *testing.T) {
	t.Run("deletes_existing_branch", func(t *testing.T) {
		repoDir := initGitRepo(t)

		// Create a branch
		cmd := exec.Command("git", "branch", "feat/to-delete")
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git branch: %s: %v", out, err)
		}

		// Verify it exists
		cmd = exec.Command("git", "branch", "--list", "feat/to-delete")
		cmd.Dir = repoDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git branch --list: %v", err)
		}
		if len(out) == 0 {
			t.Fatal("branch feat/to-delete should exist before deletion")
		}

		// Delete it
		if err := DeleteBranch(repoDir, "feat/to-delete"); err != nil {
			t.Fatalf("DeleteBranch: %v", err)
		}

		// Verify it no longer exists
		cmd = exec.Command("git", "branch", "--list", "feat/to-delete")
		cmd.Dir = repoDir
		out, err = cmd.Output()
		if err != nil {
			t.Fatalf("git branch --list after delete: %v", err)
		}
		if len(out) != 0 {
			t.Error("branch feat/to-delete should not exist after deletion")
		}
	})

	t.Run("no_error_for_nonexistent_branch", func(t *testing.T) {
		repoDir := initGitRepo(t)

		err := DeleteBranch(repoDir, "feat/does-not-exist")
		if err != nil {
			t.Fatalf("DeleteBranch should not error for nonexistent branch: %v", err)
		}
	})
}

func TestNeedsMergeWithBase(t *testing.T) {
	t.Run("no_conflicts_when_base_unchanged", func(t *testing.T) {
		repoDir := initGitRepo(t)

		// Create a feature branch and make a commit on it.
		run(t, repoDir, "git", "checkout", "-b", "feat/test")
		writeFile(t, repoDir, "feature.txt", "feature content")
		run(t, repoDir, "git", "add", "feature.txt")
		run(t, repoDir, "git", "commit", "-m", "add feature")

		hasConflicts, err := NeedsMergeWithBase(context.Background(), repoDir, "main")
		if err != nil {
			t.Fatalf("NeedsMergeWithBase: %v", err)
		}
		if hasConflicts {
			t.Error("expected no conflicts when base is unchanged")
		}
	})

	t.Run("no_conflicts_when_base_has_non_overlapping_changes", func(t *testing.T) {
		repoDir := initGitRepo(t)

		// Create a file on main.
		writeFile(t, repoDir, "base.txt", "original content")
		run(t, repoDir, "git", "add", "base.txt")
		run(t, repoDir, "git", "commit", "-m", "add base file")

		// Create feature branch.
		run(t, repoDir, "git", "checkout", "-b", "feat/no-conflict")
		writeFile(t, repoDir, "feature.txt", "feature content")
		run(t, repoDir, "git", "add", "feature.txt")
		run(t, repoDir, "git", "commit", "-m", "add feature")

		// Go back to main and make a non-overlapping change.
		run(t, repoDir, "git", "checkout", "main")
		writeFile(t, repoDir, "other.txt", "other content")
		run(t, repoDir, "git", "add", "other.txt")
		run(t, repoDir, "git", "commit", "-m", "add other file")

		// Switch back to feature branch.
		run(t, repoDir, "git", "checkout", "feat/no-conflict")

		hasConflicts, err := NeedsMergeWithBase(context.Background(), repoDir, "main")
		if err != nil {
			t.Fatalf("NeedsMergeWithBase: %v", err)
		}
		if hasConflicts {
			t.Error("expected no conflicts with non-overlapping changes")
		}
	})

	t.Run("detects_conflicts", func(t *testing.T) {
		repoDir := initGitRepo(t)

		// Create a file on main.
		writeFile(t, repoDir, "shared.txt", "original content")
		run(t, repoDir, "git", "add", "shared.txt")
		run(t, repoDir, "git", "commit", "-m", "add shared file")

		// Create feature branch with conflicting change.
		run(t, repoDir, "git", "checkout", "-b", "feat/conflict")
		writeFile(t, repoDir, "shared.txt", "feature version")
		run(t, repoDir, "git", "add", "shared.txt")
		run(t, repoDir, "git", "commit", "-m", "modify on feature")

		// Go back to main and make a conflicting change.
		run(t, repoDir, "git", "checkout", "main")
		writeFile(t, repoDir, "shared.txt", "main version")
		run(t, repoDir, "git", "add", "shared.txt")
		run(t, repoDir, "git", "commit", "-m", "modify on main")

		// Switch back to feature.
		run(t, repoDir, "git", "checkout", "feat/conflict")

		hasConflicts, err := NeedsMergeWithBase(context.Background(), repoDir, "main")
		if err != nil {
			t.Fatalf("NeedsMergeWithBase: %v", err)
		}
		if !hasConflicts {
			t.Error("expected conflicts when both branches modify the same file")
		}
	})
}

func TestRebase(t *testing.T) {
	t.Run("clean_rebase", func(t *testing.T) {
		repoDir := initGitRepo(t)

		// Create a file on main.
		writeFile(t, repoDir, "base.txt", "base content")
		run(t, repoDir, "git", "add", "base.txt")
		run(t, repoDir, "git", "commit", "-m", "add base")

		// Create feature branch.
		run(t, repoDir, "git", "checkout", "-b", "feat/rebase-clean")
		writeFile(t, repoDir, "feature.txt", "feature content")
		run(t, repoDir, "git", "add", "feature.txt")
		run(t, repoDir, "git", "commit", "-m", "add feature")

		// Add another commit to main.
		run(t, repoDir, "git", "checkout", "main")
		writeFile(t, repoDir, "other.txt", "other content")
		run(t, repoDir, "git", "add", "other.txt")
		run(t, repoDir, "git", "commit", "-m", "add other")

		// Rebase feature onto main.
		run(t, repoDir, "git", "checkout", "feat/rebase-clean")

		err := Rebase(context.Background(), repoDir, "main")
		if err != nil {
			t.Fatalf("Rebase: %v", err)
		}

		// Verify both files exist after rebase.
		if _, err := os.Stat(filepath.Join(repoDir, "feature.txt")); err != nil {
			t.Error("feature.txt should exist after clean rebase")
		}
		if _, err := os.Stat(filepath.Join(repoDir, "other.txt")); err != nil {
			t.Error("other.txt should exist after clean rebase")
		}
	})

	t.Run("conflicting_rebase_aborts", func(t *testing.T) {
		repoDir := initGitRepo(t)

		// Create a file on main.
		writeFile(t, repoDir, "shared.txt", "original")
		run(t, repoDir, "git", "add", "shared.txt")
		run(t, repoDir, "git", "commit", "-m", "add shared")

		// Create feature branch with conflicting change.
		run(t, repoDir, "git", "checkout", "-b", "feat/rebase-conflict")
		writeFile(t, repoDir, "shared.txt", "feature version")
		run(t, repoDir, "git", "add", "shared.txt")
		run(t, repoDir, "git", "commit", "-m", "modify on feature")

		// Add conflicting commit to main.
		run(t, repoDir, "git", "checkout", "main")
		writeFile(t, repoDir, "shared.txt", "main version")
		run(t, repoDir, "git", "add", "shared.txt")
		run(t, repoDir, "git", "commit", "-m", "modify on main")

		// Try to rebase.
		run(t, repoDir, "git", "checkout", "feat/rebase-conflict")

		err := Rebase(context.Background(), repoDir, "main")
		if err == nil {
			t.Fatal("expected error from conflicting rebase")
		}

		// Verify the rebase was aborted (no .git/rebase-merge dir).
		rebaseMergeDir := filepath.Join(repoDir, ".git", "rebase-merge")
		if _, err := os.Stat(rebaseMergeDir); err == nil {
			t.Error("rebase should have been aborted, but rebase-merge dir exists")
		}

		// Verify the working tree is clean (original feature content restored).
		content, err := os.ReadFile(filepath.Join(repoDir, "shared.txt"))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(content) != "feature version" {
			t.Errorf("shared.txt = %q, want %q (should be restored after abort)", string(content), "feature version")
		}
	})
}

// Test helpers

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v failed: %s: %v", args, out, err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}
