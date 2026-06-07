---
phase: 10-golangci-lint-v2-cleanup-re-gate
plan: 02
subsystem: tooling
tags: [golangci-lint, wrapcheck, unparam, lint-cleanup, error-wrap]

# Dependency graph
requires:
  - phase: 10 (Wave 1)
    provides: post-Wave-1 baseline (33 violations: 9 wrapcheck + 13 unparam + 11 other)
provides:
  - 22 of remaining 33 baseline lint violations drained (Wave 2 wrapcheck + unparam tier)
  - LINT-03 per-category decision records for wrapcheck and unparam
  - Production-side dead-param drop on (*sseEmitter).aggregatedResponse (anthropic surface) preserving prior behavior
affects: [Phase 10 Wave 3, Phase 10 Wave 4 (re-gate)]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "wrapcheck wrap pattern at canonical-package boundaries: fmt.Errorf(\"<surface.subsystem>: <op>: %w\", err) — preserves errors.Is/errors.As traceability"
    - "unparam decision split: production-code dead params dropped (Task 2 + sse.go aggregator); test-helper symmetry preserved via scoped //nolint:unparam // <rationale> (Task 3)"
    - "Worktree-path safety: when an Edit-call's absolute path was constructed from a stale main-repo pwd, edits silently land in the main repo. Recovery: git diff out the misdirected edits, git checkout -- to revert main, then git apply the patch inside the worktree (matches gsd worktree-path-safety reference #3099)."

key-files:
  created:
    - .planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-02-SUMMARY.md
  modified:
    - internal/acp/pool_pgid_unix.go
    - internal/adapter/anthropic/handlers.go
    - internal/adapter/anthropic/handlers_session_test.go
    - internal/adapter/anthropic/sse.go
    - internal/adapter/anthropic/sse_posthook_test.go
    - internal/adapter/ollama/chat_trace_e2e_test.go
    - internal/adapter/ollama/handlers.go
    - internal/adapter/ollama/handlers_test.go
    - internal/adapter/ollama/ndjson_posthook_test.go
    - internal/adapter/openai/handlers.go
    - internal/adapter/openai/handlers_session_test.go
    - internal/adapter/openai/sse_posthook_test.go
    - internal/admin/sse.go
    - internal/admin/tail.go
    - internal/engine/engine.go
    - internal/plugin/pii/contextual.go
    - internal/plugin/pii/contextual_test.go
    - internal/plugin/pii/pii.go
    - internal/plugin/pii/pii_test.go
    - internal/plugin/pii/summary.go
    - internal/pool/pool_test.go
    - internal/session/reaper_test.go

key-decisions:
  - "Wrap every wrapcheck site with fmt.Errorf and %w (not %v) so callers retain errors.Is/errors.As — uniform per-surface context prefix shapes (`<surface.subsystem>: <op>: %w`)"
  - "engine.go PreHook/PostHook wraps use the generic 'engine: prehook: %w' / 'engine: posthook: %w' shape — interfaces carry no Name() method, so per-hook name interpolation is deferred (plan said: 'if no name var exists at line 332, plain plain form')"
  - "Drop window int param from hasContextWithin and inline the existing package-level defaultContextWindow constant — 6 test sites and 1 production site updated"
  - "Drop unused s string value param from (*PIIRedactionHook).acceptNERSpans; receiver h unchanged"
  - "Drop req *canonical.ChatRequest param from (*sseEmitter).aggregatedResponse — every caller passed nil; preserve prior model=\"\" behavior to keep this wave behavior-neutral rather than picking up e.model"
  - "Test-helper unparam findings get //nolint:unparam // <rationale> (helper-pair symmetry, future-coverage axis, result-shape contract) — production-code findings get the parameter dropped"

patterns-established:
  - "wrapcheck context-string discipline: static prefix only, no PII, no path leakage, no operator-provided string interpolation (T-10-03 mitigation)"
  - "nolint:unparam // <rationale> on the function-signature line is the unparam-suppression idiom; bare //nolint:unparam without a comment is a regression — verified empty via `git diff | grep '^+.*nolint:unparam' | grep -v '// '`"

requirements-completed:
  - LINT-01
  - LINT-03

# Metrics
duration: ~14min
completed: 2026-06-07
---

# Phase 10 Plan 02: golangci-lint v2 Wave 2 wrapcheck + unparam drain Summary

**Drained 22 of 33 remaining baseline lint violations (9 wrapcheck + 13 unparam) across 3 atomic commits — every wrap preserves `%w`, every `//nolint:unparam` carries a one-line rationale, every production-side dead parameter dropped.**

## Performance

- **Duration:** ~14 min (includes one absolute-path-recovery cycle: patches misdirected to main repo were captured, reverted, and re-applied inside the worktree)
- **Started:** 2026-06-07T01:28:00Z (approx)
- **Completed:** 2026-06-07T01:42:47Z
- **Tasks:** 3/3 completed
- **Files modified:** 22 (8 in T1, 3 in T2, 11 in T3)

## Accomplishments

- All 9 wrapcheck violations resolved with `fmt.Errorf(..., %w, err)` wraps using uniform per-surface context prefixes. No `//nolint:wrapcheck` exemptions.
- All 13 unparam violations resolved: 3 production-code parameters dropped (`hasContextWithin.window`, `acceptNERSpans.s`, `(*sseEmitter).aggregatedResponse.req`); 10 test-helper findings carry scoped `//nolint:unparam // <rationale>` directives.
- Every `//nolint:unparam` carries a same-line `// <rationale>` segment (verified via `git diff | grep '^+.*nolint:unparam' | grep -v '// '` returning empty).
- `go build ./...` clean; `go vet ./...` clean (no new vet hits introduced).
- `go test -race ./...` green across all 17 test packages.
- Wave-2 baseline drain count: 22 violations removed (lint output `wrapcheck + unparam` count drops 22 → 0).

## Task Commits

Each task was committed atomically on the agent's worktree branch:

1. **Task 1: Wrap 9 wrapcheck errors at package boundaries** — `1a975c0` (fix)
2. **Task 2: Drop unused production params hasContextWithin.window + acceptNERSpans.s** — `2506b64` (fix)
3. **Task 3: Scope unparam exemptions for 10 test helpers + drop sse.go aggregator dead param** — `8b5aa0a` (chore)

## Files Created/Modified

### Created
- `.planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-02-SUMMARY.md` — this file

### Modified (8 wrapcheck targets, T1)
- `internal/acp/pool_pgid_unix.go` — wrap syscall.Kill; add fmt import
- `internal/adapter/anthropic/handlers.go` — wrap SessionRegistry.Get; add fmt import
- `internal/adapter/ollama/handlers.go` — wrap SessionRegistry.Get; add fmt import
- `internal/adapter/openai/handlers.go` — wrap SessionRegistry.Get; add fmt import
- `internal/admin/sse.go` — wrap ctx.Err() at tail-stream loop
- `internal/admin/tail.go` — wrap bufio.ReadString; add fmt import
- `internal/engine/engine.go` — wrap PreHook.Before + PostHook.After (callPreHookSafe / callPostHookSafe)
- `internal/plugin/pii/summary.go` — wrap json.Marshal in MarshalJSON; add fmt import

### Modified (3 production-unparam targets, T2)
- `internal/plugin/pii/contextual.go` — drop window int param; use existing defaultContextWindow const
- `internal/plugin/pii/contextual_test.go` — drop 50 arg from 6 test call sites
- `internal/plugin/pii/pii.go` — drop window arg from production call site + drop s string value param from acceptNERSpans (receiver h preserved)

### Modified (11 test-helper-unparam targets + 1 production dead-param drop, T3)
- `internal/adapter/anthropic/handlers_session_test.go` — `//nolint:unparam` doPostWithSid
- `internal/adapter/anthropic/sse.go` — drop req *canonical.ChatRequest from (*sseEmitter).aggregatedResponse + 9 call sites; preserve model=""
- `internal/adapter/anthropic/sse_posthook_test.go` — `//nolint:unparam` runSSEEmitterAndPostHooks
- `internal/adapter/ollama/chat_trace_e2e_test.go` — `//nolint:unparam` runNDJSONEmitterDirect
- `internal/adapter/ollama/handlers_test.go` — `//nolint:unparam` buildFormatIntegAdapter
- `internal/adapter/ollama/ndjson_posthook_test.go` — `//nolint:unparam` runNDJSONEmitterAndPostHooks
- `internal/adapter/openai/handlers_session_test.go` — `//nolint:unparam` doChatCompletions
- `internal/adapter/openai/sse_posthook_test.go` — `//nolint:unparam` runSSEEmitterAndPostHooks
- `internal/plugin/pii/pii_test.go` — `//nolint:unparam` freshHook
- `internal/pool/pool_test.go` — `//nolint:unparam` waitForSlotDead
- `internal/session/reaper_test.go` — `//nolint:unparam` eventually

## Per-category decision record (LINT-03 evidence)

Lifted verbatim from PLAN.md so the cleanup record is searchable from SUMMARY artifacts alone.

### wrapcheck (9 sites — **policy: wrap with fmt.Errorf using %w**)

`.golangci.yml` `wrapcheck.ignoreSigs` already exempts `.Errorf(`, `errors.New(`, `errors.Unwrap(`, `errors.Join(`, `.Wrap(`, `.Wrapf(`, plus `_test.go` excludes wrapcheck entirely. Every remaining hit is production code returning an error from either (a) an interface method we define (`PreHook.Before`, `PostHook.After`, `SessionRegistry.Get`, `context.Context.Err`) or (b) an external-package call (`syscall.Kill`, `bufio.Reader.ReadString`, `json.Marshal`). Both classes get the same fix: wrap with `fmt.Errorf("<context>: %w", err)` so the caller can still `errors.Is`/`errors.As` the underlying error.

Per-site context strings applied (kept short, no PII / no path leakage):
- `internal/acp/pool_pgid_unix.go:47` (`syscall.Kill`) → `"acp.pool.pgid: kill pgroup: %w"`
- `internal/adapter/anthropic/handlers.go:404` (`SessionRegistry.Get`) → `"anthropic.handlers: session lookup: %w"`
- `internal/adapter/ollama/handlers.go:368` (`SessionRegistry.Get`) → `"ollama.handlers: session lookup: %w"`
- `internal/adapter/openai/handlers.go:432` (`SessionRegistry.Get`) → `"openai.handlers: session lookup: %w"`
- `internal/admin/sse.go:177` (`ctx.Err()`) → `"admin.sse: ctx done: %w"`
- `internal/admin/tail.go:423` (`bufio.Reader.ReadString`) → `"admin.tail: read line: %w"`
- `internal/engine/engine.go:332` (`PreHook.Before`) → `"engine: prehook: %w"` (plain form — PreHook interface carries no Name() method)
- `internal/engine/engine.go:350` (`PostHook.After`) → `"engine: posthook: %w"` (plain form — PostHook interface carries no Name() method)
- `internal/plugin/pii/summary.go:131` (`json.Marshal`) → `"pii.summary: marshal: %w"`

No `//nolint:wrapcheck` exemptions added.

### unparam (13 sites — **policy: split by intent**)

Three subcategories:

1. **Production-code dead parameters (3 sites) — fix.**
   - `internal/plugin/pii/contextual.go:24` `hasContextWithin(text, matchStart, matchEnd, keywords, window)` — `window` always received `defaultContextWindow` (50) at every call site, including all 6 test sites. Dropped param; inlined the package-level `defaultContextWindow` const (already existed since the plan's "promote to const" option was already present in the source). Updated 6 test call sites + 1 production site in `pii.go`.
   - `internal/plugin/pii/pii.go:259` `(*PIIRedactionHook).acceptNERSpans(s, candidates, ...)` — `s string` value param was unused. Dropped; receiver `h` unchanged. Updated the single call site in `redact()`.
   - `internal/adapter/anthropic/sse.go:497` `(*sseEmitter).aggregatedResponse(req, stop)` — every one of the 9 call sites passed `req=nil` and `model` resolved to `""` accordingly. Dropped `req`. Preserved the prior `model=""` behavior (rather than picking up `e.model` to attach the wire model) so this wave stays strictly behavior-neutral; a future caller wanting the wire model can read `e.model` directly. Updated all 9 call sites.

2. **Test-helper symmetry exemptions (10 sites — all `_test.go`) — `//nolint:unparam` with rationale.**
   These helpers parameterize a value for symmetry with a sibling helper in the cross-package surface (anthropic ↔ openai ↔ ollama) OR keep a result-shape contract that the linter can't see across files. Per-site directives:

   | File | Function | Rationale |
   |------|----------|-----------|
   | `adapter/anthropic/handlers_session_test.go:107` | doPostWithSid | helper-pair symmetry with openai/ollama variants |
   | `adapter/anthropic/sse_posthook_test.go:33` | runSSEEmitterAndPostHooks | helper-pair symmetry with openai variant; result shape contract |
   | `adapter/ollama/chat_trace_e2e_test.go:217` | runNDJSONEmitterDirect | helper-pair symmetry with runNDJSONEmitterAndPostHooks |
   | `adapter/ollama/handlers_test.go:860` | buildFormatIntegAdapter | adapter-pair return contract |
   | `adapter/ollama/ndjson_posthook_test.go:32` | runNDJSONEmitterAndPostHooks | helper-pair symmetry with anthropic variant |
   | `adapter/openai/handlers_session_test.go:121` | doChatCompletions | helper-pair symmetry with anthropic/ollama variants |
   | `adapter/openai/sse_posthook_test.go:31` | runSSEEmitterAndPostHooks | helper-pair symmetry with anthropic variant |
   | `plugin/pii/pii_test.go:60` | freshHook | mode param kept polymorphic for future mask/hash/drop coverage |
   | `pool/pool_test.go:866` | waitForSlotDead | timeout param kept polymorphic for future timing tests |
   | `session/reaper_test.go:18` | eventually | timeout param kept polymorphic for future polling tests |

   Pattern: `func helper(...) (...) { //nolint:unparam // <rationale>`

   Bare `//nolint:unparam` without a `// <rationale>` segment is a regression — verified empty via `git diff | grep '^+.*nolint:unparam' | grep -v '// '` returning no lines.

Final tally: **3 production fixes** + **10 //nolint:unparam exemptions** = 13 unparam violations resolved.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Worktree-path safety] Recovery from absolute-path edit misdirection**
- **Found during:** Task 1 verification (lint count showed 9 wrapcheck still present after edits + worktree `git status` was clean)
- **Issue:** Bash `cd` in a prior call snapped to the main repo root. Subsequent Edit calls used the absolute path under `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/internal/...` (the main repo), not the worktree. All 8 wrapcheck edits landed in the main repo, not the worktree.
- **Fix:** Captured the misdirected diff to `/tmp/wave2-task1.patch`, `git checkout --` the 8 files in the main repo (preserving an unrelated main-repo modification to `.planning/STATE.md`), then `git apply` the patch inside the worktree. Verified via `git status` in both locations.
- **Files affected:** All 8 Task-1 wrapcheck targets, briefly.
- **Commit:** Task-1 commit `1a975c0` is correct; main repo `internal/` files remain clean.
- **Lesson:** Per `references/worktree-path-safety.md` #3099 — prefer relative paths inside the worktree; derive absolute paths from `git rev-parse --show-toplevel` run *inside* the worktree, not from a stale `pwd`.

### Deviations from PLAN.md text

**2. [Conservative behavior preservation] sse.go aggregator model preservation**
- **PLAN.md text said:** "Drop the parameter — production code should not carry test-only knobs."
- **Action taken:** Param dropped (as instructed), but the body retained `model := ""` rather than promoting to `e.model`. Every prior caller passed `req=nil` and `model` resolved to `""`, so picking up `e.model` would introduce a non-zero behavior delta (PostHook.After + downstream consumers would now observe `resp.Model = wire.Model` instead of `""`). For a lint-cleanup wave the load-bearing rule is "no behavior change" — promoting to `e.model` is a one-line follow-up if/when a caller actually needs it.
- **Why not a Rule-4 architectural checkpoint:** This is a behavior-preservation refinement, not a structural change. The plan's spirit ("drop dead param") is honored; the body keeps the observable behavior identical.
- **Files affected:** `internal/adapter/anthropic/sse.go` (aggregatedResponse body)
- **Commit:** included in `8b5aa0a`

### Out-of-scope discoveries (not fixed, NOT routed to deferred-items.md)

None new in this wave. The two QF1001 + two G703 items already in `deferred-items.md` (logged by 10-01) remain queued for Wave 3.

## Verification Results

| Check | Expected | Actual | Status |
|-------|----------|--------|--------|
| `golangci-lint` wrapcheck count | 0 | 0 | PASS |
| `golangci-lint` unparam count | 0 | 0 | PASS |
| `golangci-lint` combined wrapcheck+unparam | 0 | 0 | PASS |
| Every new //nolint:unparam carries `// <rationale>` | yes | yes (`git diff \| grep '^+.*nolint:unparam' \| grep -v '// '` empty) | PASS |
| Every wrapcheck wrap uses `%w` (not `%v`) | yes | yes (regression-check empty) | PASS |
| `go build ./...` | clean | clean | PASS |
| `go test -race ./...` | all packages green | 17 packages green | PASS |
| Total remaining baseline-style violations | 11 (33 - 22) | 15 (includes the 4 items already in deferred-items.md from Wave 1: 2 new QF1001 + 2 unmasked G703 + 11 = 15) | EXPECTED-DELTA (documented) |
| sse.go aggregator `req` param dropped (not exempted) | yes | yes (signature is `aggregatedResponse(stop canonical.StopReason)`) | PASS |

## Threat Flags

None new. Per the plan's threat_model, T-10-03 (information disclosure via wrap context strings) mitigation is verified: every per-site prefix is a static literal with no operator-provided interpolation. T-10-04 (repudiation via lost %w trace) mitigation is verified: every wrap uses `%w`, not `%v`.

## Self-Check: PASSED

- File `.planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-02-SUMMARY.md`: FOUND (this file)
- Commit `1a975c0` (Task 1): FOUND in git log
- Commit `2506b64` (Task 2): FOUND in git log
- Commit `8b5aa0a` (Task 3): FOUND in git log
- Worktree branch HEAD: on `worktree-agent-a732ac735cb5bf70f` (verified at agent start + at each commit)
- No modifications to `.planning/STATE.md` or `.planning/ROADMAP.md` in this worktree (orchestrator owns those after wave completion)
