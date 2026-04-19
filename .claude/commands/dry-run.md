---
description: Render pipeline prompts for a ticket without executing
---

Render all phase prompts for a ticket to inspect the full context that would be sent to Claude Code, without actually running the pipeline:

```bash
./soda run $ARGUMENTS --dry-run
```

If no ticket key is provided, ask the user for one.

This is useful for:
- Debugging prompt templates
- Verifying template rendering with real ticket data
- Checking context size before a real run
