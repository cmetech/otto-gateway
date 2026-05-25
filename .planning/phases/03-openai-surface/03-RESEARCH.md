# Phase 3: OpenAI Surface - Research

**Researched:** 2026-05-24
**Domain:** OpenAI-compatible HTTP surface (`/v1/chat/completions` + SSE, `/v1/completions`, `/v1/models`) over the shared canonical engine; chi multi-surface co-mount on `/v1`; Pi-SDK client contract
**Confidence:** HIGH

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

- **D-01: Refactor `server.Config` to a generic `Surfaces []SurfaceMount` list and group mounts by shared prefix.** chi panics on a duplicate `r.Route(prefix,…)` AND on a duplicate `r.Mount("/", …)` within one block. Fix: a `SurfaceMount{Prefix string, Router chi.Router}` list; `NewFromConfig` groups by prefix and opens **one** auth-wrapped `r.Route(prefix, …)` block per unique prefix onto which all surfaces sharing that prefix register routes. Anthropic `POST /messages` and OpenAI `POST /chat/completions` + `POST /completions` + `GET /models` do not collide → one `/v1` router hosts both. Ollama (`/api`) is just another list entry. The `OllamaVersionHandler` exempt-route mechanism (outer router, auth-exempt per AUTH-03) stays as-is; OpenAI has no `/version` equivalent.
  - **Planner's discretion on composition mechanics:** `RegisterRoutes(r chi.Router)` per adapter vs. server copying a returned subrouter's routes vs. `r.Mount` at distinct non-`/` subpaths. The SurfaceMount grouping is the locked contract. Existing `ProtectedRouter() chi.Router` accessors may be kept/adapted or replaced.
- **D-02: OpenAI ships SSE day-one alongside non-streaming — NOT deferred to Phase 4.** Handler branches on the request `stream` field: `true` → SSE via `engine.Run(ctx, req)` ranging `run.Stream().Chunks()`; `false` → JSON via `engine.Collect`. SSE emits OpenAI `chat.completion.chunk` frames as `data: <json>\n\n` lines terminated by a literal `data: [DONE]\n\n` frame (no `event:` lines, no per-block start/stop, no ping ticker required by the OpenAI SDK). First delta carries `role:"assistant"`; final pre-`[DONE]` chunk carries `finish_reason`. Single select-loop in the handler goroutine touches the writer (mirror 3.1 D-05). Disconnect handling: ctx-propagation-only (mirror 3.1 D-06); debug log on `ctx.Done` welcome. Explicit `session/cancel` is Phase 4.
- **D-03: `/v1/models` full; `/v1/completions` minimal shim.** `/v1/models` reflects the pool `ModelCatalog` (same source `/api/tags` uses). `/v1/completions` (legacy): map `prompt` string (or array → joined) to a single canonical user `Message`, run the engine, render `object:"text_completion"`, `choices[].text`, `choices[].finish_reason`, `usage` zeros. Advanced params (`logprobs`, `echo`, `suffix`, `best_of`, `n>1`) accepted-and-ignored. Whether `/v1/completions` honors `stream` is planner's discretion.
- **D-04: `/v1/models` mirrors the pool `ModelCatalog`; inbound `model` handled like Anthropic 3.1.** Expose kiro-cli `availableModels` from the pool `ModelCatalog` in OpenAI shape. Static synthetic list is rejected. Inbound `model:"auto"` or empty → skip `engine.SetModel`; any other string → SetModel. OpenAI adapter receives a `ModelCatalog`-equivalent injected the same way Ollama's adapter receives `pool`.
- **D-05: `ENABLED_SURFACES` default widens to `ollama,anthropic,openai` and `openai` joins the `validateEnabledSurfaces` allow-list.** Update default slice in `config.Load`, add `"openai"` to the allowed-names set, update the error message. `OPENAI_PATH_PREFIX` already exists (default `/v1`) and is CLI-flag-wired — no new config field for the prefix.

**Carry-forward (locked by precedent):** engine surface unchanged (`engine.Run` + `engine.Collect`, no `engine.Cancel`); `auth.Bearer` already dual-header (`Authorization: Bearer` + `x-api-key`); `usage` honest zeros; adapter-local `writeError` with OpenAI error envelope; `decodeJSONBody` + 4 MiB body-cap reuse; message `id` via `chatcmpl-<unix-nano>` or UUID (planner picks).

### Claude's Discretion

- Composition mechanics for two adapters sharing one prefix-router (D-01) — `RegisterRoutes(r)` vs adapted `ProtectedRouter()` vs distinct-subpath mounts.
- Whether to keep or replace the existing per-surface `ProtectedRouter()` accessors when introducing `SurfaceMount` (D-01).
- File-scoped split inside `internal/adapter/openai/`: likely `adapter.go`, `handlers.go`, `wire.go` (decode), `render.go` (non-streaming encode), `sse.go` (streaming emitter), `errors.go` (envelope helper) — mirror Anthropic; no `stub.go`.
- Whether `/v1/completions` honors `stream` (D-03).
- `message id` generation strategy (chatcmpl-<nano> vs UUID).
- SSE test approach (httptest + bufio.Scanner over the event-stream framing vs fake client) — httptest likely sufficient; real Pi-SDK round-trip is HUMAN-UAT.
- Whether the OpenAI chunk channel needs a keepalive comment frame — OpenAI SDK does not require pings; default to none unless research shows Pi needs one. **(Research confirms: NO keepalive needed — see Pitfall 6.)**

### Deferred Ideas (OUT OF SCOPE)

- Ollama NDJSON default streaming — Phase 4 (STRM-01).
- Explicit `session/cancel` on client disconnect — Phase 4 (STRM-04). Phase 3 = ctx propagation only.
- Tool dispatch / `coerceToolCall` / OpenAI JSON-string tool-call rendering — Phase 6 (TOOL-01..03). Phase 3 renders the OpenAI tool-call shape if chunks arrive but does NOT coerce JSON-as-text or execute tools.
- `/v1/completions` advanced params (`logprobs`, `echo`, `suffix`, `best_of`, `n>1`) — accepted-and-ignored.
- `/v1/embeddings` — Phase 7.
- Real token counts in `usage` — honest zeros for now.
- Real warm pool (`POOL_SIZE > 1`), dead-slot detection, stateful sessions — Phase 5.
- Hook chain implementations — Phase 8 (empty PreHook/PostHook seam unchanged).
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| SURF-02 | `ENABLED_SURFACES` (default widens to `openai,ollama,anthropic`) enables/disables surfaces; `OPENAI_PATH_PREFIX` (default `/v1`) overridable | D-05 config edits (Standard Stack §config). `OpenAIPathPrefix` field + CLI flag already present (config.go:69-71, 215, 253-254). Only the default slice + `validateEnabledSurfaces` allow-list change. |
| SURF-04 | `POST /v1/chat/completions`, `POST /v1/completions`, `GET /v1/models` served with OpenAI-compatible shapes | Precise wire shapes documented in Code Examples §; Bifrost Go reference validates `owned_by`/`object:"model"` (types.go:857-860). Chunk SSE shape in Pattern 2. |
| SURF-06 | Pi-SDK chat CLI with `base_url=…/v1` works end-to-end | **CONFIRMED**: Pi uses the official `openai` npm SDK with `baseURL` + `apiKey` → `Authorization: Bearer`; `stream:true` is hard-coded. See Pi SDK Contract §. SC2 exercises the **SSE** path. |
</phase_requirements>

## Summary

Phase 3 is the third instance of an already-proven pattern. The Anthropic adapter (Phase 3.1, `internal/adapter/anthropic/`) is a near-exact template: same `New(Config)` + consumer-defined `Engine`/`RunHandle`/`Stream` interfaces, same wire→canonical→render split, same per-request single-goroutine SSE select-loop, same `writeError` envelope helper, same golden-fixture + goleak test layout. The OpenAI adapter is **structurally simpler** than Anthropic: OpenAI SSE has no `event:` names, no per-block `content_block_start/stop` state machine, and — critically — **no ping keepalive requirement** (the official OpenAI SDK does not impose an idle timer the way `@anthropic-ai/sdk` does). The streaming state collapses to "first chunk emits a role delta, each text chunk emits a content delta, one final chunk carries `finish_reason`, then `data: [DONE]`."

The two genuinely new pieces of work are (1) the **D-01 server refactor** to a `Surfaces []SurfaceMount` list grouped by prefix — required because Anthropic and OpenAI both default to `/v1` and chi panics on a second `r.Mount("/", …)` into the same Route block (verified at chi mux.go:297); and (2) the **`/v1/completions` legacy shim** which Ollama/Anthropic have no analog for. Everything else (`/v1/models` from the pool catalog, the chat-completions JSON envelope, the error envelope, body-cap, model-identity handling) is a 1:1 adaptation of existing code.

The single highest-risk open item from STATE.md — the Pi SDK base-URL config key and streaming default — is now **resolved with file:line citations** (see Pi SDK Contract §): Pi configures providers in `~/.gsd/agent/models.json` with `"baseUrl": "http://localhost:11434/v1"`, `"api": "openai-completions"`, and an `apiKey`; under the hood it constructs the official `openai` npm `OpenAI` client (`new OpenAIClass({ apiKey, baseURL })`) which sends `Authorization: Bearer <apiKey>` to `POST {baseURL}/chat/completions` with `stream: true` **hard-coded**. The Pi round-trip (SC2) therefore exercises the **SSE path**, and the gateway MUST emit OpenAI-compatible `chat.completion.chunk` frames for the acceptance bar to pass.

**Primary recommendation:** Mirror `internal/adapter/anthropic/` file-for-file, deleting the SSE state machine in favor of a flat chunk→delta emitter. Refactor `server.NewFromConfig` to accept `Surfaces []SurfaceMount` and have each adapter expose a `RegisterRoutes(r chi.Router)` method that calls `r.Post(...)`/`r.Get(...)` directly (NOT `r.Mount("/", subrouter)`), so two surfaces register distinct endpoint paths onto one shared `/v1` Route subrouter without tripping chi's existing-path-Mount panic.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| OpenAI wire decode (`/v1/chat/completions`, `/v1/completions`, `/v1/models`) | API / Backend (`internal/adapter/openai`) | — | Surface-specific translation belongs in the adapter; TRST-04 forbids it touching the engine internals. |
| Canonical chat orchestration (session/prompt, chunk channel) | API / Backend (`internal/engine`) | `internal/pool` (ACP) | Engine is the single governance surface; unchanged in Phase 3 (D-01 carry-forward). |
| Auth (bearer + IP allowlist) | API / Backend (`internal/auth` via `internal/server`) | — | One auth chain wraps the whole `/v1` prefix once (D-01); adapter never re-implements auth. |
| Route composition / multi-surface mounting | API / Backend (`internal/server`) | `cmd/otto-gateway/main.go` (builds the SurfaceMount list) | D-01 grouping lives in `server.NewFromConfig`; main wires the list. |
| Model catalog (`/v1/models`) | API / Backend (`internal/pool` `ModelCatalog`) | adapter renders OpenAI shape | Same source as `/api/tags` (D-04) so the two lists match (SC3). |
| SSE framing / flush | API / Backend (`internal/adapter/openai` sse.go) | — | The adapter owns wire framing; the engine yields surface-agnostic `canonical.Chunk` values. |

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go stdlib `net/http` | go 1.23 | HTTP handlers, `http.Flusher`, `http.MaxBytesReader` | CLAUDE.md mandate; SSE needs only `Fprintf` + `Flusher.Flush` [VERIFIED: go.mod] |
| `github.com/go-chi/chi/v5` | v5.3.0 | Router, `r.Route`/`r.Post`/`r.Get`, middleware chain | CLAUDE.md mandate; already the project router [VERIFIED: go.mod] |
| `encoding/json` (stdlib) | go 1.23 | Decode request bodies, marshal chunk/response payloads | Field-order-load-bearing for golden tests (mirror anthropic/sse.go:30) [VERIFIED: codebase] |
| `log/slog` (stdlib) | go 1.23 | Structured logs via injected `*slog.Logger` (no `SetDefault`) | CLAUDE.md + project convention [VERIFIED: codebase] |
| `go.uber.org/goleak` | v1.3.0 | Goroutine-leak gate in `testmain_test.go` (TRST-05) | Already used by every adapter test package [VERIFIED: go.mod] |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `crypto/rand` + `encoding/hex` (stdlib) | go 1.23 | Opaque message id (`chatcmpl-…`) | If planner picks a random id over `unix-nano`; mirror anthropic/render.go:197 `genMessageID` [VERIFIED: codebase] |
| `pgregory.net/rapid` or stdlib `testing/quick` | — | Property tests for wire→canonical decode (TRST-06) | If the planner adds a `buildAcpBlocks`-equivalent OpenAI decode property test (TRST-06 names "OpenAI variants") [CITED: REQUIREMENTS.md TRST-06] |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `RegisterRoutes(r chi.Router)` per adapter (D-01 mechanic) | Keep `ProtectedRouter() chi.Router` and have server iterate `.Routes()` copying each onto the shared subrouter | chi `Mux.Routes()` returns route metadata, not re-mountable handlers cleanly; copying is fiddly. `RegisterRoutes` is simpler and avoids the existing-path Mount panic entirely. **Recommend `RegisterRoutes`.** |
| Two adapters on one `/v1` Route block | Move Anthropic to `ANTHROPIC_PATH_PREFIX=/anthropic/v1` | Operator-facing change; SURF-08 wants them co-located by default. The SurfaceMount grouping handles both (co-mount when prefixes equal, separate blocks when they differ). |

**Installation:** No new dependencies. All stack members are already in `go.mod`.

**Version verification:** `go.mod` pins `go 1.23`, `github.com/go-chi/chi/v5 v5.3.0`, `go.uber.org/goleak v1.3.0` [VERIFIED: go.mod, read 2026-05-24]. No external package install is required for this phase.

## Package Legitimacy Audit

> Not applicable — Phase 3 installs **zero** new external packages. All libraries are stdlib or already-vendored (`chi/v5`, `goleak`). No registry lookup or slopcheck needed.

## Pi SDK Contract (SURF-06 / SC2 acceptance bar — RESOLVED)

This resolves the STATE.md blocker: *"Pi SDK env var / config key for setting OpenAI base URL needs verification before Phase 3 starts."*

**(a) The exact config key for the OpenAI-compatible base URL:**

Pi (loop24-client / GSD Pi, package `@loop24/pi-ai`) configures providers in `~/.gsd/agent/models.json`. The base-URL key is **`baseUrl`** (camelCase) on a provider block, paired with `"api": "openai-completions"` and an `apiKey`:

```json
{
  "providers": {
    "otto": {
      "baseUrl": "http://localhost:11434/v1",
      "api": "openai-completions",
      "apiKey": "ollama",
      "models": [{ "id": "auto" }]
    }
  }
}
```
[CITED: ~/Projects/repos/local/loop24-client/docs/user-docs/custom-models.md:20-34, 110 — "`baseUrl` | API endpoint URL"]

Notes for the planner:
- `apiKey` is **required by Pi** even when the backend ignores it ("The `apiKey` is required but Ollama ignores it, so any value works" — custom-models.md:36). To exercise the gateway's bearer auth, set `apiKey` to the gateway's `AUTH_TOKEN` value (or use `authHeader: true` which "add[s] `Authorization: Bearer <apiKey>` automatically" — custom-models.md:114).
- The base URL must include the `/v1` suffix because the underlying SDK appends `/chat/completions` to `baseURL`. So `baseUrl: "http://localhost:11434/v1"` → requests hit `POST http://localhost:11434/v1/chat/completions`. [VERIFIED: openai-shared.ts:90-96 sets `baseURL: model.baseUrl` on the official `openai` client.]
- Some OpenAI-compatible servers need `compat.supportsDeveloperRole: false` (sends system as a `system` message instead of the `developer` role) — the gateway's wire decoder should accept BOTH `system` and `developer` roles defensively (custom-models.md:38). See Pitfall 4.

**(b) Does Pi's chat path default to streaming?**

**YES — Pi always streams.** The `openai-completions` provider hard-codes `stream: true`:

```ts
const params: OpenAI.Chat.Completions.ChatCompletionCreateParamsStreaming = {
    model: model.id,
    messages,
    stream: true,
};
```
[VERIFIED: ~/Projects/repos/local/loop24-client/packages/pi-ai/src/providers/openai-completions.ts:359-363]

It also sets `stream_options: { include_usage: true }` unless `compat.supportsUsageInStreaming === false` (openai-completions.ts:365-367). The gateway should tolerate `stream_options` in the request body (accept-and-ignore) and MAY emit a final `usage` chunk with honest zeros (see Pitfall 7).

**Transport & auth (load-bearing):** Pi constructs the **official `openai` npm SDK** client — `new OpenAIClass({ apiKey, baseURL: model.baseUrl, defaultHeaders })` (openai-shared.ts:89-96). The official SDK sends `Authorization: Bearer <apiKey>`. The gateway's existing `auth.Bearer` middleware already reads `Authorization: Bearer` (3.1 D-15), so **no auth changes are needed** — the Pi key flows straight through.

**Implication for SC2:** The Pi round-trip exercises the **SSE streaming path**, not the `stream:false` JSON path. The gateway MUST emit correct OpenAI `chat.completion.chunk` frames terminated by `data: [DONE]\n\n` for SC2 to pass. The `stream:false` JSON path is still required by SC1 (the explicit `curl … "stream":false`) but is NOT what Pi drives.

## Architecture Patterns

### System Architecture Diagram

```
                         ┌─────────────────────────────────────────────┐
  Pi-SDK CLI             │             OTTO Gateway (one port)          │
  (openai npm SDK,       │                                              │
   stream:true,          │   chi router (outer middleware:              │
   Bearer auth) ─────────┼──▶ RequestID → Recoverer → accessLog)        │
       POST /v1/         │        │                                     │
       chat/completions  │        ├─ exempt: GET / , GET /health        │
                         │        │           GET /api/version          │
  curl (stream:false) ───┤        │                                     │
                         │        ▼                                     │
                         │   r.Route("/v1", …)  ◀── ONE block per prefix│
                         │     ├ auth.Bearer (dual-header)              │   (D-01 grouping)
                         │     ├ auth.IPAllowlist                       │
                         │     │                                        │
                         │     ├─ openai.RegisterRoutes(r):             │
                         │     │     POST /chat/completions  ──┐        │
                         │     │     POST /completions          │       │
                         │     │     GET  /models               │       │
                         │     └─ anthropic.RegisterRoutes(r):  │       │
                         │           POST /messages             │       │
                         │                                      ▼       │
                         │   handler branches on `stream`:              │
                         │     stream:false → engine.Collect ──▶ render │
                         │     stream:true  → engine.Run ──▶ sse emitter│
                         │                       │                      │
                         │                       ▼                      │
                         │   internal/engine  (UNCHANGED)               │
                         │     pickCwd → buildBlocks → ACP session       │
                         │       NewSession → [SetModel] → Prompt        │
                         │                       │                      │
                         │                       ▼  canonical.Chunk chan │
                         │   internal/pool ──▶ kiro-cli ACP subprocess   │
                         │   ModelCatalog.Models() ──▶ GET /v1/models    │
                         └─────────────────────────────────────────────┘
```

Trace the Pi use case: Pi → `POST /v1/chat/completions {stream:true}` → auth chain → `openai` handler → `engine.Run` → ranges `run.Stream().Chunks()` → sse emitter writes `data: {chunk}\n\n` per chunk → `data: [DONE]\n\n` → Pi SDK parses the stream.

### Recommended Project Structure

Mirror `internal/adapter/anthropic/` (planner's discretion on exact file split; this is the recommended layout):

```
internal/adapter/openai/
├── adapter.go      # New(Config) + RegisterRoutes(r chi.Router); consumer-defined Engine/RunHandle/Stream/ModelCatalog interfaces
├── wire.go         # request structs + wireToChatRequest (chat) + promptToChatRequest (completions shim)
├── render.go       # non-streaming encode: chat.completion + text_completion + models list
├── sse.go          # streaming chat.completion.chunk emitter + single-goroutine select-loop
├── errors.go       # OpenAI error envelope helper (writeError) + writeJSON
├── decode.go       # decodeJSONBody + isMaxBytesError (copy from anthropic/decode.go verbatim)
└── *_test.go       # adapter_test, wire_test, render_test, sse_test, sse_golden_test, integration_test, testmain_test (goleak)
```

No `stub.go` (OpenAI has no Ollama-style stub endpoints). No `decode.go` divergence — the anthropic generic `decodeJSONBody[T]` is reusable as-is.

### Pattern 1: Consumer-defined Engine interface (TRST-04 boundary)

**What:** The adapter declares `Engine`, `RunHandle`, `Stream` interfaces locally and NEVER imports `internal/engine`. `cmd/main.go` wires a thin adapter (`openaiEngineAdapter`) bridging `*engine.Engine` to the local interface.
**When to use:** Always — this is the locked layout for all three adapters.
**Example:** Copy `internal/adapter/anthropic/adapter.go:35-71` (the `Engine`/`RunHandle`/`Stream` triple). For the OpenAI adapter, ALSO declare a `ModelCatalog` interface (mirror `internal/adapter/ollama/adapter.go:49-51`: `Models() []canonical.ModelInfo`) since `/v1/models` needs the pool catalog (D-04) — Anthropic had no catalog, Ollama did.

```go
// Source: internal/adapter/ollama/adapter.go:49-51 (catalog) + anthropic/adapter.go:35-44 (engine)
type Engine interface {
    Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
    Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error)
}
type ModelCatalog interface {
    Models() []canonical.ModelInfo
}
```

The `cmd/main.go` wiring already has the exact bridge for Anthropic (`anthropicEngineAdapter` + `anthropicRunHandleAdapter`, main.go:292-351). The OpenAI adapter needs the **same** `Run`/`Collect` bridge — copy it as `openaiEngineAdapter`. For the catalog, `a.pool` already satisfies `ollama.ModelCatalog` via `Models() []canonical.ModelInfo` (main.go:171-174); pass the same `a.pool` to the OpenAI adapter's `ModelCatalog` field.

### Pattern 2: Flat SSE chunk emitter (OpenAI shape — SIMPLER than Anthropic)

**What:** A single goroutine (the handler goroutine) owns the `http.ResponseWriter`. It ranges the canonical chunk channel and writes one `data: <json>\n\n` frame per content chunk, then a final `finish_reason`-bearing chunk, then `data: [DONE]\n\n`.
**When to use:** `stream:true` branch of `/v1/chat/completions` (D-02).
**Key differences vs. Anthropic sse.go:** No `event:` lines. No `content_block_start/stop` state machine. No ping ticker (Pitfall 6). The emitter is roughly half the size of `anthropic/sse.go`.

```go
// Source: derived from public OpenAI chat-streaming spec + anthropic/sse.go single-goroutine pattern
// chunk frame shape per https://platform.openai.com/docs/api-reference/chat-streaming/streaming

type chatCompletionChunk struct {
    ID      string       `json:"id"`      // "chatcmpl-…" — SAME id every chunk
    Object  string       `json:"object"`  // always "chat.completion.chunk"
    Created int64        `json:"created"` // unix seconds, fixed per response
    Model   string       `json:"model"`
    Choices []chunkChoice `json:"choices"`
}
type chunkChoice struct {
    Index        int        `json:"index"`         // always 0 (n>1 unsupported)
    Delta        chunkDelta `json:"delta"`
    FinishReason *string    `json:"finish_reason"` // null until final chunk
}
type chunkDelta struct {
    Role    string `json:"role,omitempty"`    // "assistant" on FIRST chunk only
    Content string `json:"content,omitempty"` // text fragment on subsequent chunks
}

// Emission sequence (single goroutine):
//   1. First frame: delta={"role":"assistant"}, finish_reason=null
//   2. Per ChunkKindText: delta={"content":"<fragment>"}, finish_reason=null
//   3. Final frame: delta={}, finish_reason="stop" (mapped from canonical StopReason)
//   4. Literal terminator: data: [DONE]\n\n
//   On ctx.Done: debug-log disconnect + return (no [DONE]); ACP unwinds via engine ctx.
```

Frame writer (mirror anthropic/sse.go:163-173 `writeEvent`, minus the `event:` line):
```go
func (e *sseEmitter) writeData(payload any) error {
    body, err := json.Marshal(payload)
    if err != nil { return fmt.Errorf("openai: marshal chunk: %w", err) }
    if _, err := fmt.Fprintf(e.w, "data: %s\n\n", body); err != nil {
        return fmt.Errorf("openai: write chunk: %w", err)
    }
    e.flusher.Flush()
    return nil
}
// terminator: fmt.Fprintf(e.w, "data: [DONE]\n\n"); e.flusher.Flush()
```

Select-loop (mirror anthropic/sse.go:359-381 but with NO `tickerC` case):
```go
for {
    select {
    case <-ctx.Done():
        e.logger.Debug("openai: sse client disconnect", "session_id", run.SessionID())
        return fmt.Errorf("openai: sse ctx: %w", ctx.Err())
    case c, ok := <-chunks:
        if !ok { return finalizeStream(e, run) } // emit finish_reason frame + [DONE]
        if err := e.applyChunk(c); err != nil { return err }
    }
}
```

**finish_reason mapping** (canonical.StopReason → OpenAI enum): `StopEndTurn`→`"stop"`, `StopMaxTokens`→`"length"`, `StopRefusal`→`"content_filter"` (closest), `StopCancelled`→`"stop"`, `StopUnknown`→`"stop"` (OpenAI's `finish_reason` on the terminal chunk is non-null; never emit `null` on the final frame). [CITED: OpenAI chat-streaming spec — enum values `stop`/`length`/`content_filter`/`tool_calls`]. Mirror the existing `ollama/render.go:115-124` and `anthropic/render.go:166-183` `mapStopReason` shape.

### Pattern 3: Non-streaming chat.completion envelope (stream:false / SC1)

**What:** `engine.Collect` → render the OpenAI `chat.completion` object.
**Example:**
```go
// Source: public OpenAI chat/create spec + Bifrost core/providers/openai/types.go (owned_by/object shapes)
type chatCompletion struct {
    ID      string         `json:"id"`      // "chatcmpl-…"
    Object  string         `json:"object"`  // "chat.completion"
    Created int64          `json:"created"` // unix seconds
    Model   string         `json:"model"`
    Choices []completionChoice `json:"choices"`
    Usage   usage          `json:"usage"`   // honest zeros (D-12 carry-forward)
}
type completionChoice struct {
    Index        int            `json:"index"`         // 0
    Message      responseMessage `json:"message"`      // {"role":"assistant","content":"…"}
    FinishReason string         `json:"finish_reason"` // "stop" | "length" | … (non-null)
}
type usage struct {
    PromptTokens     int `json:"prompt_tokens"`     // 0
    CompletionTokens int `json:"completion_tokens"` // 0
    TotalTokens      int `json:"total_tokens"`      // 0
}
```
Walk `resp.Message.Content` joining `ContentKindText` parts (mirror `ollama/render.go:135-146 joinTextContent`). Defensive empty: if no text, emit `content:""` (mirror anthropic/render.go:140-142).

### Pattern 4: `/v1/completions` legacy shim (D-03)

**What:** Map `prompt` (string or `[]string` joined) → one canonical user `Message`, run engine, render `text_completion` shape.
```go
type textCompletion struct {
    ID      string `json:"id"`      // "cmpl-…"
    Object  string `json:"object"`  // "text_completion"
    Created int64  `json:"created"`
    Model   string `json:"model"`
    Choices []textChoice `json:"choices"`
    Usage   usage  `json:"usage"`
}
type textChoice struct {
    Index        int     `json:"index"`
    Text         string  `json:"text"`          // assistant text (not a message object)
    FinishReason string  `json:"finish_reason"`
    Logprobs     *struct{} `json:"logprobs"`    // always null (D-03 accept-and-ignore)
}
```
[CITED: https://platform.openai.com/docs/api-reference/completions/create]. Advanced params (`logprobs`, `echo`, `suffix`, `best_of`, `n`) decode-and-ignore (mirror the Phase 2 `KeepAlive`/`Options` accept-and-ignore; do NOT `DisallowUnknownFields` — decode.go:21-26 already documents why). Stream support is planner's discretion (Pi uses chat-completions, not completions).

### Pattern 5: `/v1/models` from pool catalog (D-04)

```go
// Source: ollama/handlers.go:106-117 handleTags pattern, OpenAI shape per spec
type modelList struct {
    Object string      `json:"object"` // "list"
    Data   []modelInfo `json:"data"`
}
type modelInfo struct {
    ID      string `json:"id"`
    Object  string `json:"object"`   // "model"
    Created int64  `json:"created"`  // unix seconds (a fixed/boot timestamp is fine)
    OwnedBy string `json:"owned_by"` // "kiro" or "otto-gateway"
}
```
Prepend `"auto"` (mirror ollama/handlers.go:108-109 Node parity), then iterate `a.cfg.ModelCatalog.Models()`. When catalog is nil (KIRO_CMD unset), return only `"auto"`. [VERIFIED: Bifrost core/providers/openai/types.go:857-860 confirms `ID`/`Object`/`OwnedBy`/`Created` field names; CITED: OpenAI models/list spec.] SC3 requires `/v1/models` and `/api/tags` reflect the same set — both iterate the same `pool.Models()`, so they match by construction.

### Anti-Patterns to Avoid

- **Re-implementing auth in the adapter.** The `/v1` Route block applies `auth.Bearer` + `auth.IPAllowlist` once (D-01). The adapter handlers never check tokens.
- **A second `r.Mount("/", subrouter)` in one Route block.** chi panics (mux.go:297 "attempting to Mount() a handler on an existing path"). Use `RegisterRoutes` that calls `r.Post`/`r.Get` directly.
- **Emitting `finish_reason: null` on the terminal chunk.** The final chunk MUST carry a non-null `finish_reason`; the SDK keys stream completion on it.
- **Adding a ping/keepalive frame.** OpenAI SDK does not require it (Pitfall 6). Do not copy Anthropic's ticker.
- **`DisallowUnknownFields` on decode.** Breaks accept-and-ignore for `stream_options`, `logprobs`, future SDK fields (decode.go:21-26).
- **Echoing `err.Error()` in error responses.** T-02-33: log raw error via slog, return a generic message (mirror anthropic/handlers.go:108-112).

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| JSON body size cap | Manual `io.LimitReader` + length check | `decodeJSONBody[T]` + `http.MaxBytesReader` (copy anthropic/decode.go:27-38) | Already handles the 413 vs 400 distinction via `isMaxBytesError` |
| SSE flush | Manual buffering | `w.(http.Flusher).Flush()` after each `Fprintf` (anthropic/sse.go:290-296, 171) | Stdlib pattern; assert Flusher BEFORE writing headers |
| Engine bridge (concrete→interface) | New abstraction | `openaiEngineAdapter` copying anthropic main.go:292-351 | Go return-type invariance requires the explicit wrapper; already solved for Anthropic |
| Goroutine-leak detection | Manual wait loops | `goleak.VerifyTestMain` in testmain_test.go (anthropic/testmain_test.go) | TRST-05; proves the SSE handler is leak-free |
| Golden SSE byte comparison | Inline string asserts | `compareGolden` + `testdata/*.golden` (anthropic/sse_golden_test.go:79-95) | Field-order regressions caught byte-exact; normalize the random id |
| Stop-reason mapping | Inline switch in handler | A `mapFinishReason` helper in render.go (mirror ollama/render.go:115-124) | Single source of truth; testable |

**Key insight:** This adapter is ~70% copy-paste from `internal/adapter/anthropic/` with the SSE state machine *removed*. The risk is not building too little; it's accidentally over-building the SSE emitter by porting Anthropic's block-index/ping machinery that OpenAI does not need.

## Runtime State Inventory

> Phase 3 is a greenfield adapter addition (new code under `internal/adapter/openai/` + a server refactor + two config-line edits). It renames/migrates **no stored data, no live service config, no OS-registered state**. The only state-adjacent change is the `ENABLED_SURFACES` default value.

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | None — no datastore keys/collections involve the surface name. | None. |
| Live service config | **`ENABLED_SURFACES` default changes from `ollama,anthropic` → `ollama,anthropic,openai` (D-05).** Existing deployments that set `ENABLED_SURFACES` explicitly are unaffected; those relying on the default will newly expose `/v1/chat/completions`. Pi config (`~/.gsd/agent/models.json`) is operator-managed, not gateway state. | Document the widened default in the boot log (main.go:243-248 already logs enabled surfaces). |
| OS-registered state | None — no scheduled tasks, pm2/launchd/systemd units reference the surface. | None. |
| Secrets/env vars | None new. `AUTH_TOKEN` reused unchanged (dual-header auth already serves OpenAI's `Authorization: Bearer`). `OPENAI_PATH_PREFIX` already exists in config (config.go:69-71). | None — verified `OpenAIPathPrefix` field + `--openai-path-prefix` flag already present. |
| Build artifacts | None — no package rename; `cmd/otto-gateway` binary name unchanged. | None. |

## Common Pitfalls

### Pitfall 1: chi double-Mount panic on the shared `/v1` prefix (the D-01 trap)
**What goes wrong:** Adding OpenAI as a second `r.Route("/v1", …)` block (Anthropic already has one), OR mounting two subrouters via `r.Mount("/", …)` inside one `/v1` block, panics at startup.
**Why it happens:** chi `Mux.Mount` panics on "attempting to Mount() a handler on an existing path" [VERIFIED: chi mux.go:297, read 2026-05-24]; `Route` on a duplicate pattern likewise conflicts.
**How to avoid:** D-01's `Surfaces []SurfaceMount` grouped by prefix → one Route block per unique prefix. Each adapter exposes `RegisterRoutes(r chi.Router)` that calls `r.Post("/chat/completions", …)`, `r.Post("/completions", …)`, `r.Get("/models", …)` (OpenAI) and `r.Post("/messages", …)` (Anthropic) directly onto the shared `r`. Distinct endpoint paths → no collision.
**Warning signs:** Panic message containing `chi: attempting to Mount()` or `chi: '…' http method` at boot when both surfaces are enabled.

### Pitfall 2: `Content-Type`/`WriteHeader` order on the SSE path
**What goes wrong:** Setting headers after `WriteHeader(200)` silently drops them; the SDK then mis-parses the stream as JSON.
**Why it happens:** Once the status line flushes, header mutations are ignored.
**How to avoid:** Assert `http.Flusher` capability BEFORE writing any bytes (so the caller can still render a JSON 500 if it's missing — anthropic/sse.go:290-296), then set `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, THEN `WriteHeader(200)` (anthropic/sse.go:298-301).
**Warning signs:** Pi SDK errors like "Unexpected token" or a hung stream; `Content-Type: application/json` on a streamed response.

### Pitfall 3: Engine.Run error AFTER headers vs. BEFORE
**What goes wrong:** Returning a JSON 500 envelope after SSE headers are already written corrupts the stream.
**Why it happens:** Two distinct error windows: `engine.Run` failing (no bytes written yet → safe JSON 500) vs. a mid-stream `Result()` error (headers gone → cannot send JSON).
**How to avoid:** Mirror anthropic/handlers.go:82-104: if `engine.Run` errors, `writeError(500)` (no headers yet); if the emitter errors mid-stream, log at debug and let the emitter's own terminal handling apply. For OpenAI there is no `event: error` frame — best practice is to simply stop and let the client see a truncated stream (some gateways emit a final `data:` with an error object, but the official SDK has no error-frame contract; truncation is acceptable and matches ctx-cancel behavior).
**Warning signs:** A JSON error object appearing after `data:` lines.

### Pitfall 4: `developer` vs `system` role + role mapping from Pi
**What goes wrong:** Pi may send the system prompt under the `developer` role (reasoning models) or as a `system` message depending on `compat.supportsDeveloperRole`.
**Why it happens:** OpenAI introduced the `developer` role; Pi's `compat` toggles which it uses (custom-models.md:38).
**How to avoid:** The wire decoder must map BOTH `"system"` and `"developer"` roles → `canonical.RoleSystem`, and hoist them into `canonical.ChatRequest.System` (mirror how Anthropic hoists top-level `system` — wire.go:135-154; OpenAI carries system as a `messages[]` entry). Map `"user"`→`RoleUser`, `"assistant"`→`RoleAssistant`, `"tool"`→`RoleTool`. Unknown → `RoleUser` (canonical zero value; mirror anthropic/wire.go:354-361).
**Warning signs:** System prompt ignored; kiro-cli not honoring instructions sent by Pi.

### Pitfall 5: OpenAI `content` may be a string OR an array of content parts
**What goes wrong:** Decoding `messages[].content` as a flat `string` fails on multimodal/structured requests (`content: [{type:"text",text:"…"}]`).
**Why it happens:** OpenAI's chat-completions `content` field is polymorphic (string for simple text, array for multimodal).
**How to avoid:** Use `json.RawMessage` for `content` and try string-first then array (mirror anthropic/wire.go:43-44, 162-200). For Phase 3, join text parts; images are out of scope unless the engine already supports them via `canonical.ContentKindImage` (it does — but Pi text chat sends a plain string, so the simple path covers SC2).
**Warning signs:** 400 "invalid JSON" on a request the SDK considers valid.

### Pitfall 6: Adding an unneeded ping/keepalive (over-porting Anthropic)
**What goes wrong:** Copying Anthropic's 15s `event: ping` ticker into the OpenAI emitter adds complexity and emits frames the OpenAI SDK does not expect.
**Why it happens:** The Anthropic SDK starts a 60s idle timer; the OpenAI SDK does NOT impose an equivalent idle-disconnect on the stream.
**How to avoid:** Omit the ticker entirely. The OpenAI select-loop has only `ctx.Done` and `chunks` cases (no `tickerC`). [VERIFIED: Pi uses the official `openai` SDK which streams via `for await (const chunk of stream)` with no client-side idle ping requirement — openai-completions.ts:69-330 has no ping/keepalive logic.] CONTEXT.md D-02 and `<specifics>` both confirm this; research corroborates.
**Warning signs:** None functionally harmful, but extra `data: {…"ping"…}` frames are non-spec and may confuse strict parsers.

### Pitfall 7: `stream_options.include_usage` final usage chunk
**What goes wrong:** Pi sends `stream_options: {include_usage: true}` (openai-completions.ts:365-367). Strictly, OpenAI then emits a FINAL chunk with `choices: []` and a populated `usage` object after the `finish_reason` chunk.
**Why it happens:** It's an opt-in usage-reporting extension.
**How to avoid:** Accept-and-ignore `stream_options` in decode (do not 400). Emitting the extra usage chunk is OPTIONAL — the official SDK tolerates its absence (it simply reports zero/undefined usage). If the planner wants strict fidelity, emit one final `{choices:[], usage:{prompt_tokens:0,completion_tokens:0,total_tokens:0}}` chunk before `[DONE]` (honest zeros per D-12). **Recommendation: skip it for Phase 3** (honest zeros, simpler emitter); revisit if Pi UAT shows a problem. Document as an Assumption.
**Warning signs:** Pi logging "usage: undefined" — benign for Phase 3.

### Pitfall 8: `created` timestamp must be a single fixed value per response
**What goes wrong:** Calling `time.Now().Unix()` per chunk yields different `created` values across chunks of one stream; some strict parsers expect it stable.
**Why it happens:** Naive per-frame timestamping.
**How to avoid:** Compute `created` once when the emitter is constructed and reuse it on every chunk (and the same `id`). Mirror how anthropic's `sseEmitter` holds `messageID`/`model` as fields (sse.go:142-151).
**Warning signs:** Chunks of one completion with drifting `created`.

## Code Examples

### Full handler branch (stream vs non-stream)
```go
// Source: internal/adapter/anthropic/handlers.go:34-116 (adapted — no anthropic-version header check)
func (a *Adapter) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    if a.cfg.Engine == nil {
        writeError(w, http.StatusServiceUnavailable, errAPI, "kiro-cli not configured (set KIRO_CMD)")
        return
    }
    var wire chatCompletionRequest
    if err := decodeJSONBody(w, r, chatBodyCap, &wire); err != nil {
        if isMaxBytesError(err) {
            writeError(w, http.StatusRequestEntityTooLarge, errRequestTooLarge, "request body exceeds maximum size")
            return
        }
        writeError(w, http.StatusBadRequest, errInvalidRequest, "invalid JSON: "+err.Error())
        return
    }
    if len(wire.Messages) == 0 {
        writeError(w, http.StatusBadRequest, errInvalidRequest, "`messages` is required and must be a non-empty array")
        return
    }
    req := wireToChatRequest(&wire, r) // maps model:"auto"/"" → skip SetModel (D-04)

    if wire.Stream {
        runHandle, err := a.cfg.Engine.Run(r.Context(), req)
        if err != nil {
            a.cfg.Logger.Error("openai: engine.Run error", "err", err)
            writeError(w, http.StatusInternalServerError, errAPI, "internal error")
            return
        }
        if err := runSSEEmitter(r.Context(), w, runHandle, wire.Model, a.cfg.Logger); err != nil {
            a.cfg.Logger.Debug("openai: sse emitter terminated", "err", err)
        }
        return
    }
    resp, err := a.cfg.Engine.Collect(r.Context(), req)
    if err != nil {
        a.cfg.Logger.Error("openai: engine.Collect error", "err", err)
        writeError(w, http.StatusInternalServerError, errAPI, "internal error")
        return
    }
    writeJSON(w, chatResponseToCompletion(resp, wire.Model))
}
```

### OpenAI error envelope (errors.go)
```go
// Source: public OpenAI error spec + CONTEXT.md carry-forward "writeError" bullet
// {"error":{"message":"…","type":"…","param":null,"code":null}}
type errorEnvelope struct {
    Error errorInner `json:"error"`
}
type errorInner struct {
    Message string  `json:"message"`
    Type    string  `json:"type"`
    Param   *string `json:"param"` // null
    Code    *string `json:"code"`  // null
}
const (
    errInvalidRequest  = "invalid_request_error"  // 400 / 413
    errAuthentication  = "invalid_request_error"  // 401 (OpenAI uses this type; auth middleware emits Ollama shape — see note)
    errNotFound        = "not_found_error"         // 404
    errRequestTooLarge = "invalid_request_error"   // 413 (OpenAI has no distinct request_too_large type)
    errAPI             = "api_error"               // 500
)
```
HTTP status mapping per CONTEXT.md carry-forward: 400 invalid_request_error, 401 (auth middleware — note below), 404 not_found, 413 body cap, 500 engine/pool. **Note:** like Anthropic 3.1, the `auth.Bearer` middleware that guards `/v1` emits the *Ollama* error shape on a 401, not the OpenAI shape (it's shared middleware). This is the documented Phase 3.1 behavior (anthropic/errors.go:48-52); Phase 8's surface-aware hook chain lifts it. Flag as an Assumption — acceptable for Phase 3, the official SDK still raises an auth error on a 401 regardless of body shape.

### Server SurfaceMount refactor (D-01) — recommended shape
```go
// Source: derived from internal/server/server.go:135-199 current two-block structure
type SurfaceMount struct {
    Prefix string      // "/api", "/v1"
    Router RouteRegistrar // or chi.Router — planner's discretion
}
// In NewFromConfig: group by prefix, one auth-wrapped Route block per unique prefix.
byPrefix := map[string][]SurfaceMount{}
for _, sm := range cfg.Surfaces { byPrefix[sm.Prefix] = append(byPrefix[sm.Prefix], sm) }
for prefix, mounts := range byPrefix {
    s.router.Route(prefix, func(r chi.Router) {
        r.Use(auth.Bearer(auth.Config{Logger: cfg.Logger, Tokens: cfg.AuthTokens}))
        r.Use(auth.IPAllowlist(auth.Config{Logger: cfg.Logger, AllowedPrefixes: cfg.AllowedPrefixes, TrustXForwardedFor: cfg.AuthTrustXFF}))
        for _, sm := range mounts {
            sm.Router.RegisterRoutes(r) // each adapter calls r.Post/r.Get directly
        }
    })
}
// OllamaVersionHandler stays on the OUTER router, auth-exempt (server.go:152-157) — unchanged.
```
**Map ordering caveat:** Go map iteration is non-deterministic. If route-registration order across prefixes ever matters (it does not here — prefixes are disjoint), iterate a sorted key slice. Within a prefix, registration order is the slice order, which is deterministic.

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Parallel `OllamaPath`/`AnthropicPath` fields on `server.Config` (server.go:60-74) | Generic `Surfaces []SurfaceMount` grouped by prefix (D-01) | Phase 3 | Scales to N surfaces; no bespoke branch per surface |
| `system` role only (OpenAI legacy) | `developer` role for reasoning models (Pi `compat.supportsDeveloperRole`) | OpenAI ~2024 | Decoder must accept both (Pitfall 4) |
| `max_tokens` (chat) | `max_completion_tokens` (Pi sends this for non-`max_tokens` compat — openai-completions.ts:373-378) | OpenAI 2024 | Accept-and-ignore both; gateway has no real token cap enforcement in Phase 3 |

**Deprecated/outdated:**
- `function_call`/`functions` (pre-`tools`): out of scope (tools are Phase 6); decode-and-ignore if present.
- `/v1/completions` itself is legacy at OpenAI; Phase 3 ships a minimal shim only (D-03).

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Skipping the optional `stream_options.include_usage` final usage chunk is acceptable for Pi UAT (honest-zeros, no usage chunk emitted). | Pitfall 7 | LOW — official SDK tolerates absent usage; if Pi UAT complains, emit one zero-usage chunk before `[DONE]`. |
| A2 | The 401-from-auth-middleware emits the Ollama error shape (shared middleware), not the OpenAI shape, and this is acceptable for Phase 3 (matches Anthropic 3.1 precedent). | Code Examples (errors) | LOW — SDK raises an auth error on 401 regardless of body; Phase 8 hook chain unifies it. |
| A3 | `finish_reason` for `StopRefusal` → `"content_filter"` is the closest OpenAI enum mapping. | Pattern 2 | LOW — kiro-cli rarely emits refusal; the value is informational. Planner may pick `"stop"` instead. |
| A4 | A single fixed `created` (unix seconds) + `chatcmpl-<id>` per response satisfies Pi/openai-SDK parsing. | Pitfall 8 | LOW — SDK does not validate `created` monotonicity; this is defensive correctness. |
| A5 | Emitting a truncated stream (no error frame) on a mid-stream engine error is acceptable (OpenAI SDK has no `event: error` contract). | Pitfall 3 | MEDIUM — verify in SSE test that the official SDK does not hang on a truncated stream; ctx-cancel path already does this. |

**Confirmation note:** The Pi SDK base-URL key (`baseUrl`) and streaming default (`stream:true` hard-coded) are NOT assumptions — they are VERIFIED from local source (custom-models.md:24, openai-completions.ts:362). They were the STATE.md open item and are now closed.

## Open Questions

1. **Does the official `openai` npm SDK (Pi's transport) require the final usage chunk when `stream_options.include_usage:true` is sent?**
   - What we know: Pi sends `stream_options.include_usage:true` (openai-completions.ts:365-367); the SDK exposes `chunk.usage` when present.
   - What's unclear: Whether Pi's UI errors or just shows undefined usage when the gateway omits it.
   - Recommendation: Ship without the usage chunk (A1). The Pi round-trip is HUMAN-UAT; if Pi complains, the fix is a one-line extra frame. Do NOT block planning on this.

2. **Does `/v1/completions` need `stream` support for any Phase 3 client?**
   - What we know: Pi uses `/chat/completions` exclusively (openai-completions.ts). No Phase 3 client drives `/v1/completions` streaming.
   - Recommendation (planner's discretion per D-03): ship `/v1/completions` as JSON-only; downgrade `stream:true` to false silently (mirror ollama/handlers.go:43-45) or accept-and-ignore. Keep the shim minimal.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | build/test | ✓ | go 1.23 (go.mod) | — |
| `kiro-cli` | live SSE round-trip (integration + E2E) | (runtime — set via `KIRO_CMD`) | — | Adapter degrades to 503 when `KIRO_CMD` unset (nil-engine guard); unit/golden tests use fake streams (anthropic/sse_golden_test.go:105-124) |
| `go-arch-lint` | TRST-04 boundary check (`make arch-lint`) | (CI tool) | v1.15.0 (Makefile:55) | Boundary still enforced by code review; CI installs it |
| loop24-client / Pi | SC2 HUMAN-UAT round-trip | ✓ | local at `~/Projects/repos/local/loop24-client` (`@loop24/pi-ai`) | E2E harness automates SC1/SC3; Pi round-trip is opt-in Node SDK harness (STATE quick task 260524-pee) |

**Missing dependencies with no fallback:** None — all unit/golden/integration tests run with fakes or the existing pool; Pi round-trip is HUMAN-UAT and tooling already exists.
**Missing dependencies with fallback:** None blocking.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `net/http/httptest` + `go.uber.org/goleak` v1.3.0 |
| Config file | none — Go convention; `Makefile` targets `test`, `test-race`, `arch-lint`, `ci`, `e2e` |
| Quick run command | `go test ./internal/adapter/openai/...` |
| Full suite command | `make ci` (lint + `go test -race ./...` + govulncheck + arch-lint) |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| SURF-04 | `POST /v1/chat/completions` stream:false → `chat.completion` JSON envelope (id/object/choices/usage) | unit | `go test ./internal/adapter/openai -run TestChatCompletions_NonStream` | ❌ Wave 0 |
| SURF-04/STRM-02 | `stream:true` → `data: {chunk}\n\n` frames + role-first delta + finish_reason chunk + `data: [DONE]\n\n` | golden | `go test ./internal/adapter/openai -run TestSSEGolden` | ❌ Wave 0 |
| SURF-04 | `GET /v1/models` → `{object:"list", data:[{id,object:"model",created,owned_by}]}`; "auto" present | unit | `go test ./internal/adapter/openai -run TestModels` | ❌ Wave 0 |
| SURF-04 | `POST /v1/completions` → `{object:"text_completion", choices[].text, usage zeros}`; advanced params ignored | unit | `go test ./internal/adapter/openai -run TestCompletions` | ❌ Wave 0 |
| SURF-04 | Error envelope `{"error":{message,type,param,code}}` + status map (400/404/413/500) | unit | `go test ./internal/adapter/openai -run TestErrors` | ❌ Wave 0 |
| SURF-04 | content string-or-array decode; system/developer role hoist; model:"auto" skips SetModel | unit / property | `go test ./internal/adapter/openai -run TestWire` | ❌ Wave 0 |
| SURF-02 | `ENABLED_SURFACES` default includes openai; `validateEnabledSurfaces` accepts "openai"; unknown still fails | unit | `go test ./internal/config -run TestEnabledSurfaces` | ⚠️ extend existing config_test |
| SURF-02/D-01 | Both `/v1` surfaces mount without panic; `/v1/messages` AND `/v1/chat/completions` both routable | unit | `go test ./internal/server -run TestSurfaceMount` | ❌ Wave 0 |
| SURF-06 | SSE handler leak-free; ctx-cancel returns cleanly | goleak | `go test ./internal/adapter/openai` (TestMain goleak gate) | ❌ Wave 0 (testmain_test.go) |
| TRST-04 | `internal/adapter/openai` imports only canonical (+plugin) | arch | `make arch-lint` | ⚠️ add `adapter_openai` to .go-arch-lint.yml |
| TRST-03 | All concurrency race-clean | race | `go test -race ./internal/adapter/openai/...` | ❌ Wave 0 |
| SURF-06 | Pi-SDK real round-trip (stream) | HUMAN-UAT | Pi configured with `models.json` baseUrl→gateway/v1 + bearer; opt-in Node SDK E2E harness | manual |

### Sampling Rate
- **Per task commit:** `go test ./internal/adapter/openai/...` (fast; fakes only)
- **Per wave merge:** `go test -race ./internal/adapter/openai/... ./internal/server/... ./internal/config/...`
- **Phase gate:** `make ci` green (incl. arch-lint with the new `adapter_openai` boundary) before `/gsd:verify-work`; then Pi HUMAN-UAT.

### Wave 0 Gaps
- [ ] `internal/adapter/openai/testmain_test.go` — `goleak.VerifyTestMain` (copy anthropic/testmain_test.go) — covers SURF-06 leak gate
- [ ] `internal/adapter/openai/testdata/*.golden` — SSE byte-exact fixtures (text-only stream; finish_reason chunk; [DONE] terminator) — covers STRM-02
- [ ] `internal/adapter/openai/sse_golden_test.go` — `compareGolden` + `driveGolden` harness with id normalization (`chatcmpl-…` regex) (copy + adapt anthropic/sse_golden_test.go)
- [ ] `internal/adapter/openai/integration_test.go` — full `r.Route`/httptest round-trip incl. `Content-Type: text/event-stream` assertion + `bufio.Scanner` over frames (copy anthropic/integration_test.go pattern, lines 242-243)
- [ ] `internal/server/server_test.go` — SurfaceMount-grouping test proving two `/v1` surfaces co-mount without panic (NEW — no precedent; this is the D-01 regression pin)
- [ ] `.go-arch-lint.yml` — add `adapter_openai` component + `mayDependOn: [canonical]` (mirror `adapter_anthropic`)
- [ ] Extend `internal/config/config_test.go` — assert default slice includes "openai" and allow-list accepts it (D-05)
- [ ] Framework install: none — all tooling present.

## Security Domain

> `security_enforcement` not found in `.planning/config.json` as `false` → treated as enabled. Scope limited to this surface.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | `auth.Bearer` (dual-header) wraps `/v1` once via the SurfaceMount block (D-01). No adapter-local auth. Reused unchanged. |
| V3 Session Management | no | Phase 3 is stateless per-request (warm pool); stateful sessions are Phase 5. |
| V4 Access Control | yes | `auth.IPAllowlist` on the `/v1` prefix; `/`, `/health`, `/api/version` remain exempt (AUTH-03). |
| V5 Input Validation | yes | `decodeJSONBody` + 4 MiB `MaxBytesReader` cap (413 on exceed); field validation (`messages` non-empty); permissive decode (no DisallowUnknownFields). |
| V6 Cryptography | no | No crypto beyond `crypto/rand` for opaque ids (non-security-sensitive). Never hand-roll. |
| V7 Error Handling/Logging | yes | T-02-33: log raw `err` via slog; return generic message; never echo request body. `slog` injected, no `SetDefault`. |

### Known Threat Patterns for Go HTTP / SSE adapter

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Unbounded request body (memory exhaustion) | DoS | `http.MaxBytesReader(4 MiB)` via `decodeJSONBody` (decode.go) |
| Error message echoes request fragments / secrets | Information Disclosure | T-02-33: generic message + structured slog of raw err (anthropic/handlers.go:108-112) |
| Goroutine leak on client disconnect during SSE | DoS | Single-goroutine select-loop on `ctx.Done`; `goleak.VerifyTestMain` proves leak-free (TRST-05) |
| Tainted input to subprocess spawn (gosec G204) | Tampering/EoP | Not in adapter scope — engine/pool own ACP spawn; adapter only produces canonical types. gosec runs in `make ci`. |
| Slowloris on the streaming connection | DoS | `http.Server.ReadHeaderTimeout: 10s` already set (server.go:223) |
| Auth bypass via header confusion | Spoofing | Single auth chain on the prefix; dual-header reader is the only token source (auth/bearer.go, 3.1 D-15) |

## Sources

### Primary (HIGH confidence)
- **Codebase (read 2026-05-24):** `internal/adapter/anthropic/{adapter,handlers,sse,render,errors,decode,wire}.go`, `internal/adapter/ollama/{adapter,handlers,render}.go`, `internal/server/server.go`, `internal/config/config.go`, `internal/canonical/{chat,chunk,model,stop_reason}.go`, `internal/engine/engine.go`, `cmd/otto-gateway/main.go`, `.go-arch-lint.yml`, `go.mod`, `Makefile` — the authoritative templates and contracts.
- **chi mux.go@v5.3.0:** `panic` conditions verified at lines 274/291/297/418 (`go env GOPATH`/pkg/mod). Confirms D-01 trap.
- **Pi / loop24-client source (read 2026-05-24):** `docs/user-docs/custom-models.md:20-34,110-114,36,38` (baseUrl key, apiKey/authHeader, compat); `packages/pi-ai/src/providers/openai-completions.ts:359-367` (`stream:true` hard-coded, stream_options); `packages/pi-ai/src/providers/openai-shared.ts:89-96` (official `openai` SDK client, `baseURL`+`apiKey`). **Resolves the STATE.md open item.**
- **Bifrost Go reference (read 2026-05-24):** `core/providers/openai/types.go:857-860` (`ID`/`Object`/`OwnedBy`/`Created` model-list field names) — validates `/v1/models` shape against a production Go OpenAI gateway.

### Secondary (MEDIUM confidence — official OpenAI spec, cross-checked)
- [The chat completion chunk object](https://platform.openai.com/docs/api-reference/chat-streaming/streaming) — chunk frame shape, delta role/content, `finish_reason` enum (`stop`/`length`/`content_filter`/`tool_calls`), `data: [DONE]` terminator.
- [Chat Completions streaming events | OpenAI API Reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events) — streaming event model.
- [List models | OpenAI API Reference](https://developers.openai.com/api/reference/resources/models/methods/list) — `{object:"list", data:[{id,object:"model",created,owned_by}]}`.
- [Create completion | OpenAI API Reference](https://platform.openai.com/docs/api-reference/completions/create) — legacy `text_completion` shape (`choices[].text`).

### Tertiary (LOW confidence)
- None — all spec claims cross-verified against the Bifrost Go reference and/or the official OpenAI docs.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — zero new dependencies; all verified in go.mod.
- Pi SDK contract: HIGH — base-URL key + streaming default confirmed from local source with file:line.
- OpenAI wire shapes: HIGH — official spec + Bifrost Go reference agree.
- chi D-01 mechanic: HIGH — panic condition verified in chi source; RegisterRoutes recommendation grounded in current server.go.
- Architecture/pitfalls: HIGH — direct adaptation of the working Anthropic adapter.

**Research date:** 2026-05-24
**Valid until:** 2026-06-23 (30 days — stable stdlib + pinned deps; OpenAI chat-completions shape is highly stable; re-verify Pi source if loop24-client bumps `@loop24/pi-ai`).
