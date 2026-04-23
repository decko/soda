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
- Plugins run with host privileges and are part of the trusted computing base — they are equivalent to locally installed software, not sandboxed agents.
- The plugin protocol specification is a future design document.

## Non-Negotiable Principles

### 1. Enforcement over advisory

Unattended execution demands OS-level enforcement (Landlock, seccomp, cgroups, network namespaces), not advisory controls. The model can ignore advisory restrictions; the kernel cannot be ignored. Advisory controls (`--allowed-tools`, tool scoping) are defense-in-depth — valuable as a layer, but never the sole protection.

**Known gap:** Network namespace isolation is deferred until go-arapuca implements the NetworkProxySocket bridge. The TCP proxy provides credential isolation but does not restrict outbound network access from the sandbox.

### 2. Disk state, no daemon

All state lives on disk (`.soda/<ticket>/`). Atomic writes and file locks ensure state is always consistent. No daemon, no database, no in-memory-only state. If the process dies, the pipeline resumes by reading disk state. Phases must be idempotent — crash recovery relies on safe re-execution of interrupted phases. The `.soda/` directory is internal and must not be modified externally during execution.

### 3. Fresh context per phase

Each pipeline phase runs in a new runner session with a clean context window. No shared memory between phases. SODA controls what enters the context — the runner never accumulates stale state across phases.

### 4. Config-driven pipelines

Users own their pipeline. The engine does not hardcode phase names or ordering. Phases are defined in YAML. Users can add, remove, reorder, and override prompts without forking. Phase *type semantics* (parallel-review, polling, corrective) are engine-defined; phase *configuration and ordering* are user-defined.

### 5. Credential isolation

The sandboxed agent never sees real API keys or tokens. Credentials are injected by the host-side proxy at request time. If the sandbox is compromised, no secrets are exposed.

### 6. Context budget awareness

Every feature that injects content into the runner's context window must declare its token budget, be subject to a configurable cap, and degrade gracefully when the budget is exhausted. Budget caps, deduplication, and fallbacks are required — unbounded context injection is a bug.

### 7. Spend limits

Spend limits are mandatory for unattended execution. The proxy token budget is the hard enforcement boundary (rejects requests mid-phase). The engine budget is soft enforcement (checked between phases). Both must be configured for autonomous operation.

### 8. Observability

Every session must produce a durable, structured audit trail sufficient for post-mortem analysis. This includes pipeline events (`events.jsonl`), proxy request logs, cost/token tracking, and phase artifacts. If something goes wrong at 3 AM, the logs must explain what happened and why.

## Architectural Invariants

1. **Engine never imports TUI.** The TUI is a view layer over engine events. Business logic lives in `internal/pipeline/`, never in `internal/tui/`.

2. **State flows one way.** Engine → State → Disk. The TUI reads events; it never writes state. The pause/resume signal channel is the sole exception — it is control flow, not state.

3. **Runner abstraction.** The engine is decoupled from CLI specifics via `internal/runner/`. Swapping the backend (sandbox, mock, future plugins) requires zero engine changes.

4. **Interfaces at the consumer.** Define interfaces where they are used, not where they are implemented. Keep them minimal. The `Runner` interface in `internal/runner/` is the deliberate exception — it is a shared-types boundary package.

5. **Errors wrap, never discard.** Always `fmt.Errorf("context: %w", err)`. Silent error swallowing is a bug — except for best-effort enrichment (code snippets, cost display).

6. **Output validation.** Runner output is untrusted. The engine validates structure (JSON schema) and semantics (retry policies for `ParseError`, `SemanticError`) before using output in downstream phases.

## Self-Hosting

SODA uses itself to develop itself. This creates a recursive relationship that requires special governance:

- **Changes to prompts, schemas, pipeline config, and this constitution are meta-changes** that alter the pipeline's own behavior. Such changes require manual human review.
- **Prompt templates are first-class artifacts**, not throwaway config. Prompt regressions can cascade through rework cycles.
- **The review phase reviews code written by the implement phase** — both guided by prompts that SODA itself may have modified. When modifying review prompts, human oversight is mandatory.

## Development Rules

1. **Never commit on main.** Always use a feature branch in a worktree.
2. **Always work in worktrees.** `git worktree add .worktrees/<branch> -b <branch> main`.
3. **Stage specific files.** `git add <file>`, never `git add .` or `git add -A`.
4. **Assisted-by trailer.** Every AI-assisted commit names the model used.
5. **TDD.** Write tests first. See them fail. Then implement. Functional tests over mocks where feasible; mocks are acceptable for external process boundaries.
6. **Follow-up issues for out-of-scope work.** Don't fix unrelated bugs inline — file a ticket.
7. **Descriptive variable names.** No single-character names except receivers, loop indices, and standard Go conventions (`ctx`, `err`, `ok`).

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

This constitution can be amended by the project maintainer via PR with rationale. The principles section should rarely change — if a principle needs frequent amendment, it was not a principle. Constitutional changes are meta-changes and require human review, not pipeline auto-submit.
