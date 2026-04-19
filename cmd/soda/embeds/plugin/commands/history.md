---
description: Show phase details for a ticket
---

Show phase details for a SODA pipeline ticket:

```bash
soda history $ARGUMENTS
```

If no ticket key is provided, ask the user for one.

This shows the phase-by-phase execution history including status, duration, cost, and any errors. Use `--detail` for full structured output or `--phase <name>` to drill into a specific phase.
