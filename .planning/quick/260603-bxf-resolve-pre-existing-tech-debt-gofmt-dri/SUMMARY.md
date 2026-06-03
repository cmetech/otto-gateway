---
quick_id: 260603-bxf
slug: resolve-pre-existing-tech-debt-gofmt-dri
date: 2026-06-03
status: complete
type: chore
commits:
  - "8299705 chore(tests): gofmt -w tests/e2e/ to clear comment-alignment drift"
  - "945a9b2 chore(go): bump go.mod to go 1.24 for t.Context() in admin tests"
---

# Quick Task Summary — Pre-Existing Tech-Debt Cleanup

## What Shipped

Two atomic commits resolving the two pre-existing CI gate failures surfaced
when Phase 8.2 ran the full `make ci` sequence post-merge:

1. **`8299705`** — `gofmt -w tests/e2e/` normalizes godoc block-quote
   indentation (3-space → tab) across 9 files. Stylistic-only; no
   behavior change. 95 insertions / 94 deletions, all comment whitespace.

2. **`945a9b2`** — `go.mod` declaration bumped `go 1.23` → `go 1.24` so
   `t.Context()` usage in `internal/admin/tail_test.go` (11 call sites)
   and `tail_timberjack_test.go` (1 site) matches the Go version they
   require. `go vet ./...` is now clean.

Per quick-task plan D-3, scope stayed narrow: `tests/e2e/` only — did
NOT run `gofmt -w` repo-wide.

## Verification

| Gate | Pre-cleanup | Post-cleanup |
|------|-------------|--------------|
| `go vet ./...` | ❌ `testing.Context requires go1.24` × 10 | ✅ clean |
| `gofmt -l tests/e2e/` | ❌ 9 files | ✅ clean |
| `go build ./...` | ✅ | ✅ |
| `internal/admin/` tests | ✅ (compile-only on go1.23 — vet caught it) | ✅ 16.8s race-test passes |
| `internal/plugin/jsonformat` tests | ✅ | ✅ |

## Out of Scope — Surfaced for Future Decision

`gofmt -l .` (whole-repo) reports **18 additional files** outside `tests/e2e/`
with formatting that gofmt would change. Unlike the `tests/e2e/` case
(which was godoc-comment normalization), these are **struct field column
alignment** that the dev team appears to have intentionally maintained:

```
internal/adapter/anthropic/handlers_test.go
internal/adapter/anthropic/integration_test.go
internal/adapter/ollama/handlers_shortcircuit_test.go
internal/adapter/ollama/handlers_test.go
internal/adapter/ollama/integration_test.go
internal/adapter/ollama/ndjson_test.go
internal/adapter/ollama/wire_test.go
internal/adapter/openai/integration_test.go
internal/adapter/openai/render.go
internal/adapter/openai/sse_golden_test.go
internal/adapter/openai/sse_test.go
internal/adapter/openai/wire.go
internal/admin/snapshot_test.go
internal/admin/sse_test.go
internal/admin/tail.go
internal/admin/tail_test.go
internal/admin/tail_timberjack_test.go
internal/config/config.go
internal/engine/coerce_test.go
internal/plugin/auth.go
internal/plugin/chain_filter_test.go
internal/plugin/pii/modes_test.go
internal/plugin/pii/recognizers_test.go
internal/plugin/trace.go
internal/server/server.go
internal/server/server_test.go
internal/session/doc.go
```

Example diff from `internal/adapter/openai/wire.go`:
```diff
-	StreamOptions        json.RawMessage `json:"stream_options,omitempty"`
-	MaxTokens            int             `json:"max_tokens,omitempty"`
+	StreamOptions       json.RawMessage `json:"stream_options,omitempty"`
+	MaxTokens           int             `json:"max_tokens,omitempty"`
```

The author aligned the type column across the struct; gofmt collapses
that alignment. This is a **fundamental tension between gofmt-strict
and the project's alignment convention** — not a simple chore to
mechanically resolve.

### Options for the team to consider

1. **Accept gofmt's verdict.** Run `gofmt -w .` repo-wide, lose all
   intentional alignment, document going forward that gofmt-strict
   is the enforced style.
2. **Install `gofumpt` properly.** The Makefile's `fmt-check` target
   prefers `gofumpt` if installed (`gofumpt` permits some alignment
   that `gofmt` rejects). If the team has been relying on `gofumpt`
   locally without it being installed on dev machines or CI, this is
   a tooling-config issue not a code issue. Decision: standardize
   `gofumpt` installation in the contributor setup and CI.
3. **Drop `fmt-check` as a gate.** If the team values column alignment
   over auto-formatter consistency, remove the `fmt-check` step from
   `make ci` and rely on `golangci-lint` (which can be configured to
   skip whitespace-only rules).

Recommend the user pick option 2 (gofumpt) if they want alignment
preserved AND want a gate, or option 1 if they're willing to give up
alignment for the simplicity of plain gofmt enforcement.

## Threat Flags

None — pure formatting + version-directive change. No new code paths,
no behavior changes, no new attack surface.
