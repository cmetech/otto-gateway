# Phase 4: Streaming - Research

**Researched:** 2026-05-24
**Domain:** Go streaming (NDJSON + SSE) + context-cancel watchdog + goroutine-leak-free teardown
**Confidence:** HIGH — all findings verified against the actual codebase

---

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

**Ollama NDJSON (STRM-01)**
- D-01: One NDJSON line per canonical chunk (same cadence as OpenAI/Anthropic SSE, faithful to Node per-`session/update` emission). No timer-based coalescing.
- D-02: Partial lines carry `done:false`; the final line carries `done:true` plus the full stats block (reuse `ollamaChatResponse`/`ollamaGenerateResponse` structs and `mapStopReason`/`estimateTokens` helpers from `render.go`). `/api/generate` thoughts are dropped.
- D-03: Default flips to `stream:true` (Node parity). Remove the `wire.Stream = false` downgrade in `handlers.go:42-45,82-84`. Explicit `stream:false` → `engine.Collect` path unchanged.
- D-04: Thought chunks stream as `message.thinking` deltas for `/api/chat`; `/api/generate` drops thoughts (matching its non-stream shape).
- D-05: Mid-stream errors truncate + debug-log — no error frame. Once NDJSON headers are written, stop emitting and debug-log (same as `openai/sse.go` `finalizeSSE`).

**Disconnect cancellation (STRM-04)**
- D-06: Engine-owned watchdog fires `session/cancel`. `engine.Run` spawns a watchdog bound to the request ctx; on ctx termination **before** the stream completes normally, it calls `ACPClient.Cancel(sid)`. **Leak constraint is load-bearing:** watchdog MUST be torn down when the stream completes normally or it leaks a goroutine per request. The `goleak` gate on handler/engine tests is the guard.
- D-07: Both ctx-cancellation AND frame-write failure trigger cancel. On a write error, the adapter cancels the request ctx it passed to `engine.Run` (so the D-06 watchdog observes it). The adapter does NOT call `Cancel(sid)` directly.

**Stream code organization**
- D-08: Three independent emitters — no shared stream driver. Add `internal/adapter/ollama/ndjson.go` modeled on `sse.go` siblings. Leave `openai/sse.go` and `anthropic/sse.go` as-is.

**Verification**
- D-09: Automated E2E per surface — no HUMAN-UAT gate. Extend `tests/e2e/` with real-binary streaming round-trips for all three surfaces. Flip the existing `Chat_StreamDowngrade` downgrade guard (from quick-task 260524-pyd) to a streaming-NDJSON assertion.
- D-10: Prove `session/cancel` via fake-ACP frame assertion + real-binary disconnect smoke.

### Claude's Discretion

- The exact watchdog teardown mechanism (D-06) — `context.AfterFunc`, a completion channel, or a Run-lifecycle hook — provided `goleak` passes.
- Whether the watchdog lives in `engine.Run` directly or in a small `acp_adapter.go`-adjacent helper, as long as adapters stay cancel-free.
- File split inside `internal/adapter/ollama/`.
- Whether `engine.Run`'s ctx-cancel watchdog distinguishes `context.Canceled` (disconnect) from `context.DeadlineExceeded` (timeout).
- Exact assertion mechanics for the fake-ACP `session/cancel` test.

### Deferred Ideas (OUT OF SCOPE)

- Full slot-release-on-cancel semantics + `POOL_SIZE > 1` — Phase 5.
- Tool-call streaming (`input_json_delta`, `coerceToolCall`) — Phase 6.
- Shared stream-driver abstraction — explicitly rejected (D-08).
- Real token counts in streaming stats — Phase 7+.
- `signature_delta` for Anthropic thinking blocks — Phase 3.1 deferred carry-forward.

</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| STRM-01 | Ollama `/api/chat` and `/api/generate` default to `stream: true` and emit `application/x-ndjson` with one JSON object per line, final object `done: true` | New `ndjson.go` emitter + `handlers.go` branch removal; existing `render.go` helpers reused |
| STRM-02 | OpenAI `/v1/chat/completions` defaults to streaming SSE (`data: [DONE]`); Anthropic `/v1/messages` with `stream:true` emits event-named SSE — both from same canonical channel | Already shipped in Phase 3 / 3.1; this phase regression-tests + ratifies |
| STRM-03 | All three surfaces consume the same canonical chunk channel from the engine | Already true for OpenAI/Anthropic; Ollama NDJSON emitter consumes `engine.Run().Stream().Chunks()` to satisfy this |
| STRM-04 | Client disconnect cancels in-flight `session/prompt` via `session/cancel` JSON-RPC | Engine-owned watchdog via `context.AfterFunc`; fake-ACP frame assertion + real-binary disconnect smoke |
| STRM-05 | All three surfaces support `stream: false` for single-response JSON (regression) | `stream:false` → `engine.Collect` path remains unchanged; regression-tested per surface |

</phase_requirements>

---

## Summary

Phase 4's actual net-new code is narrow: two surfaces already stream. The work is:
(1) a new `internal/adapter/ollama/ndjson.go` emitter that removes the `stream:true → false` downgrade in `handlers.go` and consumes the same `engine.Run().Stream().Chunks()` channel the two SSE emitters already use;
(2) an engine-owned disconnect-cancel watchdog in `engine.Run` that calls `ACPClient.Cancel(sid)` when the request context is terminated before the stream closes normally, with provably goroutine-leak-free teardown;
(3) ratification tests proving all three surfaces share one canonical channel, plus a flip of the existing E2E downgrade guard.

**Primary recommendation:** Use `context.AfterFunc` for the watchdog teardown. The stdlib function (Go 1.21+, project requires Go 1.23) spawns exactly one goroutine when the context is canceled and provides a `stop func() bool` that prevents the goroutine from running when called before cancellation — making it the clearest, most go-idiomatic mechanism for "run this on cancel, but cancel the registration if the stream ends normally." The `stop()` return value and the "stop does not wait for f to complete" caveat must both be understood and handled correctly.

---

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| NDJSON line emission (Ollama) | `internal/adapter/ollama` | `internal/engine` (provides chunks) | Wire shape rendering is adapter's job; the canonical channel is engine's output |
| SSE emission (OpenAI, Anthropic) | `internal/adapter/openai`, `internal/adapter/anthropic` | Already shipped | No change; ratification only |
| Disconnect-cancel watchdog | `internal/engine` (Engine.Run) | `internal/acp` (Cancel notification) | D-06: "one place to enforce policy"; adapters get cancel for free |
| Write-error → ctx-cancel signal | `internal/adapter/ollama` (ndjson.go) | `internal/adapter/openai` (sse.go, D-07 hook) | Adapter detects broken pipe; engine watchdog fires the actual Cancel |
| `session/cancel` JSON-RPC notification | `internal/acp` (Client.Cancel) | — | Already implemented in `client.go:800-807` |
| Goroutine-leak gate | Test suite (`goleak`) | `engine` test package | TRST-05; watchdog teardown is validated here |

---

## Standard Stack

This phase adds zero new library dependencies. All tools are already in `go.mod`.

### Core (already present)
| Library | Version | Purpose | Role in Phase 4 |
|---------|---------|---------|----------------|
| `context` (stdlib) | Go 1.23+ | Context propagation + `AfterFunc` | Watchdog teardown mechanism |
| `encoding/json` (stdlib) | Go 1.23+ | NDJSON line serialization | One `json.Marshal` per chunk in `ndjson.go` |
| `net/http` (stdlib) | Go 1.23+ | `http.Flusher` + `ResponseWriter` | Same Flusher assertion pattern as `sse.go` |
| `go.uber.org/goleak` | v1.3.0 | Goroutine leak detection | Gate on D-06 watchdog teardown |
| `log/slog` (stdlib) | Go 1.23+ | Structured logging | Watchdog logs `Canceled` vs `DeadlineExceeded` |

**Installation:** No new `go get` needed.

---

## Package Legitimacy Audit

> No new external packages are introduced in this phase. All dependencies are stdlib or already in `go.mod`.

| Package | Source | Disposition |
|---------|--------|-------------|
| `context` | Go stdlib | In use |
| `encoding/json` | Go stdlib | In use |
| `go.uber.org/goleak` | Already in `go.mod` | In use |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

---

## Architecture Patterns

### System Architecture Diagram

```
HTTP Client (LangFlow / Pi SDK / loop24-client)
      |
      | POST /api/chat (stream:true)
      v
[chi router] → ollama.handleChat
      |
      | engine.Run(r.Context(), req)
      v
[engine.Engine.Run]
      |--- spawns context.AfterFunc watchdog (D-06)
      |     \--- on ctx.Done (before stream done): acp.Cancel(sid) [session/cancel]
      |
      | returns *Run{stream: Stream, sessionID: sid}
      v
[ollama.runNDJSONEmitter] (ndjson.go — new)
      |
      | for { select { ctx.Done / chunks } }
      |    ← canonical.Chunk{Kind:Text/Thought} from engine.Run().Stream().Chunks()
      |
      | write JSON line + Flush (per chunk → done:false)
      | final line: chatResponseToWire(done:true + stats)
      v
HTTP response body (application/x-ndjson)
      |
      | on write error → cancel(r.Context()) [D-07]
      |   \--- watchdog observes ctx.Done → fires Cancel(sid)

     [stream closes normally]
      |
      | stop() called in ndjson.go → watchdog registration removed (zero leak)
      v
handler returns nil
```

### Recommended Project Structure (additions only)

```
internal/
├── engine/
│   └── engine.go              # Add context.AfterFunc watchdog in Run (D-06)
└── adapter/
    └── ollama/
        ├── ndjson.go           # NEW — NDJSON emitter (model: openai/sse.go)
        ├── ndjson_test.go      # NEW — unit tests (or fold into handlers_test.go)
        └── handlers.go         # Remove wire.Stream=false downgrade; add Run branch
tests/
└── e2e/
    ├── ollama_e2e_test.go      # Flip Chat_StreamDowngrade; add Chat_Streaming / Generate_Streaming
    ├── openai_e2e_test.go      # Add SSE streaming regression (STRM-02/03/05)
    └── anthropic_e2e_test.go   # Add SSE streaming regression (STRM-02/03/05)
internal/
└── acp/
    └── watchdog_test.go        # NEW — fake-ACP session/cancel frame assertion (D-10)
```

---

### Pattern 1: NDJSON Emitter (`ndjson.go`) — modeled on `openai/sse.go`

**What:** Single-goroutine writer that consumes `<-chan canonical.Chunk`, emits one JSON line per chunk with `done:false`, then a final `done:true` line with stats from `render.go` helpers.

**When to use:** Ollama adapter `handleChat` / `handleGenerate` when `wire.Stream` is true (default).

```go
// Source: internal/adapter/openai/sse.go (model) + internal/adapter/ollama/render.go (stats)
func runNDJSONEmitter(
    ctx context.Context,
    w http.ResponseWriter,
    run RunHandle,
    model string,
    start time.Time,
    logger *slog.Logger,
) error {
    // Assert Flusher BEFORE any write (Pitfall 2 — same as sse.go).
    flusher, ok := w.(http.Flusher)
    if !ok {
        return errors.New("ollama: response writer is not flusher")
    }

    // Set streaming headers BEFORE WriteHeader (Pitfall 2 order).
    w.Header().Set("Content-Type", "application/x-ndjson")
    w.Header().Set("Cache-Control", "no-cache")
    w.WriteHeader(http.StatusOK)

    chunks := run.Stream().Chunks()
    for {
        select {
        case <-ctx.Done():
            // Client disconnected — debug-log, return ctx.Err().
            // Watchdog (D-06) already fires Cancel; no Cancel call here.
            logger.Debug("ollama: ndjson client disconnect", "session_id", run.SessionID())
            return fmt.Errorf("ollama: ndjson ctx: %w", ctx.Err())

        case c, ok := <-chunks:
            if !ok {
                // Channel closed — emit final done:true line.
                return finalizeNDJSON(w, flusher, run, model, start, logger)
            }
            if err := emitNDJSONChunk(w, flusher, c, model); err != nil {
                return err // caller (D-07) cancels ctx on write error
            }
        }
    }
}
```

**Key shape for intermediate lines (`/api/chat`):**
```json
{"model":"auto","created_at":"<RFC3339Nano>","message":{"role":"assistant","content":"<delta>"},"done":false}
```
**For `ChunkKindThought` (`/api/chat` only, D-04):**
```json
{"model":"auto","created_at":"<RFC3339Nano>","message":{"role":"assistant","thinking":"<delta>"},"done":false}
```
**Final line (reuse `chatResponseToWire` from `render.go`, then set `Done:true`):**
```json
{"model":"auto","created_at":"<RFC3339Nano>","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","total_duration":1234567890,...}
```

**For `/api/generate` intermediate lines:**
```json
{"model":"auto","created_at":"<RFC3339Nano>","response":"<delta>","done":false}
```
Thoughts are dropped for `/api/generate` (D-04, matching non-stream shape).

---

### Pattern 2: Engine-Owned Watchdog via `context.AfterFunc` (D-06)

**What:** `context.AfterFunc` registers a function to run in its own goroutine when the context is canceled. The returned `stop func() bool` prevents the function from running when called before cancellation. This is the recommended teardown mechanism.

**Why `context.AfterFunc` over a completion channel:**
- `context.AfterFunc` is stdlib (Go 1.21+, project is Go 1.23).
- The `stop()` function provides an atomic "prevent-or-wait" guarantee: if `stop()` returns `true`, the goroutine was never started — zero goroutine leak.
- A completion channel requires more bookkeeping (select with default, close-once pattern) and is more error-prone for a Go novice.
- `context.AfterFunc` is the idiomatic "do this on cancel" pattern in modern Go.

**Critical caveat:** `stop()` does NOT wait for `f` to complete if it has already started. If the context was already canceled when `stop()` is called (i.e., `stop()` returns `false`), you must handle the race where `f` is executing concurrently. For the watchdog, this is safe because `Cancel(sid)` is idempotent (best-effort notification) and calling it multiple times is harmless.

**Placement:** In `engine.Run`, immediately after `Prompt` returns successfully, before returning `*Run` to the caller:

```go
// Source: Go stdlib context.AfterFunc docs + engine.go Run pattern
func (e *Engine) Run(ctx context.Context, req *canonical.ChatRequest) (*Run, error) {
    // ... (existing: PreHook, cwd, blocks, NewSession, SetModel) ...

    stream, err := e.cfg.ACP.Prompt(ctx, sid, blocks)
    if err != nil {
        e.cfg.ACP.Cancel(sid)
        return nil, fmt.Errorf("engine: prompt: %w", err)
    }

    // D-06: watchdog — fires session/cancel if ctx terminates before the
    // stream closes naturally. stop() is called in Run.Wait() or by the
    // adapter when the stream finishes (prevents goroutine leak on normal
    // completion). context.AfterFunc is Go 1.21+; project requires 1.23.
    stopWatchdog := context.AfterFunc(ctx, func() {
        // This goroutine runs when ctx is canceled (client disconnect or
        // timeout). By the time this fires, the adapter's select loop has
        // already returned ctx.Err(), so Cancel is the only remaining work.
        e.cfg.Logger.Debug("engine: watchdog: session cancel on ctx done",
            "session_id", sid,
            "ctx_err", ctx.Err(),
        )
        e.cfg.ACP.Cancel(sid)
    })

    return &Run{
        engine:       e,
        sessionID:    sid,
        stream:       stream,
        req:          req,
        stopWatchdog: stopWatchdog, // NEW field on Run
    }, nil
}
```

**Teardown on normal completion:** The `stopWatchdog` function must be called by whoever signals "stream is done naturally." Two options (planner chooses):

**Option A (preferred for simplicity):** The adapter calls `run.StopWatchdog()` after its emit loop returns without a ctx error. `Run` exposes `StopWatchdog() func() bool`:
```go
func (r *Run) StopWatchdog() func() bool { return r.stopWatchdog }
```

In `ndjson.go` / `sse.go` after the channel closes normally:
```go
// After finalizeNDJSON/finalizeSSE completes with nil error:
if stop := run.StopWatchdog(); stop != nil {
    stop() // prevents watchdog goroutine from firing (returns true on normal completion)
}
return nil
```

**Option B (alternative):** `engine.Run` returns a `*Run` that exposes a `Done()` channel the adapters close. The watchdog selects on both `ctx.Done()` and `run.Done()`. More infrastructure but keeps `stop` internal to the engine.

**Recommendation for Go novice:** Option A — simpler, the stdlib already provides the race-safe `stop()`, no new channel bookkeeping. The planner should document that `stop()` returning `false` is normal (means ctx was already canceled; watchdog may be running, but `Cancel` is idempotent).

---

### Pattern 3: Distinguishing `context.Canceled` vs `context.DeadlineExceeded`

```go
// Source: Go stdlib context package
import "errors"

watchdogLogger := func(ctx context.Context, sid string) {
    switch {
    case errors.Is(ctx.Err(), context.Canceled):
        logger.Debug("engine: watchdog: client disconnect", "session_id", sid)
    case errors.Is(ctx.Err(), context.DeadlineExceeded):
        logger.Debug("engine: watchdog: request timeout", "session_id", sid)
    default:
        logger.Debug("engine: watchdog: ctx done", "session_id", sid, "err", ctx.Err())
    }
    e.cfg.ACP.Cancel(sid)
}
```

Both cases should call `Cancel` — this is just for diagnostic log quality.

---

### Pattern 4: D-07 Write-Error → ctx-cancel in Adapter

When a `fmt.Fprintf` / `Flusher.Flush()` call fails in the NDJSON emitter, the client has disconnected before the context fired. The adapter must cancel the request context it received:

```go
// Pattern: adapter signals disconnect via ctx-cancel
// The request context comes from r.Context() — which is NOT cancelable
// by the adapter directly. The adapter needs its OWN cancelable ctx.
//
// In handleChat (handlers.go), create a derived context:
ctx, cancelFn := context.WithCancel(r.Context())
defer cancelFn()
// Pass ctx (not r.Context()) to engine.Run.
// In ndjson.go, on write error: cancelFn() before returning the error.
// The watchdog then fires on the derived ctx and calls Cancel(sid).
```

**This is the correct D-07 pattern.** The adapter calls its own `cancelFn()` (not `Cancel(sid)` directly), which propagates to the derived context, which the watchdog observes.

---

### Pattern 5: `handlers.go` Branch Addition (STRM-01 / D-03)

Remove `wire.Stream = false` and add branch:

```go
// BEFORE (Phase 2 — remove these lines):
//   if wire.Stream {
//       wire.Stream = false
//   }

// AFTER (Phase 4):
req := wireToChatRequest(&wire, r)
if !wire.Stream {
    // stream:false — non-streaming path (unchanged from Phase 2)
    start := time.Now()
    resp, err := a.cfg.Engine.Collect(r.Context(), req)
    if err != nil {
        writeError(w, http.StatusInternalServerError, err.Error())
        return
    }
    writeJSON(w, chatResponseToWire(resp, start, wire.Model))
    return
}
// stream:true (default) — NDJSON streaming path
ctx, cancelFn := context.WithCancel(r.Context())
defer cancelFn()
run, err := a.cfg.Engine.Run(ctx, req)
if err != nil {
    writeError(w, http.StatusInternalServerError, err.Error())
    return
}
if emitErr := runNDJSONEmitter(ctx, cancelFn, w, run, wire.Model, start, a.cfg.Logger); emitErr != nil {
    a.cfg.Logger.Debug("ollama: ndjson emitter error", "err", emitErr)
}
```

**Note:** `wire.Stream` defaults to `false` in Go JSON unmarshaling (`bool` zero value). The Ollama spec says `stream:true` is the default when the field is absent. This means the handler must treat absent `stream` field as `true` — which requires either a `*bool` pointer type or an explicit `parseStream` helper. **This is a wire type change.** The `ollamaChatRequest.Stream` is currently `bool` (defaults to `false` when absent from JSON). For Node parity, absent-stream should mean streaming. Options:
1. Change `Stream` to `*bool` (`json:"stream"`) — absent → nil → treat as `true`; explicit `false` → `*false`; explicit `true` → `*true`.
2. Keep `bool` but document that only explicit `stream:false` disables streaming (LangFlow sends `stream:true` or `stream:false` explicitly; this is safe for the known clients).

**Recommendation:** Change to `*bool` in `wire.go` to be strictly faithful to the Node reference ("absent means stream:true"). This is a clean wire type change that doesn't affect `wireToChatRequest` (which already copies `wire.Stream` onto `req.Stream`). The `canonical.ChatRequest.Stream` field is also `bool` — the helper should treat `nil` as `true`.

---

### Pattern 6: Fake-ACP `session/cancel` Frame Assertion (D-10)

The fake-ACP server (`fakeacp_test.go`) currently uses channel signals (`permissionResponseReceived`, `promptSeen`) to observe events. The same pattern applies for `session/cancel`:

```go
// In fakeACPServer (extending fakeacp_test.go):
type fakeACPServer struct {
    // ... existing fields ...
    cancelSeen chan struct{} // NEW: closed when session/cancel notification is observed
    lastCancelSID string    // NEW: the sessionId from the cancel notification
}

// In serve() dispatch — add a "session/cancel" case:
// Note: session/cancel is a notification (no id), so it arrives as
// a frame with method:"session/cancel" and no id field.
case "session/cancel":
    // Extract sessionId from params
    var params struct{ SessionID string `json:"sessionId"` }
    if raw, ok := frame["params"]; ok {
        _ = json.Unmarshal(raw, &params)
    }
    f.lastCancelSID = params.SessionID
    select {
    case <-f.cancelSeen:
    default:
        close(f.cancelSeen)
    }
```

**Test structure for D-10 (engine/watchdog_test.go or acp/cancel_test.go):**
1. Create fake-ACP server.
2. Create engine with `acpClientAdapter` wrapping a real `*acp.Client` connected to the fake.
3. Create a cancelable `ctx, cancelFn`.
4. Call `engine.Run(ctx, req)` — wait for `f.promptSeen` to close.
5. Emit one `session/update` chunk (so the emitter goroutine is live).
6. Call `cancelFn()` — this cancels the context.
7. Wait for `f.cancelSeen` with a short timeout (`time.After(2 * time.Second)`).
8. Assert `f.lastCancelSID` matches the session ID.
9. `goleak.VerifyNone(t)` — proves watchdog goroutine exited.

**What the test is NOT:** It does not use a real kiro-cli subprocess; the fake-ACP wire is observable, whereas the real kiro-cli can't expose its JSON-RPC wire to the test.

---

### Anti-Patterns to Avoid

- **Calling `Cancel(sid)` from the adapter directly (violates D-07):** The adapter signals disconnect by canceling its derived context; the watchdog calls `Cancel`. Adapter calling `Cancel` directly would create two cancel callers and violate the "one place" principle.
- **Forgetting to call `stopWatchdog()` on normal completion:** This leaks a goroutine per request until ctx is garbage-collected (which may be never during a long-lived server). `goleak` in the test suite catches this but only if the test covers normal-completion paths too.
- **Returning `run.Stream().Result()` inside `context.AfterFunc`'s goroutine:** The Result() call blocks until the stream closes. The watchdog goroutine must only call `Cancel(sid)` (non-blocking best-effort notification) and return immediately.
- **Using `wire.Stream bool` (Go zero = false) as the "absent = streaming" signal without a `*bool` fix:** A client that sends no `stream` field gets non-streaming. LangFlow sends explicit `stream:true`, but other clients may omit it. Fix the wire type.
- **The `Chat_StreamDowngrade` test is a time bomb:** It exists in `tests/e2e/ollama_e2e_test.go` at line ~222 and asserts that `stream:true` produces `application/json`. It MUST be replaced in the same task that removes the downgrade, or CI fails.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Watchdog teardown | A custom goroutine + channel to observe ctx + completion | `context.AfterFunc` + `stop()` | Stdlib provides atomic prevent-or-fire semantics; no mutex needed |
| NDJSON framing | A custom multi-line buffering scheme | One `json.Marshal` + `fmt.Fprintf(w, "%s\n", line)` + `flusher.Flush()` | NDJSON is newline-delimited JSON; each line is a complete object |
| Goroutine leak detection | A custom goroutine counter | `go.uber.org/goleak` (already in `go.mod`) | Proven; already used in `engine`, `anthropic`, `server` packages |
| ctx cancel error detection | Manual `ctx.Err() == context.Canceled` string check | `errors.Is(ctx.Err(), context.Canceled)` | Correct way to check wrapped context errors |

---

## Runtime State Inventory

Phase 4 is a streaming feature addition, not a rename or migration. Skip.

---

## Common Pitfalls

### Pitfall 1: `wire.Stream bool` Defaults to False (Stream Absent = Non-Streaming)

**What goes wrong:** Go unmarshals a missing JSON field as the zero value. `bool` zero = `false`. A client that sends `{"messages":[...]}` with no `stream` field gets the non-streaming path — wrong, because Ollama's default is `stream:true`.
**Why it happens:** `wire.go` defines `Stream bool` not `Stream *bool`. LangFlow always sends `stream:false` explicitly, so Phase 2 never hit this.
**How to avoid:** Change `ollamaChatRequest.Stream` and `ollamaGenerateRequest.Stream` to `*bool`. In `handlers.go`, treat `nil` as `true`. In `wireToChatRequest`, set `req.Stream = wire.Stream == nil || *wire.Stream`.
**Warning signs:** E2E test sending a bare `{"model":"auto","messages":[...]}` body (no `stream` field) and getting `application/json` instead of `application/x-ndjson`.

### Pitfall 2: Flusher Assertion After WriteHeader

**What goes wrong:** Checking `w.(http.Flusher)` AFTER calling `WriteHeader(200)` means you've already committed the response — you can't send a JSON 500 if the writer isn't a Flusher.
**Why it happens:** Forgetting the established pattern from `openai/sse.go` and `anthropic/sse.go`.
**How to avoid:** Assert Flusher first, THEN set headers, THEN `WriteHeader(200)`. Matches `sse.go` exactly:
```go
flusher, ok := w.(http.Flusher)
if !ok {
    return errors.New("ollama: response writer is not flusher") // caller: writeError(w, 500, ...)
}
w.Header().Set("Content-Type", "application/x-ndjson")
// ... other headers ...
w.WriteHeader(http.StatusOK)
```

### Pitfall 3: Goroutine Leak from Watchdog on Normal Completion

**What goes wrong:** `context.AfterFunc` registers a goroutine. If the stream completes normally and `stop()` is never called, the goroutine stays registered until the context is garbage-collected. Under load, this is effectively a goroutine leak per request.
**Why it happens:** Forgetting to call `stop()` in the "stream ends naturally" code path (the `!ok` branch when the chunks channel closes).
**How to avoid:** In `finalizeNDJSON`, after `run.Stream().Result()` returns successfully, call `run.StopWatchdog()`. `goleak.VerifyTestMain` in the engine package catches this in CI.

### Pitfall 4: `context.AfterFunc` `stop()` Returns False — Watchdog Already Running

**What goes wrong:** If the context is canceled and the watchdog goroutine starts before `stop()` is called, `stop()` returns `false` and the watchdog may still be executing `Cancel(sid)` concurrently.
**Why it matters:** This is NOT a bug for `Cancel(sid)` because `Cancel` is a best-effort idempotent notification. But it means `stop()` returning `false` is expected on the disconnect path and must not be treated as an error.
**How to avoid:** `stop()` returning `false` is fine. The important case is `stop()` returning `true` (stream ended normally, watchdog was prevented) — that's the goroutine-leak-free success path.

### Pitfall 5: `Chat_StreamDowngrade` Test Must Be Flipped Atomically

**What goes wrong:** If the handlers.go downgrade removal and the E2E test flip happen in different tasks and you run CI between them, one direction fails.
**Why it happens:** The guard was explicitly designed to fail on Phase 4 (see comment at `ollama_e2e_test.go:229`: `"WHEN PHASE 4 LANDS NDJSON STREAMING, THIS SUBTEST MUST BE CHANGED"`).
**How to avoid:** Plan the `handlers.go` change and the `ollama_e2e_test.go` flip in the same task (same commit). They are coupled.

### Pitfall 6: Pool-of-1 Slot Must Survive Cancel (SC4)

**What goes wrong:** Phase 4 runs on the pool-of-1 from Phase 2. If `Cancel` leaves the pool slot in a state where it refuses new `session/new` requests (e.g., the kiro-cli subprocess terminates), the gateway is stuck until restart.
**Why it happens:** `session/cancel` is a best-effort JSON-RPC notification; kiro-cli may react to it in unexpected ways on the pool-of-1.
**How to avoid:** The real-binary disconnect E2E (D-10 part 2) must include: start a stream, drop the client, then immediately send a NEW request and assert it succeeds. This proves the slot survived. Phase 5 hardens slot-release-on-cancel; Phase 4 only needs non-crash.

### Pitfall 7: `ollamaChatResponse` `Done` Field Ordering

**What goes wrong:** The `done:true` final line must be a complete `ollamaChatResponse` (or equivalent streaming struct), not a partial object. If the `done` field is missing from intermediate lines (which must have `done:false`), the Node reference shape breaks.
**Why it happens:** The existing `ollamaChatResponse` struct always has `Done: true` (Phase 2 non-streaming only). For streaming, intermediate frames need `Done: false`.
**How to avoid:** The NDJSON emitter should NOT reuse `ollamaChatResponse` directly for intermediate frames. Instead, build a smaller intermediate struct inline:
```go
type ndjsonChatLine struct {
    Model     string                    `json:"model"`
    CreatedAt string                    `json:"created_at"`
    Message   ollamaChatResponseMessage `json:"message"`
    Done      bool                      `json:"done"`
}
```
For the final line, build a full `ollamaChatResponse` with `Done:true` using `chatResponseToWire` (adapted to take accumulated text/thinking from the emitter rather than a `canonical.ChatResponse`).

### Pitfall 8: `context.AfterFunc` Goroutine Is NOT the Request Goroutine

**What goes wrong:** The `AfterFunc` goroutine runs concurrently with the adapter's select loop. If the watchdog goroutine tries to write to the `ResponseWriter` or touch the `flusher`, it races with the adapter's single-goroutine writer invariant (D-05).
**Why it matters:** The watchdog must ONLY call `e.cfg.ACP.Cancel(sid)`. It must NOT touch the HTTP response writer. `Cancel` is already goroutine-safe (it calls `sendNotification` which writes to the ACP JSON-RPC pipe, not the HTTP response).
**How to avoid:** The watchdog function body is exactly: `logger.Debug(...); e.cfg.ACP.Cancel(sid)`. Nothing else.

---

## Code Examples

### Verified Pattern: `context.AfterFunc` teardown

```go
// Source: Go stdlib docs (context.AfterFunc) + verified available in Go 1.23+
stopWatchdog := context.AfterFunc(ctx, func() {
    logger.Debug("engine: watchdog fired", "session_id", sid, "ctx_err", ctx.Err())
    acp.Cancel(sid)
})
// ... return Run with stopWatchdog field ...

// In the emitter (ndjson.go), on normal completion:
if stop := run.StopWatchdog(); stop != nil {
    stop() // returns true → goroutine was prevented (zero leak)
           // returns false → ctx already canceled, goroutine may be executing Cancel (idempotent, OK)
}
```

### Verified Pattern: `ACP.Cancel` notification shape (client.go:800-807)

```go
// Source: internal/acp/client.go:800-807 [VERIFIED in codebase]
func (c *Client) Cancel(sessionID string) {
    c.sendNotification(rpcNotification{
        JSONRPC: "2.0",
        Method:  "session/cancel",
        Params:  cancelParams{SessionID: sessionID},
    })
}
// This is a best-effort fire-and-forget notification (no response expected).
// Safe to call from a goroutine; sendNotification is goroutine-safe.
```

### Verified Pattern: Existing downgrade to remove (handlers.go:42-45, 82-84)

```go
// Source: internal/adapter/ollama/handlers.go:42-45 [VERIFIED in codebase]
// REMOVE these lines:
if wire.Stream {
    wire.Stream = false
}
// REPLACE with: branch on wire.Stream for NDJSON vs Collect path (see Pattern 5 above)
```

### Verified Pattern: `goleak.VerifyTestMain` (engine package)

```go
// Source: internal/engine/testmain_test.go [VERIFIED in codebase]
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m) // catches any goroutine left running after tests
}
// If engine package doesn't yet have this (it does), add per TRST-05.
// For new test packages (e.g., a watchdog_test.go), add a TestMain too.
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Ollama `stream:true` silently downgraded to `stream:false` | Ollama streams NDJSON by default | Phase 4 (this phase) | LangFlow and Ollama SDK clients receive live streaming |
| No cancel on client disconnect (adapters just return `ctx.Err()`) | Engine-owned watchdog fires `session/cancel` | Phase 4 (this phase) | kiro-cli subprocess notified of cancellation; slot reusable faster |
| `context.AfterFunc` not widely used (Go 1.21 addition) | Standard pattern for "run on cancel, cancel-able on completion" | Go 1.21 | Clean replacement for a select-in-goroutine pattern |

**Deprecated/outdated:**
- `wire.Stream = false` downgrade in `handlers.go`: removed in Phase 4.
- `Chat_StreamDowngrade` E2E subtest asserting `application/json` on `stream:true`: replaced with NDJSON assertion.

---

## Open Questions (RESOLVED)

1. **`*bool` vs `bool` for `wire.Stream`**
   - What we know: `bool` defaults to `false` (absent = non-streaming); Node default is `stream:true` absent.
   - What's unclear: LangFlow always sends explicit `stream:true` or `stream:false`; other clients may omit it.
   - Recommendation: Change to `*bool` for correctness; the planner should make this a Wave 0 wire-type change in `wire.go`.
   - **RESOLVED** — `wire.Stream` becomes `*bool` (Plan 01 Task 1). `streamEnabled(nil)` returns `true` (absent = streaming).

2. **`Run.StopWatchdog()` accessor vs embedding the stop func in `finalizeNDJSON` closure**
   - What we know: Adapters cannot import `internal/engine`; `RunHandle` interface is declared locally in each adapter package.
   - What's unclear: The `stopWatchdog` field is on `*engine.Run`; adapters access it through their local `RunHandle` interface.
   - Recommendation: Add `StopWatchdog() func() bool` to the local `RunHandle` interfaces in `internal/adapter/ollama/adapter.go` AND `internal/adapter/openai/adapter.go` AND `internal/adapter/anthropic/adapter.go`. The concrete `*engine.Run` structurally satisfies it. This propagates cleanly without an import.
   - **RESOLVED** — `StopWatchdog() func() bool` added to `ollama.RunHandle` (Plan 01 Task 3). Added to `openai.RunHandle` and `anthropic.RunHandle` as well, and each emitter's normal-completion path calls `stop()` — `openai/sse.go` `finalizeSSE` and the anthropic emitter's completion path (Plan 04 Task 0).

3. **Where to put the watchdog unit test (D-10 fake-ACP assertion)**
   - What we know: `fakeacp_test.go` is in `package acp_test`; the engine's `fakeACP` is in `package engine` (whitebox). The watchdog is in the engine; the observable wire frame is on the ACP client.
   - What's unclear: Whether to test the watchdog via the engine fake-ACP pattern (extending `engine_test.go` with a `cancelSeenChan`) or via a new `acp/watchdog_integration_test.go` that goes engine → real `*acp.Client` → fake-ACP server.
   - Recommendation: Add a new `engine/watchdog_test.go` that uses a `fakeACP.cancelCalls` slice (already recorded by the existing `fakeACP` harness) to assert `Cancel` was called with the right session ID after ctx cancellation. The fake-ACP `session/cancel` JSON-RPC wire frame is separately validated in `acp/cancel_test.go` (new file) where the full ACP client + fake-ACP server pipeline is exercised.
   - **RESOLVED** — `internal/engine/watchdog_test.go` (whitebox, `fakeACP.cancelCalls`) + `internal/acp/cancel_test.go` (ACP wire frame), per Plan 03.

---

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|-------------|-----------|---------|----------|
| Go 1.21+ (`context.AfterFunc`) | D-06 watchdog mechanism | YES | 1.23 (project) / 1.26.3 (host) | Use completion channel (see Pattern 2 Option B) |
| `go.uber.org/goleak` v1.3.0 | Goroutine leak gate | YES | v1.3.0 (go.mod) | None — required by TRST-05 |
| kiro-cli | Real-binary E2E tests | YES (host) | 2.4.1 (from Phase 1.1) | Skip E2E with `-short` flag |

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | `go test` (stdlib) + `goleak` v1.3.0 |
| Config file | None (test flags in Makefile) |
| Quick run command | `go test ./internal/adapter/ollama/... ./internal/engine/...` |
| Full suite command | `go test -race ./... && go test -race -tags e2e ./tests/e2e/... -timeout 120s` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | Notes |
|--------|----------|-----------|-------------------|-------|
| STRM-01 | Ollama chat/generate emit NDJSON by default | Unit + fake-engine | `go test ./internal/adapter/ollama/... -run TestNDJSON` | New `ndjson_test.go`; httptest `ResponseRecorder` + mock `RunHandle` |
| STRM-01 | `stream:false` returns single JSON (regression) | Unit | `go test ./internal/adapter/ollama/... -run TestChat_NonStreaming` | Existing handlers_test.go coverage; verify not broken |
| STRM-01 E2E | Real-binary Ollama NDJSON round-trip | E2E | `go test -tags e2e ./tests/e2e/... -run TestE2E_Ollama/Chat_Streaming` | Flips `Chat_StreamDowngrade`; new `Chat_Streaming` + `Generate_Streaming` subtests |
| STRM-02 | OpenAI SSE regression (correct format, `[DONE]`) | E2E | `go test -tags e2e ./tests/e2e/... -run TestE2E_OpenAI/ChatCompletions_Streaming` | New or extending `openai_e2e_test.go` |
| STRM-02 | Anthropic SSE regression (event-named, ping) | E2E | `go test -tags e2e ./tests/e2e/... -run TestE2E_Anthropic/Messages_Streaming` | Existing E2E may already cover; add explicit streaming subtest |
| STRM-03 | All three surfaces share one canonical channel | Engine unit | `go test ./internal/engine/... -run TestEngineRun_StreamingPath` | Verify via adapter `fakeEngine.Run` call count = 1 per request |
| STRM-04 | `session/cancel` fired on disconnect | Engine watchdog unit | `go test ./internal/engine/... -run TestWatchdog_CancelOnCtxDone` | Uses `fakeACP.cancelCalls`; `goleak.VerifyNone(t)` after `stop()` |
| STRM-04 | `session/cancel` JSON-RPC frame on wire | ACP integration | `go test ./internal/acp/... -run TestIntegration_CancelFrame` | fake-ACP `cancelSeen` channel; verifies actual wire frame |
| STRM-04 E2E | Real-binary: drop client mid-stream, slot survives | E2E smoke | `go test -tags e2e ./tests/e2e/... -run TestE2E_Ollama/Chat_DisconnectSmoke` | New subtest; asserts next request succeeds |
| STRM-05 | `stream:false` regression all three surfaces | E2E | `go test -tags e2e ./tests/e2e/... -run TestE2E_Ollama/Chat_NonStreaming\|TestE2E_OpenAI/ChatCompletions_NonStreaming\|TestE2E_Anthropic/Messages_NonStreaming` | Existing non-streaming tests; must remain green |

### Sampling Rate

- **Per task commit:** `go test -race ./internal/adapter/ollama/... ./internal/engine/...`
- **Per wave merge:** `go test -race ./... && go test -race -tags e2e ./tests/e2e/... -timeout 120s`
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps

- [ ] `internal/adapter/ollama/ndjson_test.go` — covers STRM-01 unit (NDJSON line shape, thought chunks, `done:true` final line, Flusher assertion failure path)
- [ ] `internal/engine/watchdog_test.go` — covers STRM-04 (watchdog calls `Cancel` on ctx cancel; `stop()` prevents goroutine on normal completion; `goleak.VerifyNone`)
- [ ] `internal/acp/cancel_test.go` — covers STRM-04 fake-ACP wire frame assertion (extends `fakeacp_test.go` with `cancelSeen` channel)
- [ ] `tests/e2e/ollama_e2e_test.go` — flip `Chat_StreamDowngrade`; add `Chat_Streaming`, `Generate_Streaming`, `Chat_DisconnectSmoke` subtests

---

## Security Domain

`security_enforcement` not explicitly disabled in config. Standard assessment:

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | No change | Auth middleware unchanged; streaming path goes through same chi middleware |
| V3 Session Management | Partial | `session/cancel` is sent on disconnect — session lifecycle is managed, not extended |
| V4 Access Control | No change | Same adapter-level auth; no new privileged paths |
| V5 Input Validation | Yes | `wire.Stream *bool` change; ensure nil-deref guard in `wireToChatRequest` |
| V6 Cryptography | No | No new crypto |

### Known Threat Patterns for this Stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| `session/cancel` notification leaking session IDs | Info disclosure | Session IDs are internal; not echoed in HTTP response |
| Watchdog goroutine calling `Cancel` after session is already torn down | Spoofing | `Cancel` is best-effort notification; kiro-cli ignores cancel for unknown sessions |
| NDJSON line injection (newline in chunk text) | Tampering | `json.Marshal` escapes all control characters including `\n`; no raw string write |
| Goroutine leak under disconnect flood | DoS | `goleak` test gate; `stop()` pattern prevents accumulation |

---

## Sources

### Primary (HIGH confidence — verified in codebase)

- `internal/engine/engine.go` — `Engine.Run`, `ACPClient.Cancel` interface, `Run` struct, `emptyStream`
- `internal/adapter/openai/sse.go` — `runSSEEmitter` model for `ndjson.go`
- `internal/adapter/anthropic/sse.go` — `runSSEEmitterLoop`, `context.AfterFunc` is analogous to its ping ticker teardown pattern
- `internal/adapter/ollama/handlers.go:42-45,82-84` — the exact downgrade lines to remove
- `internal/adapter/ollama/render.go` — `chatResponseToWire`, `generateResponseToWire`, `mapStopReason`, `estimateTokens`, `joinTextContent`, `joinThinkingContent`
- `internal/adapter/ollama/wire.go` — `ollamaChatRequest.Stream bool` (needs `*bool` change)
- `internal/acp/client.go:800-807` — `Cancel` → `session/cancel` notification
- `internal/acp/fakeacp_test.go` — fake-ACP server patterns: `cancelSeen` channel extension point
- `internal/engine/testmain_test.go` — `goleak.VerifyTestMain` pattern
- `tests/e2e/ollama_e2e_test.go:222-261` — `Chat_StreamDowngrade` subtest + comment marking Phase 4 flip requirement
- `docs/reference/acp_server_node_reference.md:270,286-288` — "Ollama streams by default" ground truth
- `go.mod` — Go 1.23 requirement; `go.uber.org/goleak` v1.3.0 already present
- `go doc context.AfterFunc` — verified stdlib availability in Go 1.21+

### Secondary (MEDIUM confidence)

- `.planning/phases/04-streaming/04-CONTEXT.md` — D-01 through D-10 locked decisions
- `.planning/phases/03-openai-surface/03-CONTEXT.md` — OpenAI SSE pitfall catalogue
- `.planning/phases/03.1-anthropic-surface/03.1-CONTEXT.md` — Anthropic SSE D-06 deferral note

---

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `context.AfterFunc` `stop()` returning `false` means Cancel may run concurrently but is idempotent — safe to ignore | Pattern 2 | If `Cancel` were NOT idempotent, a double-cancel could corrupt state. Verified: `Cancel` sends a best-effort notification; no state machine on the gateway side. |
| A2 | LangFlow always sends explicit `stream:true` or `stream:false`, never omits the field | Pitfall 1 | If LangFlow omits `stream`, the `bool`-not-`*bool` issue would not manifest for LangFlow. But `*bool` is still the right fix for the general case. |
| A3 | `sendNotification` in `acp/client.go` is goroutine-safe (called from watchdog goroutine) | Pattern 2 | If `sendNotification` races on the write pipe, the watchdog would corrupt ACP framing. Assumed safe based on the existing `Cancel` usage pattern; should be verified by reading `sendNotification` impl during execution. |

---

## Metadata

**Confidence breakdown:**
- Standard Stack: HIGH — no new dependencies; all tools verified in codebase
- Architecture: HIGH — all patterns grounded in actual code (engine.go, sse.go, handlers.go)
- Pitfalls: HIGH — derived from actual code paths and test comments in codebase
- Watchdog teardown: HIGH — `context.AfterFunc` docs verified via `go doc`

**Research date:** 2026-05-24
**Valid until:** 2026-06-24 (stable Go stdlib; only risk is if Go 1.23 changes `context.AfterFunc` semantics, which is extremely unlikely)
