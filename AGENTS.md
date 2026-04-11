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
| Implement | Create worktree, write code, run tests, commit | Full | 15m |
| Verify | Run tests, check acceptance criteria, review code | Read + Bash | 5m |
| Submit | Push branch, create PR/MR | git + gh/glab | 3m |
| Monitor | Poll for review comments, respond (polling loop) | Full | 4h max |

Phase definitions, tools, timeouts, and retry policies are in `phases.yaml`.

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
