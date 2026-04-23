---
description: List available named pipelines
---

List all available named pipeline definitions:

```bash
soda pipelines
```

Shows pipelines discovered from:
1. `./pipelines/` (project-level)
2. `~/.config/soda/pipelines/` (user-level)
3. Built-in pipelines (quick-fix, docs-only)

Use `soda run <ticket> --pipeline <name>` to run a specific pipeline, or `soda pipelines new <name>` to scaffold a new one.
