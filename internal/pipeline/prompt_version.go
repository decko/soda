package pipeline

import (
	"fmt"
	"regexp"
	"strconv"
)

// promptVersionRe matches the soda:prompt-version=N header in raw template text.
// Applied before template parsing because Go text/template strips comments.
var promptVersionRe = regexp.MustCompile(`soda:prompt-version=(\d+)`)

// ExtractPromptVersion extracts the prompt version from raw template text.
// Returns 0 when no version header is found.
func ExtractPromptVersion(rawText string) int {
	match := promptVersionRe.FindStringSubmatch(rawText)
	if len(match) < 2 {
		return 0
	}
	version, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return version
}

// embeddedPromptVersions maps prompt paths (e.g. "prompts/triage.md") to the
// expected prompt version. Unknown prompts return 0 and skip the version check.
var embeddedPromptVersions = map[string]int{
	"prompts/triage.md":              1,
	"prompts/plan.md":                1,
	"prompts/implement.md":           1,
	"prompts/patch.md":               1,
	"prompts/verify.md":              1,
	"prompts/review.md":              1,
	"prompts/review-go.md":           1,
	"prompts/review-harness.md":      1,
	"prompts/submit.md":              1,
	"prompts/follow-up.md":           1,
	"prompts/monitor.md":             1,
	"prompts/spec.md":                1,
	"prompts/quick-fix-implement.md": 1,
	"prompts/docs-plan.md":           1,
	"prompts/docs-implement.md":      1,
}

// EmbeddedPromptVersion returns the expected prompt version for the given
// prompt path. Returns 0 for unknown prompts (no version check needed).
func EmbeddedPromptVersion(promptPath string) int {
	return embeddedPromptVersions[promptPath]
}

// ScanPromptDataFields extracts top-level PromptData field names referenced
// in a template. It scans for patterns like {{.FieldName}} or
// {{- if .FieldName}} where FieldName starts with an uppercase letter.
// Returns a deduplicated sorted slice.
var promptFieldRe = regexp.MustCompile(`\{\{[^}]*?\.([A-Z][a-zA-Z0-9]*)`)

// ScanPromptDataFields extracts top-level PromptData field names referenced
// in a Go template string. It scans raw text for patterns like {{.Field}} or
// {{- if .Field.Sub}}, capturing the first uppercase identifier after a dot
// inside {{ }}, which naturally yields PromptData fields.
func ScanPromptDataFields(tmpl string) []string {
	matches := promptFieldRe.FindAllStringSubmatch(tmpl, -1)
	seen := make(map[string]bool)
	var fields []string
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		field := match[1]
		if !seen[field] {
			seen[field] = true
			fields = append(fields, field)
		}
	}
	return fields
}

// CheckPromptVersion compares the version found in the loaded template text
// against the expected embedded version for the given prompt path.
// Returns a non-nil warning message when versions differ.
// Returns nil when versions match or the prompt is unknown (custom phase).
func CheckPromptVersion(promptPath string, rawText string) *PromptVersionWarning {
	expected := EmbeddedPromptVersion(promptPath)
	if expected == 0 {
		// Unknown prompt path — skip version check.
		return nil
	}

	actual := ExtractPromptVersion(rawText)
	if actual == 0 {
		// No version header in the template.
		return &PromptVersionWarning{
			PromptPath:      promptPath,
			ActualVersion:   0,
			ExpectedVersion: expected,
			Reason:          "template has no soda:prompt-version header",
		}
	}

	if actual != expected {
		return &PromptVersionWarning{
			PromptPath:      promptPath,
			ActualVersion:   actual,
			ExpectedVersion: expected,
			Reason:          fmt.Sprintf("template version %d does not match expected version %d", actual, expected),
		}
	}

	return nil
}

// PromptVersionWarning describes a prompt version mismatch.
type PromptVersionWarning struct {
	PromptPath      string
	ActualVersion   int
	ExpectedVersion int
	Reason          string
}
