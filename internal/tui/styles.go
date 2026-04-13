package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorGreen  = lipgloss.Color("2")
	colorYellow = lipgloss.Color("3")
	colorRed    = lipgloss.Color("1")
	colorBlue   = lipgloss.Color("4")
	colorDim    = lipgloss.Color("8")
	colorWhite  = lipgloss.Color("15")

	styleCompleted = lipgloss.NewStyle().Foreground(colorGreen)
	styleRunning   = lipgloss.NewStyle().Foreground(colorYellow)
	styleFailed    = lipgloss.NewStyle().Foreground(colorRed)
	styleRetrying  = lipgloss.NewStyle().Foreground(colorYellow)
	stylePaused    = lipgloss.NewStyle().Foreground(colorBlue)
	stylePending   = lipgloss.NewStyle().Foreground(colorDim)

	styleHeader = lipgloss.NewStyle().Bold(true)

	styleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDim)

	styleKey   = lipgloss.NewStyle().Foreground(colorYellow).Bold(true)
	styleFlash = lipgloss.NewStyle().Foreground(colorDim).Italic(true)

	styleStatsLabel = lipgloss.NewStyle().Foreground(colorDim)
	styleStatsValue = lipgloss.NewStyle().Foreground(colorWhite).Bold(true)
)
