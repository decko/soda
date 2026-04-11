package schemas

// ImplementOutput is the structured output for the implement phase.
type ImplementOutput struct {
	TicketKey     string            `json:"ticket_key"`
	Branch        string            `json:"branch"`
	Commits       []CommitRecord    `json:"commits"`
	FilesChanged  []FileChange      `json:"files_changed"`
	TaskResults   []TaskResult      `json:"task_results"`
	TestsPassed   bool              `json:"tests_passed"`
	TestOutput    string            `json:"test_output,omitempty"`
	Deviations    []string          `json:"deviations,omitempty"`
}

// CommitRecord is a single git commit made during implementation.
type CommitRecord struct {
	Hash    string `json:"hash"`
	Message string `json:"message"`
	TaskID  string `json:"task_id"`
}

// FileChange records a file that was created, modified, or deleted.
type FileChange struct {
	Path   string `json:"path"`
	Action string `json:"action"` // created, modified, deleted
}

// TaskResult records the outcome of implementing a single task.
type TaskResult struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"` // completed, failed, skipped
	Reason    string `json:"reason,omitempty"` // why failed/skipped
}
