# Walking Skeleton — Loop24 Gateway

**Phase:** 1
**Generated:** 2026-05-23

## Capability Proven End-to-End

A developer can run `make build && ./scripts/loop24 start` on a macOS laptop, hit `GET http://localhost:11434/health` and receive a valid JSON health response, then `./scripts/loop24 stop` to clean up — proving the Go binary compiles, binds to the port, serves HTTP, and shuts down gracefully via signal.

## Architectural Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Language + runtime | Go 1.23+ (module `loop24-gateway`) | Trivial cross-compile to linux/amd64 + windows/amd64 with `CGO_ENABLED=0`; stdlib-first; slog available since 1.21. No cgo = no cross-compile breakage. |
| HTTP framework | stdlib `net/http` + `github.com/go-chi/chi/v5` | Fasthttp rejected (breaks `http.Handler` ecosystem); chi provides `RequestID`, `Recoverer`, `WrapResponseWriter` without external types. No middleware DSL to learn. |
| Logging | `log/slog` (stdlib, `NewJSONHandler`) | Zero external dep; structured JSON by default; per-request child logger via `logger.With`; D-15 forbids `slog.SetDefault` — fully injected. |
| Config loading | `internal/config.Load()` with `os.Getenv` helpers | Env-var names match the Node version (`KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `DEBUG`, `PING_INTERVAL`, `HTTP_ADDR`). No third-party config lib. |
| Subprocess protocol | JSON-RPC 2.0 over stdio (NDJSON), `internal/acp` | Hand-rolled (no mature Go library for subprocess JSON-RPC + bidirectional notifications). Three-file split: framer, dispatcher, client. |
| Canonical types | `internal/canonical` — leaf package (imports nothing under `internal/`) | Adapter-over-canonical layout per brief §3.13. Adapters import canonical; canonical imports nothing. Enables dual-surface in Phase 2/3 without coupling. |
| Server lifecycle | `internal/server.Server.RunUntilSignal()` | Owns `http.Server.Shutdown(30s)` on SIGINT/SIGTERM. ACP `Client.Close()` called in `main.go` defer after signal. |
| Dev deployment | `scripts/loop24` (POSIX) + `scripts/loop24.ps1` (PowerShell) | Binary stays foreground-only (D-22). Wrapper scripts own PID file, log redirect, start/stop/status/restart/logs lifecycle. Preserves future systemd/Windows-Service path. |
| Database | None | Phase 1 has no persistence. Pool + sessions arrive in Phase 5. |
| Directory layout | `cmd/loop24-gateway/`, `internal/{acp,canonical,config,server,version,testutil}/` | Matches brief §3.13. Adapter packages (ollama, openai), engine, plugin, pool, embed scaffolded as .gitkeep; filled in later phases. |

## Stack Touched in Phase 1

- [x] Project scaffold — Go module, directory layout, `.golangci.yml` (already v2), `.pre-commit-config.yaml` (already wired), Makefile extended
- [x] Routing — `GET /health` + `GET /api/version` via chi router with middleware chain
- [ ] Database — N/A (no DB in this project's Phase 1; pool arrives in Phase 5)
- [ ] UI — N/A (this is an API gateway with no frontend)
- [x] Deployment — `make build` + `./scripts/loop24 start` round-trip proven on macOS dev laptop

## Domain Interaction Proven in Phase 1 (Skeleton Extension)

Phase 1 goes beyond a pure HTTP skeleton: the ACP client (`internal/acp`) completes a real JSON-RPC conversation with a live `kiro-cli` subprocess in a standalone integration test:

- `initialize` → `session/new` → `ping` (ACP-01, ACP-02, ACP-03)
- `session/request_permission` auto-granted (ACP-04)
- `session/update` translated to `canonical.Chunk` (ACP-05)
- No goroutine leaks (TRST-03/TRST-05 via goleak)

This integration test runs only when `kiro-cli` is on PATH (or `LOOP24_KIRO_BIN` is set); it is skipped in CI environments without the binary.

## Out of Scope (Deferred to Later Slices)

- HTTP adapters (`internal/adapter/ollama`, `internal/adapter/openai`) — Phase 2/3
- Pool warmup and session registry — Phase 5
- Auth middleware (bearer-token, IP allowlist) — Phase 2
- Streaming NDJSON / SSE encoders — Phase 4
- Tool-call coercion (`coerceToolCall`) — Phase 6
- Embeddings endpoints and sidecar — Phase 7
- Plugin hook chain (`PreHook`/`PostHook`) — Phase 8
- Cross-compile CI matrix (GitHub/GitLab CI) — Phase 9
- Hosted CI — deferred until bare module name resolved (D-08)
- Service mode (systemd / Windows Service) — deferred milestone
- `/health` returning 503 on ACP subprocess dead — Phase 5

## Subsequent Slice Plan

- Phase 2: LangFlow `POST /api/chat` reaches real `kiro-cli` through the Ollama adapter (first real end-to-end request through the full stack)
- Phase 3: Pi SDK `POST /v1/chat/completions` shares the same engine via the OpenAI adapter
- Phase 4: NDJSON (Ollama) and SSE (OpenAI) streaming off one canonical chunk channel
- Phase 5: Warm pool (`POOL_SIZE`) + stateful sessions (`X-Session-Id`) + `/health/agents`
- Phase 6: Tool-call path including `coerceToolCall` fallback
- Phase 7: Local embedding endpoints (BGE-Small, out-of-process sidecar)
- Phase 8: Plugin hook chain (`PreHook`/`PostHook`) with day-one RequestID, Auth, Logging hooks
- Phase 9: Cross-compile CI matrix gating merges
