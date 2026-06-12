---
phase: 18-reliability-long-tail
plan: 02
subsystem: observability
tags: [go, slog, goroutine-recovery, structured-logging, defense-in-depth, panic-recovery]

# Dependency graph
requires:
  - phase: 18-reliability-long-tail
    plan: 01
    provides: Config struct additive-field convention (REL-CFG-05/06/07); deriveChatTraceFile helper exists; KIRO_CMD/HTTP_ADDR validation pattern
  - phase: 15
    provides: REL-HTTP-03 WARN field set (worker_pid, kiro_exit_code, bytes_streamed, session_id, request_id, err) — mirrored verbatim by REL-HTTP-06
  - phase: 5
    provides: pool.PoolClient interface (extended in this plan with Pid() int); engine.callPreHookSafe defer-recover template (mirrored at 4 sites)
provides:
  - Symmetric structured logs for kiro-cli stderr (no more os.Stderr leak)
  - Worker death↔recovery causal-chain pair in slog (death log + recovery log share `label` field)
  - Symmetric WARN at Ollama streaming eng.Run failure (closes asymmetry with REL-HTTP-03)
  - Defense-in-depth panic recovery at 4 background goroutine sites (no goroutine can crash the gateway silently)
  - Config.AdminTailPath as single source of truth for log-tail path (eliminates writer-vs-tailer split-brain)
  - acp.Client.Pid() int accessor on pool.PoolClient interface
  - Test-only seam pattern (firePanicProbe + SetXPanicProbeForTest) for race-safe panic injection
affects: [18-03, future-observability-phases, audit-log-correlation, operator-dashboards]

# Tech tracking
tech-stack:
  added: []  # No new deps; all stdlib (bufio, runtime/debug, os/exec)
  patterns:
    - "Test-only panic-injection seam through sync.Mutex-guarded probe variable + SetXPanicProbeForTest helper that returns a restore closure (race-detector clean)"
    - "syncBuf wrapper around bytes.Buffer for slog-output goroutine-safety in panic-injection tests"
    - "Capture critical-section-internal state under lock (label, newPid), emit slog AFTER unlock — keeps lock critical-section narrow"

key-files:
  created:
    - internal/acp/regression_rel_obsv_03_test.go
    - internal/adapter/ollama/regression_rel_http_06_test.go
    - internal/pool/regression_rel_obsv_02_test.go
    - internal/pool/regression_rel_http_07_test.go
    - internal/admin/regression_rel_http_07_test.go
    - internal/admin/regression_rel_obsv_04_test.go
    - internal/engine/regression_rel_http_07_test.go
    - internal/config/regression_rel_obsv_04_test.go
    - .planning/phases/18-reliability-long-tail/18-02-SUMMARY.md
  modified:
    - internal/acp/client.go (bufio.Reader stderr drain goroutine + Pid() accessor)
    - internal/pool/config.go (PoolClient interface gains Pid() int)
    - internal/pool/pool_test.go (fakeClient.pid + Pid() method)
    - internal/pool/exit_watcher_test.go (watcherTestClient.Pid())
    - internal/pool/pool.go (respawnSlot INFO log + ctx-watcher defer-recover + probe-seam infrastructure)
    - internal/pool/exit_watcher.go (defer-recover with structured log)
    - internal/engine/engine.go (AfterFunc callback defer-recover + probe-seam infrastructure)
    - internal/adapter/ollama/handlers.go (REL-HTTP-06 WARN at chat + generate sites)
    - internal/admin/tail.go (run() defer-recover + DEBUG→WARN open-fail promotion + probe-seam)
    - internal/config/config.go (AdminTailPath additive field)
    - cmd/otto-gateway/main.go (tailer wiring reads cfg.AdminTailPath)

key-decisions:
  - "D-18-04: kiro-cli stderr scanner uses bufio.Reader.ReadString('\\n'), NOT bufio.Scanner — Scanner stops on ErrTooLong silently dropping every line after an oversized one"
  - "D-18-05: success-path reason field byte-exact 'lazy-respawn-success'; slot label field key is 'label' (mirrors exit_watcher.go death log)"
  - "D-18-06: worker_pid:0 and bytes_streamed:0 are accepted placeholders — RunHandle interface does NOT expose a Pid accessor in v1.10.3 (deferred to v1.10.4)"
  - "D-18-07: 4 byte-exact site names — admin-tailer, pool-ctx-watcher, pool-exit-watcher, engine-after-func; defer-recover mirrors engine.callPreHookSafe template"
  - "D-18-07: probe seam variables protected by sync.Mutex so the race detector observes cross-test happens-before (a goroutine from a prior test may still be reading the probe when the next test installs one)"
  - "D-18-08: AdminTailPath populated in Load via the SAME deriveChatTraceFile call that produces ChatTraceFile — writer and tailer can't diverge by construction"
  - "Auth-state INFO log line at main.go:115-120 preserved byte-identical (verified via git diff)"

patterns-established:
  - "Pattern A: structured panic-recovery template — defer func() { if r := recover(); r != nil && logger != nil { logger.Error('goroutine panic recovered', 'site', '<name>', 'panic', fmt.Sprintf('%v', r), 'stack', string(debug.Stack())) } }()"
  - "Pattern B: bufio.Reader.ReadString('\\n') with byte-cap-after-read for unbounded-line tolerance (mirrors internal/admin/tail.go) — prefer this over bufio.Scanner whenever line size cannot be bounded"
  - "Pattern C: Test-only seam pattern — package-private probe var + sync.Mutex + Fire/Set helpers; Set returns a restore closure for t.Cleanup; eliminates race detector flake from cross-test probe writes"
  - "Pattern D: syncBuf wrapper for slog destinations in panic-injection tests (bytes.Buffer is not goroutine-safe; race detector flags slog write vs test read)"
  - "Pattern E: Capture lock-protected fields into locals, release lock, then emit slog — keeps critical section narrow without holding mutex across allocator/I-O calls"

requirements-completed: [REL-HTTP-06, REL-HTTP-07, REL-OBSV-02, REL-OBSV-03, REL-OBSV-04]

# Metrics
duration: ~2h
completed: 2026-06-11
---

# Phase 18 Plan 02: Observability symmetry + HTTP error logging Summary

**Five observability-symmetry items closed: kiro-cli stderr now structured-logged at WARN, worker death+recovery emit a paired causal chain, Ollama streaming eng.Run failures emit a mirrored REL-HTTP-03 WARN, four background goroutine sites have defense-in-depth panic recovery, and the chat-trace path is a single Config field.**

## Performance

- **Duration:** ~2h (RED→GREEN per REQ-ID + lint cleanup)
- **Started:** 2026-06-11T20:51Z
- **Completed:** 2026-06-11T21:25Z
- **Tasks:** 3 (5 REQ-IDs, grouped into 3 implementation tasks per plan)
- **Files created:** 9 (8 regression test files + this SUMMARY)
- **Files modified:** 11 (production code + test fakes + main.go wiring)

## Accomplishments

- **REL-OBSV-03 (D-18-04):** kiro-cli stderr is now drained line-by-line via a dedicated goroutine using `bufio.Reader.ReadString('\n')` (NOT `bufio.Scanner` — Scanner silently stops on `ErrTooLong`) and emitted as `slog.Warn("kiro-cli stderr", "worker_pid", pid, "line", text)` with a 1 MB per-line byte cap. `goleak.VerifyNone` enforces clean exit.
- **REL-HTTP-06 (D-18-06):** Both Ollama streaming `/api/chat` and `/api/generate` handlers emit `slog.Warn("ollama: streaming eng.Run failed", ...)` BEFORE `writeError` when `eng.Run` returns a non-pool-exhausted error. Field set mirrors REL-HTTP-03 from Phase 15 (session_id, worker_pid:0 placeholder, bytes_streamed:0, request_id, err, conditional kiro_exit_code).
- **REL-OBSV-02 (D-18-05):** `Pool.respawnSlot` emits exactly one `slog.Info("pool: slot recovered", "label", slot.Label, "worker_pid", newPid, "previous_pid", oldPid, "reason", "lazy-respawn-success")` on the success path. Closes the asymmetry with the death log at `exit_watcher.go:42`. Required adding `Pid() int` to `pool.PoolClient` interface + `acp.Client`.
- **REL-HTTP-07 (D-18-07):** Defense-in-depth panic recovery at 4 background goroutine sites with byte-exact site names: `admin-tailer`, `pool-ctx-watcher`, `pool-exit-watcher`, `engine-after-func`. Each emits `slog.Error("goroutine panic recovered", "site", <name>, "panic", <value>, "stack", <debug.Stack>)` and exits cleanly. No auto-restart.
- **REL-OBSV-04 (D-18-08):** New `Config.AdminTailPath string` field populated in `config.Load()` via the SAME `deriveChatTraceFile` call that produces `ChatTraceFile`. `main.go` admin log-tail wiring reads from `cfg.AdminTailPath`. The chat-trace tailer's open-failure log promoted from `DEBUG` to `WARN` with path field.

## Task Commits

11 commits total (RED + GREEN per REQ-ID, plus one cleanup):

1. **REL-OBSV-03 RED:** `b9dbd81` — `test(18-02): RED — REL-OBSV-03 kiro-cli stderr -> structured slog`
2. **REL-OBSV-03 GREEN:** `880d499` — `feat(18-02): GREEN — REL-OBSV-03 kiro-cli stderr -> structured slog (D-18-04)`
3. **REL-HTTP-06 RED:** `d91c147` — `test(18-02): RED — REL-HTTP-06 Ollama streaming eng.Run WARN`
4. **REL-HTTP-06 GREEN:** `608a815` — `feat(18-02): GREEN — REL-HTTP-06 Ollama streaming eng.Run WARN (D-18-06)`
5. **REL-OBSV-02 RED:** `fa66066` — `test(18-02): RED — REL-OBSV-02 worker recovery INFO + acp.Client.Pid scaffold`
6. **REL-OBSV-02 GREEN:** `9314e93` — `feat(18-02): GREEN — REL-OBSV-02 worker recovery INFO (D-18-05)`
7. **REL-HTTP-07 RED:** `f27c012` — `test(18-02): RED — REL-HTTP-07 panic recovery at 4 sites + test seams`
8. **REL-HTTP-07 GREEN:** `749c127` — `feat(18-02): GREEN — REL-HTTP-07 panic recovery at 4 sites (D-18-07)`
9. **REL-OBSV-04 RED:** `94e1b97` — `test(18-02): RED — REL-OBSV-04 single-source path + WARN on missing`
10. **REL-OBSV-04 GREEN:** `e72f4aa` — `feat(18-02): GREEN — REL-OBSV-04 Config.AdminTailPath single source + WARN on missing (D-18-08)`
11. **Lint cleanup:** `d0c4251` — `refactor(18-02): gofumpt + noctx lint fixes for new 18-02 code`

## Regression Tests Per REQ-ID

| REQ-ID        | Test path                                                     | Test function name(s)                                                          |
|---------------|---------------------------------------------------------------|--------------------------------------------------------------------------------|
| REL-OBSV-03   | `internal/acp/regression_rel_obsv_03_test.go`                 | `TestRegression_REL_OBSV_03` (A1/A2/A4 subtests)                                |
| REL-HTTP-06   | `internal/adapter/ollama/regression_rel_http_06_test.go`      | `TestRegression_REL_HTTP_06` (B1/B2/B3 subtests)                                |
| REL-OBSV-02   | `internal/pool/regression_rel_obsv_02_test.go`                | `TestRegression_REL_OBSV_02`                                                    |
| REL-HTTP-07   | `internal/admin/regression_rel_http_07_test.go`               | `TestRegression_REL_HTTP_07_AdminTailer`                                        |
| REL-HTTP-07   | `internal/pool/regression_rel_http_07_test.go`                | `TestRegression_REL_HTTP_07_PoolCtxWatcher`, `TestRegression_REL_HTTP_07_PoolExitWatcher` |
| REL-HTTP-07   | `internal/engine/regression_rel_http_07_test.go`              | `TestRegression_REL_HTTP_07_EngineAfterFunc`                                    |
| REL-OBSV-04   | `internal/admin/regression_rel_obsv_04_test.go`               | `TestRegression_REL_OBSV_04_TailerOpenFailureWarn`                              |
| REL-OBSV-04   | `internal/config/regression_rel_obsv_04_test.go`              | `TestRegression_REL_OBSV_04` (C1/C2/C3 subtests)                                |

## REL-HTTP-07 — 4 Panic Sites Documented

| Site name (byte-exact) | File                              | Goroutine identity                                              |
|------------------------|-----------------------------------|-----------------------------------------------------------------|
| `admin-tailer`         | `internal/admin/tail.go`          | Single shared log-tailer goroutine launched by `Tailer.Subscribe` |
| `pool-ctx-watcher`     | `internal/pool/pool.go`           | Per-session ctx-watcher launched inside `Pool.Prompt` (~line 887) |
| `pool-exit-watcher`    | `internal/pool/exit_watcher.go`   | Per-slot exit-watcher launched by `startExitWatcher`            |
| `engine-after-func`    | `internal/engine/engine.go`       | The CALLBACK BODY of `context.AfterFunc` in `Engine.Run` (~line 255) — runs on a runtime-managed goroutine |

Each site captures the logger reference BEFORE the goroutine starts (for the goroutine sites) so the closure binds to a stable reference. The defer-recover template mirrors `engine.callPreHookSafe` at `engine.go:317-329`.

## Decisions Made

All key decisions documented in `key-decisions` frontmatter above. Notable:

- **Probe-seam race-safety:** The original plan called for bare `var xPanicProbe func()` package globals. In practice the race detector flagged cross-test reads/writes (a goroutine from a prior test may still be scheduled when the next test installs a new probe). Resolved by adding a `sync.Mutex` and `firePanicProbe` / `SetXPanicProbeForTest` helpers — production code paths are zero-cost (no contention; probe is nil in production), tests are race-clean.
- **syncBuf for slog test destinations:** `bytes.Buffer` is not goroutine-safe. The panic-injection tests have slog writing from a recovered goroutine while the test goroutine reads via `.String()`. Wrapped with a sync.Mutex in `syncBuf` (per test package — duplicated in admin, pool, engine because they're separate test packages).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Race detector flake on probe seam variables**
- **Found during:** Task 2 Part B GREEN run (REL-HTTP-07 panic recovery)
- **Issue:** Initial implementation used bare `var xPanicProbe func()` package globals. The race detector flagged the second pool subtest because the first subtest's exit-watcher goroutine (from Warmup) had read the probe and was still tracked by the runtime when the next test wrote a new probe value.
- **Fix:** Added `sync.Mutex` (`panicProbeMu`, `adminTailerPanicProbeMu`, `afterFuncPanicProbeMu`) + helpers `firePanicProbe` / `SetXPanicProbeForTest` (returns restore closure). All probe reads in the goroutine bodies go through `firePanicProbe`; all probe installations in tests go through `SetXPanicProbeForTest`.
- **Files modified:** `internal/admin/tail.go`, `internal/pool/pool.go`, `internal/pool/exit_watcher.go`, `internal/engine/engine.go`, and the 4 regression test files.
- **Verification:** `go test -race -count=1 ./internal/admin/ ./internal/pool/ ./internal/engine/` clean tree-wide.
- **Committed in:** `749c127` (REL-HTTP-07 GREEN — same commit as the structured logging because both are required for the test to assert PASS).

**2. [Rule 2 - Missing Critical] syncBuf wrapper for goroutine-safe slog destinations**
- **Found during:** Task 2 Part B GREEN run
- **Issue:** Test goroutines (panic recovers) and test main goroutine (assertions) shared a `bytes.Buffer` via slog handler write + `buf.String()` read. Race detector flagged this even on the otherwise-passing admin tailer test.
- **Fix:** Added per-test-package `syncBuf` type — `sync.Mutex` + `Write` + `String` methods. Used in all 4 REL-HTTP-07 tests + the REL-OBSV-04 admin test.
- **Files modified:** Added inline in the 4 regression test files (admin, pool, engine — each test package is separate).
- **Verification:** Same as above.
- **Committed in:** `749c127`.

**3. [Rule 3 - Blocking] noctx golangci-lint failures in new test code**
- **Found during:** Final `make ci` run
- **Issue:** golangci-lint's `noctx` linter flagged `exec.Command` and `httptest.NewRequest` calls in the new REL-HTTP-06 test (lines 68 and 176).
- **Fix:** Replaced with `exec.CommandContext(t.Context(), ...)` and `httptest.NewRequestWithContext(t.Context(), ...)`. Used Go 1.24+ `t.Context()` which is preferred over `context.Background()` in test code.
- **Files modified:** `internal/adapter/ollama/regression_rel_http_06_test.go`.
- **Verification:** `golangci-lint run ./internal/adapter/ollama/` → 0 issues.
- **Committed in:** `d0c4251`.

**4. [gofumpt formatting] Auto-applied by gofumpt -w**
- **Found during:** Final `make ci` run
- **Issue:** `make ci` requires gofumpt-clean tree; gofumpt reformatted several multi-arg `slog.Error` and `slog.Info` calls (preference for split-arg-on-own-line layout).
- **Fix:** Ran `gofumpt -w .`; no behavioral changes.
- **Files modified:** `internal/acp/client.go`, `internal/admin/tail.go`, `internal/engine/engine.go`, `internal/pool/exit_watcher.go`, `internal/pool/pool.go`.
- **Verification:** `gofumpt -l .` → empty.
- **Committed in:** `d0c4251`.

---

**Total deviations:** 4 auto-fixed (1 Rule 2, 3 Rule 3)
**Impact on plan:** All deviations were test-quality / lint hygiene; none changed production behavior or scope. The probe-seam mutex pattern is a useful generalization worth documenting (Pattern C above).

## Issues Encountered

- **Race detector + cross-test probe-variable mutation** — described above as deviation #1.
- **noctx pre-existing issues from Plan 18-01** — `internal/config/config.go:666` (`net.Listen` in REL-CFG-07 bind probe) and `internal/config/regression_rel_cfg_07_test.go:53` (`net.Listen` in port-probe test) trigger `noctx` lint errors. **Out of scope for this plan** per the deviation-rule scope boundary (Rule 1-3: "Only auto-fix issues DIRECTLY caused by the current task's changes"). Both predate this plan. **Recommendation:** open a quick fix in v1.10.4 — replace with `(&net.ListenConfig{}).Listen(ctx, ...)`. Tracked in deferred-items below.

## Acceptance Gates — All Pass

| Gate                                                                | Status                                            |
|---------------------------------------------------------------------|---------------------------------------------------|
| `go test ./...` exit 0                                              | ✅                                                |
| `go test -race ./...` exit 0 tree-wide (including goleak)           | ✅                                                |
| `go vet ./...` clean                                                | ✅                                                |
| `gofmt -l .` empty                                                  | ✅                                                |
| `gofumpt -l .` empty                                                | ✅                                                |
| `grep -rn 'cmd.Stderr = os.Stderr' internal/ cmd/ --include='*.go'` | ✅ zero production-code hits                       |
| `grep -n 'bufio.NewScanner' internal/acp/client.go`                 | ✅ zero hits                                       |
| `grep -n 'bufio.NewReader' internal/acp/client.go`                  | ✅ 1 hit (the new scanner)                         |
| `grep -c 'goroutine panic recovered'` across 4 site files            | ✅ 4 hits, 1 per site file                         |
| `grep -c 'pool: slot recovered' internal/pool/pool.go`              | ✅ 1                                               |
| `grep -c 'ollama: streaming eng.Run failed' internal/adapter/ollama/handlers.go` | ✅ 2 (one per /chat and /generate site) |
| `grep -c 'kiro-cli stderr' internal/acp/client.go`                  | ✅ 1                                               |
| `grep -n 'AdminTailPath' internal/config/config.go cmd/otto-gateway/main.go` | ✅ 7 hits (struct decl, Load assignment, main.go consumer + doc comments) |
| 4 byte-exact site names: `admin-tailer`, `pool-ctx-watcher`, `pool-exit-watcher`, `engine-after-func` | ✅ confirmed via grep |
| D-18-04 regression test asserts `goleak.VerifyNone`                 | ✅ A1/A2/A4 subtests each have `defer goleak.VerifyNone(t)` |
| D-18-05 regression test asserts `reason: "lazy-respawn-success"` byte-exact | ✅ assertion at line ~104                |
| D-18-06 regression test asserts mirrored field set + `worker_pid:0` placeholder | ✅ B1/B2/B3 subtests cover           |
| Boot-time auth-state INFO at main.go:115-120 byte-identical          | ✅ `git diff cmd/otto-gateway/main.go` shows only tailer-wiring change |

**`make ci` partial status:** All `make ci` steps pass EXCEPT the `lint` step, which fails on the two pre-existing noctx hits from Plan 18-01 (see deferred-items). Phase-close criterion 1 ("make ci exit 0") cannot be re-asserted from this plan alone; resolution will land in v1.10.4 or Plan 18-03.

## Deferred Items

- **`net.Listen` → `(*net.ListenConfig).Listen(ctx, ...)`** at `internal/config/config.go:666` and `internal/config/regression_rel_cfg_07_test.go:53` — pre-existing noctx hits from Plan 18-01 (commit `cb8cc99`). Out of this plan's scope. Deferred to v1.10.4 or Plan 18-03 (whichever lands first). Two-line fix per site.
- **`slot_id` field on D-18-04 stderr WARN log** — CONTEXT.md permits omission for v1.10.3; would require threading slot identity into `acp.Client`. Deferred per the plan's `must_haves.truths` note.
- **RunHandle Pid accessor for REL-HTTP-06** — the `worker_pid:0` placeholder is a documented accepted compromise per CONTEXT.md §D-18-06. Wiring a real Pid through RunHandle would extend the consumer-defined interface in `internal/adapter/ollama/adapter.go`; out of v1.10.3 scope.

## Pre-coordination Note (Plan 18-01 ↔ 18-02)

Per plan `<pre_coordination>`: Plan 18-01 modified `internal/config/config.go` for D-18-01/02/03 BEFORE this plan ran. This plan added `Config.AdminTailPath` at the END of the struct's string fields (immediately below the `ChatTraceFile`/`ChatTraceMaxAgeDays` block, after `ChatTraceFile`), and added `AdminTailPath: chatTraceFile` to the returned `Config{}` literal alongside the other 18-01 fields. No textual conflict; resolution was mechanical (both 18-01 and 18-02 fields are additive, both share the `deriveChatTraceFile` derivation chain).

## Next Phase Readiness

- ✅ 5 REQ-IDs closed with regression tests
- ✅ All 4 panic sites have defense-in-depth recovery
- ✅ `go test -race ./...` clean tree-wide
- ⚠️ Pre-existing 2 noctx lint hits from 18-01 block `make ci` — surface to Plan 18-03 for parallel closeout, OR address as a v1.10.4 quick fix
- ✅ Plan 18-03 (REL-HTTP-04 acp.Stream race) can proceed independently — no shared files with 18-02

---

## Self-Check: PASSED

Files verified:
- `internal/acp/regression_rel_obsv_03_test.go` exists, contains `TestRegression_REL_OBSV_03`
- `internal/adapter/ollama/regression_rel_http_06_test.go` exists, contains `TestRegression_REL_HTTP_06`
- `internal/pool/regression_rel_obsv_02_test.go` exists, contains `TestRegression_REL_OBSV_02`
- `internal/pool/regression_rel_http_07_test.go` exists, contains `TestRegression_REL_HTTP_07_PoolCtxWatcher`, `TestRegression_REL_HTTP_07_PoolExitWatcher`
- `internal/admin/regression_rel_http_07_test.go` exists, contains `TestRegression_REL_HTTP_07_AdminTailer`
- `internal/engine/regression_rel_http_07_test.go` exists, contains `TestRegression_REL_HTTP_07_EngineAfterFunc`
- `internal/admin/regression_rel_obsv_04_test.go` exists, contains `TestRegression_REL_OBSV_04_TailerOpenFailureWarn`
- `internal/config/regression_rel_obsv_04_test.go` exists, contains `TestRegression_REL_OBSV_04`

Commits verified (`git log --oneline e19d5fc..HEAD`):
- `b9dbd81` (REL-OBSV-03 RED), `880d499` (REL-OBSV-03 GREEN)
- `d91c147` (REL-HTTP-06 RED), `608a815` (REL-HTTP-06 GREEN)
- `fa66066` (REL-OBSV-02 RED), `9314e93` (REL-OBSV-02 GREEN)
- `f27c012` (REL-HTTP-07 RED), `749c127` (REL-HTTP-07 GREEN)
- `94e1b97` (REL-OBSV-04 RED), `e72f4aa` (REL-OBSV-04 GREEN)
- `d0c4251` (lint cleanup)

## TDD Gate Compliance

Plan type was `tdd` — gate sequence verified per REQ-ID:
- REL-OBSV-03: `b9dbd81` test → `880d499` feat ✅
- REL-HTTP-06: `d91c147` test → `608a815` feat ✅
- REL-OBSV-02: `fa66066` test → `9314e93` feat ✅
- REL-HTTP-07: `f27c012` test → `749c127` feat ✅
- REL-OBSV-04: `94e1b97` test → `e72f4aa` feat ✅

All 5 REQ-IDs follow the RED → GREEN gate sequence in git log. No REFACTOR commits were needed (the GREEN commits landed clean test passes; the deviation-driven refactors in GREEN itself satisfied the cleanup needs).

---
*Phase: 18-reliability-long-tail*
*Plan: 02*
*Completed: 2026-06-11*
