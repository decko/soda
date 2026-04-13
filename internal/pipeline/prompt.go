package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// PromptData is the template context for phase prompts.
// This is a plain data struct with no methods to prevent
// side-effecting calls from templates.
type PromptData struct {
	Ticket         TicketData
	Config         PromptConfigData
	Artifacts      ArtifactData
	Context        ContextData
	WorktreePath   string
	Branch         string
	BaseBranch     string
	ReviewComments string
	VerifyFeedback string
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

// NewPromptLoader creates a loader that searches the given directories in order.
func NewPromptLoader(dirs ...string) *PromptLoader {
	return &PromptLoader{dirs: dirs}
}

// Load returns the template content for the given filename.
// Searches directories in order, returning the first match.
func (loader *PromptLoader) Load(name string) (string, error) {
	// Reject path traversal attempts
	cleaned := filepath.Clean(name)
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("prompt: path traversal rejected: %s", name)
	}

	for _, dir := range loader.dirs {
		path := filepath.Join(dir, cleaned)

		// Verify resolved path stays within the directory
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) && absPath != absDir {
			return "", fmt.Errorf("prompt: path traversal rejected: %s", name)
		}

		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("prompt: read %s: %w", path, err)
		}
		return string(data), nil
	}

	return "", fmt.Errorf("prompt: %s not found in %v", name, loader.dirs)
}

// RenderPrompt executes a Go text/template against the given data.
func RenderPrompt(tmpl string, data PromptData) (string, error) {
	parsed, err := template.New("prompt").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("prompt: parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("prompt: render template: %w", err)
	}

	return buf.String(), nil
}
