package pipeline

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestParsePRRef(t *testing.T) {
	tests := []struct {
		name       string
		prURL      string
		wantOwner  string
		wantRepo   string
		wantNumber string
		wantErr    bool
	}{
		{
			name:       "standard_github_url",
			prURL:      "https://github.com/decko/soda/pull/49",
			wantOwner:  "decko",
			wantRepo:   "soda",
			wantNumber: "49",
		},
		{
			name:       "trailing_slash",
			prURL:      "https://github.com/decko/soda/pull/49/",
			wantOwner:  "decko",
			wantRepo:   "soda",
			wantNumber: "49",
		},
		{
			name:       "different_owner_repo",
			prURL:      "https://github.com/facebook/react/pull/1234",
			wantOwner:  "facebook",
			wantRepo:   "react",
			wantNumber: "1234",
		},
		{
			name:    "invalid_url_no_pull",
			prURL:   "https://github.com/decko/soda/issues/49",
			wantErr: true,
		},
		{
			name:    "too_short",
			prURL:   "https://github.com",
			wantErr: true,
		},
		{
			name:    "empty_url",
			prURL:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, number, err := parsePRRef(tt.prURL)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
			if number != tt.wantNumber {
				t.Errorf("number = %q, want %q", number, tt.wantNumber)
			}
		})
	}
}

// TestIntegration_GetNewComments is an optional integration test that exercises
// GitHubPRPoller.GetNewComments against the real GitHub API via the gh CLI.
// Skipped unless SODA_API_TEST=1 is set. Requires the 'gh' binary to be
// authenticated and a SODA_TEST_PR_URL pointing to a PR with at least one comment.
func TestIntegration_GetNewComments(t *testing.T) {
	if os.Getenv("SODA_API_TEST") == "" {
		t.Skip("skipping real API test: set SODA_API_TEST=1 to enable")
	}

	prURL := os.Getenv("SODA_TEST_PR_URL")
	if prURL == "" {
		prURL = "https://github.com/decko/soda/pull/49"
	}

	ghBin := os.Getenv("GH_BIN")
	if ghBin == "" {
		ghBin = "gh"
	}

	poller := NewGitHubPRPoller(ghBin)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Fetch all comments (afterID = "").
	comments, err := poller.GetNewComments(ctx, prURL, "")
	if err != nil {
		t.Fatalf("GetNewComments (all): %v", err)
	}

	t.Logf("total comments on %s: %d", prURL, len(comments))

	// Validate comment fields.
	for i, c := range comments {
		if c.ID == "" {
			t.Errorf("comment[%d]: ID should not be empty", i)
		}
		if c.Author == "" {
			t.Errorf("comment[%d] (%s): Author should not be empty", i, c.ID)
		}
		if c.Body == "" {
			t.Errorf("comment[%d] (%s): Body should not be empty", i, c.ID)
		}
		t.Logf("  comment[%d]: id=%s author=%s body_len=%d path=%q", i, c.ID, c.Author, len(c.Body), c.Path)
	}

	// If there is at least one comment, test afterID filtering.
	if len(comments) == 0 {
		t.Log("no comments found; skipping afterID filter test")
		return
	}

	firstID := comments[0].ID
	filtered, err := poller.GetNewComments(ctx, prURL, firstID)
	if err != nil {
		t.Fatalf("GetNewComments (afterID=%s): %v", firstID, err)
	}

	// GetNewComments always returns all RC_ (review) comments before IC_ (issue)
	// comments because they come from two separate API calls. The afterID filter
	// operates on this concatenated ordering, so results are stable as long as no
	// new comments are posted between the two fetches above. We use a relaxed
	// assertion (<=) to tolerate any race with external comment creation.
	expectedCount := len(comments) - 1
	if len(filtered) > expectedCount {
		t.Errorf("after filtering by %s: got %d comments, want at most %d", firstID, len(filtered), expectedCount)
	}

	// Verify the filtered set does not contain the afterID itself.
	for _, c := range filtered {
		if c.ID == firstID {
			t.Errorf("filtered comments should not contain afterID %s", firstID)
		}
	}
}
