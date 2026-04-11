package ticket

import (
	"regexp"
	"strings"
)

var (
	// Matches common "Acceptance Criteria" headings in wiki markup, markdown, and plain text.
	headingRe = regexp.MustCompile(
		`(?i)^(?:h[1-6]\.\s+|\#{1,6}\s+|\*{2})?[Aa]cceptance\s+[Cc]riteria\*{0,2}:?\s*$`,
	)
	// Matches the start of a new heading (signals end of AC section).
	nextHeadingRe = regexp.MustCompile(`^(?:h[1-6]\.\s+|\#{1,6}\s+)`)
	// Matches bullet items: *, -, or numbered (1., 2., etc.)
	bulletRe = regexp.MustCompile(`^\s*(?:[-*]|\d+\.)\s+(.+)`)
	// Strips checkbox prefixes: [ ], [x], [X]
	checkboxRe = regexp.MustCompile(`^\[[ xX]\]\s*`)
)

// ExtractCriteria parses a ticket description and returns acceptance criteria
// as a list of strings. It looks for a section headed "Acceptance Criteria"
// (in various formats) and extracts bullet/numbered items from it.
// Returns nil if no acceptance criteria section is found.
func ExtractCriteria(description string) []string {
	lines := strings.Split(description, "\n")
	var criteria []string
	inSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if headingRe.MatchString(trimmed) {
			inSection = true
			continue
		}

		if inSection {
			if nextHeadingRe.MatchString(trimmed) {
				break
			}
			if match := bulletRe.FindStringSubmatch(line); match != nil {
				item := strings.TrimSpace(match[1])
				item = checkboxRe.ReplaceAllString(item, "")
				criteria = append(criteria, item)
			}
		}
	}

	return criteria
}
