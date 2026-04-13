package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/decko/soda/internal/ticket"
)

type ticketView struct {
	ticket ticket.Ticket
	width  int
}

func newTicketView(t ticket.Ticket) ticketView {
	return ticketView{ticket: t}
}

func (v ticketView) View() string {
	t := v.ticket

	title := styleHeader.Render(fmt.Sprintf("%s: %s", t.Key, t.Summary))

	meta := stylePending.Render(fmt.Sprintf("Type: %s  Priority: %s", t.Type, t.Priority))

	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(meta)

	if len(t.AcceptanceCriteria) > 0 {
		b.WriteString("\n\n")
		b.WriteString(stylePending.Render("AC:"))
		for _, ac := range t.AcceptanceCriteria {
			b.WriteString("\n    ")
			b.WriteString(stylePending.Render("☐ " + ac))
		}
	}

	content := b.String()
	w := v.width
	if w < 1 {
		return content
	}

	return styleBorder.
		Width(w - 2).
		Render(lipgloss.NewStyle().Padding(0, 1).Render(content))
}
