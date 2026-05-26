---
phase: 05-pool-stateful-sessions
plan: 04
subsystem: session
tags: [acp, kiro-cli, json-rpc, wire-protocol, sc3, sc4, gap-closure, cwd-handshake]

# Dependency graph
requires:
  - phase: 05-01
    provides: pool implementation (Pool.NewSession routes through engine.pickCwd → os.Getwd fallback — the comparator path)
  - phase: 05-02
    provides: session.Registry with lazy-create, reaper, Delete + Entry surface
  - phase: 05-03
    provides: X-Session-Id surface routing in adapters; /health/agents; /v1/sessions/:id DELETE; SessionsRouter
provides:
  - Resolved Phase 5 SC3 BLOCKER gap: kiro-cli's session/prompt rejects sessions whose session/new was called with empty cwd → fix substitutes os.Getwd() in registry.createEntry.
  - Resolved Phase 5 SC4 dependency gap: reap + DELETE happy path now demonstrable end-to-end.
  - tools/kiro-shim — reusable stdio JSON-RPC recorder for future wire-protocol investigations.
  - StatefulContinuity_TwoTurns e2e subtest — the load-bearing SC3 conversation-level closure assertion.
  - Strengthened DeleteSession_CancelsInFlight that asserts pre-DELETE chunk parses as valid Ollama assistant content (no longer passes for the wrong reason).
  - internal/session/entry_acp_test.go — H-B regression guard + H-A reverse-regression guard.
affects: [05-05 (perf), future stateful-session features, kiro-cli wire-protocol evolution]

# Tech tracking
tech-stack:
  added:
    - tools/kiro-shim diagnostic binary (Go stdlib only — bufio, encoding/json, os/exec, sync)
  patterns:
    - "Wire-protocol diagnostic via tee-shim — captures every JSON-RPC frame to per-PID JSONL files without modifying production code."
    - "Confirmatory experiment in WIRE-DIFF.md: a single-variable change that flips failure → success is the evidence for a confirmed root cause; no 'most likely' framing without before/after observations."
    - "Local-domain fix for cross-package consistency: when arch-lint forbids importing a helper (engine.pickCwd) from the registry package, inline the smallest piece of behaviour needed (os.Getwd fallback) at the divergence site rather than inverting the dependency graph."
    - "H-A reverse-regression guard: pin behaviour that a naive 'fix' would tempt someone to change. The cached-SessionID accessor in Entry.NewSession is load-bearing for conversation continuity; recreating sessions per prompt would silently break the very property SC3 demands."

key-files:
  created:
    - tools/kiro-shim/main.go
    - .planning/phases/05-pool-stateful-sessions/05-04-WIRE-DIFF.md
    - .planning/phases/05-pool-stateful-sessions/05-04-WIRE-POOL.jsonl
    - .planning/phases/05-pool-stateful-sessions/05-04-WIRE-SESSION.jsonl
    - .planning/phases/05-pool-stateful-sessions/05-04-traces/{baseline,confirm}/* (per-PID transcripts)
    - internal/session/entry_acp_test.go
  modified:
    - internal/session/registry.go (createEntry — os.Getwd fallback when caller supplies empty cwd; H-B fix site)
    - internal/session/entry_acp.go (NewSession — added H-A reverse-regression note)
    - tests/e2e/pool_sessions_e2e_test.go (strengthened DeleteSession_CancelsInFlight + new StatefulContinuity_TwoTurns)
    - .planning/phases/05-pool-stateful-sessions/05-VERIFICATION.md (gaps:[] + Gap Closure History + Re-verification sections)

key-decisions:
  - "Root cause is H-B (cwd handshake), not H-A (cached-sid reuse). Confirmatory experiment: single-variable KIRO_CWD=/tmp flipped HTTP 500 → 200 against the same kiro-cli binary; two-turn continuity also worked. H-A explicitly rejected — cached SessionID supports multi-turn continuity."
  - "Fix is local to registry.createEntry, not Entry.NewSession. The session→engine arch-lint boundary forbids importing engine.pickCwd; inlining a 4-line os.Getwd fallback at the registry's session/new call site is the smallest correct change."
  - "Plan 05-04 does NOT flip the global phase status — that flip belongs to plan 05-05 Task 4 after the manual perf+RSS gate (PHASE5-PERF.md) closes."
  - "kiro-shim diagnostic binary is reusable. Lives under tools/ (outside the arch-lint workdir 'internal') and imports nothing from internal/* — its only dependencies are Go stdlib so the trivial-cross-compile constraint stays satisfied."

patterns-established:
  - "Pattern 1: Wire-protocol diagnostic shim. When unit tests pass via fake-ACP fakes but live integration fails, build a transparent stdio JSON-RPC recorder, capture both the working comparator path and the broken path with the same kiro-cli binary in the same boot, and diff the JSON-RPC method+params to find the divergence. Reusable for any future kiro-cli wire investigation."
  - "Pattern 2: Confirmatory experiment is mandatory before naming a root cause. 'It might be H-B' is not a root cause; 'I changed cwd from empty to /tmp and -32603 went away while everything else stayed' IS. Plan 05-04 enforces this via a `## Confirmatory Experiment` section in WIRE-DIFF.md."
  - "Pattern 3: Reverse-regression tests for fixes that would tempt the wrong over-correction. TestEntry_NewSession_ReturnsCachedSessionID guards against a future refactor 'fixing' the cached-sid accessor that was correctly diagnosed as NOT the bug."

requirements-completed: [SESS-01, SESS-02, SESS-03]

# Metrics
duration: 16min
completed: 2026-05-26
---

# Phase 5 Plan 04: SC3 Root Cause + Gap Closure Summary

**SC3 (stateful X-Session-Id integration) closed by patching registry.createEntry to resolve empty cwd via os.Getwd(); two-turn continuity proven end-to-end against kiro-cli 2.4.1; SC4 lifts automatically once SC3 is fixed**

## Performance

- **Duration:** ~16 min (wall-clock from first capture to final commit; includes 71s e2e suite run)
- **Started:** 2026-05-26T15:47:34Z (first wire transcript)
- **Completed:** 2026-05-26T16:03:57Z
- **Tasks:** 7 of 7 executed (Tasks 1, 2, 3, 4, 5, 5.5, 6)
- **Files modified:** 4 source files + 1 verification doc; 6 new artifacts (1 binary source, 1 diff doc, 2 merged JSONL, 14 per-PID JSONL traces)

## Accomplishments

- **Root-caused SC3 at the wire-protocol level**: kiro-cli's `session/prompt` returns rpc error -32603 "Improperly formed request" against every prompt issued on a session whose `session/new` was called with an empty `cwd`. The pool path was unaffected because `engine.Run` → `engine.pickCwd` → `os.Getwd()` fallback gave it a non-empty cwd; the registry path was forwarding `cfg.KiroCWD` ("" by default) verbatim.
- **Fix is 4 lines**: in `internal/session/registry.go::createEntry`, fall back to `os.Getwd()` when the caller-supplied cwd is empty. Mirrors engine.pickCwd's final fallback without inverting the arch-lint-forbidden session→engine dependency.
- **Two-turn conversation continuity proven**: turn 1 = "Remember the number 7." → turn 2 = "What number did I tell you to remember?" → "7." on the same kiro-cli session id. This rules out H-A (cached-sid reuse) as the cause — the cached SessionID strategy is correct.
- **Strengthened DeleteSession_CancelsInFlight**: no longer passes for the wrong reason. Parses the first pre-DELETE NDJSON chunk and demands it be valid Ollama assistant content (non-empty `message.content`, no top-level `error` key, not protocol-metadata-only).
- **Reusable diagnostic tool**: `tools/kiro-shim` — stdio JSON-RPC recorder. Surface-area: ~150 LOC of Go stdlib. Doesn't import production internals; binds to `KIRO_CMD=/tmp/kiro-shim KIRO_ARGS="$(which kiro-cli) acp"`. Trace files use the `{ts,pid,child_pid,dir,frame}` shape so merging across multiple PIDs is unambiguous.

## Task Commits

Each commit groups related task work atomically:

1. **Tasks 1+2+3 (Capture pool transcript + capture session transcript + diff + root cause)** — `36a7aac` (docs)
   - tools/kiro-shim/main.go + 14 raw trace files
   - 05-04-WIRE-POOL.jsonl + 05-04-WIRE-SESSION.jsonl (merged transcripts)
   - 05-04-WIRE-DIFF.md with all 6 required sections (Working Pool Path, Broken Session Path, Rejected Hypotheses, Confirmatory Experiment, Root Cause, Remediation Plan)

2. **Task 4 (Encode the fix + TDD guards)** — `9384851` (fix)
   - internal/session/registry.go::createEntry — os.Getwd fallback when cwd=="".
   - internal/session/entry_acp.go::NewSession — H-A reverse-regression comment.
   - internal/session/entry_acp_test.go — 4 new tests (TDD RED→GREEN cycle observed: H-B regression guard FAILED against pre-fix code, then PASSED against fix).
   - Source comments cite `05-04-WIRE-DIFF.md` so future readers can re-discover the root cause without re-running the shim.

3. **Tasks 5 + 5.5 + 6 (Test deltas + final integration sweep + VERIFICATION.md update)** — `ae19ed0` (test)
   - tests/e2e/pool_sessions_e2e_test.go — StatefulContinuity_TwoTurns subtest + strengthened DeleteSession_CancelsInFlight.
   - .planning/phases/05-pool-stateful-sessions/05-VERIFICATION.md — gaps:[] + ## Gap Closure History + ## Re-verification 2026-05-26.

**Plan metadata commit** to follow this SUMMARY.md.

## Files Created/Modified

- `tools/kiro-shim/main.go` — stdio JSON-RPC recorder. Argument contract: argv[1]=real kiro-cli path, argv[2:]=passthrough args. Frame format: `{ts,pid,child_pid,dir,frame}` JSONL.
- `.planning/phases/05-pool-stateful-sessions/05-04-WIRE-DIFF.md` — root-cause artifact: 6 required sections with cited frame line numbers.
- `.planning/phases/05-pool-stateful-sessions/05-04-WIRE-POOL.jsonl` — 23 frames covering warmup catalog + stateless /api/chat that returned HTTP 200.
- `.planning/phases/05-pool-stateful-sessions/05-04-WIRE-SESSION.jsonl` — 11 frames covering the broken stateful /api/chat that returned HTTP 500 with -32603.
- `.planning/phases/05-pool-stateful-sessions/05-04-traces/{baseline,confirm}/*.jsonl` — raw per-PID transcripts (renamed from .out/.in to dodge .gitignore's *.out rule).
- `internal/session/registry.go` — 4-line fix in createEntry + ~14 lines of explanatory comment citing 05-04-WIRE-DIFF.md.
- `internal/session/entry_acp.go` — added H-A reverse-regression note to Entry.NewSession (no behaviour change).
- `internal/session/entry_acp_test.go` — 4 new tests + recordingClient/recordingFactory fakes.
- `tests/e2e/pool_sessions_e2e_test.go` — strengthened DeleteSession_CancelsInFlight + new StatefulContinuity_TwoTurns; added `bufio` import.
- `.planning/phases/05-pool-stateful-sessions/05-VERIFICATION.md` — frontmatter `gaps:[]` (SC3+SC4 removed); appended `## Gap Closure History` + `## Re-verification 2026-05-26` sections.

## Root Cause

**One paragraph (cites 05-04-WIRE-DIFF.md ## Confirmatory Experiment):**

The session path passed `cwd:""` to kiro-cli's `session/new` because `internal/adapter/ollama/handlers.go:117` forwards `cfg.KiroCWD` verbatim to `Registry.Get`, and `KIRO_CWD` defaults to "". `internal/session/registry.go:252` then forwarded that empty string to `client.NewSession(ctx, cwd)`. kiro-cli accepted the `session/new` and returned a session id (visible in `/health/agents`), but every subsequent `session/prompt` against that empty-cwd session id was rejected with rpc error -32603 / `error.data="Improperly formed request."`. The pool path was unaffected because `engine.Run` (engine.go:165) resolves cwd via `engine.pickCwd` which falls back to `os.Getwd()` when `DefaultCWD` is empty. The confirmatory experiment in `05-04-WIRE-DIFF.md` proves this: setting `KIRO_CWD=/tmp` (single-variable change) flipped the stateful path from HTTP 500 → HTTP 200 against the same kiro-cli binary; the only diff in the captured shim transcript was the `cwd` field on `session/new`. Fix: substitute `os.Getwd()` for empty `cwd` in `registry.createEntry` before calling `client.NewSession`.

## Test Deltas

**New unit tests** (`internal/session/entry_acp_test.go`):
- `TestRegistry_CreateEntry_ResolvesEmptyCwdToOSGetwd` — the H-B regression guard. The TDD RED step verified this test FAILED against pre-fix code (`createEntry forwarded empty cwd to client.NewSession`); the GREEN step confirmed PASS after the os.Getwd fallback landed.
- `TestRegistry_CreateEntry_PassesNonEmptyCwdVerbatim` — symmetric guard: non-empty cwd is forwarded unchanged (no normalisation).
- `TestEntry_NewSession_ReturnsCachedSessionID` — H-A REVERSE-regression guard: Entry.NewSession stays a pure accessor; calling it twice on the same Entry issues ZERO additional Client.NewSession RPCs.
- `TestEntry_Prompt_PassesCachedSessionID` — companion: confirms Entry.Prompt forwards the cached SessionID to Client.Prompt verbatim.

**New e2e subtest** (`tests/e2e/pool_sessions_e2e_test.go::StatefulContinuity_TwoTurns`):
- The load-bearing SC3 closure assertion (plan 05-04 HIGH-1). Same-PID affinity is NOT sufficient. Sends two sequential `/api/chat` requests on `X-Session-Id: continuity-1`; asserts turn 2's response contains the digit "7" (case-insensitive substring). Live result against kiro-cli 2.4.1: turn 1 = "Noted. The number is 7." → turn 2 = "7."

**Strengthened e2e subtest** (`tests/e2e/pool_sessions_e2e_test.go::DeleteSession_CancelsInFlight`):
- Plan 05-04 MEDIUM-4 fix. The previous assertion ("stream terminates within 5s") passed against the SC3 bug for the WRONG REASON (stream terminated immediately because Entry.Prompt returned 500). The strengthened test (a) uses `bufio.Scanner` to count NDJSON chunks and capture the first complete line, (b) waits up to 3s for chunkCount ≥ 1 (no more fixed 250ms sleep), (c) parses the first chunk and asserts non-empty `message.content` OR `response` AND no top-level `error` key. Live result: chunkCount=4, first chunk content="Hi".

## Before / After Curl Evidence

**Before (KIRO_CWD unset):**
```
$ curl -sS -X POST -H 'X-Session-Id: smoke-broken' http://127.0.0.1:11434/api/chat -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'
{"error":"ollama engine collect: engine: collect: engine: prompt: session: prompt: acp: prompt: rpc error -32603: Internal error"}
HTTP 500
```

**After (fix landed, no KIRO_CWD required):**
```
$ curl -sS -X POST -H 'X-Session-Id: smoke-1' http://127.0.0.1:11434/api/chat -d '...'
{"model":"auto","message":{"role":"assistant","content":"Hey! I'm Kiro. ..."},"done":true,...}
HTTP 200

$ curl -sS -X POST -H 'X-Session-Id: smoke-1' http://127.0.0.1:11434/api/chat -d '...hello again...'
{"model":"auto","message":{"role":"assistant","content":"Hey! What can I help you with?"},"done":true,...}
HTTP 200

$ curl -sS -X DELETE http://127.0.0.1:11434/v1/sessions/smoke-1
{"deleted":"smoke-1"}
HTTP 200
```

## Re-verification

- `go test ./... -count=1 -race -timeout=180s` — all 12 packages PASS.
- `OTTO_E2E=1 go test -tags=e2e -run 'TestE2E_PoolSessions' ./tests/e2e/... -count=1 -timeout=180s` — **10/10 subtests PASS** in 71.32s (1 SKIP by design: AllDeadRespawnFails):
  - SessionIDAffinity PASS (previously: FAIL at line 218 with HTTP 500)
  - **StatefulContinuity_TwoTurns PASS** (NEW — turn 2 content "7.")
  - IdleReap_RealTime PASS (previously blocked behind SC3)
  - DeleteSession_OK PASS (previously blocked behind SC3)
  - DeleteSession_CancelsInFlight PASS with strengthened parse-and-validate assertion
  - DeleteSession_Unknown / HealthAgentsShape / WarmupBeforeListen / SaturationBlocking / DeadSlotLazyRespawn — all still PASS, no regression.
- `go vet ./internal/session/...` — clean (golangci-lint not installed; plan 05-04 LOW-1 conditional satisfied).
- Smoke curl × 4 (stateful turn1, stateful turn2 same sid, stateless, DELETE) — all expected statuses; `/health/agents` shows `sessions:[]` post-DELETE; no orphan kiro-cli children after gateway shutdown.

## Requirements Lifted

- **SESS-01**: PARTIAL → SATISFIED. Stateful X-Session-Id requests now route to a dedicated kiro-cli subprocess that produces real model output, end-to-end.
- **SESS-02**: PARTIAL → SATISFIED. Idle reaping (TTL=500ms in the e2e subtest) works end-to-end now that session creation actually succeeds.
- **SESS-03**: PARTIAL → SATISFIED. DELETE happy path returns 200 `{"deleted":"<id>"}` end-to-end; cancel-in-flight passes with strengthened assertion.

**Phase 5 global `status:` flip remains gated on Plan 05-05 Task 4 (manual perf+RSS gate).**

## Decisions Made

- **Reject H-A despite plan's plausibility ranking.** The plan listed H-A first as "most plausible given current code"; the wire diff plus the confirmatory experiment (single-variable KIRO_CWD=/tmp change) ruled it out. Decision: trust the evidence over the prior. Wrote a REVERSE-regression test (`TestEntry_NewSession_ReturnsCachedSessionID`) so a future executor cannot silently "fix" the cached-sid accessor that was correctly diagnosed as not-the-bug.
- **Local-domain fix in registry.createEntry, NOT a cross-package refactor.** The session→engine arch-lint boundary forbids the registry from importing `engine.pickCwd`. Inlining a 4-line `os.Getwd()` fallback at the divergence site is smaller, more reviewable, and less risky than inverting the dependency.
- **Keep `tools/kiro-shim` reusable.** Lived under `tools/` (outside arch-lint workdir) and imports nothing from `internal/*`. Surface area is ~150 LOC of Go stdlib. Future kiro-cli wire-protocol investigations can reuse it without dragging production deps into a diagnostic binary.
- **Stash raw per-PID trace files under `.planning/phases/05-pool-stateful-sessions/05-04-traces/`**, renamed `.in`/`.out` → `.kiro-to-gateway.jsonl`/`.gateway-to-kiro.jsonl` because the project's `.gitignore` excludes `*.out`. Preserves the full forensic record alongside the merged transcripts.

## Deviations from Plan

**1. [Rule 3 - Blocking] macOS TMPDIR placement of trace files**
- **Found during:** Task 1 (first shim execution)
- **Issue:** Plan assumed `/tmp/kiro-wire-*.{in,out}` would appear directly under `/tmp`; on macOS `os.TempDir()` returns the per-process `$TMPDIR` (e.g., `/var/folders/qy/.../T/`). Required updating the trace-discovery commands and merged-transcript builder.
- **Fix:** No source change required — the shim's `os.TempDir()` call is correct; the discovery commands and SUMMARY documentation were updated to reference the actual TMPDIR resolved at runtime.
- **Files modified:** none in source; only diagnostic shell paths.
- **Verification:** Wire files appeared under the resolved $TMPDIR and were merged into the canonical `.planning/...05-04-WIRE-*.jsonl` artifacts.

**2. [Rule 3 - Blocking] *.out gitignore rule excluded the per-PID raw transcripts**
- **Found during:** Task 1 commit staging
- **Issue:** The project's `.gitignore` has `*.out` (intended for test output). The shim's `.out` files (gateway → kiro direction) were silently excluded from `git add`.
- **Fix:** Renamed the raw transcript files: `*.out` → `*.gateway-to-kiro.jsonl`; `*.in` → `*.kiro-to-gateway.jsonl`. More descriptive AND not gitignored.
- **Files modified:** trace files only (renamed before commit).
- **Verification:** `git status` showed all 14 raw transcript files staged; the merged `.jsonl` artifacts are the primary reference, the per-PID files are forensic backup.

**Total deviations:** 2 auto-fixed (both Rule 3 — blocking discovery issues that did not affect plan semantics).
**Impact on plan:** None on plan goals; the discovery details surfaced earlier than expected. No scope creep; no architectural changes.

## Issues Encountered

- **None unexpected.** The Bash `cd` reset between tool calls (worktree-path-safety #3097) was anticipated; using `git rev-parse --show-toplevel` per command kept paths correct. Tests passed cleanly under `-race`. No flakes observed in the e2e suite across both diagnostic runs (broken-path capture, fixed-path full e2e).

## Known Stubs

None. The fix and its tests are complete and grounded in live integration evidence.

## User Setup Required

None — no external service configuration. The fix is entirely server-side and uses an existing fallback path (`os.Getwd()`) that all production deployments already have.

## Next Plan Readiness

- **Plan 05-05 (perf report) is unblocked.** The remaining Phase 5 closure item is the manual perf-vs-Node + RSS gate (`tests/e2e/reports/PHASE5-PERF.md`). With SC3 fixed, the gateway is stable enough to run the wrk/ab benchmark side-by-side with the Node reference.
- **Phase 5 global `status:` flip is plan 05-05's responsibility.** Plan 05-04 deliberately left `status: gaps_found` and `score: 3/5` untouched in `05-VERIFICATION.md` frontmatter (plan 05-04 LOW-2). After the manual perf+RSS gate closes, plan 05-05 Task 4 should flip the status to `verified` and update the score accordingly.
- **No new blockers introduced.** Unit suite (all 12 packages, -race), e2e suite (10/10 subtests pass), `go vet` clean. Build is reproducible from the worktree.

## Self-Check: PASSED

All claimed files exist on disk (10/10), all claimed commits exist in git history (3/3).

---
*Phase: 05-pool-stateful-sessions*
*Plan: 04*
*Completed: 2026-05-26*
