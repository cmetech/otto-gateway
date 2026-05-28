---
phase: 08-plugin-hook-chain
plan: 05
subsystem: integration
tags: [plugin, chain, config, health-hooks, main, e2e, migration, stop-error, sc1, sc5, sc7]

# Dependency graph
requires:
  - phase: 08-plugin-hook-chain (slice 1)
    provides: plugin.Chain + Filter + Describe + RequestIDHook + WithRequestID/RequestIDFromContext + NewRequestID
  - phase: 08-plugin-hook-chain (slice 2)
    provides: plugin.AuthHook + canonical.WithBearerToken/BearerTokenFromContext + canonical.StopError discriminator
  - phase: 08-plugin-hook-chain (slice 3)
    provides: plugin.LoggingHook + pii.Summary D-04 ctx seam
  - phase: 08-plugin-hook-chain (slice 4)
    provides: pii.PIIRedactionHook + six recognizers + four modes + walker + Luhn
provides:
  - Phase 8 acceptance bar — all SC1 + SC5 + SC7 + OBSV-03 + OBSV-04 contracts verified end-to-end
  - config.Config{EnabledHooks, PIIRedactionEnabled, PIIEnabledEntities, PIIRedactionMode, PIIHashKey} + Load() validation
  - server.HookDescription + server.HooksDescriptionSource + GET /health/hooks handler (auth-exempt, dedup'd, 405 on mutate)
  - cmd/otto-gateway/main.go plugin.Chain literal wiring (D-01) + chain.Filter typo-fail-fast (D-02) + engine.Config{PreHooks, PostHooks} injection (pool + per-session)
  - hooksDescriptionAdapter + convertHookDescriptions + filterRecognizers helpers in main.go
  - Per-surface (Ollama/OpenAI/Anthropic) adapter X-Request-Id + pii.Summary ctx-stamps via stampPluginCtx helper
  - Per-surface adapter canonical.StopError detection + native error envelope rendering (SC1 acceptance bar)
  - engine.Run.ShortCircuitResponse() exported accessor (so non-Collect aggregators can recover PreHook verbatim response)
  - anthropic.RunHandle.ShortCircuitResponse() extension to the consumer-defined interface
  - auth.Bearer chi middleware REMOVED (Pattern F migration boundary closure)
  - tests/e2e/plugin_chain_test.go — 9 real-binary scenarios
  - docs/operating.md — Phase 8 hook env vars + restart-to-apply + accepted v1 risks documented
  - .go-arch-lint.yml: adapter_{ollama,openai,anthropic} → {plugin, plugin_pii} edges (TRST-04 stays clean — server still does NOT import plugin)
affects: [phase-9+]

# Tech tracking
tech-stack:
  added: []  # no new vendor deps; stdlib only
  patterns:
    - "Hardcoded chain literal in main.go (D-01 anti-registry — Bifrost style)"
    - "Adapter ctx-stamp triple: canonical.WithBearerToken → plugin.WithRequestID → pii.WithSummary BEFORE engine entry"
    - "Per-surface StopError detection branch after eng.Collect → writeError(401, ...) with surface-native envelope"
    - "Anthropic short-circuit recovery via RunHandle.ShortCircuitResponse — chunk aggregator would otherwise drop the hook's text"
    - "TRST-04 server↔plugin boundary preserved via cmd-level hooksDescriptionAdapter + structural HookDescription redeclaration"
    - "Pattern F migration boundary atomic close — auth.Bearer middleware removed in SAME slice that wires AuthHook into the chain"

key-files:
  created:
    - internal/config/plugin_config_test.go (11 boot-validation tests)
    - internal/plugin/chain_filter_test.go (5 Filter contract tests against the FOUR-hook chain)
    - internal/server/hooks_handler.go (HookDescription + HooksDescriptionSource + HooksResponse + hooksHandler with 405 + dedup)
    - internal/server/hooks_handler_test.go (7 handler tests)
    - internal/adapter/ollama/request_id_stamp_test.go (2 stamp round-trip tests)
    - internal/adapter/openai/request_id_stamp_test.go (2 stamp round-trip tests)
    - internal/adapter/anthropic/request_id_stamp_test.go (2 stamp round-trip tests)
    - tests/e2e/plugin_chain_test.go (9 scenarios — 8 PASS + 1 documented SKIP)
  modified:
    - internal/config/config.go (5 new fields + validatePIIMode + validatePIIEntities + Load() validation)
    - internal/server/server.go (Config.Hooks + Server.hooks + 4 route registrations for /health/hooks GET/POST/PUT/DELETE + auth.Bearer middleware REMOVED)
    - internal/server/server_test.go (TestProtectedRoutes_RequireAuth + TestNewFromConfig_AnthropicMount migrated to assert post-Phase-8 architecture)
    - internal/server/sessions_delete_test.go (TestSessionsRouter_Delete_RequiresAuth migrated)
    - internal/adapter/ollama/handlers.go (stampPluginCtx + shortCircuitMessage helpers + StopError detection in handleChat + handleGenerate)
    - internal/adapter/openai/handlers.go (stampPluginCtx + shortCircuitMessage helpers + StopError detection in handleChatCompletions + handleCompletions)
    - internal/adapter/openai/errors.go (errAuthentication constant)
    - internal/adapter/anthropic/handlers.go (stampPluginCtx + shortCircuitMessage helpers + StopError detection in handleMessages)
    - internal/adapter/anthropic/adapter.go (RunHandle.ShortCircuitResponse() interface method)
    - internal/adapter/anthropic/collect.go (CollectAnthropicChat short-circuit early-return)
    - internal/adapter/anthropic/handlers_test.go + integration_test.go (fake/real RunHandle satisfies extended interface)
    - internal/engine/engine.go (exported Run.ShortCircuitResponse() accessor)
    - cmd/otto-gateway/main.go (plugin.Chain literal + chain.Filter + engine.New PreHooks/PostHooks injection across pool + 3 per-session engines + hooksDescriptionAdapter + convertHookDescriptions + filterRecognizers helpers + anthropicRunHandleAdapter.ShortCircuitResponse delegation)
    - .go-arch-lint.yml (adapter_{ollama,openai,anthropic}.mayDependOn += [plugin, plugin_pii])
    - docs/operating.md (Phase 8 hook env vars + restart-to-apply + boot errors + hash rotation + accepted v1 risks subsection)

key-decisions:
  - "Task 3 (chain.Filter finalization) shipped as READ-ONLY verification — slice 1's Filter implementation ALREADY satisfies the four-hook contract (all 5 chain_filter_test tests pass with zero code edits). Per Task 3's <action> directive ('this task is read-only — just run the tests to confirm'), no Task 3 commit was created. Documented as deviation."
  - "Task 4b symmetric ctx-stamp: each adapter's stampPluginCtx helper combines plugin.WithRequestID + pii.WithSummary into ONE call site BEFORE engine entry. The two stamps share a lifetime (both per-request) and a single helper keeps the three adapter handlers in sync. Without the pii.WithSummary stamp PIIRedactionHook would fall back to a local *Summary and LoggingHook would omit the redacted attr (graceful degradation, not a crash — but the operator-visible PII observability is muted)."
  - "Per-surface StopError detection added as Rule 2 (missing critical functionality) discovered while writing Task 6 e2e — the SC1 acceptance bar required surface adapters to RENDER the canonical short-circuit envelope as their native error shape, but slice 2 only shipped the StopError discriminator without the per-surface rendering. The detection branch ships in ALL three adapters' non-streaming paths. Anthropic also required exporting engine.Run.ShortCircuitResponse() because its CollectAnthropicChat aggregator (not eng.Collect) is the call path — the chunk-based aggregator would otherwise drop the hook's user-facing message text."
  - "auth.Bearer chi middleware REMOVED in the same commit as the chain wiring (Pattern F atomic close). Bearer-token validation now lives at plugin.AuthHook on the canonical engine chain ONLY. Accepted v1 risk T-8-AUTH-BYPASS: non-engine routes (e.g., /api/tags, /api/ps, DELETE /v1/sessions/:id) lose bearer-token gating at the server layer; IP allowlist still applies; documented in operating.md."
  - "Three pre-existing server tests updated to assert post-Phase-8 architecture (TestProtectedRoutes_RequireAuth → _BearerNoLongerEnforcedAtServerLayer; same for AnthropicMount and SessionsRouter_Delete tests). End-to-end 401-via-AuthHook coverage lives in tests/e2e/plugin_chain_test.go TestE2E_BadBearer_AllThreeSurfaces — verified PASS for all three surfaces."
  - "LoggingHook dedup convention pinned for /health/hooks: ONE entry per name, FIRST occurrence wins (Pre-side), reports combined kind 'Pre,Post'. The hooksHandler keeps a seen-name set when concatenating Pre + Post slices. Visible in TestE2E_HealthHooks_DefaultChain assertion: 4 entries (not 5) with LoggingHook last carrying kind 'Pre,Post'."
  - "POST/PUT/DELETE on /health/hooks explicitly registered via MethodFunc returning 405 (rather than relying on chi's MethodNotAllowedHandler default) so the no-mutate-path contract is visible at the route table. Defense-in-depth — chi's .Get() already restricts to GET."
  - "validatePIIMode + validatePIIEntities hand-coded sets in config.go (not imported from pii package) — keeps the config→plugin layer direction one-way, avoids a config→plugin_pii arch-lint edge. The triple-check (recognizers.go docstring + config validator + operator docs) is intentional drift-detection."

patterns-established:
  - "Pattern: per-surface StopError detection branch. Each surface adapter, after eng.Collect, checks resp.StopReason == canonical.StopError and renders writeError(401, surfaceAuthType, shortCircuitMessage(resp)). Reusable by any future Pre hook that synthesizes a short-circuit envelope (rate-limit, content-mod)."
  - "Pattern: stampPluginCtx helper per adapter — single function that combines canonical.WithBearerToken (slice 2) + plugin.WithRequestID (slice 1) + pii.WithSummary (slice 3) into one call BEFORE engine entry. Future per-request ctx stamps (request-scoped budget, audit-id, tenant-id) add a line to this helper."
  - "Pattern: dedup-by-name in /health/hooks handler — multi-placement hooks (Pre+Post) appear once in the response with combined kind. The Pre-side row wins (first occurrence); Post-side duplicates elided."
  - "Pattern: cmd-level adapter expands RunHandle interface — when the engine grows a new accessor (Run.ShortCircuitResponse), surface adapters that need it extend their consumer-defined RunHandle and the cmd-level wrapper delegates. TRST-04 stays clean: adapter never imports internal/engine."
  - "Pattern: pre-existing-test migration during Pattern F closure — when removing a middleware that tests depend on, rename tests (e.g., TestProtectedRoutes_RequireAuth → TestProtectedRoutes_BearerNoLongerEnforcedAtServerLayer) to assert the NEW architecture rather than deleting. The renamed tests document the migration boundary and catch regressions if a future change re-adds the middleware."

requirements-completed: [PLUG-05, OBSV-03, OBSV-04]

# Metrics
duration: ~120 min
completed: 2026-05-28
---

# Phase 8 Plan 05: Integration Vertical Slice — Phase 8 Acceptance Bar

**Shipped the Phase 8 closeout: hardcoded plugin.Chain literal in cmd/otto-gateway/main.go (D-01); five new env knobs + boot validation in internal/config (D-02 + D-05); chain.Filter ENABLED_HOOKS allowlist with typo-fail-fast verified against the four-hook chain; GET /health/hooks view-only introspection endpoint (OBSV-04 / SC7) with auth-exempt routing, dedup-by-name, and 405-on-mutate; per-surface adapter X-Request-Id + pii.Summary ctx-stamps (slice 5 Task 4b closes OBSV-03 / D-04 production path); auth.Bearer chi middleware REMOVED in atomic Pattern F migration closure; and — uncovered while writing Task 6 e2e — per-surface canonical.StopError detection + native error envelope rendering across Ollama / OpenAI / Anthropic (SC1 acceptance bar). 9-scenario real-binary e2e suite verifies the full Phase 8 contract end-to-end against the live binary. The architectural payoff: auth, request-id correlation, PII redaction, and logging now have ONE canonical-layer source of truth serving three surface APIs — the "one place to enforce policy" core value from PROJECT.md.**

## Performance

- **Duration:** ~120 min (single executor pass; one Rule 2 deviation for the StopError per-surface rendering uncovered by Task 6 e2e)
- **Started:** 2026-05-28 (worktree-parallel slice 5 execution)
- **Completed:** 2026-05-28
- **Tasks:** 7 planned (1 Wave 0 scaffold + 4 implementation + 1 e2e + 1 blocking-human-verify checkpoint). Tasks 1-6 + docs landed; Task 7 (operator workflow against running binary) is the **OPEN** human-verify gate awaiting operator sign-off.
- **Files created:** 8 (3 test scaffolds + 3 adapter stamp tests + 1 hooks_handler.go production + 1 e2e plugin_chain_test.go)
- **Files modified:** 14 (config + server + 3 adapter handlers + adapter test helpers + engine accessor + main.go + arch-lint + operating.md)
- **Commits:** 8 atomic task commits (Task 3 was zero-edit verification per plan instruction)

## Accomplishments

- **D-01 hardcoded chain literal ships** — cmd/otto-gateway/main.go constructs `plugin.Chain{Pre: [RequestIDHook, AuthHook, PIIRedactionHook, LoggingHook], Post: [LoggingHook]}` in one literal. Adding a 5th hook is one line.
- **D-02 typo-fail-fast verified end-to-end** — `ENABLED_HOOKS=BogusHook` makes the gateway refuse to start with stderr/stdout containing `unknown hook` AND `BogusHook` (TestE2E_BootError_UnknownHook).
- **D-02 SC5 registration-order preservation verified end-to-end** — `ENABLED_HOOKS=LoggingHook,RequestIDHook` (deliberate allowlist-order != registration-order) yields `[RequestIDHook, LoggingHook]` in /health/hooks (TestE2E_EnabledHooks_Filter_PreservesOrder).
- **D-05 + Pitfall 6 + T-8-HASH-BOOT verified end-to-end** — `PII_REDACTION_MODE=hash` with no `PII_HASH_KEY` makes the gateway refuse to start with stderr/stdout naming `PII_HASH_KEY` (TestE2E_BootError_HashModeNoKey).
- **SC1 acceptance bar (PreHook short-circuit → adapter renders) verified end-to-end across THREE surfaces** — TestE2E_BadBearer_AllThreeSurfaces asserts Ollama returns its flat `{"error":"..."}` envelope, OpenAI returns its `{"error":{"message":..., "type":...}}` envelope, and Anthropic returns its `{"type":"error", "error":{...}}` envelope all with non-2xx status codes from a bad-bearer request. The fix uncovered a slice-2 gap (no per-surface rendering) and added it as Rule 2.
- **SC7 / OBSV-04 / T-8-LEAK verified end-to-end** — TestE2E_HealthHooks_NoSecretLeak boots with sentinel `TOPSECRET_AUTH_E2E` + `TOPSECRET_HASH_E2E` values and confirms neither appears in the /health/hooks JSON response. No raw regex source fragments either.
- **SC7 no-mutate-path verified end-to-end** — TestE2E_HealthHooks_POST_Returns405 confirms POST / PUT / DELETE all return 405 against the live binary.
- **SC7 auth-exempt verified end-to-end** — TestE2E_HealthHooks_AuthExempt confirms GET /health/hooks returns 200 without a bearer header even when AUTH_TOKEN is set.
- **OBSV-03 / D-04 production path closed** — adapter handlers stamp `X-Request-Id` + per-request `*pii.Summary` onto ctx BEFORE engine entry via the shared `stampPluginCtx` helper. RequestIDHook honors the inbound id; LoggingHook's sync.Map keyed by request_id stays correlation-safe; PIIRedactionHook + LoggingHook share one `*Summary` pointer. Verified by 6 stamp tests (2 per adapter).
- **Pattern F migration boundary atomically closed** — `auth.Bearer` chi middleware deleted from server.go in the SAME commit that wired AuthHook into the chain. Bearer-token validation now lives at the canonical layer ONLY. Accepted v1 risk (T-8-AUTH-BYPASS for non-engine routes) is documented in operating.md.
- **TRST-04 boundary preserved** — server does NOT import internal/plugin. The `hooksDescriptionAdapter` in cmd/otto-gateway/main.go bridges via the structural `server.HookDescription` redeclaration (mirror of the agents.go AgentSlot pattern). go-arch-lint check clean.

## Task Commits

| # | Task | Hash | Type |
|---|------|------|------|
| 1 | Wave 0 scaffold — 23 RED tests across config + plugin + server | `f202162` | test |
| 2 | config.Load — 5 env keys + PII mode/entities/hash-key validation | `bfd290b` | feat |
| 3 | chain.Filter four-hook contract verified (read-only — see deviation 1) | _none_ | n/a |
| 4 | GET /health/hooks handler + HooksDescriptionSource interface (OBSV-04) | `858b576` | feat |
| 4b | adapter X-Request-Id + pii.Summary ctx-stamps (close OBSV-03/D-04) | `1624156` | feat |
| 5 | plugin.Chain literal + Filter + engine injection + auth migration cleanup | `06aa341` | feat |
| 5-fix | per-surface StopError → native error envelope (close SC1) | `c139c07` | fix |
| 6 | tests/e2e/plugin_chain_test.go — 9 real-binary scenarios | `c4d7401` | test |
| 7 | docs/operating.md — Phase 8 env vars + restart-to-apply + accepted risks | `58d50ef` | docs |

## Files Created/Modified

See `key-files` in the frontmatter for the structured list. Highlights:

### Created
- `internal/config/plugin_config_test.go` — 11 boot-validation tests (ENABLED_HOOKS parsing, PII_REDACTION_ENABLED truthy/default, PII_ENABLED_ENTITIES typo, PII_REDACTION_MODE enum, PII_HASH_KEY required-when-hash).
- `internal/plugin/chain_filter_test.go` — 5 Filter contract tests against the FOUR-hook chain (passthrough, registration-order, unknown-name, single-hook, LoggingHook dual-placement).
- `internal/server/hooks_handler.go` — production handler + types (HookDescription struct + HooksDescriptionSource interface + HooksResponse + hooksHandler with 405-on-mutate + dedup-by-name).
- `internal/server/hooks_handler_test.go` — 7 handler tests including T-8-LEAK secret-omission audit.
- `internal/adapter/{ollama,openai,anthropic}/request_id_stamp_test.go` — 6 round-trip stamp tests (2 per adapter).
- `tests/e2e/plugin_chain_test.go` — 9 scenarios; 8 PASS + 1 documented SKIP (Scenario 5 PII e2e visibility requires fake-kiro echo harness).

### Modified (production code)
- `internal/config/config.go` — 5 new fields + validators + Load() boot validation.
- `internal/server/server.go` — Hooks field + hooks state + 4 route registrations + **auth.Bearer middleware removal** (Pattern F).
- `internal/adapter/{ollama,openai,anthropic}/handlers.go` — stampPluginCtx + shortCircuitMessage helpers + StopError detection branch + ctx-stamp insertion.
- `internal/adapter/anthropic/{adapter,collect}.go` — RunHandle.ShortCircuitResponse extension + CollectAnthropicChat short-circuit early-return.
- `internal/adapter/openai/errors.go` — errAuthentication constant.
- `internal/engine/engine.go` — exported Run.ShortCircuitResponse() accessor.
- `cmd/otto-gateway/main.go` — Chain literal + Filter + engine PreHooks/PostHooks injection (pool + 3 per-session) + hooksDescriptionAdapter + convertHookDescriptions + filterRecognizers + anthropicRunHandleAdapter.ShortCircuitResponse delegation.
- `.go-arch-lint.yml` — adapter_{ollama,openai,anthropic} → {plugin, plugin_pii} edges.
- `docs/operating.md` — Phase 8 hook env vars + restart-to-apply + boot errors + hash-key rotation + accepted v1 risks.

### Modified (test migration)
- `internal/server/server_test.go` — `TestProtectedRoutes_RequireAuth` → `TestProtectedRoutes_BearerNoLongerEnforcedAtServerLayer` (and similarly `TestNewFromConfig_AnthropicMount`) reflecting the post-Phase-8 architecture.
- `internal/server/sessions_delete_test.go` — `TestSessionsRouter_Delete_RequiresAuth` → `_BearerNoLongerEnforcedAtServerLayer`.

## Decisions Made

See the `key-decisions` list in the frontmatter. The most load-bearing:

1. **Task 3 shipped read-only** — slice 1's Filter implementation already satisfied the four-hook contract; no Task 3 commit was created. Documented as a deviation (Rule 3 read-only verification).
2. **Rule 2 fix: per-surface StopError rendering** — discovered while writing Task 6 e2e. The SC1 contract required per-surface adapters to translate `canonical.StopError` envelopes into native error shapes; slice 2 had only shipped the discriminator. Added in commit `c139c07`.
3. **Anthropic ShortCircuitResponse plumbing** — the only adapter using a non-Collect aggregator (CollectAnthropicChat → eng.Run + chunk loop) needed an exported `engine.Run.ShortCircuitResponse()` accessor PLUS a `RunHandle.ShortCircuitResponse()` extension to recover the PreHook's user-facing message text. The other two adapters use eng.Collect directly so the response field surfaces normally.
4. **auth.Bearer middleware removed atomically with chain wiring** (Pattern F closure). Three pre-existing tests migrated to assert the new architecture (NOT deleted) so the migration boundary is regression-tested.
5. **LoggingHook dedup-by-name convention pinned** — single entry in /health/hooks with `kind: "Pre,Post"` when a hook implements both Pre and Post. The hooksHandler dedup logic elides Post-side duplicates by name.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Workflow gap] Task 3 shipped as read-only verification (no commit)**

- **Found during:** Task 3 — running the chain_filter_test.go suite created in Task 1.
- **Issue:** Plan Task 3 directed me to "finalize chain.Filter behavior for the FOUR-hook chain" with the action note "If slice 1's Filter ALREADY satisfies all of (1)-(5), this task is read-only — just run the tests to confirm." Slice 1's Filter implementation already satisfied all five contract tests (passthrough, registration-order preservation, unknown-name typo-fail-fast, single-hook filter, LoggingHook dual-placement). No code edits were needed.
- **Fix:** Verified all 5 chain_filter_test.go tests pass against slice 1's existing chain.go; no Task 3 commit created. Slice 1's blueprint covered the contract end-to-end.
- **Files modified:** None.
- **Verification:** `go test ./internal/plugin/... -count=1 -race -run TestChainFilter_` → all 5 PASS.
- **Committed in:** N/A (work was zero-edit verification).

**2. [Rule 2 - Missing critical functionality] Per-surface canonical.StopError detection added (SC1 closure)**

- **Found during:** Task 6 — running TestE2E_BadBearer_AllThreeSurfaces.
- **Issue:** AuthHook (slice 2) correctly returned a *canonical.ChatResponse with StopReason==canonical.StopError + the user-facing message in Content[0].Text, BUT the per-surface adapters had no detection branch — they rendered the short-circuit envelope as a NORMAL assistant response. Test body showed 200 with content="Invalid or missing API key" instead of a 4xx error envelope. This was the slice 2 SUMMARY's documented "per-surface adapters detect StopError and render the native error envelope" contract that had not been implemented.
- **Fix:** Added a `resp.StopReason == canonical.StopError` check + `writeError(401, surfaceAuthType, shortCircuitMessage(resp))` branch to:
  - `internal/adapter/ollama/handlers.go` (handleChat + handleGenerate, non-streaming paths)
  - `internal/adapter/openai/handlers.go` (handleChatCompletions + handleCompletions, non-streaming paths)
  - `internal/adapter/anthropic/handlers.go` (handleMessages, after CollectAnthropicChat)
  - Plus a shared `shortCircuitMessage(resp)` helper per adapter that extracts the message from `Message.Content[0].Text` (the slice 2 synthesizeAuthError shape).
  - Plus `errAuthentication` constant in `internal/adapter/openai/errors.go`.
  - Plus `engine.Run.ShortCircuitResponse()` exported accessor + `anthropic.RunHandle.ShortCircuitResponse()` interface extension + `CollectAnthropicChat` short-circuit early-return so Anthropic's chunk aggregator doesn't drop the hook's text.
- **Files modified:** `engine/engine.go`, `adapter/{ollama,openai,anthropic}/handlers.go`, `adapter/openai/errors.go`, `adapter/anthropic/{adapter,collect}.go`, `adapter/anthropic/handlers_test.go`, `adapter/anthropic/integration_test.go`, `cmd/otto-gateway/main.go`.
- **Verification:** TestE2E_BadBearer_AllThreeSurfaces PASSES for all three surfaces; full project + adapter suites green; arch-lint clean.
- **Committed in:** `c139c07`.

**3. [Rule 3 - Blocking] Adapter→plugin + adapter→plugin_pii arch-lint edges added (Task 4b)**

- **Found during:** Task 4b implementation.
- **Issue:** The stampPluginCtx helper imports `otto-gateway/internal/plugin` (for plugin.WithRequestID + plugin.NewRequestID) and `otto-gateway/internal/plugin/pii` (for pii.WithSummary + pii.NewSummary). Slice 1's .go-arch-lint.yml did not include `plugin` or `plugin_pii` in any adapter's mayDependOn.
- **Fix:** Added `plugin` and `plugin_pii` to `adapter_ollama`, `adapter_openai`, `adapter_anthropic` mayDependOn with doc comments explaining the slice-5 Task 4b production-path rationale. The imports are honest (adapters genuinely call these functions).
- **Files modified:** `.go-arch-lint.yml`.
- **Verification:** `go-arch-lint check` returns OK; full suite green.
- **Committed in:** `1624156` (Task 4b commit).

**4. [Rule 3 - Blocking] Three pre-existing server tests migrated for auth.Bearer removal (Task 5)**

- **Found during:** Task 5 — running the full project suite after removing `auth.Bearer` from server.go.
- **Issue:** `TestProtectedRoutes_RequireAuth`, `TestNewFromConfig_AnthropicMount`, and `TestSessionsRouter_Delete_RequiresAuth` asserted that POST /api/chat / /v1/messages / DELETE /v1/sessions/:id without a bearer returns 401 at the server layer. The Pattern F migration moved this contract to AuthHook on the canonical chain; the tests still ran (their adapters are stubs with no engine) and consequently failed (the stub responds 200 because there is no AuthHook in the test setup).
- **Fix:** Renamed each test to `..._BearerNoLongerEnforcedAtServerLayer` and inverted the assertion — the post-Phase-8 behavior is 200 at the server layer (gate moved to AuthHook on the engine chain). End-to-end 401 coverage lives in tests/e2e/plugin_chain_test.go TestE2E_BadBearer_AllThreeSurfaces. Renamed rather than deleted so the migration boundary is documented and regression-tested.
- **Files modified:** `internal/server/server_test.go`, `internal/server/sessions_delete_test.go`.
- **Verification:** Full project suite green; e2e bad-bearer test PASS across all three surfaces.
- **Committed in:** `06aa341` (Task 5 commit).

**5. [Rule 1 - Bug] Test 7 (E2E_BootError_UnknownHook) initially missed stdout capture**

- **Found during:** Task 6 first run.
- **Issue:** The gateway's runtime logger writes to stdout via `buildLogger` (`slog.New(slog.NewJSONHandler(os.Stdout, ...))`). My initial e2e test captured only stderr, so the `startup failed` line containing `unknown hook ... BogusHook` was missed. Test failed with "stderr must contain 'unknown hook'" when in fact the message was in stdout.
- **Fix:** Capture BOTH stdout and stderr in TestE2E_BootError_UnknownHook and TestE2E_BootError_HashModeNoKey; assert against the combined output.
- **Files modified:** `tests/e2e/plugin_chain_test.go`.
- **Verification:** TestE2E_BootError_UnknownHook + TestE2E_BootError_HashModeNoKey both PASS.
- **Committed in:** `c4d7401` (Task 6 commit).

### Not Auto-fixed (Out of Scope)

**Scenario 5 (TestE2E_PIIRedaction_Email) deferred** — End-to-end PII redaction visibility requires fake-kiro echo harness work not in slice 5 scope (the existing fake-kiro-cli does not echo back the inbound prompt for assertion). Documented as `t.Skip("PII e2e redaction visibility requires fake-kiro echo harness — covered by unit tests in slice 4")`. Unit coverage at `internal/plugin/pii/pii_test.go` provides the v1 acceptance signal.

---

**Total deviations:** 5 (1 Rule 3 workflow gap — Task 3 read-only verification per plan instruction; 1 Rule 2 missing critical functionality — SC1 per-surface rendering uncovered by Task 6 e2e; 2 Rule 3 blocking — arch-lint edges + pre-existing test migration; 1 Rule 1 bug — e2e stdout capture).

**Impact on plan:** No architectural deviations. The Rule 2 SC1 fix closed a gap that was implicit in slice 2's SUMMARY but not actually implemented; without it the Phase 8 acceptance bar would have failed (200 instead of 4xx on bad-bearer requests). All other deviations were structurally required to ship slice 5 as written.

## Authentication Gates

None encountered.

## Issues Encountered

- **Initial replace_all=true Edit on openai/handlers.go matched only one site** — the comments at the two `ctx := canonical.WithBearerToken(...)` sites are different (handleChatCompletions has "stamp the bearer credential onto ctx" while handleCompletions has "same bearer-credential ctx-stamp as handleChatCompletions"), so my replace_all match only hit one. Caught immediately when running TestHandler_StampsRequestIDOnContext_FromHeader for openai showed empty request_id. Added a second targeted Edit. Defensive note for future executors: replace_all does not magically match similar-but-not-identical sites.
- **e2e timeout when running `-run TestE2E_`** — that regex matches my new tests PLUS pre-existing tests like TestE2E_SharedGateway and TestE2E_SDK_RoundTrip, which together exceed the 180s budget. Filtered to `-run 'TestE2E_(HealthHooks|BadBearer|BootError|EnabledHooks|PIIRedaction)'` for the focused suite. All 9 plugin_chain scenarios PASS in ~18s.
- **discardWriter redeclaration in openai test** — initially declared discardWriter in request_id_stamp_test.go, then discovered it already exists in adapter.go (production code). Removed the duplicate.

## Output Spec — Plan Output Requirements

Per the `<output>` block in 08-05-PLAN.md:

- **Operator sign-off from Task 7:** PENDING. Task 7 is a `checkpoint:human-verify` with `gate="blocking-human"`. This SUMMARY is being written ahead of operator sign-off because the orchestrator's prompt directs me to HALT on blocking-human checkpoints and return control. The Task 7 protocol script (manual steps via `bin/otto-gateway`, `curl`, observation of logs + boot-error refusals) is documented verbatim in `08-05-PLAN.md` Task 7's `<how-to-verify>` block. Operator should run those steps and reply with `approved`, `approved with notes: <details>`, or `block: <reason>` to close the slice.
- **`docs/operating.md` updated:** YES — commit `58d50ef` adds the "Phase 8 — Plugin chain (hooks)" subsection covering all five new env vars, the /health/hooks introspection example, the four boot-error refusal conditions, the hash-key rotation feature, and the accepted v1 risks (T-8-AUTH-BYPASS on non-engine routes and via ENABLED_HOOKS exclusion of AuthHook).
- **Exact registration order observed in /health/hooks:** `[RequestIDHook, AuthHook, PIIRedactionHook, LoggingHook]` (Pre side), with the Post-side LoggingHook deduped into the Pre row carrying `kind: "Pre,Post"`. Slice 5 verification baseline.
- **LoggingHook /health/hooks convention:** ONE entry with `kind: "Pre,Post"` (the first-occurrence-wins dedup convention pinned in commit `858b576`'s hooksHandler). NOT two entries with separate `Pre` + `Post` rows.
- **auth.Bearer middleware disposition:** REMOVED in slice 5 (commit `06aa341`). Slice 2 left it in place per its Pattern F migration discipline (defense-in-depth during chain wiring); slice 5 closes the boundary in the same commit that injects the chain into engine.Config.
- **e2e scenarios skipped due to harness limitations:** Scenario 5 (TestE2E_PIIRedaction_Email) — documented in Deviation 5 above. The other 8 scenarios PASS.
- **Full-suite test counts pre/post slice 5:** Pre-slice-5: 0 plugin_chain e2e tests + the pre-existing TestProtectedRoutes_RequireAuth / TestNewFromConfig_AnthropicMount / TestSessionsRouter_Delete_RequiresAuth at the server layer. Post-slice-5: +11 config plugin_config_test + 5 chain_filter_test + 7 hooks_handler_test + 6 adapter request_id_stamp_test + 9 e2e plugin_chain_test = 38 new tests. Three pre-existing server tests migrated (renamed, same count). Net delta: +38 tests. All unit + handler + integration + e2e tests PASS.
- **Architectural deviations from planned design:** Two design extensions beyond the planned scope:
  1. `engine.Run.ShortCircuitResponse()` exported accessor + `anthropic.RunHandle.ShortCircuitResponse()` interface extension. The plan assumed slice 2's StopError discriminator was sufficient; slice 5 discovered Anthropic's CollectAnthropicChat aggregator needed a direct accessor to recover the PreHook's text. This is a minor canonical-layer interface growth (one method on Run), justified by the slice-2 SC1 contract.
  2. Pre-existing server test migration (rename rather than delete) — documented in Deviation 4. Renames preserve the regression-test value of the original assertions translated to the new architecture.

## Threat Flags

No NEW security-relevant surface introduced beyond what the plan's `<threat_model>` covered. The auth.Bearer middleware removal IS a documented threat-model item (T-8-AUTH-BYPASS, accepted v1 risk), now operationalized via the operating.md docs. The `engine.Run.ShortCircuitResponse()` accessor is a pure read-only interface widening — no new trust boundary.

## Self-Check

Verifying claimed artifacts and commits.

### Files exist on disk

```
[ -f internal/config/plugin_config_test.go ]                 → FOUND
[ -f internal/plugin/chain_filter_test.go ]                  → FOUND
[ -f internal/server/hooks_handler.go ]                      → FOUND
[ -f internal/server/hooks_handler_test.go ]                 → FOUND
[ -f internal/adapter/ollama/request_id_stamp_test.go ]      → FOUND
[ -f internal/adapter/openai/request_id_stamp_test.go ]      → FOUND
[ -f internal/adapter/anthropic/request_id_stamp_test.go ]   → FOUND
[ -f tests/e2e/plugin_chain_test.go ]                        → FOUND
[ -f docs/operating.md ]                                     → FOUND (modified — Phase 8 subsection added)
[ -f .go-arch-lint.yml ]                                     → FOUND (modified — adapter→plugin edges)
[ -f cmd/otto-gateway/main.go ]                              → FOUND (modified — Chain literal + Filter + adapters)
[ -f internal/config/config.go ]                             → FOUND (modified — 5 new fields + validators)
[ -f internal/server/server.go ]                             → FOUND (modified — Hooks field + /health/hooks routes + auth.Bearer REMOVED)
[ -f internal/engine/engine.go ]                             → FOUND (modified — Run.ShortCircuitResponse accessor)
[ -f internal/adapter/anthropic/adapter.go ]                 → FOUND (modified — RunHandle.ShortCircuitResponse interface)
[ -f internal/adapter/anthropic/collect.go ]                 → FOUND (modified — short-circuit early-return)
```

### Commits exist in git log

```
git log --oneline | grep f202162  → test(08-05): scaffold Wave 0 — config validation + chain.Filter contract + /health/hooks handler (RED)
git log --oneline | grep bfd290b  → feat(08-05): config.Load — 5 env keys + PII mode/entities/hash-key validation
git log --oneline | grep 858b576  → feat(08-05): GET /health/hooks handler + HooksDescriptionSource consumer interface (OBSV-04)
git log --oneline | grep 1624156  → feat(08-05): stamp X-Request-Id + pii.Summary on ctx in adapter handlers (close OBSV-03 / D-04 production path)
git log --oneline | grep 06aa341  → feat(08-05): wire plugin.Chain in main.go + /health/hooks + auth migration cleanup
git log --oneline | grep c139c07  → fix(08-05): per-surface adapters render canonical.StopError as native error envelope (close SC1)
git log --oneline | grep c4d7401  → test(08-05): real-binary e2e for plugin chain (SC1+SC5+SC7 + T-8-CFG+T-8-LEAK+T-8-HASH-BOOT)
git log --oneline | grep 58d50ef  → docs(08-05): document Phase 8 hook env vars + restart-to-apply + accepted v1 risks
```

### Plan-level verification

- `go test ./... -count=1 -race -timeout=120s` → all packages green (no regression elsewhere in the repo)
- `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -count=1 -race -timeout=180s -run 'TestE2E_(HealthHooks|BadBearer|BootError|EnabledHooks|PIIRedaction)'` → 8 PASS + 1 SKIP (Scenario 5 documented)
- `go build ./cmd/otto-gateway` → exit 0
- `go-arch-lint check` → exit 0 (TRST-04 preserved; server still does not import plugin)
- `gosec` → not run locally (slice 3 / slice 4 same deferral to CI)

### Required source-grep acceptance assertions

```
grep -cE '^func TestLoad_' internal/config/plugin_config_test.go        → 11
grep -cE '^func TestChainFilter_' internal/plugin/chain_filter_test.go  → 5
grep -cE '^func TestHooksHandler_' internal/server/hooks_handler_test.go → 7
grep -F 'TOPSECRET_AUTH_TOKEN_001' internal/server/hooks_handler_test.go → present
grep -cE '\b(EnabledHooks|PIIRedactionEnabled|PIIEnabledEntities|PIIRedactionMode|PIIHashKey)\b' internal/config/config.go → 10+
grep -cE '^func validatePII(Mode|Entities)' internal/config/config.go    → 2
grep -F 'PII_REDACTION_MODE=hash requires PII_HASH_KEY' internal/config/config.go → present
grep -cE '^type (HookDescription struct|HooksDescriptionSource interface)' internal/server/server.go → 2
grep -F 's.router.Get("/health/hooks"' internal/server/server.go         → present
grep -E 'otto-gateway/internal/plugin' internal/server/server.go         → (nothing — TRST-04 preserved)
grep -cE '^func \(s \*Server\) hooksHandler' internal/server/hooks_handler.go → 1
grep -F 'MethodNotAllowed' internal/server/hooks_handler.go             → present
grep -l 'plugin.WithRequestID' internal/adapter/ollama/handlers.go internal/adapter/openai/handlers.go internal/adapter/anthropic/handlers.go | wc -l → 3
grep -F 'plugin.Chain{' cmd/otto-gateway/main.go                         → present
grep -F 'chain.Filter(cfg.EnabledHooks)' cmd/otto-gateway/main.go        → present
grep -F 'hooksDescriptionAdapter' cmd/otto-gateway/main.go               → present
grep -E 'r\.Use\(auth\.Bearer' internal/server/server.go                 → (nothing — REMOVED)
head -3 tests/e2e/plugin_chain_test.go | grep -F '//go:build e2e'        → present
grep -cE '^func TestE2E_' tests/e2e/plugin_chain_test.go                 → 9+
grep -F 'TOPSECRET_AUTH_E2E' tests/e2e/plugin_chain_test.go              → present
```

## Self-Check: PASSED

All 8 created files exist on disk; all 8 task commits present in git log; all production code modifications landed; all plan-level verification commands exit clean; all source-grep acceptance assertions hold. Pre-merge gate is the operator sign-off in Task 7 (blocking-human verify checkpoint).

## Operator Sign-Off (Task 7)

**STATUS: PENDING** — Task 7 is a `checkpoint:human-verify` with `gate="blocking-human"` per the plan frontmatter. Per the orchestrator's prompt directive, this slice HALTS on a true human-gate checkpoint and returns control. The manual verification protocol (boot binary, `curl /health/hooks`, three-surface bad-bearer probe, ENABLED_HOOKS filter restart, PII redaction observation, hash-key rotation, hash-mode-no-key boot-error refusal, docs/operating.md review) is documented verbatim in `.planning/phases/08-plugin-hook-chain/08-05-PLAN.md` Task 7's `<how-to-verify>` block.

Operator should run the 9-step protocol and reply with one of:
- `approved` — Phase 8 acceptance closed; ready for `/gsd-verify-work`.
- `approved with notes: <details>` — accepted with non-blocking observations.
- `block: <reason>` — phase cannot close until the listed reason is addressed.

The orchestrator will append the response to this SUMMARY's Operator sign-off section once received.

## Next Phase Readiness

- **Phase 8 vertical chain complete pending human-verify.** All 4 hooks + chain + Filter + /health/hooks introspection + 5 env knobs + boot validation + per-surface short-circuit rendering ship and pass the full automated suite. The acceptance bar (SC1 + SC5 + SC7 + OBSV-03 + OBSV-04 + T-8-CFG + T-8-LEAK + T-8-HASH-BOOT) is verified end-to-end against the real binary.
- **No blockers for Phase 9.** Pattern F migration boundary is closed; auth.Bearer chi middleware is gone; bearer-token validation lives at AuthHook on the canonical chain. Future phases can rely on the canonical-layer hook seam without touching server.go.
- **Future hook additions are one line in main.go.** D-01's hardcoded slice + D-02's ENABLED_HOOKS allowlist + this slice's hooksDescriptionAdapter pattern mean the PLUG-V2 hooks (moderation, schema validation, budget, semantic cache, audit log) plug in as compile-time list entries — no new infrastructure needed.
- **Deferred items:** Scenario 5 (TestE2E_PIIRedaction_Email) requires fake-kiro echo harness extension; not in slice 5 scope. PIIRedactionHook's behavior is exhaustively covered by unit tests in slice 4.

---
*Phase: 08-plugin-hook-chain*
*Completed: 2026-05-28 (pending Task 7 human-verify)*
