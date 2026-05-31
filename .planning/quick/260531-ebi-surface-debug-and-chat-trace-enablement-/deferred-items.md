# Deferred Items — quick 260531-ebi

Out-of-scope, pre-existing findings discovered during execution. These are
NOT caused by this plan's changes (debug/chat_trace surfacing) and were NOT
fixed, per the executor SCOPE BOUNDARY rule.

## Pre-existing `go vet` / govet (stdversion) failures

- `internal/admin/tail_test.go` (lines 165, 192, 193, 232, 282, 334, 396, 458, 503, 508)
- `internal/admin/tail_timberjack_test.go` (line 54)

All use `t.Context()` (`testing.Context`) which requires go1.24, while `go.mod`
declares `go 1.23`. `go test` passes (local toolchain is go1.26.3), but `go vet`
and golangci-lint's `govet/stdversion` analyzer flag it against the module's
declared Go version. Files are unmodified by this plan. Fix is either bumping
the `go.mod` directive to 1.24 or replacing `t.Context()` with an explicit
`context.Background()` in those tests — neither is in scope here.

## Pre-existing gosec / golangci-lint findings (untouched files)

Surfaced by locally-installed dev/v2.12 golangci-lint + gosec (newer than the
repo's pinned CI versions, which carry test-file exclusion rules and lack the
newest G703/G705 taint analyzers):

- `internal/admin/admin.go:137` — G703 path-traversal taint on `http.ServeFileFS`
  (the static asset server, pre-existing; not part of this plan's diff).
- `internal/admin/sse.go:91` — G705 XSS taint on `fmt.Fprintf` error body
  (pre-existing SSE handler).
- `cmd/otto-gateway/main.go:855` — G301 dir perms 0o755 (pre-existing).
- `internal/admin/sse_test.go`, `internal/admin/tail_test.go` — errcheck/gosec/G304
  on test fixtures. The repo's `.golangci.yml` excludes errcheck/gosec/G304 in
  `_test.go` via `issues.exclude-rules`; the locally-installed linter minor
  version reads that block differently, so the exclusions did not apply locally.

None of these reference any line added by this plan. Verified via
`git diff HEAD` — all added lines are struct fields, doc comments, and
assignments with zero lint/security impact.
