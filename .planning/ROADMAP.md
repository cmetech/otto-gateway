# Roadmap: OTTO Gateway

## Overview

OTTO Gateway is a from-scratch Go port of an existing Node.js Ollama
proxy, expanding the surface to also expose an OpenAI-compatible API on
the same port. The roadmap follows the M0–M9 milestone plan from
`docs/briefs/go_port_brief.md` §5, with M0 and M1 collapsed into a
single foundations phase. Each phase from Phase 2 onward delivers a
runnable, end-to-end vertical slice: Phase 2 is the first time a real
client gets a real response from `kiro-cli` through the gateway
(Ollama), Phase 3 brings the OpenAI surface online, and subsequent
phases layer streaming, the warm pool, tool calls,
guardrails, and finally the cross-compile / CI distribution story. The
adapter-over-canonical layout (brief §3.13) and trust-gate suite (brief
§3.12) are established in Phase 1 and enforced from then on.

## Milestones

- ✅ **v1.5 audit WARNINGs** — Phases 1, 1.1, 2, 3, 3.1, 4, 5, 6, 6.1, 8, 8.1, 8.2, 8.3, 8.4, 9 (shipped 2026-06-04). [Archive](milestones/v1.5-ROADMAP.md)
- ✅ **v1.6 Tooling Cleanup** — Phases 10, 11 (shipped 2026-06-07). golangci-lint v2 baseline drained from 49→0, CI lint gate restored, gofumpt clean tree-wide, pre-commit hook enabled. [Archive](milestones/v1.6-ROADMAP.md) · [Audit](milestones/v1.6-MILESTONE-AUDIT.md)
- ✅ **v1.7 Go Stdlib CVE Cleanup** — Phase 12 (shipped 2026-06-07). `go.mod` bumped 1.25.0 → 1.26.4; govulncheck 23 → 0; `make ci` exits 0 end-to-end without carve-outs for the first time since v1.5. [Archive](milestones/v1.7-ROADMAP.md) · [Audit](milestones/v1.7-MILESTONE-AUDIT.md)
- ✅ **v1.8 Nyquist Coverage Uplift** — Phase 13 (shipped 2026-06-07). Flipped the 6 remaining v1.5 phase VALIDATION.md docs from `nyquist_compliant: false` to `nyquist_compliant: true` (compliance ratio 7/13 → 13/13). Zero production source edits. 3 inherited operator-deferred UAT items tracked in 13-HUMAN-UAT.md. [Archive](milestones/v1.8-ROADMAP.md) · [Audit](milestones/v1.8-MILESTONE-AUDIT.md)
- 🚧 **v1.9 Reliability Hardening** — Phases 14, 15, 16 (opened 2026-06-11). Drive the 23 Critical/High/Medium findings from `docs/reviews/2026-06-11-reliability-review.md` to closure. Phase 14 verifies each finding against current source (read-only); Phase 15 fixes the 1 Critical + 8 High (load-bearing failure paths); Phase 16 fixes the 14 Mediums (includes P-5 race fix → `-race` trust-gate posture restored). 12 Lows deferred to v1.10. Driver: [docs/reviews/2026-06-11-reliability-review.md](../docs/reviews/2026-06-11-reliability-review.md).

## Phases

<details>
<summary>✅ v1.5 audit WARNINGs — SHIPPED 2026-06-04 (13 phases, 57 plans)</summary>

- [x] **Phase 1: Foundations** — Scaffold, trust-gate suite, ACP JSON-RPC client over `kiro-cli` stdio (2026-05-23)
- [x] **Phase 1.1: ACP Wire Alignment** *(INSERTED)* — Fix 10 Phase 1 wire-shape defects vs the working Node impl + live ACP spec (2026-05-23)
- [x] **Phase 2: Ollama End-to-End** — First runnable slice: LangFlow `POST /api/chat` reaches real `kiro-cli` (2026-05-24)
- [x] **Phase 3: OpenAI Surface** — Pi-SDK `POST /v1/chat/completions` shares the same canonical engine (2026-05-25)
- [x] **Phase 3.1: Anthropic Surface** *(INSERTED)* — loop24-client `POST /v1/messages` with Anthropic SSE shares the same canonical engine (2026-05-24)
- [x] **Phase 4: Streaming** — NDJSON (Ollama) and SSE (OpenAI + Anthropic) off one canonical chunk channel, with disconnect cancellation (2026-05-25)
- [x] **Phase 5: Pool + Stateful Sessions** — Warm `POOL_SIZE` pool plus `X-Session-Id` registry, both visible on `/health/agents` (2026-05-26)
- [x] **Phase 6: Tool-Call Path** — Canonical tool calls rendered per-surface, with `coerceToolCall` for plain-JSON-as-text (2026-05-27)
- [x] **Phase 6.1: Admin Observability UI** *(INSERTED)* — Dark-mode `/admin` page rendering `/health` + `/health/agents` with brand palette (2026-05-28)
- [x] **Phase 8: Plugin Hook Chain** — `PreHook`/`PostHook` over canonical types, with RequestID, Auth, Logging, PII redaction (2026-05-28)
- [x] **Phase 8.1: Close gap INTEG-01 + v1.5 audit WARNINGs** *(INSERTED)* — Streaming-mode PreHook short-circuit fix + auth posture docs + REQUIREMENTS.md traceability fixes (2026-05-30)
- [x] **Phase 8.2: Ollama `format` Parity** *(INSERTED)* — LangFlow `format:"json"` / `format:<schema>` steered via canonical PreHook (GEN_RULES block); response fence-stripped (2026-06-03)
- [x] **Phase 8.3: ACP Prompt() Non-Blocking Refactor** *(INSERTED)* — Closes 64-slot chunk-buffer-overflow deadlock; `Prompt()` returns *Stream early, finalize via goroutine (2026-06-03)
- [x] **Phase 8.4: US Address PII Coverage** *(INSERTED)* — Three regex recognizers (USAddress, USState, USZIP) + validateUSZIPRange ahead of NER; PII-01 added; v1.10.0 released (2026-06-04)
- [x] **Phase 9: Distribution** — Cross-compile Linux+Windows+darwin from macOS, full trust-gate CI matrix gating merges (2026-05-28)

**Reverted (kept for history):**

- Phase 08.3.2 (PII Smoke Test Methodology Fix) — superseded by prompt-only fix in `scripts/test-pii.{ps1,sh}` (REVERTED 2026-06-04, commit `ff10594`)

**Deferred to v1.6/v1.7:**

- Phase 08.3.1 (ACP Per-Session Stream Demux) — WR-04 cross-session leak race not exploitable under v1's POOL_SIZE=4 pool model (each `acp.Client` bound to one worker slot). Re-scoped to v1.7 per v1.6 narrow-scope decision.

Full per-phase detail: [v1.5-ROADMAP.md archive](milestones/v1.5-ROADMAP.md)

</details>

<details>
<summary>✅ v1.6 Tooling Cleanup — SHIPPED 2026-06-07 (2 phases, 5 plans)</summary>

- [x] **Phase 10: golangci-lint v2 cleanup + re-gate** — Drain the 49-issue v2 baseline to zero across 3 waves of category-grouped fixes, then remove `continue-on-error: true` and prove the gate fires via negative-test PR #1. Wave 4's "single ci.yml edit" expanded into a 5-commit closeout of latent v2-schema migration rot (gofumpt drift + action v6→v7 + version v2.1.6→v2.12.2 + wrapcheck.ignoreSigs→extra-ignore-sigs). LINT-01/02/03. (2026-06-07)
- [x] **Phase 11: gofumpt tree-wide cleanup + pre-commit gate** — FMT-01 already at 0 thanks to Phase 10 work; verified. FMT-02 §3.12 sequence exits 0 minus the v1.7-routed govulncheck step. CI-01 added gofumpt hook to existing `.pre-commit-config.yaml` (via `scripts/pre-commit-gofumpt.sh` shell delegate per D-11-03) + enablement docs in `docs/operating.md`. (2026-06-07)

Full per-phase detail: [v1.6-ROADMAP.md archive](milestones/v1.6-ROADMAP.md) · audit: [v1.6-MILESTONE-AUDIT.md](milestones/v1.6-MILESTONE-AUDIT.md)

</details>

<details>
<summary>✅ v1.7 Go Stdlib CVE Cleanup — SHIPPED 2026-06-07 (1 phase, 1 plan)</summary>

- [x] **Phase 12: Go toolchain CVE remediation** — Bumped `go.mod`'s `go` directive from `1.25.0` to `1.26.4` (two-step: 1.26.3 → tighten to 1.26.4 after Wave 1 surfaced 2 reachable residuals). Drained all 23 baseline stdlib CVEs (GO-2026-5039 through GO-2025-4007) to zero. `make ci` exits 0 end-to-end for the first time since v1.5 — closes v1.6 Phase 11 D-11-01 carve-out. CI run [27081876026](https://github.com/cmetech/otto-gateway/actions/runs/27081876026) all 3 jobs green. Production diff: `go.mod | 2 +-`. (2026-06-07)

Full per-phase detail: [v1.7-ROADMAP.md archive](milestones/v1.7-ROADMAP.md) · audit: [v1.7-MILESTONE-AUDIT.md](milestones/v1.7-MILESTONE-AUDIT.md)

</details>

<details>
<summary>✅ v1.8 Nyquist Coverage Uplift — SHIPPED 2026-06-07 (1 phase, 6 plans)</summary>

- [x] **Phase 13: Nyquist coverage uplift** — Cross-cutting sweep flipping the 6 v1.5 phase VALIDATION.md docs with `nyquist_compliant: false` (phases 02, 03, 06, 06.1, 08, 08.4) to `nyquist_compliant: true` (compliance ratio 7/13 → 13/13). 6 independent plans run as a single parallel wave via the `gsd-nyquist-auditor` agent. NYQ-02 / NYQ-03 / NYQ-06 / NYQ-06.1 / NYQ-08 / NYQ-08.4 / NYQ-ALL. Zero production source edits. (2026-06-07)

Full per-phase detail: [v1.8-ROADMAP.md archive](milestones/v1.8-ROADMAP.md) · audit: [v1.8-MILESTONE-AUDIT.md](milestones/v1.8-MILESTONE-AUDIT.md)

**Re-deferred to v1.9+ (out of v1.8 scope per opening decision):**

- Phase 08.3.1 (ACP Per-Session Stream Demux) — awaits a real multi-tenant deployment driver.
- Windows Authenticode code-signing — awaits code-signing certificate procurement.

</details>

<details open>
<summary>🚧 v1.9 Reliability Hardening — IN PROGRESS (opened 2026-06-11, 3 phases planned)</summary>

- [x] **Phase 14: Verify Reliability Findings** — Read-only audit of each of the 23 Critical/High/Medium findings from `docs/reviews/2026-06-11-reliability-review.md` against current `main` source. Produces `.planning/phases/14-*/14-VERIFICATION-LEDGER.md`: every finding tagged `confirmed` / `false-positive` / `needs-investigation` with file:line evidence. Tests may be added to prove a finding real/false; **no production source edits**. Ledger gates Phase 15/16 scope — false-positive findings are dropped from downstream phases before those phases plan their tasks. Covers REL-VERIFY-CRIT, REL-VERIFY-HIGH, REL-VERIFY-MED, REL-VERIFY-GATE. (completed 2026-06-11)
- [ ] **Phase 15: Fix Critical + High** — Production fixes for the 1 Critical + 8 High findings confirmed by Phase 14's ledger. Pool / ACP lifecycle: P-1 bounded slot acquire with typed 503 + re-queue on respawn-failure (REL-POOL-01), P-2 deferred cleanup runs on every shutdown exit code + in-flight stream cancel during grace + second-SIGINT force exit (REL-POOL-02), P-3 CAS-guarded `activeStream` clear (REL-POOL-03). HTTP surface: H-1 long-lived SSE unwinds during grace (REL-HTTP-01), H-2 explicit `session/cancel` before slot return on idle-timeout / write-error (REL-HTTP-02), H-3 surface-native terminal error frames + WARN log on mid-stream worker death (REL-HTTP-03). Tray: T-1 PID-identity verification before stop/restart (REL-TRAY-01), T-2 Windows support-bundle completes when gateway is down (REL-TRAY-02), T-3 macOS gateway-death visibility via icon/tooltip + non-no-op channel (REL-TRAY-03). **Depends on Phase 14 ledger.**
- [ ] **Phase 16: Fix Mediums** — Production fixes for the 14 Mediums confirmed by Phase 14's ledger. Pool: P-4 readLoop liveness independent of consumer drain (REL-POOL-04), P-5 race-free `Entry.LastUsed` restoring `-race` trust gate (REL-POOL-05), P-6 Windows process-tree kill via job object / `taskkill /T /F` (REL-POOL-06). HTTP: H-4 per-request body-read deadline (REL-HTTP-04), H-5 admin tailer line-cap enforced for newline-terminated lines (REL-HTTP-05). Hooks: G-1 non-streaming error paths run PostHook chain (REL-HOOKS-01). Tray: T-4 non-blocking Windows notify (REL-TRAY-04), T-5 tray consumes `/health/pool` + treats snapshot errors as degraded (REL-TRAY-05), T-6 Windows bundle-path parses last non-empty line (REL-TRAY-06), T-7 bounded bundle size/time with progress to stderr + staging cleanup on timeout (REL-TRAY-07). Config / observability: C-1 fail-fast on negative/zero pool/session knobs + `POOL_SIZE` upper bound (REL-CFG-01), C-2 `PING_INTERVAL <= 0` boot error (REL-CFG-02), C-3 `EMBEDDING_MODEL_DEFAULT` documented or warned at boot (REL-CFG-03), O-1 `Warn("pool: waiting for free slot", ...)` at default log level (REL-CFG-04). **Depends on Phase 14 ledger.**

</details>

## Phase Details

### Phase 14: Verify Reliability Findings

**Goal**: Every Critical/High/Medium finding from the 2026-06-11 reliability review is independently confirmed against current `main` source before any fix work is scheduled, so Phase 15 and Phase 16 plan against verified failure paths — not against a stale snapshot.
**Depends on**: Nothing (opens v1.9; v1.8 shipped clean)
**Requirements**: REL-VERIFY-CRIT, REL-VERIFY-HIGH, REL-VERIFY-MED, REL-VERIFY-GATE
**Success Criteria** (what must be TRUE):

  1. A verification ledger at `.planning/phases/14-*/14-VERIFICATION-LEDGER.md` lists all 23 findings (P-1, P-2, P-3, P-4, P-5, P-6, H-1, H-2, H-3, H-4, H-5, G-1, T-1, T-2, T-3, T-4, T-5, T-6, T-7, C-1, C-2, C-3, O-1) with a `confirmed` / `false-positive` / `needs-investigation` tag and a current-source file:line citation.
  2. Every `confirmed` row carries either a failing test, an instrumented reproducer command, or a code-walk note showing the failure path still exists at the cited site.
  3. Every `false-positive` row carries a current-source citation showing the failure path no longer exists, plus the REL-* REQ-ID it removes from Phase 15 / Phase 16 scope.
  4. The ledger is the explicit input to `/gsd-plan-phase 15` and `/gsd-plan-phase 16` — those phases plan only against `confirmed` rows. Deferrals (`needs-investigation`) are documented with a reason and a target follow-up phase.
  5. `git diff main...HEAD -- ':!*_test.go' ':!.planning/' ':!docs/'` is empty at phase close (read-only-implementation rule, same posture as v1.8 Phase 13).

**Plans**: 4 plans
Plans:

- [x] 14-01-PLAN.md — Pool/ACP: verify P-1..P-6 (REL-POOL-01..06) — 6 evidence files + 5 t.Skip Go reproducers + 1 manual Windows pgid reproducer + ledger fragment 01 (+ master ledger merge if last to finish)
- [x] 14-02-PLAN.md — HTTP: verify H-1..H-5 (REL-HTTP-01..05) — 5 evidence files + 6 t.Skip Go reproducers (H-3 spans OpenAI + Ollama) + ledger fragment 02
- [x] 14-03-PLAN.md — Tray/wrapper: verify T-1..T-7 (REL-TRAY-01..07) — 7 evidence files + 7 t.Skip Go stubs (T-2/T-3/T-6 point at manual scripts) + 3 manual reproducer scripts + ledger fragment 03
- [x] 14-04-PLAN.md — Config/Hooks/Obs: verify G-1, C-1..C-3, O-1 (REL-HOOKS-01, REL-CFG-01..04) — 5 evidence files + 5 t.Skip Go reproducers (C-1/C-2 direct templates of TestLoad_StreamIdleTimeoutSec_Negative) + ledger fragment 04

**Cross-cutting constraints:**

- Read-only-implementation rule: `git diff main...HEAD -- ':!*_test.go' ':!.planning/' ':!docs/' ':!tests/reliability/'` empty at plan close

### Phase 15: Fix Critical + High

**Goal**: The 1 Critical + 8 High failure modes confirmed by Phase 14's ledger no longer trigger under the everyday laptop-shutdown / sleep-wake / mid-stream-disconnect scenarios the review was scoped against — pool exhaustion is recoverable and surfaced, Ctrl-C stops orphaning kiro-cli trees, queued requests cannot receive silent empty 200s, mid-stream worker death is visible to clients on all surfaces, and the tray is honest about gateway state on macOS / Windows.
**Depends on**: Phase 14 (consumes the verification ledger; any finding tagged `false-positive` drops its REL-* requirement from this phase's scope before planning).
**Requirements**: REL-POOL-01 (P-1), REL-POOL-02 (P-2), REL-POOL-03 (P-3), REL-HTTP-01 (H-1), REL-HTTP-02 (H-2), REL-HTTP-03 (H-3), REL-TRAY-01 (T-1), REL-TRAY-02 (T-2), REL-TRAY-03 (T-3)
**Success Criteria** (what must be TRUE):

  1. With the pool intentionally driven to zero healthy slots (e.g. by killing every kiro-cli mid-flight), the next chat request returns a typed HTTP 503 within a bounded wait instead of hanging indefinitely, and the pool re-warms slots on demand — operator does not have to restart the binary to recover. (REL-POOL-01)
  2. Killing the gateway with Ctrl-C during a streaming response leaves zero `kiro-cli` processes alive after the binary exits — verified by `ps` / Task Manager — and a second Ctrl-C during the shutdown grace forces immediate exit *after* cleanup runs. (REL-POOL-02)
  3. After a stream is cancelled mid-flight (idle-timeout or client disconnect) and the slot is recycled, the next request acquiring that slot receives its actual generated content — it never receives a well-formed but empty response. (REL-POOL-03)
  4. Hitting Ctrl-C while the admin Log Tail tab is open returns control to the shell in under a second with exit code 0, instead of blocking the full 30s grace and exiting non-zero. (REL-HTTP-01)
  5. On the OpenAI idle-timeout path, the still-generating kiro-cli session is explicitly cancelled before the slot returns to the free pool — subsequent requests cannot acquire a slot whose worker is mid-abandoned-prompt. (REL-HTTP-02)
  6. Triggering a kiro-cli crash mid-stream causes OpenAI clients to receive an explicit `data: {"error":...}` frame followed by `[DONE]`, Ollama clients to receive a `done:true, done_reason:"error"` envelope, and the gateway to log the death at WARN — not at Debug, and never as a clean half-finished answer. (REL-HTTP-03)
  7. Wrapper `Stop` / `Restart` and the tray probe verify PID identity (process name / command line) before trusting a live pid; a recycled PID reads as "stopped" and Stop/Restart cannot kill an unrelated process. (REL-TRAY-01)
  8. Running the Windows `support` verb while the gateway is stopped produces a complete support bundle on disk — `Get-GatewayStatus`'s pidfile-absent / stale-pid branches no longer terminate the script. (REL-TRAY-02)
  9. When the gateway dies on macOS, the menu-bar icon and tooltip change to a state the user can observe at a glance, and critical failures route through a channel (icon swap, dialog, or title append) that does not silently no-op for the LSUIElement agent. (REL-TRAY-03)

**Plans**: TBD
**UI hint**: yes

### Phase 16: Fix Mediums

**Goal**: The 14 Medium failure modes confirmed by Phase 14's ledger are closed so the laptop deployment is robust under everyday use — no `-race` regressions, no orphaned Windows process trees, no silent memory leaks under retry storms, no quiet config silent-coercion, no half-helpful tray indicators, and pool exhaustion is diagnosable from the log alone.
**Depends on**: Phase 14 (consumes the verification ledger; any finding tagged `false-positive` drops its REL-* requirement from this phase's scope before planning). May run in parallel with Phase 15 if planning permits, but most plans touch the same files as Phase 15 (pool, openai/sse, ollama/ndjson, tray) and are likely serialized.
**Requirements**: REL-POOL-04 (P-4), REL-POOL-05 (P-5), REL-POOL-06 (P-6), REL-HTTP-04 (H-4), REL-HTTP-05 (H-5), REL-HOOKS-01 (G-1), REL-TRAY-04 (T-4), REL-TRAY-05 (T-5), REL-TRAY-06 (T-6), REL-TRAY-07 (T-7), REL-CFG-01 (C-1), REL-CFG-02 (C-2), REL-CFG-03 (C-3), REL-CFG-04 (O-1)
**Success Criteria** (what must be TRUE):

  1. `go test -race ./...` passes clean tree-wide on `main` — the `Entry.LastUsed` race is closed and the trust-gate `-race` posture is restored. (REL-POOL-05)
  2. With a deliberately stalled chunk consumer (e.g. an SSE client that stops reading), the gateway no longer escalates the worker's ping to SIGKILL on a healthy kiro-cli — the request fails its own deadline instead of poisoning the slot. (REL-POOL-04)
  3. On Windows, killing a stateful session or restarting the gateway leaves zero `kiro-cli` (or kiro child) processes alive after the binary exits, and slot teardown does not pay a 2s `WaitDelay` penalty. (REL-POOL-06)
  4. A POST request whose client stops sending the body mid-upload fails within a bounded read deadline (not hours via TCP keepalive), and long SSE response writes are unaffected by that deadline. (REL-HTTP-04)
  5. With `CHAT_TRACE=true` and the chat-trace tail open in the admin UI, a multi-MB newline-terminated chat-trace line is truncated to `TailerMaxLineBytes` in the ring buffer and on the SSE stream — total memory growth is bounded. (REL-HTTP-05)
  6. A non-streaming Ollama or Anthropic request that fails (idle-timeout 504 or `Result()` error 500) produces a `post_chain_out` record in `chat-trace.log` and leaves zero residual entries in `LoggingHook.startTimes` / `ChatTraceHook.startTimes` — a retry storm against a wedged kiro-cli does not leak memory. (REL-HOOKS-01)
  7. Calling Stop on Windows does not stall the tray's uiLoop for up to 30s and does not pop a foreground-stealing modal — `notify()` is dispatched off the uiLoop. (REL-TRAY-04)
  8. With every pool slot busy-but-not-serving (workers alive but wedged), the tray indicates a degraded state — it reports running only when `/health/pool` says the pool is actually serving. (REL-TRAY-05)
  9. On Windows with an env file present, "Create Support Bundle…" from the tray opens Explorer at the actual `.zip` archive — wrapper stdout chatter (e.g. `loaded env file: …`) does not corrupt the path parse. (REL-TRAY-06)
  10. A support-bundle run on a multi-GB log day completes within the wrapper's timeout, the archive is under the `--max-mb` cap (live-log copies tailed before redaction), the wrapper writes progress lines to stderr, and on timeout the staging directory is cleaned up rather than leaked. (REL-TRAY-07)
  11. Booting with `POOL_SIZE=0`, `SESSION_TTL_MS=-1`, `SESSION_MAX=0`, `SESSION_TICK_INTERVAL_MS=-5`, or `CHAT_TRACE_MAX_AGE_DAYS=-1` produces a loud boot error that names the offending variable (matching the existing `STREAM_IDLE_TIMEOUT_SEC` posture), and `POOL_SIZE` above a sanity ceiling is rejected with a named error. (REL-CFG-01)
  12. Booting with `PING_INTERVAL=-60000` (or `0`) produces a config-loader boot error that names `PING_INTERVAL` — the process no longer dies via a raw goroutine panic from `time.NewTicker`, and any defensive panic that does fire lands in the structured log file. (REL-CFG-02)
  13. Booting with `EMBEDDING_MODEL_DEFAULT=foo` either implements/stubs the embeddings surface coherently OR emits a startup `Warn` line that the variable is set but unimplemented; CLAUDE.md / docs no longer claim it's wired when it isn't. (REL-CFG-03)
  14. Driving the pool to full saturation (every slot acquired) causes the gateway to emit a `Warn("pool: waiting for free slot", "busy", ..., "size", ...)` line the first time a request parks at the default log level — operators can diagnose "the gateway silently stopped answering" from the log alone. (REL-CFG-04)

**Plans**: TBD
**UI hint**: yes

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1, 1.1, 2, 3, 3.1, 4, 5, 6, 6.1, 8, 8.1, 8.2, 8.3, 8.4, 9 | v1.5 | 57/57 | Complete | 2026-06-04 |
| 10, 11 | v1.6 | 5/5 | Complete | 2026-06-07 |
| 12 | v1.7 | 1/1 | Complete | 2026-06-07 |
| 13 | v1.8 | 6/6 | Complete    | 2026-06-07 |
| 14 | v1.9 | 4/4 | Complete    | 2026-06-11 |
| 15 | v1.9 | 0/TBD | Not started | — |
| 16 | v1.9 | 0/TBD | Not started | — |
