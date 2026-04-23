---
description: Resume a failed or interrupted SODA pipeline
argument-hint: <ticket-key> [--from <phase>]
---

Resume a SODA pipeline from the last failed or interrupted phase:

```bash
nohup soda run $ARGUMENTS --from last > /tmp/soda-<ticket>.log 2>&1 &
```

Replace `<ticket>` with the actual ticket key. If a specific phase is provided in $ARGUMENTS, use it as the `--from` value instead of `last`.

The pipeline runs in the background. **Do not block waiting for it.** Instead, poll progress:

```
/loop 2m soda status
```

If no ticket key is provided, ask the user for one. You can check `soda status` or `soda sessions` to find recent tickets.
