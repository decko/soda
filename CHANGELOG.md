# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] — "Right-Sized" - 2026-04-28

### Added

#### Adaptive context fitting
- Pre-render `fitToBudget()` pass dynamically reduces context injection when prompts
  approach the token budget — no template changes needed
- Per-phase reduction priorities: siblings first, then diffs, then artifacts;
  core prompt, ticket description, acceptance criteria, and plan are never reduced
- Truncation manifest injected listing what was reduced so the model can use tools
  to retrieve missing context (~30 tokens)
- Per-phase `prompt_budget` configurable in `phases.yaml`
- Fails with clear error when budget cannot be met after reduction

#### Token estimation CLI
- `soda run <ticket> --estimate` prints per-phase token estimates using bytes/3.3
  heuristic (implies `--dry-run`)
- Warns when estimated tokens exceed `warn_tokens` config (default 60K)
- Summary line with total estimated tokens, ratio, and threshold
- AGENTS.md ticket sizing section updated with corrected 80K budget

#### Authentication: `apiKeyHelper` support
- `auth.api_key_helper` config field for custom credential scripts
- `--settings-path` passthrough to Claude Code CLI for `apiKeyHelper` integration
- Documented that `--bare` mode skips OAuth/keychain — requires API key, Vertex, or
  `apiKeyHelper`
- `soda doctor` auth check detects API key, Vertex, and `apiKeyHelper` configurations

#### RPM packaging
- `packaging/rpm/soda.spec` — full build with CGO sandbox support
- `packaging/rpm/soda-minimal.spec` — static binary without sandbox
- Both specs include shell completions (bash/zsh/fish) and `config.example.yaml`
- `Conflicts:` between packages prevents both installed simultaneously
- CI workflow step for COPR submission (gated on `COPR_API_TOKEN` secret)

#### Sandbox integration tests
- Integration tests exercising full sandbox `Run()` path with mock process
- Proxy round-trip tests with mock HTTP server
- Sandbox isolation verification (Landlock, cgroups) with `t.Skip()` on
  unsupported kernels
- Gated behind `//go:build cgo && integration`
- CI job with LFS fetch workaround (marked `continue-on-error` due to
  go-arapuca LFS fragility)

## [0.2.0] — "Trust but Verify" - 2026-04-27

### Added

#### Auto-merge safeguards
- Auto-merge for unattended PR merging with configurable safeguard chain:
  label check → approval status → CI freshness → branch protection validation → merge
- `merge_method` config (squash/merge/rebase, default squash)
- `merge_labels` config (default `auto-merge-ok`, no escape hatch)
- `auto_merge_timeout` (default 30m) prevents infinite wait after approval
- Runtime branch protection validation via optional `MergeValidator` interface
- Rebase conflict detection: monitor terminates with clear event instead of
  spinning when content conflicts prevent rebase
- Events: `auto_merge_completed`, `auto_merge_blocked`, `auto_merge_dry_run`,
  `rebase_conflict`

#### Notification hooks
- Script callback and webhook POST on pipeline completion/failure
- `on_finish` handler (fires on any completion) and `on_failure` handler
  (fires only on failed/timeout)
- Webhook payload includes per-phase token breakdown with cache tokens
- Script invocation via `exec.Command` (no shell — injection-safe)
- Default 10s webhook timeout, configurable
- Best-effort: notification failures never block the pipeline
- `soda validate` checks script paths and webhook URLs

#### Token budget estimation
- Warn-only prompt size estimation using bytes/3.3 heuristic
- Configurable warning threshold (default 60K tokens), budget limit (default 80K)
- `EstimatedPromptTokens` persisted in `PhaseState` for telemetry
- Calibration data (`prompt_bytes` vs `actual_tokens_in`) logged to events
- Events: `token_budget_warning`, `token_budget_calibration`

#### Structured error messages
- Structured error types for common pipeline failures: `RetryExhaustedError`,
  `PhaseNotFoundError`, `WorktreeError`, `PromptError`, `LockError`
- Tailored CLI recovery advice with specific commands for each error type

#### PRPoller interface expansion
- `PRStatus` expanded: `ReviewDecision` (raw GitHub value), `HeadSHA`
- `CIStatus` expanded: `CommitSHA` for CI freshness checks
- `MergePR` method with sentinel errors (`ErrMergeConflict`, `ErrPRClosed`)
- Optional `MergeValidator` interface for forge-agnostic merge prerequisite checks
- `MonitorState.MergePending` for auto-merge state tracking

#### Prompt hash traceability
- SHA-256 hash of rendered prompt content persisted in `PhaseState.PromptHash`
- Composite hash for parallel-review phases (sorted reviewer hashes)
- Visible in `soda history <ticket> --detail`

#### Status budget display
- `soda status` shows cost and budget columns per ticket
- Budget shows "∞" when no limit is configured

#### Smoke tests
- Config discovery smoke test: validates full `loadConfig` search chain
- Monitor phase smoke test: passive polling, PR state detection, comment response
- Sandbox unit tests (no CGO): phase name sanitization, `claudeEnv`, config builder
- Pipeline integration smoke test: happy path, rework loop, corrective loop, resume
- Crash recovery tests: table-driven state manipulation for all phase boundaries

### Fixed

- **`gh --paginate --jq "flatten"` silently drops comments beyond first page** —
  switched to `.[]` with `json.NewDecoder` for correct multi-page handling (#415)
- **`exec.Command` without context can hang indefinitely** — threaded
  `context.Context` through `RepoRoot`, `DeleteBranch`, `claudeEnv` (#416)
- **`LogEvent` error silently discarded** — warn on first disk failure instead
  of silently dropping all events (#417)
- **Doctor config resolution missing `UserHomeDir` fallback** — extracted shared
  `resolveConfigPath` helper matching `loadConfig` behavior (#398)
- **`checkGhAuth` unconditionally optional** — now required when
  `ticket_source: github` is configured (#399)
- **Generic `review.md` prompt missing from embedded defaults** — external users
  with custom `phases.yaml` failed at every review phase (#451)
- **3-phase docs-only pipeline not committed** — embedded file had 2 phases,
  tests expected 3 (#426)

### Changed

- Plan phase timeout: 5m → 8m (complex test specs need more planning time)
- Verify phase timeout: 5m → 8m (6+ file changes need more verification time)
- Implement phase timeout: 15m → 25m (large tickets with 7+ tasks)

## [0.1.0] - 2026-05-01

Initial release of SODA — Session-Orchestrated Development Agent.

### Added

#### Core pipeline

- Full pipeline: **triage → plan → implement → verify → review → submit → monitor**
- Worktree-first execution: pipeline creates a dedicated git worktree before any phase
  runs, keeping all work isolated from the main checkout
- Config-driven phases via `phases.yaml` — add, remove, or reorder phases without
  recompiling; root `phases.yaml` overrides the embedded default
- Per-phase tool scoping via `--allowed-tools` (read-only for triage/plan, full for
  implement/patch)
- Disk-based state in `.soda/<ticket>/` with atomic writes and flock-based locking —
  crash recovery and resume come for free
- Structured JSON output via `--json-schema` for every phase; schemas generated from
  Go structs via `go generate ./schemas/...`
- Phase dependencies via `depends_on` — phases cannot run before their prerequisites
  have completed successfully

#### Conditional phases

- **Patch phase** (corrective): runs automatically when verify FAILs; targeted fixes
  without a full re-implement cycle; regression detection prevents patch from
  introducing new failures
- **Follow-up phase** (post-submit): runs when review verdict is `pass-with-follow-ups`;
  creates GitHub issues for minor findings; failures are non-terminal
- Unified `reworkSignal` for all rework routing with independent cycle counters
  (`ReworkCycles`, `PatchCycles`) persisted in `meta.json`
- Configurable exhaustion policy for corrective patch: `stop` (default), `escalate`
  (one-shot escalation to full implement), or `retry`

#### Rework and review routing

- Review → implement rework loop (up to `max_rework_cycles`, default 2)
- At max rework cycles with only minor findings: automatic downgrade to
  `pass-with-follow-ups` so the pipeline proceeds instead of blocking
- Focused rework prompts with sequential finding-fixing guidance
- Skip redundant reviewers on rework cycles to reduce cost
- Parallel specialist review with configurable per-reviewer models and retry logic
- Partial reviewer failure tolerance — a single reviewer failure does not abort
  the whole review phase
- Deduplication of review findings across reviewers
- `PhaseGateError` for terminal domain failures (triage: not automatable;
  verify: FAIL after exhaustion; review: critical/major findings at max rework)

#### Context injection

- **Code snippet injection**: ±5 lines around each critical/major finding's
  `file:line` injected as `CodeSnippet` on `EnrichedFinding` — rework sessions
  see exact code without spending tokens on tool calls
- **Sibling-function injection**: sibling functions from the same file injected into
  implement prompts for structural context
- **Deep context injection**: full function bodies for plan-referenced files injected
  into implement prompts
- **Diff injection**: implement diff injected into rework feedback so reviewers and
  re-implement sessions see exactly what changed

#### Monitor phase

- Passive mode (default): polls PR status without `self_user` configured
- Active mode: polls PR, classifies new review comments, and dispatches response
  sessions (fix sessions for code changes, reply-only sessions for questions)
- Comment classification by type: `code_change`, `question`, `nit`, `approval`,
  `dismissal`, `bot`, `self`
- CODEOWNERS authority resolution for comment weight
- Monitor profiles: `conservative`, `smart`, `aggressive` — control auto-rebase,
  nit auto-fix, and response to non-authoritative comments
- Configurable polling intervals, max response rounds, and max monitor duration
- Termination on PR approved/merged/closed or when response round limit is reached

#### Sandbox execution

- OS-level isolation via [go-arapuca](https://github.com/sergio-correia/go-arapuca):
  Landlock filesystem isolation, network namespace (Unix sockets only), cgroup
  resource limits (memory, CPU, PIDs), seccomp syscall filter
- End-to-end sandbox integration with TCP proxy and Vertex AI passthrough
- `GH_TOKEN` and `SSH_AUTH_SOCK` forwarded into sandboxed submit/follow-up sessions
- Build tag `cgo` gates sandbox; `CGO_ENABLED=0` compiles a stub that returns a
  runtime error

#### LLM proxy

- LLM proxy layer for sandbox credential isolation — the sandbox never holds raw
  API keys; the proxy handles authentication on behalf of the sandboxed process

#### Named pipelines

- `--pipeline <name>` flag on `soda run` to select a non-default pipeline
- Built-in **quick-fix** pipeline: implement → verify → submit (no triage, no plan,
  no full review)
- Built-in **docs-only** pipeline: plan → implement → submit (documentation-only, Sonnet
  changes)
- `soda pipelines new` scaffolds a new pipeline definition file
- `soda pipelines` lists all available named pipelines (built-in + project-local)
- External JSON schema file support for custom phase output types
- `Extras` artifact map for custom phase output beyond the standard schema

#### Spec and plan extraction

- Triage detects existing specs and plans from GitHub issue comments (configurable
  start/end markers) and Jira structured fields
- When a reviewed plan is found, triage sets `skip_plan: true` and the plan phase
  is skipped — existing plan is used as the plan artifact directly
- Issue labels `spec ready` and `plan ready` signal readiness to triage
- Configurable extraction markers in `soda.yaml` under `github:` and `jira:`

#### Live streaming

- Broadcast layer: phase output streamed in real time as structured events to
  `.soda/<ticket>/events.jsonl`
- `soda attach` / TUI attach mode: connect a TUI to an already-running pipeline
  from another terminal
- Poll-based follow mode for `soda log -f` with reverse scan for terminal events

#### TUI

- Interactive bubbletea TUI with live phase progress, cost/token stats, and
  streaming phase output
- **Ticket picker**: browse open tickets from GitHub Issues or Jira, select one,
  and trigger the pipeline — no copy-paste required
- Attach mode: connect TUI to an externally running pipeline
- Phase progress widget, cost/token stats panel, and keybinding display

#### CLI commands

| Command | Description |
|---------|-------------|
| `soda run <ticket>` | Run the pipeline for a ticket |
| `soda run --pipeline <name>` | Run with a named pipeline |
| `soda run --from <phase>` | Resume from a specific phase (`last` auto-resolves) |
| `soda run --dry-run` | Render prompts without executing |
| `soda run --mode checkpoint` | Pause after each phase for confirmation |
| `soda init` | Auto-detect project stack and generate `soda.yaml` |
| `soda status` | Show active and recent pipelines with rework cycles and cost trend |
| `soda history <ticket>` | Show phase details; `--detail` for full JSON; `--phase` to drill down |
| `soda sessions` | List all previous pipeline runs |
| `soda log <ticket>` | Print formatted pipeline events; `-f` to tail in real time |
| `soda clean <ticket>` | Remove worktree and branches, preserve session data |
| `soda clean <ticket> --purge` | Remove everything including session data |
| `soda clean --all` | Clean all worktrees and branches |
| `soda validate` | Check config, phases, and prompts for errors without running |
| `soda cost` | Show cumulative cost breakdown across all sessions |
| `soda spec <description>` | Generate a ticket specification from a short description |
| `soda pipelines` | List available named pipelines |
| `soda pipelines new` | Scaffold a new pipeline definition |
| `soda render-prompt` | Render a phase prompt template for debugging |
| `soda doctor` | Run diagnostic checks (config, Claude CLI, sandbox, network) |
| `soda version` | Show version |
| `soda plugin install` | Install the SODA Claude Code plugin (project-local or global) |
| `soda plugin uninstall` | Remove the SODA Claude Code plugin |

#### Preflight checks

- `soda run` performs preflight checks before starting the pipeline: Claude CLI
  present and meets minimum version (2.1.81), config valid, ticket source reachable
- `soda doctor` provides a standalone diagnostic report for all checks

#### Claude Code plugin

- Embedded plugin installed via `soda plugin install` (project-local or `--global`)
- **Skill `soda-pipeline`**: teaches Claude about pipeline architecture, phase
  lifecycle, state management, and troubleshooting; includes operational runbook
- Slash commands: `/soda:run`, `/soda:status`, `/soda:sessions`, `/soda:history`,
  `/soda:clean`, `/soda:resume`, `/soda:render`
- **Agent `pipeline-architect`**: design-only agent that proposes a `phases.yaml`
  for the current project

#### Project stack auto-detection

- `detect.Detect` inspects the repository to identify language, build tool, test
  command, formatter, and forge; result injected into `PromptData.DetectedStack`
  for all phases

#### Budget and cost tracking

- Per-phase cost limits via `max_cost_per_phase` (cumulative across rework cycles)
  and `max_cost_per_generation` (per single run)
- Per-ticket pipeline cost cap via `max_cost_per_ticket`
- `max_pipeline_duration` global wall-clock limit for the entire pipeline
- Token counts (`TokensIn`, `TokensOut`, `CacheTokensIn`) persisted in `meta.json`
  alongside per-phase cost and duration
- `soda cost` aggregates cost across all sessions
- `BudgetExceededError` signals when any cost limit is breached

#### Retry and error handling

- Three error categories: transient (API timeout/rate limit → exponential backoff),
  parse (schema mismatch → retry with error context), semantic (domain error → retry
  with corrective feedback)
- Per-phase retry counts configurable independently for each error category
- API concurrency limiter (semaphore) to avoid rate-limit bursts during parallel review
- Binary version warning when the soda binary is newer than the pipeline state on disk

#### CI/CD

- **Release workflow**: builds multi-platform binaries and publishes a GitHub release
  on version tag push
- **Nightly workflow**: runs full test suite and lint against the latest Claude CLI
  and Go toolchain versions

#### Configuration

- `soda.yaml` with full project configuration: ticket source, forge, model, budgets,
  monitor profile, reviewer definitions, sandbox settings, spec/plan markers
- `config.example.yaml` — fully annotated reference configuration
- `soda init` auto-generates `soda.yaml` from detected project stack
- Local-first config discovery: project `soda.yaml` > home directory > defaults
- Custom prompt template directory via `prompts_path`; per-phase overrides via
  `~/.config/soda/prompts/<phase>.md`
- Per-phase model override (e.g., patch phase uses a faster/cheaper model)
- `--bare` mode for Claude Code: disables CLAUDE.md auto-discovery, saving 15–28K
  tokens per session

#### State and observability

- Structured event log (`events.jsonl`) with `phase_started`, `phase_completed`,
  `phase_failed`, and custom events
- `soda log` pretty-prints events; `-f` follows in real time
- `soda status` shows lock state, current phase, rework cycles, and running cost
- Atomic state writes (`.tmp` → rename) with archive on re-run
  (`verify.json` → `verify.json.1`)

[Unreleased]: https://github.com/decko/soda/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/decko/soda/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/decko/soda/compare/v0.1.1...v0.2.0
[0.1.0]: https://github.com/decko/soda/releases/tag/v0.1.0
