---
description: Run formatting and linting checks
---

Run formatting and linting checks on the SODA codebase:

1. Check formatting:
```bash
gofmt -l .
```

2. Run vet:
```bash
go vet ./...
```

If `gofmt -l` reports any files, fix them with `gofmt -w .` and report which files were reformatted.

Report any vet findings.
