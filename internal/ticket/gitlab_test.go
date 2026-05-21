package ticket

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func mockGlabBinary(t *testing.T) string {
	t.Helper()
	return filepath.Join(testdataDir(t), "mock_glab.sh")
}

func TestParseGitLabKey(t *testing.T) {
	tests := []struct {
		name           string
		key            string
		defaultProject string
		wantProject    string
		wantIID        string
		wantErr        bool
	}{
		{
			name:           "plain IID",
			key:            "42",
			defaultProject: "group/project",
			wantProject:    "group/project",
			wantIID:        "42",
		},
		{
			name:           "qualified key",
			key:            "group/project#42",
			defaultProject: "other/project",
			wantProject:    "group/project",
			wantIID:        "42",
		},
		{
			name:           "subgroup qualified key",
			key:            "org/team/project#99",
			defaultProject: "other/project",
			wantProject:    "org/team/project",
			wantIID:        "99",
		},
		{
			name:           "empty key",
			key:            "",
			defaultProject: "group/project",
			wantErr:        true,
		},
		{
			name:           "missing IID after hash",
			key:            "group/project#",
			defaultProject: "group/project",
			wantErr:        true,
		},
		{
			name:           "missing project before hash",
			key:            "#42",
			defaultProject: "group/project",
			wantErr:        true,
		},
		{
			name:           "non-numeric plain key",
			key:            "abc",
			defaultProject: "group/project",
			wantErr:        true,
		},
		{
			name:           "mixed alphanumeric plain key",
			key:            "42a",
			defaultProject: "group/project",
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project, iid, err := parseGitLabKey(tt.key, tt.defaultProject)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got project=%q iid=%q", project, iid)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if project != tt.wantProject {
				t.Errorf("project = %q, want %q", project, tt.wantProject)
			}
			if iid != tt.wantIID {
				t.Errorf("iid = %q, want %q", iid, tt.wantIID)
			}
		})
	}
}

func TestGitLabSource_Fetch(t *testing.T) {
	t.Setenv("MOCK_GLAB_FIXTURE", "gitlab_fetch.json")

	source, err := NewGitLabSource(GitLabConfig{
		Project: "group/project",
		Command: mockGlabBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitLabSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "42")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key != "42" {
		t.Errorf("Key = %q, want %q", ticket.Key, "42")
	}
	if ticket.Summary != "Add GitLab Issues ticket source" {
		t.Errorf("Summary = %q, want %q", ticket.Summary, "Add GitLab Issues ticket source")
	}
	if ticket.Type != "issue" {
		t.Errorf("Type = %q, want %q", ticket.Type, "issue")
	}
	if ticket.Status != "Open" {
		t.Errorf("Status = %q, want %q", ticket.Status, "Open")
	}
	if len(ticket.Labels) != 2 || ticket.Labels[0] != "enhancement" || ticket.Labels[1] != "ticket" {
		t.Errorf("Labels = %v, want [enhancement ticket]", ticket.Labels)
	}

	// Acceptance criteria should be extracted from description.
	wantAC := []string{
		"Fetch retrieves a single issue",
		"List returns open issues",
		"Labels are mapped correctly",
	}
	if len(ticket.AcceptanceCriteria) != len(wantAC) {
		t.Fatalf("AcceptanceCriteria len = %d, want %d: %v",
			len(ticket.AcceptanceCriteria), len(wantAC), ticket.AcceptanceCriteria)
	}
	for idx, want := range wantAC {
		if ticket.AcceptanceCriteria[idx] != want {
			t.Errorf("AcceptanceCriteria[%d] = %q, want %q", idx, ticket.AcceptanceCriteria[idx], want)
		}
	}

	// RawFields should contain GitLab-specific fields.
	if ticket.RawFields == nil {
		t.Fatal("RawFields is nil")
	}
	if _, ok := ticket.RawFields["state"]; !ok {
		t.Error("RawFields missing 'state'")
	}
	if assignees, ok := ticket.RawFields["assignees"].([]string); !ok || len(assignees) != 1 || assignees[0] != "ddebrito" {
		t.Errorf("RawFields[assignees] = %v, want [ddebrito]", ticket.RawFields["assignees"])
	}
	if webURL, ok := ticket.RawFields["web_url"].(string); !ok || webURL != "https://gitlab.com/group/project/-/issues/42" {
		t.Errorf("RawFields[web_url] = %v, want URL", ticket.RawFields["web_url"])
	}

	// Comments should be empty when FetchComments is false.
	if len(ticket.Comments) != 0 {
		t.Errorf("Comments len = %d, want 0 when FetchComments is false", len(ticket.Comments))
	}
}

func TestGitLabSource_List(t *testing.T) {
	t.Setenv("MOCK_GLAB_FIXTURE", "gitlab_list.json")

	source, err := NewGitLabSource(GitLabConfig{
		Project: "group/project",
		Command: mockGlabBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitLabSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tickets, err := source.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(tickets) != 2 {
		t.Fatalf("List returned %d tickets, want 2", len(tickets))
	}

	if tickets[0].Key != "42" {
		t.Errorf("tickets[0].Key = %q, want %q", tickets[0].Key, "42")
	}
	if tickets[1].Key != "43" {
		t.Errorf("tickets[1].Key = %q, want %q", tickets[1].Key, "43")
	}
	if tickets[1].Labels[0] != "bug" {
		t.Errorf("tickets[1].Labels[0] = %q, want %q", tickets[1].Labels[0], "bug")
	}

	// First ticket should have extracted AC.
	if len(tickets[0].AcceptanceCriteria) != 2 {
		t.Errorf("tickets[0].AcceptanceCriteria = %v, want 2 items", tickets[0].AcceptanceCriteria)
	}
	// Second ticket has no AC section.
	if len(tickets[1].AcceptanceCriteria) != 0 {
		t.Errorf("tickets[1].AcceptanceCriteria = %v, want empty", tickets[1].AcceptanceCriteria)
	}
}

func TestGitLabSource_List_Empty(t *testing.T) {
	t.Setenv("MOCK_GLAB_FIXTURE", "gitlab_empty_list.json")

	source, err := NewGitLabSource(GitLabConfig{
		Project: "group/project",
		Command: mockGlabBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitLabSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tickets, err := source.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(tickets) != 0 {
		t.Errorf("List returned %d tickets, want 0", len(tickets))
	}
}

func TestNewGitLabSource_MissingConfig(t *testing.T) {
	_, err := NewGitLabSource(GitLabConfig{})
	if err == nil {
		t.Fatal("NewGitLabSource should fail with empty project")
	}
}

func TestGitLabSource_Fetch_BadBinary(t *testing.T) {
	source, err := NewGitLabSource(GitLabConfig{
		Project: "group/project",
		Command: "/nonexistent/binary",
	})
	if err != nil {
		t.Fatalf("NewGitLabSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = source.Fetch(ctx, "1")
	if err == nil {
		t.Fatal("Fetch should fail with bad binary")
	}
}

func TestGitLabSource_Fetch_NotFound(t *testing.T) {
	t.Setenv("MOCK_GLAB_FIXTURE", "nonexistent_fixture.json")

	source, err := NewGitLabSource(GitLabConfig{
		Project: "group/project",
		Command: mockGlabBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitLabSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = source.Fetch(ctx, "999")
	if err == nil {
		t.Fatal("Fetch should fail for not-found issue")
	}
}

func TestGitLabSource_Fetch_WithComments(t *testing.T) {
	t.Setenv("MOCK_GLAB_FIXTURE", "gitlab_fetch.json")
	t.Setenv("MOCK_GLAB_NOTES_FIXTURE", "gitlab_notes.json")

	source, err := NewGitLabSource(GitLabConfig{
		Project:       "group/project",
		Command:       mockGlabBinary(t),
		FetchComments: true,
	})
	if err != nil {
		t.Fatalf("NewGitLabSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "42")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key != "42" {
		t.Errorf("Key = %q, want %q", ticket.Key, "42")
	}

	// Comments should be populated, system notes filtered out.
	if len(ticket.Comments) != 2 {
		t.Fatalf("Comments len = %d, want 2 (system note should be filtered)", len(ticket.Comments))
	}
	if ticket.Comments[0].Author != "reviewer1" {
		t.Errorf("Comments[0].Author = %q, want %q", ticket.Comments[0].Author, "reviewer1")
	}
	if ticket.Comments[0].Body != "Looks good, but please add tests." {
		t.Errorf("Comments[0].Body = %q, want %q", ticket.Comments[0].Body, "Looks good, but please add tests.")
	}
	if ticket.Comments[0].CreatedAt != "2025-07-01T10:00:00Z" {
		t.Errorf("Comments[0].CreatedAt = %q, want %q", ticket.Comments[0].CreatedAt, "2025-07-01T10:00:00Z")
	}
	if ticket.Comments[1].Author != "ddebrito" {
		t.Errorf("Comments[1].Author = %q, want %q", ticket.Comments[1].Author, "ddebrito")
	}
	if ticket.Comments[1].CreatedAt != "2025-07-01T12:30:00Z" {
		t.Errorf("Comments[1].CreatedAt = %q, want %q", ticket.Comments[1].CreatedAt, "2025-07-01T12:30:00Z")
	}
}

func TestGitLabSource_Fetch_QualifiedKey(t *testing.T) {
	t.Setenv("MOCK_GLAB_FIXTURE", "gitlab_fetch.json")

	source, err := NewGitLabSource(GitLabConfig{
		Project: "default/project",
		Command: mockGlabBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitLabSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use qualified key — should override the default project.
	ticket, err := source.Fetch(ctx, "group/project#42")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key != "42" {
		t.Errorf("Key = %q, want %q", ticket.Key, "42")
	}
}

func TestGitLabSource_StatusMapping(t *testing.T) {
	tests := []struct {
		state      string
		wantStatus string
	}{
		{"opened", "Open"},
		{"closed", "Closed"},
	}
	for _, tt := range tests {
		issue := glabIssue{IID: 1, State: tt.state}
		ticket := issue.toTicket()
		if ticket.Status != tt.wantStatus {
			t.Errorf("state %q → Status = %q, want %q", tt.state, ticket.Status, tt.wantStatus)
		}
	}
}

// Verify GitLabSource satisfies Source interface at compile time.
var _ Source = (*GitLabSource)(nil)
