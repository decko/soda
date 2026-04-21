---
description: Clean completed/failed pipeline worktrees and branches
---

Clean completed or failed SODA pipeline worktrees and branches:

```bash
soda clean $ARGUMENTS
```

If no ticket key is provided, ask the user for one or suggest using `--all` to clean all terminal pipelines.

By default, this removes worktrees and branches but **preserves session data** (`.soda/<ticket>/`) for raki metrics. Use `--purge` to remove everything including session data.
