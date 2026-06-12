---
phase: 18-reliability-long-tail
reviewed: 2026-06-12T00:00:00Z
depth: standard
iteration: post-fix
files_reviewed: 34
files_reviewed_list:
  - cmd/otto-gateway/main.go
  - cmd/otto-gateway/testmain_test.go
  - cmd/otto-tray/fsm.go
  - cmd/otto-tray/poller.go
  - cmd/otto-tray/regression_rel_tray_08_test.go
  - cmd/otto-tray/regression_rel_tray_09_test.go
  - internal/acp/client.go
  - internal/acp/regression_rel_obsv_03_test.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/ollama/regression_rel_http_06_test.go
  - internal/admin/regression_rel_http_07_test.go
  - internal/admin/regression_rel_obsv_04_test.go
  - internal/admin/tail.go
  - internal/config/config.go
  - internal/config/config_test.go
  - internal/config/regression_rel_cfg_05_test.go
  - internal/config/regression_rel_cfg_06_test.go
  - internal/config/regression_rel_cfg_07_test.go
  - internal/config/regression_rel_obsv_04_test.go
  - internal/config/testmain_test.go
  - internal/engine/engine.go
  - internal/engine/regression_rel_http_07_test.go
  - internal/pool/config.go
  - internal/pool/exit_watcher.go
  - internal/pool/exit_watcher_test.go
  - internal/pool/pool.go
  - internal/pool/pool_test.go
  - internal/pool/regression_rel_http_07_test.go
  - internal/pool/regression_rel_obsv_02_test.go
  - scripts/otto-gw
  - scripts/otto-gw.ps1
  - tests/scripts/test-support-bundle-rel-tray-09.sh
  - tests/scripts/test-support-bundle.sh
findings:
  critical: 0
  warning: 2
  info: 3
  total: 5
status: issues_found
open_followups:
  - id: WR-01
    title: bufio.Reader.ReadString unbounded accumulation in stderrDrainLoop
    state: deferred
    rationale: |
      Original review flagged unbounded ReadString growth as a memory-DoS surface.
      The fixer skipped this per CONTEXT.md D-18-04, which locks the
      bufio.Reader/ReadString pattern. Recommendation stands: v1.10.4 ADR + bounded
      ReadString variant. Re-review confirms no regression vs. pre-fix; the
      vulnerability remains.
---

# Phase 18: Code Review Report (Iteration 2 — Post-Fix)

**Reviewed:** 2026-06-12
**Depth:** standard
**Files Reviewed:** 34
**Status:** issues_found (2 WARNING + 3 INFO; no new BLOCKERs; one prior WARNING deferred)

## Summary

Adversarial re-review of the gsd-code-fixer's `2f55cb1`..`4342da3` pass. Nine of
ten in-scope items from the first review (CR-01..CR-03 + WR-02..WR-07) land
cleanly with regression tests that exercise the post-fix behavior. WR-01 was
intentionally deferred and surfaced under `open_followups`.

**Fix-by-fix verdict (verified against the source):**

| ID | Site | Status |
|----|------|--------|
| CR-01 | `internal/admin/tail.go:330-350` — `t.running=false` + `t.cancelRun=nil` set under `t.mu` in defer-recover; new `TestRegression_REL_HTTP_07_AdminTailer_LazyRestartAfterPanic` proves lazy restart works post-panic. | LANDED |
| CR-02 | `scripts/otto-gw.ps1:295,306,308,321-324` — every read of `firstError` now uses `$script:firstError`. | LANDED |
| CR-03 | `scripts/otto-gw.ps1:295` — `$script:firstError = $null` reset at function entry; closes "second-invocation inherits prior state" path. | LANDED |
| WR-02 | `internal/config/config.go:336-355` — `..` segment-by-segment check after `~/` expansion, with `~/foo..bar` (substring) still allowed (boundary case J). | LANDED |
| WR-03 | `internal/config/config.go:703-705` — bind-probe `Close()` error surfaced via `errs.append(...)`. | LANDED |
| WR-04 | `internal/acp/client.go:407-423` — UTF-8 rune-boundary slice walk-back with `utf8.RuneStart`. | LANDED (see WR-09 below for a subtle bounds question) |
| WR-05 | `scripts/otto-gw:243-261` (bash tmp+`mv -f`) + `scripts/otto-gw.ps1:262-277` (PowerShell tmp+`Move-Item -Force`). | LANDED |
| WR-06 | `scripts/otto-gw:214-219` — `config_error_sentinel_path` returns non-zero when `$HOME` unset; `write_config_error_sentinel` short-circuits. | LANDED (see IN-02 for a behavioral note on the symmetric PowerShell side) |
| WR-07 | `internal/pool/pool.go:336-347,415-419` — comment block documents `previous_pid=0` for non-spawned (NewWithConn / test fake) clients. | LANDED |
| WR-01 | `internal/acp/client.go:392-439` — DEFERRED. `bufio.Reader.ReadString('\n')` still accumulates an unbounded string when stderr has no `\n` terminator. The 1MB cap fires AFTER read, so the buffer can grow to RAM-exhaustion before that branch is reached. Recommend a v1.10.4 ADR. | DEFERRED |

**Phase 18 invariants spot-checked:**

- All 4 panic-recover sites present byte-exact: `admin-tailer`
  (`tail.go:334`), `pool-exit-watcher` (`exit_watcher.go:49`),
  `pool-ctx-watcher` (`pool.go:967`), `engine-after-func` (`engine.go:267`). VERIFIED via grep across `internal/pool/` `internal/admin/` `internal/engine/`.
- `cmd.Stderr = os.Stderr` absent from `cmd/` + `internal/`. VERIFIED. (One `cmd.Stderr = &stderr` exists in `cmd/otto-tray/runner.go` — captures to a buffer, NOT routing to os.Stderr; different pattern, acceptable.)
- `cmd/otto-gateway/main.go:115-120` auth-state INFO log byte-identical. VERIFIED.
- `stateInput.ConfigError` field still present at `cmd/otto-tray/fsm.go:33`; `computeState` short-circuit at top (lines 50-54). VERIFIED.
- D-18-04 still uses `bufio.NewReader` + `ReadString('\n')`, NOT Scanner (`acp/client.go:397,399`). VERIFIED.
- D-18-05 `lazy-respawn-success` reason string byte-exact (`pool.go:426`). VERIFIED.
- D-18-06 `worker_pid: 0` placeholder + REL-HTTP-03 mirrored fields (`ollama/handlers.go:260-271, 559-570`). VERIFIED.
- D-18-09 `~/.otto-gw/.config-error` sentinel path; no `StateConfigError` added (`fsm.go:33,52`; `poller.go:35`). VERIFIED.
- D-18-10 broken macOS rows (`com.otto.tray.plist`, `tray-state.txt`) removed and stay removed (`tests/scripts/test-support-bundle-rel-tray-09.sh:121-136` negative-asserts both). VERIFIED.

## Warnings

### WR-08: PowerShell sentinel write silently falls back to `$env:TEMP` when both `$HOME` and `$USERPROFILE` are unset

**File:** `scripts/otto-gw.ps1:231-234`

The bash wrapper (`scripts/otto-gw:214-219`) implements WR-06 by REFUSING to
write the sentinel when `$HOME` is unset — the rationale being that `/tmp` is
world-writable and the sentinel may carry a partial `KEY=VALUE` from the
malformed dotenv line (including fragments of `AUTH_TOKEN`, `PII_HASH_KEY`,
or `PII_ENCRYPT_KEY`). That refusal is correct and well-documented.

The PowerShell sibling silently falls back to `$env:TEMP` when BOTH `$env:HOME`
and `$env:USERPROFILE` are unset:

```powershell
function Get-ConfigErrorSentinelPath {
    $home_ = if ($env:HOME) { $env:HOME } elseif ($env:USERPROFILE) { $env:USERPROFILE } else { $env:TEMP }
    return (Join-Path $home_ '.otto-gw\.config-error')
}
```

On Windows the `Get-ConfigErrorSentinelPath`-then-`Write-ConfigErrorSentinel`
flow will write a (potentially secret-bearing) sentinel into
`%TEMP%\.otto-gw\.config-error`. `%TEMP%` per-user permissioning on Windows is
better than POSIX `/tmp`, BUT the symmetry contract WR-06 was supposed to
establish ("no `$HOME` ⇒ no sentinel") is broken on the PowerShell side.

This is a real-world divergence: the bash and PowerShell wrappers are
documented as equivalent ports, and operators reading the WR-06 fix commit
will assume the PowerShell side has the same refusal.

**Fix:** Mirror the bash refusal — return `$null` from
`Get-ConfigErrorSentinelPath` when both `$env:HOME` and `$env:USERPROFILE`
are unset; have `Write-ConfigErrorSentinel` short-circuit when the path is
`$null`. Drop the `$env:TEMP` fallback.

```powershell
function Get-ConfigErrorSentinelPath {
    $home_ = if ($env:HOME) { $env:HOME } elseif ($env:USERPROFILE) { $env:USERPROFILE } else { $null }
    if (-not $home_) { return $null }
    return (Join-Path $home_ '.otto-gw\.config-error')
}

function Write-ConfigErrorSentinel {
    param([string]$Msg)
    $sentinel = Get-ConfigErrorSentinelPath
    if (-not $sentinel) { return }   # WR-06: refuse when HOME unset
    # ... rest unchanged
}
```

### WR-09: WR-04 rune-boundary truncation may discard up to maxLineBytes-1 bytes in the pathological "all continuation bytes" case

**File:** `internal/acp/client.go:418-422`

The WR-04 fix walks back from `n = maxLineBytes` to find a UTF-8 rune-start
byte:

```go
n := maxLineBytes
for n > 0 && !utf8.RuneStart(trimmed[n]) {
    n--
}
trimmed = trimmed[:n]
```

The loop terminates when either `utf8.RuneStart(trimmed[n])` is true OR
`n == 0`. The `n == 0` case is reachable only if every byte from 0 through
`maxLineBytes` is a continuation byte (`0x80..0xBF`), which is invalid UTF-8
and not producible by a well-formed UTF-8 producer.

However, if kiro-cli's stderr is corrupted (binary garbage, a wedged
multi-megabyte log line in a non-UTF-8 encoding, etc.) the loop walks the
full `maxLineBytes` (1MB = 1,048,576) iterations and returns `trimmed[:0]`
— an EMPTY line in the log record. The operator who hits this case sees a
WARN with `"line": ""` and no diagnostic content at all, while the real
stderr payload is silently discarded.

Two concerns:

1. **No telemetry on the truncation.** The truncation path is silent — no
   metric, no separate WARN counting "stderr line corrupted, all
   continuation bytes". An operator looking at empty-`line` WARNs has no
   way to distinguish "kiro-cli produced an actual empty stderr line" from
   "we ate a 2MB corrupted-encoding payload."

2. **Walk cost.** The walk is O(maxLineBytes) = 1M iterations per corrupted
   line. Each iteration calls `utf8.RuneStart` (a one-byte mask test, cheap)
   so the total cost is small in absolute terms but is a per-line tax on the
   drain goroutine's pace under attack — a stderr-flood from a hostile
   subprocess (e.g. a fuzzer scenario) can drive the drain into 100% CPU.

**Fix:** Detect the `n == 0` exhaustion case and emit a distinct log line so
operators see "stderr line was invalid UTF-8; truncated to empty" rather
than a silent empty record:

```go
n := maxLineBytes
for n > 0 && !utf8.RuneStart(trimmed[n]) {
    n--
}
if n == 0 {
    if c.cfg.Logger != nil {
        c.cfg.Logger.Warn(
            "kiro-cli stderr: invalid UTF-8 (no rune-start byte found in cap window); truncating to empty",
            "worker_pid", pid,
            "bytes_dropped", len(trimmed),
        )
    }
    continue  // skip the empty Warn below
}
trimmed = trimmed[:n]
```

This is a low-severity addition — it does not change correctness of the
production path, but it closes an observability gap in exactly the scenario
WR-04 was supposed to harden.

## Info

### IN-01: `clear_config_error_sentinel` race window unchanged by WR-05

**File:** `scripts/otto-gw:226-230`

WR-05 made `write_config_error_sentinel` atomic via tmp+rename. The matching
`clear_config_error_sentinel` is `rm -f "$sentinel"` (line 229), which is
already atomic on POSIX. No fix needed; flagging for completeness so the
asymmetry between "atomic write" and "naive rm" is not later mistaken for a
WR-05 regression.

### IN-02: PowerShell Import-DotEnv successful parse of overrides file masks a parse error from a prior .env file

**File:** `scripts/otto-gw.ps1:321-327`

`Import-DotEnv` writes the sentinel on parse error and calls
`Clear-ConfigErrorSentinel` on clean parse. The two-call sequence from
`Initialize-Config` is:

1. `Import-DotEnv .otto-gw.env` — parse error → sentinel written
2. `Import-DotEnv .otto-gw.overrides.env` — clean parse → sentinel CLEARED

If the operator's `.otto-gw.env` is broken but their overrides file is
clean, the parse error from step 1 is silently wiped by step 2. The tray
then sees no `ConfigError` and computes a normal `Stopped`/`Running` state,
exactly the pre-fix REL-TRAY-08 failure mode.

This is the same shape as the bash wrapper's `load_env_file` behavior at
`scripts/otto-gw:308-310` so it's consistent across platforms — but the
"overrides file's clean parse hides .env's broken parse" behavior is not
documented anywhere I can see. May be intentional (overrides is the final
word) but operators will be confused when their broken `.otto-gw.env` does
not surface a tray sentinel.

Recommend either:
- Document the precedence in the dotenv-loader docstrings ("a later clean
  parse clears any sentinel from earlier files"), OR
- Track the cumulative state across both calls and only `Clear...` when
  BOTH files parsed cleanly. (More work, possibly out of scope.)

### IN-03: WR-04 panics not gated by `cap`-aware guard if `len(trimmed) == maxLineBytes`

**File:** `internal/acp/client.go:406-422`

The truncation branch is gated on `len(trimmed) > maxLineBytes`. When
`len(trimmed) == maxLineBytes` we skip the slice walk entirely — correct. But
the `n := maxLineBytes` indexing into `trimmed[n]` inside the loop assumes
`len(trimmed) > maxLineBytes` (strictly greater). The code is correct as
written but the invariant is implicit; a future refactor that changes the
gate to `>=` would introduce an off-by-one panic. Recommend asserting the
invariant with a defensive `if n >= len(trimmed) { n = len(trimmed) - 1 }`
before the loop:

```go
n := maxLineBytes
if n >= len(trimmed) { n = len(trimmed) - 1 }   // defensive
for n > 0 && !utf8.RuneStart(trimmed[n]) {
    n--
}
```

This is purely defense-in-depth — current code is correct. The invariant is
just brittle to a later edit.

## Open Follow-ups

### WR-01 (DEFERRED — not regressed; recommend v1.10.4 ADR)

**File:** `internal/acp/client.go:392-439`

Original review flagged: `stderrDrainLoop` uses `bufio.Reader.ReadString('\n')`,
which accumulates UNBOUNDED bytes in its internal buffer when the producer
emits a multi-MB line with no `\n` terminator. The 1MB cap and the
WR-04-fixed UTF-8 truncation both fire AFTER `ReadString` returns — by which
point the buffer has already grown to whatever the producer pushed (e.g. an
adversarial 100MB-no-newline blast).

Re-review confirms this is still open. The fix-pass intentionally skipped it
per CONTEXT.md D-18-04 (the lock on `bufio.Reader` + `ReadString` was a Wave
0 decision that switching to a different reader pattern is out of scope for
a fix pass).

**Recommended path forward:** v1.10.4 dedicated phase + ADR documenting the
choice between (a) wrapping the reader in an `io.LimitReader(pipe,
maxLineBytes*2)` so accumulation is bounded at the OS-pipe level, or (b)
switching to a bounded-buffer custom reader that flushes at `maxLineBytes`
even mid-line. Option (b) is closer to the existing semantics.

Threat surface today: a compromised kiro-cli (or a tool-server kiro-cli
delegates to) can DoS the gateway with a single multi-MB stderr line. Not a
remote-attacker vector by itself (kiro-cli is local), but is a privilege-
isolation gap if the operator runs untrusted MCP servers via kiro-cli.

---

_Reviewed: 2026-06-12_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
_Iteration: post-fix_
