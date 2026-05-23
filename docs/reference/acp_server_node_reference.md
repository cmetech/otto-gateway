# CLAUDE.md — acp_server/

Guidance for Claude Code when working inside this directory. The root-level
`CLAUDE.md` covers the repo at large; this file is the deep-dive into the
proxy server itself.

## Purpose

This directory implements an **Ollama-compatible HTTP server that proxies
LLM calls through `kiro-cli` ACP agents**. Anything that speaks the Ollama
REST API (LangChain, LangFlow, Continue.dev, Open WebUI, llama-index, …)
can point at `http://localhost:11434` and transparently get routed to Kiro
without knowing Kiro exists.

The server is a **drop-in Ollama daemon replacement**. It binds Ollama's
default port (11434) and implements the full surface area of the Ollama
API. Behind that surface, every chat/generate request is translated into
the Agent Client Protocol (ACP), shipped over stdio to a spawned
`kiro-cli acp` process, and the streaming response is translated back
into Ollama's NDJSON format.

```
┌────────────────┐  Ollama API   ┌─────────────────┐  ACP / JSON-RPC  ┌──────────────┐
│ LangFlow /     │ ───────────▶ │  acp-ollama-    │ ───── stdio ───▶ │  kiro-cli    │
│ LangChain /    │   :11434      │  server (Node)  │                  │  (ACP agent) │
│ Continue.dev   │ ◀─── NDJSON ─ │                 │ ◀─── stdio ───── │              │
└────────────────┘               └─────────────────┘                  └──────────────┘
```

Embeddings are the one exception: they're served locally by `fastembed`
(BGE / E5 models) and never touch Kiro. Same `/api/embed` endpoint, just
a different backend.

## Source layout

```
acp_server/
├── acp-ollama-server.js       # WSL/Linux build  (~1070 lines, ESM)
├── acp-ollama-server-win.js   # Windows native build (~1072 lines)
├── package.json               # express 5, cors, dotenv, fastembed
├── README.md                  # user-facing setup / config / endpoint list
└── local_cache/               # fastembed model cache (if EMBEDDING_CACHE_DIR set)
```

There is **no build step, no transpilation, no test suite**. Each server
file is a single ESM module. Validation is manual: hit the server with
`curl`, or point a LangFlow flow at it.

## Request lifecycle (POST /api/chat)

1. **Express** receives the JSON. CORS + (optional) bearer-auth + (optional)
   IP allowlist middleware runs first. Auth is skipped for `/`,
   `/api/version`, and `/health`.
2. **`buildAcpBlocksFromOllama(messages, tools, opts)`** flattens the
   Ollama messages array into a single ACP `text` block. The flattened
   text uses bracketed section headers so the model can parse roles:
   - `[System]` — system prompt
   - `[Reasoning]` — appended when `think: true`
   - `[Output format]` — appended when `format: 'json'` or a JSON Schema
   - `[Available tools]` — JSON-encoded tool definitions
   - `[User]`, `[Assistant]`, `[Assistant tool call: name]`, `[Tool result]`
   Inline `images: [b64, …]` arrays become separate ACP `image` blocks
   appended after the text block. MIME type is sniffed from base64 prefix.
3. **`handleOllamaCompletion()`** decides whether this is a *stateless* or
   *stateful* request:
   - **Stateless** (no `X-Session-Id` header) → grab a slot from the
     `ACPPool`, call `client.newSession(cwd)` to start a fresh ACP
     session, then prompt and release the slot.
   - **Stateful** (`X-Session-Id` provided) → look up / create an entry
     in `SessionRegistry` keyed by that ID. The session persists across
     requests for `SESSION_TTL_MS` (30 min default).
4. **`client.setModel(modelId)`** — switches the ACP agent to the
   requested model unless it's `'auto'` (the default).
5. **`client.prompt(blocks, onChunk)`** — sends `session/prompt` over
   JSON-RPC to `kiro-cli` and streams `session/update` notifications back
   as typed chunks (`text`, `thought`, `tool_call`, `plan`).
6. **`chunksToOllamaMessage(chunks, wantThinking)`** reassembles the
   chunks into an Ollama-shaped `{ role, content, thinking?, tool_calls? }`
   message. `tool_calls[].function.arguments` is a **plain object**
   (Ollama style), not a JSON string (OpenAI style).
7. **`coerceToolCall(message, tools)`** runs if no native tool calls
   came back. If the message body is plain JSON (with or without a
   ```json fence), and tools were supplied, it's converted into a
   synthetic `tool_calls` entry. **See "Load-bearing weirdness" below.**
8. Response is either streamed NDJSON (Ollama default) or a single JSON
   body when `stream: false`.

`/api/generate` follows the same path but with a single user turn built
by `buildAcpBlocksFromGenerate(prompt, system, images, opts)`.

## Core classes

### `ACPSession` (extends EventEmitter)
JSON-RPC-over-stdio wrapper around one spawned `kiro-cli acp` process.

- `start()` — spawns the process, hooks `readline` on stdout, attaches
  stderr/exit handlers. Resolves on the first `readable` event or a
  600ms timeout (whichever comes first).
- `_send(msg)` / `_req(method, params)` — `_req` returns a Promise that
  resolves when a JSON-RPC response with the same `id` arrives.
- `_route(msg)` — dispatches incoming JSON-RPC frames:
  - Responses with `id` → resolve/reject the pending promise.
  - `session/request_permission` → **auto-granted with `allow_always`**.
    This is why agent tool invocations don't block the proxy waiting for
    a human in the loop.
  - `session/update` / `_kiro.dev/session/update` → translated into
    `chunk` events (`text`, `thought`, `tool_call`, `plan`).
- `initialize()` — JSON-RPC `initialize` handshake; records
  `promptCapabilities`; starts the ping loop.
- `newSession(cwd)` — JSON-RPC `session/new`; records `sessionId`,
  `availableModels`, `currentModel`.
- `setModel(modelId)` — `session/set_model`. No-op for `'auto'`.
- `prompt(blocks, onChunk)` — `session/prompt`; subscribes onChunk to
  `chunk` events for the duration of the call.
- `cancel()` — fires `session/cancel` so an in-flight prompt is aborted
  on client disconnect (`req.on('close')`).
- Heartbeat: pings every `PING_INTERVAL` (60s). Tolerates "method not
  found" — kills the process on any other ping failure.

### `ACPPool`
Fixed array of `POOL_SIZE` (default 4) warm `kiro-cli` slots for
stateless requests.

- `warmup()` — runs *before* `app.listen()`, so the first request after
  cold start is fast (the boot itself takes a few seconds).
- `acquire()` — returns the first non-busy slot, or queues a Promise
  resolver and waits.
- Dead slots are re-spawned lazily inside `acquire()` / `release()`.

### `SessionRegistry`
Map of stateful sessions keyed by client-supplied `X-Session-Id`.

- Each entry owns its own dedicated `ACPSession` (not from the pool).
- Reaped every 60s; entries older than `SESSION_TTL_MS` are closed.
- `DELETE /v1/sessions/:id` tears one down explicitly.

### `EmbeddingRegistry`
Local `fastembed` models — completely independent of Kiro.

- Lazily loads enabled models on first request.
- Default model (`BGESmallENV15` unless overridden) is warmed at startup.
- `getModel(name)` throws `ModelNotEnabledError` for anything not in
  `EMBEDDING_MODELS_ENABLED`, which maps to a 400 response.

## Message conversion: `buildAcpBlocksFromOllama`

The Ollama wire format and the ACP block format are not 1:1. This function
is the bridge.

| Ollama input              | ACP output                                              |
|---------------------------|---------------------------------------------------------|
| `system` message          | `[System]\n<content>\n\n` at the top of the text block  |
| `user` / `assistant` text | `[User]\n…` / `[Assistant]\n…` sections                 |
| `assistant.tool_calls`    | `[Assistant tool call: <name>]\n<JSON args>\n\n`        |
| `role: 'tool'` message    | `[Tool result (id: <call_id>)]\n<content>\n\n`          |
| `tools` array (top-level) | `[Available tools]` section with JSON-encoded specs     |
| `images: [b64]` per msg   | Separate ACP `image` block with sniffed MIME type       |
| `format: 'json'`          | `[Output format] Respond ONLY with a valid JSON …`      |
| `format: { …schema }`     | `[Output format]` + JSON-fenced schema                  |
| `think: true`             | `[Reasoning] Think through the problem step by step…`   |

Everything else from the request body — `keep_alive`, `options`, etc. —
is accepted and silently ignored. That's deliberate: Ollama clients send
a lot of knobs the upstream Kiro agent can't honor.

## Load-bearing weirdness: `coerceToolCall`

LangChain (and a few other Ollama clients) sometimes provoke Kiro models
into emitting tool invocations as **plain JSON in the message body**
instead of as a true `tool_call` ACP notification:

```json
{"location": "Boston", "unit": "celsius"}
```

…or sometimes wrapped in a markdown fence:

````
```json
{"location": "Boston", "unit": "celsius"}
```
````

`coerceToolCall(message, tools)` detects this and rewrites the message:

1. Skip if no `tools` were supplied or `tool_calls` already exist.
2. Try `JSON.parse` on the raw content.
3. If that fails, look for a ` ```json … ``` ` fenced block and parse that.
4. Score each tool by how many of its schema properties appear as keys in
   the parsed object (`pickBestTool`); pick the highest scorer.
5. Replace `content` with `''` and inject a synthetic `tool_calls` entry.

**Do not "clean up" this function.** It exists because of specific
real-world LangChain failures — the commit log around `0ead935`,
`7569745`, and `995c569` is the receipts. Pull it out and LangChain
agents that rely on JSON-as-tool-call silently break.

## WSL vs Windows parity

`acp-ollama-server.js` and `acp-ollama-server-win.js` are **two copies of
the same server** with platform deltas. Most commits in `git log` are
porting fixes between them. **When you change one, change the other**
unless the change is genuinely platform-specific.

Intentional differences (also documented in `README.md`):

1. **`KIRO_CMD` default** — both default to `kiro-cli`, but resolution
   differs (Windows finds it via PATH as a `.cmd`; WSL finds the POSIX
   binary).
2. **`spawn()` options** — Windows adds `shell: true` so Node can
   execute `.cmd` files. WSL spawns directly.
3. **File URI parsing** — Windows strips `file:///` (three slashes,
   `/^file:\/\/\/?/`), POSIX strips `file://` (two, `/^file:\/\//`).
4. **Path operations** — Windows uses `path.dirname` and `path.sep`;
   POSIX uses `path.posix.dirname` and hardcoded `/`.

Plus a few minor differences picked up by the most recent porting pass:

- Windows reads `DEBUG` via `process.env.DEBUG?.trim()` (handles
  `set "DEBUG=1"` putting a trailing space on the value).
- Windows logs `kiro-cli` stderr at `log` level rather than `dbg`,
  because the platform tends to fail more loudly and we want it visible.
- Windows attaches a no-op `stdin.on('error')` to suppress unhandled
  EPIPE when the child dies mid-write.
- Windows calls a `stripMarkdownFences()` helper on the generate-path
  text content **before** passing it to the client. **Heads-up: that
  function is referenced on line 863 of `acp-ollama-server-win.js` but
  is not defined in the file** — likely a half-finished port. If you
  exercise the Windows generate path with markdown fences in the
  response, you'll get a `ReferenceError`. Fix in both files or remove
  the call; don't leave it asymmetric.

Any divergence beyond the four intentional ones (and the bullets above)
is almost certainly a missed port — reconcile it.

## Configuration

All config is environment-driven. A `.env` file in this directory works
(dotenv is loaded at the top of both server files).

| Var | Default | Notes |
|---|---|---|
| `PORT` | `11434` | Ollama's default port |
| `KIRO_CMD` | `kiro-cli` | Spawned executable |
| `KIRO_ARGS` | `acp` | Space-split argv |
| `KIRO_CWD` | `process.cwd()` | Default working dir for `kiro-cli` |
| `POOL_SIZE` | `4` | Warm slots for stateless requests |
| `SESSION_TTL_MS` | `1800000` | Stateful session idle timeout (30 min) |
| `PING_INTERVAL` | `60000` | Heartbeat interval |
| `MAX_EXEC_MS` | `600000` | Reserved (not enforced in flight today) |
| `AUTH_TOKEN` | _(empty)_ | Comma-separated bearer tokens; empty = open |
| `ALLOWED_IPS` | _(empty)_ | IP allowlist; empty = allow all |
| `DEBUG` | `0` | `1` enables verbose stderr logging |
| `EMBEDDING_MODEL_DEFAULT` | `BGESmallENV15` | Default fastembed model |
| `EMBEDDING_MODELS_ENABLED` | `=DEFAULT` | Comma-list of allowed model names |
| `EMBEDDING_CACHE_DIR` | _(none)_ | Optional override for fastembed cache |
| `EMBEDDING_BATCH_SIZE` | `32` | fastembed batch size |
| `EMBEDDING_MAX_INPUTS` | `2048` | Hard cap on `/api/embed` array length |

Per-request overrides via headers:

- `X-Session-Id` — opt in to stateful session.
- `X-Working-Dir` — override the `cwd` the spawned `kiro-cli` sees.
- `X-Request-Id` — client-supplied request ID; echoed back in response.

## Endpoint map

| Method | Path | Notes |
|---|---|---|
| `POST` | `/api/chat` | Multi-turn; **streams NDJSON by default** (`stream: true`) |
| `POST` | `/api/generate` | Single-turn; same streaming default |
| `POST` | `/api/embed` | Local fastembed; new API (string or array input) |
| `POST` | `/api/embeddings` | Legacy embeddings API (single prompt) |
| `GET` | `/api/tags` | Chat models from Kiro startup probe + enabled embedding models |
| `POST` | `/api/show` | Model metadata; capabilities = `embedding` or `completion,tools` |
| `GET` | `/api/ps` | "Running models" — returns a synthetic `auto` entry |
| `GET` | `/api/version` | Static `0.9.0` |
| `POST` | `/api/pull`, `/api/push`, `/api/create`, `/api/copy` | Stubs (no-op success) |
| `DELETE` | `/api/delete` | Stub |
| `GET` | `/health` | Pool + registry + embeddings stats |
| `GET` | `/health/agents` | Per-slot + per-session detail |
| `DELETE` | `/v1/sessions/:id` | Tear down a stateful session |

## Things that are easy to get wrong

- **Ollama streams by default.** Both `/api/chat` and `/api/generate`
  default to `stream: true` and respond with `application/x-ndjson`.
  Pass `"stream": false` for a single JSON body.
- **Don't touch `coerceToolCall` without a test plan.** See the
  "Load-bearing weirdness" section above. The fence-stripping and
  best-tool-scoring exist for specific LangChain failures.
- **`pickCwd(blocks, fallback)` derives the working directory from
  `resource_link` blocks** in the prompt by taking the longest common
  parent directory. Falls back to `KIRO_CWD`. Clients can override with
  `X-Working-Dir`.
- **`session/request_permission` is auto-granted** with `allow_always`
  in `_route()`. This is what lets agent tool invocations proceed
  without a human in the loop. If you ever want gated permissions in
  this proxy, that's the place.
- **Pool warmup happens before `app.listen()`.** The process won't accept
  HTTP until all `POOL_SIZE` `kiro-cli` instances are up. Cold boot is
  slow; the first real request is fast.
- **The Windows server references an undefined `stripMarkdownFences`.**
  See the parity section. If/when you touch the generate path on
  Windows, fix this in both files.
- **Token counts in `makeStats` are estimated** — `Math.ceil(len / 4)`.
  Don't read them as truth.
- **`CLAUDE.md` files are git-ignored at the repo root.** Both this file
  and `../CLAUDE.md` are intentionally local-only. Don't commit them.

## Running and debugging

From this directory:

```bash
npm install                 # once

# WSL / Linux
npm run ollama              # node acp-ollama-server.js
npm run ollama_dev          # DEBUG=1

# Windows (native cmd / PowerShell)
npm run win                 # node acp-ollama-server-win.js
npm run win_dev             # set DEBUG=1 first
```

Quick health checks while the server is up:

```bash
curl http://localhost:11434/health           # pool/registry/embedding stats
curl http://localhost:11434/health/agents    # per-slot detail
curl http://localhost:11434/api/tags         # available models (kiro + embed)
```

Smoke test the chat path:

```bash
curl http://localhost:11434/api/chat \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

Smoke test embeddings:

```bash
curl http://localhost:11434/api/embed \
  -d '{"model":"BGESmallENV15","input":"hello world"}'
```

When debugging streaming weirdness, set `DEBUG=1` and watch stderr:
every JSON-RPC frame in and out of `kiro-cli` is logged (truncated at
300 chars). The `coerce` tag fires whenever `coerceToolCall` rewrites a
response — useful for confirming tool-call coercion is doing what you
expect on a given client.
