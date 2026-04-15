# Contributing to SODA

## Prerequisites

- Go 1.25+
- Git 2.9+ (for `core.hooksPath` support)

## Getting started

```bash
git clone <repo-url>
cd soda
./scripts/setup-hooks.sh
```

If you work in [worktrees](https://git-scm.com/docs/git-worktree), run
`setup-hooks.sh` once from any checkout — the setting is stored in
`.git/config`, which all worktrees share, and the tracked `.githooks/`
directory is present in every checkout.

## Pre-commit hooks

After running `setup-hooks.sh`, every `git commit` triggers a pre-commit
hook that checks **staged `.go` files only**:

| Check | What it does |
|-------|-------------|
| `gofmt -l` | Lists files that aren't `gofmt`-formatted |
| `go vet` | Runs `go vet` on packages containing staged files |

The hook exits early (no-op) when no `.go` files are staged.

The hook temporarily stashes unstaged changes (`git stash --keep-index`)
so that checks run against exactly what is being committed, not what
happens to be on disk. The stash is automatically restored after the
checks finish (even if they fail). **Always re-stage after formatting** —
if you run `gofmt -w` to fix a file, `git add` it again before
committing.

`staticcheck` is intentionally excluded from the hook — it runs in CI
only because it is slower and requires a separate install.

### Skipping the hook

Three ways to skip the pre-commit checks:

1. **`--no-verify`** — built-in git flag:
   ```bash
   git commit --no-verify -m "wip"
   ```
2. **`SKIP_HOOKS=1`** — environment variable:
   ```bash
   SKIP_HOOKS=1 git commit -m "wip"
   ```
3. **Don't run setup** — if you never run `./scripts/setup-hooks.sh`, the
   hook is never activated.

## Code style

- Format with `gofmt` (no extra flags).
- Lint with `go vet` and `staticcheck`.
- Test with `go test ./...`.
- Build with `go build -o soda ./cmd/soda`.

See [AGENTS.md](AGENTS.md) for the full list of conventions.
