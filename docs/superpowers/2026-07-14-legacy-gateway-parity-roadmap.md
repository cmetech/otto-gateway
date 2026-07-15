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

### Track 0 — Tool-call wire capture — 📝 **SPEC'D** — *scopes Track 3*
Capture kiro's tool-call wire behavior *through the real gateway path* so Track 3
is scoped to what kiro actually does. **Narrowed:** the context-signal half of the
original Track 0 is **done** (2026-07-14 live capture → Track 2 shipped), so this
is now tool-calls only.

**Spec:** `docs/superpowers/specs/2026-07-14-track0-toolcall-capture-design.md`.
Decisions: gateway raw-frame capture via a bounded, `ACP_CAPTURE`-gated admin
ring-buffer endpoint (`GET /admin/acp-capture`, reachable over HTTP so a remote
desktop skill can read it); a repo-local real-kiro harness drives a tool
round-trip on each surface and classifies transport (native ACP `tool_call` vs
free-text `{"tool_call":…}`), tool-name fidelity, and JSON robustness → the Track
3 scoping report. The Co-Worker desktop skill later wraps the same procedure.
- Claude Code → `/v1/messages` with a tool; LangFlow-style → `/api/chat`; OpenAI too.
- **Output:** `docs/reviews/2026-07-14-track0-toolcall-findings.md` — scopes Track 3.

### Track 1 — Resilient model discovery — ✅ **DONE** (`393f043`) — mirrors JS `6bbd0c2`
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

### Track 2 — Context-utilization capture + proactive recycle — ✅ **DONE** (`0b5ff33` recycle + `af376f7` capture; wired `0de5b2f`) — folded into kiro-usage-parity — mirrors JS `b72d6db`
Wire confirmed (2026-07-14 live capture) **and now implemented**: kiro emits
`contextUsagePercentage` (0–100 percent), `meteringUsage[]` credits, and
`turnDurationMs` on the `_kiro.dev/metadata` notification — Go previously dropped
it. Delivered as part of the **kiro usage metrics parity** build — see
`docs/superpowers/specs/2026-07-14-kiro-usage-metrics-parity-design.md`.

**Spec:** `docs/superpowers/specs/2026-07-14-context-recycle-design.md`.

**Delivered:**
- `internal/acp/client.go` + `translate.go` — new `_kiro.dev/metadata` case
  parses `contextUsagePercentage` / `meteringUsage` / `turnDurationMs` into the
  `OnContextPct` / `OnTurnMeter` Config hooks.
- `internal/session` — per-session `Entry.lastCtxPct` (atomic), and proactive
  recycle in `Registry.Get`'s alive+ready branch: Cancel/Close + delete + lazy
  re-create via the existing Dead-path when `lastCtxPct >= CTX_RECYCLE_PCT`.
  Verified live (recycle fired at `ctx_pct=1.566` with threshold 1; the fresh
  session re-primed from the resent transcript and answered coherently).
- `internal/config` — `CTX_RECYCLE_PCT` env (Node-parity **name**).

> **⚠️ Default-value divergence (decided).** kiro's `contextUsagePercentage` is
> a **0–100 percent**, so `CTX_RECYCLE_PCT` uses **percent** semantics with
> default **80** — NOT the Node `0.8` (0..1-scaled) default, which mis-fires
> against current kiro (it would recycle at 0.8% on almost every turn). Env name
> stays Node-parity; the default value intentionally diverges. `0` disables.

- Feeds Track 4 metrics: `gw_kiro_context_usage_percent`, `gw_sessions_recycled_total`,
  `gw_kiro_credits_total`, `gw_kiro_turn_duration_seconds`.

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

**⚠️ Track 0 findings (corrected 2026-07-15) — the real blocker is upstream.**
The live capture showed Go elicits **zero** tool_calls from kiro. Root cause
(verified against `loop_24/acp_server/acp-server-ollama.js`, which works): the
free-text robustness gaps above are real but *downstream* of a missing
elicitation apparatus. JS prompt-embeds tools like Go, but also (1) uses a
strict function-calling prompt + explicit `{"tool_call":…}` JSON protocol, and
(2) **rejects** kiro's `session/request_permission` when the caller supplied
tools (`denyKiroTools`) so the model emits tool_call JSON instead of using its
own built-in tools — **Go currently auto-grants** that permission
(`internal/acp/client.go:1186`), the exact inversion. So Track 3 splits:
  - **Track 3a (NEW, do first):** port the elicitation apparatus — permission
    *denial* when caller tools present, the strict tool-call prompt, and the
    corrective-nudge/circuit-breaker. Without this kiro never emits a tool_call.
  - **Track 3b:** the free-text robustness items above (extractor/repair/remap/
    structured surfacing), sized against real kiro output *after* 3a lands.
  See `docs/reviews/2026-07-14-track0-toolcall-findings.md` for the corrected
  analysis. (The earlier "needs an `mcpServers` capability-negotiation spike"
  conclusion is withdrawn — prompt-embedding is the correct channel.)

### Track 4 — Prometheus metrics endpoint — ✅ **DONE** (4a `347a2b7`, 4b + identity `c4fd2a6`, skill attribution `4faab1c`) — usage & ops insight
Added beyond the original spec: **gateway_id** constant label on every series
(GW_ID env → persisted ULID) + `gw_build_info` for fleet grouping; **4b event
counters** (respawns, ping escalations/suspend-skips, session reaps); and
**per-skill LLM attribution** via the API-compliant `X-GW-Skill` header →
`gw_llm_requests_total{surface,skill}` (sanitized + cardinality-capped) plus
skill/client audit-log fields.

**Spec:** `docs/superpowers/specs/2026-07-14-prometheus-metrics-design.md`.
Decisions: `prometheus/client_golang` (pure-Go, cgo-free preserved); `GET /metrics`
behind `auth.IPAllowlist` (passthrough when `ALLOWED_IPS` unset); `gw_` metric
prefix. Phased 4a (endpoint + request middleware + pool/session pull-collector +
free go_/process_ metrics) → 4b (event counters at today's log-only sites).

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
