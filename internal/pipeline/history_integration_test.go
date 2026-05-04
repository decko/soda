package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildHistory_FullPipeline tests a complete pipeline run with all phases.
func TestBuildHistory_FullPipeline(t *testing.T) {
	dir := t.TempDir()

	// Write result files.
	writeJSON(t, filepath.Join(dir, "triage.json"), map[string]any{"complexity": "low", "automatable": "yes"})
	writeJSON(t, filepath.Join(dir, "plan.json"), map[string]any{"tasks": []any{map[string]any{"id": "1"}, map[string]any{"id": "2"}}})
	writeJSON(t, filepath.Join(dir, "implement.json"), map[string]any{"files_changed": []any{map[string]any{"path": "a.go"}}, "commits": []any{map[string]any{"hash": "abc"}}, "tests_passed": true})
	writeJSON(t, filepath.Join(dir, "verify.json"), map[string]any{"verdict": "PASS"})
	writeJSON(t, filepath.Join(dir, "submit.json"), map[string]any{"pr_url": "https://github.com/decko/soda/pull/99"})

	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(5000)}},
		{Phase: "plan", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "plan", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.30, "duration_ms": float64(15000)}},
		{Phase: "implement", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "implement", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 1.20, "duration_ms": float64(120000)}},
		{Phase: "verify", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "verify", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.25, "duration_ms": float64(30000)}},
		{Phase: "submit", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "submit", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.05, "duration_ms": float64(10000)}},
	}

	h := BuildHistory(events, dir)

	if len(h.Entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(h.Entries))
	}

	// Verify details are populated.
	expectedDetails := map[string]string{
		"triage":    "low",
		"plan":      "2 tasks",
		"implement": "1 file changed, 1 commit",
		"verify":    "PASS",
		"submit":    "PR #99",
	}
	for _, entry := range h.Entries {
		if want, ok := expectedDetails[entry.Phase]; ok {
			if entry.Details != want {
				t.Errorf("%s details = %q, want %q", entry.Phase, entry.Details, want)
			}
		}
	}

	// No superseded entries.
	if h.SupersededCost != 0 {
		t.Errorf("superseded cost = %f, want 0", h.SupersededCost)
	}
	for _, entry := range h.Entries {
		if entry.Superseded {
			t.Errorf("%s should not be superseded", entry.Phase)
		}
	}
}

// TestBuildHistory_FailAndRetryScenario tests a phase that fails and is rerun.
func TestBuildHistory_FailAndRetryScenario(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "verify.json"), map[string]any{"verdict": "PASS"})

	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(5000)}},
		{Phase: "plan", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "plan", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.30, "duration_ms": float64(15000)}},
		{Phase: "implement", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "implement", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 1.00, "duration_ms": float64(100000)}},
		// Verify fails on first attempt.
		{Phase: "verify", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "verify", Kind: EventPhaseFailed, Data: map[string]any{"error": "tests did not pass", "duration_ms": float64(20000), "cost": 0.15}},
		// Implement re-run (generation 2).
		{Phase: "implement", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(2)}},
		{Phase: "implement", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.80, "duration_ms": float64(90000)}},
		// Verify re-run (generation 2).
		{Phase: "verify", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(2)}},
		{Phase: "verify", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.20, "duration_ms": float64(25000)}},
	}

	h := BuildHistory(events, dir)

	if len(h.Entries) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(h.Entries))
	}

	// Check superseded entries.
	superseded := 0
	for _, entry := range h.Entries {
		if entry.Superseded {
			superseded++
		}
	}
	if superseded != 2 { // implement gen 1 + verify gen 1
		t.Errorf("expected 2 superseded entries, got %d", superseded)
	}

	// Verify gen 1 should be superseded with cost.
	verifyGen1 := h.Entries[3] // verify gen 1 (failed)
	if verifyGen1.Phase != "verify" || verifyGen1.Generation != 1 {
		t.Errorf("entry 3: phase=%q gen=%d, want verify gen 1", verifyGen1.Phase, verifyGen1.Generation)
	}
	if !verifyGen1.Superseded {
		t.Error("verify gen 1 should be superseded")
	}
	if verifyGen1.Status != PhaseFailed {
		t.Errorf("verify gen 1 status = %q, want failed", verifyGen1.Status)
	}

	// Verify gen 2 should NOT be superseded.
	verifyGen2 := h.Entries[5]
	if verifyGen2.Superseded {
		t.Error("verify gen 2 should not be superseded")
	}
	if verifyGen2.Status != PhaseCompleted {
		t.Errorf("verify gen 2 status = %q, want completed", verifyGen2.Status)
	}

	// Superseded cost = implement gen 1 (1.00) + verify gen 1 (0.15).
	expectedSuperseded := 1.15
	if h.SupersededCost < expectedSuperseded-0.01 || h.SupersededCost > expectedSuperseded+0.01 {
		t.Errorf("superseded cost = %f, want ~%f", h.SupersededCost, expectedSuperseded)
	}
}

// TestBuildHistory_MixedSkipAndRun tests a pipeline where some phases are skipped.
func TestBuildHistory_MixedSkipAndRun(t *testing.T) {
	events := []Event{
		{Phase: "triage", Kind: EventPhaseSkipped},
		{Phase: "plan", Kind: EventPhaseSkipped},
		{Phase: "implement", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(2)}},
		{Phase: "implement", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.50, "duration_ms": float64(60000)}},
		{Phase: "verify", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(2)}},
		{Phase: "verify", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.20, "duration_ms": float64(25000)}},
	}

	h := BuildHistory(events, "")

	if len(h.Entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(h.Entries))
	}

	// First two should be skipped.
	if h.Entries[0].Status != PhaseSkipped {
		t.Errorf("triage status = %q, want skipped", h.Entries[0].Status)
	}
	if h.Entries[1].Status != PhaseSkipped {
		t.Errorf("plan status = %q, want skipped", h.Entries[1].Status)
	}

	// Next two are real runs.
	if h.Entries[2].Status != PhaseCompleted {
		t.Errorf("implement status = %q, want completed", h.Entries[2].Status)
	}
	if h.Entries[3].Status != PhaseCompleted {
		t.Errorf("verify status = %q, want completed", h.Entries[3].Status)
	}
}

// TestBuildHistory_ThreeGenerations tests a phase that runs three times.
func TestBuildHistory_ThreeGenerations(t *testing.T) {
	events := []Event{
		{Phase: "implement", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "implement", Kind: EventPhaseFailed, Data: map[string]any{"error": "compile error", "cost": 0.50}},
		{Phase: "implement", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(2)}},
		{Phase: "implement", Kind: EventPhaseFailed, Data: map[string]any{"error": "test failure", "cost": 0.60}},
		{Phase: "implement", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(3)}},
		{Phase: "implement", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.70, "duration_ms": float64(90000)}},
	}

	h := BuildHistory(events, "")

	if len(h.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(h.Entries))
	}

	// Gens 1 and 2 are superseded.
	if !h.Entries[0].Superseded || !h.Entries[1].Superseded {
		t.Error("generations 1 and 2 should be superseded")
	}
	if h.Entries[2].Superseded {
		t.Error("generation 3 should not be superseded")
	}

	// Superseded cost = 0.50 + 0.60 = 1.10.
	if h.SupersededCost < 1.09 || h.SupersededCost > 1.11 {
		t.Errorf("superseded cost = %f, want ~1.10", h.SupersededCost)
	}
}

// TestReadEventsAndBuildHistory_Integration tests the full ReadEvents → BuildHistory flow.
func TestReadEventsAndBuildHistory_Integration(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)

	// Log events via logEvent.
	logEvent(dir, Event{Timestamp: ts, Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": 1}})
	logEvent(dir, Event{Timestamp: ts.Add(5 * time.Second), Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": int64(5000)}})
	logEvent(dir, Event{Timestamp: ts.Add(6 * time.Second), Phase: "plan", Kind: EventPhaseStarted, Data: map[string]any{"generation": 1}})
	logEvent(dir, Event{Timestamp: ts.Add(20 * time.Second), Phase: "plan", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.30, "duration_ms": int64(14000)}})

	// Read events back.
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}

	// Build history.
	h := BuildHistory(events, dir)
	if len(h.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(h.Entries))
	}

	if h.Entries[0].Phase != "triage" || h.Entries[0].Status != PhaseCompleted {
		t.Errorf("triage entry: phase=%q status=%q", h.Entries[0].Phase, h.Entries[0].Status)
	}
	if h.Entries[1].Phase != "plan" || h.Entries[1].Status != PhaseCompleted {
		t.Errorf("plan entry: phase=%q status=%q", h.Entries[1].Phase, h.Entries[1].Status)
	}
}

// TestFormatDuration_Comprehensive tests additional edge cases.
func TestFormatDuration_Comprehensive(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{500, "1s"},  // rounds to nearest second
		{1499, "1s"}, // rounds down
		{1500, "2s"}, // rounds up
		{45000, "45s"},
		{59999, "1m00s"},
		{60000, "1m00s"},
		{90000, "1m30s"},
		{3600000, "60m00s"},
	}
	for _, tc := range tests {
		got := FormatDuration(tc.ms)
		if got != tc.want {
			t.Errorf("FormatDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
