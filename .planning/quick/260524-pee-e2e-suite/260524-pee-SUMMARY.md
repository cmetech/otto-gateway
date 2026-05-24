---
phase: quick-260524-pee
plan: 01
subsystem: testing
tags: [e2e, anthropic, sse, kiro-cli, go-test-json, makefile, anthropic-sdk]

requires:
  - phase: 03.1-anthropic-surface
    provides: /v1/messages Anthropic surface + strict SSE framing the e2e suite drives
provides:
  - Automated black-box E2E suite that boots the real otto-gateway binary against real kiro-cli over HTTP
  - go-test-json -> markdown report renderer
  - Opt-in Node @anthropic-ai/sdk round-trip harness (HUMAN-UAT steps 4-5)
  - make e2e + make e2e-sdk-setup targets
affects: [Phase 4 streaming, future UAT automation, CI gating decisions]

tech-stack:
  added: ["@anthropic-ai/sdk ^0.90.0 (opt-in, NOT in go.mod)"]
  patterns:
    - "Build-tag + env-gate double isolation (//go:build e2e + OTTO_E2E=1) keeps default test path clean"
    - "Black-box HTTP suite: external test package, stdlib-only, never imports internal/*"
    - "Real-binary subprocess boot with /health poll + early-exit detection + skip-on-warmup-failure"
    - "POSIX-portable test-exit-code preservation via tmpfile rc capture (no bash PIPESTATUS)"

key-files:
  created:
    - tests/e2e/e2e_test.go
    - tests/e2e/cmd/report/main.go
    - tests/e2e/sdk/package.json
    - tests/e2e/sdk/sdk_roundtrip.mjs
    - tests/e2e/sdk/README.md
  modified:
    - Makefile
    - .gitignore

key-decisions:
  - "E2E behind //go:build e2e AND OTTO_E2E=1 so go test ./... never compiles or runs it"
  - "Report renderer is untagged (package main) so go run works under the default toolchain"
  - "Test exit code captured to a tmpfile rc and re-raised after the report renders (POSIX-portable)"
  - "Node SDK harness is opt-in; node_modules gitignored, source files tracked"
  - "Stdlib-only Go: no new go.mod dependencies (preserves no-cgo / minimal-deps constraint)"

patterns-established:
  - "Mirrored the Anthropic integration_test.go strict SSE frameState state machine against a real binary subprocess instead of httptest"
  - "gateOrSkip / resolveKiro split so gate-off and kiro-missing produce distinct skip reasons"

requirements-completed: []

duration: ~30min
completed: 2026-05-24
---

# Quick Task 260524-pee: E2E Test Suite Summary

**A one-command `make e2e` that boots the real otto-gateway binary against real kiro-cli, drives the Anthropic surface over HTTP (health, auth, non-streaming + streaming with strict SSE framing, surface gating, fail-fast), and always emits a reviewable markdown report — while the default `go test ./...` / `make test` / `make ci` paths stay untouched.**

## Performance

- **Duration:** ~30 min
- **Tasks:** 4/4
- **Files created:** 5
- **Files modified:** 2

## Accomplishments

1. **`tests/e2e/e2e_test.go`** — `//go:build e2e` + `package e2e_test` (external, black-box, stdlib-only, never imports `internal/*`). `TestMain` builds a temp binary only when `OTTO_E2E=1`; `bootGateway` starts the real binary on a free loopback port, polls `/health`, detects early exit, and skips on warmup failure (kiro auth-not-refreshed). Covers HUMAN-UAT steps 1, 2, 3, 6:
   - `TestE2E_SharedGateway` (one boot, shared subtests): Health 200+JSON, Unauthorized 401, NonStreaming x-api-key + bearer (type/role/stop_reason/content[0].text asserted), Streaming_SSE (strict `event:`/`data:`/blank frame state machine mirrored from `integration_test.go` + canonical event ordering + no error event).
   - `TestE2E_SurfaceGating_OllamaOnly`: `/v1/messages` -> 404 when anthropic disabled; `/api/chat` -> any non-404 (route mounted).
   - `TestE2E_SurfaceGating_TypoFailFast`: `ENABLED_SURFACES=anthrpic` exits non-zero and stderr names `anthrpic`.
   - `TestE2E_SDK_RoundTrip`: opt-in; skips when node or harness absent.
2. **`tests/e2e/cmd/report/main.go`** — untagged stdlib renderer; reads `go test -json` NDJSON from stdin, folds per-test terminal state, emits emoji-free markdown (header timestamp + version + counts, `| Test | Result | Duration |` table, `## Failures` section with captured output). Best-effort `git describe` version label; never exits non-zero.
3. **`tests/e2e/sdk/`** — opt-in Node harness: `package.json` (private ESM pinning `@anthropic-ai/sdk ^0.90.0`), `sdk_roundtrip.mjs` (non-streaming + streaming round-trip, exits 0/1), `README.md`.
4. **Makefile + .gitignore** — `e2e` (depends on `build`, always renders a report, preserves test exit code via tmpfile rc — no bash PIPESTATUS) and `e2e-sdk-setup` targets; both auto-listed by `make help`; neither wired into `all`/`ci`. `.gitignore` ignores `tests/e2e/reports/` + `tests/e2e/sdk/node_modules/`, keeps SDK source tracked.

## How to run

- **Run the E2E suite (real binary + real kiro):** `make build && OTTO_E2E=1 make e2e`
  - Writes `tests/e2e/reports/REPORT-<timestamp>.md` and `tests/e2e/reports/LATEST.md` regardless of pass/fail; the recipe re-raises go test's exit code after rendering.
  - `OTTO_KIRO_BIN=/path/to/kiro-cli` overrides PATH lookup.
- **Enable the Node SDK harness (HUMAN-UAT steps 4-5):** `make e2e-sdk-setup` (runs `pnpm install || npm install` in `tests/e2e/sdk`). Then `make e2e` auto-runs `TestE2E_SDK_RoundTrip`; or force it with `OTTO_E2E_SDK=1`.
- **Default workflow is unchanged:** `make test` / `go test ./...` never compile or run the e2e file (behind the `e2e` build tag) and never touch node.

## Graceful-skip behavior

- **Gate off** (`OTTO_E2E` unset): every test self-skips in ~0s; `TestMain` builds nothing. `go test -tags e2e ./tests/e2e/` reports `ok` with all SKIP.
- **kiro missing** (`OTTO_E2E=1` but no kiro-cli on PATH and no `OTTO_KIRO_BIN`): tests skip with "kiro-cli not on PATH".
- **Warmup failure** (kiro auth-not-refreshed): `bootGateway` captures stderr and `t.Skipf`s — not a failure (mirrors `kiroSetup` policy).
- **SDK harness absent** (no node or no `node_modules` and `OTTO_E2E_SDK` unset): `TestE2E_SDK_RoundTrip` skips with a pointer to `make e2e-sdk-setup`.

## Acceptance gate results

| Gate | Command | Result |
|------|---------|--------|
| 1 | `go build ./...` | PASS (clean; e2e file not compiled) |
| 2 | `go vet ./...` | PASS (clean) |
| 3 | `go test ./... -race -count=1` | PASS (all green); `otto-gateway/tests/e2e` test package ABSENT from `go list ./...` (only present under `-tags e2e`) |
| 4 | `go vet -tags e2e ./tests/e2e/...` | PASS (e2e compiles under its tag) |
| 5 | `OTTO_E2E= go test -tags e2e ./tests/e2e/` | PASS — all 4 tests SKIP cleanly (no kiro, no binary build, no node, no hang) |
| 6 | `go test -json ./internal/version/ \| go run ./tests/e2e/cmd/report` | PASS — valid markdown with `\| Test \| Result \| Duration \|`; synthetic pass/fail/skip stream also produced a `## Failures` section |
| 7a | `golangci-lint run ./...` | PASS — 0 issues |
| 7b | `make test` | PASS — green; recipe still `go test ./...`, does NOT run e2e |
| extra | `make help` | lists `e2e` and `e2e-sdk-setup` |
| extra | `make -n e2e` | shows build + `OTTO_E2E=1 go test -tags e2e -json` + `go run ./tests/e2e/cmd/report` + rc-preserving `exit` |
| extra | `git check-ignore tests/e2e/reports/x tests/e2e/sdk/node_modules/x` | both ignored; `package.json` NOT ignored |

> NOTE: the live `OTTO_E2E=1 make e2e` against real kiro-cli was deliberately NOT run by the executor (per task constraints — the orchestrator runs it afterward). This summary proves the suite compiles, gates/skips correctly, and the renderer + Makefile wiring work.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `make help` awk regex excluded digit-containing target names**
- **Found during:** Task 4 verification (`make help` did not list `e2e` / `e2e-sdk-setup`).
- **Issue:** The existing help recipe used `^[a-zA-Z_-]+:` which does not match target names containing digits (`e2e`, `e2e-sdk-setup`), so the plan's "auto-listed by make help" requirement failed.
- **Fix:** Widened the regex to `^[a-zA-Z0-9_-]+:`. Both new targets now list; all pre-existing targets still list unchanged.
- **Files modified:** Makefile (help target)
- **Commit:** a57cbf5

**2. [Rule 3 - Blocking] noctx trust gate required context on all HTTP/exec calls**
- **Found during:** Task 1 lint (`golangci-lint --build-tags e2e`).
- **Issue:** The project's `noctx` linter (a non-negotiable trust gate) is NOT in the `_test.go` exclusion list, so context-less `http.NewRequest`, `client.Get`, `net.Listen`, and `exec.Command` calls failed lint.
- **Fix:** Threaded `context.Context` through every HTTP request (`NewRequestWithContext` + `client.Do`), used `net.ListenConfig.Listen`, and `exec.CommandContext` for all subprocess spawns. Added one justified `//nolint:gosec` on the `TestMain` temp-binary build (`G204` — paths are test-controlled `os.MkdirTemp` output, not external input); the default `golangci-lint run ./...` is 0 issues regardless since the file is behind the `e2e` tag.
- **Files modified:** tests/e2e/e2e_test.go
- **Commit:** 466bd88

## Known Stubs

None. The suite intentionally does NOT assert a full Ollama round-trip in `TestE2E_SurfaceGating_OllamaOnly` (any non-404 proves the route is mounted) — this is a documented scope choice in the plan, not a stub.

## Self-Check: PASSED

- tests/e2e/e2e_test.go — FOUND
- tests/e2e/cmd/report/main.go — FOUND
- tests/e2e/sdk/package.json — FOUND
- tests/e2e/sdk/sdk_roundtrip.mjs — FOUND
- tests/e2e/sdk/README.md — FOUND
- Makefile / .gitignore — modified
- Commits 466bd88, a0dc868, 1413a4e, a57cbf5 — all present in git log
