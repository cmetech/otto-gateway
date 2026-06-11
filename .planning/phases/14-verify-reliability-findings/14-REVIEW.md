---
phase: 14-verify-reliability-findings
reviewed: 2026-06-11T00:00:00Z
depth: quick
files_reviewed: 28
files_reviewed_list:
  - cmd/otto-tray/regression_rel_tray_01_test.go
  - cmd/otto-tray/regression_rel_tray_02_test.go
  - cmd/otto-tray/regression_rel_tray_03_test.go
  - cmd/otto-tray/regression_rel_tray_04_test.go
  - cmd/otto-tray/regression_rel_tray_05_test.go
  - cmd/otto-tray/regression_rel_tray_06_test.go
  - cmd/otto-tray/regression_rel_tray_07_test.go
  - internal/acp/regression_rel_pool_06_test.go
  - internal/adapter/ollama/regression_rel_http_03_test.go
  - internal/adapter/openai/regression_rel_http_02_test.go
  - internal/adapter/openai/regression_rel_http_03_test.go
  - internal/admin/regression_rel_http_05_test.go
  - internal/config/regression_rel_cfg_01_test.go
  - internal/config/regression_rel_cfg_02_test.go
  - internal/config/regression_rel_cfg_03_test.go
  - internal/plugin/regression_rel_hooks_01_test.go
  - internal/pool/regression_rel_cfg_04_test.go
  - internal/pool/regression_rel_pool_01_test.go
  - internal/pool/regression_rel_pool_02_test.go
  - internal/pool/regression_rel_pool_03_test.go
  - internal/pool/regression_rel_pool_04_test.go
  - internal/server/regression_rel_http_01_test.go
  - internal/server/regression_rel_http_04_test.go
  - internal/session/regression_rel_pool_05_test.go
  - tests/reliability/manual/REL-POOL-06-repro.go
  - tests/reliability/manual/REL-TRAY-02-repro.ps1
  - tests/reliability/manual/REL-TRAY-03-repro.sh
  - tests/reliability/manual/REL-TRAY-06-repro.ps1
findings:
  critical: 0
  warning: 2
  info: 1
  total: 3
status: needs_fixes
---

# Phase 14: Code Review Report

**Reviewed:** 2026-06-11
**Depth:** quick (with targeted deep reads for assertion logic)
**Files Reviewed:** 28
**Status:** needs_fixes

## Summary

Read-only verification phase: no production source was modified (confirmed by `git diff --name-only 3a72d03..HEAD -- ':!*_test.go' ':!.planning/' ':!docs/' ':!tests/reliability/'` producing empty output). All 19 regression test scaffolds carry `t.Skip` as the first statement of the test body — no side-effectful production API call executes before the skip. Trust gates (vet, build, race) passed as reported.

Two warnings were found. Both are defects in the test scaffolds themselves that will cause a Phase 15/16 fix commit to either silently pass a broken assertion or have a non-functional self-test mode. Neither is a production-safety issue, but both should be corrected before the skips are lifted.

---

## Warnings

### WR-01: REL-POOL-04 pre-fix assertion is permanently a no-op and its error message is self-contradictory

**File:** `internal/pool/regression_rel_pool_04_test.go:134`

**Issue:** The `escalationFired` counter (declared at line 47 as `atomic.Int32`) is never incremented anywhere in the test body. Nothing wires the fake pool or fake client to call `escalationFired.Add(1)` when a ping escalation occurs. The counter is always zero, so the assertion `if got := escalationFired.Load(); got != 0 { t.Fatalf(...) }` can never fire regardless of whether the bug is present or fixed.

Additionally, the error message in that `Fatalf` reads `"want > 0 (demonstrating readLoop starvation…)"` while the condition that triggers the message is `got != 0` (i.e. got IS greater than zero). The message contradicts the condition: the test aborts when `got > 0` but claims it "wants" `got > 0`. The correct pre-fix assertion should either (a) document that `escalationFired == 0` in the fake-client test because the real pingLoop is not exercised, and omit the misleading message, or (b) wire the counter and flip the condition.

The test as written will silently pass at Phase 16 unskip even if the fix is absent, because the observation mechanism is not connected.

**Fix:** Either remove the dead counter + assertion entirely and document that this test scaffold only verifies structural cause (stream.push context identity), or wire the counter properly and correct the condition/message:

```go
// If the counter is kept, correct the condition to assert the pre-fix observable:
if got := escalationFired.Load(); got == 0 {
    t.Logf("pre-fix observable: escalationFired == 0 — fake client does not exercise the real pingLoop; " +
        "structural cause documented, full integration reproducer is REL-POOL-06-repro.go")
}
// Remove the t.Fatalf("want > 0") line — it fires on the WRONG condition.
```

---

### WR-02: REL-POOL-06 reproducer's `-stub` mode is permanently broken — child rejects `-stub-child` flag

**File:** `tests/reliability/manual/REL-POOL-06-repro.go:47-67`

**Issue:** The `-stub` flag (line 39) makes the binary spawn itself with `-stub-child` as an argument (line 67). However, `main()` only registers one flag (`-stub`). When the spawned child process calls `flag.Parse()`, it receives the unregistered `-stub-child` argument and Go's `flag` package exits with:

```
flag provided but not defined: -stub-child
exit status 2
```

The reproducer's comment at line 47 says "pass `-stub-child` to run in stub mode" but no stub-child mode is implemented. An operator following the run instructions with the `-stub` flag (the intended kiro-cli-free path) will always get exit 2 from the child start, fall into the `SKIP` branch at line 77, and get no reproduction.

**Fix:** Register and handle the stub-child mode in `main()`:

```go
stubChild := flag.Bool("stub-child", false, "run as a stub grandchild process (sleep 30s then exit)")
flag.Parse()

if *stubChild {
    // Act as the grandchild — just sleep long enough for the parent to enumerate and cancel.
    time.Sleep(30 * time.Second)
    os.Exit(0)
}
```

---

## Info

### IN-01: REL-POOL-06 exit codes are inverted from Unix convention (0 = bug present, 1 = fixed)

**File:** `tests/reliability/manual/REL-POOL-06-repro.go:119-131`

**Issue:** The reproducer exits 0 when orphans are detected (bug confirmed) and exits 1 when the process group died cleanly (fixed). This is explicitly documented in the header comment (lines 18-19) and the inline comments (lines 120, 128), so it is intentional — but it is the opposite of Unix convention (0 = success/pass) and opposite of how the other manual reproducers signal their state. A future integrator wrapping this in CI scripting would likely invert the sense accidentally.

The Bash/PowerShell reproducers (TRAY-02, TRAY-03, TRAY-06) all use `exit 1` to signal a fatal setup error and `exit 0` for successful execution (verdict printed to stdout). POOL-06 diverges.

**Fix (optional):** Invert the exit codes and update the header comment to match convention: exit 0 when the binary ran to completion (regardless of verdict), exit 1 when kiro-cli could not be started (the current exit 2 path). Print the ORPHANS/PASS verdict only to stdout so a CI wrapper can parse it independently of exit code. If the current encoding is load-bearing for a specific CI step, leave it but add a prominent `# NOTE: exit 0 means BUG CONFIRMED` comment at the top of main().

---

_Reviewed: 2026-06-11_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: quick (targeted deep reads on assertion logic and manual reproducer safety)_
