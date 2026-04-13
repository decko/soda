package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

type statsView struct {
	cost      float64
	tokensIn  int
	tokensOut int
	startedAt time.Time
	elapsed   time.Duration
	warning   string
	width     int
}

func newStatsView() statsView {
	return statsView{startedAt: time.Now()}
}

func (v *statsView) addCost(c float64) {
	v.cost += c
}

func (v *statsView) addTokens(in, out int) {
	v.tokensIn += in
	v.tokensOut += out
}

func (v *statsView) tick() {
	v.elapsed = time.Since(v.startedAt)
}

func (v statsView) View() string {
	cost := statsItem("Cost", fmt.Sprintf("$%.2f", v.cost))
	tokens := statsItem("Tokens", fmt.Sprintf("%s/%s", fmtCount(v.tokensIn), fmtCount(v.tokensOut)))
	elapsed := statsItem("Elapsed", formatDuration(v.elapsed))

	bar := fmt.Sprintf("%s  %s  %s", cost, tokens, elapsed)
	if v.warning != "" {
		bar += "  " + styleFailed.Render("⚠ "+v.warning)
	}

	w := v.width
	if w < 1 {
		return bar
	}
	return styleBorder.
		Width(w - 2).
		Render(lipgloss.NewStyle().Padding(0, 1).Render(bar))
}

func statsItem(label, value string) string {
	return styleStatsLabel.Render(label+": ") + styleStatsValue.Render(value)
}

func fmtCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
