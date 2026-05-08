package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/decko/soda/internal/git"
)

// defaultMaxDiffBytes is the default byte limit for git diffs injected into
// rework prompts when MaxDiffBytes is not set in EngineConfig.
const defaultMaxDiffBytes = 50000

// buildPromptData constructs the PromptData for a phase from its dependencies.
func (e *Engine) buildPromptData(phase PhaseConfig) (PromptData, error) {
	data := PromptData{
		Ticket:        e.config.Ticket,
		Config:        e.config.PromptConfig,
		Context:       e.config.PromptContext,
		DetectedStack: e.config.DetectedStack,
		WorktreePath:  e.state.Meta().Worktree,
		Branch:        e.state.Meta().Branch,
		BaseBranch:    e.config.BaseBranch,
		Artifacts:     ArtifactData{Extras: make(map[string]string)},
	}

	for _, dep := range phase.DependsOn {
		artifact, err := e.state.ReadArtifact(dep)
		if err != nil {
			// Not all deps produce artifacts; skip if not found.
			continue
		}
		content := string(artifact)

		switch dep {
		case "triage":
			data.Artifacts.Triage = content
		case "plan":
			data.Artifacts.Plan = content
		case "implement":
			data.Artifacts.Implement = content
		case "verify":
			data.Artifacts.Verify = content
		case "review":
			data.Artifacts.Review = content
		case "patch":
			data.Artifacts.Patch = content
		case "submit":
			data.Artifacts.Submit.PRURL = e.extractPRURL()
		default:
			// Custom/user-defined phase: store in Extras map so
			// templates can access via {{index .Artifacts.Extras "name"}}.
			data.Artifacts.Extras[dep] = content
		}
	}

	// Inject sibling-function context for phases that depend on the plan.
	// This reads the plan result JSON, extracts referenced file paths,
	// and collects function signatures so the LLM can see surrounding
	// code in the files it needs to modify.
	if data.Artifacts.Plan != "" {
		if planResult, err := e.state.ReadResult("plan"); err == nil {
			data.SiblingContext = BuildSiblingContext(e.workDir(phase), planResult, e.config.MaxSiblingContextBytes)
		}
	}

	// Populate VerifyClean from the verify result verdict. When verify
	// produced a "pass" verdict, review templates can skip test-gap and
	// schema-alignment sections to reduce cost on clean runs.
	if verifyResult, err := e.state.ReadResult("verify"); err == nil {
		var vr struct {
			Verdict string `json:"verdict"`
		}
		if json.Unmarshal(verifyResult, &vr) == nil && strings.EqualFold(vr.Verdict, "pass") {
			data.VerifyClean = true
		}
	}

	// Inject rework feedback from configured sources. The FeedbackFrom
	// list is read from the phase's own config. Sources are tried in
	// priority order; the first one that produces feedback wins.
	//
	// On patch retry (rework cycle), this block re-extracts feedback
	// from the latest verify/review result on disk — previous feedback
	// is NOT carried over. Each extractor reads the current result file,
	// so after verify/review re-run and overwrite their results, the
	// next rework cycle sees only the new failures. See ReworkFeedback
	// doc comment for the full reset lifecycle.
	if sources := phase.FeedbackFrom; len(sources) > 0 {
		for _, source := range sources {
			if fb := e.extractFeedbackFrom(source); fb != nil {
				data.ReworkFeedback = fb
				eventData := map[string]any{
					"source":  fb.Source,
					"verdict": fb.Verdict,
				}
				switch fb.Source {
				case "review":
					eventData["review_findings"] = len(fb.ReviewFindings)
				case "verify":
					eventData["fixes_count"] = len(fb.FixesRequired)
					eventData["failed_criteria"] = len(fb.FailedCriteria)
					eventData["code_issues"] = len(fb.CodeIssues)
					eventData["failed_commands"] = len(fb.FailedCommands)
				}
				e.emit(Event{
					Phase: phase.Name,
					Kind:  EventReworkFeedbackInjected,
					Data:  eventData,
				})
				break
			}
		}
	}

	return data, nil
}

// computeDiffContext returns the git diff of the current branch against the
// base branch. Used by corrective phases to see what was implemented.
// Returns an empty string on error (non-fatal).
func (e *Engine) computeDiffContext(ctx context.Context) string {
	workDir := e.workDir(PhaseConfig{})
	baseBranch := e.config.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	maxBytes := e.config.MaxDiffBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxDiffBytes
	}

	diffCtx, err := git.Diff(ctx, workDir, e.remoteName()+"/"+baseBranch, maxBytes)
	if err != nil {
		return ""
	}
	return diffCtx
}

// computePlanHash returns the SHA-256 hex digest of the plan artifact.
func (e *Engine) computePlanHash() string {
	artifact, err := e.state.ReadArtifact("plan")
	if err != nil {
		return ""
	}
	h := sha256.Sum256(artifact)
	return fmt.Sprintf("%x", h)
}

// extractPRURL reads the submit result and extracts the pr_url field.
func (e *Engine) extractPRURL() string {
	raw, err := e.state.ReadResult("submit")
	if err != nil {
		return ""
	}
	var result struct {
		PRURL string `json:"pr_url"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ""
	}
	return result.PRURL
}
