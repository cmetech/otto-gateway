---
finding: T-7
severity: M
rel_id: REL-TRAY-07
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# T-7: Support Bundle Size/Time Unbounded (REL-TRAY-07)

## Review Citation

> **[Medium] T-7: Support bundle size/time is not actually bounded — live-log copies are exempt from the `--max-mb` cap, and `runWrapper`'s 30s SIGKILL produces an opaque failure plus a leaked staging dir**
>
> Files: `scripts/otto-gw:1864-1873` (uncapped live-log copies) and `scripts/otto-gw:1957-1989` (cap drops only `*.log.gz`), `scripts/otto-gw.ps1:1489-1494` + `1586-1596` (same), `cmd/otto-tray/runner.go:29-33` (30s ctx, `Cancel` = SIGKILL, no `WaitDelay`), `scripts/otto-gw:1766` (EXIT trap).
>
> Failure scenario: The size-cap loop deletes only rotated `.log.gz` files; the redacted copies of the current-day `otto-gateway.log` and `chat-trace.log` are copied unconditionally. On timeout: bash wrapper is SIGKILLed — the `trap … EXIT` never runs (staging dir leaks in `$TMPDIR`), in-flight sed/tar children keep pipes open so `cmd.Run` blocks until they finish, then the user gets "Failed to create support bundle." with empty stderr.

## Current-Source Check

Verified against current source (worktree at commit `3a72d03`):

- **`scripts/otto-gw:1864-1873` (live-log copies):** Lines 1864–1873 show unconditional copies of `otto-gateway.log`, `otto-gateway-boot.log`, and `otto-gateway-chat-trace.log` via `redact_stream < "$src" > "$bundle_root/logs/$dst"`. No size check before or after. **Failure path 1 intact.**

- **`scripts/otto-gw:1957-1989` (cap enforcement):** Lines 1957–1989 show the `du -sm` check and the `gz_list` loop that deletes only `otto-gateway-*.log.gz` files. Live copies of non-rotated `.log` files at `$bundle_root/logs/` are not candidates for deletion. When DEBUG/CHAT_TRACE are on, the current day's log can be hundreds of MB — the cap loop cannot reduce them. **Failure path 1 intact.**

- **`cmd/otto-tray/runner.go:29-33`:** Lines 29–33 show:
  ```go
  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
  defer cancel()
  cmd := exec.CommandContext(ctx, cmdName, args...)
  ```
  `exec.CommandContext` with a cancelled context sends SIGKILL (`cmd.Cancel` default is `SIGKILL` pre-Go1.20; with Go 1.20+ it uses `os.Process.Kill` which is SIGKILL on Unix). No `cmd.WaitDelay` is set, so after SIGKILL the `cmd.Run()` call blocks until all pipes are drained (in-flight sed/tar children hold the pipes). **Failure path 2 intact.**

- **`scripts/otto-gw:1766` (EXIT trap):** The bash `trap` at line 1766 registers a cleanup function, but bash `trap ... EXIT` does NOT run when the process is killed with SIGKILL (SIGKILL bypasses signal handlers and traps). The staging directory is leaked. **Failure path 2 confirmed.**

- **`scripts/otto-gw.ps1:1489-1494` and `:1586-1596`:** The PowerShell equivalent has the same structure — live log copies before the cap check, with the cap only applying to rotated archives. **Failure path 1 confirmed on Windows too.**

## Evidence

Regression test: `cmd/otto-tray/regression_rel_tray_07_test.go` — `TestRegression_REL_TRAY_07_SupportBundleBounds`

The test is skipped with `t.Skip("REL-TRAY-07 (T-7): regression test — unskip in Phase 16 fix commit")`. The body documents the full pre-fix reproducer outline: create a 100MB fake log file, invoke the wrapper with `--max-mb 10`, stat the resulting bundle, and assert its size exceeds the cap (pre-fix observable: no enforcement on live logs).

## Verdict

**confirmed** — Three independent failure paths are all present: (1) live-log copies are outside the `--max-mb` cap enforcement loop in both bash and PowerShell wrappers; (2) bash `trap ... EXIT` does not run on SIGKILL so staging is leaked on timeout; (3) `runWrapper`'s 30s context cancels with SIGKILL but no `WaitDelay` means `cmd.Run()` can block indefinitely while pipes drain. Phase 16 scope: include live-log files in the cap enforcement; use bash `trap` SIGTERM instead of relying on EXIT after SIGKILL; set `cmd.WaitDelay` in `runWrapper` for clean timeout behavior.
