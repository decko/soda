package pipeline

import (
	"testing"
)

// staticAuthority is a test double for AuthorityResolver.
type staticAuthority struct {
	authoritative map[string]bool // "author:path" → true/false
}

func (s *staticAuthority) IsAuthoritative(author, filePath string) bool {
	key := author + ":" + filePath
	if v, ok := s.authoritative[key]; ok {
		return v
	}
	// Default: not authoritative.
	return false
}

func TestCommentClassifier_SelfFilter(t *testing.T) {
	c := NewCommentClassifier("soda-bot", nil, nil)

	result := c.Classify(PRComment{
		ID:     "IC_1",
		Author: "soda-bot",
		Body:   "I pushed a fix for this.",
	})

	if result.Type != CommentSelfAuthored {
		t.Errorf("Type = %q, want %q", result.Type, CommentSelfAuthored)
	}
	if result.Action != ActionSkip {
		t.Errorf("Action = %q, want %q", result.Action, ActionSkip)
	}
	if result.Actionable {
		t.Error("self-authored comments should not be actionable")
	}
}

func TestCommentClassifier_SelfFilterCaseInsensitive(t *testing.T) {
	c := NewCommentClassifier("Soda-Bot", nil, nil)

	result := c.Classify(PRComment{
		ID:     "IC_1",
		Author: "soda-bot",
		Body:   "Updated the code.",
	})

	if result.Type != CommentSelfAuthored {
		t.Errorf("Type = %q, want %q", result.Type, CommentSelfAuthored)
	}
}

func TestCommentClassifier_BotFilter(t *testing.T) {
	tests := []struct {
		name   string
		author string
		body   string
	}{
		{
			name:   "known_bot_user",
			author: "dependabot",
			body:   "Bumps lodash from 4.17.20 to 4.17.21",
		},
		{
			name:   "bot_body_marker",
			author: "human-user",
			body:   "<!-- bot: codecov --> Coverage report",
		},
		{
			name:   "bot_indicator_in_body",
			author: "ci-user",
			body:   "This is an automated message from the CI system.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCommentClassifier("soda-bot", []string{"dependabot", "renovate"}, nil)

			result := c.Classify(PRComment{
				ID:     "IC_1",
				Author: tt.author,
				Body:   tt.body,
			})

			if result.Type != CommentBotGenerated {
				t.Errorf("Type = %q, want %q", result.Type, CommentBotGenerated)
			}
			if result.Action != ActionSkip {
				t.Errorf("Action = %q, want %q", result.Action, ActionSkip)
			}
			if result.Actionable {
				t.Error("bot comments should not be actionable")
			}
		})
	}
}

func TestCommentClassifier_NonAuthoritative(t *testing.T) {
	auth := &staticAuthority{
		authoritative: map[string]bool{
			"owner:main.go": true,
		},
	}
	c := NewCommentClassifier("soda-bot", nil, auth)

	result := c.Classify(PRComment{
		ID:     "IC_1",
		Author: "random-user",
		Body:   "Please fix this bug.",
		Path:   "main.go",
	})

	if result.Action != ActionAcknowledge {
		t.Errorf("Action = %q, want %q", result.Action, ActionAcknowledge)
	}
	if result.Actionable {
		t.Error("non-authoritative comments should not be actionable")
	}
}

func TestCommentClassifier_AuthoritativeCodeChange(t *testing.T) {
	auth := &staticAuthority{
		authoritative: map[string]bool{
			"owner:main.go": true,
		},
	}
	c := NewCommentClassifier("soda-bot", nil, auth)

	result := c.Classify(PRComment{
		ID:     "IC_1",
		Author: "owner",
		Body:   "Please rename this variable to be more descriptive.",
		Path:   "main.go",
	})

	if result.Type != CommentCodeChange {
		t.Errorf("Type = %q, want %q", result.Type, CommentCodeChange)
	}
	if result.Action != ActionApplyFix {
		t.Errorf("Action = %q, want %q", result.Action, ActionApplyFix)
	}
	if !result.Actionable {
		t.Error("authoritative code change should be actionable")
	}
}

func TestCommentClassifier_ContentClassification(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantType   CommentType
		wantAction CommentAction
		actionable bool
	}{
		{
			name:       "approval_lgtm",
			body:       "LGTM",
			wantType:   CommentApproval,
			wantAction: ActionAcknowledge,
			actionable: false,
		},
		{
			name:       "approval_looks_good",
			body:       "Looks good to me!",
			wantType:   CommentApproval,
			wantAction: ActionAcknowledge,
			actionable: false,
		},
		{
			name:       "nit_prefix",
			body:       "nit: use camelCase here",
			wantType:   CommentNit,
			wantAction: ActionApplyFix,
			actionable: true,
		},
		{
			name:       "nit_style",
			body:       "style: prefer constants over magic numbers",
			wantType:   CommentNit,
			wantAction: ActionApplyFix,
			actionable: true,
		},
		{
			name:       "question_mark",
			body:       "Why did you choose this approach?",
			wantType:   CommentQuestion,
			wantAction: ActionRespond,
			actionable: true,
		},
		{
			name:       "question_prefix",
			body:       "Could you explain the reasoning here?",
			wantType:   CommentQuestion,
			wantAction: ActionRespond,
			actionable: true,
		},
		{
			name:       "dismissal",
			body:       "never mind",
			wantType:   CommentDismissal,
			wantAction: ActionSkip,
			actionable: false,
		},
		{
			name:       "code_change",
			body:       "Please add error handling for this edge case.",
			wantType:   CommentCodeChange,
			wantAction: ActionApplyFix,
			actionable: true,
		},
		{
			name:       "code_change_imperative",
			body:       "Move this logic to a separate function.",
			wantType:   CommentCodeChange,
			wantAction: ActionApplyFix,
			actionable: true,
		},
	}

	// Use nil authority → all authors are authoritative.
	c := NewCommentClassifier("soda-bot", nil, nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.Classify(PRComment{
				ID:     "IC_1",
				Author: "reviewer",
				Body:   tt.body,
			})

			if result.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", result.Type, tt.wantType)
			}
			if result.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", result.Action, tt.wantAction)
			}
			if result.Actionable != tt.actionable {
				t.Errorf("Actionable = %v, want %v", result.Actionable, tt.actionable)
			}
		})
	}
}

func TestCommentClassifier_ClassifyAll(t *testing.T) {
	c := NewCommentClassifier("soda-bot", []string{"ci-bot"}, nil)

	comments := []PRComment{
		{ID: "IC_1", Author: "soda-bot", Body: "Updated code."},
		{ID: "IC_2", Author: "ci-bot", Body: "CI passed."},
		{ID: "IC_3", Author: "reviewer", Body: "Please fix this."},
		{ID: "IC_4", Author: "reviewer", Body: "LGTM"},
	}

	results := c.ClassifyAll(comments)

	if len(results) != 4 {
		t.Fatalf("got %d results, want 4", len(results))
	}

	// Self-authored → skip.
	if results[0].Type != CommentSelfAuthored {
		t.Errorf("comment 0: Type = %q, want %q", results[0].Type, CommentSelfAuthored)
	}
	// Bot → skip.
	if results[1].Type != CommentBotGenerated {
		t.Errorf("comment 1: Type = %q, want %q", results[1].Type, CommentBotGenerated)
	}
	// Code change → actionable.
	if !results[2].Actionable {
		t.Error("comment 2: should be actionable")
	}
	// Approval → not actionable.
	if results[3].Actionable {
		t.Error("comment 3: approval should not be actionable")
	}
}

func TestHasActionable(t *testing.T) {
	tests := []struct {
		name       string
		classified []ClassifiedComment
		want       bool
	}{
		{
			name: "has_actionable",
			classified: []ClassifiedComment{
				{Actionable: false},
				{Actionable: true},
			},
			want: true,
		},
		{
			name: "no_actionable",
			classified: []ClassifiedComment{
				{Actionable: false},
				{Actionable: false},
			},
			want: false,
		},
		{
			name:       "empty",
			classified: nil,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasActionable(tt.classified)
			if got != tt.want {
				t.Errorf("HasActionable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCommentClassifier_NilAuthority(t *testing.T) {
	// nil authority → all authors treated as authoritative (backward-compatible).
	c := NewCommentClassifier("soda-bot", nil, nil)

	result := c.Classify(PRComment{
		ID:     "IC_1",
		Author: "anyone",
		Body:   "Fix this bug.",
		Path:   "main.go",
	})

	if result.Action == ActionAcknowledge {
		t.Error("nil authority should not result in acknowledge action")
	}
	if !result.Actionable {
		t.Error("nil authority: code change should be actionable")
	}
}

func TestIsBotComment(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{"<!-- bot: codecov --> Coverage", true},
		{"<!-- generated by some tool -->", true},
		{"[bot] automated check", true},
		{"This is an automated message.", true},
		{"codecov/project: 85%", true},
		{"sonarcloud quality gate passed", true},
		{"I think we should refactor this.", false},
		{"Please fix the bug.", false},
	}

	for _, tt := range tests {
		got := isBotComment(tt.body)
		if got != tt.want {
			t.Errorf("isBotComment(%q) = %v, want %v", tt.body, got, tt.want)
		}
	}
}
