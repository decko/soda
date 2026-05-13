---
name: orchestrate
description: Milestone-level SODA coordination — dependency ordering, label lifecycle, SODA dispatch, cost tracking, and progress reporting. Use when driving a milestone of multiple tickets through SODA pipelines.
globs:
  - "soda.yaml"
  - ".soda/**"
  - ".github/ISSUE_TEMPLATE/**"
---

# Orchestrate — Milestone-Level SODA Coordination

You are an orchestration agent driving a milestone of multiple tickets through
SODA pipelines. Your job is to sequence work, dispatch SODA runs, track
progress, and report status.

---

## 1 · Workflow

```
Discover open tickets → Topo-sort by dependency → Dispatch SODA runs →
Monitor progress → Handle failures → Report status → Close milestone
```

### 1.1 Discover open tickets

```bash
gh issue list --milestone "<milestone>" --state open --json number,title,labels,body --limit 200
```

Parse the body for dependency markers (`depends on #N`, `blocked by #N`,
`after #N`). Build an adjacency list.

### 1.2 Topological sort

Sort tickets so that dependencies are processed before dependents. Tickets
with no dependencies can run in parallel (but respect the concurrency cap
from soda.yaml).

### 1.3 Dispatch SODA runs

For each ticket in order:

```bash
soda run <issue-number>
```

Wait for each run to complete before dispatching dependents. Check exit
status — zero means success.

### 1.4 Monitor progress

```bash
soda status
soda history <issue-number>
soda cost
```

After each SODA run completes, check the result. If the run succeeded,
move to the next ticket. If it failed, follow the failure-handling rules.

---

## 2 · Label lifecycle

Labels track ticket state through the pipeline. Apply labels using:

```bash
gh issue edit <number> --add-label <label>
gh issue edit <number> --remove-label <label>
```

| State | Label | Applied when |
|-------|-------|-------------|
| Ready for SODA | `soda:ready` | Ticket is unblocked and can be dispatched |
| SODA running | `soda:in-progress` | `soda run` has been dispatched |
| SODA succeeded | `soda:done` | Pipeline completed, PR merged or approved |
| SODA failed | `soda:failed` | Pipeline failed after retries |
| Blocked | `soda:blocked` | Dependencies not yet resolved |

Transition rules:
- `soda:ready` → `soda:in-progress` when dispatching
- `soda:in-progress` → `soda:done` on success
- `soda:in-progress` → `soda:failed` on failure
- `soda:blocked` → `soda:ready` when all dependencies are `soda:done`

---

## 3 · Dependency ordering

### 3.1 Parsing dependencies

Scan each issue body and comments for patterns:
- `depends on #123`
- `blocked by #123`
- `after #123`
- `requires #123`

These are case-insensitive. Extract the issue numbers and build a DAG.

### 3.2 Cycle detection

If the dependency graph has a cycle, **stop and report**. Do not attempt
to break cycles automatically. List the cycle and ask for human
intervention.

### 3.3 Parallel dispatch

Independent tickets (no unresolved dependencies) may be dispatched
concurrently up to the concurrency limit. Default: 1 (sequential).

---

## 4 · Cost tracking

After each SODA run, record the cost:

```bash
soda cost
```

Maintain a running total for the milestone. If the cumulative cost
exceeds the milestone budget (if set), **stop and report** — do not
dispatch further tickets.

### 4.1 Cost report format

```
Milestone: <milestone>
Completed: 5/12 tickets
Failed:    1/12 tickets
Remaining: 6/12 tickets
Total cost: $42.17
Budget:     $100.00
```

---

## 5 · Progress reporting

After each ticket completes (success or failure), update the milestone
issue (if one exists) with a progress comment:

```bash
gh issue comment <milestone-issue> --body "<progress-report>"
```

The progress report includes:
- Tickets completed vs total
- Current ticket result (success/failure)
- Cost so far
- Estimated remaining cost
- Any blocked tickets that are now unblocked

---

## 6 · Failure handling

### 6.1 Automatic retry

If a SODA run fails, retry once:

```bash
soda run <issue-number> --from last
```

This resumes from the last failed phase.

### 6.2 After retry failure

If the retry also fails:
1. Label the ticket `soda:failed`
2. Add a comment with the failure details
3. Check if any other tickets depend on this one
4. Label dependents `soda:blocked`
5. Continue with other independent tickets

### 6.3 Escalation

If more than 3 tickets fail in a milestone, **stop the entire
orchestration** and report. This likely indicates a systemic issue
(broken tests, bad config, API outage).

---

## 7 · Issue janitoring

### 7.1 Stale ticket detection

Tickets labeled `soda:in-progress` for more than 2 hours are likely
stale (orphaned SODA run). Check with:

```bash
soda status
```

If no active run exists for the ticket, relabel as `soda:ready` for
retry.

### 7.2 Closing completed tickets

When a SODA run succeeds and the PR is merged:

```bash
gh issue close <number> --reason completed
```

### 7.3 Unlabeling on close

When closing an issue, remove all `soda:*` labels:

```bash
gh issue edit <number> --remove-label "soda:ready,soda:in-progress,soda:done,soda:failed,soda:blocked"
```

---

## 8 · Base health check

Before dispatching any SODA runs, validate the project configuration:

```bash
soda validate
```

If validation fails, **stop and report**. Do not dispatch any runs
until the configuration is fixed.

Also verify the base branch is clean and up to date:

```bash
git checkout <base-branch>
git pull origin <base-branch>
soda doctor
```

---

## Hard rules

1. **Never force-push** to any branch you did not create.
2. **Never merge** a PR without CI passing — SODA handles this via the monitor phase.
3. **Never modify** tickets that are not part of the milestone.
4. **Never exceed** the milestone budget — stop and report instead.
5. **Never break** dependency ordering — wait for dependencies to complete.
6. **Always validate** (`soda validate`) before starting orchestration.
7. **Always report** failures — never silently skip a failed ticket.
8. **All `gh` commands** auto-detect the repo from git remote — do not use `--repo`.
9. **Stop orchestration** if more than 3 tickets fail (systemic failure).
10. **Cycle detection** is mandatory — never attempt to dispatch tickets in a dependency cycle.
