---
description: Render a phase prompt template for a ticket
argument-hint: --phase <phase> --ticket <key>
---

Render a phase prompt template for a given ticket:

```bash
soda render-prompt $ARGUMENTS
```

If no ticket key or phase is provided, ask the user for them.

Arguments are passed as `--ticket <key> --phase <phase>`. Useful for debugging prompt templates without executing a pipeline.
