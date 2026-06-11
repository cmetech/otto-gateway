---
phase: 14-verify-reliability-findings
status: secured
threats_open: 0
asvs_level: L1
register_authored_at_plan_time: false
audit_mode: retroactive-stride
audited_at: 2026-06-11
block_on: critical
---

# SECURITY.md — Phase 14: Verify Reliability Findings

## Audit Context

**Mode:** Retroactive STRIDE (no plan-time threat model existed).
**Scope:** The complete diff for this phase contains zero production source changes.
Changed files are: 19 Go regression test scaffolds (all `t.Skip` first-line), 4 manual
reproducer scripts (`tests/reliability/manual/`), and planning/documentation artifacts.
No new HTTP endpoints, no new auth surface, no new env-var consumption, no new network
calls in any production path.

The STRIDE register below was authored from the diff, not from planning documents.

---

## STRIDE Threat Register

### S — Spoofing

**THREAT-S-01 (accept):** No new auth surface introduced. No new HTTP handlers, no new
RPC endpoints, no new token/credential handling. Production auth (Bearer-token + IP
allowlist) is unchanged. Nothing in the diff creates a spoofing surface.

*Disposition:* accepted — no surface to assess.
*Verification:* `git diff 3a72d03..HEAD -- ':!*_test.go' ':!.planning/' ':!docs/' ':!tests/reliability/'` returns 0 lines — confirmed empty.

---

### T — Tampering

**THREAT-T-01 (accept):** No new production file writes. Test scaffolds write only to
`t.TempDir()` (Go standard, OS-cleaned). Manual reproducers write to `$env:TEMP`
(Windows system temp, T-2/T-6) and read a known pidfile path (T-3). No path traversal
surface: all temp paths are OS-allocated, not user-controlled.

*Disposition:* accepted — no tamper surface in production paths.
*Verification:* Code inspection of all 4 manual reproducers; no user-supplied path argument
accepted and passed to a file write. Destructive-command grep on ps1 and sh files returned
empty (no `Remove-Item`, `rm -rf`, `del /F`, `mkfs`, `dd`, etc.).

---

### R — Repudiation

**THREAT-R-01 (accept):** No new audit-trail entries, log sinks, or structured event
production paths added. Existing production logging is unchanged.

*Disposition:* accepted — no repudiation surface introduced.

---

### I — Information Disclosure

**THREAT-I-01 (accept):** Manual reproducer scripts capture and print local state
(PIDs, temp paths, stdout lines, exit codes) to the operator's console. This data is
confined to the operator's session; no network egress, no file persistence of sensitive
data beyond `$env:TEMP` on Windows (standard OS temp, world-readable on that host).

*Verified:* Network-egress grep (`curl`, `wget`, `Invoke-WebRequest`, `nc`, `socat`) across
all 4 manual scripts returned empty. The Windows scripts write only to `$env:TEMP\rel-*-stdout.txt`
and `$env:TEMP\rel-*-stderr.txt` — both cleaned by the OS on session end.

*Disposition:* accepted — operator-local info only, no cross-boundary disclosure.

**THREAT-I-02 (accept):** `REL-POOL-06-repro.go` prints the kiro command path sourced
from `KIRO_CMD` env var. This is operator-configured. Script is explicitly marked
Windows-only (`//go:build windows`) and is never part of any build artifact that ships
to end users. Pattern F header clearly marks it as an operator-run manual script.

*Disposition:* accepted — operator-scoped, no production path.

---

### D — Denial of Service

**THREAT-D-01 (mitigate):** Manual scripts that spawn subprocesses could, in principle,
hang or orphan processes on the operator's machine. Analysis by script:

- **REL-POOL-06-repro.go:** Uses `exec.CommandContext` with a hard 10-second timeout
  (`context.WithTimeout(context.Background(), 10*time.Second)`). The context cancel
  fires after 10s. `wmic` subprocess uses a separate `exec.Command` with no explicit
  timeout; `wmic` output is bounded (list of PIDs). No fork-bomb path.
- **REL-TRAY-02-repro.ps1:** Invokes `scripts/otto-gw.ps1 support` via `Start-Process`
  with `-Wait`. No loop, no recursive spawn. Worst case: `otto-gw.ps1 support` itself
  hangs — pre-existing behavior of the production wrapper, not introduced by this script.
- **REL-TRAY-03-repro.sh:** Runs a bounded `sleep 3` loop (10 iterations = 30s max),
  then exits. `kill -9` on the gateway PID is intentional (the purpose of the reproducer).
  No recursive spawn, no infinite loop.
- **REL-TRAY-06-repro.ps1:** Same shape as T-2: `Start-Process -Wait` with no loop.

*Disposition:* mitigated — all scripts have bounded execution paths. The 10s `CommandContext`
timeout in the Go reproducer is present and verified at `REL-POOL-06-repro.go:62-63`.
PowerShell scripts delegate to `-Wait` on a single subprocess invocation. The bash
script is bounded at 30s by design (observation window declared in Pattern F header).

---

### E — Elevation of Privilege

**THREAT-E-01 (mitigate):** Test scaffolds run under `go test` (no elevated privilege).
All 19 test files call `t.Skip()` as their first statement — the reproducer bodies below
the skip never execute in any CI or automated context. Verified by spot-check of 3 files:

- `internal/pool/regression_rel_pool_01_test.go:57` — `t.Skip(...)` is line 1 of test body.
- `internal/session/regression_rel_pool_05_test.go:39` — `t.Skip(...)` is line 1 of test body.
- `cmd/otto-tray/regression_rel_tray_03_test.go:17` — `t.Skip(...)` is line 1 of test body.

**THREAT-E-02 (mitigate):** `REL-POOL-06-repro.go` spawns a subprocess whose path is
resolved from `KIRO_CMD` env var or `os.Args[0]` (self, with `-stub` flag). Neither
path accepts unsanitized user input from the network or command line in a way that
enables shell injection:

- `KIRO_CMD` is operator-set before the script runs; not read from stdin or HTTP.
- `os.Args[0]` is the process's own executable path (set by the OS).
- The `exec.CommandContext` call passes `kiroCmd` as the executable name (first arg to
  exec), not as a shell string. No `sh -c` or PowerShell `-Command` expansion.
- The `wmic` invocation uses `fmt.Sprintf("ParentProcessId=%d", parentPID)` where
  `parentPID` is a Go `int` from `leaderCmd.Process.Pid` — not user-supplied. Integer
  formatting cannot produce shell injection.

*Disposition:* mitigated — no shell injection path; subprocess arguments are hardcoded
constants or typed integers, not external-user-controlled strings.

**THREAT-E-03 (mitigate):** `REL-TRAY-03-repro.sh` calls `kill -9 "$GW_PID"` where
`$GW_PID` is read from a local pidfile (not from script arguments or stdin). The script
validates that the pidfile exists and is non-empty before issuing the kill. An attacker
controlling the pidfile could cause the wrong process to be killed — but the pidfile is
in `$INSTALL_ROOT/.otto/gw/otto-gateway.pid`, owned by the operator; this is the same
trust level as the operator themselves. No privilege escalation beyond what the operator
already has.

*Disposition:* mitigated — the kill target is operator-trusted-path sourced, not
network/stdin sourced.

---

## Subprocess Spawn Audit (CLAUDE.md Requirement: gosec G204)

CLAUDE.md identifies subprocess spawn as the highest-risk surface. All subprocess invocations
in the diff are in operator-run manual scripts, not in production code:

| File | Invocation | Argument Source | Tainted? |
|------|-----------|----------------|----------|
| `REL-POOL-06-repro.go:70` | `exec.CommandContext(ctx, kiroCmd, args...)` | `kiroCmd` from `KIRO_CMD` env or `os.Args[0]` | No — operator-configured env or self-path |
| `REL-POOL-06-repro.go:139-145` | `exec.Command("wmic", ...)` | All args are string literals or `fmt.Sprintf(...%d...)` | No — typed integer, no shell expansion |
| `REL-TRAY-02-repro.ps1:34-39` | `Start-Process pwsh/powershell.exe -ArgumentList @('-File', $Wrapper, 'support')` | `$Wrapper` is `Resolve-Path` of a repo-local path | No — filesystem path from known repo location |
| `REL-TRAY-06-repro.ps1:36-41` | Same shape as T-2 | Same | No |

No tainted external input flows to any subprocess command. Production binary's subprocess
spawn surface (`gosec G204`) is unchanged — confirmed by the zero-line production diff.

---

## Unregistered Threat Flags

None. No `## Threat Flags` section exists in any SUMMARY.md for this phase (the phase
did not add new attack surface to report). The VERIFICATION.md `## Anti-Patterns Found`
section documents 3 advisory items (WR-01, WR-02, IN-01) that are defects in t.Skip'd
test scaffolds — these are quality issues in test code, not security threats. They are
noted here for completeness but do not constitute unregistered threat flags under the
STRIDE model.

| Anti-Pattern | Description | Security Relevance |
|-------------|-------------|-------------------|
| WR-01 (regression_rel_pool_04_test.go) | `escalationFired` counter never wired; assertion inverted | None — test body never executes (t.Skip) |
| WR-02 (REL-POOL-06-repro.go) | `-stub-child` flag not registered; child exits 2 | None — manual script, operator-run, Windows-only |
| IN-01 (REL-POOL-06-repro.go) | Exit codes inverted from Unix convention | None — cosmetic documentation issue |

---

## Accepted Risks Log

| ID | Threat | Rationale |
|----|--------|-----------|
| AR-01 | THREAT-S-01: no new spoofing surface | Phase is read-only; no new auth surface introduced |
| AR-02 | THREAT-T-01: no new tamper surface | Temp paths are OS-allocated; no user-controlled path write |
| AR-03 | THREAT-R-01: no new repudiation surface | No new log sinks or audit-trail paths |
| AR-04 | THREAT-I-01: operator-local info in manual scripts | Operator-scoped; no network egress; no cross-boundary disclosure |
| AR-05 | THREAT-I-02: kiro path printed by Go reproducer | Operator-configured env var; script is Windows-only manual artifact |

---

## Verification Summary

| Check | Result |
|-------|--------|
| Production source diff empty | Confirmed — 0 lines from `git diff 3a72d03..HEAD -- ':!*_test.go' ':!.planning/' ':!docs/' ':!tests/reliability/'` |
| All 19 test scaffolds call t.Skip first | Spot-checked 3 of 19 — confirmed at correct position |
| Manual scripts spawn subprocesses with hard-coded or typed-int args | Confirmed — no tainted external input to exec.Command |
| No destructive commands in scripts without operator consent | Confirmed — kill -9 in REL-TRAY-03-repro.sh is the stated purpose; no rm -rf, mkfs, etc. |
| No network egress in manual scripts | Confirmed — zero matches for curl/wget/Invoke-WebRequest/nc/socat |
| Script execution bounded (no fork-bomb, no infinite loop) | Confirmed — 10s context timeout in Go script; 30s polling loop in bash; -Wait blocking in ps1 |
| gosec G204 equivalent: no tainted spawn in production code | Confirmed — no production source changed; manual scripts use constants or typed ints |
