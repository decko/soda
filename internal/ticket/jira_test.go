package ticket

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func TestJiraSource_Fetch_WithCustomFields(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_fetch_with_spec.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "PROJ-50")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key != "PROJ-50" {
		t.Errorf("Key = %q, want %q", ticket.Key, "PROJ-50")
	}
	if ticket.RawFields == nil {
		t.Fatal("RawFields is nil")
	}

	// Verify custom fields survive in RawFields
	specVal, ok := ticket.RawFields["customfield_10050"]
	if !ok {
		t.Fatal("RawFields missing customfield_10050")
	}
	planVal, ok := ticket.RawFields["customfield_10051"]
	if !ok {
		t.Fatal("RawFields missing customfield_10051")
	}

	// FieldExtractor should populate ExistingSpec/ExistingPlan from custom fields
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
		PlanField: "customfield_10051",
	}
	extractor.Extract(ticket)

	if ticket.ExistingSpec != "Spec from custom field." {
		t.Errorf("ExistingSpec = %q, want %q", ticket.ExistingSpec, "Spec from custom field.")
	}
	if ticket.ExistingPlan != "Plan from custom field." {
		t.Errorf("ExistingPlan = %q, want %q", ticket.ExistingPlan, "Plan from custom field.")
	}

	// Sanity-check the raw values match
	if specStr, ok := specVal.(string); !ok || specStr != "Spec from custom field." {
		t.Errorf("RawFields[customfield_10050] = %v, want %q", specVal, "Spec from custom field.")
	}
	if planStr, ok := planVal.(string); !ok || planStr != "Plan from custom field." {
		t.Errorf("RawFields[customfield_10051] = %v, want %q", planVal, "Plan from custom field.")
	}
}

func TestJiraSource_Fetch_DescriptionMarkers(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_fetch_with_spec.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "PROJ-50")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// String description should survive fetch intact
	if !strings.Contains(ticket.Description, "<!-- spec:start -->") {
		t.Fatalf("Description missing spec markers: %q", ticket.Description)
	}

	// DescriptionMarkerExtractor should extract spec/plan from description
	extractor := &DescriptionMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
		Plan: MarkerPair{
			StartMarker: "<!-- plan:start -->",
			EndMarker:   "<!-- plan:end -->",
		},
	}
	extractor.Extract(ticket)

	if ticket.ExistingSpec != "Spec from description markers." {
		t.Errorf("ExistingSpec = %q, want %q", ticket.ExistingSpec, "Spec from description markers.")
	}
	if ticket.ExistingPlan != "Plan from description markers." {
		t.Errorf("ExistingPlan = %q, want %q", ticket.ExistingPlan, "Plan from description markers.")
	}
}

func TestJiraSource_Fetch_WithSubtasks(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_subtasks.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "EPIC-20")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key != "EPIC-20" {
		t.Errorf("Key = %q, want %q", ticket.Key, "EPIC-20")
	}
	if ticket.RawFields == nil {
		t.Fatal("RawFields is nil")
	}

	// RawFields should carry subtasks as []any
	subtasksRaw, ok := ticket.RawFields["subtasks"]
	if !ok {
		t.Fatal("RawFields missing 'subtasks'")
	}
	subtasks, ok := subtasksRaw.([]any)
	if !ok {
		t.Fatalf("subtasks is %T, want []any", subtasksRaw)
	}
	if len(subtasks) != 3 {
		t.Fatalf("subtasks len = %d, want 3", len(subtasks))
	}

	// SubtaskExtractor should format ExistingPlan
	extractor := &SubtaskExtractor{}
	extractor.Extract(ticket)

	wantPlan := "- [PROJ-21] Design authentication flow (Done)\n" +
		"- [PROJ-22] Implement login endpoint (In Progress)\n" +
		"- [PROJ-23] Add session expiry logic (To Do)"
	if ticket.ExistingPlan != wantPlan {
		t.Errorf("ExistingPlan =\n%s\nwant:\n%s", ticket.ExistingPlan, wantPlan)
	}
}

func TestJiraSource_Fetch_ADFDescription(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_fetch_adf.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "PROJ-55")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key != "PROJ-55" {
		t.Errorf("Key = %q, want %q", ticket.Key, "PROJ-55")
	}

	// Description should be plain text extracted from ADF, not raw JSON
	if strings.HasPrefix(ticket.Description, "{") {
		t.Errorf("Description starts with '{', expected plain text from ADF extraction")
	}

	// All three ADF text nodes should appear in the extracted description
	for _, want := range []string{"First paragraph text.", "Section Heading", "Second paragraph text."} {
		if !strings.Contains(ticket.Description, want) {
			t.Errorf("Description missing %q: got %q", want, ticket.Description)
		}
	}
}

func TestJiraSource_Fetch_InvalidJSON(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_invalid.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = source.Fetch(ctx, "PROJ-BAD")
	if err == nil {
		t.Fatal("Fetch should fail for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "parse")
	}
}

func TestJiraSource_Fetch_NullFields(t *testing.T) {
	t.Setenv("MOCK_MCP_FIXTURE", "jira_fetch_null_fields.json")

	source, err := NewJiraSource(JiraConfig{Command: mockBinary(t)})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, "PROJ-99")
	if err != nil {
		t.Fatalf("Fetch should not fail for null fields: %v", err)
	}

	if ticket.Key != "PROJ-99" {
		t.Errorf("Key = %q, want %q", ticket.Key, "PROJ-99")
	}
	if ticket.Summary != "" {
		t.Errorf("Summary = %q, want empty", ticket.Summary)
	}
	if ticket.RawFields != nil {
		t.Errorf("RawFields = %v, want nil", ticket.RawFields)
	}
}

func TestJiraSource_Integration(t *testing.T) {
	jiraURL := os.Getenv("JIRA_TEST_URL")
	if jiraURL == "" {
		t.Skip("JIRA_TEST_URL not set; skipping integration test")
	}

	if _, lookErr := exec.LookPath("wtmcp"); lookErr != nil {
		t.Skip("wtmcp binary not found; skipping integration test")
	}

	source, err := NewJiraSource(JiraConfig{Command: "wtmcp"})
	if err != nil {
		t.Fatalf("NewJiraSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ticket, err := source.Fetch(ctx, jiraURL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if ticket.Key == "" {
		t.Error("Key is empty")
	}
	if ticket.Summary == "" {
		t.Error("Summary is empty")
	}
}

// Verify JiraSource satisfies Source interface at compile time.
var _ Source = (*JiraSource)(nil)

func TestMain(m *testing.M) {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "testdata")
	os.Chmod(filepath.Join(dir, "mock_mcp.sh"), 0755)
	os.Chmod(filepath.Join(dir, "mock_gh.sh"), 0755)
	os.Exit(m.Run())
}
