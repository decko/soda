package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SessionInfo holds the data needed to render a single session row
// in the TUI session browser.
type SessionInfo struct {
	Ticket    string
	Summary   string
	Status    string
	Cost      float64
	Elapsed   string
	StartedAt time.Time
}

// SessionAction represents an action the user selected on a session.
type SessionAction int

const (
	// SessionActionNone means no action was taken.
	SessionActionNone SessionAction = iota
	// SessionActionView means the user pressed Enter to view history.
	SessionActionView
	// SessionActionResume means the user pressed r to resume.
	SessionActionResume
	// SessionActionDelete means the user pressed d to delete/clean.
	SessionActionDelete
)

// SessionResult is returned when the user selects an action on a session.
type SessionResult struct {
	Ticket string
	Action SessionAction
}

// SessionsModel is a bubbletea model for browsing pipeline sessions.
type SessionsModel struct {
	sessions []SessionInfo
	cursor   int
	width    int
	height   int
	result   *SessionResult
	quitting bool
}

// NewSessionsModel creates a new session browser model.
func NewSessionsModel(sessions []SessionInfo) SessionsModel {
	return SessionsModel{
		sessions: sessions,
	}
}

// Result returns the selected session action, or nil if the user quit.
func (m SessionsModel) Result() *SessionResult {
	return m.result
}

// Init implements tea.Model.
func (m SessionsModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m SessionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

func (m SessionsModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.sessions) == 0 {
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
		if m.cursor < len(m.sessions)-1 {
			m.cursor++
		}
		return m, nil

	case "enter":
		m.result = &SessionResult{
			Ticket: m.sessions[m.cursor].Ticket,
			Action: SessionActionView,
		}
		return m, tea.Quit

	case "r":
		s := m.sessions[m.cursor]
		if s.Status == "failed" || s.Status == "stale" {
			m.result = &SessionResult{
				Ticket: s.Ticket,
				Action: SessionActionResume,
			}
			return m, tea.Quit
		}
		return m, nil

	case "d":
		s := m.sessions[m.cursor]
		if s.Status == "completed" || s.Status == "failed" {
			m.result = &SessionResult{
				Ticket: s.Ticket,
				Action: SessionActionDelete,
			}
			return m, tea.Quit
		}
		return m, nil
	}

	return m, nil
}

// View implements tea.Model.
func (m SessionsModel) View() string {
	if m.quitting {
		return ""
	}

	if len(m.sessions) == 0 {
		return renderSessionsFrame("No sessions found.\n\n"+m.helpBar(), m.width)
	}

	var lines []string
	for idx, session := range m.sessions {
		lines = append(lines, renderSessionRow(session, idx == m.cursor))
	}

	content := strings.Join(lines, "\n")
	content += "\n\n" + m.helpBar()

	return renderSessionsFrame(content, m.width)
}

func renderSessionRow(session SessionInfo, selected bool) string {
	cursor := "  "
	if selected {
		cursor = "> "
	}

	statusIcon := sessionStatusIcon(session.Status)
	cost := fmt.Sprintf("$%.2f", session.Cost)

	// Pad plain text to fixed column widths before applying styles so that
	// ANSI escape sequences don't break alignment.
	ticket := padRight(session.Ticket, 12)
	summary := session.Summary
	summaryRunes := []rune(summary)
	if len(summaryRunes) > 36 {
		summary = string(summaryRunes[:33]) + "..."
	}
	summary = padRight(summary, 36)

	line := stylePending.Render(ticket) + "  " +
		stylePending.Render(summary) + "  " +
		statusIcon + "  " +
		styleStatsValue.Render(cost)

	if selected {
		return styleRunning.Render(cursor) + line
	}
	return stylePending.Render(cursor) + line
}

// padRight pads s with spaces to the given width (measured in runes).
func padRight(s string, width int) string {
	r := []rune(s)
	if len(r) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(r))
}

func sessionStatusIcon(status string) string {
	switch status {
	case "completed":
		return styleCompleted.Render("✓")
	case "running":
		return styleRunning.Render("●")
	case "failed":
		return styleFailed.Render("✗")
	case "stale":
		return styleRetrying.Render("⏸")
	default:
		return stylePending.Render("○")
	}
}

func (m SessionsModel) helpBar() string {
	bindings := []struct{ key, label string }{
		{"↑/↓", "navigate"},
		{"Enter", "view history"},
		{"r", "resume"},
		{"d", "delete"},
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

func renderSessionsFrame(content string, width int) string {
	title := styleHeader.Render("Sessions")
	body := title + "\n\n" + content

	if width < 1 {
		return body
	}

	w := width - 2
	if w < 1 {
		w = 1
	}
	return styleBorder.
		Width(w).
		Render(lipgloss.NewStyle().Padding(0, 1).Render(body))
}
