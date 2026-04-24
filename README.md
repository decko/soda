# SODA — Session-Orchestrated Development Agent

[![CI](https://github.com/decko/soda/actions/workflows/ci.yaml/badge.svg)](https://github.com/decko/soda/actions/workflows/ci.yaml) [![Go 1.25](https://img.shields.io/badge/go-1.25-blue.svg)](https://go.dev) [![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

Give it a ticket, get a PR. SODA is a Go CLI/TUI that orchestrates AI coding
sessions through a structured pipeline to implement GitHub Issues end-to-end.
Each pipeline phase runs in a fresh, sandboxed Claude Code session with
structured output — no shared context, no prompt bleed. A typical run costs
**$5–11 USD** in Claude API usage and produces a reviewed, tested pull request.

## Pipeline

```
Ticket → Triage → Plan → Implement → Verify → Review → Submit → Monitor → PR
```

| Phase | What it does |
|-------|-------------|
| **Triage** | Classifies the ticket, identifies relevant files, routes the pipeline |
| **Plan** | Designs the approach, breaks work into atomic tasks |
| **Implement** | Writes code, runs tests, commits |
| **Verify** | Runs tests, checks acceptance criteria, reviews code quality |
| **Review** | Parallel specialist review (Go, AI harness, SRE) |
| **Submit** | Pushes branch, opens pull request |
| **Monitor** | Polls PR for comments and CI, responds and fixes |

Conditional phases: **Patch** runs when verify fails (targeted fixes);
**Follow-up** runs when review passes with minor findings (creates follow-up tickets).

## Cost

| Ticket type | Typical cost |
|-------------|-------------|
| Simple fix (1–3 tasks) | $2–6 |
| Medium feature (4–8 tasks) | $8–15 |
| Full 9-phase pipeline | $5–11 average |

Set `max_cost_per_ticket` in `soda.yaml` as a hard safety net. Per-phase
and per-generation limits are also available.

## Prerequisites

| Prerequisite | Why | Install |
|---|---|---|
| Claude Code CLI | AI agent backend (paid subscription required) | [claude.ai/code](https://claude.ai/code) |
| `gh` CLI | GitHub integration | [cli.github.com](https://cli.github.com) |
| Git 2.9+ | Worktree support | [git-scm.com](https://git-scm.com) |
| Go 1.25+ | For `go install` (or use a binary release) | [go.dev](https://go.dev) |

## Installation

**Via `go install`** (recommended):

```bash
go install github.com/decko/soda/cmd/soda@latest
```

**Binary release** — download a pre-built binary for your platform from
[GitHub Releases](https://github.com/decko/soda/releases):

| Platform | File |
|----------|------|
| Linux amd64 (with sandbox) | `soda-linux-amd64-sandbox` |
| Linux arm64 | `soda-linux-arm64` |
| macOS Intel | `soda-darwin-amd64` |
| macOS Apple Silicon | `soda-darwin-arm64` |

**Build from source**:

```bash
CGO_ENABLED=0 go build -o soda ./cmd/soda
```

> **Note:** Sandbox features (Landlock, seccomp, cgroups) require
> `CGO_ENABLED=1`. The binary works without them — sandbox is optional.

## Quick Start

```bash
soda init                    # generate soda.yaml for this project
soda doctor                  # verify prerequisites are installed
soda run <issue-number>      # run the full pipeline for a GitHub issue
soda status                  # check active and recent pipelines
soda history <issue-number>  # inspect phase-by-phase results
```

See [docs/quickstart.md](docs/quickstart.md) for a step-by-step walkthrough.

## Configuration

SODA uses three config layers:

| Layer | File | Purpose |
|-------|------|---------|
| Project config | `soda.yaml` | Ticket source, model, budget limits, repo settings |
| Pipeline config | `phases.yaml` | Add, remove, or reorder phases; set tools and timeouts |
| Prompt overrides | `~/.config/soda/prompts/<phase>.md` | Customize phase prompts without forking |

Run `soda init` to generate a `soda.yaml` auto-detected from your project.
See [config.example.yaml](config.example.yaml) for a fully annotated reference
and [docs/configuration.md](docs/configuration.md) for the full guide.

## CLI Reference

| Command | Purpose |
|---------|---------|
| `soda run <ticket>` | Run pipeline for a ticket |
| `soda run <ticket> --pipeline docs-only` | Use a named pipeline |
| `soda run <ticket> --from last` | Resume from last failed phase |
| `soda run <ticket> --mode checkpoint` | Pause after each phase for confirmation |
| `soda status` | Show active and recent pipelines |
| `soda history <ticket>` | Show phase details for a ticket |
| `soda log <ticket> -f` | Tail live pipeline events |
| `soda attach <ticket>` | Stream live output of a running pipeline |
| `soda clean <ticket>` | Remove worktree and branches |
| `soda cost` | Show cost breakdown across all sessions |
| `soda doctor` | Check prerequisites |
| `soda validate` | Check config, phases, and prompts for errors |
| `soda spec "description"` | Generate a ticket specification |
| `soda pick` | Interactive ticket picker |
| `soda pipelines` | List available named pipelines |
| `soda init` | Generate `soda.yaml` config |

## Named Pipelines

| Pipeline | Phases | Use case |
|----------|--------|----------|
| `default` | Full 9-phase pipeline | New features, bug fixes |
| `quick-fix` | implement → verify → submit | Small, well-understood fixes |
| `docs-only` | plan → implement → submit | Documentation changes (Sonnet) |

```bash
soda run 42 --pipeline quick-fix
soda run 42 --pipeline docs-only
soda pipelines new my-pipeline   # create a custom pipeline
```

## How It Works

SODA creates a Git worktree before any phase runs — all phases execute inside
the worktree, never in the main checkout. For each phase, SODA renders a prompt
template with handoff artifacts from upstream phases, spawns a Claude Code
session with `--bare --json-schema` (structured output, no auto-discovery),
streams output to the TUI, parses the JSON response, and writes the artifact
to `.soda/<ticket>/`. Context resets between phases: each session starts fresh.
A rework loop triggers when review flags critical or major findings (max 2
cycles). A corrective loop triggers when verify fails (patch phase, then
re-verify).

## Sandbox

When built with `CGO_ENABLED=1`, SODA uses
[go-arapuca](https://github.com/sergio-correia/go-arapuca) for OS-level
isolation around each Claude Code session:

- **Landlock** — filesystem access restricted to the worktree
- **seccomp** — syscall allowlist enforced at the kernel level
- **cgroups** — memory, CPU, and PID limits

Sandbox is **optional**. Builds with `CGO_ENABLED=0` run without isolation —
useful for environments where CGO is unavailable. Configure via the `sandbox:`
block in `soda.yaml`.

## Claude Code Plugin

SODA ships an embedded plugin that gives Claude Code knowledge of SODA
pipelines and quick access to soda commands. The plugin is auto-discovered
from the `.claude/` directory.

### Install

```bash
soda plugin install              # project-local: .claude/plugins/soda/
soda plugin install --global     # global: ~/.claude/plugins/soda/
```

### Uninstall

```bash
soda plugin uninstall            # remove project-local plugin
soda plugin uninstall --global   # remove global plugin
```

### What the plugin provides

| Component | Description |
|-----------|-------------|
| **Skill: `soda-pipeline`** | Pipeline architecture, phase lifecycle, state management, troubleshooting |
| **`/soda:run <ticket>`** | Run the pipeline for a ticket |
| **`/soda:status`** | Show current pipeline status |
| **`/soda:sessions`** | List previous pipeline sessions |
| **Agent: `pipeline-architect`** | Design-only agent that proposes a `phases.yaml` |

Plugin files are embedded in the soda binary and version-matched — updates
arrive with `go install`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup instructions, pre-commit
hooks, and code style guidelines. All contributions must follow TDD: write
failing tests before writing implementation code.

## License

Apache-2.0 — see [LICENSE](LICENSE)
