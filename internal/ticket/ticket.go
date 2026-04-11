package ticket

// Ticket holds the common fields extracted from any ticket source.
// Prompt templates access these fields directly (e.g., .Ticket.Key, .Ticket.Summary).
// Source-specific data is available via RawFields.
type Ticket struct {
	Key                string
	Summary            string
	Description        string
	Type               string
	Priority           string
	Status             string
	Labels             []string
	AcceptanceCriteria []string
	RawFields          map[string]any
}
