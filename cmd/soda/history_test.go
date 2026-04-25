package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/pipeline"
)

func TestFormatDetails(t *testing.T) {
	tests := []struct {
		name       string
		details    string
		errMsg     string
		superseded bool
		want       string
	}{
		{
			name:    "details only",
			details: "low",
			want:    "low",
		},
		{
			name:   "error only",
			errMsg: "tests failed",
			want:   "tests failed",
		},
		{
			name:    "details and error",
			details: "FAIL",
			errMsg:  "verification failed",
			want:    "FAIL — verification failed",
		},
		{
			name:       "superseded overrides everything",
			details:    "low",
			errMsg:     "some error",
			superseded: true,
			want:       "(superseded)",
		},
		{
			name: "empty",
			want: "",
		},
		{
			name:   "long error truncated",
			errMsg: "this is a very long error message that definitely exceeds sixty characters in total length",
			want:   "this is a very long error message that definitely exceeds...",
		},
		{
			name:    "details with long error truncated",
			details: "FAIL",
			errMsg:  "this is a very long error message that definitely exceeds sixty characters in total length",
			want:    "FAIL — this is a very long error message that definitely exceeds...",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDetails(tc.details, tc.errMsg, tc.superseded)
			if got != tc.want {
				t.Errorf("formatDetails(%q, %q, %v) = %q, want %q", tc.details, tc.errMsg, tc.superseded, got, tc.want)
			}
		})
	}
}

func TestPrettyJSON(t *testing.T) {
	tests := []struct {
		name string
		data json.RawMessage
		want string
	}{
		{
			name: "simple object",
			data: json.RawMessage(`{"key":"value"}`),
			want: "{\n  \"key\": \"value\"\n}",
		},
		{
			name: "nested object",
			data: json.RawMessage(`{"a":{"b":1}}`),
			want: "{\n  \"a\": {\n    \"b\": 1\n  }\n}",
		},
		{
			name: "invalid json",
			data: json.RawMessage(`not json`),
			want: "not json",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := prettyJSON(tc.data)
			if got != tc.want {
				t.Errorf("prettyJSON(%q) =\n%s\nwant:\n%s", string(tc.data), got, tc.want)
			}
		})
	}
}

func TestNewHistoryCmd_Flags(t *testing.T) {
	cmd := newHistoryCmd()

	// Verify --detail flag exists and defaults to false.
	detailFlag := cmd.Flags().Lookup("detail")
	if detailFlag == nil {
		t.Fatal("--detail flag not found")
	}
	if detailFlag.DefValue != "false" {
		t.Errorf("--detail default = %q, want %q", detailFlag.DefValue, "false")
	}

	// Verify --phase flag exists and defaults to empty.
	phaseFlag := cmd.Flags().Lookup("phase")
	if phaseFlag == nil {
		t.Fatal("--phase flag not found")
	}
	if phaseFlag.DefValue != "" {
		t.Errorf("--phase default = %q, want empty", phaseFlag.DefValue)
	}
}

func TestStatusSymbol(t *testing.T) {
	tests := []struct {
		status     pipeline.PhaseStatus
		superseded bool
		want       string
	}{
		{pipeline.PhaseCompleted, false, "✓"},
		{pipeline.PhaseFailed, false, "✗"},
		{pipeline.PhaseRunning, false, "⧗"},
		{pipeline.PhaseSkipped, false, "⏭"},
		{pipeline.PhasePending, false, "pending"},

		// Superseded variants.
		{pipeline.PhaseCompleted, true, "✓ ⏭"},
		{pipeline.PhaseFailed, true, "✗ ⏭"},
		{pipeline.PhaseRunning, true, "⏭"},
	}
	for _, tc := range tests {
		got := statusSymbol(tc.status, tc.superseded)
		if got != tc.want {
			t.Errorf("statusSymbol(%q, %v) = %q, want %q", tc.status, tc.superseded, got, tc.want)
		}
	}
}

// TestRenderEventsHistory_PromptHash verifies that when --detail is used,
// the prompt hash stored in meta.Phases is displayed per phase.
func TestRenderEventsHistory_PromptHash(t *testing.T) {
	dir := t.TempDir()

	// Write a triage result JSON so the phase has output to display.
	triageResult := map[string]any{"complexity": "low", "automatable": true}
	data, _ := json.Marshal(triageResult)
	if err := os.WriteFile(filepath.Join(dir, "triage.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile triage.json: %v", err)
	}

	const wantHash = "abc123def456"

	meta := &pipeline.PipelineMeta{
		Ticket:    "TEST-1",
		TotalCost: 0.12,
		Phases: map[string]*pipeline.PhaseState{
			"triage": {
				Status:     pipeline.PhaseCompleted,
				Cost:       0.12,
				PromptHash: wantHash,
			},
		},
	}

	events := []pipeline.Event{
		{Phase: "triage", Kind: pipeline.EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: pipeline.EventPhaseCompleted, Data: map[string]any{"cost": 0.12, "duration_ms": float64(8000)}},
	}

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := renderEventsHistory(meta, events, dir, true /* detail */, "" /* phaseFilter */)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("renderEventsHistory error: %v", err)
	}

	if !strings.Contains(output, wantHash) {
		t.Errorf("detail output missing prompt hash %q\ngot:\n%s", wantHash, output)
	}

	if !strings.Contains(output, "Prompt Hash:") {
		t.Errorf("detail output missing 'Prompt Hash:' label\ngot:\n%s", output)
	}
}

// TestRenderEventsHistory_PromptHashSuperseded verifies that superseded entries
// do NOT get the prompt hash from meta.Phases (which only stores the latest
// generation's hash). Only the current (non-superseded) entry should display it.
func TestRenderEventsHistory_PromptHashSuperseded(t *testing.T) {
	dir := t.TempDir()

	// Write result files for both generations.
	result := map[string]any{"complexity": "low", "automatable": true}
	data, _ := json.Marshal(result)
	// Gen 1 archived result.
	if err := os.WriteFile(filepath.Join(dir, "triage.json.1"), data, 0644); err != nil {
		t.Fatalf("WriteFile triage.json.1: %v", err)
	}
	// Gen 2 current result.
	if err := os.WriteFile(filepath.Join(dir, "triage.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile triage.json: %v", err)
	}

	const latestHash = "gen2hash999"

	meta := &pipeline.PipelineMeta{
		Ticket:    "TEST-SUP",
		TotalCost: 0.24,
		Phases: map[string]*pipeline.PhaseState{
			"triage": {
				Status:     pipeline.PhaseCompleted,
				Cost:       0.12,
				PromptHash: latestHash,
			},
		},
	}

	// Two generations: gen 1 completed then superseded, gen 2 completed.
	events := []pipeline.Event{
		{Phase: "triage", Kind: pipeline.EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: pipeline.EventPhaseCompleted, Data: map[string]any{"cost": 0.12, "duration_ms": float64(5000)}},
		{Phase: "triage", Kind: pipeline.EventPhaseStarted, Data: map[string]any{"generation": float64(2)}},
		{Phase: "triage", Kind: pipeline.EventPhaseCompleted, Data: map[string]any{"cost": 0.12, "duration_ms": float64(6000)}},
	}

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := renderEventsHistory(meta, events, dir, true /* detail */, "" /* phaseFilter */)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("renderEventsHistory error: %v", err)
	}

	// The latest (gen 2) should have the hash.
	if !strings.Contains(output, "Prompt Hash: "+latestHash) {
		t.Errorf("current generation should display prompt hash %q\ngot:\n%s", latestHash, output)
	}

	// Count occurrences — the hash should appear exactly once (only for gen 2).
	count := strings.Count(output, latestHash)
	if count != 1 {
		t.Errorf("prompt hash %q should appear exactly once, appeared %d times\ngot:\n%s", latestHash, count, output)
	}
}

// TestRenderEventsHistory_PromptHashNoDetail verifies that without --detail,
// the prompt hash is NOT shown in the output.
func TestRenderEventsHistory_PromptHashNoDetail(t *testing.T) {
	dir := t.TempDir()

	const wantHash = "abc123def456"

	meta := &pipeline.PipelineMeta{
		Ticket:    "TEST-2",
		TotalCost: 0.12,
		Phases: map[string]*pipeline.PhaseState{
			"triage": {
				Status:     pipeline.PhaseCompleted,
				Cost:       0.12,
				PromptHash: wantHash,
			},
		},
	}

	events := []pipeline.Event{
		{Phase: "triage", Kind: pipeline.EventPhaseStarted, Data: map[string]any{"generation": float64(1)}},
		{Phase: "triage", Kind: pipeline.EventPhaseCompleted, Data: map[string]any{"cost": 0.12, "duration_ms": float64(8000)}},
	}

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := renderEventsHistory(meta, events, dir, false /* detail */, "" /* phaseFilter */)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("renderEventsHistory error: %v", err)
	}

	if strings.Contains(output, wantHash) {
		t.Errorf("non-detail output should not contain prompt hash %q\ngot:\n%s", wantHash, output)
	}
}
