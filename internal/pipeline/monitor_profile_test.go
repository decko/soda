package pipeline

import (
	"testing"
	"time"
)

func TestGetMonitorProfile(t *testing.T) {
	tests := []struct {
		name               MonitorProfileName
		wantInitial        time.Duration
		wantMaxRounds      int
		wantAutoFixNits    bool
		wantAutoRebase     bool
		wantRespondNonAuth bool
	}{
		{
			name:               ProfileConservative,
			wantInitial:        5 * time.Minute,
			wantMaxRounds:      2,
			wantAutoFixNits:    false,
			wantAutoRebase:     false,
			wantRespondNonAuth: false,
		},
		{
			name:               ProfileSmart,
			wantInitial:        2 * time.Minute,
			wantMaxRounds:      3,
			wantAutoFixNits:    true,
			wantAutoRebase:     true,
			wantRespondNonAuth: false,
		},
		{
			name:               ProfileAggressive,
			wantInitial:        1 * time.Minute,
			wantMaxRounds:      5,
			wantAutoFixNits:    true,
			wantAutoRebase:     true,
			wantRespondNonAuth: true,
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			p, err := GetMonitorProfile(tt.name)
			if err != nil {
				t.Fatalf("GetMonitorProfile(%q): %v", tt.name, err)
			}

			if p.Name != tt.name {
				t.Errorf("Name = %q, want %q", p.Name, tt.name)
			}
			if p.InitialInterval != tt.wantInitial {
				t.Errorf("InitialInterval = %v, want %v", p.InitialInterval, tt.wantInitial)
			}
			if p.MaxResponseRounds != tt.wantMaxRounds {
				t.Errorf("MaxResponseRounds = %d, want %d", p.MaxResponseRounds, tt.wantMaxRounds)
			}
			if p.AutoFixNits != tt.wantAutoFixNits {
				t.Errorf("AutoFixNits = %v, want %v", p.AutoFixNits, tt.wantAutoFixNits)
			}
			if p.AutoRebase != tt.wantAutoRebase {
				t.Errorf("AutoRebase = %v, want %v", p.AutoRebase, tt.wantAutoRebase)
			}
			if p.RespondToNonAuth != tt.wantRespondNonAuth {
				t.Errorf("RespondToNonAuth = %v, want %v", p.RespondToNonAuth, tt.wantRespondNonAuth)
			}
		})
	}
}

func TestGetMonitorProfile_Unknown(t *testing.T) {
	_, err := GetMonitorProfile("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestMonitorProfile_ToPollingConfig(t *testing.T) {
	p, _ := GetMonitorProfile(ProfileSmart)

	cfg := p.ToPollingConfig()

	if cfg.InitialInterval.Duration != p.InitialInterval {
		t.Errorf("InitialInterval = %v, want %v", cfg.InitialInterval.Duration, p.InitialInterval)
	}
	if cfg.MaxInterval.Duration != p.MaxInterval {
		t.Errorf("MaxInterval = %v, want %v", cfg.MaxInterval.Duration, p.MaxInterval)
	}
	if cfg.EscalateAfter.Duration != p.EscalateAfter {
		t.Errorf("EscalateAfter = %v, want %v", cfg.EscalateAfter.Duration, p.EscalateAfter)
	}
	if cfg.MaxDuration.Duration != p.MaxDuration {
		t.Errorf("MaxDuration = %v, want %v", cfg.MaxDuration.Duration, p.MaxDuration)
	}
	if cfg.MaxResponseRounds != p.MaxResponseRounds {
		t.Errorf("MaxResponseRounds = %d, want %d", cfg.MaxResponseRounds, p.MaxResponseRounds)
	}
}

func TestMonitorProfile_BehaviorAccessors(t *testing.T) {
	tests := []struct {
		profile     MonitorProfileName
		wantNits    bool
		wantRebase  bool
		wantNonAuth bool
	}{
		{ProfileConservative, false, false, false},
		{ProfileSmart, true, true, false},
		{ProfileAggressive, true, true, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.profile), func(t *testing.T) {
			p, _ := GetMonitorProfile(tt.profile)

			if got := p.ShouldApplyNit(); got != tt.wantNits {
				t.Errorf("ShouldApplyNit() = %v, want %v", got, tt.wantNits)
			}
			if got := p.ShouldAutoRebase(); got != tt.wantRebase {
				t.Errorf("ShouldAutoRebase() = %v, want %v", got, tt.wantRebase)
			}
			if got := p.ShouldRespondToNonAuth(); got != tt.wantNonAuth {
				t.Errorf("ShouldRespondToNonAuth() = %v, want %v", got, tt.wantNonAuth)
			}
		})
	}
}

func TestMonitorProfile_ProfileTimingInvariants(t *testing.T) {
	profiles := []MonitorProfileName{ProfileConservative, ProfileSmart, ProfileAggressive}

	for _, name := range profiles {
		t.Run(string(name), func(t *testing.T) {
			p, _ := GetMonitorProfile(name)

			if p.InitialInterval > p.MaxInterval {
				t.Errorf("InitialInterval (%v) > MaxInterval (%v)", p.InitialInterval, p.MaxInterval)
			}
			if p.EscalateAfter > p.MaxDuration {
				t.Errorf("EscalateAfter (%v) > MaxDuration (%v)", p.EscalateAfter, p.MaxDuration)
			}
			if p.MaxResponseRounds <= 0 {
				t.Errorf("MaxResponseRounds = %d, must be > 0", p.MaxResponseRounds)
			}
		})
	}
}
