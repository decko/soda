---
description: Tail pipeline events in real-time
argument-hint: <ticket-key> [-f]
---

Print formatted pipeline events:

```bash
soda log $ARGUMENTS
```

If no ticket key is provided, ask the user for one.

Use `-f` to tail events in real-time (poll-based follow mode). Without `-f`, prints the existing event log and exits.

Events include phase starts/completions, rework routing, budget warnings, monitor polling, CI status changes, and comment detection.
