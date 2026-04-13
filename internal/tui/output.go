package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

type outputView struct {
	viewport   viewport.Model
	lines      []string
	autoScroll bool
	ready      bool
	width      int
	height     int
}

func newOutputView() outputView {
	return outputView{autoScroll: true}
}

func (v *outputView) setSize(width, height int) {
	v.width = width
	v.height = height
	contentW := width - 4 // border + padding
	contentH := height - 2 // border
	if contentW < 1 {
		contentW = 1
	}
	if contentH < 1 {
		contentH = 1
	}
	if !v.ready {
		v.viewport = viewport.New(contentW, contentH)
		v.ready = true
	} else {
		v.viewport.Width = contentW
		v.viewport.Height = contentH
	}
	v.viewport.SetContent(strings.Join(v.lines, "\n"))
	if v.autoScroll {
		v.viewport.GotoBottom()
	}
}

func (v *outputView) appendLine(line string) {
	v.lines = append(v.lines, line)
	if v.ready {
		v.viewport.SetContent(strings.Join(v.lines, "\n"))
		if v.autoScroll {
			v.viewport.GotoBottom()
		}
	}
}

func (v *outputView) clear() {
	v.lines = nil
	if v.ready {
		v.viewport.SetContent("")
		v.viewport.GotoTop()
	}
	v.autoScroll = true
}

func (v *outputView) scrollUp() {
	if v.ready {
		v.viewport.ScrollUp(1)
		v.autoScroll = false
	}
}

func (v *outputView) scrollDown() {
	if v.ready {
		v.viewport.ScrollDown(1)
		if v.viewport.AtBottom() {
			v.autoScroll = true
		}
	}
}

func (v outputView) View() string {
	if !v.ready {
		return ""
	}
	w := v.width
	if w < 1 {
		return v.viewport.View()
	}
	return styleBorder.
		Width(w - 2).
		Render(lipgloss.NewStyle().Padding(0, 1).Render(v.viewport.View()))
}
