---
phase: 08-plugin-hook-chain
verified: 2026-05-27T22:55:00Z
status: human_needed
score: 14/14 must-haves verified
overrides_applied: 0
re_verification: null
human_verification:
  - test: "Boot the gateway binary and curl /health/hooks against the live process"
    expected: "200 JSON envelope with 4 hooks [RequestIDHook, AuthHook, PIIRedactionHook, LoggingHook] in registration order; LoggingHook reports kind=\"Pre,Post\"; no secrets in config"
    why_human: "Plan 08-05 Task 7 is a blocking-human verify checkpoint covering operator-facing live-binary smoke tests beyond the automated e2e harness"
  - test: "Send bad-bearer requests to all three surfaces (Ollama /api/chat, OpenAI /v1/chat/completions, Anthropic /v1/messages) against a binary with AUTH_TOKEN set"
    expected: "Each surface returns its native error envelope shape: Ollama flat {\"error\":\"...\"}; OpenAI {\"error\":{\"message\":..., \"type\":...}}; Anthropic {\"type\":\"error\", \"error\":{...}}; status code 401"
    why_human: "Plan 08-05 Task 7 protocol — operator confirms the canonical short-circuit→per-surface rendering against a real binary, not the fake-kiro test harness"
  - test: "Restart the binary with a custom ENABLED_HOOKS list (e.g., RequestIDHook,LoggingHook excluding AuthHook + PIIRedactionHook) and curl /health/hooks"
    expected: "Filtered chain visible in /health/hooks; chain reflects the operator's allowlist; registration order preserved (NOT allowlist order)"
    why_human: "Plan 08-05 Task 7 protocol — operator observes the live filter behavior end-to-end"
  - test: "Run a PII redaction scenario against a live binary: PII_REDACTION_ENABLED=true, PII_REDACTION_MODE=replace, send a request containing an email address, observe redacted output + LoggingHook redacted={Email:1} summary line"
    expected: "Inbound 'corey@cmetech.io' replaced with '<EMAIL_1>' before reaching kiro-cli; LoggingHook's plugin.after slog record carries the redacted={Email:1} attribute"
    why_human: "Plan 08-05 Task 7 protocol — TestE2E_PIIRedaction_Email is documented SKIP pending fake-kiro echo harness; this protocol is the live-binary equivalent"
  - test: "Rotate PII_HASH_KEY (start with one key, observe a hash tag; restart with a different key, observe a different tag for the same input)"
    expected: "Hash tag changes after rotation, demonstrating the key-rotation correlation-break feature; documented in docs/operating.md"
    why_human: "Plan 08-05 Task 7 protocol — operational behavior that cannot be observed via grep"
  - test: "Boot with PII_REDACTION_MODE=hash AND empty PII_HASH_KEY; confirm hash-mode-no-key boot-error refusal"
    expected: "Gateway refuses to start; stderr/stdout contains the literal substring \"PII_HASH_KEY\" naming the missing env var"
    why_human: "Plan 08-05 Task 7 protocol — operator confirms the boot-error refusal against the live binary (TestE2E_BootError_HashModeNoKey covers automatedly; Task 7 is the operator visual confirmation)"
  - test: "Review docs/operating.md — confirm Phase 8 hook env vars documented, restart-to-apply rule called out, boot-error refusal conditions enumerated, hash-key rotation workflow described, T-8-AUTH-BYPASS accepted v1 risks documented"
    expected: "All six aspects (env knobs, restart-to-apply, boot errors, /health/hooks introspection, hash-key rotation, accepted v1 risks) present and operator-readable"
    why_human: "Operator must read the documentation as an operator would; coverage grep alone doesn't confirm clarity"
---

# Phase 8: Plugin Hook Chain Verification Report

**Phase Goal:** `PreHook` / `PostHook` interfaces operate on canonical request/response types, with day-one hooks registered: RequestID, Auth (refactored from middleware), structured Logging, and PII Redaction. Short-circuit return from `PreHook` skips the engine. The PII hook ships with an extensible regex+validator recognizer registry — six built-in entities (Email, IPv4, IPv6, SSN, Credit Card with Luhn check, US Phone) and a one-struct addition path for new entities — so future guardrails (moderation, budget, schema, cache, audit) and new PII recognizers land without touching the hook engine.

**Verified:** 2026-05-27T22:55:00Z
**Status:** human_needed
**Re-verification:** No — initial verification

---

## Goal Achievement

### Observable Truths

Sourced from ROADMAP.md `success_criteria[]` (7 criteria) merged with PLAN frontmatter intent + PROJECT.md core value. Each truth was confirmed by source-grep, full test-suite execution, e2e suite execution, and binary build.

| # | Truth (ROADMAP SC) | Status | Evidence |
| - | ------------------ | ------ | -------- |
| 1 | Engine calls `chain.Pre(ctx, canonicalReq)` before any ACP call and `chain.Post(ctx, canonicalReq, canonicalResp)` after; PreHook returning non-nil `*canonical.ChatResponse` short-circuits the engine and the adapter renders that response in its native shape | VERIFIED | `internal/engine/engine.go:161-162` iterates `e.cfg.PreHooks` calling `h.Before(ctx, req)`; `internal/engine/collect.go:118-119` iterates `e.cfg.PostHooks` calling `h.After(ctx, req, resp)`; `internal/adapter/ollama/handlers.go:147-148`, `openai/handlers.go:165-166,274-275`, `anthropic/handlers.go:220-221` each detect `resp.StopReason == canonical.StopError` and call `writeError(401, ...)` with native envelope. End-to-end `TestE2E_BadBearer_AllThreeSurfaces` PASS across ollama/openai/anthropic subtests. |
| 2 | `RequestIDHook` generates `X-Request-Id` or honors inbound; ID appears in every slog record across pre-hook, engine, ACP, post-hook spans for the same request | VERIFIED | `internal/plugin/request_id.go:108-121` reads existing id then generates ULID via `ulid.Make()`, emits `slog.Info("plugin.request_id.generated", "request_id", id)`. `WithRequestID`/`RequestIDFromContext` typed-key ctx primitives (lines 127-141). Adapters stamp via `stampPluginCtx` (ollama/openai/anthropic handlers.go) BEFORE engine entry. LoggingHook reads `RequestIDFromContext` in Before+After (logging.go:149, 191). |
| 3 | `AuthHook` (Pre) validates bearer tokens from `AUTH_TOKEN` and short-circuits with canonical auth-error response that adapter renders correctly for OpenAI `{error:{...}}` vs Ollama `{error:"..."}` (and Anthropic `{type:"error", error:{...}}`) | VERIFIED | `internal/plugin/auth.go:96-117` uses `subtle.ConstantTimeCompare`; returns `synthesizeAuthError(...)` with `StopReason=canonical.StopError`. Per-surface renderers detect StopError at adapter level. `TestE2E_BadBearer_AllThreeSurfaces` PASS verifies all three surfaces. 9 unit tests in `internal/plugin/auth_test.go`. |
| 4 | `LoggingHook` emits structured `Pre` log line on request entry and `Post` log line on response with timing — both via `log/slog` | VERIFIED | `internal/plugin/logging.go:148-167` (`plugin.before` with request_id+model+message_count); lines 190-225 (`plugin.after` with duration_ms via sync.Map LoadAndDelete + stop_reason + optional redacted). 9 unit tests in logging_test.go incl. source-audit guard. Compile-time `_ engine.PreHook` and `_ engine.PostHook` assertions at lines 230-231. |
| 5 | `ENABLED_HOOKS` env var enables/disables registered hooks at boot; hooks execute in registration order; first non-nil short-circuit wins on Pre chain | VERIFIED | `internal/plugin/chain.go:103-154` `Filter()` preserves registration order (lines 116-137), `errors.Join` for multi-typo reports (line 150), returns boot error containing `unknown hook` literal. `cmd/otto-gateway/main.go:192-196` calls `chain.Filter(cfg.EnabledHooks)` and fails fast on error. `internal/config/config.go:242` loads `ENABLED_HOOKS`. 5 tests in `chain_filter_test.go` PASS. `TestE2E_EnabledHooks_Filter_PreservesOrder` + `TestE2E_BootError_UnknownHook` PASS end-to-end. |
| 6 | `PIIRedactionHook` (Pre) walks `canonical.ChatRequest.Messages[].ContentParts[].Text` + registered Recognizers; six built-in (Email, IPv4, IPv6 net.ParseIP, SSN range-filtered, CreditCard Luhn, USPhone); patterns compiled at package init; env knobs `PII_REDACTION_ENABLED`/`PII_ENABLED_ENTITIES`/`PII_REDACTION_MODE`; one-struct addition path; pure-Go no cgo no external deps | VERIFIED | `internal/plugin/pii/recognizers.go:65-71` six `regexp.MustCompile` literals at package init; lines 138-145 `Recognizers []Recognizer` literal slice (one-line addition path verified by struct shape). `validateIPv6NetParseIP` uses `net.ParseIP` (line 96-98); `validateSSNRange` rejects reserved ranges (lines 110-130); `validateLuhn` wraps `LuhnCheck` (luhn.go); CreditCard wired via `Validate: validateLuhn`. `internal/plugin/pii/pii.go:174-254` walks `req.System` + Messages[].Content[].{Text, ToolUse.Input, ToolResult.Content} with per-canonical-value counter for referential identity. Four modes (`replace`/`mask`/`hash`/`drop`) in `modes.go:136-157` with HMAC-SHA256 (NOT raw SHA256) keyed by `PII_HASH_KEY`. 36+ unit tests; all PASS. |
| 7 | `GET /health/hooks` read-only, auth-exempt, returns chain in registration order JSON with name/kind/enabled/config (safe-to-publish); LoggingHook combined "Pre,Post" kind; no runtime mutate path in v1 (405 on POST/PUT/DELETE) | VERIFIED | `internal/server/server.go:220` registers `GET /health/hooks` on outer (auth-exempt) router; lines 225-227 register POST/PUT/DELETE returning 405. `internal/server/hooks_handler.go:81-112` `hooksHandler` returns 405 with Allow: GET on non-GET, JSON envelope `{"hooks":[]}` with dedup-by-name (LoggingHook Pre+Post collapses to one row with kind="Pre,Post"). `cmd/otto-gateway/main.go:504` wires `Hooks: hooksDescriptionAdapter{chain: chain}` preserving TRST-04 (server doesn't import plugin). 7 unit handler tests + e2e `TestE2E_HealthHooks_DefaultChain`/`_AuthExempt`/`_NoSecretLeak`/`_POST_Returns405` all PASS. |

**Score:** 7/7 ROADMAP success criteria verified at code+test+e2e levels.

### Required Artifacts

All Phase 8 artifacts validated for: exists (Level 1), substantive content (Level 2), wired into the chain runtime (Level 3), and (where dynamic) data flows through (Level 4).

| Artifact | Expected | Status | Details |
| -------- | -------- | ------ | ------- |
| `internal/plugin/chain.go` | Chain{Pre,Post} + Filter + Describe + HookDescription + Describer | VERIFIED | 8.3k file; Filter preserves registration order + typo-fail-fast via errors.Join; Describe nil-safe; used by main.go literal + filter call site. |
| `internal/plugin/request_id.go` | RequestIDHook Pre-only + WithRequestID/RequestIDFromContext typed ctx + NewRequestID ULID | VERIFIED | 7k file; Compile-time PreHook (implicit, via Before signature); `ulid.Make()` via oklog/ulid/v2 v2.1.1; unexported `ctxKey` struct prevents cross-package spoof. Used by stampPluginCtx in all three adapters. |
| `internal/plugin/auth.go` | AuthHook (Pre) + constant-time compare + synthesizeAuthError (StopError envelope) | VERIFIED | 5.9k file; `subtle.ConstantTimeCompare` at line 111; `synthesizeAuthError` builds `StopReason=canonical.StopError` envelope; compile-time `_ engine.PreHook = (*AuthHook)(nil)` at line 49. Wired in main.go:175. |
| `internal/plugin/logging.go` | LoggingHook (Pre+Post) + sync.Map timing bridge + nil-Logger fallback + pii.SummaryFromContext consumer | VERIFIED | 9.5k file; `plugin.before`/`plugin.after` slog records with request_id, model, message_count, duration_ms, stop_reason, optional redacted attr; sync.Map keyed by request_id with LoadAndDelete to prevent leak; compile-time PreHook+PostHook assertions at lines 230-231. Single instance reused in main.go:171,183,186 (Pre+Post). |
| `internal/plugin/pii/pii.go` | PIIRedactionHook (Pre) + walker invocation + per-canonical-value counter + Summary populator | VERIFIED | 11k file; reads SummaryFromContext + defensive local fallback; walks System+Text+ToolUse.Input+ToolResult.Content per D-03; counter key `"<entity>|<canonical-value>"` for referential identity; Describe whitelist `{enabled, mode, entities}` only — HashKey never published. Wired in main.go:176-182. |
| `internal/plugin/pii/recognizers.go` | Six recognizers + Recognizer struct + init-time-compiled regex + validators | VERIFIED | 6.3k file; Email/IPv4/IPv6/SSN/CreditCard/USPhone literals at init; `validateIPv4Octets`/`validateIPv6NetParseIP`/`validateSSNRange`/`validateLuhn` wired. `SourceAuditNames()` helper for /health/hooks. |
| `internal/plugin/pii/walk.go` | WalkStrings recursive walker, depth-bounded, string-LEAVES-only, map-key invariance | VERIFIED | 3.4k file; `maxDepth=64`; type-switch over string/map[string]any/[]any with default arm pass-through; map keys preserved verbatim. Property-tested via `testing/quick` MaxCount=1000 (never-panics) + MaxCount=500 (map-key invariance). |
| `internal/plugin/pii/luhn.go` | Luhn validator stdlib-only with 13-19 digit length gate | VERIFIED | 2.3k file; `LuhnCheck()` two-pass mod-10 with `unicode.IsDigit` stripping; length gate enforced. `validateLuhn` closure adapter wired into CreditCard recognizer. |
| `internal/plugin/pii/modes.go` | ApplyMode + HMAC-SHA256 hash (NOT raw) + canonical(value) before hash + UnkeyedHashSentinel + maskValue + drop | VERIFIED | 6k file; `hmac.New(sha256.New, hashKey)` at line 76 (forbidden raw SHA256 form intentionally absent from comments to keep source-audit greps clean); canonicalForm lowercases+trims before HMAC; UnkeyedHashSentinel returned + slog.Warn when key empty (defensive). 8 unit tests pin oracle `<EMAIL:h-5e114e4d>`. |
| `internal/plugin/pii/summary.go` | Summary + RedactionCount + WithSummary/SummaryFromContext + race-safe Add + nil-safe Counts | VERIFIED | 6.5k file; sync.Mutex-guarded Add (race-safe + nil-receiver no-op); WithSummary/SummaryFromContext typed ctx pair (unexported summaryKey struct). |
| `internal/server/hooks_handler.go` | GET /health/hooks handler + HookDescription + HooksDescriptionSource interface + 405 on mutate + LoggingHook dedup-by-name | VERIFIED | 4.5k file; structural HookDescription/HooksDescriptionSource declared here (NOT imported from plugin — TRST-04 preserved); dedup logic at lines 88-105; 405 with Allow: GET; auth-exempt registration at server.go:220. |
| `internal/config/config.go` (Phase 8 additions) | Config fields EnabledHooks/PIIRedactionEnabled/PIIEnabledEntities/PIIRedactionMode/PIIHashKey + Load() validation + boot-error on hash-mode-no-key + validatePIIMode + validatePIIEntities | VERIFIED | Lines 111-148 declare fields; lines 242-265 load+validate; line 264 emits `PII_REDACTION_MODE=hash requires PII_HASH_KEY` boot error. 11 unit tests in `plugin_config_test.go` PASS. |
| `cmd/otto-gateway/main.go` (chain wiring) | plugin.Chain literal (D-01) + chain.Filter typo-fail-fast (D-02) + engine.Config{PreHooks,PostHooks} injection across pool + 3 per-session engines + hooksDescriptionAdapter + auth.Bearer removed | VERIFIED | Lines 172-188 hardcoded literal `[RequestIDHook, AuthHook, PIIRedactionHook, LoggingHook]` (D-04 order); lines 192-196 chain.Filter + boot-error propagation; lines 222-223, 282-283, 291-292, 300-301 inject into pool + 3 per-session engines; line 504 hooksDescriptionAdapter wiring. `auth.Bearer` only appears in code comments — no live `r.Use(auth.Bearer(...))` call remains in server.go (Pattern F closure). |
| `tests/e2e/plugin_chain_test.go` | Real-binary scenarios across all three surfaces + boot-error refusals + /health/hooks shape + filter behavior + secret-leak audit | VERIFIED | 9 TestE2E_* functions; 8 PASS + 1 documented SKIP (TestE2E_PIIRedaction_Email requires fake-kiro echo harness). Full e2e run completed in ~18.7s with `OTTO_E2E=1 go test -tags e2e ./tests/e2e/`. |

### Key Link Verification

| From | To | Via | Status | Details |
| ---- | -- | --- | ------ | ------- |
| `cmd/otto-gateway/main.go` chain literal | `engine.New(engine.Config{PreHooks, PostHooks})` | direct field assignment | WIRED | Lines 222-223 (pool engine), 282-283 + 291-292 + 300-301 (per-session engines). |
| `chain.Filter(cfg.EnabledHooks)` | boot error propagation | `return nil, func() {}, fmt.Errorf("chain filter: %w", filterErr)` | WIRED | Lines 192-196 main.go. e2e `TestE2E_BootError_UnknownHook` PASS confirms refusal. |
| Adapter handlers (3 surfaces) | `plugin.WithRequestID` + `pii.WithSummary` ctx stamps | `stampPluginCtx` per adapter | WIRED | ollama/handlers.go:42-50 + line 100,264; openai/handlers.go:37-45 + line 98,248; anthropic/handlers.go:20-27 + line 151. All three call BEFORE engine entry. |
| Engine PreHook traversal | `h.Before(ctx, req)` | for-range over `e.cfg.PreHooks` | WIRED | engine.go:161-162. Codex H-4 short-circuit preserved (non-nil resp halts loop, returns synthesized Run). |
| Engine PostHook traversal | `h.After(ctx, req, resp)` | for-range over `e.cfg.PostHooks` in Collect | WIRED | collect.go:118-119. Codex H-5 unconditional post-traversal preserved. |
| Per-surface adapters | `canonical.StopError` detection → native error envelope rendering | `resp.StopReason == canonical.StopError` + `writeError(401, ...)` | WIRED | ollama/handlers.go:147,297; openai/handlers.go:165,274; anthropic/handlers.go:220 + RunHandle.ShortCircuitResponse delegation via main.go anthropicRunHandleAdapter. e2e `TestE2E_BadBearer_AllThreeSurfaces` PASS validates all three. |
| `PIIRedactionHook.Before` | `pii.Summary.Add(entity)` | `summary.Add(r.Name)` after each recognizer match | WIRED | pii.go:216 — populates *Summary pointer stamped by adapter middleware (or local fallback). |
| `LoggingHook.After` | `pii.Summary.Counts()` → `slog.Any("redacted", ...)` | `pii.SummaryFromContext(ctx)` | WIRED | logging.go:219-221 — emits only when SummaryFromContext returns ok=true (graceful degradation). Shared *Summary pointer across PII (populator) and Logging (consumer) is established by adapter stampPluginCtx. |
| Server `/health/hooks` handler | plugin.Chain via cmd adapter | `hooksDescriptionAdapter{chain: chain}` | WIRED | main.go:629-654 structural adapter satisfies server.HooksDescriptionSource interface without server importing internal/plugin (TRST-04 preserved per go-arch-lint check OK). |

### Data-Flow Trace (Level 4)

For each dynamic artifact: verify the data source produces real values flowing through the wiring.

| Artifact | Data Variable | Source | Produces Real Data | Status |
| -------- | ------------- | ------ | ------------------ | ------ |
| `/health/hooks` response | `resp.Hooks` slice | `s.hooks.Describe()` → real plugin.Chain.Describe walks the constructed literal | YES — `TestE2E_HealthHooks_DefaultChain` confirms 4-entry chain in registration order with non-empty config rows | FLOWING |
| `plugin.before`/`plugin.after` slog records | request_id, duration_ms, redacted | RequestIDFromContext + sync.Map LoadAndDelete + SummaryFromContext.Counts() | YES — unit tests `captureSlog` decode the records and assert presence of attrs; sync.Map timing bridge verified | FLOWING |
| Per-surface error envelope on bad-bearer | `resp.StopReason` + `Message.Content[0].Text` | AuthHook.synthesizeAuthError populates the canonical envelope; engine.Collect returns it verbatim (Codex H-4) | YES — e2e confirms each surface returns its native error shape with 401 status | FLOWING |
| PII redaction summary | `redacted={Email:N}` map | PIIRedactionHook.Before increments Summary.Add per match; LoggingHook reads via SummaryFromContext | YES (in unit tests + ctx-stamp path); ADAPTER-STAMP DEPENDENCY (production path requires adapter stampPluginCtx to call pii.WithSummary BEFORE engine — verified at handlers.go:48,43,26 across three surfaces) | FLOWING |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
| -------- | ------- | ------ | ------ |
| Binary builds as single static cross-compilable executable | `go build -o /tmp/otto-gateway-verify ./cmd/otto-gateway` | exit 0; 14MB output | PASS |
| Full unit + integration test suite green | `go test -count=1 -race -timeout=120s ./...` | All 17 packages OK | PASS |
| Plugin-only test suite green | `go test ./internal/plugin/... -count=1 -race` | OK 1.5s + OK 1.4s (plugin + plugin/pii) | PASS |
| Config plugin tests green | `go test ./internal/config/ -count=1 -race -run TestLoad_` | 11 tests PASS | PASS |
| Server hooks_handler tests green | `go test ./internal/server/ -count=1 -race -run TestHooksHandler_` | 7 tests PASS | PASS |
| Adapter request-id stamp tests green | `go test ./internal/adapter/{ollama,openai,anthropic}/ -count=1 -race -run TestHandler_StampsRequestIDOnContext` | All 6 PASS | PASS |
| End-to-end plugin_chain real-binary tests | `OTTO_E2E=1 go test -tags e2e ./tests/e2e/ -count=1 -race -run TestE2E_(HealthHooks\|BadBearer\|BootError\|EnabledHooks\|PIIRedaction)` | 8 PASS + 1 documented SKIP in ~18.7s | PASS |
| Architecture lint clean (TRST-04 preserved) | `go-arch-lint check --project-path .` | `OK - No warnings found` | PASS |

### Probe Execution

No conventional `scripts/*/tests/probe-*.sh` probes are declared in PLAN frontmatter or referenced by SUMMARY.md beyond the e2e suite. Phase 8 uses Go's e2e test harness (`tests/e2e/plugin_chain_test.go` with `//go:build e2e`) as the probe equivalent. Result: 8 PASS + 1 documented SKIP. Treated as PASS at the probe-equivalent level.

### Requirements Coverage

Mapped requirement IDs from Phase 8 PLAN frontmatter and ROADMAP scope (PLUG-01..06 + OBSV-03 + OBSV-04). Each maps to ROADMAP success criteria already verified above.

| Requirement | Source Plan | Description | Status | Evidence |
| ----------- | ----------- | ----------- | ------ | -------- |
| PLUG-01 | 08-01, 08-02, 08-03 | PreHook/PostHook interfaces on canonical types; hooks see surface-agnostic data | SATISFIED | engine.PreHook + engine.PostHook interfaces in `internal/engine/hooks.go` operate on `*canonical.ChatRequest`/`*canonical.ChatResponse`. Four hooks implement these without importing any adapter. (See Truth #1.) |
| PLUG-02 | 08-02, 08-05 | PreHook returning non-nil canonical response short-circuits the engine; adapter renders native shape | SATISFIED | engine.go:161-162 short-circuits on non-nil resp; adapters detect `StopReason==canonical.StopError` and render native shapes (ollama flat, openai object, anthropic typed). e2e `TestE2E_BadBearer_AllThreeSurfaces` PASS. (See Truth #1, Truth #3.) |
| PLUG-03 | 08-01, 08-02, 08-05 | Hooks chained in registration order; first non-nil short-circuit wins for Pre; all PostHooks run | SATISFIED | chain.go:103-154 Filter preserves order; engine.go:161-162 loops in slice order with short-circuit-on-non-nil; collect.go:118-119 loops all Posts unconditionally. 5 tests in chain_filter_test.go + 6 in chain_test.go PASS. (See Truth #5.) |
| PLUG-04 | 08-01, 08-02, 08-03 | Day-one hooks RequestIDHook (X-Request-Id generate/propagate), AuthHook (bearer validation), LoggingHook (structured log/slog) | SATISFIED | All three hooks present and wired in main.go:172-188. Bearer validation refactored from `internal/auth/bearer.go` HTTP middleware to plugin.AuthHook on canonical chain (auth.Bearer middleware removed atomically — Pattern F). (See Truths #2, #3, #4.) |
| PLUG-05 | 08-05 | ENABLED_HOOKS env var enables/disables hooks per deployment | SATISFIED | config.go:242 loads ENABLED_HOOKS; main.go:192-196 applies via chain.Filter with typo-fail-fast boot error. e2e `TestE2E_BootError_UnknownHook` + `TestE2E_EnabledHooks_Filter_PreservesOrder` PASS. (See Truth #5.) |
| PLUG-06 | 08-03, 08-04, 08-05 | PIIRedactionHook with extensible Recognizer registry + 6 built-in entities + 4 modes + per-canonical-value counter; pure-Go no cgo no external deps | SATISFIED | All six recognizers compile at package init; Email/IPv4/IPv6 net.ParseIP/SSN range-filter/CreditCard Luhn/USPhone wired in recognizers.go:138-145. Four modes (replace/mask/hash/drop) in modes.go; HMAC-SHA256 (NOT raw SHA256); boot-error on hash-mode-no-key. 36+ unit tests PASS. (See Truth #6.) |
| OBSV-03 | 08-01, 08-03, 08-05 | Structured slog with X-Request-Id correlation across pre-hook, engine, ACP, post-hook spans | SATISFIED | RequestIDFromContext available to every layer; LoggingHook before+after records carry `request_id` attr; adapters stamp BEFORE engine entry via stampPluginCtx. (See Truths #2, #4.) |
| OBSV-04 | 08-05 | GET /health/hooks returns chain JSON with name/kind/enabled/config (safe-to-publish); read-only; auth-exempt; no runtime mutate path | SATISFIED | server.go:220-227 registers GET (auth-exempt) + POST/PUT/DELETE → 405; hooks_handler.go produces dedup-by-name JSON envelope; 7 unit + 4 e2e tests PASS including secret-leak audit. (See Truth #7.) |

Note: REQUIREMENTS.md status mapping table (line 187+) currently lists PLUG-01..05 + OBSV-03 against Phase 8 with status "Pending", but omits PLUG-06 and OBSV-04 from the mapping table even though ROADMAP.md Phase 8 explicitly lists them. This is a REQUIREMENTS.md table omission — the requirements ARE implemented in Phase 8 source code (verified above). REQUIREMENTS.md should be updated to reflect (a) PLUG-06 and OBSV-04 → Phase 8 and (b) all eight requirements → Complete on milestone closeout. This is an informational note, not a Phase 8 deliverable gap.

### Anti-Patterns Found

Scanned key-files modified or created by Phase 8 (excluding `*_test.go`). Searched for `TBD`, `FIXME`, `XXX` (debt markers), `TODO`, `HACK`, `PLACEHOLDER`, "coming soon", "not yet implemented", `return null/[]`/`return Response.json([])` (stub patterns), `console.log` (Go: `fmt.Println` debug), hardcoded empty data in render paths.

| File | Line | Pattern | Severity | Impact |
| ---- | ---- | ------- | -------- | ------ |
| `internal/plugin/pii/modes.go` | 6 | `<ENTITY:h-XXXXXXXX>` (hex placeholder in docstring) | Info | Not a debt marker — hex `X` chars in token format documentation. |
| `internal/plugin/pii/recognizers.go` | 30 | `<NAME:h-XXXX>` (hex placeholder in docstring) | Info | Not a debt marker — hex `X` chars in token format documentation. |

No `TBD`, `FIXME`, `TODO`, or `HACK` debt markers in Phase 8 production source. No unreferenced debt comments. Clean.

### Human Verification Required

The following items must be tested manually by the operator. Plan 08-05 Task 7 is a `checkpoint:human-verify` with `gate="blocking-human"` — the SUMMARY explicitly marks Task 7 as PENDING operator sign-off. The operator should run the following 7-step protocol against a freshly-built `bin/otto-gateway`:

#### 1. /health/hooks introspection (live binary)

**Test:** Build `bin/otto-gateway`, start it, then run `curl http://localhost:18080/health/hooks | jq` (or your configured HTTP_ADDR/port).
**Expected:** 200 JSON envelope with 4 hooks in registration order `[RequestIDHook, AuthHook, PIIRedactionHook, LoggingHook]`; LoggingHook reports `kind: "Pre,Post"` (the Pre+Post dedup convention); no `AUTH_TOKEN`, `PII_HASH_KEY`, or regex source fragments in any `config` block.
**Why human:** Operator-facing visual confirmation of /health/hooks shape on the real binary, beyond what the automated e2e test asserts.

#### 2. Three-surface bad-bearer canonical short-circuit envelope

**Test:** With `AUTH_TOKEN=validtoken` set, send a request with `Authorization: Bearer wrongtoken` to each of:
- `POST /api/chat` (Ollama)
- `POST /v1/chat/completions` (OpenAI)
- `POST /v1/messages` (Anthropic, with `anthropic-version: 2023-06-01` header)

**Expected:**
- Ollama: HTTP 401 + flat `{"error":"Invalid or missing API key"}`
- OpenAI: HTTP 401 + `{"error":{"message":"Invalid or missing API key","type":"authentication_error",...}}`
- Anthropic: HTTP 401 + `{"type":"error","error":{"type":"authentication_error","message":"Invalid or missing API key"}}`

**Why human:** Confirms the canonical→native envelope rendering for each surface client (Pi SDK / LangFlow / loop24-client) against a real binary. The e2e automated coverage uses a Go HTTP client; this manual test uses curl/the actual client SDKs operators care about.

#### 3. ENABLED_HOOKS filter behavior

**Test:** Restart the binary with `ENABLED_HOOKS=RequestIDHook,LoggingHook` (deliberately excluding AuthHook + PIIRedactionHook). `curl /health/hooks | jq`.
**Expected:** Chain shows ONLY RequestIDHook + LoggingHook (in that order — registration order, NOT allowlist order); AuthHook + PIIRedactionHook absent. (Note: this is the documented "T-8-AUTH-BYPASS via ENABLED_HOOKS without AuthHook" accepted v1 risk — operator's explicit choice.)
**Why human:** Operator observes live filter behavior against the binary.

#### 4. PII redaction live observability

**Test:** Restart with `PII_REDACTION_ENABLED=true`, `PII_REDACTION_MODE=replace`, and a real kiro-cli backend or fake-kiro that echoes the inbound prompt. Send a request with the body containing `corey@cmetech.io`. Observe (a) the request kiro-cli sees, (b) the LoggingHook `plugin.after` slog record.
**Expected:**
- Inbound `corey@cmetech.io` replaced with `<EMAIL_1>` before reaching kiro-cli (verifiable via fake-kiro echo or real backend logs).
- `plugin.after` slog record carries `redacted={"Email":1}` attribute.

**Why human:** `TestE2E_PIIRedaction_Email` is documented SKIP pending fake-kiro echo harness extension. This protocol is the live-binary equivalent operators use to confirm PII observability end-to-end.

#### 5. PII_HASH_KEY rotation

**Test:** Start with `PII_REDACTION_MODE=hash` and `PII_HASH_KEY=initial-key-32-bytes-padding-here!!`. Send a request containing `corey@cmetech.io` and observe the hash tag (e.g., `<EMAIL:h-5e114e4d>`). Then restart with `PII_HASH_KEY=rotated-key-32-bytes-padding-here!!` and send the same request.
**Expected:** Hash tag differs between the two boots, demonstrating the documented key-rotation correlation-break feature.
**Why human:** Operational workflow tied to the docs/operating.md hash-rotation runbook; cannot be observed via grep.

#### 6. Hash-mode-no-key boot-error refusal

**Test:** Set `PII_REDACTION_MODE=hash` and leave `PII_HASH_KEY` unset; attempt to start the binary.
**Expected:** Process exits non-zero; stdout/stderr contains the literal substring `PII_HASH_KEY` naming the missing env var.
**Why human:** `TestE2E_BootError_HashModeNoKey` covers this automatedly, but Task 7 protocol calls for operator visual confirmation of the live refusal.

#### 7. docs/operating.md operator-readability review

**Test:** Read `docs/operating.md` Phase 8 subsection.
**Expected:** Documentation covers all of: (a) the five hook env vars (`ENABLED_HOOKS`, `PII_REDACTION_ENABLED`, `PII_ENABLED_ENTITIES`, `PII_REDACTION_MODE`, `PII_HASH_KEY`), (b) the restart-to-apply rule, (c) the four boot-error refusal conditions, (d) the `/health/hooks` introspection endpoint and example, (e) the `PII_HASH_KEY` rotation workflow as a security operational tool, (f) the accepted v1 risks (T-8-AUTH-BYPASS via non-engine routes and via ENABLED_HOOKS exclusion of AuthHook).
**Why human:** Operator must read the documentation as an operator would; coverage grep alone doesn't confirm clarity.

### Gaps Summary

**No automated gaps detected.** All seven ROADMAP success criteria are verified at code+test+e2e levels. All eight Phase 8 requirements (PLUG-01..06, OBSV-03, OBSV-04) are SATISFIED with code-grounded evidence. The full project test suite passes under `-race`; the e2e plugin-chain suite passes 8/8 active scenarios (1 documented SKIP for fake-kiro echo harness limitation). go-arch-lint check is clean — TRST-04 boundary (server does NOT import internal/plugin) preserved via cmd-level `hooksDescriptionAdapter` + structural `server.HookDescription` redeclaration.

The **single outstanding item** blocking phase close-out is the operator sign-off on Plan 08-05 Task 7 — a `checkpoint:human-verify` with `gate="blocking-human"` covering the 7-step manual protocol described above. This is intentional per the plan's design: certain operator workflows (binary boot, live curl, log observation, key rotation, docs review) require human eyes that no automated test can replicate.

**Recommendation:** Phase 8 acceptance bar is met at the code and automated-test level. Once the operator completes Plan 08-05 Task 7 and replies `approved` / `approved with notes` / `block`, this phase can close. The orchestrator should route via the `human_needed` → HUMAN-UAT.md path per `workflows/execute-phase.md`.

---

*Verified: 2026-05-27T22:55:00Z*
*Verifier: Claude (gsd-verifier)*
