# Phase 4: Streaming - Pattern Map

**Mapped:** 2026-05-24
**Files analyzed:** 8 new/modified files
**Analogs found:** 8 / 8

---

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `internal/adapter/ollama/ndjson.go` | service/emitter | streaming (NDJSON) | `internal/adapter/openai/sse.go` | exact |
| `internal/adapter/ollama/handlers.go` | controller | request-response + streaming branch | `internal/adapter/anthropic/sse.go` (handler side) | role-match |
| `internal/adapter/ollama/wire.go` | model | transform | `internal/adapter/ollama/wire.go` (self — `*bool` field fix) | self-modify |
| `internal/adapter/ollama/adapter.go` | config/wiring | — | `internal/adapter/openai/adapter.go` | role-match |
| `internal/engine/engine.go` | service/orchestrator | streaming + watchdog | `internal/adapter/anthropic/sse.go` (ticker teardown) | data-flow-match |
| `internal/engine/watchdog_test.go` | test | event-driven | `internal/engine/engine_test.go` | exact |
| `internal/acp/cancel_test.go` | test | event-driven | `internal/acp/fakeacp_test.go` | exact |
| `tests/e2e/ollama_e2e_test.go` | test/e2e | streaming | `tests/e2e/openai_e2e_test.go` | exact |

---

## Pattern Assignments

### `internal/adapter/ollama/ndjson.go` (emitter, streaming)

**Analog:** `internal/adapter/openai/sse.go`

**Imports pattern** (`internal/adapter/openai/sse.go` lines 1-13):
```go
package openai

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "log/slog"
    "net/http"
    "time"

    "otto-gateway/internal/canonical"
)
```
For `ndjson.go`, replace `package openai` with `package ollama`. Drop `time` unless used for `time.Now()` in chunk lines.

**Flusher assertion + header-before-WriteHeader pattern** (`openai/sse.go` lines 158-171):
```go
flusher, ok := w.(http.Flusher)
if !ok {
    return errors.New("openai: response writer is not flusher")
}

w.Header().Set("Content-Type", "text/event-stream")
w.Header().Set("Cache-Control", "no-cache")
w.Header().Set("Connection", "keep-alive")
w.WriteHeader(http.StatusOK)
```
For NDJSON replace `"text/event-stream"` with `"application/x-ndjson"`. Keep exact ordering: assert Flusher → set headers → WriteHeader (Pitfall 2 in RESEARCH.md).

**Core select-loop pattern** (`openai/sse.go` lines 181-199):
```go
chunks := run.Stream().Chunks()
for {
    select {
    case <-ctx.Done():
        e.logger.Debug("openai: sse client disconnect", "session_id", run.SessionID())
        return fmt.Errorf("openai: sse ctx: %w", ctx.Err())

    case c, ok := <-chunks:
        if !ok {
            // Channel closed — stream ended; emit final frames.
            return finalizeSSE(e, run)
        }
        if err := e.applyChunk(c); err != nil {
            return err
        }
    }
}
```
Copy this select-loop verbatim as the skeleton for `runNDJSONEmitter`. Replace `finalizeSSE(e, run)` with `finalizeNDJSON(w, flusher, run, model, start, logger)`. Replace `e.applyChunk(c)` with `emitNDJSONChunk(w, flusher, c, model)`. On write error from `emitNDJSONChunk`, call the `cancelFn` from `handlers.go` (D-07) before returning.

**finalize/error truncation pattern** (`openai/sse.go` lines 213-225):
```go
func finalizeSSE(e *sseEmitter, run RunHandle) error {
    final, rerr := run.Stream().Result()
    if rerr != nil {
        e.logger.Debug("openai: sse stream result error", "err", rerr)
        return fmt.Errorf("openai: sse stream result: %w", rerr)
    }
    // ...
}
```
In `finalizeNDJSON`: call `run.Stream().Result()`, on error debug-log and return the error (no final NDJSON line — truncated stream per D-05). On success, build the `done:true` line by calling `chatResponseToWire`/`generateResponseToWire` from `render.go` (passing `nil` resp and the accumulated stats), serialize with `json.Marshal`, write `fmt.Fprintf(w, "%s\n", line)`, and `flusher.Flush()`.

**Intermediate line struct** (RESEARCH.md Pattern 7 — no existing analog; define inline):
```go
type ndjsonChatLine struct {
    Model     string                    `json:"model"`
    CreatedAt string                    `json:"created_at"`
    Message   ollamaChatResponseMessage `json:"message"`
    Done      bool                      `json:"done"`
}

type ndjsonGenerateLine struct {
    Model     string `json:"model"`
    CreatedAt string `json:"created_at"`
    Response  string `json:"response"`
    Done      bool   `json:"done"`
}
```
Reuse `ollamaChatResponseMessage` from `internal/adapter/ollama/wire.go` lines 86-90 (already has `Role`, `Content`, `Thinking` with `omitempty`).

**Write-one-line helper pattern** (`openai/sse.go` `writeData` lines 81-91):
```go
func (e *sseEmitter) writeData(payload any) error {
    body, err := json.Marshal(payload)
    if err != nil {
        return fmt.Errorf("openai: marshal chunk: %w", err)
    }
    if _, err := fmt.Fprintf(e.w, "data: %s\n\n", body); err != nil {
        return fmt.Errorf("openai: write chunk: %w", err)
    }
    e.flusher.Flush()
    return nil
}
```
For NDJSON: `fmt.Fprintf(w, "%s\n", body)` (no `"data: "` prefix; single newline). Keep the same `json.Marshal` → `fmt.Fprintf` → `flusher.Flush()` sequence.

**RunHandle interface** — `ndjson.go` must declare a local `RunHandle` interface matching the ones in `openai/adapter.go` lines 49-58 and `anthropic/adapter.go`. It needs `Stream() Stream` and `SessionID() string`. Once D-06 lands, also add `StopWatchdog() func() bool` to the interface so adapters can call `run.StopWatchdog()` after normal completion.

---

### `internal/adapter/ollama/handlers.go` (controller, request-response + streaming branch)

**Analog:** `internal/adapter/openai/sse.go` + `internal/adapter/ollama/handlers.go` (self)

**Lines to remove** (`handlers.go` lines 43-45 and 82-84 — exact code verified in codebase):
```go
// REMOVE both of these identical blocks:
if wire.Stream {
    wire.Stream = false
}
```

**Streaming-branch pattern** (RESEARCH.md Pattern 5 — modeled on OpenAI `handleChatCompletions`):
```go
// After req := wireToChatRequest(&wire, r):
if !streamEnabled(wire.Stream) {
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
// stream:true (default when absent) — NDJSON streaming path
ctx, cancelFn := context.WithCancel(r.Context())
defer cancelFn()

run, err := a.cfg.Engine.Run(ctx, req)
if err != nil {
    writeError(w, http.StatusInternalServerError, err.Error())
    return
}
start := time.Now()
if emitErr := runNDJSONEmitter(ctx, cancelFn, w, run, wire.Model, start, a.cfg.Logger); emitErr != nil {
    a.cfg.Logger.Debug("ollama: ndjson emitter error", "err", emitErr)
}
```
Note: `streamEnabled(wire.Stream *bool)` returns `wire.Stream == nil || *wire.Stream` — evaluates absent field as `true` (Node parity per Pitfall 1 in RESEARCH.md).

**Engine interface addition** (`adapter.go` lines 40-42 — add `Run`):
```go
type Engine interface {
    Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
    // Add:
    Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error)
}
```
This mirrors `internal/adapter/openai/adapter.go` lines 38-47 exactly.

**writeError / writeJSON helpers** (`handlers.go` lines 272-287 — unchanged, copy these for reference):
```go
func writeJSON(w http.ResponseWriter, body any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
```
These are already in `handlers.go`. Do not duplicate.

---

### `internal/adapter/ollama/wire.go` (model — `*bool` fix)

**Analog:** `internal/adapter/ollama/wire.go` line 207 — `ollamaStubStreamRequest` already uses `*bool`:
```go
type ollamaStubStreamRequest struct {
    Stream *bool `json:"stream,omitempty"`
}
```
This is the exact pattern to copy for `ollamaChatRequest` and `ollamaGenerateRequest`.

**Current field** (`wire.go` lines 27 and 105):
```go
Stream    bool             `json:"stream"`         // ollamaChatRequest
Stream    bool             `json:"stream"`         // ollamaGenerateRequest
```

**Change to** (matching `ollamaStubStreamRequest` pattern at line 207):
```go
Stream    *bool            `json:"stream,omitempty"` // ollamaChatRequest
Stream    *bool            `json:"stream,omitempty"` // ollamaGenerateRequest
```

**wireToChatRequest impact** (`wire.go` lines 251-257): `req.Stream = w.Stream` currently copies `bool` directly. After the change, add a helper:
```go
// streamEnabled returns true when the *bool field is absent (nil) or true.
// Absent means the client sent no stream field — Ollama default is stream:true.
func streamEnabled(s *bool) bool {
    return s == nil || *s
}
```
Use `req.Stream = streamEnabled(w.Stream)` in `wireToChatRequest`. In `wireGenerateToChatRequest` same pattern.

The `canonical.ChatRequest.Stream` field stays `bool` — no change there.

---

### `internal/engine/engine.go` (service/orchestrator — watchdog addition)

**Analog:** `internal/adapter/anthropic/sse.go` (ticker teardown pattern) + `internal/engine/engine_test.go` (fakeACP.cancelCalls)

**Existing `Run` return statement** (`engine.go` lines 175-181):
```go
return &Run{
    engine:    e,
    sessionID: sid,
    stream:    stream,
    req:       req,
}, nil
```

**`Run` struct addition** (`engine.go` lines 105-117 — add `stopWatchdog`):
```go
type Run struct {
    engine       *Engine
    sessionID    string
    stream       Stream
    req          *canonical.ChatRequest
    response     *canonical.ChatResponse
    stopWatchdog func() bool // NEW: returned by context.AfterFunc; call to prevent watchdog on normal close
}
```

**Watchdog insertion** (after `stream, err := e.cfg.ACP.Prompt(ctx, sid, blocks)` at line 169, before the return):
```go
// D-06: engine-owned disconnect watchdog. context.AfterFunc spawns exactly
// one goroutine when ctx is canceled (client disconnect or timeout). stop()
// prevents the goroutine from running when called before cancellation —
// the adapter calls run.StopWatchdog() after the stream closes naturally.
// context.AfterFunc is Go 1.21+; project requires 1.23. (RESEARCH.md Pattern 2)
stopWatchdog := context.AfterFunc(ctx, func() {
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
    stopWatchdog: stopWatchdog,
}, nil
```

**`StopWatchdog` accessor** (new method on `*Run`):
```go
// StopWatchdog returns the context.AfterFunc stop function. Adapters call
// it after normal stream completion to prevent the watchdog goroutine from
// firing. stop() returning false means the context was already canceled
// and the goroutine may be executing Cancel — that is safe because Cancel
// is idempotent. (RESEARCH.md Pattern 2 Option A)
func (r *Run) StopWatchdog() func() bool { return r.stopWatchdog }
```

**goleak gate** (`engine/testmain_test.go` lines 17-19 — already present):
```go
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```
The watchdog teardown test in `watchdog_test.go` runs under this gate automatically.

---

### `internal/engine/watchdog_test.go` (test, event-driven)

**Analog:** `internal/engine/engine_test.go` — `fakeACP` harness + `cancelCalls` slice

**Test harness reuse** (`engine_test.go` lines 34-108):
```go
type fakeACP struct {
    mu sync.Mutex
    // ...
    cancelCalls []string // session ids  ← key field for D-10 assertion
}

func (f *fakeACP) Cancel(sessionID string) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.cancelCalls = append(f.cancelCalls, sessionID)
}
```
The `fakeACP` type is in `package engine` (whitebox test). `watchdog_test.go` must also be `package engine` to access it.

**Test structure for watchdog** (RESEARCH.md Pattern 6 adapted to engine-level):
```go
func TestWatchdog_CancelOnCtxDone(t *testing.T) {
    ack := &fakeACP{
        newSessionID: "watchdog-sid",
        // chunksToEmit: leave nil — stream closes immediately
    }
    eng := newTestEngine(t, ack)

    ctx, cancelFn := context.WithCancel(context.Background())
    defer cancelFn()

    req := &canonical.ChatRequest{
        Messages: []canonical.Message{{Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "hi"}}}},
    }
    run, err := eng.Run(ctx, req)
    if err != nil {
        t.Fatalf("Run: %v", err)
    }

    // Cancel before stream drains.
    cancelFn()

    // Give the AfterFunc goroutine time to run.
    time.Sleep(50 * time.Millisecond)

    ack.mu.Lock()
    calls := ack.cancelCalls
    ack.mu.Unlock()

    if len(calls) == 0 {
        t.Error("watchdog: Cancel not called after ctx cancellation")
    } else if calls[0] != "watchdog-sid" {
        t.Errorf("watchdog: Cancel session id: got %q, want watchdog-sid", calls[0])
    }

    // goleak.VerifyTestMain in testmain_test.go asserts no leaked goroutines.
    _ = run // suppress unused warning; in real test drain the channel
}

func TestWatchdog_StopPreventsCancel_OnNormalCompletion(t *testing.T) {
    ack := &fakeACP{
        newSessionID: "watchdog-sid-2",
        chunksToEmit: []canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello"}}},
    }
    eng := newTestEngine(t, ack)

    ctx := context.Background()
    run, err := eng.Run(ctx, req /* build req */)
    if err != nil {
        t.Fatalf("Run: %v", err)
    }

    // Drain stream (normal completion).
    for range run.Stream().Chunks() {}
    _, _ = run.Stream().Result()

    // Call StopWatchdog — should return true (goroutine was never started).
    if stop := run.StopWatchdog(); stop != nil {
        stopped := stop()
        if !stopped {
            t.Log("watchdog: stop() returned false — ctx may have already fired, Cancel is idempotent")
        }
    }

    // Assert NO cancel call on normal completion path.
    ack.mu.Lock()
    calls := ack.cancelCalls
    ack.mu.Unlock()

    if len(calls) > 0 {
        t.Errorf("watchdog: unexpected Cancel calls on normal completion: %v", calls)
    }
}
```

---

### `internal/acp/cancel_test.go` (test, event-driven)

**Analog:** `internal/acp/fakeacp_test.go` — `fakeACPServer` struct + `serve` dispatch loop + `promptSeen` channel pattern

**fakeACPServer extension** (`fakeacp_test.go` lines 75-95 — add two fields):
```go
type fakeACPServer struct {
    // ... existing fields ...
    cancelSeen    chan struct{} // NEW: closed on first session/cancel notification
    lastCancelSID string       // NEW: sessionId from the cancel notification
}
```

In `newFakeACPServer` (`fakeacp_test.go` lines 114-132), initialize:
```go
cancelSeen: make(chan struct{}),
```

**Dispatch case in `serve`** (`fakeacp_test.go` lines 189-270 — add to the `switch method` block):
```go
case "session/cancel":
    // session/cancel is a notification (no id field). Extract sessionId from params.
    var params struct {
        SessionID string `json:"sessionId"`
    }
    if raw, ok := frame["params"]; ok {
        _ = json.Unmarshal(raw, &params)
    }
    f.lastCancelSID = params.SessionID
    select {
    case <-f.cancelSeen:
        // already closed
    default:
        close(f.cancelSeen)
    }
```
Note: the existing `permissionResponseReceived` close at lines 163-168 uses the identical idempotent close pattern — copy it verbatim.

**ACP Cancel notification shape** (`internal/acp/client.go` lines 800-807 — verified):
```go
func (c *Client) Cancel(sessionID string) {
    c.sendNotification(rpcNotification{
        JSONRPC: "2.0",
        Method:  "session/cancel",
        Params:  cancelParams{SessionID: sessionID},
    })
}
```
The `session/cancel` frame the fake observes has `method:"session/cancel"` and `params.sessionId` (camelCase — check `cancelParams` struct definition for exact key).

**Test structure** (RESEARCH.md Pattern 6 steps 1-9):
```go
func TestIntegration_CancelFrame(t *testing.T) {
    f := newFakeACPServer(t)
    defer f.close()

    client := acp.NewWithConn(f.clientRWC)
    // Start client (initialize handshake).
    ctx, cancelFn := context.WithCancel(context.Background())
    defer cancelFn()
    // ... wait for promptSeen, then cancelFn() ...
    // Wait for cancelSeen with timeout:
    select {
    case <-f.cancelSeen:
        // pass
    case <-time.After(2 * time.Second):
        t.Fatal("session/cancel not observed within 2s")
    }
    if f.lastCancelSID == "" {
        t.Error("session/cancel: sessionId was empty")
    }
}
```

---

### `tests/e2e/ollama_e2e_test.go` (test/e2e, streaming)

**Analog:** `tests/e2e/openai_e2e_test.go` (SSE streaming assertion) + existing `ollama_e2e_test.go`

**Chat_StreamDowngrade subtest to replace** (`ollama_e2e_test.go` lines 222-261):

The comment at line 229 explicitly marks the flip requirement:
```
// >>> WHEN PHASE 4 LANDS NDJSON STREAMING, THIS SUBTEST MUST BE CHANGED <<<
```
Replace the entire `t.Run("Chat_StreamDowngrade", ...)` body with:
```go
t.Run("Chat_Streaming", func(t *testing.T) {
    body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say hi"}]}`)
    resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
    defer func() { _ = resp.Body.Close() }()
    if resp.StatusCode != http.StatusOK {
        t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
    }
    ct := resp.Header.Get("Content-Type")
    if !strings.HasPrefix(ct, "application/x-ndjson") {
        t.Errorf("Content-Type: got %q, want application/x-ndjson prefix", ct)
    }
    // Assert at least one NDJSON line with done:false and one with done:true.
    scanner := bufio.NewScanner(resp.Body)
    var lastLine struct {
        Done       bool   `json:"done"`
        DoneReason string `json:"done_reason"`
    }
    lineCount := 0
    for scanner.Scan() {
        line := scanner.Bytes()
        if err := json.Unmarshal(line, &lastLine); err != nil {
            t.Fatalf("malformed NDJSON line: %v — line: %s", err, line)
        }
        lineCount++
    }
    if lineCount < 2 {
        t.Errorf("NDJSON stream: got %d lines, want at least 2 (some done:false + final done:true)", lineCount)
    }
    if !lastLine.Done {
        t.Error("last NDJSON line: done==false, want true")
    }
    if lastLine.DoneReason != "stop" && lastLine.DoneReason != "length" {
        t.Errorf("last NDJSON line done_reason: got %q, want stop or length", lastLine.DoneReason)
    }
})
```

**NDJSON read helper** — reuse `bufio.NewScanner` from the SSE streaming tests in `openai_e2e_test.go`. The `ollamaRequest` helper (lines 34-58) is already shared and reusable without changes.

**bootGateway / gateOrSkip** (`e2e_test.go` lines 57-103) — reuse as-is. New subtests plug in the same `baseURL`.

**Generate streaming subtest** — same pattern as `Chat_Streaming`, but:
- body: `{"model":"auto","prompt":"say hi"}` (no `messages` key)
- assert `response` field in NDJSON lines (not `message`)
- Content-Type assertion unchanged

**Disconnect smoke subtest** (`Chat_DisconnectSmoke`, D-10 E2E):
- Start a streaming chat request.
- Close the response body mid-stream (`resp.Body.Close()` after first line received).
- Issue a second request and assert it succeeds with 200 (proves slot survived cancel).
- Assert no goroutine leak (goleak covers this in unit tests; E2E confirms slot reuse).

---

## Shared Patterns

### Single-goroutine writer invariant
**Source:** `internal/adapter/openai/sse.go` line 65 comment and `internal/adapter/anthropic/sse.go` line 138 comment
**Apply to:** `internal/adapter/ollama/ndjson.go`
```
// D-05: w + flusher are touched ONLY by writeData (which is called only
// from the select-loop goroutine). No mutex needed — single-goroutine
// invariant is enforced by construction.
```
The `runNDJSONEmitter` function must be the sole writer. The watchdog goroutine (`context.AfterFunc`) must NEVER touch `w` or `flusher`.

### Flusher-before-headers pattern
**Source:** `internal/adapter/openai/sse.go` lines 159-170 and `internal/adapter/anthropic/sse.go` lines 291-301
**Apply to:** `internal/adapter/ollama/ndjson.go`
```go
flusher, ok := w.(http.Flusher)
if !ok {
    return errors.New("ollama: response writer is not flusher")
}
// Set headers BEFORE WriteHeader(200):
w.Header().Set("Content-Type", "application/x-ndjson")
w.Header().Set("Cache-Control", "no-cache")
w.WriteHeader(http.StatusOK)
```
Caller (`handleChat`, `handleGenerate`) must check if `runNDJSONEmitter` returns this specific error while headers are not yet sent, and call `writeError(w, 500, ...)` instead.

### Truncate-on-post-header error
**Source:** `internal/adapter/openai/sse.go` `finalizeSSE` lines 213-221
**Apply to:** `internal/adapter/ollama/ndjson.go` `finalizeNDJSON`
```go
final, rerr := run.Stream().Result()
if rerr != nil {
    e.logger.Debug("openai: sse stream result error", "err", rerr)
    return fmt.Errorf("openai: sse stream result: %w", rerr)
}
```
Once NDJSON headers are written, no JSON 500 is possible. Terminal errors stop emission, debug-log, return the error. No `{done:true,error:...}` line (D-05).

### goleak gate
**Source:** `internal/engine/testmain_test.go` lines 17-19
**Apply to:** `internal/engine/watchdog_test.go`, any new test packages
```go
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```
Any new test file in `internal/engine/` is already covered by the existing `TestMain`. For `internal/acp/cancel_test.go`, check if `internal/acp/` already has a `TestMain` with goleak; if not, add one.

### writeError helper
**Source:** `internal/adapter/ollama/handlers.go` lines 283-287
**Apply to:** `handleChat`, `handleGenerate` error paths before headers are written
```go
func writeError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
```
This is the Ollama error shape. Use it for 503 (engine nil), 400 (missing fields), 500 (engine error), and Flusher-not-supported before streaming starts.

### idempotent channel close
**Source:** `internal/acp/fakeacp_test.go` lines 163-168
**Apply to:** `cancelSeen` close in extended `fakeACPServer.serve`
```go
select {
case <-f.permissionResponseReceived:
    // already closed
default:
    close(f.permissionResponseReceived)
}
```
Copy this pattern for `cancelSeen`.

---

## No Analog Found

All files have close analogs. The following aspects have no direct codebase analog but are covered by RESEARCH.md patterns:

| Aspect | File | Reason | Use Instead |
|---|---|---|---|
| `context.AfterFunc` watchdog | `internal/engine/engine.go` | No existing Go 1.21+ AfterFunc usage in codebase | RESEARCH.md Pattern 2 — verified against Go stdlib docs |
| NDJSON intermediate line struct | `internal/adapter/ollama/ndjson.go` | `ollamaChatResponse` only has `done:true`; no streaming struct exists | RESEARCH.md Pitfall 7 + Pattern 7 — define `ndjsonChatLine` inline |
| `*bool` stream field | `internal/adapter/ollama/wire.go` | `ollamaStubStreamRequest.Stream *bool` at line 207 is the closest but for a different shape | Follow the same `*bool json:"stream,omitempty"` pattern |

---

## Metadata

**Analog search scope:** `internal/adapter/openai/`, `internal/adapter/anthropic/`, `internal/adapter/ollama/`, `internal/engine/`, `internal/acp/`, `tests/e2e/`
**Files scanned:** 13 source files + 2 test files read in full
**Key constraint verified:** No new external dependencies — all patterns use stdlib (`context`, `encoding/json`, `net/http`) + `go.uber.org/goleak` already in `go.mod`
**Pattern extraction date:** 2026-05-24
