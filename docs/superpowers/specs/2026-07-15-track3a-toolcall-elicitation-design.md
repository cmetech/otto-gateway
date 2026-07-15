# Track 3a — kiro tool-call elicitation apparatus — design

**Date:** 2026-07-15
**Status:** Approved (design) — ready to implement.
**Scope:** One implementation plan (multi-commit, TDD). Track 3a of the
[legacy-gateway parity roadmap](../2026-07-14-legacy-gateway-parity-roadmap.md).
**Goal:** make kiro actually *emit tool calls* through the Go gateway, at parity
with the legacy JS gateway — by **denying** kiro's built-in-tool permission
requests when the caller supplied tools, replacing the weak `[Available tools]`
prompt with a strict function-calling prompt, and adding a `MAX_TOOL_DENIALS`
circuit breaker. Coercing the emitted `{"tool_call":…}` JSON into structured
`tool_calls` is **Track 3b** (out of scope here).

## Why

The Track 0 capture (corrected 2026-07-15,
`docs/reviews/2026-07-14-track0-toolcall-findings.md`) showed the Go gateway
elicits **zero** tool calls from kiro. Root cause, verified against the working
JS gateway (`loop_24/acp_server/acp-server-ollama.js`): both gateways
prompt-embed the caller's tools, but JS drives kiro to emit tool calls with an
apparatus Go lacks — and on one point Go does the exact opposite:

1. **Permission denial (the key inversion).** When the caller supplied tools,
   JS **rejects** kiro's `session/request_permission` (`granted:false`,
   `reject_always`) so kiro cannot complete the task with its own built-in tools
   and must emit a `{"tool_call":…}` block for the caller to execute
   (`acp-server-ollama.js:314-338`). **Go auto-grants every permission**
   (`internal/acp/client.go:1186`, `{optionId:"allow_always", granted:true}`).
2. **Strict function-calling prompt.** JS frames kiro as a function-caller for
   an EXTERNAL system, forbids its built-in tools, and demands
   ` ```json {"tool_call": {"name","arguments"}} ``` ` blocks with 6 rules
   (`~794-820`). Go emits a bare `"[Available tools]\nEmit a tool_call ACP
   notification…"` one-liner (`internal/engine/build_acp.go:105-109`).
3. **Circuit breaker.** JS resets a per-turn denial counter each prompt and
   cancels the turn after `MAX_TOOL_DENIALS` (default 4) denials
   (`:123`, `:448`, `:330`). Go has none.
4. **Corrective nudge + free-text coercion** → **Track 3b** (not 3a).

The prompt text (2) and the denial behavior (1) are a *matched pair* — the
prompt tells the model "your built-in tools will be rejected," which is only
true once (1) ships. They must land together.

## Decisions (design)

### D1 — Per-turn "deny built-in tools" signal via `context.Context`

The permission handler runs on the **readLoop** goroutine; `Prompt` runs on the
caller's goroutine (engine/pool/session). The signal ("this turn's caller
supplied tools → deny kiro's built-in tools") originates in `engine.Run` (which
has `req.Tools`) and must reach `acp.Client`'s permission handler.

**Chosen:** carry it as a **context value**. `engine.Run` sets
`ctx = acp.WithDenyBuiltinTools(ctx, len(req.Tools) > 0)` before calling
`ACP.Prompt(ctx, …)`. The value rides the *existing* `ctx` parameter unchanged
through every pass-through layer (`pool.Pool.Prompt`, `session.Entry.Prompt`,
`acpClientAdapter.Prompt`) to `acp.Client.Prompt`, which reads it once at turn
start and stores it on the Stream.

**Rejected:** extending the `Prompt` signature (or adding an interface method) —
the map (`.superpowers/sdd/track3a-go-map.md` §4) shows that touches **7
production interfaces/structs + 12 test fakes** for a single-hop pass-through
value. Context value is the right tool for request-scoped metadata crossing an
API boundary and costs **zero** interface/fake churn. It is a new idiom in this
repo (no prior `context.WithValue` for ACP), so the helpers are typed,
documented, and confined to package `acp`.

### D2 — Per-turn deny state lives on `acp.Stream` (Option A)

`Stream` is already the per-turn state carrier, already set under `c.streamMu`
in `Prompt` before the wire send, and already read under `c.streamMu` by the
`session/update` handler. Add two fields, guarded by the Stream's own
`stream.mu` (the mutex already protecting `stream.result`):

```go
denyBuiltinTools bool // set once at Prompt start from the ctx value (D1)
denialCount      int  // incremented by the permission handler this turn
```

This is inherently turn-scoped (a replaced/stale stream's fields are irrelevant
once `c.activeStream` no longer points at it) and needs **no** new Client-level
synchronization. Client-level `atomic` fields (Option B) are rejected: they
would leak across turns if the "one turn per Client" invariant ever broke, and
the denial counter must reset every turn (which per-Stream state gives for
free). The permission handler snapshots `c.activeStream` under `c.streamMu`
(existing idiom, client.go:1267-1269), then reads/mutates the Stream fields
under `stream.mu`.

### D3 — Permission handler: deny branch + circuit breaker

Rewrite the `session/request_permission` case (`client.go:1186`) to branch on
the active stream's `denyBuiltinTools`:

- **Deny path** (stream present and `denyBuiltinTools == true`):
  1. Parse the enriched `permissionParams` (D4).
  2. Pick the best **reject** option (D4 `pickRejectOption`): prefer an option
     whose `optionId`+`kind` matches `/reject.*always|always.*reject/i`, else
     any `/reject/i`; fall back to the literal `"reject_always"`.
  3. Respond on the original `frame.ID` with
     `{optionId:<reject>, granted:false}` (mirrors the existing id-echo/write
     path; only the Result map contents change).
  4. Increment the Stream's `denialCount` (under `stream.mu`); Debug-log the
     denied tool title and `denialCount/MaxToolDenials`.
  5. If `denialCount >= cfg.MaxToolDenials`, call `c.Cancel(stream.sessionID)`
     (best-effort `session/cancel` notification — safe from readLoop, goes via
     `writeCh`, not the direct-write path).
- **Grant path** (no active stream, or `denyBuiltinTools == false`): unchanged —
  respond `{optionId:"allow_always", granted:true}`. This preserves today's
  behavior for every non-tool request (Anthropic-without-tools, plain chat,
  etc.), so nothing regresses.

The id-echo mechanics, the direct-`framer.writeFrame` path, and the
no-id-drop/escalate guards are unchanged.

### D4 — Enrich `permissionParams` + `pickRejectOption`

`permissionParams` (`translate.go:151`) today only captures `RequestID`. Add:

```go
type permissionOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}
type permissionParams struct {
	RequestID string             `json:"requestId"`
	Options   []permissionOption `json:"options"`
	ToolCall  struct{ Title string `json:"title"` } `json:"toolCall"`
}
```

Plus a pure helper `pickRejectOption(opts []permissionOption) string` porting
the JS regex (reject-always → reject → `"reject_always"` literal fallback).
Field names are taken from the JS reference; **no live `session/request_permission`
frame has ever been captured in this repo** (D6 will capture one to confirm).

### D5 — Strict tool-call prompt (`build_acp.go`)

Replace the bare `[Available tools]` block with the JS-ported strict prompt: the
"function-caller for an EXTERNAL system / do NOT use your built-in tools /
output ` ```json {"tool_call": {"name","arguments"}} ``` ` blocks only" framing +
the 6 rules + the JSON tool catalog. Pure in-place rewrite of the
`len(req.Tools) > 0` branch; no new plumbing (it already has `req.Tools`). The
prompt asks for **JSON-in-text** (the shape Track 3b will coerce), matching JS —
not a native ACP notification.

### D6 — `MAX_TOOL_DENIALS` config (Node-parity env)

Follow the `CTX_RECYCLE_PCT` precedent exactly: `config.Config.MaxToolDenials
int` from `getEnvInt("MAX_TOOL_DENIALS", 4)` (must be `> 0`), threaded to
`pool.Config` + `session.Config` → `acp.Config.MaxToolDenials` (the threshold is
Client-lifetime, so a `Config` field is correct here — unlike the per-turn deny
flag). The permission handler compares the Stream's `denialCount` against
`c.cfg.MaxToolDenials`.

### D7 — Engine wires the signal

In `engine.Run`, immediately before `e.cfg.ACP.Prompt(ctx, sid, blocks)`, set
`ctx = acp.WithDenyBuiltinTools(ctx, len(req.Tools) > 0)`. Single line; the
value propagates through all pass-through layers.

## Out of scope (Track 3b)

- Coercing `{"tool_call":{…}}` wrapper JSON into structured `tool_calls`
  (`coerce.go` today only parses a bare `{args}` object — the wrapper scores
  zero in `pickBestTool` and no-ops).
- The corrective **nudge** re-prompt (re-prompt when a turn burned on denials
  with no tool_call) — needs coercion-detection ("did we get a tool_call?") and
  a retry loop above `Engine.Run`, which is 3b territory.
- Kiro→client tool-name reconciliation; truncated-JSON repair; structured
  `tool_calls` surfacing on OpenAI/Ollama.

## Verification

- Unit (per §Tests, TDD): `pickRejectOption`; enriched `permissionParams`
  unmarshal; Stream deny fields set from ctx; permission handler deny/grant
  branches + circuit-breaker cancel; strict prompt content; engine sets the ctx
  flag; config env parse/default/validation.
- Gates: `go build ./...`; `go test ./...`; `go test -race ./internal/acp/
  ./internal/engine/ ./internal/config/`; `go vet`; gofumpt-clean;
  `CGO_ENABLED=0 go build ./cmd/otto-gateway`; `GOOS=linux go build ./...`;
  `make arch-lint`.
- **Live (D6 harness):** run the Track 0 capture harness against the patched
  gateway with real kiro + a tool-declaring request; confirm the capture now
  shows (a) kiro issuing `session/request_permission` and the gateway **denying**
  it, and (b) kiro emitting `{"tool_call":…}` JSON in `agent_message_chunk`
  text. Record the real `session/request_permission` frame shape and reconcile
  `permissionParams`/`pickRejectOption` against it. Update the findings doc.

## Non-goals

- No Track 3b coercion. Success = kiro *emits* a tool-call signal (verified via
  capture), not that the client receives structured `tool_calls` end-to-end.
- No change to the grant path for tool-less requests (no regression).
- No new auth/permission model beyond the deny-vs-grant decision.

## Files touched (anticipated)

- `internal/acp/translate.go` — enrich `permissionParams` + `pickRejectOption`.
- `internal/acp/stream.go` — `denyBuiltinTools` + `denialCount` fields.
- `internal/acp/context.go` (new) — `WithDenyBuiltinTools` / `denyBuiltinToolsFromContext`.
- `internal/acp/client.go` — `Prompt` reads ctx→Stream; permission handler deny branch + breaker; `Config.MaxToolDenials`.
- `internal/engine/build_acp.go` — strict prompt.
- `internal/engine/engine.go` — set ctx flag before Prompt.
- `internal/config/config.go` — `MAX_TOOL_DENIALS`.
- `internal/pool/config.go`,`pool.go`; `internal/session/config.go`,`registry.go` — thread `MaxToolDenials` to `acp.Config`.
- `cmd/otto-gateway/main.go` — wire `cfg.MaxToolDenials` into pool/session Config.
- `tests/track3a_elicitation_test.go` (new, `//go:build kirolive`) — live verification.
- `docs/reviews/2026-07-14-track0-toolcall-findings.md` — record real permission-frame shape + 3a outcome.
