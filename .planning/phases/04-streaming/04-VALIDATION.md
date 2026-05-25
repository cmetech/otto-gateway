---
phase: 4
slug: streaming
status: planned
nyquist_compliant: true
wave_0_complete: false
created: 2026-05-24
updated: 2026-05-25
---

# Phase 4 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | `go test` (stdlib testing + `goleak` gate on engine/handler packages) |
| **Config file** | none — Go toolchain native |
| **Quick run command** | `go test -race ./internal/adapter/ollama/... ./internal/engine/...` |
| **Full suite command** | `go test -race ./... && go test -tags e2e ./tests/e2e/... -timeout 120s` |
| **Estimated runtime** | ~30–90 seconds (incl. real-binary E2E) |

---

## Sampling Rate

- **After every task commit:** Run `go test -race ./internal/adapter/... ./internal/engine/...`
- **After every plan wave:** Run `go test -race ./...`
- **Before `/gsd:verify-work`:** Full suite + `go vet` + E2E must be green; `goleak` must report zero leaked goroutines.
- **Max feedback latency:** 90 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 04-01-T1 | 04-01 | 1 | STRM-01 | T-04-02 | streamEnabled nil-deref guard | unit | `go test ./internal/adapter/ollama/... -run TestWire -v` | wire_test.go | ⬜ pending |
| 04-01-T2 | 04-01 | 1 | STRM-04 | T-04-03 | watchdog goroutine leaks via goleak gate | unit | `go test ./internal/engine/... -race -v` | engine_test.go + testmain_test.go | ⬜ pending |
| 04-01-T3 | 04-01 | 1 | STRM-01/04 | T-04-SC | interfaces compile clean | build | `go build ./... && go vet ./internal/adapter/ollama/...` | adapter.go | ⬜ pending |
| 04-02-T1 | 04-02 | 2 | STRM-01 | T-04-05 | json.Marshal escapes newlines in chunk text | unit | `go test ./internal/adapter/ollama/... -run TestNDJSON -v -race` | ndjson_test.go (new) | ⬜ pending |
| 04-02-T1 | 04-02 | 2 | STRM-04 | T-04-03 | goleak gate on ollama adapter package | unit | `go test ./internal/adapter/ollama/... -race` | testmain_test.go (new) | ⬜ pending |
| 04-02-T2 | 04-02 | 2 | STRM-01/03/05 | T-04-04/T-04-06/T-04-07 | NDJSON streaming default; stream:false regression; slot survives cancel | E2E | `go test -tags e2e ./tests/e2e/... -run TestE2E_Ollama -timeout 120s -v` | ollama_e2e_test.go | ⬜ pending |
| 04-03-T1 | 04-03 | 2 | STRM-04 | T-04-08/T-04-09 | Cancel(sid) called with correct id after ctx cancel; no Cancel on normal path | unit | `go test ./internal/engine/... -run TestWatchdog -v -race` | watchdog_test.go (new) | ⬜ pending |
| 04-03-T2 | 04-03 | 2 | STRM-04 | T-04-09 | session/cancel JSON-RPC frame on wire with correct sessionId | integration | `go test ./internal/acp/... -run TestIntegration_CancelFrame -v -race` | cancel_test.go (new) | ⬜ pending |
| 04-04-T1 | 04-04 | 3 | STRM-02/05 | T-04-12 | OpenAI SSE data:[DONE]; stream:false single JSON | E2E | `go test -tags e2e ./tests/e2e/... -run TestE2E_OpenAI/ChatCompletions_Streaming -timeout 120s -v` | openai_e2e_test.go | ⬜ pending |
| 04-04-T2 | 04-04 | 3 | STRM-02/03/05 | T-04-12 | Anthropic SSE message_stop; stream:false single JSON | E2E | `go test -tags e2e ./tests/e2e/... -run TestE2E_Anthropic/Messages_Streaming -timeout 120s -v` | anthropic_e2e_test.go | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [x] `goleak.VerifyTestMain` is confirmed active on `internal/engine` (testmain_test.go lines 17-19)
- [x] `goleak.VerifyTestMain` is confirmed active on `internal/acp` (testmain_test.go lines 18-19)
- [ ] `goleak.VerifyTestMain` must be ADDED to `internal/adapter/ollama` — created in Plan 02 Task 1 (testmain_test.go)
- [x] Existing `tests/e2e/` harness covers all surfaces; no new framework install needed

*Wave 0 is complete for engine and acp; the ollama adapter goleak gap closes in Plan 02 Task 1.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| *(none)* | — | All phase behaviors have automated verification | — |

*All phase behaviors have automated verification (D-09 automated E2E per surface; D-10 fake-ACP frame assertion + real-binary disconnect smoke). No HUMAN-UAT gate per CONTEXT.md.*

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify commands
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references (ollama testmain gap addressed in Plan 02 T1)
- [x] No watch-mode flags in any verify command
- [x] Feedback latency < 90s (full E2E suite ~30-90s per RESEARCH.md)
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** planned — pending execution
