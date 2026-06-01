// Package engine — unit tests for RangeChunksWithIdleTimeout (quick 260531-ruv).
//
// All tests use sub-200ms timeouts. None depend on the real default (30s).
// fakeIdleStream's blocking channel is unblocked at test cleanup so the
// goroutines started by the helper cannot wedge the suite.
package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// fakeIdleStream implements the Stream interface for the idle-helper tests.
// chunks is the channel exposed via Chunks(); result/err are returned from
// Result(). Tests that need a never-producing stream simply do not send on
// chunks — Cleanup closes the channel so any lingering range terminates.
type fakeIdleStream struct {
	chunks chan canonical.Chunk
	result *canonical.FinalResult
	err    error
}

func newFakeIdleStream(t *testing.T) *fakeIdleStream {
	t.Helper()
	s := &fakeIdleStream{chunks: make(chan canonical.Chunk)}
	t.Cleanup(func() {
		// Drain-safe close: only close if not already closed by the test.
		defer func() { _ = recover() }()
		close(s.chunks)
	})
	return s
}

func (s *fakeIdleStream) Chunks() <-chan canonical.Chunk { return s.chunks }
func (s *fakeIdleStream) Result() (*canonical.FinalResult, error) {
	return s.result, s.err
}

func TestRangeChunksWithIdleTimeout_FiresOnSilentStream(t *testing.T) {
	t.Parallel()
	s := newFakeIdleStream(t)
	noop := func(canonical.Chunk) error { return nil }

	start := time.Now()
	err := RangeChunksWithIdleTimeout(context.Background(), s, 50*time.Millisecond, noop)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("helper took too long to fire: %v", elapsed)
	}
}

func TestRangeChunksWithIdleTimeout_ChunkResetsTimer(t *testing.T) {
	t.Parallel()
	s := &fakeIdleStream{chunks: make(chan canonical.Chunk)}
	count := 0
	onChunk := func(canonical.Chunk) error {
		count++
		return nil
	}

	// Producer: emits 5 chunks at 25ms intervals, then closes.
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(25 * time.Millisecond)
			s.chunks <- canonical.Chunk{Kind: canonical.ChunkKindText}
		}
		close(s.chunks)
	}()

	err := RangeChunksWithIdleTimeout(context.Background(), s, 50*time.Millisecond, onChunk)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if count != 5 {
		t.Fatalf("expected 5 chunks delivered, got %d", count)
	}
}

func TestRangeChunksWithIdleTimeout_ZeroDisables(t *testing.T) {
	t.Parallel()
	s := newFakeIdleStream(t)
	noop := func(canonical.Chunk) error { return nil }

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := RangeChunksWithIdleTimeout(ctx, s, 0, noop)
	if errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("expected ctx error, got idle-timeout: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded wrap, got %v", err)
	}
}

func TestRangeChunksWithIdleTimeout_OnChunkErrorPropagates(t *testing.T) {
	t.Parallel()
	s := &fakeIdleStream{chunks: make(chan canonical.Chunk, 1)}
	s.chunks <- canonical.Chunk{Kind: canonical.ChunkKindText}
	close(s.chunks)

	boom := errors.New("boom")
	onChunk := func(canonical.Chunk) error { return boom }

	err := RangeChunksWithIdleTimeout(context.Background(), s, 50*time.Millisecond, onChunk)
	if err != boom { //nolint:errorlint // we want identity, not wrapping
		t.Fatalf("expected verbatim boom error, got %v", err)
	}
}

func TestRangeChunksWithIdleTimeout_CtxCancelBeatsIdle(t *testing.T) {
	t.Parallel()
	s := newFakeIdleStream(t)
	noop := func(canonical.Chunk) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()

	err := RangeChunksWithIdleTimeout(ctx, s, 200*time.Millisecond, noop)
	if errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("expected ctx cancel error, got idle-timeout: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected Canceled wrap, got %v", err)
	}
}
