package schemas

// PatchOutput is the structured output for the patch phase.
type PatchOutput struct {
	TicketKey        string         `json:"ticket_key"`
	Branch           string         `json:"branch"`
	FixResults       []FixResult    `json:"fix_results"`
	Commits          []CommitRecord `json:"commits"`
	FilesChanged     []FileChange   `json:"files_changed"`
	TestsPassed      bool           `json:"tests_passed"`
	TestOutput       string         `json:"test_output,omitempty"`
	TooComplex       bool           `json:"too_complex"`
	TooComplexReason string         `json:"too_complex_reason,omitempty"`
}

// FixResult records the outcome of applying a single fix from the verify feedback.
type FixResult struct {
	FixIndex int    `json:"fix_index"`
	Status   string `json:"status"` // applied, skipped, failed
	Reason   string `json:"reason,omitempty"`
}
