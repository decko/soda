# Running pipelines

This guide covers how to run SODA pipelines effectively: foreground vs.
background execution, monitoring live runs, running multiple tickets in
parallel, and recovering from failures.

---

## How `soda run` works

`soda run <ticket>` is a **blocking foreground process**. It holds your
terminal while the pipeline runs, streaming progress to stdout (or the TUI).
There is no built-in background mode, and that is intentional — see
[Why there is no `--background` flag](#why-there-is-no---background-flag).

All pipeline state is written to disk under `.soda/<ticket>/` as each phase
completes. This means:

- The process can be interrupted (`Ctrl-C`) and resumed later without losing work.
- You can inspect state, logs, and costs at any time — even while the pipeline is running.
- Crash recovery is free: restart the process and it picks up from the last completed phase.

---

## Running in the background

Use standard shell job control to run a pipeline without blocking your
terminal.

**Disown and redirect output:**

```bash
soda run 42 > .soda/42/run.log 2>&1 &
disown
```

**With `nohup` (survives terminal close):**

```bash
nohup soda run 42 > .soda/42/run.log 2>&1 &
echo $!   # save the PID if you want to kill it later
```

**Monitor the live output from another terminal:**

```bash
soda log 42 -f          # tail structured pipeline events
tail -f .soda/42/run.log  # tail raw output
```

**Check status:**

```bash
soda status             # all active and recent pipelines
soda history 42         # phase-by-phase breakdown for ticket 42
```

---

## Running multiple tickets in parallel

SODA pipelines are independent: each ticket has its own worktree, its own
state directory, and its own Claude session. You can run several in parallel
with no coordination required.

```bash
# Start three pipelines in the background
nohup soda run 42 > .soda/42/run.log 2>&1 &
nohup soda run 43 > .soda/43/run.log 2>&1 &
nohup soda run 44 > .soda/44/run.log 2>&1 &

# Watch all of them at once
soda status
```

**Practical limits:** API rate limits and token quotas are shared across
sessions. Running more than 2–3 pipelines concurrently tends to cause
timeouts and retries that increase cost and wall-clock time. Sequential
runs with immediate merge are usually cheaper overall than parallel runs
followed by rebase.

---

## Resuming after interruption

If a pipeline is interrupted (Ctrl-C, terminal close, crash), resume it from
the last failed or running phase:

```bash
soda run 42 --from last     # auto-resolves to the last failed/running phase
soda run 42 --from verify   # resume from a specific phase
```

SODA never re-runs completed phases unless you explicitly ask. State is
preserved across restarts.

---

## Getting notified on completion

Instead of watching a terminal, configure a notification hook in `soda.yaml`
to fire when a pipeline finishes:

```yaml
notify:
  webhook:
    url: https://hooks.slack.com/services/...
  script:
    command: "./scripts/on-complete.sh"
```

The webhook receives a JSON payload with ticket, status, total cost, duration,
and per-phase details. The script receives the same JSON on stdin.

See [configuration.md](configuration.md#notification-hooks) for the full
payload schema.

---

## Why there is no `--background` flag

A `--background` flag would be a thin wrapper around `nohup` + output
redirection — something the shell already does well. More importantly, adding
it would require SODA to manage process lifecycle: PID files, signal
forwarding, log rotation, and process supervision. That is daemon territory.

SODA deliberately has no daemon. Daemons add operational complexity (start on
boot, restart policy, log management) and failure modes (daemon crashes, stale
PID files, zombie processes) that are hard to debug and unnecessary when the
problem is already solved at the shell level.

The right tool for process supervision is your init system (`systemd`,
`launchd`) or a process manager (`supervisord`, `tmux`, `screen`). SODA
focuses on pipeline orchestration and leaves process management to the
infrastructure layer.

If you find yourself wanting a daemon for a specific workflow — for example,
watching a label and auto-running pipelines — that is a valid use case for a
wrapper script or an external trigger (GitHub Actions, a cron job, a
webhook receiver). SODA's disk-based state and event log are designed to
integrate cleanly with these.
