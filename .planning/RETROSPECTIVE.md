# Project Retrospective

*A living document updated after each milestone. Lessons feed forward into future planning.*

## Milestone: v1.10.3 — Reliability Closeout

**Shipped:** 2026-06-12
**Phases:** 3 (18, 19, 20) | **Plans:** 5 | **Sessions:** ~2 (opened 2026-06-11 evening, shipped 2026-06-12 mid-day)

### What Was Built

- 17/17 REQ-IDs closed across 3 small, well-scoped phases — 10 Low-severity reliability long-tail items (Phase 18, 3 parallel plans, zero file overlap), 1 production race fix (Phase 19, REL-ACP-01 `acp.Stream.Result` snapshot-under-lock), 6 Info-level code-review cleanups (Phase 20, 6 atomic refactor commits).
- Phase 19 surgically reverted the Phase 17 test-side workaround in `regression_rel_pool_02_test.go` (-20 / +0) — the workaround was load-bearing ONLY for the race this phase closed. 60-iter race-loop whitebox regression (`internal/acp/regression_rel_acp_01_test.go`) — 6,000 race trials, zero data-race reports.
- Phase 20 self-review caught 2 Warnings (WR-01/WR-02) that landed inline via `/gsd-code-review 20 --fix` before milestone audit.
- Mid-milestone CI repair: commit `af850a2` retroactively dropped an unused `level` param from `startAndDrain` that broke baseline `make ci` between Phase 19 close and Phase 20 execution (`unparam` lint regression). Caught + fixed in-flight.
- `make ci` exits 0 end-to-end at milestone tip (`0871a38`); zero touches to OpenAI/Anthropic adapters; Ollama adapter touched only by REL-HTTP-06's server-side WARN log (no wire change). All 3 v1 client integrations byte-identical at the wire.

### What Worked

- **Three loosely-coupled fix areas with zero file overlap as 3 parallel plans.** Phase 18-01 (config), 18-02 (observability), 18-03 (tray) had no shared files — clean concurrent dispatch.
- **Audit-up-front read-only validation of every requirement before close.** The pre-close audit (`v1.10.3-MILESTONE-AUDIT.md`) scored 17/17 reqs + 6/6 cross-phase seams + 3/3 client flows from a 3-source cross-reference (REQUIREMENTS.md traceability + per-phase VERIFICATION.md + per-plan SUMMARY.md frontmatter). All three agreed — high confidence at close.
- **Threat-flag handoff from Phase 17 → Phase 19.** Phase 17's `17-02-SUMMARY.md` flagged the `acp.Stream.Result` race as a non-blocker but documented two fix options. Phase 19 picked Option A (copy-under-lock per D-19-01) with signature preservation; the threat-flag → next-phase handoff worked exactly as designed.
- **Inline self-review fixes via `--fix` flag.** Phase 20's 2 Warnings landed as 2 atomic commits (`275def8` + `cdb2fe5`) without leaving the closeout state.
- **Test-side revert documented as load-bearing.** The Phase 17 drain-Chunks workaround was only safe to remove because Phase 19 proved the race was closed at the production layer first. Sequencing prevented a false-pass at the test layer.

### What Was Inefficient

- **CLI `milestone.complete` pulled accomplishments from every SUMMARY.md in the repo, not just v1.10.3.** The auto-generated MILESTONES.md entry had to be manually rewritten (170+ irrelevant accomplishment bullets dropped, replaced with a clean 5-bullet v1.10.3 summary). Tracking-debt observation: the CLI scan should scope by `phases_in_scope` from the audit file when available.
- **CLI did not auto-update STATE.md `Last Activity Description` field** — workflow noted it as a non-blocking warning but it required a separate edit pass.
- **`af850a2` baseline-CI repair landed between Phase 19 close and Phase 20 execution** — the `unparam` regression was masked by Phase 18's plan-19 spawn order. A make-ci gate between every phase commit would have caught it at Phase 18-02 instead.
- **31-item open audit at close.** Most were stale (21 quick_tasks already shipped, status `unknown` because tracking metadata was never backfilled in v1.5–v1.8). The audit-acknowledge path worked, but the volume of false-positive items obscured the 2 genuinely new v1.10.3 todos.

### Patterns Established

- **"Tech debt deferred (non-blocking)" as a first-class audit section.** v1.10.3 audit documented WR-03 + IN-01..IN-05 + lint-cache-hygiene as bounded, named follow-ups with REQ-IDs assigned to the next milestone (v1.10.4). Future audits should adopt the same shape.
- **Surgical revert + race-loop whitebox test as the two-step closeout for any production race.** Phase 19 = (a) production-layer fix at the racing field + (b) test-layer revert of any prior workaround + (c) 60-iter `-count` whitebox regression. Reusable template.
- **Build-tag combinator `//go:build darwin || windows` for shared cross-platform helpers.** Phase 20 QUAL-02 dedup pattern.
- **Dual-invariant race regression test** when the fix closes the data-race report without serializing the racing order: assert (1) `-race` clean AND (2) observed value ∈ well-defined set, NOT a single deterministic value. See Phase 19 D-19-03 deviation rationale.

### Key Lessons

1. **A "long-tail closeout" milestone is the right shape when N small unrelated items have accumulated.** v1.10.3 cleanly bundled 3 independent inputs (Low-severity reliability tail, 1 production race, 6 code-review cleanups) into 3 phases × 5 plans × 17 REQ-IDs and shipped in ~24h elapsed. Don't try to fix the long tail incrementally inside the next feature milestone — it gets dropped.
2. **Phase 17's threat-flag carried clean execution into Phase 19.** Flagging non-blockers at the originating phase's SUMMARY.md (in a named `Threat Flags` section) gives the next milestone a clean handoff. Adopt for every milestone-close.
3. **CLI `milestone.complete` needs scope-awareness.** When the audit's `phases_in_scope` is present, the accomplishments scan should restrict to those phase directories. Worth a `gsd-tools` issue.
4. **Acknowledge-and-carry tracking debt at every close.** 21 stale `quick_tasks` from v1.5–v1.8 still show as `unknown` because the original tracking entries were never backfilled. Either backfill at the next quick-task commit, or treat the open-audit list as advisory only.

### Cost Observations

- Model mix: mixed (Opus orchestrator for plan/audit/verification gating; sonnet executors for parallel plan execution); Phase 19 used Opus for the concurrency reasoning, sonnet for the test scaffolding.
- Sessions: ~2 (planning + execution split across late-evening + next-day mid-morning).
- Notable: A 3-plan parallel wave (Phase 18) + 2 single-plan phases (19, 20) is a cleaner shape for a closeout milestone than a wide parallel fan-out. The cost per REQ-ID is lower because each plan is small and the audit gate sees full per-plan verification.

---

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
