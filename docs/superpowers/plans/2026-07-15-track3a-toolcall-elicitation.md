# Track 3a — kiro tool-call elicitation apparatus — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make kiro emit tool calls through the Go gateway at parity with the JS
gateway: **deny** kiro's built-in-tool `session/request_permission` when the
caller supplied tools, ship a strict function-calling prompt, and add a
`MAX_TOOL_DENIALS` circuit breaker. Coercing the emitted `{"tool_call":…}` JSON
is Track 3b — NOT here.

**Design:** `docs/superpowers/specs/2026-07-15-track3a-toolcall-elicitation-design.md`.
**Code map (read for exact seams/excerpts):** `.superpowers/sdd/track3a-go-map.md`.
**JS reference (parity oracle):** `/Users/coreyellis/code/gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-server-ollama.js` (permission dance `314-338`, strict prompt `794-820`, `MAX_TOOL_DENIALS` `123`, per-turn reset `448`).

## Global Constraints

- Go 1.26.x, **no cgo** (`CGO_ENABLED=0 go build ./cmd/otto-gateway` must pass).
- **Additive/behavior-preserving:** the permission GRANT path for tool-less
  requests must be byte-for-byte unchanged (no regression for plain chat /
  Anthropic-without-tools). Only the new deny branch is net-new.
- gofumpt-clean, `go vet ./...` clean, `make arch-lint` clean, golangci-lint no
  new findings on touched packages.
- Env name is operator contract: `MAX_TOOL_DENIALS` (Node parity).
- Every commit ends with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2
  ```
- Do NOT `git push` (origin dual-pushes a GitLab mirror that hangs). Commit only.

---

### Task 1: enrich `permissionParams` + `pickRejectOption`

**Files:** modify `internal/acp/translate.go`; test `internal/acp/permission_test.go` (whitebox, `package acp`).

**Produces:**
- `permissionOption struct { OptionID, Kind string }` (json `optionId`/`kind`).
- Extended `permissionParams` with `Options []permissionOption` and
  `ToolCall struct{ Title string }` (json `toolCall.title`), keeping `RequestID`.
- `func pickRejectOption(opts []permissionOption) string` — porting JS
  `acp-server-ollama.js:321-322`: return the `OptionID` of the first option
  whose `Kind+" "+OptionID` matches (case-insensitive) `reject.*always` or
  `always.*reject`; else the first matching `reject`; else the literal
  `"reject_always"`.

- [ ] **Step 1 — failing test.** In `permission_test.go`:
  - `TestPickRejectOption`: table — `[{reject_once,reject},{reject_always,reject_always}]`→`reject_always`; `[{allow,allow},{deny,reject}]`→`deny`... (match on kind OR optionId); empty slice → `reject_always`; only grant options → `reject_always` (fallback).
  - `TestPermissionParams_Unmarshal`: unmarshal a frame body `{"requestId":"r1","options":[{"optionId":"reject_always","kind":"reject_always"},{"optionId":"allow_once","kind":"allow"}],"toolCall":{"title":"Read file /etc/hosts"}}` → assert RequestID, len(Options)==2, ToolCall.Title.
- [ ] **Step 2** run `go test ./internal/acp/ -run 'TestPickRejectOption|TestPermissionParams'` → FAIL (undefined / unknown fields).
- [ ] **Step 3** implement in `translate.go`. `pickRejectOption` uses `regexp.MustCompile` package-level vars (compile once) or `strings.Contains` on a lowercased `kind+" "+optionId` — simplest correct: lowercase, check `strings.Contains(s,"reject") && strings.Contains(s,"always")` first pass, then `strings.Contains(s,"reject")` second pass, else `"reject_always"`.
- [ ] **Step 4** `go test -race ./internal/acp/ -run 'TestPickRejectOption|TestPermissionParams'` → PASS.
- [ ] **Step 5** `go test ./internal/acp/` → PASS (no regression). Commit `feat(acp): parse permission options + pickRejectOption (Track 3a)`.

---

### Task 2: per-turn deny state on `Stream` + context helpers + `Prompt` reads ctx

**Files:** modify `internal/acp/stream.go`, `internal/acp/client.go`; create `internal/acp/context.go`; test `internal/acp/deny_context_test.go`, and extend a Prompt test.

**Produces:**
- `internal/acp/context.go`:
  ```go
  package acp
  import "context"
  type denyBuiltinToolsKey struct{}
  // WithDenyBuiltinTools marks ctx so the acp permission handler DENIES kiro's
  // built-in-tool session/request_permission for this turn (Track 3a). Set by
  // engine.Run when the caller supplied tools. Absent/false ⇒ auto-grant.
  func WithDenyBuiltinTools(ctx context.Context, deny bool) context.Context {
      return context.WithValue(ctx, denyBuiltinToolsKey{}, deny)
  }
  func denyBuiltinToolsFromContext(ctx context.Context) bool {
      v, _ := ctx.Value(denyBuiltinToolsKey{}).(bool)
      return v
  }
  ```
- `Stream` gains `denyBuiltinTools bool` + `denialCount int`, mutated under the
  existing `stream.mu`. Add `func (s *Stream) setDenyBuiltinTools(bool)` and
  `func (s *Stream) recordDenial() int` (increments, returns new count) — both
  lock `s.mu`. (Confirm the field name of the Stream's existing mutex in
  stream.go; the map calls it `stream.mu` guarding `result`.)
- In `Client.Prompt` (client.go:983), after `stream := newStream(ctx, sessionID)`
  and BEFORE storing `c.activeStream`, call
  `stream.setDenyBuiltinTools(denyBuiltinToolsFromContext(ctx))`.

- [ ] **Step 1 — failing tests.**
  - `TestWithDenyBuiltinTools_RoundTrip`: `denyBuiltinToolsFromContext(WithDenyBuiltinTools(ctx,true))==true`; bare ctx ⇒ false.
  - `TestPrompt_SetsStreamDenyFromContext` (whitebox): build a Client on a mock conn (reuse `newMockRWC`/`NewWithConn` from client_test.go), call `Prompt(WithDenyBuiltinTools(ctx,true), sid, blocks)`, then read `c.activeStream.denyBuiltinTools` under `c.streamMu`/`stream.mu` → true. (Guard against the send racing; the field is set before the wire send, so it's observable immediately after Prompt returns.)
- [ ] **Step 2** run → FAIL.
- [ ] **Step 3** implement context.go, Stream fields+methods, Prompt wiring.
- [ ] **Step 4** `go test -race ./internal/acp/ -run 'DenyBuiltinTools|SetsStreamDeny'` → PASS.
- [ ] **Step 5** `go test ./internal/acp/` → PASS. Commit `feat(acp): per-turn deny-builtin-tools signal via context + Stream state (Track 3a)`.

---

### Task 3: `MAX_TOOL_DENIALS` config threaded to `acp.Config`

**Files:** `internal/config/config.go` (+ test `internal/config/max_tool_denials_test.go`); `internal/acp/client.go` (`Config.MaxToolDenials`); `internal/pool/config.go`+`pool.go`; `internal/session/config.go`+`registry.go`; `cmd/otto-gateway/main.go`. Tests mirror the `Capture`/`RecyclePct` forwarding tests.

**Produces:**
- `config.Config.MaxToolDenials int` from `getEnvInt("MAX_TOOL_DENIALS", 4)`;
  reject `<= 0` (error message contains `MAX_TOOL_DENIALS`); assign in the
  returned `Config{...}` literal. (Mirror `CTX_RECYCLE_PCT`: config.go:520-536,
  815; test-shape like `ctx_recycle_test.go`.)
- `acp.Config.MaxToolDenials int` (Client-lifetime field; doc-comment it as the
  Track 3a circuit-breaker threshold).
- `pool.Config.MaxToolDenials int` forwarded onto each slot's
  `acp.Config.MaxToolDenials` in `acpSlotConfig` (next to the `Capture`/`Metrics`
  wiring); `session.Config.MaxToolDenials` forwarded in `createEntry`'s
  `acp.Config{...}` literal.
- `cmd/otto-gateway/main.go`: pass `cfg.MaxToolDenials` into both
  `pool.Config{...}` and `session.Config{...}` (next to `Capture`/`Metrics`/`RecyclePct`).

- [ ] **Step 1** config tests: default 4; override; `<=0` rejected (error contains `MAX_TOOL_DENIALS`). Use the PII-neutralizing env prefix when running config tests (`env PII_REDACTION_MODE=mask PII_ENTITY_ACTIONS= PII_ENCRYPT_KEY= PII_REDACTION_ENABLED=false go test ./internal/config/ …`) but DO NOT apply that prefix to `go test ./...`.
- [ ] **Step 2** pool test (`TestAcpSlotConfig_ForwardsMaxToolDenials`, whitebox `pool`): `New(Config{MaxToolDenials:7}).acpSlotConfig().MaxToolDenials == 7`; unset ⇒ 0. Session test (`TestCreateEntry_ForwardsMaxToolDenials`, blackbox, reuse `capturingFactory`/`newFake` from recycle_test.go): `capturedCfg.MaxToolDenials == 7`.
- [ ] **Step 3** run all three → FAIL. Implement each layer. Re-run → PASS.
- [ ] **Step 4** `go test -race ./internal/config/ ./internal/pool/ ./internal/session/` → PASS; `go build ./...`. Commit `feat(config,pool,session,acp): MAX_TOOL_DENIALS threshold plumbing (Track 3a)`.

> Note: `acpSlotConfig`/`createEntry` should forward `MaxToolDenials` even when 0; the permission handler (Task 4) treats `<=0` as "use default 4" defensively so a zero-valued Config (direct pool/session construction in tests) never disables the breaker unexpectedly. Document that fallback in Task 4.

---

### Task 4: permission handler — deny branch + circuit breaker

**Files:** modify `internal/acp/client.go` (the `session/request_permission` case ~1186-1245); test `internal/acp/permission_handler_test.go` (whitebox).

**Behavior (spec D3):** in the case, snapshot the active stream under
`c.streamMu` (existing idiom client.go:1267-1269). Then:
- If `stream != nil && stream.denyBuiltinTools`:
  - parse enriched `permissionParams`; `opt := pickRejectOption(params.Options)`;
  - marshal `rpcResponse{ID: frame.ID, Result: {"optionId": opt, "granted": false}}` and write via the existing `c.framer.writeFrame` path (unchanged);
  - `n := stream.recordDenial()`; Debug-log `params.ToolCall.Title` + `n`/threshold;
  - `max := c.cfg.MaxToolDenials; if max <= 0 { max = 4 }`; if `n >= max`, `c.Cancel(stream.sessionID)`.
- Else: unchanged auto-grant (`allow_always`,`granted:true`).

Keep the no-id guard, the id-echo, the marshal-error guard, and the
write-fail→`c.cancel()` escalation exactly as today.

- [ ] **Step 1 — failing tests** (whitebox; drive `handleNotification` directly with a synthesized `rpcFrame{Method:"session/request_permission", ID: rawIDNum(7), Params: …}` and inspect the bytes written to the mock's server side — reuse the mock's read side, e.g. `mock.serverRead`, to capture the outbound response):
  - `TestPermission_GrantsWhenNoDenyFlag`: active stream with `denyBuiltinTools=false` (or no stream) ⇒ response is `allow_always`/`granted:true` (regression guard).
  - `TestPermission_DeniesWhenDenyFlag`: active stream `denyBuiltinTools=true`, params options include a `reject_always` ⇒ response `{"optionId":"reject_always","granted":false}` and `stream.denialCount==1`.
  - `TestPermission_CircuitBreakerCancels`: set `cfg.MaxToolDenials=2`, feed 2 permission frames ⇒ after the 2nd, a `session/cancel` notification is emitted (assert Cancel fired — observe the outbound `session/cancel` frame or a Cancel spy).
  - Confirm the existing D-20 no-id-drop path still logs+returns.
- [ ] **Step 2** run → FAIL. Implement. Re-run → PASS (`-race`).
- [ ] **Step 3** `go test ./internal/acp/` → PASS. Commit `feat(acp): deny kiro built-in tools + MAX_TOOL_DENIALS breaker (Track 3a)`.

> This is the load-bearing task. The reviewer must confirm the grant path is
> unchanged for the no-deny case, the id is echoed verbatim, and the breaker
> uses per-turn `denialCount` (not Client-global state).

---

### Task 5: strict function-calling prompt (`build_acp.go`)

**Files:** modify `internal/engine/build_acp.go` (the `len(req.Tools) > 0` branch, ~66-113); test `internal/engine/build_acp_test.go` (extend existing).

Replace the bare `[Available tools]\nEmit a tool_call ACP notification…` with the
JS-ported strict prompt (mirror `acp-server-ollama.js:801-818` — adapt wording,
keep the substance): "You are acting as a function-calling model for an EXTERNAL
system that executes the tools below… you must NOT use your own built-in tools…
output a JSON code block EXACTLY `{"tool_call": {"name":"<tool>","arguments":{…}}}`
… no prose outside the blocks…" + the 6 rules + the ` ```json <catalog> ``` `.
Keep the marshal-failure header-only fallback but with the new framing.

- [ ] **Step 1 — failing test** `TestBuildBlocks_StrictToolPrompt`: with a
  request declaring one tool, assert the flattened prompt text contains the key
  parity markers — `"tool_call"`, `"arguments"`, a "do NOT use your … built-in
  tools" instruction, and the tool's name from the catalog; and assert it no
  longer contains the old `"Emit a tool_call ACP notification"` string.
- [ ] **Step 2** run → FAIL. Implement. Re-run → PASS.
- [ ] **Step 3** `go test ./internal/engine/` → PASS. Commit `feat(engine): strict function-calling tool prompt (Track 3a)`.

---

### Task 6: engine wires the deny signal

**Files:** modify `internal/engine/engine.go` (in `Run`, before the `ACP.Prompt`
call ~258); test `internal/engine/engine_test.go` (extend).

Immediately before `stream, err := e.cfg.ACP.Prompt(ctx, sid, blocks)`, add:
```go
ctx = acp.WithDenyBuiltinTools(ctx, len(req.Tools) > 0)
```
(Confirm `internal/engine` already imports `internal/acp` — the adapter does.)

- [ ] **Step 1 — failing test** `TestRun_SetsDenyBuiltinToolsWhenToolsPresent`:
  use a fake `engine.ACPClient` whose `Prompt` captures the ctx it received;
  assert `acp` deny-flag is true when `req.Tools` non-empty and false when empty.
  (This requires the fake to read the ctx via `acp.WithDenyBuiltinTools`'s
  companion reader — expose a tiny test-only accessor OR assert via a package
  `acp` exported test helper. Simplest: add an exported `acp.DenyBuiltinToolsForTest(ctx) bool` OR keep `denyBuiltinToolsFromContext` and place the assertion in a `package acp`-internal helper the engine test can't reach → instead expose `WithDenyBuiltinTools` + an exported reader `DenyBuiltinTools(ctx) bool` from context.go and have the handler use the exported one. Prefer exporting the reader.)
- [ ] **Step 2** run → FAIL. Implement (export the reader from Task 2's
  context.go if not already, and set the flag in engine.Run). Re-run → PASS.
- [ ] **Step 3** `go test ./internal/engine/` → PASS; `go build ./...`; `go vet ./...`. Commit `feat(engine): set deny-builtin-tools on the turn when caller supplied tools (Track 3a)`.

> Decision for Task 2/6: export the context READER as `acp.DenyBuiltinTools(ctx) bool` (used by the handler internally too) so the engine test can assert it. Update Task 2 accordingly if it made the reader unexported.

---

### Task 7: live verification + findings update

**Files:** create `tests/track3a_elicitation_test.go` (`//go:build kirolive`);
update `docs/reviews/2026-07-14-track0-toolcall-findings.md`.

Mirror the Track 0 harness (`tests/track0_capture_test.go`): drive a
`get_weather` tool round-trip on each surface against a gateway running with
`ACP_CAPTURE=true` + real kiro, then read `/admin/api/acp-capture` and assert the
NEW behavior:
- at least one `session/request_permission` frame appears (kiro tried a built-in
  tool) — OR kiro emitted a `{"tool_call"` block directly;
- the capture shows the gateway's denial working (kiro did NOT complete via
  built-in tools) and kiro emitted a `{"tool_call":…}` JSON block in
  `agent_message_chunk` text.

Run live (env per the Track 0 procedure; port 18099; PII-neutralize the gateway
`go run` only; kill the gateway + kiro children when done; verify no orphans;
never touch 18080). Read the real `session/request_permission` frame from the
capture and **reconcile** `permissionParams`/`pickRejectOption` field names
against it — if kiro's real `optionId`/`kind`/`title` differ from the JS-derived
guesses, fix Task 1's parsing and re-run. Append a "Track 3a outcome" section to
the findings doc with the real permission-frame shape and whether kiro now emits
tool_call JSON.

- [ ] **Step 1** write the harness; `go vet -tags kirolive ./tests/` compiles; `go test ./...` unaffected.
- [ ] **Step 2** LIVE run; read capture; reconcile field names; iterate.
- [ ] **Step 3** finalize findings section. Commit `test(track3a): live tool-call elicitation harness + findings (Track 3a)`.

---

## Verification (whole plan)

- [ ] `go build ./...`; `go test ./...` (kirolive excluded); `go test -race ./internal/acp/ ./internal/engine/ ./internal/config/ ./internal/pool/ ./internal/session/`.
- [ ] `go vet ./...`; `go run mvdan.cc/gofumpt@latest -l .` empty; `CGO_ENABLED=0 go build ./cmd/otto-gateway`; `GOOS=linux go build ./...`; `make arch-lint`.
- [ ] Grant path unchanged for tool-less requests (Task 4 regression test green).
- [ ] LIVE: patched gateway + real kiro elicits a `{"tool_call":…}` block (Track 3b then coerces it).

## Notes for the executor

- Coercion of the emitted `{"tool_call":…}` is Track 3b — do NOT touch
  `coerce.go`/`collect.go`/name-remap here. 3a's win is *elicitation*, proven by
  capture.
- The permission handler (Task 4) is the load-bearing change; keep the grant
  path identical to preserve every non-tool request.
- Do not push/merge without the human's OK.
