# SODA — Session-Orchestrated Development Agent

[![CI](https://github.com/decko/soda/actions/workflows/ci.yaml/badge.svg)](https://github.com/decko/soda/actions/workflows/ci.yaml) [![Go 1.25](https://img.shields.io/badge/go-1.25-blue.svg)](https://go.dev) [![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

A programmable pipeline that turns tickets into PRs — one phase at a time. SODA
is a Go CLI/TUI that orchestrates AI coding sessions through configurable
pipelines to implement GitHub Issues end-to-end. Each phase runs in a fresh,
sandboxed Claude Code session with structured output — no shared context, no
prompt bleed. Use the default 9-phase pipeline out of the box, or compose your
own from scratch.

## Build Your Pipeline

SODA ships three built-in pipelines. Pick one, or compose your own.

| Want to… | Use |
|----------|-----|
| Run everything end-to-end | `soda run 42` (default pipeline) |
| Skip triage and planning | `soda run 42 --pipeline quick-fix` |
| Update docs only (Sonnet) | `soda run 42 --pipeline docs-only` |
| Build a custom pipeline | `soda pipelines new my-pipeline` |
| Design phases interactively | Ask the `pipeline-architect` agent |

### Minimal custom pipeline (3 phases)

Create `phases-my-pipeline.yaml` in your project root:

```yaml
phases:
  - name: implement
    tools: [Read, Write, Edit, Glob, Grep, Bash]
    timeout: 25m

  - name: verify
    tools: [Read, Glob, Grep, Bash]
    timeout: 8m

  - name: submit
    tools: ["Bash(git:*)", "Bash(gh:*)"]
    timeout: 3m
```

Then run it:

```bash
soda run 42 --pipeline my-pipeline
```

See [docs/pipelines.md](docs/pipelines.md) for the full tutorial, conditional
phases, and model routing cookbooks.

## Default Pipeline

```
Ticket → Triage → Plan → Implement → Verify → Review → Submit → Monitor → PR
```

| Phase | What it does | Tools | Timeout | Model |
|-------|-------------|-------|---------|-------|
| **Triage** | Classifies the ticket, identifies relevant files, routes the default pipeline | Read-only | 3m | global |
| **Plan** | Designs the approach, breaks work into atomic tasks | Read-only | 8m | global |
| **Implement** | Writes code, runs tests, commits | Full | 25m | global |
| **Verify** | Runs tests, checks acceptance criteria, reviews code quality | Read + Bash | 8m | global |
| **Review** | Parallel specialist review (Go, AI harness, SRE) | Read + Bash | 12m | global |
| **Submit** | Pushes branch, opens pull request | git + gh/glab | 3m | global |
| **Monitor** | Polls PR for comments and CI, responds and fixes | Full | 10m/round | global |

Conditional phases: **Patch** runs when verify fails (targeted fixes, Sonnet);
**Follow-up** runs when review passes with minor findings (creates follow-up
tickets). All phases, tools, timeouts, and models are configurable in
`phases.yaml`.

## Per-Phase Control

Every knob in the default pipeline is configurable per phase in `phases.yaml`:

| Knob | Scope | Example |
|------|-------|---------|
| Model | Per phase | Sonnet for triage, Opus for implement |
| Tools | Per phase | Read-only for triage, full write for implement |
| Timeout | Per phase | 3m for triage, 25m for implement |
| Skip condition | Per phase | `{{ ne .Complexity "low" }}` |
| Retry policy | Per phase, per error type | 2 transient, 1 parse, 0 semantic |

See [docs/pipelines.md](docs/pipelines.md) and
[docs/configuration.md](docs/configuration.md) for the full reference.

## Status

**Status (v0.5.0):** 185+ default pipeline runs across 6 releases. 67%
first-pass success rate on medium-complexity tickets. Average cost $5–11 per
session. SODA is used to develop itself.

## Cost

| Ticket type | Typical cost |
|-------------|-------------|
| Simple fix (1–3 tasks) | $2–6 |
| Medium feature (4–8 tasks) | $8–15 |
| Full default pipeline | $5–11 average |

Set `max_cost_per_ticket` in `soda.yaml` as a hard safety net. Per-phase
and per-generation limits are also available.

## Quick Start

```bash
soda init                    # generate soda.yaml for this project
soda doctor                  # verify prerequisites are installed
soda run <issue-number>      # run the default pipeline for a GitHub issue
soda run <issue-number> --pipeline quick-fix   # or choose a named pipeline
soda status                  # check active and recent pipelines
soda history <issue-number>  # inspect phase-by-phase results
```

See [docs/quickstart.md](docs/quickstart.md) for a step-by-step walkthrough
including pipeline selection.

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

**Binary download** — copy-paste for your platform:

```bash
# Linux (amd64, with sandbox)
curl -L https://github.com/decko/soda/releases/latest/download/soda-linux-amd64-sandbox -o soda && chmod +x soda

# Linux (arm64)
curl -L https://github.com/decko/soda/releases/latest/download/soda-linux-arm64 -o soda && chmod +x soda

# macOS (Apple Silicon)
curl -L https://github.com/decko/soda/releases/latest/download/soda-darwin-arm64 -o soda && chmod +x soda

# macOS (Intel)
curl -L https://github.com/decko/soda/releases/latest/download/soda-darwin-amd64 -o soda && chmod +x soda
```

Move to your PATH: `sudo mv soda /usr/local/bin/`

See [docs/install.md](docs/install.md) for build-from-source, CGO/sandbox
builds, and checksum verification.

> **Note:** Sandbox features (Landlock, seccomp, cgroups) require the
> `-sandbox` binary (Linux amd64 only). All other binaries work without
> sandbox — it's optional.

## Configuration

SODA uses three config layers:

| Layer | File | Purpose |
|-------|------|---------|
| Project config | `soda.yaml` | Ticket source, model, budget limits, repo settings |
| Pipeline config | `phases.yaml` | Add, remove, or reorder phases; set tools, timeouts, and models |
| Prompt overrides | `~/.config/soda/prompts/<phase>.md` | Customize phase prompts without forking |

Run `soda init` to generate a `soda.yaml` auto-detected from your project.
See [config.example.yaml](config.example.yaml) for a fully annotated reference
and [docs/configuration.md](docs/configuration.md) for the full guide.

## CLI Reference

| Command | Purpose |
|---------|---------|
| `soda run <ticket>` | Run the default pipeline for a ticket |
| `soda run <ticket> --pipeline quick-fix` | Use a named pipeline |
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
| `soda pipelines new <name>` | Scaffold a custom pipeline |
| `soda init` | Generate `soda.yaml` config |

## How It Works

- **Worktree isolation** — a Git worktree is created before any phase runs; all
  phases execute inside it, never in the main checkout
- **Fresh context per phase** — each phase starts a new Claude Code session;
  context resets between phases, no prompt bleed
- **Structured JSON artifact handoff** — each phase writes a validated JSON
  artifact to `.soda/<ticket>/`; the next phase reads it as structured input
- **Rework loop** — when Review flags critical or major findings, the default
  pipeline routes back to Implement (max 2 cycles); prior findings are injected
  to prevent the whack-a-mole pattern
- **Corrective loop** — when Verify fails, a targeted Patch phase runs before
  re-verification; on exhaustion, the policy escalates or stops

## Sandbox

OS-level isolation for each Claude Code session (Landlock, seccomp, cgroups).
See [docs/sandbox.md](docs/sandbox.md) for setup, configuration, and platform
requirements.

## Plugin

Embedded plugin that gives Claude Code knowledge of SODA pipelines and quick
access to `soda` commands. See [docs/plugin.md](docs/plugin.md) for install
instructions and what the plugin provides.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup instructions, pre-commit
hooks, and code style guidelines. All contributions must follow TDD: write
failing tests before writing implementation code.

## License

Apache-2.0 — see [LICENSE](LICENSE)
