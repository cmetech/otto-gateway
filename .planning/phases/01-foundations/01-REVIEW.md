---
phase: 01-foundations
reviewed: 2026-05-23T00:00:00Z
depth: standard
files_reviewed: 5
files_reviewed_list:
  - internal/acp/dispatcher.go
  - internal/acp/client.go
  - internal/acp/translate.go
  - internal/acp/fakeacp_test.go
  - internal/acp/integration_test.go
findings:
  critical: 0
  warning: 5
  info: 4
  total: 9
status: issues_found
---

# Phase 1: Code Review Report (Re-review after gap-closure 01-05)

**Reviewed:** 2026-05-23
**Depth:** standard
**Files Reviewed:** 5
**Status:** issues_found (no new blockers; warnings around documented trade-offs and test hygiene)

## Summary

This is a re-review of `internal/acp` after gap-closure plan 01-05. The five
original BLOCKER findings (CR-01 drainAll deadlock, CR-02 Prompt success-frame
close, CR-03 readLoop cancel propagation, CR-05 canonical→ACP wire translation,
WR-02 grant_permission drop arm) and the SC#4 integration-test gap are all
closed correctly in the source. The previously deferred items (CR-04 permission
scope capture, CR-06 json.RawMessage IDs) remain deferred with documented
rationale and are out of scope here.

**Closures verified:**

- **CR-01** — `drainAll` now uses a non-blocking `select { case ch <- ...: default: }`
  under the lock; the buffered-1 send race is eliminated. `dispatcher.go:92-99`.
- **CR-02** — `Prompt()` success arm now clears `activeStream` under `streamMu`
  and calls `s.close(nil, nil)` before returning, so `Stream.Result()` unblocks
  the moment the response frame arrives. `client.go:575-580`.
- **CR-03** — `defer c.cancel()` added in `readLoop` as the second-registered
  defer; LIFO order is intentional and called out in the comment. Confirmed
  `defer` order is `wg.Done` (first registered → runs last) → `cancel` →
  stream-cleanup defer (last registered → runs first). `client.go:249-271`.
- **CR-05** — `translateBlock`, `translateBlocks`, and `wireBlock` added to
  `translate.go`; `promptParams.Blocks` retyped to `[]wireBlock`; the call site
  in `Prompt()` wraps the canonical slice in `translateBlocks(blocks)`.
  `translate.go:84-138`, `client.go:113-115`, `client.go:539`.
- **WR-02** — `default:` arm removed from the grant_permission select; the
  comment documents the deliberate readLoop-backpressure trade-off.
  `client.go:626-631`.
- **SC#4** — `TestIntegration_FakeACP_PromptChunkDelivery` proves
  Prompt → session/update → stream.Chunks → stream.Result() end-to-end,
  including the CR-02 assertion that Result() returns. `integration_test.go:168-254`.

**No new BLOCKERs introduced by the gap-closure commits.** The race I traced
hardest — `handleNotification` reading `activeStream`, the readLoop being
preempted, and the Prompt success arm closing the stream before `push()` —
cannot happen, because `handleNotification` runs **inside** the readLoop
goroutine and the response frame that wakes `Prompt()`'s caller can only be
read by readLoop *after* the in-progress `handleNotification` returns. The
mutex-released-before-push pattern is safe under the single-reader invariant.

Five WARNINGs and four INFOs follow. None block Phase 2.

## Warnings

### WR-01: `translateBlocks(nil)` / `translateBlocks([]canonical.Block{})` emits `"blocks":null` on the wire

**File:** `internal/acp/translate.go:129-138`
**Severity:** WARNING

`translateBlocks` returns a nil slice on empty input, and `json.Marshal` of a
nil `[]wireBlock` renders as `null` (not `[]`). If a Phase 2 HTTP adapter ever
forwards an empty user prompt to `client.Prompt()`, the on-the-wire JSON will
be `{"sessionId":"...","blocks":null}`, which kiro-cli is likely to reject with
an unhelpful schema error rather than a clear "empty prompt" diagnostic. The
inline comment ("Callers should pass at least one block — Phase 2 adapters
always do") is informational, not enforced.

**Fix (pick one):**

1. Return `[]wireBlock{}` for empty input so the wire JSON is `"blocks":[]`:
   ```go
   func translateBlocks(blocks []canonical.Block) []wireBlock {
       out := make([]wireBlock, len(blocks))
       for i, b := range blocks {
           out[i] = translateBlock(b)
       }
       return out
   }
   ```
2. Or return an error from `Prompt()` when `len(blocks) == 0` so the adapter
   gets a typed error instead of a wire failure.

### WR-02: Auto-grant backpressure can wedge `readLoop` indefinitely if `writeCh` saturates

**File:** `internal/acp/client.go:627-631`
**Severity:** WARNING

The WR-02 closure removed the `default:` drop arm — correct, because dropping a
grant deadlocks kiro-cli. But the replacement only exits on `c.clientCtx.Done()`,
which is cancelled solely by `Close()` or by `readLoop` itself dying. If
`writeCh` (cap 16) fills up because `writerLoop` is blocked on a full OS pipe
buffer (the subprocess is slow consuming stdin), `handleNotification` blocks
inside the readLoop goroutine. With `readLoop` paused, the subprocess can fill
its own stdout buffer, which is the classic full-duplex pipe deadlock. The only
release path is `Close()` — meaning a misbehaving subprocess can hang every
caller forever even though the gateway as a whole is healthy.

The trade-off is documented (better than dropping the grant), but the failure
mode is invisible: no metric, no warning log, no timeout. Operators have no
signal that the readLoop is wedged.

**Fix:** add a bounded-wait branch with diagnostic logging, e.g.:
```go
select {
case c.writeCh <- data:
case <-c.clientCtx.Done():
    return
case <-time.After(30 * time.Second):
    c.cfg.Logger.Error("acp: grant write blocked >30s — readLoop wedged",
        "writeChLen", len(c.writeCh), "writeChCap", cap(c.writeCh))
    // continue blocking; emit a metric here when Phase 3 adds metrics
}
```
This preserves correctness (grant still eventually sent), but surfaces the
condition. The 30 s threshold is arbitrary; tune per the deployment SLA.

### WR-03: `TestIntegration_FakeACP_PromptChunkDelivery` has a chunk-source race

**File:** `internal/acp/integration_test.go:168-254`, `fakeacp_test.go:164-180`
**Severity:** WARNING

The fake emits a session/update with content `"hello from fake"` on receiving
`session/grant_permission` (before the test calls `Prompt()`) AND another
session/update with the same content on `session/prompt`. The test only waits
for `<-fake.permissionGranted` (closed *before* the post-grant update is
written) and then calls `Prompt()`. Whether the post-grant chunk lands in the
test's stream or is dropped (no `activeStream` yet) depends on goroutine
timing:

- If the post-grant chunk arrives **before** Prompt registers `activeStream`:
  it is dropped; the chunk the test receives is the one emitted on
  `session/prompt`. Test asserts `Content == "hello from fake"` → passes.
- If it arrives **after**: it lands in the stream as the first chunk; the test
  reads it and never reads the second. Test asserts `Content == "hello from
  fake"` → also passes.

The test passes either way because the strings are identical. That hides any
regression where the chunk is routed to the wrong stream or dropped silently —
both scenarios produce the same pass output. The test's intent ("CR-02 fix
confirmed") is met, but CR-05/ACP-05 chunk delivery is *not* unambiguously
verified.

**Fix:** make the two chunk payloads distinguishable, e.g. fake emits
`"hello-from-grant"` post-grant and `"hello-from-prompt"` post-session/prompt,
and the test asserts which content arrives. If both can arrive, drain the
stream until close and assert the count + order.

### WR-04: `TestIntegration_FakeACP_PromptChunkDelivery` goroutine leak window on `goleak.VerifyNone`

**File:** `internal/acp/integration_test.go:250-253`
**Severity:** WARNING

Call sequence at the end of the test:
```go
if err := client.Close(); err != nil { ... }
goleak.VerifyNone(t)        // <-- runs HERE
// defer fake.close()        // runs AFTER
```

`client.Close()` cancels `clientCtx`, drains pending, closes the client-side
RWC, and waits for `readLoop/writerLoop/pingLoop`. But the fake's `serve()`
goroutine only exits when its scanner sees EOF on `serverRead`. The pipe-pair
delivers EOF when `clientWrite` is closed inside `client.Close()`, so the
fake's scanner *will* return — but there is no synchronisation point between
`client.Close()` returning and the fake's `serve()` goroutine reaching its
`defer close(f.done)`.

In practice the test passes because `goleak.VerifyNone` does a small retry. But
on a slow CI runner this is flaky. The fix is to swap the order or to add an
explicit wait:
```go
client.Close()
fake.close()                 // explicit wait on f.done (already blocking)
goleak.VerifyNone(t)
```
Apply to `TestIntegration_FakeACP_AutoGrantAndTranslation`, `_PingWorks`, and
`_PromptChunkDelivery` — all three have the same shape.

### WR-05: `TestIntegration_FakeACP_ChunkTranslation` does not test what its name claims

**File:** `internal/acp/integration_test.go:106-152`
**Severity:** WARNING

The test sets up a fake, calls `Initialize` and `NewSession`, waits for the
fake's `updateEmitted`, then immediately closes both sides. It never calls
`Prompt()`, so no active stream exists when the chunk is emitted — the chunk
is dropped by `handleNotification`'s nil-stream branch. The internal comment
acknowledges this:
> "The chunk would have been dropped (no activeStream at emit time) which is
> correct. The warning logged by the client is verified by the test not
> panicking."

That is a real behavioural assertion (the no-activeStream warn branch doesn't
panic), but the test *name* says "ChunkTranslation" and the docstring claims
it "proves ACP-05 end-to-end with backpressure" — neither of which it tests.
`TestIntegration_FakeACP_PromptChunkDelivery` is the real ACP-05 test.

**Fix:** rename to `TestIntegration_FakeACP_DropChunkWhenNoActiveStream` and
adjust the docstring, or delete and rely solely on the `PromptChunkDelivery`
test plus the unit test in `client_test.go::TestSessionUpdateAfterStreamClose`.

## Info

### IN-01: `_ = ctx` in `readLoop` is dead code

**File:** `internal/acp/client.go:285`
**Severity:** INFO

```go
c.disp.route(f)
_ = ctx // ctx is passed for future cancellation use; readLoop exits on EOF
```

`ctx` is never read. The comment claims "future cancellation use" but the CR-03
fix already cancels `clientCtx` from inside `readLoop` itself, so the ctx
parameter is redundant. Either remove the parameter from `readLoop`'s signature
or wire it into the read loop (e.g., a context-aware framer read).

**Fix:** drop the parameter:
```go
func (c *Client) readLoop() { ... }
// at New/NewWithConn:
go c.readLoop()
```

### IN-02: Stream is allocated but never closed on `Prompt` ctx-cancel / RPC-error paths

**File:** `internal/acp/client.go:540-568`
**Severity:** INFO

In both the `<-ctx.Done()` and `frame.Error != nil` branches of `Prompt()`, the
`stream` allocated by `newStream(...)` is left orphaned: `activeStream` is
cleared, but `stream.close(...)` is never called. The stream object is
unreferenced by the caller (Prompt returns `(nil, err)`) and is GC'd cleanly —
no goroutine leak — so this is not a correctness issue today. But the
asymmetry with the success arm (which *does* close the stream) is a subtle
trap: a future refactor that returns the stream alongside an error, or that
adds an observer-pattern hook on stream close, would break silently.

**Fix:** call `stream.close(nil, errForPath)` in both error arms so the close
semantics are uniform. The next phase will likely want this anyway when an
audit log subscribes to terminal stream events.

### IN-03: `permission` field in `permissionParams` still discarded (CR-04 deferred)

**File:** `internal/acp/translate.go:19-21`
**Severity:** INFO

```go
type permissionParams struct {
    RequestID  string `json:"requestId"`
}
```

The `permission` object from the wire is parsed but discarded — `RequestID` is
the only captured field. This is the documented CR-04 deferral (Phase 8 policy
chain); flagged here only as a re-review acknowledgement that the deferral is
still live. No action.

### IN-04: `rpcFrame.ID` remains `*uint64` (CR-06 deferred)

**File:** `internal/acp/dispatcher.go:11-17`
**Severity:** INFO

`ID *uint64` means any non-numeric ID (string, null with explicit `"id":null`
to distinguish from a notification, etc.) is parsed as nil and routed to
`onNotif`, where it will be ignored as an unknown notification. JSON-RPC 2.0
allows numeric, string, or null IDs. Documented as deferred (CR-06) pending
kiro-cli emitting a non-numeric ID. No action; flagged only to confirm the
deferral is still live.

---

_Reviewed: 2026-05-23_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
_Scope: post-gap-closure re-review of internal/acp/{dispatcher,client,translate,fakeacp_test,integration_test}.go_
