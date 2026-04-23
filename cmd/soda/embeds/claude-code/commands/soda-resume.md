---
description: Resume a failed or interrupted SODA pipeline
argument-hint: <ticket-key> [--from <phase>]
---

Resume a SODA pipeline from the last failed or interrupted phase:

```bash
soda run $ARGUMENTS --from last
```

If a specific phase is provided in $ARGUMENTS, use it as the `--from` value instead of `last`.

If no ticket key is provided, ask the user for one. You can check `soda status` or `soda sessions` to find recent tickets.
