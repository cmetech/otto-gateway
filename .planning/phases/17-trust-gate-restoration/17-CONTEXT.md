---
phase: 17-trust-gate-restoration
discussed: 2026-06-11
status: ready_for_planning
---

# Phase 17 — Trust-Gate Restoration (CONTEXT)

## Phase Goal

Restore `make ci` to clean exit-0 end-to-end. The v1.9 milestone shipped its
artifacts (audit passed 27/27 requirements, 8/8 cross-phase seams WIRED, all
SECURITY.md files complete) but `make ci` at milestone close surfaced six
trust-gate items that the per-phase verifiers (which ran `go test -race ./...`
only) had missed.

Phase 17 closes those six items so the release tag `v1.9.1` can ship from a
build-green baseline.

## Driver

`make ci` run at HEAD `0fe636d` (v1.9 milestone close) reported:

| Gate | Item | Origin |
|------|------|--------|
| fmt-check (gofmt) | `internal/server/server.go` struct field column alignment (IN-03 Info deferred in 16-REVIEW.md) | Phase 15 |
| fmt-check (gofumpt) | `internal/pool/regression_rel_pool_0{1,2}_test.go` leading blank lines | Phase 14 test scaffolding |
| lint (gosec G301) | `cmd/otto-tray/tray.go:367` `MkdirAll(0o755)` → wants `≤0o750` | Phase 16-04 (T-7 area) |
| lint (gosec G306) | `cmd/otto-tray/tray.go:372` `WriteFile(0o644)` → wants `≤0o600` | Phase 16-04 (T-7 area) |
| lint (unused) | `internal/pool/pool.go (*Pool).removeSlot` is dead code | Phase 5 D-03 leftover, pre-v1.9 debt |
| arch-lint | `adapter/{anthropic,ollama,openai}/handlers.go` imports `internal/pool` for `pool.ErrPoolExhausted` (REL-POOL-01 mapping) | Phase 15-01 (commit `4fd879b`) |
| test-race (goleak) | `TestRegression_REL_POOL_02_CtrlCOrphansChildren` leaks `stream.Result()` goroutines (~1/8 fail rate) | Phase 15-01 test scaffolding |

(`govulncheck` and `examples` gates were not reached because earlier gates
failed; they're expected to be green based on Phase 12 closure but Phase 17
should run the full sequence at close.)

## Locked Decisions

### D-17-01: Move `ErrPoolExhausted` sentinel to `canonical` package

The arch-lint violation is the load-bearing item. TRST-04 (the
adapter-over-canonical boundary established in Phase 1) is the project's
oldest architectural invariant — Phase 15-01 broke it for convenience.

**Fix:** Define `canonical.ErrPoolExhausted` as the authoritative sentinel.
Pool re-exports it as a package-level var alias so existing callers that
imported `pool.ErrPoolExhausted` still compile.

```go
// internal/canonical/errors.go (new file or existing errors.go)
var ErrPoolExhausted = errors.New("pool exhausted")

// internal/pool/pool.go
// ErrPoolExhausted is re-exported from canonical for backward compatibility.
// New code should reference canonical.ErrPoolExhausted directly.
var ErrPoolExhausted = canonical.ErrPoolExhausted

// internal/adapter/{anthropic,ollama,openai}/handlers.go
if errors.Is(err, canonical.ErrPoolExhausted) {
    // emit typed 503 with Retry-After: 5
}
```

**Why this over alternatives:**
- Cleanest fix; preserves TRST-04 boundary
- Adapters already import canonical — no new boundary crossings
- Re-export means pool's own internal callers (and any test code) don't break
- Per-package allowlist exception (Option C) would weaken TRST-04 for the
  duration of the project; not acceptable for the project's oldest invariant

**Touches:** ~10 files
- `internal/canonical/errors.go` (new sentinel; check if file already exists for additions)
- `internal/pool/pool.go` (re-export)
- `internal/adapter/anthropic/handlers.go` (2 errors.Is sites)
- `internal/adapter/anthropic/sse.go` (1 comment ref)
- `internal/adapter/ollama/handlers.go` (4 errors.Is sites)
- `internal/adapter/ollama/ndjson.go` (2 comment refs)
- `internal/adapter/openai/handlers.go` (1 errors.Is site, possibly more)

**Verification:** arch-lint passes; all existing REL-POOL-01 regression tests
green; `errors.Is(err, canonical.ErrPoolExhausted)` returns true for errors
emitted by `pool.AcquireTimeout` exhaustion path.

### D-17-02: Three atomic plans

| Plan | Scope | Why atomic |
|------|-------|------------|
| 17-01 | arch-lint relocation (D-17-01) | Non-trivial; multi-file refactor with cross-package impact |
| 17-02 | REL-POOL-02 goleak flake fix | Test hygiene; may need 2–3 iterations to fully close. Worth isolating so the iterations don't bleed into other fixes. |
| 17-03 | Mechanical batch — gofmt + gofumpt + gosec G301/G306 + dead-code removal | Five mechanical fixes in one commit; each fix is one-to-three lines; reverting the batch as a unit is acceptable. |

**Plan wave grouping:** All three plans are independent (no file overlap
between arch-lint files, test scaffolding, and tray.go/server.go/regression
tests). All can run in **Wave 1 parallel** if execute-phase isn't auto-degraded.
If auto-degraded to sequential (per #683 — likely, since `origin/HEAD` still
diverges), order: 17-03 → 17-01 → 17-02 (cheapest cleanups first; arch-lint
mid; flake fix last as it may need iteration).

### D-17-03: Release tag strategy — keep v1.9 local, ship v1.9.1 after Phase 17

- Local `v1.9` tag at HEAD `0fe636d` stays as the "milestone artifacts
  complete" marker. **Not pushed.**
- After Phase 17 ships with `make ci` clean, tag `v1.9.1` at that HEAD with
  message: "v1.9.1 — Trust-Gate Restoration: make ci clean baseline restored".
- Push `v1.9.1` to GitHub (`origin` push URL = `github.com:cmetech/otto-gateway.git`).
- Create GitHub release `v1.9.1` via `gh release create v1.9.1`. Release body
  references the v1.9 milestone audit + the Phase 17 trust-gate closeout.
- v1.9 tag stays local; not pushed, not referenced externally. Acts as a
  rollback anchor if v1.9.1 needs to revert to milestone-close state.

## Cross-Plan Wires (D-style decisions)

### D-17-04: REL-POOL-02 flake root cause

The test scaffolding spawns `go func() { _, _ = stream.Result() }()` goroutines
that aren't tracked by the outer `wg`. When `goleak.VerifyNone(t)` runs at the
test's `defer`, these goroutines are sometimes still in
`stream.Result()` waiting for the stream's done channel.

Initial fix attempt (resultWg added) reduced but didn't eliminate the flake
(~1/8 → ~1/10). The remaining race is likely between `p.Close()`'s Cancel
propagation and the blockingPromptClient's gate-or-ctx.Done select firing
the stream's `CloseForTest(nil, ctx.Err())`. Plan 17-02 should:

1. Investigate whether closing gates BEFORE `p.Close()` makes the race
   deterministic (streams close cleanly via the gate-path instead of racing
   with Cancel-via-ctx.Done).
2. If gate-before-Close still flakes, add explicit
   `<-stream.Done()` waits in the resultWg goroutines so they don't return
   until the underlying acp.Stream has fully transitioned to closed state.
3. Run 20+ iterations to confirm fix sticks. Pre-existing fix attempts that
   passed 5/5 then failed 1/8 are not sufficient evidence.

### D-17-05: Phase 17 scope is mechanical-and-bounded

Strictly scoped to the 6 items above. **Out of scope:**
- The 5 Info-level findings from 16-REVIEW.md (escapeApplescript, tooltipForState
  duplication, server.go indentation drift on lines 206-208 [already covered
  by D-17 fmt fix though], forceCloseCh contract doc, tailLines O(n²) prepend).
  Those remain v1.10 backlog.
- The 12 Low-severity reliability findings deferred from v1.9.
- Any new feature work.
- Any refactor or rename outside the arch-lint fix's direct scope.

If the arch-lint fix surfaces a downstream issue (e.g., circular import that
forces a deeper canonical/pool boundary review), surface as a Phase 17 risk
and ask before expanding scope.

## Verification (phase close criteria)

1. `make ci` exits 0 end-to-end (fmt-check + vet + build + lint + test-race +
   arch-lint + examples + govulncheck). Capture and commit the green output.
2. `TestRegression_REL_POOL_02_CtrlCOrphansChildren` passes 20/20 iterations
   under `-race` with goleak verification.
3. `errors.Is(actualErr, canonical.ErrPoolExhausted)` returns true for the
   REL-POOL-01 path (exhaustion under AcquireTimeout). Existing
   `TestRegression_REL_POOL_01_*` tests stay green without modification beyond
   updating the import path.
4. `go-arch-lint check --project-path .` reports `total notices: 0`.
5. Manual: `git diff main...HEAD -- ':!*_test.go' ':!.planning/' ':!docs/'`
   should be small — only `canonical/errors.go` additions, `pool/pool.go`
   re-export, `adapter/*` import-path changes, `tray.go` permission tightening,
   `pool/pool.go` removeSlot deletion, `server.go` formatting.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| `canonical.ErrPoolExhausted` relocation breaks an unexpected consumer (e.g., a test that asserts the exact `pool` package import) | Low | Medium — re-introduces lint debt | Re-export `pool.ErrPoolExhausted` as alias so existing consumers keep compiling. Run full test suite after relocation. |
| REL-POOL-02 flake fix doesn't fully close after 2 iterations | Medium | High — phase can't ship | Plan 17-02 explicit 3-iteration budget; if still flaky after 3 attempts, escalate: either redesign the test scaffolding or mark the test with explicit goroutine-suppression annotation + documented known-flake follow-up. |
| arch-lint fix triggers a downstream cascade (cyclic import, etc.) | Low | High — multi-day refactor | Plan 17-01 first task: spike the relocation in a worktree and confirm `go build ./...` succeeds before committing the full multi-file change. |
| Phase 16-04 tray gosec fixes break Windows file-read semantics (0o600 too restrictive for cross-user log readers) | Very Low | Low — single-user laptop deployment per project posture | Gosec tightening is for the support-bundle's per-user staging dir. Single-user posture means no cross-user reader exists. Document in commit message. |

## Next Steps

`/gsd-plan-phase 17` — split the three plans per D-17-02 with task-level
detail. Plan 17-01 needs the multi-file refactor sequenced atomically
(canonical add → pool re-export → adapter sites in one commit ideally, or
canonical-add as RED-equivalent prep commit then atomic flip on the
adapter-side import change).
