---
phase: 15
phase_name: Fix Critical + High
milestone: v1.9 Reliability Hardening
created: 2026-06-11
prior_phase: 14 (verify-reliability-findings)
spec_loaded: false
audit_mode: standard
---

<domain>
## Phase 15: Fix Critical + High — domain

Production fixes for the 1 Critical + 8 High reliability findings confirmed by Phase 14's verification ledger (`.planning/phases/14-verify-reliability-findings/14-VERIFICATION-LEDGER.md`). Three subsystems, three findings each:

| Subsystem | Findings | REL-* IDs |
|---|---|---|
| Pool / ACP lifecycle | P-1 (Critical), P-2, P-3 | REL-POOL-01, REL-POOL-02, REL-POOL-03 |
| HTTP surface | H-1, H-2, H-3 | REL-HTTP-01, REL-HTTP-02, REL-HTTP-03 |
| Tray / wrapper | T-1, T-2, T-3 | REL-TRAY-01, REL-TRAY-02, REL-TRAY-03 |

Each fix MUST: (a) remove the corresponding `t.Skip("REL-<ID>...")` from the Phase 14 regression test, and (b) include the production source change in the same atomic commit. CI proves green via `go test -race ./...` on the unskipped reproducer. This locks in the red→green verification loop established by Phase 14 D-12 / D-13.

The phase does NOT close any Medium findings — those are Phase 16 scope.
</domain>

<canonical_refs>
## Canonical references (MUST read before planning / executing)

| Path | Why |
|---|---|
| `docs/reviews/2026-06-11-reliability-review.md` | Source of all 9 findings — full failure scenarios + fix sketches |
| `.planning/phases/14-verify-reliability-findings/14-VERIFICATION-LEDGER.md` | Master ledger — confirmed file:line cites for each finding |
| `.planning/phases/14-verify-reliability-findings/14-FINDING-P-1.md` ... `14-FINDING-T-3.md` (9 files) | Per-finding evidence — current-source behavior, why bug still exists |
| `.planning/phases/14-verify-reliability-findings/14-CONTEXT.md` | Phase 14 decisions that carry forward — D-12/D-13 unskip-in-same-commit pattern |
| `internal/pool/regression_rel_pool_{01,02,03}_test.go` | The 3 Pool/ACP regression tests to unskip + prove green |
| `internal/server/regression_rel_http_01_test.go`, `internal/server/regression_rel_http_04_test.go`, `internal/adapter/openai/regression_rel_http_{02,03}_test.go`, `internal/adapter/ollama/regression_rel_http_03_test.go` | HTTP regression tests (H-3 spans 2 files: OpenAI + Ollama) |
| `cmd/otto-tray/regression_rel_tray_{01,02,03}_test.go` + `tests/reliability/manual/REL-TRAY-02-repro.ps1`, `REL-TRAY-03-repro.sh` | Tray regression tests + manual reproducers (T-2/T-3 are platform-gated `//go:build darwin || windows`) |
| `CLAUDE.md` | Project constraints — Go 1.23+, no cgo in main binary, env-var backward-compat with Node names |
| `.planning/REQUIREMENTS.md` | REQ traceability for REL-POOL-01..03, REL-HTTP-01..03, REL-TRAY-01..03 — must flip to `fulfilled` at phase close |
</canonical_refs>

<code_context>
## Files modified by Phase 15 (production source)

### Pool / ACP
- `internal/pool/pool.go` — P-1 bounded acquire (`:491-505` blocking → bounded), P-1 removeSlot-on-respawn re-queue (`:534`), P-3 CAS-guarded activeStream clear via `internal/pool/pool.go:618-635`
- `internal/pool/config.go` — P-1 new env knob `POOL_ACQUIRE_TIMEOUT_MS`
- `internal/acp/client.go` — P-3 unconditional nil → CAS at `:868-870` (ctx arm) and `:894-896` (frame arm)
- `cmd/otto-gateway/main.go` — P-2 `os.Exit` removed from `:131`; deferred cleanup at `:127` must run on every exit path
- `internal/server/server.go` — P-2 in-flight stream cancel during 30s grace (`:377-381`); H-1 long-lived SSE unwinds during grace

### HTTP surface
- `internal/admin/sse.go` — H-1 admin SSE responds to gateway shutdown ctx (`:167-203`)
- `internal/adapter/openai/sse.go` — H-2 explicit `session/cancel` before slot return on idle-timeout / write-error (`:460-462`, `:482-484`); H-3 surface-native terminal error frame at `:543-557`
- `internal/adapter/ollama/ndjson.go` — H-3 surface-native terminal `done:true` + error string at `:541-549`
- (Anthropic surface is in scope for H-3 only if Anthropic SSE has the same silent-truncation path — check `internal/adapter/anthropic/sse.go`; plan-time decision)

### Tray
- `cmd/otto-tray/tray.go` — T-1 PID-identity check before stop/restart (`:144-145`); T-3 `applyState()` (`:199-201`) extended to call `setIcon` + `SetTooltip` on every FSM transition
- `cmd/otto-tray/uihelpers_darwin.go` — T-3 replace no-op notification (`:43-58`) with state-aware icon/tooltip helpers (notification path stays as best-effort secondary signal)
- `scripts/otto-gw.ps1` — T-1 (PowerShell stop/restart PID check, `:562-564`), T-2 `Get-GatewayStatus` no longer script-terminating exit (`:581-593`), `Invoke-Support` completes on gateway-down (`:1464`)
- `scripts/otto-gw` — T-1 bash PID-identity check (`:782-784`)

### Test scaffolds (unskip + green-up)
- All 9 regression test files from Phase 14 must have their `t.Skip(...)` removed in the same commit as the production fix. The test body becomes the verification proof.
</code_context>

<decisions>
## Implementation Decisions

### Plan structure — 3 plans, one per subsystem

- **D-01:** Phase 15 splits into **3 parallel plans, one per subsystem**, mirroring the Phase 14 shape minus the config plan (Phase 15 doesn't touch config/hooks/observability). Each plan owns exactly 3 findings:
  - **Plan 15-01: Pool / ACP** — P-1 (REL-POOL-01), P-2 (REL-POOL-02), P-3 (REL-POOL-03). Touches `internal/pool/`, `internal/acp/`, `cmd/otto-gateway/main.go`, `internal/server/server.go` (P-2 in-flight cancel during grace).
  - **Plan 15-02: HTTP surface** — H-1 (REL-HTTP-01), H-2 (REL-HTTP-02), H-3 (REL-HTTP-03). Touches `internal/admin/sse.go`, `internal/adapter/openai/sse.go`, `internal/adapter/ollama/ndjson.go`, possibly `internal/adapter/anthropic/sse.go` (H-3 check).
  - **Plan 15-03: Tray / wrapper** — T-1 (REL-TRAY-01), T-2 (REL-TRAY-02), T-3 (REL-TRAY-03). Touches `cmd/otto-tray/`, `scripts/otto-gw`, `scripts/otto-gw.ps1`.
- **D-01a:** Cross-plan boundary risk: Plan 15-01 touches `internal/server/server.go` for P-2's in-flight cancel; Plan 15-02 also touches files under `internal/server/` and `internal/admin/`. **Planner's job to confirm `files_modified` is disjoint** at plan-time; if overlap is real (server.go shared between P-2 and H-1), put both P-2 and H-1 in Plan 15-01 OR serialize the two plans (`depends_on: ["15-01"]`).
- **D-02 [informational]:** All 3 plans run in parallel via `gsd-executor` with worktree isolation, same flow that worked for Phase 14.
- **D-03:** Each finding's task within its plan follows the **unskip-in-same-commit** pattern locked by Phase 14 D-12/D-13: production source edit + `t.Skip()` removal + (if needed) reproducer fixture in one atomic commit per finding. PR diff shows red→green on the reproducer.

### Configuration knobs — new env vars where needed, defaults conservative

- **D-04:** **New env var `POOL_ACQUIRE_TIMEOUT_MS`** for P-1's bounded slot acquire. **Default 30000 (30s)**. Rationale: long enough to absorb genuine kiro-cli warmup under load (typical warmup 2–8s + tail latency); short enough that callers see a typed 503 within their normal request budget. Adds to the env surface but matches the env-var-driven config style already in place (`POOL_SIZE`, `SESSION_TTL_MS`). Name follows the `<SUBJECT>_<KNOB>_MS` Node convention.
- **D-05:** **P-2 shutdown grace reuses the existing 30s timeout at `internal/server/server.go:377-381`** for the 1st-SIGINT graceful drain. No new env var for shutdown grace in v1.9. Semantics:
  - 1st SIGINT → cancel gateway ctx → in-flight HTTP handlers exit (P-2 fix: cancel propagates to live streams via existing ctx tree) → 30s drain → SIGKILL kiro-cli children if drain expires → main returns 0
  - 2nd SIGINT inside the grace window → force-cancel in-flight + immediate SIGKILL of all kiro-cli children + exit 130
- **D-06 [informational]:** No new env vars for H-1/H-2/H-3 — these are control-flow fixes, not policy choices.

### Error response shape — surface-native bodies + Retry-After: 5 on P-1

- **D-07 (P-1 503 envelope):** Pool-exhaustion 503 emits a **surface-native error body PLUS `Retry-After: 5` header** on all three surfaces. Bodies:
  - **OpenAI** (`/v1/chat/completions`): `{"error":{"type":"server_error","code":"pool_exhausted","message":"all workers busy; retry in 5s","param":null}}` — `code` is OpenAI-extension; matches Pi-SDK retry semantics.
  - **Ollama** (`/api/chat`, `/api/generate`, `/api/embeddings`): `{"error":"pool_exhausted: all workers busy; retry in 5s"}` — single `error` key matches Ollama's documented error shape; LangFlow parses this.
  - **Anthropic** (`/v1/messages`): `{"type":"error","error":{"type":"overloaded_error","message":"all workers busy; retry in 5s"}}` — `overloaded_error` is the documented Anthropic 529-style envelope; `@anthropic-ai/sdk` and loop24-client handle it as a retryable.
  - **Retry-After: 5** on every 503 — clients respecting the header (most SDKs) auto-retry.
- **D-08:** Internal typed error: define **`pool.ErrPoolExhausted`** as a sentinel `error` in `internal/pool/pool.go`. Bounded acquire returns it on timeout. Per-surface adapters map `errors.Is(err, pool.ErrPoolExhausted)` to the 503 path above. Keeps the surface-specific envelope assembly in the adapter, not in the pool.
- **D-09 (H-3 mid-stream death frame):** When a mid-stream worker dies (kiro-cli exits or `Result()` returns non-nil), each surface emits a **surface-native terminal error frame** AND the server logs **WARN** with session ID + worker PID + kiro-cli exit code.
  - **OpenAI SSE**: emit a final chunk with `"choices":[{"finish_reason":"error","delta":{}}]` then a synthetic OpenAI-style error event `data: {"error":{"type":"server_error","code":"upstream_disconnect","message":"worker terminated mid-stream"}}\n\n`, followed by `data: [DONE]\n\n`. (Some OpenAI SDKs terminate on the error event; `[DONE]` is defensive.)
  - **Ollama NDJSON**: emit a final line `{"done":true,"done_reason":"error","model":"...","error":"upstream_disconnect: worker terminated mid-stream"}`. The `done:true + error` shape is what LangFlow's NDJSON aggregator needs to see to stop on failure.
  - **Anthropic SSE** (if H-3 also affects `/v1/messages`): emit a `event: error\ndata: {"type":"error","error":{"type":"overloaded_error","message":"worker terminated mid-stream"}}\n\n` per Anthropic spec, then `event: message_stop`.
  - **WARN log fields**: `session_id` (request-scoped X-Session-Id or generated), `worker_pid` (from pool slot), `kiro_exit_code` if available, `bytes_streamed` (best-effort counter). Format: structured slog fields; one line per failure.
- **D-10:** **WARN log is non-negotiable** — visibility into mid-stream failures is the load-bearing observability fix for H-3; surface-native frame alone leaves operators blind.

### Tray T-3 macOS death visibility — applyState extends to icon + tooltip

- **D-11:** **Replace the LSUIElement no-op notification path with always-visible tray state.** `applyState()` (currently only edits menu-item titles, `cmd/otto-tray/tray.go:199-201`) gets extended to call **`setIcon(state)` AND `SetTooltip(state)` on every FSM transition**. Three icon variants (kept simple): **green = Running, yellow = Starting/Stopping, red = Error/Stopped**. Tooltip carries the short status string + uptime / last-error summary.
- **D-12:** **Notification banner stays as best-effort secondary signal.** `notify(...)` continues to be called on Running→Stopped/Error transitions, but it is no longer the load-bearing channel — `setIcon` + `SetTooltip` are. The known LSUIElement no-op behavior on macOS becomes a documented quirk, not a bug. uihelpers_darwin.go's comment at `:51-58` gets a docstring update explaining the demotion to secondary signal.
- **D-13:** **Apply the same icon/tooltip pattern to Windows** (`uihelpers_windows.go`). Even though T-3 is macOS-specific in the review, the fix's contract — applyState() always reflects FSM state on visible UI — should hold cross-platform. Windows tray notify already works; we add icon/tooltip parity so the test contract is uniform.
- **D-14:** **Icon assets:** add 3 SVG/PNG variants under `cmd/otto-tray/icon/` (whatever the existing icon scheme uses). If only one icon exists today, add the missing 2 in a pre-task before T-3 lands. Brand palette is the existing brand colors from `cmd/otto-tray/icon/` — researcher confirms whether `cmd/otto-tray/icon/icon.go`'s `MenuBarIcon` already supports tinting.
- **D-15 (T-2 windows support bundle):** `Get-GatewayStatus` refactored to **return a result object** (status string + error message) rather than calling `exit 1` mid-script. `Invoke-Support` calls it via `$status = Get-GatewayStatus` and handles the gateway-down branch by including a "gateway not running at bundle-time" note in the bundle. Bundle assembly proceeds; the operator gets evidence even when the gateway is down. This matches the bash wrapper's subshell-capture pattern (`scripts/otto-gw:1838-1840`).
- **D-16 (T-1 PID identity):** Read PID from pidfile, then verify via `Get-Process -Id $pid -ErrorAction SilentlyContinue` (PowerShell) / `ps -p $pid -o comm=` (bash) and check the command name matches `otto-gateway` (or `otto-gateway.exe`). If mismatch → log + return without stop. This prevents the "stop kills the wrong process" bug when otto-gateway crashed and a different process now owns that PID.

### Claude's Discretion (no further input needed)

- **Plan-internal task order:** each plan starts with its highest-severity finding (P-1 first in 15-01; H-1 first in 15-02; T-1 first in 15-03), then proceeds in REL-ID order. Each task = one finding's atomic commit (source + t.Skip removal + reproducer green).
- **Worktree merge order:** standard `gsd-executor` flow. The 3 plans touch mostly disjoint files (see code_context); orchestrator catches any real overlap at intra-wave check time and serializes if needed.
- **No new dependencies:** all 9 fixes are achievable with the stdlib + existing project deps. No new go.mod entries expected.
- **Anthropic surface for H-3:** if `internal/adapter/anthropic/sse.go` has the same silent-truncation path as OpenAI/Ollama, fold it into Plan 15-02 task for H-3. If it already emits a terminal error event, no work needed — document the asymmetry in the SUMMARY.
- **CLAUDE.md updates:** add `POOL_ACQUIRE_TIMEOUT_MS` to the env-var list under "Backward compat" with a note that it's net-new in v1.9. This update lands as part of Phase 15's close commit, NOT in a separate task.
- **REQUIREMENTS.md updates:** REL-POOL-01..03, REL-HTTP-01..03, REL-TRAY-01..03 flip to `status: fulfilled` at phase close.
- **No replanning of Phase 14:** all 9 findings landed in Phase 14 with `confirmed` status; the regression tests are already written. Phase 15 only needs to remove `t.Skip()` and make the tests pass.
- **PII bracket shape:** any log marker / sentinel string for redaction or test fixtures uses `[...]` not `<...>` — kiro-cli/Claude hang on angle brackets (per project anti-pattern memory).

</decisions>

<deferred>
## Noted for Later

- **terminal-notifier integration for macOS Notification Center** — would let the tray push real Notification Center entries that survive LSUIElement restrictions. Adds a binary dependency. Defer to v1.10 if D-11's icon/tooltip + best-effort notify isn't enough.
- **Bouncing dock icon (NSApp requestUserAttention) on Error** — requires un-LSUIElement-ing the binary temporarily or wiring CFBundlePackageType. Defer; D-11 is the v1.9 answer.
- **POOL_ACQUIRE_TIMEOUT_MS lower bound (e.g., refuse <100ms)** — would prevent operators from configuring a value too low to ever succeed. Useful nicety, not load-bearing for v1.9. Could fold into Phase 16's REL-CFG-01 (C-1 negative/zero env coercion) if it makes sense there.
- **Per-surface error code dictionary in PROJECT.md** — D-07's surface-native error shapes are documented here in CONTEXT.md but should eventually live in a single "Error envelopes" doc that adapters reference. Defer to v1.10 docs cleanup.
- **Two-tier shutdown grace (5s soft + 25s tail)** — rejected for v1.9 (D-05 reuses single 30s). Worth revisiting if v1.10 operator feedback shows the all-or-nothing grace is wrong.

</deferred>

<folded_todos>
## Folded Todos
- `perf-baseline-vs-node.md` — match score 0.6 on "phase, pool" keywords, but performance benchmarking is NOT in Phase 15 scope (v1.9 is reliability hardening, not performance). **Reviewed but not folded** — moved to `<deferred>` for the perf-tuning phase (likely v1.10 or v1.11).
</folded_todos>

<reviewed_todos>
## Reviewed Todos (not folded)
- `perf-baseline-vs-node.md` — keep open for the dedicated performance milestone; Phase 15 may incidentally affect tail latency (bounded acquire bounds P99 503 wait) but the baseline measurement is separate work.
</reviewed_todos>

<next_steps>
## Next Steps

`/clear` recommended, then:

`/gsd-plan-phase 15` — researcher + planner build 3 plans against the decisions above. Planner cross-checks `files_modified` per D-01a; if `internal/server/server.go` shows up in both 15-01 (P-2) and 15-02 (H-1), planner must either (a) merge P-2+H-1 into the same plan or (b) declare `15-02 depends_on: ["15-01"]`.
</next_steps>
