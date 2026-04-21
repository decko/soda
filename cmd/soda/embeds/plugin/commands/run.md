---
description: Run the SODA pipeline for a ticket
---

Run the SODA pipeline for a ticket:

```bash
soda run $ARGUMENTS
```

If no ticket key is provided, ask the user for one.

This will execute all pipeline phases: triage → plan → implement → verify → review → submit.

Use `--pipeline <name>` to select a named pipeline (e.g., `quick-fix`, `docs-only`). Run `soda pipelines` to list available pipelines.
