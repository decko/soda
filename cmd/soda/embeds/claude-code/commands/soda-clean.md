---
description: Clean pipeline worktrees, branches, and optionally session data
argument-hint: <ticket-key>|--all [--purge] [--force] [--dry-run]
---

Clean completed or failed SODA pipeline worktrees and branches:

```bash
soda clean $ARGUMENTS
```

If no ticket key is provided, ask the user for one or suggest using `--all` to clean all terminal pipelines.

By default, this removes worktrees and branches but **preserves session data** (`.soda/<ticket>/`) for raki metrics. Options:
- `--purge` — remove everything including session data
- `--force` — clean dirty worktrees and delete remote branches
- `--all` — clean all completed/failed sessions
- `--dry-run` — preview what would be cleaned
