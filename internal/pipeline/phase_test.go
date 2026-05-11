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

		if len(pipeline.Phases) != 8 {
			t.Fatalf("got %d phases, want 8", len(pipeline.Phases))
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

		// Verify patch phase
		patch := pipeline.Phases[3]
		if patch.Name != "patch" {
			t.Errorf("fourth phase = %q, want %q", patch.Name, "patch")
		}
		if patch.Type != "corrective" {
			t.Errorf("patch type = %q, want %q", patch.Type, "corrective")
		}
		if len(patch.FeedbackFrom) != 1 || patch.FeedbackFrom[0] != "verify" {
			t.Errorf("patch feedback_from = %v, want [verify]", patch.FeedbackFrom)
		}

		// Verify verify phase has corrective config
		verify := pipeline.Phases[4]
		if verify.Name != "verify" {
			t.Errorf("fifth phase = %q, want %q", verify.Name, "verify")
		}
		if verify.Corrective == nil {
			t.Fatal("verify corrective config should not be nil")
		}
		if verify.Corrective.Phase != "patch" {
			t.Errorf("verify corrective.phase = %q, want %q", verify.Corrective.Phase, "patch")
		}
		if verify.Corrective.MaxAttempts != 2 {
			t.Errorf("verify corrective.max_attempts = %d, want 2", verify.Corrective.MaxAttempts)
		}
		if verify.Corrective.OnExhausted != "stop" {
			t.Errorf("verify corrective.on_exhausted = %q, want %q", verify.Corrective.OnExhausted, "stop")
		}

		// Verify review phase
		review := pipeline.Phases[5]
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

		// Verify review phase has min_reviewers config
		if review.MinReviewers != 1 {
			t.Errorf("review min_reviewers = %d, want 1", review.MinReviewers)
		}

		// Verify review phase has rework config
		if review.Rework == nil {
			t.Fatal("review rework config should not be nil")
		}
		if review.Rework.Target != "implement" {
			t.Errorf("review rework target = %q, want %q", review.Rework.Target, "implement")
		}

		// Verify implement phase has feedback_from config
		implement := pipeline.Phases[2]
		if implement.Name != "implement" {
			t.Errorf("third phase = %q, want %q", implement.Name, "implement")
		}
		if len(implement.FeedbackFrom) != 2 {
			t.Fatalf("implement feedback_from has %d entries, want 2", len(implement.FeedbackFrom))
		}
		if implement.FeedbackFrom[0] != "review" {
			t.Errorf("implement feedback_from[0] = %q, want %q", implement.FeedbackFrom[0], "review")
		}
		if implement.FeedbackFrom[1] != "verify" {
			t.Errorf("implement feedback_from[1] = %q, want %q", implement.FeedbackFrom[1], "verify")
		}

		// Verify monitor phase has polling config
		monitor := pipeline.Phases[7]
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

	t.Run("errors_on_invalid_rework_target", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: implement
    prompt: prompts/implement.md
    timeout: 5m
  - name: review
    prompt: prompts/review.md
    timeout: 5m
    rework:
      target: nonexistent
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid rework target")
		}
		if !strings.Contains(err.Error(), "rework target") {
			t.Errorf("error = %q, want mention of rework target", err)
		}
		if !strings.Contains(err.Error(), "nonexistent") {
			t.Errorf("error = %q, want mention of %q", err, "nonexistent")
		}
	})

	t.Run("errors_on_invalid_feedback_from", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: implement
    prompt: prompts/implement.md
    timeout: 5m
    feedback_from:
      - nonexistent
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid feedback_from")
		}
		if !strings.Contains(err.Error(), "feedback_from") {
			t.Errorf("error = %q, want mention of feedback_from", err)
		}
		if !strings.Contains(err.Error(), "nonexistent") {
			t.Errorf("error = %q, want mention of %q", err, "nonexistent")
		}
	})

	t.Run("valid_rework_target_and_feedback_from", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: implement
    prompt: prompts/implement.md
    timeout: 5m
    feedback_from:
      - review
  - name: review
    prompt: prompts/review.md
    timeout: 5m
    rework:
      target: implement
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pipeline.Phases) != 2 {
			t.Errorf("got %d phases, want 2", len(pipeline.Phases))
		}
	})

	t.Run("errors_on_invalid_corrective_phase", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: verify
    prompt: prompts/verify.md
    timeout: 5m
    corrective:
      phase: nonexistent
      max_attempts: 2
      on_exhausted: stop
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid corrective.phase")
		}
		if !strings.Contains(err.Error(), "corrective.phase") {
			t.Errorf("error = %q, want mention of corrective.phase", err)
		}
		if !strings.Contains(err.Error(), "nonexistent") {
			t.Errorf("error = %q, want mention of %q", err, "nonexistent")
		}
	})

	t.Run("errors_on_invalid_corrective_escalate_to", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: patch
    prompt: prompts/patch.md
    timeout: 5m
  - name: verify
    prompt: prompts/verify.md
    timeout: 5m
    corrective:
      phase: patch
      max_attempts: 2
      on_exhausted: escalate
      escalate_to: nonexistent
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid corrective.escalate_to")
		}
		if !strings.Contains(err.Error(), "corrective.escalate_to") {
			t.Errorf("error = %q, want mention of corrective.escalate_to", err)
		}
		if !strings.Contains(err.Error(), "nonexistent") {
			t.Errorf("error = %q, want mention of %q", err, "nonexistent")
		}
	})

	t.Run("min_reviewers_parsed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: review
    type: parallel-review
    prompt: prompts/review.md
    timeout: 5m
    min_reviewers: 1
    reviewers:
      - name: a
        prompt: prompts/a.md
        focus: "test"
      - name: b
        prompt: prompts/b.md
        focus: "test"
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if pipeline.Phases[0].MinReviewers != 1 {
			t.Errorf("min_reviewers = %d, want 1", pipeline.Phases[0].MinReviewers)
		}
	})

	t.Run("min_reviewers_defaults_to_zero", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: review
    type: parallel-review
    prompt: prompts/review.md
    timeout: 5m
    reviewers:
      - name: a
        prompt: prompts/a.md
        focus: "test"
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if pipeline.Phases[0].MinReviewers != 0 {
			t.Errorf("min_reviewers = %d, want 0 (omitted)", pipeline.Phases[0].MinReviewers)
		}
	})

	t.Run("errors_on_min_reviewers_exceeds_reviewers", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: review
    type: parallel-review
    prompt: prompts/review.md
    timeout: 5m
    min_reviewers: 3
    reviewers:
      - name: a
        prompt: prompts/a.md
        focus: "test"
      - name: b
        prompt: prompts/b.md
        focus: "test"
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error when min_reviewers exceeds number of reviewers")
		}
		if !strings.Contains(err.Error(), "min_reviewers") {
			t.Errorf("error = %q, want mention of min_reviewers", err)
		}
		if !strings.Contains(err.Error(), "3") {
			t.Errorf("error = %q, want mention of min_reviewers value", err)
		}
		if !strings.Contains(err.Error(), "2") {
			t.Errorf("error = %q, want mention of reviewer count", err)
		}
	})

	t.Run("valid_reviewer_condition", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: review
    type: parallel-review
    prompt: prompts/review.md
    timeout: 5m
    reviewers:
      - name: a
        prompt: prompts/a.md
        focus: "test"
        condition: '{{ ne .Complexity "low" }}'
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pipeline.Phases[0].Reviewers[0].Condition != `{{ ne .Complexity "low" }}` {
			t.Errorf("condition = %q, want template string", pipeline.Phases[0].Reviewers[0].Condition)
		}
	})

	t.Run("valid_phase_condition", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: plan
    prompt: prompts/plan.md
    timeout: 5m
    condition: '{{ ne .Complexity "small" }}'
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pipeline.Phases[0].Condition != `{{ ne .Complexity "small" }}` {
			t.Errorf("condition = %q, want template string", pipeline.Phases[0].Condition)
		}
	})

	t.Run("errors_on_invalid_phase_condition", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: plan
    prompt: prompts/plan.md
    timeout: 5m
    condition: '{{ invalid {{ syntax }}'
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid phase condition template")
		}
		if !strings.Contains(err.Error(), "invalid condition template") {
			t.Errorf("error = %q, want mention of invalid condition template", err)
		}
		if !strings.Contains(err.Error(), "plan") {
			t.Errorf("error = %q, want mention of phase name", err)
		}
	})

	t.Run("errors_on_invalid_reviewer_condition", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: review
    type: parallel-review
    prompt: prompts/review.md
    timeout: 5m
    reviewers:
      - name: a
        prompt: prompts/a.md
        focus: "test"
        condition: '{{ invalid {{ syntax }}'
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid reviewer condition template")
		}
		if !strings.Contains(err.Error(), "invalid condition template") {
			t.Errorf("error = %q, want mention of invalid condition template", err)
		}
		if !strings.Contains(err.Error(), "review") {
			t.Errorf("error = %q, want mention of phase name", err)
		}
	})

	t.Run("errors_on_invalid_depends_on", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: triage
    prompt: prompts/triage.md
    timeout: 1m
  - name: plan
    prompt: prompts/plan.md
    timeout: 5m
    depends_on:
      - triage
      - nonexistent
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid depends_on")
		}
		if !strings.Contains(err.Error(), "depends_on") {
			t.Errorf("error = %q, want mention of depends_on", err)
		}
		if !strings.Contains(err.Error(), "nonexistent") {
			t.Errorf("error = %q, want mention of %q", err, "nonexistent")
		}
	})

	t.Run("valid_depends_on", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: triage
    prompt: prompts/triage.md
    timeout: 1m
  - name: plan
    prompt: prompts/plan.md
    timeout: 5m
    depends_on:
      - triage
  - name: implement
    prompt: prompts/implement.md
    timeout: 10m
    depends_on:
      - triage
      - plan
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pipeline.Phases) != 3 {
			t.Errorf("got %d phases, want 3", len(pipeline.Phases))
		}
	})

	t.Run("valid_corrective_config", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: implement
    prompt: prompts/implement.md
    timeout: 5m
  - name: patch
    prompt: prompts/patch.md
    timeout: 5m
  - name: verify
    prompt: prompts/verify.md
    timeout: 5m
    corrective:
      phase: patch
      max_attempts: 2
      on_exhausted: escalate
      escalate_to: implement
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pipeline.Phases) != 3 {
			t.Errorf("got %d phases, want 3", len(pipeline.Phases))
		}
	})

	t.Run("schema_file_loaded", func(t *testing.T) {
		dir := t.TempDir()
		schemaContent := `{"type":"object","properties":{"verdict":{"type":"string"}}}`
		schemaPath := filepath.Join(dir, "custom.json")
		if err := os.WriteFile(schemaPath, []byte(schemaContent), 0644); err != nil {
			t.Fatalf("WriteFile schema: %v", err)
		}

		phasesPath := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: custom-phase
    prompt: prompts/custom.md
    schema: custom.json
    timeout: 1m
`
		if err := os.WriteFile(phasesPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile phases: %v", err)
		}

		pipeline, err := LoadPipeline(phasesPath)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if pipeline.Phases[0].Schema != schemaContent {
			t.Errorf("schema mismatch:\ngot:  %s\nwant: %s", pipeline.Phases[0].Schema, schemaContent)
		}
	})

	t.Run("schema_file_relative_path", func(t *testing.T) {
		dir := t.TempDir()
		subdir := filepath.Join(dir, "schemas")
		if err := os.MkdirAll(subdir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		schemaContent := `{"type":"object","required":["action"]}`
		if err := os.WriteFile(filepath.Join(subdir, "act.json"), []byte(schemaContent), 0644); err != nil {
			t.Fatalf("WriteFile schema: %v", err)
		}

		phasesPath := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: act
    prompt: prompts/act.md
    schema: schemas/act.json
    timeout: 1m
`
		if err := os.WriteFile(phasesPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile phases: %v", err)
		}

		pipeline, err := LoadPipeline(phasesPath)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if pipeline.Phases[0].Schema != schemaContent {
			t.Errorf("schema mismatch:\ngot:  %s\nwant: %s", pipeline.Phases[0].Schema, schemaContent)
		}
	})

	t.Run("schema_file_overrides_generated", func(t *testing.T) {
		dir := t.TempDir()
		schemaContent := `{"type":"object","properties":{"custom_triage":{"type":"boolean"}}}`
		if err := os.WriteFile(filepath.Join(dir, "triage.json"), []byte(schemaContent), 0644); err != nil {
			t.Fatalf("WriteFile schema: %v", err)
		}

		phasesPath := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: triage
    prompt: prompts/triage.md
    schema: triage.json
    timeout: 1m
`
		if err := os.WriteFile(phasesPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile phases: %v", err)
		}

		pipeline, err := LoadPipeline(phasesPath)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		// The file schema should override the generated triage schema.
		if pipeline.Phases[0].Schema != schemaContent {
			t.Errorf("file schema should override generated:\ngot:  %s\nwant: %s", pipeline.Phases[0].Schema, schemaContent)
		}
		if pipeline.Phases[0].Schema == schemas.SchemaFor("triage") {
			t.Error("schema was not overridden — still matches generated triage schema")
		}
	})

	t.Run("errors_on_missing_schema_file", func(t *testing.T) {
		dir := t.TempDir()
		phasesPath := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: custom-phase
    prompt: prompts/custom.md
    schema: nonexistent.json
    timeout: 1m
`
		if err := os.WriteFile(phasesPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(phasesPath)
		if err == nil {
			t.Fatal("expected error for missing schema file")
		}
		if !strings.Contains(err.Error(), "read schema file") {
			t.Errorf("error = %q, want mention of read schema file", err)
		}
		if !strings.Contains(err.Error(), "nonexistent.json") {
			t.Errorf("error = %q, want mention of nonexistent.json", err)
		}
	})

	t.Run("errors_on_invalid_json_schema_file", func(t *testing.T) {
		dir := t.TempDir()
		schemaPath := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(schemaPath, []byte("not valid json{{{"), 0644); err != nil {
			t.Fatalf("WriteFile schema: %v", err)
		}

		phasesPath := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: custom-phase
    prompt: prompts/custom.md
    schema: bad.json
    timeout: 1m
`
		if err := os.WriteFile(phasesPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(phasesPath)
		if err == nil {
			t.Fatal("expected error for invalid JSON schema file")
		}
		if !strings.Contains(err.Error(), "not valid JSON") {
			t.Errorf("error = %q, want mention of not valid JSON", err)
		}
		if !strings.Contains(err.Error(), "bad.json") {
			t.Errorf("error = %q, want mention of bad.json", err)
		}
	})

	t.Run("errors_on_schema_path_traversal", func(t *testing.T) {
		dir := t.TempDir()
		phasesPath := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: custom-phase
    prompt: prompts/custom.md
    schema: ../../etc/passwd
    timeout: 1m
`
		if err := os.WriteFile(phasesPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(phasesPath)
		if err == nil {
			t.Fatal("expected error for path traversal in schema")
		}
		if !strings.Contains(err.Error(), "path traversal rejected") {
			t.Errorf("error = %q, want mention of path traversal rejected", err)
		}
	})

	t.Run("inline_json_schema_not_treated_as_file", func(t *testing.T) {
		dir := t.TempDir()
		phasesPath := filepath.Join(dir, "phases.yaml")
		inlineSchema := `{"type":"object","properties":{"status":{"type":"string"}}}`
		content := `phases:
  - name: custom-phase
    prompt: prompts/custom.md
    schema: '` + inlineSchema + `'
    timeout: 1m
`
		if err := os.WriteFile(phasesPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(phasesPath)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if pipeline.Phases[0].Schema != inlineSchema {
			t.Errorf("inline schema was modified:\ngot:  %s\nwant: %s", pipeline.Phases[0].Schema, inlineSchema)
		}
	})

	t.Run("inline_json_schema_with_slashes_not_treated_as_file", func(t *testing.T) {
		dir := t.TempDir()
		phasesPath := filepath.Join(dir, "phases.yaml")
		// Inline schemas containing forward slashes (e.g. $ref or $schema URLs)
		// must not be misidentified as file paths.
		inlineSchema := `{"$schema":"http://json-schema.org/draft-07/schema#","$ref":"#/definitions/Foo"}`
		content := `phases:
  - name: custom-phase
    prompt: prompts/custom.md
    schema: '` + inlineSchema + `'
    timeout: 1m
`
		if err := os.WriteFile(phasesPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(phasesPath)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if pipeline.Phases[0].Schema != inlineSchema {
			t.Errorf("inline schema with slashes was modified:\ngot:  %s\nwant: %s", pipeline.Phases[0].Schema, inlineSchema)
		}
	})
}

func TestLoadPipeline_TimeoutOverrides(t *testing.T) {
	t.Run("parses_valid_timeout_overrides", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: implement
    prompt: prompts/implement.md
    timeout: 25m
    timeout_overrides:
      - condition: '{{ eq .Complexity "high" }}'
        timeout: 45m
      - condition: '{{ eq .Complexity "low" }}'
        timeout: 10m
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		phase := pipeline.Phases[0]
		if len(phase.TimeoutOverrides) != 2 {
			t.Fatalf("got %d timeout_overrides, want 2", len(phase.TimeoutOverrides))
		}

		// First override
		if phase.TimeoutOverrides[0].Condition != `{{ eq .Complexity "high" }}` {
			t.Errorf("override[0].Condition = %q", phase.TimeoutOverrides[0].Condition)
		}
		if phase.TimeoutOverrides[0].Timeout.Duration != 45*time.Minute {
			t.Errorf("override[0].Timeout = %v, want 45m", phase.TimeoutOverrides[0].Timeout.Duration)
		}

		// Second override
		if phase.TimeoutOverrides[1].Condition != `{{ eq .Complexity "low" }}` {
			t.Errorf("override[1].Condition = %q", phase.TimeoutOverrides[1].Condition)
		}
		if phase.TimeoutOverrides[1].Timeout.Duration != 10*time.Minute {
			t.Errorf("override[1].Timeout = %v, want 10m", phase.TimeoutOverrides[1].Timeout.Duration)
		}
	})

	t.Run("no_timeout_overrides_defaults_to_empty", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: implement
    prompt: prompts/implement.md
    timeout: 25m
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pipeline, err := LoadPipeline(path)
		if err != nil {
			t.Fatalf("LoadPipeline: %v", err)
		}

		if len(pipeline.Phases[0].TimeoutOverrides) != 0 {
			t.Errorf("got %d timeout_overrides, want 0", len(pipeline.Phases[0].TimeoutOverrides))
		}
	})

	t.Run("errors_on_invalid_condition_template", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: implement
    prompt: prompts/implement.md
    timeout: 25m
    timeout_overrides:
      - condition: '{{ invalid {{ syntax }}'
        timeout: 45m
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for invalid timeout_overrides condition template")
		}
		if !strings.Contains(err.Error(), "timeout_overrides[0]") {
			t.Errorf("error = %q, want mention of timeout_overrides[0]", err)
		}
		if !strings.Contains(err.Error(), "invalid condition template") {
			t.Errorf("error = %q, want mention of invalid condition template", err)
		}
		if !strings.Contains(err.Error(), "implement") {
			t.Errorf("error = %q, want mention of phase name", err)
		}
	})

	t.Run("errors_on_empty_condition", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "phases.yaml")
		content := `phases:
  - name: implement
    prompt: prompts/implement.md
    timeout: 25m
    timeout_overrides:
      - condition: ''
        timeout: 45m
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		_, err := LoadPipeline(path)
		if err == nil {
			t.Fatal("expected error for empty timeout_overrides condition")
		}
		if !strings.Contains(err.Error(), "empty condition") {
			t.Errorf("error = %q, want mention of empty condition", err)
		}
		if !strings.Contains(err.Error(), "timeout_overrides[0]") {
			t.Errorf("error = %q, want mention of timeout_overrides[0]", err)
		}
	})
}

func TestIsFilePath(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"schema.json", true},
		{"./schema.json", true},
		{"schemas/custom.json", true},
		{"/absolute/path/schema.json", true},
		{"relative/path/schema", true},
		{`windows\path\schema`, true},
		{`{"type":"object"}`, false},
		{`{"$ref":"#/definitions/Foo"}`, false},
		{`{"$schema":"http://json-schema.org/draft-07/schema#"}`, false},
		{"plain-string", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isFilePath(tt.input)
			if got != tt.want {
				t.Errorf("isFilePath(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
