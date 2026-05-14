package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/pipeline"
)

func TestRunCost_Empty(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := runCost(nil)

	w.Close()
	os.Stdout = oldStdout

	var buf strings.Builder
	data := make([]byte, 4096)
	for {
		n, readErr := r.Read(data)
		if n > 0 {
			buf.Write(data[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()

	if runErr != nil {
		t.Fatalf("runCost error: %v", runErr)
	}
	if !strings.Contains(buf.String(), "No cost entries found") {
		t.Errorf("expected 'No cost entries found', got: %s", buf.String())
	}
}

func TestRunCost_WithEntries(t *testing.T) {
	ts := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	entries := []pipeline.CostEntry{
		{Ticket: "PROJ-123", Timestamp: ts, Cost: 1.2345, Success: true},
		{Ticket: "PROJ-456", Timestamp: ts, Cost: 2.5000, Success: false},
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := runCost(entries)

	w.Close()
	os.Stdout = oldStdout

	var buf strings.Builder
	data := make([]byte, 4096)
	for {
		n, readErr := r.Read(data)
		if n > 0 {
			buf.Write(data[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()

	if runErr != nil {
		t.Fatalf("runCost error: %v", runErr)
	}

	output := buf.String()
	if !strings.Contains(output, "PROJ-123") {
		t.Errorf("output missing PROJ-123:\n%s", output)
	}
	if !strings.Contains(output, "PROJ-456") {
		t.Errorf("output missing PROJ-456:\n%s", output)
	}
	if !strings.Contains(output, "success") {
		t.Errorf("output missing 'success':\n%s", output)
	}
	if !strings.Contains(output, "failed") {
		t.Errorf("output missing 'failed':\n%s", output)
	}
	// Total should be $3.7345
	if !strings.Contains(output, "Total:") {
		t.Errorf("output missing 'Total:':\n%s", output)
	}
	if !strings.Contains(output, "2 run(s)") {
		t.Errorf("output missing '2 run(s)':\n%s", output)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns the captured output.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wr

	fnErr := fn()

	wr.Close()
	os.Stdout = oldStdout

	var buf strings.Builder
	data := make([]byte, 4096)
	for {
		n, readErr := rd.Read(data)
		if n > 0 {
			buf.Write(data[:n])
		}
		if readErr != nil {
			break
		}
	}
	rd.Close()
	return buf.String(), fnErr
}

func TestRunCostByComplexity_Empty(t *testing.T) {
	output, err := captureStdout(t, func() error {
		return runCostByComplexity(nil)
	})
	if err != nil {
		t.Fatalf("runCostByComplexity error: %v", err)
	}
	if !strings.Contains(output, "No cost entries found") {
		t.Errorf("expected 'No cost entries found', got: %s", output)
	}
}

func TestRunCostByComplexity_WithEntries(t *testing.T) {
	entries := []pipeline.CostEntry{
		{Ticket: "T-1", Cost: 2.00, Complexity: "low"},
		{Ticket: "T-2", Cost: 4.00, Complexity: "low"},
		{Ticket: "T-3", Cost: 10.00, Complexity: "high"},
		{Ticket: "T-4", Cost: 6.00, Complexity: "medium"},
	}

	output, err := captureStdout(t, func() error {
		return runCostByComplexity(entries)
	})
	if err != nil {
		t.Fatalf("runCostByComplexity error: %v", err)
	}

	// Check all bands are present.
	if !strings.Contains(output, "LOW") {
		t.Errorf("output missing LOW:\n%s", output)
	}
	if !strings.Contains(output, "MEDIUM") {
		t.Errorf("output missing MEDIUM:\n%s", output)
	}
	if !strings.Contains(output, "HIGH") {
		t.Errorf("output missing HIGH:\n%s", output)
	}

	// Check header.
	if !strings.Contains(output, "COMPLEXITY") {
		t.Errorf("output missing COMPLEXITY header:\n%s", output)
	}
	if !strings.Contains(output, "SESSIONS") {
		t.Errorf("output missing SESSIONS header:\n%s", output)
	}
	if !strings.Contains(output, "MEAN") {
		t.Errorf("output missing MEAN header:\n%s", output)
	}
	if !strings.Contains(output, "MEDIAN") {
		t.Errorf("output missing MEDIAN header:\n%s", output)
	}

	// Check footer total.
	if !strings.Contains(output, "4 session(s)") {
		t.Errorf("output missing '4 session(s)':\n%s", output)
	}
	if !strings.Contains(output, "$22.00") {
		t.Errorf("output missing total $22.00:\n%s", output)
	}
}

func TestRunCostByComplexity_UnknownBand(t *testing.T) {
	entries := []pipeline.CostEntry{
		{Ticket: "T-1", Cost: 5.00}, // no complexity → UNKNOWN
	}

	output, err := captureStdout(t, func() error {
		return runCostByComplexity(entries)
	})
	if err != nil {
		t.Fatalf("runCostByComplexity error: %v", err)
	}

	if !strings.Contains(output, "UNKNOWN") {
		t.Errorf("output missing UNKNOWN:\n%s", output)
	}
}

func TestRunCostByOutcome_Empty(t *testing.T) {
	output, err := captureStdout(t, func() error {
		return runCostByOutcome(nil)
	})
	if err != nil {
		t.Fatalf("runCostByOutcome error: %v", err)
	}
	if !strings.Contains(output, "No cost entries found") {
		t.Errorf("expected 'No cost entries found', got: %s", output)
	}
}

func TestRunCostByOutcome_Headers(t *testing.T) {
	entries := []pipeline.CostEntry{
		{Ticket: "T-1", Cost: 1.00, Success: true, DurationMs: 5000},
	}

	output, err := captureStdout(t, func() error {
		return runCostByOutcome(entries)
	})
	if err != nil {
		t.Fatalf("runCostByOutcome error: %v", err)
	}

	for _, header := range []string{"OUTCOME", "SESSIONS", "MEAN", "MEDIAN", "MEAN DUR", "TOTAL"} {
		if !strings.Contains(output, header) {
			t.Errorf("output missing %q header:\n%s", header, output)
		}
	}
}

func TestRunCostByOutcome_RowOrder(t *testing.T) {
	entries := []pipeline.CostEntry{
		{Ticket: "T-1", Cost: 1.00, Success: false},                                  // failed
		{Ticket: "T-2", Cost: 2.00, Success: true},                                   // clean
		{Ticket: "T-3", Cost: 3.00, Success: true, ReworkCycles: 1},                  // rework_1
		{Ticket: "T-4", Cost: 4.00, Success: true, PatchCycles: 1},                   // patched
		{Ticket: "T-5", Cost: 5.00, Success: true, ReworkCycles: 3, Escalated: true}, // rework_2+
	}

	output, err := captureStdout(t, func() error {
		return runCostByOutcome(entries)
	})
	if err != nil {
		t.Fatalf("runCostByOutcome error: %v", err)
	}

	// Extract non-header, non-footer data lines.
	lines := strings.Split(output, "\n")
	var dataLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "OUTCOME") || strings.HasPrefix(trimmed, "Total:") || strings.HasPrefix(trimmed, "Rework tax:") {
			continue
		}
		dataLines = append(dataLines, trimmed)
	}

	expectedOrder := []string{"FIRST_PASS", "PATCHED", "REWORK_1", "REWORK_2+", "FAILED"}
	if len(dataLines) != len(expectedOrder) {
		t.Fatalf("expected %d data lines, got %d:\n%s", len(expectedOrder), len(dataLines), output)
	}
	for idx, expected := range expectedOrder {
		if !strings.HasPrefix(dataLines[idx], expected) {
			t.Errorf("data line %d: expected prefix %q, got %q", idx, expected, dataLines[idx])
		}
	}
}

func TestRunCostByOutcome_ReworkTaxPresent(t *testing.T) {
	entries := []pipeline.CostEntry{
		{Ticket: "T-1", Cost: 2.00, Success: true},                  // clean
		{Ticket: "T-2", Cost: 4.00, Success: true},                  // clean
		{Ticket: "T-3", Cost: 6.00, Success: true, ReworkCycles: 1}, // rework_1
	}

	output, err := captureStdout(t, func() error {
		return runCostByOutcome(entries)
	})
	if err != nil {
		t.Fatalf("runCostByOutcome error: %v", err)
	}

	if !strings.Contains(output, "Rework tax:") {
		t.Errorf("expected 'Rework tax:' line in output:\n%s", output)
	}
	// Clean mean = 3.00, rework mean = 6.00, tax = 100%
	if !strings.Contains(output, "100%") {
		t.Errorf("expected 100%% rework tax, got:\n%s", output)
	}
}

func TestRunCostByOutcome_ReworkTaxAbsent(t *testing.T) {
	entries := []pipeline.CostEntry{
		{Ticket: "T-1", Cost: 2.00, Success: true}, // clean only
		{Ticket: "T-2", Cost: 4.00, Success: true}, // clean only
	}

	output, err := captureStdout(t, func() error {
		return runCostByOutcome(entries)
	})
	if err != nil {
		t.Fatalf("runCostByOutcome error: %v", err)
	}

	if strings.Contains(output, "Rework tax:") {
		t.Errorf("should not show rework tax when no rework entries exist:\n%s", output)
	}
}

func TestRunCostByOutcomeAndComplexity_Empty(t *testing.T) {
	output, err := captureStdout(t, func() error {
		return runCostByOutcomeAndComplexity(nil)
	})
	if err != nil {
		t.Fatalf("runCostByOutcomeAndComplexity error: %v", err)
	}
	if !strings.Contains(output, "No cost entries found") {
		t.Errorf("expected 'No cost entries found', got: %s", output)
	}
}

func TestRunCostByOutcomeAndComplexity_MixedEntries(t *testing.T) {
	entries := []pipeline.CostEntry{
		{Ticket: "T-1", Cost: 2.00, Success: true, Complexity: "low"},
		{Ticket: "T-2", Cost: 4.00, Success: true, Complexity: "high"},
		{Ticket: "T-3", Cost: 6.00, Success: true, ReworkCycles: 1, Complexity: "low"},
		{Ticket: "T-4", Cost: 8.00, Success: false, Complexity: "high"},
	}

	output, err := captureStdout(t, func() error {
		return runCostByOutcomeAndComplexity(entries)
	})
	if err != nil {
		t.Fatalf("runCostByOutcomeAndComplexity error: %v", err)
	}

	// Check header has OUTCOME and complexity columns.
	if !strings.Contains(output, "OUTCOME") {
		t.Errorf("output missing OUTCOME header:\n%s", output)
	}
	if !strings.Contains(output, "LOW") {
		t.Errorf("output missing LOW column:\n%s", output)
	}
	if !strings.Contains(output, "HIGH") {
		t.Errorf("output missing HIGH column:\n%s", output)
	}

	// Check outcome rows.
	if !strings.Contains(output, "FIRST_PASS") {
		t.Errorf("output missing FIRST_PASS row:\n%s", output)
	}
	if !strings.Contains(output, "REWORK_1") {
		t.Errorf("output missing REWORK_1 row:\n%s", output)
	}
	if !strings.Contains(output, "FAILED") {
		t.Errorf("output missing FAILED row:\n%s", output)
	}

	// Absent cells should have "—".
	if !strings.Contains(output, "—") {
		t.Errorf("expected dash (—) for absent cells:\n%s", output)
	}
}

func TestRunCostByComplexity_BandOrdering(t *testing.T) {
	entries := []pipeline.CostEntry{
		{Ticket: "T-1", Cost: 1.00, Complexity: "high"},
		{Ticket: "T-2", Cost: 2.00, Complexity: "low"},
		{Ticket: "T-3", Cost: 3.00, Complexity: "medium"},
		{Ticket: "T-4", Cost: 4.00},                               // unknown
		{Ticket: "T-5", Cost: 5.00, Complexity: "custom-special"}, // alphabetical
	}

	output, err := captureStdout(t, func() error {
		return runCostByComplexity(entries)
	})
	if err != nil {
		t.Fatalf("runCostByComplexity error: %v", err)
	}

	// Verify order: LOW before MEDIUM before HIGH before CUSTOM-SPECIAL before UNKNOWN.
	lines := strings.Split(output, "\n")
	var bandLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "COMPLEXITY") || strings.HasPrefix(trimmed, "Total:") {
			continue
		}
		bandLines = append(bandLines, trimmed)
	}

	if len(bandLines) < 5 {
		t.Fatalf("expected 5 band lines, got %d:\n%s", len(bandLines), output)
	}

	expectedOrder := []string{"LOW", "MEDIUM", "HIGH", "CUSTOM-SPECIAL", "UNKNOWN"}
	for idx, expected := range expectedOrder {
		if !strings.HasPrefix(bandLines[idx], expected) {
			t.Errorf("band line %d: expected prefix %q, got %q", idx, expected, bandLines[idx])
		}
	}
}
