# Phase 5 SC3 Wire-Protocol Diagnostic — Plan 05-04

**Date:** 2026-05-26
**Kiro-cli version:** 2.4.1 (`/Users/coreyellis/.local/bin/kiro-cli`)
**Gateway commit:** 0e8d8e9 (worktree `agent-a36887bded4cd971a`)
**Capture tool:** `tools/kiro-shim/main.go` (built `/tmp/kiro-shim`)
**Trace files:**
- `05-04-WIRE-POOL.jsonl` — merged IN+OUT of pool slot-0 (shim PID 75003, kiro-cli PID 75004) handling one stateless `/api/chat`
- `05-04-WIRE-SESSION.jsonl` — merged IN+OUT of registry kiro-cli (shim PID 75516, kiro-cli PID 75517) handling one stateful `/api/chat -H 'X-Session-Id: smoke-broken'`

Both transcripts captured against the SAME kiro-cli binary, in the SAME gateway boot, under `POOL_SIZE=2` with the shim transparently teeing JSON-RPC frames.

---

## Working Pool Path

Stateless `curl POST /api/chat -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'` against pool slot-0 (shim PID 75003) returned HTTP 200 with `{"message":{"content":"Hi! How can I help you today?"}}`.

Full transcript: see `05-04-WIRE-POOL.jsonl`. Key frames (timestamps and tool/command catalogues trimmed for readability):

```
# WARMUP (boot time)
05-04-WIRE-POOL.jsonl:1  OUT  initialize             {protocolVersion:1, clientInfo, clientCapabilities}
05-04-WIRE-POOL.jsonl:2  IN   initialize.result      {agentCapabilities:{loadSession:true, promptCapabilities:{image:true,...}}}
05-04-WIRE-POOL.jsonl:3  OUT  session/new            {cwd:"", mcpServers:[]}                                ← warmup catalog session
05-04-WIRE-POOL.jsonl:4  IN   session/new.result     {sessionId:"9c23864a-...", modes:{...}, models:{...}}
05-04-WIRE-POOL.jsonl:6  OUT  session/cancel         {sessionId:"9c23864a-..."}                              ← Cancel warmup session

# STATELESS REQUEST (per-request fresh session)
05-04-WIRE-POOL.jsonl:9  OUT  session/new            {cwd:"/Users/coreyellis/.../agent-a36887bded4cd971a", mcpServers:[]}
05-04-WIRE-POOL.jsonl:10 IN   session/new.result     {sessionId:"78de878c-...", ...}
05-04-WIRE-POOL.jsonl:11 OUT  session/prompt         {sessionId:"78de878c-...", prompt:[{type:"text", text:"[User]\nhi"}], content:[same]}
05-04-WIRE-POOL.jsonl:14 IN   session/update         {update:{sessionUpdate:"agent_message_chunk", content:{type:"text", text:"Hi!"}}}
05-04-WIRE-POOL.jsonl:16 IN   session/update         (more chunks: " How can I", " help you today?")
05-04-WIRE-POOL.jsonl:21 IN   session/prompt.result  {stopReason:"end_turn"}                                  ← HTTP 200
```

**Critical observation:** the per-request `session/new` at line 9 carries a **non-empty `cwd`** (the worktree absolute path returned by `os.Getwd()` via `engine.pickCwd` → `Pool.NewSession(ctx, cwd)`). The session id `78de878c-...` returned by this call is the SAME id sent on the subsequent `session/prompt` at line 11, and kiro-cli processes the prompt successfully.

The warmup session at line 3 IS created with `cwd:""` — but it is immediately cancelled at line 6 and never receives a `session/prompt`, so its empty-cwd state is never tested at the prompt boundary.

---

## Broken Session Path

Stateful `curl POST /api/chat -H 'X-Session-Id: smoke-broken' -d '...same body...'` against the registry-spawned kiro-cli (shim PID 75516) returned HTTP 500 with:

```
{"error":"ollama engine collect: engine: collect: engine: prompt: session: prompt: acp: prompt: rpc error -32603: Internal error"}
```

Full transcript: see `05-04-WIRE-SESSION.jsonl`. Key frames:

```
# REGISTRY ENTRY CREATION (Registry.createEntry → Client.NewSession)
05-04-WIRE-SESSION.jsonl:1  OUT  initialize          {protocolVersion:1, ...}
05-04-WIRE-SESSION.jsonl:2  IN   initialize.result   {...}
05-04-WIRE-SESSION.jsonl:3  OUT  session/new         {cwd:"", mcpServers:[]}                                  ← EMPTY cwd!
05-04-WIRE-SESSION.jsonl:5  IN   session/new.result  {sessionId:"4163412f-...", modes:{...}, models:{...}}    ← kiro accepts it

# STATEFUL REQUEST PROMPT (Entry.Prompt → Client.Prompt with cached sid)
05-04-WIRE-SESSION.jsonl:7  OUT  session/prompt      {sessionId:"4163412f-...", prompt:[{type:"text", text:"[User]\nhi"}], content:[same]}
05-04-WIRE-SESSION.jsonl:10 IN   session/prompt.ERR  {code:-32603, message:"Internal error",
                                                       data:"Encountered an error in the response stream: Improperly formed request. (request_id: ce56a9e5-...)"}
05-04-WIRE-SESSION.jsonl:11 OUT  session/cancel      {sessionId:"4163412f-..."}                               ← engine.Run watchdog cleanup
```

**Critical observation:** the registry's `session/new` at line 3 carries `cwd:""`. kiro-cli accepts the request and returns a session id (line 5), but the subsequent `session/prompt` against that empty-cwd session id is rejected with JSON-RPC code -32603 and error data `"Improperly formed request."`.

---

## Rejected Hypotheses

The verification report (05-VERIFICATION.md gap 1) catalogued four candidate hypotheses (H-A through H-D). The wire transcripts rule out three of them as the root cause; the fourth (H-B) is the one a confirmatory experiment confirms.

### H-A (kiro-cli rejects reuse of a cached session id across multiple session/prompt calls)

**Rejected.** The broken session path's transcript shows only ONE `session/prompt` against the cached sid at `05-04-WIRE-SESSION.jsonl:7` — and it fails. No re-use ever happens before the failure, so reuse-across-prompts cannot be the cause.

Cross-check from the confirmatory experiment below: when KIRO_CWD is non-empty, two sequential prompts against the SAME cached sid on the registry-owned kiro-cli succeed (turn 2 referenced turn 1's content). Reuse is observably fine when cwd is non-empty. H-A is false.

### H-B (cwd handshake differs)

**NOT rejected — migrated to `## Confirmatory Experiment` and `## Root Cause` below.** The diff shows the pool path issues `session/new` with `cwd:"/Users/coreyellis/.../agent-a36887bded4cd971a"` (`05-04-WIRE-POOL.jsonl:9`) while the session path issues `session/new` with `cwd:""` (`05-04-WIRE-SESSION.jsonl:3`). Confirmatory experiment proves this is the load-bearing difference.

### H-C (implicit SetModel sequence)

**Rejected.** Neither transcript contains a `session/set_model` frame. The model is `auto`, so per `engine.Run` (engine.go:177-178: `if req.Model != "" && req.Model != "auto"`), `SetModel` is never invoked. The two paths are IDENTICAL in their SetModel behaviour for this prompt (both omit it), so a SetModel sequencing difference cannot explain why one fails and the other succeeds.

Evidence: `grep -c '"method":"session/set_model"' 05-04-WIRE-POOL.jsonl` returns 0; same for `05-04-WIRE-SESSION.jsonl`.

### H-D (prompt block construction differs)

**Rejected.** Both paths emit byte-identical `session/prompt.params.prompt` and `session/prompt.params.content` block lists:

- `05-04-WIRE-POOL.jsonl:11  →  "prompt":[{"type":"text","text":"[User]\nhi"}], "content":[{"type":"text","text":"[User]\nhi"}]`
- `05-04-WIRE-SESSION.jsonl:7 →  "prompt":[{"type":"text","text":"[User]\nhi"}], "content":[{"type":"text","text":"[User]\nhi"}]`

The block flattening (`internal/engine/build_acp.go::buildBlocks`) is invoked by `engine.Run` for both paths (engine.go:168) and produces identical canonical output for identical request bodies. The D-13 dual-field shim (`Prompt` AND `Content`) is also identical. H-D cannot explain why the prompt is rejected.

---

## Confirmatory Experiment

**Hypothesis under test:** H-B — kiro-cli's `session/prompt` rejects sessions created via `session/new` with an empty `cwd`.

**Experiment 1 — Baseline reproduction (KIRO_CWD unset):**

```bash
HTTP_ADDR=127.0.0.1:15411 AUTH_TOKEN=e2e-token POOL_SIZE=2 \
  KIRO_CMD=/tmp/kiro-shim KIRO_ARGS="$(which kiro-cli) acp" \
  bin/otto-gateway &
sleep 5
curl -sS -w '\nHTTP_STATUS:%{http_code}\n' -X POST \
  -H 'Authorization: Bearer e2e-token' -H 'X-Session-Id: smoke-broken' \
  -H 'Content-Type: application/json' \
  http://127.0.0.1:15411/api/chat \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

Observed:
```
{"error":"... acp: prompt: rpc error -32603: Internal error"}
HTTP_STATUS:500
```

This produced the `05-04-WIRE-SESSION.jsonl` transcript captured above.

**Experiment 2 — Single-variable change (KIRO_CWD=/tmp):**

Killed the gateway. Restarted with ONE variable changed — `KIRO_CWD=/tmp` (everything else identical):

```bash
HTTP_ADDR=127.0.0.1:15412 AUTH_TOKEN=e2e-token POOL_SIZE=2 KIRO_CWD=/tmp \
  KIRO_CMD=/tmp/kiro-shim KIRO_ARGS="$(which kiro-cli) acp" \
  bin/otto-gateway &
sleep 5
curl -sS -w '\nHTTP_STATUS:%{http_code}\n' -X POST \
  -H 'Authorization: Bearer e2e-token' -H 'X-Session-Id: cwd-test-1' \
  -H 'Content-Type: application/json' \
  http://127.0.0.1:15412/api/chat \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

Observed:
```
{"model":"auto","created_at":"2026-05-26T15:49:38.114909Z","message":{"role":"assistant","content":"Hi! How can I help you today?"},"done":true,...}
HTTP_STATUS:200
```

The shim trace for the registry kiro-cli (shim PID 77467) shows the change is exactly the cwd field on `session/new`:

```
OUT  session/new   {cwd:"/tmp", mcpServers:[]}
IN   session/new.result   {sessionId:"...", ...}
OUT  session/prompt {sessionId:"...", prompt:[...], content:[...]}
IN   session/update (chunks ... )
IN   session/prompt.result {stopReason:"end_turn"}
```

No more -32603. No other source-level change. The single mutation that flipped failure → success was the value of the `cwd` field on `session/new`.

**Experiment 3 — Continuity check (two prompts on the same cached sid):**

Two sequential requests with the SAME `X-Session-Id: cwd-test-2` on the same gateway boot:

```
turn 1: "Remember the number 7." → 200, body: "Got it — I'll remember the number 7."
turn 2: "What number did I tell you to remember?" → 200, body: "7."
```

The registry kiro-cli (shim PID 77839) shows TWO `session/prompt` requests sharing the SAME cached sid — both succeed, and the second response semantically references the first. This rules out H-A as the cause: the cached SessionID strategy is fine; what was broken was the cwd handshake at session creation.

**Conclusion:** Hypothesis **H-B is confirmed** as the root cause. The session path's `session/new` is invoked with an empty `cwd` (because `resolveEngine` calls `Registry.Get(r.Context(), sid, a.cfg.KiroCWD)` and `KIRO_CWD` defaults to ""), while the pool path's `session/new` is invoked with `pickCwd(req, DefaultCWD)` — which falls back to `os.Getwd()` when `DefaultCWD` is empty. kiro-cli's `session/prompt` rejects sessions that were created with an empty `cwd`. Patching the session path to derive its `cwd` the same way the pool path does (via `pickCwd` per request, with `os.Getwd()` fallback) eliminates the -32603 error.

---

## Root Cause

The session path passes an empty `cwd` to kiro-cli's `session/new` because `internal/adapter/ollama/handlers.go:117` calls `a.cfg.Registry.Get(r.Context(), sid, a.cfg.KiroCWD)` and `cfg.KiroCWD` is the literal env-var value (`""` when `KIRO_CWD` is unset) — there is no `pickCwd` / `os.Getwd()` fallback on the registry path. Inside `internal/session/registry.go:252`, that empty string is then forwarded verbatim to `client.NewSession(ctx, cwd)`. kiro-cli accepts the `session/new` (it returns a session id) but its subsequent `session/prompt` against that session rejects every request with JSON-RPC code -32603 and `error.data="Improperly formed request."` (see `05-04-WIRE-SESSION.jsonl:10`).

The pool path is unaffected because it routes through `engine.Run` (`internal/engine/engine.go:165`), which calls `pickCwd(req, e.cfg.DefaultCWD)` — that helper falls through to `os.Getwd()` when no resource_link, X-Working-Dir header, or DefaultCWD is set. The pool then issues `session/new` with the resolved non-empty cwd (`05-04-WIRE-POOL.jsonl:9`), and kiro-cli's `session/prompt` accepts the resulting session id.

`internal/session/registry.go:252` is the divergence: the registry should never see a literal `cwd:""`; it must either receive a resolved cwd from the caller OR fall through to `os.Getwd()` itself before issuing `session/new`. Confirmed by the `## Confirmatory Experiment` above — setting `KIRO_CWD=/tmp` (any non-empty value) makes the stateful path return HTTP 200 and proves two-turn continuity.

---

## Remediation Plan

The fix is **local to the session creation site**, not to `Entry.NewSession` (which is the engine-facing accessor, irrelevant to the underlying kiro-cli `session/new` RPC). Two files change:

| File | Change | Rationale |
|------|--------|-----------|
| `internal/session/registry.go::createEntry` | Resolve cwd to a non-empty value before calling `client.NewSession`. If the caller-supplied `cwd` is empty, fall back to `os.Getwd()` (matching `engine.pickCwd`'s final fallback). On `os.Getwd()` error, return a wrapped error so failure surfaces. | The registry owns the `session/new` call — it is the right boundary for the cwd guarantee. Mirroring `pickCwd`'s `os.Getwd()` fallback keeps the two paths semantically equivalent without inverting the dependency (registry would otherwise need to import `internal/engine`, which is currently forbidden by arch-lint). |
| `internal/session/entry_acp.go::NewSession` | UNCHANGED. The cached-SessionID accessor stays — H-A is rejected; the same kiro-cli session id is fine across multiple prompts (Experiment 3 proves two-turn continuity on the same cached sid). | Avoid scope creep. The diff-skip in SetModel (D-09) also stays for the same reason. |

**Test scaffolding** in `internal/session/entry_acp_test.go`:

- `TestRegistry_CreateEntry_ResolvesEmptyCwdToOSGetwd` (unit, fake-ACP): a Registry constructed with `KiroCWD:""` (config default) AND a `Get(ctx, sid, "")` call MUST cause the fake's `NewSession` to be invoked with a NON-empty cwd (the test-process os.Getwd()). Guards against the regression we just fixed.
- `TestRegistry_CreateEntry_PassesNonEmptyCwdVerbatim`: a Registry passed a non-empty cwd MUST forward that exact value to the fake's NewSession unchanged (no normalization).
- `TestEntry_Prompt_PassesCachedSessionID`: confirms `Entry.Prompt` still forwards the cached `SessionID` (H-A guard: do not over-correct by recreating sessions per prompt).

The test scaffolding GUARDS the chosen protocol sequence; the live e2e suite (Task 6) is the authority for proving the fix against real kiro-cli. The H-A reverse-regression is the most important guard — we would silently break two-turn continuity if a future refactor "fixed" the cached-sid pattern.

**Source comment in `internal/session/registry.go::createEntry`:** add a 5-7 line comment citing `.planning/phases/05-pool-stateful-sessions/05-04-WIRE-DIFF.md` and explaining the `os.Getwd()` fallback as a parity-with-pool measure, so future readers can re-discover the root cause without re-running the shim.

**Source comment in `internal/session/entry_acp.go::NewSession`:** add a one-liner noting H-A was explicitly rejected — `Entry.NewSession` MUST stay an accessor; do not "fix" it to call `Client.NewSession` per request.
