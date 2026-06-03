---
quick_id: 260603-bxf
slug: resolve-pre-existing-tech-debt-gofmt-dri
date: 2026-06-03
description: "Resolve pre-existing tech debt surfaced by Phase 8.2 trust gates"
type: chore
---

# Quick Task — Resolve Pre-Existing CI Tech Debt

## Context

Phase 8.2 (Ollama format parity) shipped clean — all phase packages build,
test, and vet — but `make ci` reveals two pre-existing tech-debt items that
have been latent on `main` and were surfaced by running the full trust-gate
sequence after the merge. Both predate Phase 8.2:

- **gofmt drift** in 9 files under `tests/e2e/` (last touched in Phase 6
  commit `6da8de4`). `gofmt -l` reports comment-alignment normalization
  diffs across `tools_*_test.go`, `pool_sessions_e2e_test.go`,
  `multi_worker_failures_e2e_test.go`, and `cmd/fake-kiro-cli/main.go`.

- **Go 1.24-only API in a Go 1.23 module.** `internal/admin/tail_test.go`
  (11 sites) and `internal/admin/tail_timberjack_test.go` (1 site) call
  `t.Context()`, which was added in Go 1.24. `go.mod` declares `go 1.23`,
  so `go vet ./...` fails with `testing.Context requires go1.24 or later`.

Both confirmed pre-existing on commit `468e231` (pre-Phase-8.2 main).

## Decisions

- **D-1: Bump `go.mod` to `go 1.24`** rather than rewriting tests to drop
  `t.Context()`. CLAUDE.md states the tech-stack constraint as "Go 1.23+",
  so 1.24 is within scope. The test code already implicitly requires 1.24;
  `go.mod` was the laggard. The rewrite alternative loses `t.Context()`'s
  auto-cancel-on-cleanup semantics and adds boilerplate at 12 sites.
- **D-2: Two atomic commits, not one.** Each fix is independently
  bisectable — gofmt drift is a stylistic normalization; the go.mod bump
  is a substantive version-floor change.
- **D-3: No `gofmt -w` over the whole repo.** Scope strictly to
  `tests/e2e/` where `gofmt -l` reports diffs today. Touching anything
  else would mix unrelated changes into this chore.

## Tasks

### Task 1 — `gofmt -w tests/e2e/`
- Run `gofmt -w tests/e2e/` to normalize comment alignment.
- Verify `gofmt -l tests/e2e/` returns empty.
- Verify `gofmt -l .` (full repo) returns empty.
- Verify `go build ./...` still passes (no behavior change expected).
- Commit: `chore(tests): gofmt -w tests/e2e/ to clear comment-alignment drift`

### Task 2 — Bump `go.mod` from `go 1.23` to `go 1.24`
- Edit `go.mod`: `go 1.23` → `go 1.24`.
- If a `toolchain` directive exists, leave it alone (it pins the toolchain
  version, not the language version).
- Verify `go vet ./...` passes clean (the `testing.Context requires go1.24`
  errors should be gone).
- Verify `go build ./...` passes.
- Verify `make test-race` still passes on at least the previously-clean
  packages (admin, jsonformat, ollama, engine, config).
- Commit: `chore(go): bump go.mod to go 1.24 for t.Context() in admin tests`

## Verification

- `go vet ./...` → clean (no `testing.Context` errors)
- `gofmt -l .` → empty (no formatting drift)
- `go build ./...` → succeeds
- `make test-race` → all packages OK
- `make ci` → ideally passes end-to-end (modulo `golangci-lint` /
  `gofumpt` not being installed on this machine, which is a separate
  developer-tooling issue, not a CI gate issue)

## Out of Scope

- Repo-wide `gofmt -w .` — only `tests/e2e/` is currently dirty.
- Installing `gofumpt` / `golangci-lint` on the dev machine — separate
  tooling-setup concern, not a code change.
- Refactoring `t.Context()` call sites — D-1 covers the alternative.
