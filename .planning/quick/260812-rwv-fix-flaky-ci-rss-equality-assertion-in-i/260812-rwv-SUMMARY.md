---
quick_id: 260812-rwv
status: complete
date: 2026-08-12
commit: 1528f7b
---

# Quick Task 260812-rwv — CI flake investigation

Goal: get `ci.yml` on `main` running without failures. Baseline was **4 of the
last 12 runs green**.

## Failure census (last 12 `ci.yml` runs on main)

| Job | Failures | Root cause | Status |
|-----|----------|-----------|--------|
| `lint + test-race + arch-lint + govulncheck` | 3 | RSS byte-equality assertion | **fixed** (`1528f7b`) — confirmed green on run 31653410481 |
| `privacy + support parity (Windows)` | 3 | ERROR_SHARING_VIOLATION during managed-secret rotation | **fixed** (`b679a70`) — CI-verified only |
| `privacy parity (macos-latest)` | 1 | old broken commit (2026-08-02), since fixed | not live |

## Fixed — RSS byte-equality assertion

`cmd/otto-gateway/main_test.go:102` took `wantSample := procstat.Read(pid)`,
then called the wired `got.ReadWorkerMemory(pid)`, and compared the two RSS
values for exact equality. RSS is live process state; the process allocates
between the two calls. Observed drift was ~128 KB:

```
main_test.go:102: ReadWorkerMemory(self) = (58400768, true), want (58269696, true)
```

**Why it never reproduced locally:** `internal/procstat/procstat_other.go`
(build tag `!linux && !windows`) returns `Sample{}` on darwin, so the check
degenerated to `0 == 0` and passed unconditionally — including 60 isolated runs
and 8 full-package `-race` runs on this machine. The assertion was dead on the
dev box and flaky on the only platform where it ran for real.

**Fix:** assert the wiring, which is the test's actual purpose — `ok` tracks
`procstat.Supported()` (a per-platform constant, so deterministic), a supported
read reports non-zero bytes, an unsupported one reports zero.

## Diagnosed, NOT fixed — Windows managed-secret rotation

Both surviving Windows symptoms trace to one condition: **transient Win32 file
errors while `ReplaceFile` swaps a managed-secret file**, which on GitHub's
Windows runners come from Defender/indexer briefly holding a handle.

There is a clear asymmetry between the two sides of that swap:

| Side | Location | Transient-error policy |
|------|----------|------------------------|
| Reader (test) | `privacy_secrets_windows_test.ps1:218` | bounded retry on `0x80070020` (SHARING_VIOLATION) and `0x80070002` (FILE_NOT_FOUND), 100 ms deadline |
| Writer (product) | `support-safe-open.ps1:430` `PublishAtomicWindows` | **none** — throws on the first failure |

Symptom A (run 31652008956, today): the writer hit it →
`gw.ps1 init failed: ... atomic support publication failed`.
Symptom B (run 30934956443, 2026-08-04): the reader exhausted its 100 ms
retry budget → `continuous reader failed while secrets were replaced: READ-ERROR:...`.
Note B post-dates the 2026-08-02 reader-hardening commits (`5c19afd`,
`a761910`), so the current bounds are still too tight.

This is a **product** gap, not only a test gap: on a real Windows box with a
scanner touching the gateway config directory, `gw.ps1 init` can fail outright
for a reason that resolves in milliseconds.

**Step 1 (commit `1528f7b`) — diagnosability.** `PublishAtomicWindows` now names
the Win32 code in its message. Previously `Win32Exception(int, string)` retained
`NativeErrorCode` but printed only the custom message, so CI logs carried no
code to triage.

**Step 2 — the code, confirmed.** The very next CI run (31653410481) failed on
the reader side with `READ-ERROR:System.IO.IOException:-2147024864` =
`0x80070020` = **ERROR_SHARING_VIOLATION**. Decisive detail: that code was
*already on the reader's retryable list*. The codes were right; the 100 ms
window was not.

**Step 3 (commit `b679a70`) — both sides fixed, on your call.**

- Writer (`PublishAtomicWindows`): bounded 5 s retry with escalating backoff
  over `ERROR_SHARING_VIOLATION` (32), `ERROR_LOCK_VIOLATION` (33) and
  `ERROR_USER_MAPPED_FILE` (1224). Retrying preserves every guarantee the method
  makes — each attempt is a single atomic primitive, and a failed attempt leaves
  both the destination and the sibling temporary untouched.
  **`ERROR_ACCESS_DENIED` is deliberately excluded**: a permission failure on a
  managed-secret file is a security signal and must fail closed rather than spin.
- Reader (test): retry deadline widened 100 ms → 5 s to match the writer budget.

The forced-failure test is unaffected: `GW_TEST_MANAGED_SECRET_REPLACE_FAILURE`
throws in `gw.ps1:1041`, *before* `Publish-SupportFileAtomically` is reached, so
it still fails fast rather than burning the retry budget.

**Verification limits, stated plainly:** the retry path itself cannot be
executed on this macOS box — `PublishAtomicWindows` throws
`PlatformNotSupportedException` off Windows. What was verified locally: the
inline C# still compiles under `pwsh`, both files pass the CI PowerShell parse
gate, and the portable publish path (first publication, replacement, temp
cleanup, sibling guard) passes end to end. The Windows-only retry behaviour is
verified by CI runs alone.

## Verification

- `go test ./cmd/otto-gateway/ -run TestApplyIdleMemoryRecyclePoolConfig -count=30` — pass
- `go build ./...`, `go vet ./cmd/...` — clean
- `golangci-lint v2.12.2 run ./cmd/otto-gateway/...` — 0 issues
- `gofumpt -l cmd/` — clean
- `support-safe-open.ps1` re-verified under `pwsh` on macOS: inline C# type still
  compiles via `Initialize-SupportSafeOpen`, and the CI PowerShell parse gate
  (`Parser::ParseFile`) reports clean

## Expected effect

Both live failure classes addressed. The RSS fix is confirmed (run 31653410481
had `lint + test-race` green). The Windows fix needs a few clean runs to call
settled, since it targets a probabilistic condition — a single green run proves
less here than it would for a deterministic bug.
