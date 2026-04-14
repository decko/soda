package schemas

// MonitorOutput is the structured output for a single monitor cycle.
type MonitorOutput struct {
	TicketKey       string          `json:"ticket_key"`
	PRURL           string          `json:"pr_url"`
	CommentsHandled []CommentAction `json:"comments_handled"`
	FilesChanged    []FileChange    `json:"files_changed,omitempty"`
	Commits         []CommitRecord  `json:"commits,omitempty"`
	TestsPassed     bool            `json:"tests_passed"`
}

// CommentAction records how a review comment was addressed.
type CommentAction struct {
	CommentID      string `json:"comment_id"`
	Author         string `json:"author"`
	Content        string `json:"content"`
	Action         string `json:"action"` // fixed, explained, deferred, skipped
	Response       string `json:"response"`
	Classification string `json:"classification"` // code_change, question, nit, approval, dismissal, bot_generated, self_authored
	Authoritative  bool   `json:"authoritative"`  // whether the author has authority over the file
}

// MonitorProfileConfig describes the monitor profile in configuration.
type MonitorProfileConfig struct {
	Name             string `json:"name"` // conservative, smart, aggressive
	AutoFixNits      bool   `json:"auto_fix_nits"`
	AutoRebase       bool   `json:"auto_rebase"`
	RespondToNonAuth bool   `json:"respond_to_non_auth"`
}
