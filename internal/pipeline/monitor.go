package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MonitorStatus represents the current status of the monitor phase.
type MonitorStatus string

const (
	MonitorPolling   MonitorStatus = "polling"
	MonitorCompleted MonitorStatus = "completed"
	MonitorFailed    MonitorStatus = "failed"
	MonitorMaxRounds MonitorStatus = "max_rounds"
)

// MonitorState holds the persistent state of the monitor polling loop.
// Stored as .soda/<ticket>/monitor.json.
type MonitorState struct {
	PRURL             string        `json:"pr_url"`
	PollCount         int           `json:"poll_count"`
	ResponseRounds    int           `json:"response_rounds"`
	ReplyRounds       int           `json:"reply_rounds"`
	MaxResponseRounds int           `json:"max_response_rounds"`
	LastCommentID     string        `json:"last_comment_id,omitempty"`
	LastCIStatus      string        `json:"last_ci_status,omitempty"`
	LastPolledAt      time.Time     `json:"last_polled_at"`
	StartedAt         time.Time     `json:"started_at"`
	Status            MonitorStatus `json:"status"`
}

// PRPoller polls a pull request for changes.
// Implementations should use the forge's API (e.g., gh cli) to query state.
type PRPoller interface {
	// GetPRStatus returns the current status of a pull request.
	GetPRStatus(ctx context.Context, prURL string) (*PRStatus, error)
	// GetNewComments returns review comments posted after the given comment ID.
	// If afterID is empty, returns all comments.
	GetNewComments(ctx context.Context, prURL string, afterID string) ([]PRComment, error)
	// GetCIStatus returns the current CI check status for the PR's head commit.
	GetCIStatus(ctx context.Context, prURL string) (*CIStatus, error)
	// PostComment posts a comment to the pull request. Used for canned
	// acknowledgments and reply summaries.
	PostComment(ctx context.Context, prURL string, body string) error
}

// PRStatus holds the current state of a pull request.
type PRStatus struct {
	State    string // "open", "closed", "merged"
	Approved bool   // true if at least one approving review
}

// PRComment represents a review comment on a pull request.
type PRComment struct {
	ID        string
	Author    string
	Body      string
	Path      string    // file path (empty for general PR comments)
	Line      int       // line number (0 for general comments)
	CreatedAt time.Time // when the comment was posted (zero if unknown)
}

// CIStatus holds the aggregate CI status and per-job details.
type CIStatus struct {
	Overall string      // "success", "failure", "pending", "unknown"
	Jobs    []CIJobInfo // individual job details
}

// CIJobInfo describes a single CI job/check.
type CIJobInfo struct {
	Name       string
	Status     string // "success", "failure", "pending", "skipped"
	Conclusion string // raw conclusion from API
	ExitCode   int    // non-zero on failure (if available)
}

// WriteMonitorState persists the monitor state to monitor_state.json atomically.
// Named monitor_state.json (not monitor.json) to avoid collision with the
// phase result file written by WriteResult.
func (s *State) WriteMonitorState(ms *MonitorState) error {
	data, err := json.MarshalIndent(ms, "", "  ")
	if err != nil {
		return fmt.Errorf("pipeline: marshal monitor state: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(s.dir, "monitor_state.json")
	if err := atomicWrite(path, data); err != nil {
		return fmt.Errorf("pipeline: write monitor state: %w", err)
	}
	return nil
}

// ReadMonitorState reads the monitor state from monitor_state.json.
// Returns nil and os.ErrNotExist if the file does not exist.
func (s *State) ReadMonitorState() (*MonitorState, error) {
	path := filepath.Join(s.dir, "monitor_state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ms MonitorState
	if err := json.Unmarshal(data, &ms); err != nil {
		return nil, fmt.Errorf("pipeline: parse monitor state: %w", err)
	}
	return &ms, nil
}
