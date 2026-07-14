# Legacy JS gateway → Go port: parity roadmap

**Date:** 2026-07-14
**Status:** Living roadmap (tracking doc)
**Source of truth for the gaps:** the fix-parity review of `loop_24`'s
`acp_server/acp-server-ollama.js` (July 2026 ACP fixes) against this repo.

## Purpose

Keep the Go gateway at parity with fixes/improvements landing on the legacy
Node gateway (`gitlab…/loop_24`, `acp_server/acp-server-ollama.js`). The Go
`CoerceToolCall` was ported from a **pre-July-2026** snapshot of the Node
reference, so several robustness fixes made after that snapshot never crossed.
This roadmap tracks the catch-up plus the observability work to get better usage
insight.

## Principles

- **Validate before building.** Whether the tool-call quirks bite the Go
  gateway depends on kiro's actual output shape *to this gateway* (native ACP
  `tool_call` notifications vs free-text `{"tool_call":…}` JSON). Observe first.
- **Env-var parity.** New knobs reuse the Node names exactly (`CTX_RECYCLE_PCT`,
  etc.) per the CLAUDE.md backward-compat constraint.
- **Prove parity, don't claim it.** The Node `acp_server/test-v1-messages.mjs`
  13-case suite becomes ported Go tests — the executable parity contract.
- **Per-item spec cycle.** Each product-code item gets its own
  `docs/superpowers/specs/…-design.md` and is implemented TDD, the way the
  ping-suspend feature was.

## Tracks

### Track 0 — Validation harness (Co-Worker skill) — *parallel, not blocking*
Build a **Co-Worker skill** that drives real tool round-trips through the Go
gateway and captures kiro's wire behavior:
- Claude Code → `/v1/messages` with a tool; a LangFlow-style tool → `/api/chat`.
- Capture: native ACP `tool_call` notifications vs free-text `{"tool_call":…}`
  JSON; whether tool **names** match the client's declared tools or kiro's
  built-ins; whether `session/update` carries `contextUsagePercentage`.
- **Output:** findings that scope Track 3 and confirm the Track 2 signal.
- Lives in the Co-Worker desktop app as a reusable skill (not throwaway).

### Track 1 — Resilient model discovery — **STARTING FIRST** — mirrors JS `6bbd0c2`
Model catalog is captured **one-shot** from slot-0's `NewSession` during
`pool.Warmup` (`internal/pool/pool.go:242-252`); `Models()` only ever returns
that snapshot. A cold/slow kiro that returns an empty `availableModels` leaves
`p.models` `nil` and `/api/tags`, `/v1/models`, `/api/show` stuck at synthetic
`"auto"` **until process restart**.
- Retry-with-backoff around the initial catalog capture.
- **Lazy self-heal** when the catalog is empty on a catalog read (singleflight-
  guarded so concurrent reads don't stampede kiro).
- Design decision (in the spec): keep Warmup fail-fast on a cold kiro, or boot
  degraded + self-heal (Node chose degraded).
- **Spec:** `docs/superpowers/specs/2026-07-14-model-discovery-resilience-design.md`

### Track 2 — Context-utilization capture + proactive recycle — mirrors JS `b72d6db`
Go reads **no** context/utilization signal (`contextUsagePercentage` /
`meteringUsage` / `turnDurationMs` appear nowhere in `internal/`); recycling is
idle-TTL only (`internal/session/reaper.go`). Long stateful conversations let
kiro's internal compaction degrade instruction-following with no intervention.
- Parse `contextUsagePercentage` (+ optionally metering/turn) from
  `session/update` in `internal/acp/translate.go`; track per-session `lastCtxPct`.
- Proactive recycle of the kiro session at `CTX_RECYCLE_PCT` (new env, default
  `0.8`) before the next request; confirm Go's transcript-resend model matches
  the Node assumption in the spec.
- Feeds Track 4 metrics (`ctx_pct`, `sessions_recycled`, credits, turn ms).

### Track 3 — Tool-call robustness — *scope set by Track 0* — mirrors JS `519c066` + `14bc655`
Go's free-text fallback (`internal/engine/coerce.go`) is strictly narrower than
the Node one: it only coerces when the *entire* assistant text is one bare JSON
arg-object, scored by key-overlap. Gaps vs Node:
- No embedded `{"tool_call":{…}}` balanced-brace scan (Node `extractToolCallObjects`).
- No truncated-JSON (missing trailing brace) repair (Node `14bc655`).
- No invented-name remap on the wrapper shape (Node `519c066`).
- Kiro-native tool calls become `[tool: <name>]` **narration** on OpenAI/Ollama
  (`internal/engine/collect.go:137-144`) instead of structured `tool_calls`; and
  the name is taken from kiro's `body.Kind` with **no remap** against the
  client's declared tools (`internal/acp/translate.go:285`).
- Approach chosen by Track 0's findings: harden the free-text extractor, and/or
  add a kiro→client tool-name reconciliation layer, and surface native calls as
  structured `tool_calls` when the client supplied tools.

### Track 4 — Prometheus metrics endpoint — **new** — usage & ops insight
Expose a `/metrics` endpoint (Prometheus text format) so metrics can be scraped
into a timeseries DB for usage insight.
- Library: `prometheus/client_golang` (pure Go — no cgo; compatible with the
  single-static-binary constraint).
- Initial metrics from signals that already exist: pool size/alive/busy,
  `spawn_failing`, slot respawns, request counts/latency/status per surface,
  active stateful sessions, ping escalations/suspend-skips.
- Grows with Track 2: per-turn `contextUsagePercentage`, kiro credits/turn,
  `sessions_recycled`.
- Design decisions (spec): endpoint auth/allowlist posture (metrics can leak
  usage shape), metric names/labels, cardinality guards.
- **Spec:** `docs/superpowers/specs/…-prometheus-metrics-design.md` (TBD)

### Track 5 — Parity as a process — *ongoing*
- Port `acp_server/test-v1-messages.mjs` → Go integration tests (parity contract).
- Standing triage of new `loop_24` `acp_server` commits for Go parity each release.

## Sequencing

1. **Track 1 (model discovery)** — first; smallest fully-understood confirmed gap.
2. **Track 0 (Co-Worker validation skill)** — in parallel; unblocks Track 3 scope.
3. **Track 4 (Prometheus `/metrics`)** — foundational observability; can start
   with existing signals and grow.
4. **Track 2 (context recycle)** — pairs with Track 4 for the kiro-utilization metrics.
5. **Track 3 (tool-call robustness)** — after Track 0 findings.
6. **Track 5 (regression suite + triage)** — folded in as each track lands.

## Not applicable / already at parity

- **Anthropic `/v1/messages`** (Node `6d2183c`): Go has a native Anthropic
  surface with `tools`/`input_schema`/`tool_choice` — parity.
- **fastembed native-tokenizer crash** (Node `b72d6db` part A): structurally
  impossible in pure-Go/no-cgo Go. The *degraded-mode* idea (hide embed models,
  503 instead of crash) is worth mirroring **if/when** Go implements `/api/embed`.
- Bootstrap / Teams / litellm-pin / Hyper-V / SSH-keygen fixes: Node-deployment-
  specific.
