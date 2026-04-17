package schemas

// PatchOutput is the structured output for the patch phase.
// Patch makes targeted fixes based on verify feedback, avoiding
// a full re-implementation.
type PatchOutput struct {
	TicketKey        string       `json:"ticket_key"`
	FixResults       []FixResult  `json:"fix_results"`
	FilesChanged     []FileChange `json:"files_changed"`
	TestsPassed      bool         `json:"tests_passed"`
	TooComplex       bool         `json:"too_complex"`
	TooComplexReason string       `json:"too_complex_reason,omitempty"`
}

// FixResult records the outcome of attempting a single fix from verify feedback.
type FixResult struct {
	FixIndex    int    `json:"fix_index"`
	Status      string `json:"status"` // "fixed", "partial", "cannot_fix"
	Description string `json:"description"`
	Reason      string `json:"reason,omitempty"`
}
