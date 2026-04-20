package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/decko/soda/schemas"
)

// PromptData is the template context for phase prompts.
// This is a plain data struct with no methods to prevent
// side-effecting calls from templates.
type PromptData struct {
	Ticket         TicketData
	Config         PromptConfigData
	Artifacts      ArtifactData
	Context        ContextData
	DetectedStack  DetectedStackData
	WorktreePath   string
	Branch         string
	BaseBranch     string
	ReviewComments string
	ReworkFeedback *ReworkFeedback
	DiffContext    string // git diff of current branch vs base, injected for monitor and corrective phases
}

// DetectedStackData holds auto-detected project stack information from the
// repository. Populated by detect.Detect and injected into prompts so
// templates can adapt behaviour based on the detected language, forge, and
// context files. Use {{if .DetectedStack.Language}} guards in templates.
type DetectedStackData struct {
	Language     string   // e.g. "go", "python", "typescript", "unknown"
	Forge        string   // e.g. "github", "gitlab", or ""
	Owner        string   // repository owner/org
	Repo         string   // repository name
	ContextFiles []string // well-known files found (e.g. "AGENTS.md", "CLAUDE.md")
}

// ReworkFeedback holds selective feedback from a failed verify phase
// or a review-rework verdict, injected into implement's prompt on resume.
//
// Current-cycle feedback is rebuilt from the latest verify.json or
// review.json on each rework cycle. After implement re-runs, verify
// and review re-run too (they depend on implement), overwriting the
// previous result files. So the top-level fields (Verdict, FixesRequired,
// etc.) always reflect only the most recent failures.
//
// PriorCycles preserves summarized context from earlier rework cycles
// (read from archived results like review.json.1, verify.json.2, etc.)
// so the LLM can see what was previously flagged and avoid repeating
// mistakes or regressing on issues that were already fixed.
type ReworkFeedback struct {
	Verdict        string
	Source         string // "verify" or "review"
	FixesRequired  []string
	FailedCriteria []FailedCriterion
	CodeIssues     []ReworkCodeIssue
	FailedCommands []FailedCommand
	ReviewFindings []EnrichedFinding
	PriorCycles    []PriorCycle
}

// EnrichedFinding wraps a ReviewFinding with code context for rework prompts.
type EnrichedFinding struct {
	schemas.ReviewFinding
	CodeSnippet string // ±5 lines around the finding's file:line; empty if unavailable
}

// PriorCycle holds a summarized snapshot of feedback from a previous
// rework cycle. Populated from archived result files (e.g., review.json.1)
// so the LLM has context about what was previously reported.
type PriorCycle struct {
	Cycle   int    // 1-based cycle number
	Source  string // "verify" or "review"
	Verdict string
	Summary string // human-readable summary of findings from that cycle
}

// FailedCriterion is a single acceptance criterion that failed verification.
type FailedCriterion struct {
	Criterion string
	Evidence  string
}

// ReworkCodeIssue is a critical or major code issue found during verification.
type ReworkCodeIssue struct {
	File         string
	Line         int
	Severity     string
	Issue        string
	SuggestedFix string
}

// FailedCommand is a verification command that failed, with truncated output.
type FailedCommand struct {
	Command  string
	ExitCode int
	Output   string
}

// TicketData holds ticket fields for prompt templates.
// Decoupled from ticket.Ticket to keep pipeline independent of ticket package.
type TicketData struct {
	Key                string
	Summary            string
	Description        string
	Type               string
	Priority           string
	AcceptanceCriteria []string
	Comments           []TicketComment
	ExistingSpec       string
	ExistingPlan       string
}

// TicketComment holds a single ticket comment for prompt templates.
// Named TicketComment (not Comment) to avoid ambiguity with PR/review comments.
type TicketComment struct {
	Author    string
	Body      string
	CreatedAt string
}

// PromptConfigData holds config fields accessible from templates.
type PromptConfigData struct {
	Repos          []RepoConfig
	Repo           RepoConfig
	Formatter      string
	TestCommand    string
	VerifyCommands []string
}

// RepoConfig holds per-repo configuration for prompts.
type RepoConfig struct {
	Name        string   `yaml:"name"`
	Forge       string   `yaml:"forge"`
	PushTo      string   `yaml:"push_to"`
	Target      string   `yaml:"target"`
	Description string   `yaml:"description"`
	Formatter   string   `yaml:"formatter"`
	TestCommand string   `yaml:"test_command"`
	Labels      []string `yaml:"labels"`
	Trailers    []string `yaml:"trailers"`
}

// ArtifactData holds rendered artifacts from previous phases.
type ArtifactData struct {
	Triage    string
	Plan      string
	Implement string
	Verify    string
	Review    string
	Patch     string
	Submit    SubmitArtifact
}

// SubmitArtifact holds parsed fields from the submit phase output.
type SubmitArtifact struct {
	PRURL string
}

// ContextData holds injected context content for prompts.
type ContextData struct {
	ProjectContext  string
	RepoConventions string
	Gotchas         string
}

// PromptLoader resolves prompt templates from the filesystem.
// Directories are searched in order; the first match wins.
type PromptLoader struct {
	dirs []string
}

// LoadResult holds the outcome of a template load, including where the
// template was found and whether validation caused a fallback.
type LoadResult struct {
	// Content is the raw template text.
	Content string
	// Source is the resolved file path from which the template was loaded.
	Source string
	// IsOverride is true when the template came from an earlier directory
	// (user override) rather than the last directory (embedded default).
	IsOverride bool
	// Fallback is true when an override was found but failed validation,
	// causing the loader to fall back to a later directory.
	Fallback bool
	// FallbackReason describes why the override was rejected, if Fallback is true.
	FallbackReason string
}

// NewPromptLoader creates a loader that searches the given directories in order.
func NewPromptLoader(dirs ...string) *PromptLoader {
	return &PromptLoader{dirs: dirs}
}

// Load returns the template content for the given filename.
// Searches directories in order, returning the first match.
func (loader *PromptLoader) Load(name string) (string, error) {
	result, err := loader.LoadWithSource(name)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// LoadWithSource returns the template content along with source metadata.
// Override templates (found in earlier directories) are validated; if
// validation fails, the loader logs the reason and falls back to the
// next directory. The last directory is assumed to hold trusted embedded
// defaults and is not validated.
func (loader *PromptLoader) LoadWithSource(name string) (*LoadResult, error) {
	cleaned := filepath.Clean(name)
	if strings.Contains(cleaned, "..") {
		return nil, fmt.Errorf("prompt: path traversal rejected: %s", name)
	}

	lastIdx := len(loader.dirs) - 1
	var fallbackReason string

	for i, dir := range loader.dirs {
		path := filepath.Join(dir, cleaned)

		// Verify resolved path stays within the directory.
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) && absPath != absDir {
			return nil, fmt.Errorf("prompt: path traversal rejected: %s", name)
		}

		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("prompt: read %s: %w", path, err)
		}

		content := string(data)
		isOverride := i < lastIdx

		// Validate override templates; skip invalid ones with fallback.
		if isOverride {
			if verr := ValidateTemplate(content); verr != nil {
				fallbackReason = fmt.Sprintf("override %s invalid: %v", absPath, verr)
				continue
			}
		}

		result := &LoadResult{
			Content:    content,
			Source:     absPath,
			IsOverride: isOverride,
		}
		if fallbackReason != "" {
			result.Fallback = true
			result.FallbackReason = fallbackReason
		}
		return result, nil
	}

	return nil, fmt.Errorf("prompt: %s not found in %v", name, loader.dirs)
}

// promptFuncs is a shared FuncMap registered on every prompt template.
// Go templates have no built-in arithmetic; the add function enables
// 1-based display indexing via {{add $idx 1}}.
var promptFuncs = template.FuncMap{
	"add": func(a, b int) int { return a + b },
}

// ValidateTemplate parses a Go text/template and executes it against a
// zero-value PromptData. This catches syntax errors and references to
// fields that don't exist on PromptData. Templates that only fail at
// render time due to missing runtime data (e.g. nil pointer deref on
// optional sections) are not caught here — they require RenderPrompt.
func ValidateTemplate(tmpl string) error {
	parsed, err := template.New("prompt").Funcs(promptFuncs).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return fmt.Errorf("prompt: parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := parsed.Execute(&buf, PromptData{}); err != nil {
		return fmt.Errorf("prompt: validate template: %w", err)
	}

	return nil
}

// RenderPrompt executes a Go text/template against the given data.
func RenderPrompt(tmpl string, data PromptData) (string, error) {
	parsed, err := template.New("prompt").Funcs(promptFuncs).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("prompt: parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("prompt: render template: %w", err)
	}

	return buf.String(), nil
}
