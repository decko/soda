package progress

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Spinner frames for the animated progress indicator.
var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ANSI escape codes for terminal colors.
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorDim    = "\033[2m"
	clearLine   = "\033[K"
)

// Progress displays live pipeline progress in the terminal.
// It shows an animated spinner while a phase is running and prints
// a summary line when each phase completes or fails.
//
// When isTTY is false (e.g., CI or piped output), the spinner is
// disabled and output is simple line-by-line text.
type Progress struct {
	w     io.Writer
	isTTY bool

	mu sync.Mutex

	// Current phase state
	phase     string
	desc      string
	startTime time.Time
	totalCost float64

	// Spinner goroutine control
	frame     int
	stopCh    chan struct{}
	stoppedCh chan struct{}

	// TickInterval controls the spinner animation speed.
	// Exported for testing; defaults to 100ms.
	TickInterval time.Duration

	// NowFunc returns the current time. Exported for testing.
	NowFunc func() time.Time
}

// New creates a new Progress writer.
// If isTTY is false, the spinner animation is disabled and output is
// line-by-line (suitable for CI/pipe).
func New(w io.Writer, isTTY bool) *Progress {
	return &Progress{
		w:            w,
		isTTY:        isTTY,
		TickInterval: 100 * time.Millisecond,
		NowFunc:      time.Now,
	}
}

// PhaseStarted begins showing a spinner for the given phase.
func (p *Progress) PhaseStarted(phase string) {
	desc := phaseDescription(phase)

	p.mu.Lock()
	p.phase = phase
	p.desc = desc
	p.startTime = p.NowFunc()
	p.frame = 0
	p.mu.Unlock()

	if p.isTTY {
		p.stopCh = make(chan struct{})
		p.stoppedCh = make(chan struct{})
		go p.spin()
	} else {
		fmt.Fprintf(p.w, "%s — %s\n", phase, desc)
	}
}

// PhaseCompleted stops the spinner and prints a completion line.
func (p *Progress) PhaseCompleted(phase, summary string, elapsed time.Duration, cost float64) {
	p.mu.Lock()
	p.totalCost += cost
	totalCost := p.totalCost
	p.mu.Unlock()

	p.stopSpinner()

	summaryPart := ""
	if summary != "" {
		summaryPart = " — " + summary
	}

	if p.isTTY {
		line := fmt.Sprintf("\r%s%s✓%s %-12s%s  %6s  $%.2f",
			clearLine, colorGreen, colorReset, phase, summaryPart,
			formatDuration(elapsed), totalCost)
		fmt.Fprintln(p.w, line)
	} else {
		line := fmt.Sprintf("✓ %-12s%s  %6s  $%.2f",
			phase, summaryPart, formatDuration(elapsed), totalCost)
		fmt.Fprintln(p.w, line)
	}
}

// PhaseFailed stops the spinner and prints a failure line.
func (p *Progress) PhaseFailed(phase, errMsg string, elapsed time.Duration) {
	p.mu.Lock()
	totalCost := p.totalCost
	p.mu.Unlock()

	p.stopSpinner()

	if p.isTTY {
		line := fmt.Sprintf("\r%s%s✗%s %-12s — %s  %6s  $%.2f",
			clearLine, colorRed, colorReset, phase, errMsg,
			formatDuration(elapsed), totalCost)
		fmt.Fprintln(p.w, line)
	} else {
		line := fmt.Sprintf("✗ %-12s — %s  %6s  $%.2f",
			phase, errMsg, formatDuration(elapsed), totalCost)
		fmt.Fprintln(p.w, line)
	}
}

// PhaseSkipped prints a skip indicator.
func (p *Progress) PhaseSkipped(phase string) {
	fmt.Fprintf(p.w, "⏭ %s  skipped\n", phase)
}

// PhaseRetrying updates the spinner description for a retry.
func (p *Progress) PhaseRetrying(phase, category string, attempt int) {
	p.mu.Lock()
	p.desc = fmt.Sprintf("retrying (%s, attempt %d)...", category, attempt)
	p.mu.Unlock()
}

// BudgetWarning prints a budget warning line.
func (p *Progress) BudgetWarning(total, limit float64) {
	if p.isTTY {
		// Temporarily clear the spinner line to print the warning
		fmt.Fprintf(p.w, "\r%s%s⚠ Budget warning: $%.2f / $%.2f%s\n",
			clearLine, colorYellow, total, limit, colorReset)
	} else {
		fmt.Fprintf(p.w, "⚠ Budget warning: $%.2f / $%.2f\n", total, limit)
	}
}

// Message prints a plain message line, clearing the spinner if active.
func (p *Progress) Message(msg string) {
	if p.isTTY {
		fmt.Fprintf(p.w, "\r%s%s\n", clearLine, msg)
	} else {
		fmt.Fprintln(p.w, msg)
	}
}

// spin runs the animated spinner in a goroutine.
func (p *Progress) spin() {
	defer close(p.stoppedCh)

	ticker := time.NewTicker(p.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.mu.Lock()
			frame := frames[p.frame%len(frames)]
			phase := p.phase
			desc := p.desc
			elapsed := p.NowFunc().Sub(p.startTime)
			totalCost := p.totalCost
			p.frame++
			p.mu.Unlock()

			line := fmt.Sprintf("\r%s%s%s%s %-12s — %s  %6s  $%.2f",
				clearLine, colorYellow, frame, colorReset, phase, desc,
				formatDuration(elapsed), totalCost)
			fmt.Fprint(p.w, line)
		}
	}
}

// stopSpinner stops the spinner goroutine if running.
func (p *Progress) stopSpinner() {
	if !p.isTTY {
		return
	}
	p.mu.Lock()
	stopCh := p.stopCh
	stoppedCh := p.stoppedCh
	p.stopCh = nil
	p.stoppedCh = nil
	p.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
		<-stoppedCh
	}
}

// formatDuration formats a duration as "Xs" or "Xm0Ys".
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}
