package main

import (
	"testing"

	"github.com/decko/soda/internal/pipeline"
)

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
