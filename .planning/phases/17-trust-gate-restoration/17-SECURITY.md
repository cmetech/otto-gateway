---
phase: 17
slug: trust-gate-restoration
status: verified
threats_total: 13
threats_open: 0
threats_closed: 13
asvs_level: 1
register_authored_at_plan_time: true
created: 2026-06-11
---

# Phase 17 — Security

> Per-phase security contract: threat register, accepted risks, and audit trail. Phase 17 closed 7 trust-gate items (TRST-04 arch-lint restore via D-17-01 sentinel relocation, REL-POOL-02 goleak deflake via D-17-04 test-scaffolding, and 5 mechanical fmt/lint/dead-code items via D-17-02). Threat register authored at plan time across 3 PLAN files (17-01, 17-02, 17-03); this audit verifies each declared mitigation is present in the implemented code.

---

## Trust Boundaries

| Boundary | Description | Data Crossing |
|----------|-------------|---------------|
| `canonical/errors.go` <-> pool & adapters | Shared `ErrPoolExhausted` sentinel var crosses package boundaries via `errors.Is` identity; tampering with the sentinel value would silently break the HTTP 503 mapping path. | error sentinel pointer |
| Adapter import surface | Removing the `internal/pool` import from anthropic/ollama/openai handlers shrinks adapter blast radius — adapters depend on `canonical` only for this code path. | package-import boundary |
| test scaffolding <-> production `acp.Stream` | REL-POOL-02 test reads `internal/acp` public APIs (`stream.Result`, `stream.Chunks`, `stream.CloseForTest`) — same boundary as the rest of the pool test suite; no new boundary crossings. | test-only |
| `cmd/otto-tray/tray.go` -> filesystem | Support-bundle staging dir + log file written by operator's user-level process; permission tightening (0o750 / 0o600) shrinks the read surface. | stderr/stdout text |
| `internal/pool/pool.go` API surface | Removing unexported `Pool.removeSlot` shrinks the package's method count; was already unexported (lowercase) so no external API change. | internal-only |

---

## Threat Register

| Threat ID | Category | Component | Disposition | Mitigation | Status |
|-----------|----------|-----------|-------------|------------|--------|
| T-17-01-01 | Tampering | `canonical/errors.go` `ErrPoolExhausted` sentinel value | mitigate | New `internal/canonical/errors_test.go` (`canonical_test` blackbox) asserts (1) self-identity via `errors.Is(s, s)`, (2) byte-exact message `pool: all workers busy; retry in 5s`, (3) `errors.Is` wrap-traversal through `fmt.Errorf("...: %w", s)`. Evidence: `internal/canonical/errors_test.go:35-62` (test body); `internal/canonical/errors.go:32` (sentinel decl). Commit: f727b24. | closed |
| T-17-01-02 | Repudiation | Adapter HTTP 503 mapping (errors.Is path) | accept | The errors.Is identity is preserved by Go's variable-assignment semantics: `pool.ErrPoolExhausted = canonical.ErrPoolExhausted` is literally the same `*errorString` value. No behavior change to client-facing 503 responses or audit logs. Evidence: `internal/pool/pool.go:21` (alias). AR-17-01 below. | closed |
| T-17-01-03 | Information Disclosure | New canonical doc comments | accept | Doc-only additions to `internal/canonical/errors.go`; no PII, secrets, or operator data exposed. AR-17-02 below. | closed |
| T-17-01-SC | Tampering | npm/pip/cargo installs (supply chain) | accept | None — pure in-tree Go edits, zero new dependencies introduced by 17-01. AR-17-03 below. | closed |
| T-17-02-01 | Denial of Service | `resultWg` deadlock if a `stream.Result` goroutine never returns | mitigate | The `blockingPromptClient` goroutines always close the stream — via gate (FinalResult) or ctx.Done (ctx.Err). `stream.Result` returns once the stream closes. Test-only scaffolding; any future production change breaking the invariant surfaces as a loud test-timeout fail (Go's default 10-min), preferable to silent-flake. Evidence: `internal/pool/regression_rel_pool_02_test.go:108` (resultWg decl), `:145-153` (Add+Done+drain), `:217` (Wait); test passed 60/60 across three independent 20-iter rounds. Commit: ca258f9. | closed |
| T-17-02-02 | Tampering | Test scaffolding edits weakening REL-POOL-02 production assertions | mitigate | Post-fix assertions (cancelsAfter >= 2; each blockingPromptClient receives Cancel) were NOT modified. Verified via `go test -race -count=20 -v`: both bc0 and bc1 receive Cancels every iteration. The per-instance `idTag`-based sid override (`fake-sess-bc0`/`fake-sess-bc1`) is test-scaffolding-only (deviation Rule 1 — pre-existing degenerate sessionSlots collapse surfaced by the goleak fix). Evidence: `internal/pool/regression_rel_pool_02_test.go` `newBlockingPromptClient("bc0"/"bc1")` call sites; assertion preserved at the test's verification block. Commit: ca258f9. | closed |
| T-17-02-03 | Information Disclosure | Test logs leaking goroutine stack traces | accept | goleak's stack traces contain internal package names only — no PII, secrets, or operator data. Standard test-output exposure. AR-17-04 below. | closed |
| T-17-02-SC | Tampering | npm/pip/cargo installs (supply chain) | accept | None — pure in-tree Go test edits in 17-02, zero new dependencies. AR-17-03 below. | closed |
| T-17-03-01 | Information Disclosure | tray.go support-bundle log file (0o644 -> 0o600) | mitigate | `os.WriteFile` permission tightened to `0o600` so the support-bundle log (which may contain kiro-cli stderr including model output, tool-call args, env-var echoes) is readable only by the owning user. Acceptable per D-17-05 single-user laptop posture documented in `docs/operating.md` "v1 no-auth posture"; no cross-user reader exists. Rationale recorded in commit body. Evidence: `cmd/otto-tray/tray.go:372` reads `os.WriteFile(logPath, []byte(content), 0o600)`. Commit: b78fd09. | closed |
| T-17-03-02 | Tampering | tray.go support-bundle staging dir (0o755 -> 0o750) | mitigate | `os.MkdirAll` permission tightened to `0o750` removing world-write surface on the staging directory. Group-readable retained for Linux dev-box tooling; world-readable removed. Evidence: `cmd/otto-tray/tray.go:367` reads `os.MkdirAll(logDir, 0o750)`. Commit: b78fd09. | closed |
| T-17-03-03 | Denial of Service | Removing `Pool.removeSlot` unmasks an unexpected caller at runtime | mitigate | Repo-wide grep in 17-03 Task 4 Step 1 confirmed zero non-comment callers in production. `grep -n "removeSlot" internal/pool/pool.go` now returns only the historical-context comment at line 277 (`removeSlot path was removed as dead code in Phase 17`). `go build ./...` + `go test -race -count=1 ./internal/pool/...` clean. Evidence: function definition absent from `internal/pool/pool.go`; lone surviving reference is the comment at `:277`. Commit: b78fd09. | closed |
| T-17-03-04 | Repudiation | server.go gofmt edits accidentally rewrite unrelated code | mitigate | 17-03 Task 1 Step 4 includes `git diff --stat` check that flags any unexpectedly large rewrite. SUMMARY 17-03 confirms "10 line diff, whitespace-only — `git diff --stat` confirmed bounded scope (no full-file rewrite)" on `internal/server/server.go` NewWithCommit constructor body (lines 200-209). No functional logic changes. Commit: b78fd09. | closed |
| T-17-03-SC | Tampering | npm/pip/cargo installs (supply chain) | accept | None — pure in-tree Go edits in 17-03, zero new dependencies. AR-17-03 below. | closed |

*Status: open · closed*
*Disposition: mitigate (implementation required) · accept (documented risk) · transfer (third-party)*

---

## Accepted Risks Log

| Risk ID | Threat Ref | Rationale | Accepted By | Date |
|---------|------------|-----------|-------------|------|
| AR-17-01 | T-17-01-02 | The errors.Is identity is preserved by Go's variable-assignment semantics: `pool.ErrPoolExhausted = canonical.ErrPoolExhausted` is the same `*errorString` pointer. Client-facing 503 body (byte-exact `pool: all workers busy; retry in 5s`) and audit-log shape (`errors.Is(err, ErrPoolExhausted) == true`) are unchanged. Risk of accidental drift mitigated by AR-17-01's sibling, T-17-01-01 (sentinel-identity test guards byte-exact text + wrap-traversal). | Phase 17 plan author (17-01-PLAN.md threat_model) | 2026-06-11 |
| AR-17-02 | T-17-01-03 | Doc-comment additions in `internal/canonical/errors.go` describe the TRST-04 rationale and the pool re-export alias. No PII, secrets, env-var values, or operator data are interpolated. Static text only. | Phase 17 plan author (17-01-PLAN.md threat_model) | 2026-06-11 |
| AR-17-03 | T-17-01-SC, T-17-02-SC, T-17-03-SC | Phase 17 added zero third-party Go modules. All edits are pure in-tree Go (sentinel relocation, test scaffolding, mechanical fmt/lint/dead-code). `go.mod` / `go.sum` unchanged across 17-01 + 17-02 + 17-03 commits (f727b24, ca258f9, b78fd09). Package-legitimacy gate is not engaged. | Phase 17 plan author (17-01/02/03-PLAN.md threat_model blocks) | 2026-06-11 |
| AR-17-04 | T-17-02-03 | goleak stack traces in test failure output contain internal package names (e.g., `otto-gateway/internal/acp.(*Stream).Result`) only. No PII, no secrets, no operator credentials. Standard Go test-runner output exposure; consistent with the goleak.VerifyTestMain usage elsewhere in the test suite. | Phase 17 plan author (17-02-PLAN.md threat_model) | 2026-06-11 |

*Accepted risks do not resurface in future audit runs.*

---

## Unregistered Flags

`## Threat Flags` sections from each Phase 17 SUMMARY:

- **17-01-SUMMARY.md** "Threat Surface Scan": no new security-relevant surface introduced; the relocation REDUCES adapter blast radius (adapters drop `internal/pool` import). No `threat_flag:` annotations. Maps cleanly to T-17-01-01..03,SC.
- **17-02-SUMMARY.md** "Threat Flags": one entry — `threat_flag: production-race` on `internal/acp/stream.go` (close/Result race). NOT mitigated in Phase 17 (D-17-05 scope-bounded to test scaffolding). Flagged for v1.10 hardening backlog with recommended fix recorded. **Unregistered** in Phase 17 threat register because the disposition is "defer to v1.10" — not a Phase 17 implementation gap; test-side workaround (drain-Chunks-then-Result) inherits the chan-close write barrier as synchronization edge.
- **17-03-SUMMARY.md** "Threat Surface Scan": no new security-relevant surface; G301/G306 tightening REDUCES filesystem read surface, dead-code removal REDUCES Pool API surface. No `threat_flag:` annotations. Maps cleanly to T-17-03-01..04,SC.

**Net unregistered flags: 1** — `production-race` on `acp.Stream` close/Result ordering, deferred to v1.10. Recommendation captured in 17-02-SUMMARY.md (move `close(s.done)` after `s.mu.Unlock()` OR copy `*s.result` into a local under s.mu in `Result()`). Tracked as informational; does NOT block Phase 17 sign-off because (a) it is a pre-existing production race not introduced by Phase 17 (Phase 17 only made it observable to the race detector by fixing the goleak that previously masked it), (b) D-17-05 explicitly scoped Phase 17 away from production-code changes outside the sentinel relocation, and (c) the test-side workaround is in place and stable (60/60 PASS).

---

## Security Audit Trail

| Audit Date | Threats Total | Closed | Open | Run By |
|------------|---------------|--------|------|--------|
| 2026-06-11 | 13 | 13 | 0 | gsd-secure-phase (Claude Opus 4.7) |

---

## Verification Methodology

For each `mitigate` threat, the audit ran grep checks against the implementation files cited in PLAN `<threat_model>` blocks:

| Threat | Grep / Verification | File | Lines Found |
|--------|---------------------|------|-------------|
| T-17-01-01 | `ErrPoolExhausted = errors.New("pool: all workers busy; retry in 5s")` | `internal/canonical/errors.go` | 32 |
| T-17-01-01 | `TestErrPoolExhausted_SentinelIdentity` (self-identity + byte-exact text + wrap-traversal) | `internal/canonical/errors_test.go` | 35-62 |
| T-17-01-01 | `var ErrPoolExhausted = canonical.ErrPoolExhausted` (alias preserves errors.Is identity) | `internal/pool/pool.go` | 21 |
| T-17-01-01 | `errors.Is(err, canonical.ErrPoolExhausted)` (8 expected sites) | `internal/adapter/{anthropic,ollama,openai}/handlers.go` | anthropic:187,370; ollama:159,244,472,521; openai:157,295 |
| T-17-01-01 | No `"otto-gateway/internal/pool"` import in any of the three handlers.go files (adapter-over-canonical boundary restored) | `internal/adapter/{anthropic,ollama,openai}/handlers.go` | confirmed absent; only `internal/canonical` imported (anthropic:10, ollama:13, openai:11) |
| T-17-02-01 | `resultWg` (decl + Add + Done + Wait) | `internal/pool/regression_rel_pool_02_test.go` | 108 (decl), 145 (Add), 147 (Done), 148 (Chunks drain), 153 (Result), 217 (Wait) |
| T-17-02-02 | Post-fix assertions preserved; per-instance unique sid via `newBlockingPromptClient(idTag)` | `internal/pool/regression_rel_pool_02_test.go` | newBlockingPromptClient signature change + bc0/bc1 callsites; assertion block intact |
| T-17-03-01 | `os.WriteFile(logPath, []byte(content), 0o600)` | `cmd/otto-tray/tray.go` | 372 |
| T-17-03-02 | `os.MkdirAll(logDir, 0o750)` | `cmd/otto-tray/tray.go` | 367 |
| T-17-03-03 | `func (p *Pool) removeSlot` absent; surviving reference is comment-only historical context | `internal/pool/pool.go` | only :277 (`removeSlot path was removed as dead code in Phase 17`) |
| T-17-03-04 | server.go gofmt diff bounded to whitespace in NewWithCommit constructor (per SUMMARY git-diff-stat verification) | `internal/server/server.go` | NewWithCommit body ~200-209 |

For each `accept` threat, the audit confirmed:
- **T-17-01-02:** `internal/pool/pool.go:21` is a single-line var assignment `ErrPoolExhausted = canonical.ErrPoolExhausted` — Go semantics guarantee pointer-identical errors.Is matching.
- **T-17-01-03:** `internal/canonical/errors.go` doc comments (lines 19-31) contain only TRST-04 rationale text; no operator data.
- **T-17-02-03:** goleak output references `internal/acp.(*Stream).Result` package paths only.
- **T-17-01-SC, T-17-02-SC, T-17-03-SC:** no `go.mod` / `go.sum` changes across f727b24, ca258f9, b78fd09.

---

## Sign-Off

- [x] All threats have a disposition (mitigate / accept / transfer)
- [x] Accepted risks documented in Accepted Risks Log (AR-17-01..04)
- [x] `threats_open: 0` confirmed
- [x] `status: verified` set in frontmatter
- [x] Register origin: authored at plan time across 17-01-PLAN.md, 17-02-PLAN.md, 17-03-PLAN.md `<threat_model>` blocks (no mid-flight additions)
- [x] Unregistered flag (acp.Stream production-race) recorded with deferral rationale; tracked for v1.10

**Approval:** verified 2026-06-11

---

## Commit Provenance

Phase 17 fixes verified across these commits:

| Commit | Plan | Threats Mitigated |
|--------|------|-------------------|
| f727b24 | 17-01 (TRST-04 + REL-POOL-01 sentinel relocation) | T-17-01-01 (mitigate), T-17-01-02/03/SC (accept) |
| ca258f9 | 17-02 (REL-POOL-02 goleak deflake) | T-17-02-01/02 (mitigate), T-17-02-03/SC (accept) |
| b78fd09 | 17-03 (mechanical batch — gofmt + gofumpt + gosec G301/G306 + dead-code) | T-17-03-01/02/03/04 (mitigate), T-17-03-SC (accept) |
