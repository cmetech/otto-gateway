---
finding: P-6
severity: M
rel_id: REL-POOL-06
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# Finding P-6: Windows cmd.Cancel() Is a Silent No-Op — Grandchildren Orphaned, Leader Kill Delayed 2s

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §1 P-6 (Medium):

> **Files:** `internal/acp/pool_pgid_windows.go:15` (`applyPgidAttr` no-op) and `:21` (`killProcessGroup` returns nil — a no-op that reports success), `internal/acp/client.go:317-326` (`cmd.Cancel` wired to that no-op; `WaitDelay = 2s`), `internal/acp/client.go:1182-1184` (post-Wait defensive pgrp kill — also a no-op on Windows).
>
> **Failure scenario:** On ctx cancel, `cmd.Cancel` runs the no-op and returns nil — Go treats the command as "successfully interrupted" and only after `WaitDelay` (2s) falls back to `TerminateProcess` on the *leader only*. So on every Windows slot teardown/respawn: (a) the worker lives 2 extra seconds, and (b) any kiro-cli children (MCP servers, tool helpers) are never killed. The file's comment ("handled by job objects / kernel close") describes machinery that does not exist anywhere in the repo.

## Current-source check

**Current file:line:** `internal/acp/pool_pgid_windows.go:15` (applyPgidAttr no-op), `internal/acp/pool_pgid_windows.go:21` (killProcessGroup no-op returning nil).

The failure path is intact in current main. Both functions in `pool_pgid_windows.go` are stubs:

- `applyPgidAttr(cmd)` at line 15: empty body `{}` — no job object created, no process group set.
- `killProcessGroup(pid, sig)` at line 21: `return nil` — a no-op that reports success. The comment "handled by job objects / kernel close" describes machinery that does not exist in the repository (confirmed via repo-wide grep: no `CreateJobObject`, no `AssignProcessToJobObject`, no `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` anywhere in the codebase).

The consequence is exactly as described: when `exec.CommandContext`'s `cmd.Cancel` (wired to `applyPgidAttr` / `killProcessGroup` via `client.go:317-326`) runs on ctx cancel, it returns nil, Go sees "successfully interrupted", and only falls back to `TerminateProcess(leader)` after `WaitDelay=2s`. Grandchildren (MCP servers, tool helpers spawned by kiro-cli) are never signaled.

## Evidence

Evidence files:

1. **Go test stub:** `internal/acp/regression_rel_pool_06_test.go::TestRegression_REL_POOL_06_WindowsPgidNoop`

   Skip string: `t.Skip("REL-POOL-06 (P-6): manual reproducer — see tests/reliability/manual/REL-POOL-06-repro.go for Windows-only reproducer")`

   Discoverability stub only — the failure is Windows-process-tree-specific and cannot be exercised in `go test` on Linux/macOS CI.

2. **Manual reproducer:** `tests/reliability/manual/REL-POOL-06-repro.go`

   Standalone `package main` Windows-only Go program with Pattern F header. Spawns a kiro-cli child and polls for orphaned grandchildren after `cmd.Cancel()`. Pre-fix exit code 0 = orphans detected; post-fix exit code 1 = clean tree death.

   Run: `GOOS=windows GOARCH=amd64 go build -o repro.exe ./tests/reliability/manual/ && .\repro.exe`

## Verdict

**confirmed**

`pool_pgid_windows.go:15` is a no-op with no corresponding job object implementation anywhere in the codebase. The comment describing "job objects / kernel close" is aspirational documentation for machinery that was never built. Per D-11 bias: confirmed. The code-walk is sufficient for a Medium finding — no Go-test reproducer is required (D-02) and the manual script provides operator-runnable evidence on the target platform.
