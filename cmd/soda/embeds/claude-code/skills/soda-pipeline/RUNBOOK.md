# Operational Runbook

Troubleshooting and operational procedures for the SODA pipeline.

## Diagnostic commands

| Command | What it shows |
|---------|---------------|
| `soda status` | Active and recent pipelines with phase progress |
| `soda history <ticket>` | Phase-by-phase execution: status, duration, cost, errors |
| `soda history <ticket> --detail` | Full structured JSON output per phase |
| `soda history <ticket> --phase <name>` | Drill into a single phase |
| `soda sessions` | All previous pipeline runs |
| `soda log <ticket>` | Print formatted pipeline events |
| `soda log <ticket> -f` | Tail events in real-time |
| `soda cost` | Cumulative cost breakdown across all sessions |
| `soda validate` | Check config, phases, and prompts for errors |
| `soda render-prompt --phase <phase> --ticket <key>` | Render a prompt template without executing |
| `soda attach <ticket>` | Observe a running pipeline in real-time (read-only) |

## State on disk

All pipeline state lives under `.soda/<ticket>/`:

```
.soda/<ticket>/
├── meta.json              # ticket metadata, phase status, costs, cycles, tokens
├── lock                   # flock file (PID + timestamp)
├── events.jsonl           # structured event log (append-only)
├── <phase>.json           # structured output per phase
├── monitor_state.json     # monitor polling state
└── logs/
    ├── <phase>_prompt.md  # rendered prompt sent to Claude
    ├── <phase>_response.md
    └── review/
        ├── prompt_<reviewer>.md
        └── response_<reviewer>.md
```

### Key files for debugging

- **`meta.json`** — check `phases.<name>.status`, `total_cost`, `rework_cycles`, `patch_cycles`, `escalated_from_patch`, `tokens_in`, `tokens_out`
- **`events.jsonl`** — chronological event log; grep for `phase_failed`, `budget_warning`, `patch_regression`, `rework_max_cycles`
- **`<phase>.json`** — the structured output returned by the LLM for that phase
- **`logs/<phase>_prompt.md`** — the exact prompt sent; useful for diagnosing bad outputs
- **`monitor_state.json`** — poll count, response rounds, CI status, last comment ID

## Common failure scenarios

### Pipeline stuck — lock held

**Symptom:** `soda run <ticket>` fails with "lock already held" or hangs.

**Cause:** A previous run crashed without releasing the flock, or another `soda` process is still running.

**Fix:**
1. Check if another process is using the lock: `cat .soda/<ticket>/lock` (shows PID + timestamp)
2. Verify the PID is alive: `kill -0 <pid>` (returns non-zero if dead)
3. If the process is dead, remove the lock file: `rm .soda/<ticket>/lock`
4. Re-run: `soda run <ticket> --from last`

> **Note:** `flock` is per-machine. The lock file does not protect against concurrent runs on different hosts.

### Process crash — no failure event in events.jsonl

**Symptom:** `soda run <ticket>` reports the pipeline as still running, but no process is alive. `events.jsonl` shows a `phase_started` with no matching `phase_completed` or `phase_failed`.

**Cause:** SODA crashed or was killed between phase completion and event emission (e.g., OOM kill, SIGKILL, machine restart).

**Diagnosis:**
1. Check the lock file: `cat .soda/<ticket>/lock` — note the PID and timestamp
2. Verify the PID is dead: `kill -0 <pid>` (non-zero exit = process not running)
3. Find the incomplete phase:
   ```bash
   grep '"phase_started"\|"phase_completed"' .soda/<ticket>/events.jsonl
   ```
   The last `phase_started` without a matching `phase_completed` is the crashed phase.

**Fix:**
1. Remove the stale lock: `rm .soda/<ticket>/lock`
2. Resume from the crashed phase: `soda run <ticket> --from <phase>`

### Phase failed — transient error (429, 500, timeout)

**Symptom:** Phase fails with `claude: transient (rate_limit)`, `claude: transient (timeout)`, or `claude: transient (overloaded)`.

**Cause:** Anthropic API rate limit, server error, or network issue. The engine retries transient errors (default: 2 retries with exponential backoff) but all retries were exhausted.

**Fix:**
1. Wait a few minutes for rate limits to clear
2. Resume: `soda run <ticket> --from last`

### Phase failed — parse error

**Symptom:** Phase fails with `claude: parse error: no JSON response envelope found` or `unexpected response type`.

**Cause:** The LLM produced output that doesn't match the expected JSON envelope format. Common when the model is overloaded or the output was truncated.

**Fix:**
1. Check the raw output: `cat .soda/<ticket>/logs/<phase>_response.md`
2. If truncated, increase the phase timeout in `phases.yaml`
3. Resume: `soda run <ticket> --from <phase>`

### Phase failed — semantic error

**Symptom:** Phase fails with `claude: semantic error: <message>`.

**Cause:** The Claude CLI returned `"subtype": "error"` — typically an invalid API key, account issue, or model access problem.

**Fix:**
1. Verify your API key: `claude --version` (should respond without error)
2. Check the error message in `events.jsonl` for specifics
3. Fix the root cause (API key, account credits, model access)
4. Resume: `soda run <ticket> --from <phase>`

### Sandbox submit — gh auth hangs

**Symptom:** Submit or follow-up phase hangs indefinitely inside the sandbox. No output after the `gh` call. The phase eventually times out.

**Cause:** The `gh` CLI attempts OS keyring or browser authentication, which is inaccessible inside the arapuca sandbox. Fixed in #361: `claudeEnv()` now extracts `GH_TOKEN` from `gh auth token` on the host side and injects it before spawning the sandbox. Users with older binaries still need the workaround.

**Fix (older binaries or if the automatic extraction fails):**
1. Export the token manually before running soda:
   ```bash
   export GH_TOKEN=$(gh auth token)
   soda run <ticket> --from submit
   ```
2. Or add it to your shell profile so it persists across sessions:
   ```bash
   echo 'export GH_TOKEN=$(gh auth token 2>/dev/null)' >> ~/.bashrc
   ```

> **Note:** Upgrade to the latest `soda` binary to get the automatic `GH_TOKEN` extraction.

### Budget exceeded

**Symptom:** Pipeline stops with `pipeline: budget exceeded in phase <name>`.

**Cause:** Accumulated cost hit `max_cost_per_ticket` or `max_cost_per_phase`.

**Fix:**
1. Check current spend: `soda history <ticket>` — review cost per phase
2. Decide whether to increase the budget in `soda.yaml`:
   ```yaml
   limits:
     max_cost_per_ticket: 30.00   # total across all phases
     max_cost_per_phase: 15.00    # per individual phase (cumulative across rework)
   ```
3. Resume: `soda run <ticket> --from <phase>`

> **Note:** `max_cost_per_phase` is cumulative across rework/patch generations, not just the current run.

### Review phase — budget exceeded after rework cycles

**Symptom:** Review phase fails with `pipeline: budget exceeded in phase review` after one or more rework cycles.

**Cause:** `max_cost_per_phase` is cumulative across rework generations. Two review cycles ($3–5 each) plus the rework implement sessions they trigger can exhaust a conservative limit. See gotcha #11 in AGENTS.md.

**Fix — choose one approach:**

Option A — raise the cumulative cap for review-heavy workflows:
```yaml
limits:
  max_cost_per_phase: 15.00
```

Option B — switch to a per-attempt cap so each generation is capped independently (allows more rework cycles):
```yaml
limits:
  max_cost_per_phase: 0        # disable cumulative cap (or remove the line)
  max_cost_per_generation: 8.00
```

> **Tradeoff:** `max_cost_per_phase` limits total spend per phase (safer for absolute budget control). `max_cost_per_generation` caps each attempt independently but allows unlimited rework cycles — total spend is unbounded if rework loops repeat.

Resume: `soda run <ticket> --from review`

### Pipeline timeout

**Symptom:** Pipeline stops with `pipeline: timeout after <elapsed> (limit <limit>) during phase <name>`.

**Cause:** Total wall-clock time exceeded `max_pipeline_duration`.

**Fix:**
1. Review phase durations: `soda history <ticket>`
2. Increase `limits.max_pipeline_duration` in `soda.yaml` if the work legitimately needs more time
3. Resume: `soda run <ticket> --from <phase>`

### Implement phase — context deadline exceeded on large tickets

**Symptom:** Implement phase fails with `context deadline exceeded` or times out part-way through. Ticket has 7 or more tasks.

**Cause:** The embedded default implement timeout is 15m — too short for large tickets. The root `phases.yaml` (if present) overrides the embedded defaults without requiring a rebuild. See gotcha #25 in AGENTS.md.

**Fix:**
1. Create or edit `phases.yaml` in your project root:
   ```yaml
   phases:
     implement:
       timeout: 25m
   ```
2. Resume: `soda run <ticket> --from implement`

> **Note:** The root `phases.yaml` takes precedence over the compiled-in defaults. Changes take effect immediately — no rebuild required.

### Triage gates ticket as not automatable

**Symptom:** Pipeline stops after triage with `pipeline: phase triage gated: <reason>`.

**Cause:** Triage classified the ticket as not suitable for automation (ambiguous requirements, infra-only work, etc.).

**Fix:**
1. Read the triage output: `soda history <ticket> --phase triage`
2. If triage was wrong, improve the ticket description with clearer acceptance criteria
3. Re-run from triage: `soda run <ticket> --from triage`

### Stale triage blocking re-run

**Symptom:** Re-running a ticket that was previously gated by triage fails again with the same gate.

**Cause:** The previous `triage.json` is still on disk and blocks re-run.

**Fix:**
1. Purge all session data: `soda clean <ticket> --purge`
2. Re-run: `soda run <ticket>`

### Verify fails — corrective patch loop

**Symptom:** Verify fails, patch runs but doesn't fix the issue, loop repeats until `max_attempts`.

**Cause:** The test failures are too complex for the patch phase (which uses a smaller model by default).

**Events to check:** `patch_exhausted`, `patch_escalated`, `patch_regression`

**Fix (depending on `on_exhausted` policy in phases.yaml):**
- `stop` (default): Pipeline stops. Resume with `--from implement` to let the full implement phase retry
- `escalate`: Engine auto-routes to implement (one-shot, guarded by `EscalatedFromPatch` flag)
- `retry`: One extra patch attempt before stopping

To force re-implementation: `soda run <ticket> --from implement`

### Patch regression detected

**Symptom:** Events show `patch_regression` — the patch phase introduced new test failures.

**Cause:** The targeted fix in patch broke something else. Patch immediately escalates rather than retrying.

**Fix:**
1. Review what patch changed: check `logs/patch_prompt.md` and `patch.json`
2. Resume from implement for a full re-attempt: `soda run <ticket> --from implement`

### Review rework loop — max cycles reached

**Symptom:** Pipeline stops with `pipeline: phase review gated: max rework cycles reached` after 2 (default) review→implement loops.

**Events to check:** `rework_max_cycles`, `rework_minors_downgraded`

**Cause:** Critical or major review findings persist after max rework cycles.

**Fix:**
1. Read the review findings: `soda history <ticket> --phase review`
2. If only minor findings remain, the engine auto-downgrades to `pass-with-follow-ups` and proceeds
3. If critical/major findings remain, fix them manually and resume: `soda run <ticket> --from implement`

### Parallel-review: one or more reviewers failed

**Symptom:** Phase fails with `engine: reviewer failures in review: go-specialist: <error>; ai-harness: <error>`.

**Cause:** The `parallel-review` phase dispatches all configured reviewers concurrently. If any reviewer fails, the engine collects all errors and fails the phase.

**Identify which reviewer failed:**
```bash
grep '"reviewer_failed"' .soda/<ticket>/events.jsonl
```

**Fix:**
1. For transient errors, wait and resume: `soda run <ticket> --from review`
2. For parse errors, check whether the reviewer's prompt template is valid
3. For prompt load errors, verify the reviewer's `prompt:` path in `phases.yaml`

### Monitor phase — comments not detected

**Symptom:** PR comments go unnoticed. Monitor polls but shows no comment activity.

**Cause:** This should not happen after v0.4.0. In passive mode (default), the monitor detects new comments and emits `monitor_new_comments` + `monitor_notify_user` events. Check:
1. Events: `grep 'monitor_new_comments\|monitor_notify_user' .soda/<ticket>/events.jsonl`
2. Monitor state: `cat .soda/<ticket>/monitor_state.json` — check `last_comment_id`

### Monitor phase — comments detected but no response

**Symptom:** Monitor logs `monitor_new_comments` but doesn't respond to them.

**Cause:** Active comment response requires `respond_to_comments: true` in `phases.yaml` and `self_user` in config. Without these, monitor runs in passive mode (detect-only).

**Fix:**
1. Add `self_user` to your config:
   ```yaml
   self_user: your-github-username
   ```
2. Enable response in `phases.yaml`:
   ```yaml
   monitor:
     polling:
       respond_to_comments: true
   ```
3. Re-run: `soda run <ticket> --from monitor`

### GitHub Actions — all CI jobs fail in under 3 seconds

**Symptom:** All CI jobs fail within 3 seconds of triggering. No test output, no compilation errors. The failure happens before any code runs.

**Cause:** GitHub Actions runner allocation failure — the runner never started. This is a GitHub infrastructure issue, not a code problem.

**How to distinguish from a real failure:**
- Real test failures: jobs run for >10 seconds before failing
- Runner allocation failures: jobs fail in <3 seconds with no meaningful output

**Fix:**
1. Re-run the workflow: `gh run rerun <run-id>`
2. If the PR is already reviewed and approved and you need to unblock immediately:
   ```bash
   gh pr merge <pr-number> --admin --squash
   ```

> **Note:** Do not push new commits or restart the pipeline in response to a <3s CI failure — the code is fine. It will pass on the next attempt.

### Worktree issues — nested worktrees or stale branches

**Symptom:** Git errors about worktree already existing, or branch already checked out.

**Fix:**
1. List worktrees: `git worktree list`
2. Clean stale worktrees: `soda clean <ticket>` (removes worktree, preserves session data)
3. Clean all: `soda clean --all`
4. Manual cleanup if soda clean fails:
   ```bash
   git worktree remove .worktrees/soda/<ticket> --force
   git branch -D soda/<ticket>
   rm -rf .soda/<ticket>
   ```

> **Important:** Always run `soda` from the main repo checkout, not from inside a worktree.

## Resuming pipelines

Resume from the last failed/interrupted phase:
```bash
soda run <ticket> --from last
```

Resume from a specific phase (re-runs that phase and everything after it):
```bash
soda run <ticket> --from <phase>
```

Phases that already completed and whose dependencies haven't changed are skipped automatically.

## Named pipelines

List available pipelines:
```bash
soda pipelines
```

Run with a specific pipeline:
```bash
soda run <ticket> --pipeline quick-fix    # 3-phase: implement→verify→submit
soda run <ticket> --pipeline docs-only    # 2-phase: implement→submit
```

Scaffold a new pipeline:
```bash
soda pipelines new <name>
```

Pipeline discovery order: `./pipelines/` → `~/.config/soda/pipelines/` → built-in.

## Cost management

Check cumulative cost across all sessions:
```bash
soda cost          # detailed cost breakdown
soda sessions      # shows cost per session
soda status        # shows cost for active pipelines
```

The cost ledger persists in `.soda/cost.json` and survives `soda clean`. Per-phase cumulative cost is tracked in `meta.json` under `phases.<name>.cumulative_cost`.

### Cost optimization tips

1. **Use `--mode checkpoint`** to pause between phases and review progress before spending more
2. **Set per-phase limits** to catch runaway phases early (`max_cost_per_phase` in `soda.yaml`)
3. **Patch uses a smaller model** (claude-sonnet-4-6 by default) to save cost on targeted fixes
4. **Use named pipelines** (`--pipeline quick-fix`) for simple changes that don't need full review
5. **Review the triage output** before letting the pipeline continue — if triage misclassifies complexity, subsequent phases may over-spend

## Event log analysis

The `events.jsonl` file is append-only and contains every engine event. Useful patterns:

```bash
# Show all failures
grep '"phase_failed"' .soda/<ticket>/events.jsonl

# Show budget warnings
grep '"budget_warning"\|"phase_budget_warning"' .soda/<ticket>/events.jsonl

# Show rework routing
grep '"rework_routed"\|"rework_max_cycles"' .soda/<ticket>/events.jsonl

# Show corrective patch activity
grep '"patch_exhausted"\|"patch_escalated"\|"patch_regression"' .soda/<ticket>/events.jsonl

# Show monitor activity
grep '"monitor_' .soda/<ticket>/events.jsonl

# Show comment detection (passive mode)
grep '"monitor_new_comments"\|"monitor_notify_user"' .soda/<ticket>/events.jsonl

# Count retries per phase
grep '"phase_retrying"' .soda/<ticket>/events.jsonl
```

## Cleaning up

```bash
soda clean <ticket>              # remove worktree + branches, preserve session data
soda clean <ticket> --purge      # remove everything including .soda/<ticket>/
soda clean <ticket> --force      # clean dirty worktrees + remote branches
soda clean --all                 # clean all worktrees, preserve session data
soda clean --all --purge         # clean everything including all session data
soda clean --all --dry-run       # preview what would be cleaned
```

Cleanup removes:
- `.worktrees/soda/<ticket>/` worktree
- Local `soda/<ticket>` branch
- Remote `origin/soda/<ticket>` branch (with `--force`)

Cleanup preserves (unless `--purge`):
- `.soda/<ticket>/` session data (meta, events, artifacts — used by raki metrics)

Cleanup does **not** remove:
- Cost ledger entries (`.soda/cost.json` is always preserved)
