package progress

import (
	"encoding/json"
	"fmt"
	"strings"
)

// phaseDescription returns a human-readable description for a running phase.
func phaseDescription(phase string) string {
	switch phase {
	case "triage":
		return "classifying ticket..."
	case "plan":
		return "designing implementation..."
	case "implement":
		return "writing code..."
	case "verify":
		return "checking acceptance criteria..."
	case "submit":
		return "creating PR..."
	case "monitor":
		return "monitoring PR..."
	default:
		return "running..."
	}
}

// PhaseSummary extracts a one-line summary from a phase's structured output.
// Returns an empty string if the result cannot be parsed or has no useful info.
func PhaseSummary(phase string, result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}

	switch phase {
	case "triage":
		return triageSummary(result)
	case "plan":
		return planSummary(result)
	case "implement":
		return implementSummary(result)
	case "verify":
		return verifySummary(result)
	case "submit":
		return submitSummary(result)
	case "monitor":
		return monitorSummary(result)
	default:
		return ""
	}
}

func triageSummary(data json.RawMessage) string {
	var result struct {
		Complexity  string `json:"complexity"`
		Automatable bool   `json:"automatable"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return ""
	}
	parts := []string{}
	if result.Complexity != "" {
		parts = append(parts, result.Complexity)
	}
	if !result.Automatable {
		parts = append(parts, "not automatable")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

func planSummary(data json.RawMessage) string {
	var result struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return ""
	}
	count := len(result.Tasks)
	if count == 0 {
		return "no tasks"
	}
	if count == 1 {
		return "1 task"
	}
	return fmt.Sprintf("%d tasks", count)
}

func implementSummary(data json.RawMessage) string {
	var result struct {
		FilesChanged []json.RawMessage `json:"files_changed"`
		Commits      []json.RawMessage `json:"commits"`
		TestsPassed  bool              `json:"tests_passed"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return ""
	}
	parts := []string{}
	fileCount := len(result.FilesChanged)
	commitCount := len(result.Commits)
	if fileCount > 0 {
		if fileCount == 1 {
			parts = append(parts, "1 file changed")
		} else {
			parts = append(parts, fmt.Sprintf("%d files changed", fileCount))
		}
	}
	if commitCount > 0 {
		if commitCount == 1 {
			parts = append(parts, "1 commit")
		} else {
			parts = append(parts, fmt.Sprintf("%d commits", commitCount))
		}
	}
	if len(parts) == 0 {
		if result.TestsPassed {
			return "tests passed"
		}
		return ""
	}
	return strings.Join(parts, ", ")
}

func verifySummary(data json.RawMessage) string {
	var result struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return ""
	}
	if result.Verdict == "" {
		return ""
	}
	return strings.ToUpper(result.Verdict)
}

func submitSummary(data json.RawMessage) string {
	var result struct {
		PRURL string `json:"pr_url"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return ""
	}
	if result.PRURL == "" {
		return ""
	}
	// Extract PR number from URL if possible (e.g., ".../pull/49" → "PR #49")
	parts := strings.Split(result.PRURL, "/")
	if len(parts) >= 2 && parts[len(parts)-2] == "pull" {
		return "PR #" + parts[len(parts)-1]
	}
	return result.PRURL
}

func monitorSummary(data json.RawMessage) string {
	var result struct {
		CommentsHandled []json.RawMessage `json:"comments_handled"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return ""
	}
	count := len(result.CommentsHandled)
	if count == 0 {
		return "no comments handled"
	}
	if count == 1 {
		return "1 comment handled"
	}
	return fmt.Sprintf("%d comments handled", count)
}
