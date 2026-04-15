package ticket

import (
	"fmt"
	"strings"
)

// ArtifactExtractor extracts named artifacts from a ticket's comments.
// Implementations scan comment bodies for delimited content and populate
// the corresponding Ticket fields (e.g. ExistingSpec, ExistingPlan).
type ArtifactExtractor interface {
	// Extract scans the ticket's comments and populates artifact fields
	// (ExistingSpec, ExistingPlan) in place. It returns the modified ticket
	// for convenience.
	Extract(t *Ticket) *Ticket
}

// MarkerPair defines start/end comment markers for a single artifact.
type MarkerPair struct {
	StartMarker string
	EndMarker   string
}

// CommentMarkerExtractor scans ticket comments for content delimited by
// configurable marker pairs. When multiple comments contain the same
// markers, the last one wins (most recent update takes precedence).
type CommentMarkerExtractor struct {
	Spec MarkerPair
	Plan MarkerPair
}

// Extract scans comment bodies for marker-delimited content and populates
// ExistingSpec and ExistingPlan on the ticket. If no markers are configured
// or no matching content is found, the fields are left empty.
func (e *CommentMarkerExtractor) Extract(t *Ticket) *Ticket {
	if t == nil {
		return t
	}

	for _, comment := range t.Comments {
		if content := extractBetweenMarkers(comment.Body, e.Spec.StartMarker, e.Spec.EndMarker); content != "" {
			t.ExistingSpec = content
		}
		if content := extractBetweenMarkers(comment.Body, e.Plan.StartMarker, e.Plan.EndMarker); content != "" {
			t.ExistingPlan = content
		}
	}

	return t
}

// DescriptionMarkerExtractor scans the ticket description (not comments) for
// content delimited by configurable marker pairs. This is useful for Jira epics
// whose description embeds spec or plan content between markers.
type DescriptionMarkerExtractor struct {
	Spec MarkerPair
	Plan MarkerPair
}

// Extract scans the ticket description for marker-delimited content and
// populates ExistingSpec and ExistingPlan. Existing non-empty values are
// not overwritten.
func (e *DescriptionMarkerExtractor) Extract(t *Ticket) *Ticket {
	if t == nil {
		return t
	}

	if content := extractBetweenMarkers(t.Description, e.Spec.StartMarker, e.Spec.EndMarker); content != "" && t.ExistingSpec == "" {
		t.ExistingSpec = content
	}
	if content := extractBetweenMarkers(t.Description, e.Plan.StartMarker, e.Plan.EndMarker); content != "" && t.ExistingPlan == "" {
		t.ExistingPlan = content
	}

	return t
}

// FieldExtractor reads spec and/or plan content from named fields in the
// ticket's RawFields map. This supports Jira custom fields (e.g.
// "customfield_10050") that hold spec or plan text directly.
// Field values are converted to string via fmt.Sprint; structured values
// (maps, slices) are stringified but may not be useful without further
// processing.
type FieldExtractor struct {
	SpecField string // RawFields key for spec content (e.g. "customfield_10050")
	PlanField string // RawFields key for plan content (e.g. "customfield_10051")
}

// Extract reads the configured fields from RawFields and populates
// ExistingSpec and ExistingPlan. Existing non-empty values are not
// overwritten. Fields that are missing or empty are silently skipped.
func (e *FieldExtractor) Extract(t *Ticket) *Ticket {
	if t == nil || t.RawFields == nil {
		return t
	}

	if e.SpecField != "" && t.ExistingSpec == "" {
		if val := fieldToString(t.RawFields[e.SpecField]); val != "" {
			t.ExistingSpec = val
		}
	}
	if e.PlanField != "" && t.ExistingPlan == "" {
		if val := fieldToString(t.RawFields[e.PlanField]); val != "" {
			t.ExistingPlan = val
		}
	}

	return t
}

// fieldToString converts a raw field value to a trimmed string.
// Returns "" for nil values.
func fieldToString(val any) string {
	if val == nil {
		return ""
	}
	str, ok := val.(string)
	if !ok {
		str = fmt.Sprint(val)
	}
	return strings.TrimSpace(str)
}

// SubtaskExtractor reads subtask data from the ticket's RawFields and
// formats matching subtasks into an ExistingPlan. This supports Jira epics
// whose subtasks represent plan tasks. Each subtask becomes a bullet item
// in the plan, formatted as "- [KEY] Summary (status)".
//
// The extractor looks for subtask data at the configured field name in
// RawFields (default: "subtasks"). The field value must be a []any of
// map[string]any items with "key" and "fields.summary" entries.
type SubtaskExtractor struct {
	Field string // RawFields key containing subtasks (default: "subtasks")
}

// Extract reads subtask data from RawFields and populates ExistingPlan
// with a formatted list. ExistingPlan is not overwritten if already set.
// ExistingSpec is never modified by this extractor.
func (e *SubtaskExtractor) Extract(t *Ticket) *Ticket {
	if t == nil || t.RawFields == nil || t.ExistingPlan != "" {
		return t
	}

	field := e.Field
	if field == "" {
		field = "subtasks"
	}

	subtasksRaw, ok := t.RawFields[field]
	if !ok {
		return t
	}

	subtasks, ok := subtasksRaw.([]any)
	if !ok || len(subtasks) == 0 {
		return t
	}

	var lines []string
	for _, item := range subtasks {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		key, _ := itemMap["key"].(string)
		summary := subtaskSummary(itemMap)
		status := subtaskStatus(itemMap)

		if summary == "" {
			continue
		}

		line := "- "
		if key != "" {
			line += "[" + key + "] "
		}
		line += summary
		if status != "" {
			line += " (" + status + ")"
		}
		lines = append(lines, line)
	}

	if len(lines) > 0 {
		t.ExistingPlan = strings.Join(lines, "\n")
	}

	return t
}

// subtaskSummary extracts the summary from a subtask map.
// Handles both flat {"summary": "..."} and nested {"fields": {"summary": "..."}}.
func subtaskSummary(item map[string]any) string {
	// Try nested fields.summary first (Jira API format)
	if fields, ok := item["fields"].(map[string]any); ok {
		if summary, ok := fields["summary"].(string); ok {
			return summary
		}
	}
	// Fall back to flat summary
	if summary, ok := item["summary"].(string); ok {
		return summary
	}
	return ""
}

// subtaskStatus extracts the status name from a subtask map.
// Handles both flat and nested field structures.
func subtaskStatus(item map[string]any) string {
	if fields, ok := item["fields"].(map[string]any); ok {
		if status, ok := fields["status"].(map[string]any); ok {
			if name, ok := status["name"].(string); ok {
				return name
			}
		}
	}
	if status, ok := item["status"].(map[string]any); ok {
		if name, ok := status["name"].(string); ok {
			return name
		}
	}
	if status, ok := item["status"].(string); ok {
		return status
	}
	return ""
}

// extractBetweenMarkers returns the trimmed text between startMarker and
// endMarker within body. Returns "" if either marker is empty or not found.
func extractBetweenMarkers(body, startMarker, endMarker string) string {
	if startMarker == "" || endMarker == "" {
		return ""
	}

	startIdx := strings.Index(body, startMarker)
	if startIdx < 0 {
		return ""
	}
	contentStart := startIdx + len(startMarker)

	endIdx := strings.Index(body[contentStart:], endMarker)
	if endIdx < 0 {
		return ""
	}

	return strings.TrimSpace(body[contentStart : contentStart+endIdx])
}
