# AGENTS.md — SODA project context

## What is SODA

**Session-Orchestrated Development Agent** — a Go CLI/TUI that orchestrates AI coding sessions through a pipeline to implement tickets end-to-end.

Each pipeline phase runs in a fresh, sandboxed Claude Code session with structured output. State lives on disk. Context resets between phases.

## Architecture

```
soda (Go CLI/TUI)
  │
  │  For each phase, SODA:
  │  1. Renders prompt template + handoff artifacts
  │  2. Spawns Claude Code session (sandboxed via go-arapuca)
  │  3. Streams output to TUI
  │  4. Parses structured JSON response
  │  5. Writes artifact to .soda/<ticket>/
  │
  └── go-arapuca sandbox (library-based)
       ├── Landlock filesystem isolation
       ├── Network namespace (Unix sockets only)
       ├── cgroup resource limits (memory, CPU, PIDs)
       ├── seccomp syscall filter
       └── claude --print --bare --output-format stream-json --json-schema ...
```

## Pipeline phases

```
Triage → Plan → Implement → [Patch] → Verify → Review → Submit → [Follow-up] → Monitor
                    ↑           ↑        │         │
                    │           +- FAIL --+         │
                    +------ rework ─────────────────+ (max 2 cycles)
```

Phases in brackets are conditional:
- **Patch** (type: corrective) — only runs when verify FAILs, auto-routes via `reworkSignal`
- **Follow-up** (type: post-submit) — only runs when review verdict is `pass-with-follow-ups`

| Phase | Purpose | Tools | Timeout | Model |
|-------|---------|-------|---------|-------|
| Triage | Classify ticket, identify repo/files/complexity, route pipeline | Read-only | 3m | global |
| Plan | Design approach, break into atomic tasks (skippable if plan exists) | Read-only | 8m | global |
| Implement | Write code, run tests, commit | Full | 25m | global |
| Patch | Targeted fixes after verify FAIL (corrective, skipped in forward pass) | Full | 8m | claude-sonnet-4-6 |
| Verify | Run tests, check acceptance criteria, review code | Read + Bash | 8m | global |
| Review | Parallel specialist review (configurable per-reviewer models) | Read + Bash | 12m | global |
| Submit | Push branch, create PR/MR | git + gh/glab | 3m | global |
| Follow-up | Create tickets from minor review findings (post-submit, best-effort) | Bash(gh:*) | 3m | global |
| Monitor | Poll PR, respond to review comments, fix CI, auto-rebase | Full | 10m/round | global |

Phase definitions, tools, timeouts, and retry policies are in `phases.yaml`. Output schemas are generated from Go structs in `schemas/` via `go generate ./schemas/...` and resolved automatically at pipeline load time. Per-phase model overrides are supported via the `model` field on `PhaseConfig`.

### Rework and corrective routing

The engine uses a unified `reworkSignal` type (with `source` and `target` fields) for all rework routing. Two routing paths exist:

**Review rework** (review → implement):

| Verdict | Condition | Action |
|---------|-----------|--------|
| `pass` | No findings | Proceed to submit |
| `pass-with-follow-ups` | Minor findings only | Proceed to submit, follow-up creates tickets |
| `rework` | Any critical or major findings | Route back to implement |

At max rework cycles (default 2), if only minor findings remain, the verdict is downgraded to `pass-with-follow-ups` and the pipeline proceeds. If critical/major findings remain, the pipeline stops with `PhaseGateError`.

**Corrective routing** (verify → patch):

When verify FAILs and the verify phase has a `corrective` config block:
1. Engine returns `reworkSignal{source: "verify", target: "patch"}`
2. Patch phase runs with verify feedback (targeted fixes only)
3. Verify re-runs after patch
4. On regression (new failures introduced) → immediate escalation
5. On exhaustion (`max_attempts` reached) → check `on_exhausted` policy:
   - `stop` (default): pipeline stops
   - `escalate`: route to full implement (one-shot, guarded by `EscalatedFromPatch` flag)
   - `retry`: allow one extra patch attempt

Feedback injection is config-driven via `feedback_from` on PhaseConfig — no hardcoded phase names.

Cycle counters (`ReworkCycles`, `PatchCycles`) are independent and persisted in `meta.json`.

### Monitor phase

After PR submission, the monitor phase polls for activity and responds:

1. **Poll cycle**: check PR status (approved/merged/closed), new comments, CI status, merge conflicts
2. **Comment classification**: each comment is classified (code_change, question, nit, approval, dismissal, bot, self) with authority checks via CODEOWNERS
3. **Response execution**: fix sessions (code changes) vs reply-only sessions (questions) with different tool sets
4. **Termination**: the phase completes when the PR is approved/merged, max response rounds are reached, or the max polling duration expires

Configuration (in `phases.yaml`):
- `polling.initial_interval`: time between polls (default 2m, escalates to `max_interval` after `escalate_after`)
- `polling.max_response_rounds`: max Claude sessions for comment responses (default 3) — counts fix + reply rounds combined
- `polling.max_duration`: total monitor phase wall-clock limit (default 4h)
- `timeout`: per-response session timeout (default 10m)

Monitor requires `self_user` in config to distinguish self-authored comments from external ones. Without it, the monitor cannot classify comments and falls back to a stub.

Monitor profiles (`conservative`, `smart`, `aggressive`) control auto-rebase, nit auto-fix, and response to non-authoritative comments.

### Notification hooks

On pipeline completion (success or failure), the engine can fire webhook and/or script notifications. Both hooks are best-effort: failures are logged as events (`notify_success` / `notify_failed`) but do not affect the pipeline's exit status.

**Configuration** (in `soda.yaml`):
```yaml
notify:
  webhook:
    url: https://hooks.example.com/soda
    headers:
      Authorization: "Bearer token"
  script:
    command: "./scripts/on-complete.sh"
```

The webhook receives an HTTP POST with a JSON `PipelineResult` payload containing ticket, status, error, total cost, duration, and per-phase details. The script receives the same JSON on stdin.

`soda validate` checks notify config for obvious errors (empty URL, non-HTTP scheme, empty command).

### Spec/plan extraction

Triage can detect existing specs and plans from ticket comments (GitHub) or structured fields (Jira). When a reviewed plan is found, triage sets `skip_plan: true` and the plan phase is skipped — the existing plan is injected as the plan artifact directly.

**GitHub:** Configure comment markers in `soda.yaml`:
```yaml
github:
  fetch_comments: true
  spec:
    start_marker: "<!-- spec:start -->"
    end_marker: "<!-- spec:end -->"
  plan:
    start_marker: "<!-- plan:start -->"
    end_marker: "<!-- plan:end -->"
```

**Jira:** Configure extraction via fields and markers:
```yaml
jira:
  extraction:
    spec:
      start_marker: "<!-- spec:start -->"
      end_marker: "<!-- spec:end -->"
    plan:
      start_marker: "<!-- plan:start -->"
      end_marker: "<!-- plan:end -->"
    spec_field: customfield_10100
    plan_field: customfield_10101
```

**Issue labels:** Use `spec ready` (has reviewed spec) and `plan ready` (has reviewed spec + plan) to signal readiness. Triage uses these as hints for routing.

### Worktree-first execution

The pipeline creates a worktree **before any phase runs**. All phases — including triage and plan — execute inside the worktree, not the main checkout. This ensures:

- Triage reads the same code that implement will modify
- No dirty state or conflicts with other work in the main checkout
- Consistent WorkDir across all phases
- Enforces "never work on main" convention

Worktree path: `.worktrees/soda/<ticket-key>`. Cleaned up only on explicit `soda clean` or after PR merge — never automatically on failure (human may want to inspect). `soda clean` preserves session data (`.soda/<ticket>/`) for raki metrics; use `--purge` to remove everything.

## Project structure

```
soda/
├── cmd/
│   ├── soda/                      # Cobra CLI entrypoint
│   │   ├── main.go                # Root command, config loading, embedded content
│   │   ├── run.go                 # soda run command + TUI integration
│   │   ├── init.go                # soda init (auto-detect project, generate config)
│   │   ├── plugin.go              # soda plugin install/uninstall
│   │   ├── status.go              # soda status
│   │   ├── sessions.go            # soda sessions
│   │   ├── history.go             # soda history
│   │   ├── clean.go               # soda clean
│   │   ├── render.go              # soda render-prompt
│   │   ├── spec.go                # soda spec (guided ticket creation)
│   │   ├── pipelines.go           # soda pipelines (list available)
│   │   ├── version.go             # soda version
│   │   └── embeds/                # go:embed content
│   │       ├── phases.yaml        # Default pipeline config
│   │       ├── pipelines/          # Named pipeline definitions (quick-fix, docs-only)
│   │       ├── prompts/           # Phase prompt templates
│   │       └── claude-code/       # Claude Code commands, skills, agents
│   ├── schemagen/main.go          # JSON schema generator
│   └── tui-demo/main.go           # TUI demo harness
├── internal/
│   ├── claude/                    # Claude Code CLI integration
│   │   ├── args.go                # CLI argument builder
│   │   ├── runner.go              # Stream + parse Claude CLI
│   │   ├── parser.go              # JSON response parser
│   │   ├── types.go               # Response types
│   │   └── errors.go              # Parse/transient error types
│   ├── config/config.go           # YAML config loading
│   ├── detect/detect.go           # Project stack auto-detection
│   ├── git/worktree.go            # Worktree management, diff, rebase
│   ├── pipeline/
│   │   ├── engine.go              # Phase loop, rework routing, corrective routing
│   │   ├── errors.go              # reworkSignal, PhaseGateError, BudgetExceededError
│   │   ├── feedback.go             # Rework feedback construction
│   │   ├── gate.go                # Phase gating and rework routing
│   │   ├── guardrails.go          # Retry logic and budget enforcement
│   │   ├── review.go              # Parallel review orchestration
│   │   ├── siblings.go            # Sibling-function context injection
│   │   ├── discovery.go           # Named pipeline discovery
│   │   ├── events.go              # Structured event log
│   │   ├── phase.go               # PhaseConfig, CorrectiveConfig, ReviewerConfig
│   │   ├── prompt.go              # PromptData, ReworkFeedback, template rendering
│   │   ├── state.go               # Disk state, locking, atomic writes
│   │   ├── meta.go                # PipelineMeta (cycles, costs, flags)
│   │   ├── monitor.go             # MonitorState, PRPoller interface
│   │   ├── monitor_poll.go        # Monitor polling loop, response execution
│   │   ├── monitor_classifier.go  # Comment classification
│   │   ├── monitor_authority.go   # CODEOWNERS authority resolution
│   │   ├── monitor_profile.go     # Monitor behavior profiles
│   │   ├── notify.go              # Webhook + script notification hooks
│   │   ├── github_poller.go       # GitHub PR poller via gh CLI
│   │   ├── history.go             # Session history queries
│   │   ├── atomic.go              # Atomic file writes
│   │   └── lock.go                # flock-based locking
│   ├── progress/
│   │   ├── progress.go            # CLI progress display
│   │   └── summary.go             # Phase summary formatting
│   ├── runner/
│   │   ├── runner.go              # Agent-agnostic runner interface
│   │   ├── claude.go              # Claude Code runner implementation
│   │   ├── errors.go              # TransientError, ParseError, SemanticError
│   │   └── mock.go                # Mock runner for testing
│   ├── sandbox/
│   │   ├── runner.go              # go-arapuca sandbox execution
│   │   ├── runner_nocgo.go        # Stub when CGO disabled
│   │   ├── config.go              # Sandbox configuration
│   │   ├── resolve.go             # Binary resolution
│   │   ├── helpers.go             # Sandbox helpers
│   │   └── errors.go              # Sandbox error types
│   ├── ticket/
│   │   ├── source.go              # Source interface
│   │   ├── ticket.go              # Ticket types
│   │   ├── github.go              # GitHub Issues via gh CLI
│   │   ├── jira.go                # Jira via wtmcp CLI
│   │   ├── mcp.go                 # MCP ticket source
│   │   └── extract.go             # Spec/plan extraction from comments
│   ├── transcript/
│   │   └── transcript.go          # Agent-agnostic transcript types (Level, Entry)
│   └── tui/
│       ├── app.go                 # Bubbletea main model
│       ├── ticket.go              # Ticket display widget
│       ├── pipeline.go            # Phase progress widget
│       ├── output.go              # Live streaming output
│       ├── stats.go               # Cost/tokens/elapsed
│       ├── keys.go                # Keybinding display
│       ├── sessions.go            # Session list view
│       └── styles.go              # Lipgloss styles
├── schemas/                       # Structured output schemas
│   ├── gen.go                     # go:generate directive
│   ├── generated.go               # Auto-generated JSON schemas
│   ├── lookup.go                  # Phase → schema mapping
│   ├── triage.go, plan.go, ...    # Per-phase Go struct definitions
│   ├── patch.go                   # PatchOutput, FixResult
│   └── followup.go               # FollowUpOutput, FollowUpAction
├── phases.yaml                    # Root pipeline config (overrides embedded)
├── config.example.yaml            # Example user config
├── packaging/
│   └── rpm/
│       ├── soda.spec              # Full RPM spec (CGO + arapuca sandbox)
│       ├── soda-minimal.spec      # Static RPM spec (no CGO, no sandbox)
│       └── build-srpm.sh          # Build SRPMs with vendor tarball
├── go.mod
└── go.sum
```

## Tech stack

- **Language**: Go 1.25
- **TUI**: bubbletea + lipgloss + bubbles
- **CLI**: cobra
- **Config**: YAML (`gopkg.in/yaml.v3`)
- **Templates**: Go `text/template` with `go:embed`
- **Sandbox**: go-arapuca (library-based, Landlock + seccomp + cgroups)
- **Agent**: Claude Code CLI (`claude --print --bare`)
- **Runner abstraction**: `internal/runner/` decouples engine from Claude CLI specifics
- **Packaging**: RPM specs for Fedora (COPR: `decko/soda`); `soda` (with sandbox) and `soda-minimal` (without)

## Claude Code CLI flags (critical)

Every phase invokes Claude Code with these flags:

```
claude --print --bare --verbose --output-format stream-json --json-schema <schema> \
       --system-prompt-file <prompt> --model <model> \
       [--max-budget-usd <budget>] --permission-mode bypassPermissions
```

| Flag | Why |
|------|-----|
| `--print` | Non-interactive, exit after response |
| `--bare` | No auto-discovery of CLAUDE.md, plugins, hooks, MCP. SODA controls the full context window. |
| `--verbose` | Required by Claude CLI ≥2.1.128 when using `--print` with `--output-format stream-json` |
| `--output-format stream-json` | JSONL per event (enables transcript capture); last line is the result envelope with `structured_output`, `total_cost_usd`, `usage`, `duration_ms` |
| `--json-schema` | Enforce structured output. CLI validates against schema. No regex parsing needed. |
| `--system-prompt-file` | Phase role + context as system prompt from file |
| `--max-budget-usd` | Hard cost cap per phase (omitted when no budget configured) |
| `--permission-mode bypassPermissions` | No interactive permission prompts (essential for unattended execution) |

Per-phase tool scoping via `--allowed-tools`:
- Triage/Plan: `Read Glob Grep Bash(git:*) Bash(ls:*)`
- Implement/Patch: `Read Write Edit Glob Grep Bash`
- Verify: `Read Glob Grep Bash`
- Submit: `Bash(git:*) Bash(gh:*) Bash(glab:*)`
- Follow-up: `Bash(gh:*)`
- Monitor fix sessions: `Read Write Edit Glob Grep Bash`
- Monitor reply-only: `Read Glob Grep Bash(git log:*) Bash(git diff:*) Bash(git show:*) Bash(git status:*) Bash(go test:*) Bash(ls:*)`

## Error handling

Three error categories with different retry strategies (types in `internal/runner/errors.go`):

| Category | Example | Action | Default retries |
|----------|---------|--------|----------------|
| Transient | API timeout, rate limit | Retry same prompt, exponential backoff | 2 |
| Parse | Output doesn't match JSON schema | Retry with error message appended | 1 |
| Semantic | Plan has no tasks, verify finds no tests | Retry with corrective feedback | 1 (0 for implement) |

Additional engine-level errors:
- `PhaseGateError` — domain gating failed (triage: not automatable, verify: FAIL, review: max rework)
- `BudgetExceededError` — per-ticket or per-phase budget exceeded
- `DependencyNotMetError` — required upstream phase not completed
- `reworkSignal` — internal sentinel (not terminal) for rework routing

## State on disk

```
.soda/<ticket>/
├── meta.json              # ticket, worktree, branch, costs, cycles, flags
├── lock                   # flock-based, contains PID + timestamp
├── triage.json            # structured output (from --json-schema)
├── plan.json
├── implement.json
├── patch.json             # corrective phase output (if ran)
├── verify.json
├── review.json            # merged review output
├── submit.json
├── follow-up.json         # follow-up actions (if ran)
├── monitor.json           # monitor response output (latest round)
├── monitor_state.json     # monitor polling state (poll count, rounds, last comment ID)
├── events.jsonl           # structured event log
└── logs/
    ├── triage_prompt.md
    ├── triage_response.md
    ├── triage_transcript.json  # agent transcript (when capture enabled)
    ├── review/
    │   ├── prompt_go-specialist.md
    │   └── prompt_ai-harness.md
    ├── monitor/
    │   ├── response_0_prompt.md
    │   ├── response_0_output.md
    │   ├── reply_0_prompt.md
    │   └── reply_0_output.md
    └── ...
```

Atomic writes: always write to `.tmp` then rename. Archive on re-run (`verify.json` → `verify.json.1`).

`meta.json` key fields:
- `complexity` — triage complexity band ("low", "medium", "high"); empty for sessions without triage
- `rework_cycles` — review rework counter
- `patch_cycles` — corrective patch counter
- `escalated_from_patch` — one-shot escalation flag
- `previous_failures` — criteria IDs for regression detection
- Per-phase `cumulative_cost` — survives across generations
- Per-phase `model_used` — the model that ran the phase (for routing validation)
- Per-phase `parse_attempts`, `parse_success_on_first` — structured output quality tracking

## Key design decisions

- **`--bare` mode**: eliminates context duplication (CLAUDE.md loaded twice) and saves 15-28K tokens per session. SODA inlines only what each phase needs.
- **Sandbox over advisory controls**: `--allowed-tools` is advisory (the model can ignore it). Landlock/seccomp/network namespaces are kernel-enforced. For unattended autonomous execution, enforcement beats advisory.
- **Disk state over in-memory**: crash recovery for free. Resume works by reading `.soda/<ticket>/`. No daemon needed.
- **Config-driven phases**: users can add, remove, or reorder phases via `phases.yaml`. Engine doesn't hardcode phase names.
- **Prompt overrides**: `~/.config/soda/prompts/<phase>.md` overrides embedded prompts without forking.
- **Root `phases.yaml` overrides embedded**: `resolvePhasesPath()` checks for a local `phases.yaml` in the working directory first, then falls back to the embedded copy.
- **Per-phase model override**: phases can specify their own model (e.g., patch uses Sonnet), enabling cost-aware model selection.
- **FeedbackFrom for generic feedback injection**: phases declare which upstream results provide rework feedback via `feedback_from` field, decoupled from hardcoded phase names.
- **Corrective vs rework routing**: two distinct loops — corrective (verify→patch, same-cycle) and rework (review→implement, full re-run) with separate cycle counters and a unified `reworkSignal` type.
- **Regression detection**: prevents patch from introducing new failures by comparing criteria between verify cycles.
- **Post-submit best-effort**: follow-up phase failures are swallowed, not terminal. Pipeline succeeds even if follow-up can't create tickets.
- **Agent-agnostic runner**: `internal/runner/` decouples the engine from Claude Code CLI specifics, enabling future backend swaps.
- **Code snippet injection for rework**: when review triggers rework, the engine reads ±5 lines around each critical/major finding's `file:line` and injects them as `CodeSnippet` on `EnrichedFinding`. This eliminates a retrieval gap — the rework implement session sees the exact code without spending tokens on tool calls to find it. Validated by raki: reduced rework cycles by 25% and cost by 17%.
- **Diff-scoped review**: review prompts include `git diff main...HEAD` so reviewers focus on changed code only. On rework cycles, prior findings are injected with exclusion instructions to prevent the whack-a-mole pattern (reviewer finds new issues in untouched code after each rework). Severity definitions (critical/major/minor) calibrate the reviewer's threshold. Validated by raki: medium-complexity first-pass rate went from 0% to 67%.
- **Agent-agnostic transcript types**: `TranscriptLevel` and `TranscriptEntry` live in `internal/transcript/`, not `internal/claude/`. The runner interface stays agent-agnostic — future backends (Pi, Opencode) can produce transcripts without importing Claude-specific code. `internal/claude/transcript.go` re-exports via type aliases for backward compatibility.

## Git workflow

- **NEVER commit directly on main.** Always use a feature branch.
- **Always work in worktrees**: `git worktree add .worktrees/<branch> -b <branch> main`
- **Worktree directory**: `.worktrees/soda/<ticket-key>/` (gitignored)
- **Branch naming**: `feat/<issue-slug>`, `fix/<issue-slug>`, `chore/<issue-slug>`, `soda/<ticket-key>` (pipeline-created)
- **One PR per issue.** Reference the issue in the PR title.
- **Push to origin**, PR against `main`.
- Only stage specific files with `git add <file>`, never `git add .` or `git add -A`.
- Do not force-push unless explicitly asked.
- Do not amend published commits.
- **Assisted-by**: add an `Assisted-by:` trailer at the end of the commit message naming the model used (e.g., `Assisted-by: Claude Opus 4.6`, `Assisted-by: GPT-4o`). One trailer per commit.
- **Squash merge format**: title is the PR title (under 70 chars), body is a concise summary of what changed (not the full list of individual commits), single `Assisted-by:` trailer at the end.
- After PR is merged, start fresh — never build on already-merged branches.
- **Pre-commit hooks**: run `./scripts/setup-hooks.sh` once to enable the `.githooks/pre-commit` hook (`gofmt -l` + `go vet` on staged `.go` files). Skip with `--no-verify` or `SKIP_HOOKS=1`. See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## Conventions

- **Formatting**: `gofmt` (standard Go formatting)
- **Linting**: `go vet` + `staticcheck` (`staticcheck` is CI-only; `gofmt` and `go vet` run locally via the pre-commit hook)
- **Pre-commit hook**: `.githooks/pre-commit` enforces `gofmt` and `go vet` on staged `.go` files. Setup: `./scripts/setup-hooks.sh`
- **Testing**: `go test ./...`
- **Building**: `CGO_ENABLED=0 go build -o soda ./cmd/soda` (CGO disabled — `go-arapuca` lib requires Git LFS which is not available in all environments)
- **Code generation**: `go generate ./schemas/...` regenerates JSON schemas from Go struct types. Run after modifying structs in `schemas/`.
- **No single-char variables**: use descriptive names in loops and closures
- **Errors**: wrap with `fmt.Errorf("context: %w", err)`, never discard
- **Interfaces**: define at the consumer, not the producer. Keep minimal.

## Gotchas

1. **`--bare` conflicts with CLAUDE.md instructions**: AGENTS.md may contain "don't start coding until asked" — with `--bare`, this is not loaded. But if you inline AGENTS.md sections into prompts, be careful not to include conflicting instructions.
2. **Claude Code CLI output format is not a stable API**: wrap all response parsing in a dedicated parser with tests against fixture files. Degrade gracefully (show "N/A" for cost) rather than crash.
3. **`--json-schema` may trigger tool use**: even with `--bare`, Claude may try to explore the codebase before answering. For pure classification phases (triage), consider `--tools ""` to disable all tools.
4. **Sandbox requires CGO**: The sandbox runner uses `go-arapuca` which requires `cgo` (build tag `//go:build cgo`). When building with `CGO_ENABLED=0`, `runner_nocgo.go` provides a stub that returns an error at runtime.
5. **Network namespace requires unprivileged user namespaces**: test with `unshare --user --net --map-current-user -- /bin/true`. If it fails, sandbox falls back to seccomp-only.
6. **File locks are per-machine, not cross-machine**: `flock` on `.soda/<ticket>/lock` prevents concurrent runs on the same host but not across machines.
7. **Lock files persist after clean exit**: `ReleaseLock()` releases the flock but does not delete the lock file (intentional — avoids TOCTOU race). `soda status` derives terminal status from phase state, not lock presence.
8. **Root `phases.yaml` overrides embedded**: a `phases.yaml` in the project root takes precedence over the compiled-in version. Changes to the embedded file require a rebuild; the root file takes effect immediately.
9. **Always run `soda` from the main repo checkout**: running from inside a worktree used to create nested worktrees (fixed in #156), but it's still good practice to run from the root.
10. **Monitor requires `self_user` config**: without `self_user` set, the monitor cannot distinguish self-authored comments from external ones and will not respond to PR comments.
11. **`max_cost_per_phase` is cumulative**: `CumulativeCost` on `PhaseState` accumulates across rework/patch generations, not just the current generation.
12. **Patch regression detection uses criterion text**: if acceptance criteria text changes between verify runs, regression detection may produce false negatives.
13. **CI git config isolation**: tests that create git repos must set `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_SYSTEM=/dev/null` (via `t.Setenv`) to prevent `url.*.insteadOf` rewrites on CI runners.
14. **`engine.go` is a merge conflict hotspot**: at ~2,800 lines with 60+ functions, every PR touching pipeline logic conflicts. Place new functions near related functions, not at the end. For new top-level concerns, extract to a separate file (see `review.go`, `feedback.go` decomposition plan).
15. **`DefaultConfig()` values are compiled in**: `internal/config/config.go` returns hardcoded defaults (model, limits, worktree dir). Changing defaults requires a rebuild — `soda.yaml` overrides are loaded at runtime, but the fallback values are baked into the binary.
16. **`ps.Cost` vs `ps.CumulativeCost`**: `Cost` resets per generation (zeroed in `MarkRunning`). `CumulativeCost` accumulates across rework/patch cycles. Budget enforcement (`checkPhaseBudget`) checks both: `MaxCostPerGeneration` against `Cost`, `MaxCostPerPhase` against `CumulativeCost`.
17. **`EventPhaseFailed` is terminal**: the TUI and history module treat it as a hard failure. Never emit it for non-fatal warnings (e.g., failed to read archived review result) — use a warning-level event string instead.
18. **Always use `exec.CommandContext`**: bare `exec.Command` in git/worktree operations can hang indefinitely. Use `exec.CommandContext(ctx, ...)` for any operation that could block (network, worktree removal).
19. **Token data persisted in meta.json**: `PhaseState` stores `TokensIn`, `TokensOut`, and `CacheTokensIn` alongside `Cost` and `DurationMs`. Token counts are accumulated per-generation (zeroed on re-run) and emitted in `phase_completed` events. Legacy sessions without token data will show zero values.
20. **`EnrichedFinding` wraps `schemas.ReviewFinding`**: defined in `prompt.go`, it adds `CodeSnippet` without changing the schema contract. Enrichment happens in `extractReviewFeedback()` only — the review phase still outputs plain `ReviewFinding`.
21. **Sandbox needs `GH_TOKEN` + `SSH_AUTH_SOCK` for submit/follow-up**: `gh` CLI authenticates via OS keyring, which is inaccessible inside the arapuca sandbox. `claudeEnv()` extracts the token from `gh auth token` on the host side and passes it as `GH_TOKEN`. The SSH agent socket directory is added to sandbox read paths for `git push`.
22. **`go mod download` never smudges LFS**: Go's module tooling uses `git archive` (even with `GOPROXY=direct`), which does not invoke git-lfs smudge filters. `libarapuca.a` in go-arapuca is an LFS pointer after `go mod download`. CI and local builds must fetch the real binary via the LFS batch API or direct download. See `sergio-correia/go-arapuca#1`.
23. **`gh api --slurp --jq` incompatibility**: newer `gh` CLI versions reject `--slurp` combined with `--jq`. Fixed in #391 — the GitHub poller now uses `--paginate --jq` without `--slurp`. If you see monitor warnings about this, rebuild the binary.
24. **`tabwriter.StripEscape` does NOT make content zero-width**: wrapping ANSI codes in `\xff` delimiters only prevents tab/newline interpretation inside the escaped segment — the bytes are still counted for column width. For ANSI-colored table output, compute column widths from plain text, pad before adding escape codes, and use `fmt.Sprintf` instead of tabwriter.
25. **Default implement timeout (15m) is too short for large tickets**: tickets with 7+ tasks routinely exceed 15 minutes. The root `phases.yaml` should use 25m for implement. The embedded default remains 15m for backward compatibility.
26. **Review budget can exhaust with 2+ rework cycles**: `CumulativeCost` accumulates across rework generations. With the default `max_cost_per_phase: $8.00`, two review cycles ($3-5 each) plus rework implement sessions can exceed the limit. Consider raising to $15 for review-heavy workflows or using `max_cost_per_generation` instead.
27. **`--verbose` required for stream-json with `--print`**: Claude CLI ≥2.1.128 requires `--verbose` when using `--print` with `--output-format stream-json`. Without it, the CLI exits with code 1 and $0 cost. The flag is always included in `BuildArgs`.
28. **Monitor ignores self-authored PR comments**: comments from the `self_user` are classified as `CommentSelfAuthored` and skipped. If you post review findings from the same account as `self_user`, the monitor won't respond. Post from a different account or use the rework pipeline instead.
29. **Monitor requires `respond_to_comments` in `phases.yaml`**: the `self_user` field in `soda.yaml` enables comment classification, but active comment response also requires `respond_to_comments: true` in the monitor phase's `polling` config in `phases.yaml`.
30. **PR reviews vs PR comments**: `gh pr review` posts to `/pulls/N/reviews`, which the monitor's GitHub poller does NOT poll. Only `/pulls/N/comments` (inline review comments) and `/issues/N/comments` (top-level conversation comments) are fetched. Post feedback as issue comments for the monitor to see it.
31. **`ParseResponse` uses JSONL reverse line scan**: with `--output-format stream-json`, `extractJSON` scans lines from the end to find the `{"type":"result"}` envelope. This is O(n) and handles fake result strings in tool output. The legacy backward brace scan (`extractJSONByDepth`) is the fallback for non-JSONL output.
32. **`CLOUD_ML_REGION=global` uses a different URL format**: the global Vertex endpoint is `https://aiplatform.googleapis.com/v1` (no region prefix), not `https://global-aiplatform.googleapis.com/v1`. The proxy constructs the upstream URL based on this — regional endpoints use `https://<region>-aiplatform.googleapis.com/v1`. Fixed in PR #520.
33. **`%gobuild` and `CGO_ENABLED=0` are incompatible**: the Fedora `%gobuild` macro hardcodes `-linkmode=external` which requires CGO. For the `soda-minimal` spec (no sandbox), use raw `go build -trimpath` instead. The full `soda` spec (CGO=1) can use `%gobuild` but currently uses raw `go build` to avoid `%gometa` shell injection issues.
34. **RPM specs use vendored builds**: `go mod vendor` + tarball (`Source1`), not `go mod download` during build. The `build-srpm.sh` script generates both the source and vendor tarballs. Version is sed'd into the spec before SRPM creation because `--define` doesn't survive COPR rebuilds.
35. **`arapuca-devel` is in Fedora**: no need for LFS fetch or cargo build of libarapuca. Just `BuildRequires: arapuca-devel` and pkg-config handles linking. This replaced the entire LFS complexity from the original spec.
36. **Schema changes to LLM output types are high risk**: adding fields to structs used in `--json-schema` (e.g., `ReviewFinding`) can cause parse failures if the model doesn't produce valid values. Always make new fields optional (`omitempty`), validate on the consumer side with defaults, and use "should" not "must" in prompt instructions. Monitor parse error rate for 10 sessions after deployment.
37. **Global config path is `~/.config/soda/soda.yaml`**: renamed from `config.yaml` in #537 for consistency with the project-local `soda.yaml`. `config.DefaultPath()` returns this. The `--config` flag default shown in `--help` is evaluated at runtime per user.

## Raki evaluation framework

[Raki](https://github.com/decko/raki) evaluates pipeline quality across completed sessions. The manifest at `raki.yaml` points at `.soda/` for session data.

### Metrics

| Metric | What it measures | Baseline (n=76) |
|--------|-----------------|-----------------|
| `first_pass_verify_rate` | Sessions passing verify without corrective patch | 1.0 |
| `first_pass_success_rate` | Sessions with no rework cycles | 0.38 (0.67 for v0.5.0 sessions) |
| `rework_cycles` | Mean review→implement rework loops per session | 0.9 |
| `review_severity_distribution` | Finding counts by severity (critical/major/minor) | — |
| `cost_efficiency` | Mean USD per session | $8.98 |
| `knowledge_retrieval_miss_rate` | Fraction of rework findings caused by missing context | 0.97 |
| `phase_execution_time` | Mean wall-clock seconds per session | 1581s |
| `token_efficiency` | Mean tokens per phase | 14,856 |
| `cost_by_complexity` | Mean/median/total USD per triage complexity band | available via `soda cost --by-complexity` |

### Interpretation

- **`knowledge_retrieval_miss_rate = 1.0`** means the agent CAN fix every bug — rework only happens when it didn't SEE the relevant code. Improvements that inject more code into prompts (snippet injection, diff context) directly reduce rework. Agent reasoning improvements are not needed.
- **Cost model**: sessions cost $5–$11 USD. Rework adds ~$3/cycle. A 2-rework session (~$11) costs ~2× a clean session (~$5.50).
- **128 minor findings** across 11 sessions are signal for the follow-up phase (which creates tickets), not noise — the engine already filters to critical/major for rework feedback.

### Running evaluation

```bash
raki run -m raki.yaml              # operational + knowledge metrics, no API key needed
raki run -m raki.yaml --docs-path . --judge --judge-provider vertex-anthropic --judge-model claude-sonnet-4-6  # includes LLM-judged retrieval quality
raki validate -m raki.yaml         # check manifest without running
raki trends                        # show metric trajectories over time
```

Reports are written to `results/`.

**All development should be done using soda itself.** Run `soda run <ticket>` to implement issues through the pipeline. Manual implementation is acceptable when the pipeline is broken, the work is on soda's own infrastructure, or triage gates the ticket as "not automatable."

### Spec and plan workflow

For non-trivial tickets, the recommended workflow is:

1. **Write a spec** — post it on the issue body (not committed to repo)
2. **Get specialist reviews** — dispatch Go Specialist + AI Harness + SRE agents in parallel to review the spec
3. **Incorporate feedback** — update the spec on the issue based on review findings
4. **Write a plan** — post it on the issue with `<!-- soda:plan -->` markers
5. **Break into sub-issues** — for large features, create a milestone and break into sub-issues with dependencies
6. **Label the issue** — `spec ready` or `plan ready`
7. **Run soda** — `soda run <ticket>`. Triage detects the existing plan and skips the plan phase.

For small/trivial tickets, skip the spec/plan and let soda handle everything end-to-end.

### Test-driven development

Every implementation must follow TDD:

1. **Write tests first** — before writing any implementation code, write failing tests that cover the acceptance criteria.
2. **Write functional tests** — test real behavior, not mocks of internals. Tests should exercise the public API of the package.
3. **Run tests, see them fail** — confirm the tests fail for the right reason before implementing.
4. **Implement** — write the minimum code to make the tests pass.
5. **Refactor** — clean up while tests stay green.

Do NOT write tests after implementation. Do NOT skip the "see it fail" step.

## Specialist reviews

Every output must be reviewed before moving to the next step. Reviews run as **subagents** to minimize context cost in the parent session (~20K total, not ~60K).

### When to review

| Ticket size | Review requirement |
|-------------|-------------------|
| Small (< 50K budget) | Skip reviews — the fix is trivial |
| Medium (50-70K) | Review after implementation (one round) |
| Large (> 70K) | Review after spec AND after implementation |

### How to review

Dispatch subagents **in parallel** using the Agent tool:

1. **Go Specialist Agent**: review for Go idioms, error handling, interface design, test quality, performance, and correctness.
2. **AI Harness Agent**: review for prompt engineering, context budget impact, Claude Code CLI integration, sandbox compatibility, and structured output reliability.
3. **SRE Agent** (for operational features): review for budget safety, observability, timeout cascading, circuit breakers, failure modes.
4. **AI/ML Agent** (for LLM-facing features): review for model reliability, cost optimization, prompt effectiveness.

Each subagent receives:
- The code or spec to review (keep concise — send only the relevant files, not the whole repo)
- Specific review questions (not "review everything")

Each reviewer should be critical and flag concrete issues, not give generic approval.

### Token cost of reviews

Subagent reviews cost ~20K in the parent session (2 dispatches + 2 summaries). The heavy analysis happens in the subagent's own context window, not in the parent.

Budget formula already includes this:
```
estimated = (read_lines × 5) + (write_lines × 8) + (packages × 5000) + 20000 (tools) + 20000 (reviews)
```

### After reviews

- Fix all critical and major issues before proceeding.
- If remaining budget is tight after fixes, defer minor suggestions to a follow-up issue.
- Do NOT skip reviews on medium/large tickets to save tokens.

## Ticket sizing

Each ticket targets an **80K token working budget** (out of 200K context, after system prompt, tool overhead, and safety buffer).

### Estimation tools

| Tool | When | What it shows |
|------|------|---------------|
| `soda run <ticket> --estimate` | Before running | Per-phase estimated tokens, warn markers |
| `token_budget.warn_tokens` in `soda.yaml` | During runs | Runtime warnings when prompts are large |
| `soda history <ticket> --detail` | After running | Actual token counts per phase |
| `soda log <ticket>` | After running | Token budget events in the event stream |

Run `soda run <ticket> --estimate` before every first run to sanity-check prompt sizes.

### Estimate three factors

| Factor | How to estimate | Token cost |
|--------|----------------|-----------|
| Read surface | Lines of existing code the session must read | ~5 tokens/line |
| Write surface | Lines of new code to produce (including tests) | ~8 tokens/line |
| Integration points | Number of existing packages to wire together | ~5K per package |

Quick formula:
```
estimated = (read_lines × 5) + (write_lines × 8) + (packages × 5000) + 20000 (tools) + 20000 (reviews)
```

### Decision

| Estimate | Action |
|----------|--------|
| < 50K | Ship as one issue |
| 50-70K | One issue, add explicit "do NOT read" list to save tokens |
| 70-80K | Split unless the work is truly indivisible |
| > 80K | Must split |

### Split when

- Multiple independent packages to create (each package is a natural boundary)
- Read surface > 25K tokens (~5K lines)
- More than 3 integration points (wiring 4+ packages)
- Mixed read-heavy and write-heavy work
- High test failure risk (complex wiring, external tools)

### Don't split when

- The work is tightly coupled (splitting creates stubs)
- Read surface is small but write surface is large (greenfield is cheap)
- Under 50K (splitting adds overhead for no benefit)

### Ticket format

Every ticket should include:

```markdown
## Context to read
- <file> (<what to look at>, ~N lines)

## Do NOT read
- <package> (reason)

## Estimated token budget
- Read: ~NK
- Write: ~NK
- Tools: ~15K
- Buffer: ~30K
- Total: ~NK / 80K available
```

## Triaging issues

Issues labeled `triage needed` require sizing and scope assessment before implementation.

When asked to triage an issue:

1. Read the issue description and understand the requirements
2. Identify files to read and packages to integrate (scan the codebase)
3. Estimate read surface, write surface, and integration points
4. Apply the token budget formula from "Ticket sizing" above
5. Update the issue with:
   - `## Context to read` and `## Do NOT read` sections
   - `## Estimated token budget` with the breakdown
   - Split proposal if estimate exceeds 70K
   - Dependencies on other issues
6. Remove the `triage needed` label once complete

If the issue lacks acceptance criteria, add them. If the scope is ambiguous, list the open questions in the issue and ask the maintainer.

## Follow-up issues

When discovering bugs, tech debt, or improvement opportunities during a task, file them as separate GitHub issues with the `triage-needed` label. Do not fix them inline — stay focused on the current ticket's scope.

## New issue checklist

When creating a new issue, check whether any existing docs need updating as part of the work:

- Does the change affect `AGENTS.md`? (architecture, conventions, project structure, gotchas)
- Does it add/change CLI commands or flags? (update `config.example.yaml`, help text)
- Does it change phase behavior? (update `phases.yaml` docs, prompt templates)
- Does it affect the state format? (update "State on disk" section in `AGENTS.md`)

If yes, include a "Docs to update" section in the issue body listing the files that need changes.

## CLI commands

| Command | Purpose |
|---------|---------|
| `soda init [--yes] [--force] [--dry-run] [--phases] [--no-gitignore]` | Auto-detect project stack and generate `soda.yaml` |
| `soda run <ticket>` | Run the pipeline for a ticket |
| `soda run <ticket> --pipeline <name>` | Run with a named pipeline (quick-fix, docs-only, or custom) |
| `soda run <ticket> --from <phase>` | Resume from a specific phase (`last` auto-resolves to last running/failed) |
| `soda run <ticket> --dry-run` | Render prompts without executing |
| `soda run <ticket> --estimate` | Dry-run with per-phase token estimates |
| `soda run <ticket> --mode checkpoint` | Pause after each phase for confirmation |
| `soda status` | Show active and recent pipelines |
| `soda history <ticket>` | Show phase details for a ticket |
| `soda history <ticket> --detail` | Show full structured JSON output per phase |
| `soda history <ticket> --phase <name>` | Drill down into a specific phase |
| `soda sessions` | List all previous pipeline runs |
| `soda clean <ticket>` | Remove worktree and branches, preserve session data |
| `soda clean <ticket> --purge` | Remove everything including session data (`.soda/<ticket>/`) |
| `soda clean <ticket> --force` | Force-remove dirty worktrees and delete remote branches |
| `soda clean --all [--dry-run]` | Clean all worktrees/branches, preserve session data |
| `soda clean --all --purge` | Clean everything including all session data |
| `soda log <ticket>` | Print formatted pipeline events |
| `soda log <ticket> -f` | Tail events in real-time (poll-based follow) |
| `soda validate` | Check config, phases, and prompts for errors without running |
| `soda cost` | Show cumulative cost breakdown across all sessions |
| `soda cost --by-complexity` | Show cost breakdown grouped by triage complexity band |
| `soda plugin install [--global] [--force]` | Install the SODA Claude Code plugin |
| `soda plugin uninstall [--global]` | Remove the SODA Claude Code plugin |
| `soda spec <description>` | Generate a ticket specification from a short description |
| `soda spec --from-file <path>` | Generate a spec from a longer description file |
| `soda pipelines` | List available named pipelines |
| `soda render-prompt --phase <phase> --ticket <key>` | Render a phase prompt template for debugging |
| `soda version` | Show version |

## What NOT to do

- Do not hardcode project-specific references (repo names, Jira projects, ticket keys)
- Do not put business logic in the TUI — it's a view layer over engine events
- Do not build the TUI and engine simultaneously — get headless working first (`--no-tui`)
