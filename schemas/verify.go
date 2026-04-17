package schemas

// VerifyOutput is the structured output for the verify phase.
type VerifyOutput struct {
	TicketKey       string            `json:"ticket_key"`
	Verdict         string            `json:"verdict"` // PASS, FAIL
	CriteriaResults []CriterionResult `json:"criteria_results"`
	CommandResults  []CommandResult   `json:"command_results"`
	CodeIssues      []CodeIssue       `json:"code_issues,omitempty"`
	FixesRequired   []string          `json:"fixes_required,omitempty"`
}

// CriterionResult is the pass/fail result for a single acceptance criterion.
type CriterionResult struct {
	Criterion string `json:"criterion"`
	Passed    bool   `json:"passed"`
	Evidence  string `json:"evidence"`
}

// CommandResult is the result of running a verification command.
type CommandResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Passed   bool   `json:"passed"`
}

// CodeIssue is a problem found during code review.
type CodeIssue struct {
	File         string `json:"file"`
	Line         int    `json:"line,omitempty"`
	Severity     string `json:"severity"` // critical, major, minor
	Issue        string `json:"issue"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
}
