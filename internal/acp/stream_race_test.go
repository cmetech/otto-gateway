package acp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// TestStreamPushClose_NoPanicOnRace exercises the race between Stream.push
// and Stream.close that previously panicked with "send on closed channel".
//
// Scenario from PRODUCTION-RELIABILITY-AUDIT.md
// (acp-push-send-on-closed-channel-panic):
//   - readLoop reads a session/update frame and calls push().
//   - In parallel, the caller's per-prompt ctx cancels and awaitPromptResult
//     calls Stream.close() while clientCtx is still alive.
//   - Under the old code push selected on s.chunks <- ch and <-ctx.Done()
//     (client lifetime) only — neither observed close, so a send on the
//     just-closed s.chunks panicked the readLoop goroutine.
//
// Under -race this test would either panic or report a data race before the
// fix; after the fix it must complete cleanly with push returning either nil
// (sent before close) or errPushAfterClose (lost the race).
func TestStreamPushClose_NoPanicOnRace(t *testing.T) {
	t.Parallel()

	const iterations = 200
	clientCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < iterations; i++ {
		s := newStream(context.Background(), "sess-race")

		// Saturate the buffer so the next push blocks on the send arm —
		// this is the exact state from the audit scenario where the
		// readLoop is mid-push when close() runs.
		for j := 0; j < cap(s.chunks); j++ {
			if err := s.push(clientCtx, canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "fill"}}); err != nil {
				t.Fatalf("iter %d fill %d: unexpected push err: %v", i, j, err)
			}
		}

		var pushErr atomic.Value // error
		var wg sync.WaitGroup
		wg.Add(2)
		ready := make(chan struct{})

		// Goroutine A: blocked push on full buffer.
		go func() {
			defer wg.Done()
			close(ready)
			err := s.push(clientCtx, canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "racy"}})
			if err != nil {
				pushErr.Store(err)
			}
		}()

		// Goroutine B: close the stream while push is blocked.
		go func() {
			defer wg.Done()
			<-ready
			// Tiny pause to maximize the chance push is parked in select.
			time.Sleep(time.Microsecond)
			s.close(&FinalResult{StopReason: canonical.StopEndTurn}, nil)
		}()

		wg.Wait()

		// Acceptable outcomes after the fix:
		//   - push returned nil (it won the race and sent before close took
		//     the write lock — possible because the consumer-less buffered
		//     channel had no drainer, but the test fills the buffer first so
		//     this is unreachable here; documented for completeness).
		//   - push returned errPushAfterClose (it lost the race and woke
		//     via the <-s.done arm).
		//   - push returned a context-cancelled error if clientCtx fired
		//     (not in this test, but valid for the production path).
		// What must NOT happen: panic, data race report, or hang.
		if v := pushErr.Load(); v != nil {
			err := v.(error)
			if !errors.Is(err, errPushAfterClose) {
				t.Fatalf("iter %d: unexpected push error: %v", i, err)
			}
		}

		// Sanity: a post-close push must always return errPushAfterClose,
		// never panic.
		postErr := s.push(clientCtx, canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "after"}})
		if !errors.Is(postErr, errPushAfterClose) {
			t.Fatalf("iter %d: post-close push expected errPushAfterClose, got %v", i, postErr)
		}
	}
}

// TestStreamPush_PostCloseReturnsError verifies the simple invariant: after
// close() returns, push() must never panic and must return errPushAfterClose.
func TestStreamPush_PostCloseReturnsError(t *testing.T) {
	t.Parallel()
	s := newStream(context.Background(), "sess-post")
	s.close(nil, ErrClientClosed)

	err := s.push(context.Background(), canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}})
	if !errors.Is(err, errPushAfterClose) {
		t.Fatalf("expected errPushAfterClose, got %v", err)
	}
}
