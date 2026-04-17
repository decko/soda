package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/schemas"
)

func TestDurationUnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"minutes", "3m", 3 * time.Minute, false},
		{"hours", "4h", 4 * time.Hour, false},
		{"seconds", "30s", 30 * time.Second, false},
		{"compound", "1h30m", 90 * time.Minute, false},
		{"invalid", "bogus", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dur Duration
			err := dur.UnmarshalYAML(func(v interface{}) error {
				ptr := v.(*string)
				*ptr = tt.input
				return nil
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && dur.Duration != tt.want {
				t.Errorf("Duration = %v, want %v", dur.Duration, tt.want)
			}
		})
	}
}

func TestLoadPipeline(t *testing.T) {
	t.Run("loads_real_phases_yaml", func(t *testing.T) {
		// Use the project's actual phases.yaml
		pipeline, err := LoadPipeline("../../phases.yaml")
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if len(pipeline.Phases) != 7 {
			t.Fatalf("got %d phases, want 7", len(pipeline.Phases))
		}

		// Verify first phase
		triage := pipeline.Phases[0]
		if triage.Name != "triage" {
			t.Errorf("first phase = %q, want %q", triage.Name, "triage")
		}
		if triage.Timeout.Duration != 3*time.Minute {
			t.Errorf("triage timeout = %v, want 3m", triage.Timeout.Duration)
		}
		if triage.Retry.Transient != 2 {
			t.Errorf("triage retry.transient = %d, want 2", triage.Retry.Transient)
		}
		if len(triage.DependsOn) != 0 {
			t.Errorf("triage depends_on = %v, want empty", triage.DependsOn)
		}

		// Verify dependency chain
		plan := pipeline.Phases[1]
		if len(plan.DependsOn) != 1 || plan.DependsOn[0] != "triage" {
			t.Errorf("plan depends_on = %v, want [triage]", plan.DependsOn)
		}

		// Verify review phase
		review := pipeline.Phases[4]
		if review.Name != "review" {
			t.Errorf("fifth phase = %q, want %q", review.Name, "review")
		}
		if review.Type != "parallel-review" {
			t.Errorf("review type = %q, want %q", review.Type, "parallel-review")
		}
		if len(review.Reviewers) != 2 {
			t.Errorf("review has %d reviewers, want 2", len(review.Reviewers))
		}
		if len(review.Reviewers) >= 2 {
			if review.Reviewers[0].Name != "go-specialist" {
				t.Errorf("first reviewer = %q, want %q", review.Reviewers[0].Name, "go-specialist")
			}
			if review.Reviewers[1].Name != "ai-harness" {
				t.Errorf("second reviewer = %q, want %q", review.Reviewers[1].Name, "ai-harness")
			}
		}

		// Verify review phase has rework config
		if review.Rework == nil {
			t.Fatal("review rework config should not be nil")
		}
		if review.Rework.Target != "implement" {
			t.Errorf("review rework target = %q, want %q", review.Rework.Target, "implement")
		}
		if len(review.Rework.FeedbackFrom) != 2 {
			t.Fatalf("review rework feedback_from has %d entries, want 2", len(review.Rework.FeedbackFrom))
		}
		if review.Rework.FeedbackFrom[0] != "review" {
			t.Errorf("review rework feedback_from[0] = %q, want %q", review.Rework.FeedbackFrom[0], "review")
		}
		if review.Rework.FeedbackFrom[1] != "verify" {
			t.Errorf("review rework feedback_from[1] = %q, want %q", review.Rework.FeedbackFrom[1], "verify")
		}

		// Verify monitor phase has polling config
		monitor := pipeline.Phases[6]
		if monitor.Name != "monitor" {
			t.Errorf("last phase = %q, want %q", monitor.Name, "monitor")
		}
		if monitor.Type != "polling" {
			t.Errorf("monitor type = %q, want %q", monitor.Type, "polling")
		}
		if monitor.Polling == nil {
			t.Fatal("monitor polling config should not be nil")
		}
		if monitor.Polling.MaxResponseRounds != 3 {
			t.Errorf("monitor max_response_rounds = %d, want 3", monitor.Polling.MaxResponseRounds)
		}
		if monitor.Polling.MaxDuration.Duration != 4*time.Hour {
			t.Errorf("monitor max_duration = %v, want 4h", monitor.Polling.MaxDuration.Duration)
		}
	})

	t.Run("resolves_generated_schemas", func(t *testing.T) {
		pipeline, err := LoadPipeline("../../phases.yaml")
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		// All phases should have schemas resolved from the generated constants.
		for _, phase := range pipeline.Phases {
			if strings.TrimSpace(phase.Schema) == "" {
				t.Errorf("phase %q has empty schema after resolution", phase.Name)
				continue
			}
			want := schemas.SchemaFor(phase.Name)
			if want == "" {
				t.Errorf("schemas.SchemaFor(%q) returned empty — phase not registered", phase.Name)
				continue
			}
			if phase.Schema != want {
				t.Errorf("phase %q schema does not match generated schema", phase.Name)
			}
		}
	})

	t.Run("inline_schema_overrides_generated", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: triage
    prompt: prompts/triage.md
    schema: '{"type":"object","properties":{"custom":{"type":"string"}}}'
    timeout: 1m
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		// Inline schema should NOT be overwritten by the generated one.
		got := pipeline.Phases[0].Schema
		want := `{"type":"object","properties":{"custom":{"type":"string"}}}`
		if got != want {
			t.Errorf("inline schema was overridden:\ngot:  %s\nwant: %s", got, want)
		}
	})

	t.Run("unknown_phase_gets_no_schema", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: custom-phase
    prompt: prompts/custom.md
    timeout: 1m
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if pipeline.Phases[0].Schema != "" {
			t.Errorf("unknown phase got schema: %q", pipeline.Phases[0].Schema)
		}
	})

	t.Run("errors_on_missing_file", func(t *testing.T) {
		_, err := LoadPipeline("/nonexistent/phases.yaml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("errors_on_invalid_yaml", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.yaml")
		if err := os.WriteFile(path, []byte("not: [valid: yaml: {{{"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid yaml")
		}
	})
}
