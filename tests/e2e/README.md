# OTTO Gateway — End-to-End (E2E) tests

This suite boots the **real compiled `otto-gateway` binary** as a subprocess
against **real `kiro-cli`** and exercises it over HTTP — the highest-fidelity
check we have. It automates the Phase 3.1 HUMAN-UAT acceptance steps so they can
be run on demand instead of by hand.

It is **opt-in and excluded from the default build/test/CI path** (behind a
`//go:build e2e` tag + an `OTTO_E2E=1` env gate), so `make test`, `make test-race`,
`make ci`, and `make build` never run it and never need kiro-cli or Node.

## TL;DR

```bash
make build && OTTO_E2E=1 make e2e
# → writes tests/e2e/reports/REPORT-<timestamp>.md and tests/e2e/reports/LATEST.md
```

To also run the SDK round-trip steps (the real `@anthropic-ai/sdk` parser):

```bash
make e2e-sdk-setup     # one-time: installs the Node harness (pnpm)
OTTO_E2E=1 make e2e    # now runs all 6 UAT steps
```

## Selecting which tests run

`make e2e` runs every group by default. Scope a run (and its report) with
`RUN=<regex>`, passed through to `go test -run`. Discover the group names with
`make e2e-list`.

```bash
make e2e-list                                  # list TestE2E_* groups
make e2e RUN=TestE2E_Ollama                    # one group (Ollama / LangFlow contract)
make e2e RUN=TestE2E_Ollama/Tags               # a single subtest
make e2e RUN='TestE2E_(Ollama|SharedGateway)'  # multiple groups (regex)
make e2e                                       # all groups (RUN defaults to empty → matches everything)
```

Groups available today: `TestE2E_SharedGateway` (Anthropic core),
`TestE2E_SurfaceGating_OllamaOnly`, `TestE2E_SurfaceGating_TypoFailFast`,
`TestE2E_SDK_RoundTrip`, `TestE2E_Ollama`.

## Prerequisites

| What | Needed for | Notes |
|------|------------|-------|
| `kiro-cli` on `PATH`, authenticated | everything | Run `kiro-cli acp` once interactively if auth is stale. Override the binary with `OTTO_KIRO_BIN=/path/to/kiro-cli`. If warmup fails (stale auth), the affected tests **skip** with the reason rather than fail. |
| Go toolchain | everything | The suite builds a throwaway binary from `./cmd/otto-gateway` (it does not depend on `bin/otto-gateway` existing). |
| Node ≥ 18 + the SDK harness | SDK round-trip (UAT steps 4-5) | Install via `make e2e-sdk-setup`. If absent, `TestE2E_SDK_RoundTrip` **skips**. |

## What it covers (→ HUMAN-UAT mapping)

| Test | UAT step | Asserts |
|------|----------|---------|
| `TestE2E_SharedGateway/Health` | 1 | `GET /health` → 200, JSON body |
| `TestE2E_SharedGateway/Unauthorized` | 1 | `POST /v1/messages` no auth → 401 |
| `TestE2E_SharedGateway/NonStreaming_XApiKey` | 2 | 200 + Anthropic Message shape (`x-api-key`) |
| `TestE2E_SharedGateway/NonStreaming_Bearer` | 2 | 200 same shape (`Authorization: Bearer`) — D-15 dual auth |
| `TestE2E_SharedGateway/Streaming_SSE` | 3 | `text/event-stream`; ordered `message_start → content_block_* → message_delta → message_stop`; exact frame byte-framing |
| `TestE2E_SurfaceGating_OllamaOnly` | 6 | `ENABLED_SURFACES=ollama` → `/v1/messages` 404, `/api/chat` works |
| `TestE2E_SurfaceGating_TypoFailFast` | 6 | `ENABLED_SURFACES=anthrpic` → process exits non-zero, stderr names `anthrpic` |
| `TestE2E_SDK_RoundTrip` | 4 + 5 | real `@anthropic-ai/sdk` `messages.create()` + `messages.stream()`/`finalMessage()` parse our wire bytes with no Zod exception |
| `TestE2E_Ollama/VersionAuthExempt` | Ollama contract | `GET /api/version` no auth → 200, has `version` + `commit` (AUTH-03 exemption; LangFlow version probe on the outer unauthenticated router) |
| `TestE2E_Ollama/Unauthorized` | Ollama contract | `POST /api/chat` no auth → 401 (auth rejects before kiro) |
| `TestE2E_Ollama/Tags` | Ollama contract | `GET /api/tags` (Bearer) → 200, non-empty `models[]` incl. `auto`; stable fields only (name, model, details.format/family) — LangFlow model list |
| `TestE2E_Ollama/Chat_NonStreaming` | Ollama contract | `POST /api/chat` (Bearer, stream:false) → 200 `application/json`, single object: model=auto, message.role=assistant, content non-empty, done, done_reason∈{stop,length} (LangFlow chat path, real kiro) |
| `TestE2E_Ollama/Chat_StreamDowngrade` | Ollama contract | `POST /api/chat` stream:true → 200 single JSON object (NOT NDJSON), done — guards the Phase-2 silent stream→non-stream downgrade |
| `TestE2E_Ollama/Generate_NonStreaming` | Ollama contract | `POST /api/generate` (Bearer, stream:false) → 200 single object: `response` non-empty, done (LangFlow generate path, real kiro) |

> **Phase 4 note:** Ollama NDJSON streaming is Phase 4. This suite currently
> asserts the non-streaming contract plus the `stream:true` silent-downgrade
> guard (`Chat_StreamDowngrade`), which must be updated to expect
> `application/x-ndjson` multi-line frames when Phase 4 lands NDJSON streaming.

Each gateway is booted on its own free loopback port; auth/streaming subtests
share one boot for speed, surface-gating cases boot their own.

## Environment knobs

| Var | Default | Purpose |
|-----|---------|---------|
| `OTTO_E2E` | unset | Must be `1` to run the suite at all (otherwise every test skips). |
| `OTTO_KIRO_BIN` | — | Absolute path to `kiro-cli` if not on `PATH`. |
| `OTTO_E2E_SDK` | unset | Force-enable the SDK round-trip even without `node_modules` present (it still needs `node` + the SDK installed). |

The gateway under test is launched with `AUTH_TOKEN=e2e-token`,
`KIRO_CMD=<resolved kiro>`, and `HTTP_ADDR=127.0.0.1:<free-port>`.

## Reports

Each run writes `tests/e2e/reports/REPORT-<timestamp>.md` and overwrites
`tests/e2e/reports/LATEST.md` (timestamp, gateway version, pass/fail/skip
counts, a per-test table, and captured output for any failures). The
`reports/` directory is gitignored. `make e2e` always renders the report —
even on failure — and exits with the test's exit code.

## Running without `make`

```bash
OTTO_E2E=1 go test -tags e2e -v ./tests/e2e/
# render a report from the JSON event stream:
OTTO_E2E=1 go test -tags e2e -json ./tests/e2e/ | go run ./tests/e2e/cmd/report
```

## Layout

```
tests/e2e/
  e2e_test.go          # the suite (//go:build e2e)
  cmd/report/main.go   # go-test-json → markdown renderer (stdlib only)
  sdk/                 # opt-in Node harness for steps 4-5
    package.json       # pins @anthropic-ai/sdk ^0.90.0 (matches loop24-client)
    sdk_roundtrip.mjs  # messages.create + messages.stream round-trip
    README.md
  reports/             # generated, gitignored
```
