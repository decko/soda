# Claude Code Plugin

SODA ships an embedded plugin that gives Claude Code knowledge of SODA
pipelines and quick access to `soda` commands. Install it once; updates arrive
with `go install` or a new binary download.

## Install

```bash
soda plugin install              # project-local: .claude/plugins/soda/
soda plugin install --global     # global: ~/.claude/plugins/soda/
```

Project-local installs apply only when Claude Code is opened from that project
directory. Global installs apply everywhere.

## Uninstall

```bash
soda plugin uninstall            # remove project-local plugin
soda plugin uninstall --global   # remove global plugin
```

## What the plugin provides

| Component | Description |
|-----------|-------------|
| **Skill: `soda-pipeline`** | Pipeline architecture, phase lifecycle, state management, troubleshooting |
| **Skill: `orchestrate`** | Milestone-level coordination: dependency ordering, label lifecycle, SODA dispatch, cost tracking, progress reporting |
| **`/soda:run <ticket>`** | Run the default pipeline for a ticket |
| **`/soda:status`** | Show current pipeline status |
| **`/soda:sessions`** | List previous pipeline sessions |
| **Agent: `pipeline-architect`** | Design-only agent that proposes a custom `phases.yaml` based on your project and requirements |

## The pipeline-architect agent

The `pipeline-architect` agent is available after installing the plugin. Invoke
it in a Claude Code session:

```
@pipeline-architect I want a pipeline that skips triage for small tickets
and uses Sonnet for everything except implement.
```

The agent analyzes your project structure and outputs a ready-to-use
`phases-<name>.yaml`. It does not execute the pipeline or write any files —
it only designs and proposes.

See [docs/pipelines.md](pipelines.md#using-the-pipeline-architect-agent) for
more detail.

## Plugin files

Plugin files are embedded in the soda binary at build time. The plugin
directory structure after install:

```
.claude/plugins/soda/
├── skills/
│   ├── soda-pipeline/
│   │   ├── SKILL.md
│   │   └── RUNBOOK.md
│   └── orchestrate/
│       └── SKILL.md
├── commands/
│   ├── soda-run.md
│   ├── soda-status.md
│   └── soda-sessions.md
└── agents/
    └── pipeline-architect.md
```
