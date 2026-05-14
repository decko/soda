# Pipelines

SODA's pipeline is fully configurable. You can add, remove, or reorder phases;
swap models per phase; scope tools per phase; and add conditional logic to skip
phases based on ticket metadata. This guide walks you through building a custom
pipeline from scratch.

---

## Built-in pipelines

| Name | Phases | Use case |
|------|--------|----------|
| `default` | triage → plan → implement → verify → review → submit → monitor | Full pipeline for standard tickets |
| `quick-fix` | implement → verify → submit | Small, well-understood fixes |
| `docs-only` | plan → implement → submit | Documentation changes (Sonnet) |

```bash
soda pipelines                       # list all available pipelines
soda run 42                          # use the default pipeline
soda run 42 --pipeline quick-fix
soda run 42 --pipeline docs-only
```

---

## Design your pipeline

### Step 1 — Scaffold a pipeline file

```bash
soda pipelines new my-pipeline              # creates phases-my-pipeline.yaml in the current directory
soda pipelines new my-pipeline --global     # creates ~/.config/soda/phases-my-pipeline.yaml
```

### Step 2 — Edit the generated file

A pipeline file is a list of phase definitions. Each phase specifies its tools,
timeout, model, and (optionally) conditional logic.

**Minimal pipeline (3 phases):**

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

### Step 3 — Run it

```bash
soda run 42 --pipeline my-pipeline
```

SODA resolves `phases-my-pipeline.yaml` from the current directory first,
then `~/.config/soda/phases-my-pipeline.yaml`, then embedded defaults.

---

## Phase field reference

```yaml
phases:
  - name: string            # phase identifier; used in logs, state files, and artifacts
    type: string            # "corrective", "post-submit", "parallel-review", "polling"
                            # omit for a normal forward phase
    prompt: string          # prompt template path (e.g. "prompts/implement.md")
                            # omit to use the auto-resolved embedded default for this phase name
    model: string           # per-phase model override
                            # omit to use the global model from soda.yaml
    tools: [string]         # allowed tools list passed to Claude Code as --allowed-tools
    timeout: duration       # phase timeout (e.g. "25m", "3m")
    condition: string       # Go template expression; phase is skipped when it evaluates to "false"
                            # (see Conditional phases cookbook below)
    depends_on: [string]    # phase names that must complete before this phase runs
    feedback_from: [string] # upstream phases whose output is injected as rework feedback
    retry:
      transient: int        # retries for transient API errors (default: 2)
      parse: int            # retries when output fails JSON schema validation (default: 1)
      semantic: int         # retries when output is valid but semantically wrong (default: 1)
```

### Tool reference

| Tool | What it allows |
|------|---------------|
| `Read` | Read files |
| `Write` | Write files |
| `Edit` | In-place edits |
| `Glob` | File pattern matching |
| `Grep` | Content search |
| `Bash` | Unrestricted shell |
| `Bash(git:*)` | Git subcommands only |
| `Bash(gh:*)` | GitHub CLI only |
| `Bash(glab:*)` | GitLab CLI only |
| `Bash(ls:*)` | Directory listing only |
| `Bash(go test:*)` | Go test invocations only |

---

## Conditional phases cookbook

Use the `condition` field to skip a phase based on ticket metadata. The
condition is a Go template expression evaluated against pipeline state; the
phase is skipped when the expression evaluates to the string `"false"`.

Available template variables mirror the `PromptData` struct — see
[docs/configuration.md](configuration.md#prompt-overrides) for the full list.
The most useful ones for conditions are:

| Variable | Type | Example values |
|----------|------|---------------|
| `{{.Complexity}}` | string | `"low"`, `"medium"`, `"high"` |
| `{{.TicketType}}` | string | `"bug"`, `"feature"`, `"docs"` |
| `{{.Artifacts.Triage}}` | JSON string | triage output (parsed by template funcs) |

### Skip plan for low-complexity tickets

When triage classifies a ticket as `low` complexity, skip the planning phase —
the implementation is straightforward enough to go straight to code.

```yaml
  - name: plan
    condition: '{{ ne .Complexity "low" }}'
    tools: [Read, Glob, Grep, "Bash(git:*)"]
    timeout: 8m
```

### Skip review for docs-only changes

Documentation changes rarely need code review from specialist agents. Skip the
review phase when the ticket type is `docs`.

```yaml
  - name: review
    condition: '{{ ne .TicketType "docs" }}'
    type: parallel-review
    timeout: 12m
    reviewers:
      - name: go-specialist
        prompt: prompts/review-go.md
```

### Only run AI harness reviewer for high-complexity tickets

Specialist AI harness review is expensive. Reserve it for complex tickets where
prompt engineering is most likely to be the failure mode.

```yaml
  - name: review
    type: parallel-review
    reviewers:
      - name: go-specialist
        prompt: prompts/review-go.md
      - name: ai-harness
        prompt: prompts/review-ai-harness.md
        condition: '{{ eq .Complexity "high" }}'
```

### Skip monitor for a quick-fix pipeline

Quick fixes don't need continuous PR monitoring — just submit and move on.
Omit the monitor phase entirely from the pipeline definition.

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

  # monitor phase omitted — pipeline ends after submit
```

---

## Model routing cookbook

### Cheap phases with Sonnet, expensive phases with Opus

Use fast, cheap models for triage and submit; reserve the most capable model
for implement and review.

```yaml
phases:
  - name: triage
    model: claude-sonnet-4-20250514   # fast and cheap for classification
    tools: [Read, Glob, Grep, "Bash(git:*)", "Bash(ls:*)"]
    timeout: 3m

  - name: plan
    model: claude-sonnet-4-20250514
    tools: [Read, Glob, Grep, "Bash(git:*)"]
    timeout: 8m

  - name: implement
    # no model override — uses global model (e.g. Opus) from soda.yaml
    tools: [Read, Write, Edit, Glob, Grep, Bash]
    timeout: 25m

  - name: verify
    # no model override
    tools: [Read, Glob, Grep, Bash]
    timeout: 8m

  - name: submit
    model: claude-sonnet-4-20250514   # no heavy reasoning needed for submission
    tools: ["Bash(git:*)", "Bash(gh:*)"]
    timeout: 3m
```

Set the global model in `soda.yaml`:

```yaml
model: claude-opus-4-20251101   # used for phases with no per-phase override
```

### Corrective patch phase with Sonnet

The corrective patch phase can use a cheaper model for targeted fixes, falling
back to the global model for full implement escalation.

```yaml
  - name: patch
    type: corrective
    model: claude-sonnet-4-20250514   # fast targeted fixes
    tools: [Read, Write, Edit, Glob, Grep, Bash]
    timeout: 8m
```

This is the default pipeline's approach — Sonnet for quick targeted fixes,
Opus reserved for full implement sessions.

### Per-reviewer model selection

Different reviewers can use different models:

```yaml
  - name: review
    type: parallel-review
    reviewers:
      - name: go-specialist
        prompt: prompts/review-go.md
        # no model override — uses global model
      - name: ai-harness
        prompt: prompts/review-ai-harness.md
        model: claude-sonnet-4-20250514   # cheaper for harness review
      - name: sre
        prompt: prompts/review-sre.md
        model: claude-sonnet-4-20250514
```

---

## Annotated walkthroughs

### `quick-fix` pipeline

Skips triage, plan, review, and monitor. Assumes you know what to fix.
Use when you've diagnosed the problem and want to automate the mechanical work.

```yaml
phases:
  - name: implement
    tools: [Read, Write, Edit, Glob, Grep, Bash]
    timeout: 15m
    retry:
      transient: 2
      parse: 1
      semantic: 0   # fail fast — no semantic retry on implement

  - name: verify
    tools: [Read, Glob, Grep, Bash]
    timeout: 8m
    corrective:
      phase: patch
      max_attempts: 1
      on_exhausted: stop

  - name: patch
    type: corrective
    model: claude-sonnet-4-20250514
    tools: [Read, Write, Edit, Glob, Grep, Bash]
    timeout: 8m

  - name: submit
    tools: ["Bash(git:*)", "Bash(gh:*)"]
    timeout: 3m
```

**When to use:** Bug fixes where you've already diagnosed the problem. API
changes with obvious call-site updates. Dependency upgrades with known
migration paths.

### `docs-only` pipeline

Uses Sonnet throughout (docs don't need Opus). Skips triage and all
testing/review phases.

```yaml
phases:
  - name: plan
    model: claude-sonnet-4-20250514
    tools: [Read, Glob, Grep, "Bash(git:*)"]
    timeout: 5m

  - name: implement
    model: claude-sonnet-4-20250514
    tools: [Read, Write, Edit, Glob, Grep, Bash]
    timeout: 10m

  - name: submit
    model: claude-sonnet-4-20250514
    tools: ["Bash(git:*)", "Bash(gh:*)"]
    timeout: 3m
```

**When to use:** README updates, changelog entries, docstring improvements,
configuration reference updates. Any change where the test suite doesn't cover
correctness of the output.

---

## Using the pipeline-architect agent

The `pipeline-architect` agent is a design-only Claude Code agent that proposes
a custom pipeline configuration based on your project and requirements. It
analyzes your tech stack and suggests phases, reviewers, timeouts, and model
selection calibrated to your codebase.

Install the SODA plugin first:

```bash
soda plugin install
```

Then, in a Claude Code session:

```
@pipeline-architect I need a pipeline for reviewing and merging dependency
updates. Run tests, check for breaking changes, and submit without full review.
```

The agent outputs a complete `phases-<name>.yaml` you can copy into your
project and run immediately with `soda run <ticket> --pipeline <name>`.

> **Note:** The pipeline-architect agent only *designs* pipelines — it does not
> execute them. It will not write any files.

---

## Discovery order

SODA resolves named pipelines (`phases-<name>.yaml`) in this order (first found
wins):

1. Current working directory (`./phases-<name>.yaml`)
2. User config directory (`~/.config/soda/phases-<name>.yaml`)
3. Embedded defaults (compiled into the binary)

The default pipeline (`phases.yaml`) also checks `phases_path` in `soda.yaml`
before the current working directory.

For the full `phases.yaml` field reference including rework routing, corrective
routing, parallel review, and polling configuration, see
[docs/configuration.md](configuration.md#phasesyaml-reference).
