---
phase: 8
slug: plugin-hook-chain
status: approved
nyquist_compliant: true
wave_0_complete: true
created: 2026-05-27
---

# Phase 8 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (Go 1.23+) |
| **Config file** | none — uses go.mod (existing) |
| **Quick run command** | `go test ./internal/plugin/... -count=1 -race` |
| **Full suite command** | `go test ./... -count=1 -race -timeout=120s` |
| **Estimated runtime** | ~25s quick / ~90s full |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/plugin/... -count=1 -race`
- **After every plan wave:** Run `go test ./... -count=1 -race -timeout=120s`
- **Before `/gsd-verify-work`:** Full suite must be green; `golangci-lint run` and `gosec ./...` both exit 0
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

> Filled by Plan 13-06 (Nyquist coverage uplift) on 2026-06-07.
> Task count: 26 across 5 plans (BASELINE.txt records "31" — see 13-06-GAPS.txt for reconciliation).
> PII sub-package tasks (08-04-01 through 08-04-05) reference internal/plugin/pii/*_test.go files owned by plan 13-01.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 08-01-01 | 08-01 | 1 | — | T-8-SC (package legitimacy) | `github.com/oklog/ulid/v2` verified legitimate before `go get`; operator approval gates install | manual-only | N/A (human-verify checkpoint; package verified by operator; auto-approved per user directive per 08-01-SUMMARY.md) | ✅ (decision in 08-01-SUMMARY.md) | ✅ green |
| 08-01-02 | 08-01 | 1 | PLUG-01, PLUG-02, OBSV-03 | T-8-RID-2 (ULID entropy) | Wave 0 scaffold: `TestChain_RegistrationOrder` asserts Pre slice order = execution order (3-hook loop produces `["A","B","C"]`); `TestChain_ShortCircuit` asserts second hook never invoked on non-nil return; goleak gate in testmain_test.go; 9 failing stubs before Task 3/4 (RED state enforced) | unit | `go test ./internal/plugin -run 'TestChain_\|TestRequestID_' -count=1 -race` | ✅ (internal/plugin/chain_test.go, request_id_test.go, testmain_test.go) | ✅ green |
| 08-01-03 | 08-01 | 1 | PLUG-01 | — | `Chain.Filter` typo-fail-fast: `TestChain_Filter_UnknownNameError` asserts error contains `"unknown hook"`; `TestChain_Filter_PreservesRegistrationOrder` asserts allowlist `["C","A"]` yields `[A,C]` NOT `[C,A]`; `TestChain_Describe_NilSafe` asserts empty Chain returns `([], [])` not panic | unit | `go test ./internal/plugin -run 'TestChain_' -count=1 -race` | ✅ (internal/plugin/chain.go, chain_test.go) | ✅ green |
| 08-01-04 | 08-01 | 1 | PLUG-02, OBSV-03 | T-8-RID-1 (ctx-key collision) | `TestRequestID_GeneratesULID_WhenAbsent` asserts 26-char Crockford Base32 shape `^[0-9A-HJKMNP-TV-Z]{26}$`; `TestRequestID_HonorsInboundID` asserts stamped id wins over regeneration; `TestRequestID_SlogCorrelation` asserts JSON slog record contains `"request_id":"TEST-ID"` — these assertions would fail with a wrong id, empty string, or missing field | unit | `go test ./internal/plugin -run 'TestRequestID_' -count=1 -race` | ✅ (internal/plugin/request_id.go, request_id_test.go) | ✅ green |
| 08-01-05 | 08-01 | 2 | — | — | `.go-arch-lint.yml` `plugin` component declares `mayDependOn: [canonical, engine]`; `plugin_pii` declares `mayDependOn: [canonical, plugin]`; no adapter may import plugin internals; boundary violation would surface as non-zero exit | static | `go-arch-lint check` (or `grep -rE 'otto-gateway/internal/plugin' internal/adapter/`)| ✅ (.go-arch-lint.yml) | ✅ green |
| 08-02-01 | 08-02 | 2 | PLUG-03, OBSV-03 | — | `TestWithBearerToken_RoundTrip` asserts `(token, true)` round-trip; `TestBearerTokenFromContext_AbsentReturnsEmpty` asserts `("", false)` on fresh ctx; `TestWithBearerToken_EmptyTokenStillStored` asserts `("", true)` distinguishes absent from empty-stamp | unit | `go test ./internal/canonical -run TestWithBearerToken -count=1 -race` | ✅ (internal/canonical/auth_ctx.go, auth_ctx_test.go) | ✅ green |
| 08-02-02 | 08-02 | 2 | PLUG-03 | T-8-AUTH | Wave 0 scaffold: 9 `TestAuthHook_*` failing tests. `TestAuthHook_InvalidToken_ShortCircuit` asserts `resp.StopReason == canonical.StopError` AND `resp.Message.Content[0].Text` contains `"Invalid or missing API key"` — a correct-but-wrong-key impl that returns 200 would fail this; `TestAuthHook_Describe_NoSecrets` walks the config map and fails if any key contains `"token"` other than `"token_count"` | unit | `go test ./internal/plugin -run 'TestAuthHook_' -count=1 -race` | ✅ (internal/plugin/auth_test.go) | ✅ green |
| 08-02-03 | 08-02 | 2 | PLUG-03 | T-8-AUTH, T-8-LEAK | AuthHook implementation: `TestAuthHook_ConstantTimeCompareSourceAudit` opens auth.go via `os.ReadFile` and asserts `subtle.ConstantTimeCompare` present AND no `== string(` pattern — source-level guard against timing-side-channel regression; `TestAuthHook_EmptyTokens_Passthrough` (nil) asserts `(nil, nil)` so auth-disabled-by-default is tested | unit | `go test ./internal/plugin -run 'TestAuthHook_' -count=1 -race` | ✅ (internal/plugin/auth.go) | ✅ green |
| 08-02-04 | 08-02 | 2 | PLUG-03, OBSV-03 | T-8-AUTH-4 | All 3 adapters stamp `canonical.WithBearerToken` before `engine.Run` / `engine.Collect`; Anthropic stamps `x-api-key` first then `Authorization` fallback (D-15); no raw-token logging; existing adapter test suite unchanged-green | unit (adapter regression) | `go test ./internal/adapter/... -count=1 -race` | ✅ (internal/adapter/{ollama,openai,anthropic}/handlers.go) | ✅ green |
| 08-03-01 | 08-03 | 2 | PLUG-06 | T-8-PII-2 | `TestSummary_AddIsRaceSafe` spawns 100 goroutines and asserts `Counts()["Email"] == 100` under `-race` — a non-mutex impl would produce a data race or wrong count; `TestSummary_NilSafeAdd` asserts `(*Summary)(nil).Add("Email")` does not panic | unit | `go test ./internal/plugin/pii -run 'TestSummary_\|TestWithSummary_\|TestSummaryFromContext_' -count=1 -race` | ✅ (internal/plugin/pii/summary_test.go, testmain_test.go) | ✅ green |
| 08-03-02 | 08-03 | 2 | PLUG-06 | T-8-PII-2 | `pii.Summary` implementation: `summaryKey` is unexported struct-typed (cross-pkg collision guard); `Add` is nil-receiver-safe; `MarshalJSON` emits object-form for slog; `SummaryFromContext` returns `(nil, false)` on absent | unit | `go test ./internal/plugin/pii -run 'TestSummary_\|TestWithSummary_\|TestSummaryFromContext_' -count=1 -race` | ✅ (internal/plugin/pii/summary.go) | ✅ green |
| 08-03-03 | 08-03 | 2 | PLUG-04, OBSV-03 | T-8-PII, T-8-GO-LEAK | Wave 0 scaffold: 9 `TestLoggingHook_*` failing. `TestLoggingHook_Before_EmitsCorrelatedRecord` asserts decoded JSON record has `msg=="plugin.before"`, `request_id=="TEST-RID"`, `model=="auto"`, `message_count==2` — all four fields must be present; missing or wrong values fail | unit | `go test ./internal/plugin -run 'TestLoggingHook_' -count=1 -race` | ✅ (internal/plugin/logging_test.go) | ✅ green |
| 08-03-04 | 08-03 | 2 | PLUG-04, OBSV-03 | T-8-PII, T-8-GO-LEAK, T-8-LEAK-3 | LoggingHook implementation: `TestLoggingHook_SourceAudit_NoRawContent` opens logging.go and asserts no `slog.Any("request", req)` / `slog.Any("messages"` (T-8-PII guard); `TestLoggingHook_EmitsRedactedSummary_WhenPresent` asserts `record["redacted"]` JSON object contains `{"Email":2,"SSN":1}`; no `slog.SetDefault` (T-8-LEAK-3) | unit | `go test ./internal/plugin -run 'TestLoggingHook_' -count=1 -race` | ✅ (internal/plugin/logging.go) | ✅ green |
| 08-04-01 | 08-04 | 2 | PLUG-06 | T-8-WALK-PANIC | Wave 0 scaffold for all PII components (walk, luhn, recognizers, modes, hook). PII tests owned by plan 13-01; this row references internal/plugin/pii/walk_test.go etc. `TestWalkStrings_NeverPanics` uses `testing/quick` MaxCount=1000 with `defer recover()` — any panic in the walker would be caught | unit+property | `go test ./internal/plugin/pii -run 'TestWalkStrings_\|TestLuhnCheck_\|TestPIIRedactionHook_' -count=1 -race` (13-01 scope) | ✅ (internal/plugin/pii/walk_test.go, luhn_test.go, recognizers_test.go, modes_test.go, pii_test.go) | ✅ green |
| 08-04-02 | 08-04 | 2 | PLUG-06 | T-8-WALK-PANIC, T-8-PII | `WalkStrings` + `LuhnCheck` implementations. `TestWalkStrings_DepthBounded` constructs 70-level nested map and asserts deepest string NOT transformed (depth>64 pass-through); `TestWalkStrings_KeysAndNonStringLeavesPreserved` asserts `"count":float64(42)` unchanged; `TestLuhnCheck_KnownValid` asserts Visa/MC/Amex test BINs return true | unit+property | `go test ./internal/plugin/pii -run 'TestWalkStrings_\|TestLuhnCheck_' -count=1 -race` (13-01 scope) | ✅ (internal/plugin/pii/walk.go, luhn.go) | ✅ green |
| 08-04-03 | 08-04 | 2 | PLUG-06 | T-8-RE2, T-8-PII-BYPASS | Six recognizers with validators. `TestSSNRecognizer_ReservedRangeValidator` asserts 000-XX, 666-XX, 9XX-XX, XXX-00, XXX-0000 all rejected (5 negative cases); `TestIPv4Recognizer_OctetValidator` asserts `256.1.1.1` rejected; `regexp.MustCompile` at init (test passes only if binary doesn't panic at startup) | unit | `go test ./internal/plugin/pii -run 'TestEmail\|TestIPv4\|TestIPv6\|TestSSN\|TestCreditCard\|TestUSPhone\|TestRecognizers_' -count=1 -race` (13-01 scope) | ✅ (internal/plugin/pii/recognizers.go) | ✅ green |
| 08-04-04 | 08-04 | 2 | PLUG-06 | T-8-HASH | HMAC-SHA256 modes. `TestApplyMode_Hash_HMAC_SHA256_NotRawSHA256` computes `raw_sha = hex(sha256(value))[:4]` and asserts the hash tag does NOT contain that substring (proves HMAC, not raw SHA256); `TestApplyMode_Hash_CanonicalForm` asserts `"Corey@CMETECH.io"` and `"  corey@cmetech.io  "` produce same 8-hex tag | unit | `go test ./internal/plugin/pii -run 'TestApplyMode_' -count=1 -race` (13-01 scope) | ✅ (internal/plugin/pii/modes.go) | ✅ green |
| 08-04-05 | 08-04 | 2 | PLUG-06 | T-8-PII, T-8-PII-COUNTER, T-8-HASH | PIIRedactionHook implementation. `TestPIIRedactionHook_CounterScope_PerRequest` calls Before twice with same email and asserts second call also produces `<EMAIL_1>` (counter reset); `TestPIIRedactionHook_ToolUseInputRecursed` asserts map KEYS present verbatim while string values contain `<EMAIL`; `TestPIIRedactionHook_Describe_NoSecrets` walks cfg and fails on any `hash_key`/`pattern` key | unit | `go test ./internal/plugin/pii -run 'TestPIIRedactionHook_' -count=1 -race` (13-01 scope) | ✅ (internal/plugin/pii/pii.go) | ✅ green |
| 08-05-01 | 08-05 | 3 | PLUG-05, OBSV-04 | T-8-CFG, T-8-LEAK | Wave 0 scaffold: 11 `TestLoad_*`, 5 `TestChainFilter_*`, 7 `TestHooksHandler_*`. `TestHooksHandler_SecretOmissionAudit` injects sentinel `"TOPSECRET_AUTH_TOKEN_001"` into the test fixture and asserts response body does NOT contain that literal string — would fail if Describe() leaked the token | unit | `go test ./internal/config ./internal/plugin ./internal/server -count=1 -race` | ✅ (internal/config/plugin_config_test.go, internal/plugin/chain_filter_test.go, internal/server/hooks_handler_test.go) | ✅ green |
| 08-05-02 | 08-05 | 3 | PLUG-05 | T-8-CFG, T-8-HASH-BOOT | config.Load 5 new env keys. `TestLoad_PIIHashModeRequiresKey` asserts `err.Error()` contains `"PII_HASH_KEY"` when mode=hash and key unset; `TestLoad_PIIRedactionMode_UnknownModeError` asserts error contains `"PII_REDACTION_MODE"` AND `"bogus"`; `TestLoad_PIIEnabledEntities_UnknownNameError` asserts error references the bad entity name | unit | `go test ./internal/config -run 'TestLoad_' -count=1 -race` | ✅ (internal/config/config.go, plugin_config_test.go) | ✅ green |
| 08-05-03 | 08-05 | 3 | PLUG-01, PLUG-05 | T-8-CFG | Chain.Filter finalization. `TestChainFilter_KnownAllowlist_PreservesRegistrationOrder` provides `["LoggingHook","RequestIDHook"]` (deliberate wrong order) and asserts filtered[0] is RequestIDHook (registration-order wins); `TestChainFilter_LoggingHookInBothPreAndPost_AllowlistKeepsBoth` asserts single allowlist entry `"LoggingHook"` keeps both Pre AND Post slots | unit | `go test ./internal/plugin -run 'TestChainFilter_' -count=1 -race` | ✅ (internal/plugin/chain.go, chain_filter_test.go) | ✅ green |
| 08-05-04 | 08-05 | 3 | OBSV-04 | T-8-LEAK | GET /health/hooks handler. `TestHooksHandler_SecretOmissionAudit` uses `strings.Contains(body, "TOPSECRET")` negative assertion; `TestHooksHandler_POST_Returns405` asserts StatusMethodNotAllowed; `TestHooksHandler_AuthExempt` asserts 200 without bearer header; `TestHooksHandler_FourHookChain_JSONEnvelope` asserts entries[0].Name=="RequestIDHook" (order locked) | unit | `go test ./internal/server -run 'TestHooksHandler_' -count=1 -race` | ✅ (internal/server/hooks_handler.go, hooks_handler_test.go, server.go) | ✅ green |
| 08-05-04b | 08-05 | 3 | PLUG-02, OBSV-03 | — | All 3 adapters stamp `plugin.WithRequestID` on ctx from `X-Request-Id` header (generated ULID if absent). 6 new round-trip tests: `TestHandler_StampsRequestIDOnContext_FromHeader` asserts `RequestIDFromContext(ctx) == "01HKQ8..."` (exact inbound value); `TestHandler_StampsRequestIDOnContext_GeneratedWhenAbsent` asserts 26-char Crockford Base32 shape | unit (adapter) | `go test ./internal/adapter/... -count=1 -race` | ✅ (internal/adapter/{ollama,openai,anthropic}/handlers.go + tests) | ✅ green |
| 08-05-05 | 08-05 | 3 | PLUG-01, PLUG-03, PLUG-05, OBSV-04 | T-8-AUTH-BYPASS, T-8-CFG | main.go wiring: `plugin.Chain{Pre:[RequestIDHook→AuthHook→PIIRedactionHook→LoggingHook], Post:[LoggingHook]}` + `chain.Filter(cfg.EnabledHooks)` + engine injection + hooksDescriptionAdapter (TRST-04) + auth.Bearer middleware removal (Pattern F). `go build ./cmd/otto-gateway` gates compile; full `go test ./...` gates runtime | unit+build | `go build ./cmd/otto-gateway && go test ./... -count=1 -race -timeout=120s` | ✅ (cmd/otto-gateway/main.go) | ✅ green |
| 08-05-06 | 08-05 | 3 | PLUG-01, PLUG-03, PLUG-05, OBSV-04 | T-8-CFG, T-8-HASH-BOOT, T-8-LEAK | Real-binary e2e: SC1 `TestE2E_BadBearer_AllThreeSurfaces` asserts each of 3 surfaces returns native error envelope shape (not just 4xx — shape must match per-surface spec); SC7 `TestE2E_HealthHooks_NoSecretLeak` boot with sentinel token and asserts it absent from /health/hooks body; SC5 `TestE2E_EnabledHooks_Filter_PreservesOrder` asserts registration-order in live binary | e2e | `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_ -count=1 -race -timeout=180s` | ✅ (tests/e2e/plugin_chain_test.go) | ✅ green |
| 08-05-07 | 08-05 | 4 | PLUG-05, PLUG-06, OBSV-04 | — | Operator workflow verification: /health/hooks JSON audit, SC1 three-surface short-circuit curl, SC5 ENABLED_HOOKS filter live, SC6 PII redaction live, D-05 hash-key rotation invalidates correlation, T-8-HASH-BOOT boot-fail-fast, docs/operating.md PII knobs documented | manual-only | N/A (human-verify checkpoint; approved per 08-05-SUMMARY.md operator sign-off) | ✅ (docs/operating.md; 08-05-SUMMARY.md) | ✅ green |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [x] `internal/plugin/chain_test.go` — chain ordering + short-circuit + post-runs-unconditionally stubs (PLUG-01)
- [x] `internal/plugin/request_id_test.go` — ULID generation + inbound honor + ctx propagation stubs (PLUG-02, OBSV-03)
- [x] `internal/plugin/auth_test.go` — constant-time compare + short-circuit envelope stubs (PLUG-03)
- [x] `internal/plugin/logging_test.go` — slog record shape + timing + redaction summary stubs (PLUG-04)
- [x] `internal/plugin/pii/walk_test.go` — `testing/quick` generator + 4 walker invariants (PLUG-06)
- [x] `internal/plugin/pii/recognizers_test.go` — fixed-table positive/negative cases per recognizer + Luhn (PLUG-06)
- [x] `internal/plugin/pii/modes_test.go` — replace / mask / hash (HMAC-SHA256) / drop coverage (PLUG-06)
- [x] `internal/config/plugin_config_test.go` — `ENABLED_HOOKS` typo + `mode=hash` no key boot errors (PLUG-05)
- [x] `internal/server/hooks_handler_test.go` — `/health/hooks` JSON envelope + auth-exempt + no-secret-leak (OBSV-04)
- [x] `tests/e2e/plugin_chain_test.go` — three-surface short-circuit + request-id propagation real-binary test

*Framework already present (`go test`); no install task needed.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Operator workflow: rotate `PII_HASH_KEY` to invalidate prior correlations | PLUG-06 / D-05 | Operational behavior; the assertion is that `docs/operating.md` describes the rotation workflow + restart-to-apply expectation | Read `docs/operating.md`; confirm it documents `PII_HASH_KEY` rotation as a feature, restart-to-apply, and that prior tags become non-correlating. Verified in 08-05-SUMMARY.md operator sign-off. |
| `/health/hooks` regex-source omission audit | OBSV-04 | Visual inspection of `Describe()` JSON for each hook to confirm no regex source, secret token, or hash key appears | `curl http://localhost:11434/health/hooks` against a running binary with all four hooks enabled; grep response for any literal regex pattern, `AUTH_TOKEN` value, or `PII_HASH_KEY` value — must return nothing. Covered by e2e `TestE2E_HealthHooks_NoSecretLeak` AND by unit `TestHooksHandler_SecretOmissionAudit` with sentinel values. Operator-confirmed in 08-05-SUMMARY.md. |
| Auth-bypass posture for Ollama list-mode stubs | AUTH-01 / AUTH-02 | Accepted v1 risk: Ollama `/api/tags`, `/api/version`, `/api/ps` return static fixtures without kiro-cli; auth exemption is intentional because LangFlow polls these unconditionally | `docs/operating.md` contains an "Auth posture quick reference" subsection documenting the list-mode auth-exempt posture as an accepted v1 design. Verified by 08.1 Plan 04 Task 08.1-04-02 (doc-assertion green). |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references
- [x] No watch-mode flags
- [x] Feedback latency < 30s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** 2026-06-07 (Plan 13-06 Nyquist uplift — per-task map filled, Wave 0 all ticked, sign-off boxes verified across 26-task surface, sampling continuity confirmed)
