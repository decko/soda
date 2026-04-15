package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func testSessions() []SessionInfo {
	return []SessionInfo{
		{Ticket: "36", DirName: "36", Summary: "Add version command to CLI", Status: "completed", Cost: 0.98, Elapsed: "5m49s", StartedAt: time.Now().Add(-2 * time.Hour)},
		{Ticket: "58", DirName: "58", Summary: "Show detailed pipeline outcome", Status: "completed", Cost: 5.58, Elapsed: "12m47s", StartedAt: time.Now().Add(-30 * time.Minute)},
		{Ticket: "64", DirName: "64", Summary: "Delete branch when cleaning", Status: "running", Cost: 0.45, Elapsed: "2m12s", StartedAt: time.Now()},
		{Ticket: "50", DirName: "50", Summary: "Resume gate check bug", Status: "failed", Cost: 1.23, Elapsed: "4m30s", StartedAt: time.Now().Add(-5 * time.Hour)},
	}
}

func TestSessionsModel_InitialState(t *testing.T) {
	sessions := testSessions()
	model := NewSessionsModel(sessions)

	if model.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", model.cursor)
	}
	if model.result != nil {
		t.Error("expected nil result initially")
	}
}

func TestSessionsModel_NavigateDown(t *testing.T) {
	model := NewSessionsModel(testSessions())

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m := model2.(SessionsModel)
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", m.cursor)
	}

	// Navigate to last
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m3, _ := m2.(SessionsModel).handleKey(tea.KeyMsg{Type: tea.KeyDown})
	mm := m3.(SessionsModel)
	if mm.cursor != 3 {
		t.Errorf("expected cursor at 3, got %d", mm.cursor)
	}

	// Cannot go past last
	m4, _ := mm.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if m4.(SessionsModel).cursor != 3 {
		t.Errorf("expected cursor still at 3, got %d", m4.(SessionsModel).cursor)
	}
}

func TestSessionsModel_NavigateUp(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.cursor = 2

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	m := model2.(SessionsModel)
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1 after up, got %d", m.cursor)
	}

	// Navigate to first
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if m2.(SessionsModel).cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", m2.(SessionsModel).cursor)
	}

	// Cannot go past first
	m3, _ := m2.(SessionsModel).handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if m3.(SessionsModel).cursor != 0 {
		t.Errorf("expected cursor still at 0, got %d", m3.(SessionsModel).cursor)
	}
}

func TestSessionsModel_NavigateWithKJ(t *testing.T) {
	model := NewSessionsModel(testSessions())

	// j moves down
	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if model2.(SessionsModel).cursor != 1 {
		t.Errorf("expected cursor at 1 after j, got %d", model2.(SessionsModel).cursor)
	}

	// k moves up
	model3, _ := model2.(SessionsModel).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if model3.(SessionsModel).cursor != 0 {
		t.Errorf("expected cursor at 0 after k, got %d", model3.(SessionsModel).cursor)
	}
}

func TestSessionsModel_EnterViewsHistory(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.cursor = 1

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m := model2.(SessionsModel)

	if m.result == nil {
		t.Fatal("expected result after Enter")
	}
	if m.result.Ticket != "58" {
		t.Errorf("expected ticket 58, got %q", m.result.Ticket)
	}
	if m.result.DirName != "58" {
		t.Errorf("expected dirName 58, got %q", m.result.DirName)
	}
	if m.result.Action != SessionActionView {
		t.Errorf("expected SessionActionView, got %d", m.result.Action)
	}
	if cmd == nil {
		t.Error("expected quit command after Enter")
	}
}

func TestSessionsModel_EnterPropagatesDirName(t *testing.T) {
	sessions := []SessionInfo{
		{Ticket: "PROJ-42", DirName: "proj-42-slugified", Summary: "Test", Status: "completed", Cost: 1.0},
	}
	model := NewSessionsModel(sessions)

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m := model2.(SessionsModel)

	if m.result == nil {
		t.Fatal("expected result after Enter")
	}
	if m.result.Ticket != "PROJ-42" {
		t.Errorf("expected ticket PROJ-42, got %q", m.result.Ticket)
	}
	if m.result.DirName != "proj-42-slugified" {
		t.Errorf("expected dirName proj-42-slugified, got %q", m.result.DirName)
	}
}

func TestSessionsModel_ResumeFailedSession(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.cursor = 3 // failed session

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m := model2.(SessionsModel)

	if m.result == nil {
		t.Fatal("expected result after r on failed session")
	}
	if m.result.Action != SessionActionResume {
		t.Errorf("expected SessionActionResume, got %d", m.result.Action)
	}
	if cmd == nil {
		t.Error("expected quit command after resume")
	}
}

func TestSessionsModel_ResumeIgnoredOnCompleted(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.cursor = 0 // completed session

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m := model2.(SessionsModel)

	if m.result != nil {
		t.Error("expected no result on r for completed session")
	}
}

func TestSessionsModel_ResumeIgnoredOnRunning(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.cursor = 2 // running session

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m := model2.(SessionsModel)

	if m.result != nil {
		t.Error("expected no result on r for running session")
	}
}

func TestSessionsModel_DeleteCompletedSession(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.cursor = 0 // completed session

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m := model2.(SessionsModel)

	if m.result == nil {
		t.Fatal("expected result after d on completed session")
	}
	if m.result.Action != SessionActionDelete {
		t.Errorf("expected SessionActionDelete, got %d", m.result.Action)
	}
	if cmd == nil {
		t.Error("expected quit command after delete")
	}
}

func TestSessionsModel_DeleteIgnoredOnRunning(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.cursor = 2 // running session

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m := model2.(SessionsModel)

	if m.result != nil {
		t.Error("expected no result on d for running session")
	}
}

func TestSessionsModel_Quit(t *testing.T) {
	model := NewSessionsModel(testSessions())

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m := model2.(SessionsModel)

	if !m.quitting {
		t.Error("expected quitting true after q")
	}
	if m.result != nil {
		t.Error("expected nil result on quit")
	}
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestSessionsModel_ViewContent(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.width = 80
	model.height = 24

	view := model.View()

	// Should show session browser title
	if !strings.Contains(view, "Sessions") {
		t.Error("view missing Sessions title")
	}

	// Should show ticket keys
	for _, ticket := range []string{"36", "58", "64", "50"} {
		if !strings.Contains(view, ticket) {
			t.Errorf("view missing ticket %q", ticket)
		}
	}

	// Should show status icons
	if !strings.Contains(view, "✓") {
		t.Error("view missing completed icon ✓")
	}
	if !strings.Contains(view, "●") {
		t.Error("view missing running icon ●")
	}
	if !strings.Contains(view, "✗") {
		t.Error("view missing failed icon ✗")
	}

	// Should show help bar
	if !strings.Contains(view, "navigate") {
		t.Error("view missing help bar")
	}
	if !strings.Contains(view, "view history") {
		t.Error("view missing 'view history' in help bar")
	}

	// Should show cursor on first item
	if !strings.Contains(view, ">") {
		t.Error("view missing cursor '>'")
	}
}

func TestSessionsModel_ViewEmpty(t *testing.T) {
	model := NewSessionsModel(nil)
	model.width = 80

	view := model.View()
	if !strings.Contains(view, "No sessions found") {
		t.Error("empty view should show 'No sessions found'")
	}
}

func TestSessionsModel_EmptyListQuit(t *testing.T) {
	model := NewSessionsModel(nil)

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m := model2.(SessionsModel)

	if !m.quitting {
		t.Error("expected quitting true after q on empty list")
	}
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestSessionsModel_ViewQuitting(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.quitting = true

	view := model.View()
	if view != "" {
		t.Errorf("expected empty view when quitting, got %q", view)
	}
}

func TestSessionStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		icon   string
	}{
		{"completed", "✓"},
		{"running", "●"},
		{"failed", "✗"},
		{"stale", "⏸"},
		{"pending", "○"},
	}
	for _, tc := range tests {
		got := sessionStatusIcon(tc.status)
		if !strings.Contains(got, tc.icon) {
			t.Errorf("sessionStatusIcon(%q) = %q, want to contain %q", tc.status, got, tc.icon)
		}
	}
}

func TestSessionsModel_CostDisplay(t *testing.T) {
	model := NewSessionsModel(testSessions())
	model.width = 80

	view := model.View()
	if !strings.Contains(view, "$0.98") {
		t.Error("view missing cost $0.98")
	}
	if !strings.Contains(view, "$5.58") {
		t.Error("view missing cost $5.58")
	}
}
