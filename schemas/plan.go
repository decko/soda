package schemas

// PlanOutput is the structured output for the plan phase.
type PlanOutput struct {
	TicketKey    string         `json:"ticket_key"`
	Approach     string         `json:"approach"`
	Tasks        []PlanTask     `json:"tasks"`
	Verification VerifyStrategy `json:"verification"`
	Deviations   []string       `json:"deviations,omitempty"`
}

// PlanTask is an atomic unit of work within the plan.
type PlanTask struct {
	ID          string   `json:"id"`                   // e.g. "T1", "T2"
	Description string   `json:"description"`          // what to do
	Files       []string `json:"files"`                // files to create or modify
	DoneWhen    string   `json:"done_when"`            // verifiable condition
	DependsOn   []string `json:"depends_on,omitempty"` // task IDs
}

// VerifyStrategy defines how to verify the implementation.
type VerifyStrategy struct {
	Commands    []string `json:"commands"`               // shell commands to run
	ManualSteps []string `json:"manual_steps,omitempty"` // human verification needed
}
