---
phase: 18-reliability-long-tail
fixed_at: 2026-06-11T22:35:00Z
review_path: .planning/phases/18-reliability-long-tail/18-REVIEW.md
iteration: 1
findings_in_scope: 10
fixed: 9
skipped: 1
status: partial
---

# Phase 18: Code Review Fix Report

**Fixed at:** 2026-06-11T22:35:00Z
**Source review:** `.planning/phases/18-reliability-long-tail/18-REVIEW.md`
**Iteration:** 1

**Summary:**
- Findings in scope (Critical + Warning): 10
- Fixed: 9
- Skipped: 1 (WR-01 design-conflict with D-18-04)
- Out-of-scope (INFO): 4 (IN-01 through IN-04 — not addressed per fix_scope=critical_warning)

All commits below were made on `gsd-reviewfix/18-54753` and verified by:
- `go test -race ./...` clean across the full tree (every commit).
- `gofmt -l .` clean.
- `go vet ./...` clean.
- `shellcheck scripts/otto-gw` clean (only the pre-existing SC1091 info
  for the `lib/redact.sh` source statement remains, unrelated to this work).
- Byte-exact site names preserved (`admin-tailer`, `pool-ctx-watcher`,
  `pool-exit-watcher`, `engine-after-func`).
- Boot-time `auth mode` log at `cmd/otto-gateway/main.go:115-120`
  unchanged.

## Fixed Issues

### CR-01: Tailer panic-recovery breaks lazy-restart contract

**Files modified:** `internal/admin/tail.go`, `internal/admin/regression_rel_http_07_test.go`
**Commit:** `2f55cb1`
**Applied fix:** Reset `t.running = false` and `t.cancelRun = nil` under
`t.mu` inside the deferred recover at `Tailer.run`. This restores the
docstring contract ("a subsequent Subscribe will lazy-start a fresh
tailer goroutine"). Added regression test
`TestRegression_REL_HTTP_07_AdminTailer_LazyRestartAfterPanic` that
keeps the first subscriber attached after the panic (so `Unsubscribe`
cannot mask the bug by resetting `running` on subscriber-count drop)
and asserts a second `Subscribe` spawns a fresh goroutine by observing
the probe fire a second time. The test fails without the fix and
passes with it (verified by stash-pop cycle).

### CR-02 + CR-03: PowerShell Import-DotEnv firstError scope mismatch

**Files modified:** `scripts/otto-gw.ps1`
**Commit:** `f0248e2`
**Applied fix:** Both findings share the same root cause — a PowerShell
ForEach-Object child-scope bug — and the same call site, so they were
landed in a single commit. Replaced the local `$firstError` with
`$script:firstError` everywhere it is read inside the pipeline block.
Added `$script:firstError = $null` at function entry to reset between
the two `Import-DotEnv` calls in a single `Load-Config` (one for
`.otto-gw.env`, one for `.otto-gw.overrides.env`). The "first
malformed line wins" contract now matches the bash counterpart at
`scripts/otto-gw:264`.

**Test coverage gap:** `pwsh` is not available on the macOS dev host
(`which pwsh` → not found). The fix was verified via static inspection:
`grep -n 'firstError' scripts/otto-gw.ps1` now shows all references
qualified `$script:` and a single `$script:firstError = $null` reset
at function entry (line 283). A platform-runner regression — Pester
fixture with three malformed lines asserting the sentinel matches the
FIRST one, plus a clean-second-file case asserting the sentinel is
cleared — is `skipped: needs-platform-runner` and should be added in
the Phase 18 follow-up that runs on a Windows / pwsh-Linux CI lane.

### WR-02: KIRO_CWD `~/..` path escape

**Files modified:** `internal/config/config.go`, `internal/config/regression_rel_cfg_06_test.go`
**Commit:** `1a6cc9d`
**Applied fix:** After splitting on `/` (via `filepath.ToSlash`),
reject any path segment exactly equal to `..` in the post-`~/` portion.
Strict segment match (not `strings.Contains`) so legitimate names like
`foo..bar` are not flagged. Added three regression sub-tests:
- `H_KIRO_CWD_tilde_dotdot_rejected` (`~/..`)
- `I_KIRO_CWD_tilde_embedded_dotdot_rejected` (`~/foo/../bar`)
- `J_KIRO_CWD_dotdot_substring_in_segment_allowed` (`~/foo..bar`)
All pass.

### WR-03: HTTP_ADDR bind probe Close() error discarded

**Files modified:** `internal/config/config.go`
**Commit:** `bca4aa3`
**Applied fix:** Replaced `_ = ln.Close()` with `if cerr := ln.Close();
cerr != nil { errs = append(...) }`. A failed Close leaves the probe
listener holding the port and the real `ListenAndServe` would
deterministically fail 5–10s later with EADDRINUSE — defeating the
probe's purpose. The error message follows the existing config-error
naming convention (`config: HTTP_ADDR (%q): bind probe close failed:
%w`).

**Test coverage gap:** No new test. Triggering this branch requires
EBADF / EINTR at `net.Listener.Close()` which is hard to simulate
without an injection seam (the listener is bound by `net.Listen`, not
a test fake). The fix is one branch with clear semantics; documenting
this gap as `skipped: requires-test-seam` is the pragmatic call.

### WR-04: stderrDrainLoop UTF-8 mid-rune slicing

**Files modified:** `internal/acp/client.go`
**Commit:** `49f6d64`
**Applied fix:** Added `unicode/utf8` import. Replaced the naive
`trimmed[:maxLineBytes]` with a walk-back-to-rune-start loop. Cost is
at most 3 byte-walks. The trim is now "≤ maxLineBytes on UTF-8-safe
boundary", documented in the code comment. The existing REL-OBSV-03
byte-cap test continues to pass because its payload is all-ASCII (`X`)
so the cap byte is always a rune start and the trimmed length is
unchanged from the pre-fix.

**Test coverage gap:** No new test for mid-rune payload. The existing
test asserts strict 1 MB length; a multi-byte-rune test would assert
length ≤ 1 MB and that `utf8.Valid(line)` is true. Worth adding in a
follow-up but the fix is mechanically simple enough that the gap is
acceptable for this iteration.

### WR-05: Atomic sentinel write (bash + pwsh)

**Files modified:** `scripts/otto-gw`, `scripts/otto-gw.ps1`
**Commit:** `cadd254`
**Applied fix:** Replaced both `printf > "$sentinel"` (bash) and
`Set-Content -Path $sentinel` (pwsh) with the standard tmp-sibling +
atomic-rename pattern:
- bash: `printf > "${sentinel}.tmp.$$"` then `mv -f ... "$sentinel"`,
  with `rm -f` on rename failure.
- pwsh: `Set-Content -Path "$sentinel.tmp"` then `Move-Item -Force`,
  leaving the tmp on rename failure (throwing inside the catch would
  mask the original Set-Content error).
POSIX rename and NTFS MoveFile on the same filesystem are atomic, so
the tray sees either old or new content but never a zero-byte
intermediate.

**Test coverage gap:** No sentinel-specific test script exists on the
bash side. The behavior change is mechanical (same content, same
modes, same cap) and verified by `bash -n` + `shellcheck`. A
follow-up phase could add a Bats test fixture exercising the
mid-write race window if the operational signal demands it.

### WR-06: bash sentinel refuses /tmp fallback when $HOME unset

**Files modified:** `scripts/otto-gw`
**Commit:** `a9e8f42`
**Applied fix:** `config_error_sentinel_path` now returns non-zero
when `$HOME` is unset / empty, instead of falling back to
`/tmp/.otto-gw/.config-error`. Callers (`clear_config_error_sentinel`,
`write_config_error_sentinel`) short-circuit via
`sentinel="$(config_error_sentinel_path)" || return 0`. The stderr
WARN is still emitted, preserving the operator diagnostic path.

Verified via an in-script test exercising three cases (HOME set,
HOME unset, HOME explicitly empty string) — all behave correctly.

The Go tray-side reader at `cmd/otto-tray/poller.go:31-34` was
already correctly returning `""` when `$HOME` is empty; this fix
closes the wrapper-write side.

### WR-07: previous_pid=0 ambiguity in lazy-respawn-success log

**Files modified:** `internal/pool/pool.go`
**Commit:** `4342da3`
**Applied fix:** Added a code comment documenting that `previous_pid=0`
indicates a non-spawned (NewWithConn / test-fake) client. In production
every slot is spawned via `cfg.Factory.Spawn` so `previous_pid > 0`
always — the field is emitted unconditionally to keep the structured
log shape stable for downstream parsers.

**Why not elide:** The reviewer offered two fixes (elide vs. comment).
Eliding the field conditionally would change the structured-log
contract for downstream consumers (Splunk / ELK pipelines may have
schemas keyed on the presence of `previous_pid`). Per the prompt skip
rule ("Any fix that would require new behavior the operator hasn't
agreed to … changing public API"), the log shape was preserved.

## Skipped Issues

### WR-01: stderrDrainLoop ReadString unbounded accumulation

**File:** `internal/acp/client.go:391-422`
**Status:** `skipped: design-conflict with D-18-04`
**Reason:**

The reviewer's primary fix swaps `bufio.Reader.ReadString('\n')` for
a `reader.ReadByte()` manual loop with per-line cap + drain-to-newline
semantics. This directly conflicts with the prompt's D-18-04
constraint: "**D-18-04 must continue to use `bufio.Reader.ReadString('\n')`
(NOT `bufio.Scanner`)**". The reviewer's alternate suggestion
("accept the design and document the bound") is documentation-only
and provides no behavioral protection against the memory-DoS surface
described in the finding (50 MB of stderr without a newline still
forces the bufio internal buffer to grow to 50 MB before the per-line
cap fires).

Apparent options under the D-18-04 constraint:
1. Wrap the pipe in `io.LimitReader` — but this puts a budget on
   TOTAL bytes drained ever, silently stopping drainage after N bytes
   (worse failure mode than the current one).
2. Use `bufio.NewReaderSize` with a bounded internal buffer — but
   `ReadString` calls `ReadBytes` which appends to a growing
   application-side `[]byte`, so the unbounded thing is the output
   slice, not the bufio internal buffer.
3. Replace `ReadString` with `ReadSlice` in a manual loop (returns
   `bufio.ErrBufferFull` on overflow) — this preserves "line-by-line
   from bufio.Reader" semantics but isn't strictly `ReadString('\n')`
   either.

Because the design fix conflicts with the locked invariant and the
documentation-only fix provides no behavioral value, this finding
needs operator-level reconciliation. Recommended path: revisit D-18-04
in a follow-up phase to clarify whether "ReadString('\n')" is a
hard binding or a stand-in for "line-by-line, not Scanner". If the
former, document the memory-DoS bound; if the latter, apply the
ReadByte-loop fix the reviewer described.

**Original issue:** `stderrDrainLoop` `ReadString` accumulates
unbounded internal buffer before the 1 MB per-line cap is applied;
worst-case transient memory under attack is N × incoming-stderr-volume
per slot.

---

## Notes on out-of-scope (Info) findings

Per `fix_scope=critical_warning`, the four Info findings (IN-01
through IN-04) were not addressed:

- IN-01: documented benign double-close (no fix required).
- IN-02: documented ordering note (no fix required).
- IN-03: documented one-time-cache behavior (no fix required).
- IN-04: cosmetic separator-literal preference (no fix required).

None of these affect correctness or the Phase 18 invariants.

---

_Fixed: 2026-06-11T22:35:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
