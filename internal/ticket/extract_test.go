package ticket

import "testing"

func TestCommentMarkerExtractor_HappyPath(t *testing.T) {
	extractor := &CommentMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
		Plan: MarkerPair{
			StartMarker: "<!-- plan:start -->",
			EndMarker:   "<!-- plan:end -->",
		},
	}

	ticket := &Ticket{
		Key:     "42",
		Summary: "Test ticket",
		Comments: []Comment{
			{
				Author: "user1",
				Body:   "<!-- spec:start -->\nThis is the spec content.\n<!-- spec:end -->",
			},
			{
				Author: "user2",
				Body:   "<!-- plan:start -->\nThis is the plan content.\n<!-- plan:end -->",
			},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "This is the spec content." {
		t.Errorf("ExistingSpec = %q, want %q", result.ExistingSpec, "This is the spec content.")
	}
	if result.ExistingPlan != "This is the plan content." {
		t.Errorf("ExistingPlan = %q, want %q", result.ExistingPlan, "This is the plan content.")
	}
}

func TestCommentMarkerExtractor_LastWins(t *testing.T) {
	extractor := &CommentMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
	}

	ticket := &Ticket{
		Key: "42",
		Comments: []Comment{
			{
				Author: "user1",
				Body:   "<!-- spec:start -->\nOld spec.\n<!-- spec:end -->",
			},
			{
				Author: "user1",
				Body:   "<!-- spec:start -->\nUpdated spec.\n<!-- spec:end -->",
			},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "Updated spec." {
		t.Errorf("ExistingSpec = %q, want %q (last comment should win)", result.ExistingSpec, "Updated spec.")
	}
}

func TestCommentMarkerExtractor_NoMarkers(t *testing.T) {
	extractor := &CommentMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
		Plan: MarkerPair{
			StartMarker: "<!-- plan:start -->",
			EndMarker:   "<!-- plan:end -->",
		},
	}

	ticket := &Ticket{
		Key: "42",
		Comments: []Comment{
			{
				Author: "user1",
				Body:   "Just a regular comment with no markers.",
			},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty", result.ExistingSpec)
	}
	if result.ExistingPlan != "" {
		t.Errorf("ExistingPlan = %q, want empty", result.ExistingPlan)
	}
}

func TestCommentMarkerExtractor_EmptyComments(t *testing.T) {
	extractor := &CommentMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
	}

	ticket := &Ticket{
		Key:      "42",
		Comments: nil,
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty", result.ExistingSpec)
	}
}

func TestCommentMarkerExtractor_EmptyMarkerConfig(t *testing.T) {
	// No markers configured — nothing should be extracted.
	extractor := &CommentMarkerExtractor{}

	ticket := &Ticket{
		Key: "42",
		Comments: []Comment{
			{
				Author: "user1",
				Body:   "<!-- spec:start -->\nSome content.\n<!-- spec:end -->",
			},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (no markers configured)", result.ExistingSpec)
	}
}

func TestCommentMarkerExtractor_NilTicket(t *testing.T) {
	extractor := &CommentMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
	}

	result := extractor.Extract(nil)
	if result != nil {
		t.Errorf("Extract(nil) = %v, want nil", result)
	}
}

func TestCommentMarkerExtractor_MissingEndMarker(t *testing.T) {
	extractor := &CommentMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
	}

	ticket := &Ticket{
		Key: "42",
		Comments: []Comment{
			{
				Author: "user1",
				Body:   "<!-- spec:start -->\nContent without end marker.",
			},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (missing end marker)", result.ExistingSpec)
	}
}

func TestCommentMarkerExtractor_BothArtifactsInSameComment(t *testing.T) {
	extractor := &CommentMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
		Plan: MarkerPair{
			StartMarker: "<!-- plan:start -->",
			EndMarker:   "<!-- plan:end -->",
		},
	}

	ticket := &Ticket{
		Key: "42",
		Comments: []Comment{
			{
				Author: "user1",
				Body: "<!-- spec:start -->\nThe spec.\n<!-- spec:end -->\n\n" +
					"<!-- plan:start -->\nThe plan.\n<!-- plan:end -->",
			},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "The spec." {
		t.Errorf("ExistingSpec = %q, want %q", result.ExistingSpec, "The spec.")
	}
	if result.ExistingPlan != "The plan." {
		t.Errorf("ExistingPlan = %q, want %q", result.ExistingPlan, "The plan.")
	}
}

func TestCommentMarkerExtractor_MultilineContent(t *testing.T) {
	extractor := &CommentMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
	}

	ticket := &Ticket{
		Key: "42",
		Comments: []Comment{
			{
				Author: "user1",
				Body:   "<!-- spec:start -->\nLine 1\nLine 2\nLine 3\n<!-- spec:end -->",
			},
		},
	}

	result := extractor.Extract(ticket)

	want := "Line 1\nLine 2\nLine 3"
	if result.ExistingSpec != want {
		t.Errorf("ExistingSpec = %q, want %q", result.ExistingSpec, want)
	}
}

func TestDescriptionMarkerExtractor_HappyPath(t *testing.T) {
	extractor := &DescriptionMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
		Plan: MarkerPair{
			StartMarker: "<!-- plan:start -->",
			EndMarker:   "<!-- plan:end -->",
		},
	}

	ticket := &Ticket{
		Key: "EPIC-1",
		Description: "Epic overview.\n\n" +
			"<!-- spec:start -->\nThe spec from the epic.\n<!-- spec:end -->\n\n" +
			"<!-- plan:start -->\nThe plan from the epic.\n<!-- plan:end -->",
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "The spec from the epic." {
		t.Errorf("ExistingSpec = %q, want %q", result.ExistingSpec, "The spec from the epic.")
	}
	if result.ExistingPlan != "The plan from the epic." {
		t.Errorf("ExistingPlan = %q, want %q", result.ExistingPlan, "The plan from the epic.")
	}
}

func TestDescriptionMarkerExtractor_DoesNotOverwrite(t *testing.T) {
	extractor := &DescriptionMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
	}

	ticket := &Ticket{
		Key:          "EPIC-2",
		Description:  "<!-- spec:start -->\nNew spec.\n<!-- spec:end -->",
		ExistingSpec: "Already set spec.",
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "Already set spec." {
		t.Errorf("ExistingSpec = %q, want %q (should not overwrite)", result.ExistingSpec, "Already set spec.")
	}
}

func TestDescriptionMarkerExtractor_NoMarkers(t *testing.T) {
	extractor := &DescriptionMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
	}

	ticket := &Ticket{
		Key:         "EPIC-3",
		Description: "Just a plain description with no markers.",
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty", result.ExistingSpec)
	}
}

func TestDescriptionMarkerExtractor_NilTicket(t *testing.T) {
	extractor := &DescriptionMarkerExtractor{
		Spec: MarkerPair{
			StartMarker: "<!-- spec:start -->",
			EndMarker:   "<!-- spec:end -->",
		},
	}

	result := extractor.Extract(nil)
	if result != nil {
		t.Errorf("Extract(nil) = %v, want nil", result)
	}
}

func TestDescriptionMarkerExtractor_EmptyMarkerConfig(t *testing.T) {
	extractor := &DescriptionMarkerExtractor{}

	ticket := &Ticket{
		Key:         "EPIC-4",
		Description: "<!-- spec:start -->\nSome content.\n<!-- spec:end -->",
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (no markers configured)", result.ExistingSpec)
	}
}

func TestFieldExtractor_HappyPath(t *testing.T) {
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
		PlanField: "customfield_10051",
	}

	ticket := &Ticket{
		Key: "PROJ-42",
		RawFields: map[string]any{
			"customfield_10050": "The custom spec content.",
			"customfield_10051": "The custom plan content.",
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "The custom spec content." {
		t.Errorf("ExistingSpec = %q, want %q", result.ExistingSpec, "The custom spec content.")
	}
	if result.ExistingPlan != "The custom plan content." {
		t.Errorf("ExistingPlan = %q, want %q", result.ExistingPlan, "The custom plan content.")
	}
}

func TestFieldExtractor_DoesNotOverwrite(t *testing.T) {
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
	}

	ticket := &Ticket{
		Key:          "PROJ-43",
		ExistingSpec: "Already set spec.",
		RawFields: map[string]any{
			"customfield_10050": "New spec from field.",
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "Already set spec." {
		t.Errorf("ExistingSpec = %q, want %q (should not overwrite)", result.ExistingSpec, "Already set spec.")
	}
}

func TestFieldExtractor_MissingField(t *testing.T) {
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
		PlanField: "customfield_10051",
	}

	ticket := &Ticket{
		Key: "PROJ-44",
		RawFields: map[string]any{
			"summary": "some issue",
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (field not present)", result.ExistingSpec)
	}
	if result.ExistingPlan != "" {
		t.Errorf("ExistingPlan = %q, want empty (field not present)", result.ExistingPlan)
	}
}

func TestFieldExtractor_NilValue(t *testing.T) {
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
	}

	ticket := &Ticket{
		Key: "PROJ-45",
		RawFields: map[string]any{
			"customfield_10050": nil,
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (nil value)", result.ExistingSpec)
	}
}

func TestFieldExtractor_NonStringValue(t *testing.T) {
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
	}

	ticket := &Ticket{
		Key: "PROJ-46",
		RawFields: map[string]any{
			"customfield_10050": 42,
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "42" {
		t.Errorf("ExistingSpec = %q, want %q", result.ExistingSpec, "42")
	}
}

func TestFieldExtractor_WhitespaceOnlyValue(t *testing.T) {
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
	}

	ticket := &Ticket{
		Key: "PROJ-47",
		RawFields: map[string]any{
			"customfield_10050": "   \n  ",
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (whitespace-only)", result.ExistingSpec)
	}
}

func TestFieldExtractor_NilTicket(t *testing.T) {
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
	}

	result := extractor.Extract(nil)
	if result != nil {
		t.Errorf("Extract(nil) = %v, want nil", result)
	}
}

func TestFieldExtractor_NilRawFields(t *testing.T) {
	extractor := &FieldExtractor{
		SpecField: "customfield_10050",
	}

	ticket := &Ticket{
		Key:       "PROJ-48",
		RawFields: nil,
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (nil RawFields)", result.ExistingSpec)
	}
}

func TestFieldExtractor_EmptyFieldConfig(t *testing.T) {
	extractor := &FieldExtractor{}

	ticket := &Ticket{
		Key: "PROJ-49",
		RawFields: map[string]any{
			"customfield_10050": "Some content.",
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (no fields configured)", result.ExistingSpec)
	}
	if result.ExistingPlan != "" {
		t.Errorf("ExistingPlan = %q, want empty (no fields configured)", result.ExistingPlan)
	}
}

func TestSubtaskExtractor_HappyPath(t *testing.T) {
	extractor := &SubtaskExtractor{}

	ticket := &Ticket{
		Key: "EPIC-10",
		RawFields: map[string]any{
			"subtasks": []any{
				map[string]any{
					"key": "PROJ-11",
					"fields": map[string]any{
						"summary": "Design authentication flow",
						"status":  map[string]any{"name": "Done"},
					},
				},
				map[string]any{
					"key": "PROJ-12",
					"fields": map[string]any{
						"summary": "Implement login endpoint",
						"status":  map[string]any{"name": "In Progress"},
					},
				},
			},
		},
	}

	result := extractor.Extract(ticket)

	want := "- [PROJ-11] Design authentication flow (Done)\n- [PROJ-12] Implement login endpoint (In Progress)"
	if result.ExistingPlan != want {
		t.Errorf("ExistingPlan = %q, want %q", result.ExistingPlan, want)
	}
}

func TestSubtaskExtractor_DoesNotOverwrite(t *testing.T) {
	extractor := &SubtaskExtractor{}

	ticket := &Ticket{
		Key:          "EPIC-11",
		ExistingPlan: "Already have a plan.",
		RawFields: map[string]any{
			"subtasks": []any{
				map[string]any{
					"key": "PROJ-13",
					"fields": map[string]any{
						"summary": "Some subtask",
					},
				},
			},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingPlan != "Already have a plan." {
		t.Errorf("ExistingPlan = %q, want %q (should not overwrite)", result.ExistingPlan, "Already have a plan.")
	}
}

func TestSubtaskExtractor_CustomField(t *testing.T) {
	extractor := &SubtaskExtractor{Field: "sub_tasks"}

	ticket := &Ticket{
		Key: "EPIC-12",
		RawFields: map[string]any{
			"sub_tasks": []any{
				map[string]any{
					"key": "PROJ-14",
					"fields": map[string]any{
						"summary": "Task from custom field",
					},
				},
			},
		},
	}

	result := extractor.Extract(ticket)

	want := "- [PROJ-14] Task from custom field"
	if result.ExistingPlan != want {
		t.Errorf("ExistingPlan = %q, want %q", result.ExistingPlan, want)
	}
}

func TestSubtaskExtractor_NoSubtasks(t *testing.T) {
	extractor := &SubtaskExtractor{}

	ticket := &Ticket{
		Key: "EPIC-13",
		RawFields: map[string]any{
			"summary": "An epic with no subtasks",
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingPlan != "" {
		t.Errorf("ExistingPlan = %q, want empty", result.ExistingPlan)
	}
}

func TestSubtaskExtractor_EmptySubtaskList(t *testing.T) {
	extractor := &SubtaskExtractor{}

	ticket := &Ticket{
		Key: "EPIC-14",
		RawFields: map[string]any{
			"subtasks": []any{},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingPlan != "" {
		t.Errorf("ExistingPlan = %q, want empty", result.ExistingPlan)
	}
}

func TestSubtaskExtractor_NilTicket(t *testing.T) {
	extractor := &SubtaskExtractor{}

	result := extractor.Extract(nil)
	if result != nil {
		t.Errorf("Extract(nil) = %v, want nil", result)
	}
}

func TestSubtaskExtractor_NilRawFields(t *testing.T) {
	extractor := &SubtaskExtractor{}

	ticket := &Ticket{
		Key:       "EPIC-15",
		RawFields: nil,
	}

	result := extractor.Extract(ticket)

	if result.ExistingPlan != "" {
		t.Errorf("ExistingPlan = %q, want empty", result.ExistingPlan)
	}
}

func TestSubtaskExtractor_SkipsMalformedEntries(t *testing.T) {
	extractor := &SubtaskExtractor{}

	ticket := &Ticket{
		Key: "EPIC-16",
		RawFields: map[string]any{
			"subtasks": []any{
				"not a map",
				map[string]any{
					"key": "PROJ-15",
					"fields": map[string]any{
						"summary": "Valid subtask",
					},
				},
				map[string]any{
					// no summary at all
					"key": "PROJ-16",
				},
			},
		},
	}

	result := extractor.Extract(ticket)

	want := "- [PROJ-15] Valid subtask"
	if result.ExistingPlan != want {
		t.Errorf("ExistingPlan = %q, want %q", result.ExistingPlan, want)
	}
}

func TestSubtaskExtractor_FlatStructure(t *testing.T) {
	extractor := &SubtaskExtractor{}

	ticket := &Ticket{
		Key: "EPIC-17",
		RawFields: map[string]any{
			"subtasks": []any{
				map[string]any{
					"key":     "PROJ-17",
					"summary": "Flat summary",
					"status":  "Open",
				},
			},
		},
	}

	result := extractor.Extract(ticket)

	want := "- [PROJ-17] Flat summary (Open)"
	if result.ExistingPlan != want {
		t.Errorf("ExistingPlan = %q, want %q", result.ExistingPlan, want)
	}
}

func TestSubtaskExtractor_DoesNotModifySpec(t *testing.T) {
	extractor := &SubtaskExtractor{}

	ticket := &Ticket{
		Key: "EPIC-18",
		RawFields: map[string]any{
			"subtasks": []any{
				map[string]any{
					"key": "PROJ-18",
					"fields": map[string]any{
						"summary": "Subtask",
					},
				},
			},
		},
	}

	result := extractor.Extract(ticket)

	if result.ExistingSpec != "" {
		t.Errorf("ExistingSpec = %q, want empty (SubtaskExtractor should not modify spec)", result.ExistingSpec)
	}
}

// Verify CommentMarkerExtractor satisfies ArtifactExtractor at compile time.
var _ ArtifactExtractor = (*CommentMarkerExtractor)(nil)

// Verify DescriptionMarkerExtractor satisfies ArtifactExtractor at compile time.
var _ ArtifactExtractor = (*DescriptionMarkerExtractor)(nil)

// Verify FieldExtractor satisfies ArtifactExtractor at compile time.
var _ ArtifactExtractor = (*FieldExtractor)(nil)

// Verify SubtaskExtractor satisfies ArtifactExtractor at compile time.
var _ ArtifactExtractor = (*SubtaskExtractor)(nil)
