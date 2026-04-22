package schemas

// SpecOutput is the structured output for the spec generation command.
// Used to generate the JSON schema passed to --json-schema.
type SpecOutput struct {
	Title              string      `json:"title"`
	Summary            string      `json:"summary"`
	AcceptanceCriteria []string    `json:"acceptance_criteria"`
	FilesToRead        []FileRef   `json:"files_to_read"`
	FilesToWrite       []FileRef   `json:"files_to_write"`
	DoNotRead          []FileRef   `json:"do_not_read"`
	IntegrationPoints  int         `json:"integration_points"`
	TokenBudget        TokenBudget `json:"token_budget"`
	SuggestedLabels    []string    `json:"suggested_labels"`
	TicketBody         string      `json:"ticket_body"` // full markdown ticket body ready for issue creation
}

// FileRef identifies a file with optional context about why it's relevant.
type FileRef struct {
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
	Lines  int    `json:"lines,omitempty"` // estimated line count
}

// TokenBudget estimates the token cost for implementing the ticket.
type TokenBudget struct {
	ReadTokens   int    `json:"read_tokens"`   // read_lines × 5
	WriteTokens  int    `json:"write_tokens"`  // write_lines × 8
	ToolTokens   int    `json:"tool_tokens"`   // ~15K-20K
	ReviewCost   int    `json:"review_cost"`   // ~20K if medium+
	BufferTokens int    `json:"buffer_tokens"` // ~30K safety buffer
	TotalTokens  int    `json:"total_tokens"`
	Verdict      string `json:"verdict"` // "fits", "tight", "split_required"
}
