---
status: resolved
phase: 01-foundations
source: [01-VERIFICATION.md]
started: 2026-05-23T18:50:00Z
updated: 2026-05-23T19:45:00Z
resolved_by: 01-05-PLAN.md (gap-closure)
---

## Current Test

[resolved 2026-05-23 — gap-closure plan 01-05 landed all five correctness fixes plus the SC#4 integration test; re-verification returned status=passed]

## Tests

### 1. Verify integration test proves `session/update` translation onto `Stream.Chunks` channel
expected: An integration test calls `Prompt()`, receives `session/update` from the fake server, and a `canonical.Chunk` with `ChunkKindText` and `Content "hello from fake"` arrives on `stream.Chunks` before the stream closes.
result: passed — TestIntegration_FakeACP_PromptChunkDelivery (internal/acp/integration_test.go:168-254) lands a typed canonical.Chunk on stream.Chunks and stream.Result() returns; PASS under race detector with goleak.VerifyNone

why_human: |
  SC#4 (ROADMAP.md) says "translates a session/update into a typed chunk." The existing
  integration tests confirm auto-grant (ACP-04) and that session/update is emitted, but the
  fake server emits session/update BEFORE any `Prompt()` call, so the chunk is dropped (no
  active stream). No integration test verifies a typed chunk actually lands on
  `Stream.Chunks`.

  The translation function is fully unit-tested in `translate_test.go` (all 4 chunk kinds:
  Text, Thought, ToolCall, Plan). The push-to-stream path is unit-tested in
  `TestSessionUpdateAfterStreamClose`. The integration pipeline runs end-to-end — the
  "session/update for unknown session — dropped" Warn log line in the integration test
  proves readLoop → handleNotification → translateUpdate → activeStream lookup all
  executed correctly.

  Two readings of SC#4 are possible:
    (a) Unit test of translateUpdate + confirmed pipeline execution satisfies SC#4.
    (b) An end-to-end integration test where a chunk lands on Stream.Chunks via an
        active Prompt() call is explicitly required.

  Reading (a) is the verifier's recommendation given MVP mode + the fact that CR-02
  (Stream.Result() deadlock on successful Prompt) would block any such test today —
  the test can't pass until CR-02 is fixed in Phase 2 anyway.

  Reading (b) would block Phase 1 sign-off until CR-02 + a new integration test land.

## Code Review Disposition

The 6 critical findings in `01-REVIEW.md` (CR-01 dispatcher drainAll deadlock, CR-02 Stream.Result deadlock, CR-03 readLoop death not propagated, CR-04 permission field dropped, CR-05 Block has no JSON tags, CR-06 string-ID handling) do not block any of the Phase 1 success criteria as written, but CR-01/CR-02/CR-03/CR-05/WR-02 will affect Phase 2 the moment HTTP handlers call `Prompt()`.

Recommended path forward: Phase 2's PLAN.md should open a gap-closure task block covering CR-01, CR-02, CR-03, CR-05, WR-02 before any adapter code calls `Prompt()`.

## Summary

total: 1
passed: 1
issues: 0
pending: 0
skipped: 0
blocked: 0

## Gaps

- id: phase-1-acp-correctness-gap
  source: 01-REVIEW.md + 01-VERIFICATION.md (SC#4 partial)
  status: resolved
  resolved_by: 01-05-PLAN.md (commits cfaa9d0, ec46b45, 91dbf2b)
  resolved_date: 2026-05-23
  items:
    - CR-01: RESOLVED — dispatcher.drainAll uses non-blocking select with default arm (dispatcher.go:89-100)
    - CR-02: RESOLVED — Prompt success arm closes stream before return (client.go:570-580)
    - CR-03: RESOLVED — readLoop has `defer c.cancel()` (client.go:260)
    - CR-05: RESOLVED — internal/acp/translate.go adds wireBlock + translateBlock + translateBlocks; canonical/chunk.go untouched (D-04 preserved)
    - WR-02: RESOLVED — grant_permission select has no default drop arm (client.go:627-631)
    - SC#4: RESOLVED — TestIntegration_FakeACP_PromptChunkDelivery passes under race + goleak (integration_test.go:168-254)
  deferred:
    - CR-04 (permission audit) — Phase 8 hook chain
    - CR-06 (string IDs) — defer until kiro-cli emits non-numeric IDs
