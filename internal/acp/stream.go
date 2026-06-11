package acp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"otto-gateway/internal/canonical"
)

// errPushAfterClose is returned by Stream.push when the stream has already
// been closed. Callers swallow this (typically with a Warn log) — the chunk
// is dropped silently because the consumer has already torn down. This
// replaces the previous send-on-closed-channel panic from the readLoop
// goroutine.
var errPushAfterClose = errors.New("acp: stream push after close")

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
// arrive from kiro-cli. After Chunks is closed, Result() returns the accumulated
// FinalResult and any terminal error.
//
// D-03: streaming channel from day 1 — no buffer-and-return.
//
// Concurrency contract (Phase 8.3): the readLoop goroutine is the sole
// producer on Chunks. Prompt() returns (*Stream, error) as soon as the
// session/prompt request is accepted by the writer; the per-prompt
// awaitPromptResult goroutine in acp.Client owns the response wait and
// finalizes the stream via close(...) when the session/prompt response arrives
// (or on ctx cancel / client close). Stream.Result() blocks on <-s.done and
// is the single sync point for the terminal StopReason / error.
//
// Drainage of Chunks MAY be concurrent with whatever goroutine called Prompt
// for throughput on long responses, but is no longer required for correctness
// — push() backpressure on a slow consumer no longer cascades into the
// response-wait path because that path runs on its own goroutine independent
// of the chunk drainage rate. The internal channel is buffered (64 slots) to
// absorb short bursts; if a consumer is slower than the producer the readLoop
// blocks on push() until the consumer catches up, but the session/prompt
// response continues to flow through awaitPromptResult and Stream.Result()
// will still return cleanly once the buffer drains.
type Stream struct {
	// Chunks is the receive-only channel of translated canonical chunks.
	// The channel is closed when the stream ends (session/update done or error).
	Chunks <-chan canonical.Chunk

	chunks chan canonical.Chunk // send side (internal)

	// ctx is the per-request context captured at newStream time. It is the
	// caller's Prompt ctx — bounded by the per-request timeout / client
	// disconnect, NOT by the client lifetime context. P-4 fix
	// (REL-POOL-04): handleNotification calls s.push with s.Ctx() (a
	// per-request ctx) rather than c.clientCtx. A stalled SSE consumer
	// that fills the chunk buffer used to block the readLoop goroutine
	// against c.clientCtx; with per-request ctx the stalled consumer
	// fails its OWN request when ctx times out, and the readLoop is
	// freed to dispatch the next frame (including ping responses, which
	// previously starved and SIGKILLed a healthy worker).
	ctx context.Context //nolint:containedctx // load-bearing per-request ctx for P-4 (REL-POOL-04) — stream lifetime is bounded by Prompt ctx

	done      chan struct{}
	closeOnce sync.Once

	// sendMu serializes close() against in-flight push(). push() holds
	// RLock during the send; close() takes Lock to wait for in-flight
	// pushes to drain or bail before it closes s.chunks. The closed flag
	// is set under Lock before s.chunks is closed so a push that wins the
	// RLock race after close() landed bails out without touching the
	// channel. close() closes s.done BEFORE taking Lock so any push
	// already blocked on a full chunks buffer can wake via the <-s.done
	// arm and release its RLock.
	sendMu sync.RWMutex
	closed bool

	mu     sync.Mutex
	result *FinalResult
	err    error
}

// newStream constructs a Stream for the given sessionID.
// The internal channel is buffered (64 slots) to allow kiro-cli to emit bursts
// without blocking the readLoop goroutine.
//
// P-4 fix (REL-POOL-04): the per-request ctx is captured on the Stream so
// handleNotification can pass it to push instead of c.clientCtx. A nil ctx
// is tolerated (test helpers may pass nil); push() guards against nil and
// falls back to context.Background() so the select arm <-ctx.Done() never
// fires spuriously.
func newStream(ctx context.Context, sessionID string) *Stream {
	ch := make(chan canonical.Chunk, 64)
	s := &Stream{
		Chunks: ch,
		chunks: ch,
		ctx:    ctx,
		done:   make(chan struct{}),
		result: &FinalResult{SessionID: sessionID},
	}
	return s
}

// Ctx returns the per-request context captured at newStream time, or
// context.Background() if the stream was constructed with a nil ctx
// (test helpers). Used by handleNotification to bound push() backpressure
// to the per-request lifetime rather than the client lifetime — the P-4
// (REL-POOL-04) fix.
func (s *Stream) Ctx() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// push sends chunk to the stream channel with backpressure.
// REVIEW FIX (Codex MEDIUM): blocks on select rather than dropping chunks silently.
// ctx should be the client lifetime context so backpressure respects Close().
//
// Concurrency: holds sendMu.RLock for the duration of the send. The select
// observes <-s.done so a close() racing with a blocked push (per-prompt ctx
// cancel while the client ctx is still alive) wakes this goroutine and
// returns errPushAfterClose instead of panicking on send-to-closed.
func (s *Stream) push(ctx context.Context, ch canonical.Chunk) error {
	s.sendMu.RLock()
	defer s.sendMu.RUnlock()
	if s.closed {
		return errPushAfterClose
	}
	select {
	case s.chunks <- ch:
		s.mu.Lock()
		s.result.ChunkCount++
		s.mu.Unlock()
		return nil
	case <-ctx.Done():
		return fmt.Errorf("acp: stream push cancelled: %w", ctx.Err())
	case <-s.done:
		return errPushAfterClose
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
		// Close s.done FIRST (no lock required) so any push() currently
		// blocked on a full s.chunks buffer wakes via the <-s.done arm and
		// releases sendMu.RLock. Only then can sendMu.Lock() be acquired
		// without deadlocking against the in-flight push.
		close(s.done)
		s.sendMu.Lock()
		s.closed = true
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
		s.sendMu.Unlock()
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

// SessionID returns the ACP session id captured at newStream time. Safe
// to call concurrently — the underlying result pointer is stable after
// newStream allocates it (only ChunkCount + StopReason mutate). Used by
// handleNotification to drop session/update frames whose session id
// does not match the active stream (audit
// acp-late-update-cross-session-leak).
func (s *Stream) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result == nil {
		return ""
	}
	return s.result.SessionID
}
