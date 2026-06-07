# Requirements: OTTO Gateway — Milestone v1.6 "Tooling Cleanup"

**Milestone goal:** Drain the trust-gate violation backlog (golangci-lint v2 + gofumpt) and restore lint as a merge gate before the next feature milestone.

**Status:** Active (opened 2026-06-06)

**Predecessor:** v1.5 "audit WARNINGs" — shipped 2026-06-04, 63/63 requirements. Archived at `.planning/milestones/v1.5-REQUIREMENTS.md`.

---

## Active Requirements

### Lint (golangci-lint v2)

- [ ] **LINT-01**: `golangci-lint run --timeout=5m` (pinned v2.1.6, against `.golangci.yml` v2 schema) reports zero issues from a clean working tree. The current 49-issue baseline (captured at commit `fa4109e`) is drained to zero through a mix of fixes, scoped `//nolint:linter // <rationale>` exemptions, or surgical `.golangci.yml` rule disables — each non-fix carries a written rationale in the diff that introduces it.
- [ ] **LINT-02**: The golangci-lint step in `.github/workflows/ci.yml` no longer carries `continue-on-error: true`. Lint failures block PR merges and `main` pushes at the CI gate. The temporary TODO comment introduced in commit `f3a70fc` is removed.
- [ ] **LINT-03**: A per-category decision record exists for every linter that fired in the baseline (wrapcheck, unparam, revive, gosec, unused, noctx, staticcheck, bodyclose, nilerr). Stored as a section in the phase's PLAN.md or SUMMARY.md so future contributors can see why each category was handled the way it was — fix policy, disabled rule, or accepted exemption pattern.

### Formatting (gofumpt)

- [ ] **FMT-01**: `gofumpt -d .` reports no diffs from a clean working tree. Covers the pre-existing drift across `cmd/` + `internal/adapter/*` documented in v1.5's "Known deferred / accepted tech debt" list (Phase 2/3.1/8 origin), plus anything that drifted in during v1.6 work.
- [ ] **FMT-02**: `make ci` runs the fmt-check step against the whole tree and exits 0 on a clean checkout. The brief §3.12 sequence (gofumpt → vet → build → lint → test-race → arch-lint → examples → govulncheck → cross) holds end-to-end on `main`.

### CI hygiene (regression prevention)

- [ ] **CI-01**: A pre-commit hook OR an explicit `make pre-commit` target invokes `gofumpt -l .` and `golangci-lint run` against staged files, surfacing violations before the operator pushes. Decision between hook vs make target is up to the implementer; rationale captured in the implementing phase's PLAN.md.

---

## Future Requirements (post-v1.6)

Carried forward from v1.5; explicitly NOT in v1.6 scope. Each will be scoped into v1.7 or later via `/gsd-new-milestone`:

- **Phase 08.3.1 — ACP Per-Session Stream Demux** (carried from v1.5). Replace single-slot `c.activeStream *Stream` with per-sessionID map; closes WR-04 silent cross-session leak race. Required only for multi-tenant gateway scenarios v1 does not run.
- **Nyquist coverage uplift.** 3/11 v1.5 phases fully compliant. Bring older phases up to the post-08.1 validation standard.
- **Windows Authenticode code-signing.** Seed `001-authenticode-code-signing-windows-distribution` in `.planning/seeds/` documents the rationale. Distribution-trust improvement.

---

## Out of Scope

Explicit exclusions to keep v1.6 narrow and ship-fast:

- **Functional code changes beyond what lint demands.** v1.6 is not the place for "while I'm in here" refactors. If a `wrapcheck` fix or `unparam` cleanup tempts a broader rework, defer the rework to a follow-up phase and minimize the lint-fix diff.
- **golangci-lint version bumps beyond v2.1.6.** The pin is fresh and the v2 schema is stable; chasing newer releases delays the gate restoration. Bump deliberately in a future milestone if a new linter or rule we want lands.
- **New linters or `.golangci.yml` rule additions.** v1.6 is debt-reduction, not capability expansion. Adding linters is a v1.7+ activity once the existing rule set is honored.
- **Pre-existing v1.5 carryovers other than gofumpt** (see Future Requirements). v1.6 stays narrow so it ships in days, not weeks.
- **Trust-gate sequence reorganization.** The brief §3.12 sequence is canonical; v1.6 enforces it, doesn't redesign it.

---

## Traceability

(Filled by `/gsd-plan-phase` and `/gsd-execute-phase` as work lands. Each REQ-ID maps to the phase + plan that satisfies it.)

| REQ-ID | Phase | Plan | Status |
|--------|-------|------|--------|
| LINT-01 | Phase 10 | 10-01, 10-02, 10-03 | Complete |
| LINT-02 | Phase 10 | 10-04 | Complete |
| LINT-03 | Phase 10 | 10-04 | Complete |
| FMT-01 | Phase 11 | 11-01 | Complete |
| FMT-02 | Phase 11 | 11-01 | Complete (govulncheck routed to v1.7) |
| CI-01 | Phase 11 | 11-01 | Complete |

---

*Milestone v1.6 opened 2026-06-06.*
