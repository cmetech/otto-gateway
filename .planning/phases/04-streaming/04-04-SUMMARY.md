---
phase: "04-streaming"
plan: "04"
subsystem: "e2e ratification — OpenAI SSE + Anthropic SSE"
tags: ["e2e", "strm-02", "strm-03", "strm-05", "openai-sse", "anthropic-sse", "ratification"]
dependency_graph:
  requires:
    - "04-01: StopWatchdog teardown in openai/sse.go + anthropic/sse.go; D-07 derived ctx in handlers"
    - "04-02: Ollama NDJSON E2E (Chat_Streaming, Generate_Streaming, Chat_DisconnectSmoke)"
    - "04-03: STRM-04 watchdog unit + ACP cancel frame integration tests"
  provides:
    - "TestE2E_Anthropic/Messages_Streaming — Anthropic SSE STRM-02 + STRM-03 ratification"
    - "TestE2E_Anthropic/Messages_NonStreaming — Anthropic stream:false STRM-05 ratification"
    - "STRM-02/STRM-03/STRM-05 comments in TestE2E_OpenAI/ChatCompletions_Streaming + ChatCompletions_NonStreaming"
  affects:
    - "tests/e2e/anthropic_e2e_test.go"
    - "tests/e2e/openai_e2e_test.go"
tech_stack:
  added: []
  patterns:
    - "assertStrictSSE reused from e2e_test.go for Anthropic event sequence validation (message_start→message_stop)"
    - "postMessages reused from e2e_test.go for Anthropic request building (x-api-key + Bearer auth paths)"
    - "json.Decoder second-call io.EOF pattern proves single-JSON-object non-streaming response"
    - "Absent stream field → false routing confirmed for both OpenAI and Anthropic (bool absent=false per both public specs)"
key_files:
  created:
    - "tests/e2e/anthropic_e2e_test.go — TestE2E_Anthropic with Messages_Streaming + Messages_NonStreaming subtests"
  modified:
    - "tests/e2e/openai_e2e_test.go — added STRM-02/STRM-03/STRM-05 ratification comments to ChatCompletions_Streaming + ChatCompletions_NonStreaming"
decisions:
  - "ChatCompletions_Streaming and ChatCompletions_NonStreaming confirmed existing in openai_e2e_test.go from Phase 3 — plan specified confirm-or-add; confirmed + comments added only"
  - "TestE2E_SharedGateway/Streaming_SSE already covers Anthropic SSE but under a non-plan-required name; added dedicated TestE2E_Anthropic per plan ratification spec without removing existing coverage"
  - "assertStrictSSE reused for Messages_Streaming — it already asserts message_start, content_block_delta, message_stop presence, which meets the plan acceptance criteria"
  - "Messages_NonStreaming uses absent stream field (not stream:false) to prove the bool-absent=false Anthropic spec behavior per plan spec"
metrics:
  duration: "~12 minutes"
  completed_date: "2026-05-25"
  tasks: 2
  files_modified: 2
---

# Phase 04 Plan 04: OpenAI + Anthropic E2E Ratification Summary

STRM-02, STRM-03, and STRM-05 ratified: all three streaming surfaces (Ollama NDJSON + OpenAI SSE + Anthropic SSE) have passing E2E subtests against the real kiro-cli backend, confirmed via the shared engine.Run() canonical channel.

## What Was Built

### Task 1: OpenAI E2E ratification comments (confirmed existing)

**tests/e2e/openai_e2e_test.go:**

Both `ChatCompletions_Streaming` and `ChatCompletions_NonStreaming` subtests were confirmed existing from Phase 3 with full structural assertions (the plan specified confirm-or-add). Ratification comments were added:

- `ChatCompletions_Streaming`: Added STRM-02 (OpenAI SSE) and STRM-03 (one canonical channel across all three surfaces) comment.
- `ChatCompletions_NonStreaming`: Added STRM-05 comment documenting stream:false routing and bool-absent=false semantics per OpenAI public spec.

The existing `assertOpenAISSE` helper already asserts:
- Content-Type text/event-stream prefix
- Data-only framing (no event: lines — flat OpenAI format)
- Role-first delta (choices[0].delta.role == "assistant" on first chunk)
- At least one non-null finish_reason frame
- Terminal `data: [DONE]` terminator
- Accumulated content non-empty

### Task 2: Anthropic E2E test file (created)

**tests/e2e/anthropic_e2e_test.go** (new file, 128 lines):

`TestE2E_Anthropic` boots ONE gateway (default ENABLED_SURFACES, AUTH_TOKEN=e2e-token, real kiro) and runs:

**Messages_Streaming** — POST /v1/messages (x-api-key: e2e-token, stream:true):
- Asserts status 200
- Asserts Content-Type text/event-stream prefix
- Uses `assertStrictSSE` (shared from e2e_test.go) which:
  - Validates strict event→data→blank state machine for every frame
  - Asserts message_start is first event, message_stop is last event
  - Asserts content_block_start, content_block_delta, content_block_stop, message_delta all appear
  - Asserts no error event on the happy path
- STRM-02 (Anthropic SSE) + STRM-03 (canonical channel) comment documented

**Messages_NonStreaming** — POST /v1/messages (Bearer, no stream field → absent=false per Anthropic spec):
- Asserts status 200
- Asserts Content-Type application/json prefix
- Decodes Anthropic message response: type=="message", role=="assistant", stop_reason non-nil/non-empty, content[0].type=="text", content[0].text non-empty
- Second `json.Decode` returns io.EOF (proves single JSON object, not a stream)
- STRM-05 (stream:false Anthropic regression) comment documented with *bool rationale

Anthropic D-05 exemption documented in file header and Messages_Streaming comment per plan spec.

## Deviations from Plan

### Confirmed-Existing (not a deviation — plan specified confirm-or-add)

**OpenAI subtests confirmed existing from Phase 3:**
- `ChatCompletions_Streaming` confirmed at line 200 of openai_e2e_test.go
- `ChatCompletions_NonStreaming` confirmed at line 133 of openai_e2e_test.go
- Both subtests already have full structural assertions meeting all plan acceptance criteria
- Action: ratification comments added only (no test logic changed)

**Anthropic coverage in TestE2E_SharedGateway:**
- `TestE2E_SharedGateway/Streaming_SSE` already covers Anthropic SSE via `assertStrictSSE`
- Plan required a dedicated `TestE2E_Anthropic` top-level function — added without removing existing coverage

None of the above required a Rule 1/2/3/4 deviation; they were plan-specified confirm-or-add outcomes.

## Verification

```
go build ./...                         — exit 0
go vet ./...                           — exit 0
go build -tags e2e ./tests/e2e/...    — exit 0
go vet -tags e2e ./tests/e2e/...      — exit 0
go test -race ./...                    — all PASS (14 packages)
go test -tags e2e -list '.*' ./tests/e2e/... — TestE2E_Anthropic listed (compilation gate)
golangci-lint pre-commit hooks         — PASS on both commits
```

E2E subtests (require OTTO_E2E=1 + kiro-cli):
- `TestE2E_OpenAI/ChatCompletions_Streaming` — confirmed existing; asserts text/event-stream + data: [DONE] + finish_reason
- `TestE2E_OpenAI/ChatCompletions_NonStreaming` — confirmed existing; asserts application/json + single JSON object
- `TestE2E_Anthropic/Messages_Streaming` — new; asserts text/event-stream + full Anthropic SSE event sequence
- `TestE2E_Anthropic/Messages_NonStreaming` — new; asserts application/json + single JSON object + stop_reason non-nil

## Known Stubs

None — this plan is test-only ratification. No production stubs exist. All test assertions are wired to real production code paths via the E2E binary build.

## Threat Flags

None — all changes are test files. No new network endpoints, auth paths, file access patterns, or schema changes introduced.

## Self-Check: PASSED
