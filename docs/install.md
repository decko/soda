# Installation

This page covers all installation methods for soda.

## Requirements

- **Claude Code CLI >= 2.1.81** — soda spawns `claude` in each pipeline phase.
  Install it from [claude.ai/code](https://claude.ai/code). Run `soda doctor`
  to verify your version meets the minimum requirement.
- **Go 1.25+** — required for `go install` or building from source.
- **git** — required for worktree management and commits.
- **gh** (GitHub) or **glab** (GitLab) — required for PR creation and
  the monitor phase.

---

## Go install

```bash
go install github.com/decko/soda/cmd/soda@latest
```

This installs the latest tagged release into `$GOPATH/bin` (or
`$HOME/go/bin`). The binary is built with `CGO_ENABLED=0` and runs
without sandbox enforcement.

---

## Binary download

Pre-built binaries are published to the
[GitHub Releases](https://github.com/decko/soda/releases) page on
every tagged release.

### Available binaries

| Binary | Platform | Sandbox |
|--------|----------|---------|
| `soda-linux-amd64-sandbox` | Linux x86_64 | ✅ full isolation |
| `soda-linux-arm64` | Linux ARM64 | ❌ no sandbox |
| `soda-darwin-amd64` | macOS Intel | ❌ no sandbox |
| `soda-darwin-arm64` | macOS Apple Silicon | ❌ no sandbox |

Binaries with `-sandbox` in the name include kernel-enforced process
isolation (Landlock + seccomp + cgroups) via the `go-arapuca` library.
Without it, soda runs normally but skips sandbox enforcement — the
agent is still bounded by `--allowed-tools` and `--permission-mode`,
but there is no OS-level isolation.

The `-sandbox` binary is currently only available for Linux amd64
because cross-compiling CGO to other targets requires a matching
toolchain on the build host.

### Platform instructions

**Linux (amd64, with sandbox):**

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-linux-amd64-sandbox -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

**Linux (arm64):**

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-linux-arm64 -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

**macOS (Apple Silicon / arm64):**

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-darwin-arm64 -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

**macOS (Intel / amd64):**

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-darwin-amd64 -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

### Verify checksums

Each release includes a `checksums.txt` with SHA-256 digests for all
binaries. Download and verify before installing:

```bash
# Download the binary and checksums for your platform (example: linux-amd64-sandbox)
curl -L https://github.com/decko/soda/releases/latest/download/soda-linux-amd64-sandbox -o soda
curl -L https://github.com/decko/soda/releases/latest/download/checksums.txt -o checksums.txt

# Verify
grep soda-linux-amd64-sandbox checksums.txt | sha256sum --check
```

---

## Build from source

### Without sandbox (CGO_ENABLED=0)

The simplest build — produces a fully static binary, no CGO
dependencies required:

```bash
git clone https://github.com/decko/soda
cd soda
CGO_ENABLED=0 go build -o soda ./cmd/soda
sudo mv soda /usr/local/bin/
```

This is the recommended method for macOS and Linux arm64 where
cross-compiled CGO toolchains are not readily available.

> **Note:** `CGO_ENABLED=0` disables sandbox enforcement at build time.
> The stub in `internal/sandbox/runner_nocgo.go` returns an error at
> runtime if the sandbox is invoked — soda will still work, but each
> phase runs without OS-level isolation.

### With sandbox (CGO_ENABLED=1, Linux amd64 only)

The sandbox requires the `go-arapuca` native library (`libarapuca.a`),
which is stored in the `go-arapuca` module via Git LFS. Standard
`go mod download` does **not** fetch LFS objects.

1. Install `git-lfs` and fetch the library:

   ```bash
   git lfs install
   go mod download github.com/sergio-correia/go-arapuca
   ```

   If `git-lfs` is not available, fetch the binary directly from the
   LFS batch API — see the [release workflow](.github/workflows/release.yaml)
   for the exact curl commands used in CI.

2. Build with CGO enabled:

   ```bash
   git clone https://github.com/decko/soda
   cd soda
   CGO_ENABLED=1 go build -o soda ./cmd/soda
   sudo mv soda /usr/local/bin/
   ```

   A C compiler (`gcc`) must be present on the build host.

---

## Verify the installation

```bash
soda version
```

Then generate a starter config for your project:

```bash
soda init
```

See [quickstart.md](quickstart.md) for next steps.
