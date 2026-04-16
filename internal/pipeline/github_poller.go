package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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
}

// ghCheck is a single CI check from the PR status.
type ghCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // "COMPLETED", "IN_PROGRESS", "QUEUED"
	Conclusion string `json:"conclusion"` // "SUCCESS", "FAILURE", "NEUTRAL", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED"
}

// ghComment is a review comment from gh api.
type ghComment struct {
	ID     int    `json:"id"`
	NodeID string `json:"node_id"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Body string `json:"body"`
	Path string `json:"path"`
	Line int    `json:"line"`
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
		"--json", "state,reviewDecision",
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
		State:    state,
		Approved: approved,
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
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%s/comments", owner, repo, number)
	out, err := exec.CommandContext(ctx, p.command,
		"api", endpoint,
		"--paginate", "--slurp",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("monitor: get comments: %w: %s", err, ghStderr(err))
	}

	var comments []ghComment
	if err := json.Unmarshal(out, &comments); err != nil {
		return nil, fmt.Errorf("monitor: parse comments: %w", err)
	}

	// Also get issue comments (top-level PR conversation comments).
	issueEndpoint := fmt.Sprintf("repos/%s/%s/issues/%s/comments", owner, repo, number)
	issueOut, err := exec.CommandContext(ctx, p.command,
		"api", issueEndpoint,
		"--paginate", "--slurp",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("monitor: get issue comments: %w: %s", err, ghStderr(err))
	}

	var issueComments []ghComment
	if err := json.Unmarshal(issueOut, &issueComments); err != nil {
		return nil, fmt.Errorf("monitor: parse issue comments: %w", err)
	}

	// Convert review comments with RC_ prefix.
	var allComments []PRComment
	for _, comment := range comments {
		allComments = append(allComments, PRComment{
			ID:     fmt.Sprintf("RC_%d", comment.ID),
			Author: comment.User.Login,
			Body:   comment.Body,
			Path:   comment.Path,
			Line:   comment.Line,
		})
	}
	// Convert issue comments with IC_ prefix.
	for _, comment := range issueComments {
		allComments = append(allComments, PRComment{
			ID:     fmt.Sprintf("IC_%d", comment.ID),
			Author: comment.User.Login,
			Body:   comment.Body,
			Path:   comment.Path,
			Line:   comment.Line,
		})
	}

	// Filter to comments after afterID.
	var result []PRComment
	pastAfter := afterID == ""
	for _, prComment := range allComments {
		if !pastAfter {
			if prComment.ID == afterID {
				pastAfter = true
			}
			continue
		}
		result = append(result, prComment)
	}

	return result, nil
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
		"--json", "statusCheckRollup",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("monitor: get CI status: %w: %s", err, ghStderr(err))
	}

	var pr struct {
		StatusCheckRollup []ghCheck `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, fmt.Errorf("monitor: parse CI status: %w", err)
	}

	status := &CIStatus{
		Overall: "unknown",
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

// ghStderr extracts stderr from an exec.ExitError for error messages.
func ghStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return "(stderr: " + strings.TrimSpace(string(exitErr.Stderr)) + ")"
	}
	return ""
}
