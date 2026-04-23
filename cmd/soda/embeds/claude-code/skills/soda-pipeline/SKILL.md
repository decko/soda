---
name: soda-pipeline
description: SODA pipeline architecture, phase lifecycle, state management, and operational procedures. Use when working on pipeline code, debugging phase failures, or understanding how SODA orchestrates AI coding sessions.
globs:
  - "internal/pipeline/**"
  - "cmd/soda/embeds/**"
  - "phases.yaml"
  - "pipelines/**"
  - "schemas/**"
---

# SODA Pipeline Architecture

## Phase lifecycle

Each pipeline phase follows this lifecycle:

1. Engine calls `buildPromptData()` — assembles ticket, artifacts from prior phases, config, and context
2. `PromptLoader.Load()` resolves the prompt template (user override > working dir > embedded)
3. `RenderPrompt()` executes the Go `text/template` against `PromptData`
4. Runner invokes Claude Code CLI with `--print --bare --output-format json --json-schema <schema>`
5. Response is parsed via `claude.ParseResponse()` — extracts structured output, cost, tokens
6. Result is written atomically to `.soda/<ticket>/<phase>.json`
7. Engine emits events (`EventPhaseStarted`, `EventPhaseCompleted`, etc.)

## Phase types

| Type | Behavior |
|------|----------|
| (default) | One-shot execution, structured output |
| `corrective` | Triggered by a parent phase's `corrective` config on failure |
| `parallel-review` | Runs multiple reviewers concurrently, merges findings |
| `polling` | Repeated execution on interval (monitor phase) |
| `post-submit` | Runs after submit (follow-up phase) |

## Prompt template data

All prompts receive `pipeline.PromptData`:
- `Ticket` — key, summary, description, acceptance criteria, comments
- `Config` — repos, formatter, test command
- `Artifacts` — outputs from prior phases (triage, plan, implement, verify, review)
- `Context` — project context, repo conventions, gotchas
- `WorktreePath`, `Branch`, `BaseBranch` — git context
- `ReworkFeedback` — injected on rework cycles (verify failures or review findings with code snippets)
- `DiffContext` — git diff for monitor and corrective phases
- `ReviewComments` — PR review comments for monitor response sessions
- `SiblingContext` — sibling functions near review findings (injected automatically)

## Context injection

The engine injects context to reduce rework (knowledge_miss_rate):

1. **Code snippet injection** — ±5 lines around each review finding's `file:line` as `EnrichedFinding.CodeSnippet`
2. **Sibling function injection** — full bodies of functions adjacent to findings (`SiblingContext` in PromptData)
3. **Full-file injection** — when rework feedback references a file, inject its full content (30KB cap, dedup)
4. **Deep context injection** — full function bodies for files referenced in the plan (budget-controlled)

## Error categories and retry

| Category | Retry | Strategy |
|----------|-------|----------|
| Transient (429, 500, timeout) | configurable (default 2) | exponential backoff |
| Parse (invalid JSON output) | configurable (default 1) | retry with error appended |
| Semantic (empty plan, no tests) | configurable (default 1) | retry with corrective feedback |

## State management

Pipeline state lives in `.soda/<ticket>/`:
- `meta.json` — ticket, phases status, costs, worktree, branch, rework cycles, token counts
- `<phase>.json` — structured output per phase
- `lock` — flock-based concurrency control
- `events.jsonl` — structured event log
- `monitor_state.json` — monitor polling state (poll count, rounds, last comment ID)
- Atomic writes: write to `.tmp` then rename; archive on re-run

## Rework and corrective routing

- **Review rework**: critical/major findings route back to implement (max 2 cycles)
- **Verify corrective**: verify failures trigger the `patch` corrective phase
- **Patch escalation**: if patch exhausts max attempts, escalates to implement
- **Regression detection**: prevents patch from introducing new failures
- Feedback is rebuilt from scratch each cycle (not accumulated)

## Monitor phase

After PR submission, the monitor polls for activity:

- **Passive mode** (default): polls CI status, PR state (merged/closed/approved), detects new comments and notifies user. No self_user required.
- **Active mode** (`respond_to_comments: true` + `self_user`): classifies comments, responds with Claude sessions (fix or reply), posts acknowledgments, auto-rebases.
- **Profiles**: `conservative`, `smart`, `aggressive` control auto-rebase, nit handling, and response to non-authoritative comments.

## Named pipelines

Custom pipeline definitions in `./pipelines/` or `~/.config/soda/pipelines/`:
- `soda run <ticket> --pipeline quick-fix` — 3-phase: implement→verify→submit
- `soda run <ticket> --pipeline docs-only` — 2-phase: implement→submit
- `soda pipelines` — list available pipelines
- `soda pipelines new <name>` — scaffold a new pipeline definition

## Embedded content

Embedded content lives in `cmd/soda/embeds/`:
- `phases.yaml` — pipeline phase definitions (go:embed as `[]byte`)
- `prompts/*.md` — phase prompt templates (go:embed as `embed.FS`)

Resolution order: user config dir > working dir > embedded defaults.

## Operational runbook

See [RUNBOOK.md](./RUNBOOK.md) for troubleshooting, common failure scenarios, resume procedures, cost management, and event log analysis.
