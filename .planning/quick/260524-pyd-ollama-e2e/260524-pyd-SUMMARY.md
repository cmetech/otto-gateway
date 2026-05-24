---
phase: quick-260524-pyd
plan: 01
subsystem: tests/e2e
tags: [e2e, ollama, contract-test, langflow]
requires:
  - tests/e2e/e2e_test.go (shared helpers: bootGateway, gateOrSkip, resolveKiro, freePort, readAll, TestMain)
  - internal/adapter/ollama (wire shapes under test, not modified)
provides:
  - Ollama API contract E2E coverage (6 subtests) behind e2e tag + OTTO_E2E gate
affects:
  - tests/e2e suite
tech-stack:
  added: []
  patterns:
    - "context-bounded request helper (t.Cleanup(cancel)) for the noctx trust gate"
    - "single-JSON-object proof via second json.Decoder.Decode == io.EOF"
key-files:
  created:
    - tests/e2e/ollama_e2e_test.go
  modified:
    - tests/e2e/README.md
decisions:
  - "Assert the Phase-2 non-streaming contract PLUS a stream:true→non-stream downgrade guard so a future Phase-4 NDJSON change must update Chat_StreamDowngrade deliberately."
  - "Assert only deterministic JSON fields (name/model/details.format/details.family, message.role/content, done, done_reason, response); skip durations, *_eval_count, digest, size, modified_at."
metrics:
  duration: ~10m
  tasks: 2
  files: 2
  completed: 2026-05-24
---

# Phase quick-260524-pyd Plan 01: Ollama E2E Contract Coverage Summary

Added 6 Ollama API contract subtests to the existing E2E suite — locking the LangFlow-facing wire shape at HTTP fidelity against the real binary + real kiro — plus a `stream:true` silent-downgrade guard that forces a deliberate update when Phase 4 lands NDJSON streaming.

## What Was Built

A new `tests/e2e/ollama_e2e_test.go` (package `e2e_test`, behind `//go:build e2e` + the `OTTO_E2E=1` env gate), reusing the shared helpers (`bootGateway`, `gateOrSkip`, `resolveKiro`, `freePort`, `readAll`, `TestMain`, `moduleRoot`) from `e2e_test.go` with no redeclaration. One top-level `TestE2E_Ollama` boots a single gateway and runs six `t.Run` subtests over that shared warmup. A file-scope `ollamaRequest` helper builds context-bounded requests (60s, `t.Cleanup(cancel)` for the noctx trust gate), sets `Content-Type: application/json` for bodies, and applies the `Authorization: Bearer e2e-token` header.

### The 6 Ollama subtests

1. **VersionAuthExempt** — `GET /api/version` with NO auth → 200; asserts `version` + `commit` keys present (AUTH-03 exemption; version endpoint is on the outer unauthenticated router so LangFlow can probe without creds). Values are build-dependent, so only key presence is asserted.
2. **Unauthorized** — `POST /api/chat` with NO auth → 401 (auth rejects before kiro is touched; no warmup dependency).
3. **Tags** — `GET /api/tags` (Bearer) → 200; `models[]` non-empty, contains an entry named `auto` (always prepended by `handleTags`), and for that entry `name`, `model`, `details.format`, `details.family` are non-empty. `digest`/`size`/`modified_at` are intentionally NOT asserted (`digest` is `""`, `size` is `0`).
4. **Chat_NonStreaming** — `POST /api/chat` (Bearer, `stream:false`) → 200; `Content-Type` starts with `application/json` and is NOT `application/x-ndjson`; a single JSON object decodes (second `Decode` == `io.EOF`) with `model=="auto"`, `message.role=="assistant"`, `message.content` non-empty, `done==true`, `done_reason ∈ {stop,length}`. Real kiro path; inherits `bootGateway` warmup-skip.
5. **Chat_StreamDowngrade** — `POST /api/chat` (Bearer, `stream:true`) → 200; asserts the Phase-2 silent `stream:true → non-stream` downgrade (`handlers.go handleChat` sets `wire.Stream=false` for Node parity): `Content-Type` is `application/json` (NOT `application/x-ndjson`), a single JSON object (second `Decode` == `io.EOF`), `done==true`.
6. **Generate_NonStreaming** — `POST /api/generate` (Bearer, `stream:false`) → 200; single JSON object whose assistant text lives in `response` (NOT `message{}`, per `render.go generateResponseToWire`); asserts `response` non-empty and `done==true`.

`tests/e2e/README.md` gained 6 `TestE2E_Ollama/*` rows in the "What it covers" table (each mapped to its Ollama contract / LangFlow usage) plus a Phase-4 NDJSON note. Existing rows unchanged.

## Stream-Downgrade Rationale + Phase-4 Note

The current Ollama contract is **non-streaming** — NDJSON streaming is **Phase 4**. `handleChat`/`handleGenerate` honor only non-streaming and silently set `wire.Stream=false` when a client sends `stream:true` (Node-parity behaviour). `Chat_StreamDowngrade` pins this: it asserts a `stream:true` request still produces a single `application/json` object, NOT `application/x-ndjson` multi-line frames. Its prominent in-code comment (and the README Phase-4 note) flag that **this subtest MUST be changed to expect `application/x-ndjson` multi-line frames when Phase 4 lands NDJSON streaming** — its failure on a Phase-4 change is intentional, forcing a deliberate update rather than a silent contract drift.

## Run / Report (unchanged)

The run and reporting workflow is unchanged by this plan: `OTTO_E2E=1 make e2e` runs the full suite (now including `TestE2E_Ollama`) and writes `tests/e2e/reports/REPORT-<timestamp>.md` + `tests/e2e/reports/LATEST.md`. With `OTTO_E2E` unset the suite all-skips cheaply.

## Acceptance Gate Results

| Gate | Result |
|------|--------|
| `go build ./...` | clean (BUILD OK) |
| `go vet ./...` | clean (VET OK) |
| `go vet -tags e2e ./tests/e2e/...` | clean (VET-E2E OK) — new file compiles under the tag alongside the existing one; no helper redeclaration |
| `OTTO_E2E= go test -tags e2e ./tests/e2e/` | `ok ... 0.347s` — all skip cleanly (gate-off path; no kiro/hang) |
| `go test ./... -race -count=1` | green across all packages; e2e files excluded from default run (build constraints exclude all Go files in tests/e2e/ without the tag) |
| `golangci-lint run ./...` | 0 issues |
| `golangci-lint run --build-tags e2e ./tests/e2e/...` | 0 issues |

Live `OTTO_E2E=1 make e2e` against real kiro was deliberately NOT run here — the orchestrator runs it after this plan.

## Deviations from Plan

None — plan executed exactly as written. No `internal/` or `cmd/` source modified; stdlib-only imports (`bytes`, `context`, `encoding/json`, `io`, `net/http`, `strings`, `testing`, `time`); no new `go.mod` deps.

## Commits

- `705e897` — test(260524-pyd): add Ollama contract E2E subtests (tests/e2e/ollama_e2e_test.go)
- `49fb09e` — docs(260524-pyd): document Ollama contract E2E subtests in README (tests/e2e/README.md)

## Self-Check: PASSED

- FOUND: tests/e2e/ollama_e2e_test.go
- FOUND: tests/e2e/README.md (6 TestE2E_Ollama rows)
- FOUND: commit 705e897
- FOUND: commit 49fb09e
