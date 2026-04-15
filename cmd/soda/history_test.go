package main

import (
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
