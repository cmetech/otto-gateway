---
phase: 10-golangci-lint-v2-cleanup-re-gate
plan: 04
wave: 4
status: complete
date: 2026-06-07
requirements: [LINT-02]
---

# Wave 4 — Re-gate CI

**Purpose:** Remove the temporary `continue-on-error: true` exemption from the golangci-lint step in `.github/workflows/ci.yml` now that Waves 1-3 drained the v2 baseline to zero, and prove the gate fires via a negative-test PR.

## Outcome

**LINT-02 satisfied.**

| Probe | Run | Lint step outcome | Result |
|-------|-----|-------------------|--------|
| Main on `62b3ca5` (clean) | [27080012241](https://github.com/cmetech/otto-gateway/actions/runs/27080012241) | `golangci-lint found no issues` (0 issues) | ✓ Lint step passes on clean code |
| PR #1 on `7219db8` (deliberate `unused` violation) | [27080014440](https://github.com/cmetech/otto-gateway/actions/runs/27080014440) | `internal/version/lintbreaker.go:5:6: func unusedHelperForGateNegativeTest is unused (unused)` — `1 issues: unused: 1`, `##[error]issues found` | ✓ Lint step blocks on violation |

The lint step is now a hard merge gate. Future regressions reintroducing any of the 49 baseline categories will be caught at PR review time, not silently accreted as debt.

## Commits

| Hash | Subject |
|------|---------|
| `803530f` | fix(10-04-T1): remove golangci-lint continue-on-error from CI |
| `52da974` | fix(10-04): drop trailing blank line in request_id.go (gofumpt) |
| `6a82f03` | fix(10-04): bump golangci-lint-action v6 → v7 for v2.x support |
| `6ed9f98` | fix(10-04): bump golangci-lint pin v2.1.6 → v2.12.2 (Go 1.25 build) |
| `62b3ca5` | fix(10-04): correct wrapcheck v2 schema key (ignoreSigs → extra-ignore-sigs) |

## Deviations from plan

Four follow-on fixes were required beyond Task 1's single ci.yml edit. Each was a layer of latent CI-config rot exposed once `continue-on-error: true` was removed and the lint step started reporting its real outcome:

1. **gofumpt regression in `internal/plugin/request_id.go`** — Wave 1 commit `e8b2861` (chore: remove unused `newRequestIDFromReader`) left a trailing blank line. The CI gofumpt step caught it; local lint-only verification didn't. Fixed in `52da974`.

2. **`golangci-lint-action@v6` cannot install v2.x** — the v2.1.6 pin (introduced in `f3a70fc` during the original CI fix) was incompatible with the action's v6 series: `"golangci-lint v2 is not supported by golangci-lint-action v6, you must update to golangci-lint-action v7."` This means **every CI lint job since `f3a70fc` was failing at the action install step** before lint ever ran, masked locally by `continue-on-error: true`. Bumped to `@v7` in `6a82f03`.

3. **`v2.1.6` built with Go 1.24, codebase declares Go 1.25.0** — once v7 of the action installed, it rejected v2.1.6 with `"can't load config: the Go language version (go1.24) used to build golangci-lint is lower than the targeted Go version (1.25.0)"`. Bumped pin to `v2.12.2` (built with Go 1.26) in `6ed9f98`. This matches the local dev pin.

4. **`linters.settings.wrapcheck.ignoreSigs` is not the v2 schema key** — `golangci-lint config verify` (run by action @v7 before lint) rejected `ignoreSigs` as `additional properties 'ignoreSigs' not allowed`. v2 uses `extra-ignore-sigs` (kebab-case) for additions to the default ignore list, which already covers `.Errorf`, `errors.New`, `errors.Unwrap`, `.Wrap`, `.Wrapf`, `.WithMessage`, `.WithMessagef`, `.WithStack`. Local `golangci-lint run` was silently tolerating the unknown key. Fixed in `62b3ca5` — only `errors.Join(` needed to be added via `extra-ignore-sigs`.

The chain of fixes (1→4) reveals that the `.golangci.yml` v2-schema migration in commit `f3a70fc` was incomplete in subtle ways no one noticed because the gate was off. Wave 4 was therefore not just "remove one line" — it was the closing audit of the whole v2 migration. Each issue is documented in its commit message; future operators have the full reasoning.

## Negative-test details

- **Branch:** `test/lint-gate-negative-test` (deleted post-verification)
- **PR:** #1 (closed-without-merge)
- **Deliberate violation:** `internal/version/lintbreaker.go` introduced an unused exported function that triggers golangci-lint's `unused` linter
- **Failure CI run:** [27080014440](https://github.com/cmetech/otto-gateway/actions/runs/27080014440)
- **Failure message (exact):**
  ```
  internal/version/lintbreaker.go:5:6: func unusedHelperForGateNegativeTest is unused (unused)
  func unusedHelperForGateNegativeTest() string { return "unused" }
       ^
  1 issues:
  * unused: 1
  ##[error]issues found
  ```

The error is unambiguous — no version-mismatch detours, no gofumpt false-positives, no config-schema rejections. The gate fires for the right reason.

## Unmasked follow-up (out of scope for v1.6)

Main CI's *Vulnerability scan* step now fails on multiple Go stdlib CVEs (GO-2026-5039, -5037, -4982, -4980, -4971, -4947, -4946, -4870, …) flagged by `govulncheck` against the `go 1.25.0` declared in `go.mod`. These vulnerabilities pre-existed Phase 10 — they were masked because the lint step always failed first, terminating the lint-test-arch-govulncheck job before vulnerability scan ran. Phase 10's gate restoration exposed them.

**Routed to v1.7** as a separate phase (recommended scope: bump Go toolchain pin to the latest 1.25.x or 1.26.x patch series and verify `govulncheck ./...` exits 0). Not blocking the v1.6 milestone close — Phase 10's contract was "lint gate restored", not "all CI steps green".

## LINT-03 evidence — per-category decision record

Consolidated from Plans 10-01, 10-02, 10-03. Every baseline lint category has a documented policy:

| Linter | Baseline count | Policy | Reference |
|--------|----------------|--------|-----------|
| staticcheck (QF1001) | 3 (+2 unmasked) | Mechanical De Morgan rewrite where it improves clarity; scoped `//nolint:staticcheck` where rewrite reduces clarity. | 10-01-PLAN Task 1, 10-03-PLAN Task 3 |
| unused | 4 | Delete dead code unconditionally. | 10-01-PLAN Task 2 |
| revive (redefines-builtin-id) | 3 | Rename shadowing identifiers (`min`/`cap`). | 10-01-PLAN Task 3 |
| revive (exported stutters + unexported-return + godoc form) | 6 | Scoped `//nolint:revive` for stutter renames and unexported-return when the API rename ripples through >5 callers — defer the rename to a dedicated API phase. Fix the godoc form trivially. | 10-03-PLAN Task 3 |
| gosec G301 | 2 | Tighten dir perms 0o755 → 0o750. | 10-01-PLAN Task 4 |
| gosec G703 (initial + 2 unmasked) | 3 | Operator-controlled boot-time paths (install dir, config dir) — scoped `//nolint:gosec // G703 — operator-controlled boot path, not request-time`. | 10-03-PLAN Task 1 |
| gosec G705 (XSS) | 2 | Targeted fix: switch `fmt.Fprintf(w, "{...}", source)` → `json.NewEncoder(w).Encode(...)` so the source param is quote-escaped through json.Marshal. | 10-03-PLAN Task 1 |
| noctx | 4 (+1 in-scope drift) | Mechanical swap: `NewRequest` → `NewRequestWithContext`, `exec.Command` → `exec.CommandContext`. | 10-01-PLAN Task 5 |
| wrapcheck | 9 | Wrap errors at package boundaries with `fmt.Errorf("<context>: %w", err)`. Adapter SessionRegistry.Get sites, ACP syscall.Kill, engine PreHook/PostHook, plugin/pii json.Marshal. | 10-02-PLAN Task 1 |
| unparam | 13 | 2 production fixes (drop unused params in `internal/adapter/anthropic/sse.go:497` aggregatedResponse and `internal/plugin/pii/pii.go:259` acceptNERSpans). 11 scoped `//nolint:unparam` for test helpers where signature stability for future expansion or interface-method satisfaction is the rationale. | 10-02-PLAN Tasks 2-3 |
| bodyclose | 1 | Scoped `//nolint:bodyclose` exemption — the receiver-site defer was already present; the lint hit was a cross-channel ownership-analysis limitation. | 10-03-PLAN Task 2 |
| nilerr | 1 | Scoped `//nolint:nilerr` exemption with rationale documenting the intentional nil-return pattern in the test fake. | 10-03-PLAN Task 2 |

Every `//nolint:` directive added across Waves 1-3 carries a `// <rationale>` segment — verified by `grep "nolint:" -r --include="*.go" | grep -v "//.*//"`.

## Must-haves satisfied

- [x] `continue-on-error: true` removed from `.github/workflows/ci.yml` golangci-lint step
- [x] The TODO comment from `f3a70fc` is removed
- [x] CI run on `main` post-Wave-4 passes golangci-lint (run 27080012241: `0 issues`)
- [x] A throwaway branch with a deliberate lint violation FAILS the CI lint job (run 27080014440: `1 issues: unused: 1`)
- [x] Phase 10 LINT-03 evidence table consolidated (above)

## Files modified

- `.github/workflows/ci.yml` — removed `continue-on-error: true` + TODO comment block; bumped `GOLANGCI_LINT_VERSION` and `golangci/golangci-lint-action`
- `.golangci.yml` — migrated `wrapcheck.ignoreSigs` to v2 schema `wrapcheck.extra-ignore-sigs`
- `internal/plugin/request_id.go` — removed trailing blank line (gofumpt drift from Wave 1)

## Status

**LINT-02 closed.** Phase 10 ready for orchestrator verification + roadmap progress update.
