---
description: Run the full test suite
---

Run all tests for the SODA project:

```bash
go test ./...
```

Note: `internal/sandbox` tests may fail due to Git LFS requirements for go-arapuca. This is expected in environments without LFS — ignore that package's failures.

Report the test results, highlighting any failures.
