# Phase 3: OpenAI Surface - Pattern Map

**Mapped:** 2026-05-24
**Files analyzed:** 16 (8 new adapter source + 4 new test + 4 modified)
**Analogs found:** 16 / 16 (every file has a strong analog; the only genuinely-new logic is the `/v1/completions` shim and the D-01 SurfaceMount grouping)

> **Primary template:** `internal/adapter/anthropic/` (Phase 3.1) — near-exact, file-for-file. The OpenAI adapter is ~70% copy-paste from it with the SSE state machine *removed* (no `event:` lines, no block-index machine, no ping ticker).
> **Secondary template:** `internal/adapter/ollama/` — for the `ModelCatalog` injection (`/v1/models` mirrors `handleTags`) and the `stream:true` silent-downgrade pattern (`/v1/completions`).
>
> All file paths below are absolute under the repo root `/Users/coreyellis/Projects/repos/local/loop24-gateway`.

---

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `internal/adapter/openai/adapter.go` | adapter (constructor + interfaces) | request-response | `internal/adapter/anthropic/adapter.go` + `internal/adapter/ollama/adapter.go:40-51` (catalog) | exact (composite) |
| `internal/adapter/openai/handlers.go` | controller | request-response + streaming | `internal/adapter/anthropic/handlers.go` | exact |
| `internal/adapter/openai/wire.go` | transform (decode) | request-response | `internal/adapter/anthropic/wire.go` | role-match (OpenAI content polymorphism is simpler) |
| `internal/adapter/openai/render.go` | transform (encode) | request-response | `internal/adapter/anthropic/render.go` + `internal/adapter/ollama/render.go` | exact |
| `internal/adapter/openai/sse.go` | streaming emitter | streaming (SSE) | `internal/adapter/anthropic/sse.go` (minus state machine) | role-match (structurally simpler) |
| `internal/adapter/openai/errors.go` | utility (error envelope) | request-response | `internal/adapter/anthropic/errors.go` | exact (different envelope shape) |
| `internal/adapter/openai/decode.go` | utility (body cap) | request-response | `internal/adapter/anthropic/decode.go` | exact (copy verbatim) |
| `internal/adapter/openai/testmain_test.go` | test (goleak gate) | — | `internal/adapter/anthropic/testmain_test.go` | exact (copy, rename pkg) |
| `internal/adapter/openai/sse_golden_test.go` | test (golden) | streaming | `internal/adapter/anthropic/sse_golden_test.go` | exact (adapt id regex) |
| `internal/adapter/openai/integration_test.go` | test (httptest round-trip) | streaming | `internal/adapter/anthropic/integration_test.go` | exact |
| `internal/adapter/openai/testdata/*.golden` | test fixture | streaming | `internal/adapter/anthropic/testdata/*.golden` | role-match (different frame shape) |
| `internal/server/server.go` | config + route composition | request-response | itself (refactor target) — current two-block structure at lines 60-74, 159-196 | refactor-in-place |
| `internal/config/config.go` | config | — | itself (lines 143, 317-335) | edit-in-place |
| `cmd/otto-gateway/main.go` | wiring (composition root) | — | itself (lines 187-264, 292-352 — copy the anthropic bridge) | edit-in-place |
| `.go-arch-lint.yml` | config (arch boundary) | — | itself (lines 31-32, 60-63 `adapter_anthropic` entries) | edit-in-place |
| `internal/config/config_test.go` | test | — | extend existing `TestEnabledSurfaces`-style cases | extend |

---

## Pattern Assignments

### `internal/adapter/openai/adapter.go` (adapter, request-response)

**Analogs:** `internal/adapter/anthropic/adapter.go` (Engine/RunHandle/Stream + New + nil-logger default), `internal/adapter/ollama/adapter.go:40-74` (ModelCatalog interface + Config field).

**Consumer-defined interface triple** — copy from `anthropic/adapter.go:35-71` verbatim (TRST-04: NEVER import `internal/engine`):
```go
type Engine interface {
	Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
	Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error)
}
type RunHandle interface {
	Stream() Stream
	SessionID() string
}
type Stream interface {
	Chunks() <-chan canonical.Chunk
	Result() (*canonical.FinalResult, error)
}
```

**ADD a ModelCatalog interface** (Anthropic has none; copy from `ollama/adapter.go:49-51` — `/v1/models` needs it per D-04):
```go
// ollama/adapter.go:49-51
type ModelCatalog interface {
	Models() []canonical.ModelInfo
}
```
`canonical.ModelInfo` (`internal/canonical/model.go:7-12`) has only `ID string` and `Name string` — the `created`/`owned_by` fields are adapter-synthesized, not sourced from the catalog.

**Config struct** — mirror `anthropic/adapter.go:77-85` PLUS the `ModelCatalog` field from `ollama/adapter.go:65-67`:
```go
type Config struct {
	Logger       *slog.Logger
	Engine       Engine        // nil → 503 from chat handler
	ModelCatalog ModelCatalog  // nil → /v1/models returns only "auto"
}
```

**New + nil-logger guard + discardWriter** — copy `anthropic/adapter.go:95-106, 116-121` verbatim. **Composition-mechanic decision (D-01, planner's discretion):** RESEARCH.md §Pattern recommends replacing `ProtectedRouter()` with a `RegisterRoutes(r chi.Router)` method that calls `r.Post`/`r.Get` directly (avoids the chi double-Mount panic — see Shared Patterns §Route Composition). Register:
```go
func (a *Adapter) RegisterRoutes(r chi.Router) {
	r.Post("/chat/completions", a.handleChatCompletions)
	r.Post("/completions", a.handleCompletions)
	r.Get("/models", a.handleModels)
}
```
(Contrast `anthropic/adapter.go:101-104` and `ollama/adapter.go:92-108` which build an internal `chi.NewRouter()` and expose it via `ProtectedRouter()`.)

---

### `internal/adapter/openai/handlers.go` (controller, request-response + streaming)

**Analog:** `internal/adapter/anthropic/handlers.go:34-116` (the whole `handleMessages` flow). Drop the `anthropic-version` header check (lines 40-44) and `anthropic-beta` log (lines 46-49) — OpenAI has no equivalent headers.

**Body cap const** — copy `anthropic/handlers.go:10`:
```go
const chatBodyCap int64 = 4 << 20 // 4 MiB
```

**Nil-engine guard + decode + validation + stream branch** — adapt `anthropic/handlers.go:34-116`. The OpenAI version (matches RESEARCH.md §Code Examples lines 452-491):
```go
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
	req := wireToChatRequest(&wire, r)

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

**T-02-33 (NEVER echo `err.Error()` from engine errors):** the analog at `anthropic/handlers.go:106-113` logs raw err via `slog.Error` then returns a generic `"internal error"` 500. OpenAI MUST do the same. (Note: Ollama's `handlers.go:51-52` `writeError(..., err.Error())` is the OLD pattern — do NOT copy Ollama's engine-error path; copy Anthropic's.)

**`/v1/completions` shim (D-03, new):** no analog has a `prompt`→message map. Reuse the `stream:true` silent-downgrade from `ollama/handlers.go:42-45` (`if wire.Stream { wire.Stream = false }`) if completions is JSON-only (planner's discretion per RESEARCH.md Open Question 2). Map `prompt` (string or `[]string` joined) → one `canonical.Message{Role: RoleUser, ...}`, run `engine.Collect`, render via `promptResponseToTextCompletion` (render.go).

**`/v1/models` handler:** mirror `ollama/handlers.go:106-117` `handleTags` — prepend `"auto"`, iterate `a.cfg.ModelCatalog.Models()`, nil-catalog returns only `"auto"`.

---

### `internal/adapter/openai/wire.go` (transform/decode, request-response)

**Analog:** `internal/adapter/anthropic/wire.go` (request structs + `wireToChatRequest` + role map + content polymorphism).

**Content string-OR-array (Pitfall 5):** OpenAI `messages[].content` is polymorphic. Copy the `json.RawMessage` + try-string-then-array pattern from `anthropic/wire.go:43-44` (RawMessage field) and `:162-186` (decode both forms):
```go
// anthropic/wire.go:162-176 — try string first, then array-of-blocks
var contentStr string
if err := json.Unmarshal(m.Content, &contentStr); err == nil {
	// flat-string form — one text part
	...
}
var blocks []anthropicContentBlock
if err := json.Unmarshal(m.Content, &blocks); err != nil { ... }
```

**Role mapping (Pitfall 4 — system AND developer):** adapt `anthropic/wire.go:354-361` `mapAnthropicRole`. For OpenAI map `"system"` AND `"developer"` → `canonical.RoleSystem` (hoist into `ChatRequest.System` like `anthropic/wire.go:135-154` does for the top-level `system` field — but OpenAI carries system as a `messages[]` entry), `"user"`→`RoleUser`, `"assistant"`→`RoleAssistant`, `"tool"`→`RoleTool`, unknown→`RoleUser`.

**model:"auto"/"" handling (D-04):** the canonical request just carries `Model: w.Model`; the engine skips `SetModel` on `"auto"`/empty (same as `anthropic/wire.go:125-126` which sets `Model: w.Model` unconditionally and lets the engine decide). Do NOT special-case in the adapter.

**Accept-and-ignore (no `DisallowUnknownFields`):** OpenAI request struct decodes `stream`, `stream_options`, `max_completion_tokens`, `logprobs`, `tools`, `function_call` etc. without 400-ing. This is the `decode.go` invariant (see below) — mirror `anthropic/wire.go:35` (`Metadata json.RawMessage // accepted-and-ignored`).

---

### `internal/adapter/openai/render.go` (transform/encode, request-response)

**Analogs:** `anthropic/render.go` (envelope build + `mapStopReason` + `genMessageID`), `ollama/render.go:135-146` (`joinTextContent`).

**`chat.completion` envelope** — new struct shapes (field order load-bearing for goldens), per RESEARCH.md §Pattern 3. Walk `resp.Message.Content` joining `ContentKindText` parts via a helper copied from `ollama/render.go:135-146`:
```go
// ollama/render.go:135-146 — copy verbatim
func joinTextContent(parts []canonical.ContentPart) string {
	out := ""
	for _, p := range parts {
		if p.Kind == canonical.ContentKindText {
			out += p.Text
		}
	}
	return out
}
```
Defensive empty: emit `content:""` if no text (mirror `anthropic/render.go:140-142`).

**`mapStopReason`/`mapFinishReason`** — single-source-of-truth helper. Adapt `anthropic/render.go:166-183` (shape) and `ollama/render.go:115-124` (the Ollama "stop"/"length"/default switch). OpenAI mapping (RESEARCH.md §Pattern 2 + A3): `StopEndTurn`→`"stop"`, `StopMaxTokens`→`"length"`, `StopRefusal`→`"content_filter"` (or `"stop"` per A3), `StopCancelled`→`"stop"`, default→`"stop"`. **OpenAI's terminal-chunk `finish_reason` is non-null** — return `string`, NOT `*string` (Anthropic returns `*string` for null; OpenAI never nulls the final frame).

**`genMessageID`** — copy `anthropic/render.go:185-203` `crypto/rand` + `encoding/hex` pattern, change prefix `"msg_01"` → `"chatcmpl-"` (chat) and `"cmpl-"` (completions). Planner may use unix-nano instead (D-05 carry-forward / discretion).

**`usage` honest zeros** — `{prompt_tokens:0, completion_tokens:0, total_tokens:0}` (D-12; mirror `anthropic/render.go:92` `usage{InputTokens: 0, OutputTokens: 0}`).

**`/v1/models` list render + `/v1/completions` text_completion render** — new shapes per RESEARCH.md §Pattern 4 and §Pattern 5; field names validated against Bifrost (`id`/`object`/`created`/`owned_by`).

---

### `internal/adapter/openai/sse.go` (streaming emitter, SSE) — STRUCTURALLY SIMPLER THAN ANTHROPIC

**Analog:** `internal/adapter/anthropic/sse.go` — but DELETE the block-index state machine (`applyChunk` lines 204-270), the ping ticker (`PingInterval` line 24, the `tickerC` select case line 367-370), and the `event:`-named framing.

**Header-order + Flusher assertion (Pitfall 2)** — copy `anthropic/sse.go:289-301` EXACTLY (assert Flusher BEFORE any write so the caller can still emit a JSON 500):
```go
func runSSEEmitter(ctx context.Context, w http.ResponseWriter, run RunHandle, model string, logger *slog.Logger) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("openai: response writer is not flusher")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	...
}
```

**Frame writer** — replace `anthropic/sse.go:163-173` `writeEvent` (which emits `event: %s\ndata: %s\n\n`) with the flat `data:`-only form (RESEARCH.md §Pattern 2 lines 270-279):
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
// terminator: fmt.Fprintf(e.w, "data: [DONE]\n\n"); e.flusher.Flush()
```

**Select-loop — TWO cases only (NO tickerC; Pitfall 6)** — adapt `anthropic/sse.go:359-381` removing the `case <-tickerC`:
```go
for {
	select {
	case <-ctx.Done():
		e.logger.Debug("openai: sse client disconnect", "session_id", run.SessionID())
		return fmt.Errorf("openai: sse ctx: %w", ctx.Err())
	case c, ok := <-chunks:
		if !ok {
			return finalizeStream(e, run) // emit finish_reason frame + [DONE]
		}
		if err := e.applyChunk(c); err != nil {
			return err
		}
	}
}
```

**Emission sequence (RESEARCH.md §Pattern 2 lines 260-266):** (1) first frame `delta={"role":"assistant"}`, `finish_reason=null`; (2) per `ChunkKindText` frame `delta={"content":"<fragment>"}`; (3) final frame `delta={}`, `finish_reason="stop"` (mapped); (4) literal `data: [DONE]\n\n`. On `ctx.Done` return without `[DONE]`.

**Fixed `id`/`created` per response (Pitfall 8):** compute once in the emitter struct (mirror `anthropic/sse.go:142-151` which holds `messageID`/`model` as fields) — same `id` and `created` on EVERY chunk.

**Single-goroutine writer invariant (3.1 D-05):** only `writeData` touches `e.w`/`e.flusher`, and it's called only from the loop goroutine — no mutex (same as `anthropic/sse.go:133-140` comment).

**Mid-stream error (Pitfall 3):** OpenAI has NO `event: error` frame contract. Do NOT copy `anthropic/sse.go:411` `writeSSEError`. On a `run.Stream().Result()` error after headers, log at debug and stop (truncated stream is acceptable — A5).

---

### `internal/adapter/openai/errors.go` (utility, request-response)

**Analog:** `internal/adapter/anthropic/errors.go` — same construction (Content-Type before WriteHeader, errcheck-discarded encode), DIFFERENT envelope shape.

**OpenAI envelope** (per RESEARCH.md §Code Examples lines 498-513) vs Anthropic's two-level `{"type":"error","error":{...}}`:
```go
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
	errNotFound        = "not_found_error"          // 404
	errRequestTooLarge = "invalid_request_error"    // 413 (OpenAI has no distinct type)
	errAPI             = "api_error"                // 500
)
```

**`writeError`/`writeJSON`** — copy the construction pattern from `anthropic/errors.go:82-92` (writeError) and `:130-134` (writeJSON) verbatim, swapping the envelope struct. Content-Type set BEFORE WriteHeader; encode error discarded with `_ =` (errcheck).

**401 note (A2 carry-forward):** the `auth.Bearer` middleware on `/v1` emits the OLLAMA error shape on 401, not OpenAI's — shared middleware, documented Phase 3.1 behavior (`anthropic/errors.go:48-52`), acceptable for Phase 3.

---

### `internal/adapter/openai/decode.go` (utility, request-response)

**Analog:** `internal/adapter/anthropic/decode.go` — **copy VERBATIM**, change package name only. RESEARCH.md §Recommended Structure line 208 + §Don't Hand-Roll confirm the generic `decodeJSONBody[T]` + `isMaxBytesError` are reusable as-is:
```go
// anthropic/decode.go:27-46 — copy whole file, s/anthropic/openai/ in comments
func decodeJSONBody[T any](w http.ResponseWriter, r *http.Request, maxBytes int64, dst *T) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil { ... }
	return nil
}
func isMaxBytesError(err error) bool { var maxErr *http.MaxBytesError; return errors.As(err, &maxErr) }
```
**Do NOT add `dec.DisallowUnknownFields()`** — breaks accept-and-ignore for `stream_options`/`logprobs`/future SDK fields (the documented invariant at `anthropic/decode.go:21-26`).

---

### Test files (mirror anthropic test layout)

| New test file | Copy from | Adaptation |
|---------------|-----------|------------|
| `testmain_test.go` | `anthropic/testmain_test.go` (whole file, 22 lines) | change `package anthropic` → `package openai`. `goleak.VerifyTestMain(m)` covers the SSE leak gate (TRST-05). |
| `sse_golden_test.go` | `anthropic/sse_golden_test.go` (whole file) | change the id regex `messageIDRegexp` from `"id":"msg_01[0-9a-fA-F_]+"` to `"id":"chatcmpl-..."`. Keep `compareGolden`/`driveGolden`/`fakeRunHandle`/`fakeStream` harness + the trailing-newline trim logic (lines 79-95). |
| `integration_test.go` | `anthropic/integration_test.go` | copy `resolveKiroCLI` (lines 32-45), `kiroSetup` (57-101), `realEngineAdapter` (107-...). For the SSE round-trip assertion copy lines 232-244 (status 200 + `Content-Type: text/event-stream` prefix check) and the `bufio.Scanner` frame loop (lines 274-279); adapt the `knownEvents` map (lines 249-258) — OpenAI has no event names, so scan for `data: ` prefixes and the `[DONE]` terminator instead. |
| `testdata/*.golden` | `anthropic/testdata/sse_text_only.golden` (shape reference only) | NEW byte-exact fixtures: text-only stream (role-first delta → content deltas → finish_reason chunk → `data: [DONE]`). Different frame shape (no `event:` lines). |

**Golden harness driver** — `anthropic/sse_golden_test.go:105-124` `driveGolden` constructs a `fakeRunHandle{stream: &fakeStream{chunks, final}}`, fills+closes the channel, calls `runSSEEmitter` against an `httptest.NewRecorder()`, returns `rec.Body.Bytes()`. Copy this exactly.

---

## Shared Patterns

### Route Composition (D-01 — the one genuinely-new server change)
**Source (refactor target):** `internal/server/server.go:60-74` (parallel `Ollama*`/`Anthropic*` Config fields) + `:159-196` (two `r.Route` blocks).
**Apply to:** `internal/server/server.go` + `cmd/otto-gateway/main.go`.

**The trap (Pitfall 1):** chi panics on a second `r.Route("/v1", …)` AND on a duplicate `r.Mount("/", …)` within one block (chi mux.go:297). Anthropic + OpenAI both default to `/v1`, so the current two-`r.Route`-block structure (server.go:161-196) cannot host both.

**The fix (RESEARCH.md §Code Examples lines 517-538):** replace the parallel fields with a `Surfaces []SurfaceMount` list grouped by prefix; one auth-wrapped `r.Route(prefix, …)` per unique prefix:
```go
type SurfaceMount struct {
	Prefix string         // "/api", "/v1"
	Router RouteRegistrar // exposes RegisterRoutes(chi.Router); planner's discretion on exact type
}

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
```
**KEEP unchanged:** the auth chain construction (copy `server.go:163-171` `auth.Bearer` + `auth.IPAllowlist` with Codex H-7 `TrustXForwardedFor`), the outer exempt routes (`server.go:150-157` — `/`, `/health`, `OllamaVersionHandler` stays on the OUTER router; OpenAI has no `/version`).

### Authentication (V2/V4 — reused unchanged, never re-implement in adapter)
**Source:** `internal/server/server.go:163-171` (`auth.Bearer` + `auth.IPAllowlist` Use chain). `auth.Bearer` is already dual-header (`Authorization: Bearer` + `x-api-key`, 3.1 D-15) — Pi's official `openai` SDK sends `Authorization: Bearer <apiKey>`, works unchanged.
**Apply to:** the `/v1` SurfaceMount block. Adapter handlers NEVER check tokens (anti-pattern).

### Error handling / T-02-33 (never echo engine errors)
**Source:** `internal/adapter/anthropic/handlers.go:106-113` — `a.cfg.Logger.Error("...", "err", err)` then `writeError(500, errAPI, "internal error")`.
**Apply to:** every engine-error path in `openai/handlers.go`. Do NOT copy `ollama/handlers.go:51-52` which echoes `err.Error()`.

### Body cap (V5)
**Source:** `internal/adapter/anthropic/decode.go:27-38` `decodeJSONBody[T]` + `http.MaxBytesReader`, 4 MiB cap (`anthropic/handlers.go:10`).
**Apply to:** all OpenAI POST handlers (`/chat/completions`, `/completions`). 413 via `isMaxBytesError`, else 400.

### Engine bridge (cmd-level seam, TRST-04)
**Source:** `cmd/otto-gateway/main.go:292-352` (`anthropicEngineAdapter` + `anthropicRunHandleAdapter`).
**Apply to:** `cmd/otto-gateway/main.go` — copy as `openaiEngineAdapter`/`openaiRunHandleAdapter` (identical `Run`/`Collect` wrapping; Go return-type invariance requires the explicit wrapper). For the catalog, `a.pool` already satisfies the `Models() []canonical.ModelInfo` shape (used at main.go:171-174 for `catalogForAdapter`) — pass the SAME `a.pool` to the OpenAI adapter's `ModelCatalog` field. The test-local equivalent is `realEngineAdapter` at `anthropic/integration_test.go:103-...`.

### slog (no SetDefault)
**Source:** every adapter — `*slog.Logger` injected via Config; nil → discard logger (`anthropic/adapter.go:96-98`). Apply to `openai.New`.

---

## Modified-File Edit Map (concrete edits, not new files)

### `internal/config/config.go` (D-05)
- **Line 143:** widen default — `getEnvStrSliceComma("ENABLED_SURFACES", []string{"ollama", "anthropic"})` → add `"openai"`.
- **Lines 321-324:** add `"openai": {}` to the `allowed` map in `validateEnabledSurfaces`.
- **Line 328:** update error message `"(allowed: ollama, anthropic)"` → `"(allowed: ollama, anthropic, openai)"`.
- **No new field:** `OpenAIPathPrefix` (lines 68-71) + `--openai-path-prefix` flag (line 215, 253-254) already exist and are wired.

### `cmd/otto-gateway/main.go` (newApp)
- After the anthropic block (lines 187-206), add an `if slices.Contains(cfg.EnabledSurfaces, "openai")` branch constructing `openai.New(openai.Config{Logger, Engine: openaiEngineAdapter{...}, ModelCatalog: catalogForAdapter})`.
- Build the `[]server.SurfaceMount` list (Ollama→`/api`, Anthropic→`cfg.AnthropicPathPrefix`, OpenAI→`cfg.OpenAIPathPrefix`) and pass to `server.NewFromConfig` (replacing the parallel `OllamaProtectedRouter`/`AnthropicProtectedRouter` fields at lines 259-262).
- Add `openaiEngineAdapter`/`openaiRunHandleAdapter` (copy lines 292-352).
- Update the boot-log line (lines 243-248) to include `openai_mounted`.

### `.go-arch-lint.yml` (TRST-04)
- Add component `adapter_openai: { in: adapter/openai/** }` (mirror lines 31-32).
- Add deps entry `adapter_openai: { anyVendorDeps: true, mayDependOn: [canonical] }` (mirror lines 60-63).

### `internal/server/server.go` (D-01) — see Shared Patterns §Route Composition above.

---

## No Analog Found

| File / logic | Role | Data Flow | Reason |
|--------------|------|-----------|--------|
| `internal/adapter/openai/handlers.go` — `/v1/completions` legacy shim | controller | request-response | Neither Ollama nor Anthropic has a `prompt`→message text-completion endpoint. Closest reuse: `ollama/handlers.go:42-45` stream-downgrade + RESEARCH.md §Pattern 4. Render shape is new. |
| `internal/server/server.go` — `SurfaceMount` grouping | route composition | request-response | First time two surfaces share one prefix; the grouped-by-prefix loop is genuinely new (refactor of the parallel-field structure). RESEARCH.md §Code Examples lines 517-538 is the reference. |
| `internal/server/server_test.go` — D-01 co-mount regression test | test | — | No precedent — proves `/v1/messages` AND `/v1/chat/completions` both route without a chi panic. New test, no analog to copy. |

---

## Metadata

**Analog search scope:** `internal/adapter/anthropic/` (all source + test files), `internal/adapter/ollama/` (adapter, handlers, render), `internal/server/server.go`, `internal/config/config.go`, `cmd/otto-gateway/main.go`, `.go-arch-lint.yml`, `internal/canonical/model.go`.
**Files scanned:** 16 source/test/config files read in full (all ≤ 802 lines; no large-file targeting needed).
**Pattern extraction date:** 2026-05-24
