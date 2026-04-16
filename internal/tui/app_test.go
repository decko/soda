package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/ticket"
)

func testModel() Model {
	t := ticket.Ticket{
		Key:      "TEST-1",
		Summary:  "Test ticket",
		Type:     "Task",
		Priority: "High",
		AcceptanceCriteria: []string{
			"First criterion",
			"Second criterion",
		},
	}
	phases := []string{"triage", "plan", "implement"}
	events := make(chan pipeline.Event, 10)
	return New(t, phases, events, nil)
}

func TestPhaseStartedSetsRunning(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseStarted,
		Phase: "triage",
	})
	if m.pipeline.info["triage"].status != pipeline.PhaseRunning {
		t.Errorf("expected running, got %s", m.pipeline.info["triage"].status)
	}
}

func TestPhaseCompletedSetsDone(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseStarted,
		Phase: "triage",
	})
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseCompleted,
		Phase: "triage",
		Data: map[string]any{
			"summary":    "small, my-service",
			"duration_ms": float64(8000),
			"cost":       0.12,
			"tokens_in":  3200.0,
			"tokens_out": 850.0,
		},
	})
	if m.pipeline.info["triage"].status != pipeline.PhaseCompleted {
		t.Errorf("expected completed, got %s", m.pipeline.info["triage"].status)
	}
	if m.pipeline.info["triage"].summary != "small, my-service" {
		t.Errorf("expected summary 'small, my-service', got %q", m.pipeline.info["triage"].summary)
	}
	if m.stats.cost != 0.12 {
		t.Errorf("expected cost 0.12, got %f", m.stats.cost)
	}
	if m.stats.tokensIn != 3200 {
		t.Errorf("expected tokensIn 3200, got %d", m.stats.tokensIn)
	}
}

func TestPhaseFailedSetsStatus(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseFailed,
		Phase: "plan",
		Data:  map[string]any{"error": "something broke"},
	})
	if m.pipeline.info["plan"].status != pipeline.PhaseFailed {
		t.Errorf("expected failed, got %s", m.pipeline.info["plan"].status)
	}
}

func TestOutputChunkAppendsLine(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventOutputChunk,
		Phase: "implement",
		Data:  map[string]any{"line": "hello world"},
	})
	if len(m.output.lines) != 1 || m.output.lines[0] != "hello world" {
		t.Errorf("expected 1 line 'hello world', got %v", m.output.lines)
	}
}

func TestPhaseStartedClearsOutput(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind: pipeline.EventOutputChunk,
		Data: map[string]any{"line": "old line"},
	})
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseStarted,
		Phase: "plan",
	})
	if len(m.output.lines) != 0 {
		t.Errorf("expected output cleared on new phase, got %d lines", len(m.output.lines))
	}
}

func TestBudgetWarning(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind: pipeline.EventBudgetWarning,
		Data: map[string]any{"message": "80% budget used"},
	})
	if m.stats.warning != "80% budget used" {
		t.Errorf("expected budget warning, got %q", m.stats.warning)
	}
}

func TestKeyQuit(t *testing.T) {
	m := testModel()
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestKeyDetailToggle(t *testing.T) {
	m := testModel()
	m.width = 80
	m.height = 24
	m.layout()
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !m2.(Model).detailMode {
		t.Error("expected detail mode on")
	}
	m3, _ := m2.(Model).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if m3.(Model).detailMode {
		t.Error("expected detail mode off")
	}
}

func TestKeyPauseToggle(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{Kind: pipeline.EventPhaseStarted, Phase: "triage"})
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	mm := m2.(Model)
	if !mm.paused {
		t.Error("expected paused")
	}
	if mm.pipeline.info["triage"].status != pipeline.PhasePaused {
		t.Errorf("expected paused status, got %s", mm.pipeline.info["triage"].status)
	}
	m3, _ := m2.(Model).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	mm3 := m3.(Model)
	if mm3.paused {
		t.Error("expected unpaused")
	}
	if mm3.pipeline.info["triage"].status != pipeline.PhaseRunning {
		t.Errorf("expected running status after unpause, got %s", mm3.pipeline.info["triage"].status)
	}
}

func TestKeySteerNotImplemented(t *testing.T) {
	m := testModel()
	m2, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if m2.(Model).keys.flash != "steer: not yet implemented" {
		t.Errorf("expected flash message, got %q", m2.(Model).keys.flash)
	}
	if cmd == nil {
		t.Error("expected clearFlash command")
	}
}

func TestKeyRetryNotImplemented(t *testing.T) {
	m := testModel()
	m2, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m2.(Model).keys.flash != "retry: not yet implemented" {
		t.Errorf("expected flash message, got %q", m2.(Model).keys.flash)
	}
	if cmd == nil {
		t.Error("expected clearFlash command")
	}
}

func TestCostAccumulation(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseCompleted,
		Phase: "triage",
		Data:  map[string]any{"cost": 0.50},
	})
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseCompleted,
		Phase: "plan",
		Data:  map[string]any{"cost": 1.25},
	})
	if m.stats.cost != 1.75 {
		t.Errorf("expected accumulated cost 1.75, got %f", m.stats.cost)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{3 * time.Second, "3s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},
		{127 * time.Second, "2m07s"},
	}
	for _, tc := range tests {
		got := formatDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestTicketViewContent(t *testing.T) {
	m := testModel()
	v := m.ticket.View()
	for _, want := range []string{
		"TEST-1",
		"Test ticket",
		"Task",
		"High",
		"First criterion",
		"Second criterion",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("ticket view missing %q", want)
		}
	}
}

func TestPipelineViewPhaseIcons(t *testing.T) {
	m := testModel()

	// All pending initially — should show ○ for each phase
	v := m.pipeline.View()
	for _, name := range []string{"triage", "plan", "implement"} {
		if !strings.Contains(v, name) {
			t.Errorf("pipeline view missing phase %q", name)
		}
	}
	if count := strings.Count(v, "○"); count != 3 {
		t.Errorf("expected 3 pending icons, got %d", count)
	}

	// Start triage — should show ● for running
	m.handleEvent(pipeline.Event{Kind: pipeline.EventPhaseStarted, Phase: "triage"})
	v = m.pipeline.View()
	if !strings.Contains(v, "●") {
		t.Error("pipeline view missing running icon ●")
	}

	// Complete triage — should show ✓
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseCompleted,
		Phase: "triage",
		Data:  map[string]any{"summary": "small, my-service", "duration_ms": float64(8000)},
	})
	v = m.pipeline.View()
	if !strings.Contains(v, "✓") {
		t.Error("pipeline view missing completed icon ✓")
	}
	if !strings.Contains(v, "small, my-service") {
		t.Error("pipeline view missing summary text")
	}

	// Fail plan — should show ✗
	m.handleEvent(pipeline.Event{Kind: pipeline.EventPhaseFailed, Phase: "plan"})
	v = m.pipeline.View()
	if !strings.Contains(v, "✗") {
		t.Error("pipeline view missing failed icon ✗")
	}
}

func TestPipelineViewRetryingIcon(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{Kind: pipeline.EventPhaseRetrying, Phase: "triage"})
	v := m.pipeline.View()
	if !strings.Contains(v, "↻") {
		t.Error("pipeline view missing retrying icon ↻")
	}
}

func TestPipelineViewPausedIcon(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{Kind: pipeline.EventPhaseStarted, Phase: "triage"})
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	v := m2.(Model).pipeline.View()
	if !strings.Contains(v, "⏸") {
		t.Error("pipeline view missing paused icon ⏸")
	}
}

func TestStatsViewContent(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind:  pipeline.EventPhaseCompleted,
		Phase: "triage",
		Data: map[string]any{
			"cost":       0.50,
			"tokens_in":  3200.0,
			"tokens_out": 850.0,
		},
	})
	m.stats.tick()
	v := m.stats.View()
	if !strings.Contains(v, "$0.50") {
		t.Errorf("stats view missing cost, got: %s", v)
	}
	if !strings.Contains(v, "3.2k") {
		t.Errorf("stats view missing tokens_in, got: %s", v)
	}
	if !strings.Contains(v, "850") {
		t.Errorf("stats view missing tokens_out, got: %s", v)
	}
	if !strings.Contains(v, "Elapsed") {
		t.Errorf("stats view missing elapsed label, got: %s", v)
	}
}

func TestStatsViewBudgetWarning(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{
		Kind: pipeline.EventBudgetWarning,
		Data: map[string]any{"message": "90% budget used"},
	})
	v := m.stats.View()
	if !strings.Contains(v, "90% budget used") {
		t.Errorf("stats view missing budget warning, got: %s", v)
	}
}

func TestKeysViewContent(t *testing.T) {
	m := testModel()
	v := m.keys.View()
	for _, want := range []string{"[p]", "[s]", "[d]", "[r]", "[q]"} {
		if !strings.Contains(v, want) {
			t.Errorf("keys view missing %q", want)
		}
	}
	if !strings.Contains(v, "pause") {
		t.Error("keys view missing 'pause' label")
	}
}

func TestKeysViewShowsResumeWhenPaused(t *testing.T) {
	m := testModel()
	m.keys.paused = true
	v := m.keys.View()
	if !strings.Contains(v, "resume") {
		t.Error("keys view should show 'resume' when paused")
	}
}

func TestFullViewComposition(t *testing.T) {
	m := testModel()
	m.width = 80
	m.height = 40
	m.layout()

	v := m.View()
	// Should contain elements from all panels
	if !strings.Contains(v, "TEST-1") {
		t.Error("full view missing ticket key")
	}
	if !strings.Contains(v, "triage") {
		t.Error("full view missing phase name")
	}
	if !strings.Contains(v, "[q]") {
		t.Error("full view missing keys bar")
	}
}

func TestDetailModeHidesOtherPanels(t *testing.T) {
	m := testModel()
	m.width = 80
	m.height = 40
	m.layout()
	m.detailMode = true

	v := m.View()
	// Should NOT contain ticket info in detail mode
	if strings.Contains(v, "TEST-1") {
		t.Error("detail mode should hide ticket panel")
	}
	// Should still show keys bar
	if !strings.Contains(v, "[q]") {
		t.Error("detail mode should still show keys bar")
	}
}

func TestPauseBuffersEvents(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{Kind: pipeline.EventPhaseStarted, Phase: "triage"})

	// Pause
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	mm := m2.(Model)

	// Simulate receiving events while paused (via Update path)
	ev := pipeline.Event{Kind: pipeline.EventOutputChunk, Phase: "triage", Data: map[string]any{"line": "buffered line"}}
	mm.buffered = append(mm.buffered, ev)

	// Output should NOT have the line yet
	if len(mm.output.lines) != 0 {
		t.Errorf("expected no output lines while paused, got %d", len(mm.output.lines))
	}

	// Unpause — buffered events should be flushed
	m3, _ := mm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	mm3 := m3.(Model)
	if len(mm3.output.lines) != 1 || mm3.output.lines[0] != "buffered line" {
		t.Errorf("expected buffered line after unpause, got %v", mm3.output.lines)
	}
	if len(mm3.buffered) != 0 {
		t.Errorf("expected buffer cleared after unpause, got %d", len(mm3.buffered))
	}
}

func TestCheckpointPause(t *testing.T) {
	m := testModel()
	m.handleEvent(pipeline.Event{Kind: pipeline.EventCheckpointPause})
	if !m.paused {
		t.Error("expected paused after checkpoint")
	}
	if m.keys.flash != "press Enter to continue" {
		t.Errorf("expected checkpoint flash, got %q", m.keys.flash)
	}
	// press Enter to resume
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := m2.(Model)
	if mm.paused {
		t.Error("expected unpaused after Enter")
	}
}

func TestPauseSignalSent(t *testing.T) {
	tk := ticket.Ticket{
		Key:     "TEST-1",
		Summary: "Test ticket",
		Type:    "Task",
	}
	phases := []string{"triage", "plan"}
	events := make(chan pipeline.Event, 10)
	pauseSignal := make(chan bool, 10)
	m := New(tk, phases, events, pauseSignal)

	m.handleEvent(pipeline.Event{Kind: pipeline.EventPhaseStarted, Phase: "triage"})

	// Press p to pause
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	mm := m2.(Model)
	if !mm.paused {
		t.Error("expected paused")
	}

	// Verify pause signal was sent
	select {
	case paused := <-pauseSignal:
		if !paused {
			t.Error("expected true on pause signal")
		}
	default:
		t.Error("no pause signal received")
	}

	// Press p to resume
	m3, _ := mm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	mm3 := m3.(Model)
	if mm3.paused {
		t.Error("expected unpaused")
	}

	// Verify resume signal was sent
	select {
	case paused := <-pauseSignal:
		if paused {
			t.Error("expected false on resume signal")
		}
	default:
		t.Error("no resume signal received")
	}
}

func TestSendPauseSignalNonBlocking(t *testing.T) {
	// Verify that sendPauseSignal does not block when the channel is full.
	ch := make(chan bool) // unbuffered channel with no receiver
	m := &Model{
		pauseSignal: ch,
		pauseG:      &pauseGuard{},
	}

	done := make(chan struct{})
	go func() {
		m.sendPauseSignal(true) // should not block
		close(done)
	}()

	select {
	case <-done:
		// OK — did not block
	case <-time.After(2 * time.Second):
		t.Fatal("sendPauseSignal blocked on full channel")
	}
}

func TestEnterResumeSignalSent(t *testing.T) {
	tk := ticket.Ticket{
		Key:     "TEST-1",
		Summary: "Test ticket",
		Type:    "Task",
	}
	phases := []string{"triage"}
	events := make(chan pipeline.Event, 10)
	pauseSignal := make(chan bool, 10)
	m := New(tk, phases, events, pauseSignal)

	// Simulate checkpoint pause
	m.handleEvent(pipeline.Event{Kind: pipeline.EventCheckpointPause})
	if !m.paused {
		t.Error("expected paused after checkpoint")
	}

	// Drain the channel (checkpoint doesn't send pause signal itself)
	// Press Enter to resume
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := m2.(Model)
	if mm.paused {
		t.Error("expected unpaused after Enter")
	}

	// Verify resume signal was sent
	select {
	case paused := <-pauseSignal:
		if paused {
			t.Error("expected false (resume) signal on Enter")
		}
	default:
		t.Error("no resume signal received on Enter")
	}
}
