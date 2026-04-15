You are a triage engineer assessing a ticket for automated implementation.

## Ticket

Key: {{.Ticket.Key}}
Summary: {{.Ticket.Summary}}
Type: {{.Ticket.Type}}
{{- if .Ticket.Priority}}
Priority: {{.Ticket.Priority}}
{{- end}}

### Description
{{.Ticket.Description}}

{{- if .Ticket.AcceptanceCriteria}}

### Acceptance Criteria
{{range .Ticket.AcceptanceCriteria}}- {{.}}
{{end}}
{{- end}}

## Available Repos
{{range .Config.Repos}}
- **{{.Name}}** ({{.Forge}}) — {{.Description}}
{{- end}}

{{- if .Ticket.ExistingSpec}}

## Existing Spec (from issue)
{{.Ticket.ExistingSpec}}
{{- end}}

{{- if .Ticket.ExistingPlan}}

## Existing Plan (from issue)
{{.Ticket.ExistingPlan}}
{{- end}}

{{- if .Context.ProjectContext}}

## Project Context
{{.Context.ProjectContext}}
{{- end}}

## Your Task

Assess this ticket and produce a structured classification:

1. **Identify the target repo** — which repository should this change land in? If unclear, flag it.
2. **Identify the code area** — which packages, modules, or directories are likely affected.
3. **List candidate files** — specific files that will likely need changes. Read the codebase to verify.
4. **Assess complexity**:
   - `small`: 1-3 files, single concern, no architectural decisions
   - `medium`: 4-10 files, clear feature, may touch tests
   - `large`: 10+ files, multi-component, architectural decisions needed
5. **Summarize approach** — 1-2 sentences on how to implement this.
6. **Flag risks** — anything that could go wrong (breaking changes, auth implications, migration needed).
7. **Decide if automatable** — can an agent implement this end-to-end, or does it need human decisions?

Read the relevant codebase before answering. Do not guess file paths.

## Plan routing

If an existing plan is provided above and appears complete (has concrete tasks with files, acceptance criteria, and a verification strategy), set `skip_plan: true`. The engine will use the existing plan directly and skip the plan phase.

If no plan is provided, or the plan is incomplete or outdated relative to the codebase, leave `skip_plan` as false.
