package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

	t.Run("errors_on_missing_file", func(t *testing.T) {
		_, err := LoadPipeline("/nonexistent/phases.yaml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("errors_on_invalid_yaml", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.yaml")
		os.WriteFile(path, []byte("not: [valid: yaml: {{{"), 0644)

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid yaml")
		}
	})
}
