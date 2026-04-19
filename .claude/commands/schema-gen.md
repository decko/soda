---
description: Regenerate JSON schemas from Go structs
---

Regenerate the JSON schema definitions from Go struct types in the `schemas/` package:

```bash
go generate ./schemas/...
```

This must be run after modifying any struct in `schemas/` (e.g., TriageOutput, PlanOutput, ImplementOutput, etc.). The generated schemas are used by `phases.yaml` and the pipeline engine for structured output validation.

After generation, verify the output compiles:
```bash
go build ./schemas/...
```

Report which schemas were regenerated and whether generation succeeded.
