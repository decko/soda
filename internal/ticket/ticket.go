package ticket

// Ticket holds the common fields extracted from any ticket source.
// Prompt templates access these fields directly (e.g., .Ticket.Key, .Ticket.Summary).
// Source-specific data is available via RawFields.
type Ticket struct {
	Key                string         `json:"key"`
	Summary            string         `json:"summary"`
	Description        string         `json:"description"`
	Type               string         `json:"type"`
	Priority           string         `json:"priority"`
	Status             string         `json:"status"`
	Labels             []string       `json:"labels"`
	AcceptanceCriteria []string       `json:"acceptance_criteria"`
	Comments           []Comment      `json:"comments,omitempty"`
	ExistingSpec       string         `json:"existing_spec,omitempty"`
	ExistingPlan       string         `json:"existing_plan,omitempty"`
	RawFields          map[string]any `json:"raw_fields,omitempty"`
}

// Comment holds a single comment from a ticket.
type Comment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}
