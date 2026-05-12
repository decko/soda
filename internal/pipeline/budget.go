package pipeline

import (
	"fmt"
	"strings"
)

// defaultContextBudget is the global default context budget in tokens when
// no per-phase ContextBudget is configured. 80 000 tokens is a conservative
// limit that fits within all common model context windows.
const defaultContextBudget = 80_000

// truncateKeepBytes is the number of bytes kept at the beginning and end
// of an artifact when it is truncated to fit the context budget.
const truncateKeepBytes = 500

// manifestTemplate is the note injected into PromptData.ManifestNote when
// fitToBudget reduces the prompt. It tells the model which sections were
// trimmed and to use tools to retrieve missing context.
const manifestTemplate = "[context-fitted] The following sections were reduced to fit the context window: %s. Use file-read and search tools to retrieve any missing context you need."

// manifestReserveTokens is the number of tokens reserved during the
// reduction loop to accommodate the manifest note that is injected after
// fitting. This prevents a false ContextBudgetError when the last
// reduction step barely brings the prompt under budget but the manifest
// overhead pushes it back over.
const manifestReserveTokens = 150

// reductionStep defines a single reducible field and how to clear it.
// The label identifies the field for the manifest note.
type reductionStep struct {
	label   string
	reduce  func(d *PromptData)
	applies func(d *PromptData) bool
}

// phaseReductionOrder returns the ordered list of reduction steps for a
// given phase name. Protected fields (ticket description, acceptance
// criteria, plan artifact) are never included.
//
// Context is split into two steps: projectContext (ProjectContext+Gotchas,
// shed early) and conventions (RepoConventions, shed last). This keeps
// the compact, high-value convention checklist available as long as possible.
//
// The order is phase-specific:
//   - implement: siblings → exemplars → projectContext → extras → review comments → diff → (rework) → (artifacts) → conventions
//   - review:    siblings → exemplars → extras → Diff → projectContext → (rework) → (artifacts) → conventions
//   - verify:    siblings → exemplars → extras → projectContext → diff → (rework) → (artifacts)  (no conventions — verify.md never renders RepoConventions)
//   - patch:     diff → siblings → exemplars → extras → projectContext → (rework) → (artifacts) → conventions
//
// For unknown phases a sensible default order is used (conventions always last).
func phaseReductionOrder(phase string) []reductionStep {
	// Common steps used across multiple phases.
	siblingStep := reductionStep{
		label:   "SiblingContext",
		reduce:  func(d *PromptData) { d.SiblingContext = "" },
		applies: func(d *PromptData) bool { return d.SiblingContext != "" },
	}
	exemplarStep := reductionStep{
		label:   "PackageExemplars",
		reduce:  func(d *PromptData) { d.PackageExemplars = "" },
		applies: func(d *PromptData) bool { return d.PackageExemplars != "" },
	}
	projectContextStep := reductionStep{
		label: "ProjectContext",
		reduce: func(d *PromptData) {
			d.Context.ProjectContext = ""
			d.Context.Gotchas = ""
		},
		applies: func(d *PromptData) bool {
			return d.Context.ProjectContext != "" || d.Context.Gotchas != ""
		},
	}
	conventionsStep := reductionStep{
		label:   "RepoConventions",
		reduce:  func(d *PromptData) { d.Context.RepoConventions = "" },
		applies: func(d *PromptData) bool { return d.Context.RepoConventions != "" },
	}
	extrasStep := reductionStep{
		label:   "Artifacts.Extras",
		reduce:  func(d *PromptData) { d.Artifacts.Extras = nil },
		applies: func(d *PromptData) bool { return len(d.Artifacts.Extras) > 0 },
	}
	reviewCommentsStep := reductionStep{
		label:   "ReviewComments",
		reduce:  func(d *PromptData) { d.ReviewComments = "" },
		applies: func(d *PromptData) bool { return d.ReviewComments != "" },
	}
	diffStep := reductionStep{
		label:   "DiffContext",
		reduce:  func(d *PromptData) { d.DiffContext = "" },
		applies: func(d *PromptData) bool { return d.DiffContext != "" },
	}
	// Artifact truncation steps — only non-protected artifacts.
	triageStep := reductionStep{
		label:   "Artifacts.Triage",
		reduce:  func(d *PromptData) { d.Artifacts.Triage = truncateArtifact(d.Artifacts.Triage) },
		applies: func(d *PromptData) bool { return len(d.Artifacts.Triage) > 2*truncateKeepBytes },
	}
	implementStep := reductionStep{
		label:   "Artifacts.Implement",
		reduce:  func(d *PromptData) { d.Artifacts.Implement = truncateArtifact(d.Artifacts.Implement) },
		applies: func(d *PromptData) bool { return len(d.Artifacts.Implement) > 2*truncateKeepBytes },
	}
	verifyStep := reductionStep{
		label:   "Artifacts.Verify",
		reduce:  func(d *PromptData) { d.Artifacts.Verify = truncateArtifact(d.Artifacts.Verify) },
		applies: func(d *PromptData) bool { return len(d.Artifacts.Verify) > 2*truncateKeepBytes },
	}
	reviewArtifactStep := reductionStep{
		label:   "Artifacts.Review",
		reduce:  func(d *PromptData) { d.Artifacts.Review = truncateArtifact(d.Artifacts.Review) },
		applies: func(d *PromptData) bool { return len(d.Artifacts.Review) > 2*truncateKeepBytes },
	}
	patchStep := reductionStep{
		label:   "Artifacts.Patch",
		reduce:  func(d *PromptData) { d.Artifacts.Patch = truncateArtifact(d.Artifacts.Patch) },
		applies: func(d *PromptData) bool { return len(d.Artifacts.Patch) > 2*truncateKeepBytes },
	}

	// Rework feedback reduction: inner-out order.
	// 1. Code snippets on findings
	reworkSnippetsStep := reductionStep{
		label: "ReworkFeedback.Snippets",
		reduce: func(d *PromptData) {
			for i := range d.ReworkFeedback.ReviewFindings {
				d.ReworkFeedback.ReviewFindings[i].CodeSnippet = ""
			}
		},
		applies: func(d *PromptData) bool {
			if d.ReworkFeedback == nil {
				return false
			}
			for _, f := range d.ReworkFeedback.ReviewFindings {
				if f.CodeSnippet != "" {
					return true
				}
			}
			return false
		},
	}
	// 2. Implement diff in rework feedback
	reworkDiffStep := reductionStep{
		label:   "ReworkFeedback.ImplementDiff",
		reduce:  func(d *PromptData) { d.ReworkFeedback.ImplementDiff = "" },
		applies: func(d *PromptData) bool { return d.ReworkFeedback != nil && d.ReworkFeedback.ImplementDiff != "" },
	}
	// 3. Command output in failed commands
	reworkCommandOutputStep := reductionStep{
		label: "ReworkFeedback.CommandOutput",
		reduce: func(d *PromptData) {
			for i := range d.ReworkFeedback.FailedCommands {
				d.ReworkFeedback.FailedCommands[i].Output = ""
			}
		},
		applies: func(d *PromptData) bool {
			if d.ReworkFeedback == nil {
				return false
			}
			for _, c := range d.ReworkFeedback.FailedCommands {
				if c.Output != "" {
					return true
				}
			}
			return false
		},
	}
	// 4. Prior cycles
	reworkPriorCyclesStep := reductionStep{
		label:   "ReworkFeedback.PriorCycles",
		reduce:  func(d *PromptData) { d.ReworkFeedback.PriorCycles = nil },
		applies: func(d *PromptData) bool { return d.ReworkFeedback != nil && len(d.ReworkFeedback.PriorCycles) > 0 },
	}

	// Artifact truncation group used across phases.
	artifactSteps := []reductionStep{triageStep, implementStep, verifyStep, reviewArtifactStep, patchStep}

	// Rework reduction group used when ReworkFeedback is present.
	reworkSteps := []reductionStep{reworkSnippetsStep, reworkDiffStep, reworkCommandOutputStep, reworkPriorCyclesStep}

	// reviewDiffStep reduces the review-phase Diff field (distinct from
	// DiffContext which is used for corrective/monitor phases and is never
	// set during review).
	reviewDiffStep := reductionStep{
		label:   "Diff",
		reduce:  func(d *PromptData) { d.Diff = "" },
		applies: func(d *PromptData) bool { return d.Diff != "" },
	}

	switch phase {
	case "implement":
		// Conventions are shed last — they are compact and high-value for implement.
		steps := []reductionStep{siblingStep, exemplarStep, projectContextStep, extrasStep, reviewCommentsStep, diffStep}
		steps = append(steps, reworkSteps...)
		steps = append(steps, artifactSteps...)
		steps = append(steps, conventionsStep)
		return steps
	case "review":
		steps := []reductionStep{siblingStep, exemplarStep, extrasStep, reviewDiffStep, projectContextStep}
		steps = append(steps, reworkSteps...)
		steps = append(steps, artifactSteps...)
		steps = append(steps, conventionsStep)
		return steps
	case "verify":
		// verify.md does not render RepoConventions, so conventionsStep is
		// omitted — shedding a field the template never emits would produce
		// a false manifest note ("RepoConventions reduced") for a field the
		// model never saw.
		steps := []reductionStep{siblingStep, exemplarStep, extrasStep, projectContextStep, diffStep}
		steps = append(steps, reworkSteps...)
		steps = append(steps, artifactSteps...)
		return steps
	case "patch":
		steps := []reductionStep{diffStep, siblingStep, exemplarStep, extrasStep, projectContextStep}
		steps = append(steps, reworkSteps...)
		steps = append(steps, artifactSteps...)
		steps = append(steps, conventionsStep)
		return steps
	default:
		// Sensible default for unknown/custom phases.
		steps := []reductionStep{siblingStep, exemplarStep, extrasStep, reviewCommentsStep, diffStep, projectContextStep}
		steps = append(steps, reworkSteps...)
		steps = append(steps, artifactSteps...)
		steps = append(steps, conventionsStep)
		return steps
	}
}

// estimateTokens converts byte length to an approximate token count using
// the provided bytes-per-token ratio. Returns at least 1 for non-empty input.
func estimateTokens(byteLen int, bytesPerToken float64) int {
	if byteLen <= 0 {
		return 0
	}
	if bytesPerToken <= 0 {
		bytesPerToken = 3.3
	}
	tokens := int(float64(byteLen) / bytesPerToken)
	if tokens < 1 {
		return 1
	}
	return tokens
}

// fitToBudget reduces the PromptData to fit within the given token budget.
// It is a pure function: it returns a modified copy and the list of reduced
// field labels. The caller's PromptData is not mutated — a shallow copy is
// made and returned.
//
// The function works by:
//  1. Rendering the prompt template to measure the current token count.
//  2. If it fits, returning immediately.
//  3. Otherwise, iterating through phase-specific reduction steps in order,
//     re-rendering after each step, until the prompt fits or all steps are
//     exhausted.
//  4. When reductions were applied, injecting a manifest note (~30 tokens)
//     telling the model to use tools for missing context.
//  5. If the prompt still doesn't fit after all reductions, returning a
//     ContextBudgetError.
//
// Protected fields that are never reduced:
//   - Ticket.Description, Ticket.AcceptanceCriteria
//   - Artifacts.Plan
//   - ReworkFeedback.ReviewFindings (the findings themselves, not their snippets)
//   - ReworkFeedback.FixesRequired, ReworkFeedback.FailedCriteria, ReworkFeedback.CodeIssues
func fitToBudget(tmpl string, data PromptData, phase string, budgetTokens int, bytesPerToken float64) (PromptData, []string, error) {
	if bytesPerToken <= 0 {
		bytesPerToken = 3.3
	}
	if budgetTokens <= 0 {
		budgetTokens = defaultContextBudget
	}

	// Render to measure current size.
	rendered, err := RenderPrompt(tmpl, data)
	if err != nil {
		return data, nil, fmt.Errorf("fitToBudget: initial render: %w", err)
	}

	currentTokens := estimateTokens(len(rendered), bytesPerToken)
	if currentTokens <= budgetTokens {
		return data, nil, nil
	}

	// Work on a shallow copy to avoid mutating the caller's data.
	fitted := data
	// Deep-copy ReworkFeedback if present so mutations don't leak.
	if data.ReworkFeedback != nil {
		rfCopy := *data.ReworkFeedback
		// Deep-copy slices that will be mutated.
		if len(rfCopy.ReviewFindings) > 0 {
			findings := make([]EnrichedFinding, len(rfCopy.ReviewFindings))
			copy(findings, rfCopy.ReviewFindings)
			rfCopy.ReviewFindings = findings
		}
		if len(rfCopy.FailedCommands) > 0 {
			cmds := make([]FailedCommand, len(rfCopy.FailedCommands))
			copy(cmds, rfCopy.FailedCommands)
			rfCopy.FailedCommands = cmds
		}
		fitted.ReworkFeedback = &rfCopy
	}
	// Deep-copy Extras map so mutations don't leak.
	if len(data.Artifacts.Extras) > 0 {
		extras := make(map[string]string, len(data.Artifacts.Extras))
		for k, v := range data.Artifacts.Extras {
			extras[k] = v
		}
		fitted.Artifacts.Extras = extras
	}

	steps := phaseReductionOrder(phase)
	var reduced []string

	// Reserve headroom for the manifest note that will be injected after
	// reductions. This prevents the manifest from pushing the prompt over
	// budget when the last reduction step barely fit.
	loopBudget := budgetTokens - manifestReserveTokens
	if loopBudget < 1 {
		loopBudget = 1
	}

	stepIdx := 0
	for ; stepIdx < len(steps); stepIdx++ {
		step := steps[stepIdx]
		if !step.applies(&fitted) {
			continue
		}

		step.reduce(&fitted)
		reduced = append(reduced, step.label)

		rendered, err = RenderPrompt(tmpl, fitted)
		if err != nil {
			return data, nil, fmt.Errorf("fitToBudget: render after reducing %s: %w", step.label, err)
		}

		currentTokens = estimateTokens(len(rendered), bytesPerToken)
		if currentTokens <= loopBudget {
			stepIdx++ // mark this step as consumed before breaking
			break
		}
	}

	// Inject manifest note telling the model what was trimmed.
	if len(reduced) > 0 {
		fitted.ContextFitted = true
		fitted.ManifestNote = fmt.Sprintf(manifestTemplate, strings.Join(reduced, ", "))

		// Re-render with manifest to check final size.
		rendered, err = RenderPrompt(tmpl, fitted)
		if err != nil {
			return data, nil, fmt.Errorf("fitToBudget: render with manifest: %w", err)
		}
		currentTokens = estimateTokens(len(rendered), bytesPerToken)

		// If the manifest pushed us over the real budget, continue
		// reducing from where we left off.
		for ; currentTokens > budgetTokens && stepIdx < len(steps); stepIdx++ {
			step := steps[stepIdx]
			if !step.applies(&fitted) {
				continue
			}

			step.reduce(&fitted)
			reduced = append(reduced, step.label)

			// Re-generate manifest with updated list.
			fitted.ManifestNote = fmt.Sprintf(manifestTemplate, strings.Join(reduced, ", "))

			rendered, err = RenderPrompt(tmpl, fitted)
			if err != nil {
				return data, nil, fmt.Errorf("fitToBudget: render after reducing %s: %w", step.label, err)
			}
			currentTokens = estimateTokens(len(rendered), bytesPerToken)
		}
	}

	if currentTokens > budgetTokens {
		return data, reduced, &ContextBudgetError{
			Phase:         phase,
			BudgetTokens:  budgetTokens,
			CurrentTokens: currentTokens,
		}
	}

	return fitted, reduced, nil
}

// truncateArtifact keeps the first and last truncateKeepBytes of s,
// inserting an ellipsis marker in between. Returns s unchanged if it
// is already short enough.
func truncateArtifact(s string) string {
	if len(s) <= 2*truncateKeepBytes {
		return s
	}
	return s[:truncateKeepBytes] + "\n\n... [truncated] ...\n\n" + s[len(s)-truncateKeepBytes:]
}
