package pipeline

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPipelineTimeoutError(t *testing.T) {
	err := &PipelineTimeoutError{
		Limit:   2 * time.Hour,
		Elapsed: 2*time.Hour + 5*time.Minute,
		Phase:   "implement",
	}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}
	if !strings.Contains(msg, "implement") {
		t.Errorf("Error() should contain phase name, got: %s", msg)
	}
	if !strings.Contains(msg, "2h0m0s") {
		t.Errorf("Error() should contain limit, got: %s", msg)
	}
	if !strings.Contains(msg, "2h5m0s") {
		t.Errorf("Error() should contain elapsed, got: %s", msg)
	}

	var target *PipelineTimeoutError
	if !errors.As(err, &target) {
		t.Error("errors.As should match PipelineTimeoutError")
	}
	if target.Phase != "implement" {
		t.Errorf("Phase = %q, want %q", target.Phase, "implement")
	}
}

func TestBudgetExceededError(t *testing.T) {
	err := &BudgetExceededError{Limit: 15.00, Actual: 15.50, Phase: "verify"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	var target *BudgetExceededError
	if !errors.As(err, &target) {
		t.Error("errors.As should match BudgetExceededError")
	}
	if target.Phase != "verify" {
		t.Errorf("Phase = %q, want %q", target.Phase, "verify")
	}
}

func TestPhaseBudgetExceededError(t *testing.T) {
	err := &PhaseBudgetExceededError{Limit: 8.00, Actual: 10.50, Phase: "implement"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}
	if !strings.Contains(msg, "implement") {
		t.Errorf("Error() should contain phase name, got: %s", msg)
	}
	if !strings.Contains(msg, "8.00") {
		t.Errorf("Error() should contain limit, got: %s", msg)
	}
	if !strings.Contains(msg, "10.50") {
		t.Errorf("Error() should contain actual cost, got: %s", msg)
	}

	var target *PhaseBudgetExceededError
	if !errors.As(err, &target) {
		t.Error("errors.As should match PhaseBudgetExceededError")
	}
	if target.Phase != "implement" {
		t.Errorf("Phase = %q, want %q", target.Phase, "implement")
	}
}

func TestDependencyNotMetError(t *testing.T) {
	err := &DependencyNotMetError{Phase: "implement", Dependency: "plan"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	var target *DependencyNotMetError
	if !errors.As(err, &target) {
		t.Error("errors.As should match DependencyNotMetError")
	}
}

func TestPhaseGateError(t *testing.T) {
	err := &PhaseGateError{Phase: "triage", Reason: "not automatable"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() should return non-empty string")
	}

	var target *PhaseGateError
	if !errors.As(err, &target) {
		t.Error("errors.As should match PhaseGateError")
	}
}

func TestReworkSignal(t *testing.T) {
	t.Run("with_findings", func(t *testing.T) {
		err := &reworkSignal{
			target: "implement",
			findings: []reworkFinding{
				{Severity: "critical", Issue: "nil deref"},
				{Severity: "minor", Issue: "naming"},
				{Severity: "major", Issue: "missing error check"},
			},
		}

		msg := err.Error()
		if !strings.Contains(msg, "target implement") {
			t.Errorf("Error() should contain target, got: %s", msg)
		}
		// Only critical and major issues should appear.
		if !strings.Contains(msg, "nil deref") {
			t.Errorf("Error() should contain critical issue, got: %s", msg)
		}
		if !strings.Contains(msg, "missing error check") {
			t.Errorf("Error() should contain major issue, got: %s", msg)
		}
		if strings.Contains(msg, "naming") {
			t.Errorf("Error() should NOT contain minor issue, got: %s", msg)
		}

		var target *reworkSignal
		if !errors.As(err, &target) {
			t.Error("errors.As should match reworkSignal")
		}
		if target.target != "implement" {
			t.Errorf("target = %q, want %q", target.target, "implement")
		}
	})

	t.Run("without_findings", func(t *testing.T) {
		err := &reworkSignal{target: "plan"}
		msg := err.Error()
		if !strings.Contains(msg, "target plan") {
			t.Errorf("Error() should contain target, got: %s", msg)
		}
	})
}
