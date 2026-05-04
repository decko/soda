package schemas

// TriageOutput is the structured output for the triage phase.
// Used to generate the JSON schema passed to --json-schema.
type TriageOutput struct {
	TicketKey   string   `json:"ticket_key"`
	Repo        string   `json:"repo"`
	CodeArea    string   `json:"code_area"`
	Files       []string `json:"files"`
	Complexity  string   `json:"complexity"  jsonschema:"enum=low|medium|high"` // low, medium, high
	Approach    string   `json:"approach"`
	Risks       []string `json:"risks"`
	Automatable string   `json:"automatable" jsonschema:"enum=yes|no|partial"` // yes, no, partial
	BlockReason string   `json:"block_reason,omitempty"`                       // why not automatable
	SkipPlan    bool     `json:"skip_plan,omitempty"`                          // true when an existing plan is complete; engine uses ExistingPlan from ticket
}
