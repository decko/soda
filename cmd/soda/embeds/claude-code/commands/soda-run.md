---
description: Run the SODA pipeline for a ticket
argument-hint: <ticket-key> [--pipeline <name>] [--from <phase>] [--mode checkpoint]
---

Run the SODA pipeline for a ticket:

```bash
nohup soda run $ARGUMENTS > /tmp/soda-<ticket>.log 2>&1 &
```

Replace `<ticket>` with the actual ticket key in the log path.

The pipeline runs in the background. **Do not block waiting for it.** Instead, set up a polling loop to check progress:

```
/loop 2m soda status
```

Or use `soda log <ticket> -f` in a separate terminal. Use `soda attach <ticket>` to observe in real-time.

If no ticket key is provided, ask the user for one.

This will execute all pipeline phases: triage → plan → implement → verify → review → submit → monitor.

Options:
- `--pipeline <name>` — select a named pipeline (e.g., `quick-fix`, `docs-only`). Run `soda pipelines` to list available pipelines.
- `--from <phase>` — resume from a specific phase (`last` auto-resolves to the last running/failed phase)
- `--mode checkpoint` — pause after each phase for confirmation
- `--dry-run` — render prompts without executing
