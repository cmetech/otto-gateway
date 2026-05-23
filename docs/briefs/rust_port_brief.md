# Brainstorm Brief: Rust Port of `acp_server`

> **How to use:** Paste this entire file as the opening message of a
> `/superpowers:brainstorming` session. It is written to give the
> brainstorming agent enough context that it doesn't waste cycles
> re-discovering what the existing system is, and to surface the
> design decisions that actually need debate.

---

## 0. What I want out of this brainstorm

I want to build a **Rust port** of an existing Node.js HTTP server.
The Node server (`acp_server/acp-ollama-server.js`) is in production
and works, but I want a Rust rewrite that:

- Is **functionally identical** (same wire protocol, same endpoints,
  same client compatibility — LangFlow, LangChain, Continue.dev all
  keep working without changes).
- Compiles to **a single native executable for Linux and Windows**
  (and macOS if it's free — I'm on macOS for dev).
- Is **faster, lower-memory, and scales further** under concurrent load
  than the Node implementation.
- Uses **idiomatic async Rust** so I learn the language properly while
  building something real.

**This is my first Rust project.** I am a senior engineer in other
languages (JS, Python, shell, some Go) but I have written exactly zero
lines of Rust. I need the brainstorm output to include the toolchain
and environment setup, not just architecture.

By the end of the brainstorm I want a written plan that I (or a future
Claude Code session) can execute. The plan should cover:

1. Rust toolchain install (rustup, cargo, components) for macOS dev,
   plus what's needed on the target build hosts (or for cross-compiling
   from macOS).
2. Recommended editor/IDE setup (I use Claude Code in VS Code).
3. Project layout (workspace? single crate? binary + library split?).
4. Crate selection with rationale — async runtime, HTTP framework,
   JSON-RPC, embeddings, etc. Where there are real choices (axum vs
   actix-web vs hyper-only), I want the trade-offs spelled out, not
   just a pick.
5. A staged implementation plan — what to build first, in what order,
   with a working milestone at each stage.
6. Cross-compilation / distribution strategy (how do I produce
   `acp-server-linux-x86_64` and `acp-server-windows-x86_64.exe` from
   one dev machine, or in CI).
7. A testing strategy. The Node version has zero tests. I want the
   Rust version to have *enough* tests to refactor confidently — not
   exhaustive coverage on day one, but the load-bearing weirdness
   (tool-call coercion, NDJSON streaming, session lifecycle) must be
   under test.

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
  (Windows native). Most porting fixes get applied to both. Rust
  should collapse this into one binary that runs on both OSes.

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
   entry. **This must be preserved in the Rust port.** It exists
   because LangChain agents rely on JSON-as-tool-call. Removing it
   silently breaks real users.
8. Response: NDJSON streamed (default) or a single JSON body when
   `stream: false`.

### Internal classes (Node) — to be re-modeled in Rust

- **`ACPSession`** — wraps one spawned `kiro-cli acp` process. JSON-RPC
  over stdio. Tracks pending requests by ID. Auto-grants
  `session/request_permission` with `allow_always`. Emits typed
  chunks. Has a 60s ping heartbeat that kills the process on failure.
- **`ACPPool`** — fixed-size (default 4) pool of warm `ACPSession`s
  for stateless requests. Lazy re-spawn of dead slots.
- **`SessionRegistry`** — `HashMap<sessionId, ACPSession>` for
  stateful sessions. Idle reap every 60s.
- **`EmbeddingRegistry`** — local fastembed models, completely
  independent of Kiro. Default model warmed at startup.

### Config (env-driven)

`PORT`, `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE`,
`SESSION_TTL_MS`, `PING_INTERVAL`, `AUTH_TOKEN` (comma-list),
`ALLOWED_IPS` (comma-list), `DEBUG`, `EMBEDDING_MODEL_DEFAULT`,
`EMBEDDING_MODELS_ENABLED`, `EMBEDDING_CACHE_DIR`,
`EMBEDDING_BATCH_SIZE`, `EMBEDDING_MAX_INPUTS`.

Per-request headers: `X-Session-Id`, `X-Working-Dir`, `X-Request-Id`.

### Things that must survive the port

- **Ollama default `stream: true`** on `/api/chat` and `/api/generate`.
  Must respond `application/x-ndjson` with chunked transfer.
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
- **Client disconnect cancels the in-flight ACP prompt**
  (`session/cancel`).

---

## 2. Why Rust, specifically

I have a soft set of reasons — push back if any of them are weak.

- **Single static binary.** `cargo build --release` plus appropriate
  target triple → one file users can drop into a directory and run.
  No `node`, no `npm install`, no `node_modules`. This is the headline
  feature for me.
- **Async I/O without callback hell or single-thread bottleneck.**
  tokio gives me true multi-core async. Node forces everything onto
  one event loop; my pool warmup and embedding compute already fight
  for that loop.
- **Memory safety + predictable resource use.** The Node server holds
  N child processes, M JSON-RPC pending maps, embedding model state,
  streaming response buffers. I want the compiler to enforce lifetimes
  on those rather than discovering leaks at 3am.
- **Lower per-request overhead.** Streaming NDJSON in Express 5 has
  visible CPU cost at high request rates. I want to see if Rust can
  saturate the `kiro-cli` pool with much lower proxy overhead.
- **Cross-compilation story is real.** I want to build Linux and
  Windows binaries from my macOS dev box (or in CI). Node packaging
  to a single binary (`pkg`, `nexe`) is fragile and bundles 40MB of
  V8.
- **Learning goal.** I want to actually learn async Rust on a project
  with a clear spec and a working reference implementation, not on a
  greenfield design exercise.

---

## 3. Decisions I want the brainstorm to surface trade-offs on

I have weak priors. Tell me where they're wrong.

### 3.1 Async runtime
- Prior: `tokio`. It's the default and everything in the ecosystem
  integrates with it.
- Question: is there *any* reason to prefer `async-std` or
  `smol` for this use case? I don't think so but I want it asked.

### 3.2 HTTP framework
- Prior: `axum` (built on `tower` + `hyper`, well-supported, the
  modern default).
- Alternatives to consider: `actix-web` (more mature, slightly faster
  in some benchmarks, different ergonomics), raw `hyper` (most
  control, more code).
- I want a real trade-off here, not just "axum is popular."
  Specifically: which one handles **chunked NDJSON streaming with
  client-disconnect detection** most ergonomically? That's the
  hot path.

### 3.3 JSON-RPC client over child stdio
- I don't think there's an off-the-shelf crate for "spawn a
  subprocess, talk JSON-RPC 2.0 to it over stdio, handle bidirectional
  notifications, support pending-request correlation by id." The Node
  code rolls this by hand (`readline` on stdout, `Map` of pending
  promises).
- Brainstorm: is there a crate (`jsonrpsee`, `tower-jsonrpc`, etc.)
  that fits, or do I roll it? My guess is roll it — most JSON-RPC
  crates assume HTTP or WebSocket transport. Want this confirmed.
- Sub-question: how to model the bidirectional channel idiomatically?
  My intuition is: one task reading lines from stdout, dispatching
  to either (a) a `oneshot::Sender` keyed by request id, or (b) a
  `broadcast`/`mpsc` channel for notifications. One task writing to
  stdin behind an `mpsc`. Sanity-check this.

### 3.4 Embeddings
This is the one place I expect real research:
- `fastembed-rs` exists. Same ONNX models as the Node binding. Likely
  the path of least resistance.
- `candle` is HuggingFace's pure-Rust ML framework. Heavier; gives me
  options beyond BGE/E5.
- `ort` (ONNX Runtime bindings) — lower level, more flexible.
- I want to know which crate has the smoothest path to: load BGE /
  E5 models, batch-embed strings, cache to a local dir, work on
  Windows + Linux. Binary size matters (would prefer to keep release
  binary under ~50MB if feasible).

### 3.5 Subprocess management
- `tokio::process::Command` is the obvious answer. Cross-platform
  spawning, async stdin/stdout/stderr.
- The Node version has Windows-specific `shell: true` because
  `kiro-cli` is a `.cmd` file on Windows. I think `tokio::process`
  on Windows handles `.cmd`/`.bat` automatically via `CreateProcessW`,
  but I want the brainstorm to verify. If not, what's the idiom?

### 3.6 Configuration loading
- Prior: `dotenvy` + `figment` or `config`, env-driven.
- Question: is there a cleaner pattern (e.g. `serde` + a single
  `Config` struct deserialized from env with sensible defaults)?
- Don't over-engineer this — env vars are fine. But pick a crate.

### 3.7 Logging
- Prior: `tracing` + `tracing-subscriber`. Structured logs, async
  context preservation. The Node version uses a custom `log(tag,
  ...args)` helper — `tracing` is the obvious upgrade.
- Confirm or rebut.

### 3.8 Project structure
- Single binary crate?
- Workspace with a `core` library crate + a `bin` crate? (Easier to
  test, but more ceremony.)
- Where does the JSON-RPC / ACP layer live? Where does the HTTP /
  Ollama-translation layer live? My instinct is to split them as
  modules, not crates, on day one.

### 3.9 Cross-compilation strategy
- Building Linux from macOS: `cross` (docker-based, well-trodden)
  vs. `cargo-zigbuild` (uses Zig as the cross-linker, lighter).
- Building Windows from macOS: same two options, but Windows needs
  MinGW or MSVC toolchain access. `cargo-zigbuild --target
  x86_64-pc-windows-gnu` is reportedly the smoothest path.
- Or just delegate to GitHub Actions matrix builds. What's the lowest
  friction for someone whose dev box is macOS Darwin 25?

### 3.10 Testing strategy
- Unit tests for the pure functions (build_acp_blocks, coerce_tool_call,
  pick_cwd, etc.).
- Integration tests for the HTTP layer. Probably use `axum::testkit`
  or `reqwest` against a `tokio::spawn`'d test server.
- The hard one: testing the ACP/subprocess layer. Options:
  (a) mock `kiro-cli` with a fake binary that speaks JSON-RPC,
  (b) abstract `ACPSession` behind a trait so tests inject a fake
  transport,
  (c) skip subprocess tests and only test integration end-to-end
  against a real `kiro-cli` in CI.
  I lean (b). Discuss.

### 3.11 Distribution & packaging
- Static linking on Linux: `musl` target for a fully static binary,
  or `gnu` target with dynamic libc? Embeddings via ONNX Runtime
  may force `gnu` because ONNX has C++ dependencies.
- Windows: MSVC vs MinGW. Which one ships smaller / fewer
  dependencies for end users?
- Signing? Notarization? (Probably out of scope for v1; flag it.)

### 3.12 Trust gates for AI-assisted code (non-negotiable)

This codebase will be written with heavy AI assistance. I want the
trust gates from "Making AI-Generated Rust Code Trustworthy"
(L. Garcia, Medium) baked in from day one — not bolted on later.
The brainstorm should treat the items below as requirements and
focus on *how* to implement them, not whether to.

Required gates:

1. **Strict Clippy in CI.** `cargo clippy --all-targets --all-features
   -- -D warnings`. Warnings are hard failures. No `#[allow(...)]`
   without an inline comment explaining why.
2. **Forbid `unwrap()` and `expect()` outside tests.** Configure
   `clippy::unwrap_used` and `clippy::expect_used` as deny-level lints
   at the crate root, allowed only in `#[cfg(test)]` modules. Decide
   whether to apply to `main.rs` (startup is reasonable place for
   `expect`).
3. **`missing_docs` enforced on public items.** Every `pub fn`, `pub
   struct`, `pub enum` requires a doc comment. Set as deny on the
   crate root.
4. **Doctests as executable spec.** Where a function has non-obvious
   contract (e.g. `build_acp_blocks_from_ollama`, `coerce_tool_call`),
   the doc comment includes a runnable `///` example, validated by
   `cargo test --doc` in CI.
5. **Project-specific lints via `dylint`.** Layered constraints that
   stock clippy can't express. Starter set (≤5 rules, expand later):
   - No `unwrap()`/`expect()` in HTTP handler modules even in tests.
   - No imports from `infrastructure::*` inside `domain::*` (or
     whatever module boundaries the brainstorm settles on).
   - Public types in the HTTP-DTO layer must derive `Serialize +
     Deserialize` (drift here = silent protocol break).
   - Functions whose name starts `handle_` must return `Result<_,
     ApiError>` and not `Result<_, anyhow::Error>` (force API errors
     through the typed error surface).
   - Any `unsafe` block must have a `// SAFETY:` comment immediately
     above (custom dylint or rely on `clippy::undocumented_unsafe_blocks`).
6. **Layered CI pipeline.** Stages run in order, each blocks the next:
   `cargo fmt --check` → `cargo build` → `cargo clippy -D warnings`
   → `cargo dylint --all` → `cargo test` → `cargo test --doc` →
   property tests → cross-compile smoke. No skipping on green branches.
7. **Property tests for protocol translation.** The Ollama↔ACP block
   builder and the chunk reassembler are the kinds of functions that
   pass unit tests but fail on the input shapes you didn't think of.
   Use `proptest` or `quickcheck` to fuzz them. Minimum coverage:
   - `buildAcpBlocksFromOllama` round-trip property: every valid
     messages array produces blocks that parse cleanly.
   - `coerceToolCall` property: any plain-JSON-as-text + tools input
     either coerces or passes through; never panics.
8. **Doc the rules themselves.** Each dylint rule has a paragraph in
   `docs/lints.md` explaining what invariant it protects and a link
   to the incident or design decision that motivated it. The doc is
   for both human reviewers and the AI agents writing code against
   the rules.
9. **Lint-on-incident loop.** When a human review catches a bug
   AI-generated, the immediate question is "could a lint have caught
   this?" If yes, write the lint before the fix lands. Plan should
   include this as an operating procedure, not just a one-time setup.
10. **Start with 3–5 dylint rules, not 20.** Expand iteratively. The
    starter set in item 5 is the v1 baseline; growth comes from item 9.

Want the brainstorm to address: which of these have ecosystem rough
edges (e.g. is `dylint` painful to bootstrap on a fresh project?
does it cross-compile cleanly?), and whether any of the dylint rules
in item 5 are better expressed as integration tests, type-system
encoding, or just plain clippy config.

Source: <https://medium.com/@lagarciag/making-ai-generated-rust-code-trustworthy-3403966b69db>

---

## 4. Non-goals (for v1)

To keep scope honest, I'm explicitly *not* asking for:

- A new wire protocol or breaking changes to the Ollama API surface.
- A web UI / dashboard.
- Multi-tenant auth (current bearer-token + IP-allowlist scheme is
  enough).
- Anything beyond x86_64 Linux + x86_64 Windows for v1. ARM and
  macOS-as-deployment-target are nice-to-haves, not blockers.
- Replacing `kiro-cli` itself. The whole point is to proxy it
  faithfully.
- Backward-compat with the Node server's `.env` file format
  beyond using the same env-var names. (I'll keep variable names
  identical so a deployment can swap binaries.)

---

## 5. Output I want from the brainstorm

When this brainstorm closes, I want a written summary (markdown,
saved to `acp_server_rust/PLAN.md` or wherever the brainstorming
skill puts it) containing:

1. **Toolchain install**, step-by-step, for macOS dev. Including
   rustup, default toolchain, components (`rustfmt`, `clippy`,
   `rust-analyzer`), and any cross-compilation prerequisites
   (`cross` and/or `cargo-zigbuild`, `zig` install, MinGW if needed).
2. **VS Code / Claude Code setup** — extensions, settings worth
   tweaking, debug config if non-obvious.
3. **A `Cargo.toml` skeleton** with the chosen crates and version
   pins (or version ranges with rationale).
4. **A module layout sketch** — what files exist on day one and what
   each is responsible for.
5. **A staged implementation plan** with milestones. Something like:
   - M0: hello-world axum server on :11434 returning hard-coded
     `/api/version` and `/health`.
   - M1: spawn `kiro-cli`, do the JSON-RPC handshake, log received
     frames.
   - M2: `/api/chat` non-streaming, single pooled session, no tools.
   - M3: streaming NDJSON, client-disconnect cancellation.
   - M4: pool + stateful registry.
   - M5: tool-call path + coercion.
   - M6: embeddings.
   - M7: cross-compile builds.
   - …adjust as the brainstorm sees fit.
6. **Decisions log** — for each of the §3 decisions, the chosen
   answer with one paragraph of rationale.
7. **Trust-gate config artifacts**, concrete and ready to drop in:
   - The clippy lint config (in `Cargo.toml` `[lints.clippy]` or a
     top-of-crate `#![deny(...)]` block).
   - The starter `dylint` workspace layout and rule stubs.
   - A `.github/workflows/ci.yml` (or equivalent) implementing the
     layered pipeline from §3.12 item 6.
   - A `docs/lints.md` skeleton with one example rule documented in
     full so future rules follow the pattern.
8. **Open questions** that the brainstorm couldn't resolve and need
   either research or a prototype spike.

---

## 6. Files in this repo you should read before answering

If the brainstorming agent has tool access:

- `acp_server/CLAUDE.md` — the deep-dive doc I just wrote on how the
  Node server is structured. This is the spec to mirror.
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

Don't read all 1070 lines of the Node server top-to-bottom; the
sections above are where the load-bearing logic lives.

---

## 7. How I want to interact during the brainstorm

- **Ask me questions.** I'd rather answer five clarifying questions
  than read a 4000-word plan that guessed wrong on three of them.
- **Push back on my priors** in §3 if you think I'm wrong. I marked
  them as priors, not requirements.
- **Don't write Rust code yet.** This brainstorm is about deciding
  *what* to build and *how* to set up. Implementation comes after,
  in a separate session that consumes the plan this one produces.
- **Flag risks.** If something I'm asking for has a known-painful
  Rust ecosystem story (e.g. ONNX on Windows-MinGW, or whatever),
  surface it early so I can re-scope rather than discover it at M6.
