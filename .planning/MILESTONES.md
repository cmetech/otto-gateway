# Milestones

## v1.6 Tooling Cleanup (Shipped: 2026-06-07)

**Phases completed:** 2 phases (10, 11)
**Plans:** 5 across the 2 phases
**Requirements:** 6/6 satisfied (LINT-01, LINT-02, LINT-03, FMT-01, FMT-02 with documented v1.7 carve-out, CI-01)
**Timeline:** 2026-06-07 (single-day milestone — planned, executed, audited, archived)
**Commits (feat+fix+docs):** 31
**Branching:** none (commits on `main`)

**Delivered:** Drained the trust-gate violation backlog (golangci-lint v2 baseline 49 → 0 violations) and restored CI's lint step as a hard merge gate. Added a local pre-commit gofumpt hook + contributor enablement docs so lint/fmt regressions cannot land silently on either side of the gate.

**Key accomplishments:**

- **golangci-lint v2 cleanup, 49 → 0** — Phase 10 across 3 waves of category-grouped fixes: Wave 1 mechanical (staticcheck QF1001, unused, revive redefines-builtin-id, gosec G301, noctx; 16 sites), Wave 2 wrapcheck wraps + unparam triage (22 sites, 11 of which got scoped `//nolint:unparam` with rationale for test-helper signature stability), Wave 3 real-review for gosec G703/G705 + bodyclose + nilerr + revive remainder (11 sites, including a targeted `json.NewEncoder` rewrite for G705 XSS in admin/sse.go).
- **CI lint gate restored** — Phase 10 Wave 4 removed `continue-on-error: true` from `.github/workflows/ci.yml`. Proven by a deliberate negative-test PR (#1) whose lint step failed with `internal/version/lintbreaker.go:5:6: func unusedHelperForGateNegativeTest is unused (unused)`, exit code non-zero, vs. the same workflow on `main` reporting `golangci-lint found no issues`.
- **5-layer CI-config rot exposed and closed** — Wave 4 caught what the gate's absence had been hiding: gofumpt trailing-blank-line regression in `internal/plugin/request_id.go`; `golangci-lint-action@v6` cannot install golangci-lint v2.x → bump to `@v7`; pin `v2.1.6` was built with Go 1.24 vs go.mod's Go 1.25.0 → bump to `v2.12.2` (built with Go 1.26); `wrapcheck.ignoreSigs` was v1 schema → migrate to v2 `extra-ignore-sigs`. Each closed in its own atomic commit with rationale.
- **gofumpt tree-wide clean** — `gofumpt -l .` returns empty on `main`. The pre-existing v1.5 drift across `cmd/` and `internal/adapter/*` was incidentally cleaned during Phase 10 work (notably Wave 4's `52da974`); Phase 11 verified.
- **Pre-commit gate operational** — `.pre-commit-config.yaml` carries a gofumpt hook (via `scripts/pre-commit-gofumpt.sh` shell delegate per D-11-03) alongside the pre-existing golangci-lint hook (pin matches CI's `v2.12.2`). `docs/operating.md` documents `pre-commit install` enablement.
- **Per-category decision record** — every linter category from the baseline (wrapcheck, unparam, revive, gosec, unused, noctx, staticcheck, bodyclose, nilerr) has a documented fix-policy / exemption-pattern in `10-04-SUMMARY.md` "LINT-03 evidence" table.

**Open decisions resolved at close:**

- **D-11-01 govulncheck routed to v1.7.** Phase 10's gate restoration unmasked Go stdlib CVE failures in `govulncheck`. v1.6's narrow envelope does not include vulnerability cleanup. Captured in PROJECT.md "Deferred to v1.7" + REQUIREMENTS.md "Future Requirements".
- **D-11-02 pre-commit hook over `make pre-commit` target.** The codebase already had `.pre-commit-config.yaml`; adding gofumpt there is lowest-friction. `make pre-commit` was rejected as unnecessary surface.
- **D-11-03 shell delegate extraction.** The plan's inline-bash YAML hook entry tripped `mapping values are not allowed` because of `: ` inside the install hint. Extracted to `scripts/pre-commit-gofumpt.sh` with no behavioral change.

**Known deferred / accepted tech debt (carried to v1.7):**

- **Go stdlib CVE backlog** — `govulncheck ./...` fails on multiple CVEs (GO-2026-5039, -5037, -4982, -4980, -4971, -4947, -4946, -4870, …). Pre-existed v1.6 but were hidden by the failing lint step. v1.7 starting move: bump Go toolchain pin and re-run.
- **Phase 08.3.1 ACP Per-Session Stream Demux** — carried from v1.5, re-deferred from v1.6.
- **Nyquist coverage uplift** — 3/11 v1.5 phases fully compliant.
- **Windows Authenticode code-signing.**

**Audit references:**
- `.planning/milestones/v1.6-MILESTONE-AUDIT.md` — full pre-close audit (status: passed; 2 non-blocking warnings closed at audit time)
- `.planning/milestones/v1.6-ROADMAP.md` — archived per-phase detail
- `.planning/milestones/v1.6-REQUIREMENTS.md` — archived traceability

---

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
