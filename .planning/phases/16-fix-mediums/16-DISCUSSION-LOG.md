---
phase: 16
phase_name: Fix Mediums
created: 2026-06-11
---

# Phase 16 — Discussion Log

Audit trail for the gray areas surfaced during `/gsd-discuss-phase 16`. CONTEXT.md is the canonical record; this log preserves the question/answer thread for retrospectives.

## Area 1 — Plan granularity

**Question:** How should Phase 16's 14 findings be split into plans?

**Options presented:**
1. 5 plans by subsystem (Pool+O-1 / HTTP / Hooks / Tray / Config) — mirrors Phase 14/15
2. 4 plans — fold Hooks into HTTP (shared handler chain)
3. 3 plans — aggressive folding (Pool+Hooks+O-1 / HTTP+Tray / Config)

**Decision:** Option 1 — 5 plans by subsystem. O-1 folds into Pool (it IS the pool exhaustion WARN). Captured as D-01.

**Notes:** Plan 16-04 (Tray) likely depends on Plans 16-01 + 16-02 because T-5 consumes the `pool.status` enum whose data source is in pool.go. Captured as D-05c.

## Area 2 — C-3 EMBEDDING_MODEL_DEFAULT scope

**Question:** Implement the embedding surface, stub + boot WARN + doc-fix, or pure-Go embeddings?

**Options presented:**
1. Stub + boot WARN + doc-fix
2. Implement via sidecar shim (brief §3.4 Option C)
3. Pure-Go embeddings via existing libs (brief §3.4 Option B)

**Decision:** Option 1 — stub + WARN + doc-fix. Captured as D-03.

**Rationale:** `docs/briefs/go_port_brief.md` §3.4 explicitly defers the embeddings problem. Reliability hardening milestone is the wrong place to take on a new surface area. Success criterion #13 only allows "implement OR WARN+doc-fix" — removing the var would still leave CLAUDE.md backward-compat claiming it.

## Area 3 — H-4 body-read deadline

**Question:** Config knob and scope for the bounded body-read deadline?

**Options presented:**
1. New env `HTTP_BODY_READ_TIMEOUT_SEC` default 30s, chat-body POSTs only
2. Fixed 30s constant, no env var
3. Env var applied to ALL POST handlers including admin

**Decision:** Option 1 — `HTTP_BODY_READ_TIMEOUT_SEC=30` default, chat-body POSTs only. Captured as D-04.

**Notes:** Diverges from Phase 15 D-04's `_MS` convention (`POOL_ACQUIRE_TIMEOUT_MS`) — accepted, because the existing config-loader idiom for timeouts is `_SEC` (`STREAM_IDLE_TIMEOUT_SEC`, `StreamIdleTimeoutSec` in admin). Phase 15 was the outlier; not propagating. Documented as D-04 caveat.

## Area 4 — T-5 'degraded' semantics

**Question 1 — API shape:** How should the gateway expose 'pool degraded' to the tray?

**Options presented:**
1. Extend `/health`, add `pool.status` enum
2. New `/health/pool` endpoint with rich snapshot
3. Add `/health/pool` with summarized status only

**Decision:** Option 1 — extend existing `/health`. Captured as D-05.

**Question 2 — Degraded rule:** What rule defines `pool.status == "degraded"` server-side?

**Options presented:**
1. All slots busy AND none made progress in last 30s
2. All slots busy for > 60s (simpler, false-positive on long generations)
3. Defer to env var (`POOL_DEGRADED_STALL_SEC`)

**Decision:** Option 1 — per-slot `last_progress_at` tracking, 30s threshold. Captured as D-05a.

**Notes:** Threshold hardcoded as constant in `health.go`, not a new env var — env-surface restraint matches the C-1/C-3 conversation. If operators report false-positives, the env var becomes a future increment (queued in deferred ideas).

## Claude's Discretion (no further input needed)

Captured in CONTEXT.md final section. Highlights:
- Wave layout: 16-01, 16-02, 16-03, 16-05 in Wave 1; 16-04 in Wave 2 (depends on 16-01 + 16-02)
- C-1 fail-on-first error posture (matches existing `STREAM_IDLE_TIMEOUT_SEC` pattern)
- `POOL_SIZE` upper bound: 256
- T-7 defaults: `--max-mb=512`, `--timeout=180`
- O-1 first-park WARN, subsequent parks Debug (anti-spam)
- T-4 cross-platform parity (apply non-blocking pattern to darwin too for symmetry)

## Deferred Ideas

See CONTEXT.md `<deferred>` section. None of these block Phase 16; queued for v1.10+ planning.
