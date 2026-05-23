---
phase: 01-foundations
plan: "05"
subsystem: acp
tags: [acp, dispatcher, stream, translate, json-rpc, integration-test, gap-closure, correctness]
gap_closure: true
dependency_graph:
  requires:
    - 01-01 (framer/dispatcher/Client skeleton)
    - 01-02 (handleNotification, auto-grant, Stream)
    - 01-03 (fakeACPServer + integration_test scaffolding)
    - 01-04 (operating.md / wrapper docs — unchanged here)
  provides:
    - internal/acp/dispatcher.go: drainAll non-blocking send (CR-01 mitigation)
    - internal/acp/client.go: Prompt success closes the stream (CR-02), readLoop cancels clientCtx (CR-03), grant has no default drop arm (WR-02)
    - internal/acp/translate.go: translateBlock + translateBlocks + wireBlock (CR-05 wire-shape adapter for session/prompt)
    - internal/acp/fakeacp_test.go: session/prompt handler that emits update + response
    - internal/acp/integration_test.go: TestIntegration_FakeACP_PromptChunkDelivery (SC#4 end-to-end Prompt→chunk→Result test)
  affects:
    - 02-* (Phase 2 HTTP adapters that call Prompt the moment a request arrives — all five defects would have produced hangs or wire corruption)
tech_stack:
  added: []
  patterns:
    - Non-blocking channel-send under a held mutex (`select { case ch <- x: default: }`) — used in dispatcher.drainAll
    - Defer LIFO ordering as a deliberate cleanup ordering tool (readLoop's wg.Done / cancel / stream-cleanup chain)
    - Adapter-pattern wire-shape translation kept inside the protocol package (`internal/acp`), not the canonical package (D-04)
key_files:
  created: []
  modified:
    - internal/acp/dispatcher.go
    - internal/acp/client.go
    - internal/acp/translate.go
    - internal/acp/fakeacp_test.go
    - internal/acp/integration_test.go
decisions:
  - "Adapter-pattern wire-shape translation lives in internal/acp/translate.go; canonical/chunk.go has NO JSON tags or MarshalJSON. Keeps the canonical package ACP-agnostic per D-04 — if kiro-cli changes its wire format only translate.go changes."
  - "CR-04 (permission field) deferred: capturing the permission scope now is dead code (Phase 1 auto-grants unconditionally per ACP-04) and golangci-lint's unused linter would flag it. Wire it in Phase 8 alongside the policy plugin chain that actually consumes the scope."
  - "CR-06 (rpcFrame.ID as *uint64 only) deferred: kiro-cli v2.4.1 uses numeric IDs exclusively. A json.RawMessage dispatcher rewrite would be non-trivial and currently exercises no codepath. Defer until kiro-cli emits a non-numeric ID and integration tests surface the regression deterministically."
  - "Defer ordering in readLoop is load-bearing: wg.Done() registered first → cancel() second → stream-cleanup defer last. LIFO execution means the stream-cleanup defer runs first (closes activeStream before clientCtx cancels callers), cancel() runs middle (unblocks subsequent send() calls), wg.Done() runs last (Close()'s wg.Wait sees all goroutines exited). Documented inline in client.go to prevent reordering by future cleanup passes."
  - "No promptReceived channel added to fakeACPServer despite the new session/prompt handler. Sequencing is already guaranteed by client.Prompt registering activeStream BEFORE the send; adding an unused field would fail golangci-lint."
metrics:
  duration_minutes: 9
  completed_date: "2026-05-23"
  tasks_completed: 4
  tasks_total: 4
  files_created: 0
  files_modified: 5
requirements-completed: [ACP-04, ACP-05]
---

# Phase 01 Plan 05: Phase 1 Gap Closure (CR-01/02/03/05 + WR-02 + SC#4) Summary

**Five Phase-1 correctness defects in internal/acp fixed (drainAll deadlock, Stream.Result deadlock, readLoop crash propagation, canonical.Block wire-shape, grant-permission drop arm) plus the SC#4 end-to-end Prompt-to-chunk integration test that proves the typed canonical pipeline before Phase 2 adapters start calling Prompt.**

## Performance

- **Duration:** ~9 min (first commit 15:38:00 EDT → last code commit 15:40:03 EDT; SUMMARY follows)
- **Started:** 2026-05-23T19:32:50Z (worktree base)
- **Completed:** 2026-05-23T19:41:15Z
- **Tasks:** 4/4
- **Files modified:** 5 (0 created)

## Accomplishments

- **CR-01 — dispatcher.drainAll deadlock eliminated.** Sentinel-error send now uses a non-blocking `select { case ch <- ...: default: }` so a buffered-1 pending channel already full from a racing route() never deadlocks the mutex.
- **CR-02 — Stream.Result no longer hangs on successful Prompt.** The Prompt success arm now calls `stream.close(nil, nil)` and clears activeStream before returning, so Result() unblocks the moment the response frame arrives rather than waiting for subprocess EOF.
- **CR-03 — readLoop death propagates to clientCtx.** `defer c.cancel()` added as readLoop's second deferred call. Subprocess crash before Close() now unblocks any subsequent send() with ErrClientClosed instead of hanging on writeCh.
- **CR-05 — session/prompt now emits the ACP wire shape.** `translateBlock` + `translateBlocks` + `wireBlock` added to internal/acp/translate.go; promptParams.Blocks retyped to `[]wireBlock`. kiro-cli now receives `{"type":"text","content":"..."}` instead of Go's default discriminated-struct encoding.
- **WR-02 — grant_permission can no longer be silently dropped.** Removed the `default:` arm from the writeCh send in handleNotification. Backpressure briefly pauses readLoop, which is the correct trade-off — kiro-cli blocks forever if a grant is missed.
- **SC#4 — TestIntegration_FakeACP_PromptChunkDelivery added.** Proves a canonical.Chunk (Kind=Text, Content="hello from fake") reaches stream.Chunks through an active Prompt() call, then stream.Result() returns without blocking, then goleak.VerifyNone confirms no goroutine leaks.

## Task Commits

Each task was committed atomically:

1. **Task 1: CR-01/02/03 + WR-02 fixes (dispatcher.go + client.go)** — `cfaa9d0` (fix)
2. **Task 2: CR-05 — translateBlock / wireBlock in translate.go + wiring in client.go** — `ec46b45` (fix)
3. **Task 3: session/prompt handler in fakeacp_test.go + TestIntegration_FakeACP_PromptChunkDelivery** — `91dbf2b` (test)
4. **Task 4: Full CI gate verification** — no commit (verification-only; nothing required code changes)

_Plan metadata commit follows separately when the orchestrator merges this worktree back._

## Files Created/Modified

- `internal/acp/dispatcher.go` — `drainAll` send wrapped in `select { case ch <- ...: default: }` (CR-01)
- `internal/acp/client.go` — `defer c.cancel()` added to `readLoop` (CR-03); Prompt success arm calls `s.close(nil, nil)` and clears activeStream (CR-02); grant_permission select no longer has a `default:` arm (WR-02); `promptParams.Blocks` retyped from `[]canonical.Block` to `[]wireBlock`; Prompt RPC body now wraps blocks in `translateBlocks(blocks)` (CR-05 wiring)
- `internal/acp/translate.go` — added `wireBlock` struct, `translateBlock(canonical.Block) wireBlock`, `translateBlocks([]canonical.Block) []wireBlock` (CR-05)
- `internal/acp/fakeacp_test.go` — new `case "session/prompt"` in serve(): emits session/update with content "hello from fake", then the prompt response frame; docstring updated to enumerate the new step
- `internal/acp/integration_test.go` — added `TestIntegration_FakeACP_PromptChunkDelivery` (SC#4)

`internal/canonical/chunk.go` was **NOT** modified. The canonical package remains ACP-agnostic per D-04 — only translate.go knows the wire shape kiro-cli speaks.

## Gaps Closed

| Gap | Severity | File | Fix Summary |
|-----|----------|------|-------------|
| CR-01 | BLOCKER | `internal/acp/dispatcher.go:82-89` | Non-blocking `select { case ch <- rpcFrame{...}: default: }` in drainAll |
| CR-02 | BLOCKER | `internal/acp/client.go:541-551` | Prompt success arm closes stream + clears activeStream before returning |
| CR-03 | BLOCKER | `internal/acp/client.go:243-255` | `defer c.cancel()` added to readLoop (second-registered defer) |
| CR-05 | BLOCKER | `internal/acp/translate.go` + `internal/acp/client.go:107-110` | `translateBlock` / `wireBlock` adapter; promptParams.Blocks retyped |
| WR-02 | WARNING | `internal/acp/client.go:595-601` | Removed `default:` arm from grant_permission select; backpressure is correct |
| SC#4 | TEST GAP | `internal/acp/integration_test.go` | `TestIntegration_FakeACP_PromptChunkDelivery` proves Prompt→Stream.Chunks→Result end-to-end |

## Deferred Gaps (Explicit, with Rationale)

**CR-04 — `permission` field silently discarded.** `permissionParams` captures only `RequestID`, losing the `permission` object (scope: shell_exec, file_write, etc.). Phase 1 auto-grants unconditionally per ACP-04 spec; the "one place to enforce policy" core value materialises in Phase 8 when the plugin hook chain adds policy enforcement. Capturing the field now would add dead code that golangci-lint's `unused` linter would flag. The field will be captured + logged + echoed in Phase 8 alongside the audit log.

**CR-06 — `rpcFrame.ID` is `*uint64` only.** kiro-cli v2.4.1 (the current deployed version) uses numeric IDs exclusively. Adding `json.RawMessage` ID handling would require a non-trivial dispatcher rewrite for a codepath that no current consumer exercises. Defer until either (a) kiro-cli emits a non-numeric ID and integration tests surface the regression deterministically, or (b) a JSON-RPC spec audit becomes a phase objective on its own. The current behaviour ("malformed frame" log on parse failure, caller hangs) is undesirable but observable; production deployments would alarm on the log line.

## Decisions Made

- **Adapter translation lives in `internal/acp/translate.go`, not in `internal/canonical/chunk.go`.** The canonical package must remain free of ACP-wire concerns (D-04). `translateBlock` mirrors the existing `translateUpdate` pattern.
- **`promptParams.Blocks` typed as `[]wireBlock` (not `any`).** Static typing at the struct boundary prevents future regressions where a caller could pass an un-translated `canonical.Block` slice. The conversion is forced at the call site (Prompt) via `translateBlocks(blocks)`.
- **No `promptReceived` synchronisation channel added to `fakeACPServer`.** The plan's note about ordering was correct — `client.Prompt` registers `activeStream` BEFORE the RPC send, so the fake's `session/update` notification cannot race ahead of the registration. An unused struct field would trip golangci-lint and break `make ci` (the very gate this plan is supposed to keep green).
- **Defer order in `readLoop` is load-bearing and documented inline.** `wg.Done` registered first (runs last), `cancel` second (runs middle), stream-cleanup defer registered last (runs first). A future refactor that "tidies" these into a single defer would re-introduce CR-03.

## Deviations from Plan

None — plan executed exactly as written.

The plan's `<interfaces>` block already specified line-accurate edits and the `<verify>` step in each task was satisfied without any unexpected linter findings or test failures. No Rule 1/2/3 auto-fixes were required.

## Issues Encountered

None.

## Verification Results

```
$ go build ./...                                 # exit 0 (no output)
$ go test -race -count=1 ./internal/acp/...      # ok  loop24-gateway/internal/acp  2.122s
$ make lint                                      # 0 issues.
$ make ci
  golangci-lint run ./...                        → 0 issues.
  go test -race ./...                            → ok  internal/acp, internal/config, internal/server (all PASS)
  go-arch-lint check --project-path .            → OK - No warnings found
  govulncheck ./...                              → No vulnerabilities found.
```

Specifically for SC#4:

```
$ go test -race -count=1 -run TestIntegration_FakeACP_PromptChunkDelivery ./internal/acp/... -v
=== RUN   TestIntegration_FakeACP_PromptChunkDelivery
    integration_test.go:235: received chunk: Kind=0 Content="hello from fake"
    integration_test.go:245: stream.Result() returned — CR-02 fix confirmed
--- PASS: TestIntegration_FakeACP_PromptChunkDelivery (0.00s)
PASS
ok  loop24-gateway/internal/acp  1.250s
```

## Self-Check

All claims verified against the filesystem and git history.

```
1. CR-01 non-blocking send in dispatcher.go:        FOUND (line 94)
2. CR-03 defer c.cancel() in client.go:             FOUND (line 260)
3. WR-02 uncommented "dropping grant_permission":   COUNT 0
4. CR-05 func translateBlock in translate.go:       FOUND (line 101)
5. CR-05 wireBlock used in client.go:               FOUND (line 114, 539)
6. CR-02 s.close(nil, nil) in client.go:            FOUND (line 579)
7. TestIntegration_FakeACP_PromptChunkDelivery:     FOUND (integration_test.go:168)
8. canonical/chunk.go modified since base:          0 commits (correctly untouched)
9. Task 1 commit cfaa9d0 in git log:                FOUND
10. Task 2 commit ec46b45 in git log:               FOUND
11. Task 3 commit 91dbf2b in git log:               FOUND
```

## Self-Check: PASSED

## Next Phase Readiness

- **Phase 2 (HTTP adapter wiring) can now safely call `client.Prompt(...)` on every incoming HTTP request.** The five defects that would have produced hangs or wire corruption are closed; the integration test proves the typed-canonical pipeline (Prompt → translateUpdate → push → Chunks → Result) end-to-end under the race detector.
- **No blockers for Phase 2.** The kiro-cli wire-shape contract for session/prompt is now adapter-mediated; if Phase 2 needs additional block kinds (image, ref, etc.), they slot into translate.go's `translateBlock` switch.
- **Two known gaps remain in internal/acp but are explicitly deferred with rationale:** CR-04 (permission scope capture) → Phase 8 policy chain; CR-06 (json.RawMessage ID handling) → defer until kiro-cli emits non-numeric IDs.

---
*Phase: 01-foundations*
*Plan: 05*
*Completed: 2026-05-23*
