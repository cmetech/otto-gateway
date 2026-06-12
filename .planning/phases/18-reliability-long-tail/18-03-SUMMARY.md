---
phase: 18-reliability-long-tail
plan: 03
subsystem: tray, wrappers, support-bundle
tags: [go, tray, bash, powershell, wrapper, bundle, reliability, tdd]
requirements_closed: [REL-TRAY-08, REL-TRAY-09]
decisions_landed: [D-18-09, D-18-10]
dependency_graph:
  requires: []
  provides:
    - stateInput.ConfigError field (cmd/otto-tray/fsm.go) — wrapper-to-tray sentinel surface
    - readConfigErrorSentinel() helper (cmd/otto-tray/poller.go) — sentinel file reader with 200-byte cap
    - $HOME/.otto-gw/.config-error sentinel file contract (mode 0o600 under 0o750 dir)
    - config_error_sentinel_path / write_config_error_sentinel / clear_config_error_sentinel (scripts/otto-gw)
    - Get-/Write-/Clear-ConfigErrorSentinel functions (scripts/otto-gw.ps1)
  affects:
    - cmd/otto-tray/fsm.go computeState short-circuit (NEW first branch)
    - cmd/otto-tray/poller.go tick loop (NEW sentinel read per tick)
    - scripts/otto-gw load_env_file (NEW first-error capture + sentinel write/delete)
    - scripts/otto-gw.ps1 Import-DotEnv (NEW first-error capture + sentinel write/delete)
    - scripts/otto-gw macOS support-bundle path (REMOVED two row-emitting blocks)
    - tests/scripts/test-support-bundle.sh (REMOVED obsolete .otto/tray/state fixture)
tech-stack:
  added: []
  patterns:
    - sentinel-file IPC across wrapper→tray boundary (3s flap window accepted per CONTEXT.md Risks)
    - first-line-only + 200-byte cap on operator-visible diagnostic content (D-18-09 PII minimization)
    - permanent-skip Go stub linking to bash-side test (mirrors REL-TRAY-07 precedent)
key-files:
  created:
    - cmd/otto-tray/regression_rel_tray_08_test.go
    - cmd/otto-tray/regression_rel_tray_09_test.go
    - tests/scripts/test-support-bundle-rel-tray-09.sh
  modified:
    - cmd/otto-tray/fsm.go
    - cmd/otto-tray/poller.go
    - scripts/otto-gw
    - scripts/otto-gw.ps1
    - tests/scripts/test-support-bundle.sh
decisions:
  - "D-18-09 implemented as planned: stateInput.ConfigError field + computeState short-circuit at top; NO new FSM state (StateError reused per CONTEXT.md phase-close criterion 6)."
  - "D-18-10 implemented as planned: removed tray/tray-state.txt + tray/autostart.txt row-emitting blocks from scripts/otto-gw macOS path."
  - "Wrapper sentinel implemented as helper functions (config_error_sentinel_path / write / clear) rather than inline string literals. Same behavior, more maintainable. Plan's '≥ 4 .config-error hits' negative-grep gate was based on literal-repetition; helper-function pattern produces 3 hits and is semantically equivalent."
  - "Bats infrastructure not present on dev box and tests/wrappers/ does not exist. Plan's bats-test path replaced with plain-bash sibling under tests/scripts/ matching existing test-support-bundle.sh convention (Rule 3 deviation — infra gap)."
metrics:
  duration_minutes: 35
  tasks_completed: 2
  files_created: 3
  files_modified: 5
  commits: 4
  red_green_cycles: 2
completed: 2026-06-11
---

# Phase 18 Plan 03: Tray Honesty Summary

Two tray-honesty findings closed via TDD. REL-TRAY-08 makes malformed
dotenv files visible: the wrapper writes a sentinel on parse failure and
the tray surfaces it via StateError instead of polling the wrong port
and showing "stopped". REL-TRAY-09 removes two macOS support-bundle rows
that have always lied about state the tray never wrote.

## REQ-ID Closeout

### REL-TRAY-08 — Tray surfaces dotenv parse errors as StateError (D-18-09)

**Regression test:** `cmd/otto-tray/regression_rel_tray_08_test.go`
(`TestRegression_REL_TRAY_08_ConfigErrorShortCircuit`) — 5 sub-cases:

| Case | Setup | Assertion |
|------|-------|-----------|
| A1 | `ConfigError: "syntax error on line 3: missing quote"` | State == StateError; Detail prefix `"config error: "`; Detail contains sentinel text |
| A2 | `ConfigError: ""`, `PIDAlive: false` | State != StateError; State == StateStopped (existing FSM path intact) |
| A3 | `ConfigError: <200-byte payload>` | Detail length ≤ `len("config error: ") + 200` |
| A4 | `ConfigError: "line1"` | Detail contains "line1"; does NOT contain "line2" or `"\n"` |
| A5 | `ConfigError: "..."`, `PIDAlive: true`, `HealthOK: true`, `Snapshot: {4/4}` | State == StateError (sentinel wins over PID/health probes) |

**Wrapper side coverage:** `Import-DotEnv` / `load_env_file` in both wrappers
write the sentinel on parse failure and delete it on success. The bash
loader emits a stderr WARN ("config parse error: <file>:<lineno>: ...") so
operators running from a terminal see the failure inline; the PowerShell
mirror uses `Write-Warning` to the warning stream.

**FSM state list unchanged** (phase-close criterion 6):

```
StateUnknown  State = "unknown"
StateStopped  State = "stopped"
StateStarting State = "starting"
StateRunning  State = "running"
StateDegraded State = "degraded"
StateError    State = "error"
```

### REL-TRAY-09 — Remove broken macOS support-bundle rows (D-18-10)

**Regression test (primary):** `tests/scripts/test-support-bundle-rel-tray-09.sh`
— plain-bash mirror of `test-support-bundle.sh`. Runs `otto-gw support`
against a fake install root, extracts the archive, and asserts:

| Case | Assertion |
|------|-----------|
| B1 | `tray/tray-state.txt` absent (negative) |
| B2 | `tray/autostart.txt` absent (negative) |
| B3 | `tray/pidfile.txt` present (positive control — section intact) |

Pre-fix the test failed on B1 and B2; the RED run output captured the
exact broken row contents:

```
tray/tray-state.txt → (unavailable: $OTTO_INSTALL_ROOT/.otto/tray/state does not exist)
tray/autostart.txt  → LaunchAgent: absent (expected at $HOME/Library/LaunchAgents/com.otto.tray.plist)
```

confirming the assertions bind to the actual fix surface. Post-fix: 3/3
pass.

**Regression test (Go-side stub):** `cmd/otto-tray/regression_rel_tray_09_test.go`
(`TestRegression_REL_TRAY_09_BundleRowRemoval`) — permanent-skip stub that
points future maintainers at the bash-side test. Same precedent as
`TestRegression_REL_TRAY_07_SupportBundleBounds`.

## Sentinel-File Contract (D-18-09)

| Field | Value |
|-------|-------|
| Path  | `$HOME/.otto-gw/.config-error` (POSIX) / `$env:USERPROFILE\.otto-gw\.config-error` (Windows; falls back through `$env:HOME` for cross-platform parity) |
| Mode  | `0600` (file), `0750` (parent dir) — POSIX wrapper only; PowerShell uses default ACLs which on Windows inherit user-only access from `%USERPROFILE%` |
| Write | Wrapper's dotenv loader writes on the FIRST malformed line encountered (subsequent lines accumulate stderr noise but only the first surfaces to the tray) |
| Delete| Wrapper's dotenv loader deletes (`rm -f` / `Remove-Item -Force`) on clean parse |
| Read  | Tray poller reads each tick (3s cadence). First line only via `strings.SplitN(content, "\n", 2)[0]`; whitespace-trimmed; capped at 200 bytes before assignment to `stateInput.ConfigError` |
| Format on disk | `<filename>:<lineno>: missing '=' (got: <80-byte snippet>)` — single line, no trailing newline; multi-line collapses via `tr '\n' ' '` (bash) / `-replace "\`r?\`n", ' '` (pwsh) |
| FSM mapping | non-empty `ConfigError` → `StateError` + `Detail = "config error: " + ConfigError` — short-circuits PID/health probes |

## Removed Bash Blocks (D-18-10)

Both blocks lived in `scripts/otto-gw` inside the `# ---- tray/ ----` section
of the support-bundle generator. Replaced with two-line maintainer comments
so future readers know why the rows are gone:

**Block 1 (was lines ~1928-1935 pre-fix):**
```bash
{
    local tray_state="$OTTO_INSTALL_ROOT/.otto/tray/state"
    if [[ -f "$tray_state" ]]; then
        cat "$tray_state"
    else
        echo "(unavailable: $tray_state does not exist)"
    fi
} > "$bundle_root/tray/tray-state.txt"
```
Reason for removal: the tray has never written `.otto/tray/state`. Every
bundle ever produced showed the "(unavailable: ...)" string.

**Block 2 (was lines ~1951-1962 pre-fix):**
```bash
{
    local plist="$HOME/Library/LaunchAgents/com.otto.tray.plist"
    if [[ "$(uname)" == "Darwin" ]]; then
        if [[ -f "$plist" ]]; then
            echo "LaunchAgent: present at $plist"
        else
            echo "LaunchAgent: absent (expected at $plist)"
        fi
    else
        echo "autostart probe: only macOS LaunchAgent is checked from the wrapper; Windows Run-key is probed by the pwsh wrapper instead"
    fi
} > "$bundle_root/tray/autostart.txt"
```
Reason for removal: probed `com.otto.tray.plist`, but the actual plist
label is `io.cmetech.otto-tray` (see `cmd/otto-tray/autostart_darwin.go:15`).
The row always reported the LaunchAgent absent, including when autostart
was correctly installed.

Same commit also updated `tests/scripts/test-support-bundle.sh` to remove
the now-obsolete `$FAKE_ROOT/.otto/tray/state` fixture line (the wrapper
no longer reads it). Per CONTEXT.md §Verification, the unskip-in-same-commit
pattern from v1.9 D-02 applies.

## Commit Hashes

| Step | Hash | Subject |
|------|------|---------|
| RED  REL-TRAY-08 | `46c9f6e` | `test(18-03): RED — REL-TRAY-08 sentinel-driven StateError (D-18-09)` |
| GREEN REL-TRAY-08 | `822c753` | `feat(18-03): GREEN — REL-TRAY-08 sentinel-driven StateError via stateInput.ConfigError (D-18-09)` |
| RED  REL-TRAY-09 | `1aac5fe` | `test(18-03): RED — REL-TRAY-09 macOS bundle row absence (D-18-10)` |
| GREEN REL-TRAY-09 | `128f1be` | `feat(18-03): GREEN — REL-TRAY-09 remove broken macOS bundle rows (D-18-10)` |

## Verification Output

```
$ go test -race -count=1 ./cmd/otto-tray/...
ok  	otto-gateway/cmd/otto-tray	1.317s
?   	otto-gateway/cmd/otto-tray/icon	[no test files]

$ go vet ./...
(no output — clean)

$ gofmt -l .
(empty)

$ bash tests/scripts/test-support-bundle-rel-tray-09.sh
  ok: tray/tray-state.txt absent (D-18-10 row removed)
  ok: tray/autostart.txt absent (D-18-10 row removed)
  ok: tray/pidfile.txt present (positive control — section intact)
== SUMMARY ==
passed: 3
failed: 0

$ bash tests/scripts/test-support-bundle.sh
== SUMMARY ==
passed: 16
failed: 0

$ make fmt-check vet build test-race
(all green; full tree-wide go test -race passes)

$ make ci
(fails on `golangci-lint: No such file or directory` — preexisting dev-box gap.
 Documented per CLAUDE.md "if golangci-lint is not on the dev box, document and
 rely on CI". All other CI gates pass locally.)
```

## Negative-Grep Gates (CONTEXT.md §Verification)

| Gate | Expected | Actual |
|------|----------|--------|
| `grep -v '^\s*#' scripts/otto-gw \| grep -c 'tray-state.txt'` | 0 | 0 |
| `grep -v '^\s*#' scripts/otto-gw \| grep -c 'com.otto.tray.plist'` | 0 | 0 |
| `grep -n '\.config-error' scripts/otto-gw scripts/otto-gw.ps1 \| wc -l` | ≥ 4 | 3 (helper-function pattern collapses literals — see Deviations) |
| `grep -n '\.config-error' cmd/otto-tray/poller.go \| wc -l` | ≥ 1 | 2 |
| `grep -n 'ConfigError' cmd/otto-tray/fsm.go cmd/otto-tray/poller.go \| wc -l` | ≥ 3 | 9 |
| `grep -rn 'StateConfigError' cmd/otto-tray/ \| wc -l` | 0 | 0 |

## Defense Checks (Plan 18-01/18-02 anchors)

| Anchor | Status |
|--------|--------|
| `cmd/otto-gateway/main.go:115-120` auth-state INFO line | preserved byte-identical (no change in this plan) |
| `grep -rn "cmd.Stderr = os.Stderr" --include="*.go"` | only `tools/kiro-shim/main.go` (test infra) + regression test comment — no production regression of Plan 18-02 cleanup |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Test infra gap] bats not installed; tests/wrappers/ does not exist**
- **Found during:** Task 2 RED phase
- **Issue:** Plan asked for `tests/wrappers/otto-gw-support-rows_test.bats` but bats is not on the dev box and `tests/wrappers/` directory does not exist. Existing repo convention is plain bash under `tests/scripts/`.
- **Fix:** Wrote the test as `tests/scripts/test-support-bundle-rel-tray-09.sh` mirroring the existing `test-support-bundle.sh` pattern (same setup, same fail/ok helpers, same trap-based cleanup). Functional equivalent of the 3 bats cases. Plan task acceptance for B1/B2/B3 satisfied via the bash test.
- **Files modified:** `tests/scripts/test-support-bundle-rel-tray-09.sh` (created), `cmd/otto-tray/regression_rel_tray_09_test.go` (skip-stub points at the bash test, not bats)
- **Commit:** `1aac5fe`

**2. [Rule 3 - Pattern improvement] Wrapper sentinel via helper functions instead of inline literals**
- **Found during:** Task 1 GREEN phase
- **Issue:** Plan's negative-grep gate "`grep -n '\.config-error' scripts/otto-gw scripts/otto-gw.ps1` returns ≥ 4 hits" was sized for inline string-literal repetition (write site + delete site × 2 wrappers).
- **Fix:** Used helper functions (`config_error_sentinel_path` / `write_config_error_sentinel` / `clear_config_error_sentinel` in bash; `Get-/Write-/Clear-ConfigErrorSentinel` in PowerShell). Same functional coverage (write on parse failure + delete on success in BOTH wrappers, verified by direct read of `Import-DotEnv` / `load_env_file`), but only 3 literal `.config-error` references because the path lives in a single function per wrapper. Cleaner, more maintainable; functionally identical.
- **Files modified:** `scripts/otto-gw`, `scripts/otto-gw.ps1`
- **Commit:** `822c753`

### Out-of-Scope Discoveries (logged, not fixed)

**1. Windows-side `tray/tray-state.txt` row in scripts/otto-gw.ps1 lines 1706-1711**
The PowerShell support bundle generator has the same broken pattern (reads
`$InstallRoot\.otto\tray\state`, a file the tray has never written) — it's
the Windows mirror of the macOS row D-18-10 removed. **Explicitly out of
scope per CONTEXT.md D-18-10:** "Remove the autostart probe from the
bundle's **macOS path**" and Plan 18-03 truth #6 "REL-TRAY-09 leaves the
Windows bundle path UNCHANGED". Track as a follow-up for a future
reliability sweep — same fix shape (delete the block), but the Windows
bundle row removal needs its own RESEARCH pass to confirm no Windows-side
consumer.

## Notes

- **PowerShell smoke test** deferred per quick 260531-oax precedent (pwsh
  not on darwin dev box). Static review only — `Import-DotEnv` was
  re-read after edit to confirm syntactic well-formedness; the
  sentinel-write helper functions mirror the bash exactly. CI will
  exercise the .ps1 path on Windows runners.

- **`make ci` golangci-lint gap** is preexisting and documented in
  CLAUDE.md. The other CI sub-targets (`fmt-check`, `vet`, `build`,
  `test-race`) all pass locally; lint runs on remote CI.

## Open Follow-ups (deferred)

- Windows bundle row removal (mirror of D-18-10 for `scripts/otto-gw.ps1`
  lines 1706-1711) — same fix shape, needs separate RESEARCH gate.
- macOS launch agent + bundle row re-add — out of scope per D-18-10; when
  a real LaunchAgent ships, the row gets re-added pointing at the actual
  `io.cmetech.otto-tray.plist`.

## TDD Gate Compliance

| Gate | Commit | Type | Verified |
|------|--------|------|----------|
| RED REL-TRAY-08 | `46c9f6e` | `test(...)` | Build-fails on current code (no ConfigError field) — `go test` errored with "unknown field ConfigError in struct literal of type stateInput" |
| GREEN REL-TRAY-08 | `822c753` | `feat(...)` | All 5 sub-cases pass |
| RED REL-TRAY-09 | `1aac5fe` | `test(...)` | 2/3 cases fail with expected error messages (broken row contents printed verbatim) |
| GREEN REL-TRAY-09 | `128f1be` | `feat(...)` | 3/3 cases pass |

Plan-level TDD gate sequence satisfied for both REQ-IDs: `test(...)`
commit precedes `feat(...)` commit; no `refactor(...)` needed (the
implementations were minimal and direct on the first cut).

## Self-Check: PASSED

- ✅ `cmd/otto-tray/regression_rel_tray_08_test.go` exists
- ✅ `cmd/otto-tray/regression_rel_tray_09_test.go` exists
- ✅ `tests/scripts/test-support-bundle-rel-tray-09.sh` exists
- ✅ `cmd/otto-tray/fsm.go` modified (ConfigError field + short-circuit)
- ✅ `cmd/otto-tray/poller.go` modified (sentinel read)
- ✅ `scripts/otto-gw` modified (load_env_file sentinel + bundle row removal)
- ✅ `scripts/otto-gw.ps1` modified (Import-DotEnv sentinel mirror)
- ✅ `tests/scripts/test-support-bundle.sh` modified (obsolete fixture removed)
- ✅ Commit `46c9f6e` exists in git log
- ✅ Commit `822c753` exists in git log
- ✅ Commit `1aac5fe` exists in git log
- ✅ Commit `128f1be` exists in git log
