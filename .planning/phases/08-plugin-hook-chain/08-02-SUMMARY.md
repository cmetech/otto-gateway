---
phase: 08-plugin-hook-chain
plan: 02
subsystem: plugin
tags: [plugin, auth, bearer, short-circuit, migration, constant-time, T-8-AUTH]

# Dependency graph
requires:
  - phase: 08-plugin-hook-chain
    plan: 01
    provides: engine.PreHook/PostHook seam, plugin.Chain shell, Pattern B (typed-ctx-key + accessor pair) blueprint
  - phase: 02-ollama-end-to-end
    provides: internal/auth/bearer.go ConstantTimeCompare loop precedent (refactor source)
  - phase: 03.1-anthropic-surface
    provides: D-15 dual-header (x-api-key + Authorization Bearer) auth precedence rule
provides:
  - canonical.WithBearerToken / canonical.BearerTokenFromContext (typed-key ctx bridge for adapter→AuthHook credential transit)
  - canonical.StopError (7th StopReason value — Pre-hook short-circuit envelope discriminator)
  - plugin.AuthHook (engine.PreHook; constant-time-compare; short-circuits via canonical envelope on bad/missing token)
  - auth.ExtractToken (exported, was private extractToken — Authorization-Bearer with x-api-key fallback per D-15)
  - Per-surface adapter ctx-stamping: ollama (handleChat + handleGenerate), openai (handleChatCompletions + handleCompletions), anthropic (handleMessages with x-api-key-first precedence)
affects: [08-03-logging-hook, 08-04-pii-redaction-hook, 08-05-main-wiring, 08-06-health-hooks-handler]

# Tech tracking
tech-stack:
  added: []  # no new dependencies; reuses stdlib crypto/subtle (Phase 2 already)
  patterns:
    - "Canonical-typed ctx-credential bridge (Pattern C variant): adapters stamp via canonical.WithBearerToken, hooks read via canonical.BearerTokenFromContext"
    - "Synthetic short-circuit envelope: StopReason == StopError + Message.Content[0].Text == user_message; per-surface adapter renders native error envelope"
    - "Shared header-parse helper via auth.ExtractToken export — three adapter handlers share one source of truth for the D-15 dual-header precedence"
    - "Migration-boundary discipline (Pattern F): AuthHook + auth.Bearer chi middleware co-exist transiently; slice 5 removes middleware after main.go wires AuthHook"

key-files:
  created:
    - internal/canonical/auth_ctx.go (WithBearerToken + BearerTokenFromContext + bearerKey ctx primitive)
    - internal/canonical/auth_ctx_test.go (3 tests: round-trip, absent-returns-empty, empty-stamp-still-stored)
    - internal/canonical/testmain_test.go (goleak gate for canonical package — established this slice)
    - internal/plugin/auth.go (AuthHook + synthesizeAuthError + compile-time engine.PreHook check)
    - internal/plugin/auth_test.go (9 tests: nil/empty/valid/invalid/missing/empty-string/multi-token + Name + Describe-no-secrets + source-audit)
  modified:
    - internal/canonical/stop_reason.go (added StopError as 7th StopReason)
    - internal/canonical/types_test.go (extended locked StopReason set from 6 to 7)
    - internal/auth/bearer.go (renamed extractToken → ExtractToken — exported for adapter consumption)
    - internal/auth/auth_internal_test.go (rename caller — extractToken → ExtractToken; 10 references)
    - .go-arch-lint.yml (added `auth` to adapter_ollama / adapter_openai / adapter_anthropic mayDependOn for ExtractToken)
    - internal/adapter/ollama/handlers.go (handleChat + handleGenerate stamp ctx via canonical.WithBearerToken before engine.Run / eng.Collect)
    - internal/adapter/openai/handlers.go (handleChatCompletions + handleCompletions stamp ctx via canonical.WithBearerToken)
    - internal/adapter/anthropic/handlers.go (handleMessages stamps ctx with x-api-key-first precedence + Bearer fallback)

key-decisions:
  - "Added canonical.StopError as a new StopReason value (NOT reusing StopUnknown or StopRefusal) — Plan Task 3 explicitly authorized 'smallest necessary canonical-type extension'; StopRefusal carries Anthropic-spec semantics that the AuthHook envelope is NOT, and StopUnknown is the parse-failure / abrupt-close sentinel. StopError gives surface adapters one discriminator with one meaning: 'this response was synthesized by a Pre hook to short-circuit the chain'."
  - "Exported auth.extractToken → auth.ExtractToken AND added `auth` to each adapter's go-arch-lint mayDependOn rather than rolling the header parser inline in each adapter. One source of truth — a future fix to the D-15 precedence touches one file. The arch-lint dep is honest: adapters genuinely consume the auth helper now."
  - "Anthropic stamps with x-api-key FIRST then Bearer fallback (matches Anthropic SDK convention), inverting the Ollama / OpenAI precedence (Authorization-wins). The per-surface inversion is deliberate — it preserves backward-compat with loop24-client / @anthropic-ai/sdk which prioritizes x-api-key. AuthHook only sees the resolved string; the per-surface precedence does NOT leak into the canonical layer."
  - "Stream-path ctx must derive from the bearer-stamped ctx (NOT r.Context()) — the existing pattern `ctx, cancelFn := context.WithCancel(r.Context())` would shadow the stamp, leaving AuthHook blind on streaming. Renamed to `streamCtx` in all three adapters to make the dependency on the outer `ctx` explicit."
  - "Test count: plan said '8 tests' in acceptance_criteria but `<behavior>` itemized 9 (the source-audit test was uncounted). Kept all 9 — the source-audit test is the belt-and-suspenders guard against a future `==` refactor and was clearly intended."

patterns-established:
  - "Pattern: canonical-typed ctx-credential bridge — when a hook needs HTTP-layer info that doesn't belong on ChatRequest (auth tokens, future per-request flags), the bridge lives in canonical (because both adapters AND plugin must read it) as a typed-key ctx primitive. AuthHook is the blueprint for slices 3 (LoggingHook may need request-id ctx propagation patterns) and 4 (PII summary ctx)."
  - "Pattern: short-circuit envelope with StopError discriminator — Pre hooks return a *canonical.ChatResponse with StopReason==StopError + a text content part carrying the user-facing message. engine.Collect preserves verbatim (Codex H-4); per-surface adapters detect StopError and render the native error envelope. Reusable by any future Pre hook (rate-limiter, content moderator) that needs a synthetic non-error short-circuit."
  - "Pattern: migration-boundary discipline (Pattern F materialized) — old defense (auth.Bearer middleware) stays active until new defense (AuthHook on the chain) is fully wired. Two slices instead of one big-bang commit; intermediate state is correct (defense-in-depth) and rollback is one revert."

requirements-completed: [PLUG-03, PLUG-04]

# Metrics
duration: ~28 min (worktree parallel executor pass; no checkpoints triggered)
completed: 2026-05-27
---

# Phase 8 Plan 02: AuthHook Vertical Slice Summary

**Shipped `plugin.AuthHook` — the second Pre hook in the day-one chain — together with the load-bearing `canonical.WithBearerToken`/`BearerTokenFromContext` ctx-credential bridge and the three per-surface adapter stamps that feed AuthHook from `Authorization` / `x-api-key` headers without a per-adapter auth path. Bearer-token validation now has ONE canonical-layer source of truth (the chain) instead of three (one per surface), realizing the Phase 8 PLUG-03 cash-out of the project's "one place to enforce policy" core value. Defense-in-depth preserved transiently: the `auth.Bearer` chi middleware stays active until slice 5 wires AuthHook into the chain in `main.go`.**

## Performance

- **Duration:** ~28 min (single executor pass; no checkpoints triggered)
- **Started:** 2026-05-27 (worktree-parallel execution)
- **Tasks:** 4 implementation tasks (no human-action / human-verify checkpoints in this slice)
- **Files created:** 5 (3 source, 2 test)
- **Files modified:** 7 (canonical/types_test.go, canonical/stop_reason.go, auth/bearer.go, auth/auth_internal_test.go, .go-arch-lint.yml, three adapter handlers.go)
- **Commits:** 4 atomic task commits

## Accomplishments

- **`canonical.StopError` ships as the synthetic short-circuit discriminator** — per-surface adapters (slice 5) will detect this on a `*canonical.ChatResponse` returned by `Collect` and render their native error shape. One discriminator, three renderers, zero coupling between hook and surface.
- **`canonical.WithBearerToken` / `BearerTokenFromContext` is the canonical-typed bridge** — adapters call `WithBearerToken(ctx, token)` BEFORE invoking `engine.Run` / `engine.Collect`; AuthHook reads via `BearerTokenFromContext(ctx)`. The bridge lives in `canonical` (not `plugin`) because BOTH adapter packages AND plugin must read from it (TRST-04 forbids adapter→plugin imports).
- **Three-way state distinction preserved:** `BearerTokenFromContext` returns `(token, ok)` so callers can tell `(absent, ok=false)` from `(present-but-empty, ok=true, token="")` from `(present-with-value)`. AuthHook treats both `absent` and `present-but-empty` as failure when `Tokens` is non-empty; the distinction matters for future hooks (e.g., a hook that emits a "missing credential" warning only when the adapter ran auth-resolution but found nothing).
- **T-8-AUTH mitigated source-level:** `auth.go` uses `subtle.ConstantTimeCompare` (verified by acceptance grep); `TestAuthHook_ConstantTimeCompareSourceAudit` is the belt-and-suspenders guard against a future `==` regression (opens auth.go via `os.ReadFile` and asserts the forbidden patterns).
- **T-8-LEAK mitigated:** `Describe()` exposes only `token_count`, NEVER the tokens. `TestAuthHook_Describe_NoSecrets` walks the returned map and fails any key whose lowercased name contains `"token"` other than the allowed `"token_count"`.
- **T-8-AUTH-4 mitigated:** No adapter handler introduces raw-token logging. Verified by `grep -nE 'logger\..+(Authorization|x-api-key|bearer)' internal/adapter/*/handlers.go` returning empty.
- **Migration-boundary discipline preserved (Pattern F):** `auth.Bearer` chi middleware UNCHANGED — slice 5 removes it in one atomic commit after main.go wires AuthHook into the chain. Intermediate state is correct (defense-in-depth) and rollback is a one-revert.
- **Three-surface ctx-stamp consistency:** All three adapter handlers stamp ctx before invoking engine — no per-adapter auth logic past header parsing. The per-surface dual-header precedence (Anthropic prefers x-api-key; OpenAI/Ollama prefer Authorization) is encoded at the adapter boundary; AuthHook sees one canonical string.

## Task Commits

Each task was committed atomically:

1. **Task 1: `canonical.WithBearerToken` / `BearerTokenFromContext` ctx bridge** — `121607b` (`feat(08-02): add canonical.WithBearerToken / BearerTokenFromContext ctx bridge`)
2. **Task 2: Wave 0 AuthHook test scaffold (9 RED tests)** — `e8f1b42` (`test(08-02): scaffold Wave 0 tests for AuthHook`)
3. **Task 3: AuthHook implementation + `canonical.StopError` addition** — `32dc703` (`feat(08-02): implement AuthHook with constant-time compare + canonical short-circuit envelope`)
4. **Task 4: Three-adapter ctx-stamps + `auth.ExtractToken` export + arch-lint dep edge** — `a4c613f` (`feat(08-02): adapters stamp bearer credential onto ctx for AuthHook consumption`)

## Files Created/Modified

### Created

- `internal/canonical/auth_ctx.go` — `WithBearerToken` / `BearerTokenFromContext` typed-key ctx primitive (`bearerKey` unexported struct; `bearerTokenKey = bearerKey{name: "bearer-token"}` package var). Three-state semantics documented in package banner.
- `internal/canonical/auth_ctx_test.go` — 3 tests pinning the round-trip, absent-returns-empty, and empty-stamp-still-stored contracts.
- `internal/canonical/testmain_test.go` — `goleak.VerifyTestMain` gate (Phase 8 PATTERNS Pattern D); established this slice for the canonical package (previously had no testmain).
- `internal/plugin/auth.go` — `AuthHook` (`Tokens []string`), compile-time `var _ engine.PreHook = (*AuthHook)(nil)`, `Name() == "AuthHook"`, `Describe() == ("Pre", {"token_count": N})`, `Before` short-circuits via `synthesizeAuthError` on missing/empty/invalid credential.
- `internal/plugin/auth_test.go` — 9 tests: empty-tokens passthrough (nil + empty subtests), valid token passthrough, invalid/missing/empty-string short-circuit, multi-token any-match (4 subtests), Name(), Describe-no-secrets (T-8-LEAK guard), source-audit (T-8-AUTH guard).

### Modified

- `internal/canonical/stop_reason.go` — Added `StopError` as the 7th `StopReason` value with the canonical docstring on its semantics.
- `internal/canonical/types_test.go` — Extended the locked StopReason set from 6 to 7 entries to include `StopError`.
- `internal/auth/bearer.go` — Renamed private `extractToken` → exported `ExtractToken`. Updated docstring to reference the new adapter consumers (Phase 8 Plan 08-02 Task 4). Caller updated.
- `internal/auth/auth_internal_test.go` — 10 `extractToken` references renamed to `ExtractToken`.
- `.go-arch-lint.yml` — Added `auth` to `adapter_ollama` / `adapter_openai` / `adapter_anthropic` `mayDependOn` so the `auth.ExtractToken` call type-checks. Header-parse helper only; documented as the slice-2 / Phase 8 PLUG-03 boundary.
- `internal/adapter/ollama/handlers.go` — `handleChat` (line 59) and `handleGenerate` (line 208) stamp `ctx` via `canonical.WithBearerToken(r.Context(), auth.ExtractToken(r))` BEFORE invoking `eng.Collect` / `eng.Run`. Stream-path `ctx` renamed `streamCtx` to derive from the bearer-stamped ctx (no shadowing).
- `internal/adapter/openai/handlers.go` — `handleChatCompletions` (line 63) and `handleCompletions` (line 199) stamp via the same idiom. Stream path renamed `streamCtx`.
- `internal/adapter/anthropic/handlers.go` — `handleMessages` (line 131) stamps with the Phase 3.1 D-15 dual-header precedence: `r.Header.Get("x-api-key")` first; if empty, fall back to `auth.ExtractToken(r)`. Stream path renamed `streamCtx`.

## Decisions Made

1. **Added `canonical.StopError` (NOT reused `StopUnknown` / `StopRefusal`).** The plan explicitly authorized "smallest necessary canonical-type extension" for the short-circuit envelope. `StopRefusal` carries Anthropic-spec assistant-refusal semantics that the AuthHook envelope is NOT, and `StopUnknown` is the parse-failure / abrupt-close sentinel. `StopError` gives surface adapters one discriminator with one meaning: "this response was synthesized by a Pre hook to short-circuit the chain". Required updating `internal/canonical/types_test.go`'s locked-set test (6 → 7 distinct values).
2. **Exported `auth.extractToken` → `auth.ExtractToken` + added `auth` to each adapter's `go-arch-lint mayDependOn`.** Plan explicitly authorized the rename. One source of truth — a future fix to the D-15 precedence touches one file (`internal/auth/bearer.go`). The arch-lint dep edge is honest: adapters genuinely consume the auth helper now. Updated the bearer.go docstring to document the new consumers.
3. **Anthropic stamps with `x-api-key` FIRST then `auth.ExtractToken` (Bearer) fallback** — inverting the Ollama / OpenAI precedence. This matches Anthropic SDK convention and preserves backward-compat with loop24-client / `@anthropic-ai/sdk` which prioritizes `x-api-key`. The per-surface inversion is deliberate; AuthHook only sees the resolved token string, so the per-surface precedence does NOT leak into the canonical layer.
4. **Stream-path ctx must derive from the bearer-stamped ctx, NOT `r.Context()`.** The existing pattern `ctx, cancelFn := context.WithCancel(r.Context())` would shadow the bearer stamp, leaving AuthHook blind on streaming. Renamed the stream-scope variable to `streamCtx` in all three adapters (six call sites total) so the dependency on the outer `ctx` is explicit.
5. **Kept 9 AuthHook tests (not 8).** Plan acceptance_criteria said "8 tests with TestAuthHook_ prefix" but the `<behavior>` block itemized 9 (the source-audit test was explicit but uncounted). The source-audit test is the belt-and-suspenders guard against a future `==` refactor and is clearly intended — documenting the count discrepancy as a plan-text inconsistency rather than removing a useful test.
6. **Added `internal/canonical/testmain_test.go` (new goleak gate for the canonical package).** Plan Task 1 directed this conditionally on its absence; it was absent. The canonical package previously had no testmain because all canonical tests were pure-data round-trips with no goroutine surface; the gate is established now so future hooks that stash channels / cancelable contexts can't leak goroutines into the canonical suite.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Stream-path ctx variable shadowing (latent bug fixed in Task 4)**

- **Found during:** Task 4 implementation review while threading ctx through `handleChat`.
- **Issue:** The pre-existing pattern `ctx, cancelFn := context.WithCancel(r.Context())` on the stream branch shadowed the Task-4-stamped `ctx` (`canonical.WithBearerToken(r.Context(), auth.ExtractToken(r))`) — AuthHook would have been blind on the streaming path. Easy to miss because the variable name is the same.
- **Fix:** Renamed the stream-scope variable to `streamCtx` in all three adapters' stream branches and derived from the outer (stamped) `ctx`: `streamCtx, cancelFn := context.WithCancel(ctx)`. Six call sites total. All `eng.Run(streamCtx, req)` / `runSSEEmitter(streamCtx, ...)` / `runNDJSONEmitter(streamCtx, ...)` updated to the new variable.
- **Files modified:** `internal/adapter/ollama/handlers.go`, `internal/adapter/openai/handlers.go`, `internal/adapter/anthropic/handlers.go`.
- **Verification:** Full adapter test suite (`./internal/adapter/...`) green; full project suite (`./...`) green.
- **Committed in:** `a4c613f` (Task 4 commit).

**2. [Rule 3 - Blocking] `auth` package added to three adapters' `go-arch-lint.mayDependOn`**

- **Found during:** Task 4 — `auth.ExtractToken` import in `internal/adapter/ollama/handlers.go` initially failed `go-arch-lint check` with "depends on auth (not in mayDependOn)".
- **Issue:** Plan explicitly authorized the `ExtractToken` rename + export but the arch-lint edge addition required to USE the export from the adapter packages was implicit.
- **Fix:** Added `- auth` to `adapter_ollama`, `adapter_anthropic`, `adapter_openai` `mayDependOn` blocks in `.go-arch-lint.yml` with the same multi-line documentation idiom the file uses for the Phase 6 engine dep edge. Header-parse helper only — the `auth.Bearer` chi middleware stays at the server router until slice 5 removes it.
- **Files modified:** `.go-arch-lint.yml`.
- **Verification:** `go-arch-lint check` exits `OK - No warnings found`.
- **Committed in:** `a4c613f` (Task 4 commit).

**3. [Plan-text inconsistency] Test count: 9 vs "8 tests"**

- **Found during:** Task 2 scaffold.
- **Issue:** Plan `<acceptance_criteria>` said "Verify: `grep -cE '^func TestAuthHook_' internal/plugin/auth_test.go` returns `8`" and `<action>` said "with the 8 tests in `<behavior>`", but the `<behavior>` block enumerated 9 tests explicitly (the `TestAuthHook_ConstantTimeCompareSourceAudit` test was clearly defined but uncounted).
- **Fix:** Kept all 9 tests — the source-audit test is the belt-and-suspenders guard against a future `==` refactor reintroducing the timing side-channel; removing it to hit a count was the wrong fix. Documented the discrepancy in the Task 2 commit message.
- **Files modified:** None beyond Task 2 source.
- **Committed in:** `e8f1b42` (Task 2 commit).

---

**Total deviations:** 2 auto-fixed (Rule 3 — both required for plan completion) + 1 plan-text inconsistency note.

**Impact on plan:** No scope expansion beyond what the plan implied. The stream-ctx shadowing fix is structurally required to make AuthHook see streaming-path auth; the arch-lint edge is required to USE the exported helper the plan asked for; the test count discrepancy is a plan-text bug not a scope change.

## Output Spec — Plan Output Requirements

Per the `<output>` block in 08-02-PLAN.md:

- **`canonical.StopError` — newly added.** Did not exist before this slice. Added in `internal/canonical/stop_reason.go` as the 7th `StopReason` value alongside an extension of the locked-set test in `types_test.go`. Slice 5's per-surface error rendering needs this signal to detect "synthetic short-circuit from a Pre hook → render native error envelope".
- **`auth.extractToken` — exported as `auth.ExtractToken`.** Plan-text-authorized rename. 11 call sites updated (1 in `bearer.go`, 10 in `auth_internal_test.go`). The rename is a public-API touch slice 5 should note in its main.go wiring documentation.
- **Per-adapter ctx-stamp insertion points:**
  - `internal/adapter/ollama/handlers.go:59` — `handleChat` stamps `ctx := canonical.WithBearerToken(r.Context(), auth.ExtractToken(r))` BEFORE the `resolveEngine(r)` call; `eng.Collect(ctx, req)` at line 88; stream path `streamCtx, cancelFn := context.WithCancel(ctx)` at line 132 + `eng.Run(streamCtx, req)` at line 135.
  - `internal/adapter/ollama/handlers.go:208` — `handleGenerate` mirrors the same pattern; `eng.Collect(ctx, req)` at line 229; stream `eng.Run(streamCtx, req)` at line 250.
  - `internal/adapter/openai/handlers.go:63` — `handleChatCompletions` stamps before `resolveEngine`; stream `eng.Run(streamCtx, req)` at line 99; non-streaming `eng.Collect(ctx, req)` at line 117.
  - `internal/adapter/openai/handlers.go:199` — `handleCompletions` stamps; `eng.Collect(ctx, req)` at line 217.
  - `internal/adapter/anthropic/handlers.go:131` — `handleMessages` stamps with `x-api-key`-first precedence (line 127: `token := r.Header.Get("x-api-key")`; if empty, line 129: `token = auth.ExtractToken(r)`); stream `eng.Run(streamCtx, req)` at line 157; non-streaming `CollectAnthropicChat(ctx, eng, req)` at line 187.
- **Unexpected adapter test failures:** None. Full `go test ./internal/adapter/...` and `go test ./...` green after each task commit.

## Issues Encountered

- **`gosec` not installed locally** — the Task 3 acceptance criterion `gosec ./internal/plugin/... -severity high -confidence high` was not executable in this worktree environment. The project's CI runs gosec; the AuthHook source uses `subtle.ConstantTimeCompare` which is gosec-approved (G401-G407 don't apply; G505 doesn't apply). Source-level audit test (`TestAuthHook_ConstantTimeCompareSourceAudit`) is the in-suite guard for the same property. Slice 5 / CI will surface any gosec issue.
- **Worktree HEAD-base mismatch on agent startup** — the agent-init reset moved HEAD from `d2b8851` (which included `cf6ec80` + `f843e06` from slice 1) back to `f843e06` per the EXPECTED_BASE env. Slice 1 commits are present in `git log`. No work lost; the worktree reset only re-pins the parent commit for this agent's branch.

## Self-Check

Verifying claimed artifacts and commits.

### Files exist on disk

```
[ -f internal/canonical/auth_ctx.go ]           → FOUND
[ -f internal/canonical/auth_ctx_test.go ]      → FOUND
[ -f internal/canonical/testmain_test.go ]      → FOUND
[ -f internal/plugin/auth.go ]                  → FOUND
[ -f internal/plugin/auth_test.go ]             → FOUND
[ -f internal/canonical/stop_reason.go ]        → FOUND (modified — StopError added)
[ -f internal/canonical/types_test.go ]         → FOUND (modified — 7-value lock)
[ -f internal/auth/bearer.go ]                  → FOUND (modified — ExtractToken export)
[ -f internal/auth/auth_internal_test.go ]      → FOUND (modified — renamed callers)
[ -f .go-arch-lint.yml ]                        → FOUND (modified — adapter→auth dep edge)
[ -f internal/adapter/ollama/handlers.go ]      → FOUND (modified — ctx stamp)
[ -f internal/adapter/openai/handlers.go ]      → FOUND (modified — ctx stamp)
[ -f internal/adapter/anthropic/handlers.go ]   → FOUND (modified — dual-header ctx stamp)
```

### Commits exist in git log

```
git log --oneline | grep 121607b  → feat(08-02): add canonical.WithBearerToken / BearerTokenFromContext ctx bridge
git log --oneline | grep e8f1b42  → test(08-02): scaffold Wave 0 tests for AuthHook
git log --oneline | grep 32dc703  → feat(08-02): implement AuthHook with constant-time compare + canonical short-circuit envelope
git log --oneline | grep a4c613f  → feat(08-02): adapters stamp bearer credential onto ctx for AuthHook consumption
```

### Plan-level verification

- `go test ./internal/canonical/... -count=1 -race` → exit 0 (auth_ctx tests pass + goleak gate established)
- `go test ./internal/plugin/... -count=1 -race -run TestAuthHook` → exit 0 (9 AuthHook tests pass)
- `go test ./internal/adapter/... -count=1 -race` → exit 0 (no adapter regression)
- `go test ./... -count=1 -race -timeout=120s` → exit 0 (full suite green)
- `go build ./...` → exit 0
- `go-arch-lint check` → exit 0 (`OK - No warnings found`)
- Source-grep assertions (Task 3): `subtle.ConstantTimeCompare(` present, `var _ engine.PreHook = (*AuthHook)(nil)` present, `token_count` present, no `tokens:` JSON-style key, no `provided == valid` / `valid == provided` / `== string(` patterns.
- Source-grep assertions (Task 4): three adapter handlers contain `canonical.WithBearerToken(`, Anthropic contains `x-api-key`, no `logger.*Authorization|x-api-key|bearer` matches in any adapter handler.

## Self-Check: PASSED

All 5 created files exist on disk; all 4 task commits present in git log; all plan-level verification commands exit clean; every source-level acceptance assertion holds.

## Next Phase Readiness

- **Slice 2 complete.** AuthHook + canonical ctx bridge + per-surface adapter stamping is stable. Defense-in-depth maintained — the `auth.Bearer` chi middleware is still active in the server router.
- **Ready for 08-03 (LoggingHook slice).** Pattern B from slice 1 + the canonical-typed ctx primitive pattern from slice 2 give LoggingHook two blueprints: typed-key ctx for `loggingStartKey` (PATTERNS reuse) and the slog correlation idiom via `RequestIDFromContext`.
- **Ready for 08-04 (PIIRedactionHook slice).** `canonical.StopError` is now available if PIIRedaction ever needs to short-circuit on a fatal violation. The synthesizeAuthError helper in `plugin/auth.go` is a copy-paste template for `synthesizePIIBlocked` if needed.
- **Ready for 08-05 (main.go wiring + auth.Bearer middleware removal).** Slice 5 will:
  1. Construct the chain literal: `Chain{Pre: []engine.PreHook{&plugin.RequestIDHook{...}, &plugin.AuthHook{Tokens: cfg.AuthToken}, &plugin.LoggingHook{...}}}`.
  2. Pass through `Chain.Filter(cfg.EnabledHooks)`.
  3. Wire into engine.Config.PreHooks / PostHooks.
  4. Remove the `auth.Bearer(cfg)` call at `internal/server/server.go:230` in the SAME commit (Pattern F atomic removal). The per-surface error renderers (slice 5) will detect `StopReason == canonical.StopError` and render the native error envelope.
  5. Note for slice 5: `auth.ExtractToken` is now public — main.go does NOT need to call it (adapters already stamp); but if main.go ever needs the same parse, it's the canonical helper.
- **Ready for 08-06 (`/health/hooks` handler).** AuthHook's `Describe()` returns `("Pre", {"token_count": N})` — the handler emits this with no further work; the existing `Chain.Describe()` from slice 1 already calls per-hook `Describer`.
- **No blockers.** No deferred items. No outstanding architectural questions.

---
*Phase: 08-plugin-hook-chain*
*Completed: 2026-05-27*
