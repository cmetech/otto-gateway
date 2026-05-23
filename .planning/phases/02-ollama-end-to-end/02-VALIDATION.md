---
phase: 2
slug: ollama-end-to-end
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-23
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

> This table is filled by the planner from the per-plan task list. Each row maps a task to its test command, the requirement it satisfies, and any security threat it mitigates. Planner generates one row per task in PLAN.md.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| (planner fills) | | | | | | | | | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

Wave 0 = test scaffolding that must land before the implementation tasks. For Phase 2:

- [ ] `internal/canonical/chat_test.go` — zero-value + round-trip assertions for ChatRequest/Response/Message/ContentPart
- [ ] `internal/engine/engine_test.go` — fake ACPClient harness (controllable channel)
- [ ] `internal/engine/pickcwd_test.go` — table-driven cases + property tests via `testing/quick`
- [ ] `internal/engine/build_acp_test.go` — golden bracketed-section text per CONTEXT.md D-02
- [ ] `internal/pool/pool_test.go` — fake ACP spawn harness, fail-fast warmup assertions, Stats() race test
- [ ] `internal/auth/bearer_test.go` — constant-time match, multi-token CSV, exempt-path bypass
- [ ] `internal/auth/iplist_test.go` — CIDR + single-IP, IPv4/IPv6, malformed env handling
- [ ] `internal/adapter/ollama/wire_test.go` — golden Ollama wire ↔ canonical round-trip for /api/chat, /api/generate
- [ ] `internal/adapter/ollama/handlers_test.go` — handler-level tests using httptest + fake engine
- [ ] `internal/adapter/ollama/stub_test.go` — success-shape assertions for pull/push/create/copy/delete

Real-kiro integration tests reuse Phase 1's `resolveKiroCLI()` + `LOOP24_INTEGRATION=1` gate pattern.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| LangFlow zero-reconfig | SURF-05 | Requires running LangFlow instance pointed at gateway | Start gateway with `KIRO_CMD=kiro-cli`; in existing LangFlow flow, point Ollama model component at `http://localhost:11434`; run flow; expect successful chat response with no flow edits |
| LangFlow `/api/pull` dispatch | SURF-05 | LangFlow model-setup behavior is environment-dependent | Trigger LangFlow's "pull model" UX; observe gateway returns `{status:"success"}` and LangFlow does not retry |
| Stub-shape parity vs Node reference | SURF-07 | Node implementation's exact bytes are the target | `curl` each stub endpoint against the Node version at known port; diff against gateway output; investigate non-trivial diffs |
| Bearer + IP allowlist rejection | AUTH-01..03 | Easiest to confirm with a deliberate bad-credential request | `curl -H "Authorization: Bearer wrong" http://localhost:11434/api/chat` returns 401; `curl http://localhost:11434/api/version` returns 200 (exempt path) |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s for quick / 90s for full
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
