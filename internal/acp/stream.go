package acp

import (
	"context"
	"fmt"
	"sync"

	"loop24-gateway/internal/canonical"
)

// FinalResult holds per-stream metadata available after Chunks closes.
// Callers obtain it via Stream.Result() which blocks until the stream is done.
type FinalResult struct {
	// SessionID is the kiro-cli session that produced this stream.
	SessionID string
	// ChunkCount is the number of canonical.Chunk values pushed to Chunks.
	ChunkCount int
	// StopReason carries the agent's stop reason from the session/prompt
	// response. Zero value canonical.StopUnknown indicates an abrupt close
	// (readLoop teardown), an unknown wire value (D-02 forward-compat), or
	// that the response was never read.
	StopReason canonical.StopReason
}

// Stream is the handle returned by Client.Prompt.
// Callers range over Chunks to receive translated canonical.Chunk values as they
// arrive from kiro-cli.  After Chunks is closed, Result() returns the accumulated
// FinalResult and any terminal error.
//
// D-03: streaming channel from day 1 — no buffer-and-return.
type Stream struct {
	// Chunks is the receive-only channel of translated canonical chunks.
	// The channel is closed when the stream ends (session/update done or error).
	Chunks <-chan canonical.Chunk

	chunks chan canonical.Chunk // send side (internal)

	done      chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	result *FinalResult
	err    error
}

// newStream constructs a Stream for the given sessionID.
// The internal channel is buffered (64 slots) to allow kiro-cli to emit bursts
// without blocking the readLoop goroutine.
func newStream(_ context.Context, sessionID string) *Stream {
	ch := make(chan canonical.Chunk, 64)
	s := &Stream{
		Chunks:    ch,
		chunks:    ch,
		done:      make(chan struct{}),
		result:    &FinalResult{SessionID: sessionID},
	}
	return s
}

// push sends chunk to the stream channel with backpressure.
// REVIEW FIX (Codex MEDIUM): blocks on select rather than dropping chunks silently.
// ctx should be the client lifetime context so backpressure respects Close().
func (s *Stream) push(ctx context.Context, ch canonical.Chunk) error {
	select {
	case s.chunks <- ch:
		s.mu.Lock()
		s.result.ChunkCount++
		s.mu.Unlock()
		return nil
	case <-ctx.Done():
		return fmt.Errorf("acp: stream push cancelled: %w", ctx.Err())
	}
}

// close finalises the stream: merges any provided FinalResult fields onto the
// stream's existing result (which was initialised in newStream and updated by
// push during the stream lifetime), stores err, and closes both channels.
// Idempotent — safe to call multiple times via sync.Once.
//
// Phase 1.1 D-07 merge semantics: rather than replacing s.result with the
// caller-supplied pointer (which would discard the SessionID set in newStream
// and the ChunkCount accumulated inside push), copy ONLY the non-zero fields
// from result onto the existing s.result. Today only StopReason flows in via
// this path — the existing readLoop teardown path calls close(nil, err), and
// the Prompt happy path calls close(&FinalResult{StopReason: stop}, nil).
func (s *Stream) close(result *FinalResult, err error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.result == nil {
			// Defensive — newStream always allocates, but guard so the merge
			// below doesn't crash on a hypothetical future caller.
			s.result = &FinalResult{}
		}
		if result != nil && result.StopReason != canonical.StopUnknown {
			s.result.StopReason = result.StopReason
		}
		s.err = err
		s.mu.Unlock()
		close(s.chunks)
		close(s.done)
	})
}

// Result blocks until the stream is closed and then returns the FinalResult
// and any terminal error. Safe to call from any goroutine.
func (s *Stream) Result() (*FinalResult, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, s.err
}
