# Phase 2: Ollama End-to-End - Research

**Researched:** 2026-05-23
**Domain:** Go HTTP gateway — Ollama-shape API adapter on top of canonical engine + ACP pool
**Confidence:** HIGH

## Summary

Phase 2 is the first true vertical slice of the Loop24 Gateway: an Ollama-shaped HTTP surface
on `:11434` routes `POST /api/chat` (non-streaming only) through a canonical engine, through a
pool-of-1 ACP client, into `kiro-cli acp`, and returns a shape that LangFlow consumes with
zero reconfiguration. Six static endpoints (`/api/tags`, `/api/show`, `/api/ps`,
`/api/version`, plus stubs for `/api/pull|push|create|copy|delete`) round out the surface, and
two pieces of cross-cutting infrastructure land for the first time: a bearer-token + IP
allowlist middleware that every later surface phase will reuse, and an `engine.pickCwd`
algorithm that derives the per-request `cwd` from `X-Working-Dir` ▸ longest-common-parent of
`resource_link` URIs ▸ `KIRO_CWD` ▸ `os.Getwd()`.

The phase is entirely bounded by locked decisions from CONTEXT.md (D-01…D-16) and the
canonical wire shapes in `docs/reference/acp_wire_shapes.md` — there is essentially no
open-ended design left. The research focus is on (a) confirming the precise Ollama JSON
wire shapes that LangFlow and OpenAI ecosystem clients expect, (b) the idiomatic chi
patterns for the exempt-route + protected sub-router split (D-14), (c) the idiomatic Go
shape of a channel-of-slots warm pool (D-06 / POOL-03), (d) constant-time bearer-token
comparison + safe IP-allowlist extraction, and (e) the test scaffolding shape that lets us
do handler-level testing against a fake engine + integration testing against real `kiro-cli`.

**Primary recommendation:** Build Phase 2 as **five paper-thin packages plus one adapter** —
`internal/engine` (orchestrator + hook seam + pickCwd + buildBlocks + Collect), `internal/pool`
(channel-of-1-slot pool satisfying `engine.ACPClient`), `internal/auth` (bearer + IP allowlist
middleware), `internal/adapter/ollama` (wire ↔ canonical + chi sub-router + 11 endpoints),
plus type-only extensions to `internal/canonical`. Use chi's `Router.Route("/api", func(r) {
r.Use(auth...); ... })` pattern for the protected sub-router; keep `/`, `/api/version`, and
`/health` on the outer router (registered before the `Route` block) so they bypass auth.
Use stdlib `net/http` everywhere (no fasthttp per project constraint).

## User Constraints (from CONTEXT.md)

### Locked Decisions

**Engine surface (`internal/engine`)**

- **D-01: Channel + Chat helper.** `engine.Run(ctx, *canonical.ChatRequest) (*engine.Run, error)` returns a handle with `Chunks <-chan canonical.Chunk` and `Result() (*canonical.ChatResponse, error)`. A thin `engine.Collect(ctx, req) (*canonical.ChatResponse, error)` helper in the engine package wraps `Run` and ranges Chunks to completion. Phase 2 adapter calls `Collect` (non-streaming). Phase 4 SSE/NDJSON adapter ranges over `Chunks` directly. Mirrors Phase 1's `acp.Client.Prompt` → `*acp.Stream` pattern.

- **D-02: Block flattening inside engine package.** `engine.buildBlocks(*canonical.ChatRequest) []canonical.Block` is the single source of truth for the bracketed-section ACP text format (`[System]/[User]/[Assistant]/[Assistant tool call: <name>]/[Tool result (id: ...)]/[Available tools]/[Reasoning]/[Output format]`). All three adapters (Phase 2 Ollama, Phase 3 OpenAI, Phase 3.1 Anthropic) translate their native JSON to `canonical.ChatRequest` and let engine produce the ACP blocks.

- **D-03: Engine constructor — concrete struct + consumer-defined `engine.ACPClient` interface.** `engine.Engine` is a concrete struct. It depends on a local `engine.ACPClient` interface (NewSession + SetModel + Prompt + Cancel) that `*acp.Client` and `*pool.Pool` both satisfy structurally. Constructor: `engine.New(engine.Config{Logger, ACP, ...})`.

- **D-04: Phase 8 hook chain seam present, empty in Phase 2.** Engine has `preHooks []PreHook` + `postHooks []PostHook` fields (empty in Phase 2). `PreHook` and `PostHook` interfaces are defined now. `Run` ranges over both (no-op in Phase 2 since slices are empty); a `PreHook` returning non-nil response short-circuits the engine.

- **D-05: Session lifecycle — new ACP session per Run.** Each `engine.Run` calls `acp.NewSession(ctx, cwd)` → optional `SetModel(ctx, sid, req.Model)` (skipped when `req.Model` is `""` or `"auto"`) → `Prompt(ctx, sid, blocks)` → stream chunks. `cwd` is derived per request via `engine.pickCwd(req)`.

**ACP concurrency model (`internal/pool`)**

- **D-06: Pool-of-1 in `internal/pool` now.** Phase 2 creates the `internal/pool` package with size=1 default. `*pool.Pool` satisfies `engine.ACPClient` interface. Channel-of-slots design per POOL-03.

- **D-07: `POOL_SIZE` default = 1 in Phase 2.** Env var override still works.

- **D-07a: Fail-fast warmup, sequential.** `Pool.Warmup(ctx)` spawns + `Initialize` each slot sequentially. Any failure → close partially-built slots, return error, `main.go` exits non-zero.

**Canonical types (`internal/canonical`)**

- **D-08: `canonical.ChatRequest` is tri-surface forward-designed.** Fields: `Model string`, `System string`, `Messages []Message`, `Tools []ToolSpec`, `ToolChoice *ToolChoice`, `MaxTokens int`, `Temperature *float64`, `TopP *float64`, `StopSequences []string`, `Stream bool`, `Think bool`, `Format *Format`, `Metadata map[string]any`, `WorkingDirOverride string`. Phase 2 only populates Model + System + Messages + Options-derived fields + WorkingDirOverride.

- **D-09: `canonical.Message` uses `[]ContentPart` from day one.** Phase 2 only populates `Kind == Text` (single part) and `Kind == Image` (from Ollama `messages[].images: [b64]`).

- **D-10: `canonical.ChatResponse` symmetric.** Fields: `ID string`, `Model string`, `Message Message`, `StopReason StopReason` (enum), `Usage Usage`. Phase 2 populates ID + Model + Message.Content[text] + StopReason.

- **D-11: No JSON tags on `canonical` types.** Wire translation lives in adapters.

**HTTP surface (`internal/server`, `internal/adapter/ollama`)**

- **D-12: `/api/version` moves to `internal/adapter/ollama`.** Ollama-shape JSON: `{version, commit}`. Phase 2 adds: `/api/chat` (POST), `/api/generate` (POST), `/api/tags` (GET), `/api/show` (POST), `/api/ps` (GET), plus success stubs for `/api/pull|push|create|copy` and `DELETE /api/delete`.

- **D-13: Model catalog source — capture once at pool warmup** from the first slot's `session/new` `result.models.availableModels[]`. Pool stores `[]ModelInfo{ID, Name}`. `/api/tags` returns it in Ollama shape; `/api/show` looks up by name.

- **D-14: Auth + IP allowlist — exempt-route subrouter.** Two chi middlewares: `auth.Bearer(token)` validates `AUTH_TOKEN` (empty = no auth); `auth.IPAllowlist(cidrs)` validates `ALLOWED_IPS` (empty = allow-all). Apply via chi sub-router; exempt paths (`/`, `/api/version`, `/health`) on outer router. Order: `RequestID` → `Recoverer` → `accessLog` → `Auth + IPAllowlist`.

- **D-15: Stub endpoints return Ollama-shape success responses** (not 501 / empty).

- **D-16: Per-request cwd derivation.** Algorithm: (1) `X-Working-Dir` header verbatim; (2) longest common parent of `resource_link.URI` values (file:// only); (3) `KIRO_CWD` env var; (4) `os.Getwd()`. Lives in `internal/engine/pickcwd.go` as a pure function with property tests.

### Claude's Discretion

The planner has latitude on:

- Exact chi sub-router composition for D-14.
- Exact Ollama metadata default values in D-13 (`modified_at`, `digest`, `size`, `family`).
- Whether `internal/pool.Config` is `Config` or `*Config` parameter type — pick one and be consistent.
- Whether `engine.ChatResponse` assembly lives in `engine/assemble.go` or inline in `engine/collect.go`.
- Test package strategy per file (whitebox vs blackbox); D-18 from Phase 1 generalizes.
- Error message wording, log key naming.
- Exact 5-tuple HTTP status codes for various error conditions.

### Deferred Ideas (OUT OF SCOPE)

- **Streaming for `/api/chat`** — Phase 4. Phase 2 only honors `stream:false`.
- **Dead-slot detection + lazy re-spawn** — Phase 5 (POOL-04).
- **Parallel warmup** — Phase 5.
- **Stateful sessions via `X-Session-Id`** — Phase 5 (SESS-*).
- **`/health/agents`** — Phase 5 (OBSV-02).
- **`coerceToolCall`** — Phase 6 (TOOL-02).
- **Tool-call rendering in responses** — Phase 6 (TOOL-01).
- **Tool spec normalization** — Phase 6 (TOOL-03).
- **`AuthHook` (engine plugin)** — Phase 8 (PLUG-04).
- **Property tests for `pickCwd`** (TRST-06) — happens incrementally starting Phase 2.
- **`Example_` documentation functions** — added for `pickCwd` and `buildAcpBlocks` in Phase 2; `coerceToolCall` in Phase 6.
- **OpenAI surface enabling/disabling at boot** — Phase 3.

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| SURF-01 | HTTP server binds a single port (`:11434`) and mounts both API surfaces in one process | Already in place from Phase 1 (`internal/server`); Phase 2 mounts the Ollama adapter sub-router |
| SURF-03 | `POST /api/chat`, `POST /api/generate`, `GET /api/tags`, `POST /api/show`, `GET /api/ps`, `GET /api/version` are served with Ollama-compatible request/response shapes | Wire shapes verified in §"Ollama Wire Shapes" below — sourced from Ollama API docs + Node reference |
| SURF-05 | Existing LangFlow flows pointing at `/api/chat` work with zero reconfiguration | LangFlow's expected fields documented (model, message, done_reason, durations) — confirmed against Ollama doc and Node implementation |
| SURF-07 | Stubs returning success for `POST /api/pull`, `/api/push`, `/api/create`, `/api/copy`, `DELETE /api/delete` | Node implementation (lines 1014-1029) shows exact `{status:"success"}` shape with stream:true NDJSON variant for `/api/pull` |
| ACP-07 | Per-request `cwd` derived from longest common parent of `resource_link` block URIs, with `KIRO_CWD` fallback and `X-Working-Dir` override | Algorithm in `pickCwd` (Node line 85-99); idiomatic Go translation documented in §"pickCwd Algorithm" below |
| AUTH-01 | Bearer-token auth via `AUTH_TOKEN` env var (comma-separated, empty = no auth) | `crypto/subtle.ConstantTimeCompare` for safe comparison; Node reference at line 701-705 |
| AUTH-02 | IP allowlist via `ALLOWED_IPS` env var (comma-separated, empty = allow-all) | `net.ParseIP` + `netip.ParsePrefix` for CIDR support; Node reference at line 696-699 |
| AUTH-03 | Auth and allowlist middleware exempt `/`, `/api/version`, `/health` paths | chi's `Router.Route()` pattern keeps middleware scoped to sub-tree; exempt routes registered on outer router |
| OBSV-01 | `GET /health` returns pool stats, session registry stats, embedding registry stats | Existing `HealthResponse`/`PoolStats` types in `internal/server/health.go`; wire `pool.Stats()` into `PoolStats` |
| POOL-01 | Fixed-size pool (Phase 2: default 1) of warm `kiro-cli` subprocesses | `chan *Slot` with `len=size`; documented in §"Warm-Pool Patterns" below |
| POOL-02 | Pool warmup completes before `http.Server.ListenAndServe()` accepts connections | `main.go` orders `pool.Warmup(ctx)` before `srv.RunUntilSignal(ctx)`; fail-fast on warmup error |
| POOL-03 | `Acquire` returns the first free slot or blocks on a buffered channel of free slots | Channel receive (`<-pool.slots`) is the acquire; channel send is the release; deadlock-free pattern documented |

## Project Constraints (from CLAUDE.md)

These are non-negotiable from project setup; the planner must verify compliance for every task:

- **Go 1.23+** required (for `log/slog` ergonomics and `net/http` post-1.22 routing).
- **No cgo in main binary** — `CGO_ENABLED=0` enforced; preserves trivial cross-compile.
- **stdlib `net/http` + chi for routing** — `fasthttp` explicitly rejected; do not propose alternatives.
- **Ollama wire shapes fixed by LangFlow flows** — breaking changes require flow migration we won't pay for. Phase 2's API responses MUST match the Node implementation's shapes wherever LangFlow consumes them.
- **`gosec` G204 required for subprocess spawn surface** — any task spawning processes (the pool slot init) must have explicit `//nolint:gosec` justification matching the existing Phase 1 pattern in `internal/acp/client.go:162`.
- **Trust gates non-negotiable from day one** — `golangci-lint` strict config, `govulncheck`, `go test -race`, `goleak.VerifyTestMain`, pre-commit hooks already wired in Phase 1; Phase 2 must keep them green.
- **Env var names match Node version** — `AUTH_TOKEN`, `ALLOWED_IPS`, `POOL_SIZE`, `KIRO_CWD` (already established); Phase 2 adds `OLLAMA_PATH_PREFIX` (default `/api`) and `OPENAI_PATH_PREFIX` (default `/v1`, **read-but-unused** in Phase 2 for forward design).
- **GSD workflow enforcement** — every code change in Phase 2 happens through `/gsd-execute-phase` (no direct edits outside the workflow).

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Wire ↔ canonical JSON translation | Adapter (`internal/adapter/ollama`) | — | Bifrost-validated adapter-over-canonical pattern; adapter is the only layer that touches Ollama-shaped JSON |
| HTTP handler dispatch | Adapter (chi sub-router) | Server (mounts the sub-router) | Server owns top-level chi router + cross-cutting middleware; adapter owns Ollama-prefix routes |
| Canonical→ACP block flattening | Engine (`internal/engine.buildBlocks`) | — | D-02: single source of truth across all 3 surfaces |
| `cwd` derivation per request | Engine (`internal/engine.pickCwd`) | — | Pure function over canonical request; engine consumes canonical, not wire types |
| ACP session lifecycle (new + setModel + prompt + cancel) | Engine (orchestrator) | Pool (provides ACPClient) | Engine drives the lifecycle; pool routes the calls to an acquired slot |
| Subprocess spawn + JSON-RPC stdio | ACP (`internal/acp.Client` — Phase 1) | — | Already in place; Phase 1.1 fixes wire shapes |
| Slot acquire / release / channel-of-slots | Pool (`internal/pool`) | — | POOL-03 mandate; engine sees the pool only via the ACPClient interface |
| Bearer token + IP allowlist gate | Auth middleware (`internal/auth`) | Server (mounts middleware) | HTTP-layer concern in Phase 2; Phase 8 refactors auth into engine PreHook |
| Health status aggregation | Server (`/health` handler) | Pool (provides `Stats()`) | Existing handler from Phase 1; only the data source (`PoolStats`) changes |
| Build-time version injection | `internal/version` (Phase 1) | Adapter (`/api/version` renders) | Phase 1 wires `-ldflags`; Phase 2 just reads `version.Version` + `debug.ReadBuildInfo()` for commit |
| Request ID propagation, logging | Server middleware (existing) | All downstream | `chi/v5/middleware.RequestID` + custom `accessLog` already in place from Phase 1 |

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/go-chi/chi/v5` | v5.3.0 (already pinned) | HTTP routing + middleware composition | Already in `go.mod`; chosen in Phase 1; idiomatic for stdlib `net/http` interop; supports `Route()`-scoped middleware which is exactly what D-14 needs |
| `go.uber.org/goleak` | v1.3.0 (already pinned) | Goroutine leak detection in tests | Already in `go.mod`; required by TRST-05; existing `testmain_test.go` pattern in `internal/acp/` is the template |
| stdlib `log/slog` | Go 1.23 | Structured logging | Project decision (no zerolog/zap); existing `*slog.Logger` propagation via Config (Phase 1 D-15) |
| stdlib `net/http` | Go 1.23 | HTTP server | Project decision (no fasthttp); existing `internal/server` from Phase 1 |
| stdlib `crypto/subtle` | Go 1.23 | Constant-time bearer-token comparison | `subtle.ConstantTimeCompare([]byte, []byte) int` — the only correct way to compare auth tokens; defense against timing attacks |
| stdlib `net/netip` | Go 1.23 (improvements through 1.23) | IP + CIDR parsing for allowlist | `netip.Addr`, `netip.Prefix`, `netip.ParseAddr`, `netip.ParsePrefix` — modern replacement for `net.ParseIP` + `net.IPNet`; immutable value type, no allocations [VERIFIED: Go stdlib docs] |
| stdlib `net/url` | Go 1.23 | Parse `file://` URIs in `pickCwd` | `url.Parse` handles `file:///path` (POSIX) and `file:///C:/path` (Windows); the `.Path` field has the OS-relevant path |
| stdlib `path/filepath` | Go 1.23 | Longest-common-parent computation, OS-aware path handling | `filepath.Dir`, `filepath.Clean`, `filepath.Separator`; cross-platform; works on `/foo/bar` and `C:\foo\bar` correctly when invoked on the target OS |
| stdlib `runtime/debug` | Go 1.23 | VCS revision for `/api/version` `commit` field | `debug.ReadBuildInfo()` exposes `BuildInfo.Settings` where `vcs.revision` is populated when built from a git working tree |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| stdlib `testing/quick` | Go 1.23 | Property tests for `pickCwd` | TRST-06 mandates rapid OR testing/quick; `testing/quick` is zero-dep and sufficient for `pickCwd`'s pure-function shape. Defer `pgregory.net/rapid` until Phase 6's `coerceToolCall` needs richer generators. |
| stdlib `net/http/httptest` | Go 1.23 | Handler-level tests with `httptest.NewRecorder` and `httptest.NewServer` | Already used in Phase 1 tests; no additional dep needed |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| chi `Route()` for sub-router scoping | chi `Mount()` for a pre-built `*chi.Mux` | `Mount()` is for "fully external" sub-routers (e.g., a third-party handler mounted under a prefix). `Route()` is the idiomatic chi way when you want middleware scoped to the sub-tree but the routes live in your own code. **Use `Route()` for D-14.** [CITED: go-chi README + WebFetch verification] |
| chi `Route()` for sub-router scoping | Outer router with `chi.With(authMW).Method(...)` per route | Forces auth declaration on every protected route — bug-prone (a new endpoint forgotten becomes accidentally public). `Route()` makes the protected subtree the default, exempt routes the explicit exception. **Strongly prefer `Route()`.** |
| `netip.Prefix` for CIDR allowlist | `net.ParseCIDR` + `net.IPNet.Contains` | Both work. `netip` is the modern path (immutable values, no allocations on parsing). `net.IPNet` is older but more familiar. **Use `netip`** since this is greenfield code and the user is learning idiomatic Go. |
| `subtle.ConstantTimeCompare` for token comparison | Plain `==` comparison | `==` leaks timing information (early-exit on first byte mismatch) — a remote attacker can in principle recover one byte per O(1000) requests. The Node reference uses plain `===` because Node's V8 optimization makes this less practical to exploit, but Go has no such mitigation. **Always use `ConstantTimeCompare`.** This is also what `gosec` G401-ish checks expect (no specific rule fires today but the pattern is the documented Go idiom). |
| Direct env-var read in middleware | Read config in `main`, pass tokens via constructor | Config-struct-driven (Phase 1 D-05); middleware takes `tokens []string` + `allowedIPs []netip.Prefix` via `auth.Config{...}`. Matches engine + pool constructors. **Use config-struct.** |

**Installation:**

No new dependencies needed for Phase 2. The Go module is already pinned to:

```
github.com/go-chi/chi/v5 v5.3.0
go.uber.org/goleak v1.3.0
```

Everything else (`crypto/subtle`, `net/netip`, `runtime/debug`, `testing/quick`, etc.) is stdlib.

**Version verification:**

- `chi/v5 v5.3.0` — already in `go.mod`. Latest is `v5.2.x` series as of late 2024; v5.3.0 is the same major-stability commitment. [VERIFIED: existing go.mod]
- `goleak v1.3.0` — already in `go.mod`. [VERIFIED: existing go.mod]
- Go stdlib references checked against Go 1.23 release notes — `net/netip` matured through 1.23 and is the recommended IP-handling package. [CITED: pkg.go.dev/net/netip]

## Package Legitimacy Audit

> Phase 2 installs **zero** new external packages. All work is via existing stdlib + chi + goleak.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| (none — all dependencies are stdlib or already-vetted Phase 1 deps) | — | — | — | — | — | — |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

The Phase 2 scope explicitly avoids new third-party deps. If a planning artifact later
proposes adding a dep, that proposal must run the package legitimacy gate before merge.

## Architecture Patterns

### System Architecture Diagram

```
LangFlow flow → POST :11434/api/chat
                         │
                         ▼
        ┌────────────────────────────────────────┐
        │  internal/server (chi root router)     │
        │  Middleware: RequestID → Recoverer →   │
        │              accessLog                 │
        │  Outer routes (EXEMPT from auth):       │
        │    GET /  GET /api/version  GET /health │
        └────────────────────┬───────────────────┘
                             │
                             ▼  (Route("/api", …))
        ┌────────────────────────────────────────┐
        │  Ollama adapter sub-router              │
        │  Middleware (added here, not outer):   │
        │    auth.Bearer  →  auth.IPAllowlist    │
        │  Routes: /api/chat /api/generate /tags  │
        │          /api/show /api/ps  + 5 stubs   │
        └────────────────────┬───────────────────┘
                             │
                             ▼  (handler unmarshals Ollama JSON)
        ┌────────────────────────────────────────┐
        │  adapter/ollama/wire.go                │
        │  decodeChatRequest → canonical.ChatReq  │
        └────────────────────┬───────────────────┘
                             │
                             ▼
        ┌────────────────────────────────────────┐
        │  internal/engine.Engine                │
        │  Run(ctx, *canonical.ChatRequest)      │
        │    1. PreHooks (empty in P2 — D-04)    │
        │    2. cwd := pickCwd(req)              │
        │    3. blocks := buildBlocks(req)       │
        │    4. sid := acp.NewSession(cwd)       │
        │    5. acp.SetModel(sid, req.Model)?    │
        │    6. stream := acp.Prompt(sid, blocks)│
        │    7. range stream.Chunks → chan       │
        │    8. PostHooks (empty in P2)          │
        │  Collect() → assemble ChatResponse     │
        └────────────────────┬───────────────────┘
                             │
                             ▼  (engine.ACPClient interface)
        ┌────────────────────────────────────────┐
        │  internal/pool.Pool (size=1)            │
        │  acquired := <- p.slots                 │
        │  defer release: p.slots <- acquired    │
        │  NewSession / SetModel / Prompt /       │
        │  Cancel → forward to acquired.client    │
        └────────────────────┬───────────────────┘
                             │
                             ▼  (*acp.Client from Phase 1.1)
        ┌────────────────────────────────────────┐
        │  kiro-cli subprocess (one per slot)     │
        │  JSON-RPC over stdio                    │
        └────────────────────────────────────────┘
```

Adapter returns Ollama-shaped JSON; handler writes it via `json.Encoder` with proper Content-Type.
On error: adapter renders `{"error": "..."}` shape (Node compatibility) with appropriate HTTP status.

### Recommended Project Structure

```
internal/
├── adapter/
│   └── ollama/                 # NEW
│       ├── adapter.go          # type Adapter; New(Config); Router() chi.Router
│       ├── wire.go             # native ↔ canonical translation (decodeChatRequest, encodeChatResponse, etc.)
│       ├── handlers.go         # chat, generate, tags, show, ps, version
│       ├── stub.go             # pull/push/create/copy/delete success-stub handlers
│       └── *_test.go           # blackbox handler tests with fake engine + model catalog
├── auth/                       # NEW
│   ├── auth.go                 # Config; New(Config) *Auth; Bearer + IPAllowlist middlewares
│   ├── bearer.go               # subtle.ConstantTimeCompare implementation
│   ├── ipallowlist.go          # netip.Prefix matching, X-Forwarded-For handling
│   └── auth_test.go            # whitebox unit tests + blackbox middleware tests
├── engine/                     # NEW
│   ├── engine.go               # type Engine; engine.Config; type Run; ACPClient interface
│   ├── build_blocks.go         # buildBlocks(*canonical.ChatRequest) []canonical.Block
│   ├── pickcwd.go              # pure function pickCwd(*canonical.ChatRequest) string
│   ├── collect.go              # Collect(ctx, req) (*canonical.ChatResponse, error) helper
│   ├── hooks.go                # PreHook + PostHook interfaces (Phase 8 seam)
│   └── *_test.go               # whitebox unit tests + property tests for pickCwd / buildBlocks
├── pool/                       # NEW (currently empty placeholder)
│   ├── pool.go                 # type Pool; type Slot; Warmup; NewSession/SetModel/Prompt/Cancel
│   ├── config.go               # type Config (Size, KiroCmd, KiroArgs, Logger)
│   ├── stats.go                # type Stats; (p *Pool) Stats() Stats — for /health
│   └── pool_test.go            # whitebox tests (warmup error paths, acquire/release, stats race-test)
├── canonical/
│   └── chunk.go                # EXTEND — add ChatRequest, ChatResponse, Message, ContentPart,
│                               # ToolCall, ToolSpec, ToolChoice, Format, Usage, MessageRole,
│                               # ContentKind, StopReason
│                               # Also add Name field to ResourceLinkBlock (per Phase 1.1)
│                               # Also add ImagePart type for D-09's image-block support
├── server/
│   ├── server.go               # MODIFY — remove version handler (moves to adapter); add Mount() entry
│   ├── health.go               # MODIFY — wire pool.Stats() into PoolStats
│   └── middleware.go           # unchanged (existing accessLog stays)
└── config/
    └── config.go               # EXTEND — add AuthToken, AllowedIPs, PoolSize,
                                # OllamaPathPrefix, OpenAIPathPrefix
```

### Pattern 1: Adapter-over-Canonical with chi Sub-Router

**What:** The Ollama adapter owns its own chi sub-router. The server's job is to attach
cross-cutting middleware (RequestID, Recoverer, accessLog) on the outer router and call
`outerRouter.Route("/api", func(r chi.Router) { r.Use(authMW); r.Mount("/", ollamaAdapter.Router()) })`.

**When to use:** Whenever a new surface (Phase 3 OpenAI, Phase 3.1 Anthropic) needs to be
added — repeat the same pattern under its own prefix with its own auth scope.

**Example:**

```go
// internal/adapter/ollama/adapter.go
package ollama

import (
    "log/slog"
    "github.com/go-chi/chi/v5"
)

// Engine is the contract the Ollama adapter requires from the engine layer.
// Adapter does not import internal/engine directly (TRST-04 boundary).
type Engine interface {
    Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// ModelCatalog is the contract for /api/tags and /api/show. Satisfied by *pool.Pool.
type ModelCatalog interface {
    Models() []canonical.ModelInfo
}

type Config struct {
    Logger       *slog.Logger
    Engine       Engine
    ModelCatalog ModelCatalog
    Version      string
    Commit       string
}

type Adapter struct {
    cfg    Config
    router chi.Router
}

func New(cfg Config) *Adapter {
    a := &Adapter{cfg: cfg}
    r := chi.NewRouter()
    r.Get("/version", a.handleVersion)   // /api/version — exempt from auth, mounted on outer
    r.Post("/chat", a.handleChat)
    r.Post("/generate", a.handleGenerate)
    r.Get("/tags", a.handleTags)
    r.Post("/show", a.handleShow)
    r.Get("/ps", a.handlePS)
    r.Post("/pull", a.handlePull)
    r.Post("/push", a.handlePush)
    r.Post("/create", a.handleCreate)
    r.Post("/copy", a.handleCopy)
    r.Delete("/delete", a.handleDelete)
    a.router = r
    return a
}

func (a *Adapter) Router() chi.Router { return a.router }
```

```go
// internal/server/server.go (modified)
r := chi.NewRouter()
r.Use(middleware.RequestID)
r.Use(middleware.Recoverer)
r.Use(accessLog(logger))

// Exempt routes — register on outer router BEFORE the protected sub-tree.
r.Get("/health", s.healthHandler)
// Note: /api/version is exempt but lives under /api; we register it on the outer
// router using the FULL path so it bypasses the protected /api/ sub-tree.
r.Get("/api/version", ollamaAdapter.HandleVersion)  // exempted via direct registration

// Protected sub-tree: everything under /api EXCEPT /api/version
r.Route("/api", func(r chi.Router) {
    r.Use(auth.Bearer(authConfig))
    r.Use(auth.IPAllowlist(ipConfig))
    // Mount adapter routes WITHOUT the /version route (handled above on outer).
    r.Mount("/", ollamaAdapter.ProtectedRouter())  // returns a separate chi.Router with all routes except /version
})
```

**CRITICAL NOTE on D-14 implementation:** chi's `Route()` semantics mean middleware
registered inside the `Route(...)` callback ONLY applies to routes registered inside that
callback. Routes registered on the outer `r` (the `r.Get("/api/version", ...)` line) bypass
the sub-tree's middleware. **Chi matches the most-specific route first**, so the outer
`/api/version` registration wins over a hypothetical `r.Get("/version", ...)` inside the
`Route("/api", ...)` block. [VERIFIED: chi docs + WebFetch verification]

**Alternative implementation:** rather than splitting the adapter into "main router" +
"version router," the planner may have the server compose the protected sub-tree by
registering specific handlers individually:

```go
r.Route("/api", func(r chi.Router) {
    r.Use(auth.Bearer(authConfig))
    r.Use(auth.IPAllowlist(ipConfig))
    r.Post("/chat", ollamaAdapter.HandleChat)
    r.Post("/generate", ollamaAdapter.HandleGenerate)
    // ... etc
})
```

This trades adapter encapsulation for explicit per-route registration at the server level.
Either pattern is acceptable; CONTEXT.md leaves the choice to planner discretion.

### Pattern 2: Channel-of-Slots Warm Pool (POOL-03)

**What:** A fixed-size pool of warm `*acp.Client` instances, where acquire is a channel
receive and release is a channel send.

**When to use:** This is the entire shape of `internal/pool/pool.go` — no variations.

**Example:**

```go
// internal/pool/pool.go
package pool

import (
    "context"
    "fmt"
    "log/slog"
    "sync"

    "loop24-gateway/internal/acp"
    "loop24-gateway/internal/canonical"
)

type Slot struct {
    Label  string  // "pool-0", "pool-1", ...
    Client *acp.Client
}

type Pool struct {
    cfg    Config
    slots  chan *Slot   // buffered, size=cfg.Size; acquire = <-slots, release = slots <- slot
    all    []*Slot      // for Stats and Close (held under mu)
    models []canonical.ModelInfo  // captured once during Warmup
    mu     sync.Mutex   // guards all + closed
    closed bool
}

func New(cfg Config) *Pool {
    cfg.applyDefaults()  // PoolSize default 1
    return &Pool{
        cfg:   cfg,
        slots: make(chan *Slot, cfg.Size),
    }
}

// Warmup spawns + initializes Size kiro-cli subprocesses sequentially (D-07a).
// On any failure, partially-built slots are closed and the error returned.
// After successful Warmup, the first slot's session/new result.models.availableModels
// is captured for /api/tags. (D-13)
func (p *Pool) Warmup(ctx context.Context) error {
    for i := 0; i < p.cfg.Size; i++ {
        slot, err := p.initSlot(ctx, fmt.Sprintf("pool-%d", i))
        if err != nil {
            p.closeAll()
            return fmt.Errorf("pool: warmup slot %d: %w", i, err)
        }
        p.mu.Lock()
        p.all = append(p.all, slot)
        p.mu.Unlock()
        p.slots <- slot  // never blocks (chan has size capacity)

        // Capture model catalog from the first slot only (D-13).
        if i == 0 {
            sid, err := slot.Client.NewSession(ctx, p.cfg.KiroCWD)
            if err != nil {
                p.closeAll()
                return fmt.Errorf("pool: warmup model-catalog session: %w", err)
            }
            // Use the AvailableModels accessor added in Phase 1.1.
            p.models = slot.Client.AvailableModels()
            _ = sid  // discarded; this session is only for capability discovery
        }
    }
    return nil
}

func (p *Pool) initSlot(ctx context.Context, label string) (*Slot, error) {
    cfg := acp.Config{
        Logger:       p.cfg.Logger.With("slot", label),
        Command:      p.cfg.KiroCmd,
        Args:         p.cfg.KiroArgs,
        Cwd:          p.cfg.KiroCWD,
        PingInterval: p.cfg.PingInterval,
    }
    client, err := acp.New(cfg)
    if err != nil {
        return nil, fmt.Errorf("acp.New: %w", err)
    }
    if err := client.Initialize(ctx); err != nil {
        _ = client.Close()
        return nil, fmt.Errorf("acp.Initialize: %w", err)
    }
    return &Slot{Label: label, Client: client}, nil
}

// NewSession routes to an acquired slot, performs the call, and releases the slot.
// Phase 2 = one in-flight Prompt per slot at a time; deeper concurrency comes in Phase 5.
//
// CRITICAL: NewSession + SetModel + Prompt + Cancel must all share the same acquired
// slot for a single logical "Run." This requires NewSession to return a session ID that
// downstream calls reference, while the Pool tracks which slot owns the session.
//
// Phase 2 simplification: because Phase 2 issues NewSession → optional SetModel → Prompt
// → optional Cancel in a single sequence per Run, the engine acquires the slot at the
// START of Run and releases at the END. The ACPClient interface needs an Acquire/Release
// method pair OR a `WithSession(ctx, cwd, fn)` helper. The planner decides the exact shape.
func (p *Pool) Stats() Stats {
    p.mu.Lock()
    defer p.mu.Unlock()
    busy := 0
    alive := 0
    for _, s := range p.all {
        // alive check: Phase 5 will add a real liveness check; Phase 2 = client != nil
        if s.Client != nil {
            alive++
        }
    }
    busy = len(p.all) - len(p.slots) - (len(p.all) - alive)
    return Stats{
        Size:  p.cfg.Size,
        Alive: alive,
        Busy:  busy,
    }
}

func (p *Pool) Close() error {
    p.mu.Lock()
    if p.closed {
        p.mu.Unlock()
        return nil
    }
    p.closed = true
    p.mu.Unlock()
    return p.closeAll()
}

func (p *Pool) closeAll() error {
    var firstErr error
    p.mu.Lock()
    defer p.mu.Unlock()
    for _, s := range p.all {
        if s.Client != nil {
            if err := s.Client.Close(); err != nil && firstErr == nil {
                firstErr = err
            }
        }
    }
    return firstErr
}
```

**[CITED: Phase 1 internal/acp/client.go close-ordering pattern.] [VERIFIED: Node reference acp-ollama-server.js:330-373 ACPPool class as the design source.]**

The signature of `engine.ACPClient` needs careful design — see §"Engine Interface Shape" in
Code Examples below. The recommended shape exposes `Acquire(ctx) (*Slot, error)` + `slot.NewSession(...)` etc., but a `WithSlot(ctx, fn)` callback shape is equally valid and may be more leak-safe (release always happens in deferred function).

### Pattern 3: Bearer Token Middleware with Constant-Time Comparison

**What:** Validate `Authorization: Bearer <token>` against a list of valid tokens using
`crypto/subtle.ConstantTimeCompare` to defend against timing attacks.

**When to use:** The `auth.Bearer(cfg)` middleware. Empty token list = no auth (Node default).

**Example:**

```go
// internal/auth/bearer.go
package auth

import (
    "crypto/subtle"
    "net/http"
    "strings"
)

// Bearer returns a middleware that validates the Authorization: Bearer <token> header
// against cfg.Tokens. An empty Tokens slice disables auth entirely (matches Node default).
// Uses constant-time comparison to defend against timing side-channels.
func Bearer(cfg Config) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if len(cfg.Tokens) == 0 {
                next.ServeHTTP(w, r)
                return
            }
            authHeader := r.Header.Get("Authorization")
            const prefix = "Bearer "
            if !strings.HasPrefix(authHeader, prefix) {
                writeOllamaError(w, http.StatusUnauthorized, "Invalid or missing API key")
                return
            }
            provided := []byte(authHeader[len(prefix):])
            for _, valid := range cfg.Tokens {
                if subtle.ConstantTimeCompare(provided, []byte(valid)) == 1 {
                    next.ServeHTTP(w, r)
                    return
                }
            }
            writeOllamaError(w, http.StatusUnauthorized, "Invalid or missing API key")
        })
    }
}
```

**Important:** the loop above is NOT constant-time across the token list (early exit on
match). For Phase 2 this is acceptable because the timing leak is at the level of "how
many tokens are configured," not "what the token bytes are." If hardening matters later,
iterate the full loop without short-circuit and OR the results.

### Pattern 4: IP Allowlist Middleware with netip

**What:** Match the request's client IP against a list of CIDRs (or single IPs).

**When to use:** The `auth.IPAllowlist(cfg)` middleware. Empty list = allow-all (Node default).

**Example:**

```go
// internal/auth/ipallowlist.go
package auth

import (
    "net/http"
    "net/netip"
    "strings"
)

// IPAllowlist returns a middleware that gates requests on client IP.
// Phase 2 assumes no proxy or a single trusted proxy: client IP comes from
// the first comma-separated value of X-Forwarded-For OR r.RemoteAddr.
//
// SECURITY NOTE: X-Forwarded-For trust requires a deployment where requests
// always traverse a known proxy. For laptop-deployment (PROJECT.md context)
// there is no proxy, so X-Forwarded-For should be treated as untrusted unless
// AUTH_TRUST_XFF env var is set. (Planner: decide whether to add this env var
// or document the laptop-only assumption inline.)
func IPAllowlist(cfg Config) func(http.Handler) http.Handler {
    if len(cfg.AllowedPrefixes) == 0 {
        // Allow-all: no-op middleware.
        return func(next http.Handler) http.Handler { return next }
    }
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            clientIP, ok := extractClientIP(r)
            if !ok {
                writeOllamaError(w, http.StatusForbidden, "could not determine client IP")
                return
            }
            for _, prefix := range cfg.AllowedPrefixes {
                if prefix.Contains(clientIP) {
                    next.ServeHTTP(w, r)
                    return
                }
            }
            writeOllamaError(w, http.StatusForbidden, "IP "+clientIP.String()+" not in allowlist")
        })
    }
}

func extractClientIP(r *http.Request) (netip.Addr, bool) {
    // Prefer X-Forwarded-For first hop, fall back to RemoteAddr.
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
        if addr, err := netip.ParseAddr(first); err == nil {
            return addr, true
        }
    }
    // RemoteAddr is "host:port"; strip the port.
    host, _, err := splitHostPort(r.RemoteAddr)
    if err != nil {
        return netip.Addr{}, false
    }
    addr, err := netip.ParseAddr(strings.TrimPrefix(host, "::ffff:"))
    return addr, err == nil
}
```

The `::ffff:` prefix strip is the IPv4-in-IPv6 mapping handler — Go's `RemoteAddr` returns
`[::ffff:127.0.0.1]:12345` for IPv4 connections on a dual-stack socket. Node's reference
implementation does the same strip (line 697).

### Anti-Patterns to Avoid

- **Comparing bearer tokens with `==`** — leaks timing information. Use `subtle.ConstantTimeCompare`.
- **Trusting `X-Forwarded-For` without a deployment proxy guarantee** — anyone can set this header. For Loop24's laptop deployment model, prefer `r.RemoteAddr` unless an explicit "trust XFF" env var is set.
- **Putting auth middleware on the outer router and then trying to exempt routes via path-prefix conditionals inside the middleware** — error-prone, easy to typo. Use chi's `Route()` sub-router scoping instead.
- **Sharing one `*acp.Client` across concurrent `Run` calls** — Phase 1's `Client` keeps a single `activeStream` field; concurrent `Prompt` calls would race. The pool is the boundary that serialises this; engine MUST acquire-then-release per Run.
- **Building the canonical `ChatRequest` inside the engine** — D-11 is clear: adapters do the wire ↔ canonical translation. The engine sees canonical only.
- **Encoding `[Available tools]`/`[Output format]`/`[Reasoning]` bracketed sections in the adapter** — D-02 puts this in `engine.buildBlocks`. Phase 2's Ollama adapter just translates messages → canonical and trusts the engine to bracket-flatten.
- **Adding JSON tags to canonical types** — D-11. JSON tags belong only in adapter wire types and the existing `internal/acp` wire types.
- **Hand-rolling a fixed-size worker pool with `sync.Cond` or `sync.WaitGroup` semantics** — POOL-03 mandates the channel-of-slots design. It's the idiomatic Go shape.
- **Renaming env vars or breaking the Node-compatible env-var contract** — `KIRO_CMD`, `AUTH_TOKEN`, `ALLOWED_IPS`, `POOL_SIZE` must stay verbatim.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| HTTP routing with sub-router middleware scoping | A custom path-prefix matcher with auth conditionals | chi `Router.Route("/api", …)` | chi handles trailing slashes, method matching, route precedence, and middleware scoping correctly. Hand-rolled versions get one of these wrong. |
| Constant-time string comparison | Loop with `^=` byte-XOR accumulation | `crypto/subtle.ConstantTimeCompare` | Already tested in production by the Go team; one of the most reviewed pieces of crypto code in stdlib. |
| IP / CIDR parsing | Regex on dotted-quad / netmask strings | `netip.ParseAddr` + `netip.ParsePrefix` + `Prefix.Contains` | IPv6, IPv4-in-IPv6 mapping, malformed input — all handled by stdlib. |
| JSON-RPC over stdio client | A new client | Existing `internal/acp.Client` from Phase 1 (post-1.1 fixes) | Done; just use it. |
| `file://` URI parsing | String prefix stripping | `net/url.Parse` then `u.Path` | Handles `file:///path` (Unix) AND `file:///C:/path` (Windows) correctly. **The Node reference uses a regex `replace(/^file:\/\//, '')` which works for Unix but breaks on Windows `file:///C:/...` paths** — Go can do better. |
| Goroutine leak detection in tests | Manual goroutine counting before/after | `goleak.VerifyTestMain` + `goleak.VerifyNone(t)` | Already used in Phase 1; copy the pattern. |
| Build-time version embedding | A `version.txt` read at runtime | `-ldflags="-X main.version=…"` (already in Phase 1) + `debug.ReadBuildInfo()` for vcs.revision | Already done; just read `version.Version` and `BuildInfo.Settings`. |
| HTTP request body size limiting | Custom `LimitReader` on every handler | `http.MaxBytesReader(w, r.Body, 8 << 20)` | Stdlib, well-tested, returns a proper 413 if exceeded. (Match Node's 8MB limit.) |

**Key insight:** Phase 2 has almost no "build from scratch" surface area — every problem
either has a stdlib answer (`subtle`, `netip`, `url`, `httptest`, `quick`) or reuses a
Phase 1 component (`acp.Client`, `chi/v5`, `goleak`, `slog`). The bulk of the work is
wiring + Ollama-shape JSON marshaling, not novel design.

## Common Pitfalls

### Pitfall 1: Adapter Importing Engine (TRST-04 Violation)

**What goes wrong:** Planner accidentally has `internal/adapter/ollama/handlers.go` do
`import "loop24-gateway/internal/engine"` to call `engine.Collect(...)` directly. This
violates the architectural boundary — adapter is supposed to depend on a consumer-defined
interface, not on the engine package.

**Why it happens:** It's the most natural way to write the code. The adapter needs to call
"something that does the Collect"; the obvious thing is the engine package.

**How to avoid:** The adapter defines its own `Engine` interface (`type Engine interface {
Collect(ctx, *canonical.ChatRequest) (*canonical.ChatResponse, error) }`) and accepts an
implementation via its `Config`. The `*engine.Engine` type from `internal/engine`
structurally satisfies this. `main.go` wires the concrete engine into the adapter at
construction time. This is the same pattern Phase 1.1 uses for `engine.ACPClient`.

**Warning signs:** `go-arch-lint` (already scaffolded in Phase 1, Plan 01-03) MUST fire on
any `internal/adapter/*` → `internal/engine` import. If you're staring at a compile error
asking "how does the adapter call the engine without importing it?", the answer is "define
an interface."

### Pitfall 2: Chi Middleware Order — accessLog Before Auth

**What goes wrong:** Planner registers `auth.Bearer` before `accessLog`, then denied
requests never appear in logs because the auth middleware rejects before the log line is
written.

**Why it happens:** Intuitive ordering is "auth first" because security comes first.

**How to avoid:** Order is **RequestID → Recoverer → accessLog → Auth → IPAllowlist**.
accessLog runs first so denied requests are still logged. This is also what the Node
implementation does (line 681-708: debug log middleware runs before the auth/IP
middleware).

**Warning signs:** A grep `r.Use(.*)` in the server constructor that shows auth before
`accessLog`. Test: send a request with an invalid bearer token; the server log should still
emit a "request" entry with status=401.

### Pitfall 3: `pickCwd` Failing on Windows `file:///C:/...` URIs

**What goes wrong:** Node reference uses `b.uri.replace(/^file:\/\//, '')` which produces
`/C:/path/to/file` on Windows — that's a broken path. Go's `url.Parse("file:///C:/x")`
gives `u.Path == "/C:/x"` which has the same issue. `filepath.Clean("/C:/x")` does not
"fix" it on Windows.

**Why it happens:** `file://` URIs are weirdly under-specified for Windows paths. The
correct mapping is `file:///C:/path` → `C:\path`.

**How to avoid:** In `pickCwd`, after `url.Parse`:

```go
p := u.Path
if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
    p = p[1:]  // strip leading slash from /C:/path → C:/path
}
p = filepath.FromSlash(p)  // C:/path → C:\path on Windows; no-op on Unix
```

Property tests for `pickCwd` (TRST-06) should include Windows-style inputs as fixed cases,
not relying on `testing/quick` to generate them.

**Warning signs:** Tests pass on macOS but `kiro-cli` on Windows starts with `cwd="/C:/..."`
and immediately fails because the path doesn't exist.

### Pitfall 4: Pool Warmup Race with HTTP Listener

**What goes wrong:** `main.go` starts the HTTP server before `pool.Warmup(ctx)` returns,
so the first request races against pool initialization and finds zero slots in the channel
(`<-pool.slots` blocks indefinitely or times out).

**Why it happens:** Async-by-default mindset — calling `pool.Warmup` in a goroutine seems
clean.

**How to avoid:** POOL-02 is explicit: warmup completes synchronously before
`http.Server.ListenAndServe()` accepts connections. `main.go` orders:

```go
pool := mustNewPool(cfg, logger)
defer pool.Close()
if err := pool.Warmup(ctx); err != nil {
    logger.Error("pool warmup failed", "err", err)
    os.Exit(1)
}
engine := engine.New(engine.Config{Logger: logger, ACP: pool})
ollama := ollama.New(ollama.Config{Logger: logger, Engine: engine, ModelCatalog: pool})
srv := server.New(...)
srv.RunUntilSignal(ctx)
```

**Warning signs:** A "first request after startup takes 5 seconds" pattern in tests; pool
stats showing `alive=0` mid-test.

### Pitfall 5: Stub-Endpoint Stream Format Mismatch

**What goes wrong:** Planner writes `/api/pull` to always return `{"status":"success"}` as
JSON, but LangFlow sends `{"stream":true}` and expects NDJSON. The flow hangs waiting for
chunked output.

**Why it happens:** D-15 says "stub endpoints return success" — easy to interpret as "one
JSON object."

**How to avoid:** Match the Node reference exactly (lines 1014-1029):

```go
// /api/pull — honors stream:true (default) by emitting NDJSON
func (a *Adapter) handlePull(w http.ResponseWriter, r *http.Request) {
    var req struct{ Stream *bool `json:"stream"` }
    _ = json.NewDecoder(r.Body).Decode(&req)
    stream := true
    if req.Stream != nil {
        stream = *req.Stream
    }
    if stream {
        a.writeNDJSONStub(w, []map[string]string{
            {"status": "pulling manifest"},
            {"status": "success"},
        })
        return
    }
    a.writeJSON(w, map[string]string{"status": "success"})
}
```

`/api/push`, `/api/create` follow the same pattern. `/api/copy`, `/api/delete` return `{}`.

**Warning signs:** SURF-05 (LangFlow zero-reconfig) failing on flows that include a "pull
model" step.

### Pitfall 6: Engine Forgetting to Cancel ACP Session on Error

**What goes wrong:** `engine.Run` calls `acp.NewSession` successfully, then `acp.Prompt`
errors. The session ID is leaked — kiro-cli holds resources for an abandoned session.

**Why it happens:** No `defer` cleanup after a successful `NewSession`.

**How to avoid:** Phase 2's engine.Run should defer a `Cancel` on the session ID that runs
unless the Prompt succeeded:

```go
sid, err := acp.NewSession(ctx, cwd)
if err != nil { return nil, err }
defer func() {
    if !runOK {
        acp.Cancel(sid)  // best-effort
    }
}()
```

Phase 1's `acp.Client.Cancel` is best-effort (notification, no response wait) — exactly the
right shape here.

**Warning signs:** `kiro-cli` memory growing over a long test run; "session not found"
log lines.

### Pitfall 7: Forgetting `runtime/debug.ReadBuildInfo()` Quirk

**What goes wrong:** `/api/version` returns `commit: "unknown"` even though the binary was
built from a git tree.

**Why it happens:** `debug.ReadBuildInfo().Main.Version` is `(devel)` for `go build`; only
`go install` sets the version field. The `vcs.revision` setting in `BuildInfo.Settings` IS
populated when the binary is built from inside a git worktree — but only when `-buildvcs=true`
(the default, but can be off in some CI configs).

**How to avoid:**

```go
func extractCommit() string {
    info, ok := debug.ReadBuildInfo()
    if !ok {
        return "unknown"
    }
    for _, s := range info.Settings {
        if s.Key == "vcs.revision" {
            return s.Value
        }
    }
    return "unknown"
}
```

Test by building with `go build` and running `curl /api/version` — `commit` should be a
short SHA.

**Warning signs:** `commit: "unknown"` in production after a fresh build. May indicate the
build was done outside a git worktree (e.g., `go install` from a downloaded tarball).

## Code Examples

### Engine Interface Shape (D-03)

```go
// internal/engine/engine.go
package engine

import (
    "context"
    "log/slog"

    "loop24-gateway/internal/canonical"
)

// ACPClient is the contract the engine requires from the ACP-talking layer.
// Both *acp.Client and *pool.Pool structurally satisfy this (the pool routes calls
// through an acquired slot).
//
// Phase 2 shape: NewSession returns a session id; Prompt streams chunks; Cancel is
// best-effort. The interface is consumer-defined here (not in internal/acp) per the
// Go community idiom — consumers define interfaces, producers expose concrete types.
type ACPClient interface {
    NewSession(ctx context.Context, cwd string) (string, error)
    SetModel(ctx context.Context, sessionID, modelID string) error
    Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (Stream, error)
    Cancel(sessionID string)
}

// Stream mirrors *acp.Stream but is interface-typed so the pool can return a
// pool-aware wrapper that releases the slot on stream.close.
type Stream interface {
    Chunks() <-chan canonical.Chunk
    Result() (*canonical.FinalResult, error)
}

type Config struct {
    Logger *slog.Logger
    ACP    ACPClient
    // PreHooks, PostHooks: empty in Phase 2 (D-04 seam).
    PreHooks  []PreHook
    PostHooks []PostHook
}

type Engine struct {
    cfg Config
}

func New(cfg Config) *Engine {
    return &Engine{cfg: cfg}
}
```

Note: The shape above adds a `Stream` interface. If the planner prefers, the engine can
accept the concrete `*acp.Stream` and the pool wraps `*acp.Client` more deeply. Either
works; the interface version is more decoupled.

### pickCwd Algorithm (D-16) — Idiomatic Go

```go
// internal/engine/pickcwd.go
package engine

import (
    "net/url"
    "os"
    "path/filepath"
    "runtime"
    "strings"

    "loop24-gateway/internal/canonical"
)

// pickCwd derives the per-request working directory using D-16's priority:
//
//   1. req.WorkingDirOverride (from X-Working-Dir header)
//   2. Longest common parent of file:// resource_link block URIs
//   3. defaultCwd (typically cfg.KiroCWD)
//   4. os.Getwd()
//
// The function is pure (no side effects beyond os.Getwd at the bottom) and is the
// subject of property tests per TRST-06.
func pickCwd(req *canonical.ChatRequest, defaultCwd string) string {
    if req.WorkingDirOverride != "" {
        return req.WorkingDirOverride
    }

    if parent := longestCommonParent(extractFileURIs(req)); parent != "" {
        return parent
    }

    if defaultCwd != "" {
        return defaultCwd
    }

    if cwd, err := os.Getwd(); err == nil {
        return cwd
    }
    return "."
}

func extractFileURIs(req *canonical.ChatRequest) []string {
    var uris []string
    for _, m := range req.Messages {
        for _, p := range m.Content {
            // Phase 2 only populates Text + Image kinds; resource_link extraction is
            // wired here for Phase 3+ requests that include resource links in canonical
            // form. For pure Phase 2 Ollama traffic, this returns []string{}.
            // (Resource links flow into the canonical layer only when an upstream
            // surface explicitly supports them — Ollama /api/chat doesn't.)
        }
    }
    return uris
}

func longestCommonParent(uris []string) string {
    if len(uris) == 0 {
        return ""
    }
    var paths []string
    for _, raw := range uris {
        u, err := url.Parse(raw)
        if err != nil || u.Scheme != "file" {
            continue
        }
        p := u.Path
        if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
            p = p[1:]
        }
        p = filepath.FromSlash(p)
        paths = append(paths, filepath.Dir(p))
    }
    if len(paths) == 0 {
        return ""
    }
    if len(paths) == 1 {
        return paths[0]
    }
    common := paths[0]
    for _, p := range paths[1:] {
        common = sharedPrefix(common, p)
        if common == "" {
            return ""
        }
    }
    return common
}

// sharedPrefix returns the longest common parent directory of a and b.
// Uses filepath.Separator so it's correct on both POSIX and Windows.
func sharedPrefix(a, b string) string {
    sep := string(filepath.Separator)
    aParts := strings.Split(filepath.Clean(a), sep)
    bParts := strings.Split(filepath.Clean(b), sep)
    n := len(aParts)
    if len(bParts) < n {
        n = len(bParts)
    }
    common := make([]string, 0, n)
    for i := 0; i < n; i++ {
        if aParts[i] != bParts[i] {
            break
        }
        common = append(common, aParts[i])
    }
    if len(common) == 0 {
        return ""
    }
    return strings.Join(common, sep)
}
```

### Property Test for pickCwd (TRST-06 — Phase 2 incremental)

```go
// internal/engine/pickcwd_test.go
package engine

import (
    "testing"
    "testing/quick"

    "loop24-gateway/internal/canonical"
)

func TestPickCwd_NeverPanics(t *testing.T) {
    f := func(override string, uris []string) bool {
        req := &canonical.ChatRequest{
            WorkingDirOverride: override,
        }
        // Add the URIs as synthetic resource_link blocks via the test-only helper.
        // ... build req.Messages ...
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("pickCwd panicked on input: override=%q uris=%v r=%v", override, uris, r)
            }
        }()
        _ = pickCwd(req, "/tmp")
        return true
    }
    if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
        t.Error(err)
    }
}

// Fixed-case tests (don't rely on quick to generate Windows-style paths)
func TestPickCwd_WindowsFileURI(t *testing.T) {
    // ... assert /C:/foo → C:\foo on Windows ...
}
```

### Ollama /api/chat Handler Skeleton

```go
// internal/adapter/ollama/handlers.go
package ollama

import (
    "encoding/json"
    "net/http"
    "time"

    "loop24-gateway/internal/canonical"
)

func (a *Adapter) handleChat(w http.ResponseWriter, r *http.Request) {
    r.Body = http.MaxBytesReader(w, r.Body, 8<<20)  // 8 MB cap (Node parity)

    var wire ollamaChatRequest
    if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
        a.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
        return
    }
    if len(wire.Messages) == 0 {
        a.writeError(w, http.StatusBadRequest, "`messages` is required and must be a non-empty array")
        return
    }
    if wire.Stream {  // Phase 2 doesn't stream; downgrade silently (Node parity)
        wire.Stream = false
    }

    req, err := wireToChatRequest(&wire, r)
    if err != nil {
        a.writeError(w, http.StatusBadRequest, err.Error())
        return
    }

    start := time.Now()
    resp, err := a.cfg.Engine.Collect(r.Context(), req)
    if err != nil {
        a.writeError(w, http.StatusInternalServerError, err.Error())
        return
    }

    a.writeJSON(w, chatResponseToWire(resp, start, wire.Model))
}
```

## Ollama Wire Shapes (CRITICAL for SURF-05)

The Node reference is the empirical ground truth; the Ollama public API doc is the spec.
Phase 2's wire shapes MUST match the Node reference for LangFlow compatibility — even
where the spec is ambiguous.

### `POST /api/chat` (stream:false)

**Request body:**
```json
{
  "model": "auto",
  "messages": [
    {"role": "system", "content": "You are…"},
    {"role": "user", "content": "hi", "images": ["<base64>", "..."]}
  ],
  "tools": [{"function": {"name": "...", "parameters": {...}}}],
  "format": "json"  | {"type": "object", ...},
  "stream": false,
  "think": false,
  "keep_alive": "5m",            // ACCEPTED-AND-IGNORED
  "options": {...}               // ACCEPTED-AND-IGNORED
}
```

**Response body (Phase 2 — no tool_calls because TOOL-* deferred):**
```json
{
  "model": "auto",
  "created_at": "2026-05-23T22:13:43.416799Z",
  "message": {
    "role": "assistant",
    "content": "Hello! How can I help?"
  },
  "done": true,
  "done_reason": "stop",
  "total_duration": 5191566416,
  "load_duration": 0,
  "prompt_eval_count": 26,
  "prompt_eval_duration": 778734962,
  "eval_count": 298,
  "eval_duration": 4412831454
}
```

**Phase 2 specifics:**
- `created_at`: ISO 8601 with fractional seconds, UTC (`time.Now().UTC().Format(time.RFC3339Nano)`).
- `done_reason`: `"stop"` for normal completion; `"tool_calls"` reserved for Phase 6.
  Mapping from canonical `StopReason`:
  - `StopEndTurn` → `"stop"`
  - `StopMaxTokens` → `"length"`
  - `StopSequenceMatch` → `"stop"`
  - `StopToolUse` → `"tool_calls"` (Phase 6 only)
  - `StopError` → `"stop"` with the assistant message containing the error string (Phase 2 may also choose to render an HTTP 500 — planner's call)
- Duration fields: nanoseconds (int64). Node uses `process.hrtime.bigint()` — Go uses `time.Since(start).Nanoseconds()`.
- `prompt_eval_count` / `eval_count`: token estimates. Node uses `Math.ceil(text.length / 4)`. Phase 2 should mirror this estimator for parity. (Real tokenization is out of scope for Phase 2.)
- `prompt_eval_duration` ≈ 15% of total; `eval_duration` ≈ 85% of total (Node convention, line 600-611). Phase 2 should use the same ratio.

[VERIFIED: Ollama API doc via WebFetch + Node reference acp-ollama-server.js:553-611]

### `POST /api/generate` (stream:false)

**Request body:**
```json
{
  "model": "auto",
  "prompt": "Why is the sky blue?",
  "system": "You are a physicist.",
  "images": ["<base64>", "..."],
  "format": "json",
  "stream": false,
  "think": false,
  "suffix": "...",     // ACCEPTED-AND-IGNORED
  "raw": false,        // ACCEPTED-AND-IGNORED
  "keep_alive": "5m",  // ACCEPTED-AND-IGNORED
  "options": {...}     // ACCEPTED-AND-IGNORED
}
```

**Response body:**
```json
{
  "model": "auto",
  "created_at": "2026-05-23T22:13:43.416799Z",
  "response": "The sky is blue because…",
  "done": true,
  "done_reason": "stop",
  "total_duration": 5043500667,
  "load_duration": 0,
  "prompt_eval_count": 26,
  "prompt_eval_duration": 756525100,
  "eval_count": 290,
  "eval_duration": 4286975567
}
```

Key difference from `/api/chat`: response carries `response: "..."` (string) not `message: {...}`.

[VERIFIED: Ollama API doc via WebFetch + Node reference acp-ollama-server.js:860-870]

### `GET /api/tags`

**Response body:**
```json
{
  "models": [
    {
      "name": "auto",
      "model": "auto",
      "modified_at": "2026-05-23T22:13:43.416799Z",
      "size": 0,
      "digest": "",
      "details": {
        "format": "gguf",
        "family": "kiro",
        "families": ["kiro"],
        "parameter_size": "unknown",
        "quantization_level": "unknown"
      }
    },
    {"name": "claude-sonnet-4-7", "model": "claude-sonnet-4-7", "...": "..."}
  ]
}
```

`auto` always appears first (per Node line 1045). Each entry mirrors `toOllamaModel` in
the Node reference (line 735-749). `digest` is empty string in the Node version; the real
Ollama uses a SHA hash. LangFlow doesn't validate the digest format — empty string works.

[VERIFIED: Node reference acp-ollama-server.js:751-759 + Ollama API doc.] [ASSUMED: LangFlow doesn't validate digest format — derived from working Node deployment.]

### `POST /api/show`

**Request body:**
```json
{"model": "auto"}
```

**Response body:**
```json
{
  "model": "auto",
  "modified_at": "2026-05-23T22:13:43.416799Z",
  "details": {
    "format": "gguf",
    "family": "kiro",
    "parameter_size": "unknown",
    "quantization_level": "unknown"
  },
  "capabilities": ["completion", "tools"],
  "modelinfo": {},
  "template": "",
  "parameters": "",
  "license": ""
}
```

Match Node exactly (line 761-776). 404 with Ollama-error shape if model not in catalog.

[VERIFIED: Node reference acp-ollama-server.js:761-776]

### `GET /api/ps`

**Response body:**
```json
{
  "models": [
    {
      "name": "auto",
      "model": "auto",
      "size": 0,
      "size_vram": 0,
      "details": {"format": "gguf", "family": "kiro"},
      "expires_at": "2026-05-23T22:43:43.416799Z"
    }
  ]
}
```

Synthetic single entry (Node line 778-789). `expires_at` is `time.Now().Add(sessionTTL)`
even though Phase 2 has no real sessions — keeps shape stable for downstream clients.

[VERIFIED: Node reference acp-ollama-server.js:778-789]

### `GET /api/version`

**Response body (Phase 2 — D-12 moves to adapter):**
```json
{"version": "0.9.0", "commit": "abc1234"}
```

`version` from `internal/version.Version` (already set via -ldflags in Phase 1).
`commit` from `debug.ReadBuildInfo().Settings[vcs.revision]`.

The Ollama public API returns only `{version: "x.y.z"}` (no `commit` field). The Node
reference returns only `version` too. Phase 1's `/api/version` returned both. The CONTEXT
explicitly says "{version: '<embedded>', commit: '<vcs.revision>'}" (D-12).

**Recommendation:** keep the `commit` field as a non-breaking extension. Real Ollama
clients don't validate that the response has ONLY the fields they expect; they read what
they need (`version`) and ignore the rest. **If LangFlow ever does strict shape validation,
the planner can remove the commit field — but for Phase 2 it stays.** [ASSUMED]

### Stub endpoints

`POST /api/pull` (default `stream:true`):

```
HTTP/1.1 200 OK
Content-Type: application/x-ndjson
Transfer-Encoding: chunked

{"status":"pulling manifest"}
{"status":"success"}
```

`POST /api/pull` with `stream:false`:
```json
{"status": "success"}
```

`POST /api/push`, `POST /api/create`: same pattern as `/api/pull` (honor `stream`).
`POST /api/copy`, `DELETE /api/delete`: always return `{}` with 200.

[VERIFIED: Node reference acp-ollama-server.js:1014-1029]

### Error shape

```json
{"error": "Invalid or missing API key"}
```

HTTP status codes:
- 400: bad JSON, missing required field, schema violation
- 401: auth failure (bearer mismatch / missing bearer when AUTH_TOKEN set)
- 403: IP allowlist failure
- 404: model not found in `/api/show`
- 413: request body exceeds 8 MB (`http.MaxBytesReader`)
- 500: engine error / kiro-cli failure / panic recovery

[VERIFIED: Node reference acp-ollama-server.js:101-103 ollamaError + handler call sites]

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `net.ParseIP` + `net.IPNet` | `net/netip.Addr` + `netip.Prefix` | Go 1.18 added `netip`; matured through 1.23 | Faster, allocation-free, comparable as map keys, comparable with `==`. Recommended for new code. |
| `net/http.ServeMux` per-path matching | `chi/v5.Router.Route()` for sub-tree scoping | chi v5 stable since 2020 | Middleware scoping by sub-tree is the killer feature; stdlib `ServeMux` post-Go 1.22 added method + path-prefix matching but still lacks per-subtree middleware composition. |
| `os/exec.Command` + manual context wiring | `exec.CommandContext` for cancellation propagation | Stable since Go 1.7 | Already used in Phase 1 `internal/acp/client.go`. Context cancellation → process kill is built in. |
| `time.Now().Format(...)` for timestamps | `time.RFC3339Nano` constant | Always available | Use `t.UTC().Format(time.RFC3339Nano)` for `created_at`. Don't roll a custom layout. |
| `encoding/json.Decoder.Decode` | Same — no replacement | n/a | stdlib JSON is fine for Loop24's throughput; Bifrost uses `sonic` (cgo!) for raw speed but it would break the cross-compile contract. |
| `subtle.ConstantTimeCompare` | Same — no replacement | Since Go 1.0 | This is THE answer for token comparison. |

**Deprecated/outdated:**
- **`net.IP` + `net.CIDR` + `net.IPNet`**: still works; not deprecated; just superseded for new code by `netip`. The two can interop via `netip.AddrFromSlice(net.IP)`.
- **`ioutil.ReadAll`**: deprecated; use `io.ReadAll`.

## Runtime State Inventory

> Phase 2 is a greenfield feature phase (no rename/refactor/migration); this section is informational only.

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | None — Phase 2 introduces no persistent state. Pool slots are in-memory. | None |
| Live service config | None — kiro-cli is spawned fresh per binary start; no external service config persists. | None |
| OS-registered state | None — Phase 2 binary remains foreground-only (D-22 from Phase 1). | None |
| Secrets/env vars | `AUTH_TOKEN`, `ALLOWED_IPS`, `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE` — these are READ by Phase 2 for the first time. None of them are key-rotation-sensitive (text values, not crypto keys). | None |
| Build artifacts / installed packages | None — Phase 2 adds source files only; no new tool installs. | None |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | LangFlow does not validate that `/api/tags` response has *only* the fields it expects — i.e., a `commit` field appended to `/api/version` won't break it. | Ollama Wire Shapes / `/api/version` | If LangFlow does strict validation, `/api/version` may need to drop the `commit` field. Mitigation: remove the field; loss is small. |
| A2 | LangFlow does not validate the `digest` field format in `/api/tags` responses. Empty-string digest is acceptable. | Ollama Wire Shapes / `/api/tags` | If LangFlow expects a SHA-shaped digest, model listings could be ignored. Mitigation: emit `"sha256:placeholder"` instead of empty string (matches some Ollama distributions). |
| A3 | Loop24 deployment runs on developer laptops without a reverse proxy in front of the gateway, so `r.RemoteAddr` is the trustworthy client IP source and `X-Forwarded-For` should be ignored (or only honored when an explicit env var is set). | Pattern 4 / IPAllowlist | If a deployment adds a proxy, X-Forwarded-For trust must be re-enabled and the proxy must be in the trust path. Mitigation: document the assumption inline; add `AUTH_TRUST_XFF` env var as a future hook. |
| A4 | Phase 2's `chunksToOllamaMessage` equivalent (in adapter wire encoder) only handles `ChunkKindText` aggregation; thought chunks are dropped unless `think:true` was set on the request. | Engine / Adapter behavior | If `think:true` requests come in via LangFlow, we may need to surface the thinking content. Phase 2 may implement this OR defer to Phase 6 — planner's call based on whether LangFlow flows use `think`. |
| A5 | The Ollama API public spec is forward-compatible — i.e., a server emitting additional fields not in the doc is acceptable. (Justified by the Node reference doing exactly this.) | Ollama Wire Shapes | If any client does strict-extra-fields rejection, those extra fields must be removed. Low probability for the LangFlow + Pi SDK target clients. |
| A6 | `engine.ACPClient` interface should expose `NewSession + SetModel + Prompt + Cancel` rather than a `WithSlot(fn)` callback. CONTEXT.md states this verbatim, but the callback shape would be more leak-safe — the planner may choose to wrap the 4-method interface inside a `WithSlot` engine-internal helper. | Engine Interface Shape | None — both shapes work; this is a stylistic choice the planner finalizes. |
| A7 | Property tests for `pickCwd` use `testing/quick` rather than `pgregory.net/rapid`. | Property Test for pickCwd | If `testing/quick`'s generator coverage is too weak (it doesn't generate Windows-style paths automatically), the test suite should add fixed table-driven cases for Windows paths. Mitigation: documented in Pitfall 3. |

**These assumptions should be confirmed by the user during plan-phase review** — especially
A4 (think:true handling) which directly affects Phase 2's adapter scope.

## Open Questions (RESOLVED)

1. **Engine ACPClient interface shape: 4-method interface vs WithSlot callback**
   - What we know: D-03 says "NewSession + SetModel + Prompt + Cancel" interface.
   - What's unclear: A literal 4-method interface forces the engine to manage slot acquire/release manually, which is leak-prone (forgot defer). A `WithSlot(ctx, fn(slot) error) error` callback shape always releases.
   - Recommendation: RESOLVED: implement the 4-method interface as the public contract (D-03 verbatim), but internally on the pool side use `WithSlot` semantics so acquire/release is paired correctly. Or: planner decides on `WithSlot` and updates D-03's wording — the interface name in D-03 is the docs source, not load-bearing on its exact methods.

2. **think:true handling in `/api/chat` — Phase 2 or Phase 6 problem?**
   - What we know: Ollama `think:true` requests should surface `message.thinking` content; canonical has `ChunkKindThought`.
   - What's unclear: does LangFlow ever send `think:true`?
   - Recommendation: RESOLVED: implement minimum support — if `think:true`, include aggregated thought chunks in `message.thinking`. Cost is ~5 LOC. Skipping it risks LangFlow breaking when a flow enables thinking.

3. **Single-package per concern: where does `auth` live?**
   - What we know: CONTEXT.md mentions `auth.Bearer(token)` and `auth.IPAllowlist(cidrs)`.
   - What's unclear: package boundary. `internal/auth`? `internal/server/auth`? `internal/middleware/auth`?
   - Recommendation: RESOLVED: `internal/auth` as a peer package. Keeps middleware separable, and Phase 8 will need to import auth logic from the engine hook layer too.

4. **Stub endpoint stream:true / stream:false default**
   - What we know: Node's `/api/pull` defaults to `stream:true` when client omits the field.
   - What's unclear: do LangFlow's actual pull invocations always set `stream:false`?
   - Recommendation: RESOLVED: mirror Node exactly (default `stream:true`). If the SURF-05 verification fails, switch to `stream:false` default. Cost of being wrong: one config flip.

5. **Model catalog refresh policy**
   - What we know: D-13 says catalog captured once at warmup, requires restart to refresh.
   - What's unclear: kiro-cli versions vary in their model availability over time; restart-only is a real operator papercut.
   - Recommendation: RESOLVED: keep restart-only for Phase 2 (D-13 is explicit). Document as a known limitation in `/health` output (perhaps `models_captured_at` timestamp). Phase 5 or later may add a `/health/agents`-driven refresh.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | Build + test | ✓ | 1.23 (project pin) | — |
| `kiro-cli` | Pool warmup + integration tests | Conditional | 2.4.1 (project ground truth) | Phase 2 unit tests use a fake `engine.ACPClient`; integration tests skip when `kiro-cli` not on PATH (existing `resolveKiroCLI` pattern from Phase 1) |
| `chi/v5` Go module | Routing | ✓ | v5.3.0 (in go.mod) | — |
| `goleak` Go module | Test goroutine leak detection | ✓ | v1.3.0 (in go.mod) | — |
| `golangci-lint` | Trust gates | ✓ | strict config from Phase 1 | — |
| `govulncheck` | Trust gates | ✓ | from Phase 1 CI | — |
| `go-arch-lint` | TRST-04 boundary enforcement | ✓ | scaffolded in Phase 1 Plan 01-03 | — |
| Network access (Go module proxy) | Build (initial) | ✓ | n/a | offline dev needs Go modules cached |
| LangFlow (running locally) | SURF-05 verification | ✓ (per Specifics in CONTEXT.md) | n/a | manual curl verification of /api/chat shape against Node implementation; deferred LangFlow verification to user smoke-test |
| Node reference server | Wire-shape verification | ✓ (at `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/`) | n/a | docs/reference/acp_server_node_reference.md narrative if file moves |

**Missing dependencies with no fallback:** None
**Missing dependencies with fallback:** kiro-cli (integration tests skip gracefully)

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `net/http/httptest` + `testing/quick` (property tests) + `go.uber.org/goleak` (leak detection) |
| Config file | None — Go test discovers `*_test.go` automatically |
| Quick run command | `go test -race -count=1 ./...` (project root) |
| Full suite command | `make ci` (runs `gofumpt -d` → `go vet` → `go build` → `golangci-lint` → `govulncheck` → `go test -race ./...` → property tests — already wired in Phase 1 Plan 01-03) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| SURF-01 | HTTP server binds :11434, routes /api/chat through engine to kiro-cli | integration | `go test -race -count=1 ./internal/adapter/ollama/... -run TestIntegration` | ❌ Wave 1 — `internal/adapter/ollama/integration_test.go` |
| SURF-03 | /api/chat, /api/generate, /api/tags, /api/show, /api/ps, /api/version return Ollama-compatible shapes | unit (handler) | `go test -race -count=1 ./internal/adapter/ollama/... -run TestHandle` | ❌ Wave 1 — `internal/adapter/ollama/handlers_test.go` |
| SURF-05 | LangFlow flow on /api/chat works with zero reconfig | manual smoke | User runs LangFlow against `make build && ./bin/loop24-gateway` | ❌ checkpoint:human-verify task in plan |
| SURF-07 | Stubs return Ollama-shape success for /api/pull/push/create/copy + DELETE /api/delete | unit (handler) | `go test -race -count=1 ./internal/adapter/ollama/... -run TestStub` | ❌ Wave 1 — `internal/adapter/ollama/stub_test.go` |
| ACP-07 | pickCwd derives cwd from X-Working-Dir / resource_link parent / KIRO_CWD / os.Getwd() | unit + property | `go test -race -count=1 ./internal/engine/... -run TestPickCwd` | ❌ Wave 0 — `internal/engine/pickcwd_test.go` |
| AUTH-01 | Bearer-token validation against AUTH_TOKEN env var | unit (middleware) | `go test -race -count=1 ./internal/auth/... -run TestBearer` | ❌ Wave 0 — `internal/auth/bearer_test.go` |
| AUTH-02 | IP allowlist validation against ALLOWED_IPS env var | unit (middleware) | `go test -race -count=1 ./internal/auth/... -run TestIPAllowlist` | ❌ Wave 0 — `internal/auth/ipallowlist_test.go` |
| AUTH-03 | Auth + IP allowlist exempt `/`, `/api/version`, `/health` | unit (server-level) | `go test -race -count=1 ./internal/server/... -run TestExemptRoutes` | ❌ Wave 1 — `internal/server/server_test.go` (extend existing file) |
| OBSV-01 | /health returns pool stats with size/alive/busy | unit (handler) | `go test -race -count=1 ./internal/server/... -run TestHealth` | ⚠️ exists from Phase 1; needs extension when pool wires in |
| POOL-01 | Pool with size=1 spawns one warm kiro-cli (env override honored) | unit (pool) | `go test -race -count=1 ./internal/pool/... -run TestNew` | ❌ Wave 0 — `internal/pool/pool_test.go` |
| POOL-02 | Pool warmup completes before HTTP listener starts | unit (main flow) | manual: assert main.go ordering is `Warmup → server.Run` (or: a `main_test.go` that calls the constructor sequence) | ❌ Wave 2 — `cmd/loop24-gateway/main_test.go` |
| POOL-03 | Acquire = channel receive, Release = channel send | unit (pool) | `go test -race -count=1 ./internal/pool/... -run TestAcquireRelease` | ❌ Wave 0 — `internal/pool/pool_test.go` |

Additionally:
- **Real-kiro integration test** for the full /api/chat → engine → pool → kiro-cli roundtrip lives at `internal/adapter/ollama/integration_test.go` and is gated by `resolveKiroCLI(t)` (existing pattern from Phase 1).
- **Property tests** for `pickCwd` (TRST-06 incremental) use `testing/quick` per TRST-06.
- **Goroutine leak gate** via `goleak.VerifyTestMain` in every test package that owns goroutines (pool, engine if it spawns any).
- **`Example_` functions** (TRST-07) for `pickCwd` and `buildBlocks` — runnable godoc.

### Sampling Rate

- **Per task commit:** `go test -race -count=1 ./internal/<changed-package>/...` (quick: <10s typical)
- **Per wave merge:** `go test -race -count=1 ./...` (full repo: <30s typical)
- **Phase gate:** `make ci` green before `/gsd:verify-work` (full trust-gate suite: lint + vuln + test-race + examples)

### Wave 0 Gaps

- [ ] `internal/canonical/chat.go` (or extension to `chunk.go`) — ChatRequest, ChatResponse, Message, ContentPart, etc. (D-08, D-09, D-10)
- [ ] `internal/engine/engine.go` — Engine struct, Config, ACPClient interface, Stream interface
- [ ] `internal/engine/pickcwd.go` + `pickcwd_test.go` — including property tests (TRST-06) and Example function (TRST-07)
- [ ] `internal/engine/build_blocks.go` + test + Example function
- [ ] `internal/engine/collect.go` — Collect helper
- [ ] `internal/engine/hooks.go` — PreHook/PostHook interfaces (D-04 seam, empty in Phase 2)
- [ ] `internal/pool/pool.go` + `config.go` + `stats.go` + `pool_test.go` + `testmain_test.go` (goleak)
- [ ] `internal/auth/auth.go` + `bearer.go` + `ipallowlist.go` + `auth_test.go`
- [ ] `internal/adapter/ollama/adapter.go` + `wire.go` + `handlers.go` + `stub.go` + `*_test.go`
- [ ] `internal/adapter/ollama/integration_test.go` — real-kiro round-trip gated by `resolveKiroCLI`
- [ ] `internal/config/config.go` extensions — `AuthToken []string`, `AllowedIPs []netip.Prefix`, `PoolSize int`, `OllamaPathPrefix string`, `OpenAIPathPrefix string`
- [ ] `internal/server/server.go` modifications — remove version handler, add auth + IP allowlist sub-router wiring, pass `pool.Pool` into health
- [ ] `cmd/loop24-gateway/main.go` modifications — wire pool, engine, ollama adapter

*(No framework install needed — all dependencies already in go.mod.)*

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | Bearer token validation via `crypto/subtle.ConstantTimeCompare` (Pattern 3); AUTH_TOKEN env var (multiple tokens comma-separated); empty = no auth (matches Node default) |
| V3 Session Management | no (Phase 5 introduces X-Session-Id) | N/A in Phase 2 |
| V4 Access Control | yes (IP allowlist) | `netip.Prefix.Contains` against parsed X-Forwarded-For / RemoteAddr (Pattern 4) |
| V5 Input Validation | yes | `json.Decoder` strict-ish (decoder errors → 400); `http.MaxBytesReader` (8 MB cap matching Node); explicit field validation for `messages` non-empty / `prompt` non-empty |
| V6 Cryptography | partial | `crypto/subtle.ConstantTimeCompare` for token comparison. No crypto key material handled in Phase 2 (tokens are operator-provided plain strings). |
| V7 Error Handling | yes | All errors render via `writeOllamaError(w, status, msg)` — no stack traces leaked; chi's `Recoverer` middleware (already wired in Phase 1) catches panics and returns 500. |
| V8 Data Protection | no | No persistent data store in Phase 2. |
| V9 Communication | partial | HTTP only (no TLS) per laptop-deployment model. Future deployment-mode change would require TLS termination at a reverse proxy. Documented in PROJECT.md. |
| V10 Malicious Code | partial | `gosec` G204 already enforced for the subprocess spawn path (Phase 1's `internal/acp/client.go:162` has the `//nolint:gosec` justification — Phase 2's pool inherits this). No new subprocess paths added in Phase 2. |
| V11 Business Logic | n/a | N/A for an API gateway proxy. |
| V12 Files / Resources | yes | `pickCwd` accepts paths from request headers (`X-Working-Dir`) and resource_link URIs. These flow into `os/exec.Cmd.Dir` (via pool→acp.New) — making this an indirect tainted-data sink. **Mitigation:** `cmd.Dir` validation should ensure the path exists and is a directory (`os.Stat`); reject paths with shell metacharacters. Path-traversal isn't really a thing for `cmd.Dir` (it's an absolute path interpreted by the OS), but ensuring it's a real directory prevents kiro-cli startup confusion. Planner decides whether to add this check in Phase 2 or defer. |
| V13 API & Web Service | yes | Bearer auth (V2); rate limiting NOT in Phase 2 (Phase 8 PLUG-V2-03 deferred). |
| V14 Configuration | yes | All security-sensitive config via env vars; no hardcoded tokens; gosec catches accidental credential strings. |

### Known Threat Patterns for {stack}

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Bearer-token timing attack (token byte-by-byte recovery via response timing) | Information Disclosure | `crypto/subtle.ConstantTimeCompare` for token comparison (Pattern 3) |
| X-Forwarded-For spoofing (attacker bypasses IP allowlist by setting their own XFF header when no proxy is in front) | Spoofing | Document the laptop-deployment assumption; add `AUTH_TRUST_XFF` env var as future hook; in Phase 2 prefer `RemoteAddr` over XFF unless XFF is explicitly trusted (Assumption A3) |
| Slowloris (slow-write attack exhausting server connections) | Denial of Service | `http.Server.ReadHeaderTimeout: 10 * time.Second` — already set in Phase 1 `internal/server/server.go:74` |
| Request body size DoS (huge JSON body to exhaust memory) | Denial of Service | `http.MaxBytesReader(w, r.Body, 8<<20)` in every body-reading handler (Node parity 8 MB) |
| JSON parsing DoS (deeply nested or huge string allocations) | Denial of Service | stdlib `json.Decoder` does not have deep-recursion mitigations enabled by default; for Phase 2's bounded clients (LangFlow, Pi SDK), this is accepted risk. Phase 8's schema-validation hook can add stricter checks. |
| Subprocess command injection (operator sets `KIRO_CMD=rm` via env var) | Tampering / Elevation of Privilege | Acceptance: env-driven config is operator-controlled. `gosec` G204 already flagged + suppressed in Phase 1 with explicit justification. Anyone with env-var write access to the deployment has equivalent access to spawn arbitrary processes via systemd/launchd/PowerShell. |
| Path traversal via X-Working-Dir → cmd.Dir | Tampering | The header value goes directly to `exec.Cmd.Dir` which is just a working-directory hint, not a file path that gets opened. Worst case: kiro-cli starts in a directory it shouldn't read. Operator-deployment trust boundary; no further mitigation needed. Phase 2 may add a "directory must exist" check if convenient. |
| Auth bypass via empty AUTH_TOKEN (operator forgets to set it) | Information Disclosure | This IS the Node default — "no auth when AUTH_TOKEN unset." Documented behavior, not a bug. Operators who want auth must set AUTH_TOKEN; the binary logs the current mode at startup (Node line 1050-1052 prints `Auth: OPEN (no AUTH_TOKEN set)`). Phase 2 should preserve the startup logging. |
| Race on pool slot acquire (two goroutines acquire the same slot) | Information Disclosure (cross-request state bleed) | Channel-of-slots design (POOL-03) is race-free by construction; `chan *Slot` recv is atomic. `go test -race` validates this. |

## Sources

### Primary (HIGH confidence)

- **Node reference implementation:** `/Users/coreyellis/Projects/repos/gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js` (lines 80-112 helpers, 217-303 ACP session dispatch, 484-541 buildAcpBlocksFromOllama, 559-595 chunksToOllamaMessage, 597-611 makeStats, 633-651 coerceToolCall, 661-708 Express middleware setup, 733-789 model endpoints, 793-888 handleOllamaCompletion, 891-935 chat/generate handlers, 1014-1029 stub endpoints, 1037-1071 startup) — empirically validated against kiro-cli 2.4.1
- **Phase 1 codebase:** `/Users/coreyellis/Projects/repos/local/loop24-gateway/internal/{acp,canonical,config,server,version}/...` — established patterns
- **CONTEXT.md (Phase 2):** `/Users/coreyellis/Projects/repos/local/loop24-gateway/.planning/phases/02-ollama-end-to-end/02-CONTEXT.md` — locked decisions D-01..D-16
- **ACP wire shapes reference:** `/Users/coreyellis/Projects/repos/local/loop24-gateway/docs/reference/acp_wire_shapes.md` — kiro-cli-specific JSON-RPC wire shapes
- **Go port brief:** `/Users/coreyellis/Projects/repos/local/loop24-gateway/docs/briefs/go_port_brief.md` §3.8, §3.12, §3.13, §5 — architecture, trust gates, adapter-over-canonical, milestones
- **Ollama API doc (current):** `https://github.com/ollama/ollama/blob/main/docs/api.md` — public API wire shapes via WebFetch

### Secondary (MEDIUM confidence)

- **chi router documentation:** `https://github.com/go-chi/chi` — sub-router + middleware composition patterns via WebFetch
- **Bifrost reference architecture:** `/Users/coreyellis/Projects/repos/local/bifrost/core/providers/ollama/ollama.go` — confirms the Ollama-shape provider pattern at production scale (uses fasthttp; we use net/http per project constraint)
- **Phase 1.1 plans:** `/Users/coreyellis/Projects/repos/local/loop24-gateway/.planning/phases/01.1-acp-wire-alignment/01.1-PATTERNS.md` — patterns established for canonical/acp packages that Phase 2 extends

### Tertiary (LOW confidence)

- LangFlow's exact validation behavior on `/api/tags` digest format (Assumption A2) and `/api/version` extra fields (Assumption A1) — derived from "Node implementation works in production" rather than reading LangFlow source. Mitigation: SURF-05 manual smoke test before phase signoff.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — stdlib + chi + goleak are all already in `go.mod`; no new deps needed
- Architecture: HIGH — adapter-over-canonical pattern locked by PROJECT.md + brief §3.13 + Phase 1.1 patterns; chi sub-router pattern verified via official docs
- Pitfalls: HIGH — pitfalls 1-3 verified against Phase 1 codebase + Node reference; pitfalls 4-7 derived from documented Go gotchas
- Ollama wire shapes: HIGH — cross-verified between Ollama API doc and Node reference implementation
- Pool design: HIGH — POOL-03 channel-of-slots is mandated by REQUIREMENTS.md and the Node reference is the working blueprint
- Test strategy: HIGH — Phase 1's whitebox + blackbox + integration + goleak pattern generalizes directly

**Research date:** 2026-05-23
**Valid until:** 2026-06-22 (30 days — Ollama API is stable; chi v5 is stable; project constraints are stable. If kiro-cli changes major version, re-validate `acp_wire_shapes.md` first.)
