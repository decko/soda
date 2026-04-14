package pipeline

import (
	"fmt"
	"time"
)

// MonitorProfileName identifies a preset monitor behavior profile.
type MonitorProfileName string

const (
	ProfileConservative MonitorProfileName = "conservative"
	ProfileSmart        MonitorProfileName = "smart"
	ProfileAggressive   MonitorProfileName = "aggressive"
)

// MonitorProfile defines the behavioral parameters for the monitor phase.
// Profiles control how aggressively the monitor responds to comments,
// resolves nits, and rebases.
type MonitorProfile struct {
	Name MonitorProfileName `yaml:"name" json:"name"`

	// Polling parameters.
	InitialInterval time.Duration `yaml:"initial_interval" json:"initial_interval"`
	MaxInterval     time.Duration `yaml:"max_interval" json:"max_interval"`
	EscalateAfter   time.Duration `yaml:"escalate_after" json:"escalate_after"`
	MaxDuration     time.Duration `yaml:"max_duration" json:"max_duration"`

	// Response budget.
	MaxResponseRounds int `yaml:"max_response_rounds" json:"max_response_rounds"`

	// Behavior flags.
	AutoFixNits      bool `yaml:"auto_fix_nits" json:"auto_fix_nits"`             // Apply nit fixes without asking
	AutoRebase       bool `yaml:"auto_rebase" json:"auto_rebase"`                 // Auto-rebase on conflicts
	RespondToNonAuth bool `yaml:"respond_to_non_auth" json:"respond_to_non_auth"` // Respond to non-authoritative comments
}

// GetMonitorProfile returns a predefined profile by name.
// Returns an error for unknown profile names.
func GetMonitorProfile(name MonitorProfileName) (*MonitorProfile, error) {
	switch name {
	case ProfileConservative:
		return &MonitorProfile{
			Name:              ProfileConservative,
			InitialInterval:   5 * time.Minute,
			MaxInterval:       15 * time.Minute,
			EscalateAfter:     1 * time.Hour,
			MaxDuration:       8 * time.Hour,
			MaxResponseRounds: 2,
			AutoFixNits:       false,
			AutoRebase:        false,
			RespondToNonAuth:  false,
		}, nil

	case ProfileSmart:
		return &MonitorProfile{
			Name:              ProfileSmart,
			InitialInterval:   2 * time.Minute,
			MaxInterval:       5 * time.Minute,
			EscalateAfter:     30 * time.Minute,
			MaxDuration:       4 * time.Hour,
			MaxResponseRounds: 3,
			AutoFixNits:       true,
			AutoRebase:        true,
			RespondToNonAuth:  false,
		}, nil

	case ProfileAggressive:
		return &MonitorProfile{
			Name:              ProfileAggressive,
			InitialInterval:   1 * time.Minute,
			MaxInterval:       3 * time.Minute,
			EscalateAfter:     15 * time.Minute,
			MaxDuration:       2 * time.Hour,
			MaxResponseRounds: 5,
			AutoFixNits:       true,
			AutoRebase:        true,
			RespondToNonAuth:  true,
		}, nil

	default:
		return nil, fmt.Errorf("pipeline: unknown monitor profile %q (valid: conservative, smart, aggressive)", name)
	}
}

// ToPollingConfig converts a MonitorProfile to a PollingConfig for use
// with the existing monitor polling loop.
func (p *MonitorProfile) ToPollingConfig() *PollingConfig {
	return &PollingConfig{
		InitialInterval:   Duration{Duration: p.InitialInterval},
		MaxInterval:       Duration{Duration: p.MaxInterval},
		EscalateAfter:     Duration{Duration: p.EscalateAfter},
		MaxDuration:       Duration{Duration: p.MaxDuration},
		MaxResponseRounds: p.MaxResponseRounds,
	}
}

// ShouldApplyNit returns true if the profile allows auto-fixing nits.
func (p *MonitorProfile) ShouldApplyNit() bool {
	return p.AutoFixNits
}

// ShouldAutoRebase returns true if the profile allows auto-rebasing.
func (p *MonitorProfile) ShouldAutoRebase() bool {
	return p.AutoRebase
}

// ShouldRespondToNonAuth returns true if the profile allows responding
// to non-authoritative comments.
func (p *MonitorProfile) ShouldRespondToNonAuth() bool {
	return p.RespondToNonAuth
}
