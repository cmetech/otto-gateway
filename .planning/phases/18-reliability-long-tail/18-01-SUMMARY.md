---
phase: 18-reliability-long-tail
plan: 01
subsystem: infra
tags: [go, config-validation, slog, reliability, tdd]

# Dependency graph
requires:
  - phase: 14-reliability-cluster
    provides: REL-CFG-01/02/03 patterns (errs accumulator, captureSlogDefault helper, slog.Default Warn precedent at config.go:576-580)
provides:
  - REL-CFG-05 closure — degenerate AUTH_TOKEN/ALLOWED_IPS emit slog.Warn + treat-as-unset (D-18-01)
  - REL-CFG-06 closure — KIRO_CMD/KIRO_CWD config-named errors + ~ expansion (D-18-02)
  - REL-CFG-07 closure — bind-then-close HTTP_ADDR port probe before pool warmup (D-18-03)
  - Three new TDD regression tests under internal/config/ that lock the behavior
affects:
  - Phase 18-02 — observability symmetry (no overlap; both touch independent files)
  - Phase 18-03 — tray honesty (no overlap)
  - Future operators editing dotenv files — boot errors now name the offending env var

# Tech tracking
tech-stack:
  added: []  # No new deps; only stdlib (os/exec, net, path/filepath, os.UserHomeDir)
  patterns:
    - "config.Load() validates env at boot via errs accumulator → errors.Join (existing pattern; extended)"
    - "slog.Default().Warn for degenerate inputs that are intentionally treat-as-unset (extends REL-CFG-03 emission shape)"
    - "Bind-then-close TCP probe surfaces port-in-use pre-warmup (TOCTOU window accepted)"
    - "Package-wide TestMain stamps KIRO_CMD='go' so default-load path works on CI runners without kiro-cli installed"

key-files:
  created:
    - internal/config/regression_rel_cfg_05_test.go
    - internal/config/regression_rel_cfg_06_test.go
    - internal/config/regression_rel_cfg_07_test.go
  modified:
    - internal/config/config.go  # +3 validation branches; +3 imports (net, os/exec)
    - internal/config/config_test.go  # TestLoadDefaults / TestLoadEnvOverrides fixed for new KIRO_CMD validation
    - internal/config/testmain_test.go  # Stamps KIRO_CMD="go" alongside existing PII_ENCRYPT_KEY
    - cmd/otto-gateway/testmain_test.go  # Same KIRO_CMD stamp for cmd-level tests

key-decisions:
  - "D-18-01 honored byte-exact: WARN msg 'AUTH_TOKEN/ALLOWED_IPS looks degenerate (no entries after trim+CSV split); treating as unset' with raw=<input> field"
  - "D-18-01 implementation: use os.Getenv (no TrimSpace) for the 'is set' predicate so whitespace-only AUTH_TOKEN values still trip the WARN (Case F)"
  - "D-18-02 error string byte-exact: 'config: KIRO_CMD (\"x\"): not found in PATH or unreadable' with %q formatting"
  - "D-18-02 tilde expansion: ~/sub → filepath.Join(home, 'sub'); bare ~ → home. KIRO_CMD does NOT get tilde expansion"
  - "D-18-03 probe placement: AFTER all other env loads, BEFORE errors.Join — keeps single error surface"
  - "D-18-03 wraps underlying net.OpError via %w so errors.Unwrap preserves the syscall chain for diagnosis"
  - "Test-package TestMain stamps KIRO_CMD='go' so the new LookPath validation does not boot-fail CI runners that lack kiro-cli (the production install path always has it)"

patterns-established:
  - "Config-named error format: 'config: <VAR_NAME> (%q): <reason>' — operators see which env var to fix without re-reading the env"
  - "Treat-as-unset + WARN over fail-fast for degenerate auth env values (CLAUDE.md posture preserved)"
  - "Bind-then-close TCP probe for port reservation hints (TOCTOU acknowledged, microseconds wide)"

requirements-completed: [REL-CFG-05, REL-CFG-06, REL-CFG-07]

# Metrics
duration: 30 min
completed: 2026-06-12
---

# Phase 18 Plan 01: Config Hardening Summary

**Three boot-time validation branches added to config.Load(): WARN on degenerate AUTH_TOKEN/ALLOWED_IPS (D-18-01), config-named KIRO_CMD/KIRO_CWD errors with ~ expansion (D-18-02), and bind-then-close HTTP_ADDR port probe surfacing port-in-use before kiro-cli pool warmup (D-18-03). Six atomic RED→GREEN commits; tree-wide `go test -race` clean.**

## Performance

- **Duration:** 30 min
- **Started:** 2026-06-12T00:10:00Z (approx)
- **Completed:** 2026-06-12T00:41:26Z
- **Tasks:** 3 (each TDD: RED + GREEN = 6 commits)
- **Files modified:** 7 (3 new, 4 modified)

## Accomplishments

- **REL-CFG-05 closed.** `AUTH_TOKEN=" , "`, `ALLOWED_IPS=",,  ,"`, `AUTH_TOKEN="   "` and friends now emit a structured `slog.Warn` from `config.Load` via `slog.Default()` with the byte-exact msg required by CONTEXT.md, AND continue to be treated as unset (no fail-fast — CLAUDE.md "no auth if env unset" posture preserved).
- **REL-CFG-06 closed.** `KIRO_CMD` validated via `exec.LookPath` and surfaces a `config: KIRO_CMD ("<value>"): not found in PATH or unreadable` error. `KIRO_CWD` validated via `os.Stat` + `IsDir` with two named error variants. `~/sub` and bare `~` in `KIRO_CWD` resolved via `os.UserHomeDir + filepath.Join` once during `Load()`; expanded path stored in `Config.KiroCWD`.
- **REL-CFG-07 closed.** A `net.Listen("tcp", httpAddr)` + immediate `Close()` probe runs at the end of `Load()` and surfaces a `config: HTTP_ADDR ("<addr>"): bind probe failed: <wrapped syscall err>` error within milliseconds — before the 5–10s pool warmup. The TOCTOU window is documented and accepted.
- Boot-time auth-state INFO line at `cmd/otto-gateway/main.go:115-120` is byte-identical to the pre-plan baseline (verified by `git diff cmd/otto-gateway/main.go` showing zero hunks).
- TDD discipline visible in git history: every fix has a RED test commit BEFORE the GREEN implementation commit.

## Task Commits

1. **Task 1 — REL-CFG-05 RED:** `82a5eba` (test) — `test(18-01): RED — REL-CFG-05 degenerate AUTH_TOKEN/ALLOWED_IPS Warn assertion`
2. **Task 1 — REL-CFG-05 GREEN:** `25b8bf3` (feat) — `feat(18-01): GREEN — REL-CFG-05 Warn on degenerate AUTH_TOKEN/ALLOWED_IPS (D-18-01)`
3. **Task 2 — REL-CFG-06 RED:** `171061f` (test) — `test(18-01): RED — REL-CFG-06 named KIRO_CMD/KIRO_CWD errors + ~ expansion`
4. **Task 2 — REL-CFG-06 GREEN:** `4e514cc` (feat) — `feat(18-01): GREEN — REL-CFG-06 named KIRO_CMD/KIRO_CWD errors + ~ expansion (D-18-02)`
5. **Task 3 — REL-CFG-07 RED:** `cb8cc99` (test) — `test(18-01): RED — REL-CFG-07 bind-then-close HTTP_ADDR port probe`
6. **Task 3 — REL-CFG-07 GREEN:** `61da4ac` (feat) — `feat(18-01): GREEN — REL-CFG-07 bind-then-close HTTP_ADDR probe (D-18-03)`

## REQ-ID Closeout

| REQ-ID | Status | Regression test | Implementation site |
|--------|--------|-----------------|---------------------|
| REL-CFG-05 | ✅ closed | `internal/config/regression_rel_cfg_05_test.go::TestRegression_REL_CFG_05` (6 subcases A–F) | `internal/config/config.go` — degenerate-env branches at AUTH_TOKEN + ALLOWED_IPS load sites |
| REL-CFG-06 | ✅ closed | `internal/config/regression_rel_cfg_06_test.go::TestRegression_REL_CFG_06` (7 subcases A–G) | `internal/config/config.go` — `exec.LookPath` on KIRO_CMD; `os.Stat`+`IsDir` on KIRO_CWD; `os.UserHomeDir`+`filepath.Join` tilde expansion |
| REL-CFG-07 | ✅ closed | `internal/config/regression_rel_cfg_07_test.go::TestRegression_REL_CFG_07` (2 subcases A pre-bound + B kernel-assigned) | `internal/config/config.go` — `net.Listen("tcp", httpAddr)` + immediate `Close()` before `errors.Join` |

## Files Created/Modified

- `internal/config/regression_rel_cfg_05_test.go` (NEW) — TestRegression_REL_CFG_05 table-driven 6-case suite; reuses `captureSlogDefault` / `decodeLogRecords` from `regression_rel_cfg_03_test.go`.
- `internal/config/regression_rel_cfg_06_test.go` (NEW) — TestRegression_REL_CFG_06 7-case suite covering KIRO_CMD bogus/known-present, KIRO_CWD missing/not-a-dir/~ expansion/bare ~/optional-empty cases.
- `internal/config/regression_rel_cfg_07_test.go` (NEW) — TestRegression_REL_CFG_07 2-case suite (pre-bound listener forces EADDRINUSE; kernel-assigned :0 is the success path).
- `internal/config/config.go` (MODIFIED) — Added `os/exec` and `net` imports; added three validation branches (degenerate-WARN, LookPath+Stat+~ expansion, bind probe).
- `internal/config/config_test.go` (MODIFIED) — Pre-existing `TestLoadDefaults` and `TestLoadEnvOverrides` used fake paths (`/usr/local/bin/kiro-cli`, `/tmp/test`) that now fail KIRO_CMD/KIRO_CWD validation. Switched to `go` + `t.TempDir()`. Rule 1 fix (pre-existing tests broken by my correctness improvement).
- `internal/config/testmain_test.go` (MODIFIED) — Extended package-wide env stamp to also set `KIRO_CMD="go"` so default-load tests pass on CI runners that don't ship `kiro-cli`.
- `cmd/otto-gateway/testmain_test.go` (MODIFIED) — Same `KIRO_CMD="go"` stamp for `cmd/otto-gateway/main_test.go` which also calls `config.Load()` directly.

`cmd/otto-gateway/main.go` is byte-identical to the pre-plan baseline.

## Decisions Made

- **Used `os.Getenv` directly (no TrimSpace) for the AUTH_TOKEN "is set" predicate** (D-18-01 Case F). The plan's table-driven test asserted `AUTH_TOKEN="   "` (whitespace only) MUST trip the WARN; trimming first would have classified it as unset and silently skipped. The `getEnvStrSliceComma` parse step still drops whitespace, so the "raw vs parsed-empty" comparison cleanly detects degeneracy.
- **`net.Listen("tcp", ...)` probe placed AFTER every other env load** but BEFORE `errors.Join` so all config errors surface in one bundle. Probe runs unconditionally — even when HTTP_ADDR is the documented default — per CONTEXT.md.
- **`%q` formatting for offending values in named errors** (e.g. `KIRO_CMD (%q)`). Matches RESEARCH.md recommendation and produces a consistent quoted-value shape across the three new error types.
- **Test-package TestMain stamps `KIRO_CMD="go"`.** Rationale: KIRO_CMD's default `"kiro-cli"` is not guaranteed in CI PATH. The Go toolchain always is. Tests that exercise the not-found path override with their own `t.Setenv`. This preserves the production validation contract while keeping the test suite portable.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — Bug] Pre-existing tests broken by new KIRO_CMD/KIRO_CWD validation**

- **Found during:** Task 2 GREEN (REL-CFG-06 implementation).
- **Issue:** `internal/config/config_test.go::TestLoadEnvOverrides` set `KIRO_CMD="/usr/local/bin/kiro-cli"` and `KIRO_CWD="/tmp/test"` as placeholder values. Neither exists on a fresh dev box, so both new validation branches fired and the test failed with `config: invalid env vars: config: KIRO_CMD ("/usr/local/bin/kiro-cli"): not found in PATH or unreadable; config: KIRO_CWD ("/tmp/test"): directory does not exist`. The same package's `TestLoadDefaults` would fail on any CI runner without `kiro-cli` in PATH (the default KIRO_CMD value).
- **Fix:** (a) Updated `TestLoadEnvOverrides` to use `KIRO_CMD="go"` (guaranteed in any Go CI environment) and `KIRO_CWD=t.TempDir()`. (b) Extended both `internal/config/testmain_test.go` and `cmd/otto-gateway/testmain_test.go`'s `TestMain` to stamp `KIRO_CMD="go"` alongside the existing `PII_ENCRYPT_KEY` stamp. (c) Updated `TestLoadDefaults` to assert `cfg.KiroCmd == "go"` (the stamped value) rather than the production default `"kiro-cli"`, with a doc comment explaining why.
- **Files modified:** `internal/config/config_test.go`, `internal/config/testmain_test.go`, `cmd/otto-gateway/testmain_test.go`.
- **Verification:** `go test -race ./...` clean tree-wide after the fixes; the new `regression_rel_cfg_06_test.go` Case A (bogus KIRO_CMD) still asserts the error path correctly because it explicitly overrides the stamp.
- **Committed in:** `4e514cc` (Task 2 GREEN — combined with the implementation as the fix and the failure cause are the same change).

**Total deviations:** 1 auto-fixed (Rule 1 — bug: my new correctness check broke pre-existing tests that used placeholder paths).
**Impact on plan:** No scope creep. The fix is mechanical and preserves the test's intent; the validation contract is unchanged.

## Issues Encountered

None blocking. The pre-existing-test breakage above was discovered immediately by the test-race run and resolved in the same GREEN commit.

## Verification Results

| Gate | Result | Notes |
|------|--------|-------|
| `go test -race ./internal/config/...` | ✅ ok | All three new regression suites + existing tests green |
| `go test -race ./...` | ✅ ok | Tree-wide; no regressions in other packages |
| `go vet ./...` | ✅ clean | No new vet findings |
| `gofmt -l internal/config/` | ✅ empty | gofmt applied to test file once during execution; clean at commit |
| `git diff cmd/otto-gateway/main.go` | ✅ empty | Byte-identical to pre-plan baseline (D-18-01 INFO preservation) |
| `grep 'config: KIRO_CMD' internal/config/config.go` | 1 | Matches plan expectation |
| `grep 'config: KIRO_CWD' internal/config/config.go` | 2 | Matches plan expectation (two distinct error strings) |
| `grep 'config: HTTP_ADDR' internal/config/config.go` | 1 | Matches plan expectation |
| `slog.Default().Warn` count (non-comment) | 4 | Existing 2 (embedding stub + pii entity_actions) + new 2 (auth + allowlist degenerate) |
| `make fmt-check` | ✅ ok | gofumpt formatting clean |
| `make vet` | ✅ ok | `go vet ./...` clean |
| `make build` | ✅ ok | Cross-package build succeeds |
| `make test-race` | ✅ ok | All tests pass with race detector |
| `make arch-lint` | ✅ ok | No new dep-graph violations |
| `make examples` | ✅ ok | Go Example tests still discoverable |
| `make lint` | ⚠️ skipped locally | `golangci-lint` not installed on dev box — runs in CI. No new lint-targeted patterns introduced; LookPath/Stat/Listen are stdlib idioms |
| `make ci` | ⚠️ partial locally | Same — every gate except `lint` runs clean locally |

## User Setup Required

None — no external service configuration, env-var additions, or operator action needed. The new validation branches surface existing-but-degenerate config; operators with valid envs see no behavior change.

## Next Phase Readiness

- 18-02 (Observability symmetry + HTTP error logging) is ready to start — no file overlap with this plan.
- 18-03 (Tray honesty) is ready to start — no overlap.
- The new `Config` field set is unchanged (Config.KiroCWD type still `string`); downstream consumers (acp.Client) consume the expanded path transparently.
- No follow-ups parked. All three D-IDs (D-18-01, D-18-02, D-18-03) lock cleanly per CONTEXT.md.

## Self-Check: PASSED

All claimed files exist on disk and all claimed commit hashes are present in git history:

- ✅ `internal/config/regression_rel_cfg_05_test.go` (FOUND)
- ✅ `internal/config/regression_rel_cfg_06_test.go` (FOUND)
- ✅ `internal/config/regression_rel_cfg_07_test.go` (FOUND)
- ✅ `.planning/phases/18-reliability-long-tail/18-01-SUMMARY.md` (FOUND)
- ✅ Commits `82a5eba`, `25b8bf3`, `171061f`, `4e514cc`, `cb8cc99`, `61da4ac` (all FOUND)

---
*Phase: 18-reliability-long-tail*
*Plan: 01-config-hardening*
*Completed: 2026-06-12*
