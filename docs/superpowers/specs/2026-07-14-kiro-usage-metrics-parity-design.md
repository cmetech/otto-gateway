# Kiro usage metrics parity — design

**Date:** 2026-07-14
**Status:** Approved (design) — ready to implement.
**Scope:** One implementation plan (multi-commit). Extends Track 4 (metrics) and
subsumes Track 2's context capture. Roadmap:
[legacy-gateway parity](../2026-07-14-legacy-gateway-parity-roadmap.md).
**Goal:** bring the Go `/metrics` to full DATA parity with the loop24 Node
gateway's `stats` object — same intent/usage for kiro (names may differ). No
Teams delivery (a Co-Worker-side agent scrapes `/metrics` → Grafana Cloud).

## Confirmed kiro wire shape (live capture, 2026-07-14)

kiro emits utilization on the **`_kiro.dev/metadata`** notification — Go's
`acp.handleNotification` has NO case for it today, so it is silently dropped.
Raw params captured from a live kiro session:

```json
{"sessionId":"…","contextUsagePercentage":4.897}                    // streams continuously, mid-turn
{"sessionId":"…","contextUsagePercentage":1.567,
 "meteringUsage":[{"value":0.0287,"unit":"credit","unitPlural":"credits"}],
 "turnDurationMs":2012}                                             // on turn completion
```

- `contextUsagePercentage` — number on a **0–100 percent** scale (NOT 0..1).
  Live values were 4.897, 1.567 (early, short conversation). Streams multiple
  times per turn; the turn-completion frame also carries metering + duration.
- `meteringUsage` — array of `{value, unit, unitPlural}`; sum `value` where
  `unit == "credit" || unitPlural == "credits"` = real credits for the turn.
- `turnDurationMs` — number, the turn wall-time.

MCP init rides on **`_kiro.dev/mcp/server_initialized`** /
**`_kiro.dev/mcp/server_init_failure`**, params `{ serverName }` (Node
acp-server-ollama.js:375-382). Not seen in the capture (no MCP servers wired),
but the field names are confirmed from the Node reference.

### ⚠️ Percent-scale correction (decided)

kiro's value is 0–100. The Node code compares it to `CTX_RECYCLE_PCT` default
`0.8` (0..1 semantics) — on a 0–100 value that recycles at **0.8%**, almost
every turn (a latent Node bug). **We use percent semantics:**
`gw_kiro_context_usage_percent` is 0–100, and `CTX_RECYCLE_PCT` default is **80**
(percent). Document the divergence from the Node default value.

## Decisions (locked with the human)

- **Full parity in one build** (kiro metadata capture + MCP counters + model/
  flow/client attribution + sessions_created + Track 2 context-recycle off the
  same signal). **Skip** the rough char/4 token estimates (real credits are the
  better cost signal).
- Context %: **percent (0–100)**, recycle threshold default **80**.
- Delivery: none in-gateway — expose `/metrics`; a Co-Worker agent scrapes →
  Grafana Cloud.

## Metric set (gw_ prefix, all carry gateway_id)

Kiro usage (from `_kiro.dev/metadata`):
- `gw_kiro_credits_total` — counter (sum of `meteringUsage` credits).
- `gw_kiro_turns_total` — counter (turns that reported metering).
- `gw_kiro_turn_duration_seconds` — histogram (`turnDurationMs`/1000).
- `gw_kiro_context_usage_percent` — histogram, observed once per completed turn
  (0–100). Grafana derives avg/max/p95 (covers Node's last + peak_context_pct).

Kiro MCP (from `_kiro.dev/mcp/*`):
- `gw_kiro_mcp_server_init_total{server,result}` — counter, result=ok|fail.

Attribution (request/session layer):
- `gw_model_requests_total{model}` — counter (model from the canonical request).
- `gw_llm_requests_total{surface,skill}` — EXISTS; extend: read `X-Flow-Name`
  as a skill alias (LangFlow flows), and add a bounded `client` label from
  `X-GW-Client`. (Covers Node flows{} + callers{}.)
- `gw_sessions_created_total` — counter (session.Registry.createEntry).
- `gw_sessions_recycled_total` — counter (Track 2 recycle).

Already parity-or-better (no work): `gw_http_requests_total{route,status}`
(requests/errors/by-endpoint), `gw_http_request_duration_seconds` (latency),
`gw_pool_*` + `max_over_time` (pool_size/peak_busy), `process_start_time_seconds`
(server_started), `gw_sessions_active`.

## Architecture

Kiro metadata + MCP clients live in BOTH the pool (stateless requests) AND the
session registry (stateful X-Session-Id conversations, which spawn their own acp
clients via `session.Registry.createEntry` — separate from the pool). So the
capture must aggregate from both.

1. **acp layer** — new cases in `acp.handleNotification` for `_kiro.dev/metadata`
   and `_kiro.dev/mcp/*`, parsed into new optional `acp.Config` hooks (same
   pattern as the ping hooks added in Track 4b):
   - `OnTurnMeter func(credits float64, turnMs int64)` — fired when a metadata
     frame carries `meteringUsage` (turn complete).
   - `OnContextPct func(pct float64)` — fired on any `contextUsagePercentage`.
   - `OnMCPInit func(server string, ok bool)` — fired on the two MCP methods.
   Define matching param structs alongside `sessionUpdateParams` in translate.go.
2. **Shared recorder** — a small metrics recorder passed into BOTH `pool.Config`
   and `session.Config`; pool (`acpSlotConfig`) and registry (`createEntry`'s
   acp.Config) set the acp hooks to forward to it. The recorder increments the
   Prometheus counters/histograms directly OR feeds counters the metrics
   pull-collector reads (choose to match the Track 4 pull pattern; direct
   CounterVec/Histogram registered in metrics.New is simplest for per-event).
3. **Track 2 recycle** — the `OnContextPct` hook on a REGISTRY client also
   updates that session `Entry.lastCtxPct`; `session.Registry.Get`'s alive+ready
   branch recycles (Cancel/Close old client, delete, lazy-recreate via the
   Dead-path) when `lastCtxPct >= CTX_RECYCLE_PCT` (percent). `0` disables.
   `sessions_recycled` counter increments here. (Per the Track 2 spec —
   transcript resend makes recycle safe.)
4. **Attribution** — `gw_model_requests_total` recorded where the model is known
   (adapter/engine after decode); `X-Flow-Name`/`X-GW-Client` folded into the
   existing metrics middleware LLM path; `gw_sessions_created_total` in
   `createEntry`.

## Tests (TDD)

- acp: `_kiro.dev/metadata` with contextUsagePercentage-only fires OnContextPct;
  with meteringUsage+turnDurationMs fires OnTurnMeter (credits summed over
  `unit==credit`); `_kiro.dev/mcp/*` fires OnMCPInit(server, ok/fail). Malformed
  params are dropped, not fatal.
- metrics: gw_kiro_credits_total / turns_total / turn_duration_seconds /
  context_usage_percent surface; mcp_server_init_total{server,result};
  model/flow/client labels; sessions_created/recycled counters. All carry
  gateway_id.
- session: Get recycles at/above 80% context, not below; recycled entry starts
  at 0; CTX_RECYCLE_PCT=0 disables. sessions_created increments on create.
- Verify live: drive a long stateful conversation; confirm credits/turns/ctx%
  climb, a recycle fires near 80%, and all series scrape.

## Files touched (anticipated)

- `internal/acp/translate.go` — metadata + mcp param structs.
- `internal/acp/client.go` — handleNotification cases + Config hooks.
- `internal/pool/pool.go` (`acpSlotConfig`) + `internal/pool/config.go` — forward hooks.
- `internal/session/registry.go` (`createEntry`, `Get`) + `internal/session/config.go`
  — hooks, lastCtxPct, recycle, sessions_created/recycled, CTX_RECYCLE_PCT.
- `internal/config/config.go` — `CTX_RECYCLE_PCT` (float percent, default 80, 0 disables).
- `internal/metrics/*` — new kiro + attribution metrics + recorder wiring.
- adapter/engine — `gw_model_requests_total`; `X-Flow-Name`/`X-GW-Client` in the LLM path.
- `cmd/otto-gateway/main.go` — wire the shared recorder into pool + session.
- Tests across all of the above.

## Verification gates

`go build ./...`; `go test ./...`; gofumpt; `go vet`; golangci-lint on touched
packages; `CGO_ENABLED=0 go build ./cmd/otto-gateway`; `GOOS=linux/windows`;
live scrape of the new `gw_kiro_*` families + a recycle at threshold.
