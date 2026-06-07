# Requirements: OTTO Gateway — Milestone v1.8 "Nyquist Coverage Uplift"

**Milestone goal:** Bring the 6 v1.5 phases with `nyquist_compliant: false` up to the post-08.1 validation standard so every shipped phase carries a complete VALIDATION.md contract.

**Status:** Active (opened 2026-06-07)

**Predecessor:** v1.7 "Go Stdlib CVE Cleanup" — shipped 2026-06-07, 4/4 requirements (zero carve-outs). Archived at `.planning/milestones/v1.7-REQUIREMENTS.md`.

---

## Active Requirements

### Per-phase Nyquist compliance

Each of the 6 non-compliant v1.5 phases gets one REQ-ID. Compliance criterion (same for every phase): the phase's VALIDATION.md satisfies all five sign-off constraints — per-task verification map filled, Wave 0 requirements enumerated, no 3 consecutive tasks without automated verify, no watch-mode flags, feedback latency under the per-phase budget — and the frontmatter flips from `nyquist_compliant: false` to `nyquist_compliant: true`.

- [ ] **NYQ-02**: Phase 02 (Ollama end-to-end). VALIDATION.md flipped to `nyquist_compliant: true` with the per-task map filled for every task in the original PLAN.md (verifiable by `grep "nyquist_compliant: true" .planning/phases/02-*/02-VALIDATION.md`).
- [ ] **NYQ-03**: Phase 03 (OpenAI surface). Same compliance criterion against `.planning/phases/03-*/03-VALIDATION.md`.
- [ ] **NYQ-06**: Phase 06 (Tool-call path). Same compliance criterion against `.planning/phases/06-*/06-VALIDATION.md`.
- [ ] **NYQ-06.1**: Phase 06.1 (Admin observability UI). Same compliance criterion against `.planning/phases/06.1-*/06.1-VALIDATION.md`.
- [ ] **NYQ-08**: Phase 08 (Plugin hook chain). Same compliance criterion against `.planning/phases/08-*/08-VALIDATION.md`. Note: this is the largest target phase (most tasks, most surface area).
- [ ] **NYQ-08.4**: Phase 08.4 (US Address PII coverage). Same compliance criterion against `.planning/phases/08.4-*/08.4-VALIDATION.md`. Should be the smallest because the phase shipped recently under tighter discipline; expect lightweight gaps.

### Milestone-level sign-off

- [ ] **NYQ-ALL**: All 13 v1.5 phase VALIDATION.md docs (or however many exist) report `nyquist_compliant: true` at milestone close. Compliance ratio goes from 7/13 to 13/13. Verified by a shell loop counting `nyquist_compliant: true` against the count of `*-VALIDATION.md` files under `.planning/phases/`. The milestone audit confirms.

---

## Future Requirements (post-v1.8)

Carried forward; explicitly NOT in v1.8 scope. Each will be scoped into v1.9 or later via `/gsd-new-milestone`:

- **Phase 08.3.1 — ACP Per-Session Stream Demux** (carried from v1.5, re-re-re-deferred from v1.6, v1.7, and v1.8). Replace single-slot `c.activeStream *Stream` with per-sessionID map; closes WR-04 silent cross-session leak race. Required only for multi-tenant gateway scenarios v1 does not run. Will move when a real multi-tenant driver appears (e.g. a deployment story that genuinely shares a `kiro-cli` worker across concurrent prompts).
- **Windows Authenticode code-signing.** Seed `001-authenticode-code-signing-windows-distribution` in `.planning/seeds/` documents the rationale. Distribution-trust improvement; requires code-signing certificate procurement decision and operator coordination — long pole that would stall v1.8 if bundled.

---

## Out of Scope

Explicit exclusions to keep v1.8 narrow and ship-fast (mirrors v1.6 + v1.7 discipline):

- **Implementation changes during the Nyquist uplift.** The `gsd-nyquist-auditor` agent's read-only-implementation rule holds milestone-wide: if a test reveals an actual implementation bug, ESCALATE it as a separate phase (Phase 14+) rather than silently patching during the uplift. Test files (`*_test.go`, fixtures, `VALIDATION.md` itself) are read-write; production source is read-only.
- **Adding NEW phases to the validation contract.** v1.8 closes gaps in EXISTING phase VALIDATION.md docs. Phase 12 (v1.7) and Phases 10/11 (v1.6) were planned under the post-08.1 discipline and should already be compliant; verify they are, but no new validation infrastructure is built for them.
- **Refactoring the GSD framework's VALIDATION.md template.** The template at `~/.claude/get-shit-done/templates/VALIDATION.md` is the canonical contract. v1.8 fills the contract; it doesn't redesign it.
- **Performance or flakiness work on existing tests.** If a test in a target phase is slow or flaky, document it in the phase's VALIDATION.md "Manual-only verifications" or "Sign-off caveats" section. Fixing slowness or flakiness is a separate concern (v1.9 candidate).
- **Test framework migrations or version bumps.** `go test -race` stays the canonical runner. No `gotestsum`, no `ginkgo`, no test-runner swap in scope.
- **New linters or pre-commit hooks.** v1.6 set those baselines; v1.7/v1.8 keep them.

---

## Traceability

(Filled by `/gsd-plan-phase` and `/gsd-execute-phase` as work lands.)

| REQ-ID | Phase | Plan | Status |
|--------|-------|------|--------|
| NYQ-02 | Phase 13 | TBD (13-NN) | Pending |
| NYQ-03 | Phase 13 | TBD (13-NN) | Pending |
| NYQ-06 | Phase 13 | TBD (13-NN) | Pending |
| NYQ-06.1 | Phase 13 | TBD (13-NN) | Pending |
| NYQ-08 | Phase 13 | TBD (13-NN) | Pending |
| NYQ-08.4 | Phase 13 | TBD (13-NN) | Pending |
| NYQ-ALL | Phase 13 (milestone-close audit) | — (satisfied automatically when NYQ-02..08.4 all Complete + v1.8 milestone audit passes) | Pending |

---

*Milestone v1.8 opened 2026-06-07. Roadmap drafted 2026-06-07: single Phase 13 with 6 parallel plans (one per target VALIDATION.md), NYQ-ALL satisfied at milestone close. Phase 08.3.1 ACP demux + Windows Authenticode re-deferred to v1.9+ per v1.8-opens narrow-scope decision.*
