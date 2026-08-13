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
| `lint + test-race + arch-lint + govulncheck` | 3 | RSS byte-equality assertion | **fixed here** |
| `privacy + support parity (Windows)` | 3 | transient Win32 file errors during managed-secret rotation | **diagnosed, not fixed** |
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

**Landed here (diagnosability only, no behavior change):**
`PublishAtomicWindows` now names the Win32 code in its message. Previously
`Win32Exception(int, string)` retained `NativeErrorCode` but printed only the
custom message, so CI logs carried no code to triage. The next Windows failure
will identify the exact error.

**Not landed (needs your call):** bounded retry on the writer side, mirroring
the policy already accepted on the reader side. Held back because it changes a
security-sensitive privacy-secret path that cannot be executed on this macOS
box — the Windows-only APIs (`ReplaceFile`, DACL checks) mean verification
would be by CI runs alone.

## Verification

- `go test ./cmd/otto-gateway/ -run TestApplyIdleMemoryRecyclePoolConfig -count=30` — pass
- `go build ./...`, `go vet ./cmd/...` — clean
- `golangci-lint v2.12.2 run ./cmd/otto-gateway/...` — 0 issues
- `gofumpt -l cmd/` — clean
- `support-safe-open.ps1` re-verified under `pwsh` on macOS: inline C# type still
  compiles via `Initialize-SupportSafeOpen`, and the CI PowerShell parse gate
  (`Parser::ParseFile`) reports clean

## Expected effect

Removes the most frequent failure (3/12). Windows remains flaky at roughly
3/12 until the writer-side retry decision is made.
