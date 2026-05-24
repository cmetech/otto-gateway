# Phase 3: OpenAI Surface - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-24
**Phase:** 3-OpenAI Surface
**Areas discussed:** /v1 co-mount strategy, OpenAI SSE timing, models+completions scope, model identity

> The user asked for a recommendation and rationale on each gray area rather
> than picking blind; Claude recommended an option for each and the user
> confirmed three and overrode one (SSE timing).

---

## /v1 co-mount strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Generic Surfaces list | Refactor server.Config to Surfaces []SurfaceMount; group by prefix so OpenAI + Anthropic register onto one shared /v1 router. Solves the chi double-mount trap, matches 3.1 D-17, seam-now preference. | ✓ |
| Minimal shared /v1 block | One bespoke combined /v1 block; hits chi "can't Mount two routers at /" trap, needs workaround + another bespoke branch per future surface. | |

**User's choice:** Generic Surfaces list (Recommended).
**Notes:** Claude flagged that the "minimal" option is not actually minimal — chi panics on both duplicate Route prefixes and duplicate `/` mounts. Generic list also aligns with the user's documented seam-now preference and the Phase 3.1 D-17 forward plan. → CONTEXT D-01.

---

## OpenAI SSE timing

| Option | Description | Selected |
|--------|-------------|----------|
| Non-streaming only (Recommended by Claude) | Mirror Phase 2 Ollama; SSE deferred to Phase 4 per roadmap. OpenAI SDK defaults to stream=false so basic Pi works non-streaming. | |
| SSE day-one | Ship OpenAI SSE now via engine.Run; Phase 4 just refactors the shared path. | ✓ |

**User's choice:** SSE day-one (override of Claude's non-streaming recommendation).
**Notes:** User overrode toward shipping streaming in the surface phase. Consistent with the Anthropic 3.1 day-one SSE call. Narrows Phase 4 to Ollama NDJSON + disconnect cancellation + one-channel ratification. → CONTEXT D-02 + deferred note + [[feedback_streaming_day_one_per_surface]].

---

## models + completions scope

| Option | Description | Selected |
|--------|-------------|----------|
| Models full, completions minimal | /v1/models mirrors /api/tags; /v1/completions = thin prompt→user-message shim, no legacy advanced params. | ✓ |
| Full both endpoints | Implement /v1/completions advanced params (logprobs/echo/suffix/best_of) too. | |

**User's choice:** Models full, completions minimal (Recommended).
**Notes:** /v1/completions is legacy/deprecated; advanced params can't be honored by kiro-cli and Pi uses /chat/completions. → CONTEXT D-03.

---

## model identity

| Option | Description | Selected |
|--------|-------------|----------|
| Mirror /api/tags catalog | Expose pool ModelCatalog (same set as /api/tags per SC3); inbound auto/empty skips SetModel. | ✓ |
| Static synthetic list | Fixed id like "otto"; conflicts with SC3. | |

**User's choice:** Mirror /api/tags catalog (Recommended).
**Notes:** Essentially forced by Phase 3 success criterion 3 ("same underlying set"). → CONTEXT D-04.

---

## Claude's Discretion

- Composition mechanics for two adapters sharing one prefix-router (RegisterRoutes vs adapted ProtectedRouter vs distinct-subpath mounts).
- OpenAI adapter file split; message id generation strategy; whether /v1/completions honors stream; SSE keepalive comment frame (likely none); SSE test approach.

## Deferred Ideas

- Ollama NDJSON streaming + explicit session/cancel disconnect — Phase 4 (now narrowed scope).
- Tool dispatch / coerceToolCall / OpenAI JSON-string tool-call rendering — Phase 6.
- /v1/completions advanced params — accept-and-ignore now, implement on demand.
- /v1/embeddings — Phase 7. Real token counting in usage — Phase 7+.
- SurfaceMount list extension for Phase 5 pool/session mounts (DELETE /v1/sessions/:id).
