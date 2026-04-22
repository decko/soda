---
description: Generate a ticket specification from a description
---

Generate a well-structured ticket specification:

```bash
soda spec $ARGUMENTS
```

If no description is provided, ask the user to describe what they want to build.

Claude will scan the codebase (read-only) and generate a complete ticket body with:
- Summary and acceptance criteria
- Context to read / Do NOT read sections
- Token budget estimation
- Suggested labels

Use `--from-file <path>` for longer descriptions, `--yes` to auto-create the issue, or `--dry-run` to preview the prompt.
