# Requirements: Loop24 Gateway

**Defined:** 2026-05-23
**Core Value:** All three API surfaces (OpenAI for Pi SDK, Ollama for LangFlow, Anthropic for loop24-client / GSD Pi) serve their respective clients without those clients knowing kiro-cli exists, with one place to enforce policy.

## v1 Requirements

Requirements for initial release. Each maps to roadmap phases (see Traceability). Source of truth for behaviors: `docs/briefs/go_port_brief.md` and `docs/reference/acp_server_node_reference.md`.

### Surface — Dual API compatibility

- [x] **SURF-01**: HTTP server binds a single port (default `:11434`) and mounts both API surfaces in one process.
- [x] **SURF-02**: `ENABLED_SURFACES` env var (default `openai,ollama,anthropic`) enables or disables any surface at deploy time. `OPENAI_PATH_PREFIX` (default `/v1`), `OLLAMA_PATH_PREFIX` (default `/api`), and `ANTHROPIC_PATH_PREFIX` (default `/v1`) are overridable. OpenAI and Anthropic intentionally share the `/v1` prefix and disambiguate at the endpoint level (`POST /v1/chat/completions` vs `POST /v1/messages`); if a deployment needs them on separate prefixes set `ANTHROPIC_PATH_PREFIX=/anthropic/v1`.
- [x] **SURF-03**: `POST /api/chat`, `POST /api/generate`, `GET /api/tags`, `POST /api/show`, `GET /api/ps`, `GET /api/version` are served with Ollama-compatible request/response shapes.
- [x] **SURF-04**: `POST /v1/chat/completions`, `POST /v1/completions`, `GET /v1/models` are served with OpenAI-compatible shapes.
- [x] **SURF-05**: Existing LangFlow flows pointing at `/api/chat` work with zero reconfiguration against this gateway. (Embeddings endpoints `/api/embed`, `/api/embeddings` cut from v1 — see PROJECT.md Decisions.)
- [x] **SURF-06**: A Pi-SDK chat CLI configured with an OpenAI provider and `base_url=http://localhost:11434/v1` works end-to-end.
- [x] **SURF-07**: Stubs returning success for `POST /api/pull`, `POST /api/push`, `POST /api/create`, `POST /api/copy`, `DELETE /api/delete` (preserves Ollama-client compatibility).
- [x] **SURF-08**: `ANTHROPIC_PATH_PREFIX` (default `/v1`) — shares the `/v1` prefix with OpenAI but disambiguates by endpoint (`POST /v1/messages` vs `POST /v1/chat/completions`). When both surfaces are enabled, chi router mounts them under the same prefix without conflict; setting `ANTHROPIC_PATH_PREFIX=/anthropic/v1` moves Anthropic to a separate prefix.

### Surface — Anthropic Messages API

- [x] **ANTH-01**: `POST /v1/messages` returns Anthropic-compatible JSON: top-level `id`, `type:"message"`, `role:"assistant"`, `model`, `content:[{type:"text",text:"..."}]`, `stop_reason` (`end_turn`/`max_tokens`/`stop_sequence`/`tool_use`), `stop_sequence` (nullable), `usage:{input_tokens,output_tokens,cache_creation_input_tokens?,cache_read_input_tokens?}`.
- [x] **ANTH-02**: Streaming emits `text/event-stream` with Anthropic's full event sequence — `message_start` → (`content_block_start` → `content_block_delta` → `content_block_stop`)+ → `message_delta` → `message_stop`, plus periodic `ping` keepalives. Delta types covered: `text_delta`, `thinking_delta`, `signature_delta`, `input_json_delta`. Frames use `event: <name>\ndata: <json>\n\n` shape; the `@anthropic-ai/sdk` `messages.stream()` client must round-trip without modification.
- [x] **ANTH-03**: Tool calls round-trip in Anthropic's native shape — outbound `tool_use` blocks carry `input` as a plain JSON object (NOT a JSON-string like OpenAI); inbound `messages[].content` may include `tool_result` blocks with `content` as string or block array. Canonical `ToolCallChunk.Args` (`map[string]any`) is the pivot — adapter/anthropic marshals/unmarshals natively.
- [x] **ANTH-04**: Header contract enforced — `anthropic-version` header is required (typical value `2023-06-01`; missing returns canonical `invalid_request_error`); accepts both `x-api-key: <key>` and `Authorization: Bearer <key>` auth modes (loop24-client uses both per provider); `anthropic-beta` headers (`fine-grained-tool-streaming-2025-05-14`, `interleaved-thinking-2025-05-14`, etc.) are accepted and passed through without behavior change at the gateway layer.
- [x] **ANTH-05**: System prompt mapping — Anthropic carries `system` at the top level of the request body (string OR array of blocks), not in `messages`. Adapter merges it into the canonical `ChatRequest.System` field (canonical engine sees one shape; adapter/openai and adapter/ollama hoist their own per-format equivalents).
- [x] **ANTH-06**: Errors render in Anthropic shape: `{"type":"error","error":{"type":"<error_type>","message":"<...>"}}` where `<error_type>` is one of `invalid_request_error`, `authentication_error`, `permission_error`, `not_found_error`, `request_too_large`, `rate_limit_error`, `api_error`, `overloaded_error`. HTTP status codes match Anthropic's: 400/401/403/404/413/429/500/529.
- [x] **ANTH-07**: Thinking content blocks supported in both directions — outbound `thinking` blocks (`{type:"thinking",thinking:"..."}`) and `redacted_thinking` blocks are emitted when the canonical chunk channel yields `ChunkKindThought`; inbound `messages[].content` may include `thinking` blocks (preserved through to kiro-cli).

### Streaming — NDJSON and SSE

- [x] **STRM-01**: Ollama `/api/chat` and `/api/generate` default to `stream: true` and emit `application/x-ndjson` with one JSON object per line, final object containing `done: true`.
- [x] **STRM-02**: OpenAI `/v1/chat/completions` defaults to streaming and emits `text/event-stream` SSE with `data: ` prefix and `data: [DONE]` terminator. Anthropic `/v1/messages` with `stream:true` emits `text/event-stream` SSE with explicit `event:` lines (see ANTH-02) — both SSE shapes are sourced from the same canonical chunk channel; adapter renders the wire shape.
- [x] **STRM-03**: All three surfaces consume the same canonical chunk channel from the engine.
- [x] **STRM-04**: Client disconnect (HTTP request context canceled) cancels the in-flight `session/prompt` via `session/cancel` over the JSON-RPC channel.
- [x] **STRM-05**: All three surfaces also support `stream: false` (Anthropic: explicit `stream` field, defaults to false on `messages.create`) for single-response JSON.

### Tools — Tool-call handling and coercion

- [x] **TOOL-01**: Tool-call output is rendered in the surface's native shape — OpenAI uses JSON-string `arguments`, Ollama uses plain-object `arguments`.
- [x] **TOOL-02**: `coerceToolCall` behavior preserved: when `tools` is provided and the model returns bare JSON (or markdown-fenced JSON) as text, convert to a synthetic `tool_calls` entry. Best-tool selection via property-overlap scoring.
- [x] **TOOL-03**: Tool definitions in OpenAI request shape (`tools[].function`) and Ollama request shape are normalized to a canonical tool spec consumed by the engine.

### ACP — kiro-cli JSON-RPC behaviors

- [x] **ACP-01**: Each `kiro-cli` subprocess is spawned via `os/exec.CommandContext` with `KIRO_CMD` (default `kiro-cli`) and `KIRO_ARGS` (default `acp`). Windows-native `.cmd` resolution works without manual `shell:true`.
- [x] **ACP-02**: JSON-RPC 2.0 over stdio with id correlation. One reader goroutine, one writer goroutine per session; pending requests tracked by id with `chan<- response`.
- [x] **ACP-03**: `initialize`, `session/new`, `session/set_model`, `session/prompt`, `session/cancel`, `ping` RPC methods supported.
- [x] **ACP-04**: Incoming `session/request_permission` notifications are auto-granted with `{optionId:"allow_always", granted:true}`.
- [x] **ACP-05**: Incoming `session/update` (and `_kiro.dev/session/update`) notifications are translated into typed canonical chunks: `text`, `thought`, `tool_call`, `plan`.
- [x] **ACP-06**: 60s ping heartbeat (`PING_INTERVAL` overridable). Failed ping kills the process; pool replaces the dead slot lazily.
- [x] **ACP-07**: Per-request working directory derived from longest common parent of `resource_link` block URIs in the prompt; falls back to `KIRO_CWD`; `X-Working-Dir` header overrides everything.

### Pool — Warm subprocess pool

- [x] **POOL-01**: Fixed-size pool (default `POOL_SIZE=4`) of warm `kiro-cli` subprocesses.
- [x] **POOL-02**: Pool warmup completes before `http.Server.ListenAndServe()` accepts connections. Cold boot pays the warmup cost up front; first real request is fast.
- [x] **POOL-03**: `Acquire` returns the first free slot or blocks on a buffered channel of free slots. `Release` returns the slot to the channel.
- [x] **POOL-04**: Dead slots are detected and re-spawned lazily without blocking other acquires.

### Session — Stateful sessions

- [x] **SESS-01**: Requests with `X-Session-Id` header use a dedicated `kiro-cli` subprocess via `SessionRegistry`, not the warm pool.
- [x] **SESS-02**: Idle sessions reaped after `SESSION_TTL_MS` (default 1,800,000 = 30 min). Reaper runs every 60s.
- [x] **SESS-03**: `DELETE /v1/sessions/:id` tears down a stateful session immediately and returns `{deleted: "<id>"}`.

### Plugins — Guardrails / hook chain

- [x] **PLUG-01**: `PreHook` / `PostHook` interfaces in `internal/plugin` operate on canonical request/response types. Hooks see surface-agnostic data.
- [x] **PLUG-02**: A `PreHook` returning a non-nil canonical response short-circuits the engine call; the adapter renders the response in its native surface shape.
- [x] **PLUG-03**: Hooks are chained in registration order; first non-nil short-circuit wins for `PreHook`; all `PostHook`s run.
- [x] **PLUG-04**: Day-one hooks registered: `RequestIDHook` (generate/propagate `X-Request-Id`), `AuthHook` (bearer-token validation), `LoggingHook` (structured request/response logging via `log/slog`).
- [x] **PLUG-05**: `ENABLED_HOOKS` env var (or equivalent config key) enables/disables hooks per deployment.
- [x] **PLUG-06**: `PIIRedactionHook` (Pre) scrubs PII from `canonical.ChatRequest.Messages[].ContentParts[].Text` using an extensible `Recognizer{Name, Pattern *regexp.Regexp, Validate func(string) bool}` registry. v1 ships six built-in recognizers: Email, IPv4 (octet-range validated), IPv6 (`net.ParseIP` validated), SSN (range-rule filtered), Credit Card (Luhn-validated), US Phone. Patterns compiled at package init. Env knobs: `PII_REDACTION_ENABLED` (bool, default off), `PII_ENABLED_ENTITIES` (comma list, default all six), `PII_REDACTION_MODE` (`replace|mask|hash|drop`, default `replace`). Replacement tokens use `<ENTITY>` form, optionally counter-suffixed (`<EMAIL_1>`, `<EMAIL_2>`) to preserve referential identity within a prompt. Extension path: appending one `Recognizer{}` entry adds a new entity type — no changes to the hook, chain runner, or callers. Pure-Go, no cgo, no external deps.

### Auth + observability

- [x] **AUTH-01**: Bearer-token auth via `AUTH_TOKEN` env var (comma-separated list). Empty means no auth (matches Node default). **Carve-outs:** (1) `/admin` route is auth-exempt by design (Phase 6.1 D-01; operator binds localhost or fronts with reverse-proxy auth — see `docs/operating.md` `### v1 no-auth posture`). (2) Ollama list-mode stubs (`/api/tags`, `/api/ps`, `/api/show`, `/api/copy`, `/api/delete`, `/api/pull`, `/api/push`, `/api/create`) bypass `AuthHook` because they do not route through the canonical engine. IP allowlist remains. Accepted v1 risk — these endpoints have no model-execution surface (see `docs/operating.md` `#### Accepted v1 risks`).
- [x] **AUTH-02**: IP allowlist via `ALLOWED_IPS` env var (comma-separated). Empty means allow-all. **Carve-outs:** same as AUTH-01 — `/admin` is exempt-by-design; Ollama list-mode stubs bypass the canonical chain (IP allowlist still applies to them).
- [x] **AUTH-03**: Auth and allowlist middleware exempt `/`, `/api/version`, and `/health` paths.
- [x] **OBSV-01**: `GET /health` returns pool stats and session registry stats in a JSON object.
- [x] **OBSV-02**: `GET /health/agents` returns per-pool-slot detail (`alive`, `busy`, `label`) and per-session detail (`alive`, `last_used`).
- [x] **OBSV-03**: Structured logging via `log/slog` with `X-Request-Id` correlation across pre-hook, engine, ACP, and post-hook spans.
- [x] **OBSV-04**: `GET /health/hooks` returns the registered Pre/Post chain as JSON — each entry includes `name`, `kind` (`Pre`, `Post`, `Pre,Post`), `enabled`, and an optional `config` object exposing safe-to-publish settings only. Read-only; exempt from auth like `/health` and `/health/agents`. No runtime mutate path in v1.

### Build — Distribution and cross-compile

- [x] **BLD-01**: `make build` produces a host-platform binary at `bin/loop24-gateway`. `make cross` produces `linux/amd64` and `windows/amd64` binaries.
- [x] **BLD-02**: Cross-compilation works from macOS dev box with vanilla `go build` + `GOOS`/`GOARCH` env vars. No `cross`, no `cargo-zigbuild`, no MinGW. `CGO_ENABLED=0` enforced for release builds.
- [x] **BLD-03**: Binary embeds version via `-ldflags="-X main.version=$VERSION"`. `/api/version` returns the embedded value.
- [x] **BLD-04**: Release binaries are stripped (`-ldflags="-s -w"`) and statically linked. Target: ≤25 MB per binary.

### Trust gates — Lint, test, security

- [x] **TRST-01**: `golangci-lint` strict config (errcheck, errorlint, gosec, staticcheck, revive, wrapcheck, ineffassign, unused, unparam, nilerr, noctx, bodyclose) — warnings are CI hard failures. No `//nolint:` without inline justification.
- [x] **TRST-02**: `govulncheck` scans deps on every PR and nightly on `main`.
- [x] **TRST-03**: `go test -race ./...` runs in CI. Race detector always on.
- [x] **TRST-04**: Architectural boundaries enforced via `go-arch-lint` (or equivalent): `internal/adapter/*` cannot import `internal/engine`; `internal/canonical` imports nothing under `internal/`.
- [x] **TRST-05**: `goleak` checks goroutine leaks in handler-level tests. `goleak.VerifyTestMain` at top of test packages.
- [x] **TRST-06**: Property tests (`pgregory.net/rapid` or stdlib `testing/quick`) for `buildAcpBlocks` (Ollama and OpenAI variants) and `coerceToolCall`. Round-trip + never-panic invariants. _(Function was renamed `buildAcpBlocks` → `buildBlocks` during Phase 1.1 ACP wire alignment — property tests live in `internal/engine/build_acp_property_test.go`.)_
- [x] **TRST-07**: `Example_` functions in `_test.go` document non-obvious functions (`coerceToolCall`, `pickCwd`, `buildAcpBlocks`); validated via `go test -run Example`. _(`Example_buildBlocks` reflects the post-rename function name.)_
- [x] **TRST-08**: Pre-commit hooks installed (`gitleaks`, `golangci-lint`, `go mod tidy`, trailing-whitespace, etc.).

## v2 Requirements

Deferred to future release. Tracked but not in current roadmap.

### Future plugin hooks

- **PLUG-V2-01**: Content moderation hook (calls external moderation API; short-circuits on policy violation).
- **PLUG-V2-02**: Schema validation hook (validates tool definitions, prompt size, image counts).
- **PLUG-V2-03**: Budget / rate-limit hook (token-bucket per API key; short-circuits with 429-equivalent).
- **PLUG-V2-04**: Semantic cache hook (cache lookup on canonical request; short-circuits on hit).
- **PLUG-V2-05**: Audit log hook (post-hook writes request+response to audit sink).

### Additional surfaces

- ~~**SURF-V2-01**~~: Anthropic-compatible surface (`/v1/messages`). **Promoted to v1 as ANTH-01..07 (Phase 3.1)** — required by loop24-client (GSD Pi CLI) which calls `@anthropic-ai/sdk` via `ANTHROPIC_BASE_URL`.
- **SURF-V2-02**: Google GenAI-compatible surface (`/genai/v1beta/models/{model}`).
- **SURF-V2-03**: Anthropic-via-Vertex (`@anthropic-ai/vertex-sdk`) and Anthropic-via-Bedrock (`@aws-sdk/client-bedrock-runtime`) variants. loop24-client supports both, but they require different auth/transport (GCP OAuth, AWS SigV4). Defer until a deployment needs them.

### Operational

- **OPS-V2-01**: Prometheus `/metrics` endpoint with histograms for end-to-end latency, ACP call latency, pool wait time.
- **OPS-V2-02**: OpenTelemetry tracing across HTTP → plugin chain → engine → ACP spans.
- **OPS-V2-03**: ARM64 binaries (Linux + macOS).
- **OPS-V2-04**: Hot config reload (signal-driven re-read of plugin chain configuration).

## Out of Scope

Explicitly excluded. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| Multi-provider routing (OpenAI cloud, Anthropic cloud, etc.) | Single backend (`kiro-cli`); no fallback chains or weighted load balancing. Bifrost has these; we don't need them. |
| Virtual keys, teams, customers, hierarchical budgets | Bearer-token auth is sufficient. |
| Web UI / status dashboard | `/health` JSON is enough for v1. |
| MCP, WebSocket, or WebRTC realtime surfaces | Maybe later; not v1. |
| macOS-as-deploy or ARM64 binaries (v1) | Dev runs on macOS but deployments are x86_64 Linux + Windows. |
| Replacing `kiro-cli` itself | The whole point is to proxy it faithfully. |
| In-process ONNX embeddings via cgo (v1) | Sacrificing the trivial cross-compile story is too expensive. Out-of-process sidecar preferred; revisit in v2. |
| Hot config reload / dynamic plugin registration | Restart to change config. |
| `fasthttp` HTTP framework | Bifrost uses it for throughput; our bottleneck is `kiro-cli` latency, not HTTP parsing. Stdlib `net/http` + `chi` is sufficient. |
| Backward compat with the Node `.env` file format | Env-var names match; file format does not. |
| Rust port | Considered (`docs/briefs/rust_port_brief.md`) and rejected on cross-compile friction + first-Go-project pragmatism. |

## Traceability

Populated by the roadmapper from `.planning/ROADMAP.md`. Updated as phases complete.

| Requirement | Phase | Status |
|-------------|-------|--------|
| SURF-01 | Phase 2 | Complete |
| SURF-02 | Phase 3 | Complete |
| SURF-03 | Phase 2 | Complete |
| SURF-04 | Phase 3 | Complete |
| SURF-05 | Phase 2 | Complete |
| SURF-06 | Phase 3 | Complete |
| SURF-07 | Phase 2 | Complete |
| SURF-08 | Phase 3.1 | Complete |
| ANTH-01 | Phase 3.1 | Complete |
| ANTH-02 | Phase 3.1 | Complete |
| ANTH-03 | Phase 3.1 | Complete |
| ANTH-04 | Phase 3.1 | Complete |
| ANTH-05 | Phase 3.1 | Complete |
| ANTH-06 | Phase 3.1 | Complete |
| ANTH-07 | Phase 3.1 | Complete |
| STRM-01 | Phase 4 | Complete |
| STRM-02 | Phase 4 | Complete |
| STRM-03 | Phase 4 | Complete |
| STRM-04 | Phase 4 | Complete |
| STRM-05 | Phase 4 | Complete |
| TOOL-01 | Phase 6 | Complete |
| TOOL-02 | Phase 6 | Complete |
| TOOL-03 | Phase 6 | Complete |
| ACP-01 | Phase 1 | Complete |
| ACP-02 | Phase 1 | Complete |
| ACP-03 | Phase 1 | Complete |
| ACP-04 | Phase 1 | Complete |
| ACP-05 | Phase 1 | Complete |
| ACP-06 | Phase 1 | Complete |
| ACP-07 | Phase 2 | Complete |
| POOL-01 | Phase 5 | Complete |
| POOL-02 | Phase 5 | Complete |
| POOL-03 | Phase 5 | Complete |
| POOL-04 | Phase 5 | Complete |
| SESS-01 | Phase 5 | Complete |
| SESS-02 | Phase 5 | Complete |
| SESS-03 | Phase 5 | Complete |
| PLUG-01 | Phase 8 | Complete |
| PLUG-02 | Phase 8 | Complete |
| PLUG-03 | Phase 8 | Complete |
| PLUG-04 | Phase 8 | Complete |
| PLUG-05 | Phase 8 | Complete |
| PLUG-06 | Phase 8 | Complete |
| AUTH-01 | Phase 2 | Complete |
| AUTH-02 | Phase 2 | Complete |
| AUTH-03 | Phase 2 | Complete |
| OBSV-01 | Phase 2 | Complete |
| OBSV-02 | Phase 5 | Complete |
| OBSV-03 | Phase 8 | Complete |
| OBSV-04 | Phase 8 | Complete |
| BLD-01 | Phase 1 | Complete |
| BLD-02 | Phase 9 | Complete |
| BLD-03 | Phase 9 | Complete |
| BLD-04 | Phase 9 | Complete |
| TRST-01 | Phase 1 | Complete |
| TRST-02 | Phase 1 | Complete |
| TRST-03 | Phase 1 | Complete |
| TRST-04 | Phase 9 | Complete |
| TRST-05 | Phase 9 | Complete |
| TRST-06 | Phase 9 | Complete |
| TRST-07 | Phase 9 | Complete |
| TRST-08 | Phase 1 | Complete |

**Coverage:**
- v1 requirements: 62 total
- Mapped to phases: 62 ✓
- Unmapped: 0 ✓

> Note: an earlier draft of this file listed "53 total" requirements; the actual count of REQ-IDs above is 62 (after the v1.5 close-out added PLUG-06 + OBSV-04 to the traceability table). Corrected in Phase 08.1 D-14.

---
*Requirements defined: 2026-05-23*
*Last updated: 2026-05-28 — Traceability refreshed in Phase 08.1 D-14; PLUG-06 + OBSV-04 added; all 62 v1 REQ-IDs mapped to phases 1–9.*
