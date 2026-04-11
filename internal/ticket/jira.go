package ticket

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// JiraConfig holds configuration for the Jira ticket source.
type JiraConfig struct {
	Command string // MCP server binary (e.g., "wtmcp")
	Project string // Jira project key (e.g., "MYPROJECT")
	Query   string // default JQL query for List when none is provided
}

// JiraSource fetches tickets from Jira via an MCP server (e.g., wtmcp).
// Each call to Fetch or List spawns a short-lived MCP session.
type JiraSource struct {
	config JiraConfig
}

// NewJiraSource creates a Jira ticket source with the given configuration.
func NewJiraSource(config JiraConfig) (*JiraSource, error) {
	if config.Command == "" {
		return nil, fmt.Errorf("ticket: jira command is required")
	}
	return &JiraSource{config: config}, nil
}

// Fetch retrieves a single Jira issue by key.
func (s *JiraSource) Fetch(ctx context.Context, key string) (*Ticket, error) {
	client, err := newMCPClient(ctx, s.config.Command)
	if err != nil {
		return nil, fmt.Errorf("ticket: jira fetch %s: %w", key, err)
	}
	defer client.close()

	text, err := client.callTool("jira_get_issues", map[string]any{
		"issue_keys": key,
		"brief":      false,
		"fields":     "*",
	})
	if err != nil {
		return nil, fmt.Errorf("ticket: jira fetch %s: %w", key, err)
	}

	var result jiraSearchResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("ticket: jira parse response for %s: %w", key, err)
	}

	if len(result.Issues) == 0 {
		return nil, fmt.Errorf("ticket: jira issue %s not found", key)
	}

	issue := result.Issues[0]
	if issue.Error != "" {
		return nil, fmt.Errorf("ticket: jira issue %s: %s", key, issue.Error)
	}

	return issue.toTicket(), nil
}

// List returns tickets matching a JQL query. If query is empty, the
// configured default query is used.
func (s *JiraSource) List(ctx context.Context, query string) ([]Ticket, error) {
	if query == "" {
		query = s.config.Query
	}
	if query == "" {
		return nil, fmt.Errorf("ticket: jira list requires a query")
	}

	client, err := newMCPClient(ctx, s.config.Command)
	if err != nil {
		return nil, fmt.Errorf("ticket: jira list: %w", err)
	}
	defer client.close()

	text, err := client.callTool("jira_search", map[string]any{
		"jql":         query,
		"brief":       false,
		"fields":      "summary,description,issuetype,priority,labels,status",
		"max_results": 50,
	})
	if err != nil {
		return nil, fmt.Errorf("ticket: jira list: %w", err)
	}

	var result jiraSearchResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("ticket: jira parse search results: %w", err)
	}

	tickets := make([]Ticket, 0, len(result.Issues))
	for _, issue := range result.Issues {
		if issue.Error != "" {
			continue // skip inaccessible issues
		}
		tickets = append(tickets, *issue.toTicket())
	}

	return tickets, nil
}

// Jira API response types

type jiraSearchResult struct {
	Issues []jiraIssue `json:"issues"`
	Count  int         `json:"count"`
	Total  int         `json:"total"`
}

type jiraIssue struct {
	Key    string          `json:"key"`
	Fields json.RawMessage `json:"fields"`
	Error  string          `json:"error,omitempty"`
}

type jiraFields struct {
	Summary     string         `json:"summary"`
	Description any            `json:"description"` // string (Server) or ADF JSON (Cloud)
	IssueType   *jiraNameField `json:"issuetype"`
	Priority    *jiraNameField `json:"priority"`
	Status      *jiraNameField `json:"status"`
	Labels      []string       `json:"labels"`
}

type jiraNameField struct {
	Name string `json:"name"`
}

func (issue *jiraIssue) toTicket() *Ticket {
	var fields jiraFields
	if err := json.Unmarshal(issue.Fields, &fields); err != nil {
		return &Ticket{Key: issue.Key}
	}

	description := extractDescription(fields.Description)

	var rawFields map[string]any
	_ = json.Unmarshal(issue.Fields, &rawFields)

	ticket := &Ticket{
		Key:                issue.Key,
		Summary:            fields.Summary,
		Description:        description,
		Labels:             fields.Labels,
		AcceptanceCriteria: ExtractCriteria(description),
		RawFields:          rawFields,
	}

	if fields.IssueType != nil {
		ticket.Type = fields.IssueType.Name
	}
	if fields.Priority != nil {
		ticket.Priority = fields.Priority.Name
	}
	if fields.Status != nil {
		ticket.Status = fields.Status.Name
	}

	return ticket
}

// extractDescription converts the Jira description field to plain text.
// Server/DC returns a string; Cloud v3 returns an ADF document (JSON object).
func extractDescription(desc any) string {
	switch val := desc.(type) {
	case string:
		return val
	case map[string]any:
		var builder strings.Builder
		extractADFText(val, &builder)
		return builder.String()
	default:
		return ""
	}
}

// extractADFText recursively extracts text from an Atlassian Document Format node.
func extractADFText(node map[string]any, builder *strings.Builder) {
	if text, ok := node["text"].(string); ok {
		builder.WriteString(text)
	}

	content, ok := node["content"].([]any)
	if !ok {
		return
	}

	for _, child := range content {
		childMap, ok := child.(map[string]any)
		if !ok {
			continue
		}

		childType, _ := childMap["type"].(string)
		if childType == "listItem" {
			builder.WriteString("- ")
		}

		extractADFText(childMap, builder)

		if childType == "paragraph" || childType == "heading" || childType == "listItem" {
			builder.WriteString("\n")
		}
	}
}
