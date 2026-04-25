You are a surgical code fixer applying targeted corrections to an existing implementation.

## Ticket

Key: 376
Summary: feat(engine): notification hook on pipeline completion — webhook or script callback

### Acceptance Criteria (reference only)
- Script callback works via `exec.Command` (no shell)
- Webhook POST works with 10s default timeout
- Status enum: `success`, `failed`, `timeout`, `partial`
- Token fields included in webhook (per-phase with cache breakdown)
- Both `on_finish` and `on_failure` handlers supported
- Fires from both `Run()` and `Resume()` paths
- Notification failure logged, never blocks pipeline
- Script stderr captured and logged
- `soda validate` checks script path and webhook URL
- Tests cover: success notification, failure notification, webhook timeout, missing script, partial status


## FIXES REQUIRED

You will address the issues listed below **one at a time, in order**. Report one fix_result per item in the Fixes section (use fix_index matching the 0-based index).

Do NOT address multiple findings in a single edit. Fix one, verify it, then move to the next.

### Verdict: FAIL
### Fixes
#### Finding 1 of 5
0. **Status enum uses "failure" but AC requires "failed" — change NotifyStatusFailure to "failed"**
→ Fix this finding, then verify before proceeding.
#### Finding 2 of 5
1. **Default timeout is 30s but AC specifies 10s — change defaultNotifyTimeout to 10 * time.Second**
→ Fix this finding, then verify before proceeding.
#### Finding 3 of 5
2. **Webhook payload missing token fields — AC requires per-phase token counts with cache breakdown (TokensIn, TokensOut, CacheTokensIn per phase). PhaseState already has these fields; add a Tokens map to webhookPayload**
→ Fix this finding, then verify before proceeding.
#### Finding 4 of 5
3. **No separate on_finish and on_failure handlers — AC requires both; current implementation fires a single notifyOnFinish for all outcomes. Add on_failure config option (separate webhook URL or script) that only fires when runErr != nil**
→ Fix this finding, then verify before proceeding.
#### Finding 5 of 5
4. **Script stderr not captured separately — AC says "Script stderr captured and logged". Currently uses CombinedOutput() which merges stdout+stderr, and on success discards all output. Use separate cmd.Stderr buffer, and log stderr content even on success**
→ Fix this finding, then verify before proceeding.

### Failed Acceptance Criteria
#### Finding 1 of 5
0. **FAIL**: Webhook POST works with 10s default timeout
   Evidence: notify.go:39 sets defaultNotifyTimeout = 30 * time.Second. AC specifies 10s default.
→ Fix this finding, then verify before proceeding.
#### Finding 2 of 5
1. **FAIL**: Status enum: success, failed, timeout, partial
   Evidence: notify.go:21 defines NotifyStatusFailure = "failure" but AC specifies the value must be "failed"
→ Fix this finding, then verify before proceeding.
#### Finding 3 of 5
2. **FAIL**: Token fields included in webhook (per-phase with cache breakdown)
   Evidence: webhookPayload struct (notify.go:57-65) only contains Ticket, Status, Branch, PRURL, TotalCost, Error. No per-phase token counts (TokensIn, TokensOut, CacheTokensIn) despite PhaseState having these fields in meta.go:30-32.
→ Fix this finding, then verify before proceeding.
#### Finding 4 of 5
3. **FAIL**: Both on_finish and on_failure handlers supported
   Evidence: Only a single notifyOnFinish handler exists that always fires. No separate on_failure config option (e.g., a failure-only webhook URL or script). NotificationsConfig in config.go has only WebhookURL and Script — no on_finish/on_failure distinction.
→ Fix this finding, then verify before proceeding.
#### Finding 5 of 5
4. **FAIL**: Script stderr captured and logged
   Evidence: notify.go:125 uses CombinedOutput() which mixes stdout+stderr. On success, all output is discarded. AC requires stderr to be captured separately and logged. Should use separate stderr buffer and log its contents even when the script exits 0.
→ Fix this finding, then verify before proceeding.


## Working Directory

Worktree: /home/ddebrito/dev/soda/.worktrees/soda/376
Branch: soda/376
Base: main

## Rules

1. **BEFORE editing any file**, state which fix number you are addressing and why.
2. **Do NOT modify files** not mentioned in the feedback above.
3. **Do NOT add new features** or refactor beyond what the fixes require.
4. **If a fix requires more than 50 lines of changes**, STOP and set `too_complex: true` with a reason. Do not attempt the fix.
5. **Run the formatter** after changes: `gofmt -w .`
6. **Run the tests** after changes: `go test ./...`
7. **Each fix_result must map 1:1** to the numbered items in the **Fixes** section only. Use the same 0-based index (the number before each fix). Display headers show 1-based "Finding X of N" but fix_index uses the 0-based number. Code Issues provide additional context but do not need separate fix_results.
8. **Commit** the fix with a message referencing the ticket key and fix number.
