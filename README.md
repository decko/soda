# SODA — Session-Orchestrated Development Agent

A CLI/TUI that orchestrates AI coding sessions through a pipeline to implement tickets end-to-end.

```
Ticket → Triage → Plan → Implement → Verify → Submit → Monitor → Done
```

Each phase runs in a fresh, sandboxed Claude Code session with structured output.
State lives on disk. Context resets between phases. The agent runs inside
OS-level isolation (Landlock, network namespaces, cgroups).

## Installation

### Go install

```bash
go install github.com/decko/soda/cmd/soda@latest
```

Requires Go 1.25+. The installed binary runs without sandbox enforcement (`CGO_ENABLED=0`).

### Binary download

Pre-built binaries are available on the
[GitHub Releases](https://github.com/decko/soda/releases) page.

| Binary | Platform | Sandbox |
|--------|----------|---------|
| `soda-linux-amd64-sandbox` | Linux x86_64 | ✅ full isolation |
| `soda-linux-arm64` | Linux ARM64 | ❌ no sandbox |
| `soda-darwin-amd64` | macOS Intel | ❌ no sandbox |
| `soda-darwin-arm64` | macOS Apple Silicon | ❌ no sandbox |

Binaries with `-sandbox` include kernel-enforced process isolation
(Landlock + seccomp + cgroups). Without it, soda runs normally but
skips sandbox enforcement.

**Linux (amd64, with sandbox):**

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-linux-amd64-sandbox -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

**Linux (arm64):**

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-linux-arm64 -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

**macOS (Apple Silicon):**

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-darwin-arm64 -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

**macOS (Intel):**

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-darwin-amd64 -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

Verify the download against `checksums.txt` included in each release.

### Build from source

```bash
git clone https://github.com/decko/soda
cd soda
CGO_ENABLED=0 go build -o soda ./cmd/soda
```

`CGO_ENABLED=0` produces a fully static binary without sandbox support.
The sandbox (Landlock + seccomp + cgroups) requires CGO and the
`go-arapuca` native library — see [docs/install.md](docs/install.md)
for CGO build instructions.

## Status

Design phase. Not yet implemented.

## Getting Started

Generate a starter configuration file:

```bash
soda init                        # auto-detect project stack, write soda.yaml
soda init --dry-run              # preview generated config without writing
soda init --phases               # also write phases.yaml alongside the config
soda init --no-gitignore         # skip adding .soda/.worktrees to .gitignore
soda init --force                # overwrite existing config
soda init -o ./my-config.yaml    # write to a custom path
```

The generated file auto-detects your project's language, forge, and
tooling from the repository. Edit it to match your project before
running the pipeline. See [config.example.yaml](config.example.yaml)
for a fully annotated reference.

## Architecture

```
soda (Go TUI + Pipeline Engine)
  └── agentic-orchestrator sandbox
       ├── Landlock filesystem isolation
       ├── Network namespace isolation
       ├── cgroup resource limits
       └── claude --print --bare --output-format json --json-schema ...
```

## Design Documents

- `phases.yaml` — Pipeline phase configuration (tools, timeouts, retries, dependencies)
- `prompts/` — Phase prompt templates (Go templates)
- `schemas/` — Structured output schemas per phase (Go structs → JSON Schema)
- `config.example.yaml` — User configuration example

## Claude Code Integration

SODA ships an embedded Claude Code plugin that gives Claude knowledge of SODA
pipelines and quick access to soda commands.

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
| **Skill: `soda-pipeline`** | Teaches Claude about pipeline architecture, phase lifecycle, state management, and troubleshooting |
| **`/soda:run <ticket>`** | Run the pipeline for a ticket |
| **`/soda:status`** | Show current pipeline status |
| **`/soda:sessions`** | List previous pipeline sessions |
| **Agent: `pipeline-architect`** | Design-only agent that proposes a `phases.yaml` for the current project |

The plugin is auto-discovered by Claude Code from `.claude/plugins/`. Plugin
files are embedded in the soda binary and version-matched — updates come with
`go install`.

## Key Design Decisions

- **Go + bubbletea** for the CLI/TUI
- **Claude Code CLI** as the inner agent (`--bare` mode for full context control)
- **agentic-orchestrator** sandbox for OS-level isolation
- **Structured output** via `--json-schema` (no regex parsing)
- **Disk-based state** in `.soda/<ticket>/` (crash recovery, resume)
- **Pluggable ticket sources** (Jira first, GitHub later)
- **Config-driven phases** (add, remove, reorder via YAML)

## Documentation

Full documentation lives in [`docs/`](docs/README.md):

- **[Quickstart](docs/quickstart.md)** — install, configure, run your first ticket
- **[CLI reference](docs/cli-reference.md)** — all commands and flags
- **[Configuration](docs/configuration.md)** — `soda.yaml` and `phases.yaml` reference
- **[Pipelines](docs/pipelines.md)** — named pipelines and phase customization
- **[Troubleshooting](docs/troubleshooting.md)** — common failure modes and fixes

See [`docs/README.md`](docs/README.md) for the full documentation index.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup instructions, pre-commit
hooks, and code style guidelines.

## License

Apache-2.0 — see [LICENSE](LICENSE)
