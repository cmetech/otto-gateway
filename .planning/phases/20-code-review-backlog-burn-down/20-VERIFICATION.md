---
phase: 20-code-review-backlog-burn-down
verified: 2026-06-12T00:00:00Z
status: passed
score: 7/7 must-haves verified
overrides_applied: 0
---

# Phase 20: Code-review backlog burn-down — Verification Report

**Phase Goal:** Close 6 Info-level findings deferred from Phase 16/17 code reviews as a single mechanical-refactor batch (QUAL-01..06). Refactor-only with one narrow behavioral expansion (QUAL-01 AppleScript escape set).

**Verified:** 2026-06-12
**Status:** passed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | escapeApplescript translates raw `\n`/`\r`/`\t` into the two-char AS escape sequences and strips other C0/DEL bytes (QUAL-01) | VERIFIED | `cmd/otto-tray/uihelpers_darwin.go:141-162` — switch covers `'"'`/`'\\'` (line 146), `'\n'` (148), `'\r'` (150), `'\t'` (152), `c < 0x20 \|\| c == 0x7F` strip (154). `go test ./cmd/otto-tray/... -run TestEscapeApplescript -race -count=1` passes. Table covers empty, plain ASCII, `"`, `\`, `\n`/`\r`/`\t`, raw 0x00/0x1F/0x7F, high-byte passthrough, mixed. |
| 2 | tooltipForState exists exactly once in cmd/otto-tray, in a build-tagged shared file (QUAL-02) | VERIFIED | `grep -rn "func tooltipForState" cmd/otto-tray/` returns exactly 1 — at `cmd/otto-tray/tooltip.go:8` with `//go:build darwin \|\| windows`. Prior duplicates in `uihelpers_darwin.go` and `uihelpers_windows.go` are gone. |
| 3 | Server.Run() called directly with a nil forceCloseCh shuts down cleanly under ctx.Done() (QUAL-03) | VERIFIED | `internal/server/server.go:182-193` documents the new contract; `NewWithCommit` (line 214) and `NewFromConfig` (line 283) intentionally leave `forceCloseCh` nil; allocation moved to `RunUntilSignal` (line 537) before spawning `Run` goroutine. `TestServer_Run_DirectShutdown` and `TestServer_NewWithCommit_NilForceClose` in `internal/server/run_direct_test.go` pass under `-race -count=3`. |
| 4 | tailLines no longer prepends with append([]string{t}, kept...) — uses collect-then-reverse (QUAL-04) | VERIFIED | `cmd/otto-tray/tray.go:412-434` — walks back-to-front, appends most-recent-first, breaks at `n`, then swaps in place (`kept[i], kept[j] = kept[j], kept[i]` at line 431). `grep "append(\[\]string{t}, kept\.\.\.)" cmd/otto-tray/tray.go` returns 0 live references (one historical mention in doc comment at line 408 documenting the prior pattern). |
| 5 | internal/pool/regression_rel_pool_02_test.go has no sessions/sessionsMu declarations (QUAL-05) | VERIFIED | `grep -nE "sessions\|sessionsMu" internal/pool/regression_rel_pool_02_test.go` returns only the prose "sessions" appearing inside English-language comments (lines 67, 133, 150, 154, 162) — no `sessions :=`, no `sessionsMu sync.Mutex`, no `sessionsMu.Lock()`, no `sessions = append(...)`. Net diff at commit `2216074` is `-5` lines (5 deletions, 0 additions). |
| 6 | internal/pool/respawn_ctx_cancel_test.go line ~119 no longer references the removed removeSlot symbol as a live mechanism (QUAL-06) | VERIFIED | `internal/pool/respawn_ctx_cancel_test.go:117-122` — comment rewritten per Option A (describe with parenthetical historical note). The single remaining `removeSlot` reference at line 121 is inside the phrase "Phase 17-03 removed the unconditional removeSlot call that previously dropped the slot" — clearly historical, not a live mechanism reference. Acceptable per Phase 17 dead-code stale-comment policy. |
| 7 | make ci exits 0 after all six commits — race, vet, staticcheck, gosec, arch-lint all green | VERIFIED | `PATH="$(go env GOPATH)/bin:$PATH" make ci` exits 0. Output confirms: race tests green; `go vet`/`staticcheck` green; `gosec` clean; `go-arch-lint check` "OK - No warnings found"; `govulncheck` "No vulnerabilities found". |

**Score:** 7/7 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `cmd/otto-tray/escapeApplescript_darwin_test.go` | Table-driven `TestEscapeApplescript` covering D-20-01 behavior | VERIFIED | 50 lines added; `//go:build darwin` on line 1; `package main`; `TestEscapeApplescript` covers 12 table cases including empty/plain/quote/backslash/\\n/\\r/\\t/strip-nul/strip-1f/strip-del/high-byte/mixed. Test passes under `-race`. |
| `cmd/otto-tray/tooltip.go` | Single shared `tooltipForState` under `//go:build darwin \|\| windows` (D-20-03) | VERIFIED | 14 lines; `//go:build darwin \|\| windows`; single `tooltipForState(state State, detail string) string` function; doc comment preserved. |
| `internal/server/run_direct_test.go` | Regression test asserting nil forceCloseCh shutdown is clean (D-20-04 guard rail) | VERIFIED | 73 lines; package `server` (internal — required for `s.forceCloseCh` sentinel access); two test functions: `TestServer_Run_DirectShutdown` (asserts nil channel + clean shutdown after ctx cancel) and `TestServer_NewWithCommit_NilForceClose` (constructor-divergence guard). |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|----|--------|---------|
| uihelpers_darwin.go (notifyImpl/infoDialog/confirmDialog) | escapeApplescript (expanded escape set) | direct function call inside fmt.Sprintf | WIRED | `escapeApplescript(...)` called at lines 80, 96, 112-114 (notifyImpl, infoDialog, confirmDialog). All three sites continue to flow operator-derived strings through the function. |
| internal/server/server.go RunUntilSignal | Server.forceCloseCh (allocated in RunUntilSignal, nil for direct Run callers) | `s.forceCloseCh = make(chan struct{})` before spawning Run | WIRED | Allocation at `server.go:537`; constructors (lines 214, 283) leave field nil with explicit comment. `close(s.forceCloseCh)` at line 562 in force-close path. Direct Run callers exercise nil-channel select-never idiom (server.go:483 select arm unchanged). |
| uihelpers_darwin.go + uihelpers_windows.go | cmd/otto-tray/tooltip.go (shared definition) | build-tag combination `//go:build darwin \|\| windows` | WIRED | `grep -rn "func tooltipForState" cmd/otto-tray/` returns exactly 1 result at `tooltip.go:8`. No duplicates remain in `uihelpers_darwin.go` or `uihelpers_windows.go`. |

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|--------------------|----|
| escapeApplescript | byte stream `s` | Operator-controlled `body`/`title`/button labels via notifyImpl/infoDialog/confirmDialog callers | Yes — raw strings transformed byte-by-byte into AS-safe escaped form | FLOWING |
| tooltipForState | `state State, detail string` | Caller-supplied FSM state — used as tray tooltip | Yes — `fmt.Sprintf` composes "OTTO Gateway · {state} ({detail})" | FLOWING |
| forceCloseCh | `chan struct{}` | Allocated in `RunUntilSignal` before spawning Run; nil for direct callers | Yes — `close()` signal propagates through select to drive `srv.Close()` in WR-08 fix path | FLOWING |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| QUAL-01 escapeApplescript table-driven test | `go test ./cmd/otto-tray/... -run TestEscapeApplescript -race -count=1` | `ok otto-gateway/cmd/otto-tray 1.257s` | PASS |
| QUAL-03 Run-direct shutdown regression | `go test ./internal/server/... -run TestServer_Run_DirectShutdown -race -count=3` | `ok otto-gateway/internal/server 1.507s` | PASS |
| Full CI gate | `PATH="$(go env GOPATH)/bin:$PATH" make ci` | exit code 0; race + vet + staticcheck + gosec + arch-lint + govulncheck all green | PASS |
| Six atomic commits exist | `git log --oneline -8` | Six commits `aa5ebd8`, `bf617ed`, `3dabe7c`, `57c1314`, `2216074`, `834016f` — subjects match `^refactor\(20\): close QUAL-0[1-6]` | PASS |
| Per-commit file scope respected (D-20-09) | `git show --stat <hash>` for each commit | QUAL-01: 2 files, QUAL-02: 3 files, QUAL-03: 2 files, QUAL-04: 1 file, QUAL-05: 1 file, QUAL-06: 1 file — matches PLAN expectations | PASS |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| QUAL-01 | 20-01-PLAN.md | escapeApplescript escapes newlines + control chars in addition to `"` and `\` | SATISFIED | Implementation at `cmd/otto-tray/uihelpers_darwin.go:141-162`; commit `aa5ebd8`; test `TestEscapeApplescript` passes. |
| QUAL-02 | 20-01-PLAN.md | tooltipForState no longer duplicated across uihelpers_{windows,darwin}.go | SATISFIED | Shared definition at `cmd/otto-tray/tooltip.go:8`; duplicates deleted; commit `bf617ed`. |
| QUAL-03 | 20-01-PLAN.md | forceCloseCh channel contract visible — moved allocation into RunUntilSignal | SATISFIED | Allocation relocation at `server.go:537`; constructor allocations removed; field doc comment at lines 182-193 documents the contract; regression test in `run_direct_test.go`; commit `3dabe7c`. |
| QUAL-04 | 20-01-PLAN.md | tailLines switches from O(n²) prepend to collect-then-reverse | SATISFIED | New algorithm at `cmd/otto-tray/tray.go:412-434`; commit `57c1314`. |
| QUAL-05 | 20-01-PLAN.md | Dead sessions/sessionsMu variables removed from REL-POOL-02 test | SATISFIED | All five lines deleted (net `-5` diff); no remaining references; commit `2216074`. |
| QUAL-06 | 20-01-PLAN.md | Stale removeSlot comment updated to reflect Phase 17-03 removal | SATISFIED | Comment at `respawn_ctx_cancel_test.go:117-122` rewritten per Option A (describe with parenthetical historical note); commit `834016f`. |

All six required IDs from PLAN frontmatter `requirements: [QUAL-01..06]` are cross-referenced in REQUIREMENTS.md lines 35-40 and confirmed satisfied in code. No orphaned IDs.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| (none in phase-modified files) | — | — | — | — |

No `TODO`/`FIXME`/`XXX`/`HACK`/`PLACEHOLDER` markers introduced. The historical `removeSlot` reference at `respawn_ctx_cancel_test.go:121` is documented as removed-historical (Option A per D-20-08 / Phase 17-03 policy) — not a live anti-pattern.

### Advisory Items (from 20-REVIEW.md — surfaced, NOT blocking)

These advisory findings from the code review are NOT goal failures for Phase 20. They belong in a future code-review backlog phase.

| ID | Severity | Topic | Disposition |
|----|----------|-------|-------------|
| WR-01 | Warning | QUAL-03 receiver-field mutation lacks sync.Once / single-call guard; test does not exercise RunUntilSignal-second-signal path | Advisory — D-20-04 explicitly defines the relocation as the closure spec for QUAL-03; the goal was met. Hardening (`sync.Once` or pass-by-arg) belongs in a follow-up phase. |
| WR-02 | Warning | TestServer_Run_DirectShutdown uses 50ms time.Sleep instead of readiness signal | Advisory — Run's outer select fires on ctx.Done() regardless of bind state, so the test still asserts the load-bearing invariant. Test-sync refactor is follow-up. |
| WR-03 | Warning | regression_rel_pool_02_test.go uses 100ms sleep as readiness signal | Advisory — predates Phase 20; sleep is in unchanged code outside QUAL-05's scope. Follow-up. |
| IN-01..05 | Info | tooltipForState dedup half-done (applyState open-codes same format); test missing CRLF + boundary cases; range-slice idiom; close-then-check dead select; no tooltip_test.go | Advisory — incremental polish; not in PLAN scope. |

### Post-verification Follow-up (Orchestrator)

**REQUIREMENTS.md traceability:** The PLAN success criterion (line 468) and `<verification>` step 7 call for QUAL-01..QUAL-06 rows at `REQUIREMENTS.md:74-79` to be flipped from `Open` to `Closed` in a SUMMARY-time bookkeeping commit (NOT in the per-QUAL commits — that is locked by D-20-09). At verification time these rows still read `Open`. Per Phase 19 precedent (commit `014ce3e docs(19): close REL-ACP-01 in requirements traceability table` landed AFTER verification), this bookkeeping is conventionally an orchestrator-managed follow-up after VERIFICATION passes. Phase goal achievement (the code change) is independent — the traceability flip is a documentation artifact that does not affect goal-backward verification.

**Recommended follow-up commits before phase close:**
1. Flip REQUIREMENTS.md QUAL-01..06 rows to `Closed`.
2. Tick `[ ]` → `[x]` for the six QUAL items at REQUIREMENTS.md lines 35-40.
3. Update STATE.md (stopped_at, last_activity) to reflect phase completion.
4. Tick `[ ]` → `[x]` for the Phase 20 entry at ROADMAP.md line 110.

### Human Verification Required

None. All seven must-haves verified via direct code inspection and automated CI gates. There is no UI/UX behavior change requiring visual confirmation — QUAL-01 is the one intentional behavior change and is fully exercised by `TestEscapeApplescript` table cases.

### Gaps Summary

No goal-blocking gaps. The phase delivered exactly what D-20-01..09 + PLAN `<success_criteria>` specified:

- Six atomic commits (subjects match `refactor(20): close QUAL-0[1-6] — ...`) — one per QUAL.
- Per-commit file scope respected (1-3 files each, never crossing QUAL boundaries).
- `make ci` exits 0 (race + vet + staticcheck + gosec + arch-lint + govulncheck all green).
- `Server.Run` signature unchanged.
- `tooltipForState` defined exactly once.
- Behavior preserved except for the one intentional QUAL-01 escape-set expansion.
- Regression guard (`TestServer_Run_DirectShutdown`) in place under `-race`.

The REQUIREMENTS.md traceability flip is the standard post-verification bookkeeping handled by the orchestrator per Phase 19 precedent, and does not gate goal achievement.

---

_Verified: 2026-06-12_
_Verifier: Claude (gsd-verifier)_
