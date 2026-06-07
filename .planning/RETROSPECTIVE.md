# Project Retrospective

*A living document updated after each milestone. Lessons feed forward into future planning.*

## Milestone: v1.8 — Nyquist Coverage Uplift

**Shipped:** 2026-06-07
**Phases:** 1 | **Plans:** 6 | **Sessions:** 1

### What Was Built

- 6 v1.5 phase VALIDATION.md docs lifted from `nyquist_compliant: false` to `nyquist_compliant: true` (phases 02, 03, 06, 06.1, 08, 08.4) — compliance ratio 7/13 → 13/13.
- Each target VALIDATION.md now carries a complete per-task verification map (4–26 task rows), Wave 0 fixtures, manual-only rationales, and all 6 sign-off boxes ticked.
- 36 commits, +3113/-239 LOC across 32 files. Zero non-test production source edits — read-only-implementation rule held milestone-wide.

### What Worked

- **6-way parallel worktree dispatch under a single orchestrator.** Each plan owned a disjoint target VALIDATION.md and disjoint test-package surface (PII / admin / openai-adapter / ollama-adapter / engine+jsonformat / plugin+hooks). The `files_modified` lists had no overlap, so the wave ran clean parallel.
- **Adversarial-stance read-only auditor.** The `gsd-nyquist-auditor` agent's contract — test files read-write, production source read-only, ESCALATE any bug — held milestone-wide. Zero silent patches.
- **Phase verification with `human_needed` + user-approval path.** 3 pre-existing operator-deferred UAT items surfaced cleanly, persisted to 13-HUMAN-UAT.md, and didn't block the milestone close.

### What Was Inefficient

- **`worktree.cleanup-wave` SDK helper failed on the first wave.** Two stray untracked files (`13-01-SUMMARY.md`, `13-04-GAPS.txt`) appeared in the main worktree before merge; the helper's pre-merge check refused to merge. Recovered manually via `git merge --no-ff` per worktree. Root cause not investigated this milestone (the leaked files were byte-identical to the worktree-committed versions, so just removing them and merging manually worked).
- **SUMMARY.md `requirements_completed` frontmatter empty for 4 of 6 plans.** Substantively complete (traceable via PLAN frontmatter, shell criterion, REQUIREMENTS.md `[x]`), but a 3-source cross-reference would have been one step cleaner if the executor template ensured frontmatter completeness.

### Patterns Established

- **Single-phase milestone for cross-cutting documentation sweeps.** Worked well here. Avoided the "wide-net audit + replan" loop by collapsing the entire uplift into one phase with 6 parallel plans.
- **Read-only auditor agent as a milestone primitive.** The `gsd-nyquist-auditor` pattern (test-write/source-read, adversarial stance, ESCALATE on real bugs) is reusable for any future cross-cutting "lift documentation to current standard" milestone.
- **Inherited UAT items don't block close.** When verification returns `human_needed` for pre-existing operator-deferred items (not new gaps from this phase), the right path is persist + user-approve + move on, not gap closure.

### Key Lessons

1. **Parallel worktree dispatch needs CWD pinning at orchestrator.** Shell drift into a worktree happened twice during cleanup; only `cd $(git rev-parse --show-toplevel)` before each `gsd-sdk` call kept the orchestrator on main.
2. **`workflow.cleanup-wave` is fragile when post-execute state has untracked files in the main tree.** Manual `git merge --no-ff` per branch is a reliable fallback that takes ~5 seconds per worktree.
3. **Documentation-only milestones still need a build+test gate.** Even with zero production source edits, `make test` + `go build ./...` post-merge caught no regressions but provided the green-tree confirmation needed to sign off cleanly.

### Cost Observations

- Model mix: ~100% sonnet (6 executor agents + 1 verifier agent under Opus orchestrator)
- Sessions: 1 (single-day milestone)
- Notable: 6 parallel sonnet executors against an Opus orchestrator was the right tier mix for this workload — auditor-style work has tight constraints and high parallel speedup; orchestration needs the bigger model for state-tracking across worktree merge complications.

---

## Cross-Milestone Trends

### Process Evolution

| Milestone | Sessions | Phases | Key Change |
|-----------|----------|--------|------------|
| v1.8 | 1 | 1 | Read-only auditor agent pattern + 6-way parallel worktree dispatch |

### Cumulative Quality

| Milestone | Tests | Coverage | Zero-Dep Additions |
|-----------|-------|----------|-------------------|
| v1.8 | 18/18 packages green | n/a (doc milestone) | 0 (read-only rule) |

### Top Lessons (Verified Across Milestones)

1. _(awaits v1.9+ for cross-validation)_
