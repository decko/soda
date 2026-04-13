package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/decko/soda/internal/pipeline"
)

type phaseInfo struct {
	status    pipeline.PhaseStatus
	startedAt time.Time
	elapsed   time.Duration
	summary   string
}

type pipelineView struct {
	phases []string
	info   map[string]*phaseInfo
	width  int
}

func newPipelineView(phases []string) pipelineView {
	info := make(map[string]*phaseInfo, len(phases))
	for _, p := range phases {
		info[p] = &phaseInfo{status: pipeline.PhasePending}
	}
	return pipelineView{phases: phases, info: info}
}

func (v *pipelineView) setStatus(phase string, status pipeline.PhaseStatus) {
	pi, ok := v.info[phase]
	if !ok {
		return
	}
	pi.status = status
	if status == pipeline.PhaseRunning {
		pi.startedAt = time.Now()
	}
}

func (v *pipelineView) setElapsed(phase string, d time.Duration) {
	if pi, ok := v.info[phase]; ok {
		pi.elapsed = d
	}
}

func (v *pipelineView) setSummary(phase string, s string) {
	if pi, ok := v.info[phase]; ok {
		pi.summary = s
	}
}

func (v *pipelineView) tick() {
	for _, p := range v.phases {
		pi := v.info[p]
		if pi.status == pipeline.PhaseRunning && !pi.startedAt.IsZero() {
			pi.elapsed = time.Since(pi.startedAt)
		}
	}
}

func (v pipelineView) View() string {
	var lines []string
	for _, name := range v.phases {
		pi := v.info[name]
		lines = append(lines, renderPhase(name, pi))
	}
	content := strings.Join(lines, "\n")
	w := v.width
	if w < 1 {
		return content
	}
	return styleBorder.
		Width(w - 2).
		Render(lipgloss.NewStyle().Padding(0, 1).Render(content))
}

func renderPhase(name string, pi *phaseInfo) string {
	var icon string
	var style lipgloss.Style

	switch pi.status {
	case pipeline.PhaseCompleted:
		icon = "✓"
		style = styleCompleted
	case pipeline.PhaseRunning:
		icon = "●"
		style = styleRunning
	case pipeline.PhaseFailed:
		icon = "✗"
		style = styleFailed
	case pipeline.PhaseRetrying:
		icon = "↻"
		style = styleRetrying
	case pipeline.PhasePaused:
		icon = "⏸"
		style = stylePaused
	default:
		icon = "○"
		style = stylePending
	}

	elapsed := ""
	if pi.elapsed > 0 {
		elapsed = " " + formatDuration(pi.elapsed)
	}

	summary := ""
	if pi.summary != "" {
		summary = "  " + stylePending.Render(pi.summary)
	}

	return style.Render(fmt.Sprintf("%s %-12s", icon, name)) + stylePending.Render(elapsed) + summary
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}
