package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/pipeline"
)

func TestResolveLastPhase(t *testing.T) {
	phases := []pipeline.PhaseConfig{
		{Name: "triage"},
		{Name: "plan"},
		{Name: "implement"},
		{Name: "verify"},
	}

	tests := []struct {
		name    string
		meta    *pipeline.PipelineMeta
		want    string
		wantErr bool
	}{
		{
			name: "single failed phase",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{
					"triage": {Status: pipeline.PhaseCompleted},
					"plan":   {Status: pipeline.PhaseFailed},
				},
			},
			want: "plan",
		},
		{
			name: "running phase (stale)",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{
					"triage":    {Status: pipeline.PhaseCompleted},
					"plan":      {Status: pipeline.PhaseCompleted},
					"implement": {Status: pipeline.PhaseRunning},
				},
			},
			want: "implement",
		},
		{
			name: "multiple failed — latest in pipeline wins",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{
					"triage":    {Status: pipeline.PhaseFailed},
					"plan":      {Status: pipeline.PhaseFailed},
					"implement": {Status: pipeline.PhaseFailed},
				},
			},
			want: "implement",
		},
		{
			name: "no failed or running phases",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{
					"triage": {Status: pipeline.PhaseCompleted},
				},
			},
			wantErr: true,
		},
		{
			name: "empty phases",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveLastPhase(tt.meta, phases)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveLastPhase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want string
	}{
		{"zero", 0, "—"},
		{"negative", -100, "—"},
		{"sub_second", 500, "500ms"},
		{"one_second", 1000, "1.0s"},
		{"seconds", 5500, "5.5s"},
		{"one_minute", 60000, "1m"},
		{"minutes_and_seconds", 125000, "2m5s"},
		{"exact_minutes", 180000, "3m"},
		{"large_duration", 754000, "12m34s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.ms)
			if got != tt.want {
				t.Errorf("formatDuration(%d) = %q, want %q", tt.ms, got, tt.want)
			}
		})
	}
}

func TestFormatPhaseDetails(t *testing.T) {
	t.Run("triage", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("triage", json.RawMessage(`{
			"ticket_key": "T-1",
			"repo": "soda",
			"complexity": "small",
			"automatable": true
		}`))

		got := formatPhaseDetails(state, "triage")
		if !strings.Contains(got, "repo=soda") {
			t.Errorf("expected repo=soda, got %q", got)
		}
		if !strings.Contains(got, "complexity=small") {
			t.Errorf("expected complexity=small, got %q", got)
		}
	})

	t.Run("triage_blocked", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("triage", json.RawMessage(`{
			"ticket_key": "T-1",
			"repo": "soda",
			"complexity": "large",
			"automatable": false,
			"block_reason": "needs design review"
		}`))

		got := formatPhaseDetails(state, "triage")
		if !strings.Contains(got, "BLOCKED: needs design review") {
			t.Errorf("expected block reason, got %q", got)
		}
	})

	t.Run("plan", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("plan", json.RawMessage(`{
			"ticket_key": "T-1",
			"approach": "test",
			"tasks": [{"id":"T1","description":"a","files":[],"done_when":"x"},{"id":"T2","description":"b","files":[],"done_when":"y"}],
			"verification": {"commands": ["go test"]}
		}`))

		got := formatPhaseDetails(state, "plan")
		if got != "2 tasks" {
			t.Errorf("expected '2 tasks', got %q", got)
		}
	})

	t.Run("implement", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("implement", json.RawMessage(`{
			"ticket_key": "T-1",
			"branch": "soda/T-1",
			"commits": [{"hash":"abc","message":"feat","task_id":"T1"},{"hash":"def","message":"test","task_id":"T1"}],
			"files_changed": [{"path":"a.go","action":"modified"},{"path":"b.go","action":"created"},{"path":"c.go","action":"modified"}],
			"task_results": [],
			"tests_passed": true
		}`))

		got := formatPhaseDetails(state, "implement")
		if got != "3 files changed, 2 commits" {
			t.Errorf("expected '3 files changed, 2 commits', got %q", got)
		}
	})

	t.Run("verify_pass", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("verify", json.RawMessage(`{
			"ticket_key": "T-1",
			"verdict": "PASS",
			"criteria_results": [{"criterion":"works","passed":true,"evidence":"yes"}],
			"command_results": []
		}`))

		got := formatPhaseDetails(state, "verify")
		if got != "PASS — all criteria met" {
			t.Errorf("expected 'PASS — all criteria met', got %q", got)
		}
	})

	t.Run("verify_fail", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("verify", json.RawMessage(`{
			"ticket_key": "T-1",
			"verdict": "FAIL",
			"criteria_results": [
				{"criterion":"tests pass","passed":true,"evidence":"ok"},
				{"criterion":"lint clean","passed":false,"evidence":"nope"},
				{"criterion":"docs","passed":false,"evidence":"missing"}
			],
			"command_results": []
		}`))

		got := formatPhaseDetails(state, "verify")
		if got != "FAIL — 2 criteria not met" {
			t.Errorf("expected 'FAIL — 2 criteria not met', got %q", got)
		}
	})

	t.Run("submit", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("submit", json.RawMessage(`{
			"ticket_key": "T-1",
			"pr_url": "https://github.com/org/repo/pull/42",
			"pr_number": 42,
			"title": "feat: thing",
			"branch": "soda/T-1",
			"target": "main",
			"forge": "github"
		}`))

		got := formatPhaseDetails(state, "submit")
		if got != "https://github.com/org/repo/pull/42" {
			t.Errorf("expected PR URL, got %q", got)
		}
	})

	t.Run("missing_result_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")

		got := formatPhaseDetails(state, "triage")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("invalid_json_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("triage", json.RawMessage(`{invalid json`))

		got := formatPhaseDetails(state, "triage")
		if got != "" {
			t.Errorf("expected empty string for invalid JSON, got %q", got)
		}
	})

	t.Run("unknown_phase_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("unknown", json.RawMessage(`{"key":"val"}`))

		got := formatPhaseDetails(state, "unknown")
		if got != "" {
			t.Errorf("expected empty string for unknown phase, got %q", got)
		}
	})
}

// testPhases returns a standard phase list for summary tests.
func testPhases() []pipeline.PhaseConfig {
	return []pipeline.PhaseConfig{
		{Name: "triage"},
		{Name: "plan"},
		{Name: "implement"},
		{Name: "verify"},
		{Name: "submit"},
	}
}

func TestPrintSummarySuccess(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-42")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-42"
	meta.Worktree = "/tmp/worktrees/PROJ-42"
	meta.TotalCost = 2.50

	// Set up completed phases
	for _, name := range []string{"triage", "plan", "implement", "verify", "submit"} {
		meta.Phases[name] = &pipeline.PhaseState{
			Status:     pipeline.PhaseCompleted,
			DurationMs: 30000,
			Cost:       0.50,
		}
	}

	// Write structured results
	state.WriteResult("triage", json.RawMessage(`{"ticket_key":"PROJ-42","repo":"soda","complexity":"small","automatable":true}`))
	state.WriteResult("plan", json.RawMessage(`{"ticket_key":"PROJ-42","approach":"x","tasks":[{"id":"T1","description":"d","files":[],"done_when":"w"}],"verification":{"commands":[]}}`))
	state.WriteResult("implement", json.RawMessage(`{"ticket_key":"PROJ-42","branch":"soda/PROJ-42","commits":[{"hash":"abc","message":"feat","task_id":"T1"}],"files_changed":[{"path":"a.go","action":"modified"}],"task_results":[],"tests_passed":true}`))
	state.WriteResult("verify", json.RawMessage(`{"ticket_key":"PROJ-42","verdict":"PASS","criteria_results":[],"command_results":[]}`))
	state.WriteResult("submit", json.RawMessage(`{"ticket_key":"PROJ-42","pr_url":"https://github.com/org/repo/pull/99","pr_number":99,"title":"feat","branch":"soda/PROJ-42","target":"main","forge":"github"}`))

	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Add feature X", 5*time.Minute, nil)
	output := buf.String()

	// Check header
	if !strings.Contains(output, "✅ Pipeline completed successfully") {
		t.Error("expected success header")
	}

	// Check ticket info
	if !strings.Contains(output, "PROJ-42") {
		t.Error("expected ticket key")
	}
	if !strings.Contains(output, "Add feature X") {
		t.Error("expected ticket summary")
	}
	if !strings.Contains(output, "soda/PROJ-42") {
		t.Error("expected branch")
	}

	// Check phase table has all phases
	for _, name := range []string{"triage", "plan", "implement", "verify", "submit"} {
		if !strings.Contains(output, name) {
			t.Errorf("expected phase %q in output", name)
		}
	}

	// Check status symbols
	if !strings.Contains(output, "✓") {
		t.Error("expected ✓ status symbol")
	}

	// Check totals
	if !strings.Contains(output, "$2.50") {
		t.Error("expected total cost $2.50")
	}

	// Check PR URL
	if !strings.Contains(output, "https://github.com/org/repo/pull/99") {
		t.Error("expected PR URL in output")
	}

	// Should NOT contain failure hints
	if strings.Contains(output, "Next steps") {
		t.Error("should not contain next steps on success")
	}
}

func TestPrintSummaryFailure(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-99")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-99"
	meta.TotalCost = 1.80

	meta.Phases["triage"] = &pipeline.PhaseState{
		Status:     pipeline.PhaseCompleted,
		DurationMs: 15000,
		Cost:       0.30,
	}
	meta.Phases["plan"] = &pipeline.PhaseState{
		Status:     pipeline.PhaseCompleted,
		DurationMs: 25000,
		Cost:       0.50,
	}
	meta.Phases["implement"] = &pipeline.PhaseState{
		Status:     pipeline.PhaseFailed,
		DurationMs: 60000,
		Cost:       1.00,
		Error:      "test suite failed",
	}

	state.WriteResult("triage", json.RawMessage(`{"ticket_key":"PROJ-99","repo":"soda","complexity":"medium","automatable":true}`))
	state.WriteResult("plan", json.RawMessage(`{"ticket_key":"PROJ-99","approach":"x","tasks":[{"id":"T1","description":"d","files":[],"done_when":"w"},{"id":"T2","description":"e","files":[],"done_when":"x"}],"verification":{"commands":[]}}`))

	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Fix bug Y", 2*time.Minute, fmt.Errorf("engine: phase implement failed"))
	output := buf.String()

	// Check failure header
	if !strings.Contains(output, "❌ Pipeline failed") {
		t.Error("expected failure header")
	}

	// Check failed phase marker
	if !strings.Contains(output, "✗") {
		t.Error("expected ✗ status symbol for failed phase")
	}

	// Check error details for implement phase
	if !strings.Contains(output, "test suite failed") {
		t.Error("expected error message in failed phase details")
	}

	// Check next steps
	if !strings.Contains(output, "Next steps") {
		t.Error("expected next steps section")
	}
	// Should suggest --from plan (predecessor of implement)
	if !strings.Contains(output, "--from plan") {
		t.Error("expected --from plan in next steps")
	}
}

func TestPrintSummarySkippedPhases(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-50")
	meta := state.Meta()
	meta.TotalCost = 0.30

	// Only triage completed, rest are pending (not in Phases map)
	meta.Phases["triage"] = &pipeline.PhaseState{
		Status:     pipeline.PhaseCompleted,
		DurationMs: 10000,
		Cost:       0.30,
	}

	state.WriteResult("triage", json.RawMessage(`{"ticket_key":"PROJ-50","repo":"soda","complexity":"small","automatable":false,"block_reason":"needs design review"}`))

	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Blocked ticket", 30*time.Second, fmt.Errorf("gate: triage blocked"))
	output := buf.String()

	// Phases without state should show · status
	lines := strings.Split(output, "\n")
	pendingCount := 0
	for _, line := range lines {
		if strings.Contains(line, "·") {
			pendingCount++
		}
	}
	// plan, implement, verify, submit should all be pending
	if pendingCount < 4 {
		t.Errorf("expected at least 4 pending phases (·), got %d", pendingCount)
	}

	// Triage should show blocked details
	if !strings.Contains(output, "BLOCKED: needs design review") {
		t.Error("expected blocked details for triage")
	}
}
