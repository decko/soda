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
	Key         string
	Summary     string
	Type        string
	Priority    string
	Status      string
	Labels      []string
	Description string
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

// ticketsRefreshedMsg carries the result of a background ticket refresh.
type ticketsRefreshedMsg struct {
	tickets []TicketInfo
	err     error
}

// PickerModel is a bubbletea model for browsing and selecting tickets.
type PickerModel struct {
	tickets     []TicketInfo
	cursor      int
	width       int
	height      int
	result      *PickerResult
	quitting    bool
	filter      string
	filtering   bool
	refreshFunc func() ([]TicketInfo, error)
	refreshErr  string
	loading     bool
}

// NewPickerModel creates a new ticket picker model.
func NewPickerModel(tickets []TicketInfo) PickerModel {
	return PickerModel{
		tickets: tickets,
	}
}

// NewPickerModelWithRefresh creates a picker model that supports refreshing
// the ticket list by pressing 'r'. refreshFn is called in the background.
func NewPickerModelWithRefresh(tickets []TicketInfo, refreshFn func() ([]TicketInfo, error)) PickerModel {
	m := NewPickerModel(tickets)
	m.refreshFunc = refreshFn
	return m
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

	case ticketsRefreshedMsg:
		m.loading = false
		if msg.err != nil {
			m.refreshErr = msg.err.Error()
		} else {
			m.refreshErr = ""
			m.tickets = msg.tickets
			m.cursor = 0
		}
		return m, nil
	}

	return m, nil
}

// filteredTickets returns the subset of tickets whose Key or Summary
// contains the current filter string (case-insensitive substring match).
// When filter is empty all tickets are returned.
func (m PickerModel) filteredTickets() []TicketInfo {
	if m.filter == "" {
		return m.tickets
	}
	lower := strings.ToLower(m.filter)
	var result []TicketInfo
	for _, t := range m.tickets {
		if strings.Contains(strings.ToLower(t.Key), lower) ||
			strings.Contains(strings.ToLower(t.Summary), lower) {
			result = append(result, t)
		}
	}
	return result
}

func (m PickerModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Filter input mode: capture runes for the filter string.
	if m.filtering {
		switch msg.String() {
		case "esc":
			m.filtering = false
			m.filter = ""
			m.cursor = 0
		case "enter":
			m.filtering = false
		case "backspace":
			if len(m.filter) > 0 {
				runes := []rune(m.filter)
				m.filter = string(runes[:len(runes)-1])
			}
			if filtered := m.filteredTickets(); m.cursor >= len(filtered) && len(filtered) > 0 {
				m.cursor = len(filtered) - 1
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.filter += string(msg.Runes)
				if filtered := m.filteredTickets(); m.cursor >= len(filtered) && len(filtered) > 0 {
					m.cursor = len(filtered) - 1
				}
			}
		}
		return m, nil
	}

	if len(m.tickets) == 0 {
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		case "r":
			if m.refreshFunc != nil && !m.loading {
				m.loading = true
				fn := m.refreshFunc
				return m, func() tea.Msg {
					tickets, err := fn()
					return ticketsRefreshedMsg{tickets: tickets, err: err}
				}
			}
		}
		return m, nil
	}

	filtered := m.filteredTickets()

	switch msg.String() {
	case "q", "ctrl+c", "esc":
		m.quitting = true
		return m, tea.Quit

	case "/":
		m.filtering = true
		return m, nil

	case "r":
		if m.refreshFunc != nil && !m.loading {
			m.loading = true
			fn := m.refreshFunc
			return m, func() tea.Msg {
				tickets, err := fn()
				return ticketsRefreshedMsg{tickets: tickets, err: err}
			}
		}
		return m, nil

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case "down", "j":
		if m.cursor < len(filtered)-1 {
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
		last := len(filtered) - 1
		if last < 0 {
			last = 0
		}
		if m.cursor+pageSize < last {
			m.cursor += pageSize
		} else {
			m.cursor = last
		}
		return m, nil

	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		ticket := filtered[m.cursor]
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

	if m.loading {
		return renderPickerFrame(stylePending.Render("Refreshing…")+"\n\n"+m.pickerHelpBar(), m.width)
	}

	if m.refreshErr != "" {
		return renderPickerFrame(styleFailed.Render("Error: "+m.refreshErr)+"\n\n"+m.pickerHelpBar(), m.width)
	}

	if len(m.tickets) == 0 {
		return renderPickerFrame("No tickets found.\n\n"+m.pickerHelpBar(), m.width)
	}

	filtered := m.filteredTickets()

	var filterLine string
	if m.filtering {
		filterLine = styleKey.Render("/") + " " + m.filter + "▋\n\n"
	} else if m.filter != "" {
		filterLine = stylePending.Render("filter: ") + m.filter + "\n\n"
	}

	var listLines []string
	for idx, ticket := range filtered {
		listLines = append(listLines, renderTicketRow(ticket, idx == m.cursor))
	}
	if len(filtered) == 0 {
		listLines = []string{stylePending.Render("No matches.")}
	}
	listBody := strings.Join(listLines, "\n")

	// Preview panel for the highlighted ticket.
	var body string
	if len(filtered) > 0 {
		preview := renderPreviewPanel(filtered[m.cursor])
		body = lipgloss.JoinHorizontal(lipgloss.Top, listBody, "  ", preview)
	} else {
		body = listBody
	}

	content := filterLine + body + "\n\n" + m.pickerHelpBar()
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

// renderPreviewPanel returns a compact detail view for the given ticket,
// shown to the right of the ticket list.
func renderPreviewPanel(t TicketInfo) string {
	lines := []string{
		styleHeader.Render(t.Key),
		"",
		stylePending.Render("Type:     ") + t.Type,
		stylePending.Render("Status:   ") + t.Status,
		stylePending.Render("Priority: ") + t.Priority,
	}
	if len(t.Labels) > 0 {
		lines = append(lines, stylePending.Render("Labels:   ")+strings.Join(t.Labels, ", "))
	}
	if t.Description != "" {
		desc := t.Description
		if runes := []rune(desc); len(runes) > 80 {
			desc = string(runes[:77]) + "..."
		}
		lines = append(lines, "", stylePending.Render("Desc:"), desc)
	}
	return strings.Join(lines, "\n")
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
		{"/", "filter"},
		{"r", "refresh"},
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
