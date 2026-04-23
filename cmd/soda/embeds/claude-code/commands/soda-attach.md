---
description: Attach to a running SODA pipeline (read-only)
argument-hint: <ticket-key> [--tui] [--from-start] [--events]
---

Attach to a running SODA pipeline to observe output in real-time:

```bash
soda attach $ARGUMENTS
```

If no ticket key is provided, ask the user for one.

Options:
- `--tui` — use the TUI interface (read-only mode)
- `--from-start` — replay events from the beginning
- `--events` — show raw events instead of formatted output

The attached view is read-only — pause, steer, and retry controls are disabled.
