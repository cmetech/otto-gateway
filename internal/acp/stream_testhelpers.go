package acp

import (
	"loop24-gateway/internal/canonical"
)

// NewStreamForTest constructs a *Stream owned by the test code.
// The returned stream has a pre-allocated buffered Chunks channel
// (capacity matches the production newStream) and exposes the send
// + close handles via PushForTest and CloseForTest so cross-package
// test harnesses (e.g., internal/pool/pool_test.go) can drive a real
// *acp.Stream without spinning up a fakeacp pipe server.
//
// Usage is restricted to test code by convention — the name advertises
// that intent. The signature deliberately mirrors newStream so the
// production path and the test path agree on FinalResult initialisation.
//
// Returns the constructed *Stream. Use PushForTest to inject chunks
// and CloseForTest to finalise the stream (idempotent via the existing
// sync.Once on the receiver).
func NewStreamForTest(sessionID string) *Stream {
	return newStream(nil, sessionID) //nolint:staticcheck // newStream tolerates nil ctx (the param is unused)
}

// PushForTest pushes a chunk onto the stream's send channel using a
// non-blocking send semantics adequate for unit tests — tests are
// expected to size their fake event budgets to fit inside the 64-slot
// buffer. Falls through silently if the stream is already closed.
//
// Returns true if the chunk was accepted, false if the channel is full
// (caller should size their test data to fit).
func (s *Stream) PushForTest(ch canonical.Chunk) bool {
	select {
	case s.chunks <- ch:
		s.mu.Lock()
		s.result.ChunkCount++
		s.mu.Unlock()
		return true
	default:
		return false
	}
}

// CloseForTest finalises a test-constructed stream. Delegates to the
// internal close() helper; safe to call multiple times via the
// receiver's sync.Once. result may be nil (the stream's FinalResult
// retains the SessionID set in NewStreamForTest); err may be nil
// (happy-path close).
func (s *Stream) CloseForTest(result *FinalResult, err error) {
	s.close(result, err)
}
