package pipeline

import (
	"fmt"
	"strings"
)

// CommentType classifies a review comment's intent.
type CommentType string

const (
	CommentCodeChange   CommentType = "code_change"   // Requests a code modification
	CommentQuestion     CommentType = "question"      // Asks for clarification
	CommentNit          CommentType = "nit"           // Minor stylistic suggestion
	CommentApproval     CommentType = "approval"      // Positive/LGTM feedback
	CommentDismissal    CommentType = "dismissal"     // Dismissive or unrelated
	CommentBotGenerated CommentType = "bot_generated" // Automated (CI, bots)
	CommentSelfAuthored CommentType = "self_authored" // Comment from the PR author
)

// CommentAction indicates what the monitor should do in response.
type CommentAction string

const (
	ActionApplyFix    CommentAction = "apply_fix"   // Make a code change
	ActionRespond     CommentAction = "respond"     // Post a reply
	ActionAcknowledge CommentAction = "acknowledge" // Note but take no action
	ActionSkip        CommentAction = "skip"        // Ignore entirely
)

// ClassifiedComment holds the result of classifying a single PRComment.
type ClassifiedComment struct {
	Comment          PRComment
	Type             CommentType
	Action           CommentAction
	Actionable       bool   // true if this comment should count toward response rounds
	NonAuthoritative bool   // true if the comment author lacks authority on the file
	Reason           string // human-readable explanation of classification
}

// CommentClassifier classifies PR comments and determines actions.
type CommentClassifier struct {
	selfUser  string            // the bot/PR author's username
	botUsers  map[string]bool   // known bot usernames (lowercased)
	authority AuthorityResolver // nil means all authors are authoritative
}

// NewCommentClassifier creates a classifier. selfUser is the username
// of the PR author (i.e., the bot) and must be non-empty. botUsers is
// a list of known bot usernames to filter. authority may be nil for
// backward compatibility.
func NewCommentClassifier(selfUser string, botUsers []string, authority AuthorityResolver) (*CommentClassifier, error) {
	if strings.TrimSpace(selfUser) == "" {
		return nil, fmt.Errorf("pipeline: NewCommentClassifier requires non-empty selfUser")
	}
	bots := make(map[string]bool, len(botUsers))
	for _, u := range botUsers {
		bots[strings.ToLower(u)] = true
	}
	return &CommentClassifier{
		selfUser:  strings.ToLower(selfUser),
		botUsers:  bots,
		authority: authority,
	}, nil
}

// Classify processes a single comment and returns its classification.
func (c *CommentClassifier) Classify(comment PRComment) ClassifiedComment {
	author := strings.ToLower(comment.Author)

	// 1. Self-authored filter: comments from the PR author itself.
	if author == c.selfUser && c.selfUser != "" {
		return ClassifiedComment{
			Comment:    comment,
			Type:       CommentSelfAuthored,
			Action:     ActionSkip,
			Actionable: false,
			Reason:     "comment from PR author (self)",
		}
	}

	// 2. Bot filter: known bot accounts.
	if c.botUsers[author] || isBotComment(comment.Body) {
		return ClassifiedComment{
			Comment:    comment,
			Type:       CommentBotGenerated,
			Action:     ActionSkip,
			Actionable: false,
			Reason:     "bot-generated comment",
		}
	}

	// 3. Authority check: non-authoritative commenters get acknowledged.
	if c.authority != nil && !c.authority.IsAuthoritative(comment.Author, comment.Path) {
		return ClassifiedComment{
			Comment:          comment,
			Type:             classifyCommentBody(comment.Body),
			Action:           ActionAcknowledge,
			Actionable:       false,
			NonAuthoritative: true,
			Reason:           "comment from non-authoritative user",
		}
	}

	// 4. Classify by content.
	commentType := classifyCommentBody(comment.Body)

	switch commentType {
	case CommentApproval:
		return ClassifiedComment{
			Comment:    comment,
			Type:       commentType,
			Action:     ActionAcknowledge,
			Actionable: false,
			Reason:     "approval/positive feedback",
		}
	case CommentDismissal:
		return ClassifiedComment{
			Comment:    comment,
			Type:       commentType,
			Action:     ActionSkip,
			Actionable: false,
			Reason:     "dismissive or unrelated comment",
		}
	case CommentNit:
		return ClassifiedComment{
			Comment:    comment,
			Type:       commentType,
			Action:     ActionApplyFix,
			Actionable: true,
			Reason:     "minor style suggestion (nit)",
		}
	case CommentQuestion:
		return ClassifiedComment{
			Comment:    comment,
			Type:       commentType,
			Action:     ActionRespond,
			Actionable: true,
			Reason:     "question requiring response",
		}
	case CommentCodeChange:
		return ClassifiedComment{
			Comment:    comment,
			Type:       commentType,
			Action:     ActionApplyFix,
			Actionable: true,
			Reason:     "code change requested",
		}
	default:
		// Treat unclassified comments as code changes (safe default).
		return ClassifiedComment{
			Comment:    comment,
			Type:       CommentCodeChange,
			Action:     ActionApplyFix,
			Actionable: true,
			Reason:     "unclassified comment treated as code change request",
		}
	}
}

// ClassifyAll processes multiple comments and returns classified results.
func (c *CommentClassifier) ClassifyAll(comments []PRComment) []ClassifiedComment {
	results := make([]ClassifiedComment, 0, len(comments))
	for _, comment := range comments {
		results = append(results, c.Classify(comment))
	}
	return results
}

// HasActionable returns true if any classified comment is actionable.
func HasActionable(classified []ClassifiedComment) bool {
	for _, c := range classified {
		if c.Actionable {
			return true
		}
	}
	return false
}

// classifyCommentBody determines the comment type from its text content.
func classifyCommentBody(body string) CommentType {
	lower := strings.ToLower(strings.TrimSpace(body))

	// Approval indicators.
	if isApproval(lower) {
		return CommentApproval
	}

	// Nit indicators.
	if isNit(lower) {
		return CommentNit
	}

	// Question indicators.
	if isQuestion(lower) {
		return CommentQuestion
	}

	// Dismissal indicators.
	if isDismissal(lower) {
		return CommentDismissal
	}

	// Default: code change request.
	return CommentCodeChange
}

// isApproval detects approval/positive feedback patterns.
func isApproval(lower string) bool {
	approvals := []string{
		"lgtm",
		"looks good to me",
		"looks good",
		"ship it",
		":+1:",
		"👍",
		"approved",
		"nice work",
		"great job",
	}
	for _, pattern := range approvals {
		if lower == pattern || strings.HasPrefix(lower, pattern+"!") || strings.HasPrefix(lower, pattern+".") {
			return true
		}
	}
	return false
}

// isNit detects minor stylistic suggestions.
func isNit(lower string) bool {
	nitPrefixes := []string{
		"nit:",
		"nit ",
		"nitpick:",
		"minor:",
		"style:",
		"optional:",
		"suggestion:",
	}
	for _, prefix := range nitPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// isQuestion detects questions requiring a response.
func isQuestion(lower string) bool {
	if strings.HasSuffix(lower, "?") {
		return true
	}
	questionPrefixes := []string{
		"why ",
		"what ",
		"how ",
		"could you explain",
		"can you explain",
		"i'm confused",
		"i don't understand",
		"question:",
	}
	for _, prefix := range questionPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// isDismissal detects dismissive or unrelated comments.
func isDismissal(lower string) bool {
	dismissals := []string{
		"n/a",
		"not applicable",
		"never mind",
		"nevermind",
		"ignore this",
		"disregard",
	}
	for _, pattern := range dismissals {
		if lower == pattern || strings.HasPrefix(lower, pattern+".") {
			return true
		}
	}
	return false
}

// isBotComment detects bot-generated content by common patterns.
func isBotComment(body string) bool {
	lower := strings.ToLower(body)
	botIndicators := []string{
		"<!-- bot:",
		"<!-- generated by",
		"[bot]",
		"automated message",
		"this is an automated",
		"codecov/",
		"sonarcloud",
		"dependabot",
	}
	for _, indicator := range botIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}
