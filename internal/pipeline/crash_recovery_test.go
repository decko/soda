package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/runner"
)

// fivePhaseConfig returns a standard 5-phase pipeline configuration used
// across the crash recovery table tests.
func fivePhaseConfig() []PhaseConfig {
	return []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "implement",
			Prompt:    "implement.md",
			DependsOn: []string{"plan"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "verify",
			Prompt:    "verify.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "review",
			Prompt:    "review.md",
			DependsOn: []string{"implement"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}
}

// fivePhaseResults returns mock runner responses keyed by phase name for a
// full happy-path run through all five phases.
func fivePhaseResults() map[string][]flexResponse {
	return map[string][]flexResponse{
		"triage": {{
			result: &runner.RunResult{
				Output:  json.RawMessage(`{"automatable":true}`),
				RawText: "Triage: automatable",
				CostUSD: 0.10,
			},
		}},
		"plan": {{
			result: &runner.RunResult{
				Output:  json.RawMessage(`{"tasks":["task1","task2"]}`),
				RawText: "Plan: two tasks",
				CostUSD: 0.20,
			},
		}},
		"implement": {{
			result: &runner.RunResult{
				Output:  json.RawMessage(`{"commits":1}`),
				RawText: "Implement: done",
				CostUSD: 1.50,
			},
		}},
		"verify": {{
			result: &runner.RunResult{
				Output:  json.RawMessage(`{"verdict":"PASS"}`),
				RawText: "Verify: pass",
				CostUSD: 0.30,
			},
		}},
		"review": {{
			result: &runner.RunResult{
				Output:  json.RawMessage(`{"verdict":"pass","findings":[]}`),
				RawText: "Review: pass",
				CostUSD: 0.25,
			},
		}},
	}
}

// crashBoundary describes a single crash recovery test case: which phases
// completed before the crash, which phase was left running, and the
// accumulated cost up to that point.
type crashBoundary struct {
	name               string   // descriptive test name
	completed          []string // phases that completed before the crash
	crashed            string   // phase left in "running" status
	preCrashCost       float64  // TotalCost accumulated before the crash
	expectedGeneration int      // expected generation of the crashed phase after recovery + re-run
}

// allCrashBoundaries returns test cases covering crash at every phase boundary.
func allCrashBoundaries() []crashBoundary {
	return []crashBoundary{
		{
			name:               "crash_during_triage",
			completed:          nil,
			crashed:            "triage",
			preCrashCost:       0,
			expectedGeneration: 2,
		},
		{
			name:               "crash_during_plan",
			completed:          []string{"triage"},
			crashed:            "plan",
			preCrashCost:       0.10,
			expectedGeneration: 2,
		},
		{
			name:               "crash_during_implement",
			completed:          []string{"triage", "plan"},
			crashed:            "implement",
			preCrashCost:       0.30,
			expectedGeneration: 2,
		},
		{
			name:               "crash_during_verify",
			completed:          []string{"triage", "plan", "implement"},
			crashed:            "verify",
			preCrashCost:       1.80,
			expectedGeneration: 2,
		},
		{
			name:               "crash_during_review",
			completed:          []string{"triage", "plan", "implement", "verify"},
			crashed:            "review",
			preCrashCost:       2.10,
			expectedGeneration: 2,
		},
	}
}

// phaseResult returns a minimal JSON result for a given phase name.
func phaseResult(phase string) json.RawMessage {
	switch phase {
	case "triage":
		return json.RawMessage(`{"automatable":true}`)
	case "plan":
		return json.RawMessage(`{"tasks":["task1","task2"]}`)
	case "implement":
		return json.RawMessage(`{"commits":1}`)
	case "verify":
		return json.RawMessage(`{"verdict":"PASS"}`)
	case "review":
		return json.RawMessage(`{"verdict":"pass","findings":[]}`)
	default:
		return json.RawMessage(fmt.Sprintf(`{"phase":"%s"}`, phase))
	}
}

// phaseArtifact returns a minimal markdown artifact for a given phase name.
func phaseArtifact(phase string) []byte {
	return []byte(fmt.Sprintf("# %s handoff\nDone.", phase))
}

// phaseCost returns the mock cost for a given phase name, matching fivePhaseResults.
func phaseCost(phase string) float64 {
	switch phase {
	case "triage":
		return 0.10
	case "plan":
		return 0.20
	case "implement":
		return 1.50
	case "verify":
		return 0.30
	case "review":
		return 0.25
	default:
		return 0.01
	}
}

// populateCompletedPhases writes state entries for phases that completed before
// the crash: marks them running, writes result/artifact, accumulates cost,
// and marks them completed. It fails the test immediately if any state
// operation returns an error.
func populateCompletedPhases(t testing.TB, state *State, phases []string) {
	t.Helper()
	for _, p := range phases {
		if err := state.MarkRunning(p); err != nil {
			t.Fatalf("populateCompletedPhases: MarkRunning(%s): %v", p, err)
		}
		if err := state.WriteResult(p, phaseResult(p)); err != nil {
			t.Fatalf("populateCompletedPhases: WriteResult(%s): %v", p, err)
		}
		if err := state.WriteArtifact(p, phaseArtifact(p)); err != nil {
			t.Fatalf("populateCompletedPhases: WriteArtifact(%s): %v", p, err)
		}
		if err := state.AccumulateCost(p, phaseCost(p)); err != nil {
			t.Fatalf("populateCompletedPhases: AccumulateCost(%s): %v", p, err)
		}
		if err := state.MarkCompleted(p); err != nil {
			t.Fatalf("populateCompletedPhases: MarkCompleted(%s): %v", p, err)
		}
	}
}

// TestCrashRecovery_AllBoundaries_Run uses table-driven tests to verify crash
// recovery via Run() at every phase boundary in a 5-phase pipeline.
//
// For each boundary the test:
//  1. Pre-populates state with completed phases and a "running" crashed phase
//  2. Runs the engine (which calls recoverCrashedPhases)
//  3. Verifies a crash-recovery phase_failed event is emitted for the crashed phase
//  4. Verifies the crashed phase is re-run and completes successfully
//  5. Verifies all downstream phases complete
//  6. Verifies cost accumulation includes both pre-crash and post-recovery costs
//  7. Verifies generation is incremented for the recovered phase
func TestCrashRecovery_AllBoundaries_Run(t *testing.T) {
	for _, tc := range allCrashBoundaries() {
		t.Run(tc.name, func(t *testing.T) {
			phases := fivePhaseConfig()
			mock := &flexMockRunner{responses: fivePhaseResults()}

			var events []Event
			engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
				cfg.OnEvent = func(ev Event) { events = append(events, ev) }
			})

			// Populate completed phases.
			populateCompletedPhases(t, state, tc.completed)

			// Simulate crash: mark the target phase as running but don't complete it.
			_ = state.MarkRunning(tc.crashed)

			// Verify pre-crash state: crashed phase is running, not completed.
			ps := state.Meta().Phases[tc.crashed]
			if ps == nil || ps.Status != PhaseRunning {
				t.Fatalf("pre-crash: phase %q status = %v, want running", tc.crashed, ps)
			}

			// Run the engine — should recover the crashed phase, then execute the pipeline.
			if err := engine.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// --- Assertion 1: crash-recovery phase_failed event emitted ---
			var crashFailedEvents int
			for _, ev := range events {
				if ev.Kind == EventPhaseFailed && ev.Phase == tc.crashed {
					if errMsg, ok := ev.Data["error"].(string); ok && strings.Contains(errMsg, "crashed") {
						crashFailedEvents++
					}
				}
			}
			if crashFailedEvents != 1 {
				t.Errorf("expected exactly 1 crash-recovery phase_failed event for %q, got %d", tc.crashed, crashFailedEvents)
			}

			// --- Assertion 2: crash-recovery event appears BEFORE phase_started ---
			// Search independently for both indices so the ordering check is
			// not tautological.
			crashIdx, startIdx := -1, -1
			for i, ev := range events {
				if ev.Kind == EventPhaseFailed && ev.Phase == tc.crashed {
					if errMsg, _ := ev.Data["error"].(string); strings.Contains(errMsg, "crashed") {
						crashIdx = i
					}
				}
				if ev.Kind == EventPhaseStarted && ev.Phase == tc.crashed && startIdx < 0 {
					startIdx = i
				}
			}
			if crashIdx < 0 {
				t.Fatal("no crash-recovery phase_failed event found")
			}
			if startIdx < 0 {
				t.Fatal("no phase_started event found for crashed phase")
			}
			if crashIdx >= startIdx {
				t.Errorf("crash-recovery phase_failed (idx=%d) must precede phase_started (idx=%d)", crashIdx, startIdx)
			}

			// --- Assertion 3: all phases completed ---
			allPhases := []string{"triage", "plan", "implement", "verify", "review"}
			for _, p := range allPhases {
				if !state.IsCompleted(p) {
					t.Errorf("phase %q should be completed after recovery", p)
				}
			}

			// --- Assertion 4: generation incremented for crashed phase ---
			recoveredPS := state.Meta().Phases[tc.crashed]
			if recoveredPS == nil {
				t.Fatalf("phase %q not found in meta after recovery", tc.crashed)
			}
			if recoveredPS.Generation != tc.expectedGeneration {
				t.Errorf("phase %q generation = %d, want %d", tc.crashed, recoveredPS.Generation, tc.expectedGeneration)
			}

			// --- Assertion 5: completed phases retain generation 1 ---
			for _, p := range tc.completed {
				cps := state.Meta().Phases[p]
				if cps == nil {
					t.Errorf("completed phase %q not found in meta", p)
					continue
				}
				if cps.Generation != 1 {
					t.Errorf("completed phase %q generation = %d, want 1 (should not be re-run)", p, cps.Generation)
				}
			}

			// --- Assertion 6: TotalCost includes pre-crash + post-recovery costs ---
			if state.Meta().TotalCost < tc.preCrashCost {
				t.Errorf("TotalCost = %v, should be >= pre-crash cost %v", state.Meta().TotalCost, tc.preCrashCost)
			}
		})
	}
}

// TestCrashRecovery_AllBoundaries_Resume uses table-driven tests to verify crash
// recovery via Resume() at every phase boundary in a 5-phase pipeline.
//
// Resume re-runs from the crashed phase, and the engine should emit a
// crash-recovery phase_failed event before re-running it.
func TestCrashRecovery_AllBoundaries_Resume(t *testing.T) {
	for _, tc := range allCrashBoundaries() {
		t.Run(tc.name, func(t *testing.T) {
			phases := fivePhaseConfig()

			// Build mock responses only for phases that need to run.
			// Completed phases are skipped, but we need the crashed phase
			// and all downstream phases to have responses.
			allResults := fivePhaseResults()
			mock := &flexMockRunner{responses: allResults}

			var events []Event
			engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
				cfg.OnEvent = func(ev Event) { events = append(events, ev) }
			})

			// Populate completed phases.
			populateCompletedPhases(t, state, tc.completed)

			// Simulate crash: mark the target phase as running.
			_ = state.MarkRunning(tc.crashed)

			// Resume from the crashed phase.
			if err := engine.Resume(context.Background(), tc.crashed); err != nil {
				t.Fatalf("Resume: %v", err)
			}

			// --- Assertion 1: crash-recovery phase_failed event emitted ---
			var crashFailedEvents int
			for _, ev := range events {
				if ev.Kind == EventPhaseFailed && ev.Phase == tc.crashed {
					if errMsg, ok := ev.Data["error"].(string); ok && strings.Contains(errMsg, "crashed") {
						crashFailedEvents++
					}
				}
			}
			if crashFailedEvents != 1 {
				t.Errorf("expected exactly 1 crash-recovery phase_failed event for %q, got %d", tc.crashed, crashFailedEvents)
			}

			// --- Assertion 2: crashed phase completed after recovery ---
			if !state.IsCompleted(tc.crashed) {
				t.Errorf("phase %q should be completed after crash recovery via Resume", tc.crashed)
			}

			// --- Assertion 3: generation incremented for crashed phase ---
			recoveredPS := state.Meta().Phases[tc.crashed]
			if recoveredPS == nil {
				t.Fatalf("phase %q not found in meta after recovery", tc.crashed)
			}
			if recoveredPS.Generation != tc.expectedGeneration {
				t.Errorf("phase %q generation = %d, want %d", tc.crashed, recoveredPS.Generation, tc.expectedGeneration)
			}
		})
	}
}

// TestCrashRecovery_StatePreservation verifies that crash recovery preserves
// completed phase artifacts and results across all phase boundaries.
func TestCrashRecovery_StatePreservation(t *testing.T) {
	for _, tc := range allCrashBoundaries() {
		if len(tc.completed) == 0 {
			continue // skip: no completed phases to verify preservation for
		}

		t.Run(tc.name, func(t *testing.T) {
			phases := fivePhaseConfig()
			mock := &flexMockRunner{responses: fivePhaseResults()}

			engine, state := setupEngine(t, phases, mock)

			// Populate completed phases with known artifacts/results.
			populateCompletedPhases(t, state, tc.completed)

			// Simulate crash.
			_ = state.MarkRunning(tc.crashed)

			// Run the engine to trigger recovery.
			if err := engine.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// Verify that completed phase artifacts are preserved.
			for _, p := range tc.completed {
				artifact, err := state.ReadArtifact(p)
				if err != nil {
					t.Errorf("completed phase %q: artifact lost after crash recovery: %v", p, err)
					continue
				}
				expected := string(phaseArtifact(p))
				if string(artifact) != expected {
					t.Errorf("completed phase %q: artifact = %q, want %q", p, artifact, expected)
				}

				result, err := state.ReadResult(p)
				if err != nil {
					t.Errorf("completed phase %q: result lost after crash recovery: %v", p, err)
					continue
				}
				if len(result) == 0 {
					t.Errorf("completed phase %q: result empty after crash recovery", p)
				}
			}
		})
	}
}

// TestCrashRecovery_NoCrashNoRecoveryEvent verifies that when no phases are
// in "running" status, recoverCrashedPhases emits no crash-recovery events.
// This is tested for every possible "clean" pre-state.
func TestCrashRecovery_NoCrashNoRecoveryEvent(t *testing.T) {
	// Test cases: pipeline with various completed phases, none crashed.
	cases := []struct {
		name      string
		completed []string
	}{
		{"no_phases_completed", nil},
		{"triage_completed", []string{"triage"}},
		{"triage_plan_completed", []string{"triage", "plan"}},
		{"triage_plan_implement_completed", []string{"triage", "plan", "implement"}},
		{"all_completed", []string{"triage", "plan", "implement", "verify", "review"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			phases := fivePhaseConfig()
			mock := &flexMockRunner{responses: fivePhaseResults()}

			var events []Event
			engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
				cfg.OnEvent = func(ev Event) { events = append(events, ev) }
			})

			populateCompletedPhases(t, state, tc.completed)

			if err := engine.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// No crash-recovery events should be emitted.
			for _, ev := range events {
				if ev.Kind == EventPhaseFailed {
					if errMsg, ok := ev.Data["error"].(string); ok && strings.Contains(errMsg, "crashed") {
						t.Errorf("unexpected crash-recovery event for phase %q", ev.Phase)
					}
				}
			}
		})
	}
}

// TestCrashRecovery_MultiplePhasesCrashed verifies recovery when multiple
// phases are left in "running" status from a crash. Each crashed phase
// should get a phase_failed event and be recovered.
func TestCrashRecovery_MultiplePhasesCrashed(t *testing.T) {
	cases := []struct {
		name      string
		completed []string
		crashed   []string
	}{
		{
			name:      "triage_and_plan_crashed",
			completed: nil,
			crashed:   []string{"triage", "plan"},
		},
		{
			name:      "implement_and_verify_crashed",
			completed: []string{"triage", "plan"},
			crashed:   []string{"implement", "verify"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			phases := fivePhaseConfig()
			mock := &flexMockRunner{responses: fivePhaseResults()}

			var events []Event
			engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
				cfg.OnEvent = func(ev Event) { events = append(events, ev) }
			})

			populateCompletedPhases(t, state, tc.completed)

			// Mark multiple phases as crashed.
			for _, p := range tc.crashed {
				_ = state.MarkRunning(p)
			}

			if err := engine.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// Each crashed phase should have a recovery event.
			for _, crashed := range tc.crashed {
				var found bool
				for _, ev := range events {
					if ev.Kind == EventPhaseFailed && ev.Phase == crashed {
						if errMsg, ok := ev.Data["error"].(string); ok && strings.Contains(errMsg, "crashed") {
							found = true
							break
						}
					}
				}
				if !found {
					t.Errorf("no crash-recovery phase_failed event for %q", crashed)
				}
			}

			// All phases should complete.
			for _, p := range []string{"triage", "plan", "implement", "verify", "review"} {
				if !state.IsCompleted(p) {
					t.Errorf("phase %q should be completed", p)
				}
			}
		})
	}
}

// TestCrashRecovery_FailedPhaseIsNotRecovered verifies that phases in "failed"
// status (normal failure, not crash) are NOT treated as crash victims.
// Only "running" status triggers crash recovery.
func TestCrashRecovery_FailedPhaseIsNotRecovered(t *testing.T) {
	for _, tc := range allCrashBoundaries() {
		t.Run(tc.name+"_failed_not_recovered", func(t *testing.T) {
			phases := fivePhaseConfig()
			mock := &flexMockRunner{responses: fivePhaseResults()}

			var events []Event
			engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
				cfg.OnEvent = func(ev Event) { events = append(events, ev) }
			})

			populateCompletedPhases(t, state, tc.completed)

			// Mark phase as running then explicitly failed (normal failure, not crash).
			_ = state.MarkRunning(tc.crashed)
			_ = state.MarkFailed(tc.crashed, fmt.Errorf("normal failure"))

			if err := engine.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// NO crash-recovery events should appear for this phase.
			for _, ev := range events {
				if ev.Kind == EventPhaseFailed && ev.Phase == tc.crashed {
					if errMsg, ok := ev.Data["error"].(string); ok && strings.Contains(errMsg, "crashed") {
						t.Errorf("should not emit crash-recovery event for phase %q that was explicitly failed", tc.crashed)
					}
				}
			}
		})
	}
}

// TestCrashRecovery_PhaseStatusTransitions verifies the exact status
// transitions during crash recovery for every phase boundary:
// running (stale) → failed (recovery) → running (re-run) → completed
func TestCrashRecovery_PhaseStatusTransitions(t *testing.T) {
	for _, tc := range allCrashBoundaries() {
		t.Run(tc.name, func(t *testing.T) {
			phases := fivePhaseConfig()
			mock := &flexMockRunner{responses: fivePhaseResults()}

			var statusLog []string
			engine, state := setupEngine(t, phases, mock, func(cfg *EngineConfig) {
				cfg.OnEvent = func(ev Event) {
					if ev.Phase != tc.crashed {
						return
					}
					switch ev.Kind {
					case EventPhaseFailed:
						statusLog = append(statusLog, "failed")
					case EventPhaseStarted:
						statusLog = append(statusLog, "started")
					case EventPhaseCompleted:
						statusLog = append(statusLog, "completed")
					}
				}
			})

			populateCompletedPhases(t, state, tc.completed)
			_ = state.MarkRunning(tc.crashed)

			if err := engine.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// Expected transition: failed (crash recovery) → started (re-run) → completed
			want := []string{"failed", "started", "completed"}
			if len(statusLog) != len(want) {
				t.Fatalf("status transitions for %q = %v, want %v", tc.crashed, statusLog, want)
			}
			for i := range want {
				if statusLog[i] != want[i] {
					t.Errorf("status transition [%d] = %q, want %q (full: %v)", i, statusLog[i], want[i], statusLog)
				}
			}
		})
	}
}

// TestCrashRecovery_DiskPersistence verifies that crash recovery state
// survives a reload from disk. After recovery and re-run, the meta.json
// should reflect the recovered state.
func TestCrashRecovery_DiskPersistence(t *testing.T) {
	for _, tc := range allCrashBoundaries() {
		t.Run(tc.name, func(t *testing.T) {
			phases := fivePhaseConfig()
			mock := &flexMockRunner{responses: fivePhaseResults()}

			engine, state := setupEngine(t, phases, mock)

			populateCompletedPhases(t, state, tc.completed)
			_ = state.MarkRunning(tc.crashed)

			if err := engine.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// Reload state from disk. state.dir is stateDir/ticket, so
			// LoadOrCreate needs the parent directory.
			reloaded, err := LoadOrCreate(filepath.Dir(state.dir), "TEST-1")
			if err != nil {
				t.Fatalf("reload from disk: %v", err)
			}

			// Crashed phase should be completed with incremented generation.
			ps := reloaded.Meta().Phases[tc.crashed]
			if ps == nil {
				t.Fatalf("phase %q not found in reloaded meta", tc.crashed)
			}
			if ps.Status != PhaseCompleted {
				t.Errorf("reloaded phase %q status = %q, want completed", tc.crashed, ps.Status)
			}
			if ps.Generation != tc.expectedGeneration {
				t.Errorf("reloaded phase %q generation = %d, want %d", tc.crashed, ps.Generation, tc.expectedGeneration)
			}

			// All phases should be completed.
			for _, p := range []string{"triage", "plan", "implement", "verify", "review"} {
				rps := reloaded.Meta().Phases[p]
				if rps == nil || rps.Status != PhaseCompleted {
					t.Errorf("reloaded phase %q should be completed", p)
				}
			}
		})
	}
}
