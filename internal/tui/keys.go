package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type keysView struct {
	paused   bool
	readOnly bool
	flash    string
	width    int
}

func newKeysView() keysView {
	return keysView{}
}

func (v keysView) View() string {
	var bindings []struct{ key, label string }
	if v.readOnly {
		bindings = []struct{ key, label string }{
			{"d", "detail"},
			{"q", "quit"},
		}
	} else {
		bindings = []struct{ key, label string }{
			{"p", pauseLabel(v.paused)},
			{"s", "steer"},
			{"d", "detail"},
			{"r", "retry"},
			{"q", "quit"},
		}
	}
	var parts []string
	for _, b := range bindings {
		parts = append(parts, fmt.Sprintf("%s %s",
			styleKey.Render("["+b.key+"]"),
			stylePending.Render(b.label),
		))
	}

	bar := lipgloss.JoinHorizontal(lipgloss.Top, joinWith(parts, "  "))
	if v.readOnly {
		bar = styleFlash.Render("ATTACHED (read-only)") + "  " + bar
	}
	if v.flash != "" {
		bar += "  " + styleFlash.Render(v.flash)
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(bar)
}

func pauseLabel(paused bool) string {
	if paused {
		return "resume"
	}
	return "pause"
}

func joinWith(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}

type clearFlashMsg struct{}

func clearFlashAfter() tea.Cmd {
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg {
		return clearFlashMsg{}
	})
}
