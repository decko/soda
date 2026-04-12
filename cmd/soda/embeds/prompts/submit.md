You are submitting a verified implementation as a pull request or merge request.

## Ticket

Key: {{.Ticket.Key}}
Summary: {{.Ticket.Summary}}

## Implementation Report
{{.Artifacts.Implement}}

## Verification Report
{{.Artifacts.Verify}}

## Repo Configuration

Forge: {{.Config.Repo.Forge}}
Push to: {{.Config.Repo.PushTo}}
Target: {{.Config.Repo.Target}}

{{- if .Config.Repo.Labels}}
Labels: {{range .Config.Repo.Labels}}{{.}}, {{end}}
{{- end}}

{{- if .Config.Repo.Trailers}}
Commit trailers: {{range .Config.Repo.Trailers}}
- {{.}}
{{- end}}
{{- end}}

## Working Directory

Worktree: {{.WorktreePath}}
Branch: {{.Branch}}

## Your Task

1. **Push the branch** to the configured remote.
2. **Create the PR/MR** with:
   - Title: concise, under 70 characters, referencing the ticket key
   - Body: summary of changes, test plan, acceptance criteria checklist
   - Labels: as configured
   - Any required trailers on the last commit (if not already present)

{{- if eq .Config.Repo.Forge "github"}}

Use `gh pr create`:
```
gh pr create --title "<title>" --body "<body>" {{range .Config.Repo.Labels}}--label "{{.}}" {{end}}
```

{{- else if eq .Config.Repo.Forge "gitlab"}}

Use `glab mr create`:
```
glab mr create --title "<title>" --description "<body>" --remove-source-branch --squash-before-merge {{range .Config.Repo.Labels}}--label "{{.}}" {{end}}
```

{{- end}}

3. **Report** the PR/MR URL and any issues encountered.
