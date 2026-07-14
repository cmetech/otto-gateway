# Resilient model discovery — design

**Date:** 2026-07-14
**Status:** Proposed (design) — awaiting review
**Scope:** One implementation plan. Track 1 of the
[legacy-gateway parity roadmap](../2026-07-14-legacy-gateway-parity-roadmap.md).
**Mirrors:** Node `6bbd0c2` — "resilient model discovery".

## Problem

The Go gateway discovers the kiro model catalog **once**, from slot-0's
`NewSession` during `pool.Warmup`:

```
sid, err := slot.Client.NewSession(ctx, p.cfg.KiroCWD)   // internal/pool/pool.go:245
...
p.models = slot.Client.AvailableModels()                 // one-shot capture
```

`Pool.Models()` only ever returns that snapshot; nothing repopulates it. The
catalog feeds `/api/tags`, `/v1/models`, and `/api/show` via the adapters'
`ModelCatalog.Models()` seam, which prepends a synthetic `"auto"` and appends
the catalog (`internal/adapter/ollama/handlers.go:672-681`).

Two cold-boot failure modes, both unrecovered without a process restart:

1. **`NewSession` errors** (kiro-cli cold/slow at boot): `Warmup` is fail-fast —
   it tears down and aborts startup entirely (`pool.go:245-249`). The whole
   gateway fails to boot on a transient kiro hiccup.
2. **`NewSession` succeeds but returns an empty `availableModels`** (kiro warm
   but catalog not ready yet): `client.models` is set to `nil`
   (`internal/acp/client.go` — empty source → nil), `Warmup` succeeds, and the
   catalog is **permanently** `nil` → `/api/tags` shows only `"auto"` until the
   process is restarted. This is the exact "auto-only until warm" degradation the
   Node fix targeted.

The Node fix replaced the one-shot probe with `discoverModels()` (retry +
backoff) plus a lazy self-heal on `/api/tags` so the list recovers without a
restart.

## Non-goals

- No change to the `ModelCatalog.Models()` adapter seam or the `"auto"`-prepend
  wire behavior (`/api/tags`, `/v1/models`, `/api/show` shapes are unchanged).
- No change to the model *catalog contents* or `canonical.ModelInfo`.
- No new dependency; no change to the per-request session model (engine.Run
  still creates fresh sessions per request).
- The Node `ACP_STATE_DIR`/`acp_state.json` state-file move is Node-specific
  (Go has no `acp_state.json`) — out of scope.

## Behavior

### 1. Retry-with-backoff on the initial capture

In `Warmup`, wrap the slot-0 catalog capture in a bounded retry: if
`NewSession` errors **or** returns an empty catalog, retry up to `N` times with
backoff before giving up. This absorbs a transiently-cold kiro at boot.

- Bounded, context-aware (each attempt honors the `Warmup` ctx; a cancelled ctx
  aborts immediately).
- Parameters: `N = 3` attempts, backoff `250ms → 500ms → 1s` (constants; not new
  env — the operator already tunes cold-boot via existing pool knobs). Values
  finalized in the plan.

### 2. Degrade instead of fail-fast on catalog capture

If, after retries, the catalog is still empty, **do not abort boot**. Warmup
completes; the gateway serves with a `"auto"`-only catalog and self-heals later
(step 3). Rationale: a usable gateway serving `"auto"` beats a gateway that
won't boot — and matches the Node "degraded, then recover" posture and this
repo's existing degraded-mode precedent (`KIRO_CMD` unset → `Size 0` healthy).

Scope note: this changes ONLY the model-catalog branch. A slot **spawn/Initialize**
failure keeps its existing fail-fast semantics (a pool that can't spawn any
worker genuinely can't serve). Only "spawned + initialized fine, but the catalog
came back empty" degrades instead of aborting.

### 3. Lazy self-heal on catalog read

Add a re-probe path so the catalog recovers on demand once kiro is warm:

- When `Pool.Models()` is asked for the catalog and it is currently empty, kick
  a **background** re-probe (acquire a slot, `NewSession`, capture, `Cancel`),
  then return the current (possibly still-empty → `"auto"`) snapshot immediately.
  The read never blocks on kiro; the *next* read sees the healed catalog.
- **Singleflight guard:** at most one re-probe in flight at a time (a `sync.Once`-
  style / atomic-guarded goroutine), so a burst of `/api/tags` polls triggers one
  probe, not a stampede against the pool.
- Once the catalog is non-empty, no further probes fire (the guard short-circuits
  on a populated catalog).

The re-probe reuses the same acquire→`NewSession`→capture→`Cancel` sequence as
Warmup, factored into a shared unexported `probeCatalog(ctx)` helper so Warmup
and the self-heal path cannot drift.

## Testability

All three behaviors are driven through the existing pool **`Factory` seam**
(`internal/pool/config.go` — tests already inject a fake `ClientFactory` /
`PoolClient`). The fake client's `NewSession` / `AvailableModels` are scripted to
model each scenario. No real kiro-cli, deterministic.

## Tests (TDD)

1. **Retry then success.** Fake `NewSession` errors twice, then returns a
   2-model catalog. Assert `Warmup` succeeds and `Models()` returns the 2 models
   (retry recovered the transient failure).
2. **Empty catalog degrades, does not abort.** Fake `NewSession` always returns
   an empty catalog. Assert `Warmup` returns nil (boot succeeds) and `Models()`
   is empty (→ adapters render `"auto"`-only).
3. **Lazy self-heal on read.** Start from the degraded (empty) state; the fake
   is then flipped to return a real catalog. Call `Models()` (triggers the
   background probe), wait for the probe, call `Models()` again → non-empty.
   Assert exactly one probe ran for concurrent reads (singleflight).
4. **No re-probe once populated.** With a populated catalog, N concurrent
   `Models()` calls trigger **zero** probes (guard short-circuits).
5. **Spawn/Initialize failure still fail-fast.** A slot spawn error still aborts
   `Warmup` (guards against the degrade-on-empty change loosening genuine spawn
   failures).

## Files touched (anticipated)

- `internal/pool/pool.go` — extract `probeCatalog`; retry+backoff in Warmup;
  degrade-on-empty; background self-heal in/around `Models()` with a singleflight
  guard field on `Pool`.
- `internal/pool/pool_test.go` (or a focused new test file) — the five tests via
  the `Factory` fake.

## Verification

- `go build ./...`, `go vet ./...`, gofumpt-clean.
- `go test ./internal/pool/... ./internal/adapter/...` green (adapters unaffected;
  guards the `Models()` contract).
- `golangci-lint` on `internal/pool/...`.
- Manual: boot with a real kiro-cli that is briefly cold; confirm `/api/tags`
  starts at `"auto"`-only and self-heals to the full catalog on a subsequent poll
  without a restart.
