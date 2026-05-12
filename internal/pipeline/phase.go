package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/decko/soda/schemas"
	"gopkg.in/yaml.v3"
)

// isFilePath returns true if s looks like a file path rather than an inline
// JSON schema string. It checks for path separators or a .json suffix.
// Strings that start with '{' are treated as inline JSON, not file paths.
func isFilePath(s string) bool {
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		return false
	}
	return strings.ContainsAny(s, "/\\") || strings.HasSuffix(s, ".json")
}

// ModelRoutingConfig holds configuration for automatic model escalation
// when parse failures exceed a threshold during retry loops.
type ModelRoutingConfig struct {
	FallbackThreshold int `yaml:"fallback_threshold"` // number of parse failures before escalating to global model; 0 disables
}

// PhasePipeline holds the ordered list of phases loaded from phases.yaml.
type PhasePipeline struct {
	Name         string             `yaml:"-"` // pipeline name; set after loading (e.g. "default", "fast")
	Phases       []PhaseConfig      `yaml:"phases"`
	ModelRouting ModelRoutingConfig `yaml:"model_routing"` // optional model routing quality gate
}

// PhaseConfig holds the configuration for a single phase.
type PhaseConfig struct {
	Name             string            `yaml:"name"`
	Prompt           string            `yaml:"prompt"`
	Schema           string            `yaml:"schema"`
	Model            string            `yaml:"model,omitempty"` // per-phase model override; empty uses global EngineConfig.Model
	Tools            []string          `yaml:"tools"`
	Timeout          Duration          `yaml:"timeout"`
	TimeoutOverrides []TimeoutOverride `yaml:"timeout_overrides,omitempty"` // conditional timeout overrides; first match wins
	ModelOverrides   []ModelOverride   `yaml:"model_overrides,omitempty"`   // conditional model overrides; first match wins
	Type             string            `yaml:"type"`
	Retry            RetryConfig       `yaml:"retry"`
	DependsOn        []string          `yaml:"depends_on"`
	Polling          *PollingConfig    `yaml:"polling,omitempty"`
	Reviewers        []ReviewerConfig  `yaml:"reviewers,omitempty"`
	ReviewerStagger  Duration          `yaml:"reviewer_stagger,omitempty"`
	MinReviewers     int               `yaml:"min_reviewers,omitempty"` // minimum successful reviewers required; 0 means all must succeed
	Rework           *ReworkConfig     `yaml:"rework,omitempty"`
	Corrective       *CorrectiveConfig `yaml:"corrective,omitempty"`
	FeedbackFrom     []string          `yaml:"feedback_from,omitempty"` // ordered feedback sources injected into prompt
	ContextBudget    int               `yaml:"prompt_budget,omitempty"` // max prompt tokens for adaptive fitting; 0 uses global default
	Condition        string            `yaml:"condition,omitempty"`     // Go text/template evaluated at runtime; output "false" skips the phase
}

// ReworkConfig configures rework routing for a phase. When a phase with
// this config produces a rework verdict, the engine routes back to Target.
type ReworkConfig struct {
	Target string `yaml:"target"` // phase to route back to on rework
}

// CorrectiveConfig configures corrective phase routing. Lives on the
// triggering phase (e.g., verify), not on the corrective phase (e.g., patch).
type CorrectiveConfig struct {
	Phase       string `yaml:"phase"`        // corrective phase to route to on failure
	MaxAttempts int    `yaml:"max_attempts"` // max corrective cycles before exhaustion
	OnExhausted string `yaml:"on_exhausted"` // "stop" | "escalate" | "retry"
	EscalateTo  string `yaml:"escalate_to"`  // target for "escalate" policy
}

// ReviewerConfig holds configuration for a single specialist reviewer
// in a parallel-review phase.
type ReviewerConfig struct {
	Name      string `yaml:"name"`
	Prompt    string `yaml:"prompt"`
	Focus     string `yaml:"focus"`
	Model     string `yaml:"model,omitempty"`
	Condition string `yaml:"condition,omitempty"` // Go text/template evaluated at runtime; output "false" skips the reviewer
}

// TimeoutOverride defines a conditional timeout override for a phase.
// When the Condition template renders to anything other than "false",
// the override's Timeout replaces the phase default. Overrides are
// evaluated in order; the first match wins.
type TimeoutOverride struct {
	Condition string   `yaml:"condition"`
	Timeout   Duration `yaml:"timeout"`
	Label     string   `yaml:"label,omitempty"` // human-readable label for event logging
}

// ModelOverride defines a conditional model override for a phase.
// When the Condition template renders to anything other than "false",
// the override's Model replaces the phase/global default. Overrides are
// evaluated in order; the first match wins.
type ModelOverride struct {
	Condition string `yaml:"condition"`
	Model     string `yaml:"model"`
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
	Profile           MonitorProfileName `yaml:"profile,omitempty"`             // preset profile name (conservative, smart, aggressive)
	RespondToComments bool               `yaml:"respond_to_comments,omitempty"` // enable comment classification + response (requires self_user)
	AutoMerge         bool               `yaml:"auto_merge,omitempty"`          // merge PR when checks green + approved (see #338 for safeguards)
	MergeMethod       string             `yaml:"merge_method,omitempty"`        // merge method: "merge", "squash", "rebase"; defaults to "squash"
	MergeLabels       []string           `yaml:"merge_labels,omitempty"`        // required PR labels before auto-merge proceeds
	AutoMergeTimeout  Duration           `yaml:"auto_merge_timeout,omitempty"`  // max wait after approval before giving up; defaults to 30m
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

	// Build a set of known phase names for cross-reference validation.
	phaseNames := make(map[string]struct{}, len(pipeline.Phases))
	for _, p := range pipeline.Phases {
		phaseNames[p.Name] = struct{}{}
	}

	// Validate cross-references: DependsOn, ReworkConfig.Target,
	// FeedbackFrom, CorrectiveConfig.Phase, and CorrectiveConfig.EscalateTo
	// must refer to phases that exist in the pipeline.
	for _, phase := range pipeline.Phases {
		for _, dep := range phase.DependsOn {
			if _, ok := phaseNames[dep]; !ok {
				return nil, fmt.Errorf("pipeline: phase %q depends_on references unknown phase %q", phase.Name, dep)
			}
		}
		if phase.Rework != nil {
			if _, ok := phaseNames[phase.Rework.Target]; !ok {
				return nil, fmt.Errorf("pipeline: phase %q rework target %q not found in pipeline", phase.Name, phase.Rework.Target)
			}
		}
		for _, src := range phase.FeedbackFrom {
			if _, ok := phaseNames[src]; !ok {
				return nil, fmt.Errorf("pipeline: phase %q feedback_from references unknown phase %q", phase.Name, src)
			}
		}
		if phase.MinReviewers > len(phase.Reviewers) {
			return nil, fmt.Errorf("pipeline: phase %q min_reviewers (%d) exceeds number of configured reviewers (%d)", phase.Name, phase.MinReviewers, len(phase.Reviewers))
		}
		for _, reviewer := range phase.Reviewers {
			if reviewer.Condition != "" {
				if _, err := template.New("condition").Parse(reviewer.Condition); err != nil {
					return nil, fmt.Errorf("pipeline: phase %q reviewer %q has invalid condition template: %w", phase.Name, reviewer.Name, err)
				}
			}
		}
		if phase.Condition != "" {
			if _, err := template.New("condition").Parse(phase.Condition); err != nil {
				return nil, fmt.Errorf("pipeline: phase %q has invalid condition template: %w", phase.Name, err)
			}
		}
		for i, override := range phase.TimeoutOverrides {
			if override.Condition == "" {
				return nil, fmt.Errorf("pipeline: phase %q timeout_overrides[%d] has empty condition", phase.Name, i)
			}
			if override.Timeout.Duration <= 0 {
				return nil, fmt.Errorf("pipeline: phase %q timeout_overrides[%d] has zero or missing timeout", phase.Name, i)
			}
			if _, err := template.New("condition").Parse(override.Condition); err != nil {
				return nil, fmt.Errorf("pipeline: phase %q timeout_overrides[%d] has invalid condition template: %w", phase.Name, i, err)
			}
		}
		for i, override := range phase.ModelOverrides {
			if override.Condition == "" {
				return nil, fmt.Errorf("pipeline: phase %q model_overrides[%d] has empty condition", phase.Name, i)
			}
			if override.Model == "" {
				return nil, fmt.Errorf("pipeline: phase %q model_overrides[%d] has empty model", phase.Name, i)
			}
			if _, err := template.New("condition").Parse(override.Condition); err != nil {
				return nil, fmt.Errorf("pipeline: phase %q model_overrides[%d] has invalid condition template: %w", phase.Name, i, err)
			}
		}
		if phase.Corrective != nil {
			if _, ok := phaseNames[phase.Corrective.Phase]; !ok {
				return nil, fmt.Errorf("pipeline: phase %q corrective.phase %q not found", phase.Name, phase.Corrective.Phase)
			}
			if phase.Corrective.EscalateTo != "" {
				if _, ok := phaseNames[phase.Corrective.EscalateTo]; !ok {
					return nil, fmt.Errorf("pipeline: phase %q corrective.escalate_to %q not found", phase.Name, phase.Corrective.EscalateTo)
				}
			}
		}
	}

	// Resolve schemas: if a phase has no inline schema, look up the
	// generated schema by phase name from the schemas package.
	// If the schema value looks like a file path, read the file contents.
	pipelineDir := filepath.Dir(path)
	for idx := range pipeline.Phases {
		phase := &pipeline.Phases[idx]
		if strings.TrimSpace(phase.Schema) == "" {
			if generated := schemas.SchemaFor(phase.Name); generated != "" {
				phase.Schema = generated
			}
		} else if isFilePath(phase.Schema) {
			schemaPath := filepath.Clean(phase.Schema)
			if strings.Contains(schemaPath, "..") {
				return nil, fmt.Errorf("pipeline: phase %q: schema path traversal rejected: %s", phase.Name, phase.Schema)
			}
			if !filepath.IsAbs(schemaPath) {
				schemaPath = filepath.Join(pipelineDir, schemaPath)
			}
			schemaData, err := os.ReadFile(schemaPath)
			if err != nil {
				return nil, fmt.Errorf("pipeline: phase %q: read schema file %s: %w", phase.Name, phase.Schema, err)
			}
			if !json.Valid(schemaData) {
				return nil, fmt.Errorf("pipeline: phase %q: schema file %s is not valid JSON", phase.Name, phase.Schema)
			}
			phase.Schema = string(schemaData)
		}
	}

	return &pipeline, nil
}
