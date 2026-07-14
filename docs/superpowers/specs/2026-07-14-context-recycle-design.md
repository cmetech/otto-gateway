# Context-utilization capture + proactive session recycle — design

**Date:** 2026-07-14
**Status:** Proposed (design) — awaiting review. **Implementation BLOCKED on
Track 0** (Co-Worker validation skill must confirm the ACP wire field for
context %). Everything except the wire-parse seam is designable/testable now.
**Scope:** One implementation plan. Track 2 of the
[legacy-gateway parity roadmap](../2026-07-14-legacy-gateway-parity-roadmap.md).
**Mirrors:** Node `b72d6db` — ctx-based session recycling.

## Problem

A stateful conversation (`X-Session-Id` — Claude Code / GSD Pi) maps to a
**persistent kiro session**: `session.Registry` lazy-creates one `Entry` per sid
that caches the kiro `SessionID` and reuses it across requests
(`internal/session/registry.go:181-323`, `entry_acp.go:35`). As the conversation
grows, kiro's context window fills; once near-full, kiro's **internal compaction**
kicks in and degrades instruction-following. The gateway reads **no** context
signal today (`contextUsagePercentage`/`meteringUsage`/`turnDurationMs` appear
nowhere) and recycles sessions only on **idle TTL** (`internal/session/reaper.go`),
so nothing intervenes before compaction bites.

The Node fix recycles the kiro session at ~80% context utilization on the
session's NEXT request — the client re-sends the full transcript anyway, so the
fresh session is re-primed and nothing is lost.

## Why this is safe in the Go gateway

`engine.Run` rebuilds the full prompt from the client-supplied transcript on
**every** request (`engine.buildBlocks`, `internal/engine/build_acp.go:46`
iterates the entire `req.Messages`). Nothing reads prior turns from kiro's
memory. So dropping + recreating the kiro session mid-conversation loses no
conversational context — the next request re-sends the whole transcript and
re-primes the new session (and `Entry.SetModel`'s `LastModel` cache resets on
recreate, so the model is re-applied). This matches the Node "safe because the
caller re-sends the full transcript" guarantee.

## Decisions (locked with the human)

- **Wire-field confirmation waits on Track 0.** The Co-Worker validation skill
  will observe a live kiro session and report where `contextUsagePercentage`
  rides (the Node code reads it from the continuously-streaming `session/update`;
  it may instead ride the `session/prompt` response). Implementation of the
  parse seam (below) is gated on that finding; the rest can be built against an
  injectable ctx-% seam in the meantime.
- **Env knob:** `CTX_RECYCLE_PCT` (Node-parity name), float 0..1, default `0.8`,
  `0` disables. No other new env.

## Design

### 1. Capture `contextUsagePercentage` (seam — parse gated on Track 0)

Add a per-turn `ContextUsagePct float64` that flows from the ACP layer to the
session `Entry`. The parse location depends on Track 0's finding:

- **If it rides `session/update`** (Node's path — "streams continuously"): add
  the field to `sessionUpdateParams`/`sessionUpdateBody`
  (`internal/acp/translate.go:41,75`) and keep the latest value on the stream.
- **If it rides the `session/prompt` response** (cleaner — one sync point per
  turn): add `ContextUsagePct` to `promptResult` (`client.go:233`) and
  `acp.FinalResult` (`stream.go:21`); the shim copies FinalResult → the session
  layer at `entry_acp.go:124-134`.

Either way it surfaces at the session layer as the value for step 2. The seam
(`FinalResult.ContextUsagePct` / a stream accessor) is defined now; only the
byte-level parse is Track-0-gated.

### 2. Track last context % per session

Add `lastCtxPct` (an `atomic`-friendly field) to `session.Entry`
(`internal/session/registry.go:39`). Populate it under `entry.Mu` right where
`MarkUsed` already runs after the stream completes
(`internal/adapter/anthropic/handlers.go:169`, plus the OpenAI/Ollama twins),
reading the threaded ctx-% off the completed `FinalResult`.

### 3. Proactive recycle in `Registry.Get`

The alive+ready branch of `Registry.Get` (`registry.go:206-223`) is the single
choke point every stateful request passes through. Before returning the cached
entry, if recycling is enabled and `e.lastCtxPct >= CTX_RECYCLE_PCT`:

- `Cancel`/`Close` the old client (mirroring `watchEntry`/`reapOnce` teardown),
- `delete(r.entries, sid)`, and fall through to the existing **Dead-path**
  lazy-recreate (`registry.go:189-191`) — the same machinery that already
  recreates a dead entry. The new entry starts at `lastCtxPct = 0`, so the guard
  is naturally one-shot (it won't re-trip until the fresh session fills again).
- Log INFO `session.recycled` with the sid (short) + the tripping pct.
- Increment `sessions_recycled` (feeds Track 4 `gw_sessions_recycled_total`).

`CTX_RECYCLE_PCT == 0` disables the whole check (fast-path unchanged).

### 4. Observability

- Per-session `ctx_pct` in `/health/agents` (`session.SessionDetail` +
  `admin.SnapshotSess` — additive field; snake_case wire contract preserved by
  adding, not renaming).
- `sessions_recycled` counter → Track 4 metric `gw_sessions_recycled_total`.

## Testability

The recycle decision is unit-testable in `internal/session` via the existing
fake client/factory: inject `lastCtxPct` on an `Entry` (test seam) and assert
`Get` recycles (old client `Close`d, a new kiro `NewSession` issued, counter
incremented) at/above threshold, and reuses the entry below threshold. The
capture parse (step 1) gets its own focused ACP test once Track 0 fixes the
field location.

## Tests (TDD)

1. **Recycle at/above threshold.** Entry with `lastCtxPct = 0.85`,
   `CTX_RECYCLE_PCT = 0.8`: `Get` closes the old client, creates a fresh session,
   and `sessions_recycled` increments by 1.
2. **No recycle below threshold.** `lastCtxPct = 0.5`: `Get` returns the SAME
   entry/client; no recycle, counter unchanged.
3. **One-shot.** After a recycle, the fresh entry has `lastCtxPct = 0`, so the
   immediate next `Get` does not recycle again.
4. **Disabled.** `CTX_RECYCLE_PCT = 0`: even `lastCtxPct = 0.99` does not
   recycle (fast-path preserved).
5. **Capture (Track-0-gated).** Once the field is confirmed: a `session/update`
   (or prompt-response) carrying `contextUsagePercentage` lands on
   `Entry.lastCtxPct` after a turn.

## Files touched (anticipated)

- `internal/acp/translate.go` **or** `internal/acp/client.go` + `stream.go` —
  parse `contextUsagePercentage` (location per Track 0); thread via FinalResult.
- `internal/canonical` — `FinalResult.ContextUsagePct` (if threaded through canonical).
- `internal/session/registry.go` + `entry_acp.go` — `lastCtxPct`; recycle in
  `Get`; `sessions_recycled` counter; config `CTX_RECYCLE_PCT`.
- `internal/session/config.go` + `internal/config/config.go` — `CTX_RECYCLE_PCT`
  env parse (float 0..1, default 0.8).
- `internal/session/stats.go` + `internal/admin/snapshot.go` — additive
  `ctx_pct` on the per-session detail.
- Tests across the above.

## Verification

- `go build ./...`, `go vet`, gofumpt-clean.
- `go test ./internal/session/... ./internal/acp/... ./internal/admin/...`
  green (incl. the recycle tests; the capture test once Track 0 lands).
- `golangci-lint` on touched packages.
- Manual (real kiro): drive a long stateful conversation past
  `CTX_RECYCLE_PCT`; confirm a `session.recycled` log fires, the conversation
  continues coherently (transcript re-primed), and `/health/agents` shows the
  per-session `ctx_pct` climbing then resetting after a recycle.
