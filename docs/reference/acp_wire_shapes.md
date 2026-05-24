# ACP Wire Shapes — Ground Truth

**Status:** Authoritative reference for ACP JSON-RPC wire shapes that
otto-gateway must produce/consume when talking to `kiro-cli acp`.

**Sources (in precedence order):**

1. **`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js`**
   — the Node.js implementation that is **proven working against `kiro-cli`
   2.4.1** on the author's dev box. When this differs from the spec, this
   wins because it's empirically validated.
2. **https://agentclientprotocol.com/** — the upstream ACP spec
   (`protocol/initialization.md`, `protocol/prompt-turn.md`,
   `protocol/content.md`). Authoritative for shapes the Node code doesn't
   exercise (e.g., image/audio/resource blocks the Node version stubbed out).
3. **https://kiro.dev/docs/cli/acp/** — Kiro's per-CLI documentation.
   Currently contradicts the agentclientprotocol.com spec in places
   (notification method name, type casing) — treat as informational only.

**Why this doc exists:** Phase 1's ACP wire shapes were derived from
`acp_server_node_reference.md` (a narrative doc), not from the Node source
or live spec. Phase 1 integration tests used a fake server we wrote to
match our (incorrect) assumptions. This doc captures the discrepancies
discovered during Phase 2 discuss and is the spec downstream agents must
follow for any ACP wire-shape work.

## 1. initialize

### Client → Agent (request)

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "protocolVersion": 1,
    "clientInfo": {
      "name": "otto-gateway",
      "version": "<embedded -ldflags version>"
    },
    "clientCapabilities": {
      "fs": {
        "readTextFile": true,
        "writeTextFile": true
      },
      "terminal": true
    }
  }
}
```

**Differences vs Phase 1 Go code:**

- `params.protocolVersion: 1` — REQUIRED by ACP spec; currently missing
  from `internal/acp/client.go` `initializeParams` struct.
- `params.clientCapabilities` (not `params.capabilities`) — currently
  `internal/acp/client.go` sends `capabilities: {}`. Wrong field name.
- Phase 1 has empty `clientCapabilities` — that's likely fine for
  Phase 2 since the gateway doesn't (yet) implement `fs/*` or `terminal/*`
  request handlers. Setting them to `false` (or omitting) tells kiro it
  cannot ask us to read/write files or open terminals. For Phase 2 we
  can omit; the Node impl declares `true` because it relays those calls.

### Agent → Client (response)

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "protocolVersion": 1,
    "agentInfo": { "name": "...", "version": "..." },
    "agentCapabilities": {
      "loadSession": true,
      "promptCapabilities": {
        "image": true,
        "audio": true,
        "embeddedContext": true
      },
      "mcpCapabilities": { "http": true, "sse": true }
    },
    "authMethods": []
  }
}
```

**Phase 2 should capture** `agentCapabilities.promptCapabilities` and gate
image/audio block construction on it (similar to Node's
`this.promptCapabilities = caps`).

## 2. session/new

### Client → Agent (request)

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "session/new",
  "params": {
    "cwd": "/path/to/working/dir",
    "mcpServers": []
  }
}
```

**Differences vs Phase 1 Go code:**

- `params.mcpServers: []` — currently missing from `sessionNewParams`.
  kiro-cli may tolerate it missing; Node sends it defensively.

### Agent → Client (response)

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "sessionId": "<or 'id'>",
    "models": {
      "availableModels": [
        { "modelId": "claude-sonnet-4-7", "name": "Claude Sonnet 4.7" }
      ],
      "currentModelId": "auto"
    }
  }
}
```

**Differences vs Phase 1 Go code:**

- Result field may be `sessionId` or `id` (Node falls back: `result.sessionId ?? result.id`).
  `internal/acp/client.go` only reads `sessionId`.
- The available models list is **nested under `result.models.availableModels`**,
  not at the top level. Each entry is an OBJECT with at least `modelId`;
  Node extracts just the `modelId` string. The gateway's `/api/tags` and
  `/api/show` endpoints will draw from this list.
- `currentModelId` defaults to `"auto"` in the Node code's interpretation.

## 3. session/set_model

### Client → Agent (request)

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "session/set_model",
  "params": {
    "sessionId": "...",
    "modelId": "claude-sonnet-4-7"
  }
}
```

**Skip if `modelId === "auto"` or matches the current model.** Matches
Phase 1's existing `SetModel` shape — this one is correct.

## 4. session/prompt

### Client → Agent (request)

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "session/prompt",
  "params": {
    "sessionId": "...",
    "prompt": [ /* ContentBlock[] */ ],
    "content": [ /* same blocks, sent defensively */ ]
  }
}
```

**CRITICAL — Differences vs Phase 1 Go code:**

- Field name is **`prompt`** (per ACP spec) — the Node version also sends
  `content` defensively for backward compat. Phase 1 sends a field named
  `blocks`, which kiro-cli does not read. **Empty prompts on every call.**
- The block array uses the spec's ContentBlock shape (see §6 below), not
  the `{type, content, uri, title}` flat shape Phase 1 currently encodes.

### Agent → Client (response, after all session/update notifications)

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "stopReason": "end_turn"
  }
}
```

Possible `stopReason` values per ACP spec:
`end_turn | max_tokens | max_turn_requests | refusal | cancelled`

**Phase 1 Go code does not read `stopReason`** — engine should surface it
through `canonical.ChatResponse.StopReason` (the StopReason enum we
designed during discuss).

## 5. session/cancel

### Client → Agent (notification)

```json
{
  "jsonrpc": "2.0",
  "method": "session/cancel",
  "params": { "sessionId": "..." }
}
```

Notification (no `id`). The agent must respond to the in-flight
`session/prompt` with `result.stopReason: "cancelled"`. Phase 1's
`Cancel` method matches this shape — correct.

## 6. ContentBlock shapes (used in session/prompt input AND session/update content)

### text

```json
{ "type": "text", "text": "...", "annotations": { ... } }
```

**Phase 1 wireBlock uses `content` field — wrong.** Must be `text`.

### image

```json
{
  "type": "image",
  "mimeType": "image/png",
  "data": "<base64>",
  "uri": "...optional...",
  "annotations": { ... }
}
```

Requires the agent's `promptCapabilities.image: true` (captured from
initialize response). Phase 2 builds image blocks from Ollama's
`messages[].images: [b64, ...]` array.

### audio

```json
{
  "type": "audio",
  "mimeType": "audio/wav",
  "data": "<base64>",
  "annotations": { ... }
}
```

Requires `promptCapabilities.audio: true`. Not needed for Phase 2.

### resource

```json
{
  "type": "resource",
  "resource": {
    "uri": "file:///...",
    "text": "...content...",
    "mimeType": "text/plain"
  },
  "annotations": { ... }
}
```

Or with `"blob": "<base64>"` instead of `"text"`. Requires
`promptCapabilities.embeddedContext: true`. Not needed for Phase 2.

### resource_link

```json
{
  "type": "resource_link",
  "uri": "file:///path/to/file",
  "name": "filename.ext",
  "mimeType": "text/plain",
  "title": "...",
  "description": "...",
  "size": 12345,
  "annotations": { ... }
}
```

**`name` is REQUIRED per spec** — Phase 1 wireBlock omits it. Phase 2
must populate it (e.g., from `path.Base(uri)`). Used for the per-request
`cwd` derivation (longest common parent).

## 7. session/update notifications (Agent → Client)

The agent emits multiple `session/update` notifications during a prompt
turn, then sends the response to the original `session/prompt` request
with the final `stopReason`.

**Method name:** Accept ALL of:

- `session/update` (ACP spec)
- `session/notification` (alt per Kiro docs)
- `_kiro.dev/session/update` (Kiro extension)

**Params body location:** `msg.params.update ?? msg.params`. Some
versions wrap the variant in `params.update`; others put it flat in
`params`.

**Discriminator field:** `body.sessionUpdate ?? body.type`. The ACP spec
field is `sessionUpdate`; older code may use `type`.

### agent_message_chunk

```json
{
  "method": "session/update",
  "params": {
    "sessionId": "...",
    "update": {
      "sessionUpdate": "agent_message_chunk",
      "content": { "type": "text", "text": "Hello, " }
    }
  }
}
```

**Content extraction:** `body.content?.text ?? body.content ?? body.text`.
Some versions emit `{content: {type, text}}` (block-shaped); others emit
`{content: "string"}`; others emit `{text: "string"}`.

**Maps to canonical:** `ChunkKindText` with `TextChunk.Content`.

CamelCase alias: `AgentMessageChunk` (accept defensively).

### agent_thought_chunk

Same shape as `agent_message_chunk`; maps to `ChunkKindThought` with
`ThoughtChunk.Content`. CamelCase alias: `AgentThoughtChunk`.

### tool_call / tool_call_chunk

```json
{
  "method": "session/update",
  "params": {
    "sessionId": "...",
    "update": {
      "sessionUpdate": "tool_call",
      "toolCallId": "call_xyz",
      "title": "read_file",
      "args": { "path": "/foo/bar" }
    }
  }
}
```

Or with `content.text` carrying serialized args. The Node version renders
this as a `thought`-kind chunk with text `[tool: <title>]\n` — it doesn't
emit a real `tool_call` canonical chunk in this branch. Phase 6 will
properly extract `toolCallId`/`title`/`args` and emit
`canonical.ToolCallChunk`.

### tool_call_update

```json
{
  "method": "session/update",
  "params": {
    "sessionId": "...",
    "update": {
      "sessionUpdate": "tool_call_update",
      "toolCallId": "call_xyz",
      "output": "...result text...",
      "content": { "text": "...alt result..." }
    }
  }
}
```

Carries progress/output from a running tool. Node renders the
`output ?? content?.text` as a `thought`-kind chunk.

### plan

```json
{
  "method": "session/update",
  "params": {
    "sessionId": "...",
    "update": {
      "sessionUpdate": "plan",
      "entries": [
        { "content": "Step 1: ..." },
        { "content": "Step 2: ..." }
      ]
    }
  }
}
```

Maps to `canonical.PlanChunk` with `Content` joined from
`entries[].content` (or entries themselves if they're strings).

## 8. session/request_permission (Agent → Client)

**This is a REQUEST (has `id`), not a notification.** The agent expects a
RESPONSE on the same id, not a separate method call.

### Agent → Client (request)

```json
{
  "jsonrpc": "2.0",
  "id": 17,
  "method": "session/request_permission",
  "params": { "requestId": "..." /* + other fields */ }
}
```

### Client → Agent (response — auto-grant)

```json
{
  "jsonrpc": "2.0",
  "id": 17,
  "result": { "optionId": "allow_always", "granted": true }
}
```

**CRITICAL — Phase 1 Go code is wrong here.** Phase 1's
`handleNotification` for `session/request_permission` sends a NEW request
with method `session/grant_permission` and a NEW `id`. kiro-cli is
waiting for a response to the original id and **blocks forever**. This
will deadlock Phase 2's `/api/chat` on the first tool-using prompt.

The fix is to respond to the original id with the result envelope
(same wire shape Node uses on line 218 of `acp-ollama-server.js`).

## 9. ping

Matches Phase 1's existing shape — no params, no result body. Sometimes
kiro-cli returns "method not found" (-32601); treat as success.

## Summary of Phase 1 wire-shape defects to fix in Phase 2 Wave 0

1. `initialize` — add `protocolVersion: 1`, rename `capabilities` →
   `clientCapabilities`, optionally declare empty/false `fs` + `terminal`.
2. `session/new` — add `mcpServers: []`. Read `result.id` as fallback for
   `result.sessionId`. Surface `result.models.availableModels[].modelId`
   list to the engine (for `/api/tags` and `/api/show`).
3. `session/prompt` — rename param `blocks` → `prompt` (and add `content`
   alias for defensive compat).
4. Block wire shape — text uses field `text` not `content`; resource_link
   adds required `name`; add `image` block construction (Ollama
   `messages[].images: []`).
5. Notification method handling — also accept `session/notification`.
6. Notification body parsing — unwrap `params.update` and read
   `sessionUpdate` discriminator (with `type` fallback).
7. Notification type vocabulary — handle `agent_message_chunk`,
   `agent_thought_chunk`, `tool_call`, `tool_call_chunk`,
   `tool_call_update`, `plan` (with CamelCase aliases).
8. Notification content extraction — `body.content?.text ?? body.content
   ?? body.text` fallback chain.
9. `session/request_permission` — RESPOND to the same id with
   `{result:{optionId,granted}}`, do NOT send a separate
   `session/grant_permission` request.
10. `session/prompt` response — read `result.stopReason` and surface it
    on `canonical.ChatResponse.StopReason`.

## Verification gate for Phase 2

The Phase 2 plan must include an integration test
(`internal/acp/integration_test.go`) that:

1. Skips if `kiro-cli` is not on PATH (mirrors Phase 1 D-17).
2. Spawns real `kiro-cli acp`.
3. Executes `Initialize → NewSession → Prompt("hi") → drains
   stream.Chunks → reads stopReason`.
4. Asserts at least one `ChunkKindText` chunk with non-empty content
   arrived.
5. Asserts `stopReason == StopEndTurn` (or another non-error reason).

This is the test Phase 1's smoke test stopped short of. Phase 2 cannot
ship without it passing.
