# Phase 14: Verify Reliability Findings - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-06-11
**Phase:** 14-verify-reliability-findings
**Areas discussed:** Verification rigor per severity, Parallelization shape, Ledger format + downstream consumption, False-positive bar + test-addition policy

---

## Verification rigor per severity

| Option | Description | Selected |
|--------|-------------|----------|
| Mixed by severity | C+H → failing-test reproducer; M → code-walk only. ~9 reproducer tests + 14 code-walk entries. Wall-clock ~1–1.5 days. | ✓ |
| Code-walk for all 23 | Cheapest. Every finding gets a re-read + 2–3 sentence note. No tests. Risk: a Critical/High slips through 'confirmed' when actually already mitigated. | |
| Failing-test reproducer for all 23 | Most rigorous. Every confirmed finding has an actual failing/skipped test. ~3–4 days; 5–6 findings need manual reproducers anyway. | |

**User's choice:** Mixed by severity.
**Notes:** Load-bearing failures get the highest evidence bar; cheap-to-fix Mediums get cheap evidence. Roughly matches Phase 15/16 effort distribution.

---

## Parallelization shape

| Option | Description | Selected |
|--------|-------------|----------|
| 4 parallel plans by subsystem area | Mirrors v1.8 Phase 13. Pool/HTTP/Tray/Cfg-Hooks-Obs in separate worktrees. Orchestrator merges fragments. | ✓ |
| Single sequential plan | One executor walks all 23 in severity order. Simpler; slower wall-clock; no merge complexity. | |
| 2 plans split by rigor (test vs code-walk) | Plan A: 9 C+H reproducers. Plan B: 14 Mediums code-walk. Plays to workload shape but loses area-locality. | |

**User's choice:** 4 parallel plans by subsystem.
**Notes:** Independence is high — findings rarely cross subsystem boundaries. Standard `gsd-executor` agent (not `gsd-nyquist-auditor`, which was v1.8-specific).

---

## Ledger format + downstream consumption

| Option | Description | Selected |
|--------|-------------|----------|
| Single table + per-finding evidence files | Master table for fast filter; per-finding `.md` files for rich evidence. Best of both for downstream planners. | ✓ |
| Single table, evidence inline | One ~2000-line file. High diffability, low navigability. | |
| Per-finding files only, no master table | 23 files. Highest detail; weakest at-a-glance overview. | |

**User's choice:** Single table + per-finding evidence files.
**Notes:** 7-column master table (Finding ID, Severity, REL-* ID, Status, File:line, Evidence ref, Target phase). Per-finding files at `14-FINDING-<ID>.md` hold the failing-test pointer, code-walk write-up, or false-positive justification.

---

## False-positive bar

| Option | Description | Selected |
|--------|-------------|----------|
| Strict + 'needs-investigation' as escape hatch | False-positive requires cite literally gone OR concrete guard added. Ambiguity → `needs-investigation` with 'what would flip this' note. Phase 14 close gate: needs-investigation count == 0. | ✓ |
| Lenient | Any plausible mitigation since the review counts. Risk: drops real findings whose mitigation doesn't actually cover trigger. | |

**User's choice:** Strict + 'needs-investigation' as escape hatch.
**Notes:** Bias is toward `confirmed` when ambiguous. Phase 15/16 plan cannot run while any `needs-investigation` rows remain.

---

## Test-addition policy (C+H reproducers)

| Option | Description | Selected |
|--------|-------------|----------|
| Skipped via t.Skip(reason) | Standard Go idiom. Reproducer body lives in tree; `t.Skip("REL-<ID> ...")` blocks runtime. Phase 15 unskips as part of fix commit (red→green in same PR diff). | ✓ |
| Build-tagged (//go:build reliability_regression) | Tests excluded from default `go test ./...`. Phase 15 removes the build tag. More invisible than t.Skip. | |
| Manual reproducer scripts under tests/reliability/ | Runnable scripts only; no Go test entry. Best for findings that genuinely can't reproduce in CI. | |

**User's choice:** Skipped via t.Skip(reason).
**Notes:** Manual reproducer scripts ARE still used for the 4 OS-specific findings that can't run in Go-test land (T-3 macOS, P-6/T-2/T-6 Windows) — but with a structured `t.Skip` placeholder in Go that points to the script. Not an either/or for those cases.

---

## Claude's Discretion

- Plan internal structure (SUMMARY/PLAN/VALIDATION shape) — standard via `/gsd-plan-phase`.
- Worktree management — standard `gsd-executor` flow; 4 plans touch disjoint test files so merges are clean.
- Ledger merge step at Phase 14 close — straightforward markdown assembly of 4 fragments + a summary block above the table.
- REQUIREMENTS.md traceability updates for any false-positive rows — landed as part of Phase 14 close commit, not a separate phase.
- No CLAUDE.md updates in Phase 14 — those mirror actual fixes and belong to Phase 15/16 close.

## Deferred Ideas

- `tests/reliability/automation` — wiring the regression set into a separate CI job (e.g., `reliability-regression` matrix) is a v1.10+ infrastructure decision.
- Lows backlog (12 findings) — already deferred to v1.10 via REQUIREMENTS.md `## v2 Requirements`. Phase 14 lists them in the master ledger only as `n/a (deferred to v1.10)` placeholder rows for completeness.
- Generalize the ledger format as a reusable template for future review-driven milestones — not a Phase 14 concern.
