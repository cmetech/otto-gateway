# Phase 14: Verify Reliability Findings - Context

**Gathered:** 2026-06-11
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 14 is a **read-only audit** of the 23 Critical/High/Medium findings in `docs/reviews/2026-06-11-reliability-review.md` against current `main` source. It produces a verification ledger that gates which REL-* requirements flow into Phase 15 (Critical+High fixes) and Phase 16 (Medium fixes). The phase delivers evidence — not fixes — so Phase 15/16 plan only against failure paths that still exist.

**In scope:** Re-confirm every Critical/High/Medium finding's file:line citation against current source, characterize the failure path's current state, and write a per-finding evidence file plus a single master ledger.

**Out of scope:** Any production source edit (read-only-implementation rule, same posture as v1.8 Phase 13). Lows (12) — already deferred to v1.10. New findings not in the review.

**Hard gate downstream:** the ledger's `status` column drives Phase 15/16 task generation. A `confirmed` row enters its target phase's scope; a `false-positive` row drops its REL-* from the target phase before that phase plans tasks; a `needs-investigation` row must be resolved (flipped to confirmed or false-positive) before Phase 15/16 plan can start.

</domain>

<decisions>
## Implementation Decisions

### Verification rigor — mixed by severity

- **D-01:** Critical (1) + High (8) findings → **failing-test reproducer required**. Each ships as a Go test in the appropriate package marked with `t.Skip("REL-<ID> (<finding>): regression test — unskip in Phase 15 fix commit")`. The test body is the actual reproducer. Phase 15's fix commit removes the `t.Skip()` and proves the fix by flipping red→green in the same PR diff.
- **D-02:** Medium (14) findings → **code-walk only**. Each gets a per-finding evidence file with: re-confirmed file:line cite, a 2–4 sentence walk through the failure path in current source, and a "still exists / mitigated by X / cite gone" verdict. No new test required.
- **D-03:** **Manual-reproducer fallback for findings that can't run in Go-test land.** T-3 (macOS notification no-op needs a real macOS GUI), P-6 (Windows pgid kill is a Windows-only failure mode), T-2 (Windows PowerShell `exit 1`), and T-6 (Windows PowerShell stdout pollution) can't fail in `go test` on a Linux CI runner. For these 4, the `t.Skip` test acts as a structured placeholder (one Go test per REL-* ID, skipped with a pointer to the script), AND a runnable manual reproducer ships under `tests/reliability/manual/REL-<ID>-repro.{sh,ps1,go}`. Phase 15 verification = operator re-runs the script after the fix and observes expected non-failure.

### Parallelization — 4 plans by subsystem

- **D-04:** Phase 14 splits into **4 parallel plans**, each running in its own git worktree (mirrors v1.8 Phase 13's 6-worktree shape via `gsd-nyquist-auditor`). Plan ownership:
  - **Plan 14-01: Pool/ACP (6 findings)** — P-1, P-2, P-3, P-4, P-5, P-6 → REL-POOL-01..06
  - **Plan 14-02: HTTP (5 findings)** — H-1, H-2, H-3, H-4, H-5 → REL-HTTP-01..05
  - **Plan 14-03: Tray (7 findings)** — T-1, T-2, T-3, T-4, T-5, T-6, T-7 → REL-TRAY-01..07
  - **Plan 14-04: Config / Hooks / Observability (5 findings)** — G-1, C-1, C-2, C-3, O-1 → REL-HOOKS-01, REL-CFG-01..04
- **D-05:** Each plan writes a **ledger fragment** (`14-LEDGER-FRAGMENT-{01..04}.md`) plus its share of per-finding evidence files. The orchestrator (executor closing the phase) merges the 4 fragments into `14-VERIFICATION-LEDGER.md`. Plans do not need to coordinate at runtime — fragment files have no overlap.
- **D-06:** Plans use the standard `gsd-executor` agent (not `gsd-nyquist-auditor`, which is v1.8-specific). No specialized auditor agent is needed — this is straightforward read-and-write work.

### Ledger format — master table + per-finding evidence files

- **D-07:** **Master ledger** at `.planning/phases/14-verify-reliability-findings/14-VERIFICATION-LEDGER.md`. Single markdown table, one row per finding, with these columns (in order):
  1. `Finding ID` — P-1, P-2, ..., O-1 (matches review document)
  2. `Sev` — C / H / M (matches review document)
  3. `REL-* ID` — the requirement ID this finding maps to (from REQUIREMENTS.md traceability table)
  4. `Status` — `confirmed` / `false-positive` / `needs-investigation`
  5. `File:line` — current-source citation (may differ from review's cite if code moved since)
  6. `Evidence` — relative path to per-finding file (e.g. `14-FINDING-P-1.md`) OR `—` for false-positive with the verdict inline in a 7th column
  7. `Target phase` — `15` / `16` / `drop` (false-positives) / `v1.10` (Lows out of scope, included only for completeness)
- **D-08:** **Per-finding evidence files** at `.planning/phases/14-verify-reliability-findings/14-FINDING-<ID>.md` (e.g. `14-FINDING-P-1.md`). One file per Critical/High/Medium finding (23 total). Each contains:
  - Frontmatter: `finding`, `severity`, `rel_id`, `status`, `target_phase`, `verified_at`
  - Section: "Review citation" — the original file:line + failure scenario from the review (verbatim or quoted)
  - Section: "Current-source check" — fresh file:line citation; what the code does today; whether the failure path still exists
  - Section: "Evidence" — for C+H: pointer to the `t.Skip`'d regression test (package + test name); for M: 2–4 sentence code-walk; for the 4 manual-reproducer cases: pointer to `tests/reliability/manual/REL-<ID>-repro.{sh,ps1,go}`
  - Section: "Verdict" — `confirmed` (failure path intact) / `false-positive` (cite gone OR new guard makes trigger impossible) / `needs-investigation` (ambiguous; what would flip it)

### False-positive bar — strict + needs-investigation escape hatch

- **D-09:** **`false-positive` requires** one of: (a) the cited file:line literally no longer exists in current source, or (b) a concrete guard has been added since the review (citable at file:line) that provably prevents the failure trigger. Speculative or partial mitigations are NOT enough.
- **D-10:** **`needs-investigation`** is the escape hatch for ambiguity. Each `needs-investigation` row MUST carry a "what would flip this" note in its evidence file naming the specific condition (e.g., "trigger requires laptop sleep > 30 min — needs operator-deferred reproducer"). Each MUST also propose a target follow-up: either (i) flip to confirmed/false-positive after specific runtime experiment, or (ii) defer to a named future phase. **The Phase 14 close gate is `needs_investigation_count == 0`** — all 23 findings resolve to confirmed or false-positive before Phase 15/16 plan can run.
- **D-11:** **Bias is toward `confirmed`**. When the cite is intact and no concrete mitigation is visible, the default verdict is `confirmed`. "It probably doesn't trigger in practice" is NOT a false-positive — it's a confirmed-but-low-frequency.

### Test policy — skipped via t.Skip(reason)

- **D-12:** For the 9 Critical+High failing-test reproducers (5 of which run in Go-test land; 4 of which are the manual-reproducer cases per D-03), each test is shipped with `t.Skip("REL-<ID> (<finding>): regression test — unskip in Phase 15 fix commit")`. Pattern:
  ```go
  func TestRegression_REL_POOL_01_PoolShrinksToZero(t *testing.T) {
      t.Skip("REL-POOL-01 (P-1): regression test — unskip in Phase 15 fix commit")
      // … actual reproducer body proving the bug …
  }
  ```
  CI stays green at Phase 14 close (`go test -race ./...` passes — trust gate intact). The tests ARE compiled (`t.Skip` doesn't exclude compilation) so they catch any signature drift, but don't run. They're discoverable via `go test -run TestRegression_REL_ -v` — operators can see the full list of regression tests waiting to be unskipped.
- **D-13:** Phase 15 fix commits include both the source fix AND the `t.Skip()` removal in the same atomic commit, so the PR diff shows red→green on the reproducer alongside the production change. This is the verification mechanism for Phase 15 success criteria — every Critical+High criterion is observable as a regression test flipping green.

### Claude's Discretion (no further input needed)

- **Plan structure inside each of the 4 plans:** standard SUMMARY/PLAN/VALIDATION shape via `/gsd-plan-phase` on each. Tasks within a plan are naturally per-finding (5–7 tasks per plan); no further sub-structure needed.
- **Worktree management:** standard `gsd-executor` worktree flow. The 4 plans touch fully disjoint test files (`internal/pool/`, `internal/server/` + `internal/adapter/*/`, `cmd/otto-tray/` + `scripts/`, `internal/config/` + `internal/plugin/` + `internal/admin/`), so merges should be clean.
- **Ledger merge step:** the executor closing Phase 14 reads the 4 fragments, concatenates them into the master table with consistent column ordering and a header row, and adds a small "Summary" section above the table counting confirmed / false-positive / needs-investigation by severity. No special tooling — straightforward markdown assembly.
- **REQUIREMENTS.md traceability table updates:** if any finding is marked `false-positive`, its row in REQUIREMENTS.md's traceability table gets `status: dropped (FP — see 14-FINDING-<ID>.md)`. This update lands as part of the Phase 14 close commit, not separately.
- **CLAUDE.md updates:** none. Phase 14 produces evidence; CLAUDE.md project documentation doesn't change yet. Updates that mirror the actual fixes belong in Phase 15/16 close.
- **No new Plans 14-05+:** the 4-plan structure (one per subsystem) is locked. Findings cannot be split or moved between plans during execution — that would re-open the parallelization decision.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Driver document (THE source of truth for each finding)
- `docs/reviews/2026-06-11-reliability-review.md` — The reliability review itself. Sections 1–6 hold the 35 findings; the 23 in this phase's scope are the Critical + High + Medium tier (P-1 to P-8, H-1 to H-7, T-1 to T-9, C-1 to C-6, O-1 to O-4, G-1 — minus the Lows which are explicitly out of scope). Every per-finding evidence file MUST quote or cite the relevant review section.

### Milestone-level planning artifacts
- `.planning/PROJECT.md` §"Current Milestone: v1.9 Reliability Hardening" — the scope statement and per-subsystem requirement category map.
- `.planning/REQUIREMENTS.md` §"v1.9 Requirements" + §"Traceability" — locked REL-* IDs and 1:1 finding-to-REL-*-to-phase mapping. Phase 14's ledger MUST match this mapping verbatim.
- `.planning/ROADMAP.md` §"v1.9 Reliability Hardening" + §"Phase Details: Phase 14" — phase goal, success criteria, dependency on Phase 14 ledger.

### Trust-gate posture (non-negotiable)
- `CLAUDE.md` §Constraints — `gofumpt`, `go vet`, `go build`, `golangci-lint`, `govulncheck`, `go test -race ./...` all must pass at phase close. Specifically: Phase 14's `t.Skip`'d reproducer tests MUST compile clean (no signature drift) and MUST NOT run (no red CI).

### Prior-phase reference (parallelization pattern)
- `.planning/milestones/v1.8-ROADMAP.md` §"Phase 13: Nyquist coverage uplift" — the closest prior precedent for a multi-worktree cross-cutting audit phase. Uses `gsd-nyquist-auditor`; Phase 14 uses `gsd-executor` instead, but the wave-of-parallel-worktrees shape is the same.

### Source files referenced by findings in scope
*(Listed here so downstream agents know which files Phase 14 will read against — not exhaustive, see individual review sections for full cite lists.)*
- Pool/ACP: `internal/pool/pool.go`, `internal/acp/client.go`, `internal/acp/stream.go`, `internal/acp/pool_pgid_unix.go`, `internal/acp/pool_pgid_windows.go`, `internal/session/registry.go`, `internal/session/reaper.go`, `internal/session/entry_acp.go`, `cmd/otto-gateway/main.go`
- HTTP: `internal/server/server.go`, `internal/server/health.go`, `internal/adapter/openai/sse.go`, `internal/adapter/ollama/ndjson.go`, `internal/adapter/anthropic/sse.go`, `internal/admin/sse.go`, `internal/admin/tail.go`
- Tray: `cmd/otto-tray/tray.go`, `cmd/otto-tray/fsm.go`, `cmd/otto-tray/poller.go`, `cmd/otto-tray/runner.go`, `cmd/otto-tray/uihelpers_darwin.go`, `cmd/otto-tray/uihelpers_windows.go`, `cmd/otto-tray/dotenv.go`, `cmd/otto-tray/autostart_darwin.go`, `scripts/otto-gw`, `scripts/otto-gw.ps1`
- Config/Hooks/Obs: `internal/config/config.go`, `internal/plugin/logging.go`, `internal/plugin/trace.go`, `internal/engine/collect.go`, `internal/engine/engine.go`, `internal/adapter/anthropic/collect.go`

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- **`gsd-executor` agent + worktree workflow** — the standard execution path. No new agent type needed.
- **`docs/reviews/2026-06-11-reliability-review.md`** — already provides file:line cites and failure scenarios for every finding. Phase 14's evidence files quote-then-verify rather than redoing the analysis.
- **Existing test infrastructure** — `goleak` integration (referenced in 08.3 CONTEXT.md and used in `internal/acp/` tests), table-driven test patterns, the `fakeacp` test double for ACP wire-level testing. C+H reproducer tests can hook into the same fixtures.

### Established Patterns
- **Read-only-implementation rule** — v1.8 Phase 13 enforced this milestone-wide with `git diff main...HEAD -- ':!*_test.go' ':!*VALIDATION.md' ':!testdata/' ':!.planning/'` returning empty at phase close. Phase 14 mirrors this: empty production diff, only test files + ledger artifacts + manual reproducer scripts changed.
- **Wave-based parallel plans + worktrees** — v1.8 Phase 13's 6-plan parallel wave is the closest precedent. Phase 14 runs 4 instead of 6.
- **Per-plan ledger fragments merged at phase close** — generalizes the v1.8 per-phase VALIDATION.md → milestone-level compliance summary pattern.
- **`t.Skip()` with structured reason string** — already used elsewhere in the tree for deliberate test deferrals; the `"REL-<ID> (<finding>): regression test — unskip in Phase 15 fix commit"` format is new for this milestone and should be consistent across all 9 reproducers.

### Integration Points
- **Phase 15/16 PLAN.md generation** — `/gsd-plan-phase 15` and `/gsd-plan-phase 16` MUST consume `14-VERIFICATION-LEDGER.md` as their first input. Specifically, a task is generated for REL-X only if the ledger row for REL-X has `Status: confirmed`. `false-positive` rows drop their REL-* from the phase scope; this drop is recorded in the phase's PLAN.md "Out of scope" section with a backlink to `14-FINDING-<ID>.md`.
- **`t.Skip` → fix-commit unskip** — each Phase 15/16 fix commit pair-deletes the corresponding `t.Skip()` line from the reproducer test. The PR diff for any C+H fix MUST show both the source fix AND the `-` line removing the t.Skip. This is the executable verification mechanism for Phase 15 success criteria.

</code_context>

<specifics>
## Specific Ideas

- **One regression-test naming convention across all 9 C+H reproducers:** `TestRegression_REL_<KEY>_<ShortDescription>`. Examples: `TestRegression_REL_POOL_01_PoolShrinksToZero`, `TestRegression_REL_POOL_02_CtrlCOrphansChildren`, `TestRegression_REL_POOL_03_StaleActiveStreamClobber`, `TestRegression_REL_HTTP_01_ShutdownBlocksOnAdminSSE`, `TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker`, `TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent`, `TestRegression_REL_TRAY_01_PIDIdentityUnchecked`, `TestRegression_REL_TRAY_02_WindowsBundleExitOne`, `TestRegression_REL_TRAY_03_MacosSilentGatewayDeath`. Consistent naming makes `go test -run TestRegression_REL_` a discoverable surface for "what's the reliability regression set?".
- **Manual reproducer script header convention:** every script under `tests/reliability/manual/` opens with a header comment block stating: finding ID, REL-* ID, target phase, target OS, expected pre-fix behavior, expected post-fix behavior, and step-by-step run instructions. Operators reading these scripts should be able to verify the fix without context from this conversation.

</specifics>

<deferred>
## Deferred Ideas

- **`tests/reliability/automation`** — if a future phase wants to wire the 9 reproducer tests into a separate CI job (e.g., a `reliability-regression` GitHub Actions matrix that runs `go test -tags=reliability_regression`), that's a v1.10+ infrastructure decision. v1.9 Phase 14 uses `t.Skip` (visible in default list, doesn't run), which is sufficient for the milestone goal.
- **Lows backlog (12 findings)** — already deferred to v1.10 per REQUIREMENTS.md `## v2 Requirements`. Phase 14 does NOT verify them; they appear in the master ledger only as `n/a (deferred to v1.10)` placeholder rows for completeness so the ledger covers all 35 review findings.
- **Generalize finding-ledger format as a reusable template** — if v1.10+ runs another external review, the 14-VERIFICATION-LEDGER.md + 14-FINDING-<ID>.md pattern could templatize. Not a Phase 14 concern.

</deferred>

---

*Phase: 14-verify-reliability-findings*
*Context gathered: 2026-06-11*
