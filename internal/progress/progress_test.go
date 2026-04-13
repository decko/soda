package progress

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, true)
	if prog == nil {
		t.Fatal("expected non-nil Progress")
	}
	if !prog.isTTY {
		t.Error("expected isTTY to be true")
	}
}

func TestPhaseStartedTTY(t *testing.T) {
	var buf safeBuffer
	prog := New(&buf, true)
	prog.TickInterval = 10 * time.Millisecond

	prog.PhaseStarted("triage")
	// Give the spinner goroutine time to write at least one frame
	time.Sleep(50 * time.Millisecond)
	prog.stopSpinner()

	output := buf.String()
	if !strings.Contains(output, "triage") {
		t.Errorf("expected output to contain 'triage', got %q", output)
	}
	if !strings.Contains(output, "classifying ticket...") {
		t.Errorf("expected output to contain description, got %q", output)
	}
}

func TestPhaseStartedNonTTY(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, false)

	prog.PhaseStarted("plan")

	output := buf.String()
	if !strings.Contains(output, "plan") {
		t.Errorf("expected output to contain 'plan', got %q", output)
	}
	if !strings.Contains(output, "designing implementation...") {
		t.Errorf("expected output to contain description, got %q", output)
	}
}

func TestPhaseCompletedTTY(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, true)

	prog.PhaseCompleted("triage", "low", 40*time.Second, 0.15)

	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Errorf("expected check mark, got %q", output)
	}
	if !strings.Contains(output, "triage") {
		t.Errorf("expected phase name, got %q", output)
	}
	if !strings.Contains(output, "low") {
		t.Errorf("expected summary 'low', got %q", output)
	}
	if !strings.Contains(output, "40s") {
		t.Errorf("expected elapsed time '40s', got %q", output)
	}
	if !strings.Contains(output, "$0.15") {
		t.Errorf("expected cost '$0.15', got %q", output)
	}
}

func TestPhaseCompletedNonTTY(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, false)

	prog.PhaseCompleted("plan", "2 tasks", 53*time.Second, 0.32)

	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Errorf("expected check mark, got %q", output)
	}
	if !strings.Contains(output, "plan") {
		t.Errorf("expected phase name, got %q", output)
	}
	if !strings.Contains(output, "2 tasks") {
		t.Errorf("expected summary, got %q", output)
	}
	if !strings.Contains(output, "53s") {
		t.Errorf("expected elapsed '53s', got %q", output)
	}
	if !strings.Contains(output, "$0.32") {
		t.Errorf("expected cost, got %q", output)
	}
	// Non-TTY should NOT have ANSI escape codes
	if strings.Contains(output, "\033[") {
		t.Errorf("non-TTY output should not have ANSI codes, got %q", output)
	}
}

func TestPhaseFailedTTY(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, true)

	prog.PhaseFailed("verify", "missing ReadBuildInfo()", 95*time.Second)

	output := buf.String()
	if !strings.Contains(output, "✗") {
		t.Errorf("expected X mark, got %q", output)
	}
	if !strings.Contains(output, "verify") {
		t.Errorf("expected phase name, got %q", output)
	}
	if !strings.Contains(output, "missing ReadBuildInfo()") {
		t.Errorf("expected error message, got %q", output)
	}
	if !strings.Contains(output, "1m35s") {
		t.Errorf("expected elapsed '1m35s', got %q", output)
	}
}

func TestPhaseFailedNonTTY(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, false)

	prog.PhaseFailed("verify", "test failure", 60*time.Second)

	output := buf.String()
	if !strings.Contains(output, "✗") {
		t.Errorf("expected X mark, got %q", output)
	}
	if !strings.Contains(output, "test failure") {
		t.Errorf("expected error message, got %q", output)
	}
	if strings.Contains(output, "\033[") {
		t.Errorf("non-TTY output should not have ANSI codes, got %q", output)
	}
}

func TestPhaseSkipped(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, false)

	prog.PhaseSkipped("monitor")

	output := buf.String()
	if !strings.Contains(output, "⏭") {
		t.Errorf("expected skip icon, got %q", output)
	}
	if !strings.Contains(output, "monitor") {
		t.Errorf("expected phase name, got %q", output)
	}
	if !strings.Contains(output, "skipped") {
		t.Errorf("expected 'skipped', got %q", output)
	}
}

func TestCostAccumulation(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, false)

	prog.PhaseCompleted("triage", "", 10*time.Second, 0.15)
	prog.PhaseCompleted("plan", "", 20*time.Second, 0.20)

	output := buf.String()
	// After plan, total cost should be 0.35
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), output)
	}
	if !strings.Contains(lines[0], "$0.15") {
		t.Errorf("first line should show $0.15, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "$0.35") {
		t.Errorf("second line should show $0.35, got %q", lines[1])
	}
}

func TestBudgetWarningNonTTY(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, false)

	prog.BudgetWarning(4.50, 5.00)

	output := buf.String()
	if !strings.Contains(output, "⚠") {
		t.Errorf("expected warning icon, got %q", output)
	}
	if !strings.Contains(output, "$4.50") {
		t.Errorf("expected total cost, got %q", output)
	}
	if !strings.Contains(output, "$5.00") {
		t.Errorf("expected limit, got %q", output)
	}
}

func TestBudgetWarningTTY(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, true)

	prog.BudgetWarning(4.50, 5.00)

	output := buf.String()
	if !strings.Contains(output, "⚠") {
		t.Errorf("expected warning icon, got %q", output)
	}
	if !strings.Contains(output, "$4.50") {
		t.Errorf("expected total cost, got %q", output)
	}
}

func TestMessage(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, false)

	prog.Message("Pipeline started")

	output := buf.String()
	if !strings.Contains(output, "Pipeline started") {
		t.Errorf("expected message, got %q", output)
	}
}

func TestMessageTTY(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, true)

	prog.Message("Created worktree: .worktrees/test (soda/test)")

	output := buf.String()
	if !strings.Contains(output, "Created worktree") {
		t.Errorf("expected message, got %q", output)
	}
}

func TestPhaseRetrying(t *testing.T) {
	var buf safeBuffer
	prog := New(&buf, true)
	prog.TickInterval = 10 * time.Millisecond

	prog.PhaseStarted("triage")
	prog.PhaseRetrying("triage", "transient", 2)
	// Give the spinner time to render with the updated description
	time.Sleep(50 * time.Millisecond)
	prog.stopSpinner()

	output := buf.String()
	if !strings.Contains(output, "retrying") {
		t.Errorf("expected 'retrying' in spinner output, got %q", output)
	}
}

func TestSpinnerFrames(t *testing.T) {
	var buf safeBuffer
	prog := New(&buf, true)
	prog.TickInterval = 10 * time.Millisecond

	prog.PhaseStarted("triage")
	// Wait long enough for multiple frames to be written
	time.Sleep(100 * time.Millisecond)
	prog.stopSpinner()

	output := buf.String()
	// At least one frame character should appear
	foundFrame := false
	for _, frame := range frames {
		if strings.Contains(output, frame) {
			foundFrame = true
			break
		}
	}
	if !foundFrame {
		t.Errorf("expected at least one spinner frame character, got %q", output)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{3 * time.Second, "3s"},
		{45 * time.Second, "45s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m00s"},
		{90 * time.Second, "1m30s"},
		{127 * time.Second, "2m07s"},
		{5*time.Minute + 9*time.Second, "5m09s"},
	}
	for _, tc := range tests {
		got := formatDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestPhaseCompletedNoSummary(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, false)

	prog.PhaseCompleted("triage", "", 10*time.Second, 0.10)

	output := buf.String()
	if strings.Contains(output, " — ") {
		t.Errorf("expected no summary separator when summary is empty, got %q", output)
	}
}

func TestStopSpinnerIdempotent(t *testing.T) {
	var buf bytes.Buffer
	prog := New(&buf, true)

	// Stopping when no spinner is running should not panic
	prog.stopSpinner()
	prog.stopSpinner()
}

func TestPhaseDescriptions(t *testing.T) {
	tests := []struct {
		phase string
		want  string
	}{
		{"triage", "classifying ticket..."},
		{"plan", "designing implementation..."},
		{"implement", "writing code..."},
		{"verify", "checking acceptance criteria..."},
		{"submit", "creating PR..."},
		{"monitor", "monitoring PR..."},
		{"unknown", "running..."},
	}
	for _, tc := range tests {
		got := phaseDescription(tc.phase)
		if got != tc.want {
			t.Errorf("phaseDescription(%q) = %q, want %q", tc.phase, got, tc.want)
		}
	}
}

// safeBuffer is a thread-safe bytes.Buffer for use in spinner tests.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
