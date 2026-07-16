# HANDOFF — Gateway tool-call surfacing (alias-primary) + kiro persona bleed

**Paste this whole file into a fresh session to continue.** It is self-contained.

You are in **`otto-gateway`** (`/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/`),
a Go service that fronts real `kiro-cli` over ACP and exposes OpenAI-, Ollama-,
and Anthropic-compatible HTTP surfaces. Work happens on branch
**`quick/gateway-toolcall-surfacing`** (a GSD quick task, dir
`.planning/quick/260716-bv0-fix-gateway-tool-call-surfacing-kiro-per/` — see
`PLAN.md` + `SUMMARY.md` there). **Do NOT push** — `origin` dual-pushes to GitHub
*and* an Ericsson GitLab; keep everything local. Original spec:
`docs/2026-07-16-gateway-toolcall-surfacing-fix-prompt.md`.

## The two defects being fixed

1. **Tool calls leaked as `[tool: <name>]` text** instead of structured
   `tool_calls`. FIXED and then REDESIGNED (see below).
2. **kiro persona bleed** — model self-identified as "Kiro CLI"/AWS and refused
   host tasks. FIXED via a brand-neutral `identityGuardClause` always composed
   into the `[System]` section in `internal/engine/build_acp.go` (commit
   `116df0a`). Done.

## The KEY insight (grounded in a real kiro-cli capture — do not re-litigate)

I captured one real turn (offered a `run_shell` tool, prompted "run
python3 -c print(2+2)", `ACP_CAPTURE=true`). Findings:

- kiro emits a **native ACP `tool_call`** for its OWN built-in shell tool:
  `kind:"execute"`, `_meta.kiro.toolName:"shell"`, `rawInput:{"command":"..."}`.
  translate.go maps `kind` → `ToolCallChunk.Name` = `"execute"`; args from
  `rawInput`. It emits a `tool_call_chunk` (no args) then a `tool_call` (args),
  and RETRIES with a fresh id after each denial.
- The gateway **denies** kiro's built-in tools whenever the caller offered tools
  (`internal/acp/client.go` ~1230, engaged by `engine.go:262`
  `acp.WithDenyBuiltinTools(ctx, len(req.Tools) > 0)`). Deny works.
- kiro's arg schema (`{command}`) **matches** the host's `run_shell` schema.
- The first "structured surfacing" fix (commits `019c7e4`/`1f4fcd3`/`e6334d2`)
  surfaced the native `execute` name **verbatim** → host gets a call to a tool
  it never offered AND (idempotency guard) the correct coerced `run_shell` got
  suppressed. That would FAIL the `toolcall-exec` parity check.

**Conclusion → the alias-primary redesign (commit `39d5320`, IMPLEMENTED & unit-tested):**
Resolve the native tool name against offered tools + a config alias map; surface
under the offered name; drop unaliased built-ins; dedup kiro's chunk+full+retry
frames.

## What is DONE (commit 39d5320, 20 unit packages green, build+cross-compile clean)

- **Config `KIRO_TOOL_ALIASES`** (`internal/config/config.go`): comma-separated
  `from:to` pairs, e.g. `execute:run_shell,fs_read:read_file`. Empty default
  (aliases are deployment-specific). `parseToolAliases` + `Config.ToolAliases`.
  Tests: `internal/config/tool_aliases_test.go`.
- **Shared primitives** (`internal/engine/toolcall_resolve.go` + `_test.go`):
  - `ResolveNativeToolName(name, tools, aliases) (resolved, surface bool)`:
    no tools → surface as-is; name/alias matches an offered tool → surface
    resolved; else drop.
  - `DedupToolCalls(calls)`: merge by id (chunk+full), collapse identical
    (name,args) denial-retries.
- **Wired into every surface** (accumulate resolved native calls, dedup, clear
  deny-regime prose):
  - `internal/engine/collect.go` (OpenAI+Ollama non-stream).
  - `internal/adapter/openai/sse.go` (streaming: accumulate → emit deduped
    `delta.tool_calls` at end via `emitToolCallFrames`; `sawKiroNativeToolCall`
    only set when a call SURFACES so coerce still runs on drops).
  - `internal/adapter/ollama/ndjson.go` (streaming: accumulate → done:true line).
  - `internal/adapter/anthropic/collect.go` `CollectAnthropicChat` (non-stream).
- **`ToolAliases` threaded** through `engine.Config` + all three adapter
  `Config`s + `cmd/otto-gateway/main.go`.
- **Item 2 (encrypt-mode replay):** `runSyntheticSSEFromResponse` (OpenAI) now
  emits tool_calls + `finish_reason:"tool_calls"`. Ollama's synthetic replay
  already carried them via `chatResponseToWire`.

## What REMAINS (do these next, in order)

### 1. Real-kiro re-capture (HIGHEST VALUE — proves the redesign end-to-end)
Real `kiro-cli` is on PATH (`/Users/coreyellis/.local/bin/kiro-cli`, v2.12.1),
already authenticated. Confirm the gateway now emits a **structured `run_shell`**
(not `execute`, not `[tool:` text). Run (uses a tiny real model call — ask the
user's OK first; it costs a few credits):

```bash
cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway
go build -o /tmp/otto-gw ./cmd/otto-gateway/
cat > /tmp/toolreq.json <<'JSON'
{"model":"auto","stream":false,
 "messages":[{"role":"user","content":"Use the run_shell tool to run this exact command and tell me the result: python3 -c \"print(2+2)\". You must call the run_shell tool; do not answer from memory."}],
 "tools":[{"type":"function","function":{"name":"run_shell","description":"Run a shell command on the host and return its stdout.","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}]}
JSON
PORT=18081
PII_REDACTION_MODE=replace ACP_CAPTURE=true POOL_SIZE=1 \
  KIRO_TOOL_ALIASES="execute:run_shell,shell:run_shell" \
  HTTP_ADDR=127.0.0.1:$PORT AUTH_TOKEN=cap-token KIRO_CMD=kiro-cli \
  /tmp/otto-gw > /tmp/gw.log 2>&1 &
GWPID=$!
curl -sS --retry 40 --retry-connrefused --retry-delay 1 -o /dev/null -w "%{http_code}\n" http://127.0.0.1:$PORT/health
curl -sS --max-time 150 -X POST http://127.0.0.1:$PORT/v1/chat/completions \
  -H "Authorization: Bearer cap-token" -H "Content-Type: application/json" -d @/tmp/toolreq.json
kill $GWPID 2>/dev/null
```
**PASS** = response has `message.tool_calls:[{...function:{name:"run_shell",
arguments:"{\"command\":\"python3 -c \\\"print(2+2)\\\"\"}"}}]` +
`finish_reason:"tool_calls"`, ONE call, NO `[tool:` and NO `execute` in output.
Also re-run WITHOUT `KIRO_TOOL_ALIASES` to confirm graceful fallback (native
dropped → coerce extracts `run_shell` from kiro's wrapper text, flaky).

### 2. e2e tests (`tests/e2e/`, build tag `e2e`, `GW_E2E=1`)
- The e2e tool matrix boots the REAL gateway binary + a **fake-kiro**
  (`tests/e2e/cmd/fake-kiro-cli`). It does NOT do the permission/deny dance.
- The existing `tests/e2e/tools_{openai,ollama,anthropic}_test.go` and
  `tools_fixtures_test.go` were updated (commit `fd9c8ce`) to assert structured
  tool_calls, feeding native `get_weather` WITH `get_weather` offered (direct
  match → surfaces). Those still pass.
- ADD a scenario proving the alias path: fake-kiro emits native `execute` (args
  `{command}`) + boot with `KIRO_TOOL_ALIASES=execute:run_shell` (bootGateway
  merges env) + offer `run_shell` → assert structured `run_shell`. Also a
  drop case: native `fs_write`, no alias → assert NO tool_calls.
- **Run e2e with:** `PII_REDACTION_MODE=replace GW_E2E=1 go test -tags e2e -count=1 -run 'TestE2E_Tools' ./tests/e2e/`
  (the gateway's DEFAULT `PII_REDACTION_MODE` is `encrypt`, which needs a key at
  boot — always set `PII_REDACTION_MODE=replace` for e2e/manual runs).

### 3. Docs + SUMMARY + STATE
- Document `KIRO_TOOL_ALIASES` in the admin docs page
  (`internal/admin/docs.html.tmpl` + the `DocsData`/`EnvVarRow` builder in
  `internal/admin/*` — grep `STREAM_IDLE_TIMEOUT` for the pattern) and add it to
  any env reference. There is an admin docs env-var table + a
  `TestDocs_...`-style sentinel; mirror `STREAM_IDLE_TIMEOUT_SEC` wiring.
- Append to `SUMMARY.md` the alias-primary redesign; update the
  `260716-bv0` row in `.planning/STATE.md` "Quick Tasks Completed".

### 4. Follow-up (flagged in commit 39d5320 — decide with user)
The **Anthropic *streaming* SSE** (`internal/adapter/anthropic/sse.go`, a
content-block state machine) still surfaces native `tool_use` **as-is** (its
pre-existing behavior — NOT a regression). To make it consistent, apply
`engine.ResolveNativeToolName` at the block-open (~line 453 `toolUseBlockHeader`,
gate the block if `!surface`) + at `aggToolCalls` (~line 613), threading
`aliases` onto the emitter (it already has `tools []canonical.ToolSpec`; add
`aliases`, thread from `a.cfg.ToolAliases` via `runSSEEmitter` ~line 962). Add a
per-turn dedup guard for denial-retries. This was deferred for context budget +
state-machine risk; test hard against real kiro if you do it.

## Gotchas / conventions
- **Never push** (dual remote). Commit locally only.
- **Default `PII_REDACTION_MODE=encrypt`** requires `PII_ENCRYPT_KEY` at boot →
  always pass `PII_REDACTION_MODE=replace` for manual/e2e gateway runs.
- Commit messages end with the two trailer lines
  (`Co-Authored-By: Claude Opus 4.8 ...` + `Claude-Session: ...`).
- kiro/Claude mis-parses `<...>` markers as XML — the persona guard + prompts
  avoid angle brackets (memory: `feedback-pii-bracket-shape`).
- Verify each phase: `gofmt -l internal/ cmd/`, `go vet ./...`,
  `go test ./...` (20 pkgs), then e2e as above.
- The engine sets deny-builtin-tools whenever `len(req.Tools) > 0`. The
  alias-primary path surfaces the native call for the host to execute; kiro's
  local execution stays denied (host executes; result round-trips next turn via
  build_acp.go `[Tool result]` sections).

## Quick resume checklist
1. `git -C <repo> log --oneline main..HEAD` → confirm HEAD is `39d5320` (or
   later) on `quick/gateway-toolcall-surfacing`.
2. `go test ./...` → 20 ok.
3. Do REMAINING #1 (real-kiro re-capture) — ask user before the model call.
4. Then #2 (e2e), #3 (docs), and offer #4 (Anthropic streaming) as a decision.
