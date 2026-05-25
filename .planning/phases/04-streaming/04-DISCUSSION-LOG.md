# Phase 4: Streaming - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-24
**Phase:** 4-Streaming
**Areas discussed:** Ollama NDJSON shape, Disconnect-cancel mechanism, Stream code organization, Verification approach

---

## Ollama NDJSON shape

### NDJSON delta cadence

| Option | Description | Selected |
|--------|-------------|----------|
| One line per chunk | One NDJSON object per canonical.Chunk — same cadence as the OpenAI/Anthropic SSE emitters; faithful to Node's per-session/update emission | ✓ |
| Coalesce on a timer | Buffer chunks, flush every N ms; adds ticker + buffer, diverges from siblings and Node parity | |

### Thinking during stream

| Option | Description | Selected |
|--------|-------------|----------|
| Emit message.thinking deltas | Partials carry message.thinking for ChunkKindThought, mirroring non-stream chatResponseToWire (Phase 3.1 D-02) | ✓ |
| Drop thoughts mid-stream | Stream text deltas only; inconsistent with non-stream response | |

### Mid-stream error handling

| Option | Description | Selected |
|--------|-------------|----------|
| Truncate + debug-log | Stop writing + debug-log on terminal error after headers, like openai/sse.go finalizeSSE (Pitfall 3) | ✓ |
| Best-effort error done line | {done:true,error:...} line — non-standard for Ollama NDJSON, risks echoing internal error text (T-02-33) | |

**User's choice:** All recommended options.
**Notes:** Grounded in `docs/reference/acp_server_node_reference.md` (Ollama streams by default) and the existing `render.go` structs/helpers.

---

## Disconnect-cancel mechanism (STRM-04)

### Where the cancel responsibility lives

| Option | Description | Selected |
|--------|-------------|----------|
| Engine-owned watchdog | engine.Run spawns a ctx-bound watchdog; calls ACP.Cancel(sid) on early ctx termination; adapters need zero new code | ✓ |
| Run.Close() defer in each adapter | engine exposes Run.Close(); each emitter defers it — explicit but three places to keep correct | |
| Per-adapter cancel call | Each emitter calls engine.Cancel(sid) on ctx.Done — most explicit, duplicates policy across 3 surfaces | |

### What triggers the cancel

| Option | Description | Selected |
|--------|-------------|----------|
| Both ctx.Done and write errors | ctx cancellation AND a failed frame write both signal the client is gone | ✓ |
| ctx.Done only | Treat only r.Context() cancellation as disconnect | |

**User's choice:** All recommended options.
**Notes:** Realizes the core-value "one place to enforce policy" and cashes the Phase 3.1 D-06 / Deferred-Ideas "watchdog" promise. Write-error path cancels the request ctx so the single watchdog stays authoritative (CONTEXT D-07). Goroutine-leak teardown is the load-bearing constraint (goleak gate).

---

## Stream code organization

| Option | Description | Selected |
|--------|-------------|----------|
| Three independent emitters | Add ollama/ndjson.go modeled on sse.go siblings; leave the two shipped SSE emitters alone | ✓ |
| Shared stream driver + renderers | Extract StreamPump + Renderer interface — leaky across 3 divergent wire shapes, rewrites working code | |

**User's choice:** Three independent emitters (recommended).
**Notes:** The engine watchdog (D-06) already centralizes the cancel *policy*, so the duplicated select-loop skeleton is low-stakes. Shared driver deferred as speculative DRY.

---

## Verification approach

### Overall verification mode

| Option | Description | Selected |
|--------|-------------|----------|
| Automated E2E per surface | Extend tests/e2e/ with real-binary streaming round-trips for all three surfaces + stream:false regression; flip the Ollama downgrade guard | ✓ |
| Automated E2E + HUMAN-UAT | Same plus a manual disconnect/streaming HUMAN-UAT gate | |

### Proving session/cancel fires

| Option | Description | Selected |
|--------|-------------|----------|
| Fake-ACP frame assert + real-binary smoke | Assert session/cancel notification on the fake-ACP wire after mid-stream ctx-cancel; real-binary E2E for slot survival + no leak | ✓ |
| Fake-ACP frame assertion only | Deterministic but no real-binary disconnect exercise | |
| Real-kiro integration only | Closest to prod but can't assert the exact frame; gated on kiro-cli | |

**User's choice:** All recommended options.
**Notes:** Consistent with the standing "automate UAT per surface" preference. The deterministic STRM-04 unit lives on the fake-ACP wire (observable); real-kiro can't expose its wire to assert the frame.

## Claude's Discretion

- Watchdog teardown mechanism (completion channel / context.AfterFunc / Run-lifecycle hook), provided goleak passes.
- Whether the watchdog lives in engine.Run or an adjacent helper.
- Ollama file split (ndjson.go + handlers.go edits; test file placement).
- ctx.Canceled vs ctx.DeadlineExceeded log differentiation (both cancel).
- Fake-ACP session/cancel assertion mechanics (channel signal vs recorded-frames slice).

## Deferred Ideas

- Full slot-release-on-cancel + POOL_SIZE > 1 — Phase 5.
- Tool-call streaming (input_json_delta, coerceToolCall) — Phase 6.
- Shared stream-driver abstraction — rejected for now (D-08); revisit on a 4th surface.
- Real streaming token counts — Phase 7+.
- signature_delta for Anthropic thinking — carried from Phase 3.1.
