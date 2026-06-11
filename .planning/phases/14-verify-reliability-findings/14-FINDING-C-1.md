---
finding: C-1
severity: M
rel_id: REL-CFG-01
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# Finding C-1: Negative/zero pool and session env vars are silently coerced

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §5 (Config, secrets, and startup):

> **[Medium] C-1: Negative/zero POOL_SIZE, SESSION_MAX, SESSION_TTL_MS, SESSION_TICK_INTERVAL_MS, CHAT_TRACE_MAX_AGE_DAYS are silently coerced, not rejected**
>
> **Files:** `internal/config/config.go:313, 336, 343, 353, 487` (parse sites; sign never checked), `internal/pool/config.go:119-122` (`Size <= 0` → 1), `internal/session/config.go:99-108` (TTL ≤ 0 → 30m, tick ≤ 0 → 60s, max ≤ 0 → 32), `cmd/otto-gateway/main.go:296` (negative MaxAge into timberjack).
>
> **Failure scenario:** An operator sets `SESSION_TTL_MS=0` intending "never expire" — sessions silently get reaped after 30 minutes anyway. `POOL_SIZE=0` silently spawns one worker. Negative `CHAT_TRACE_MAX_AGE_DAYS` goes into timberjack `MaxAge` with undefined pruning behavior on a file holding raw prompts.

## Current-source check

Verified against current source (commit `3a72d03`):

| Env var | config.go parse site | Sign check in Load()? | Fallback behavior |
|---|---|---|---|
| `POOL_SIZE` | line 313: `getEnvInt("POOL_SIZE", 4)` | **No** | `pool.Config.applyDefaults`: `Size <= 0 → 1` (pool/config.go:120) |
| `SESSION_MAX` | line 343: `getEnvInt("SESSION_MAX", 32)` | **No** | `session.Config.applyDefaults`: `MaxSessions <= 0 → 32` (session/config.go:106) |
| `SESSION_TTL_MS` | line 336: `getEnvDuration("SESSION_TTL_MS", 30*time.Minute)` | **No** | `session.Config.applyDefaults`: `TTL <= 0 → 30m` (session/config.go:100) |
| `SESSION_TICK_INTERVAL_MS` | line 353: `getEnvDuration("SESSION_TICK_INTERVAL_MS", 60*time.Second)` | **No** | `session.Config.applyDefaults`: `TickInterval <= 0 → 60s` (session/config.go:103) |
| `CHAT_TRACE_MAX_AGE_DAYS` | line 487: `getEnvInt("CHAT_TRACE_MAX_AGE_DAYS", 3)` | **No** | Passed as-is to timberjack `MaxAge`; behavior with negative int is undefined |

**Contrast with `STREAM_IDLE_TIMEOUT_SEC`** (config.go:366-368): already has an explicit sign check:
```go
if streamIdleTimeoutSec < 0 {
    errs = append(errs, fmt.Errorf("STREAM_IDLE_TIMEOUT_SEC: must be >= 0, got %d", streamIdleTimeoutSec))
}
```

All 5 vars lack this check. Grep for sign-check in Load() confirms: no `POOL_SIZE`, `SESSION_MAX`, `SESSION_TTL_MS`, `SESSION_TICK_INTERVAL_MS`, or `CHAT_TRACE_MAX_AGE_DAYS` appear in any error-append expression in config.go's `Load()` function.

## Evidence

This is a Medium finding per D-02 (code-walk + t.Skip'd regression test).

**Go regression test:** `internal/config/regression_rel_cfg_01_test.go`
- Function: `TestRegression_REL_CFG_01_NegativeZeroEnvCoercion`
- Pattern: direct template of `TestLoad_StreamIdleTimeoutSec_Negative` (config_test.go:655-667)
- 5 t.Run() subtests, one per env var, each sets the var to "-5", calls config.Load(), and asserts the returned error mentions the var name and "must be >= 0"
- Pre-fix observable: Load() returns nil for all 5 cases (values are silently coerced downstream)

**Code-walk:** Each var's negative value passes `getEnvInt` / `getEnvDuration` without error (negative ints and durations are syntactically valid), then flows into the config struct. The struct values then hit `applyDefaults` which silently floors them to sensible defaults. `CHAT_TRACE_MAX_AGE_DAYS` is the most dangerous: a negative value reaches `timberjack.New(..., MaxAge: chatTraceMaxAgeDays, ...)` with no documented behavior for negative `MaxAge`, potentially disabling pruning on a file holding raw prompts.

## Verdict

**confirmed** — All 5 env vars lack the sign check that `STREAM_IDLE_TIMEOUT_SEC` has. The failure path (silent coercion instead of boot error) still exists. Phase 16 fix scope: add `if x <= 0 { errs = append(errs, fmt.Errorf("VAR: must be >= 0, got %d", x)) }` (or `> 0` for PING_INTERVAL) after each parse site, matching the `STREAM_IDLE_TIMEOUT_SEC` pattern at config.go:366-368.
