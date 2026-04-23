---
description: Show phase details for a ticket
argument-hint: <ticket-key> [--detail] [--phase <name>]
---

Show phase details for a SODA pipeline ticket:

```bash
soda history $ARGUMENTS
```

If no ticket key is provided, ask the user for one.

This shows the phase-by-phase execution history including status, duration, cost, and any errors. Use `--detail` for full structured JSON output or `--phase <name>` to drill into a specific phase.
