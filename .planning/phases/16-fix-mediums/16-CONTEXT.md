---
phase: 16
phase_name: Fix Mediums
milestone: v1.9 Reliability Hardening
created: 2026-06-11
prior_phase: 15 (fix-critical-high)
spec_loaded: false
audit_mode: standard
---

<domain>
## Phase 16: Fix Mediums — domain

Production fixes for the 14 Medium reliability findings confirmed by Phase 14's verification ledger (`.planning/phases/14-verify-reliability-findings/14-VERIFICATION-LEDGER.md`). Closes v1.9 Reliability Hardening. Five subsystems:

| Subsystem | Findings | REL-* IDs |
|---|---|---|
| Pool / ACP lifecycle | P-4, P-5, P-6, O-1 | REL-POOL-04, REL-POOL-05, REL-POOL-06, REL-CFG-04 |
| HTTP surface | H-4, H-5 | REL-HTTP-04, REL-HTTP-05 |
| Hooks | G-1 | REL-HOOKS-01 |
| Tray / wrapper | T-4, T-5, T-6, T-7 | REL-TRAY-04, REL-TRAY-05, REL-TRAY-06, REL-TRAY-07 |
| Config | C-1, C-2, C-3 | REL-CFG-01, REL-CFG-02, REL-CFG-03 |

Same red→green verification posture as Phase 15: each fix removes the corresponding `t.Skip("REL-<ID>...")` from the Phase 14 regression test in the same commit as the production source change. `go test -race ./...` must be clean tree-wide at phase close (success criterion #1, REL-POOL-05). The phase closes the v1.9 milestone — Lows defer to v1.10.
</domain>

<canonical_refs>
## Canonical references (MUST read before planning / executing)

| Path | Why |
|---|---|
| `docs/reviews/2026-06-11-reliability-review.md` | Source of all 14 Mediums — full failure scenarios + fix sketches |
| `.planning/phases/14-verify-reliability-findings/14-VERIFICATION-LEDGER.md` | Master ledger — confirmed file:line cites for each finding |
| `.planning/phases/14-verify-reliability-findings/14-FINDING-P-4.md`...`14-FINDING-O-1.md` (14 files) | Per-finding evidence — current-source behavior, why bug still exists |
| `.planning/phases/15-fix-critical-high/15-CONTEXT.md` | Phase 15 decisions that carry forward — error-envelope shapes, WARN log fields, PID-identity pattern, env-var conventions |
| `.planning/phases/14-verify-reliability-findings/14-CONTEXT.md` | Phase 14 D-12/D-13 unskip-in-same-commit pattern |
| `internal/pool/regression_rel_pool_{04,05,06}_test.go` | The 3 Pool regression tests to unskip + prove green |
| `internal/server/regression_rel_http_04_test.go`, `internal/admin/regression_rel_http_05_test.go` | HTTP regression tests for H-4, H-5 |
| `internal/server/regression_rel_hooks_01_test.go` (or wherever G-1 reproducer lives — verify in ledger fragment 02/04) | Hooks regression test for G-1 |
| `cmd/otto-tray/regression_rel_tray_{04,05,06,07}_test.go` + `tests/reliability/manual/REL-TRAY-*-repro.{sh,ps1}` | Tray regression tests + manual reproducers (T-4, T-6, T-7 are platform-gated) |
| `internal/config/regression_rel_cfg_{01,02,03}_test.go` | Config regression tests for C-1, C-2, C-3 |
| `docs/briefs/go_port_brief.md` §3.4 | Why embeddings stay deferred (C-3) — 4 options all unsuitable for v1.9 |
| `CLAUDE.md` | Project constraints — env-var backward-compat list (needs C-3 doc fix) |
| `.planning/REQUIREMENTS.md` | REQ traceability for all 14 Phase 16 IDs — must flip to `fulfilled` at phase close |
</canonical_refs>

<code_context>
## Files modified by Phase 16 (production source) — planner confirms `files_modified` at plan time

### Pool / ACP (Plan 16-01 — P-4, P-5, P-6, O-1)
- `internal/pool/pool.go` — P-4 readLoop liveness independent of consumer drain; P-5 `Entry.LastUsed` race fix; O-1 `Warn("pool: waiting for free slot", ...)` on first park
- `internal/pool/config.go` — possibly nothing new (per D-03 below)
- `internal/acp/client.go` — P-4 readLoop signaling (specific lines per ledger fragment 01)
- `cmd/otto-gateway/main.go` (windows-specific) — P-6 Windows process-tree kill via job object or `taskkill /T /F`
- Windows-specific cmd helper (likely `internal/pool/proc_windows.go` or similar) — P-6 changes

### HTTP surface (Plan 16-02 — H-4, H-5)
- `internal/server/server.go` — H-4 per-request body-read deadline wrapper applied to chat-body POST handlers (chat/completions / chat / generate / messages / embed)
- `internal/server/health.go` — extended for T-5 (cross-plan dependency — see D-05); Plan 16-04 imports the new field
- `internal/admin/sse.go` or `internal/admin/tailer.go` — H-5 newline-terminated line cap enforced via `TailerMaxLineBytes`

### Hooks (Plan 16-03 — G-1)
- `internal/adapter/ollama/handlers.go`, `internal/adapter/anthropic/handlers.go` (non-streaming error paths) — G-1 PostHook chain runs on idle-timeout 504 and `Result()`-error 500
- `internal/plugin/*` (LoggingHook, ChatTraceHook) — G-1 `startTimes` `sync.Map` entries no longer leak under retry storms

### Tray (Plan 16-04 — T-4, T-5, T-6, T-7)
- `cmd/otto-tray/uihelpers_windows.go` — T-4 non-blocking notify (off the uiLoop)
- `cmd/otto-tray/tray.go` — T-5 status probe consumes `/health` `pool.status` enum
- `scripts/otto-gw.ps1` — T-6 bundle-path parses last non-empty line; T-7 size/time bounds, progress to stderr, staging cleanup on timeout
- `cmd/otto-tray/regression_rel_tray_{04,05,06,07}_test.go` — unskip in same commit

### Config (Plan 16-05 — C-1, C-2, C-3)
- `internal/config/config.go` — C-1 fail-fast on negative/zero `POOL_SIZE`, `SESSION_MAX`, `SESSION_TTL_MS`, `SESSION_TICK_INTERVAL_MS`, `CHAT_TRACE_MAX_AGE_DAYS`; sanity upper bound on `POOL_SIZE`; C-2 `PING_INTERVAL <= 0` validation in `config.Load`; C-3 stub + boot WARN
- `CLAUDE.md` — C-3 doc-fix: backward-compat list marks `EMBEDDING_MODEL_DEFAULT` as `(reserved, not yet implemented)`
- `cmd/otto-gateway/main.go` — C-3 WARN emit during boot if `EMBEDDING_MODEL_DEFAULT` is set

### Test scaffolds (unskip + green-up)
- All 14 regression test files from Phase 14 must have their `t.Skip(...)` removed in the same commit as the production fix.
</code_context>

<decisions>
## Implementation Decisions

### Plan structure — 5 plans by subsystem, parallel waves

- **D-01:** Phase 16 splits into **5 parallel plans, one per subsystem cluster**. Mirrors Phase 14's shape (which also had Config + Tray + Pool + HTTP) and Phase 15's plan-per-subsystem D-01.
  - **Plan 16-01: Pool / ACP** — P-4 (REL-POOL-04), P-5 (REL-POOL-05), P-6 (REL-POOL-06), **O-1 (REL-CFG-04)**. O-1 folds into Pool because it IS the pool exhaustion WARN — production source is `internal/pool/pool.go`, same plan owner.
  - **Plan 16-02: HTTP surface** — H-4 (REL-HTTP-04), H-5 (REL-HTTP-05).
  - **Plan 16-03: Hooks** — G-1 (REL-HOOKS-01). Smallest plan; kept standalone because the failure surface (non-streaming error paths in adapters) is distinct from HTTP body/tailer concerns.
  - **Plan 16-04: Tray / wrapper** — T-4 (REL-TRAY-04), T-5 (REL-TRAY-05), T-6 (REL-TRAY-06), T-7 (REL-TRAY-07).
  - **Plan 16-05: Config** — C-1 (REL-CFG-01), C-2 (REL-CFG-02), C-3 (REL-CFG-03). All three are `internal/config/config.go` + boot-path WARN.
- **D-01a:** Cross-plan dependency: **Plan 16-04 (T-5) consumes the `pool.status` enum added by Plan 16-02 to `internal/server/health.go`** (per D-05). Planner sets `depends_on: ["16-02"]` on Plan 16-04, OR moves the `health.go` extension into Plan 16-04 so Plan 16-02 doesn't touch it. Planner's call at plan-time — flag any `files_modified` overlap on `health.go`.
- **D-01b:** Cross-plan boundary: Plan 16-01 (P-4 readLoop) and Plan 16-03 (G-1 PostHook) both interact with the per-request lifecycle in `internal/adapter/*` and `internal/pool/pool.go`. **Planner confirms `files_modified` is disjoint** at plan-time; if `pool.go` shows up in both, fold G-1 into Plan 16-01 or serialize. Likely disjoint — G-1's changes are in adapter handlers, not pool.
- **D-02:** Each finding's task within its plan follows the **unskip-in-same-commit** pattern (Phase 14 D-12/D-13, Phase 15 D-03): production source edit + `t.Skip()` removal + (if needed) reproducer fixture in one atomic commit per finding. `go test -race ./...` must be green tree-wide after Plan 16-01 (REL-POOL-05's race fix is success criterion #1).

### C-3 EMBEDDING_MODEL_DEFAULT — stub + WARN + doc-fix

- **D-03 (C-3 scope):** **Stub + boot WARN + doc-fix.** No new code paths for embeddings — the surface stays unimplemented in v1.9. Three concrete changes:
  1. On boot, if `EMBEDDING_MODEL_DEFAULT` is set (non-empty), emit `Warn("embedding surface is not implemented; EMBEDDING_MODEL_DEFAULT will be ignored", "value", v)` once. Same logger / level posture as existing boot warnings.
  2. Update `CLAUDE.md` "Backward compat" list (line 42): change `EMBEDDING_MODEL_DEFAULT` to `EMBEDDING_MODEL_DEFAULT (reserved, not yet implemented)`. Single line edit; lands in Plan 16-05's last commit.
  3. **Out of scope:** wiring `/v1/embeddings`, `/api/embed`, `/api/embeddings` to anything. If those routes already return 404 (default Chi behaviour), leave them. If they're registered as 501 elsewhere, leave them. Do not add new handlers in this phase.
- **D-03 rationale:** `docs/briefs/go_port_brief.md` §3.4 explicitly defers the embeddings problem (Options A=cgo onnx, B=pure-Go quality drop, C=sidecar, D=WASM — all unsuitable for the "single static binary" constraint at v1.9). Reliability hardening is the wrong milestone to take on a new surface. Re-evaluate in v1.10+ via the existing sidecar/WASM tradeoff discussion.

### H-4 body-read deadline — new env var, chat-body POSTs only

- **D-04 (H-4 knob):** **New env var `HTTP_BODY_READ_TIMEOUT_SEC`, default 30 seconds.**
  - **Naming convention:** `_SEC` suffix matches existing `STREAM_IDLE_TIMEOUT_SEC` posture. **Diverges from Phase 15's `POOL_ACQUIRE_TIMEOUT_MS`** — accepted, because the existing config-loader idiom for timeouts is `_SEC` (see `internal/config/config_test.go:618+` and `internal/admin/admin.go:103` `StreamIdleTimeoutSec`). Phase 15's `_MS` was the outlier; not propagating.
  - **Default value rationale:** 30s is long enough to absorb genuine slow networks (HTTP body reads on consumer broadband under typical chat payload sizes); short enough that a stalled / abandoned upload doesn't park the handler goroutine for hours via TCP keepalive. Matches Phase 15's `POOL_ACQUIRE_TIMEOUT_MS=30000` choice for the same kind of "long enough but not unbounded" tradeoff.
  - **Validation:** `<= 0` is a boot error (matches C-1 / C-2 fail-fast posture). Sanity ceiling: any positive value accepted (no upper cap — operator's tradeoff).
- **D-04a (H-4 scope):** **Apply to chat-body POST handlers only**, NOT to admin / system POSTs. Concretely: `/v1/chat/completions`, `/v1/messages`, `/v1/completions`, `/api/chat`, `/api/generate`, `/api/embed`, `/api/embeddings`, `/v1/embeddings` (even if embeddings are stubbed — the body read is still real). Admin POSTs (`/admin/config/...`) keep current behavior; their bodies are small and the stall failure mode does not appear in the review.
- **D-04b (H-4 implementation pattern):** Wrap `r.Body` with a deadline-enforcing `io.ReadCloser` that fires ONLY on the read phase — close the body / cancel the request context on deadline, but do NOT set `http.Server.ReadTimeout` or `WriteTimeout` (would break SSE response writes, success criterion #4). Pattern: a goroutine + `time.AfterFunc` that calls `r.Body.Close()` after the deadline; planner can also consider `http.MaxBytesReader` chained with a deadline reader. Detail decision is planner-discretion; the contract is "bounded body read phase, unbounded SSE response write".

### T-5 /health pool.status enum — server owns the policy

- **D-05 (T-5 shape):** **Extend the existing `GET /health` `PoolStats` struct with a new `Status string` field**, instead of adding a new `/health/pool` endpoint. Enum: `ok` | `degraded` | `exhausted` | `unknown`. JSON-backwards-compatible (new field; existing consumers ignore unknown fields).
  - `internal/server/health.go` `PoolStats` gets `Status string \`json:"status"\``.
  - Tray probe (`cmd/otto-tray/tray.go`) consumes the enum directly; if it cannot reach `/health` or JSON decode fails, treats as `unknown` → degraded indicator. No tray-side policy logic.
- **D-05a (degraded rule):** **`pool.status == "degraded"` when `Busy == Alive == Size && (now - max(last_progress_at across slots) > 30s)`.**
  - Server tracks per-slot `last_progress_at` (`atomic.Int64` UnixNano). Advanced on: every streamed chunk emit, every ping ack, every slot release. **Not** advanced on slot acquire (acquire alone doesn't prove forward progress).
  - 30s threshold: most LLM chunk intervals land within 30s even on slow workers; flips to `degraded` only when the pool genuinely stalls. Hardcoded constant in `health.go`, NOT a new env var (D-04 rationale on env-surface restraint applies).
- **D-05b (other statuses):**
  - `exhausted`: P-1's `pool.ErrPoolExhausted` state — Phase 15 already exposes the typed error; map to `exhausted` when no slot is available AND the bounded-acquire timeout would fire. Single boolean check on `pool.IsExhausted()` helper.
  - `unknown`: failed health snapshot — tray-side only (D-05 above). Server always returns one of `ok | degraded | exhausted`.
  - `ok`: default — anything not degraded or exhausted.
- **D-05c:** **Plan dependency:** Plan 16-02 (HTTP) owns `internal/server/health.go` (touches it for nothing else); Plan 16-04 (Tray) consumes the new field. Planner sets `depends_on: ["16-02"]` on Plan 16-04 OR moves the health.go extension into Plan 16-04. Recommendation: keep it in Plan 16-02 because the data source (per-slot `last_progress_at`) lives in `internal/pool/pool.go` which Plan 16-01 owns — but P-4's `last_progress_at` plumbing IS the read-side input for D-05a. So actually: **Plan 16-01 owns the `pool.LastProgressAt()` API**, **Plan 16-02 owns the `health.go` Status computation**, **Plan 16-04 owns the tray consumer**. Three plans chain. Planner sets `depends_on: ["16-01", "16-02"]` on Plan 16-04. Plans 16-03 and 16-05 stay independent of this chain.

### Claude's Discretion (no further input needed)

- **Plan-internal task order:** each plan starts with its lowest-numbered REL-ID (P-4 first in 16-01, H-4 first in 16-02, etc.), then proceeds in REL-ID order. Each task = one finding's atomic commit (source + `t.Skip` removal + reproducer green).
- **Wave layout:** Plans 16-01, 16-02, 16-03, 16-05 in Wave 1 (independent). Plan 16-04 in Wave 2 (`depends_on: ["16-01", "16-02"]`). Standard `gsd-executor` worktree-isolated flow.
- **C-1 batched errors:** Loud boot errors for negative/zero `POOL_SIZE` / `SESSION_MAX` / `SESSION_TTL_MS` / `SESSION_TICK_INTERVAL_MS` / `CHAT_TRACE_MAX_AGE_DAYS` are emitted **one at a time** matching the existing `STREAM_IDLE_TIMEOUT_SEC` posture (first invalid var → error → exit). No batched-errors envelope; the existing pattern is fine. `POOL_SIZE` upper bound: **256** (sanity ceiling — system with > 256 concurrent kiro-cli workers is unsupported; review section §3.4).
- **C-2 PING_INTERVAL:** validate in `config.Load` BEFORE any goroutine starts. The defensive panic from `time.NewTicker` keeps its `defer recover()` posture so any future regression lands in slog rather than crashing silently.
- **T-4 non-blocking notify (Windows):** spawn each `notify()` call in a fire-and-forget goroutine with a bounded retry (max 3 attempts, 500ms backoff). If the notify channel saturates, drop with a Debug log line — silent overflow is fine for a non-critical UX signal.
- **T-7 bundle bounds:** `--max-mb` flag defaults to **512MB**; verb timeout defaults to **180s**. Both overridable on the CLI invocation (`otto-gw support --max-mb 1024 --timeout 300`). Cleanup-on-timeout via `defer os.RemoveAll(stagingDir)` in the PowerShell wrapper; bash wrapper already has the equivalent trap.
- **O-1 WARN throttling:** emit `Warn("pool: waiting for free slot", ...)` on the FIRST acquire that parks per pool generation (reset on `Close()` / restart). Subsequent parks during the same saturation episode emit Debug only — avoids log spam during sustained load.
- **No new dependencies:** all 14 fixes are achievable with the stdlib + existing project deps. No new `go.mod` entries expected.
- **CLAUDE.md updates:** add `HTTP_BODY_READ_TIMEOUT_SEC` to the env-var list under "Backward compat" with a `(net-new in v1.9)` note. Same single-commit close pattern as Phase 15 D-04.
- **Cross-platform parity for T-4:** even though T-4 is Windows-specific in the review, the non-blocking-notify pattern (off-uiLoop dispatch) lands on `uihelpers_darwin.go` too for symmetry. Cost is one line; benefit is the test contract is uniform.
</decisions>

<deferred>
## Noted for Later

- **Real embeddings surface** — `/v1/embeddings`, `/api/embed`, `/api/embeddings` wired to a real embeddings implementation. v1.10+ scope. Decision tree in `docs/briefs/go_port_brief.md` §3.4 (Options A/B/C/D); preferred path is Option C (sidecar) per current brief.
- **Batched config errors envelope** — collecting ALL invalid env vars at boot instead of fail-on-first. Nice for new-deployment debugging but adds error-aggregation surface area. Defer until operators ask for it.
- **`POOL_DEGRADED_STALL_SEC` env var** — making the 30s degraded threshold tunable. If operators report false-positive degraded states on slow-network deployments, add this knob. Not adding speculatively.
- **`/health/pool` rich endpoint** — per-slot details (pid, busy, last_used, last_progress_at). Operator-debugging value; tray doesn't need it. v1.10+ ops-tooling scope.
- **Universal HTTP body-read deadline** including admin POSTs — wait for a real admin-POST stall to motivate it.
- **Lows from the v1.9 reliability review** — 12 Lows deferred per ROADMAP v1.9 description; queued for v1.10.
</deferred>
