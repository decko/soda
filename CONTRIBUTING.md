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

## Release checklist

When upgrading the supported Claude Code CLI version range:

1. **Install** the target CLI version locally.
2. **Run** the full test suite: `CGO_ENABLED=0 go test ./... -count=1`.
3. **Run** `soda doctor` against a real project to confirm no regressions.
4. **Bump** `MaxTestedCLIVersion` in `internal/claude/args.go` to the newly
   validated version.
5. **If** the new CLI introduces flags SODA depends on, bump
   `MinCLIVersion` as well and update the flag timeline comment.
6. **Commit** with a message like
   `chore(claude): bump MaxTestedCLIVersion to X.Y.Z`.
