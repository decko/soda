package progress

import (
	"encoding/json"
	"testing"
)

func TestTriageSummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "complexity and automatable",
			input: `{"automatable":true,"complexity":"low","ticket_key":"TEST-1"}`,
			want:  "low",
		},
		{
			name:  "not automatable",
			input: `{"automatable":false,"complexity":"high","ticket_key":"TEST-1"}`,
			want:  "high, not automatable",
		},
		{
			name:  "complexity only",
			input: `{"automatable":true,"complexity":"medium"}`,
			want:  "medium",
		},
		{
			name:  "no useful data",
			input: `{"automatable":true}`,
			want:  "",
		},
		{
			name:  "invalid json",
			input: `{broken`,
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PhaseSummary("triage", json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("PhaseSummary(triage, %s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestPlanSummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "multiple tasks",
			input: `{"tasks":[{"id":"1"},{"id":"2"},{"id":"3"}]}`,
			want:  "3 tasks",
		},
		{
			name:  "single task",
			input: `{"tasks":[{"id":"1"}]}`,
			want:  "1 task",
		},
		{
			name:  "no tasks",
			input: `{"tasks":[]}`,
			want:  "no tasks",
		},
		{
			name:  "missing tasks field",
			input: `{}`,
			want:  "no tasks",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PhaseSummary("plan", json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("PhaseSummary(plan, %s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestImplementSummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "files and commits",
			input: `{"files_changed":[{"path":"a.go"},{"path":"b.go"},{"path":"c.go"}],"commits":[{"hash":"abc"},{"hash":"def"}],"tests_passed":true}`,
			want:  "3 files changed, 2 commits",
		},
		{
			name:  "single file single commit",
			input: `{"files_changed":[{"path":"a.go"}],"commits":[{"hash":"abc"}]}`,
			want:  "1 file changed, 1 commit",
		},
		{
			name:  "only tests passed",
			input: `{"tests_passed":true}`,
			want:  "tests passed",
		},
		{
			name:  "empty result",
			input: `{}`,
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PhaseSummary("implement", json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("PhaseSummary(implement, %s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestVerifySummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "pass verdict",
			input: `{"verdict":"pass"}`,
			want:  "PASS",
		},
		{
			name:  "fail verdict",
			input: `{"verdict":"FAIL"}`,
			want:  "FAIL",
		},
		{
			name:  "no verdict",
			input: `{}`,
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PhaseSummary("verify", json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("PhaseSummary(verify, %s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSubmitSummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "github PR URL",
			input: `{"pr_url":"https://github.com/decko/soda/pull/49"}`,
			want:  "PR #49",
		},
		{
			name:  "no PR URL",
			input: `{}`,
			want:  "",
		},
		{
			name:  "non-standard URL",
			input: `{"pr_url":"https://gitlab.com/group/repo/-/merge_requests/42"}`,
			want:  "https://gitlab.com/group/repo/-/merge_requests/42",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PhaseSummary("submit", json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("PhaseSummary(submit, %s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMonitorSummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "multiple comments handled",
			input: `{"comments_handled":[{"id":"IC_1"},{"id":"IC_2"},{"id":"IC_3"}]}`,
			want:  "3 comments handled",
		},
		{
			name:  "single comment handled",
			input: `{"comments_handled":[{"id":"IC_1"}]}`,
			want:  "1 comment handled",
		},
		{
			name:  "no comments handled",
			input: `{"comments_handled":[]}`,
			want:  "no comments handled",
		},
		{
			name:  "missing comments_handled field",
			input: `{}`,
			want:  "no comments handled",
		},
		{
			name:  "invalid json",
			input: `{broken`,
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PhaseSummary("monitor", json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("PhaseSummary(monitor, %s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestPhaseSummaryUnknownPhase(t *testing.T) {
	got := PhaseSummary("unknown", json.RawMessage(`{"foo":"bar"}`))
	if got != "" {
		t.Errorf("expected empty summary for unknown phase, got %q", got)
	}
}

func TestPhaseSummaryEmptyResult(t *testing.T) {
	got := PhaseSummary("triage", nil)
	if got != "" {
		t.Errorf("expected empty summary for nil result, got %q", got)
	}
}
