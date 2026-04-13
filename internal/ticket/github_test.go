package ticket

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func mockGHBinary(t *testing.T) string {
	t.Helper()
	return filepath.Join(testdataDir(t), "mock_gh.sh")
}

func TestGitHubSource_Fetch(t *testing.T) {
	t.Setenv("MOCK_GH_FIXTURE", "github_fetch.json")

	source, err := NewGitHubSource(GitHubConfig{
		Owner:   "decko",
		Repo:    "soda",
		Command: mockGHBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitHubSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "36")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key != "36" {
		t.Errorf("Key = %q, want %q", ticket.Key, "36")
	}
	if ticket.Summary != "Add GitHub Issues ticket source" {
		t.Errorf("Summary = %q, want %q", ticket.Summary, "Add GitHub Issues ticket source")
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

	// Acceptance criteria should be extracted from body
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

	// RawFields should contain GitHub-specific fields
	if ticket.RawFields == nil {
		t.Fatal("RawFields is nil")
	}
	if _, ok := ticket.RawFields["state"]; !ok {
		t.Error("RawFields missing 'state'")
	}
	if assignees, ok := ticket.RawFields["assignees"].([]string); !ok || len(assignees) != 1 || assignees[0] != "ddebrito" {
		t.Errorf("RawFields[assignees] = %v, want [ddebrito]", ticket.RawFields["assignees"])
	}
}

func TestGitHubSource_List(t *testing.T) {
	t.Setenv("MOCK_GH_FIXTURE", "github_list.json")

	source, err := NewGitHubSource(GitHubConfig{
		Owner:   "decko",
		Repo:    "soda",
		Command: mockGHBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitHubSource: %v", err)
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

	if tickets[0].Key != "36" {
		t.Errorf("tickets[0].Key = %q, want %q", tickets[0].Key, "36")
	}
	if tickets[1].Key != "37" {
		t.Errorf("tickets[1].Key = %q, want %q", tickets[1].Key, "37")
	}
	if tickets[1].Labels[0] != "bug" {
		t.Errorf("tickets[1].Labels[0] = %q, want %q", tickets[1].Labels[0], "bug")
	}

	// First ticket should have extracted AC
	if len(tickets[0].AcceptanceCriteria) != 2 {
		t.Errorf("tickets[0].AcceptanceCriteria = %v, want 2 items", tickets[0].AcceptanceCriteria)
	}
	// Second ticket has no AC section
	if len(tickets[1].AcceptanceCriteria) != 0 {
		t.Errorf("tickets[1].AcceptanceCriteria = %v, want empty", tickets[1].AcceptanceCriteria)
	}
}

func TestGitHubSource_List_Empty(t *testing.T) {
	t.Setenv("MOCK_GH_FIXTURE", "github_empty_list.json")

	source, err := NewGitHubSource(GitHubConfig{
		Owner:   "decko",
		Repo:    "soda",
		Command: mockGHBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitHubSource: %v", err)
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

func TestNewGitHubSource_MissingConfig(t *testing.T) {
	_, err := NewGitHubSource(GitHubConfig{})
	if err == nil {
		t.Fatal("NewGitHubSource should fail with empty owner/repo")
	}

	_, err = NewGitHubSource(GitHubConfig{Owner: "decko"})
	if err == nil {
		t.Fatal("NewGitHubSource should fail with empty repo")
	}
}

func TestGitHubSource_Fetch_BadBinary(t *testing.T) {
	source, err := NewGitHubSource(GitHubConfig{
		Owner:   "decko",
		Repo:    "soda",
		Command: "/nonexistent/binary",
	})
	if err != nil {
		t.Fatalf("NewGitHubSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = source.Fetch(ctx, "1")
	if err == nil {
		t.Fatal("Fetch should fail with bad binary")
	}
}

func TestGitHubSource_Fetch_NotFound(t *testing.T) {
	t.Setenv("MOCK_GH_FIXTURE", "nonexistent_fixture.json")

	source, err := NewGitHubSource(GitHubConfig{
		Owner:   "decko",
		Repo:    "soda",
		Command: mockGHBinary(t),
	})
	if err != nil {
		t.Fatalf("NewGitHubSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = source.Fetch(ctx, "999")
	if err == nil {
		t.Fatal("Fetch should fail for not-found issue")
	}
}

// Verify GitHubSource satisfies Source interface at compile time.
var _ Source = (*GitHubSource)(nil)
