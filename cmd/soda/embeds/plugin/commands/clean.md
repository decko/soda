---
description: Clean completed/failed pipeline state and worktrees
---

Clean completed or failed SODA pipeline state and worktrees:

```bash
soda clean $ARGUMENTS
```

If no ticket key is provided, ask the user for one or suggest using `--all` to clean all terminal pipelines.

This removes worktrees, branches, and state directories for pipelines that have completed or failed.
