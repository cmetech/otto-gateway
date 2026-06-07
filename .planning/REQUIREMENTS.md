# Requirements: OTTO Gateway — Milestone v1.7 "Go Stdlib CVE Cleanup"

**Milestone goal:** Drain the Go stdlib CVE backlog `govulncheck` flagged on `main` after v1.6 Phase 10 restored the lint gate, so `make ci` exits 0 end-to-end without the v1.6 carve-out.

**Status:** Active (opened 2026-06-07)

**Predecessor:** v1.6 "Tooling Cleanup" — shipped 2026-06-07, 6/6 requirements (FMT-02 with documented `(govulncheck routed to v1.7)` carve-out). Archived at `.planning/milestones/v1.6-REQUIREMENTS.md`.

---

## Active Requirements

### Vulnerability remediation (govulncheck)

- [ ] **CVE-01**: `govulncheck ./...` exits 0 from a clean checkout of `main` against the Go stdlib CVE list flagged at v1.6 close (GO-2026-5039, -5037, -4982, -4980, -4971, -4947, -4946, -4870, plus any others surfaced in CI run [27080012241](https://github.com/cmetech/otto-gateway/actions/runs/27080012241)'s Vulnerability scan step). Resolution path per finding: prefer toolchain bump (a single `go` directive change in `go.mod` resolves most stdlib CVEs); for any residual finding where the gateway code reaches the vulnerable function path, either fix the calling code or document the unreachability with a written rationale in the implementing phase's PLAN.md or SUMMARY.md.
- [ ] **CVE-02**: The `go` directive in `go.mod` (and any toolchain pin in `go.work` or related files) is bumped to a patched Go release that resolves the CVE-01 findings. The bump is the minimum change necessary — no opportunistic language-feature uptake or test-helper refactors. Rationale for the chosen Go version (e.g. "1.25.4 — patches GO-2026-5039 stdlib chain in net/http", "1.26.0 — adopts the canonical x509.Verify hardening") captured in the commit message.
- [ ] **CVE-03**: CI's `Vulnerability scan` step (under the `lint + test-race + arch-lint + govulncheck` job in `.github/workflows/ci.yml`) passes on `main` post-bump. Verified by a successful CI run on `main` after the milestone-closing commit.

### Trust-gate completion (v1.6 carve-out close)

- [ ] **CI-02**: `make ci` (the full brief §3.12 sequence: gofumpt → vet → build → lint → test-race → arch-lint → examples → govulncheck → cross) exits 0 end-to-end on a clean checkout of `main`. Closes the documented carve-out in v1.6 Phase 11's `11-01-SUMMARY.md` D-11-01.

---

## Future Requirements (post-v1.7)

Carried forward; explicitly NOT in v1.7 scope. Each will be scoped into v1.8 or later via `/gsd-new-milestone`:

- **Phase 08.3.1 — ACP Per-Session Stream Demux** (carried from v1.5, re-re-deferred from v1.6 and v1.7). Replace single-slot `c.activeStream *Stream` with per-sessionID map; closes WR-04 silent cross-session leak race. Required only for multi-tenant gateway scenarios v1 does not run.
- **Nyquist coverage uplift.** 3/11 v1.5 phases fully compliant. Bring older phases up to the post-08.1 validation standard.
- **Windows Authenticode code-signing.** Seed `001-authenticode-code-signing-windows-distribution` in `.planning/seeds/` documents the rationale. Distribution-trust improvement; requires code-signing certificate procurement decision and operator coordination — long pole that would stall v1.7.

---

## Out of Scope

Explicit exclusions to keep v1.7 narrow and ship-fast (mirrors v1.6's discipline):

- **Opportunistic language-feature uptake.** The Go toolchain bump for CVE remediation is the minimum change necessary. Migrating to new stdlib APIs, replacing third-party libs with newly-promoted stdlib equivalents, or adopting new language features are explicit non-goals for v1.7. Defer to a future milestone if there's value.
- **Application-level security review beyond govulncheck-flagged paths.** v1.7 closes the CVE backlog; a broader security review (gosec G304 re-enable, SAST sweep, fuzzing uplift) is a v1.8+ candidate.
- **Third-party dependency bumps unrelated to CVE remediation.** If a `go mod tidy` after the toolchain bump pulls in updated indirect deps, that's accepted. Touching direct dependencies beyond what govulncheck demands is out of scope.
- **`golangci-lint` / `gofumpt` / pre-commit hook changes.** v1.6 set those baselines; v1.7 keeps them.
- **CI workflow changes beyond confirming the `Vulnerability scan` step passes.** No re-org of `ci.yml`, no new jobs, no matrix expansion.

---

## Traceability

(Filled by `/gsd-plan-phase` and `/gsd-execute-phase` as work lands. Each REQ-ID maps to the phase + plan that satisfies it.)

| REQ-ID | Phase | Plan | Status |
|--------|-------|------|--------|
| CVE-01 | — | — | Active |
| CVE-02 | — | — | Active |
| CVE-03 | — | — | Active |
| CI-02 | — | — | Active |

---

*Milestone v1.7 opened 2026-06-07.*
