---
finding: T-1
severity: H
rel_id: REL-TRAY-01
status: confirmed
target_phase: 15
verified_at: 2026-06-11
---

# T-1: PID Identity Is Never Verified (REL-TRAY-01)

## Review Citation

> **[High] T-1: PID identity is never verified — Stop/Restart can kill an unrelated process, and a stale pidfile wedges the tray in an unrecoverable "error" state**
>
> Files: `scripts/otto-gw:553-561` (start), `scripts/otto-gw:776-786` (stop), `scripts/otto-gw.ps1:388-394` (Start-Gateway), `scripts/otto-gw.ps1:560-566` (Stop-Gateway), `cmd/otto-tray/tray.go:144-151` (makeProbe), `cmd/otto-tray/fsm.go:40-50`.
>
> Failure scenario: Both wrappers and the tray probe treat "pid in pidfile is alive" as "that pid is otto-gateway" — nothing checks the process name/command line, even though identity-aware machinery exists (`gateway_pids()` at `otto-gw:646`, `Stop-GatewayByName` in the ps1) but is only used when the pid is *dead*. Windows recycles PIDs aggressively; macOS recycles after reboot, and the pidfile survives both. After a crash/power loss: (1) the OS hands the PID to another process; (2) the tray probe sees alive-pid + failing `/health` → FSM shows error; (3) user clicks **Start** → wrapper says "already running (PID N)", exit 1 — Start fails forever with no in-UI recovery; (4) user clicks **Stop**/**Restart** instead → `kill "$pid"` / `$proc.Kill()` **kills the innocent recycled-PID process**, then Restart masks what happened.

## Current-Source Check

Verified against current source (worktree at commit `3a72d03`):

- **`scripts/otto-gw:776-786` (stop function):** Lines 776–788 contain `stop()` which reads the PID with `cat "$OTTO_PID"` and immediately calls `kill "$pid"` without any cmdline or process-name verification. The `kill -0` check at line 782 only tests liveness — no identity check. **Failure path intact.**

- **`scripts/otto-gw.ps1:560-566` (Stop-Gateway):** Lines 558–570 contain `Stop-Gateway` which calls `Get-Process -Id $storedPid` and then `$proc.Kill()` — no `.Path` or cmdline comparison before killing. `$proc.Path -eq $BinPath` is available but unused here. **Failure path intact.**

- **`cmd/otto-tray/tray.go:144-151` (makeProbe):** Lines 140–163 show `makeProbe()` calling `processAlive(pid)` with no identity guard. `processAlive` only tests signal-0 / `GetExitCodeProcess` liveness. There is no name/cmdline cross-check before returning `alive = true`. **Failure path intact.**

- **`cmd/otto-tray/fsm.go:40-50` (computeState):** Lines 39–61 show `computeState` consumes `in.PIDAlive` directly with no identity context available. The state machine has no identity field in `stateInput`. **Failure path intact.**

No process-name or cmdline verification has been added since the review. Identity-aware machinery (`gateway_pids()` / `Stop-GatewayByName`) exists in the scripts but is only used on the dead-PID fallback path, not the live-PID primary path.

## Evidence

Regression test: `cmd/otto-tray/TestRegression_REL_TRAY_01_PIDIdentityUnchecked`

The test writes a pidfile containing `os.Getpid()` (the test binary's own PID), reads it back via `readPIDFile`, confirms `processAlive` returns `true`, and documents the absence of an identity guard via comment. The test is skipped via `t.Skip("REL-TRAY-01 (T-1): regression test — unskip in Phase 15 fix commit")` so CI stays green. Phase 15 removes the skip and adds the `verifyGatewayIdentity` call that should reject the test binary.

Test file: `cmd/otto-tray/regression_rel_tray_01_test.go`

## Verdict

**confirmed** — PID identity check is absent from all three code paths cited by the review (bash wrapper stop, PowerShell wrapper Stop-Gateway, tray makeProbe). No mitigation guard has been added since the review. The failure trigger (recycled PID after gateway crash on any OS) is realistic and the blast radius is catastrophic (SIGKILL sent to an unrelated process). Phase 15 scope: add `verifyGatewayIdentity` before any `kill`/`$proc.Kill()` call on a live pidfile PID.
