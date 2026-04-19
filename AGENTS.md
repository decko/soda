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
  │  2. Writes SandboxRunConfig JSON
  │  3. Spawns sandboxed Claude Code session
  │  4. Streams output to TUI
  │  5. Parses structured JSON response
  │  6. Writes artifact to .soda/<ticket>/
  │
  └── agent-node sandbox-run (agentic-orchestrator)
       ├── Landlock filesystem isolation
       ├── Network namespace (Unix sockets only)
       ├── cgroup resource limits (memory, CPU, PIDs)
       ├── seccomp syscall filter
       └── claude --print --bare --output-format json --json-schema ...
```

## Pipeline phases

```
Triage → Plan → Implement → Verify → Review → Submit → Monitor
                    ↑                    │
                    └── rework ──────────┘ (max 2 cycles)
```

| Phase | Purpose | Tools | Timeout |
|-------|---------|-------|---------|
| Triage | Classify ticket, identify repo/files/complexity, route pipeline | Read-only | 3m |
| Plan | Design approach, break into atomic tasks (skippable if plan exists) | Read-only | 8m |
| Implement | Write code, run tests, commit | Full | 15m |
| Verify | Run tests, check acceptance criteria, review code | Read + Bash | 8m |
| Review | Parallel specialist review (Go + AI harness) | Read + Bash | 12m |
| Submit | Push branch, create PR/MR | git + gh/glab | 3m |
| Monitor | Poll PR, respond to review comments, fix CI, auto-rebase | Full | 10m/round |

Phase definitions, tools, timeouts, and retry policies are in `phases.yaml`. Output schemas are generated from Go structs in `schemas/` via `go generate ./schemas/...` and resolved automatically at pipeline load time.

### Monitor phase

After PR submission, the monitor phase polls for activity and responds:

1. **Poll cycle**: check PR status (approved/merged/closed), new comments, CI status, merge conflicts
2. **Comment classification**: each comment is classified (code_change, question, nit, approval, dismissal, bot, self) with authority checks via CODEOWNERS
3. **Response execution**: when actionable comments are found, a Claude session runs with the monitor prompt to apply fixes, reply to comments, run tests, and push changes
4. **Termination**: the phase completes when the PR is approved/merged, max response rounds are reached, or the max polling duration expires

Configuration (in `phases.yaml`):
- `polling.initial_interval`: time between polls (default 2m, escalates to `max_interval` after `escalate_after`)
- `polling.max_response_rounds`: max Claude sessions for comment responses (default 3)
- `polling.max_duration`: total monitor phase wall-clock limit (default 4h)
- `timeout`: per-response session timeout (default 10m)

Monitor profiles (`conservative`, `smart`, `aggressive`) control auto-rebase, nit auto-fix, and response to non-authoritative comments.

### Spec/plan extraction

Triage can detect existing specs and plans from ticket comments (GitHub) or structured fields (Jira). When a reviewed plan is found, triage sets `skip_plan: true` and the plan phase is skipped — the existing plan is injected as the plan artifact directly.

**GitHub:** Configure comment markers in `soda.yaml`:
```yaml
github:
  fetch_comments: true
  spec:
    source: comment_marker
    marker: "<!-- soda:spec -->"
  plan:
    source: comment_marker
    marker: "<!-- soda:plan -->"
```

**Jira:** Configure extraction strategies:
```yaml
jira:
  spec:
    source: epic_description
  plan:
    source: subtask
    match: "Implementation Plan"
```

**Issue labels:** Use `spec ready` (has reviewed spec) and `plan ready` (has reviewed spec + plan) to signal readiness. Triage uses these as hints for routing.

### Review rework routing

After the review phase, findings are classified by severity:

| Verdict | Condition | Action |
|---------|-----------|--------|
| `pass` | No findings | Proceed to submit |
| `pass-with-follow-ups` | Minor findings only | Proceed to submit, queue follow-up |
| `rework` | Any critical or major findings | Route back to implement |

Rework routing: when review verdict is "rework", the engine automatically routes back to implement with the review findings injected into the prompt. The cycle is: implement → verify → review → (check verdict). Max rework cycles default to 2; after that, the engine stops with a `PhaseGateError` for human intervention. The cycle count is persisted in `meta.json` as `rework_cycles`.

### Worktree-first execution

The pipeline creates a worktree **before any phase runs**. All phases — including triage and plan — execute inside the worktree, not the main checkout. This ensures:

- Triage reads the same code that implement will modify
- No dirty state or conflicts with other work in the main checkout
- Consistent WorkDir across all phases
- Enforces "never work on main" convention

The worktree is cleaned up only on explicit `soda clean` or after PR merge — never automatically on failure (human may want to inspect).

## Project structure

```
soda/
├── cmd/soda/main.go           # Cobra CLI entrypoint
├── internal/
│   ├── config/config.go       # YAML config loading
│   ├── ticket/                # Pluggable ticket sources
│   │   ├── source.go          # Source interface
│   │   ├── jira.go            # Jira via wtmcp CLI
│   │   └── github.go          # GitHub via gh CLI
│   ├── pipeline/
│   │   ├── engine.go          # Phase loop, error handling, events
│   │   ├── phase.go           # Phase interface + config loading
│   │   ├── state.go           # Disk state, locking, atomic writes
│   │   └── phases/            # Phase-specific logic (if needed)
│   ├── sandbox/
│   │   └── runner.go          # Wraps agent-node sandbox-run
│   ├── claude/
│   │   └── command.go         # Claude Code CLI command builder
│   └── tui/
│       ├── app.go             # Bubbletea main model
│       ├── ticket.go          # Ticket display widget
│       ├── pipeline.go        # Phase progress widget
│       ├── output.go          # Live streaming output
│       ├── stats.go           # Cost/tokens/elapsed
│       ├── picker.go          # Interactive ticket picker
│       └── styles.go          # Lipgloss styles
├── prompts/                   # Phase prompt templates (go:embed)
├── schemas/                   # Structured output schemas (Go structs + generated JSON schemas)
├── phases.yaml                # Phase pipeline configuration
├── config.example.yaml        # Example user config
├── go.mod
└── go.sum
```

## Tech stack

- **Language**: Go 1.25
- **TUI**: bubbletea + lipgloss + bubbles
- **CLI**: cobra
- **Config**: YAML (viper or raw `gopkg.in/yaml.v3`)
- **Templates**: Go `text/template` with `go:embed`
- **Sandbox**: agentic-orchestrator (`agent-node sandbox-run`)
- **Agent**: Claude Code CLI (`claude --print --bare`)

## Claude Code CLI flags (critical)

Every phase invokes Claude Code with these flags:

```
claude --print --bare --output-format json --json-schema <schema> \
       --system-prompt-file <prompt> --model <model> \
       --max-budget-usd <budget> --permission-mode bypassPermissions
```

| Flag | Why |
|------|-----|
| `--print` | Non-interactive, exit after response |
| `--bare` | No auto-discovery of CLAUDE.md, plugins, hooks, MCP. SODA controls the full context window. |
| `--output-format json` | Structured response with `structured_output`, `total_cost_usd`, `usage`, `duration_ms` |
| `--json-schema` | Enforce structured output. CLI validates against schema. No regex parsing needed. |
| `--system-prompt-file` | Phase role + context as system prompt from file |
| `--max-budget-usd` | Hard cost cap per phase |
| `--permission-mode bypassPermissions` | No interactive permission prompts (essential for unattended execution) |

Per-phase tool scoping via `--allowed-tools`:
- Triage/Plan: `Read Glob Grep Bash(git:*) Bash(ls:*)`
- Implement: `Read Write Edit Glob Grep Bash`
- Verify: `Read Glob Grep Bash`
- Submit: `Bash(git:*) Bash(gh:*) Bash(glab:*)`
- Monitor: `Read Write Edit Glob Grep Bash` (per response session)

## Error handling

Three error categories with different retry strategies:

| Category | Example | Action | Default retries |
|----------|---------|--------|----------------|
| Transient | API timeout, rate limit | Retry same prompt, exponential backoff | 2 |
| Parse | Output doesn't match JSON schema | Retry with error message appended | 1 |
| Semantic | Plan has no tasks, verify finds no tests | Retry with corrective feedback | 1 (0 for implement) |

## State on disk

```
.soda/<ticket>/
├── meta.json           # ticket, phase, worktree, branch, budget, generation
├── lock                # flock-based, contains PID + timestamp
├── triage.json         # structured output (from --json-schema)
├── plan.json
├── implement.json
├── verify.json
├── submit.json
├── monitor.json        # monitor response output (latest round)
├── monitor_state.json  # monitor polling state (poll count, rounds, last comment ID)
├── events.jsonl        # structured event log
└── logs/
    ├── triage_prompt.md
    ├── triage_response.md
    ├── monitor_response_1_prompt.md
    ├── monitor_response_1_output.md
    └── ...
```

Atomic writes: always write to `.tmp` then rename. Archive on re-run (`verify.json` → `verify.json.1`).

## Key design decisions

- **`--bare` mode**: eliminates context duplication (CLAUDE.md loaded twice) and saves 15-28K tokens per session. SODA inlines only what each phase needs.
- **Sandbox over advisory controls**: `--allowed-tools` is advisory (the model can ignore it). Landlock/seccomp/network namespaces are kernel-enforced. For unattended autonomous execution, enforcement beats advisory.
- **Disk state over in-memory**: crash recovery for free. Resume works by reading `.soda/<ticket>/`. No daemon needed.
- **Config-driven phases**: users can add, remove, or reorder phases via `phases.yaml`. Engine doesn't hardcode phase names.
- **Prompt overrides**: `~/.config/soda/prompts/<phase>.md` overrides embedded prompts without forking.
- **Local config auto-discovery**: when `--config` is not set, `loadConfig` checks for `soda.config.yaml` in the working directory before falling back to `~/.config/soda/config.yaml`. This means `soda init` + `soda run <ticket>` works without extra flags.
- **Root `phases.yaml` overrides embedded**: `resolvePhasesPath()` checks for a local `phases.yaml` in the working directory first, then falls back to the embedded copy. When changing pipeline config, update BOTH `cmd/soda/embeds/phases.yaml` (compiled into binary) AND the root `phases.yaml` (runtime override). Or just update the root file for immediate effect without rebuild.

## Git workflow

- **NEVER commit directly on main.** Always use a feature branch.
- **Always work in worktrees**: `git worktree add .worktrees/<branch> -b <branch> main`
- **Worktree directory**: `.worktrees/<branch>/` (gitignored)
- **Branch naming**: `feat/<issue-slug>`, `fix/<issue-slug>`, `chore/<issue-slug>`
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
4. **Landlock requires `agent-sandbox` wrapper binary**: it's a separate binary in the agentic-orchestrator. Must be on PATH.
5. **Network namespace requires unprivileged user namespaces**: test with `unshare --user --net --map-current-user -- /bin/true`. If it fails, sandbox falls back to seccomp-only.
6. **File locks are per-machine, not cross-machine**: `flock` on `.soda/<ticket>/lock` prevents concurrent runs on the same host but not across machines.
7. **Lock files persist after clean exit**: `ReleaseLock()` releases the flock but does not delete the lock file (intentional — avoids TOCTOU race). `soda status` derives terminal status from phase state, not lock presence.
8. **Root `phases.yaml` overrides embedded**: a `phases.yaml` in the project root takes precedence over the compiled-in version. Changes to the embedded file require a rebuild; the root file takes effect immediately.
9. **Always run `soda` from the main repo checkout**: running from inside a worktree used to create nested worktrees (fixed in #156), but it's still good practice to run from the root.

## Implementation workflow

**All development should be done using soda itself.** Run `soda run <ticket>` to implement issues through the pipeline. Manual implementation is acceptable when the pipeline is broken, the work is on soda's own infrastructure, or triage gates the ticket as "not automatable."

### Spec and plan workflow

For non-trivial tickets, the recommended workflow is:

1. **Write a spec** — post it on the issue (in body or as a comment with `<!-- soda:spec -->` markers)
2. **Get specialist reviews** — dispatch Go Specialist + AI Harness agents in parallel to review the spec
3. **Incorporate feedback** — update the spec based on review findings
4. **Write a plan** — post it on the issue with `<!-- soda:plan -->` markers
5. **Label the issue** — `spec ready` or `plan ready`
6. **Run soda** — `soda run <ticket>`. Triage detects the existing plan and skips the plan phase.

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
| Small (< 100K budget) | Skip reviews — the fix is trivial |
| Medium (100-140K) | Review after implementation (one round) |
| Large (> 140K) | Review after spec AND after implementation |

### How to review

Dispatch two subagents **in parallel** using the Agent tool:

1. **Go Specialist Agent**: review for Go idioms, error handling, interface design, test quality, performance, and correctness.
2. **AI Harness Agent**: review for prompt engineering, context budget impact, Claude Code CLI integration, sandbox compatibility, and structured output reliability.

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

Each ticket targets a **160K token working budget** (out of 256K context, after system prompt and safety buffer).

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
| < 100K | Ship as one issue |
| 100-140K | One issue, add explicit "do NOT read" list to save tokens |
| 140-160K | Split unless the work is truly indivisible |
| > 160K | Must split |

### Split when

- Multiple independent packages to create (each package is a natural boundary)
- Read surface > 50K tokens (~10K lines)
- More than 3 integration points (wiring 4+ packages)
- Mixed read-heavy and write-heavy work
- High test failure risk (complex wiring, external tools)

### Don't split when

- The work is tightly coupled (splitting creates stubs)
- Read surface is small but write surface is large (greenfield is cheap)
- Under 100K (splitting adds overhead for no benefit)

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
- Total: ~NK / 160K available
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
   - Split proposal if estimate exceeds 140K
   - Dependencies on other issues
6. Remove the `triage needed` label once complete

If the issue lacks acceptance criteria, add them. If the scope is ambiguous, list the open questions in the issue and ask the maintainer.

## Build sequence

Issues are numbered in dependency order:

1. `claude/command.go` — Claude Code CLI wrapper (#1)
2. `sandbox/runner.go` — agentic-orchestrator integration (#2, depends on #1)
3. `pipeline/state.go` — disk state with locking (#3, parallel with #1)
4. `pipeline/engine.go` — phase loop (#4, depends on #1-#3)
5. E2E triage + implement (#5, depends on #1-#4)
6. `ticket/` — Jira source (#6, parallel with #1-#3)
7. CLI commands (#7, depends on #3-#6)
8. TUI (#8, depends on #4, #7)

Parallelizable: #1, #3, and #6 have no dependencies on each other.

## Follow-up issues

When discovering bugs, tech debt, or improvement opportunities during a task, file them as separate GitHub issues with the `triage-needed` label. Do not fix them inline — stay focused on the current ticket's scope.

## New issue checklist

When creating a new issue, check whether any existing docs need updating as part of the work:

- Does the change affect `AGENTS.md`? (architecture, conventions, project structure, build sequence, gotchas)
- Does it add/change CLI commands or flags? (update `config.example.yaml`, help text)
- Does it change phase behavior? (update `phases.yaml` docs, prompt templates)
- Does it affect the state format? (update "State on disk" section in `AGENTS.md`)

If yes, include a "Docs to update" section in the issue body listing the files that need changes.

## CLI commands

| Command | Purpose |
|---------|---------|
| `soda init` | Create a starter `soda.config.yaml` in the current directory |
| `soda run <ticket>` | Run the pipeline for a ticket |
| `soda run <ticket> --from <phase>` | Resume from a specific phase |
| `soda status` | Show active and recent pipelines (sorted by status group, then submission time) |
| `soda history <ticket>` | Show phase details for a ticket |
| `soda history <ticket> --detail` | Show full structured JSON output per phase |
| `soda history <ticket> --phase <name>` | Drill down into a specific phase |
| `soda sessions` | List all previous pipeline runs with filtering and sorting |
| `soda clean <ticket>` | Remove completed/failed pipeline state and worktrees |
| `soda render-prompt <phase> <ticket>` | Render a phase prompt template for debugging |

## What NOT to do

- Do not hardcode project-specific references (repo names, Jira projects, ticket keys)
- Do not build a plugin system for phases — config-driven is enough for now
- Do not build an adapter/abstraction over multiple agent backends — build for Claude Code CLI first
- Do not put business logic in the TUI — it's a view layer over engine events
- Do not build the TUI and engine simultaneously — get headless working first (`--no-tui`)
