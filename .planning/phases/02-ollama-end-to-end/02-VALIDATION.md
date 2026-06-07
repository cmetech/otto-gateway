---
phase: 2
slug: ollama-end-to-end
status: complete
nyquist_compliant: true
wave_0_complete: true
created: 2026-05-23
updated: 2026-06-07
---

# Phase 2 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) |
| **Config file** | none — uses go.mod / go.work; integration tests gated by `LOOP24_INTEGRATION=1` env var per Phase 1 D-17 |
| **Quick run command** | `go test -short ./...` |
| **Full suite command** | `go test -race ./... && LOOP24_INTEGRATION=1 go test -race ./internal/acp/... ./internal/engine/... ./internal/pool/... ./internal/adapter/ollama/...` |
| **Estimated runtime** | ~30 seconds (quick) / ~90 seconds (full with integration) |

---

## Sampling Rate

- **After every task commit:** Run `go test -short ./{package}/...` for the touched package
- **After every plan wave:** Run `go test -race ./...`
- **Before `/gsd:verify-work`:** Full suite must be green (race + integration)
- **Max feedback latency:** 30 seconds for quick; 90 seconds for full

---

## Per-Task Verification Map

> Filled by nyquist uplift plan 13-04 (2026-06-07). One row per task sub-deliverable across all 6 Phase-02 plans.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 02-01-T1 | 02-01 | 1 | SURF-03, SURF-05 | T-02-01 (field-shape drift) | Discriminator coverage locks iota positions | unit | `go test -race -run 'TestChatRequest_ZeroValue\|TestMessageRole_DiscriminatorCoverage\|TestContentKind_DiscriminatorCoverage\|TestChatResponse_AssemblyShape\|TestNoJSONTags\|TestFinalResult_ZeroValue' ./internal/canonical/...` | `internal/canonical/chat_test.go` | ✅ green |
| 02-01-T2 | 02-01 | 1 | SURF-03, SURF-05 | T-02-02 (JSON-tag re-introduction) | TestNoJSONTags reflective sweep blocks json:"..." on canonical types | unit | `go test -race -run 'TestChatRequest_ResourceLinks' ./internal/canonical/...` | `internal/canonical/chat_test.go` | ✅ green |
| 02-01-T3 | 02-01 | 1 | SURF-03 | T-02-01 (BlockKindImage iota drift) | BlockKindText==0, BlockKindResourceLink==1, BlockKindImage==2 locked | unit | `go test -race -run 'TestBlockKindImage_Discriminator\|TestImageBlock_ZeroValue\|TestBlock_ImageVariant\|TestNoJSONTags_ChunkImageBlock' ./internal/canonical/...` | `internal/canonical/chunk_image_test.go` | ✅ green |
| 02-02-T1 | 02-02 | 1 | AUTH-01, AUTH-02 | T-02-09 (token leak in log) | writeOllamaError emits only error message, no token content | unit | `go test -race -run 'TestWriteOllamaError_Shape' ./internal/auth/...` | `internal/auth/auth_internal_test.go` | ✅ green |
| 02-02-T2a | 02-02 | 1 | AUTH-01 | T-02-05 (timing side-channel) | subtle.ConstantTimeCompare prevents token recovery via response timing | unit | `go test -race -run 'TestBearer_' ./internal/auth/...` | `internal/auth/auth_test.go` | ✅ green |
| 02-02-T2b | 02-02 | 1 | AUTH-02 | T-02-06 (XFF spoofing), T-02-07 (IPv4-in-IPv6) | XFF NOT trusted by default (Codex H-7); ::ffff: stripped before CIDR match | unit | `go test -race -run 'TestIPAllowlist_' ./internal/auth/...` | `internal/auth/auth_test.go` | ✅ green |
| 02-02-T3 | 02-02 | 1 | AUTH-01, AUTH-02, AUTH-03 | T-02-06, T-02-07 | Full auth middleware contract exercised via httptest blackbox | unit | `go test -race ./internal/auth/...` | `internal/auth/auth_test.go` | ✅ green |
| 02-03-T1 | 02-03 | 1 | AUTH-01, AUTH-02, POOL-01 | T-02-11 (malformed CIDR → allow-all), T-02-12 (bad POOL_SIZE) | Load() error on any malformed ALLOWED_IPS or POOL_SIZE; no silent default | unit | `go test -race -run 'TestLoad_Auth\|TestLoad_Allowed\|TestLoad_Pool\|TestLoad_Ollama\|TestLoad_OpenAI\|TestLoad_AuthTrust' ./internal/config/...` | `internal/config/config_test.go` | ✅ green |
| 02-03-T2 | 02-03 | 1 | AUTH-01, AUTH-02 | T-02-14 (comma vs whitespace split confusion) | getEnvStrSliceComma splits on comma; whitespace trimmed per entry | unit | `go test -race -run 'TestParseCIDRs_' ./internal/config/...` | `internal/config/config_internal_test.go` | ✅ green |
| 02-04-T1a | 02-04 | 2 | ACP-07, SURF-03 | none | Phase 1.1 preflight gate (Codex H-1) fails build if Phase 1.1 surface absent | unit (compile-time) | `go test -race -run 'TestPreflight_Phase11Surface' ./internal/engine/...` | `internal/engine/preflight_phase11_test.go` | ✅ green |
| 02-04-T1b | 02-04 | 2 | ACP-07 | none | acp_adapter.go isolates acp import; acpStreamShim bridges Chunks field→method | unit | `go test -race -run 'TestACPClientAdapter_Compiles\|TestACPStreamShim_' ./internal/engine/...` | `internal/engine/acp_adapter_test.go` | ✅ green |
| 02-04-T2 | 02-04 | 2 | ACP-07, SURF-03 | none | pickCwd reads ResourceLinks (Codex H-2, SC#5); buildBlocks emits BlockKindImage (Codex M-1) | unit + property | `go test -race -run 'TestPickCwd_\|TestBuildBlocks_' ./internal/engine/...` | `internal/engine/pickcwd_test.go`, `internal/engine/build_acp_test.go` | ✅ green |
| 02-04-T3 | 02-04 | 2 | ACP-07, SURF-03 | none | PreHook short-circuit preserves hook payload (Codex H-4); PostHook mutation visible to caller (Codex H-5) | unit | `go test -race -run 'TestEngine\|TestCollect' ./internal/engine/...` | `internal/engine/engine_test.go`, `internal/engine/collect_test.go` | ✅ green |
| 02-05-T1 | 02-05 | 3 | POOL-01, POOL-02, POOL-03, OBSV-01 | T-02-28 (ACPClient interface drift) | ClientFactory + PoolClient seams (Codex M-2); Warmup NewSession on slot 0 then AvailableModels (Codex H-6) | unit | `go test -race -run 'TestPool_New\|TestPool_Close\|TestPool_Stats' ./internal/pool/...` | `internal/pool/pool_test.go` | ✅ green |
| 02-05-T2 | 02-05 | 3 | POOL-01, POOL-03 | T-02-24 (slot leak via missing release) | poolStreamWrapper releases slot on Result, ctx-cancel watch goroutine, AND Pool.Cancel (Codex M-3) | unit | `go test -race -run 'TestPool_Prompt\|TestPool_Cancel\|TestPool_Context\|TestPool_Stream' ./internal/pool/...` | `internal/pool/pool_test.go` | ✅ green |
| 02-05-T3 | 02-05 | 3 | POOL-01, POOL-02, POOL-03, OBSV-01 | T-02-43 (lifecycle untested without real kiro-cli) | Fake-factory tests (Codex M-2) exercise session→slot tracking, Prompt/Cancel/Result release without kiro-cli | unit (fake injection) | `go test -race ./internal/pool/...` | `internal/pool/pool_test.go`, `internal/pool/export_test.go` | ✅ green |
| 02-06-T1a | 02-06 | 4 | SURF-01, SURF-03, SURF-07 | T-02-29 (oversized body), T-02-45 (body-unbounded stubs) | decodeJSONBody helper (Codex M-5) applies MaxBytesReader on every body-reading endpoint | unit | `go test -race -run 'TestDecodeJSONBody_' ./internal/adapter/ollama/...` | `internal/adapter/ollama/decode_test.go` | ✅ green |
| 02-06-T1b | 02-06 | 4 | SURF-01, SURF-03, SURF-05, SURF-07 | T-02-44 (chi route precedence), T-02-33 (engine error leak) | ProtectedRouter()/HandleVersion() split (Codex M-4); /version NOT in protected router; images→ContentKindImage (Codex M-1) | unit | `go test -race -run 'TestHandle\|TestWire\|TestStub\|TestHandlePull\|TestHandlePush\|TestHandleCreate\|TestHandleCopy\|TestHandleDelete' ./internal/adapter/ollama/...` | `internal/adapter/ollama/handlers_test.go`, `internal/adapter/ollama/stub_test.go`, `internal/adapter/ollama/wire_test.go` | ✅ green |
| 02-06-T2a | 02-06 | 4 | AUTH-01, AUTH-02, AUTH-03, OBSV-01 | T-02-06 (XFF end-to-end) | AuthTrustXFF threads config→server→auth.IPAllowlist (Codex H-7); /health pool.Stats wired | unit | `go test -race -run 'TestExempt\|TestIPAllowlist_XFFTrustGate\|TestProtected' ./internal/server/...` | `internal/server/server_test.go` | ✅ green |
| 02-06-T2b | 02-06 | 4 | POOL-02, SURF-03 | T-02-36 (warmup deadline) | pool.Warmup called BEFORE server start (POOL-02 ordering asserted by test) | unit | `go test -race -run 'TestApp_' ./cmd/otto-gateway/...` | `cmd/otto-gateway/main_test.go` | ✅ green |
| 02-06-T2c | 02-06 | 4 | AUTH-01, AUTH-02 | T-02-38 (wrapper injection) | Wrapper scripts pass AUTH_TOKEN, ALLOWED_IPS, POOL_SIZE, KIRO_CWD, OLLAMA_PATH_PREFIX, OPENAI_PATH_PREFIX, AUTH_TRUST_XFF | manual-only | `grep -q 'AUTH_TOKEN' scripts/loop24 && grep -q 'AUTH_TRUST_XFF' scripts/loop24` (presence check only) | `scripts/loop24`, `scripts/loop24.ps1` | ⚠️ manual — script passthrough verified by grep; behavioral smoke requires live shell + kiro-cli |
| 02-06-T3 | 02-06 | 4 | SURF-01, SURF-05 | T-02-37 (LangFlow audit) | Integration test gated on LOOP24_INTEGRATION=1; LangFlow zero-reconfig verified by human at checkpoint | integration (gated) | `LOOP24_INTEGRATION=1 go test -race -run 'TestIntegration' ./internal/adapter/ollama/... -timeout 60s` (skips without kiro-cli) | `internal/adapter/ollama/integration_test.go` | ⚠️ manual — auto-skips without LOOP24_INTEGRATION=1; LangFlow UAT requires operator |
| HUMAN-UAT-01 | 02-HUMAN-UAT | — | SURF-01, SURF-05, AUTH-01 | T-02-37 | Three 02-HUMAN-UAT.md items operator-deferred (exercised implicitly by Phases 8.2+8.4 in production) | manual-only | n/a — requires live binary + kiro-cli + optional LangFlow | `internal/adapter/ollama/integration_test.go` covers item 1 when LOOP24_INTEGRATION=1 | ⚠️ manual — documented rationale: requires running kiro-cli authenticated; LangFlow zero-reconfig requires live LangFlow instance |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky/manual*

**Auditor notes (2026-06-07):**
- All 20 auto-classified rows verified green: `go test -race ./internal/canonical/... ./internal/auth/... ./internal/config/... ./internal/engine/... ./internal/pool/... ./internal/adapter/ollama/... ./internal/server/... ./cmd/otto-gateway/...` — all packages exit 0, no data races.
- No production source edits made; `git diff --name-only -- internal/ cmd/` is empty outside `_test.go` files.
- 4 manual-only rows (02-06-T2c, 02-06-T3, HUMAN-UAT-01, and wrapper script row) have written rationale explaining why programmatic automation is not feasible.
- ESCALATE file (13-04-ESCALATIONS.txt) not created — no bugs found requiring escalation; all existing tests pass.

---

## Wave 0 Requirements

Wave 0 = test scaffolding that must land before the implementation tasks. For Phase 2:

- [x] `internal/canonical/chat_test.go` — zero-value + round-trip assertions for ChatRequest/Response/Message/ContentPart
- [x] `internal/engine/engine_test.go` — fake ACPClient harness (controllable channel)
- [x] `internal/engine/pickcwd_test.go` — table-driven cases + property tests via `testing/quick`
- [x] `internal/engine/build_acp_test.go` — golden bracketed-section text per CONTEXT.md D-02
- [x] `internal/pool/pool_test.go` — fake ACP spawn harness, fail-fast warmup assertions, Stats() race test
- [x] `internal/auth/auth_test.go` — bearer constant-time match, multi-token CSV, exempt-path bypass; IPAllowlist CIDR + single-IP, IPv4/IPv6, malformed XFF handling
- [x] `internal/adapter/ollama/wire_test.go` — golden Ollama wire ↔ canonical round-trip for /api/chat, /api/generate
- [x] `internal/adapter/ollama/handlers_test.go` — handler-level tests using httptest + fake engine
- [x] `internal/adapter/ollama/stub_test.go` — success-shape assertions for pull/push/create/copy/delete

_Note: Wave 0 files were planned as separate bearer_test.go + iplist_test.go but shipped as auth_test.go covering both. This is functionally equivalent and meets the Wave 0 requirements._

Real-kiro integration tests reuse Phase 1's `resolveKiroCLI()` + `LOOP24_INTEGRATION=1` gate pattern.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| LangFlow zero-reconfig | SURF-05 | Requires running LangFlow instance pointed at gateway | Start gateway with `KIRO_CMD=kiro-cli`; in existing LangFlow flow, point Ollama model component at `http://localhost:11434`; run flow; expect successful chat response with no flow edits |
| LangFlow `/api/pull` dispatch | SURF-05 | LangFlow model-setup behavior is environment-dependent | Trigger LangFlow's "pull model" UX; observe gateway returns `{status:"success"}` and LangFlow does not retry |
| Stub-shape parity vs Node reference | SURF-07 | Node implementation's exact bytes are the target | Unit tests (TestHandlePull_StreamTrue, TestHandleCopy_EmptyObject, etc.) assert byte-shape via decode; operator smoke via `curl` provides final confirmation |
| Bearer + IP allowlist rejection | AUTH-01..03 | Easiest to confirm with a deliberate bad-credential request | `curl -H "Authorization: Bearer wrong" http://localhost:11434/api/chat` returns 401; `curl http://localhost:11434/api/version` returns 200 (exempt path). Automated: TestIPAllowlist_XFFTrustGate + TestExemptRoutes_BypassAuth cover the middleware contract. |
| Wrapper script env-var passthrough | AUTH-01..03 | Shell execution requires live kiro-cli environment | `grep -q 'AUTH_TOKEN' scripts/loop24 && grep -q 'AUTH_TRUST_XFF' scripts/loop24` passes; behavioral smoke requires running binary |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references
- [x] No watch-mode flags
- [x] Feedback latency < 30s for quick / 90s for full
- [x] Frontmatter compliance flag set to true

**Approval:** approved 2026-06-07
