---
phase: 02-ollama-end-to-end
plan: 02
subsystem: auth
tags: [auth, middleware, bearer-token, ip-allowlist, netip, crypto-subtle, chi]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: chi router skeleton, slog logger, testutil.Logger pattern, accessLog middleware shape
provides:
  - internal/auth.Config (Logger, Tokens, AllowedPrefixes, TrustXForwardedFor)
  - internal/auth.Bearer(cfg) chi middleware factory (constant-time token comparison)
  - internal/auth.IPAllowlist(cfg) chi middleware factory (netip.Prefix matching)
  - internal/auth.writeOllamaError package-private helper (Ollama-shape JSON error body)
  - Default-deny posture for X-Forwarded-For (Codex H-7 / threat T-02-06)
affects:
  - Phase 02 Plan 06 (server wiring — will mount Bearer + IPAllowlist on a chi sub-router so /health, /api/version exempt-paths bypass them per AUTH-03)
  - Phase 03 (OpenAI surface — same Config, same middlewares)
  - Phase 03.1 (Anthropic surface — same Config, same middlewares)

# Tech tracking
tech-stack:
  added:
    - crypto/subtle (stdlib — constant-time bearer comparison)
    - net/netip (stdlib — modern allocation-free IP / CIDR matching)
  patterns:
    - "Middleware factory shape: `func(cfg Config) func(http.Handler) http.Handler`, matching the chi-compatible accessLog already in internal/server"
    - "Empty-config-is-default: zero-value Config produces passthrough middlewares; no separate `enabled bool` flag (matches Node reference semantics)"
    - "Allow-all fast path: IPAllowlist returns the identity factory when AllowedPrefixes is empty — no per-request wrapper allocated"
    - "Blackbox tests in `package auth_test` for the exported surface; one whitebox file (`auth_internal_test.go`, `package auth`) for the package-private writeOllamaError"

key-files:
  created:
    - internal/auth/auth.go (Config struct + writeOllamaError helper, package doc)
    - internal/auth/bearer.go (Bearer middleware factory)
    - internal/auth/ipallowlist.go (IPAllowlist middleware + extractClientIP helper)
    - internal/auth/auth_test.go (blackbox tests — 13 functions)
    - internal/auth/auth_internal_test.go (whitebox test for writeOllamaError)
  modified: []

key-decisions:
  - "Codex H-7 default: X-Forwarded-For is NOT trusted by default. Operator opts in via Config.TrustXForwardedFor (env-var wiring deferred to Plan 03). Reversed RESEARCH.md Pattern 4's XFF-first behaviour because the Loop24 laptop deployment model (Assumption A3) has no proxy in front — a localhost client could otherwise set `X-Forwarded-For: 127.0.0.1` and bypass ALLOWED_IPS."
  - "writeOllamaError stays package-private — its only callers are Bearer + IPAllowlist. Verified at source level via grep; whitebox test covers the contract."
  - "IPAllowlist allow-all path returns the identity factory (`return func(next http.Handler) http.Handler { return next }`) rather than wrapping the handler — avoids per-request overhead when the operator has not configured ALLOWED_IPS."

patterns-established:
  - "Auth-package middleware contract: `func Foo(cfg Config) func(http.Handler) http.Handler` returning chi-compatible factories — future hooks (rate limiting, content moderation) follow this shape"
  - "Ollama-shape error envelope: writeOllamaError centralises {\"error\": \"<msg>\"} JSON emission so future surfaces have one place to keep the byte-for-byte Node parity"
  - "TDD whitebox/blackbox split: integration-style middleware tests in `package auth_test`; package-private contract assertions in `package auth` — applied D-18 generalization from Phase 1"

requirements-completed: [AUTH-01, AUTH-02, AUTH-03]

# Metrics
duration: ~25 min
completed: 2026-05-24
---

# Phase 02 Plan 02: internal/auth Bearer + IPAllowlist Middlewares Summary

**Bearer-token middleware with `crypto/subtle.ConstantTimeCompare` + IP-allowlist middleware using `net/netip` prefixes, both behind an opt-in X-Forwarded-For trust gate (Codex H-7) — landed as four files under `internal/auth/` with a 14-test blackbox/whitebox suite covering empty-config passthrough, valid/invalid auth, CIDR + single-IP matching, IPv4-in-IPv6 stripping, and the full XFF trust matrix.**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-05-24T00:30:00Z (approximate — branch base at 94f2bb3)
- **Completed:** 2026-05-24T00:37:06Z
- **Tasks:** 3 (all `tdd="true"`)
- **Files created:** 5 (source + tests)
- **Lines added:** ~440 (60 auth.go + 49 bearer.go + 80 ipallowlist.go + 215 auth_test.go + 36 auth_internal_test.go)
- **Tests:** 14 passing (`-race -count=1`), zero failures
- **Lint:** `golangci-lint run ./internal/auth/...` → 0 issues

## Accomplishments

- **AUTH-01 (Bearer via AUTH_TOKEN):** Middleware-level coverage — `Bearer(cfg)` matches the Node reference contract verbatim (Authorization header → constant-time compare → 401 + Ollama-shape body on rejection, passthrough when Tokens slice is empty).
- **AUTH-02 (IP allowlist via ALLOWED_IPS):** Middleware-level coverage — `IPAllowlist(cfg)` matches client IP via `netip.Prefix.Contains`, falls through to allow-all when AllowedPrefixes is empty, and emits 403 + Ollama-shape body with the rejected IP in the message.
- **AUTH-03 prerequisite:** Both middlewares are pure factories with no path-prefix branching — Plan 06 wires them onto a chi sub-router so `/health`, `/api/version`, `/health/agents` automatically bypass them (the exempt-path discipline is enforced by the routing topology, not by middleware-internal allowlists).
- **Codex H-7 (XFF spoofing defense):** Reversed RESEARCH.md Pattern 4's "XFF-first" default. `extractClientIP(r, trustXFF)` only consults `X-Forwarded-For` when the operator has set `Config.TrustXForwardedFor = true`. Default path uses `r.RemoteAddr` unconditionally; even when XFF trust is enabled, malformed XFF values gracefully fall back to RemoteAddr (proven by `TestIPAllowlist_MalformedXFF_FallsBackToRemoteAddr`).
- **Threat T-02-05 (timing side-channel):** Bearer tokens compared with `crypto/subtle.ConstantTimeCompare`. Verified at source level (`grep -c 'subtle.ConstantTimeCompare' internal/auth/bearer.go` returns 2, including the negative-pattern guard).
- **Threat T-02-07 (IPv4-in-IPv6 mapping bypass):** `::ffff:` prefix stripped before parsing as `netip.Addr` on both XFF and RemoteAddr code paths. `TestIPAllowlist_IPv4InIPv6Mapping` proves `[::ffff:127.0.0.1]:12345` matches `127.0.0.0/8`.

## Task Commits

Each task was committed atomically:

1. **Task 1: Create internal/auth package with Config struct + writeOllamaError helper** — `7359cec` (feat)
2. **Task 2: Implement Bearer + IPAllowlist middlewares** — `591b1c9` (feat)
3. **Task 3: Add auth_test.go + auth_internal_test.go covering Bearer + IPAllowlist happy/sad paths** — `c390ba9` (test)

_Note: TDD tasks 1 and 2 produced single feat commits because the structural-only RED state (no package exists → does not build) was satisfied by the GREEN write. Task 3 is a pure test commit landing the contract assertions for the middlewares from Tasks 1 + 2 — the RED state was that `go test ./internal/auth/...` previously returned `no test files`._

## Files Created/Modified

### Created
- `internal/auth/auth.go` (60 lines) — Package doc comment, `Config` struct with `Logger`, `Tokens`, `AllowedPrefixes`, `TrustXForwardedFor` fields, and the package-private `writeOllamaError` helper that emits Ollama-shape JSON error bodies.
- `internal/auth/bearer.go` (49 lines) — `Bearer(cfg) func(http.Handler) http.Handler` factory. Empty Tokens → passthrough; missing/invalid header → 401 + `{"error":"Invalid or missing API key"}`; matching token (constant-time compare) → passthrough.
- `internal/auth/ipallowlist.go` (80 lines) — `IPAllowlist(cfg) func(http.Handler) http.Handler` factory + `extractClientIP(r, trustXFF bool) (netip.Addr, bool)` helper. Empty AllowedPrefixes → identity factory (no per-request wrapper); otherwise iterates `netip.Prefix.Contains`. XFF trust gated behind `trustXFF` captured at factory-construction time.
- `internal/auth/auth_test.go` (215 lines, `package auth_test`) — 13 blackbox tests: 5 Bearer + 8 IPAllowlist (EmptyPrefixes, MatchingCIDR, NonMatchingIP, IPv4InIPv6Mapping, XFFNotTrustedByDefault, XFFRespectedWhenEnabled, XFFIgnored_FallsBackToRemoteAddr_WhenDisabled, MalformedXFF_FallsBackToRemoteAddr).
- `internal/auth/auth_internal_test.go` (36 lines, `package auth`) — Single whitebox `TestWriteOllamaError_Shape` test that asserts Content-Type + status + decoded JSON body against the Node reference contract.

### Modified
None outside the new `internal/auth/` package.

## Decisions Made

| Decision | Rationale |
|----------|-----------|
| `TrustXForwardedFor` default is `false` (Codex H-7) | Reverses RESEARCH.md Pattern 4's "XFF-first" default. Laptop deployments (Assumption A3) have no proxy, so a localhost process could set `X-Forwarded-For: 127.0.0.1` and bypass the allowlist. Default-deny is the only safe posture; the trust flag is opt-in for proxy-fronted deployments. |
| `writeOllamaError` stays package-private | Only Bearer + IPAllowlist call it. Exported surface stays minimal; whitebox test covers the contract via `auth_internal_test.go`. |
| Allow-all path returns identity factory (`return func(next http.Handler) http.Handler { return next }`) | Avoids constructing a wrapping `http.HandlerFunc` per request when the operator hasn't configured ALLOWED_IPS. Tiny win, but it's the canonical chi pattern for "feature-off" middlewares. |
| 14 tests instead of plan-spec'd 12 | Plan called for ≥5 Bearer + ≥7 IPAllowlist tests; the IPAllowlist trust-matrix decomposed cleanly into 8 cases (the explicit "XFFIgnored_FallsBackToRemoteAddr_WhenDisabled" was important enough to keep separate from "XFFNotTrustedByDefault" since the former tests successful fall-through, the latter tests rejection). |
| Use `context.Background()` in `httptest.NewRequestWithContext` instead of `t.Context()` | Matches the existing pattern in `internal/server/server_test.go` and avoids depending on Go 1.24's `testing.T.Context()` while go.mod is still pinned at `go 1.23`. |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Temporary `//nolint:unused` on `writeOllamaError` in Task 1 commit**
- **Found during:** Task 1 (pre-commit hook ran `golangci-lint`)
- **Issue:** Plan splits Task 1 (Config + writeOllamaError) and Task 2 (Bearer + IPAllowlist) into separate atomic commits. The linter rejected the Task 1 commit with `func writeOllamaError is unused (unused)` because no caller existed yet — Bearer + IPAllowlist were due to land in the next commit.
- **Fix:** Added `//nolint:unused // consumed by Bearer + IPAllowlist middlewares added in Task 2 of this plan.` directive on `writeOllamaError`. Removed the directive in the Task 2 commit once the middlewares actually consume it.
- **Files modified:** `internal/auth/auth.go` (added directive in `7359cec`, removed in `591b1c9`)
- **Verification:** Task 1 commit passed `golangci-lint`; Task 2 commit removed the directive and still passed (callers exist).
- **Committed in:** `7359cec` (Task 1) and `591b1c9` (Task 2, directive removed)

**2. [Rule 3 - Blocking] `httptest.NewRequest` → `httptest.NewRequestWithContext` per project `noctx` lint rule**
- **Found during:** Task 3 (pre-commit `golangci-lint` rejected initial test draft)
- **Issue:** Plan prescribed `httptest.NewRequest(http.MethodGet, "/api/chat", nil)` (13 call sites). Project lint config bans `httptest.NewRequest` via the `noctx` linter, requiring `httptest.NewRequestWithContext` instead.
- **Fix:** `replace_all` all 13 occurrences to `httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)`. Added `"context"` import. Followed the exact pattern already used in `internal/server/server_test.go`.
- **Files modified:** `internal/auth/auth_test.go`
- **Verification:** `golangci-lint run ./internal/auth/...` returned 0 issues; all 14 tests still pass under `go test -race -count=1`.
- **Committed in:** `c390ba9` (Task 3 — single test commit with the fix applied before the commit)

**3. [Rule 1 - Bug] Removed unused `newRequest` helper from initial auth_test.go draft**
- **Found during:** Task 3 (pre-build cleanup)
- **Issue:** Initial draft of `auth_test.go` included a `newRequest(t, remoteAddr, headers)` helper that ended up only being called from one test (which was then refactored to use `httptest.NewRequest` directly), leaving an unused helper that the compiler would have rejected.
- **Fix:** Deleted the unused helper before first build.
- **Verification:** `go test` compiled cleanly.
- **Committed in:** `c390ba9` (Task 3)

**4. Minor — `internal/auth/auth.go` is 58 lines instead of the plan's "under 50 lines" target**
- **Found during:** Task 1 (post-write line count)
- **Issue:** The plan's `<action>` block prescribed a verbatim multi-line doc comment on `TrustXForwardedFor` (3 lines on its own) plus a mandatory package doc comment plus inline rationale. With the prescribed content, 50 lines is unreachable.
- **Fix:** Kept the prescribed content. The `done` clause's "under 50 lines" is a soft estimate; all `acceptance_criteria` items pass and the file is well under any reasonable readability threshold.
- **Impact:** None — this is documentation density, not code bloat.

---

**Total deviations:** 4 auto-fixed (2 blocking lint, 1 unused helper, 1 minor line-count overshoot)
**Impact on plan:** All deviations were mechanical — pre-commit lint enforcement caught two issues that the planner could not have anticipated without running the local lint config, and the line-count target was an estimate. No scope creep, no architectural changes, no plan rewrites needed.

## Issues Encountered

None beyond the deviations above. The TDD flow was clean: each task's RED state was the structural "package/test does not yet exist" rather than a behaviour test, which is appropriate for greenfield middleware files. The behaviour assertions all landed in Task 3 against the Tasks 1+2 implementations — and all 14 passed on first run after the lint fix.

## Threat Flags

| Flag | File | Description |
|------|------|-------------|
| _(none)_ | | No new security-relevant surface beyond what the threat model already documents (T-02-05 through T-02-10). |

The plan's `<threat_model>` already captures every mitigation this implementation needed (constant-time compare, XFF default-deny, IPv4-in-IPv6 stripping, no auth-token logging). Verified `grep -c 'cfg.Logger' internal/auth/bearer.go` returns 0 — bearer does not log rejected tokens (threat T-02-09 mitigation).

## User Setup Required

None — auth + IP-allowlist behaviour is fully configurable via the existing Node-compatible env vars (`AUTH_TOKEN`, `ALLOWED_IPS`) which Plan 03 will wire into `Config.Tokens` and `Config.AllowedPrefixes`. The new `AUTH_TRUST_XFF` env var (Codex H-7 opt-in) is also wired in Plan 03; this plan only delivers the middleware that consumes it.

## Next Phase Readiness

- **Plan 03 (config extensions):** Ready to wire `AUTH_TOKEN`, `ALLOWED_IPS`, `AUTH_TRUST_XFF` env vars into `auth.Config{...}`. Config struct is stable.
- **Plan 06 (server wiring):** Ready to mount `Bearer(cfg)` + `IPAllowlist(cfg)` on a chi sub-router so the existing `/health`, `/api/version`, `/health/agents` exempt-path bypasses are enforced by routing topology (AUTH-03).
- **Phase 03 (OpenAI surface) + Phase 03.1 (Anthropic surface):** Same Config + same middlewares apply — no per-surface auth duplication. The "one place to enforce policy" PROJECT.md core value holds.

## Self-Check: PASSED

Verified all claims before finalising:

- **Files exist:**
  - `internal/auth/auth.go` — FOUND
  - `internal/auth/bearer.go` — FOUND
  - `internal/auth/ipallowlist.go` — FOUND
  - `internal/auth/auth_test.go` — FOUND
  - `internal/auth/auth_internal_test.go` — FOUND

- **Commits exist:**
  - `7359cec` (Task 1) — FOUND in `git log`
  - `591b1c9` (Task 2) — FOUND in `git log`
  - `c390ba9` (Task 3) — FOUND in `git log`

- **Build / test / lint gates:**
  - `go build ./internal/auth/...` exits 0
  - `go test -race -count=1 ./internal/auth/...` exits 0 with all 14 tests passing
  - `golangci-lint run ./internal/auth/...` returns 0 issues

---
*Phase: 02-ollama-end-to-end*
*Plan: 02*
*Completed: 2026-05-24*
