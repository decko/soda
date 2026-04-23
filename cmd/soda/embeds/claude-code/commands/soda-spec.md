---
description: Generate a ticket specification from a description
argument-hint: "<description>" [--from-file <path>] [--yes] [--dry-run]
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

Options:
- `--from-file <path>` — read description from a file (for longer specs)
- `--yes` — auto-create the GitHub issue without confirmation
- `--dry-run` — preview the prompt without executing
