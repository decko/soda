# SODA — Session-Orchestrated Development Agent

A CLI/TUI that orchestrates AI coding sessions through a pipeline to implement tickets end-to-end.

```
Ticket → Triage → Plan → Implement → Verify → Submit → Monitor → Done
```

Each phase runs in a fresh, sandboxed Claude Code session with structured output.
State lives on disk. Context resets between phases. The agent runs inside
OS-level isolation (Landlock, network namespaces, cgroups).

## Status

Design phase. Not yet implemented.

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

## Key Design Decisions

- **Go + bubbletea** for the CLI/TUI
- **Claude Code CLI** as the inner agent (`--bare` mode for full context control)
- **agentic-orchestrator** sandbox for OS-level isolation
- **Structured output** via `--json-schema` (no regex parsing)
- **Disk-based state** in `.soda/<ticket>/` (crash recovery, resume)
- **Pluggable ticket sources** (Jira first, GitHub later)
- **Config-driven phases** (add, remove, reorder via YAML)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup instructions, pre-commit
hooks, and code style guidelines.

## License

Apache-2.0 — see [LICENSE](LICENSE)
