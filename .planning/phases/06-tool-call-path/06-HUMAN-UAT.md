---
status: partial
phase: 06-tool-call-path
source: [06-VERIFICATION.md, 06-VALIDATION.md]
started: 2026-05-27T13:46:21Z
updated: 2026-05-27T13:46:21Z
---

## Current Test

[awaiting human testing]

## Tests

### 1. loop24-client messages.stream() SDK conformance against live binary
expected: @anthropic-ai/sdk MessageStream emits content_block_start -> content_block_delta -> content_block_stop events with a complete tool_use block carrying object input AND stop_reason:"tool_use" in BOTH streaming and non-streaming paths
why_human: SDK parser conformance — only the real @anthropic-ai/sdk client can verify that the streamed bytes are parsed into a structurally-correct tool_use block at the application layer. The byte-level golden test TestSSE_Golden_ToolUse locks the wire shape, but the SDK parser's interpretation requires the live loop24-client smoke run per VALIDATION.md "Manual-Only Verifications". This is the Task 4 checkpoint from plan 06-05 that the planner deliberately deferred from a workflow.human_verify_mode=end-of-phase checkpoint.
instructions:
  1. Set OTTO_E2E=1 and start the gateway: `go run ./cmd/otto-gateway`
  2. From loop24-client repo: `ANTHROPIC_BASE_URL=http://localhost:11434 npm run smoke:tool-use`
  3. Assert SDK emits content_block_start -> content_block_delta -> content_block_stop events
  4. Assert final message.content includes a complete tool_use block with object input (NOT null, NOT JSON string)
  5. Assert stop_reason is "tool_use" (NOT "end_turn")
  6. Run in both streaming and non-streaming modes (messages.create vs messages.stream)
result: [pending]

## Summary

total: 1
passed: 0
issues: 0
pending: 1
skipped: 0
blocked: 0

## Gaps
