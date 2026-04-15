package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// GitHubConfig holds configuration for the GitHub Issues ticket source.
type GitHubConfig struct {
	Owner         string
	Repo          string
	Command       string // gh binary path; defaults to "gh"
	FetchComments bool   // when true, Fetch includes issue comments
}

// GitHubSource fetches tickets from GitHub Issues via the gh CLI.
type GitHubSource struct {
	config GitHubConfig
}

// NewGitHubSource creates a GitHub ticket source with the given configuration.
func NewGitHubSource(cfg GitHubConfig) (*GitHubSource, error) {
	if cfg.Owner == "" || cfg.Repo == "" {
		return nil, fmt.Errorf("ticket: github owner and repo are required")
	}
	if cfg.Command == "" {
		cfg.Command = "gh"
	}
	return &GitHubSource{config: cfg}, nil
}

func (s *GitHubSource) repo() string {
	return s.config.Owner + "/" + s.config.Repo
}

// Fetch retrieves a single GitHub issue by number.
func (s *GitHubSource) Fetch(ctx context.Context, key string) (*Ticket, error) {
	out, err := exec.CommandContext(ctx, s.config.Command,
		"issue", "view", key,
		"--repo", s.repo(),
		"--json", "number,title,body,labels,state,assignees",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("ticket: github fetch %s: %w%s", key, err, exitStderr(err))
	}

	var issue ghIssue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("ticket: github parse response for %s: %w", key, err)
	}

	return issue.toTicket(), nil
}

// List returns open issues from the configured repository.
// If query is non-empty it is passed as a GitHub search filter.
func (s *GitHubSource) List(ctx context.Context, query string) ([]Ticket, error) {
	args := []string{
		"issue", "list",
		"--repo", s.repo(),
		"--state", "open",
		"--json", "number,title,body,labels,state,assignees",
		"--limit", "20",
	}
	if query != "" {
		args = append(args, "--search", query)
	}

	out, err := exec.CommandContext(ctx, s.config.Command, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("ticket: github list: %w%s", err, exitStderr(err))
	}

	var issues []ghIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("ticket: github parse list response: %w", err)
	}

	tickets := make([]Ticket, 0, len(issues))
	for _, issue := range issues {
		tickets = append(tickets, *issue.toTicket())
	}
	return tickets, nil
}

// GitHub API response types

type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Labels    []ghLabel `json:"labels"`
	Assignees []ghUser  `json:"assignees"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghUser struct {
	Login string `json:"login"`
}

func (issue *ghIssue) toTicket() *Ticket {
	labels := make([]string, len(issue.Labels))
	for i, l := range issue.Labels {
		labels[i] = l.Name
	}

	var assignees []string
	for _, a := range issue.Assignees {
		assignees = append(assignees, a.Login)
	}

	rawFields := map[string]any{
		"number":    issue.Number,
		"state":     issue.State,
		"assignees": assignees,
	}

	var status string
	if issue.State != "" {
		status = strings.ToUpper(issue.State[:1]) + issue.State[1:]
	}

	return &Ticket{
		Key:                fmt.Sprintf("%d", issue.Number),
		Summary:            issue.Title,
		Description:        issue.Body,
		Type:               "issue",
		Status:             status,
		Labels:             labels,
		AcceptanceCriteria: ExtractCriteria(issue.Body),
		RawFields:          rawFields,
	}
}

func exitStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return " (stderr: " + strings.TrimSpace(string(exitErr.Stderr)) + ")"
	}
	return ""
}
