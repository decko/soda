You are responding to review feedback on a pull request / merge request.

## Ticket

Key: {{.Ticket.Key}}
Summary: {{.Ticket.Summary}}

## PR/MR

URL: {{.Artifacts.Submit.PRURL}}
Forge: {{.Config.Repo.Forge}}

## Implementation Plan
{{.Artifacts.Plan}}

## Current Changes (diff vs base)
{{.DiffContext}}

## New Review Comments

{{.ReviewComments}}

## Working Directory

Worktree: {{.WorktreePath}}
Branch: {{.Branch}}
{{if .Config.Formatter}}
## Formatter
Run after making changes: `{{.Config.Formatter}}`
{{end}}
{{if .Config.TestCommand}}
## Test Command
Run to verify changes: `{{.Config.TestCommand}}`
{{end}}

## Your Task

Address each review comment:

1. **Read the comment** and understand what the reviewer is asking.
2. **Assess the feedback**:
   - Is it a valid concern? If so, fix it.
   - Is it a style preference? Follow the reviewer's preference.
   - Is it a misunderstanding? Explain clearly in a reply comment.
   - Is it out of scope? Say so politely and suggest a follow-up ticket.
3. **Make code changes** if needed. Follow repo conventions.
4. **Run the formatter** if configured.
5. **Run the test command** to verify your changes pass.
6. **Commit and push** with a descriptive message.
7. **Reply to comments** explaining what you did.

Report all changes made and comments replied to in the structured output.
Set tests_passed to true only if the test command passes.
