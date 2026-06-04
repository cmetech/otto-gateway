# Milestones

## v1.5 audit WARNINGs (Shipped: 2026-06-04)

**Phases completed:** 13 phases (01, 01.1, 02, 03, 03.1, 04, 05, 06, 06.1, 08, 08.1, 08.2, 08.3, 08.4, 9) + 1 deferred to v1.6 (08.3.1) + 1 reverted (08.3.2)
**Plans:** 57 across the 13 completed phases
**Requirements:** 63/63 satisfied (added PII-01 in 08.4)
**Timeline:** 2026-05-23 → 2026-06-04 (13 days)
**Code:** 69,011 LOC across 233 Go source files (136 test files) + PowerShell/bash scripts
**Commits (feat+fix):** 285
**Binary releases:** v1.0 → v1.10.0 (24+ tags) — last release v1.10.0 published 2026-06-04 with cross-compiled artifacts (darwin-arm64, darwin-amd64, linux-amd64, windows-amd64)
**Branching:** none (commits on `main`)

**Delivered:** A Go-based LLM gateway that exposes OpenAI-, Ollama-, and Anthropic-compatible HTTP APIs on a single port, routing every request through a configurable plugin chain to a warm pool of `kiro-cli` ACP worker subprocesses. Replaces the Node.js Ollama proxy with one statically-linked cross-platform binary that adds two new surfaces.

**Key accomplishments:**

- **All three API surfaces serving real clients** — LangFlow `/api/chat` (Ollama), Pi-SDK `/v1/chat/completions` (OpenAI), and loop24-client `/v1/messages` (Anthropic with `ANTHROPIC_BASE_URL`) all flow through one canonical engine → ACP worker subprocess pipeline. The "single governance surface" load-bearing property holds.
- **Single static cross-compiled binary** — Phase 9 closed the no-cgo, single-binary constraint with darwin-arm64, darwin-amd64, linux-amd64, and windows-amd64 artifacts shipped via tagged GitHub Releases. Trust-gate suite (gofumpt → vet → build → golangci-lint → govulncheck → `go test -race ./...`) runs on every PR plus nightly on `main`.
- **Plugin guardrails chain operational** — `PreHook`/`PostHook` interface over canonical types with RequestID, Auth bearer-token, structured logging, and PII redaction (encrypt mode tokenizes 11 entity types including the new US Address triad) registered as day-one hooks. Phase 8.1 closed the INTEG-01 blocker where streaming-mode PreHook short-circuits produced benign-looking streams with no auth error rendered.
- **Stateful sessions + warm pool** — Fixed-size `POOL_SIZE` pool of warm `kiro-cli` subprocesses; `X-Session-Id` opts requests into stateful sessions via SessionRegistry; idle entries reaped after `SESSION_TTL_MS`. `/health/agents` exposes per-slot detail.
- **Streaming with disconnect cancellation** — NDJSON (Ollama) and SSE (OpenAI + Anthropic) off one canonical chunk channel; client disconnect cancels in-flight `session/prompt` via `session/cancel`. Phase 08.3 refactored `acp.Client.Prompt()` from blocking to early-return-with-goroutine to close a 64-slot chunk-buffer-overflow deadlock.
- **PII coverage hardening** — Eleven entity types redacted with byte-for-byte encrypt-mode round-trip across all three surfaces, including the v1.5-closing US Address PII coverage (street addresses, state codes, ZIP codes) with overlap arbiter suppressing NER PERSON false positives on street names. Operator HUMAN-UAT confirmed 33/33 needle checks on splunk-box v1.10.0 binary.

**Open decisions resolved at close:**

- Phase 08.3.1 (ACP Per-Session Stream Demux): deferred to v1.6 — WR-04 cross-session leak race not exploitable under v1's POOL_SIZE=4 pool model (each `acp.Client` bound to one worker slot; concurrent prompts on the same Client are not part of v1's multi-tenant scope).

**Known deferred / accepted tech debt (carried to v1.6):**

- Pre-existing gofumpt drift across `cmd/` + `internal/adapter/*` (16+ files; Phase 2/3.1/8 origin). `make ci` fails locally at `fmt-check` until cleaned up.
- `internal/admin/tail_test.go` uses Go 1.24 `testing.Context()` while `go.mod` is 1.23 (Phase 6.1 origin).
- Nyquist validation: 3/11 phases fully compliant — accepted-trailing.
- Phase 02 HUMAN-UAT: 3 operator-side gates (real-kiro round-trip, LangFlow zero-reconfig, auth posture smoke) operator-deferred per 2026-05-28 audit. Implicitly verified by Phases 8.2 (LangFlow `format` parity, which requires base /api/chat) and the v1.10.0 splunk-box smoke.
- Phase 8 HUMAN-UAT: 7-step operator protocol operator-deferred per audit.
- Phase 08.4 documented accepted_deviations: WR-NEW-03 USState span subsumes trailing ZIP; BL-NEW-01 acknowledged AP-2 residual on English-word + 5-digit-quantity ambiguity (inherent RE2 limit, no lookahead).

**Audit references:**
- `.planning/milestones/v1.5-MILESTONE-AUDIT.md` — full pre-close audit (status: passed)
- `.planning/milestones/v1.5-ROADMAP.md` — archived per-phase detail
- `.planning/milestones/v1.5-REQUIREMENTS.md` — archived traceability

---
