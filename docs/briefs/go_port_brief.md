# Brainstorm Brief: Go Port of `acp_server`

> **How to use:** Paste this entire file as the opening message of a
> `/superpowers:brainstorming` session. It mirrors `rust_port_brief.md`
> so the two options can be compared side-by-side. Pick one and commit;
> don't do both.

---

## 0. What I want out of this brainstorm

I want to build a **Go port** of an existing Node.js HTTP server.
The Node server (`acp_server/acp-ollama-server.js`) is in production
and works, but I want a Go rewrite that:

- Is **functionally identical for existing Ollama clients** (LangFlow
  flows that already point at `/api/chat` and `/api/embed` keep
  working unchanged).
- **Also exposes an OpenAI-compatible API surface** (`/v1/chat/completions`,
  `/v1/embeddings`, etc.) on the same port, so a Pi SDK-based chat
  CLI (and any other OpenAI-shaped client) can point at the same
  server. Both surfaces share one engine, one pool, one process.
  See §1.5 / §1.6 / §3.13.
- Compiles to **a single native executable for Linux and Windows**
  from my macOS dev box, ideally with zero extra cross-compile tooling.
  This is the headline reason to consider Go.
- Handles concurrent load with lower memory and tail latency than the
  Node implementation, and without Node's single-event-loop bottleneck.
- Is idiomatic enough that a Go reviewer wouldn't wince.

**My background:** senior engineer in JS / Python / shell, *some Go*
already (not green-field-fluent but I can read it and have shipped
small Go tools). This is meaningfully less ramp-up than the Rust
option in `rust_port_brief.md`. By the end of the brainstorm I want
a written plan covering:

1. Go toolchain install for macOS dev and what's required (if anything)
   on the build hosts. Including version pin policy (Go 1.22+? 1.23+?).
2. Recommended editor / IDE setup (I use Claude Code in VS Code).
3. Project layout — `cmd/`, `internal/`, package boundaries.
4. Library selection with rationale: HTTP framework, structured
   logging, config, testing. Where there are real choices (stdlib
   `net/http` vs chi vs gin), I want the trade-offs spelled out, not
   just a pick.
5. A staged implementation plan with working milestones.
6. **The embeddings problem solved explicitly** — see §3.4. This is
   the one area where Go is genuinely weaker than Rust and I want the
   trade-off named, not papered over.
7. Cross-compilation / distribution strategy. The thing I most want
   from Go is the trivial cross-compile story; the brainstorm should
   tell me where that breaks (spoiler: cgo) and how to avoid it.
8. A testing strategy. The Node version has zero tests; the Go
   version should cover the load-bearing weirdness (tool-call
   coercion, NDJSON streaming, session lifecycle) and the protocol
   translation functions.

---

## 1. What the existing server does

It is a **drop-in replacement for the Ollama daemon** that proxies
LLM calls through `kiro-cli` ACP agents. Anything that speaks the
Ollama REST API (LangChain, LangFlow, Continue.dev, Open WebUI,
llama-index, …) can point at `http://localhost:11434` and transparently
route to Kiro without knowing Kiro exists.

```
┌────────────────┐  Ollama API   ┌─────────────────┐  ACP / JSON-RPC  ┌──────────────┐
│ LangFlow /     │ ───────────▶ │  acp-ollama-    │ ───── stdio ───▶ │  kiro-cli    │
│ LangChain /    │   :11434      │  server         │                  │  (ACP agent) │
│ Continue.dev   │ ◀─── NDJSON ─ │                 │ ◀─── stdio ───── │              │
└────────────────┘               └─────────────────┘                  └──────────────┘
```

Embeddings are the one exception: served locally by `fastembed`
(BGE / E5 models), never touch Kiro.

### Stack today

- Node.js 20+, ESM, no build step.
- Express 5, CORS, dotenv.
- `fastembed` (Node binding for ONNX-based embeddings).
- ~1070 lines per platform file. There are **two copies** —
  `acp-ollama-server.js` (WSL/Linux) and `acp-ollama-server-win.js`
  (Windows native). Most porting fixes get applied to both. Go should
  collapse this into one binary that runs on both OSes.

### Endpoints (the full surface)

| Method | Path | Notes |
|---|---|---|
| `POST` | `/api/chat` | Multi-turn chat; **streams NDJSON by default** |
| `POST` | `/api/generate` | Single-turn raw generation; same streaming default |
| `POST` | `/api/embed` | Local embeddings, new API (string or array input) |
| `POST` | `/api/embeddings` | Legacy embeddings (single prompt, single vector) |
| `GET` | `/api/tags` | List available models (Kiro + embed) |
| `POST` | `/api/show` | Model metadata |
| `GET` | `/api/ps` | "Running models" — returns a synthetic `auto` entry |
| `GET` | `/api/version` | Static version string |
| `POST` | `/api/pull`, `/api/push`, `/api/create`, `/api/copy` | Stubs (no-op) |
| `DELETE` | `/api/delete` | Stub |
| `GET` | `/health` | Pool + registry + embedding stats |
| `GET` | `/health/agents` | Per-slot + per-session detail |
| `DELETE` | `/v1/sessions/:id` | Tear down a stateful session |

### Request lifecycle (the part that matters)

1. Express receives Ollama-format JSON. CORS / bearer-auth /
   IP-allowlist middleware runs first (exempt: `/`, `/api/version`,
   `/health`).
2. **`buildAcpBlocksFromOllama(messages, tools, opts)`** flattens the
   Ollama `messages` array into a single ACP `text` block with
   bracketed section headers (`[System]`, `[User]`, `[Assistant]`,
   `[Tool result]`, `[Available tools]`, `[Output format]`,
   `[Reasoning]`). Inline `images: [b64]` arrays become separate ACP
   `image` blocks; MIME type sniffed from the base64 prefix.
3. Decide stateless vs stateful:
   - **Stateless** (no `X-Session-Id` header) → grab a slot from the
     `ACPPool` (default 4 warm `kiro-cli` slots), call `session/new`,
     prompt, release.
   - **Stateful** (`X-Session-Id` provided) → look up / create entry
     in `SessionRegistry`. Each entry owns a dedicated `kiro-cli`
     process. Idle entries reaped after `SESSION_TTL_MS` (30 min).
4. `session/set_model` if model != `'auto'`.
5. `session/prompt` is sent via JSON-RPC over the child's stdin.
   Notifications (`session/update` and `_kiro.dev/session/update`)
   stream back on stdout and are translated into typed chunks:
   `text`, `thought`, `tool_call`, `plan`.
6. **`chunksToOllamaMessage(chunks, wantThinking)`** reassembles those
   into Ollama-shape `{ role, content, thinking?, tool_calls? }`.
   Note: Ollama's `tool_calls[].function.arguments` is a **plain
   object** (unlike OpenAI's JSON-string).
7. **`coerceToolCall(message, tools)`** — load-bearing hack. If the
   model returned a plain JSON object (or fenced JSON) as text and
   tools were provided, convert it into a synthetic `tool_calls`
   entry. **This must be preserved in the Go port.** It exists
   because LangChain agents rely on JSON-as-tool-call. Removing it
   silently breaks real users.
8. Response: NDJSON streamed (default) or a single JSON body when
   `stream: false`.

### Internal classes (Node) — to be re-modeled in Go

- **`ACPSession`** — wraps one spawned `kiro-cli acp` process.
  JSON-RPC over stdio. Tracks pending requests by ID. Auto-grants
  `session/request_permission` with `allow_always`. Emits typed
  chunks. Has a 60s ping heartbeat that kills the process on failure.
  **Idiomatic Go shape**: a `Session` struct with a goroutine reading
  lines from stdout, a write-side goroutine pumping a `chan request`,
  and a `map[int]chan<- response` for correlation. Use
  `context.Context` for cancellation.
- **`ACPPool`** — fixed-size (default 4) pool of warm `Session`s.
  Acquire/release via a buffered channel of `*Session`. Lazy
  re-spawn of dead slots.
- **`SessionRegistry`** — `map[string]*registryEntry` guarded by
  `sync.RWMutex`. Background goroutine reaps idle entries every 60s.
- **`EmbeddingRegistry`** — local embedding models. See §3.4 for
  the Go-side trade-off.

### Config (env-driven)

`PORT`, `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE`,
`SESSION_TTL_MS`, `PING_INTERVAL`, `AUTH_TOKEN` (comma-list),
`ALLOWED_IPS` (comma-list), `DEBUG`, `EMBEDDING_MODEL_DEFAULT`,
`EMBEDDING_MODELS_ENABLED`, `EMBEDDING_CACHE_DIR`,
`EMBEDDING_BATCH_SIZE`, `EMBEDDING_MAX_INPUTS`.

Per-request headers: `X-Session-Id`, `X-Working-Dir`, `X-Request-Id`.

Variable names must stay identical so a deployment can swap binaries
without changing the unit file / docker env.

### Things that must survive the port

- **Ollama default `stream: true`** on `/api/chat` and `/api/generate`.
  Must respond `application/x-ndjson` with chunked transfer. In Go
  this means writing to `http.ResponseWriter` and calling
  `Flusher.Flush()` after each line.
- **`coerceToolCall`** — preserve behavior, including the markdown-fence
  stripping (` ```json …``` `) and the property-overlap "best tool"
  scoring.
- **`pickCwd(blocks, fallback)`** — derive cwd from longest common
  parent of `resource_link` block URIs; fall back to `KIRO_CWD`; allow
  `X-Working-Dir` header override.
- **`session/request_permission` auto-grant** with
  `optionId: 'allow_always', granted: true`. Without this, agent tool
  invocations block forever.
- **Pool warmup before accepting HTTP.** Cold boot is slow; first real
  request is fast.
- **Client disconnect cancels the in-flight ACP prompt.** In Go that's
  watching `r.Context().Done()` from the handler and calling
  `session/cancel` over the JSON-RPC channel.

### 1.5 Clients we need to support

The Go server has three concrete consumers in production:

1. **A chat CLI built on Pi SDK** (`@earendil-works/pi-ai`,
   `https://pi.dev`). Pi is a multi-provider LLM harness; pointing
   it at our server means using its OpenAI provider with a custom
   `base_url`. Pi will issue requests to `POST /v1/chat/completions`
   with `Authorization: Bearer ...` — standard OpenAI shape.
   *Open verification item:* confirm the exact env var / config key
   Pi uses to set the OpenAI base URL (likely `OPENAI_BASE_URL` or
   equivalent). Brainstorm should ask me to check this before M0.
2. **A LangFlow server** running locally for low-code flows. Flows
   already have model components configured to call
   `http://localhost:11434/api/chat` (Ollama shape). These flows
   must keep working with zero reconfiguration.
3. **loop24-client / GSD Pi** (`../loop24-client`, npm
   `@loop24/client` v1.0.1) — a TypeScript Node coding-agent CLI
   that calls `@anthropic-ai/sdk` (v0.90.x) and honors
   `ANTHROPIC_BASE_URL` for transport redirection (see
   `packages/pi-ai/src/providers/anthropic.ts:39`). It uses both
   non-streaming `messages.create()` and streaming `messages.stream()`
   heavily, sends `x-api-key` or `Authorization: Bearer` auth plus
   the required `anthropic-version` header, and treats `tool_use`
   blocks (with `input` as object, NOT string) and `thinking` blocks
   as first-class. Routing through the gateway means setting
   `ANTHROPIC_BASE_URL=http://localhost:11434` in the client env.
   *Added 2026-05-23 as the third client; the architecture must
   support this without forking the engine.*

Other clients are not yet committed but the architecture should
leave room (LangChain Python, Continue.dev, Open WebUI — all of
which support either Ollama-shape, OpenAI-shape, or Anthropic-shape
base URLs).

### 1.6 Triple API surface (OpenAI + Ollama + Anthropic) — non-negotiable

All three surfaces run on the same server, same port, same process.
This is **not** a "maybe" — it's a hard requirement driven by §1.5.

**Path layout (chi routes by endpoint, not just prefix — OpenAI and Anthropic share `/v1`):**

| Surface | Path prefix | Endpoints |
|---|---|---|
| **OpenAI** | `/v1/*` | `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings`, `GET /v1/models` |
| **Anthropic** | `/v1/*` | `POST /v1/messages`, `POST /v1/messages/count_tokens` (deferred) |
| **Ollama** | `/api/*` | `POST /api/chat`, `POST /api/generate`, `POST /api/embed`, `POST /api/embeddings`, `GET /api/tags`, `POST /api/show`, `GET /api/ps`, `GET /api/version` |
| **Shared** | `/health/*`, `/v1/sessions/:id` | health probes + stateful session teardown |

OpenAI and Anthropic intentionally co-exist under `/v1/*` because both
spec their canonical paths there and we want zero-config drop-in
behavior for SDK clients. chi disambiguates by endpoint; if a
deployment needs them on separate prefixes, set
`ANTHROPIC_PATH_PREFIX=/anthropic/v1`.

**The cost is bounded:** two extra adapter packages
(`internal/adapter/openai`, `internal/adapter/anthropic`). Everything
below the HTTP boundary (pool, session registry, JSON-RPC,
embeddings) is shared — see §3.13 for the layered shape.

**Where the surfaces actually differ:**

1. **Streaming format.** OpenAI uses SSE with line-prefix framing
   (`Content-Type: text/event-stream`, `data: <chunk>\n\n`,
   terminated by `data: [DONE]\n\n`). Ollama uses NDJSON
   (`application/x-ndjson`, one JSON object per line, final object
   has `done: true`). Anthropic uses SSE with named events
   (`event: <name>\ndata: <json>\n\n`) — the sequence is
   `message_start` → (`content_block_start` → `content_block_delta`+
   → `content_block_stop`)+ → `message_delta` → `message_stop`, with
   periodic `ping` keepalives. One canonical chunk source feeds
   three streamers.
2. **Tool-call argument shape.** OpenAI: `arguments` is a **JSON
   string** under `tool_calls[].function.arguments`. Ollama:
   `arguments` is a **plain object** under
   `message.tool_calls[].function.arguments`. Anthropic: `input` is
   a **plain object** under `content[].input` for `tool_use` blocks
   (same as Ollama, different field path). The `coerceToolCall`
   logic from the Node version applies to all three; each adapter
   emits the shape its client expects.
3. **Field name / placement drift.** System prompt: OpenAI/Ollama in
   `messages[]` with `role:"system"`; Anthropic at the top-level
   `system` field (string OR array of blocks). Image attachments:
   OpenAI `content` parts with `image_url`; Ollama top-level
   `images: [b64]`; Anthropic `content` blocks with
   `{type:"image",source:{type:"base64",media_type,data}}`.
   Output-format: OpenAI `response_format.json_schema`; Ollama
   `format`; Anthropic via tool-use coercion. Each adapter handles
   its own translation; the canonical internal type is the union of
   features.
4. **Model listing shape.** `/v1/models` returns
   `{ object: "list", data: [...] }`; `/api/tags` returns
   `{ models: [...] }`; Anthropic's `/v1/models` (if we serve it)
   returns `{ data: [{id, display_name, ...}], has_more, first_id,
   last_id }`. Same underlying list, three render functions —
   though canonical `/v1/models` will collide between OpenAI and
   Anthropic shapes; deferred for now and routed by `User-Agent`
   sniff or accept-header negotiation if we ever serve it on
   Anthropic.
5. **Auth conventions.** OpenAI/Ollama accept `Authorization: Bearer
   <token>`; Anthropic accepts both `x-api-key: <key>` AND
   `Authorization: Bearer <key>` (loop24-client uses both depending
   on provider — see `loop24-client/packages/pi-ai/src/providers/anthropic.ts:65-72`).
   The existing `AUTH_TOKEN` env var applies to all three;
   `anthropic-version` is a required header on the Anthropic surface.

**Configuration:**

```bash
ENABLED_SURFACES=openai,ollama,anthropic   # default: all three; can disable any
OPENAI_PATH_PREFIX=/v1                     # default
OLLAMA_PATH_PREFIX=/api                    # default
ANTHROPIC_PATH_PREFIX=/v1                  # default — shares with OpenAI
ANTHROPIC_VERSION_DEFAULT=2023-06-01       # used when client omits the header (also accept request override)
```

Disable any surface in deployments where only some are needed (e.g.
internal Ollama-only environment) without changing code.

---

## 2. Why Go, specifically

Real reasons, not Go-cheerleading. Push back if any is weak.

- **Cross-compilation is trivial.** `GOOS=linux GOARCH=amd64 go build`
  and `GOOS=windows GOARCH=amd64 go build` produce native binaries
  from my macOS box with **zero extra tooling**. No `cross`, no
  `cargo-zigbuild`, no MinGW, no docker. This is the single biggest
  reason to pick Go for this project. *Caveat:* the moment cgo enters
  the picture (e.g. ONNX Runtime bindings for embeddings) this story
  collapses to roughly Rust's level. See §3.4.
- **Subprocess management + concurrent pools are idiomatic Go.**
  `os/exec` + goroutines + channels is exactly the shape this server
  needs. The Node version is fighting the event loop for the same
  thing; the Rust version pays a learning tax to express it. Go is
  the language where this code writes itself.
- **HTTP streaming is stdlib.** `http.ResponseWriter` +
  `http.Flusher` + `bufio.Scanner` covers everything we need without
  picking a framework. NDJSON streaming is ~5 lines per handler.
- **Build times in seconds.** Tight feedback loop on a project where
  I'm doing a from-scratch port.
- **Smaller binaries.** Typical Go server binary 10–15 MB. Rust + ONNX
  could be 50 MB+. Matters for distribution.
- **I already have some Go.** Faster productive ramp than Rust.
- **Mature production server tooling.** `golangci-lint`,
  `govulncheck`, `httptest`, `slog` are all stdlib-or-stdlib-adjacent
  and well-understood. The trust-gate story (§3.12) is less
  ground-breaking than Rust's but very serviceable.
- **Industry-standard pattern for LLM gateways is Go.** Bifrost
  (`maximhq/bifrost`) — the closest production reference for what
  we're building — is written in Go and exposes exactly the
  multi-API-surface pattern we need (`/openai/v1/chat/completions`,
  `/anthropic/v1/messages`, etc. routing to a single canonical
  engine). We're building a tiny subset of Bifrost, but the shape
  it demonstrates is exactly the shape we want. See §3.15.

**What Go gives up vs Rust:**

- **No borrow checker.** GC handles lifetimes; some classes of
  resource bug (forgetting to close a goroutine, leaking a `chan`)
  go uncaught by the compiler. Mitigations: goroutine-leak
  detectors in tests, `errcheck`, strict review.
- **Weaker embeddings ecosystem.** See §3.4. The honest path requires
  giving something up: either cgo (losing easy cross-compile), or
  out-of-process embeddings (adding deployment complexity), or
  weaker model selection.
- **Verbose error handling.** `if err != nil { return err }` will
  appear thousands of times. Live with it.
- **GC pauses exist.** For this workload — short request cycles,
  bounded concurrency, no allocation-heavy hot paths — pauses are
  unlikely to matter. Worth measuring at the end.

---

## 3. Decisions I want the brainstorm to surface trade-offs on

I have priors. Push back where they're wrong.

### 3.1 HTTP framework
- Prior: **stdlib `net/http` + `chi` for routing.** chi is a thin
  router on top of stdlib handlers, idiomatic, no surprises, ~zero
  runtime overhead. Works natively with `httptest`, `slog`,
  `r.Context()`, and every other piece of stdlib middleware.
- Alternatives: `gin` (more features, popular), `echo` (similar to
  gin), `httprouter` (just the router, no middleware story).
- **`fasthttp` deserves an honest callout.** Bifrost — our reference
  architecture (§3.15) — uses `fasthttp` + `fasthttp/router` for the
  3–5x throughput uplift over stdlib `net/http`. It's a real
  trade-off, not a casual choice:
  - **Pro:** measurably faster, lower per-request allocation, used
    at scale by Bifrost / VictoriaMetrics / others.
  - **Con:** **incompatible with `http.Handler`.** Everything
    downstream (middleware, testing, `r.Context()`, `httptest`)
    changes. You can't reuse stdlib HTTP libraries. `r.Context()`
    becomes `ctx.UserValue()`. The Go ecosystem assumes `net/http`;
    fasthttp is a separate universe.
  - **Con:** harder to hire/onboard against — most Go devs have
    never written fasthttp code.
  - **For our scale** (handful of concurrent clients, bottleneck is
    `kiro-cli` subprocess latency not HTTP parsing), stdlib `net/http`
    is fine. The throughput advantage of fasthttp doesn't matter when
    your downstream calls take 100ms+.
  - **Recommendation:** stdlib + chi. Revisit only if profiling shows
    HTTP overhead is the bottleneck (it won't be).
- Real question for the brainstorm: which router gives the
  **cleanest middleware story for auth + IP allowlist + request-id**
  on stdlib `net/http`, and the **least friction for chunked
  streaming (both NDJSON and SSE) with client-disconnect detection**?
  Chi covers both natively. Confirm or rebut.

### 3.2 JSON-RPC over child stdio
- No mature off-the-shelf Go library for "spawn subprocess, talk
  JSON-RPC 2.0 over stdio, handle bidirectional notifications,
  correlate by id." Roll it.
- Stdlib gives everything needed: `os/exec`, `bufio.Scanner` on
  stdout, `encoding/json` for frames, `sync.Map` or `map +
  sync.RWMutex` for pending requests, channels for the write side.
- Brainstorm: validate the shape — one reader goroutine, one writer
  goroutine, request handles are `chan<- json.RawMessage`, all owned
  by the Session struct. Context propagation for cancellation.
- Sub-question: handle JSON-RPC parse errors how? Log + drop the
  frame and continue, or treat as fatal and kill the session?
  Node version logs and continues; I want that confirmed for Go.

### 3.3 Subprocess lifecycle
- `os/exec.CommandContext` with a cancelable `context.Context` is
  the obvious shape.
- Windows specifics: `kiro-cli` is `kiro-cli.cmd` on Windows. The
  Node version uses `shell: true` to handle this; Go's
  `exec.LookPath` plus letting Windows resolve `.CMD`/`.BAT`
  automatically should work natively. The brainstorm should verify
  this on a Windows test box (or at minimum on a Windows VM /
  GitHub Actions runner) — don't assume.
- EPIPE handling on child death: confirm Go behavior on writes to
  a closed pipe and add tests.
- Heartbeat: ticker on a `time.Ticker`, send `ping` via the
  JSON-RPC layer, kill on failure. Standard.

### 3.4 Embeddings — the central trade-off

This is where Go is genuinely weaker than Rust. Options ordered by
cross-compile friendliness:

**Option A — in-process, cgo to ONNX Runtime.**
Crate: `github.com/yalue/onnxruntime_go`. Same models as the Node
binding. Fast, low latency.
- *Cost:* cgo is required → `CGO_ENABLED=1`. The trivial
  cross-compile story breaks. You now need MinGW-w64 for Windows
  cross-builds from macOS, or a Windows build host, or CI matrix
  builds. Static linking gets harder.

**Option B — in-process, pure-Go BERT.**
Crates: `github.com/sugarme/transformer`, `github.com/nlpodyssey/cybertron`,
similar. No cgo, easy cross-compile preserved.
- *Cost:* fewer models, slower (no SIMD-optimized ONNX runtime),
  some are partially abandoned. Real risk of "works on my BGE-Small
  but breaks on my next model."

**Option C — out-of-process embeddings sidecar.**
Run a small Python or Node service (or the existing Node server's
embedding endpoint, even) on a private port; Go proxy forwards
`/api/embed` and `/api/embeddings` to it.
- *Cost:* deployment complexity (two binaries instead of one,
  startup ordering, health-check coordination). But the main Go
  binary stays pure-Go and trivially cross-compilable. **This is
  the most idiomatic Go answer for a workload that has one
  non-Go-friendly component.**

**Option D — embeddings via WASM.**
ONNX-via-wasm runtimes exist. Probably too immature for production
right now. Flag and dismiss unless brainstorm has reason to think
otherwise.

I lean **Option C** because the cross-compile story is the *reason*
to be in Go in the first place. Compromising on it for embeddings
gives up Go's headline advantage. But I want the brainstorm to
score Option A honestly — if the cgo cost is manageable with
`goreleaser` or GitHub Actions matrix builds, A might be cleaner
than running two binaries.

### 3.5 Structured logging
- Prior: **`log/slog`** (stdlib since Go 1.21). Structured, async-
  context-aware, no extra deps.
- Alternatives: `zerolog` (allocation-free, slightly faster, JSON-
  first), `zap` (Uber, fastest, more ceremony).
- For this server's volume, slog is fine. Confirm or rebut.

### 3.6 Configuration loading
- Prior: env vars only, parsed into a `Config` struct via a tiny
  in-house helper, plus `godotenv` for `.env` support to match the
  Node version.
- Alternatives: `kelseyhightower/envconfig` (struct-tag-driven),
  `caarlos0/env`, `koanf` (multiple sources), `viper` (over-
  featured, common but heavy).
- envconfig is the lightest "do the right thing" answer. Don't pull
  viper unless we need multi-source config.

### 3.7 Concurrency primitives for pool / registry
- Pool: **buffered channel of `*Session`**. `Acquire` is a recv,
  `Release` is a send. Trivial backpressure. Dead-slot replacement
  via a sentinel value or by sending a re-initialized session back.
- Registry: `map[string]*entry` + `sync.RWMutex`, plus a `time.Ticker`
  goroutine for idle reaping.
- Both are straightforward Go idioms. Brainstorm should confirm and
  call out any subtle correctness traps (e.g. `Release` after a
  panic on the handler path — recover and release in a `defer`).

### 3.8 Project structure

Layout reflects the dual-surface requirement (§1.6) and the
adapter-over-canonical pattern (§3.13). Loosely modeled on Bifrost
(§3.15) but flattened heavily for our scale:

```
cmd/acp-server/main.go        # binary entrypoint, flag parsing, server.Start()
internal/acp/                 # ACPSession + JSON-RPC over stdio (kiro-cli)
internal/pool/                # ACPPool + SessionRegistry
internal/embed/               # embeddings (with sidecar shim if §3.4 Option C)
internal/canonical/           # canonical request/response types — engine-facing
internal/engine/              # consumes canonical, drives pool/registry/ACP
internal/adapter/ollama/      # Ollama API surface — translates ↔ canonical
internal/adapter/openai/      # OpenAI API surface — translates ↔ canonical
internal/adapter/anthropic/   # Anthropic Messages API surface — translates ↔ canonical, includes SSE event-stream renderer
internal/server/              # HTTP router, middleware, surface mounting
internal/plugin/              # PreHook/PostHook interface + chain (§3.14)
internal/config/              # env loading + Config struct
internal/version/             # build-time version info
go.mod / go.sum
```

Key invariants this layout enforces (and which §3.12 dylint-style
rules / §3.14 boundary linter should protect):

- `internal/adapter/*` may import `internal/canonical` and
  `internal/plugin`. It **must not** import `internal/engine`,
  `internal/acp`, or `internal/pool` directly.
- `internal/engine` may import `internal/canonical`, `internal/pool`,
  `internal/acp`, `internal/embed`, `internal/plugin`. It **must
  not** import any `internal/adapter/*` or `internal/server`.
- `internal/canonical` imports nothing from `internal/*`. It is the
  innermost layer.

`internal/` keeps everything unimportable from outside, which is
fine — this is an app, not a library. Confirm the shape.

### 3.9 Cross-compilation
- The simple story: `GOOS=linux GOARCH=amd64 go build` + `GOOS=windows
  GOARCH=amd64 go build`. Done. Reproducible from any dev box.
- Goes away the instant cgo is enabled (see §3.4). If embeddings
  Option A is chosen, the brainstorm needs to recommend a concrete
  cross-build setup: probably **GitHub Actions matrix** (linux + windows
  runners, native build on each) rather than wrestling MinGW locally.
- Tag both binaries with `runtime/debug.ReadBuildInfo()` so
  `/api/version` returns the actual commit SHA.

### 3.10 Testing strategy
- **Stdlib `testing` + `net/http/httptest`** for HTTP integration
  tests. No third-party framework needed.
- `testify/require` for assertions if the brainstorm wants nicer
  failure messages. (Not strictly necessary; flag as optional.)
- The hard one: testing the ACP/subprocess layer. Options:
  (a) Build a fake `kiro-cli` binary in the test setup that speaks
      JSON-RPC and assert against its inputs.
  (b) Define `Session` behind an interface (`type ACPClient interface
      { Prompt(...); SetModel(...); ... }`) so handlers can be tested
      with a mock that doesn't spawn anything.
  (c) Skip subprocess tests and only test E2E against real `kiro-cli`
      in CI.
  I lean **(b) for handlers + (a) for the ACP layer itself**. Discuss.
- **Goroutine leak detection** in tests (`go.uber.org/goleak`) —
  cheap insurance for a server that spawns goroutines per request.

### 3.11 Distribution & packaging
- `go build -ldflags="-s -w -X main.version=$(git rev-parse --short HEAD)"`
  for stripped builds with embedded version.
- `goreleaser` for tagged releases producing artifacts for both OSes
  in one command. Worth adopting on day one or wait? My read: wait
  until the second release, then add it.
- Static linking: pure-Go binaries are statically linked by default.
  cgo binaries are not — another reason Option C in §3.4 is
  attractive.
- Signing / notarization: out of scope for v1.

### 3.12 Trust gates for AI-assisted code (non-negotiable)

This codebase will be written with heavy AI assistance. The Rust
brief cites the "Making AI-Generated Rust Code Trustworthy" article
(L. Garcia) and bakes its recommendations in. Go has its own
equivalent toolchain; the *philosophy* (strict linting, deny-by-
default, layered CI gates, lint-on-incident) applies identically.
What changes is the specific tooling.

Required gates (treat as requirements, not decisions):

1. **`golangci-lint` with a strict config.** Enable at minimum:
   `govet`, `staticcheck`, `errcheck`, `gosec`, `revive`,
   `unparam`, `unused`, `goimports`, `gofumpt`, `ineffassign`,
   `bodyclose`, `noctx`, `nilerr`, `wrapcheck`, `errorlint`.
   Run on PR, block merges on findings. No `//nolint:...` without
   an inline justification.
2. **Forbid `panic()` outside `main`/`init` and tests.** Use the
   `revive` rule or a custom `staticcheck` config. Panics in
   handler code are the Go equivalent of `.unwrap()`.
3. **Required error wrapping.** `errorlint` enforces `errors.Is`/
   `errors.As` instead of `==`. `wrapcheck` enforces wrapping at
   package boundaries.
4. **`gosec` for security lints.** Catches `os.Exec` with tainted
   input, weak crypto, hardcoded credentials. Critical for a server
   that spawns subprocesses.
5. **`govulncheck` in CI.** Scans dependencies for CVEs against the
   Go vulnerability database. Run on every PR + nightly on `main`.
6. **Required godoc on exported symbols.** `revive`'s
   `exported` rule enforces this.
7. **Doctest equivalent: `Example_` functions.** Go's `Example`
   functions in `_test.go` files are runnable, output-validated, and
   show up in godoc. Required for non-obvious functions
   (`buildAcpBlocks`, `coerceToolCall`, `pickCwd`).
8. **Property tests for protocol translation.** Use `pgregory.net/rapid`
   (or `testing/quick` from stdlib for simple cases) to fuzz the
   Ollama↔ACP block builder and the chunk reassembler. Minimum
   coverage:
   - `buildAcpBlocks` round-trip: every valid messages array produces
     blocks that parse cleanly.
   - `coerceToolCall`: any plain-JSON-as-text + tools input either
     coerces or passes through; never panics, never returns nil.
9. **Goroutine leak detection.** Wrap key tests in
   `goleak.VerifyTestMain` (top-level) and `goleak.VerifyNone(t)`
   (per-test for handlers). Catches the most common bug class for
   this server shape.
10. **Architectural boundary enforcement.** Go doesn't have dylint,
    but `go-arch-lint` (or a custom `internal/` boundary review)
    can enforce that `internal/acp` doesn't import `internal/server`
    and vice versa. Start with one rule (no upward imports across
    layers), expand iteratively.
11. **Layered CI pipeline.** Stages run in order, each blocks the
    next: `gofumpt -d` → `go vet` → `go build` → `golangci-lint run`
    → `govulncheck` → `go test -race` → `go test -run Example` →
    property tests → cross-compile smoke. No skipping on green
    branches.
12. **`-race` enabled in CI test runs.** Always. The race detector
    is the closest Go gets to Rust's compile-time safety.
13. **Doc the rules themselves.** Each non-obvious lint config has
    a paragraph in `docs/lints.md` explaining what it protects and
    a link to the incident or design decision that motivated it.
    Same purpose as the dylint-doc requirement in the Rust brief.
14. **Lint-on-incident loop.** When a human review catches an
    AI-generated bug, ask "could a lint have caught this?" If yes,
    add the lint before the fix lands. This is procedure, not just
    setup.
15. **Start with the linter set above, expand iteratively.** Don't
    over-engineer custom static analysis on day one. The default
    `golangci-lint` strict config catches 80% of what dylint catches
    in Rust.

Want the brainstorm to address: which of these conflicts with
others (e.g. `wrapcheck` + `errorlint` sometimes argue), whether
`gofumpt` is worth the friction over `gofmt`, and what `revive`
rule set to actually enable (the full set has ~70 rules; most
projects pick 10–20).

Source for the underlying philosophy: <https://medium.com/@lagarciag/making-ai-generated-rust-code-trustworthy-3403966b69db>
(The article is Rust-specific; this section adapts its principles
to Go's tooling.)

### 3.13 Adapter layer (OpenAI + Ollama + Anthropic API surfaces)

The triple-surface requirement (§1.6) means the HTTP layer is split
in three and the engine layer is shared. This is the canonical LLM
gateway pattern; Bifrost (§3.15) does the same thing at much larger
scale.

**Shape:**

```
                                       ┌──────────────────────┐
   Pi SDK ──POST /v1/chat/completions ─▶│ adapter/openai      │─┐
                                       │ native ↔ canonical  │ │
                                       └──────────────────────┘ │
                                                                 │
                                       ┌──────────────────────┐ │
   loop24-client ──POST /v1/messages ──▶│ adapter/anthropic   │─┤
                                       │ native ↔ canonical  │ │
                                       │ + SSE event-stream  │ │
                                       └──────────────────────┘ │
                                                                 ▼
                                                       ┌──────────────────┐
                                                       │   plugin.Chain   │
                                                       │   (Pre hooks)    │
                                                       └──────────────────┘
                                                                 │
                                                                 ▼
                                                       ┌──────────────────┐
                                                       │   canonical      │     ┌──────────┐
                                                       │     engine       │────▶│ kiro-cli │
                                                       │ (pool, sessions, │     └──────────┘
                                                       │  embeddings)     │
                                                       └──────────────────┘
                                                                 ▲
                                                                 │
                                                       ┌──────────────────┐
                                                       │   plugin.Chain   │
                                                       │   (Post hooks)   │
                                                       └──────────────────┘
                                                                 ▲
                                       ┌──────────────────────┐ │
   LangFlow ──POST /api/chat ──────────▶│ adapter/ollama      │─┘
                                       │ native ↔ canonical  │
                                       └──────────────────────┘
```

**Canonical types live in `internal/canonical/`.** Roughly:

- `ChatRequest` — model, messages, tools, stream, format, think,
  images (as []byte after base64 decode), output-format spec,
  top-level `System` (hoisted from Anthropic; synthesized from
  `messages[role==system]` for OpenAI/Ollama), `MaxTokens`
  (required by Anthropic; optional for others).
- `ChatChunk` — discriminated union over `Text` / `Thought` /
  `ToolCall` / `Plan`. This already exists conceptually in the
  Node version's chunk types. `Thought` maps to Anthropic
  `thinking` blocks and OpenAI/Ollama reasoning fields.
- `ChatResponse` — assembled message + tool_calls (as canonical
  shape, not OpenAI-string-arguments / Ollama-object-arguments /
  Anthropic-tool_use-input). Includes `StopReason` (canonical;
  rendered per surface: OpenAI `finish_reason`, Anthropic
  `stop_reason`, Ollama `done_reason`).
- `EmbedRequest` / `EmbedResponse` — model, inputs, vectors.
- `Error` — typed error with `Code`, `Message`, `HTTPStatus`,
  optional `Cause`. Rendered per surface: OpenAI
  `{error:{message,type,code}}`, Anthropic
  `{type:"error",error:{type,message}}`, Ollama `{error:"..."}`.

**Adapter packages own:**

- Request decoding (wire JSON → canonical, validating shape).
- Response encoding (canonical → wire JSON, emitting in the
  surface's idiom — JSON-string args for OpenAI, object args for
  Ollama, object `input` under `tool_use` blocks for Anthropic).
- Streaming (canonical `ChatChunk` channel → SSE `data:` lines for
  OpenAI, NDJSON for Ollama, named-event SSE for Anthropic with the
  `message_start` / `content_block_*` / `message_delta` /
  `message_stop` sequence and `ping` keepalives).
- Surface-specific quirks: model-listing endpoint shape,
  `system` field placement (top-level for Anthropic, `messages[]`
  for OpenAI/Ollama), `images` field placement, response-format
  spec translation, `anthropic-version` header enforcement.

**Adapter packages do NOT own:**

- Pool/session lifecycle.
- JSON-RPC to `kiro-cli`.
- Embedding model loading.
- Authentication / IP allowlist (those live in `internal/server`
  middleware, applied uniformly across surfaces).

**Decisions for the brainstorm:**

1. **Canonical type granularity.** Do we have one `ChatRequest`
   shared by all three surfaces (union of all features), or three
   adapter-local request types that converge at an internal
   `engine.Run(...)` signature? Intuition: a single canonical type
   — feature drift between OpenAI / Ollama / Anthropic is bounded
   (system-prompt placement, max_tokens, tool-arg shape, thinking
   blocks). The union is small and a single canonical type means a
   single plugin chain. Discuss.
2. **Tool call argument shape.** Canonical holds args as
   `map[string]any` (idiomatic Go). OpenAI adapter `json.Marshal`s
   to a string at the wire; Ollama adapter emits the map directly;
   Anthropic adapter emits the map directly under `tool_use[].input`.
   The single map is the pivot point that lets one plugin (e.g.
   `SchemaValidationHook`) cover all three.
3. **Streaming abstraction.** Bifrost has a `StreamConfig` with
   per-route `ResponseConverter` and `ErrorConverter` callbacks
   on a centralized stream-pump (see
   `transports/bifrost-http/integrations/router.go` in the Bifrost
   repo). For us, simpler: one `engine.Run(ctx, req)` that returns
   `(chan canonical.ChatChunk, error)`, and each adapter has its
   own pump function with a known signature. Don't over-engineer
   a `StreamConfig` registry on day one.
4. **Surface enable/disable.** From §1.6, `ENABLED_SURFACES` env
   var controls which adapters get mounted by
   `internal/server`. Implementation: `server.Mount(router,
   adapters []Adapter)` where each adapter implements
   `Mount(router *chi.Mux)`.

### 3.14 Plugin / hook architecture for guardrails

This is the highest-leverage piece I want lifted from Bifrost.
Their governance plugin demonstrates a clean pattern for "things
that need to run before and after every LLM call" — auth, budget,
content moderation, rate limits, schema validation, audit logging.
We don't need most of those today, but we **want the seams** so
they can be added without rewriting handlers.

**The Bifrost-inspired interface:**

```go
// internal/plugin/plugin.go
package plugin

import (
    "context"
    "your/module/internal/canonical"
)

// PreHook runs before the engine call. Returning a non-nil
// (*canonical.ChatResponse, nil) short-circuits the engine and
// returns that response directly to the client. Returning an
// error aborts with that error. Returning (nil, nil) continues.
type PreHook interface {
    Name() string
    PreChat(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// PostHook runs after the engine call. Can mutate the response
// (e.g. PII redaction) or attach metadata.
type PostHook interface {
    Name() string
    PostChat(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error
}

// Chain is the registered set of hooks, run in order on every
// engine call. Held by the engine, populated at boot from config.
type Chain struct {
    Pre  []PreHook
    Post []PostHook
}
```

Engine code calls `chain.Pre` before `acp.Prompt(...)` and
`chain.Post` after. Short-circuit return on any Pre hook returning
a response (this is how budget-exceeded / content-blocked / cached
responses skip the LLM call).

**What we register on day one:**

- `RequestIDHook` (Pre) — generate / propagate `X-Request-Id`,
  attach to logger context.
- `AuthHook` (Pre) — bearer-token validation. Could be middleware
  instead; the plugin version is more flexible if we ever want
  per-token policy (rate limit, model allowlist).
- `LoggingHook` (Pre+Post) — structured request/response logging
  with timing.

**What the seams allow (future, not v1):**

- `ContentModerationHook` (Pre) — call a moderation API, block
  on policy violation. Returns a canonical error response that
  the adapter renders in its native error shape.
- `SchemaValidationHook` (Pre) — validate tool definitions, prompt
  size, image counts.
- `BudgetHook` (Pre) — token-bucket per API key, short-circuit
  with 429-equivalent canonical response when exceeded.
- `CacheHook` (Pre) — semantic cache lookup; short-circuit with
  cached response on hit.
- `AuditHook` (Post) — write request+response to audit log /
  S3 / observability backend.

**Critical property:** hooks run on the **canonical** types, not
on adapter-native types. This means a single moderation hook
covers both Pi SDK and LangFlow traffic — we don't write the same
guardrail twice.

**Decisions for the brainstorm:**

1. **Hooks vs middleware.** HTTP middleware runs before the
   adapter; plugin hooks run after the adapter has translated to
   canonical. Use middleware for transport concerns (CORS, raw
   request logging, IP allowlist); use hooks for LLM-call concerns
   (auth on canonical request, content moderation, budgets). Don't
   conflate.
2. **Error propagation.** When a hook short-circuits, the response
   is canonical. The adapter renders it in its native error shape
   (OpenAI's `{ error: { message, type, code } }` vs Ollama's
   `{ error: "..." }`). Confirm the error type is rich enough.
3. **Hook config.** Hooks are plain Go types registered at boot.
   No YAML config layer needed today. If we ever add hot-reload,
   that's a future problem.
4. **Streaming hooks.** Bifrost handles this; we should think about
   whether Post hooks have access to streamed chunks or only the
   reassembled final response. My intuition: Post sees the final
   reassembled response only (simpler), and per-chunk
   instrumentation is `engine`-internal. Discuss.

Reference: see `plugins/governance/main.go` in the local Bifrost
clone — specifically `HTTPTransportPreHook` (line ~342) and
`HTTPTransportPostHook` (line ~669). Their interface is more
elaborate (governance + virtual keys + rate-limit buckets); ours
is a stripped-down version.

### 3.15 Bifrost as reference architecture

`~/Projects/repos/local/bifrost` (a.k.a. `maximhq/bifrost`,
`docs.getbifrost.ai/overview`) is a production-grade Go LLM gateway
that does at large scale what we're building at small scale. **We
are not replicating it.** It is roughly 50× the scope of what we
need. But its architecture is the right shape and worth borrowing
from.

**What to read in the local clone:**

| Path | Why |
|---|---|
| `transports/bifrost-http/main.go` | How the binary wires logger + server + bootstrap. Mirror the shape. |
| `transports/bifrost-http/integrations/openai.go` | OpenAI adapter pattern — request decoding, model handling, streaming hooks. |
| `transports/bifrost-http/integrations/router.go` | `GenericRouter` + `StreamConfig` pattern. Worth understanding but **do not copy** — it's overbuilt for two surfaces. |
| `transports/bifrost-http/lib/middleware.go` | `ChainMiddlewares` helper. Simple, copyable. |
| `plugins/governance/main.go` | `HTTPTransportPreHook` / `HTTPTransportPostHook` — the guardrail interface we're modeling §3.14 on. |
| `plugins/compat/{dropparams,requestcopy,conversion}.go` | How a "SDK quirk" layer is kept separate from the core. Useful if we hit OpenAI/Ollama client quirks later. |
| `core/schemas/` | Their canonical type layout. Heavily over-engineered for us; skim for ideas only. |
| `framework/` | Configstore, kvstore, vectorstore, telemetry — out of scope for v1 but a useful map of "things a real gateway grows into." |

**Patterns we explicitly take:**

1. **`core/transports/plugins/framework/integrations` split.** We
   collapse to `engine/server/adapter/plugin/internal` but the
   conceptual split is identical.
2. **PreHook/PostHook on canonical types.** §3.14.
3. **Per-surface integration packages mounting onto a shared
   router.** §3.13.
4. **Compat layer for SDK quirks** as a separate concern from the
   adapter — if we discover Pi SDK or some specific LangFlow flow
   sends non-spec request shapes, that fix lives in a compat hook,
   not in the adapter itself.
5. **Embedded UI/assets via `go:embed`** if we ever ship a status
   UI. Out of scope for v1 but worth knowing the pattern exists.

**Patterns we explicitly reject (and why):**

1. **`fasthttp`.** Bifrost uses it for the throughput. We don't
   need it; stdlib `net/http` is simpler and our bottleneck is
   `kiro-cli` latency. §3.1.
2. **`sonic` (bytedance) JSON.** Faster than `encoding/json` but
   another non-stdlib dep with cgo-adjacent risks. Stick with
   stdlib until profiling proves otherwise.
3. **Configstore as a separate package with HTTP control plane.**
   Way too much. Env vars + a `Config` struct is enough (§3.6).
4. **Virtual keys, teams, customers, hierarchical budgets.** Out
   of scope. Bearer-token auth is sufficient.
5. **Multi-provider routing, fallback chains, weighted load
   balancing.** We have one backend (`kiro-cli`). N/A.
6. **MCP / WebSocket / WebRTC realtime surfaces.** Maybe future,
   not v1.
7. **Embedded React UI.** Not v1. `/health` JSON is enough.

**The point of borrowing from Bifrost is the *shape*, not the
*surface area*.** When the brainstorm produces the M0–M7 milestone
plan, each milestone should reference the Bifrost file that
demonstrates the equivalent at large scale, so the AI doing the
implementation can grep the reference instead of reinventing.

---

## 4. Non-goals (for v1)

- No new wire protocol or breaking changes to the Ollama API surface.
- No web UI / dashboard.
- No multi-tenant auth beyond the current bearer-token + IP-allowlist.
- Targets are x86_64 Linux + x86_64 Windows for v1. ARM and macOS-
  as-deployment-target are nice-to-haves.
- Not replacing `kiro-cli` itself. The whole point is to proxy it.
- No backward-compat with the Node server's `.env` file format
  beyond using the same env-var names.

---

## 5. Output I want from the brainstorm

When this brainstorm closes, I want a written summary saved to
`acp_server_go/PLAN.md` (or wherever the brainstorming skill puts
it) containing:

1. **Toolchain install**, step-by-step, for macOS dev. Go version
   pin policy (1.22+ vs 1.23+ — pick based on what stdlib features
   we use, especially `log/slog` ergonomics and any post-1.22
   `net/http` routing).
2. **VS Code / Claude Code setup** — `gopls`, the official Go
   extension, debugger config, `golangci-lint` integration.
3. **A `go.mod` skeleton** with the chosen dependencies and version
   policy.
4. **Module / package layout sketch** — files on day one and what
   each is responsible for.
5. **A staged implementation plan** with milestones. Updated to
   reflect dual-surface (§1.6) and adapter+engine layering (§3.13):
   - **M0:** `net/http` + chi server on :11434 returning hard-coded
     `/health`, `/api/version`, `/v1/models`. Empty adapter packages
     mounted but no real routes yet. Layout from §3.8 in place.
   - **M1:** `internal/acp` — spawn `kiro-cli`, JSON-RPC handshake,
     log received frames. No HTTP integration yet.
   - **M2:** `internal/canonical` types + minimal `internal/engine`
     with a single pooled session. `adapter/ollama` implements
     `POST /api/chat` non-streaming, no tools. End-to-end smoke
     test: curl → Ollama adapter → engine → kiro-cli → response.
   - **M3:** `adapter/openai` implements `POST /v1/chat/completions`
     non-streaming, no tools, sharing the same engine. Verify both
     surfaces work on the same port.
   - **M4:** Streaming. Ollama NDJSON + OpenAI SSE off the same
     canonical chunk channel. Client-disconnect cancellation via
     `r.Context()`.
   - **M5:** `internal/pool` proper — POOL_SIZE warm slots —
     plus `internal/registry` for stateful sessions via
     `X-Session-Id`.
   - **M6:** Tool-call path: `coerceToolCall`, plus the two adapter-
     specific arg shapes (string for OpenAI, object for Ollama).
   - **M7:** Embeddings per the §3.4 decision (Option C sidecar
     unless brainstorm flips it). Adapter routes:
     `POST /api/embed`, `POST /api/embeddings`, `POST /v1/embeddings`.
   - **M8:** Plugin hooks from §3.14: register `RequestIDHook`,
     `AuthHook`, `LoggingHook`. Wire chain into engine.
   - **M9:** Cross-compile builds + CI matrix per §3.9, distribution
     artifacts per §3.11.
   - …adjust as the brainstorm sees fit. Each milestone should
     reference the Bifrost file demonstrating the equivalent
     pattern at larger scale (§3.15).
6. **Decisions log** — for each of the §3 decisions, the chosen
   answer with one paragraph of rationale. The §3.4 embedding
   trade-off needs the deepest writeup. §3.13 (canonical type
   granularity) is second-most important.
7. **Trust-gate config artifacts**, concrete and ready to drop in:
   - A `.golangci.yml` with the linter set from §3.12.
   - A `goreleaser.yaml` skeleton (even if not used day one).
   - A `.github/workflows/ci.yml` implementing the layered pipeline.
   - A `docs/lints.md` skeleton with one example rule documented in
     full so future rules follow the pattern.
   - A `go-arch-lint` (or `import-boundaries`) config enforcing the
     §3.8 layer invariants — adapter cannot import engine, engine
     cannot import adapter, canonical imports nothing internal.
8. **Adapter spec stubs**, concrete and ready to expand:
   - `internal/canonical/types.go` skeleton with `ChatRequest`,
     `ChatChunk`, `ChatResponse`, `EmbedRequest`, `EmbedResponse`,
     `Error`.
   - `internal/adapter/ollama/adapter.go` and
     `internal/adapter/openai/adapter.go` each with an `Adapter`
     interface stub and `Mount(*chi.Mux)` method.
   - `internal/plugin/plugin.go` with the `PreHook` / `PostHook` /
     `Chain` types from §3.14.
9. **Open questions** that the brainstorm couldn't resolve and need
   either research or a prototype spike:
   - The embeddings benchmark for §3.4 Option A vs Option C.
   - Pi SDK's exact env var / config key for setting OpenAI base
     URL (§1.5 verification item).
   - Whether OpenAI's `response_format.json_schema` is feature-
     parity with Ollama's `format: <schema>` for the kiro-cli
     models we route to (probably yes, but spike to confirm).

---

## 6. Files in this repo you should read before answering

- `acp_server/CLAUDE.md` — deep-dive doc on how the Node server is
  structured. This is the spec to mirror.
- `acp_server/acp-ollama-server.js` — canonical Node implementation
  (~1070 lines). Read at least these regions:
  - lines 116–326 (`ACPSession` class)
  - lines 330–409 (`ACPPool`, `SessionRegistry`)
  - lines 484–541 (`buildAcpBlocksFromOllama`)
  - lines 559–595 (`chunksToOllamaMessage`)
  - lines 633–652 (`coerceToolCall`)
  - lines 793–888 (`handleOllamaCompletion`)
- `acp_server/README.md` — user-facing API and config surface.
- `acp_server/package.json` — current dependency set.
- Root `CLAUDE.md` — broader repo context.
- `rust_port_brief.md` — sibling brief for the Rust option. Read it
  for direct comparison; the structure is intentionally parallel.

**Outside this repo — the Bifrost reference (§3.15):**

Located at `~/Projects/repos/local/bifrost`. Read in this order
(don't read top-to-bottom; the codebase is huge):

| Path | Why | Approx priority |
|---|---|---|
| `~/Projects/repos/local/bifrost/transports/bifrost-http/main.go` | Binary entrypoint pattern — mirror the shape | **must read** |
| `~/Projects/repos/local/bifrost/transports/bifrost-http/integrations/openai.go` | OpenAI adapter — request decoding, model handling | **must read** |
| `~/Projects/repos/local/bifrost/transports/bifrost-http/integrations/router.go` (top docstring) | Streaming abstraction; understand, do not copy | **must read** |
| `~/Projects/repos/local/bifrost/plugins/governance/main.go` lines 342, 669 | `HTTPTransportPreHook` / `PostHook` — §3.14 model | **must read** |
| `~/Projects/repos/local/bifrost/transports/bifrost-http/lib/middleware.go` | `ChainMiddlewares` — directly copyable | useful |
| `~/Projects/repos/local/bifrost/plugins/compat/` | SDK-quirk layer pattern | useful if quirks appear |
| `~/Projects/repos/local/bifrost/core/schemas/` | Canonical type ideas | skim only |
| `~/Projects/repos/local/bifrost/framework/` | What a gateway grows into — future map | skim only |
| `https://docs.getbifrost.ai/overview` | Public docs, high-level concepts | skim only |

Avoid the rest of the Bifrost tree — `core/providers/`, `core/mcp/`,
`framework/configstore/`, `ui/`, `cli/`, `helm-charts/`, `terraform/`
are all out of scope for what we're building.

---

## 7. How I want to interact during the brainstorm

- **Ask me questions.** Five clarifying questions beat a 4000-word
  plan that guessed wrong on three of them.
- **Push back on my priors in §3** if you think I'm wrong. They're
  priors, not requirements.
- **The embeddings decision in §3.4 is the most important call.**
  Don't bury it. If you think Option A (cgo + ONNX) is actually
  fine despite the cross-compile hit, argue for it concretely.
  If Option C (sidecar) is the right call, the plan needs to spec
  the sidecar (what runs it, how it's bundled with releases, how
  health-checks coordinate).
- **Don't write Go code yet.** This brainstorm decides *what* to
  build and *how* to set up. Implementation comes after.
- **Flag risks early.** If anything I'm asking for has a painful
  Go-ecosystem story, surface it now so I can re-scope rather than
  discover at M6.
