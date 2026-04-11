package ticket

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	return filepath.Join(filepath.Dir(file), "testdata")
}

func mockBinary(t *testing.T) string {
	t.Helper()
	return filepath.Join(testdataDir(t), "mock_mcp.sh")
}

func TestJiraSource_Fetch(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_fetch.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "PROJ-42")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key != "PROJ-42" {
		t.Errorf("Key = %q, want %q", ticket.Key, "PROJ-42")
	}
	if ticket.Summary != "Add user authentication" {
		t.Errorf("Summary = %q, want %q", ticket.Summary, "Add user authentication")
	}
	if ticket.Type != "Story" {
		t.Errorf("Type = %q, want %q", ticket.Type, "Story")
	}
	if ticket.Priority != "High" {
		t.Errorf("Priority = %q, want %q", ticket.Priority, "High")
	}
	if ticket.Status != "In Progress" {
		t.Errorf("Status = %q, want %q", ticket.Status, "In Progress")
	}
	if len(ticket.Labels) != 2 || ticket.Labels[0] != "backend" || ticket.Labels[1] != "auth" {
		t.Errorf("Labels = %v, want [backend auth]", ticket.Labels)
	}

	// Acceptance criteria should be extracted from description
	wantAC := []string{
		"Users can register with email",
		"Users can log in with credentials",
		"Session expires after 30 minutes",
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

	// RawFields should contain all Jira fields
	if ticket.RawFields == nil {
		t.Fatal("RawFields is nil")
	}
	if _, ok := ticket.RawFields["summary"]; !ok {
		t.Error("RawFields missing 'summary'")
	}
	if _, ok := ticket.RawFields["components"]; !ok {
		t.Error("RawFields missing 'components' (source-specific field)")
	}
}

func TestJiraSource_Fetch_NotFound(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_not_found.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = source.Fetch(ctx, "PROJ-999")
	if err == nil {
		t.Fatal("Fetch should fail for not-found issue")
	}
}

func TestJiraSource_List(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_search.json")

	source, err := NewJiraSource(JiraConfig{
		Command: mockBinary(t),
		Query:   "project = PROJ",
	})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
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

	if tickets[0].Key != "PROJ-42" {
		t.Errorf("tickets[0].Key = %q, want %q", tickets[0].Key, "PROJ-42")
	}
	if tickets[1].Key != "PROJ-43" {
		t.Errorf("tickets[1].Key = %q, want %q", tickets[1].Key, "PROJ-43")
	}
	if tickets[1].Type != "Bug" {
		t.Errorf("tickets[1].Type = %q, want %q", tickets[1].Type, "Bug")
	}

	// First ticket should have extracted AC from wiki markup
	if len(tickets[0].AcceptanceCriteria) != 2 {
		t.Errorf("tickets[0].AcceptanceCriteria = %v, want 2 items", tickets[0].AcceptanceCriteria)
	}
	// Second ticket has no AC section
	if len(tickets[1].AcceptanceCriteria) != 0 {
		t.Errorf("tickets[1].AcceptanceCriteria = %v, want empty", tickets[1].AcceptanceCriteria)
	}
}

func TestJiraSource_List_ExplicitQuery(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_empty_search.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tickets, err := source.List(ctx, "status = Done")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(tickets) != 0 {
		t.Errorf("List returned %d tickets, want 0", len(tickets))
	}
}

func TestJiraSource_List_NoQuery(t *testing.T) {
	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = source.List(ctx, "")
	if err == nil {
		t.Fatal("List should fail when no query is provided and no default is configured")
	}
}

func TestNewJiraSource_EmptyCommand(t *testing.T) {
	_, err := NewJiraSource(JiraConfig{})
	if err == nil {
		t.Fatal("NewJiraSource should fail with empty command")
	}
}

func TestJiraSource_Fetch_BadBinary(t *testing.T) {
	source, err := NewJiraSource(JiraConfig{Command: "/nonexistent/binary"})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = source.Fetch(ctx, "PROJ-1")
	if err == nil {
		t.Fatal("Fetch should fail with bad binary")
	}
}

// Verify JiraSource satisfies Source interface at compile time.
var _ Source = (*JiraSource)(nil)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
