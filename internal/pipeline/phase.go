package pipeline

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/decko/soda/schemas"
	"gopkg.in/yaml.v3"
)

// PhasePipeline holds the ordered list of phases loaded from phases.yaml.
type PhasePipeline struct {
	Phases []PhaseConfig `yaml:"phases"`
}

// PhaseConfig holds the configuration for a single phase.
type PhaseConfig struct {
	Name      string           `yaml:"name"`
	Prompt    string           `yaml:"prompt"`
	Schema    string           `yaml:"schema"`
	Tools     []string         `yaml:"tools"`
	Timeout   Duration         `yaml:"timeout"`
	Type      string           `yaml:"type"`
	Retry     RetryConfig      `yaml:"retry"`
	DependsOn []string         `yaml:"depends_on"`
	Polling   *PollingConfig   `yaml:"polling,omitempty"`
	Reviewers []ReviewerConfig `yaml:"reviewers,omitempty"`
}

// ReviewerConfig holds configuration for a single specialist reviewer
// in a parallel-review phase.
type ReviewerConfig struct {
	Name   string `yaml:"name"`
	Prompt string `yaml:"prompt"`
	Focus  string `yaml:"focus"`
	Model  string `yaml:"model,omitempty"`
}

// RetryConfig holds per-category retry limits.
type RetryConfig struct {
	Transient int `yaml:"transient"`
	Parse     int `yaml:"parse"`
	Semantic  int `yaml:"semantic"`
}

// PollingConfig holds monitor-phase polling parameters.
type PollingConfig struct {
	InitialInterval   Duration           `yaml:"initial_interval"`
	MaxInterval       Duration           `yaml:"max_interval"`
	EscalateAfter     Duration           `yaml:"escalate_after"`
	MaxDuration       Duration           `yaml:"max_duration"`
	MaxResponseRounds int                `yaml:"max_response_rounds"`
	Profile           MonitorProfileName `yaml:"profile,omitempty"` // preset profile name (conservative, smart, aggressive)
}

// Duration wraps time.Duration for YAML unmarshaling.
type Duration struct {
	time.Duration
}

// UnmarshalYAML parses a Go duration string (e.g., "3m", "4h").
func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw string
	if err := unmarshal(&raw); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

// LoadPipeline reads and parses a phases.yaml file.
func LoadPipeline(path string) (*PhasePipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pipeline: read phases config %s: %w", path, err)
	}

	var pipeline PhasePipeline
	if err := yaml.Unmarshal(data, &pipeline); err != nil {
		return nil, fmt.Errorf("pipeline: parse phases config %s: %w", path, err)
	}

	if len(pipeline.Phases) == 0 {
		return nil, fmt.Errorf("pipeline: no phases defined in %s", path)
	}

	// Resolve schemas: if a phase has no inline schema, look up the
	// generated schema by phase name from the schemas package.
	for idx := range pipeline.Phases {
		phase := &pipeline.Phases[idx]
		if strings.TrimSpace(phase.Schema) == "" {
			if generated := schemas.SchemaFor(phase.Name); generated != "" {
				phase.Schema = generated
			}
		}
	}

	return &pipeline, nil
}
