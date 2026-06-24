# Sandbox

When built with `CGO_ENABLED=1`, SODA uses
[go-arapuca](https://github.com/sergio-correia/go-arapuca) for OS-level
isolation around each Claude Code session.

## What the sandbox enforces

- **Landlock** — filesystem access restricted to the worktree; the agent cannot
  read or write outside the working directory
- **seccomp** — syscall allowlist enforced at the kernel level; blocks syscalls
  the agent has no business making
- **cgroups** — memory, CPU, and PID limits per session; prevents runaway
  resource consumption
- **Network namespace** — Unix sockets only; the agent cannot make outbound
  network calls from inside the sandbox (API calls are proxied through the host)

## Why sandbox?

The `--allowed-tools` flag passed to Claude Code is **advisory** — the model
can choose to ignore it. Landlock, seccomp, and cgroups are kernel-enforced and
cannot be bypassed by the agent. For unattended autonomous execution,
enforcement beats advisory.

## Enabling the sandbox

The sandbox is **optional**. Builds with `CGO_ENABLED=0` run without isolation,
which is appropriate for environments where CGO is unavailable (macOS, Linux
ARM64). The agent is still bounded by `--allowed-tools` and
`--permission-mode bypassPermissions`.

### Download the sandbox binary

Download the `-sandbox` binary (Linux amd64 only) from the
[releases page](https://github.com/decko/soda/releases):

```bash
curl -L https://github.com/decko/soda/releases/latest/download/soda-linux-amd64-sandbox -o soda
chmod +x soda
sudo mv soda /usr/local/bin/
```

### Enable in soda.yaml

```yaml
sandbox:
  enabled: true
  limits:
    memory_mb: 2048      # cgroup memory limit in MiB
    cpu_percent: 200     # cgroup CPU quota as a percentage of one core
    max_pids: 256        # cgroup PID limit
```

### Verify

```bash
soda doctor
```

Look for the `sandbox` check in the output. If it shows `✓`, the sandbox is
active.

## Proxy mode

Enable the proxy to route API calls through a host-side bridge for credential
isolation and token metering. Requires `sandbox.enabled: true`.

```yaml
sandbox:
  enabled: true
  proxy:
    enabled: true
    upstream_url: https://api.anthropic.com   # default; set ANTHROPIC_BASE_URL to override
    max_input_tokens: 0                        # 0 = unlimited per session
    max_output_tokens: 0
    log_dir: ""                                # set to a directory path to enable request/response audit logging
```

When the proxy is enabled, API calls are bridged through a Unix socket. The
host-side proxy injects credentials, so no API key is needed inside the
sandbox.

## MCP server support

When a pipeline phase declares MCP servers (via `mcp_servers` in
`soda.yaml`), the sandbox adapters write the appropriate config files
into the sandbox temp directory so the agent discovers them
automatically:

- **Claude Code**: a temp `soda-mcp-config-*.json` file is written to
  `tmpDir` and passed via `--mcp-config` / `--strict-mcp-config`.
- **Opencode**: MCP server declarations are merged into
  `{tmpDir}/.opencode.json` (since `HOME` is overridden to `tmpDir`).
- **Pi**: MCP is not supported; MCP server declarations are ignored.

### Network isolation trade-offs

MCP servers typically need outbound network access to reach external
APIs (Jira, GitHub, etc.). When MCP servers are configured for a phase,
the sandbox **automatically disables network namespace isolation**
(`UseNetNS` is forced to `false`) and emits a warning to stderr:

```
sandbox: warning: network isolation disabled for phase "implement" because MCP servers are configured
```

This means the sandboxed agent process can make arbitrary outbound
network connections for that phase. The remaining sandbox controls
(Landlock, seccomp, cgroups) are **not affected** — filesystem and
resource isolation remain fully enforced.

**Security implications:**

| Control | With MCP | Without MCP |
|---------|----------|-------------|
| Landlock (filesystem) | ✅ enforced | ✅ enforced |
| seccomp (syscalls) | ✅ enforced | ✅ enforced |
| cgroups (resources) | ✅ enforced | ✅ enforced |
| Network namespace | ❌ disabled | ✅ enforced |

**Mitigations:**

- MCP server binaries are resolved via `exec.LookPath` and their parent
  directories are added as read paths — the sandbox does not grant
  blanket filesystem access.
- MCP config files go to `tmpDir` (cleaned up by `defer os.RemoveAll`),
  not to `WorkDir` — they are not visible to the agent's Read tool.
- The LLM proxy (if enabled) continues to meter API calls and enforce
  token budgets even when the network namespace is disabled.

**Recommendation:** only declare MCP servers for phases that genuinely
need them. Phases without MCP servers retain full network isolation.

## Platform support

| Platform | Sandbox available |
|----------|-------------------|
| Linux amd64 | ✅ full isolation (Landlock + seccomp + cgroups) |
| Linux arm64 | ❌ no sandbox (CGO cross-compilation not available) |
| macOS (any) | ❌ no sandbox |

Sandbox requires unprivileged user namespaces. Verify with:

```bash
unshare --user --net --map-current-user -- /bin/true
```

If this fails, the sandbox falls back to seccomp-only mode.

## Full configuration reference

See [docs/configuration.md](configuration.md#sandbox) for all sandbox options.
