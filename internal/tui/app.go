package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/ticket"
)

// Model is the top-level bubbletea model for the TUI.
type Model struct {
	ticket   ticketView
	pipeline pipelineView
	output   outputView
	stats    statsView
	keys     keysView

	phases      []string
	events      <-chan pipeline.Event
	pauseSignal chan<- bool
	buffered    []pipeline.Event
	detailMode  bool
	paused      bool
	width       int
	height      int
}

// New creates a new TUI model. The pauseSignal channel receives true when
// the user pauses and false when they resume, allowing the caller to
// suspend the underlying agent/process. It may be nil if pause signaling
// is not needed.
func New(t ticket.Ticket, phases []string, events <-chan pipeline.Event, pauseSignal chan<- bool) Model {
	return Model{
		ticket:      newTicketView(t),
		pipeline:    newPipelineView(phases),
		output:      newOutputView(),
		stats:       newStatsView(),
		keys:        newKeysView(),
		phases:      phases,
		events:      events,
		pauseSignal: pauseSignal,
	}
}

type tickMsg time.Time

type eventMsg pipeline.Event

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.tickCmd(),
		m.pollEvents(),
	)
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) pollEvents() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.events
		if !ok {
			return eventMsg(pipeline.Event{Kind: pipeline.EventEngineCompleted})
		}
		return eventMsg(ev)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil

	case tickMsg:
		m.pipeline.tick()
		m.stats.tick()
		return m, m.tickCmd()

	case clearFlashMsg:
		m.keys.flash = ""
		return m, nil

	case eventMsg:
		ev := pipeline.Event(msg)
		if m.paused {
			m.buffered = append(m.buffered, ev)
		} else {
			m.handleEvent(ev)
		}
		return m, m.pollEvents()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m *Model) handleEvent(ev pipeline.Event) {
	switch ev.Kind {
	case pipeline.EventPhaseStarted:
		m.pipeline.setStatus(ev.Phase, pipeline.PhaseRunning)
		m.output.clear()

	case pipeline.EventPhaseCompleted:
		m.pipeline.setStatus(ev.Phase, pipeline.PhaseCompleted)
		if s, ok := ev.Data["summary"].(string); ok {
			m.pipeline.setSummary(ev.Phase, s)
		}
		if d, ok := ev.Data["duration_s"].(float64); ok {
			m.pipeline.setElapsed(ev.Phase, time.Duration(d*float64(time.Second)))
		}
		if c, ok := ev.Data["cost"].(float64); ok {
			m.stats.addCost(c)
		}
		if tin, ok := ev.Data["tokens_in"].(float64); ok {
			tout, _ := ev.Data["tokens_out"].(float64)
			m.stats.addTokens(int(tin), int(tout))
		}

	case pipeline.EventPhaseFailed:
		m.pipeline.setStatus(ev.Phase, pipeline.PhaseFailed)
		if errStr, ok := ev.Data["error"].(string); ok {
			m.output.appendLine("ERROR: " + errStr)
		}

	case pipeline.EventPhaseRetrying:
		m.pipeline.setStatus(ev.Phase, pipeline.PhaseRetrying)

	case pipeline.EventOutputChunk:
		if line, ok := ev.Data["line"].(string); ok {
			m.output.appendLine(line)
		}

	case pipeline.EventBudgetWarning:
		if msg, ok := ev.Data["message"].(string); ok {
			m.stats.warning = msg
		}

	case pipeline.EventCheckpointPause:
		m.paused = true
		m.keys.paused = true
		m.keys.flash = "press Enter to continue"

	case pipeline.EventEngineCompleted:
		m.keys.flash = "pipeline complete"
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "d":
		m.detailMode = !m.detailMode
		m.layout()
		return m, nil

	case "p":
		m.paused = !m.paused
		m.keys.paused = m.paused
		m.sendPauseSignal(m.paused)
		if m.paused {
			for _, name := range m.phases {
				pi := m.pipeline.info[name]
				if pi.status == pipeline.PhaseRunning {
					pi.status = pipeline.PhasePaused
				}
			}
		} else {
			for _, name := range m.phases {
				pi := m.pipeline.info[name]
				if pi.status == pipeline.PhasePaused {
					pi.status = pipeline.PhaseRunning
				}
			}
			m.flushBuffered()
		}
		return m, nil

	case "s":
		m.keys.flash = "steer: not yet implemented"
		return m, clearFlashAfter()

	case "r":
		m.keys.flash = "retry: not yet implemented"
		return m, clearFlashAfter()

	case "enter":
		if m.paused {
			m.paused = false
			m.keys.paused = false
			m.keys.flash = ""
			m.sendPauseSignal(false)
			m.flushBuffered()
		}
		return m, nil

	case "up", "k":
		m.output.scrollUp()
		return m, nil

	case "down", "j":
		m.output.scrollDown()
		return m, nil
	}

	return m, nil
}

func (m *Model) sendPauseSignal(paused bool) {
	if m.pauseSignal != nil {
		select {
		case m.pauseSignal <- paused:
		default:
			// Drop signal if buffer is full — engine will catch up.
		}
	}
}

func (m *Model) flushBuffered() {
	for _, ev := range m.buffered {
		m.handleEvent(ev)
	}
	m.buffered = nil
}

func (m *Model) layout() {
	w := m.width

	m.ticket.width = w
	m.pipeline.width = w
	m.stats.width = w
	m.keys.width = w

	// Calculate output viewport height: total height minus other components
	// ticket ~5 lines + border, pipeline ~8 lines + border, stats 3 lines, keys 1 line, gaps
	fixedHeight := 5 + len(m.phases) + 2 + 3 + 1 + 4 // rough estimate of non-output lines
	if m.detailMode {
		fixedHeight = 1 // just the keys bar
	}
	outputH := m.height - fixedHeight
	if outputH < 3 {
		outputH = 3
	}
	m.output.width = w
	m.output.setSize(w, outputH)
}

func (m Model) View() string {
	if m.width == 0 {
		return "initializing..."
	}

	if m.detailMode {
		return m.output.View() + "\n" + m.keys.View()
	}

	return m.ticket.View() + "\n" +
		m.pipeline.View() + "\n" +
		m.output.View() + "\n" +
		m.stats.View() + "\n" +
		m.keys.View()
}
