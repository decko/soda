package main

import (
	"encoding/json"
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
