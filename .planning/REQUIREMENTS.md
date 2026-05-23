# Requirements: Loop24 Gateway

**Defined:** 2026-05-23
**Core Value:** Both API surfaces (OpenAI for Pi SDK, Ollama for LangFlow) serve their respective clients without those clients knowing kiro-cli exists, with one place to enforce policy.

## v1 Requirements

Requirements for initial release. Each maps to roadmap phases (see Traceability). Source of truth for behaviors: `docs/briefs/go_port_brief.md` and `docs/reference/acp_server_node_reference.md`.

### Surface — Dual API compatibility

- [ ] **SURF-01**: HTTP server binds a single port (default `:11434`) and mounts both API surfaces in one process.
- [ ] **SURF-02**: `ENABLED_SURFACES` env var (default `openai,ollama`) enables or disables either surface at deploy time. `OPENAI_PATH_PREFIX` (default `/v1`) and `OLLAMA_PATH_PREFIX` (default `/api`) are overridable.
- [ ] **SURF-03**: `POST /api/chat`, `POST /api/generate`, `GET /api/tags`, `POST /api/show`, `GET /api/ps`, `GET /api/version` are served with Ollama-compatible request/response shapes.
- [ ] **SURF-04**: `POST /v1/chat/completions`, `POST /v1/completions`, `GET /v1/models` are served with OpenAI-compatible shapes.
- [ ] **SURF-05**: Existing LangFlow flows pointing at `/api/chat` and `/api/embed` work with zero reconfiguration against this gateway.
- [ ] **SURF-06**: A Pi-SDK chat CLI configured with an OpenAI provider and `base_url=http://localhost:11434/v1` works end-to-end.
- [ ] **SURF-07**: Stubs returning success for `POST /api/pull`, `POST /api/push`, `POST /api/create`, `POST /api/copy`, `DELETE /api/delete` (preserves Ollama-client compatibility).

### Streaming — NDJSON and SSE

- [ ] **STRM-01**: Ollama `/api/chat` and `/api/generate` default to `stream: true` and emit `application/x-ndjson` with one JSON object per line, final object containing `done: true`.
- [ ] **STRM-02**: OpenAI `/v1/chat/completions` defaults to streaming and emits `text/event-stream` SSE with `data: ` prefix and `data: [DONE]` terminator.
- [ ] **STRM-03**: Both surfaces consume the same canonical chunk channel from the engine.
- [ ] **STRM-04**: Client disconnect (HTTP request context canceled) cancels the in-flight `session/prompt` via `session/cancel` over the JSON-RPC channel.
- [ ] **STRM-05**: Both surfaces also support `stream: false` for single-response JSON.

### Tools — Tool-call handling and coercion

- [ ] **TOOL-01**: Tool-call output is rendered in the surface's native shape — OpenAI uses JSON-string `arguments`, Ollama uses plain-object `arguments`.
- [ ] **TOOL-02**: `coerceToolCall` behavior preserved: when `tools` is provided and the model returns bare JSON (or markdown-fenced JSON) as text, convert to a synthetic `tool_calls` entry. Best-tool selection via property-overlap scoring.
- [ ] **TOOL-03**: Tool definitions in OpenAI request shape (`tools[].function`) and Ollama request shape are normalized to a canonical tool spec consumed by the engine.

### ACP — kiro-cli JSON-RPC behaviors

- [ ] **ACP-01**: Each `kiro-cli` subprocess is spawned via `os/exec.CommandContext` with `KIRO_CMD` (default `kiro-cli`) and `KIRO_ARGS` (default `acp`). Windows-native `.cmd` resolution works without manual `shell:true`.
- [ ] **ACP-02**: JSON-RPC 2.0 over stdio with id correlation. One reader goroutine, one writer goroutine per session; pending requests tracked by id with `chan<- response`.
- [ ] **ACP-03**: `initialize`, `session/new`, `session/set_model`, `session/prompt`, `session/cancel`, `ping` RPC methods supported.
- [ ] **ACP-04**: Incoming `session/request_permission` notifications are auto-granted with `{optionId:"allow_always", granted:true}`.
- [ ] **ACP-05**: Incoming `session/update` (and `_kiro.dev/session/update`) notifications are translated into typed canonical chunks: `text`, `thought`, `tool_call`, `plan`.
- [ ] **ACP-06**: 60s ping heartbeat (`PING_INTERVAL` overridable). Failed ping kills the process; pool replaces the dead slot lazily.
- [ ] **ACP-07**: Per-request working directory derived from longest common parent of `resource_link` block URIs in the prompt; falls back to `KIRO_CWD`; `X-Working-Dir` header overrides everything.

### Pool — Warm subprocess pool

- [ ] **POOL-01**: Fixed-size pool (default `POOL_SIZE=4`) of warm `kiro-cli` subprocesses.
- [ ] **POOL-02**: Pool warmup completes before `http.Server.ListenAndServe()` accepts connections. Cold boot pays the warmup cost up front; first real request is fast.
- [ ] **POOL-03**: `Acquire` returns the first free slot or blocks on a buffered channel of free slots. `Release` returns the slot to the channel.
- [ ] **POOL-04**: Dead slots are detected and re-spawned lazily without blocking other acquires.

### Session — Stateful sessions

- [ ] **SESS-01**: Requests with `X-Session-Id` header use a dedicated `kiro-cli` subprocess via `SessionRegistry`, not the warm pool.
- [ ] **SESS-02**: Idle sessions reaped after `SESSION_TTL_MS` (default 1,800,000 = 30 min). Reaper runs every 60s.
- [ ] **SESS-03**: `DELETE /v1/sessions/:id` tears down a stateful session immediately and returns `{deleted: "<id>"}`.

### Embeddings — Local embedding endpoints

- [ ] **EMBD-01**: `POST /api/embed` (new Ollama API, string or array input) returns one embedding per input.
- [ ] **EMBD-02**: `POST /api/embeddings` (legacy Ollama API, single string) returns a single flat vector.
- [ ] **EMBD-03**: `POST /v1/embeddings` (OpenAI API) returns embeddings in OpenAI shape (`{object:"list", data:[{object:"embedding", embedding:[...], index:N}]}`).
- [ ] **EMBD-04**: BGE-Small EN-V1.5 default model. Additional models gated by `EMBEDDING_MODELS_ENABLED` env var. Default model warmed at startup.
- [ ] **EMBD-05**: Embedding inputs are bounded by `EMBEDDING_MAX_INPUTS` (default 2048) per request; over-limit returns 400.
- [ ] **EMBD-06**: Embeddings never invoke `kiro-cli`; they are served by a local backend (TBD per brief §3.4 — provisional out-of-process sidecar; revisit during phase planning).

### Plugins — Guardrails / hook chain

- [ ] **PLUG-01**: `PreHook` / `PostHook` interfaces in `internal/plugin` operate on canonical request/response types. Hooks see surface-agnostic data.
- [ ] **PLUG-02**: A `PreHook` returning a non-nil canonical response short-circuits the engine call; the adapter renders the response in its native surface shape.
- [ ] **PLUG-03**: Hooks are chained in registration order; first non-nil short-circuit wins for `PreHook`; all `PostHook`s run.
- [ ] **PLUG-04**: Day-one hooks registered: `RequestIDHook` (generate/propagate `X-Request-Id`), `AuthHook` (bearer-token validation), `LoggingHook` (structured request/response logging via `log/slog`).
- [ ] **PLUG-05**: `ENABLED_HOOKS` env var (or equivalent config key) enables/disables hooks per deployment.

### Auth + observability

- [ ] **AUTH-01**: Bearer-token auth via `AUTH_TOKEN` env var (comma-separated list). Empty means no auth (matches Node default).
- [ ] **AUTH-02**: IP allowlist via `ALLOWED_IPS` env var (comma-separated). Empty means allow-all.
- [ ] **AUTH-03**: Auth and allowlist middleware exempt `/`, `/api/version`, and `/health` paths.
- [ ] **OBSV-01**: `GET /health` returns pool stats, session registry stats, and embedding registry stats in a JSON object.
- [ ] **OBSV-02**: `GET /health/agents` returns per-pool-slot detail (`alive`, `busy`, `label`) and per-session detail (`alive`, `last_used`).
- [ ] **OBSV-03**: Structured logging via `log/slog` with `X-Request-Id` correlation across pre-hook, engine, ACP, and post-hook spans.

### Build — Distribution and cross-compile

- [ ] **BLD-01**: `make build` produces a host-platform binary at `bin/loop24-gateway`. `make cross` produces `linux/amd64` and `windows/amd64` binaries.
- [ ] **BLD-02**: Cross-compilation works from macOS dev box with vanilla `go build` + `GOOS`/`GOARCH` env vars. No `cross`, no `cargo-zigbuild`, no MinGW. `CGO_ENABLED=0` enforced for release builds.
- [ ] **BLD-03**: Binary embeds version via `-ldflags="-X main.version=$VERSION"`. `/api/version` returns the embedded value.
- [ ] **BLD-04**: Release binaries are stripped (`-ldflags="-s -w"`) and statically linked. Target: ≤25 MB per binary.

### Trust gates — Lint, test, security

- [ ] **TRST-01**: `golangci-lint` strict config (errcheck, errorlint, gosec, staticcheck, revive, wrapcheck, ineffassign, unused, unparam, nilerr, noctx, bodyclose) — warnings are CI hard failures. No `//nolint:` without inline justification.
- [ ] **TRST-02**: `govulncheck` scans deps on every PR and nightly on `main`.
- [ ] **TRST-03**: `go test -race ./...` runs in CI. Race detector always on.
- [ ] **TRST-04**: Architectural boundaries enforced via `go-arch-lint` (or equivalent): `internal/adapter/*` cannot import `internal/engine`; `internal/canonical` imports nothing under `internal/`.
- [ ] **TRST-05**: `goleak` checks goroutine leaks in handler-level tests. `goleak.VerifyTestMain` at top of test packages.
- [ ] **TRST-06**: Property tests (`pgregory.net/rapid` or stdlib `testing/quick`) for `buildAcpBlocks` (Ollama and OpenAI variants) and `coerceToolCall`. Round-trip + never-panic invariants.
- [ ] **TRST-07**: `Example_` functions in `_test.go` document non-obvious functions (`coerceToolCall`, `pickCwd`, `buildAcpBlocks`); validated via `go test -run Example`.
- [ ] **TRST-08**: Pre-commit hooks installed (`gitleaks`, `golangci-lint`, `go mod tidy`, trailing-whitespace, etc.).

## v2 Requirements

Deferred to future release. Tracked but not in current roadmap.

### Future plugin hooks

- **PLUG-V2-01**: Content moderation hook (calls external moderation API; short-circuits on policy violation).
- **PLUG-V2-02**: Schema validation hook (validates tool definitions, prompt size, image counts).
- **PLUG-V2-03**: Budget / rate-limit hook (token-bucket per API key; short-circuits with 429-equivalent).
- **PLUG-V2-04**: Semantic cache hook (cache lookup on canonical request; short-circuits on hit).
- **PLUG-V2-05**: Audit log hook (post-hook writes request+response to audit sink).

### Additional surfaces

- **SURF-V2-01**: Anthropic-compatible surface (`/anthropic/v1/messages`).
- **SURF-V2-02**: Google GenAI-compatible surface (`/genai/v1beta/models/{model}`).

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
| SURF-01 | Phase 2 | Pending |
| SURF-02 | Phase 3 | Pending |
| SURF-03 | Phase 2 | Pending |
| SURF-04 | Phase 3 | Pending |
| SURF-05 | Phase 2 | Pending |
| SURF-06 | Phase 3 | Pending |
| SURF-07 | Phase 2 | Pending |
| STRM-01 | Phase 4 | Pending |
| STRM-02 | Phase 4 | Pending |
| STRM-03 | Phase 4 | Pending |
| STRM-04 | Phase 4 | Pending |
| STRM-05 | Phase 4 | Pending |
| TOOL-01 | Phase 6 | Pending |
| TOOL-02 | Phase 6 | Pending |
| TOOL-03 | Phase 6 | Pending |
| ACP-01 | Phase 1 | Pending |
| ACP-02 | Phase 1 | Pending |
| ACP-03 | Phase 1 | Pending |
| ACP-04 | Phase 1 | Pending |
| ACP-05 | Phase 1 | Pending |
| ACP-06 | Phase 1 | Pending |
| ACP-07 | Phase 2 | Pending |
| POOL-01 | Phase 5 | Pending |
| POOL-02 | Phase 5 | Pending |
| POOL-03 | Phase 5 | Pending |
| POOL-04 | Phase 5 | Pending |
| SESS-01 | Phase 5 | Pending |
| SESS-02 | Phase 5 | Pending |
| SESS-03 | Phase 5 | Pending |
| EMBD-01 | Phase 7 | Pending |
| EMBD-02 | Phase 7 | Pending |
| EMBD-03 | Phase 7 | Pending |
| EMBD-04 | Phase 7 | Pending |
| EMBD-05 | Phase 7 | Pending |
| EMBD-06 | Phase 7 | Pending |
| PLUG-01 | Phase 8 | Pending |
| PLUG-02 | Phase 8 | Pending |
| PLUG-03 | Phase 8 | Pending |
| PLUG-04 | Phase 8 | Pending |
| PLUG-05 | Phase 8 | Pending |
| AUTH-01 | Phase 2 | Pending |
| AUTH-02 | Phase 2 | Pending |
| AUTH-03 | Phase 2 | Pending |
| OBSV-01 | Phase 2 | Pending |
| OBSV-02 | Phase 5 | Pending |
| OBSV-03 | Phase 8 | Pending |
| BLD-01 | Phase 1 | Pending |
| BLD-02 | Phase 9 | Pending |
| BLD-03 | Phase 9 | Pending |
| BLD-04 | Phase 9 | Pending |
| TRST-01 | Phase 1 | Pending |
| TRST-02 | Phase 1 | Pending |
| TRST-03 | Phase 1 | Pending |
| TRST-04 | Phase 9 | Pending |
| TRST-05 | Phase 9 | Pending |
| TRST-06 | Phase 9 | Pending |
| TRST-07 | Phase 9 | Pending |
| TRST-08 | Phase 1 | Pending |

**Coverage:**
- v1 requirements: 58 total
- Mapped to phases: 58 ✓
- Unmapped: 0 ✓

> Note: an earlier draft of this file listed "53 total" requirements; the actual count of REQ-IDs above is 58. Corrected here as part of the roadmap traceability pass.

---
*Requirements defined: 2026-05-23*
*Last updated: 2026-05-23 — Traceability populated by roadmapper; all 58 v1 REQ-IDs mapped to phases 1–9.*
