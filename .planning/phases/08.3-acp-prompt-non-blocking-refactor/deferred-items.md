# Phase 08.3 — Deferred Items

Out-of-scope findings discovered during Plan 08.3-01 execution that are NOT being fixed in this phase per the GSD executor scope-boundary rule (only fix issues DIRECTLY caused by the current task's changes).

---

## D-08.3-DEFER-01: Pre-existing gofmt drift in internal/plugin/pii/

**Discovered:** During `make ci` invocation at the end of Plan 08.3-01 REFACTOR.

**Files:**
- `internal/plugin/pii/recognizers.go` — column-alignment drift in the MSISDN entry (gofmt wants `Name:     "MSISDN", Pattern:  msisdnRe,` to align with sibling `Validate: nil,`).
- `internal/plugin/pii/recognizers_test.go` — column-alignment drift in MAC + lat/lon test-case comment columns.

**Evidence pre-existing (not introduced by Phase 8.3):**
- `git diff --name-only HEAD~3..HEAD -- internal/plugin/pii/` returns empty (the three Phase 8.3 commits did not touch any pii file).
- `git log -1 --oneline -- internal/plugin/pii/recognizers.go` returns `c724fd2 docs(admin): update UI docs for secure-by-default PII` (pre-Phase-8.3).
- `git log -1 --oneline -- internal/plugin/pii/recognizers_test.go` returns `fc4f103 feat(pii): add SITE recognizer for telecom network elements` (pre-Phase-8.3).

**Why deferred:** Same pattern recorded in quick task `260603-bxf` (resolve-pre-existing-tech-debt-gofmt-drift): the project has un-resolved gofmt drift outside `tests/e2e/` that the team decided to leave to a follow-up rather than mechanically rewriting. Fixing it here would expand Phase 8.3's surface beyond ACP refactoring and would be the same team-decision question (gofumpt? accept gofmt-strict? drop fmt-check gate?) bxf surfaced.

**Recommendation:** Open a dedicated quick task `gofmt -w internal/plugin/pii/` or a phase-level decision on the broader fmt-check policy. Do NOT bundle into Phase 8.3.

---

## D-08.3-DEFER-02: golangci-lint not installed on dev box

**Discovered:** During `make ci` at REFACTOR gate — `make lint` exits with `golangci-lint: No such file or directory`.

**Why deferred:** Environment/tooling concern, not a code issue. The `arch-lint`, `vet`, `build`, `test-race`, and `examples` gates all pass cleanly. The CI pipeline (`.github/workflows/ci.yml`) installs golangci-lint and will exercise it on the worktree branch / phase merge.

**Recommendation:** No action needed in Plan 08.3-01. Rely on the GitHub Actions ci.yml job to enforce golangci-lint on the merged branch. Developer setup docs in `DEVELOPERS.md` already cover the install step.
