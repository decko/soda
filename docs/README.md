# SODA Documentation

> **Start here.** This index covers user guides, contributor resources, and
> project reference material. Root-level files (`AGENTS.md`, `CLAUDE.md`,
> `CONSTITUTION.md`, `CONTRIBUTING.md`) remain at the project root — this
> directory is for structured, audience-oriented docs.

## User docs

Guides for people running SODA against their own projects.

| Document | Description | Status |
|----------|-------------|--------|
| [quickstart.md](quickstart.md) | Install, configure, and run your first ticket end-to-end | planned (#367) |
| [install.md](install.md) | All installation methods (binary, Go install, Docker) | planned (#368) |
| [configuration.md](configuration.md) | `soda.yaml` and `phases.yaml` reference | planned |
| [cli-reference.md](cli-reference.md) | Full CLI command reference | planned |
| [pipelines.md](pipelines.md) | Named pipelines, custom pipelines, phase customization | planned |
| [plugin.md](plugin.md) | Claude Code plugin: install, commands, agent | planned |
| [troubleshooting.md](troubleshooting.md) | Top failure modes and fixes | planned |

## Contributor docs

Resources for people working on SODA itself.

| Document | Description | Status |
|----------|-------------|--------|
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | Setup, pre-commit hooks, code style | available |
| [../AGENTS.md](../AGENTS.md) | Full project context: architecture, conventions, gotchas | available |
| [architecture.md](architecture.md) | High-level architecture overview for new contributors | planned |
| [internal/](internal/) | Internal design specs and implementation plans | available |

## Project reference

Files that define project scope, principles, and history.

| Document | Description | Audience |
|----------|-------------|---------|
| [../README.md](../README.md) | Project landing page | user |
| [../CONSTITUTION.md](../CONSTITUTION.md) | Project principles and values | contributor |
| [../CLAUDE.md](../CLAUDE.md) | Claude Code auto-loaded summary | contributor (AI) |
| [../AGENTS.md](../AGENTS.md) | AI agent context | contributor (AI) |
| [../CHANGELOG.md](../CHANGELOG.md) | Release history (#369) | user / contributor |
| [../config.example.yaml](../config.example.yaml) | Fully annotated configuration example | user |
| [../phases.yaml](../phases.yaml) | Pipeline phase definitions | user / contributor |

## Internal design specs

Historical specs and implementation plans live in [`internal/`](internal/).
These are working documents written during development — not user guides.

- [`internal/specs/`](internal/specs/) — design specifications
- [`internal/plans/`](internal/plans/) — implementation plans
