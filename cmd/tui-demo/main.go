package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/ticket"
	"github.com/decko/soda/internal/tui"
)

func main() {
	events := make(chan pipeline.Event, 100)
	pauseSignal := make(chan bool, 1)

	t := ticket.Ticket{
		Key:      "PROJ-42",
		Summary:  "Add validation to user input",
		Type:     "Task",
		Priority: "Medium",
		AcceptanceCriteria: []string{
			"Input validated before processing",
			"Error messages returned for invalid input",
		},
	}
	phases := []string{"triage", "plan", "implement", "verify", "submit", "monitor"}

	gate := newPauseGate(pauseSignal)
	go simulatePhases(events, phases, gate)

	model := tui.New(t, phases, events, pauseSignal)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// pauseGate blocks callers of wait() while paused.
type pauseGate struct {
	mu     sync.Mutex
	cond   *sync.Cond
	paused bool
}

func newPauseGate(signal <-chan bool) *pauseGate {
	g := &pauseGate{}
	g.cond = sync.NewCond(&g.mu)
	go func() {
		for p := range signal {
			g.mu.Lock()
			g.paused = p
			if !p {
				g.cond.Broadcast()
			}
			g.mu.Unlock()
		}
	}()
	return g
}

// wait blocks until the gate is unpaused.
func (g *pauseGate) wait() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.paused {
		g.cond.Wait()
	}
}

func simulatePhases(ch chan<- pipeline.Event, phases []string, gate *pauseGate) {
	defer close(ch)

	type phaseScenario struct {
		duration  time.Duration
		outputs   []string
		summary   string
		cost      float64
		tokensIn  int
		tokensOut int
	}

	scenarios := map[string]phaseScenario{
		"triage": {
			duration: 3 * time.Second,
			outputs: []string{
				"Analyzing ticket PROJ-42...",
				"Identified scope: input validation",
				"Classification: small, my-service",
			},
			summary:  "small, my-service",
			cost:     0.12,
			tokensIn: 3200, tokensOut: 850,
		},
		"plan": {
			duration: 5 * time.Second,
			outputs: []string{
				"Reading existing codebase...",
				"Found 2 relevant files",
				"Creating implementation plan...",
				"Plan: 3 tasks across 2 files",
			},
			summary:  "3 tasks, 2 files",
			cost:     0.45,
			tokensIn: 8500, tokensOut: 2100,
		},
		"implement": {
			duration: 8 * time.Second,
			outputs: []string{
				"Task 1/3: Adding validation function...",
				"Writing validate_input.py...",
				"Task 2/3: Adding error messages...",
				"Writing test_validation.py...",
				"Added test_invalid_email",
				"Added test_empty_input",
				"Task 3/3: Wiring up handler...",
				"Running pytest...",
				"All tests passing",
			},
			summary:  "task 3/3",
			cost:     1.23,
			tokensIn: 22000, tokensOut: 6800,
		},
		"verify": {
			duration: 4 * time.Second,
			outputs: []string{
				"Running full test suite...",
				"pytest: 47 passed, 0 failed",
				"Lint: no issues found",
				"Coverage: 94%",
			},
			summary:  "47 passed",
			cost:     0.34,
			tokensIn: 7500, tokensOut: 1800,
		},
		"submit": {
			duration: 3 * time.Second,
			outputs: []string{
				"Creating branch feat/add-validation...",
				"Committing 3 files...",
				"Opening pull request #123...",
				"PR created successfully",
			},
			summary:  "PR #123",
			cost:     0.15,
			tokensIn: 2800, tokensOut: 450,
		},
		"monitor": {
			duration: 2 * time.Second,
			outputs: []string{
				"Watching CI pipeline...",
				"CI: all checks passed",
			},
			summary:  "CI passed",
			cost:     0.05,
			tokensIn: 1200, tokensOut: 100,
		},
	}

	time.Sleep(500 * time.Millisecond)

	for _, name := range phases {
		gate.wait()
		sc := scenarios[name]

		emit(ch, pipeline.Event{
			Timestamp: time.Now(),
			Phase:     name,
			Kind:      pipeline.EventPhaseStarted,
		})

		interval := sc.duration / time.Duration(len(sc.outputs)+1)
		for _, line := range sc.outputs {
			gate.wait()
			time.Sleep(interval)
			emit(ch, pipeline.Event{
				Timestamp: time.Now(),
				Phase:     name,
				Kind:      pipeline.EventOutputChunk,
				Data:      map[string]any{"line": line},
			})
		}

		gate.wait()
		time.Sleep(interval)
		emit(ch, pipeline.Event{
			Timestamp: time.Now(),
			Phase:     name,
			Kind:      pipeline.EventPhaseCompleted,
			Data: map[string]any{
				"summary":    sc.summary,
				"duration_ms": sc.duration.Milliseconds(),
				"cost":       sc.cost,
				"tokens_in":  float64(sc.tokensIn),
				"tokens_out": float64(sc.tokensOut),
			},
		})
	}

	time.Sleep(500 * time.Millisecond)
	emit(ch, pipeline.Event{
		Timestamp: time.Now(),
		Kind:      pipeline.EventEngineCompleted,
		Data:      map[string]any{"message": "pipeline complete"},
	})
}

func emit(ch chan<- pipeline.Event, ev pipeline.Event) {
	ch <- ev
}
