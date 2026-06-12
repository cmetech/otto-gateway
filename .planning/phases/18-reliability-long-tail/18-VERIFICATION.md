---
phase: 18-reliability-long-tail
verified: 2026-06-12T01:38:03Z
status: passed
score: 10/10 must-haves verified
overrides_applied: 0
---

# Phase 18: Reliability long-tail — Verification Report

**Phase Goal:** Close 10 of the 11 deferred Low-severity reliability findings from the 2026-06-11 audit (REL-ACP-01 deferred to Phase 19). Three loosely-coupled fix areas across 3 parallel plans.

**Verified:** 2026-06-12T01:38:03Z
**Status:** passed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths (10 REQ-IDs)

| # | REQ-ID | Truth | Status | Evidence |
|---|--------|-------|--------|----------|
| 1 | REL-CFG-05 | Degenerate `AUTH_TOKEN` / `ALLOWED_IPS` emit `slog.Warn` and are treated as unset | VERIFIED | `internal/config/config.go:377,394` (`slog.Default().Warn` calls); regression test `TestRegression_REL_CFG_05` |
| 2 | REL-CFG-06 | `KIRO_CMD` not in PATH → `config: KIRO_CMD (%q): not found in PATH or unreadable`; `KIRO_CWD` missing/not-dir → named errors; `~/` expansion via `os.UserHomeDir + filepath.Join` | VERIFIED | `internal/config/config.go:318,340,342`; regression test `TestRegression_REL_CFG_06` |
| 3 | REL-CFG-07 | Bind-then-close HTTP_ADDR probe in config.Load surfaces `config: HTTP_ADDR (%q): bind probe failed: %w` pre-warmup | VERIFIED | `internal/config/config.go:668`; regression test `TestRegression_REL_CFG_07` |
| 4 | REL-HTTP-06 | Ollama streaming `eng.Run` failures emit `slog.Warn("ollama: streaming eng.Run failed", ...)` BEFORE error frame write, mirroring REL-HTTP-03 fields | VERIFIED | 2 hits in `internal/adapter/ollama/handlers.go`; regression test `TestRegression_REL_HTTP_06` |
| 5 | REL-HTTP-07 | Panic recovery at 4 byte-exact sites (`admin-tailer`, `pool-ctx-watcher`, `pool-exit-watcher`, `engine-after-func`), logs `"goroutine panic recovered"` at Error | VERIFIED | All 4 sites confirmed via grep: `internal/admin/tail.go:334`, `internal/pool/pool.go:962`, `internal/pool/exit_watcher.go:50`, `internal/engine/engine.go:268`; regression tests `TestRegression_REL_HTTP_07_{AdminTailer,PoolCtxWatcher,PoolExitWatcher,EngineAfterFunc}` |
| 6 | REL-OBSV-02 | `Pool.respawnSlot` success path emits `slog.Info("pool: slot recovered", "label", ..., "worker_pid", ..., "previous_pid", ..., "reason", "lazy-respawn-success")` | VERIFIED | `internal/pool/pool.go:420` ("lazy-respawn-success" byte-exact); regression test `TestRegression_REL_OBSV_02` |
| 7 | REL-OBSV-03 | kiro-cli stderr drained via `bufio.NewReader.ReadString('\n')` (NOT `bufio.Scanner`) in dedicated goroutine; 1MB byte-cap; emits `slog.Warn("kiro-cli stderr", ...)` | VERIFIED | `internal/acp/client.go:396` (NewReader), `:398` (ReadString); `:395` (1MB cap `1024 * 1024`); no `bufio.NewScanner` hits; regression test `TestRegression_REL_OBSV_03` |
| 8 | REL-OBSV-04 | NEW `Config.AdminTailPath` field populated in `config.Load` via the SAME `deriveChatTraceFile` call; `main.go` tailer reads `cfg.AdminTailPath`; tailer reopen() open-fail promoted DEBUG→WARN | VERIFIED | `internal/config/config.go:283` (field decl), `:742` (`AdminTailPath: chatTraceFile`); `cmd/otto-gateway/main.go:662`; regression tests `TestRegression_REL_OBSV_04` (config + admin) |
| 9 | REL-TRAY-08 | `stateInput.ConfigError` field + top-of-`computeState` short-circuit to `StateError` + `"config error: " + in.ConfigError`; poller reads `$HOME/.otto-gw/.config-error`; wrapper writes/deletes sentinel | VERIFIED | `cmd/otto-tray/fsm.go:33,52-53`; `cmd/otto-tray/poller.go:35,96`; `scripts/otto-gw:206`, `scripts/otto-gw.ps1:233`; regression test `TestRegression_REL_TRAY_08_ConfigErrorShortCircuit` (5 sub-cases A1–A5) |
| 10 | REL-TRAY-09 | Two broken macOS rows REMOVED from `scripts/otto-gw` support bundle (`tray-state.txt`, autostart probe checking wrong plist) | VERIFIED | `grep -v '^\s*#' scripts/otto-gw \| grep -c 'tray-state.txt' == 0`; same for `com.otto.tray.plist`; regression test `tests/scripts/test-support-bundle-rel-tray-09.sh` (3/3 pass); Go skip-stub `TestRegression_REL_TRAY_09_BundleRowRemoval` links to bash test |

**Score:** 10/10 truths verified

### Required Artifacts — Per-File Status

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `internal/config/regression_rel_cfg_05_test.go` | TestRegression_REL_CFG_05 (6 sub-cases) | VERIFIED | Function present at line 70 |
| `internal/config/regression_rel_cfg_06_test.go` | TestRegression_REL_CFG_06 (7 sub-cases) | VERIFIED | Function present at line 53 |
| `internal/config/regression_rel_cfg_07_test.go` | TestRegression_REL_CFG_07 (2 sub-cases) | VERIFIED | Function present at line 46 |
| `internal/config/config.go` | 3 validation branches (D-18-01/02/03), AdminTailPath field/assign | VERIFIED | All present; named errors at L318/L340/L342; degenerate WARNs at L377/L394; bind probe at L668; AdminTailPath at L283/L742 |
| `internal/acp/regression_rel_obsv_03_test.go` | TestRegression_REL_OBSV_03 (A1/A2/A4) | VERIFIED | Function at line 120 |
| `internal/acp/client.go` | StderrPipe + ReadString loop + 1MB cap + `Pid()` accessor | VERIFIED | NewReader/ReadString in stderr drain goroutine; `Pid()` method present |
| `internal/pool/regression_rel_obsv_02_test.go` | TestRegression_REL_OBSV_02 | VERIFIED | Function at line 38 |
| `internal/pool/pool.go` | respawnSlot INFO log + ctx-watcher defer-recover; PoolClient interface gains Pid() int | VERIFIED | "lazy-respawn-success" at L420; "pool-ctx-watcher" at L962 |
| `internal/pool/exit_watcher.go` | exit-watcher defer-recover with site="pool-exit-watcher" | VERIFIED | Site name at L50 |
| `internal/pool/regression_rel_http_07_test.go` | TestRegression_REL_HTTP_07_{PoolCtxWatcher,PoolExitWatcher} | VERIFIED | Functions at L111/L150 |
| `internal/engine/engine.go` | AfterFunc callback defer-recover, site="engine-after-func" | VERIFIED | Site name at L268 |
| `internal/engine/regression_rel_http_07_test.go` | TestRegression_REL_HTTP_07_EngineAfterFunc | VERIFIED | Function at line 53 |
| `internal/admin/tail.go` | run() defer-recover (site="admin-tailer") + DEBUG→WARN reopen() promotion | VERIFIED | Site name at L334 |
| `internal/admin/regression_rel_http_07_test.go` | TestRegression_REL_HTTP_07_AdminTailer | VERIFIED | Function at line 71 |
| `internal/admin/regression_rel_obsv_04_test.go` | TestRegression_REL_OBSV_04_TailerOpenFailureWarn | VERIFIED | Function at line 29 |
| `internal/adapter/ollama/handlers.go` | "ollama: streaming eng.Run failed" WARN at both /chat + /generate | VERIFIED | 2 hits via grep |
| `internal/adapter/ollama/regression_rel_http_06_test.go` | TestRegression_REL_HTTP_06 (B1/B2/B3) | VERIFIED | Function at line 88 |
| `cmd/otto-tray/fsm.go` | stateInput.ConfigError field + top-of-computeState short-circuit | VERIFIED | Field at L33; short-circuit at L52-53 |
| `cmd/otto-tray/poller.go` | readConfigErrorSentinel() helper + tick-loop wire-in | VERIFIED | Helper at L30; wire-in at L96 |
| `cmd/otto-tray/regression_rel_tray_08_test.go` | TestRegression_REL_TRAY_08_ConfigErrorShortCircuit (5 sub-cases) | VERIFIED | Function at line 34 |
| `cmd/otto-tray/regression_rel_tray_09_test.go` | TestRegression_REL_TRAY_09_BundleRowRemoval (skip-stub) | VERIFIED | Function at line 35 |
| `scripts/otto-gw` | sentinel write/delete in load_env_file + macOS row removal | VERIFIED | Sentinel helpers at L206; bundle rows removed (negative grep = 0) |
| `scripts/otto-gw.ps1` | sentinel mirror in Import-DotEnv | VERIFIED | Get-ConfigErrorSentinel at L233 |
| `tests/scripts/test-support-bundle-rel-tray-09.sh` | Bundle row absence test (replaces planned bats) | VERIFIED | Test exists; passes 3/3 on darwin (Rule 3 deviation documented in 18-03-SUMMARY.md — bats not installed) |

### Key Link Verification (Wiring)

| From | To | Via | Status |
|------|-----|-----|--------|
| `internal/config/config.go` degenerate-env branch | `slog.Default().Warn` | `slog.Default().Warn(...)` | WIRED — L377, L394 |
| `internal/config/config.go` named errors | `errs` accumulator → `errors.Join` | `errs = append(...)` | WIRED — L318, L340, L342 |
| `internal/config/config.go` port probe | `net.Listen("tcp", httpAddr)` + Close | conditional append on bind error | WIRED — L668 |
| `internal/acp/client.go` stderr drain | `cfg.Logger.Warn("kiro-cli stderr", ...)` | bufio.Reader.ReadString | WIRED — L396, L398 |
| `internal/pool/pool.go` respawnSlot success | `p.cfg.Logger.Info("pool: slot recovered", ...)` | slog.Info after p.mu.Unlock | WIRED — L420 |
| `internal/adapter/ollama/handlers.go` | `a.cfg.Logger.Warn("ollama: streaming eng.Run failed", ...)` | slog.Warn BEFORE writeError | WIRED — 2 sites (chat + generate) |
| 4 panic sites | `logger.Error("goroutine panic recovered", "site", ..., ...)` | defer recover | WIRED — all 4 sites byte-exact |
| `Config.AdminTailPath` | `cmd/otto-gateway/main.go` tailer | `logPaths["chat-trace"] = cfg.AdminTailPath` | WIRED — L662 |
| `scripts/otto-gw load_env_file` | `$HOME/.otto-gw/.config-error` | echo/rm via helper functions | WIRED — L206/L242 |
| `cmd/otto-tray/poller.go` sentinel read | `stateInput.ConfigError` | `os.ReadFile($HOME/.otto-gw/.config-error)` | WIRED — L35, L96 |
| `cmd/otto-tray/fsm.go` computeState | `StateError` + `"config error: " + Detail` | top-of-function short-circuit | WIRED — L52-53 |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Project builds tree-wide | `go build ./...` | exit 0, no output | PASS |
| Vet passes tree-wide | `go vet ./...` | exit 0, no output | PASS |
| Format is clean | `gofmt -l .` | empty output | PASS |
| Tests pass with race detector tree-wide | `go test -race ./...` | All packages `ok` (cached) | PASS |
| 10 named regression tests execute | `go test -race -count=1 -run "TestRegression_REL_(CFG_05\|CFG_06\|CFG_07\|HTTP_06\|HTTP_07\|OBSV_02\|OBSV_03\|OBSV_04\|TRAY_08\|TRAY_09)" ./...` | All package suites ok, race-clean | PASS |
| REL-TRAY-09 bundle removal test | `bash tests/scripts/test-support-bundle-rel-tray-09.sh` | passed: 3, failed: 0 | PASS |
| Phase-close gate 4: no production `cmd.Stderr = os.Stderr` | `grep -rn "cmd.Stderr = os.Stderr" cmd/ internal/` | 1 hit (test-file comment in `internal/acp/regression_rel_obsv_03_test.go:3` — comment-only, no executable assignment) | PASS |
| Phase-close gate 5: main.go:115-120 INFO line unchanged | `git show e72f4aa -- cmd/otto-gateway/main.go` | Only diff is `logPaths["chat-trace"] = cfg.AdminTailPath` at L662; auth-mode INFO line byte-identical | PASS |
| Phase-close gate 6: no StateConfigError | `grep -rn 'StateConfigError' cmd/otto-tray/` | 0 hits | PASS |

### Requirements Coverage

| REQ-ID | Source Plan | Description | Status | Evidence |
|--------|-------------|-------------|--------|----------|
| REL-CFG-05 | 18-01 | Degenerate auth env values → Warn + treat-as-unset | SATISFIED | `internal/config/config.go:377,394`; `TestRegression_REL_CFG_05` |
| REL-CFG-06 | 18-01 | Named KIRO_CMD/KIRO_CWD errors + `~` expansion | SATISFIED | `internal/config/config.go:318,340,342`; `TestRegression_REL_CFG_06` |
| REL-CFG-07 | 18-01 | Bind-then-close HTTP_ADDR probe pre-warmup | SATISFIED | `internal/config/config.go:668`; `TestRegression_REL_CFG_07` |
| REL-HTTP-06 | 18-02 | Ollama streaming eng.Run WARN mirror | SATISFIED | `internal/adapter/ollama/handlers.go` (2 sites); `TestRegression_REL_HTTP_06` |
| REL-HTTP-07 | 18-02 | Panic recovery at 4 goroutine sites | SATISFIED | All 4 sites with byte-exact names; 4 regression tests |
| REL-OBSV-02 | 18-02 | Worker recovery INFO with "lazy-respawn-success" | SATISFIED | `internal/pool/pool.go:420`; `TestRegression_REL_OBSV_02` |
| REL-OBSV-03 | 18-02 | kiro-cli stderr → structured slog (no os.Stderr leak) | SATISFIED | `internal/acp/client.go` (ReadString + 1MB cap); `TestRegression_REL_OBSV_03` |
| REL-OBSV-04 | 18-02 | Config.AdminTailPath single source; WARN on missing | SATISFIED | `internal/config/config.go:283,742`; `cmd/otto-gateway/main.go:662`; 2 regression tests |
| REL-TRAY-08 | 18-03 | Sentinel-driven StateError surfacing dotenv errors | SATISFIED | `cmd/otto-tray/{fsm,poller}.go` + wrappers; `TestRegression_REL_TRAY_08_ConfigErrorShortCircuit` |
| REL-TRAY-09 | 18-03 | Remove broken macOS bundle rows | SATISFIED | `scripts/otto-gw` (rows removed); `tests/scripts/test-support-bundle-rel-tray-09.sh` (3/3 pass) |

All 10 REQ-IDs declared in plan frontmatter and addressed in production code. No orphaned requirements detected.

### Open Question Lockdown Verification

| Open Question | Resolution | Codebase Evidence |
|--------------|------------|-------------------|
| OQ-1 (D-18-07 panic recovery sites) | 4 byte-exact site names | All 4 confirmed: `admin-tailer` (tail.go:334), `pool-ctx-watcher` (pool.go:962), `pool-exit-watcher` (exit_watcher.go:50), `engine-after-func` (engine.go:268) |
| OQ-2 (D-18-09 probeFunc signature) | probeFunc unchanged; stateInput.ConfigError added; computeState short-circuits at top | `fsm.go:33` (field), `fsm.go:52-53` (short-circuit at top before PIDAlive/HealthOK) |
| OQ-3 (D-18-05 success reason) | byte-exact `"lazy-respawn-success"` | `internal/pool/pool.go:420` |
| OQ-4 (D-18-04 buffer overflow) | `bufio.NewReader` + `ReadString('\n')`, NOT `bufio.Scanner`; 1MB cap applied to line variable | `internal/acp/client.go:396` (NewReader), `:398` (ReadString), `:395` (`1024 * 1024` cap); zero `bufio.NewScanner` hits |

### TDD Commit Pairs (RED + GREEN per D-ID)

| D-ID | REQ-ID | RED commit | GREEN commit |
|------|--------|-----------|--------------|
| D-18-01 | REL-CFG-05 | `82a5eba` | `25b8bf3` |
| D-18-02 | REL-CFG-06 | `171061f` | `4e514cc` |
| D-18-03 | REL-CFG-07 | `cb8cc99` | `61da4ac` |
| D-18-04 | REL-OBSV-03 | `b9dbd81` | `880d499` |
| D-18-05 | REL-OBSV-02 | `fa66066` | `9314e93` |
| D-18-06 | REL-HTTP-06 | `d91c147` | `608a815` |
| D-18-07 | REL-HTTP-07 | `f27c012` | `749c127` |
| D-18-08 | REL-OBSV-04 | `94e1b97` | `e72f4aa` |
| D-18-09 | REL-TRAY-08 | `46c9f6e` | `822c753` |
| D-18-10 | REL-TRAY-09 | `1aac5fe` | `128f1be` |

Additional commits in phase: `d0c4251` (gofumpt/noctx lint cleanup), `c8192dd` (noctx suppress on bind probe — chore), `e19d5fc`/`15b4db5`/`a136d17` (SUMMARY docs).

All 10 RED commits precede their GREEN counterparts. TDD discipline satisfied tree-wide.

### Six Phase-Close Criteria (from CONTEXT.md §Verification)

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| 1 | `make ci` exit 0 (v1.10.2 baseline preserved); golangci-lint not installed locally — verified via `go build`, `go vet`, `gofmt -l`, `go test -race` | PASS (with documented dev-box gap per CLAUDE.md) | `go build ./...` exit 0; `go vet ./...` exit 0; `gofmt -l .` empty; `go test -race ./...` all ok; golangci-lint deferred to CI per CLAUDE.md |
| 2 | Each REL-* / REQ-ID has at least one regression test | PASS | All 10 regression test functions confirmed via `grep -rn "^func TestRegression_REL_"` |
| 3 | `go test -race ./...` clean tree-wide | PASS | All 17 testable packages report `ok`, race-clean |
| 4 | `grep -rn "cmd.Stderr = os.Stderr"` returns no production hits | PASS | Only hits: `tools/kiro-shim/main.go:79` (dev-only test harness — explicitly acceptable per prompt) and `internal/acp/regression_rel_obsv_03_test.go:3` (test-file comment). Zero hits under `cmd/` or `internal/` production code |
| 5 | Boot-time auth-state INFO at `cmd/otto-gateway/main.go:115-120` remains INFO | PASS | Confirmed via `git show e72f4aa -- cmd/otto-gateway/main.go` — only diff is `logPaths["chat-trace"] = cfg.AdminTailPath` at L662; auth-mode INFO line is byte-identical (`logger.Info("auth mode", "enabled", ..., "ip_allowlist", ..., "trust_xff", ...)`) |
| 6 | Tray FSM state list unchanged (no `StateConfigError`) | PASS | `grep -rn 'StateConfigError' cmd/otto-tray/` returns 0; FSM constants unchanged: `StateUnknown`, `StateStopped`, `StateStarting`, `StateRunning`, `StateDegraded`, `StateError` |

### Anti-Patterns Scan

| File | Pattern | Severity | Impact |
|------|---------|----------|--------|
| None found in phase-modified files | — | — | No TBD/FIXME/XXX debt markers introduced; no console.log-only handlers; no stub returns in production paths |

The plans modified production code paths in `internal/config`, `internal/acp`, `internal/pool`, `internal/engine`, `internal/admin`, `internal/adapter/ollama`, `cmd/otto-gateway`, `cmd/otto-tray`, and `scripts/`. All modifications carry behavioral substance verified by named regression tests.

### Human Verification Required

None. All assertions verified programmatically via grep, build, vet, test, and behavioral spot-checks.

### Documented Deviations (auto-fixed during execution)

The SUMMARYs document all deviations and their fixes:

1. **18-01 — pre-existing tests broken by new KIRO_CMD/KIRO_CWD validation** (Rule 1 bug): test fixtures updated to use `go` binary + `t.TempDir()`; package-wide `TestMain` stamps `KIRO_CMD="go"`. Fixed in commit `4e514cc`.
2. **18-02 — race-detector flake on probe seam variables** (Rule 3): added `sync.Mutex` + helper pattern (`firePanicProbe`/`SetXPanicProbeForTest`). Fixed in commit `749c127`.
3. **18-02 — syncBuf wrapper for goroutine-safe slog destinations** (Rule 2): added per-test-package `syncBuf` type. Fixed in commit `749c127`.
4. **18-02 — noctx lint hits in new test code** (Rule 3): `exec.Command` → `exec.CommandContext`. Fixed in commit `d0c4251`.
5. **18-02 — pre-existing noctx hit from 18-01 bind probe** (out-of-scope deviation): subsequent chore commit `c8192dd` suppressed with rationale.
6. **18-03 — bats not on dev box** (Rule 3 infra gap): replaced planned bats test with plain bash `tests/scripts/test-support-bundle-rel-tray-09.sh`; functional equivalent verified (3/3 pass).
7. **18-03 — helper-function pattern reduced `.config-error` hit count below planned ≥4** (cleanup deviation): documented; functionally equivalent.

All deviations are documented in plan SUMMARYs with rationale and remained inside the phase's behavioral scope.

### Open Follow-ups (deferred, non-blocking)

From SUMMARYs:

- `slot_id` field on D-18-04 stderr WARN log — CONTEXT.md permits omission for v1.10.3 (worker_pid is load-bearing).
- RunHandle Pid accessor for REL-HTTP-06 — `worker_pid:0` placeholder accepted per CONTEXT.md §D-18-06.
- Windows mirror of D-18-10 bundle row removal (`scripts/otto-gw.ps1` lines 1706–1711) — out of scope per CONTEXT.md and Plan 18-03 truth #6 ("REL-TRAY-09 leaves the Windows bundle path UNCHANGED"). Tracked as future follow-up.

These are explicit scope-bounded items, not gaps in phase 18 closure.

### Gaps Summary

None. All 10 REQ-IDs have regression tests, production implementation, byte-exact slog message/field invariants, and TDD RED→GREEN commit pairs. All six phase-close criteria from CONTEXT.md §Verification pass. All 4 open questions surfaced by research are locked into the codebase.

---

_Verified: 2026-06-12T01:38:03Z_
_Verifier: Claude (gsd-verifier)_
