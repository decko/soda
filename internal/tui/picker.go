package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TicketInfo holds the data needed to render a single ticket row
// in the TUI ticket picker.
type TicketInfo struct {
	Key      string
	Summary  string
	Type     string
	Priority string
	Status   string
	Labels   []string
}

// PickerAction represents an action the user selected on a ticket.
type PickerAction int

const (
	// PickerActionNone means no action was taken.
	PickerActionNone PickerAction = iota
	// PickerActionRun means the user selected a ticket to run.
	PickerActionRun
)

// PickerResult is returned when the user selects a ticket.
type PickerResult struct {
	Ticket TicketInfo
	Action PickerAction
}

// PickerModel is a bubbletea model for browsing and selecting tickets.
type PickerModel struct {
	tickets  []TicketInfo
	cursor   int
	width    int
	height   int
	result   *PickerResult
	quitting bool
}

// NewPickerModel creates a new ticket picker model.
func NewPickerModel(tickets []TicketInfo) PickerModel {
	return PickerModel{
		tickets: tickets,
	}
}

// Result returns the selected ticket action, or nil if the user quit.
func (m PickerModel) Result() *PickerResult {
	return m.result
}

// Init implements tea.Model.
func (m PickerModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m PickerModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.tickets) == 0 {
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c", "esc":
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case "down", "j":
		if m.cursor < len(m.tickets)-1 {
			m.cursor++
		}
		return m, nil

	case "pgup":
		pageSize := m.pageSize()
		if m.cursor > pageSize {
			m.cursor -= pageSize
		} else {
			m.cursor = 0
		}
		return m, nil

	case "pgdown":
		pageSize := m.pageSize()
		last := len(m.tickets) - 1
		if m.cursor+pageSize < last {
			m.cursor += pageSize
		} else {
			m.cursor = last
		}
		return m, nil

	case "enter":
		ticket := m.tickets[m.cursor]
		m.result = &PickerResult{
			Ticket: ticket,
			Action: PickerActionRun,
		}
		return m, tea.Quit
	}

	return m, nil
}

// View implements tea.Model.
func (m PickerModel) View() string {
	if m.quitting {
		return ""
	}

	if len(m.tickets) == 0 {
		return renderPickerFrame("No tickets found.\n\n"+m.pickerHelpBar(), m.width)
	}

	var lines []string
	for idx, ticket := range m.tickets {
		lines = append(lines, renderTicketRow(ticket, idx == m.cursor))
	}

	content := strings.Join(lines, "\n")
	content += "\n\n" + m.pickerHelpBar()

	return renderPickerFrame(content, m.width)
}

func renderTicketRow(ticket TicketInfo, selected bool) string {
	cursor := "  "
	if selected {
		cursor = "> "
	}

	key := padRight(ticket.Key, 10)
	summary := ticket.Summary
	summaryRunes := []rune(summary)
	if len(summaryRunes) > 40 {
		summary = string(summaryRunes[:37]) + "..."
	}
	summary = padRight(summary, 40)

	priority := padRight(ticket.Priority, 10)

	labels := ""
	if len(ticket.Labels) > 0 {
		labels = stylePending.Render("[" + strings.Join(ticket.Labels, ", ") + "]")
	}

	line := stylePending.Render(key) + "  " +
		stylePending.Render(summary) + "  " +
		ticketPriorityStyle(ticket.Priority).Render(priority)

	if labels != "" {
		line += "  " + labels
	}

	if selected {
		return styleRunning.Render(cursor) + line
	}
	return stylePending.Render(cursor) + line
}

func ticketPriorityStyle(priority string) lipgloss.Style {
	switch strings.ToLower(priority) {
	case "critical", "highest":
		return styleFailed
	case "high":
		return styleRunning
	case "medium":
		return stylePending
	case "low", "lowest":
		return stylePending
	default:
		return stylePending
	}
}

// pageSize returns the number of rows to skip on pg up/pg down.
func (m PickerModel) pageSize() int {
	if m.height > 6 {
		return m.height - 6
	}
	return 10
}

func (m PickerModel) pickerHelpBar() string {
	bindings := []struct{ key, label string }{
		{"↑/↓", "navigate"},
		{"Enter", "select"},
		{"q", "quit"},
	}
	var parts []string
	for _, b := range bindings {
		parts = append(parts, fmt.Sprintf("%s %s",
			styleKey.Render(b.key),
			stylePending.Render(b.label),
		))
	}
	return strings.Join(parts, "  ")
}

func renderPickerFrame(content string, width int) string {
	title := styleHeader.Render("Pick a ticket")
	body := title + "\n\n" + content

	if width < 1 {
		return body
	}

	frameWidth := width - 2
	if frameWidth < 1 {
		frameWidth = 1
	}
	return styleBorder.
		Width(frameWidth).
		Render(lipgloss.NewStyle().Padding(0, 1).Render(body))
}
