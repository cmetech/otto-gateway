---
phase: quick-260524-pyd
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - tests/e2e/ollama_e2e_test.go
  - tests/e2e/README.md
autonomous: true
requirements: [OLLAMA-E2E]

must_haves:
  truths:
    - "GET /api/version returns 200 with no auth header (AUTH-03 exemption)"
    - "POST /api/chat with no auth returns 401"
    - "GET /api/tags (authed) returns a non-empty models[] including an entry named 'auto'"
    - "POST /api/chat (authed, stream:false) returns a single JSON object, Content-Type application/json, done==true"
    - "POST /api/chat (authed, stream:true) is silently downgraded to a single non-NDJSON JSON object (Phase-2 parity guard)"
    - "POST /api/generate (authed, stream:false) returns a single JSON object with a non-empty response field"
  artifacts:
    - path: "tests/e2e/ollama_e2e_test.go"
      provides: "Ollama API contract E2E subtests (6) in package e2e_test, behind e2e tag + OTTO_E2E gate"
      contains: "func TestE2E_Ollama"
    - path: "tests/e2e/README.md"
      provides: "Coverage table rows for the 6 Ollama subtests + Phase-4 NDJSON note"
      contains: "Ollama"
  key_links:
    - from: "tests/e2e/ollama_e2e_test.go"
      to: "bootGateway / gateOrSkip / freePort (e2e_test.go)"
      via: "same package e2e_test (reuse, do not redefine)"
      pattern: "bootGateway\\(t,"
---

<objective>
Extend the existing E2E suite (`tests/e2e/`) with Ollama API contract coverage — the surface LangFlow consumes — mirroring the structure of the existing Anthropic subtests. The current Ollama contract is non-streaming (NDJSON streaming is Phase 4); this plan asserts that current contract PLUS the silent `stream:true → non-stream` downgrade guard so a future Phase-4 change is forced to update these tests deliberately.

Purpose: Lock the load-bearing Ollama wire shape (the surface LangFlow flows depend on) at HTTP fidelity, against the real binary + real kiro, so shape drift fails loudly.
Output: One new test file `tests/e2e/ollama_e2e_test.go` and an updated `tests/e2e/README.md`. Additive only — no `internal/` or `cmd/` changes.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@./CLAUDE.md
@.planning/STATE.md

<!-- The new file is the SAME package as the existing suite (e2e_test). It MUST
     reuse the helpers below verbatim and MUST NOT redefine them. -->
@tests/e2e/e2e_test.go
@tests/e2e/README.md

<!-- Source of truth for the exact JSON field names being asserted. Read these,
     do not guess tags. -->
@internal/adapter/ollama/handlers.go
@internal/adapter/ollama/render.go
@internal/adapter/ollama/wire.go
@internal/auth/bearer.go

<interfaces>
<!-- Reusable from tests/e2e/e2e_test.go (package e2e_test) — DO NOT redefine: -->
- `func gateOrSkip(t *testing.T)`            // skips unless OTTO_E2E=1
- `func bootGateway(t *testing.T, extraEnv map[string]string) (string, func())` // baseline env sets AUTH_TOKEN=e2e-token, KIRO_CMD=<resolved kiro>; returns baseURL + cleanup; t.Skipf on warmup failure
- `func resolveKiro(t *testing.T) string`    // skips when kiro env absent
- `func freePort(t *testing.T) string`
- `func readAll(resp *http.Response) string` // drains body for error logging

<!-- Auth (internal/auth/bearer.go): the middleware accepts BOTH
     `Authorization: Bearer <token>` (tried first) and `x-api-key: <token>`
     (fallback). The Anthropic subtests use `Authorization: Bearer e2e-token`;
     reuse that. With AUTH_TOKEN=e2e-token set, the token is "e2e-token". -->

<!-- Exact JSON tags to assert (verified in wire.go / render.go):
  /api/version (ollamaVersionResponse, AUTH-EXEMPT, outer router):
      {"version": string, "commit": string}
  /api/tags (ollamaTagsResponse):
      {"models": [ {"name","model","modified_at","size","digest",
                    "details": {"format","family","families",
                                "parameter_size","quantization_level"}} ]}
      -> "auto" is ALWAYS prepended (handlers.go handleTags). Assert ONLY
         stable fields: name, model, details.format, details.family.
         Do NOT assert digest/size/modified_at (digest is "", size 0).
  /api/chat response (ollamaChatResponse):
      {"model","created_at","message":{"role","content","thinking"(omitempty)},
       "done":bool,"done_reason":string, ...durations/counts...}
      -> message.role == "assistant"; done == true; done_reason in {"stop","length"}.
         Do NOT assert durations or *_eval_count (non-deterministic).
  /api/generate response (ollamaGenerateResponse):
      {"model","created_at","response":string,"done":bool,"done_reason":string, ...}
      -> assistant text lives in "response" (NOT message{}). done == true.
  Stream downgrade (handlers.go handleChat/handleGenerate):
      stream:true silently sets wire.Stream=false; response always goes through
      writeJSON -> Content-Type application/json, single JSON object (NOT
      application/x-ndjson, NOT multi-line frames). -->
</interfaces>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Add tests/e2e/ollama_e2e_test.go — 6 Ollama contract subtests</name>
  <files>tests/e2e/ollama_e2e_test.go</files>
  <action>
Create a NEW file `tests/e2e/ollama_e2e_test.go` with `//go:build e2e` as the first line and `package e2e_test`. It is the SAME package as `e2e_test.go` — REUSE `gateOrSkip`, `bootGateway`, `resolveKiro`, `freePort`, and `readAll` directly. DO NOT redefine them, TestMain, the moduleRoot const, or any existing helper. Import only stdlib (`bytes`, `context`, `encoding/json`, `net/http`, `strings`, `testing`, `time`) — no new go.mod deps.

Define one top-level test `func TestE2E_Ollama(t *testing.T)` that calls `gateOrSkip(t)` first, then boots ONE shared gateway: `baseURL, cleanup := bootGateway(t, nil)` followed by `defer cleanup()`. Passing `nil` uses the baseline env (default ENABLED_SURFACES so Ollama is mounted, AUTH_TOKEN=e2e-token, real kiro via KIRO_CMD). Add the 6 cases as `t.Run` subtests sharing that single boot.

Add a small package-private request helper at file scope (named e.g. `ollamaRequest`) that builds a context-bounded request (use `context.WithTimeout(context.Background(), 60*time.Second)` registered via `t.Cleanup(cancel)` — matches the noctx trust gate and the postMessages pattern), sets Content-Type application/json for POST bodies, applies an optional auth header, executes via `http.DefaultClient`, and returns `*http.Response`. The auth header to use throughout is `Authorization: Bearer e2e-token` (verified accepted in bearer.go).

Subtests (assert ONLY deterministic fields per the <interfaces> notes — never durations, *_eval_count, digest, size, modified_at):

1. "VersionAuthExempt" — GET `/api/version` with NO auth header. Expect 200. Decode JSON; assert it has a "version" key and a "commit" key (both string; non-nil presence is enough — values are build-dependent). Comment: AUTH-03 exemption — LangFlow probes version without creds, and /api/version is mounted on the OUTER (unauthenticated) router.

2. "Unauthorized" — POST `/api/chat` with NO auth header, body `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`. Expect 401. (Auth rejects before kiro is touched.)

3. "Tags" — GET `/api/tags` with `Authorization: Bearer e2e-token`. Expect 200. Decode into a struct mirroring ollamaTagsResponse (only the fields asserted: models[] each with name, model, details{format, family}). Assert models[] is non-empty; assert at least one entry has name=="auto"; for that "auto" entry assert name, model are non-empty and details.format and details.family are non-empty. Do NOT assert digest/size/modified_at. (Does not require the engine.)

4. "Chat_NonStreaming" — POST `/api/chat` with auth, body `{"model":"auto","messages":[{"role":"user","content":"say hi"}],"stream":false}`. Expect 200. Assert `Content-Type` starts with "application/json" and is NOT "application/x-ndjson". Decode a single JSON object (use json.Decoder and confirm exactly one value — the response is a single object, not NDJSON). Assert: model=="auto"; message.role=="assistant"; message.content non-empty; done==true; done_reason is one of {"stop","length"}. (Real kiro — LangFlow chat path; inherits bootGateway warmup-skip.) On non-200, t.Fatalf including readAll(resp).

5. "Chat_StreamDowngrade" — POST `/api/chat` with auth, body `{"model":"auto","messages":[{"role":"user","content":"say hi"}],"stream":true}`. Expect 200. Assert `Content-Type` starts with "application/json" (NOT "application/x-ndjson"). Read the FULL body and assert it is a SINGLE JSON object (one decode succeeds and a second `decoder.Decode` returns io.EOF — i.e. NOT multi-line NDJSON frames). Assert done==true on that object. Add a prominent comment: this documents the Phase-2 silent stream→non-stream downgrade (Node parity, handlers.go handleChat sets wire.Stream=false). This subtest MUST be changed to expect `application/x-ndjson` multi-line frames when Phase 4 lands NDJSON streaming.

6. "Generate_NonStreaming" — POST `/api/generate` with auth, body `{"model":"auto","prompt":"say hi","stream":false}`. Expect 200. Decode a single JSON object; assert the "response" field (the exact tag from render.go generateResponseToWire / wire.go ollamaGenerateResponse — assistant text lives in "response", NOT message{}) is non-empty and done==true. On non-200, t.Fatalf including readAll(resp).

Close every response body with `defer func() { _ = resp.Body.Close() }()`. For single-object assertions, decode with a local anonymous struct using exact json tags. To prove "single JSON object" for the downgrade/non-streaming cases, after the first successful Decode call Decode again into a throwaway and assert it returns io.EOF (import "io" if used).
  </action>
  <verify>
    <automated>cd /Users/coreyellis/Projects/repos/local/loop24-gateway && go build ./... && go vet ./... && go vet -tags e2e ./tests/e2e/... && OTTO_E2E= go test -tags e2e ./tests/e2e/ 2>&1 | tail -5 && go test ./... -race 2>&1 | tail -5</automated>
  </verify>
  <done>
File exists with `//go:build e2e` + `package e2e_test` + `func TestE2E_Ollama` containing 6 t.Run subtests; reuses bootGateway/gateOrSkip without redefining helpers; stdlib-only imports. `go build ./...` clean; `go vet ./...` clean; `go vet -tags e2e ./tests/e2e/...` clean; with OTTO_E2E unset the e2e suite all-skips (no failures); `go test ./... -race` green with the e2e file excluded (no e2e tag). Live `OTTO_E2E=1` run is NOT executed here — the orchestrator runs it after.
  </done>
</task>

<task type="auto">
  <name>Task 2: Document the 6 Ollama subtests in tests/e2e/README.md</name>
  <files>tests/e2e/README.md</files>
  <action>
Edit `tests/e2e/README.md` "What it covers" table. Add 6 rows for the new Ollama subtests, mapping each to its Ollama contract / LangFlow usage (the UAT-step column can read "Ollama contract" since these are not in the original HUMAN-UAT step list):

- `TestE2E_Ollama/VersionAuthExempt` — `GET /api/version` no auth → 200, has `version`+`commit` (AUTH-03 exemption; LangFlow version probe)
- `TestE2E_Ollama/Unauthorized` — `POST /api/chat` no auth → 401
- `TestE2E_Ollama/Tags` — `GET /api/tags` (Bearer) → 200, non-empty `models[]` incl. `auto`; stable fields only (LangFlow model list)
- `TestE2E_Ollama/Chat_NonStreaming` — `POST /api/chat` (Bearer, stream:false) → 200 `application/json`, single object: message.role=assistant, content non-empty, done, done_reason∈{stop,length} (LangFlow chat path, real kiro)
- `TestE2E_Ollama/Chat_StreamDowngrade` — `POST /api/chat` stream:true → 200 single JSON object (NOT NDJSON), done — guards the Phase-2 silent stream→non-stream downgrade
- `TestE2E_Ollama/Generate_NonStreaming` — `POST /api/generate` (Bearer, stream:false) → 200 single object: `response` non-empty, done (LangFlow generate path, real kiro)

Add a one-line note near the table (or in a short sentence under it) stating: Ollama NDJSON streaming is Phase 4 — this suite currently asserts the non-streaming contract plus the `stream:true` silent-downgrade guard, which must be updated when Phase 4 lands NDJSON. Keep edits additive; do not remove or reorder existing rows.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/Projects/repos/local/loop24-gateway && grep -c "TestE2E_Ollama" tests/e2e/README.md</automated>
  </verify>
  <done>
README "What it covers" table has all 6 `TestE2E_Ollama/*` rows (grep count >= 6) each mapped to its Ollama contract / LangFlow usage, plus a one-line Phase-4 NDJSON note. Existing rows unchanged.
  </done>
</task>

</tasks>

<verification>
Acceptance gates (all run by Task 1 verify, plus golangci-lint):
- `go build ./...` clean
- `go vet ./...` clean
- `go vet -tags e2e ./tests/e2e/...` clean
- `OTTO_E2E= go test -tags e2e ./tests/e2e/` → all skip (no fail)
- `go test ./... -race` green (e2e file excluded — no tag)
- `golangci-lint run` (and with `-tags e2e` if the repo lints the e2e tag) → 0 issues
- Live `OTTO_E2E=1 make e2e` is NOT run here — the orchestrator runs it after this plan.
</verification>

<success_criteria>
- New file `tests/e2e/ollama_e2e_test.go` adds exactly the 6 specified Ollama subtests under one shared gateway boot, reusing existing helpers, stdlib-only, behind the e2e tag + OTTO_E2E gate.
- Assertions cover only deterministic fields and verify the exact JSON tags from wire.go/render.go.
- The stream-downgrade subtest documents Phase-2 parity and is flagged for Phase-4 NDJSON update.
- README documents all 6 subtests and the Phase-4 NDJSON note.
- No `internal/` or `cmd/` source modified; no new go.mod deps.
- All static gates green (build, vet, vet -tags e2e, gate-off skip, race, golangci-lint).
</success_criteria>

<output>
Create `.planning/quick/260524-pyd-ollama-e2e/260524-pyd-SUMMARY.md` when done.
</output>
