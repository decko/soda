package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/pipeline"
)

func TestRunCost_Empty(t *testing.T) {
	dir := t.TempDir()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := runCost(dir)

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
	dir := t.TempDir()

	ts := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	if err := pipeline.AppendCostEntry(dir, pipeline.CostEntry{
		Ticket:    "PROJ-123",
		Timestamp: ts,
		Cost:      1.2345,
		Success:   true,
	}); err != nil {
		t.Fatalf("AppendCostEntry: %v", err)
	}
	if err := pipeline.AppendCostEntry(dir, pipeline.CostEntry{
		Ticket:    "PROJ-456",
		Timestamp: ts,
		Cost:      2.5000,
		Success:   false,
	}); err != nil {
		t.Fatalf("AppendCostEntry: %v", err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := runCost(dir)

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
