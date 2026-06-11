---
finding: H-4
severity: M
rel_id: REL-HTTP-04
status: confirmed
target_phase: 16
verified_at: "2026-06-11"
---

# H-4: No Per-Request Body-Read Deadline — Stalled Upload Parks Handler Indefinitely

## Review Citation

**Source:** `docs/reviews/2026-06-11-reliability-review.md` §H-4 (section "2. HTTP surface reliability")

> `ReadHeaderTimeout: 10s` covers headers only; `IdleTimeout: 120s` covers between requests. Once headers arrive, a client that stalls while sending a POST body parks the handler goroutine inside `decodeJSONBody` with no deadline — until kernel TCP keepalive reaps it (~2h default). Wi-Fi drops or the machine sleeps mid-upload from LangFlow; each occurrence leaks a goroutine + fd + connection for hours.

Cited source file:lines: `internal/server/server.go:347-360`.

## Current-Source Check

**`internal/server/server.go:347-360`** — `http.Server` construction verified:

```go
srv := &http.Server{
    Addr:              s.addr,
    Handler:           s.router,
    ReadHeaderTimeout: 10 * time.Second, // mitigates Slowloris (gosec G112)
    // Audit server-http-no-idle-readtimeout: ...
    IdleTimeout: 120 * time.Second,
    // WriteTimeout intentionally OMITTED
}
```

Confirmed:
- `ReadHeaderTimeout: 10 * time.Second` — covers headers only (Slowloris mitigation).
- `IdleTimeout: 120 * time.Second` — covers the keep-alive gap between requests.
- `ReadTimeout` — **absent**. A blanket `ReadTimeout` would also terminate in-flight SSE/NDJSON reads (the comment at line 350-359 explicitly explains this omission).
- No per-request `http.ResponseController.SetReadDeadline()` call before any `decodeJSONBody` invocation in any handler.

The comment explains the design intent: `WriteTimeout` is omitted to avoid truncating legitimate long streaming responses. However, the body-read phase (before streaming begins) is left fully unbounded — once headers are received, a client can stall sending the JSON body indefinitely.

All adapter handlers (OpenAI `handleChatCompletions`, Ollama `handleChat`/`handleGenerate`, Anthropic `handleMessages`) call `decodeJSONBody` which wraps the request body with `http.MaxBytesReader` (size cap) but not a time deadline.

## Evidence

Regression test file: `internal/server/regression_rel_http_04_test.go`
Function: `TestRegression_REL_HTTP_04_BodyReadDeadlineMissing`

The test drives `srv.ServeHTTP` with a `stalledReader` that blocks all `Read()` calls on a gate channel. A 3-second watchdog confirms the handler has NOT returned within that window — the body-read is unbounded. After the watchdog fires, the gate and request context are released, allowing the handler to exit and preventing a goroutine leak. The test is currently `t.Skip`'d per D-12 and will be unskipped in the Phase 16 fix commit.

The fix per review: use `http.ResponseController.SetReadDeadline(time.Now().Add(30*time.Second))` before `decodeJSONBody` in each chat handler, then clear it afterwards with `time.Time{}`. This bounds body reads without touching streaming writes (which have their own idle watchdog via `STREAM_IDLE_TIMEOUT_SEC`).

## Verdict

**CONFIRMED** — The `http.Server` construction at `server.go:347-360` lacks `ReadTimeout` and no handler uses `SetReadDeadline` around body decoding. A client that sends headers but stalls mid-body will park a handler goroutine indefinitely. The per-request `http.MaxBytesReader` cap (4 MiB) bounds size but not duration.

Assigned to Phase 16 for fix (Medium severity; H-1/H-2/H-3 High findings have priority in Phase 15).
