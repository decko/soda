package schemas

// ReviewOutput is the structured output for the review phase.
// Merges findings from multiple specialist reviewers into a single result.
type ReviewOutput struct {
	TicketKey string          `json:"ticket_key"`
	Findings  []ReviewFinding `json:"findings"`
	Verdict   string          `json:"verdict"` // "pass", "rework", "pass-with-follow-ups"
}

// ReviewFinding is a single issue found by a specialist reviewer.
type ReviewFinding struct {
	Source     string `json:"source"`   // reviewer name, e.g. "go-specialist" or "ai-harness"
	Severity   string `json:"severity"` // "critical", "major", "minor"
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
}
