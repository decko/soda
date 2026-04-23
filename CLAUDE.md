# CLAUDE.md

This file is loaded automatically by Claude Code when working on this project.
For comprehensive project documentation, see [AGENTS.md](AGENTS.md).
For non-negotiable principles and design rules, see [CONSTITUTION.md](CONSTITUTION.md).

## What is SODA

**Session-Orchestrated Development Agent** — a Go CLI/TUI that orchestrates AI coding sessions through a pipeline of phases (triage → plan → implement → verify → review → submit → monitor) to implement tickets end-to-end.

## Essential commands

```bash
# Build
CGO_ENABLED=0 go build -o soda ./cmd/soda

# Test
go test ./...

# Format
gofmt -w .

# Lint
go vet ./...

# Generate schemas (after modifying structs in schemas/)
go generate ./schemas/...

# Set up pre-commit hooks (once)
./scripts/setup-hooks.sh
```

## Conventions

- **Go 1.25** — standard library preferred over third-party packages
- **Formatting**: `gofmt` only (no extra flags)
- **Errors**: always wrap with `fmt.Errorf("context: %w", err)`, never discard
- **Interfaces**: define at the consumer, not the producer; keep minimal
- **Variables**: no single-char names; use descriptive names in loops and closures
- **Tests**: functional tests over mocks; table-driven where appropriate; TDD (write tests first)

## Git workflow

- Never commit directly on main — always use a feature branch
- Work in worktrees: `git worktree add .worktrees/<branch> -b <branch> main`
- Stage specific files with `git add <file>`, never `git add .` or `git add -A`
- Add `Assisted-by:` trailer naming the model used

## Project structure

```
cmd/soda/              CLI entrypoint + embedded content (prompts, phases.yaml)
internal/claude/       Claude Code CLI wrapper (args, parser, runner)
internal/config/       YAML config loading
internal/pipeline/     Phase engine, state, prompts, events, monitor
internal/runner/       Runner interface + Claude runner implementation
internal/sandbox/      Sandboxed execution via agentic-orchestrator
internal/ticket/       Pluggable ticket sources (GitHub, Jira)
internal/tui/          Bubbletea TUI (view layer only — no business logic)
schemas/               Structured output schemas (Go structs → JSON Schema)
```

## Key design decisions

- `--bare` mode eliminates context duplication; SODA controls the full context window
- Disk-based state (`.soda/<ticket>/`) enables crash recovery and resume
- Config-driven phases via `phases.yaml` — users can add/remove/reorder
- Prompt overrides: `~/.config/soda/prompts/<phase>.md` overrides embedded prompts
- Root `phases.yaml` overrides the embedded copy without rebuild
