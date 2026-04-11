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
	RawFields          map[string]any `json:"raw_fields,omitempty"`
}
