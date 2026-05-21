package ticket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// GitLabConfig holds configuration for the GitLab Issues ticket source.
type GitLabConfig struct {
	Project       string // e.g. "group/project" or numeric project ID
	Command       string // glab binary path; defaults to "glab"
	FetchComments bool   // when true, Fetch includes issue comments (notes)
}

// GitLabSource fetches tickets from GitLab Issues via the glab CLI.
type GitLabSource struct {
	config GitLabConfig
}

// NewGitLabSource creates a GitLab ticket source with the given configuration.
func NewGitLabSource(cfg GitLabConfig) (*GitLabSource, error) {
	if cfg.Project == "" {
		return nil, fmt.Errorf("ticket: gitlab project is required")
	}
	if cfg.Command == "" {
		cfg.Command = "glab"
	}
	return &GitLabSource{config: cfg}, nil
}

// parseGitLabKey parses a GitLab issue key into a project and IID.
// Accepted formats:
//   - "42"                  → uses the source's configured project
//   - "group/project#42"   → uses the specified project
func parseGitLabKey(key string, defaultProject string) (project string, iid string, err error) {
	if idx := strings.LastIndex(key, "#"); idx >= 0 {
		project = key[:idx]
		iid = key[idx+1:]
		if project == "" || iid == "" {
			return "", "", fmt.Errorf("ticket: invalid gitlab key %q: expected \"group/project#IID\"", key)
		}
		return project, iid, nil
	}

	// Plain IID — all characters must be digits.
	for _, ch := range key {
		if ch < '0' || ch > '9' {
			return "", "", fmt.Errorf("ticket: invalid gitlab key %q: expected numeric IID or \"group/project#IID\"", key)
		}
	}
	if key == "" {
		return "", "", fmt.Errorf("ticket: empty gitlab key")
	}
	return defaultProject, key, nil
}

// Fetch retrieves a single GitLab issue by IID.
func (s *GitLabSource) Fetch(ctx context.Context, key string) (*Ticket, error) {
	project, iid, err := parseGitLabKey(key, s.config.Project)
	if err != nil {
		return nil, err
	}

	out, err := exec.CommandContext(ctx, s.config.Command,
		"issue", "view", iid,
		"--repo", project,
		"--output", "json",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("ticket: gitlab fetch %s: %w%s", key, err, exitStderr(err))
	}

	var issue glabIssue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("ticket: gitlab parse response for %s: %w", key, err)
	}

	ticket := issue.toTicket()

	if s.config.FetchComments {
		comments, fetchErr := s.fetchNotes(ctx, project, iid)
		if fetchErr != nil {
			return nil, fmt.Errorf("ticket: gitlab fetch comments for %s: %w", key, fetchErr)
		}
		ticket.Comments = comments
	}

	return ticket, nil
}

// List returns open issues from the configured project.
// If query is non-empty it is passed as a search filter.
func (s *GitLabSource) List(ctx context.Context, query string) ([]Ticket, error) {
	args := []string{
		"issue", "list",
		"--repo", s.config.Project,
		"--output", "json",
	}
	if query != "" {
		args = append(args, "--search", query)
	}

	out, err := exec.CommandContext(ctx, s.config.Command, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("ticket: gitlab list: %w%s", err, exitStderr(err))
	}

	var issues []glabIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("ticket: gitlab parse list response: %w", err)
	}

	tickets := make([]Ticket, 0, len(issues))
	for _, issue := range issues {
		tickets = append(tickets, *issue.toTicket())
	}
	return tickets, nil
}

// fetchNotes retrieves issue comments (notes) via the glab API endpoint.
// System-generated notes (state changes, label assignments) are filtered out.
func (s *GitLabSource) fetchNotes(ctx context.Context, project string, iid string) ([]Comment, error) {
	encoded := url.PathEscape(project)
	endpoint := fmt.Sprintf("projects/%s/issues/%s/notes", encoded, iid)

	out, err := exec.CommandContext(ctx, s.config.Command,
		"api", endpoint,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("glab api %s: %w%s", endpoint, err, exitStderr(err))
	}

	var notes []glabNote
	if err := json.Unmarshal(out, &notes); err != nil {
		return nil, fmt.Errorf("parse notes: %w", err)
	}

	var comments []Comment
	for _, note := range notes {
		if note.System {
			continue
		}
		comments = append(comments, Comment{
			Author:    note.Author.Username,
			Body:      note.Body,
			CreatedAt: note.CreatedAt,
		})
	}
	return comments, nil
}

// GitLab API response types

type glabIssue struct {
	IID         int        `json:"iid"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"`
	Labels      []string   `json:"labels"`
	Assignees   []glabUser `json:"assignees"`
	Author      glabUser   `json:"author"`
	WebURL      string     `json:"web_url"`
}

type glabUser struct {
	Username string `json:"username"`
}

type glabNote struct {
	Body      string   `json:"body"`
	Author    glabUser `json:"author"`
	CreatedAt string   `json:"created_at"`
	System    bool     `json:"system"`
}

func (issue *glabIssue) toTicket() *Ticket {
	labels := make([]string, len(issue.Labels))
	copy(labels, issue.Labels)

	var assignees []string
	for _, assignee := range issue.Assignees {
		assignees = append(assignees, assignee.Username)
	}

	rawFields := map[string]any{
		"iid":       issue.IID,
		"state":     issue.State,
		"assignees": assignees,
		"web_url":   issue.WebURL,
	}

	var status string
	switch issue.State {
	case "opened":
		status = "Open"
	case "closed":
		status = "Closed"
	default:
		if issue.State != "" {
			status = strings.ToUpper(issue.State[:1]) + issue.State[1:]
		}
	}

	return &Ticket{
		Key:                fmt.Sprintf("%d", issue.IID),
		Summary:            issue.Title,
		Description:        issue.Description,
		Type:               "issue",
		Status:             status,
		Labels:             labels,
		AcceptanceCriteria: ExtractCriteria(issue.Description),
		RawFields:          rawFields,
	}
}
