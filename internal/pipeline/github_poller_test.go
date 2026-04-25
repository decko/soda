package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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

func TestFilterCommentsAfterID(t *testing.T) {
	comments := []PRComment{
		{ID: "RC_1", Author: "alice"},
		{ID: "IC_2", Author: "bob"},
		{ID: "RC_3", Author: "charlie"},
	}

	tests := []struct {
		name    string
		afterID string
		wantIDs []string
	}{
		{
			name:    "empty_afterID_returns_all",
			afterID: "",
			wantIDs: []string{"RC_1", "IC_2", "RC_3"},
		},
		{
			name:    "after_first",
			afterID: "RC_1",
			wantIDs: []string{"IC_2", "RC_3"},
		},
		{
			name:    "after_middle",
			afterID: "IC_2",
			wantIDs: []string{"RC_3"},
		},
		{
			name:    "after_last_returns_empty",
			afterID: "RC_3",
			wantIDs: nil,
		},
		{
			name:    "not_found_returns_empty",
			afterID: "IC_999",
			wantIDs: nil,
		},
		{
			name:    "deleted_comment_returns_empty",
			afterID: "RC_deleted",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterCommentsAfterID(comments, tt.afterID)
			var gotIDs []string
			for _, c := range result {
				gotIDs = append(gotIDs, c.ID)
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Errorf("got[%d] = %q, want %q", i, gotIDs[i], tt.wantIDs[i])
				}
			}
		})
	}
}

func TestFilterCommentsAfterID_Empty(t *testing.T) {
	result := filterCommentsAfterID(nil, "IC_1")
	if result != nil {
		t.Errorf("nil comments should return nil, got %v", result)
	}
}

func TestDecodeGHComments(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int // expected number of decoded comments
		wantErr bool
	}{
		{
			name: "single_page",
			input: `{"id":1,"body":"first","user":{"login":"alice"}}
{"id":2,"body":"second","user":{"login":"bob"}}
{"id":3,"body":"third","user":{"login":"charlie"}}
`,
			want: 3,
		},
		{
			name: "multi_page",
			// Simulates gh --paginate --jq ".[]" output across two pages.
			// Each page's array is unwrapped into individual objects, then
			// concatenated. The old json.Unmarshal approach would fail here
			// because the result is not a single JSON array.
			input: `{"id":1,"body":"page1-a","user":{"login":"alice"}}
{"id":2,"body":"page1-b","user":{"login":"bob"}}
{"id":3,"body":"page2-a","user":{"login":"charlie"}}
{"id":4,"body":"page2-b","user":{"login":"dave"}}
`,
			want: 4,
		},
		{
			name:  "empty",
			input: "",
			want:  0,
		},
		{
			name:  "single_comment",
			input: `{"id":42,"body":"only one","user":{"login":"eve"}}`,
			want:  1,
		},
		{
			name:    "invalid_json",
			input:   `{"id":1,"body":"ok"}\n{not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeGHComments([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("got %d comments, want %d", len(got), tt.want)
			}
		})
	}
}

func TestDecodeGHComments_LargePageSimulation(t *testing.T) {
	// Simulate 3+ pages of 25 comments each (typical GitHub page size is 30).
	// This produces 75 newline-delimited JSON objects, which the old
	// json.Unmarshal approach could not decode.
	var buf strings.Builder
	const total = 75
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&buf, `{"id":%d,"body":"comment %d","user":{"login":"user%d"}}`, i, i, i%5)
		buf.WriteByte('\n')
	}

	got, err := decodeGHComments([]byte(buf.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != total {
		t.Fatalf("got %d comments, want %d", len(got), total)
	}

	// Spot-check first and last.
	if got[0].ID != 1 {
		t.Errorf("first comment ID = %d, want 1", got[0].ID)
	}
	if got[total-1].ID != total {
		t.Errorf("last comment ID = %d, want %d", got[total-1].ID, total)
	}
	if got[0].Body != "comment 1" {
		t.Errorf("first comment Body = %q, want %q", got[0].Body, "comment 1")
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

func TestMergePR_Success(t *testing.T) {
	// Build a fake gh binary script that succeeds.
	binPath := writeFakeGH(t, "", "", 0)

	poller := NewGitHubPRPoller(binPath)
	ctx := context.Background()

	err := poller.MergePR(ctx, "https://github.com/owner/repo/pull/1", "squash")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestMergePR_ConflictError(t *testing.T) {
	binPath := writeFakeGH(t, "", "Pull request merge conflict", 1)

	poller := NewGitHubPRPoller(binPath)
	ctx := context.Background()

	err := poller.MergePR(ctx, "https://github.com/owner/repo/pull/1", "merge")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrMergeConflict) {
		t.Errorf("expected ErrMergeConflict, got: %v", err)
	}
}

func TestMergePR_AlreadyMergedError(t *testing.T) {
	binPath := writeFakeGH(t, "", "Pull request was already merged", 1)

	poller := NewGitHubPRPoller(binPath)
	ctx := context.Background()

	err := poller.MergePR(ctx, "https://github.com/owner/repo/pull/1", "squash")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrPRAlreadyMerged) {
		t.Errorf("expected ErrPRAlreadyMerged, got: %v", err)
	}
}

func TestMergePR_ClosedError(t *testing.T) {
	binPath := writeFakeGH(t, "", "Pull request is closed", 1)

	poller := NewGitHubPRPoller(binPath)
	ctx := context.Background()

	err := poller.MergePR(ctx, "https://github.com/owner/repo/pull/1", "squash")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrPRClosed) {
		t.Errorf("expected ErrPRClosed, got: %v", err)
	}
}

func TestValidateMergePrerequisites_NoProtection(t *testing.T) {
	// First call: gh pr view → returns baseRefName.
	// Second call: gh api → 404 (no protection).
	callNum := 0
	binPath := writeFakeGHMulti(t, func(args []string) (string, string, int) {
		callNum++
		if callNum == 1 {
			// gh pr view ... --json baseRefName
			return `{"baseRefName":"main"}`, "", 0
		}
		// gh api ... → 404
		return "", "HTTP 404: Not Found", 1
	})

	poller := NewGitHubPRPoller(binPath)
	ctx := context.Background()

	err := poller.ValidateMergePrerequisites(ctx, "https://github.com/owner/repo/pull/1")
	if err != nil {
		t.Fatalf("expected no error for unprotected branch, got: %v", err)
	}
}

func TestValidateMergePrerequisites_NoDismissStale(t *testing.T) {
	callNum := 0
	binPath := writeFakeGHMulti(t, func(args []string) (string, string, int) {
		callNum++
		if callNum == 1 {
			return `{"baseRefName":"main"}`, "", 0
		}
		// Protection exists but dismiss_stale_reviews is false.
		return `{"required_pull_request_reviews":{"dismiss_stale_reviews":false}}`, "", 0
	})

	poller := NewGitHubPRPoller(binPath)
	ctx := context.Background()

	err := poller.ValidateMergePrerequisites(ctx, "https://github.com/owner/repo/pull/1")
	if err != nil {
		t.Fatalf("expected no error when dismiss_stale_reviews is false, got: %v", err)
	}
}

func TestValidateMergePrerequisites_DismissStaleReviews(t *testing.T) {
	callNum := 0
	binPath := writeFakeGHMulti(t, func(args []string) (string, string, int) {
		callNum++
		if callNum == 1 {
			return `{"baseRefName":"main"}`, "", 0
		}
		return `{"required_pull_request_reviews":{"dismiss_stale_reviews":true}}`, "", 0
	})

	poller := NewGitHubPRPoller(binPath)
	ctx := context.Background()

	err := poller.ValidateMergePrerequisites(ctx, "https://github.com/owner/repo/pull/1")
	if err == nil {
		t.Fatal("expected error when dismiss_stale_reviews is true")
	}
	if !strings.Contains(err.Error(), "dismiss_stale_reviews") {
		t.Errorf("expected error mentioning dismiss_stale_reviews, got: %v", err)
	}
}

func TestValidateMergePrerequisites_BaseRefError(t *testing.T) {
	binPath := writeFakeGH(t, "", "not found", 1)

	poller := NewGitHubPRPoller(binPath)
	ctx := context.Background()

	err := poller.ValidateMergePrerequisites(ctx, "https://github.com/owner/repo/pull/1")
	if err == nil {
		t.Fatal("expected error when base branch fetch fails")
	}
}

// writeFakeGH creates a shell script in a temp dir that outputs the given
// stdout/stderr and exits with the given code. Returns the script path.
func writeFakeGH(t *testing.T, stdout, stderr string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/gh"
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q >&1\nprintf '%%s' %q >&2\nexit %d\n",
		stdout, stderr, exitCode)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return path
}

// writeFakeGHMulti creates a shell script that calls back into a Go test
// binary to handle multiple invocations with different responses.
// The handler function receives the command-line arguments and returns
// (stdout, stderr, exitCode). Uses a counter file to track call number.
func writeFakeGHMulti(t *testing.T, handler func(args []string) (stdout, stderr string, exitCode int)) string {
	t.Helper()
	dir := t.TempDir()

	// Pre-compute responses by calling handler sequentially.
	// We support up to 10 calls.
	type resp struct {
		stdout   string
		stderr   string
		exitCode int
	}
	var responses []resp
	for i := 0; i < 10; i++ {
		s, e, c := handler(nil)
		responses = append(responses, resp{s, e, c})
	}

	// Create a counter file and individual response scripts.
	counterPath := dir + "/counter"
	if err := os.WriteFile(counterPath, []byte("0"), 0644); err != nil {
		t.Fatalf("write counter: %v", err)
	}

	// Write response files.
	for i, r := range responses {
		stdoutFile := fmt.Sprintf("%s/stdout_%d", dir, i)
		stderrFile := fmt.Sprintf("%s/stderr_%d", dir, i)
		exitFile := fmt.Sprintf("%s/exit_%d", dir, i)
		os.WriteFile(stdoutFile, []byte(r.stdout), 0644)
		os.WriteFile(stderrFile, []byte(r.stderr), 0644)
		os.WriteFile(exitFile, []byte(fmt.Sprintf("%d", r.exitCode)), 0644)
	}

	// Build a shell script that reads the counter, outputs the right response,
	// then increments the counter.
	path := dir + "/gh"
	script := fmt.Sprintf(`#!/bin/sh
COUNTER=$(cat %q)
STDOUT=$(cat "%s/stdout_${COUNTER}")
STDERR=$(cat "%s/stderr_${COUNTER}")
EXIT=$(cat "%s/exit_${COUNTER}")
NEXT=$((COUNTER + 1))
echo "$NEXT" > %q
printf '%%s' "$STDOUT" >&1
printf '%%s' "$STDERR" >&2
exit $EXIT
`, counterPath, dir, dir, dir, counterPath)

	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return path
}
