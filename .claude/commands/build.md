---
description: Build the SODA binary
---

Build the SODA binary with CGO disabled:

```bash
CGO_ENABLED=0 go build -o soda ./cmd/soda
```

Report whether the build succeeded or failed, and any compilation errors.
