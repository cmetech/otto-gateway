# Phase 08.1 Deferred Items

Items discovered during execution that are out of scope for the current
task and need follow-up work in a later plan/phase.


## Plan 03 — fmt-check surfaces pre-existing gofumpt drift

Discovered by: Plan 03 (Makefile + CI WARNING-01 close-out), 2026-05-28

When the new `make fmt-check` target was first run on this worktree
checkout, gofumpt reported formatting drift in 16+ existing files
across `cmd/`, `internal/adapter/{anthropic,ollama,openai}/`, including:

- cmd/otto-gateway/main.go
- cmd/otto-gateway/testmain_test.go
- internal/adapter/anthropic/handlers_session_test.go
- internal/adapter/anthropic/handlers_test.go
- internal/adapter/anthropic/integration_test.go
- internal/adapter/ollama/handlers_shortcircuit_test.go
- internal/adapter/ollama/integration_test.go
- internal/adapter/ollama/ndjson.go
- internal/adapter/ollama/ndjson_test.go
- internal/adapter/openai/handlers_shortcircuit_test.go
- internal/adapter/openai/integration_test.go
- internal/adapter/openai/render.go
- internal/adapter/openai/sse.go
- internal/adapter/openai/sse_golden_test.go
- internal/adapter/openai/sse_test.go
- internal/adapter/openai/wire.go

This is **NOT caused by Plan 03**. Plan 03 only adds the explicit
`fmt-check` target so the brief §3.12 step is visible in the Makefile;
the underlying drift predates Phase 08.1 (most files are from Phases
2/3.1/8). The fmt-check target is working as designed — surfacing
drift is precisely what WARNING-01 asked for.

**Action:** A separate plan/phase should run `make fmt` (which calls
`gofumpt -w .`) across the tree as a single mechanical commit, then
the new `make ci` gate will block any future drift.

Plan 03's parallel-execution constraints explicitly forbid touching
files outside Makefile + .github/workflows/ci.yml, so this fix cannot
land in this plan.

## Plan 03 — vet surfaces pre-existing testing.Context Go-version mismatch

Discovered by: Plan 03, 2026-05-28

When the new `make vet` target was first run, go vet reported:

  internal/admin/tail_test.go:457:28: testing.Context requires go1.24 or later (module is go1.23)
  internal/admin/tail_test.go:502:32: testing.Context requires go1.24 or later (module is go1.23)
  internal/admin/tail_test.go:507:32: testing.Context requires go1.24 or later (module is go1.23)
  internal/admin/tail_timberjack_test.go:54:28: testing.Context requires go1.24 or later (module is go1.23)

go.mod declares `go 1.23` but test code calls `testing.Context()` which
is a Go 1.24+ stdlib addition. Pre-existing from Phase 6.1 (admin UI).

This is **NOT caused by Plan 03**. The vet step is working as designed —
exactly the brief §3.12 step 2 finding that golangci-lint's govet
linter would *also* surface, just under a different label.

**Action:** A separate plan should either bump go.mod's `go` directive
to 1.24 or rewrite the calls to not use testing.Context. Plan 03's
scope boundary forbids modifying internal/admin/*.

Plan 03 ships the explicit `vet` target; surfacing real issues was the
expected outcome.
