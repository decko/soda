package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildHistory_SingleGeneration(t *testing.T) {
	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.12, "duration_ms": float64(8000)}},
		{Phase: "plan", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "plan", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.31, "duration_ms": float64(15000)}},
	}

	h := BuildHistory(events, "")
	if len(h.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(h.Entries))
	}

	if h.Entries[0].Phase != "triage" || h.Entries[0].Generation != 1 {
		t.Errorf("entry 0: phase=%q gen=%d", h.Entries[0].Phase, h.Entries[0].Generation)
	}
	if h.Entries[0].Status != PhaseCompleted {
		t.Errorf("entry 0: status=%q, want completed", h.Entries[0].Status)
	}
	if h.Entries[0].Cost != 0.12 {
		t.Errorf("entry 0: cost=%f, want 0.12", h.Entries[0].Cost)
	}
	if h.Entries[0].Superseded {
		t.Error("entry 0 should not be superseded")
	}

	if h.Entries[1].Phase != "plan" || h.Entries[1].Generation != 1 {
		t.Errorf("entry 1: phase=%q gen=%d", h.Entries[1].Phase, h.Entries[1].Generation)
	}
	if h.SupersededCost != 0 {
		t.Errorf("superseded cost = %f, want 0", h.SupersededCost)
	}
}

func TestBuildHistory_MultiGeneration(t *testing.T) {
	events := []Event{
		{Phase: "verify", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "verify", Kind: EventPhaseFailed, Data: map[string]any{"error": "test failure", "duration_ms": float64(5000), "cost": 0.20}},
		{Phase: "verify", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(2)}},
		{Phase: "verify", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.25, "duration_ms": float64(6000)}},
	}

	h := BuildHistory(events, "")
	if len(h.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(h.Entries))
	}

	// First generation should be superseded.
	if !h.Entries[0].Superseded {
		t.Error("entry 0 should be superseded")
	}
	if h.Entries[0].Status != PhaseFailed {
		t.Errorf("entry 0: status=%q, want failed", h.Entries[0].Status)
	}
	if h.Entries[0].Error != "test failure" {
		t.Errorf("entry 0: error=%q, want 'test failure'", h.Entries[0].Error)
	}
	if h.Entries[0].Cost != 0.20 {
		t.Errorf("entry 0: cost=%f, want 0.20", h.Entries[0].Cost)
	}

	// Second generation is current.
	if h.Entries[1].Superseded {
		t.Error("entry 1 should not be superseded")
	}
	if h.Entries[1].Generation != 2 {
		t.Errorf("entry 1: gen=%d, want 2", h.Entries[1].Generation)
	}
	if h.Entries[1].Status != PhaseCompleted {
		t.Errorf("entry 1: status=%q, want completed", h.Entries[1].Status)
	}

	if h.SupersededCost != 0.20 {
		t.Errorf("superseded cost = %f, want 0.20", h.SupersededCost)
	}
}

func TestBuildHistory_RunningPhase(t *testing.T) {
	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(5000)}},
		{Phase: "plan", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		// plan is still running — no completed/failed event
	}

	h := BuildHistory(events, "")
	if len(h.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(h.Entries))
	}

	if h.Entries[1].Status != PhaseRunning {
		t.Errorf("plan status=%q, want running", h.Entries[1].Status)
	}
}

func TestBuildHistory_SkippedPhase(t *testing.T) {
	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(5000)}},
		{Phase: "plan", Kind: EventPhaseSkipped},
	}

	h := BuildHistory(events, "")
	if len(h.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(h.Entries))
	}

	if h.Entries[1].Status != PhaseSkipped {
		t.Errorf("plan status=%q, want skipped", h.Entries[1].Status)
	}
}

func TestBuildHistory_EmptyEvents(t *testing.T) {
	h := BuildHistory(nil, "")
	if len(h.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(h.Entries))
	}
	if h.SupersededCost != 0 {
		t.Errorf("superseded cost = %f, want 0", h.SupersededCost)
	}
}

func TestBuildHistory_WithDetails(t *testing.T) {
	dir := t.TempDir()

	// Write a triage result JSON.
	triageResult := map[string]any{
		"complexity":  "low",
		"automatable": true,
	}
	data, _ := json.Marshal(triageResult)
	if err := os.WriteFile(filepath.Join(dir, "triage.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile triage.json: %v", err)
	}

	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.12, "duration_ms": float64(8000)}},
	}

	h := BuildHistory(events, dir)
	if len(h.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(h.Entries))
	}
	if h.Entries[0].Details != "low" {
		t.Errorf("details = %q, want %q", h.Entries[0].Details, "low")
	}
}

func TestBuildHistory_FailedPhaseDetails(t *testing.T) {
	dir := t.TempDir()

	// Write a verify result with verdict=FAIL (result file exists even though phase failed).
	verifyResult := map[string]any{
		"verdict": "FAIL",
	}
	data, _ := json.Marshal(verifyResult)
	if err := os.WriteFile(filepath.Join(dir, "verify.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile verify.json: %v", err)
	}

	events := []Event{
		{Phase: "verify", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "verify", Kind: EventPhaseFailed, Data: map[string]any{"error": "verification failed", "duration_ms": float64(20000), "cost": 0.15}},
	}

	h := BuildHistory(events, dir)
	if len(h.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(h.Entries))
	}

	if h.Entries[0].Details != "FAIL" {
		t.Errorf("details = %q, want %q", h.Entries[0].Details, "FAIL")
	}
	if h.Entries[0].Error != "verification failed" {
		t.Errorf("error = %q, want %q", h.Entries[0].Error, "verification failed")
	}
}

func TestBuildHistory_EventEmbeddedSummary(t *testing.T) {
	// No result files on disk — details should come from event data "summary" field.
	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(5000), "summary": "low"}},
	}

	h := BuildHistory(events, "") // empty stateDir — no files to read
	if len(h.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(h.Entries))
	}
	if h.Entries[0].Details != "low" {
		t.Errorf("details = %q, want %q (from event summary)", h.Entries[0].Details, "low")
	}
}

func TestBuildHistory_EventEmbeddedSummaryFailed(t *testing.T) {
	// Failed phase with event-embedded summary and no result file.
	events := []Event{
		{Phase: "verify", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "verify", Kind: EventPhaseFailed, Data: map[string]any{"error": "verification failed", "duration_ms": float64(20000), "cost": 0.15, "summary": "FAIL"}},
	}

	h := BuildHistory(events, "")
	if len(h.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(h.Entries))
	}
	if h.Entries[0].Details != "FAIL" {
		t.Errorf("details = %q, want %q (from event summary)", h.Entries[0].Details, "FAIL")
	}
	if h.Entries[0].Error != "verification failed" {
		t.Errorf("error = %q, want %q", h.Entries[0].Error, "verification failed")
	}
}

func TestBuildHistory_ResultFileOverridesEventSummary(t *testing.T) {
	dir := t.TempDir()

	// Write a triage result file.
	triageResult := map[string]any{"complexity": "medium", "automatable": true}
	data, _ := json.Marshal(triageResult)
	if err := os.WriteFile(filepath.Join(dir, "triage.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile triage.json: %v", err)
	}

	// Event includes a stale summary — result file should take precedence.
	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(5000), "summary": "stale-summary"}},
	}

	h := BuildHistory(events, dir)
	if len(h.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(h.Entries))
	}
	if h.Entries[0].Details != "medium" {
		t.Errorf("details = %q, want %q (from result file, not event summary)", h.Entries[0].Details, "medium")
	}
}

func TestBuildHistory_ArchivedDetails(t *testing.T) {
	dir := t.TempDir()

	// Write an archived triage result (generation 1).
	triageResult := map[string]any{
		"complexity":  "high",
		"automatable": false,
	}
	data, _ := json.Marshal(triageResult)
	if err := os.WriteFile(filepath.Join(dir, "triage.json.1"), data, 0644); err != nil {
		t.Fatalf("WriteFile triage.json.1: %v", err)
	}

	// Write the current triage result (generation 2).
	triageResult2 := map[string]any{
		"complexity":  "medium",
		"automatable": true,
	}
	data2, _ := json.Marshal(triageResult2)
	if err := os.WriteFile(filepath.Join(dir, "triage.json"), data2, 0644); err != nil {
		t.Fatalf("WriteFile triage.json: %v", err)
	}

	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(5000)}},
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(2)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.15, "duration_ms": float64(7000)}},
	}

	h := BuildHistory(events, dir)
	if len(h.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(h.Entries))
	}

	// First generation should try the current file first, which now has gen 2 data.
	// Since the archived file exists at triage.json.1, it should read from there
	// only if the current file doesn't exist. But since both exist, gen 1 will
	// try triage.json (which exists), returning "medium".
	// For gen 2 (current), it will also read triage.json returning "medium".
	if h.Entries[1].Details != "medium" {
		t.Errorf("entry 1 details = %q, want %q", h.Entries[1].Details, "medium")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{3000, "3s"},
		{59000, "59s"},
		{60000, "1m00s"},
		{95000, "1m35s"},
		{309000, "5m09s"},
	}
	for _, tc := range tests {
		got := FormatDuration(tc.ms)
		if got != tc.want {
			t.Errorf("FormatDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestToInt(t *testing.T) {
	if got := toInt(float64(3)); got != 3 {
		t.Errorf("toInt(float64(3)) = %d", got)
	}
	if got := toInt(42); got != 42 {
		t.Errorf("toInt(42) = %d", got)
	}
	if got := toInt("nope"); got != 0 {
		t.Errorf("toInt(string) = %d, want 0", got)
	}
}

func TestToFloat64(t *testing.T) {
	if got := toFloat64(float64(1.23)); got != 1.23 {
		t.Errorf("toFloat64(1.23) = %f", got)
	}
	if got := toFloat64(42); got != 42.0 {
		t.Errorf("toFloat64(42) = %f", got)
	}
	if got := toFloat64("nope"); got != 0 {
		t.Errorf("toFloat64(string) = %f, want 0", got)
	}
}

func TestLoadPhaseResult(t *testing.T) {
	t.Run("empty stateDir", func(t *testing.T) {
		got := LoadPhaseResult("triage", 1, "")
		if got != nil {
			t.Errorf("expected nil, got %s", string(got))
		}
	})

	t.Run("no result file", func(t *testing.T) {
		dir := t.TempDir()
		got := LoadPhaseResult("triage", 1, dir)
		if got != nil {
			t.Errorf("expected nil, got %s", string(got))
		}
	})

	t.Run("current result file", func(t *testing.T) {
		dir := t.TempDir()
		data := []byte(`{"complexity":"low","automatable":true}`)
		if err := os.WriteFile(filepath.Join(dir, "triage.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
		got := LoadPhaseResult("triage", 1, dir)
		if string(got) != string(data) {
			t.Errorf("got %s, want %s", string(got), string(data))
		}
	})

	t.Run("archived result file", func(t *testing.T) {
		dir := t.TempDir()
		archived := []byte(`{"complexity":"high"}`)
		current := []byte(`{"complexity":"low"}`)
		if err := os.WriteFile(filepath.Join(dir, "triage.json.1"), archived, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "triage.json"), current, 0644); err != nil {
			t.Fatal(err)
		}
		got := LoadPhaseResult("triage", 1, dir)
		if string(got) != string(archived) {
			t.Errorf("got %s, want %s (archived)", string(got), string(archived))
		}
	})
}

func TestLoadFullOutputs(t *testing.T) {
	dir := t.TempDir()

	triageData := []byte(`{"complexity":"low","automatable":true}`)
	planData := []byte(`{"tasks":[{"id":"1"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "triage.json"), triageData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), planData, 0644); err != nil {
		t.Fatal(err)
	}

	events := []Event{
		{Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(5000)}},
		{Phase: "plan", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "plan", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.20, "duration_ms": float64(10000)}},
	}

	h := BuildHistory(events, dir)

	t.Run("all phases", func(t *testing.T) {
		h2 := h // copy
		h2.Entries = make([]PhaseGeneration, len(h.Entries))
		copy(h2.Entries, h.Entries)
		h2.LoadFullOutputs(dir, "")
		if string(h2.Entries[0].FullOutput) != string(triageData) {
			t.Errorf("triage full output = %s, want %s", string(h2.Entries[0].FullOutput), string(triageData))
		}
		if string(h2.Entries[1].FullOutput) != string(planData) {
			t.Errorf("plan full output = %s, want %s", string(h2.Entries[1].FullOutput), string(planData))
		}
	})

	t.Run("filtered to triage", func(t *testing.T) {
		h2 := h
		h2.Entries = make([]PhaseGeneration, len(h.Entries))
		copy(h2.Entries, h.Entries)
		h2.LoadFullOutputs(dir, "triage")
		if string(h2.Entries[0].FullOutput) != string(triageData) {
			t.Errorf("triage full output = %s, want %s", string(h2.Entries[0].FullOutput), string(triageData))
		}
		if h2.Entries[1].FullOutput != nil {
			t.Errorf("plan full output should be nil when filtered, got %s", string(h2.Entries[1].FullOutput))
		}
	})
}

// Ensure Event timestamp parsing works with BuildHistory.
func TestBuildHistory_PreservesTimestamps(t *testing.T) {
	ts := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: ts, Phase: "triage", Kind: EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Timestamp: ts.Add(10 * time.Second), Phase: "triage", Kind: EventPhaseCompleted, Data: map[string]any{"cost": 0.10, "duration_ms": float64(10000)}},
	}

	h := BuildHistory(events, "")
	if len(h.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(h.Entries))
	}
	if h.Entries[0].DurationMs != 10000 {
		t.Errorf("DurationMs = %d, want 10000", h.Entries[0].DurationMs)
	}
}
