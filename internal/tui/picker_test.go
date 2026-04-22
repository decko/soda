package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testTickets() []TicketInfo {
	return []TicketInfo{
		{Key: "42", Summary: "Add validation to user input", Type: "Feature", Priority: "High", Status: "Open", Labels: []string{"enhancement"}},
		{Key: "58", Summary: "Fix login timeout error", Type: "Bug", Priority: "Critical", Status: "Open", Labels: []string{"bug"}},
		{Key: "64", Summary: "Update documentation for API", Type: "Task", Priority: "Low", Status: "In Progress", Labels: []string{"docs"}},
		{Key: "99", Summary: "Refactor config loading", Type: "Task", Priority: "Medium", Status: "Open", Labels: []string{}},
	}
}

func TestPickerModel_InitialState(t *testing.T) {
	tickets := testTickets()
	model := NewPickerModel(tickets)

	if model.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", model.cursor)
	}
	if model.result != nil {
		t.Error("expected nil result initially")
	}
}

func TestPickerModel_NavigateDown(t *testing.T) {
	model := NewPickerModel(testTickets())

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m := model2.(PickerModel)
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", m.cursor)
	}

	// Navigate to last
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m3, _ := m2.(PickerModel).handleKey(tea.KeyMsg{Type: tea.KeyDown})
	mm := m3.(PickerModel)
	if mm.cursor != 3 {
		t.Errorf("expected cursor at 3, got %d", mm.cursor)
	}

	// Cannot go past last
	m4, _ := mm.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if m4.(PickerModel).cursor != 3 {
		t.Errorf("expected cursor still at 3, got %d", m4.(PickerModel).cursor)
	}
}

func TestPickerModel_NavigateUp(t *testing.T) {
	model := NewPickerModel(testTickets())
	model.cursor = 2

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	m := model2.(PickerModel)
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1 after up, got %d", m.cursor)
	}

	// Navigate to first
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if m2.(PickerModel).cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", m2.(PickerModel).cursor)
	}

	// Cannot go past first
	m3, _ := m2.(PickerModel).handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if m3.(PickerModel).cursor != 0 {
		t.Errorf("expected cursor still at 0, got %d", m3.(PickerModel).cursor)
	}
}

func TestPickerModel_NavigateWithKJ(t *testing.T) {
	model := NewPickerModel(testTickets())

	// j moves down
	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if model2.(PickerModel).cursor != 1 {
		t.Errorf("expected cursor at 1 after j, got %d", model2.(PickerModel).cursor)
	}

	// k moves up
	model3, _ := model2.(PickerModel).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if model3.(PickerModel).cursor != 0 {
		t.Errorf("expected cursor at 0 after k, got %d", model3.(PickerModel).cursor)
	}
}

func TestPickerModel_EnterSelectsTicket(t *testing.T) {
	model := NewPickerModel(testTickets())
	model.cursor = 1

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m := model2.(PickerModel)

	if m.result == nil {
		t.Fatal("expected result after Enter")
	}
	if m.result.Ticket.Key != "58" {
		t.Errorf("expected ticket 58, got %q", m.result.Ticket.Key)
	}
	if m.result.Action != PickerActionRun {
		t.Errorf("expected PickerActionRun, got %d", m.result.Action)
	}
	if cmd == nil {
		t.Error("expected quit command after Enter")
	}
}

func TestPickerModel_Quit(t *testing.T) {
	model := NewPickerModel(testTickets())

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m := model2.(PickerModel)

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

func TestPickerModel_EscQuits(t *testing.T) {
	model := NewPickerModel(testTickets())

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m := model2.(PickerModel)

	if !m.quitting {
		t.Error("expected quitting true after esc")
	}
	if m.result != nil {
		t.Error("expected nil result on esc")
	}
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestPickerModel_CtrlCQuits(t *testing.T) {
	model := NewPickerModel(testTickets())

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	m := model2.(PickerModel)

	if !m.quitting {
		t.Error("expected quitting true after ctrl+c")
	}
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestPickerModel_ViewContent(t *testing.T) {
	model := NewPickerModel(testTickets())
	model.width = 80
	model.height = 24

	view := model.View()

	// Should show title
	if !strings.Contains(view, "Pick a ticket") {
		t.Error("view missing title")
	}

	// Should show ticket keys
	for _, key := range []string{"42", "58", "64", "99"} {
		if !strings.Contains(view, key) {
			t.Errorf("view missing ticket %q", key)
		}
	}

	// Should show summaries
	if !strings.Contains(view, "Add validation") {
		t.Error("view missing ticket summary")
	}

	// Should show cursor on first item
	if !strings.Contains(view, ">") {
		t.Error("view missing cursor '>'")
	}

	// Should show help bar
	if !strings.Contains(view, "select") {
		t.Error("view missing 'select' in help bar")
	}
	if !strings.Contains(view, "quit") {
		t.Error("view missing 'quit' in help bar")
	}
}

func TestPickerModel_ViewEmpty(t *testing.T) {
	model := NewPickerModel(nil)
	model.width = 80

	view := model.View()
	if !strings.Contains(view, "No tickets found") {
		t.Error("empty view should show 'No tickets found'")
	}
}

func TestPickerModel_ViewQuitting(t *testing.T) {
	model := NewPickerModel(testTickets())
	model.quitting = true

	view := model.View()
	if view != "" {
		t.Errorf("expected empty view when quitting, got %q", view)
	}
}

func TestPickerModel_EmptyListQuit(t *testing.T) {
	model := NewPickerModel(nil)

	model2, cmd := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m := model2.(PickerModel)

	if !m.quitting {
		t.Error("expected quitting true after q on empty list")
	}
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestPickerModel_PriorityDisplay(t *testing.T) {
	model := NewPickerModel(testTickets())
	model.width = 80

	view := model.View()
	if !strings.Contains(view, "High") {
		t.Error("view missing priority 'High'")
	}
}

func TestPickerModel_WindowSize(t *testing.T) {
	model := NewPickerModel(testTickets())

	updatedModel, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m := updatedModel.(PickerModel)

	if m.width != 120 {
		t.Errorf("expected width 120, got %d", m.width)
	}
	if m.height != 40 {
		t.Errorf("expected height 40, got %d", m.height)
	}
}

func TestPickerModel_ResultReturnsSelection(t *testing.T) {
	model := NewPickerModel(testTickets())
	model.cursor = 2

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m := model2.(PickerModel)

	result := m.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Ticket.Key != "64" {
		t.Errorf("expected ticket key '64', got %q", result.Ticket.Key)
	}
	if result.Ticket.Summary != "Update documentation for API" {
		t.Errorf("expected summary, got %q", result.Ticket.Summary)
	}
}

func TestPickerModel_ResultNilOnQuit(t *testing.T) {
	model := NewPickerModel(testTickets())

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m := model2.(PickerModel)

	if m.Result() != nil {
		t.Error("expected nil result after quit")
	}
}

func TestPickerModel_EnterOnFirstTicket(t *testing.T) {
	model := NewPickerModel(testTickets())

	model2, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m := model2.(PickerModel)

	result := m.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Ticket.Key != "42" {
		t.Errorf("expected ticket key '42', got %q", result.Ticket.Key)
	}
	if result.Action != PickerActionRun {
		t.Errorf("expected PickerActionRun, got %d", result.Action)
	}
}

func TestPickerModel_LabelsShown(t *testing.T) {
	model := NewPickerModel(testTickets())
	model.width = 120

	view := model.View()
	if !strings.Contains(view, "enhancement") {
		t.Error("view missing label 'enhancement'")
	}
}
