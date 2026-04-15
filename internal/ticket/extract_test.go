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

// Verify CommentMarkerExtractor satisfies ArtifactExtractor at compile time.
var _ ArtifactExtractor = (*CommentMarkerExtractor)(nil)

// Verify DescriptionMarkerExtractor satisfies ArtifactExtractor at compile time.
var _ ArtifactExtractor = (*DescriptionMarkerExtractor)(nil)
