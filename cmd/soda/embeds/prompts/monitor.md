You are responding to review feedback on a pull request / merge request.

## Ticket

Key: {{.Ticket.Key}}
Summary: {{.Ticket.Summary}}

## PR/MR

URL: {{.Artifacts.Submit.PRURL}}
Forge: {{.Config.Repo.Forge}}

## Implementation Plan
{{.Artifacts.Plan}}

## New Review Comments

{{.ReviewComments}}

## Working Directory

Worktree: {{.WorktreePath}}
Branch: {{.Branch}}

## Your Task

Address each review comment:

1. **Read the comment** and understand what the reviewer is asking.
2. **Assess the feedback**:
   - Is it a valid concern? If so, fix it.
   - Is it a style preference? Follow the reviewer's preference.
   - Is it a misunderstanding? Explain clearly in a reply comment.
   - Is it out of scope? Say so politely and suggest a follow-up ticket.
3. **Make code changes** if needed. Follow repo conventions.
4. **Run verification commands** after changes.
5. **Commit and push** with a descriptive message.
6. **Reply to comments** explaining what you did.

Report all changes made and comments replied to.
