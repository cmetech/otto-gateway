# Milestones

## v1.10.3 Reliability Closeout (Shipped: 2026-06-12)

**Phases completed:** 3 phases (18, 19, 20), 5 plans
**Requirements:** 17/17 satisfied (REL-CFG-05/06/07, REL-HTTP-06/07, REL-OBSV-02/03/04, REL-TRAY-08/09, REL-ACP-01, QUAL-01..06)
**Audit:** [milestones/v1.10.3-MILESTONE-AUDIT.md](milestones/v1.10.3-MILESTONE-AUDIT.md) · **Archive:** [milestones/v1.10.3-ROADMAP.md](milestones/v1.10.3-ROADMAP.md)

**Delivered:** Closed the long-tail surfaced at v1.10.2 release: 8 deferred Low-severity findings from the 2026-06-11 reliability review (Phase 18), the production `acp.Stream.Result` race flagged by Phase 17's threat scan (Phase 19), and 6 Info-level Phase 16/17 code-review cleanups (Phase 20). 17/17 requirements closed; `make ci` exits 0 end-to-end at milestone tip.

**Key accomplishments:**

- **Phase 18 — Reliability long-tail (10 REQ-IDs, 3 parallel plans).** Config hardening: degenerate `AUTH_TOKEN` / `ALLOWED_IPS` now WARN + treat-as-unset (REL-CFG-05); `KIRO_CMD` / `KIRO_CWD` errors name the variable + `~` expansion for `KIRO_CWD` (REL-CFG-06); HTTP_ADDR bind-then-close port probe surfaces port-in-use pre-warmup (REL-CFG-07). Observability symmetry: Ollama streaming `eng.Run` failures emit mirrored REL-HTTP-03 WARN (REL-HTTP-06); defense-in-depth `defer recover()` on 4 background goroutine sites (REL-HTTP-07); worker death + recovery emit paired causal-chain log (REL-OBSV-02); kiro-cli stderr captured into structured slog file with `worker_pid` / `slot_id` tags (REL-OBSV-03); single `Config.AdminTailPath` source-of-truth for log-tail (REL-OBSV-04). Tray honesty: sentinel-driven `StateError` for wrapper dotenv parse failures (REL-TRAY-08); macOS support-bundle tray rows either correct or removed (REL-TRAY-09).
- **Phase 19 — acp.Stream concurrency fix (1 REQ-ID, 1 plan).** REL-ACP-01: `acp.Stream.Result` now snapshots `*s.result` under `s.mu` (`cp := *s.result; return &cp`) instead of returning a pointer that races `close(s.done)` vs the StopReason write. Signature unchanged. New 60-iteration race-loop whitebox regression test (`internal/acp/regression_rel_acp_01_test.go`) — 6,000 race trials zero data-race reports. Phase 17 test-side drain-Chunks-then-Result workaround in `regression_rel_pool_02_test.go` surgically reverted (-20 / +0).
- **Phase 20 — Code-review backlog burn-down (6 REQ-IDs, 1 plan, 6 atomic refactor commits).** QUAL-01: `escapeApplescript` escape-set expanded to cover newlines + control chars with table-driven darwin-build-tag unit tests. QUAL-02: `tooltipForState` deduplicated into shared `cmd/otto-tray/tooltip.go` under `//go:build darwin || windows`. QUAL-03: `forceCloseCh` allocation relocated to `RunUntilSignal` (nil-channel select-never idiom); new `internal/server/run_direct_test.go` regression guard. QUAL-04: `tailLines` O(n²) prepend replaced with collect-then-reverse. QUAL-05: dead `sessions`/`sessionsMu` vars removed from REL-POOL-02 test. QUAL-06: stale `removeSlot` comment refreshed.
- **Trust gates green end-to-end.** `make ci` at tip (`0871a38`): `go fmt` + `go vet` + `go build` + `golangci-lint run` (0 issues) + `go test -race ./...` + `go-arch-lint` + `govulncheck` all clean. TRST-04 adapter-over-canonical boundary preserved across all 3 phases. Zero touches to OpenAI / Anthropic adapters; Ollama adapter touched only for REL-HTTP-06's server-side WARN log (no wire change). All 3 v1 client integrations (LangFlow → Ollama, Pi-SDK → OpenAI, loop24-client → Anthropic) byte-identical at the wire.
- **Phase 20 self-review closed 2 Warnings inline** via `/gsd-code-review 20 --fix`: WR-01 guarded `forceCloseCh` allocation with `sync.Once` (commit `275def8`); WR-02 dropped `time.Sleep(100ms)` readiness anti-pattern from new Run-direct test (commit `cdb2fe5`).
- **Phase 18 mid-milestone CI repair.** Commit `af850a2` retroactively dropped an unused `level` param from `startAndDrain` that broke baseline CI between Phase 19's completion and Phase 20's execution (`unparam` lint regression). Recorded for retrospective.

**Tech debt deferred (non-blocking, documented in audit):**

- **WR-03:** `time.Sleep(100ms)` readiness pattern in `internal/pool/regression_rel_pool_02_test.go:134` predates v1.10.3 (introduced Plan 17-02 / D-17-04); deferred to a future phase that owns pool-test readiness signalling.
- **IN-01..IN-05** (5 Info findings from Phase 20 self-review): half-done `tooltipForState` dedup leaving `cmd/otto-tray/tray.go` open-coding the header format; missing CRLF / strip / strip-only cases in `escapeApplescript` table tests; allocating 2-element slice for `for range []*blockingPromptClient{bc0,bc1}`; `RunUntilSignal`'s defensive select-then-close(forceCloseCh) is dead code under the new `sync.Once` writer; new `cmd/otto-tray/tooltip.go` has no companion test file.
- **Lint hygiene:** stale `golangci-lint` cache pointing at deleted `/tmp/sv-20-reviewfix-*` worktrees can surface phantom `gosec G703` on cold runs (`golangci-lint cache clean` resolves). Candidate for a future hygiene phase: bake into `make lint` pre-step.

**Human verification deferred (4 operator gates, out-of-band):**

- REL-TRAY-02 (v1.9 carry) — Windows tray operator gate; code wired + statically verified.
- REL-TRAY-03 (v1.9 carry) — macOS GUI-session tray operator gate; code wired + statically verified.
- REL-TRAY-08 (new) — dotenv error → tray "config error" state; awaits visual confirmation on darwin/windows.
- REL-TRAY-09 (new) — macOS support bundle tray diagnostics output; awaits human run.

**Nyquist:** No `VALIDATION.md` for Phases 18/19/20 — milestone scope was reliability bug-fix + refactor cleanup, not feature coverage. Same posture as v1.9 audit.

**Acknowledged carry-forward at close (no new v1.10.3 blockers):**

- 21 stale `quick_tasks` tracking entries (all substantively shipped during v1.5–v1.8; tracking metadata never backfilled).
- 4 UAT-gap + 3 verification-gap inherited operator-deferred items from v1.5 / v1.8 / v1.9 (Phase 02 / 06 / 06.1 / 08 / 15).
- 2 new v1.10.4 todos: `bounded-bufio-reader-readstring-stderrdrainloop` (WR-01 ADR follow-up) + `perf-baseline-vs-node` (pre-existing).
- SEED-001 Authenticode (dormant; awaits cert procurement).

**Git stats:** Phases 18+19+20 spanned 2026-06-11 → 2026-06-12 (~24h elapsed). 84 commits on `main` since milestone open (`b2c0d8a`). All work on `main` (branching strategy `none`).

---

## v1.9 Reliability Hardening (Shipped: 2026-06-11)

**Phases completed:** 3 phases (14, 15, 16), 12 plans
**Requirements:** 27/27 satisfied
**Audit:** [milestones/v1.9-MILESTONE-AUDIT.md](milestones/v1.9-MILESTONE-AUDIT.md) · **Archive:** [milestones/v1.9-ROADMAP.md](milestones/v1.9-ROADMAP.md)

**Key accomplishments:**

- **23 reliability findings closed** (1 Critical + 8 High + 14 Medium) from the 2026-06-11 reliability review. Every finding verified by Phase 14's ledger before fix work began (read-only-implementation rule, zero production source edits in Phase 14).
- **`go test -race ./...` clean tree-wide** — REL-POOL-05 closed the `Entry.LastUsed` race via `atomic.Int64` conversion. The `-race` trust gate is restored for the first time since v1.5.
- **Pool lifecycle hardened on all 3 OSes** — bounded slot acquisition with typed HTTP 503 + Retry-After (REL-POOL-01), explicit `cleanup()` on every Ctrl-C path + in-flight stream Cancel during grace + two-signal force-exit (REL-POOL-02), CAS-guarded `activeStream` clear (REL-POOL-03), Windows process-tree kill via `taskkill /T /F` (REL-POOL-06), per-request stream ctx so slow consumers can't poison the readLoop (REL-POOL-04).
- **Mid-stream death surfaced honestly to clients** — OpenAI receives `data: {"error":...}` + `[DONE]` and Ollama receives `done:true, done_reason:"error"` on kiro-cli crashes; gateway WARN-logs the death with `worker_pid`, `kiro_exit_code`, `bytes_streamed`, `session_id` fields (REL-HTTP-03). Long-lived admin SSE no longer blocks the full 30s shutdown grace (REL-HTTP-01).
- **Tray honest on macOS + Windows** — PID identity verified before any kill/stop action via `verifyGatewayIdentity` (REL-TRAY-01); macOS icon/tooltip transition on FSM state change (REL-TRAY-03); Windows support-bundle completes even when the gateway is stopped (REL-TRAY-02 — `Get-GatewayStatus` returns object, no longer `exit 1`); non-blocking notify with 3-attempt/500ms-backoff retry (REL-TRAY-04); bundle size/time bounded with staging cleanup on timeout (REL-TRAY-07).
- **Config fail-closed** — negative/zero values for `POOL_SIZE`, `SESSION_TTL_MS`, `SESSION_MAX`, `SESSION_TICK_INTERVAL_MS`, `CHAT_TRACE_MAX_AGE_DAYS` now produce loud boot errors naming the offending variable; `POOL_SIZE > 256` rejected as sanity violation; `PING_INTERVAL <= 0` produces a named boot error instead of raw `time.NewTicker` panic; `EMBEDDING_MODEL_DEFAULT` Warn when set but unimplemented + CLAUDE.md doc fix; pool exhaustion no longer silent at default log level.
- **Phase 16 sequential auto-degrade pattern** — worktree execution auto-degraded to sequential when `origin/HEAD` divergence from current HEAD was detected (#683). All 5 Phase 16 plans ran serially on the main working tree with RED→GREEN TDD discipline. Pattern documented for future milestones with similar branch posture.

**Issues deferred (acknowledged at close):**

- REL-TRAY-02 + REL-TRAY-03 — platform-specific operator gates; code wired + statically verified, awaits human run on target platform
- 12 Low-severity findings — rolled to v1.10
- 5 Info-level code review findings from Phase 16 — non-blocking quality work
- 3 inherited operator-deferred smoke tests from v1.8 — not v1.9 blockers
- Nyquist VALIDATION.md for Phases 14, 16 — milestone scope was reliability bug-fix, not feature coverage

---

## v1.8 Nyquist Coverage Uplift (Shipped: 2026-06-07)

**Phases completed:** 1 phases, 6 plans, 8 tasks

**Key accomplishments:**

- One-liner:
- Phase 02 (Ollama End-to-End) VALIDATION.md lifted from nyquist_compliant: false to true — 23-row per-task verification map filled, all 9 Wave 0 fixtures confirmed, 6 sign-off boxes ticked, zero production source edits.
- Nyquist audit of Phase 06 (Tool-Call Path): per-task map populated for all 20 VALIDATION.md V-rows, frontmatter flipped to `nyquist_compliant: true`, all 6 sign-off boxes ticked. Zero production source edits. All auto-classified rows verified green under `go test -race ./internal/engine/...`.
- Lifted Phase 08 (Plugin Hook Chain) VALIDATION.md to the post-08.1 Nyquist standard: filled all 26 task rows, ticked Wave 0 requirements, verified sampling continuity, ticked 6 sign-off boxes, and flipped `nyquist_compliant: false → true` — largest target in the v1.8 milestone (5 plans, 26 tasks, 4-hook chain covering auth, PII, logging, and chain ordering).

---

## v1.7 Go Stdlib CVE Cleanup (Shipped: 2026-06-07)

**Phases completed:** 1 phase (Phase 12)
**Plans:** 1
**Requirements:** 4/4 satisfied (CVE-01, CVE-02, CVE-03, CI-02) — zero carve-outs
**Timeline:** 2026-06-07 (same-day milestone — planned, executed, audited, archived in a single session)
**Commits (feat+fix+docs):** 8
**Branching:** none (commits on `main`)

**Delivered:** Closed the v1.6 Phase 11 D-11-01 carve-out by bumping `go.mod`'s `go` directive from `1.25.0` to `1.26.4`. Drained all 23 baseline stdlib CVEs (GO-2026-5039 through GO-2025-4007) to zero. `make ci` exits 0 end-to-end for the first time since v1.5 shipped.

**Key accomplishments:**

- **23 → 0 stdlib CVEs** — One-line bump in `go.mod` (`go 1.25.0` → `go 1.26.4`) plus `go mod tidy` (no `go.sum` delta). `~/go/bin/govulncheck ./...` reports `No vulnerabilities found.`
- **D-12-01 two-step decision** — Initial bump to `1.26.3` to match the developer's local toolchain surfaced 2 reachable residuals (GO-2026-5039 in `net/http`, GO-2026-5037 in `x509`). Rather than `//nolint:gosec` exempt them, tightening to `1.26.4` (minimum patch level that closes all 23 CVEs) yielded zero residual taints and zero exemptions. Two commits + an extra `go mod tidy` traded for a cleaner final state. Documented as D-12-01.
- **D-11-01 carve-out closed** — `make ci` runs the full brief §3.12 sequence end-to-end (gofumpt → vet → build → lint → test-race → arch-lint → examples → govulncheck → cross) and exits 0. 12-01-SUMMARY.md carries 13 cross-references to the carve-out language in 11-01-SUMMARY.md confirming the closure.
- **CI fully green** — CI run [27081876026](https://github.com/cmetech/otto-gateway/actions/runs/27081876026) is the first since the v1.6 lint gate was restored where all 3 jobs report success simultaneously: `lint + test-race + arch-lint + govulncheck` ✓, `publish.sh dry-run + Layer-1 tests` ✓, `cross-compile (darwin arm64+amd64, linux amd64, windows amd64)` ✓.
- **Scope discipline held** — Production source diff is `go.mod | 2 +-`. Zero opportunistic edits. No language-feature uptake, no test refactors, no third-party dep bumps beyond `go mod tidy` auto-resolution. The CLAUDE.md "Don't refactor beyond what the task requires" discipline held end-to-end through both Phase 10's complex multi-wave drain and Phase 12's micro-bump.

**Open decisions resolved at close:**

- **D-12-01 Two-step Go bump** — see Key accomplishments above. Final pin: `go 1.26.4`.

**Known deferred / accepted tech debt (carried to v1.8):**

- **Phase 08.3.1 ACP Per-Session Stream Demux** — carried from v1.5, re-re-deferred from v1.6 and v1.7. Multi-tenant concern not exploitable under v1's POOL_SIZE=4 model.
- **Nyquist coverage uplift** — 6 of 13 v1.5 phases have `nyquist_compliant: false`. (The previously-documented "3/11" figure was stale.) Bring older phases up to the post-08.1 validation standard.
- **Windows Authenticode code-signing** — Long pole; requires code-signing certificate procurement.

**Audit references:**

- `.planning/milestones/v1.7-MILESTONE-AUDIT.md` — full pre-close audit (status: passed; **zero warnings**)
- `.planning/milestones/v1.7-ROADMAP.md` — archived per-phase detail
- `.planning/milestones/v1.7-REQUIREMENTS.md` — archived traceability

---

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
