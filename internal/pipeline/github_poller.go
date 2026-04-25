package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// GitHubPRPoller implements PRPoller using the gh CLI.
type GitHubPRPoller struct {
	command string // gh binary path; defaults to "gh"
}

// NewGitHubPRPoller creates a PRPoller that uses the gh CLI.
func NewGitHubPRPoller(ghCommand string) *GitHubPRPoller {
	if ghCommand == "" {
		ghCommand = "gh"
	}
	return &GitHubPRPoller{command: ghCommand}
}

// parsePRRef extracts owner, repo, and PR number from a PR URL.
// Expected format: https://github.com/<owner>/<repo>/pull/<number>
func parsePRRef(prURL string) (owner, repo, number string, err error) {
	// Remove trailing slash
	prURL = strings.TrimRight(prURL, "/")

	parts := strings.Split(prURL, "/")
	if len(parts) < 4 {
		return "", "", "", fmt.Errorf("monitor: invalid PR URL %q", prURL)
	}

	// Find "pull" in the URL parts
	for idx := range parts {
		if parts[idx] == "pull" && idx+1 < len(parts) && idx >= 2 {
			return parts[idx-2], parts[idx-1], parts[idx+1], nil
		}
	}

	return "", "", "", fmt.Errorf("monitor: cannot parse PR URL %q", prURL)
}

// ghPR is the response from gh pr view.
type ghPR struct {
	State          string `json:"state"`          // "OPEN", "CLOSED", "MERGED"
	ReviewDecision string `json:"reviewDecision"` // "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED", ""
	HeadRefOid     string `json:"headRefOid"`     // SHA of the PR head commit
}

// ghCheck is a single CI check from the PR status.
type ghCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // "COMPLETED", "IN_PROGRESS", "QUEUED"
	Conclusion string `json:"conclusion"` // "SUCCESS", "FAILURE", "NEUTRAL", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED"
}

// ghComment is a review comment from gh api.
type ghComment struct {
	ID        int       `json:"id"`
	NodeID    string    `json:"node_id"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Body string `json:"body"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

// decodeGHComments decodes a newline-delimited JSON stream of ghComment
// objects (as produced by gh --paginate --jq ".[]") into a slice.
func decodeGHComments(data []byte) ([]ghComment, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var comments []ghComment
	for {
		var c ghComment
		if err := dec.Decode(&c); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, nil
}

// GetPRStatus returns the current status of a pull request.
func (p *GitHubPRPoller) GetPRStatus(ctx context.Context, prURL string) (*PRStatus, error) {
	owner, repo, number, err := parsePRRef(prURL)
	if err != nil {
		return nil, err
	}

	nwoRef := owner + "/" + repo

	out, err := exec.CommandContext(ctx, p.command,
		"pr", "view", number,
		"--repo", nwoRef,
		"--json", "state,reviewDecision,headRefOid",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("monitor: get PR status: %w: %s", err, ghStderr(err))
	}

	var pr ghPR
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, fmt.Errorf("monitor: parse PR status: %w", err)
	}

	state := strings.ToLower(pr.State)
	approved := strings.EqualFold(pr.ReviewDecision, "APPROVED")

	return &PRStatus{
		State:          state,
		Approved:       approved,
		ReviewDecision: pr.ReviewDecision,
		HeadSHA:        pr.HeadRefOid,
	}, nil
}

// GetNewComments returns review comments posted after afterID.
// Uses the pulls/comments API endpoint.
func (p *GitHubPRPoller) GetNewComments(ctx context.Context, prURL string, afterID string) ([]PRComment, error) {
	owner, repo, number, err := parsePRRef(prURL)
	if err != nil {
		return nil, err
	}

	// Get review comments (inline code review comments).
	// --paginate with --jq ".[]" unwraps each page's array into individual
	// JSON objects, producing a clean newline-delimited stream across pages.
	// Sort by created ascending so newest comments are last (consistent with afterID filtering).
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%s/comments?sort=created&direction=asc", owner, repo, number)
	out, err := exec.CommandContext(ctx, p.command,
		"api", endpoint,
		"--paginate", "--jq", ".[]",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("monitor: get comments: %w: %s", err, ghStderr(err))
	}

	comments, err := decodeGHComments(out)
	if err != nil {
		return nil, fmt.Errorf("monitor: parse comments: %w", err)
	}

	// Also get issue comments (top-level PR conversation comments).
	// Sort ascending so newest are last, consistent with afterID filtering.
	issueEndpoint := fmt.Sprintf("repos/%s/%s/issues/%s/comments?sort=created&direction=asc", owner, repo, number)
	issueOut, err := exec.CommandContext(ctx, p.command,
		"api", issueEndpoint,
		"--paginate", "--jq", ".[]",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("monitor: get issue comments: %w: %s", err, ghStderr(err))
	}

	issueComments, err := decodeGHComments(issueOut)
	if err != nil {
		return nil, fmt.Errorf("monitor: parse issue comments: %w", err)
	}

	// Convert review comments with RC_ prefix.
	var allComments []PRComment
	for _, comment := range comments {
		allComments = append(allComments, PRComment{
			ID:        fmt.Sprintf("RC_%d", comment.ID),
			Author:    comment.User.Login,
			Body:      comment.Body,
			Path:      comment.Path,
			Line:      comment.Line,
			CreatedAt: comment.CreatedAt,
		})
	}
	// Convert issue comments with IC_ prefix.
	for _, comment := range issueComments {
		allComments = append(allComments, PRComment{
			ID:        fmt.Sprintf("IC_%d", comment.ID),
			Author:    comment.User.Login,
			Body:      comment.Body,
			Path:      comment.Path,
			Line:      comment.Line,
			CreatedAt: comment.CreatedAt,
		})
	}

	// Sort all comments by creation time so that afterID filtering is stable
	// regardless of whether the comment is a review (RC_) or issue (IC_) comment.
	sort.Slice(allComments, func(i, j int) bool {
		if allComments[i].CreatedAt.Equal(allComments[j].CreatedAt) {
			return allComments[i].ID < allComments[j].ID
		}
		return allComments[i].CreatedAt.Before(allComments[j].CreatedAt)
	})

	return filterCommentsAfterID(allComments, afterID), nil
}

// GetCIStatus returns the current CI check status for the PR.
func (p *GitHubPRPoller) GetCIStatus(ctx context.Context, prURL string) (*CIStatus, error) {
	owner, repo, number, err := parsePRRef(prURL)
	if err != nil {
		return nil, err
	}

	nwoRef := owner + "/" + repo

	out, err := exec.CommandContext(ctx, p.command,
		"pr", "view", number,
		"--repo", nwoRef,
		"--json", "statusCheckRollup,headRefOid",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("monitor: get CI status: %w: %s", err, ghStderr(err))
	}

	var pr struct {
		StatusCheckRollup []ghCheck `json:"statusCheckRollup"`
		HeadRefOid        string    `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, fmt.Errorf("monitor: parse CI status: %w", err)
	}

	status := &CIStatus{
		Overall:   "unknown",
		CommitSHA: pr.HeadRefOid,
	}

	if len(pr.StatusCheckRollup) == 0 {
		return status, nil
	}

	allSuccess := true
	anyFailure := false
	anyPending := false

	for _, check := range pr.StatusCheckRollup {
		job := CIJobInfo{
			Name:       check.Name,
			Status:     strings.ToLower(check.Status),
			Conclusion: strings.ToLower(check.Conclusion),
		}

		switch job.Conclusion {
		case "failure", "timed_out", "cancelled":
			anyFailure = true
			allSuccess = false
		case "success", "neutral", "skipped":
			// ok
		default:
			if job.Status != "completed" {
				anyPending = true
				allSuccess = false
			}
		}

		status.Jobs = append(status.Jobs, job)
	}

	switch {
	case anyFailure:
		status.Overall = "failure"
	case anyPending:
		status.Overall = "pending"
	case allSuccess:
		status.Overall = "success"
	}

	return status, nil
}

// PostComment posts a top-level comment to the PR using gh pr comment.
func (p *GitHubPRPoller) PostComment(ctx context.Context, prURL string, body string) error {
	owner, repo, number, err := parsePRRef(prURL)
	if err != nil {
		return err
	}

	nwoRef := owner + "/" + repo

	cmd := exec.CommandContext(ctx, p.command,
		"pr", "comment", number,
		"--repo", nwoRef,
		"--body", body,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("monitor: post comment: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// MergePR merges the pull request using the specified method
// ("merge", "squash", or "rebase"). Maps gh CLI errors to sentinel errors.
func (p *GitHubPRPoller) MergePR(ctx context.Context, prURL string, method string) error {
	owner, repo, number, err := parsePRRef(prURL)
	if err != nil {
		return err
	}

	nwoRef := owner + "/" + repo

	// Default to squash if no method specified.
	flag := "--squash"
	switch strings.ToLower(method) {
	case "merge":
		flag = "--merge"
	case "rebase":
		flag = "--rebase"
	case "squash", "":
		flag = "--squash"
	default:
		return fmt.Errorf("monitor: unsupported merge method %q", method)
	}

	cmd := exec.CommandContext(ctx, p.command,
		"pr", "merge", number,
		"--repo", nwoRef,
		flag,
		"--yes",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		stderr := strings.ToLower(strings.TrimSpace(string(output)))
		switch {
		case strings.Contains(stderr, "merge conflict"):
			return fmt.Errorf("%w: %s", ErrMergeConflict, stderr)
		case strings.Contains(stderr, "was already merged"):
			return fmt.Errorf("%w: %s", ErrPRAlreadyMerged, stderr)
		case strings.Contains(stderr, "closed"),
			strings.Contains(stderr, "not mergeable"):
			return fmt.Errorf("%w: %s", ErrPRClosed, stderr)
		default:
			return fmt.Errorf("monitor: merge PR: %w: %s", err, stderr)
		}
	}
	return nil
}

// ValidateMergePrerequisites checks whether the PR's target branch has
// branch protection rules that might block a merge. It fetches the PR's
// base branch, then queries branch protection for dismiss_stale_reviews.
func (p *GitHubPRPoller) ValidateMergePrerequisites(ctx context.Context, prURL string) error {
	owner, repo, number, err := parsePRRef(prURL)
	if err != nil {
		return err
	}

	nwoRef := owner + "/" + repo

	// Step 1: Get the base branch from the PR.
	out, err := exec.CommandContext(ctx, p.command,
		"pr", "view", number,
		"--repo", nwoRef,
		"--json", "baseRefName",
	).Output()
	if err != nil {
		return fmt.Errorf("monitor: get PR base branch: %w: %s", err, ghStderr(err))
	}

	var prInfo struct {
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal(out, &prInfo); err != nil {
		return fmt.Errorf("monitor: parse PR base branch: %w", err)
	}

	if prInfo.BaseRefName == "" {
		return fmt.Errorf("monitor: PR base branch is empty")
	}

	// Step 2: Fetch branch protection rules for the base branch.
	// URL-encode the branch name to handle branches with '/' (e.g., "feature/foo").
	endpoint := fmt.Sprintf("repos/%s/%s/branches/%s/protection", owner, repo, url.PathEscape(prInfo.BaseRefName))
	protOut, err := exec.CommandContext(ctx, p.command,
		"api", endpoint,
	).Output()
	if err != nil {
		// 404 means no branch protection rules — merging is allowed.
		stderr := ghStderr(err)
		if strings.Contains(stderr, "404") || strings.Contains(strings.ToLower(stderr), "not found") {
			return nil
		}
		return fmt.Errorf("monitor: get branch protection: %w: %s", err, stderr)
	}

	var protection struct {
		RequiredPullRequestReviews struct {
			DismissStaleReviews bool `json:"dismiss_stale_reviews"`
		} `json:"required_pull_request_reviews"`
	}
	if err := json.Unmarshal(protOut, &protection); err != nil {
		return fmt.Errorf("monitor: parse branch protection: %w", err)
	}

	if protection.RequiredPullRequestReviews.DismissStaleReviews {
		return fmt.Errorf("monitor: branch protection requires dismiss_stale_reviews; auto-merge may fail after new pushes")
	}

	return nil
}

// filterCommentsAfterID returns comments that appear after afterID in the
// sorted comment list. If afterID is empty, all comments are returned.
// If afterID is not found (e.g., deleted comment), returns nil to avoid
// re-processing the entire comment history.
func filterCommentsAfterID(comments []PRComment, afterID string) []PRComment {
	if afterID == "" {
		return comments
	}

	var result []PRComment
	pastAfter := false
	for _, c := range comments {
		if !pastAfter {
			if c.ID == afterID {
				pastAfter = true
			}
			continue
		}
		result = append(result, c)
	}

	// If afterID was not found (e.g., deleted comment), return empty
	// to avoid re-processing all comments. The next poll with an updated
	// afterID will pick up new comments.
	if !pastAfter {
		return nil
	}

	return result
}

// ghStderr extracts stderr from an exec.ExitError for error messages.
func ghStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return "(stderr: " + strings.TrimSpace(string(exitErr.Stderr)) + ")"
	}
	return ""
}
