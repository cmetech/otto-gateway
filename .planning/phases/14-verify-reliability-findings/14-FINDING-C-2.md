---
finding: C-2
severity: M
rel_id: REL-CFG-02
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# Finding C-2: PING_INTERVAL <= 0 crashes with raw goroutine panic instead of config error

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §5 (Config, secrets, and startup):

> **[Medium] C-2: Negative PING_INTERVAL crashes the process with a raw goroutine panic instead of a config error**
>
> **Files:** `internal/config/config.go:295, 1057-1071` (accepts negative), `internal/acp/client.go:59-61` (`applyDefaults` only fills when `== 0`), `internal/acp/client.go:505` (`time.NewTicker` panics on non-positive interval, in a goroutine with no recover).
>
> **Failure scenario:** `PING_INTERVAL=-60000` → process dies during pool warmup with a panic stack on **stderr** — and when `LOG_FILE` is set the panic is not in the structured log file, so a user tailing `logs/otto-gateway.log` sees the gateway die with nothing logged.

## Current-source check

Verified against current source (commit `3a72d03`):

**`internal/config/config.go:295`:**
```go
pingInterval, err := getEnvDuration("PING_INTERVAL", 60*time.Second)
```
`getEnvDuration` (lines 1057-1071) accepts any syntactically valid duration or integer millisecond. Zero parses to `0*time.Millisecond = 0` and a negative value like `"-60000"` parses to `-60*time.Second`. Both pass through with no error.

**`internal/acp/client.go:59-61`:**
```go
if c.PingInterval == 0 {
    c.PingInterval = 60 * time.Second
}
```
Only fills the default when `== 0` exactly. A negative value like `-60s` passes through `applyDefaults` unchanged.

**`internal/acp/client.go:505`:**
```go
ticker := time.NewTicker(c.cfg.PingInterval)
```
`time.NewTicker` panics when the interval is non-positive: "non-positive interval for NewTicker". This executes inside the `pingLoop` goroutine launched during `Initialize`, with no `recover()`.

**No sign check in Load():** Grep for `PING_INTERVAL` in config.go's error-append expressions finds only the parse error on unparseable input — no `> 0` guard.

**Contrast:** `STREAM_IDLE_TIMEOUT_SEC` has an explicit `< 0` check at lines 366-368. `PING_INTERVAL` has none.

## Evidence

This is a Medium finding per D-02 (code-walk + t.Skip'd regression test).

**Go regression test:** `internal/config/regression_rel_cfg_02_test.go`
- Function: `TestRegression_REL_CFG_02_PingIntervalPanic`
- Pattern: direct template of `TestLoad_StreamIdleTimeoutSec_Negative` (config_test.go:655-667)
- Two subtests: `PING_INTERVAL=0s` and `PING_INTERVAL=-60000` (the millisecond-integer format used by Node-compat deployments)
- Pre-fix observable: Load() returns nil for both cases; the panic occurs later in the pingLoop goroutine
- Post-fix: Load() returns an error mentioning "PING_INTERVAL" and "must be > 0"

**Code-walk:** The failure path is: `config.Load()` calls `getEnvDuration("PING_INTERVAL", ...)` → negative duration stored in `cfg.PingInterval` → `pool.Warmup` → `acp.Client.Initialize` → spawns `pingLoop` goroutine → `time.NewTicker(cfg.PingInterval)` panics with no recover → process exits with raw panic stack on stderr, bypassing the structured log sink.

## Verdict

**confirmed** — The cite is intact. A zero or negative `PING_INTERVAL` passes `config.Load()` without error, and `acp/client.go:505` will panic in a goroutine on first use. No guard has been added since the review. Phase 16 fix: add `if pingInterval <= 0 { errs = append(errs, fmt.Errorf("PING_INTERVAL: must be > 0, got %v", pingInterval)) }` after the `getEnvDuration` call at config.go:295, and defensively change the `applyDefaults` guard from `== 0` to `<= 0`.
