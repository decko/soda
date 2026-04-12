package main

import (
	"testing"

	"github.com/decko/soda/internal/pipeline"
)

func TestResolveLastPhase(t *testing.T) {
	phases := []pipeline.PhaseConfig{
		{Name: "triage"},
		{Name: "plan"},
		{Name: "implement"},
		{Name: "verify"},
	}

	tests := []struct {
		name    string
		meta    *pipeline.PipelineMeta
		want    string
		wantErr bool
	}{
		{
			name: "single failed phase",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{
					"triage": {Status: pipeline.PhaseCompleted},
					"plan":   {Status: pipeline.PhaseFailed},
				},
			},
			want: "plan",
		},
		{
			name: "running phase (stale)",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{
					"triage":    {Status: pipeline.PhaseCompleted},
					"plan":      {Status: pipeline.PhaseCompleted},
					"implement": {Status: pipeline.PhaseRunning},
				},
			},
			want: "implement",
		},
		{
			name: "multiple failed — latest in pipeline wins",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{
					"triage":    {Status: pipeline.PhaseFailed},
					"plan":      {Status: pipeline.PhaseFailed},
					"implement": {Status: pipeline.PhaseFailed},
				},
			},
			want: "implement",
		},
		{
			name: "no failed or running phases",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{
					"triage": {Status: pipeline.PhaseCompleted},
				},
			},
			wantErr: true,
		},
		{
			name: "empty phases",
			meta: &pipeline.PipelineMeta{
				Phases: map[string]*pipeline.PhaseState{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveLastPhase(tt.meta, phases)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveLastPhase() = %q, want %q", got, tt.want)
			}
		})
	}
}
