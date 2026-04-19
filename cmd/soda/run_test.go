package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/claude"
	"github.com/decko/soda/internal/config"
	"github.com/decko/soda/internal/pipeline"
	"github.com/decko/soda/internal/progress"
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

	t.Run("patch_fixed", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("patch", json.RawMessage(`{
			"ticket_key": "T-1",
			"fix_results": [
				{"fix_index": 0, "status": "fixed", "description": "fixed test"},
				{"fix_index": 1, "status": "cannot_fix", "description": "too complex"}
			],
			"files_changed": [],
			"tests_passed": true,
			"too_complex": false
		}`))

		got := formatPhaseDetails(state, "patch")
		if got != "1/2 fixed" {
			t.Errorf("expected '1/2 fixed', got %q", got)
		}
	})

	t.Run("patch_too_complex", func(t *testing.T) {
		dir := t.TempDir()
		state, _ := pipeline.LoadOrCreate(dir, "T-1")
		state.WriteResult("patch", json.RawMessage(`{
			"ticket_key": "T-1",
			"fix_results": [],
			"files_changed": [],
			"tests_passed": false,
			"too_complex": true,
			"too_complex_reason": "needs refactoring"
		}`))

		got := formatPhaseDetails(state, "patch")
		if got != "too complex" {
			t.Errorf("expected 'too complex', got %q", got)
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
	fprintSummary(&buf, state, testPhases(), "Add feature X", 5*time.Minute, nil, nil)
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
	fprintSummary(&buf, state, testPhases(), "Fix bug Y", 2*time.Minute, fmt.Errorf("engine: phase implement failed"), nil)
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

	// Check next steps — generic error should suggest --from plan (predecessor)
	if !strings.Contains(output, "Next steps") {
		t.Error("expected next steps section")
	}
	if !strings.Contains(output, "--from plan") {
		t.Error("expected --from plan in next steps")
	}
	// Generic error should show "Resume the pipeline"
	if !strings.Contains(output, "Resume the pipeline") {
		t.Error("expected 'Resume the pipeline' suggestion for generic error")
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
	skipped := map[string]bool{"plan": true, "implement": true, "verify": true, "submit": true}
	fprintSummary(&buf, state, testPhases(), "Blocked ticket", 30*time.Second, fmt.Errorf("gate: triage blocked"), skipped)
	output := buf.String()

	// Skipped phases should show ⏭ status
	lines := strings.Split(output, "\n")
	skippedCount := 0
	for _, line := range lines {
		if strings.Contains(line, "⏭") {
			skippedCount++
		}
	}
	// plan, implement, verify, submit should all be skipped
	if skippedCount < 4 {
		t.Errorf("expected at least 4 skipped phases (⏭), got %d", skippedCount)
	}

	// Triage should show blocked details
	if !strings.Contains(output, "BLOCKED: needs design review") {
		t.Error("expected blocked details for triage")
	}
}

func TestPrintSummaryVerifyGate(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-10")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-10"
	meta.Worktree = "/tmp/worktrees/PROJ-10"
	meta.TotalCost = 3.00

	meta.Phases["triage"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 10000, Cost: 0.30}
	meta.Phases["plan"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 20000, Cost: 0.50}
	meta.Phases["implement"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 50000, Cost: 1.20}
	meta.Phases["verify"] = &pipeline.PhaseState{Status: pipeline.PhaseFailed, DurationMs: 30000, Cost: 1.00, Error: "verification failed"}

	state.WriteResult("verify", json.RawMessage(`{"ticket_key":"PROJ-10","verdict":"FAIL","criteria_results":[{"criterion":"tests pass","passed":false,"evidence":"2 failures"}],"command_results":[]}`))

	gateErr := &pipeline.PhaseGateError{Phase: "verify", Reason: "verification failed: tests pass"}
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Verify gate test", 3*time.Minute, gateErr, nil)
	output := buf.String()

	if !strings.Contains(output, "Next steps") {
		t.Error("expected next steps section")
	}
	if !strings.Contains(output, "Review the verify output") {
		t.Error("expected 'Review the verify output' suggestion")
	}
	if !strings.Contains(output, "cd /tmp/worktrees/PROJ-10") {
		t.Error("expected cd to worktree path")
	}
	if !strings.Contains(output, "--from implement") {
		t.Error("expected --from implement suggestion")
	}
	if !strings.Contains(output, "--from verify") {
		t.Error("expected --from verify suggestion")
	}
	if !strings.Contains(output, "re-implement with updated context") {
		t.Error("expected parenthetical explaining --from implement")
	}
	if !strings.Contains(output, "re-verify after manual fixes") {
		t.Error("expected parenthetical explaining --from verify")
	}
}

func TestPrintSummaryVerifyGateNoWorktree(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-11")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-11"
	// No worktree set
	meta.TotalCost = 2.00

	meta.Phases["triage"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 10000, Cost: 0.30}
	meta.Phases["plan"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 20000, Cost: 0.50}
	meta.Phases["implement"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 50000, Cost: 0.70}
	meta.Phases["verify"] = &pipeline.PhaseState{Status: pipeline.PhaseFailed, DurationMs: 30000, Cost: 0.50, Error: "verification failed"}

	gateErr := &pipeline.PhaseGateError{Phase: "verify", Reason: "verification failed"}
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Verify no worktree", 2*time.Minute, gateErr, nil)
	output := buf.String()

	// Should NOT contain cd line when no worktree
	if strings.Contains(output, "cd ") {
		t.Error("should not contain cd line when no worktree is set")
	}
	if !strings.Contains(output, "--from implement") {
		t.Error("expected --from implement suggestion")
	}
}

func TestPrintSummaryNonVerifyPhaseGateError(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-15")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-15"
	meta.TotalCost = 0.30

	meta.Phases["triage"] = &pipeline.PhaseState{Status: pipeline.PhaseFailed, DurationMs: 10000, Cost: 0.30, Error: "not automatable"}

	gateErr := &pipeline.PhaseGateError{Phase: "triage", Reason: "not automatable"}
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Non-verify gate test", 30*time.Second, gateErr, nil)
	output := buf.String()

	if !strings.Contains(output, "Next steps") {
		t.Error("expected next steps section")
	}
	if !strings.Contains(output, `Phase "triage" was gated`) {
		t.Error("expected gated phase description mentioning triage")
	}
	if !strings.Contains(output, "not automatable") {
		t.Error("expected gate reason in output")
	}
	if !strings.Contains(output, "Re-run from that phase") {
		t.Error("expected 're-run from that phase' suggestion")
	}
	if !strings.Contains(output, "--from triage") {
		t.Error("expected --from triage suggestion")
	}
	if !strings.Contains(output, "retry after fixing the gate condition") {
		t.Error("expected parenthetical explaining retry")
	}
	// Should NOT contain verify-specific advice
	if strings.Contains(output, "Review the verify output") {
		t.Error("should not contain verify-specific advice for non-verify gate error")
	}
}

func TestPrintSummaryBudgetExceeded(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-20")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-20"
	meta.TotalCost = 5.00

	meta.Phases["triage"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 10000, Cost: 0.50}
	meta.Phases["plan"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 20000, Cost: 0.50}
	meta.Phases["implement"] = &pipeline.PhaseState{Status: pipeline.PhaseFailed, DurationMs: 60000, Cost: 4.00, Error: "budget exceeded"}

	budgetErr := &pipeline.BudgetExceededError{Limit: 5.00, Actual: 5.00, Phase: "implement"}
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Budget test", 4*time.Minute, budgetErr, nil)
	output := buf.String()

	if !strings.Contains(output, "Next steps") {
		t.Error("expected next steps section")
	}
	if !strings.Contains(output, "Budget limit ($5.00)") {
		t.Error("expected budget limit in output")
	}
	if !strings.Contains(output, "limits.max_cost_per_ticket") {
		t.Error("expected config key suggestion")
	}
	if !strings.Contains(output, "--from implement") {
		t.Error("expected --from implement suggestion")
	}
	if !strings.Contains(output, "resume with higher budget") {
		t.Error("expected parenthetical explaining --from suggestion")
	}
}

func TestPrintSummaryTransientError(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-30")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-30"
	meta.TotalCost = 1.00

	meta.Phases["triage"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 10000, Cost: 0.30}
	meta.Phases["plan"] = &pipeline.PhaseState{Status: pipeline.PhaseFailed, DurationMs: 5000, Cost: 0.70, Error: "timeout"}

	transientErr := fmt.Errorf("engine: phase plan failed (transient, no retries left): %w",
		&claude.TransientError{Reason: "timeout", Err: fmt.Errorf("connection reset")})
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Transient test", 1*time.Minute, transientErr, nil)
	output := buf.String()

	if !strings.Contains(output, "Next steps") {
		t.Error("expected next steps section")
	}
	if !strings.Contains(output, "transient error") {
		t.Error("expected transient error description")
	}
	if !strings.Contains(output, "--from plan") {
		t.Error("expected --from plan suggestion")
	}
	if !strings.Contains(output, "retry the failed phase") {
		t.Error("expected parenthetical explaining retry")
	}
}

func TestPrintSummaryParseError(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-40")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-40"
	meta.TotalCost = 0.80

	meta.Phases["triage"] = &pipeline.PhaseState{Status: pipeline.PhaseFailed, DurationMs: 15000, Cost: 0.80, Error: "parse error"}

	parseErr := fmt.Errorf("engine: phase triage failed (parse, no retries left): %w",
		&claude.ParseError{Raw: []byte("bad output"), Err: fmt.Errorf("invalid JSON")})
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Parse test", 30*time.Second, parseErr, nil)
	output := buf.String()

	if !strings.Contains(output, "Next steps") {
		t.Error("expected next steps section")
	}
	if !strings.Contains(output, "could not be parsed") {
		t.Error("expected parse error description")
	}
	if !strings.Contains(output, "--from triage") {
		t.Error("expected --from triage suggestion")
	}
	if !strings.Contains(output, "retry with a fresh attempt") {
		t.Error("expected parenthetical explaining retry")
	}
}

func TestPrintSummaryTransientErrorEmptyPhase(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-31")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-31"
	meta.TotalCost = 0.10

	// No phase has PhaseFailed, so failedPhase will be "".
	transientErr := fmt.Errorf("engine: transient: %w",
		&claude.TransientError{Reason: "timeout", Err: fmt.Errorf("connection reset")})
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Transient empty phase", 30*time.Second, transientErr, nil)
	output := buf.String()

	if strings.Contains(output, "--from ") {
		t.Error("should not suggest --from when failedPhase is empty")
	}
	if !strings.Contains(output, "soda run PROJ-31") {
		t.Error("expected bare soda run suggestion")
	}
}

func TestPrintSummaryParseErrorEmptyPhase(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-41")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-41"
	meta.TotalCost = 0.10

	// No phase has PhaseFailed, so failedPhase will be "".
	parseErr := fmt.Errorf("engine: parse: %w",
		&claude.ParseError{Raw: []byte("bad"), Err: fmt.Errorf("invalid")})
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Parse empty phase", 30*time.Second, parseErr, nil)
	output := buf.String()

	if strings.Contains(output, "--from ") {
		t.Error("should not suggest --from when failedPhase is empty")
	}
	if !strings.Contains(output, "soda run PROJ-41") {
		t.Error("expected bare soda run suggestion")
	}
}

func TestPrintSummaryGenericError(t *testing.T) {
	dir := t.TempDir()
	state, _ := pipeline.LoadOrCreate(dir, "PROJ-60")
	meta := state.Meta()
	meta.Branch = "soda/PROJ-60"
	meta.Worktree = "/tmp/worktrees/PROJ-60"
	meta.TotalCost = 1.50

	meta.Phases["triage"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 10000, Cost: 0.50}
	meta.Phases["plan"] = &pipeline.PhaseState{Status: pipeline.PhaseCompleted, DurationMs: 20000, Cost: 0.50}
	meta.Phases["implement"] = &pipeline.PhaseState{Status: pipeline.PhaseFailed, DurationMs: 40000, Cost: 0.50, Error: "unknown failure"}

	genericErr := fmt.Errorf("engine: phase implement failed: something unexpected")
	var buf bytes.Buffer
	fprintSummary(&buf, state, testPhases(), "Generic error test", 2*time.Minute, genericErr, nil)
	output := buf.String()

	if !strings.Contains(output, "Next steps") {
		t.Error("expected next steps section")
	}
	if !strings.Contains(output, "Resume the pipeline") {
		t.Error("expected 'Resume the pipeline' suggestion")
	}
	// Generic error on implement (idx=2) suggests --from plan (predecessor)
	if !strings.Contains(output, "--from plan") {
		t.Error("expected --from plan in generic error next steps")
	}
	if !strings.Contains(output, "Inspect the worktree") {
		t.Error("expected worktree inspection suggestion")
	}
	if !strings.Contains(output, "cd /tmp/worktrees/PROJ-60") {
		t.Error("expected cd to worktree path")
	}
}

func TestBuildPromptConfigDetectDefaults(t *testing.T) {
	t.Run("detected_values_fill_empty_fields", func(t *testing.T) {
		cfg := &config.Config{
			Repos: []config.RepoConfig{
				{Name: "myrepo", Forge: "github"},
				// Formatter and TestCommand are empty.
			},
		}
		promptConfig := buildPromptConfig(cfg)

		// Simulate what runPipeline does after detect.
		if promptConfig.Formatter == "" {
			promptConfig.Formatter = "gofmt -w ."
		}
		if promptConfig.TestCommand == "" {
			promptConfig.TestCommand = "go test ./..."
		}

		if promptConfig.Formatter != "gofmt -w ." {
			t.Errorf("Formatter = %q, want %q", promptConfig.Formatter, "gofmt -w .")
		}
		if promptConfig.TestCommand != "go test ./..." {
			t.Errorf("TestCommand = %q, want %q", promptConfig.TestCommand, "go test ./...")
		}
	})

	t.Run("explicit_config_takes_precedence", func(t *testing.T) {
		cfg := &config.Config{
			Repos: []config.RepoConfig{
				{
					Name:        "myrepo",
					Forge:       "github",
					Formatter:   "custom-fmt",
					TestCommand: "custom-test",
				},
			},
		}
		promptConfig := buildPromptConfig(cfg)

		// Simulate detection filling — should NOT overwrite.
		if promptConfig.Formatter == "" {
			promptConfig.Formatter = "gofmt -w ."
		}
		if promptConfig.TestCommand == "" {
			promptConfig.TestCommand = "go test ./..."
		}

		if promptConfig.Formatter != "custom-fmt" {
			t.Errorf("Formatter = %q, want %q (explicit config should take precedence)", promptConfig.Formatter, "custom-fmt")
		}
		if promptConfig.TestCommand != "custom-test" {
			t.Errorf("TestCommand = %q, want %q (explicit config should take precedence)", promptConfig.TestCommand, "custom-test")
		}
	})

	t.Run("no_repos_uses_detected_values", func(t *testing.T) {
		cfg := &config.Config{}
		promptConfig := buildPromptConfig(cfg)

		if promptConfig.Formatter == "" {
			promptConfig.Formatter = "cargo fmt"
		}
		if promptConfig.TestCommand == "" {
			promptConfig.TestCommand = "cargo test"
		}

		if promptConfig.Formatter != "cargo fmt" {
			t.Errorf("Formatter = %q, want %q", promptConfig.Formatter, "cargo fmt")
		}
		if promptConfig.TestCommand != "cargo test" {
			t.Errorf("TestCommand = %q, want %q", promptConfig.TestCommand, "cargo test")
		}
	})
}

func TestHandleEventPatchExhaustedCycles(t *testing.T) {
	// Verify that handleEvent correctly extracts patch_cycles as int
	// (native Go type from the engine) and falls back to float64 (JSON).
	tests := []struct {
		name       string
		data       map[string]any
		wantCycles string
	}{
		{
			name:       "int value from engine",
			data:       map[string]any{"patch_cycles": 3, "on_exhausted": "stop"},
			wantCycles: "3 cycles",
		},
		{
			name:       "float64 value from JSON",
			data:       map[string]any{"patch_cycles": float64(5), "on_exhausted": "escalate"},
			wantCycles: "5 cycles",
		},
		{
			name:       "zero value",
			data:       map[string]any{"patch_cycles": 0, "on_exhausted": "stop"},
			wantCycles: "0 cycles",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			state, _ := pipeline.LoadOrCreate(dir, "T-1")

			var buf bytes.Buffer
			prog := progress.New(&buf, false)

			event := pipeline.Event{
				Kind: pipeline.EventPatchExhausted,
				Data: tt.data,
			}
			handleEvent(context.Background(), nil, nil, state, prog, event)

			output := buf.String()
			if !strings.Contains(output, tt.wantCycles) {
				t.Errorf("expected output to contain %q, got %q", tt.wantCycles, output)
			}
		})
	}
}
