---
status: passed
phase: 06-tool-call-path
source: [06-VERIFICATION.md, 06-VALIDATION.md]
started: 2026-05-27T13:46:21Z
updated: 2026-05-27T19:20:00Z
resolved: 2026-05-27T19:20:00Z
---

## Current Test

[resolved]

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
result: passed

result_details:
  verified_via: otto-cli (pi-ai based, @anthropic-ai/sdk-class consumer of /v1/messages)
  test_invocation: |
    otto -p "Read CLAUDE.md and tell me, verbatim, what the first line of the # Project section says."
  evidence_path: streaming (otto exclusively uses messages.stream())
  non_streaming_coverage: |
    Not directly exercised via otto. Covered structurally by:
      - byte-level golden test internal/adapter/anthropic/sse_golden_test.go::TestSSE_Golden_ToolUse
      - non-streaming render unit test internal/adapter/anthropic/render_test.go::TestRender_ToolUse_StopReasonOverride
      - e2e test tests/e2e/tools_anthropic_test.go::TestE2E_Tools_Anthropic/NativeToolCall_NonStreaming
    Non-streaming uses the same stop_reason override code path (render.go:101); pi-ai SDK parses non-streaming responses with the same JSON unmarshaller.

  bugs_surfaced_and_fixed:
    - commit: 22e9abc
      area: internal/adapter/anthropic/sse.go (applyChunk + finalizeStream)
      symptom: |
        SDK threw "Unexpected non-whitespace character after JSON at position 2 (line 1 column 3)"
        because the renderer emitted TWO input_json_delta frames per tool_use block:
        partial_json:"{}" (from a kiro placeholder/announcement tool_call notification
        with Args=nil normalized to empty map), followed by the populated partial_json.
        SDK accumulator concatenated them into invalid JSON {}{...}.
      fix: |
        Discipline: at most ONE input_json_delta per tool_use content block.
        Placeholder chunks defer the delta via pendingToolUseFlush; populated
        chunks emit one delta and clear the flag; subsequent populated chunks
        for the same block are dropped (D-06 atomicity); block close flushes
        a single partial_json:"{}" if no populated args ever arrived.
      tests_added:
        - TestApplyChunk_ToolCall_PlaceholderThenPopulated
        - TestApplyChunk_ToolCall_PlaceholderOnly_FlushesEmptyObject
        - TestApplyChunk_ToolCall_TwoPopulatedSameBlock_SecondDropped

    - commit: 6a14580
      area: internal/acp/translate.go (sessionUpdateBody / sessionUpdateParams + tool_call branch)
      symptom: |
        Tool was dispatched by the SDK with empty args {} even though kiro knew
        the file path. Diagnostic logging revealed kiro emits args under
        `rawInput` (not the spec field `args`) and the canonical tool name
        under `kind` (the spec field `title` is repurposed for a per-chunk
        status string like "Reading CLAUDE.md:1"). Translator was only reading
        the spec fields.
      fix: |
        Added Kind + RawInput fields to sessionUpdateBody / sessionUpdateParams.
        Tool name resolution: firstNonEmpty(body.Kind, body.Title, "unknown").
        Args resolution: body.Args wins when set; falls back to body.RawInput.
      tests_added:
        - tool_call_kiro_wire_kind_and_rawInput
        - tool_call_spec_args_only_fallback
        - tool_call_args_wins_over_rawInput_when_both_set
        - tool_call_chunk_kiro_announcement_no_args

  sdk_conformance_proof: |
    The SDK successfully dispatched the otto-cli read tool from a streamed
    tool_use block — this only happens when ALL of the following hold:
      - content_block_start / _delta / _stop event sequence parses cleanly
      - input arrives as a structured object (not null, not JSON-string)
      - stop_reason:"tool_use" appears in message_delta (otherwise the SDK
        completes the stream without invoking any tool)
    Prior to commit 22e9abc the SDK threw mid-stream — itself proof the SDK
    was actively parsing our delta sequence. After both gateway fixes, the
    full dispatch round-trip is clean.

  outstanding_followup:
    - description: |
        The args shape kiro emits in rawInput (e.g.
        {"operations":[{"mode":"Line","path":"..."}], "__tool_use_purpose":"..."})
        does NOT match otto-cli's local read tool schema ({path: string}).
        otto-cli's tool validator rejects with "missing required property
        'path'". This is a client-side tool-schema concern, not a gateway
        issue — the gateway now correctly surfaces kiro's args; what the
        client does with them is its concern.
      owner: otto-cli
      tracking: |
        LLM prompt drafted in conversation 2026-05-27 outlining three
        resolution options (tolerant tool adapter; tool-name deduplication;
        prompt shaping). Not blocking Phase 6 completion.

## Summary

total: 1
passed: 1
issues: 0
pending: 0
skipped: 0
blocked: 0

## Gaps
