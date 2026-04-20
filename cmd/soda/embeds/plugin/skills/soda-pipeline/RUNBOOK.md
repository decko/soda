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
| `soda render-prompt --phase <phase> --ticket <key>` | Render a prompt template without executing |

## State on disk

All pipeline state lives under `.soda/<ticket>/`:

```
.soda/<ticket>/
├── meta.json              # ticket metadata, phase status, costs, cycles
├── lock                   # flock file (PID + timestamp)
├── events.jsonl           # structured event log (append-only)
├── <phase>.json           # structured output per phase
└── logs/
    ├── <phase>_prompt.md  # rendered prompt sent to Claude
    └── <phase>_response.md
```

### Key files for debugging

- **`meta.json`** — check `phases.<name>.status`, `total_cost`, `rework_cycles`, `patch_cycles`, `escalated_from_patch`
- **`events.jsonl`** — chronological event log; grep for `phase_failed`, `budget_warning`, `patch_regression`, `rework_max_cycles`
- **`<phase>.json`** — the structured output returned by the LLM for that phase
- **`logs/<phase>_prompt.md`** — the exact prompt sent; useful for diagnosing bad outputs

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

### Pipeline timeout

**Symptom:** Pipeline stops with `pipeline: timeout after <elapsed> (limit <limit>) during phase <name>`.

**Cause:** Total wall-clock time exceeded `max_pipeline_duration`.

**Fix:**
1. Review phase durations: `soda history <ticket>`
2. Increase `limits.max_pipeline_duration` in `soda.yaml` if the work legitimately needs more time
3. Resume: `soda run <ticket> --from <phase>`

### Triage gates ticket as not automatable

**Symptom:** Pipeline stops after triage with `pipeline: phase triage gated: <reason>`.

**Cause:** Triage classified the ticket as not suitable for automation (ambiguous requirements, infra-only work, etc.).

**Fix:**
1. Read the triage output: `soda history <ticket> --phase triage`
2. If triage was wrong, improve the ticket description with clearer acceptance criteria
3. Re-run from triage: `soda run <ticket> --from triage`

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

### Monitor phase not responding to PR comments

**Symptom:** PR comments go unanswered. Monitor polls but takes no action.

**Cause:** Missing `self_user` config — the monitor cannot distinguish self-authored comments from external ones.

**Fix:**
1. Add `self_user` to your monitor config in `soda.yaml`:
   ```yaml
   monitor:
     self_user: your-github-username
   ```
2. Clean and re-run: `soda clean <ticket>` then `soda run <ticket>`

### Monitor ignoring comments from non-owners

**Symptom:** Monitor only responds to some reviewers, ignores others.

**Cause:** Authority resolution via CODEOWNERS is filtering out non-owners, or the monitor profile is set to `conservative` / `smart` (which ignore non-authoritative comments).

**Fix:**
- Switch to `aggressive` profile to respond to all comments:
  ```yaml
  monitor:
    profile: aggressive
  ```
- Or disable CODEOWNERS filtering by removing the `codeowners` config key

### Worktree issues — nested worktrees or stale branches

**Symptom:** Git errors about worktree already existing, or branch already checked out.

**Fix:**
1. List worktrees: `git worktree list`
2. Clean stale worktrees: `soda clean <ticket>` (removes worktree + state)
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

## Cost management

Check cumulative cost across all sessions:
```bash
soda sessions    # shows cost per session
soda status      # shows cost for active pipelines
```

The cost ledger persists in `.soda/cost.json` and survives `soda clean`. Per-phase cumulative cost is tracked in `meta.json` under `phases.<name>.cumulative_cost`.

### Cost optimization tips

1. **Use `--mode checkpoint`** to pause between phases and review progress before spending more
2. **Set per-phase limits** to catch runaway phases early (`max_cost_per_phase` in `soda.yaml`)
3. **Patch uses a smaller model** (claude-sonnet-4-6 by default) to save cost on targeted fixes
4. **Review the triage output** before letting the pipeline continue — if triage misclassifies complexity, subsequent phases may over-spend

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

# Count retries per phase
grep '"phase_retrying"' .soda/<ticket>/events.jsonl
```

## Cleaning up

```bash
soda clean <ticket>              # remove state + worktree for one ticket
soda clean --all                 # clean all completed/failed sessions
soda clean --all --dry-run       # preview what would be cleaned
```

Cleanup removes:
- `.soda/<ticket>/` state directory
- `.worktrees/soda/<ticket>/` worktree
- Local `soda/<ticket>` branch

Cleanup does **not** remove:
- Remote branches (push cleanup is manual)
- Cost ledger entries (`.soda/cost.json` is preserved)
