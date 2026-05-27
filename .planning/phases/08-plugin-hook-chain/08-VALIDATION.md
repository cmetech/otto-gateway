---
phase: 8
slug: plugin-hook-chain
status: draft
nyquist_compliant: false
wave_0_complete: false
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

> Tasks below are placeholders derived from CONTEXT.md scope. The planner fills concrete task IDs into this table during planning (Step 13b records the final mapping).

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 8-{plan}-{N} | plugin-chain | 1 | PLUG-01 | — | `chain.Pre` runs in registration order; first non-nil short-circuit wins | unit | `go test ./internal/plugin -run TestChainOrder -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | plugin-chain | 1 | PLUG-01 | — | PostHook chain runs unconditionally on assembled response (Codex H-5 in-place mutation preserved) | unit | `go test ./internal/plugin -run TestPostChainAlwaysRuns -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | request-id | 1 | PLUG-02, OBSV-03 | — | `RequestIDHook` generates ULID when absent; honors inbound `X-Request-Id`; ID propagates through ctx into every slog record (pre/engine/ACP/post) | unit + e2e | `go test ./internal/plugin -run TestRequestID -race` + `go test ./tests/e2e -run TestRequestIDPropagation -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | auth | 1 | PLUG-03 | T-8-AUTH (bearer bypass) | `AuthHook.Before` uses `subtle.ConstantTimeCompare`; bad token → short-circuit `*canonical.ChatResponse` rendered as native error envelope by each surface adapter (OpenAI / Ollama / Anthropic) | unit + e2e | `go test ./internal/plugin -run TestAuthHook -race` + `go test ./tests/e2e -run TestAuthRejection -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | logging | 1 | PLUG-04, OBSV-03 | — | `LoggingHook.Before` and `.After` emit structured slog records carrying request-id; redacted summary `{Email:N, SSN:M}` available via `pii.SummaryFromContext` (API seam present, ship-or-defer to planner) | unit | `go test ./internal/plugin -run TestLoggingHook -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | pii-walker | 1 | PLUG-06 | T-8-PII (PII leak in logs) | `WalkStrings(any, func(string) string)` never panics, idempotent, string-LEAVES-only, map-key invariant; nested `map[string]any` / `[]any` recursion bounded; non-string leaves bit-identical | unit + property | `go test ./internal/plugin/pii -run TestWalkStrings -race && go test ./internal/plugin/pii -run TestWalkProperty -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | pii-recognizers | 1 | PLUG-06 | T-8-PII | Six recognizers compile at package init (no per-request compile); Luhn validator passes fixed-table test (positive + negative samples); SSN reserved-range filter rejects `000-XX-XXXX`, `666-XX-XXXX`, `9XX-XX-XXXX`; IPv6 uses `net.ParseIP` validator; Email/IPv4/US Phone patterns covered by Presidio-derived fixtures | unit | `go test ./internal/plugin/pii -run TestRecognizers -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | pii-modes | 1 | PLUG-06 | T-8-HASH (length-extension if raw SHA256) | `replace` emits `<ENTITY>`; `mask` emits partial; `hash` uses HMAC-SHA256 (NOT raw SHA256) keyed by `PII_HASH_KEY`; 8-hex-char tag; canonical form (lowercased+trimmed) before hashing so referential identity is preserved across formatting variants | unit | `go test ./internal/plugin/pii -run TestRedactionModes -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | pii-counter | 1 | PLUG-06 | — | Counter resets per `canonical.ChatRequest` (intra-prompt referential identity); never per-process; property test asserts cross-request isolation | unit + property | `go test ./internal/plugin/pii -run TestCounterScope -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | config-validation | 1 | PLUG-05 | T-8-CFG (typo silently disables PII) | `ENABLED_HOOKS` unknown name → boot error containing `"unknown hook"`; `PII_REDACTION_MODE=hash` AND empty/unset `PII_HASH_KEY` → boot error containing `"PII_HASH_KEY"`; allowlist filter preserves registration order (NOT allowlist order) | unit | `go test ./internal/config -run TestPluginConfigValidation -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | health-hooks | 2 | OBSV-04 | T-8-LEAK (secret in /health/hooks config) | `GET /health/hooks` returns 200 JSON; chain in registration order; per-entry `{name, kind, enabled, config}`; `config` contains NO secrets (no `Tokens`, no `PII_HASH_KEY`, no regex sources); auth-exempt like `/health` and `/health/agents`; no mutate path (POST/PUT/DELETE return 405) | unit + e2e | `go test ./internal/server -run TestHooksEndpoint -race` + `go test ./tests/e2e -run TestHealthHooksAuthExempt -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | short-circuit | 2 | PLUG-01 | — | Codex H-4 contract preserved: PreHook returning non-nil `*ChatResponse` halts the chain, engine never invoked, response rendered through the surface adapter in native shape (OpenAI `{error:{...}}` / Ollama `{error:"..."}` / Anthropic `{type:"error", error:{...}}`) | e2e | `go test ./tests/e2e -run TestShortCircuit -race` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | arch-lint | 2 | — | — | `.go-arch-lint.yml` enforces: `internal/plugin` may import `internal/canonical` + `internal/engine` (interface types only); `internal/plugin/pii` may import `internal/plugin` + `internal/canonical`; no adapter imports plugin internals | static | `go-arch-lint check` | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | leak-check | 2 | — | — | Every new `_test.go` with goroutines / timing calls `goleak.VerifyTestMain(m)` (LoggingHook timing, e2e harness, /health/hooks handler test) | unit | `go test ./internal/plugin/... -count=1 -race` (leak failures abort) | ❌ W0 | ⬜ pending |
| 8-{plan}-{N} | gosec | 2 | — | — | `gosec ./...` exits 0 with G204 (subprocess) and G401/G501 (weak crypto) findings = 0; AuthHook constant-time comparison covered by SA suppression annotations only if intentional | static | `gosec ./... -severity high -confidence high` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/plugin/chain_test.go` — chain ordering + short-circuit + post-runs-unconditionally stubs (PLUG-01)
- [ ] `internal/plugin/request_id_test.go` — ULID generation + inbound honor + ctx propagation stubs (PLUG-02, OBSV-03)
- [ ] `internal/plugin/auth_test.go` — constant-time compare + short-circuit envelope stubs (PLUG-03)
- [ ] `internal/plugin/logging_test.go` — slog record shape + timing + redaction summary stubs (PLUG-04)
- [ ] `internal/plugin/pii/walk_test.go` — `testing/quick` generator + 4 walker invariants (PLUG-06)
- [ ] `internal/plugin/pii/recognizers_test.go` — fixed-table positive/negative cases per recognizer + Luhn (PLUG-06)
- [ ] `internal/plugin/pii/modes_test.go` — replace / mask / hash (HMAC-SHA256) / drop coverage (PLUG-06)
- [ ] `internal/config/plugin_config_test.go` — `ENABLED_HOOKS` typo + `mode=hash` no key boot errors (PLUG-05)
- [ ] `internal/server/hooks_handler_test.go` — `/health/hooks` JSON envelope + auth-exempt + no-secret-leak (OBSV-04)
- [ ] `tests/e2e/plugin_chain_test.go` — three-surface short-circuit + request-id propagation real-binary test

*Framework already present (`go test`); no install task needed.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Operator workflow: rotate `PII_HASH_KEY` to invalidate prior correlations | PLUG-06 / D-05 | Operational behavior; the assertion is that `docs/operating.md` describes the rotation workflow + restart-to-apply expectation | Read `docs/operating.md`; confirm it documents `PII_HASH_KEY` rotation as a feature, restart-to-apply, and that prior tags become non-correlating |
| `/health/hooks` regex-source omission audit | OBSV-04 | Visual inspection of `Describe()` JSON for each hook to confirm no regex source, secret token, or hash key appears | `curl http://localhost:11434/health/hooks` against a running binary with all four hooks enabled; grep response for any literal regex pattern, `AUTH_TOKEN` value, or `PII_HASH_KEY` value — must return nothing |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
