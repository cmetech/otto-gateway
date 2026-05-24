---
phase: 02-ollama-end-to-end
plan: 03
subsystem: config
tags: [config, env-vars, netip, node-parity, auth, ip-allowlist, pool, h-7]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: "Config struct + getEnvStr/getEnvBool/getEnvDuration/getEnvStrSlice helpers + multi-error errors.Join aggregation pattern in Load()"
provides:
  - "Config.AuthToken []string (AUTH_TOKEN, comma-split, default nil = no auth)"
  - "Config.AllowedIPs []netip.Prefix (ALLOWED_IPS, comma-split, default nil = allow-all; set-but-malformed → Load error)"
  - "Config.PoolSize int (POOL_SIZE, default 1; set-but-unparseable → Load error)"
  - "Config.OllamaPathPrefix string (OLLAMA_PATH_PREFIX, default /api)"
  - "Config.OpenAIPathPrefix string (OPENAI_PATH_PREFIX, default /v1 — forward design for Phase 3)"
  - "Config.AuthTrustXFF bool (AUTH_TRUST_XFF, default false — Codex H-7 safe-by-default; set-but-malformed → Load error)"
  - "getEnvStrSliceComma helper (distinct from whitespace-split getEnvStrSlice)"
  - "getEnvInt helper (matches getEnvBool error shape)"
  - "parseCIDRs helper (netip.ParsePrefix + bare-IP fallback via netip.ParseAddr → host prefix)"
affects: [02-04, 02-05, 02-06, 03-openai, 03.1-anthropic, 05-pool, 08-hooks]

# Tech tracking
tech-stack:
  added: [net/netip]
  patterns:
    - "netip-only CIDR/IP handling (no net.ParseCIDR/net.ParseIP/net.IPNet anywhere in config)"
    - "Comma-split env-var helper (getEnvStrSliceComma) distinct from whitespace-split (getEnvStrSlice) — separator semantics encoded in the helper name"
    - "Multi-error accumulation in Load() preserved — every new field appends to the same errs slice; errors.Join surfaces them all in one Load() error rather than fail-fast"
    - "Whitebox-test split: internal_test.go (package config) for unexported helpers; external_test.go (package config_test) for the public Load() surface"
    - "(nil → nil, nil) vs ([]string{} → empty non-nil slice, nil) contract for parseCIDRs — documented and asserted"

key-files:
  created:
    - "internal/config/config_internal_test.go (whitebox package config — parseCIDRs coverage)"
  modified:
    - "internal/config/config.go (six new fields + three new helpers, all in additive position; Load() body extended; existing fields and tests untouched)"
    - "internal/config/config_test.go (14 new TestLoad_* tests for the new Load() surface)"

key-decisions:
  - "parseCIDRs returns (nil, nil) for nil input but an empty non-nil slice for []string{} input — this preserves the 'empty env = allow-all' Node-parity at the Load() level (where the env helper returns nil on empty) while still giving callers a stable empty-slice contract when they explicitly pass an empty slice"
  - "ALLOWED_IPS error is wrapped with 'ALLOWED_IPS: %w' rather than letting parseCIDRs's per-entry messages stand alone — the wrapping lets the test assert the env-var name appears in the final error, which gives operators a one-line breadcrumb back to the offending env var"
  - "AUTH_TRUST_XFF reuses the existing getEnvBool helper rather than introducing a new boolean path — consistency with the existing DEBUG flag, free malformed-value error surfacing"
  - "Three new helpers placed adjacent to existing helpers (getEnvStrSlice → getEnvStrSliceComma → getEnvInt → parseCIDRs) rather than at file end — keeps the helper block contiguous so future readers find them all in one place"

patterns-established:
  - "Pattern: env-var helper naming encodes the separator (getEnvStrSlice = whitespace, getEnvStrSliceComma = comma) — future helpers should follow this convention"
  - "Pattern: every new env var with a typed default appends to the existing errs slice when malformed — never short-circuit, so operators get the full list of issues at startup"
  - "Pattern: unexported helpers tested via *_internal_test.go (package <pkg>); exported surface tested via *_test.go (package <pkg>_test). Mirrors auth_internal_test.go from Plan 02."

requirements-completed:
  - AUTH-01
  - AUTH-02
  - POOL-01

# Metrics
duration: 9min
completed: 2026-05-24
---

# Phase 02 Plan 03: Config Extension Summary

**Six new Config fields + three new env-var helpers wire AUTH_TOKEN, ALLOWED_IPS, POOL_SIZE, OLLAMA_PATH_PREFIX, OPENAI_PATH_PREFIX, and AUTH_TRUST_XFF into the typed Config struct so Plan 06's main.go can hand one value to the auth middleware, the pool, the Ollama adapter, and the server constructor.**

## Performance

- **Duration:** ~9 min
- **Started:** 2026-05-24T00:27Z
- **Completed:** 2026-05-24T00:36Z
- **Tasks:** 2
- **Files modified:** 3 (1 created, 2 edited)

## Accomplishments

- Six typed Config fields populated from Node-compatible env vars (plus the Loop24-specific AUTH_TRUST_XFF per Codex H-7) — Phase 2 wiring no longer has to re-read env vars in each consumer.
- Three new helpers (`getEnvStrSliceComma`, `getEnvInt`, `parseCIDRs`) round out the env-parsing kit; the comma-split helper is intentionally distinct from the existing whitespace-split helper so the separator semantics live in the helper name, not in operator memory.
- 14 new `TestLoad_*` tests + 7 `TestParseCIDRs_*` tests cover defaults, overrides, malformed inputs, IPv6, and the (nil vs []string{}) contract distinction; existing Phase 1 tests untouched and still green; golangci-lint clean.
- Threat model mitigations T-02-11 (malformed ALLOWED_IPS), T-02-12 (malformed POOL_SIZE), and T-02-39 (XFF opt-in safe-by-default) all asserted in test code, not just documented.

## Task Commits

Each task was committed atomically:

1. **Task 1: Extend Config struct + helpers** — `e53844c` (feat)
2. **Task 2: Extend tests + add internal_test for parseCIDRs** — `ad7acb8` (test)

## Files Created/Modified

- `internal/config/config.go` — Added 6 fields after `PingInterval`, 3 helpers after `getEnvStrSlice`, and 6 env-var reads in `Load()`; new `net/netip` import. Existing exports unchanged.
- `internal/config/config_test.go` — Appended 14 new test functions exercising the new `Load()` surface; preserved `package config_test` (blackbox). Added `reflect`, `sort`, `strings` imports.
- `internal/config/config_internal_test.go` — NEW whitebox file (`package config`) holding 7 `TestParseCIDRs_*` functions for the unexported helper; mirrors the auth_internal_test.go split from Plan 02.

## Decisions Made

- **`parseCIDRs(nil)` → `(nil, nil)`; `parseCIDRs([]{})` → `([]{}, nil)`.** Documented contract distinction so the empty-env-means-allow-all Node parity at the `Load()` layer composes cleanly with callers that always want a non-nil slice. Test coverage asserts both.
- **`ALLOWED_IPS` wraps `parseCIDRs`'s error with `"ALLOWED_IPS: %w"`.** Gives the operator a one-token breadcrumb back to the env-var name even when the inner error message names only the entry. The test asserts `strings.Contains(err.Error(), "ALLOWED_IPS")`.
- **`AUTH_TRUST_XFF` reuses `getEnvBool`.** No new boolean-parsing path; gets malformed-value error surfacing for free.

## Deviations from Plan

None — plan executed exactly as written. The plan's `<action>` block called for the `ALLOWED_IPS` error to be appended to `errs` directly; I wrapped it with the env-var name (`fmt.Errorf("ALLOWED_IPS: %w", err)`) so the test could assert the env-var name surfaces in `err.Error()` — this matches the plan's `<acceptance_criteria>` which expects the test to optionally assert the offending env var key appears in the error message. Not a deviation in behavior; a strict adherence to the acceptance contract over the literal `<action>` wording.

## Issues Encountered

None. Build, tests, and lint passed on first run.

## User Setup Required

None — no external service configuration required. Operators who want to opt into XFF trust now set `AUTH_TRUST_XFF=true`; defaults are safe.

## Threat Surface Scan

No new HTTP / network / file-access surfaces introduced. The only trust-boundary change is at the env-var layer (operator-controlled) and is covered by the plan's `<threat_model>` register (T-02-11, T-02-12, T-02-13, T-02-14, T-02-39).

## Self-Check

**Files claimed:**
- `/Users/coreyellis/Projects/repos/local/loop24-gateway/.claude/worktrees/agent-a1ae61c7598c774be/internal/config/config.go` — modified
- `/Users/coreyellis/Projects/repos/local/loop24-gateway/.claude/worktrees/agent-a1ae61c7598c774be/internal/config/config_test.go` — modified
- `/Users/coreyellis/Projects/repos/local/loop24-gateway/.claude/worktrees/agent-a1ae61c7598c774be/internal/config/config_internal_test.go` — created

**Commits claimed:**
- `e53844c` (Task 1: feat)
- `ad7acb8` (Task 2: test)

**Verification:** All three files exist on disk; both commits are present in `git log`; `go build ./internal/config/...`, `go test -race -count=1 ./internal/config/...`, and `golangci-lint run ./internal/config/...` all exit 0.

## Self-Check: PASSED

## Next Phase Readiness

- **Plan 02-04 (Ollama adapter wiring)** can now read `cfg.OllamaPathPrefix` instead of re-parsing `OLLAMA_PATH_PREFIX`.
- **Plan 02-05 (Pool)** can read `cfg.PoolSize` instead of re-parsing `POOL_SIZE`; the existing plan-04 `Pool.applyDefaults` floor-to-1 logic is still defense-in-depth for negative values (getEnvInt accepts `-1` since it only errors on non-integers).
- **Plan 02-06 (auth middleware + server constructor)** can read `cfg.AuthToken`, `cfg.AllowedIPs`, and `cfg.AuthTrustXFF` directly — the auth.Config wiring spelled out in PATTERNS.md §MODIFY: internal/server/server.go now has a typed source.
- **Phase 3 (OpenAI surface)** can begin consuming `cfg.OpenAIPathPrefix`; field is in place and tested even though Phase 2 doesn't read it.
- No new blockers introduced.

---
*Phase: 02-ollama-end-to-end*
*Completed: 2026-05-24*
