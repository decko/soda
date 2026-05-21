# Phases reference

Every SODA pipeline is a sequence of phases. Each phase runs in a fresh,
sandboxed Claude Code session with a scoped tool set and a structured output
schema. This guide documents every phase: what it does, what it costs, how it
fails, and how it connects to other phases.

For pipeline construction (adding, removing, reordering phases), see
[pipelines.md](pipelines.md). For `phases.yaml` field-level reference, see
[configuration.md](configuration.md#phasesyaml-reference).

---

## Phase execution model

Each phase follows the same lifecycle:

1. **Render prompt** — the engine renders the phase's Go template with
   `PromptData` (ticket, artifacts from prior phases, rework feedback).
2. **Spawn session** — Claude Code runs in `--print --bare` mode inside a
   sandbox with `--allowed-tools` scoped to the phase's tool list.
3. **Parse output** — the response is validated against the phase's JSON schema
   (auto-generated from Go structs in `schemas/`).
4. **Write artifact** — the structured output is written to
   `.soda/<ticket>/<phase>.json`.
5. **Gate** — the engine evaluates the output (verdict, complexity, etc.) and
   decides whether to proceed, rework, or stop.

Context resets between phases — each session starts clean. Prior phase results
are injected via prompt template variables, not carried in memory.

---

## Pipeline flow

```
Triage → Plan → Implement → Verify ──→ Review → Submit → Follow-up → Monitor
                    ↑           ↑   FAIL   │        │
                    │           └── Patch ──┘        │
                    │              (corrective)      │
                    └──────── rework ────────────────┘
                              (max 2 cycles)
```

Phases in the forward path run in order. Two feedback loops exist:

- **Corrective loop** (verify → patch → verify): triggered when verify returns
  a `FAIL` verdict. The patch phase makes targeted fixes; verify re-runs. Up to
  `max_attempts` cycles (default 2).
- **Rework loop** (review → implement → verify → review): triggered when review
  returns a `rework` verdict (critical or major findings). The implement phase
  re-runs with review feedback injected. Up to 2 cycles (configurable).

Two phases are conditional and do not run in the forward pass:

- **Patch** (`type: corrective`) — only runs when verify triggers it.
- **Follow-up** (`type: post-submit`) — only runs when review verdict is
  `pass-with-follow-ups`.

---

## Phase reference

### Triage

Classify the ticket, identify the target repo, affected files, and complexity
band. Decide whether the ticket is automatable and whether an existing plan
should be reused.

| Field | Value |
|-------|-------|
| **Type** | normal (forward) |
| **Tools** | `Read`, `Glob`, `Grep`, `Bash(git:*)`, `Bash(ls:*)` |
| **Timeout** | 3m |
| **Model** | global (from `soda.yaml`) |
| **Retry** | transient: 2, parse: 1, semantic: 1 |
| **Depends on** | *(none — first phase)* |
| **Feedback from** | *(none)* |
| **Cost** | mean $0.62 (n=88, min $0.14, max $1.35) |

**Structured output** (`schemas.TriageOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `repo` | string | Target repository |
| `code_area` | string | Affected code area |
| `files` | []string | Files likely to be modified |
| `complexity` | enum | `low`, `medium`, or `high` |
| `approach` | string | High-level approach description |
| `risks` | []string | Implementation risks |
| `automatable` | enum | `yes`, `no`, or `partial` |
| `block_reason` | string | Why not automatable (when `no` or `partial`) |
| `skip_plan` | bool | `true` when an existing plan is found in the ticket |

**Failure modes:**

- **`automatable: "no"`** — the engine emits a `PhaseGateError` and stops.
  Common causes: ticket requires human judgment, external system access, or
  manual testing.
- **Parse failure** — triage output doesn't match the JSON schema. Retried with
  the error message appended to the prompt (up to `parse` retries).
- **Semantic failure** — output is valid JSON but semantically wrong (e.g.,
  `complexity` doesn't match the actual ticket scope). Retried with corrective
  feedback.
- **Timeout** — 3 minutes exceeded. Usually caused by the model exploring too
  many files. Tool scoping to read-only prevents runaway writes.

**Engine behavior:**

- Sets `Complexity` on pipeline state (used by downstream `condition` templates).
- When `skip_plan: true`, the plan phase is skipped and the existing plan from
  the ticket is injected directly as the plan artifact.
- When `automatable: "partial"`, the pipeline proceeds but the approach
  describes the automatable subset.

---

### Plan

Design the implementation approach and break it into atomic tasks with
verifiable done-when conditions.

| Field | Value |
|-------|-------|
| **Type** | normal (forward) |
| **Tools** | `Read`, `Glob`, `Grep`, `Bash(git:*)`, `Bash(ls:*)` |
| **Timeout** | 8m |
| **Model** | global (from `soda.yaml`) |
| **Retry** | transient: 2, parse: 1, semantic: 1 |
| **Depends on** | triage |
| **Feedback from** | *(none)* |
| **Cost** | mean $0.70 (n=93, min $0.06, max $1.66) |

**Structured output** (`schemas.PlanOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `approach` | string | Implementation strategy |
| `tasks` | []PlanTask | Ordered list of atomic tasks |
| `tasks[].id` | string | Task ID (e.g., `T1`, `T2`) |
| `tasks[].description` | string | What to do |
| `tasks[].files` | []string | Files to create or modify |
| `tasks[].done_when` | string | Verifiable completion condition |
| `tasks[].depends_on` | []string | Task IDs this task depends on |
| `verification` | VerifyStrategy | How to verify the implementation |
| `verification.commands` | []string | Shell commands to run |
| `verification.manual_steps` | []string | Human verification steps (if any) |
| `deviations` | []string | Deviations from the ticket's requirements |

**Failure modes:**

- **Empty tasks list** — semantic failure. The model produced a valid plan
  structure but no actionable tasks. Retried with corrective feedback.
- **Skipped** — when triage sets `skip_plan: true` (existing plan found in the
  ticket) or when a `condition` expression evaluates to `"false"` (e.g.,
  `{{ ne .Complexity "low" }}`).
- **Parse failure** — plan output doesn't match the JSON schema. Retried.
- **Timeout** — 8 minutes exceeded. Rare for read-only phases.

**Engine behavior:**

- Plan output is injected into the implement phase's prompt as `{{.Artifacts.Plan}}`.
- Task IDs and `done_when` conditions are used by verify to check acceptance criteria.
- Read-only tool set prevents the plan phase from modifying code.

---

### Implement

Write code, run tests, and commit changes. This is the most expensive phase and
the primary code-generation step.

| Field | Value |
|-------|-------|
| **Type** | normal (forward) |
| **Tools** | `Read`, `Write`, `Edit`, `Glob`, `Grep`, `Bash` (full access) |
| **Timeout** | 25m |
| **Model** | `claude-opus-4-6` (per-phase override in default pipeline) |
| **Retry** | transient: 2, parse: 1, semantic: 0 |
| **Depends on** | plan |
| **Feedback from** | review, verify |
| **Cost** | mean $2.53/generation (n=94, min $0.16, max $8.73) |

**Structured output** (`schemas.ImplementOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `branch` | string | Working branch name |
| `commits` | []CommitRecord | Commits made during implementation |
| `commits[].hash` | string | Commit hash |
| `commits[].message` | string | Commit message |
| `commits[].task_id` | string | Plan task this commit implements |
| `files_changed` | []FileChange | Files created, modified, or deleted |
| `files_changed[].path` | string | File path |
| `files_changed[].action` | string | `created`, `modified`, or `deleted` |
| `task_results` | []TaskResult | Outcome per plan task |
| `task_results[].task_id` | string | Plan task ID |
| `task_results[].status` | string | `completed`, `failed`, or `skipped` |
| `task_results[].reason` | string | Why the task failed or was skipped |
| `tests_passed` | bool | Whether tests passed before committing |
| `test_output` | string | Test command output (on failure) |
| `deviations` | []string | Deviations from the plan |

**Failure modes:**

- **Timeout** — 25 minutes exceeded. The most common failure for high-complexity
  tickets (7+ tasks). Consider raising the timeout or splitting the ticket.
- **Semantic retry disabled** (`semantic: 0`) — the engine does not retry
  semantic failures for implement. If the output is structurally valid but the
  code is wrong, the verify phase catches it and the corrective loop handles
  fixes. This avoids expensive full re-implementations.
- **Budget exceeded** — `BudgetExceededError` when `max_cost_per_generation` or
  `max_cost_per_phase` (cumulative) is hit. Cost accumulates across rework
  cycles.
- **Rework injection** — on rework cycles, `ReworkFeedback` is injected with
  prior review findings, enriched with ±5 lines of code context around each
  `file:line` location (`EnrichedFinding.CodeSnippet`). This eliminates
  retrieval gaps that cause repeated rework.

**Engine behavior:**

- Full tool access — the model can read, write, and execute arbitrary commands.
- Runs inside a sandbox with Landlock filesystem isolation and network
  namespaces when `sandbox.enabled: true`.
- On rework cycles, prior verify and review findings are injected via
  `feedback_from` configuration.
- Sibling function context (`SiblingContext`) is injected from files referenced
  in the plan to reduce retrieval misses.

---

### Patch

Make targeted fixes based on verify feedback. A lightweight corrective phase
that avoids re-running a full implement session.

| Field | Value |
|-------|-------|
| **Type** | `corrective` (only runs when triggered by verify) |
| **Tools** | `Read`, `Write`, `Edit`, `Glob`, `Grep`, `Bash` (full access) |
| **Timeout** | 8m |
| **Model** | `claude-sonnet-4-6` (cheaper model for targeted fixes) |
| **Retry** | transient: 2, parse: 1, semantic: 0 |
| **Depends on** | implement |
| **Feedback from** | verify |
| **Cost** | mean $0.55/generation (n=18, min $0.08, max $1.91) |

**Structured output** (`schemas.PatchOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `fix_results` | []FixResult | Outcome per verify fix request |
| `fix_results[].fix_index` | int | Index into verify's `fixes_required` |
| `fix_results[].status` | string | `fixed`, `partial`, or `cannot_fix` |
| `fix_results[].description` | string | What was done |
| `fix_results[].reason` | string | Why partial or cannot_fix |
| `files_changed` | []FileChange | Files modified by the patch |
| `tests_passed` | bool | Whether tests pass after patching |
| `too_complex` | bool | Patch self-reports as too complex for targeted fix |
| `too_complex_reason` | string | Why escalation is needed |

**Failure modes:**

- **`too_complex: true`** — the patch model determines the fix requires broader
  changes than a targeted patch can handle. The engine checks the
  `on_exhausted` policy: `stop`, `escalate` (route to full implement), or
  `retry`.
- **Regression** — patch introduces new test failures that did not exist in the
  previous verify run. Detected by comparing `criteria_results` between verify
  cycles. Triggers immediate escalation regardless of remaining attempts.
- **Max attempts exhausted** — `max_attempts` corrective cycles reached
  (default 2). The `on_exhausted` policy determines next action.
- **Escalation** — when `on_exhausted: "escalate"`, the engine routes to a
  full implement session (one-shot, guarded by the `EscalatedFromPatch` flag
  to prevent infinite loops).

**Engine behavior:**

- Never runs in the forward pass — only triggered by verify's `corrective`
  config block.
- Uses a cheaper model (Sonnet) for cost efficiency on targeted fixes.
- Verify re-runs after every patch cycle.
- `PatchCycles` counter in `meta.json` tracks corrective attempts independently
  from `ReworkCycles`.

---

### Verify

Run tests, check acceptance criteria, and review code for issues. Acts as the
quality gate between implementation and review.

| Field | Value |
|-------|-------|
| **Type** | normal (forward) |
| **Tools** | `Read`, `Glob`, `Grep`, `Bash` (no write access) |
| **Timeout** | 8m |
| **Model** | global (from `soda.yaml`) |
| **Retry** | transient: 2, parse: 1, semantic: 1 |
| **Depends on** | plan, implement |
| **Feedback from** | *(none)* |
| **Cost** | mean $1.47 (n=85, min $0.08, max $6.87) |

**Structured output** (`schemas.VerifyOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `verdict` | enum | `PASS` or `FAIL` |
| `criteria_results` | []CriterionResult | Pass/fail per acceptance criterion |
| `criteria_results[].criterion` | string | Acceptance criterion text |
| `criteria_results[].passed` | bool | Whether the criterion is met |
| `criteria_results[].evidence` | string | Evidence supporting the result |
| `command_results` | []CommandResult | Test command execution results |
| `command_results[].command` | string | Command that was run |
| `command_results[].exit_code` | int | Exit code |
| `command_results[].output` | string | Command output |
| `command_results[].passed` | bool | Whether the command succeeded |
| `code_issues` | []CodeIssue | Code problems found during review |
| `code_issues[].file` | string | File path |
| `code_issues[].line` | int | Line number |
| `code_issues[].severity` | string | `critical`, `major`, or `minor` |
| `code_issues[].issue` | string | Issue description |
| `code_issues[].suggested_fix` | string | Suggested fix |
| `fixes_required` | []string | Specific fixes needed (fed to patch) |

**Failure modes:**

- **`verdict: "FAIL"`** — triggers the corrective loop. The engine invokes the
  patch phase with `fixes_required` as feedback. After patch, verify re-runs.
- **Flaky tests** — transient test failures that pass on retry. The `transient`
  retry policy (default 2) handles API-level transients, but test flakiness
  requires the corrective loop.
- **Timeout** — 8 minutes exceeded. Can happen with large test suites. The
  `test_command` from `soda.yaml` controls which tests run.
- **All criteria pass but verdict is FAIL** — semantic inconsistency. The
  semantic retry (default 1) catches this.

**Engine behavior:**

- No write access — verify cannot modify code, only read and run tests.
- `criteria_results` are compared between verify cycles for regression
  detection (criteria that passed before but fail after patch).
- `fixes_required` is injected into the patch phase's prompt via
  `feedback_from: [verify]`.
- `command_results` captures test output for debugging.

---

### Review

Parallel specialist review of the implementation. Multiple reviewers run
concurrently and their findings are merged into a single output.

| Field | Value |
|-------|-------|
| **Type** | `parallel-review` |
| **Tools** | `Read`, `Glob`, `Grep`, `Bash` (no write access) |
| **Timeout** | 12m |
| **Model** | `claude-opus-4-6` (per-phase override in default pipeline) |
| **Retry** | transient: 2, parse: 1, semantic: 1 |
| **Depends on** | plan, implement, verify |
| **Feedback from** | *(none)* |
| **Cost** | mean $3.10/generation (n=82, min $0.16, max $9.95) |

**Structured output** (`schemas.ReviewOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `findings` | []ReviewFinding | Issues found by reviewers |
| `findings[].source` | string | Reviewer name (engine-populated) |
| `findings[].severity` | string | `critical`, `major`, or `minor` |
| `findings[].file` | string | File path |
| `findings[].line` | int | Line number |
| `findings[].issue` | string | Issue description |
| `findings[].suggestion` | string | Suggested fix |
| `findings[].category` | enum | `retrieval`, `convention`, `logic`, `test_pattern`, or `documentation` |
| `verdict` | enum | `pass`, `rework`, or `pass-with-follow-ups` |

**Default reviewers** (configurable in `phases.yaml`):

| Reviewer | Focus | Condition |
|----------|-------|-----------|
| `go-specialist` | Go idioms, error handling, interface design, test quality, performance | always |
| `ai-harness` | prompt engineering, context budget, Claude CLI integration, sandbox, structured output | `{{ ne .Complexity "low" }}` (skipped for low-complexity tickets) |

**Failure modes:**

- **`verdict: "rework"`** — critical or major findings trigger the rework loop.
  The engine routes back to implement with findings injected as
  `ReworkFeedback`. Code snippets (±5 lines around each finding's `file:line`)
  are injected as `EnrichedFinding.CodeSnippet`.
- **Max rework cycles** — after 2 cycles (default), if only minor findings
  remain, verdict is downgraded to `pass-with-follow-ups`. If critical/major
  findings remain, the engine emits `PhaseGateError` and stops.
- **Reviewer failure** — `min_reviewers: 1` means the phase succeeds if at
  least one reviewer passes. Total reviewer failure (all reviewers fail) stops
  the pipeline.
- **Budget exhaustion** — review is the most expensive phase. With 2+ rework
  cycles ($3–5 each), `CumulativeCost` can exceed `max_cost_per_phase`.
  Consider raising the limit or using `max_cost_per_generation` instead.

**Engine behavior:**

- Reviewers run in parallel with `reviewer_stagger: 5s` between starts to
  reduce API burst.
- Review prompts include `git diff main...HEAD` so reviewers focus on changed
  code only.
- On rework cycles, prior findings are injected with exclusion instructions to
  prevent the whack-a-mole pattern (reviewer re-flagging already-fixed code).
- `source` field on `ReviewFinding` is populated by the engine (not the model)
  with the reviewer's `name`.

---

### Submit

Push the branch and create a pull request (or merge request for GitLab).

| Field | Value |
|-------|-------|
| **Type** | normal (forward) |
| **Tools** | `Bash(git:*)`, `Bash(gh:*)`, `Bash(glab:*)` |
| **Timeout** | 3m |
| **Model** | global (from `soda.yaml`) |
| **Retry** | transient: 2, parse: 1, semantic: 0 |
| **Depends on** | implement, verify, review |
| **Feedback from** | *(none)* |
| **Cost** | mean $0.17 (n=80, min $0.04, max $0.63) |

**Structured output** (`schemas.SubmitOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `pr_url` | string | Pull request URL |
| `pr_number` | int | PR number |
| `title` | string | PR title |
| `branch` | string | Source branch |
| `target` | string | Target branch |
| `forge` | string | `github` or `gitlab` |

**Failure modes:**

- **Auth failure** — `GH_TOKEN` not available inside the sandbox. The engine
  extracts it from `gh auth token` on the host side and passes it in. SSH agent
  socket is added to sandbox read paths for `git push`.
- **Branch conflict** — a branch with the same name already exists on the
  remote. Usually from a previous failed run. `soda clean` removes stale
  branches.
- **Timeout** — 3 minutes exceeded. Rare. Usually indicates network issues.

**Engine behavior:**

- Tool set is minimal — only git and forge CLI access. No file reads or writes.
- `pr_url` is stored in `SubmitOutput` and injected into downstream phases
  (follow-up, monitor) via `{{.Artifacts.Submit.PRURL}}`.
- Labels from `repos[].labels` are applied to the created PR.
- Commit trailers from `repos[].trailers` are appended to commit messages.

---

### Follow-up

Create follow-up tickets from minor review findings. Only runs when the review
verdict is `pass-with-follow-ups`.

| Field | Value |
|-------|-------|
| **Type** | `post-submit` (best-effort, non-terminal) |
| **Tools** | `Bash(gh:*)` |
| **Timeout** | 3m |
| **Model** | global (from `soda.yaml`) |
| **Retry** | transient: 2, parse: 1, semantic: 0 |
| **Depends on** | review, submit |
| **Feedback from** | *(none)* |
| **Cost** | mean $0.07 (n=11, min $0.05, max $0.09) |

**Structured output** (`schemas.FollowUpOutput`):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `actions` | []FollowUpAction | Action taken per minor finding |
| `actions[].finding` | string | The minor finding text |
| `actions[].action` | string | `created`, `updated`, or `skipped` |
| `actions[].ticket_url` | string | URL of the created/updated ticket |
| `actions[].ticket_number` | int | Ticket number |
| `actions[].reason` | string | Why skipped (if applicable) |

**Failure modes:**

- **Failures are non-terminal** — the follow-up phase is `post-submit`.
  If it fails (API error, timeout, parse failure), the pipeline still succeeds.
  The failure is logged as an event but does not affect exit status.
- **No findings** — if the review produced no minor findings, the phase has
  nothing to do. It still runs and produces an empty `actions` list.

**Engine behavior:**

- Only runs when review verdict is `pass-with-follow-ups` (minor findings
  exist but no critical/major issues).
- Uses only `Bash(gh:*)` — it creates GitHub issues, not code changes.
- The cheapest phase by far at $0.07 mean cost.

---

### Monitor

Poll the PR for review comments, CI status, and merge conflicts. Respond to
comments, fix code, and optionally auto-merge.

| Field | Value |
|-------|-------|
| **Type** | `polling` (long-running loop) |
| **Tools** | `Read`, `Write`, `Edit`, `Glob`, `Grep`, `Bash` (full access for fix sessions) |
| **Timeout** | 10m per response session |
| **Model** | global (from `soda.yaml`) |
| **Retry** | transient: 2, parse: 1, semantic: 0 |
| **Depends on** | submit, plan |
| **Feedback from** | *(none)* |
| **Cost** | estimated ~$1–3 per response round (no aggregate data available) |

**Structured output** (`schemas.MonitorOutput`, per response round):

| Field | Type | Description |
|-------|------|-------------|
| `ticket_key` | string | Ticket identifier |
| `pr_url` | string | Pull request URL |
| `comments_handled` | []CommentAction | How each comment was addressed |
| `comments_handled[].comment_id` | string | Comment ID |
| `comments_handled[].author` | string | Comment author |
| `comments_handled[].content` | string | Comment text |
| `comments_handled[].action` | string | `fixed`, `explained`, `deferred`, or `skipped` |
| `comments_handled[].response` | string | Response text |
| `comments_handled[].classification` | string | `code_change`, `question`, `nit`, `approval`, `dismissal`, `bot_generated`, or `self_authored` |
| `comments_handled[].authoritative` | bool | Whether the author has CODEOWNERS authority |
| `files_changed` | []FileChange | Files modified during fix sessions |
| `commits` | []CommitRecord | Commits made during fix sessions |
| `tests_passed` | bool | Whether tests pass after fixes |

**Polling behavior:**

The monitor phase does not run a single LLM session. It runs a polling loop
that checks PR status periodically and only spawns Claude sessions when there
is something to respond to.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `initial_interval` | 2m | Time between poll cycles |
| `max_interval` | 5m | Poll interval after escalation |
| `escalate_after` | 30m | Switch to `max_interval` after this duration |
| `max_duration` | 4h | Total wall-clock limit for the monitor phase |
| `max_response_rounds` | 3 | Max Claude sessions (fix + reply combined) |
| `respond_to_comments` | true | Enable comment classification and response |
| `auto_merge` | false | Auto-merge when CI green and approved |

**Polling cycle:**

1. Check PR status (approved, merged, closed)
2. Check for new comments since last poll
3. Check CI status
4. Check for merge conflicts
5. If new actionable comments exist → spawn a Claude session to respond
6. Sleep for `initial_interval` (or `max_interval` after escalation)

**LLM sessions are only spawned when:**

- A new comment is classified as actionable (`code_change` or `question`)
- A `nit` comment arrives and the monitor profile enables auto-fix
- CI fails and the fix requires code changes

**Comment classification:**

| Classification | Action | LLM cost |
|----------------|--------|----------|
| `code_change` | Fix session (full tool access) | ~$1–3 |
| `question` | Reply-only session (read-only tools) | ~$0.50 |
| `nit` | Auto-fix if profile allows, else skip | ~$0.50 |
| `approval` | Skip (no response needed) | $0 |
| `dismissal` | Skip | $0 |
| `bot_generated` | Skip | $0 |
| `self_authored` | Skip | $0 |

**Monitor profiles:**

| Profile | Poll interval | Response rounds | Auto-fix nits | Auto-rebase | Non-auth comments |
|---------|--------------|-----------------|--------------|-------------|-------------------|
| `conservative` | 5m | 2 | no | no | ignore |
| `smart` (default) | 2m | 3 | yes | yes | ignore |
| `aggressive` | 1m | 5 | yes | yes | respond |

**Failure modes:**

- **`self_user` not configured** — the monitor cannot classify comments and
  falls back to a stub. No responses are sent.
- **`respond_to_comments: false`** — comment classification is disabled; the
  monitor only watches for merge/close events.
- **Max response rounds exceeded** — the monitor stops responding but continues
  polling until `max_duration` or PR merge/close.
- **PR review comments invisible** — `gh pr review` posts to
  `/pulls/N/reviews`, which the GitHub poller does NOT poll. Only inline review
  comments (`/pulls/N/comments`) and top-level conversation comments
  (`/issues/N/comments`) are fetched.

**Engine behavior:**

- Fix sessions get full tool access; reply-only sessions get read-only tools.
- Each response round (fix or reply) counts toward `max_response_rounds`.
- The timeout (10m) applies per response session, not to the entire polling
  loop. `max_duration` (4h) caps total monitor time.
- Monitor state is persisted in `monitor_state.json` (poll count, response
  rounds used, last comment ID) for crash recovery.
- Requires `self_user` in `soda.yaml` to distinguish self-authored comments
  from external ones.

---

## Cost summary

Real cost data from 99 pipeline sessions (extracted from `.soda/*/meta.json`).
All values in USD.

| Phase | Mean | Min | Max | Sessions |
|-------|------|-----|-----|----------|
| Triage | $0.62 | $0.14 | $1.35 | 88 |
| Plan | $0.70 | $0.06 | $1.66 | 93 |
| Implement | $2.53 | $0.16 | $8.73 | 94 |
| Patch | $0.55 | $0.08 | $1.91 | 18 |
| Verify | $1.47 | $0.08 | $6.87 | 85 |
| Review | $3.10 | $0.16 | $9.95 | 82 |
| Submit | $0.17 | $0.04 | $0.63 | 80 |
| Follow-up | $0.07 | $0.05 | $0.09 | 11 |
| Monitor | *~$1–3/round* | — | — | *estimated* |

> **Note on monitor cost:** No aggregate monitor cost data is available from
> raki sessions. The estimate is based on per-response session costs for fix
> and reply sessions, which are comparable to patch-level work.

**Cost drivers:**

- **Review** is the most expensive phase ($3.10 mean) — parallel specialist
  reviewers each run full sessions. On rework cycles, cumulative review cost
  can reach $10–15.
- **Implement** is the second most expensive ($2.53 mean) — full code
  generation with test execution. High-complexity tickets can reach $8+.
- **Follow-up** is the cheapest ($0.07 mean) — it only creates GitHub issues.
- **Submit** is nearly free ($0.17 mean) — minimal reasoning needed for
  git/forge CLI calls.
- **Patch** is cost-efficient ($0.55 mean) — uses Sonnet for targeted fixes,
  avoiding expensive full re-implementations.

---

## Corrective loop detail

```
Verify ──FAIL──→ Patch ──→ Verify (re-run)
  │                           │
  │                           ├── PASS → proceed to Review
  │                           ├── FAIL (attempts remaining) → Patch again
  │                           ├── FAIL (regression detected) → escalate immediately
  │                           └── FAIL (max_attempts reached) → on_exhausted policy
  │
  └──PASS──→ proceed to Review
```

**Configuration** (on the verify phase in `phases.yaml`):

```yaml
corrective:
  phase: patch          # corrective phase to invoke
  max_attempts: 2       # max patch cycles
  on_exhausted: stop    # "stop", "escalate", or "retry"
  escalate_to: implement
```

**Exhaustion policies:**

| Policy | Behavior |
|--------|----------|
| `stop` | Pipeline stops with a failure |
| `escalate` | Route to full implement (one-shot, guarded by `EscalatedFromPatch` flag) |
| `retry` | Allow one extra patch attempt |

**Regression detection:** The engine compares `criteria_results` between verify
cycles. If a criterion that passed in the previous cycle now fails, it is
classified as a regression introduced by the patch. Regressions trigger
immediate escalation regardless of remaining attempts.

---

## Rework loop detail

```
Review ──rework──→ Implement (with ReworkFeedback) ──→ Verify ──→ Review
  │                                                                 │
  │                                                    ├── rework (cycle < max) → loop
  │                                                    ├── pass → Submit
  │                                                    └── rework (cycle = max) → downgrade or stop
  │
  ├──pass──→ Submit
  └──pass-with-follow-ups──→ Submit + Follow-up
```

**Rework feedback injection:**

When review triggers rework, the engine constructs `ReworkFeedback` containing:

- Prior review findings (critical and major only)
- Code snippets (±5 lines around each finding's `file:line`)
- Exclusion instructions for findings that were already fixed

This context is injected into the implement prompt via
`{{.ReworkFeedback}}`. The implement session sees the exact code without
spending tokens on tool calls to locate it.

**Max rework behavior:**

At max rework cycles (default 2):

- If only minor findings remain → verdict is downgraded to
  `pass-with-follow-ups` and the pipeline proceeds to submit.
- If critical or major findings remain → `PhaseGateError` stops the pipeline.

---

## See also

- [pipelines.md](pipelines.md) — building custom pipelines, conditional phases,
  model routing
- [configuration.md](configuration.md#phasesyaml-reference) — `phases.yaml`
  field-level reference
- [troubleshooting.md](troubleshooting.md) — common failure modes and fixes
- [pipeline-metrics.md](pipeline-metrics.md) — evaluating pipeline quality
  with raki
