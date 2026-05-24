---
phase: 02-ollama-end-to-end
plan: 06
subsystem: ollama-adapter
tags: [ollama-adapter, chi-sub-router, auth-bearer, ip-allowlist, xff-trust, langflow-uat, pool-warmup-ordering, codex-m1, codex-m4, codex-m5, codex-h7]

# Dependency graph
requires:
  - phase: 01-foundations
    provides: "internal/server (chi router + accessLog + RequestID); internal/version; cmd/loop24-gateway scaffold; scripts/loop24{,.ps1} POSIX/PowerShell wrappers"
  - phase: 01.1-acp-wire-alignment
    provides: "real-kiro round-trip gate pattern (resolveKiroCLI + LOOP24_INTEGRATION env gate)"
  - phase: 02-ollama-end-to-end
    provides: "Plan 01 canonical.ChatRequest/Response/Message/ContentPart with ContentKindImage seam; Plan 02 auth.Bearer + auth.IPAllowlist with TrustXForwardedFor; Plan 03 config.Config with AuthTrustXFF/PoolSize/AllowedIPs/OllamaPathPrefix; Plan 04 engine.Engine.Collect + buildBlocks BlockKindImage emission; Plan 05 pool.Pool satisfying engine.ACPClient + Models()/Stats()"
provides:
  - "internal/adapter/ollama: chi sub-router with 10 protected routes (chat/generate/tags/show/ps + 5 stubs) + HandleVersion accessor for outer-router mounting (Codex M-4 split)"
  - "internal/adapter/ollama/decode.go: decodeJSONBody[T] generic helper enforcing http.MaxBytesReader uniformly across every body-reading handler (Codex M-5 — closes the previously-unbounded stub path)"
  - "Codex M-1 image translation: Ollama messages[].images → canonical.ContentKindImage parts with detectMIME-resolved MIME, flowing into engine.buildBlocks BlockKindImage end-to-end"
  - "internal/server.NewFromConfig: Phase 2 constructor exposing server.Config{AuthTrustXFF, OllamaProtectedRouter, OllamaVersionHandler, Pool} — auth scoped to cfg.OllamaPath sub-tree, /api/version registered exactly ONCE on the outer auth-exempt router (Codex M-4 no precedence dance)"
  - "Codex H-7 wiring path complete: config.Load(AUTH_TRUST_XFF) → server.Config.AuthTrustXFF → auth.IPAllowlist auth.Config.TrustXForwardedFor"
  - "cmd/loop24-gateway/main.go: newApp(ctx, cfg, logger) → (*app, cleanup, error) — pool.Warmup bounded by 30s ctx deadline (T-02-36) BEFORE server constructor (POOL-02), engine wired only when pool exists, adapter wired always (degraded mode keeps /, /health, /api/version, stubs alive when KIRO_CMD unset)"
  - "Wrapper scripts pass-through documentation: AUTH_TOKEN, ALLOWED_IPS, AUTH_TRUST_XFF, POOL_SIZE, KIRO_CWD, OLLAMA_PATH_PREFIX, OPENAI_PATH_PREFIX inheritance via parent env"
  - "Integration test internal/adapter/ollama/integration_test.go (TestIntegration_ChatEndToEnd + TestIntegration_TagsEndpoint) gated by LOOP24_INTEGRATION=1 + kiro-cli on PATH"
affects: ["phase 03 openai-adapter (server.NewFromConfig wiring template + auth/IP middleware reuse)", "phase 03.1 anthropic-adapter (same middleware reuse; canonical.ChatRequest already populated by ollama path)", "phase 05 pool size + session registry (raises POOL_SIZE default; /health pool stats already plumbed)", "phase 06 tools/PostHook (adapter already passes Tools through to canonical.ToolSpec for the bracketed-section placeholder)"]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Adapter-over-canonical: internal/adapter/ollama imports ONLY canonical (TRST-04). Consumer-defined Engine + ModelCatalog interfaces declared in adapter.go; concrete *engine.Engine + *pool.Pool structurally satisfy them at wiring time in main.go."
    - "Router split (Codex M-4): adapter exposes ProtectedRouter() chi.Router for the auth-protected mount AND HandleVersion() http.HandlerFunc for the outer auth-exempt registration. /api/version registered exactly once on the outer router."
    - "Generic body-cap helper (Codex M-5): decodeJSONBody[T any](w, r, max, *T) error applies http.MaxBytesReader before json decode; every body-reading handler MUST use it (handlers/stub callsites grepped to confirm no direct json.NewDecoder calls)."
    - "Image translation chain (Codex M-1): wire.go detectMIME peeks first bytes of decoded base64 → canonical.ContentKindImage ContentPart → engine.buildBlocks emits BlockKindImage with Data=base64-decoded bytes."
    - "XFF trust opt-in (Codex H-7): default-false in config.Config.AuthTrustXFF; threaded through server.Config.AuthTrustXFF into auth.Config.TrustXForwardedFor. Laptop deployments are safe-by-default."
    - "newApp factoring: cmd/loop24-gateway/main.go's wiring extracted into newApp(ctx, cfg, logger) so main_test.go can drive the construction sequence directly. Returns (*app, cleanup, error) — cleanup is the deferred-by-caller idempotent shutdown closure."

key-files:
  created:
    - "internal/adapter/ollama/adapter.go (137 lines) — Adapter + Config + Engine/ModelCatalog interfaces + New() + ProtectedRouter() + HandleVersion()"
    - "internal/adapter/ollama/wire.go (348 lines) — every Ollama JSON struct (chat/generate/tags/show/ps/version/stubs) + wireToChatRequest + wireGenerateToChatRequest with Codex M-1 image translation + detectMIME"
    - "internal/adapter/ollama/decode.go (48 lines) — decodeJSONBody[T] generic helper + isMaxBytesError classifier (Codex M-5)"
    - "internal/adapter/ollama/render.go (138 lines) — chatResponseToWire + generateResponseToWire + mapStopReason + estimateTokens + joinTextContent/joinThinkingContent"
    - "internal/adapter/ollama/handlers.go (~270 lines) — handleChat/Generate/Tags/Show/PS + handleVersion (exposed via HandleVersion accessor) + writeJSON/writeError helpers"
    - "internal/adapter/ollama/stub.go (~120 lines) — handlePull/Push/Create via stubStreaming + handleCopy/Delete returning {} + writeNDJSON helper"
    - "internal/adapter/ollama/handlers_test.go (~390 lines, whitebox) — fakeEngine + fakeCatalog test doubles + chat/generate/tags/show/ps/version coverage + M-4 ProtectedRouter-doesn't-register-version + M-5 body-too-large path"
    - "internal/adapter/ollama/stub_test.go (~200 lines) — D-15 byte-shape parity vs Node reference for every stub endpoint"
    - "internal/adapter/ollama/wire_test.go (~250 lines) — table-driven wireToChatRequest + Codex M-1 Images proof + Images_MalformedBase64 defensive-skip + render helpers"
    - "internal/adapter/ollama/decode_test.go (~75 lines) — happy/exceeds-limit/malformed-json coverage of decodeJSONBody"
    - "internal/adapter/ollama/integration_test.go (~200 lines) — real-kiro round-trip gated by LOOP24_INTEGRATION=1 + resolveKiroCLI (Phase 1 D-17 pattern)"
    - "cmd/loop24-gateway/main_test.go (~75 lines) — TestApp_NoKiroCmd_StartsHealthOnly + TestApp_WarmupBeforeListen"
  modified:
    - "internal/server/server.go — added server.Config + NewFromConfig (Phase 2 constructor); kept New as Phase 1 wrapper; PoolStatsSource interface declared here"
    - "internal/server/health.go — healthHandler reads s.pool (PoolStatsSource), renders into PoolStats; nil-safe"
    - "internal/server/server_test.go — added 6 new tests (Exempt/Protected/IPAllowlist Deny+Allow/XFFTrustGate/HealthPoolWiring)"
    - "cmd/loop24-gateway/main.go — full rewrite per PATTERNS.md template; extracted newApp + poolStatsAdapter; warmup-deadline guard"
    - "scripts/loop24 — inline doc comment enumerates the full env-var pass-through set (including AUTH_TRUST_XFF per Codex H-7)"
    - "scripts/loop24.ps1 — same env-var pass-through documentation update"

key-decisions:
  - "Codex M-4 router split (ProtectedRouter + HandleVersion accessors) — the prior design relied on chi's most-specific-wins precedence between an inner /version inside the protected router and an outer /api/version on the server. The split eliminates that dependency: /api/version is registered EXACTLY ONCE on the outer router via the dedicated HandleVersion() http.HandlerFunc accessor; the adapter's ProtectedRouter() carries only the 10 protected routes. No precedence dance possible."
  - "Codex M-5 uniform body cap via decode.go's decodeJSONBody[T] generic — pre-fix, only handleChat/handleGenerate carried http.MaxBytesReader, leaving handleShow + every stub (/api/pull, /push, /create, /copy, /delete) body-unbounded. T-02-29/T-02-45 fix: ALL body-reading handlers now invoke the helper with per-endpoint caps (4 MiB chat/generate, 1 MiB show, 64 KiB stubs). Acceptance: TestDecodeJSONBody_ExceedsLimit asserts a 5 MiB JSON body under a 4 MiB cap is rejected with *http.MaxBytesError; grep-gates confirm zero direct json.NewDecoder calls in handlers.go + stub.go."
  - "Codex M-1 image translation chain: wire.go appends ContentKindImage parts to canonical Message.Content (text first, images after) so engine.buildBlocks can emit BlockKindImage blocks in canonical order. detectMIME peeks the first bytes of the base64-decoded payload (PNG/JPEG/GIF magic, fallback application/octet-stream) — Ollama wire shape carries no MIME, so this is the only sniffing point. Malformed base64 is silently dropped (defensive — a single corrupt image must not abort the whole prompt)."
  - "Codex H-7 default-false AuthTrustXFF wiring path: laptop deployments are safe-by-default. main.go threads cfg.AuthTrustXFF into server.Config.AuthTrustXFF which threads into auth.IPAllowlist via auth.Config.TrustXForwardedFor. End-to-end test TestIPAllowlist_XFFTrustGate proves both modes (false ignores spoofed XFF → 403; true honors it → 200). Operators surface the choice via the startup log line `auth mode enabled=… ip_allowlist=… trust_xff=…`."
  - "server.NewFromConfig added alongside (not replacing) the Phase 1 server.New constructor — keeps Phase 1 callers (and the degraded /health-only path) working without rewrite. The two constructors share the same Server struct + Run/RunUntilSignal lifecycle methods."
  - "Adapter never imports internal/engine (TRST-04 boundary). Engine + ModelCatalog interfaces declared in adapter.go; *engine.Engine + *pool.Pool satisfy them structurally. Wiring happens in main.go where the boundary crossing is one location instead of N."
  - "newApp(ctx, cfg, logger) extraction in main.go: the wiring graph is intentionally linear and lives in a single function so main_test.go can drive the entire construction sequence. cleanup is returned as a closure the caller defers — closes pool.Close best-effort; safe to call when pool is nil (degraded mode)."
  - "Integration test gated by LOOP24_INTEGRATION=1 + kiro-cli on PATH (D-17 pattern from Phase 1). httptest.NewServer binds an ephemeral port — plan forbids hardcoding :11434 in tests."
  - "Wrapper scripts (POSIX + PowerShell) document the env-var pass-through set in comments. Both scripts already inherit parent env automatically (nohup / Start-Process); the comment is the spec of which vars are user-tunable. Includes AUTH_TRUST_XFF (Codex H-7 opt-in)."
  - "writeJSON's status parameter was always http.StatusOK (every successful response in this package is 200; errors go through writeError with appropriate non-200 status). Lint (unparam) flagged it; signature dropped to writeJSON(w, body any) — caught by pre-commit hook on first push, fixed in the same commit."

patterns-established:
  - "Consumer-defined interface for cross-boundary calls: when a downstream package would otherwise need to import an upstream concrete type (adapter → engine; adapter → pool), declare the local interface and let the concrete type structurally satisfy it. Wiring lives in cmd/. Keeps the boundary one location (TRST-04 / REQ-CI-04)."
  - "Per-endpoint body cap via a single generic helper: decodeJSONBody[T any] applies http.MaxBytesReader before json decode. Replaces N open-coded callsites with one. Easy to grep-gate (handlers must call helper; no direct json.NewDecoder allowed)."
  - "Router-split for exempt-vs-protected routes: when one route in an otherwise-protected family is exempt from auth, expose it as a separate http.HandlerFunc accessor that the server mounts on the OUTER router. Avoids relying on chi precedence semantics."
  - "Magic-byte MIME detection for wire shapes that carry no MIME: detectMIME peeks the first 3-4 bytes of decoded payload, matches PNG/JPEG/GIF signatures, falls back to application/octet-stream. Stateless, allocation-free."
  - "Auth-mode startup log line: the operator-visible `logger.Info('auth mode', 'enabled', …, 'trust_xff', …)` makes deployment posture auditable (T-02-31 + Codex H-7 surfaces). Pattern repeatable for future opt-in toggles."
  - "Cleanup closure returned from newApp: callers defer the closure; the closure handles ordered shutdown (pool.Close after server.Shutdown — server.Shutdown lives inside Run/RunUntilSignal). Avoids the test-vs-main divergence of repeated defer chains."

requirements-completed: [SURF-01, SURF-03, SURF-05, SURF-07, AUTH-01, AUTH-02, AUTH-03, OBSV-01, ACP-07, POOL-02]

# Metrics
duration: ~45min
completed: 2026-05-24
---

# Phase 02 Plan 06: Ollama Adapter + Server Wiring + Main Integration Summary

**End-to-end Phase 2 slice: Ollama adapter (10-route chi sub-router with Codex M-1 image translation + Codex M-5 uniform body cap + Codex M-4 router-split for exempt /api/version), server.NewFromConfig with auth/IP-allowlist scoped to /api and Codex H-7 default-false XFF trust, main.go newApp wiring pool→Warmup→engine→ollama→server with POOL-02 ordering enforced + warmup-deadline guard, wrapper-script env-var documentation, and a real-kiro integration test gated by LOOP24_INTEGRATION=1.**

## Performance

- **Duration:** ~45 min
- **Started:** 2026-05-23T21:30Z (worktree spawn)
- **Completed:** 2026-05-24T02:02Z
- **Tasks:** 3 (Task 1 auto-TDD, Task 2 auto-TDD, Task 3 integration test ahead of human-verify checkpoint)
- **Files created:** 12 (10 under `internal/adapter/ollama/`, 1 `cmd/loop24-gateway/main_test.go`, 1 SUMMARY)
- **Files modified:** 6 (`internal/server/{server,health,server_test}.go` + `cmd/loop24-gateway/main.go` + `scripts/loop24{,.ps1}`)
- **Lines added:** ~2900

## Accomplishments

- **`POST /api/chat` end-to-end:** Ollama-shape JSON → wire.go translator → canonical.ChatRequest → engine.Collect → pool→kiro-cli → canonical.ChatResponse → render.go → Ollama-shape JSON response. Non-streaming per Phase 2 D-01; silent stream:true → false downgrade (Node parity).
- **`POST /api/generate`** mirrors /api/chat with response→`response` field instead of `message`.
- **`GET /api/tags`, `POST /api/show`, `GET /api/ps`, `GET /api/version`** all return Ollama-shape JSON sourced from pool.Models() with "auto" prepended (Node parity). show returns 404 for unknown models, capabilities=[completion, tools].
- **Stub endpoints (/api/pull, /push, /create, /copy, /delete)** byte-shape parity vs Node reference: pull stream:true emits NDJSON `[{status:pulling manifest},{status:success}]`, stream:false returns `{status:success}`; push/create same dichotomy; copy/delete return `{}`.
- **Auth + IP allowlist applied at the chi sub-router scope (`Route("/api", ...)` block)** — /, /health, /api/version remain auth-exempt per AUTH-03. Bearer middleware uses crypto/subtle constant-time compare. IPAllowlist consults X-Forwarded-For only when AUTH_TRUST_XFF=1 (Codex H-7 — default-false, safe for laptop deployments).
- **`/health` returns pool stats (Size/Alive/Busy)** via PoolStatsSource interface (OBSV-01).
- **`pool.Warmup` runs BEFORE the HTTP listener accepts** (POOL-02), bounded by a 30s context deadline so a hung kiro-cli Initialize cannot stall startup forever (T-02-36).
- **Wrapper scripts (POSIX + PowerShell) document the full env-var pass-through set** including AUTH_TRUST_XFF.
- **Integration test asserts real /api/chat → kiro-cli round-trip** when LOOP24_INTEGRATION=1; skips cleanly otherwise.

## Task Commits

Each task was committed atomically:

1. **Task 1: Ollama adapter (wire + render + handlers + stub + decode + all unit tests)** — `e74573c` (`feat(02-06): ollama adapter — wire + render + handlers + stub + decode helper`)
2. **Task 2: Server NewFromConfig + main.go newApp + main_test + wrapper script env-var docs** — `6bd21db` (`feat(02-06): server NewFromConfig + main.go pool→engine→ollama wiring + wrapper env-vars`)
3. **Task 3 (integration code, ahead of human-verify checkpoint): real-kiro integration test** — `18ee206` (`test(02-06): integration test against real kiro-cli for /api/chat + /api/tags`)

**Plan metadata commit:** (this SUMMARY commit — to follow)

## Files Created/Modified

See frontmatter `key-files` for the full list. Highlights:

- **`internal/adapter/ollama/adapter.go`** — Adapter + Config + Engine/ModelCatalog consumer interfaces. The TRST-04 boundary: this file imports only `loop24-gateway/internal/canonical` and `github.com/go-chi/chi/v5` from external/internal Go.
- **`internal/adapter/ollama/decode.go`** — decodeJSONBody[T] generic helper. Single point of body-cap enforcement (Codex M-5).
- **`internal/adapter/ollama/wire.go`** — All Ollama JSON shapes + canonical translators + detectMIME + Codex M-1 image translation logic.
- **`internal/adapter/ollama/handlers.go`** — Chat/generate/tags/show/ps + the HandleVersion-exposed handleVersion (mounted on outer router by server.NewFromConfig).
- **`internal/server/server.go`** — server.Config + NewFromConfig (Phase 2 path); Phase 1 New() kept as the degraded /health-only constructor; PoolStatsSource interface declared here.
- **`cmd/loop24-gateway/main.go`** — Full rewrite per PATTERNS.md template; extracted newApp + poolStatsAdapter; warmup-deadline guard.

## Decisions Made

See frontmatter `key-decisions` for the full list with rationale.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] TestDecodeJSONBody_ExceedsLimit initial fixture was non-JSON 5 MiB blob**
- **Found during:** Task 1 (decode_test.go)
- **Issue:** Initial test built a 5 MiB body of `aaaa...` (raw bytes) and expected the helper to return *http.MaxBytesError. The JSON decoder bailed out on the first byte (`a` is not a valid JSON start) with a parse error BEFORE the cap fired — the helper saw a regular JSON error and reported as such. The cap is fine; the test was wrong.
- **Fix:** Build a syntactically-valid JSON envelope (`{"field":"<5 MiB of a's>"}`) so the decoder MUST read past the 4 MiB cap to consume the string; *http.MaxBytesError now surfaces. Validates the load-bearing Codex M-5 invariant correctly.
- **Files modified:** `internal/adapter/ollama/decode_test.go`
- **Verification:** Test now passes; isMaxBytesError(err) returns true.
- **Committed in:** `e74573c` (Task 1 commit — fix landed before the commit was created)

**2. [Rule 3 - Blocking] golangci-lint pre-commit hook caught noctx/unparam violations on first push**
- **Found during:** Task 1 pre-commit hook
- **Issue:** Three linter violations: (a) `noctx` flagged `httptest.NewRequest` in test files (must use NewRequestWithContext); (b) `unparam` flagged writeJSON's status arg (always http.StatusOK); (c) `unparam` flagged wireToChatRequest + wireGenerateToChatRequest's error return (always nil — translation cannot fail at this layer).
- **Fix:** (a) Replaced every `httptest.NewRequest(...)` with `httptest.NewRequestWithContext(context.Background(), ...)` + added context import. (b) Dropped status from writeJSON signature; writeJSON now hard-codes http.StatusOK with updated doc-comment explaining the invariant (errors go through writeError). (c) Dropped error return from both wire translators; updated handler callsites accordingly.
- **Files modified:** `internal/adapter/ollama/{wire,handlers,stub,decode_test,wire_test,handlers_test,stub_test}.go`
- **Verification:** golangci-lint passes; all 30 unit tests still pass with -race.
- **Committed in:** `e74573c` (same Task 1 commit — fixed before the commit succeeded)

---

**Total deviations:** 2 auto-fixed (1 test bug, 1 blocking lint).
**Impact on plan:** Both fixes preserved the load-bearing invariants (Codex M-5 cap enforcement; TRST-04 boundary; whitebox test ownership of fakeEngine/fakeCatalog). No scope creep.

## Issues Encountered

- **None blocking.** Plan acceptance criterion `grep -c 'decodeJSONBody' internal/adapter/ollama/handlers.go` expected ≥ 4 textual occurrences; my code yields 3 (chat, generate, show — all body-reading handlers in handlers.go). The actual invariant ("every body-reading handler uses decodeJSONBody") is satisfied: handlers.go has exactly 3 body-reading handlers; stub.go has the other 5 callsites (3 via stubStreaming + 2 in copy/delete). The textual count gate is a slight under-spec; the runtime invariant holds.
- Similarly the `grep -c 'decodeJSONBody' internal/adapter/ollama/stub.go` gate expects ≥ 5 textual occurrences; my code yields 3 because the 3 stream-emitting stubs (pull/push/create) share the `stubStreaming` helper (DRY). Inlining would add textual matches at the cost of duplication. The shared helper still applies the body cap uniformly; TestHandleChat_RequestBodyTooLarge proves the cap path end-to-end.

## User Setup Required

None — Phase 2 introduces no external service configuration. Operators set env vars (AUTH_TOKEN, ALLOWED_IPS, AUTH_TRUST_XFF, POOL_SIZE, KIRO_CMD, etc.) per the existing deployment model; wrapper scripts already inherit parent env.

## Verification Status (Task 3 Checkpoint)

**Programmatic verification — PASSING:**
- `make build` produces `bin/loop24-gateway` (~6.8 MiB static binary)
- `go test -race -count=1 ./...` green across all 9 packages (cmd, acp, adapter/ollama, auth, canonical, config, engine, pool, server)
- All Codex/threat-model gates verified (M-1 image translation, M-4 router split, M-5 body cap, H-7 XFF trust default-off)
- Unit-test coverage: 30 ollama-adapter tests, 12 server tests, 2 main tests, all green
- Integration test (`TestIntegration_ChatEndToEnd`) skips cleanly without `LOOP24_INTEGRATION=1`; ready to run against a real kiro-cli when the env gate is set

**Human verification — PENDING:**
- **LangFlow zero-reconfig acceptance** (SURF-05 + Phase 2 SC #2) is a manual gate that cannot be automated. The acceptance protocol from the plan's `<how-to-verify>` section:
  1. Verify the integration test passes locally:
     ```
     LOOP24_INTEGRATION=1 go test -race -count=1 ./internal/adapter/ollama/... -run TestIntegration -v -timeout 60s
     ```
  2. Start the binary:
     ```
     make build && AUTH_TOKEN= ALLOWED_IPS= POOL_SIZE=1 ./bin/loop24-gateway
     ```
     Expected log lines: `pool warmup complete` (or equivalent) → `listening addr=:11434`.
  3. Smoke /api/chat with `curl -X POST http://localhost:11434/api/chat -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'` → Ollama-shape JSON.
  4. Smoke /api/tags and /api/version with `curl http://localhost:11434/api/tags` and `curl http://localhost:11434/api/version`.
  5. Open an existing LangFlow flow whose Ollama component points at `http://localhost:11434/api/chat`, make NO modifications, run the flow with a simple chat input, confirm the response renders correctly.
  6. Auth smoke test with `AUTH_TOKEN=s3cret` → 401 without bearer, 200 with valid bearer, 200 on exempt paths regardless.

The LangFlow human-verify checkpoint is the only gate this executor cannot complete autonomously. All other Phase 2 acceptance gates (SC #1 / #3 / #4 / #5) are programmatic and passing.

## Next Phase Readiness

- **Phase 2 functionally complete pending the LangFlow human-verify gate** — every requirement ID (SURF-01/03/05/07, AUTH-01..03, OBSV-01, ACP-07, POOL-02) has executable surface and (where automatable) test coverage.
- **Phase 3 (OpenAI adapter) can begin immediately** — it imitates the same adapter-over-canonical pattern: `internal/adapter/openai` declaring its own consumer interfaces, mounted via a second `server.Route(cfg.OpenAIPathPrefix, ...)` block in server.NewFromConfig. server.Config already accepts the future addition without rewrite.
- **Phase 3.1 (Anthropic SSE) similarly unblocked** — same pattern, plus the canonical.ChatRequest.ResourceLinks field (already populated by Plan 04's pickCwd consumer) is the surface anthropic needs for resource_link content blocks.
- **No new blockers introduced.** The pre-existing Phase 1 blocker about Pi SDK env-var discovery (PROJECT.md Context — Clients) is still open and will surface for Phase 3.

---
*Phase: 02-ollama-end-to-end*
*Completed: 2026-05-24*

## Self-Check: PASSED

Files claimed exist:
- FOUND: internal/adapter/ollama/adapter.go
- FOUND: internal/adapter/ollama/decode.go
- FOUND: internal/adapter/ollama/decode_test.go
- FOUND: internal/adapter/ollama/handlers.go
- FOUND: internal/adapter/ollama/handlers_test.go
- FOUND: internal/adapter/ollama/render.go
- FOUND: internal/adapter/ollama/stub.go
- FOUND: internal/adapter/ollama/stub_test.go
- FOUND: internal/adapter/ollama/wire.go
- FOUND: internal/adapter/ollama/wire_test.go
- FOUND: internal/adapter/ollama/integration_test.go
- FOUND: cmd/loop24-gateway/main_test.go
- FOUND: cmd/loop24-gateway/main.go (modified)
- FOUND: internal/server/server.go (modified)
- FOUND: internal/server/health.go (modified)
- FOUND: internal/server/server_test.go (modified)
- FOUND: scripts/loop24 (modified)
- FOUND: scripts/loop24.ps1 (modified)

Commits claimed exist:
- FOUND: e74573c (feat(02-06): ollama adapter — wire + render + handlers + stub + decode helper)
- FOUND: 6bd21db (feat(02-06): server NewFromConfig + main.go pool→engine→ollama wiring + wrapper env-vars)
- FOUND: 18ee206 (test(02-06): integration test against real kiro-cli for /api/chat + /api/tags)
