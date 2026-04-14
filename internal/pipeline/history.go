package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/decko/soda/internal/progress"
)

// PhaseGeneration represents a single generation (attempt) of a phase.
type PhaseGeneration struct {
	Phase      string
	Generation int
	Status     PhaseStatus
	Cost       float64
	DurationMs int64
	Error      string
	Details    string // one-line summary from result JSON
	Superseded bool   // true if a later generation exists
}

// History holds the reconstructed multi-generation history for a ticket.
type History struct {
	Entries        []PhaseGeneration
	SupersededCost float64
}

// BuildHistory reconstructs multi-generation phase history from a sequence
// of events. It reads archived result files (e.g. verify.json.1) from
// stateDir to populate the Details field via progress.PhaseSummary.
//
// The events are processed in order. Each phase_started event begins a new
// generation entry. phase_completed and phase_failed events finalize it.
// When a phase has multiple generations, earlier ones are marked Superseded.
func BuildHistory(events []Event, stateDir string) History {
	var h History

	// Track the index of the current (latest started, not yet finalized) entry
	// per phase, and a list of all entry indices per phase for superseded marking.
	currentIdx := map[string]int{} // phase → index in h.Entries
	allIdx := map[string][]int{}   // phase → all indices

	for _, ev := range events {
		switch ev.Kind {
		case EventPhaseStarted:
			gen := 1
			if v, ok := ev.Data["generation"]; ok {
				gen = toInt(v)
			}

			// Mark any previous entry for this phase as superseded.
			if indices, ok := allIdx[ev.Phase]; ok {
				for _, idx := range indices {
					h.Entries[idx].Superseded = true
				}
			}

			entry := PhaseGeneration{
				Phase:      ev.Phase,
				Generation: gen,
				Status:     PhaseRunning,
			}
			idx := len(h.Entries)
			h.Entries = append(h.Entries, entry)
			currentIdx[ev.Phase] = idx
			allIdx[ev.Phase] = append(allIdx[ev.Phase], idx)

		case EventPhaseCompleted:
			idx, ok := currentIdx[ev.Phase]
			if !ok {
				continue
			}
			h.Entries[idx].Status = PhaseCompleted
			if v, ok := ev.Data["cost"]; ok {
				h.Entries[idx].Cost = toFloat64(v)
			}
			if v, ok := ev.Data["duration_ms"]; ok {
				h.Entries[idx].DurationMs = toInt64(v)
			}
			// Load details from result JSON.
			h.Entries[idx].Details = loadPhaseDetails(ev.Phase, h.Entries[idx].Generation, stateDir)

		case EventPhaseFailed:
			idx, ok := currentIdx[ev.Phase]
			if !ok {
				continue
			}
			h.Entries[idx].Status = PhaseFailed
			if v, ok := ev.Data["error"]; ok {
				if s, ok := v.(string); ok {
					h.Entries[idx].Error = s
				}
			}
			if v, ok := ev.Data["duration_ms"]; ok {
				h.Entries[idx].DurationMs = toInt64(v)
			}
			if v, ok := ev.Data["cost"]; ok {
				h.Entries[idx].Cost = toFloat64(v)
			}

		case EventPhaseSkipped:
			entry := PhaseGeneration{
				Phase:  ev.Phase,
				Status: "skipped",
			}
			idx := len(h.Entries)
			h.Entries = append(h.Entries, entry)
			allIdx[ev.Phase] = append(allIdx[ev.Phase], idx)
		}
	}

	// Calculate superseded cost.
	for i := range h.Entries {
		if h.Entries[i].Superseded {
			h.SupersededCost += h.Entries[i].Cost
		}
	}

	return h
}

// loadPhaseDetails reads the result JSON for a phase generation and returns
// a one-line summary via progress.PhaseSummary. For the current (latest)
// generation, the file is <phase>.json. For archived generations, it is
// <phase>.json.<generation>.
func loadPhaseDetails(phase string, generation int, stateDir string) string {
	if stateDir == "" {
		return ""
	}

	// Try archived path first for non-latest generations.
	path := filepath.Join(stateDir, phase+".json")
	archivePath := fmt.Sprintf("%s.%d", path, generation)
	data, err := os.ReadFile(archivePath)
	if err != nil {
		// Fall back to current result file.
		data, err = os.ReadFile(path)
		if err != nil {
			return ""
		}
	}

	return progress.PhaseSummary(phase, json.RawMessage(data))
}

// FormatDuration formats a duration from milliseconds as "Xs" or "XmYYs".
func FormatDuration(ms int64) string {
	d := (time.Duration(ms) * time.Millisecond).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// toInt converts a JSON number (float64) to int.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// toInt64 converts a JSON number (float64) to int64.
func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
}

// toFloat64 converts a JSON number to float64.
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}
