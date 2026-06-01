---
quick_id: 260531-ra6
type: execute
subsystem: pool-lifecycle
tags: [pool, slot-release, process-group, sigterm, wrapper-reap, kiro-cli, posix, powershell]

requires:
  - phase: 5
    provides: pool.Pool slot lifecycle (Codex M-3 release-on-every-terminal-path)
provides:
  - Defense-in-depth regression test for Pool.Cancel-without-Result-drain release
  - kiro-cli subprocesses are pgrp leaders on darwin/linux so SIGTERM cascades
  - scripts/otto-gw stop reaps orphaned $KIRO_CMD subprocesses on POSIX + Windows
affects: [phase-5 pool, internal/acp client lifecycle, operator wrapper UX]

tech-stack:
  added:
    - syscall.SysProcAttr.Setpgid (darwin/linux build-tagged file)
    - Win32_Process CIM ExecutablePath match (Windows wrapper)
  patterns:
    - "Build-tagged unix/windows split with matching signatures (applyPgidAttr / killProcessGroup) — first build-tagged file pair in internal/"
    - "Path-exact subprocess match (never basename) — reap NEVER touches operator-launched kiro-cli outside the gateway"

key-files:
  created:
    - internal/acp/pool_pgid_unix.go
    - internal/acp/pool_pgid_windows.go
  modified:
    - internal/pool/pool_test.go         # TestPool_Cancel_ReleasesSlot_WithoutResultDrain regression
    - internal/acp/client.go             # applyPgidAttr(cmd) before cmd.Start()
    - scripts/otto-gw                    # reap_kiro_orphans() + wiring
    - scripts/otto-gw.ps1                # Repair-KiroOrphans + wiring

key-decisions:
  - "Task 1 ships the regression test only (no production code change). The Cancel-without-drain test PASSED against the current Pool.Cancel implementation — the existing code already satisfies the Codex M-3 contract for the synchronous Cancel terminal path. Test is committed as defense-in-depth (test() commit, not fix())."
  - "applyPgidAttr lives in internal/acp (single spawn site at client.go cmd.Start) rather than internal/pool — keeps the syscall import scoped to the package that owns subprocess creation."
  - "killProcessGroup is exposed but unused in this task — exec.CommandContext's existing SIGKILL on ctx-cancel is now delivered to the pgrp leader, which IS the kiro-cli root. Future shutdown paths (Phase 2 close hooks) can use the helper explicitly."
  - "Wrapper reap matches by EXACT path (resolved via `command -v` POSIX / `Get-Command` Windows) — never by basename. Operator-side kiro-cli outside the gateway is unaffected (T-ra6-01 mitigation)."

patterns-established:
  - "Build-tag conventions: explicit `//go:build darwin || linux` (NOT `!windows` reverse tag). Mirror signatures across the unix/windows files so the call site is platform-agnostic."
  - "Wrapper-side orphan reap: one-shot guard ($_reap_kiro_done / $script:KiroReapDone) + ancestor self-protection ($$ + $PPID / $PID) + 2s grace → TERM → 2s grace → KILL."

requirements-completed: [RA6-01, RA6-02, RA6-03]

duration: ~30min
completed: 2026-05-31
---

# Quick Task 260531-ra6: Pool slot release on Cancel + kiro-cli process hygiene — Summary

**Three small, related lifecycle hygiene fixes shipped as three atomic commits: regression test asserting Pool.Cancel releases slots without Result drain (Issue 1), kiro-cli children promoted to their own process group so SIGTERM to the gateway cascades (Issue 2), and `otto-gw stop` wrappers reap stray $KIRO_CMD subprocesses on hard-crash paths (Issue 3).**

## Performance

- **Tasks:** 3 / 3
- **Files modified:** 5
- **Lines added:** ~360
- **Completed:** 2026-05-31

## Task Commits

1. **Task 1: Cancel slot-release regression test (RA6-01)** — `49340f7` (test)
   - Adds `TestPool_Cancel_ReleasesSlot_WithoutResultDrain` in `internal/pool/pool_test.go`.
   - Drives the Pool.Cancel terminal path WITHOUT closing the stream or calling Result.
   - Asserts Stats.Busy=0 + Alive=1 within 250 ms after Cancel.
2. **Task 2: Spawn kiro-cli in own process group (RA6-02)** — `6ff1ade` (fix)
   - New `internal/acp/pool_pgid_unix.go` (`//go:build darwin || linux`): `applyPgidAttr` sets `SysProcAttr.Setpgid=true`; `killProcessGroup(pid, sig) = syscall.Kill(-pid, sig)`.
   - New `internal/acp/pool_pgid_windows.go` (`//go:build windows`): no-op stubs with matching signatures.
   - `internal/acp/client.go` calls `applyPgidAttr(cmd)` immediately before `cmd.Start()`.
3. **Task 3: Wrapper reap of stray $KIRO_CMD (RA6-03)** — `9f23c4d` (fix)
   - `scripts/otto-gw`: `reap_kiro_orphans()` resolves $KIRO_CMD via .env loader + `command -v`, scans `ps -eo pid,command` for exact match, TERM → 2s → KILL. Wired into both `stop()` and `stop_by_name()` with a one-shot guard. shellcheck + `bash -n` clean.
   - `scripts/otto-gw.ps1`: `Repair-KiroOrphans` mirror via `Get-CimInstance Win32_Process` filtered by `ExecutablePath -ieq $env:KIRO_CMD` (with CommandLine -like fallback). Wired into `Stop-Gateway` and `Stop-GatewayByName`.

Note: the `260531-ra6-PLAN.md` and `260531-ra6-SUMMARY.md` files are committed separately by the orchestrator's docs sweep, not as part of the per-task code commits.

## Why

### Issue 1 — Pool.Cancel slot release (RA6-01)
The plan referenced a 260531-oox observation that the engine watchdog's cancel path (engine.go:207 `context.AfterFunc → e.cfg.ACP.Cancel`) did not produce a `pool.release` log on production traces. Investigation found:

- Production wiring at `cmd/otto-gateway/main.go:289` is `ACP: a.pool` (the pool IS the engine's ACPClient for the no-X-Session-Id path).
- `Pool.Cancel` (pool.go:628) already captures `slot.Client` under p.mu (WR-04), forwards `client.Cancel(sid)`, then calls `releaseSlotForSession` which performs the map-delete-first guard and sends the slot back to `p.slots`.
- The new regression test PASSES against the current code — `Pool.Cancel` already satisfies the Codex M-3 "release on every terminal path" contract for the synchronous Cancel-without-drain shape.

Per the TDD fail-fast rule, no production code fix shipped in Task 1. The test was kept as a defense-in-depth assertion that future refactors of `Pool.Cancel` / `releaseSlotForSession` / `poolStreamWrapper` cannot regress the contract. The original operator-visible "slot-N permanently Busy" symptom (if it recurs with this regression shipped) would have to be elsewhere — adapter ctx propagation, the per-session registry path, or a code path that bypasses Pool.Cancel — and is out of scope for this task per its scope-lock.

### Issue 2 — kiro-cli process group adoption (RA6-02)
Go's `exec.Cmd` defaults to placing children in the parent's process group. SIGTERM/SIGKILL to the gateway therefore did NOT propagate to kiro-cli on macOS/Linux. On hard exit, kiro-cli reparented to init (pid 1) and outlived the gateway. The fix promotes the child to its OWN process-group leader at spawn time (`SysProcAttr.Setpgid = true`); exec.CommandContext's existing SIGKILL-on-ctx-cancel is now delivered to the leader, who is the root of the kiro process tree.

### Issue 3 — Wrapper reap (RA6-03)
Belt-and-suspenders for hard-crash paths (segfault, OOM, `Stop-Process -Force` of the gateway) that bypass even the Task 2 process-group cascade. The wrapper resolves `$KIRO_CMD` to its absolute path and scans the OS process table for an EXACT match — never a basename substring — so an operator running kiro-cli outside the gateway is unaffected. Bounded: 2 s grace → TERM → 2 s grace → KILL. No retry loop.

## Manual reproducer — Issue 2 (Unix process group)

1. Build and start the gateway: `./scripts/otto-gw start`.
2. Drive any chat request that warm-spawns kiro-cli, OR just rely on the pool warmup (it pre-spawns N kiro-cli on boot). `ps -A -o pid,pgid,command | grep -E 'kiro-cli|otto-gateway'` lists the parent + children with their pgids.
3. Let `$G` be the gateway pid and `$K` one kiro-cli pid. Confirm `ps -o pid,pgid -p $K` shows `pgid == $K` (i.e. $K is its OWN pgrp leader). Pre-fix, `pgid` would equal $G's pgrp.
4. `kill -TERM $G`. Within a few seconds, `$K` exits. Pre-fix, `$K` survives and is reparented to init/pid-1.

## Manual reproducer — Issue 3 (wrapper reap)

### POSIX

1. Start the gateway: `./scripts/otto-gw start`.
2. Simulate a hard crash that bypasses the Task 2 pgrp cascade: `kill -KILL $(cat .otto/gw/otto-gateway.pid)`.
3. Confirm a stray kiro-cli is running by checking `ps -A -o pid,command | awk -v cmd="$(command -v "$KIRO_CMD")" '$2 == cmd'` (or simply `pgrep -F "$(command -v "$KIRO_CMD")"`).
4. Run `./scripts/otto-gw stop`. Expect:
   - `otto-gw: reaping stray kiro-cli orphans: <pid-list>`
   - `otto-gw: kiro-cli orphans reaped`
   - Post-stop, no kiro-cli at the resolved $KIRO_CMD path.

### PowerShell (Windows)

1. Start the gateway: `.\scripts\otto-gw.ps1 start`.
2. Hard-crash: `Stop-Process -Id (Get-Content .\.otto\gw\otto-gateway.pid) -Force`.
3. Confirm a stray kiro-cli: `Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -ieq (Get-Command $env:KIRO_CMD).Source }`.
4. Run `.\scripts\otto-gw.ps1 stop`. Expect the same `otto-gw: reaping ...` / `otto-gw: kiro-cli orphans reaped` lines, and no surviving process at the resolved path.

## What was NOT touched (scope lock honoured)

- engine watchdog (engine.go AfterFunc)
- SSE emitter / Anthropic adapter / Ollama adapter / OpenAI adapter
- PII redaction hook (260531-pt8) / chat-trace hook (260531-ll2)
- guardrail chain wiring
- acp_adapter shim
- pool's poolStreamWrapper, ctx-watcher goroutine, releaseSlotForSession signature
- general config layer (only reads $KIRO_CMD inside the new reap functions)

The plan's scope-lock explicitly excluded all of the above. No "while I'm here" refactors were taken.

## Tests

| What | How |
| ---- | --- |
| Issue 1 regression | `TestPool_Cancel_ReleasesSlot_WithoutResultDrain` in `internal/pool/pool_test.go` |
| Issue 2 behaviour | Manual reproducer above (subprocess lifecycle is awkward to unit-test cleanly) |
| Issue 3 POSIX | Manual reproducer above; `shellcheck scripts/otto-gw` + `bash -n scripts/otto-gw` clean |
| Issue 3 Windows | Manual reproducer above |

## Verification results

| Gate | Result |
| ---- | ------ |
| `go build ./...` | clean |
| `GOOS=windows go build ./...` | clean |
| `go test ./internal/pool/... ./internal/engine/... ./internal/acp/... -race -count=1` | all green |
| `shellcheck scripts/otto-gw` | clean (no findings) |
| `bash -n scripts/otto-gw` | clean (exit 0) |
| `go test -run TestPool_Cancel_ReleasesSlot_WithoutResultDrain -v` | PASS |

## Self-Check

- `49340f7` (test): `internal/pool/pool_test.go` — FOUND in git log
- `6ff1ade` (fix): `internal/acp/{client.go, pool_pgid_unix.go, pool_pgid_windows.go}` — FOUND in git log
- `9f23c4d` (fix): `scripts/{otto-gw, otto-gw.ps1}` — FOUND in git log
- All files referenced in `key-files` exist on disk in the worktree.
- All three commits reference `260531-ra6` in their subject or body.

**Self-Check: PASSED**

## Deviations from Plan

### [Rule 1 - Diagnosis] Task 1 shipped as `test()` not `fix()`

- **Found during:** Task 1 Step 1 (RED phase).
- **Issue:** The plan instructed a TDD RED → DIAGNOSE → GREEN cycle for `Pool.Cancel`. Step 1's failing-test gate did NOT fail — the regression test passed against the current code on the first run.
- **Diagnosis:** Per Task 1 Step 2, verified production wiring (`cmd/otto-gateway/main.go:289`: `ACP: a.pool`) and walked Pool.Cancel → releaseSlotForSession line by line. The existing code already satisfies the documented Codex M-3 contract for the synchronous Cancel terminal path. Either the operator symptom that drove the plan was observed on a different code path (per-session registry, adapter-level ctx propagation, or surface-handler), or it has already been incidentally fixed by an earlier commit in this milestone.
- **Action:** Per the TDD fail-fast rule AND the plan's scope-lock ("if you encounter unexpected state during Task 1's pool diagnose, write up what you found and STOP rather than refactoring"), no production code fix was shipped in Task 1. The regression test was committed as defense-in-depth so a future refactor cannot regress the contract.
- **Files modified:** internal/pool/pool_test.go (test added).
- **Commit:** `49340f7` (`test:` prefix to truthfully reflect that no production code shipped).

### Cross-worktree contamination during Task 1 commit

- **Found during:** Task 1 commit step.
- **Issue:** The first commit for Task 1 (`b7d70b1`) landed on the parent repo's `main` branch instead of the worktree's per-agent branch because a bash shell call had its CWD silently reset between invocations (#3097 / #3099 cwd-drift class). The Edit tool calls in Task 2 also wrote files into the parent repo's checkout rather than the worktree's checkout because absolute file paths supplied to the tool resolved to the parent repo's working tree.
- **Action:** Cherry-picked `b7d70b1` from parent main into the worktree branch as `49340f7`, then re-applied the Task 2 file creates/edits inside the worktree (`internal/acp/pool_pgid_{unix,windows}.go`, `internal/acp/client.go`), and reverted the parent repo's working-tree changes via per-file `git checkout --` plus untracked-file removal. The parent repo's `main` HEAD still has the stale `b7d70b1` test-only commit at the tip.
- **Recommended follow-up by the user:** Reset parent-repo `main` from `b7d70b1` to `e621489` (the previous tip) so the worktree's branch is the only carrier of the ra6 commits. Per the worktree commit protocol's destructive-prohibition rules I MUST NOT do this reset myself — protected-ref rewinds in the worktree-execution context are explicitly forbidden (#2924).
  - Suggested command (run from the parent repo, NOT inside the worktree):
    ```
    cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway
    git checkout main
    git reset --hard e621489dfdb4cf8e890270f618eae2ed84e068a8
    ```
  - Verify with `git log --oneline -3` afterwards.

## Known Stubs

None.

## Threat Flags

None. No new network endpoints, auth paths, or schema changes. Subprocess spawn site already had a `nolint:gosec G204` annotation (env-var-controlled, not user input) — same disposition unchanged.
