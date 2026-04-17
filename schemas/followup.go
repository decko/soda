package schemas

// FollowUpOutput is the structured output for the follow-up phase.
// Reports actions taken for each minor review finding (issue created,
// updated, or skipped).
type FollowUpOutput struct {
	TicketKey string           `json:"ticket_key"`
	Actions   []FollowUpAction `json:"actions"`
}

// FollowUpAction describes the action taken for a single minor finding.
type FollowUpAction struct {
	Finding      string `json:"finding"`
	Action       string `json:"action"` // "created", "updated", "skipped"
	TicketURL    string `json:"ticket_url,omitempty"`
	TicketNumber int    `json:"ticket_number,omitempty"`
	Reason       string `json:"reason,omitempty"`
}
