# CONSTITUTION.md — SODA Project Constitution

## Identity

SODA is a **Session-Orchestrated Development Agent** — a CLI that orchestrates AI coding sessions through a pipeline of isolated phases to implement tickets end-to-end, unattended.

SODA is NOT an AI model, a library, a framework, or a general-purpose automation tool. It is an opinionated pipeline runner that treats AI sessions as sandboxed, stateless compute units.

## Extensibility

SODA is extensible through **external runner plugins**, not through a public Go API.

Internally, the engine is decoupled from any specific AI backend via the `Runner` interface. The built-in runner wraps Claude Code CLI. The interface exists so the project can adopt new backends without rewriting the engine — but it is an internal boundary, not a public import path. SODA does not expose `pkg/` packages and is not a library.

External extensibility follows the **exec plugin model** (same pattern as git, terraform providers, and docker plugins): a standalone binary that SODA discovers and executes, communicating via a defined protocol (prompt on stdin, structured JSON on stdout). Plugin authors build and ship their own binary — no fork, no Go import, no coupling to SODA internals.

This means:
- SODA stays a CLI. Always.
- New backends (direct API clients, alternative models, REST services) are plugins, not PRs to the engine.
- The protocol is the contract. SODA and plugins can evolve independently.
- The plugin protocol specification is a future design document.

## Non-Negotiable Principles

### 1. Enforcement over advisory

Unattended execution demands OS-level enforcement (Landlock, seccomp, cgroups, network namespaces), not advisory controls (`--allowed-tools`). The model can ignore advisory restrictions; the kernel cannot be ignored.

### 2. Disk state, no daemon

All state lives on disk (`.soda/<ticket>/`). Crash recovery is free — resume by reading files. No daemon, no database, no in-memory-only state. If the process dies, nothing is lost.

### 3. Fresh context per phase

Each pipeline phase runs in a new Claude session with a clean context window. No shared memory between phases. SODA controls what enters the context — the model never accumulates stale state across phases.

### 4. Config-driven pipelines

Users own their pipeline. The engine does not hardcode phase names, ordering, or behavior. Phases are defined in YAML. Users can add, remove, reorder, and override prompts without forking.

### 5. Credential isolation

The sandboxed agent never sees real API keys or tokens. Credentials are injected by the host-side proxy at request time. If the sandbox is compromised, no secrets are exposed.

### 6. Context budget awareness

Every feature must account for its token cost. Injecting 30KB of context is a design decision, not an afterthought. Budget caps, deduplication, and fallbacks are required — unbounded context injection is a bug.

## Architectural Invariants

1. **Engine never imports TUI.** The TUI is a view layer over engine events. Business logic lives in `internal/pipeline/`, never in `internal/tui/`.

2. **State flows one way.** Engine → State → Disk. The TUI reads events; it never writes state.

3. **Runner abstraction.** The engine is decoupled from Claude Code CLI specifics via `internal/runner/`. Swapping the backend (sandbox, mock, future API-direct) requires zero engine changes.

4. **Interfaces at the consumer.** Define interfaces where they are used, not where they are implemented. Keep them minimal.

5. **Errors wrap, never discard.** Always `fmt.Errorf("context: %w", err)`. Silent error swallowing is a bug — except for best-effort enrichment (code snippets, cost display).

## Development Rules

1. **Never commit on main.** Always use a feature branch in a worktree.
2. **Always work in worktrees.** `git worktree add .worktrees/<branch> -b <branch> main`.
3. **Stage specific files.** `git add <file>`, never `git add .` or `git add -A`.
4. **Assisted-by trailer.** Every commit names the model used.
5. **TDD.** Write tests first. See them fail. Then implement. Functional tests over mocks.
6. **Follow-up issues for out-of-scope work.** Don't fix unrelated bugs inline — file a ticket.
7. **No single-character variable names.** Descriptive names in all contexts.

## Quality Gates

Every merge requires:

- `go test ./...` passes
- `go vet ./...` clean
- `gofmt` formatted (enforced by pre-commit hook)

Every release tracks (via raki):

- `first_pass_verify_rate` — target: ≥ 0.9
- `rework_cycles` — target: ≤ 0.5
- `cost_efficiency` — target: ≤ $15/session
- `self_correction_rate` — target: ≥ 0.9
- `knowledge_miss_rate` — tracked, no gate (retrieval improvement signal)

## Amendments

This constitution can be amended by the project maintainer. Changes require a PR with rationale. The principles section should rarely change — if a principle needs frequent amendment, it was not a principle.
