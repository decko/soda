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
Triage → Plan → Implement → Verify → Submit → Monitor
```

| Phase | Purpose | Tools | Timeout |
|-------|---------|-------|---------|
| Triage | Classify ticket, identify repo/files/complexity | Read-only | 3m |
| Plan | Design approach, break into atomic tasks | Read-only | 5m |
| Implement | Write code, run tests, commit | Full | 15m |
| Verify | Run tests, check acceptance criteria, review code | Read + Bash | 5m |
| Submit | Push branch, create PR/MR | git + gh/glab | 3m |
| Monitor | Poll for review comments, respond (polling loop) | Full | 4h max |

Phase definitions, tools, timeouts, and retry policies are in `phases.yaml`.

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
├── schemas/                   # Structured output schemas (Go structs)
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
├── events.jsonl        # structured event log
└── logs/
    ├── triage_prompt.md
    ├── triage_response.md
    └── ...
```

Atomic writes: always write to `.tmp` then rename. Archive on re-run (`verify.json` → `verify.json.1`).

## Key design decisions

- **`--bare` mode**: eliminates context duplication (CLAUDE.md loaded twice) and saves 15-28K tokens per session. SODA inlines only what each phase needs.
- **Sandbox over advisory controls**: `--allowed-tools` is advisory (the model can ignore it). Landlock/seccomp/network namespaces are kernel-enforced. For unattended autonomous execution, enforcement beats advisory.
- **Disk state over in-memory**: crash recovery for free. Resume works by reading `.soda/<ticket>/`. No daemon needed.
- **Config-driven phases**: users can add, remove, or reorder phases via `phases.yaml`. Engine doesn't hardcode phase names.
- **Prompt overrides**: `~/.config/soda/prompts/<phase>.md` overrides embedded prompts without forking.
- **Monitor is a polling loop**: separate from one-shot phases. 2m initial interval, 5m after 30m, max 4h, max 3 auto-response rounds.

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

## Conventions

- **Formatting**: `gofmt` (standard Go formatting)
- **Linting**: `go vet` + `staticcheck`
- **Testing**: `go test ./...`
- **Building**: `go build -o soda ./cmd/soda`
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

## Implementation workflow

Issues are fully specified with acceptance criteria.
Do NOT write separate spec or plan documents.
Read the issue, read the existing code, implement, test.

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

## What NOT to do

- Do not hardcode project-specific references (repo names, Jira projects, ticket keys)
- Do not build a plugin system for phases — config-driven is enough for now
- Do not build an adapter/abstraction over multiple agent backends — build for Claude Code CLI first
- Do not put business logic in the TUI — it's a view layer over engine events
- Do not build the TUI and engine simultaneously — get headless working first (`--no-tui`)
