package pipeline

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/decko/soda/schemas"
)

func TestCheckSchemaVersions(t *testing.T) {
	tests := []struct {
		name       string
		phases     []PhaseConfig
		fromPhase  string
		setup      func(*State) // prepare state before check
		force      bool
		wantErr    bool
		wantEvents int // expected EventSchemaVersionMismatch events
	}{
		{
			name: "matching_versions_no_error",
			phases: []PhaseConfig{
				{Name: "triage", Prompt: "triage.md"},
				{Name: "plan", Prompt: "plan.md"},
			},
			fromPhase: "plan",
			setup: func(state *State) {
				state.MarkRunning("triage")
				// Write with current schema version (injected by WriteResult).
				state.WriteResult("triage", json.RawMessage(`{"ticket_key":"TEST-1","complexity":"low"}`))
				state.MarkCompleted("triage")
			},
			wantErr:    false,
			wantEvents: 0,
		},
		{
			name: "missing_version_warns_but_no_error",
			phases: []PhaseConfig{
				{Name: "triage", Prompt: "triage.md"},
				{Name: "plan", Prompt: "plan.md"},
			},
			fromPhase: "plan",
			setup: func(state *State) {
				state.MarkRunning("triage")
				// Write directly to bypass _schema_version injection.
				writeResultRaw(state, "triage", json.RawMessage(`{"ticket_key":"TEST-1","complexity":"low"}`))
				state.MarkCompleted("triage")
			},
			wantErr:    false,
			wantEvents: 1,
		},
		{
			name: "mismatched_version_blocks",
			phases: []PhaseConfig{
				{Name: "triage", Prompt: "triage.md"},
				{Name: "plan", Prompt: "plan.md"},
			},
			fromPhase: "plan",
			setup: func(state *State) {
				state.MarkRunning("triage")
				// Write with a fake old schema version.
				writeResultRaw(state, "triage", json.RawMessage(`{"ticket_key":"TEST-1","_schema_version":"deadbeef12345678"}`))
				state.MarkCompleted("triage")
			},
			wantErr:    true,
			wantEvents: 1,
		},
		{
			name: "mismatched_version_force_overrides",
			phases: []PhaseConfig{
				{Name: "triage", Prompt: "triage.md"},
				{Name: "plan", Prompt: "plan.md"},
			},
			fromPhase: "plan",
			force:     true,
			setup: func(state *State) {
				state.MarkRunning("triage")
				writeResultRaw(state, "triage", json.RawMessage(`{"ticket_key":"TEST-1","_schema_version":"deadbeef12345678"}`))
				state.MarkCompleted("triage")
			},
			wantErr:    false,
			wantEvents: 1,
		},
		{
			name: "uncompleted_phase_skipped",
			phases: []PhaseConfig{
				{Name: "triage", Prompt: "triage.md"},
				{Name: "plan", Prompt: "plan.md"},
			},
			fromPhase: "plan",
			setup: func(state *State) {
				// triage not completed — should be skipped.
			},
			wantErr:    false,
			wantEvents: 0,
		},
		{
			name: "unknown_schema_phase_skipped",
			phases: []PhaseConfig{
				{Name: "custom_phase", Prompt: "custom.md"},
				{Name: "plan", Prompt: "plan.md"},
			},
			fromPhase: "plan",
			setup: func(state *State) {
				state.MarkRunning("custom_phase")
				state.WriteResult("custom_phase", json.RawMessage(`{"key":"value"}`))
				state.MarkCompleted("custom_phase")
			},
			wantErr:    false,
			wantEvents: 0,
		},
		{
			name: "multiple_phases_checks_all",
			phases: []PhaseConfig{
				{Name: "triage", Prompt: "triage.md"},
				{Name: "plan", Prompt: "plan.md"},
				{Name: "implement", Prompt: "implement.md"},
			},
			fromPhase: "implement",
			setup: func(state *State) {
				// triage: matching version
				state.MarkRunning("triage")
				state.WriteResult("triage", json.RawMessage(`{"ticket_key":"TEST-1","complexity":"low"}`))
				state.MarkCompleted("triage")

				// plan: mismatched version → blocks
				state.MarkRunning("plan")
				writeResultRaw(state, "plan", json.RawMessage(`{"ticket_key":"TEST-1","_schema_version":"deadbeef12345678"}`))
				state.MarkCompleted("plan")
			},
			wantErr:    true,
			wantEvents: 1, // only the mismatch event
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []Event
			engine, _ := setupEngine(t, tt.phases, &flexMockRunner{}, func(cfg *EngineConfig) {
				cfg.Force = tt.force
				cfg.OnEvent = func(e Event) { events = append(events, e) }
			})

			if tt.setup != nil {
				tt.setup(engine.state)
			}

			err := engine.checkSchemaVersions(tt.fromPhase)

			if tt.wantErr && err == nil {
				t.Fatal("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantErr {
				var svErr *SchemaVersionMismatchError
				if !errors.As(err, &svErr) {
					t.Errorf("error should be SchemaVersionMismatchError, got %T: %v", err, err)
				}
			}

			// Count schema version mismatch events.
			mismatchEvents := 0
			for _, ev := range events {
				if ev.Kind == EventSchemaVersionMismatch {
					mismatchEvents++
				}
			}
			if mismatchEvents != tt.wantEvents {
				t.Errorf("schema_version_mismatch events = %d, want %d", mismatchEvents, tt.wantEvents)
			}
		})
	}
}

func TestExtractSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	state, _ := LoadOrCreate(dir, "T-ESV")

	t.Run("present", func(t *testing.T) {
		state.MarkRunning("triage")
		// Use WriteResult which injects _schema_version.
		state.WriteResult("triage", json.RawMessage(`{"ticket_key":"TEST"}`))

		version, err := extractSchemaVersion(state, "triage")
		if err != nil {
			t.Fatalf("extractSchemaVersion: %v", err)
		}
		expected := schemas.SchemaVersionFor("triage")
		if version != expected {
			t.Errorf("version = %q, want %q", version, expected)
		}
	})

	t.Run("missing_field", func(t *testing.T) {
		state.MarkRunning("plan")
		writeResultRaw(state, "plan", json.RawMessage(`{"ticket_key":"TEST"}`))

		version, err := extractSchemaVersion(state, "plan")
		if err != nil {
			t.Fatalf("extractSchemaVersion: %v", err)
		}
		if version != "" {
			t.Errorf("version = %q, want empty", version)
		}
	})

	t.Run("missing_file", func(t *testing.T) {
		_, err := extractSchemaVersion(state, "nonexistent")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}

// writeResultRaw writes a result without _schema_version injection,
// bypassing the normal WriteResult path for testing purposes.
func writeResultRaw(state *State, phase string, data json.RawMessage) error {
	return atomicWrite(state.Dir()+"/"+phase+".json", data)
}
