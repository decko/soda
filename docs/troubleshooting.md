# Troubleshooting

Common failure modes, what causes them, and how to fix them.

Run `soda doctor` first — it checks prerequisites and prints actionable fixes:

```bash
soda doctor
```

---

## 1. `claude: command not found`

**Error:**
```
✗ claude: not found in PATH
  fix: install Claude Code: https://docs.anthropic.com/en/docs/claude-code
```
or, when running a pipeline:
```
preflight check failed:
  ✗ claude: not found in PATH
    fix: install Claude Code: https://docs.anthropic.com/en/docs/claude-code
```

**Cause:** The Claude Code CLI is not installed or is not on your `PATH`.

**Fix:**

Install Claude Code via npm:

```bash
npm install -g @anthropic-ai/claude-code
```

Then verify the install:

```bash
claude --version
```

See the [Claude Code docs](https://docs.anthropic.com/en/docs/claude-code) for other install options. After installing, re-run `soda doctor` to confirm.

---

## 2. `gh auth status` fails

**Error:**
```
⚠ gh-auth: gh is not authenticated
  fix: run: gh auth login
```
or when fetching a GitHub ticket:
```
error fetching ticket: gh: To get started with GitHub CLI, please run: gh auth login
```

**Cause:** The GitHub CLI (`gh`) is installed but not authenticated. SODA uses `gh` to fetch GitHub issues and to push branches and open pull requests.

**Fix:**

```bash
gh auth login
```

Follow the prompts to authenticate with GitHub. Then verify:

```bash
gh auth status
```

If you use a GitHub token directly (CI/CD environments), set:

```bash
export GH_TOKEN=<your-token>
```

---

## 3. JSON schema parse failure

**Error:**
```
parse error: claude output does not match expected schema for phase "implement"
```
or:
```
failed to parse structured output: unexpected field ...
```

**Cause:** The Claude Code CLI returned output that doesn't conform to the JSON schema SODA generated for that phase. This happens when:

- The Claude Code CLI version is outdated (structured output schema support changed).
- The Claude session hit its context limit mid-response and produced truncated JSON.
- A transient API error caused a malformed response.

**Fix:**

1. **Retry** — transient parse failures are retried automatically (up to the limit configured in `phases.yaml`). If the pipeline already retried and failed, re-run from the failing phase:

   ```bash
   soda run <ticket> --from last
   ```

2. **Check Claude Code version** — ensure you are on a supported version:

   ```bash
   soda doctor
   claude --version
   ```

   If the version is outdated, upgrade:

   ```bash
   npm update -g @anthropic-ai/claude-code
   ```

3. **Check context pressure** — if the ticket is large, the session may be running out of context. Consider splitting the ticket into smaller sub-issues.

---

## 4. `PhaseGateError: not automatable`

**Error:**
```
PhaseGateError: triage classified ticket as not automatable
reason: "ticket requires direct database access — cannot be safely automated"
```

**Cause:** The triage phase classified the ticket as outside SODA's automation envelope. Common reasons:

- The ticket requires human judgment (e.g., architecture decisions, security reviews).
- The ticket description is too vague for the agent to understand what to build.
- The ticket requires access to external systems (prod databases, third-party APIs without mocks).
- The ticket type is explicitly excluded by your pipeline configuration.

**Fix:**

**Option A — implement manually.** Triage is correct; the ticket genuinely needs human attention.

**Option B — improve the ticket description.** Add:

- Explicit acceptance criteria.
- Pointers to the relevant files and packages.
- A `## Context to read` section listing files the agent should examine.
- A spec or plan in the ticket body (see [spec/plan workflow in AGENTS.md](../AGENTS.md)).

Then re-run:

```bash
soda run <ticket>
```

**Option C — override triage** (use with care). If you are certain the ticket is automatable, you can resume from the plan phase, skipping triage:

```bash
soda run <ticket> --from plan
```

---

## 5. Budget exceeded

**Error:**
```
BudgetExceededError: phase "implement" exceeded max_cost_per_phase ($8.00)
cumulative cost: $9.43
```
or:
```
BudgetExceededError: ticket exceeded max_cost_per_ticket ($30.00)
total cost so far: $31.17
```

**Cause:** The pipeline hit a cost guard configured in `soda.yaml`. Guards exist at three levels:

| Limit | Config key | Applies to |
|-------|-----------|-----------|
| Per generation | `max_cost_per_generation` | Single phase attempt |
| Per phase (cumulative) | `max_cost_per_phase` | All attempts of one phase |
| Per ticket | `max_cost_per_ticket` | Entire pipeline run |

**Fix:**

**Option A — raise the limit in `soda.yaml`:**

```yaml
limits:
  max_cost_per_ticket: 50.00
  max_cost_per_phase: 20.00
  max_cost_per_generation: 12.00
```

**Option B — split the ticket.** Large tickets are the most common cause of budget overruns. Split into smaller sub-issues that each fit comfortably within the default limits. See the [ticket sizing guide in AGENTS.md](../AGENTS.md).

**Option C — resume from the failed phase** after raising the limit:

```bash
soda run <ticket> --from last
```

---

## 6. Phase timeout

**Error:**
```
error: phase "implement" exceeded timeout (15m0s)
```

**Cause:** A phase ran longer than the configured `timeout` in `phases.yaml`. The implement phase default (15 minutes) is often too short for tickets with 5+ tasks.

**Fix:**

**Option A — raise the timeout in `phases.yaml`** (project root file takes precedence over the embedded default):

```yaml
phases:
  - name: implement
    timeout: 25m   # raise from default 15m
    # ... rest of phase config unchanged
```

**Option B — split the ticket** into smaller pieces. A timeout usually signals that the scope is too large for a single session.

**Option C — resume from the failed phase** after adjusting the timeout:

```bash
soda run <ticket> --from last
```

---

## 7. `soda run` hangs

**Symptom:** `soda run` starts but nothing progresses — no output, no phase transitions, no errors.

**Cause — prerequisites missing:** A required binary (`claude`, `git`, `gh`) is not found at runtime even if it exists in your shell's `PATH`. This can happen with `sudo` or in environments where `PATH` is restricted.

**Cause — stale lock:** A previous `soda run` was killed without releasing the file lock on `.soda/<ticket>/lock`.

**Fix:**

1. **Run `soda doctor`** to check prerequisites:

   ```bash
   soda doctor
   ```

2. **Check pipeline status:**

   ```bash
   soda status
   ```

   If the ticket shows as running but no process is active, a stale lock is present.

3. **Clear the stale lock** by cleaning and re-running:

   ```bash
   soda clean <ticket>
   soda run <ticket>
   ```

   `soda clean` removes the worktree and branches. Session data (`.soda/<ticket>/`) is preserved so you can inspect what happened. Use `--purge` to remove everything:

   ```bash
   soda clean <ticket> --purge
   ```

4. **Check for a hung subprocess:** In rare cases, a Claude Code session may be stuck waiting for I/O. Find and kill it:

   ```bash
   ps aux | grep claude
   kill <pid>
   ```

---

## 8. Worktree already exists

**Error:**
```
error: worktree already exists at .worktrees/soda/<ticket-key>
```
or from git:
```
fatal: '<path>' already exists
```

**Cause:** A previous pipeline run created a worktree that was never cleaned up. SODA does not auto-delete worktrees on failure — they are preserved so you can inspect the state.

**Fix:**

Clean the stale worktree:

```bash
soda clean <ticket>
```

If the worktree directory is dirty or git refuses to remove it:

```bash
soda clean <ticket> --force
```

`--force` removes the worktree even if it has uncommitted changes and deletes the remote branch if one was pushed.

Then re-run:

```bash
soda run <ticket>
```

---

## 9. `permission denied` on sandbox

**Error:**
```
sandbox: failed to create user namespace: permission denied
```
or:
```
sandbox: unshare --user --net: operation not permitted
```

**Cause:** The go-arapuca sandbox uses Linux **unprivileged user namespaces** for network isolation. Some kernels or system configurations disable unprivileged user namespaces (common on Debian, hardened kernels, and some container runtimes).

**Diagnose:**

```bash
unshare --user --net --map-current-user -- /bin/true
```

If this fails, unprivileged user namespaces are disabled on your system.

**Fix:**

**Option A — enable unprivileged user namespaces** (requires root, kernel ≥ 3.8):

```bash
# Debian/Ubuntu
sudo sysctl -w kernel.unprivileged_userns_clone=1
# persist across reboots:
echo 'kernel.unprivileged_userns_clone=1' | sudo tee /etc/sysctl.d/99-userns.conf
```

**Option B — disable the sandbox** in `soda.yaml` (falls back to seccomp-only isolation):

```yaml
sandbox:
  enabled: false
```

> **Warning:** Disabling the sandbox removes network and filesystem isolation. Only do this in trusted environments.

**Option C — check your container runtime.** If running inside Docker or a VM, ensure the container has `--privileged` or `--security-opt seccomp=unconfined`, or use a host with user namespace support enabled.

---

## 10. Monitor not responding to comments

**Symptom:** Pull request comments from reviewers are ignored — the monitor phase does not respond or take action.

**Cause:** `self_user` is not set in `soda.yaml`. Without it, the monitor cannot distinguish comments that SODA itself wrote from comments written by human reviewers. To avoid infinite loops, the monitor falls back to a stub that does not respond.

**Fix:**

Set `self_user` in `soda.yaml` to the GitHub (or GitLab) username that SODA uses to author comments:

```yaml
monitor:
  self_user: soda-bot   # the username SODA posts comments as
```

If SODA posts as your personal account, use your own username:

```yaml
monitor:
  self_user: your-github-username
```

After updating the config, the monitor will pick up the change on the next polling cycle — no restart required if a monitor session is already running. For a pipeline that has already completed the submit phase, resume the monitor:

```bash
soda run <ticket> --from monitor
```

---

## Still stuck?

- Run `soda doctor` — it catches most environment problems automatically.
- Check `soda status` and `soda history <ticket>` for phase-level details.
- Inspect `.soda/<ticket>/logs/` for raw phase prompts and responses.
- Inspect `.soda/<ticket>/events.jsonl` for the structured event log.
