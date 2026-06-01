// Package engine — RangeChunksWithIdleTimeout helper (quick 260531-ruv).
//
// This helper exists because the engine's request-context watchdog
// (engine.Run's context.AfterFunc) only fires when the request ctx
// terminates. When kiro hangs and emits zero chunks, the SSE/NDJSON
// emitter never writes, the client never disconnects, and streamCtx
// never gets canceled — so the pool slot is held indefinitely.
//
// RangeChunksWithIdleTimeout adds a per-chunk-loop idle watchdog. It is
// the single canonical implementation; all five chunk-loop sites
// (engine.Collect, anthropic.CollectAnthropicChat, anthropic SSE, ollama
// NDJSON, openai SSE) call it (non-streaming sites) or replicate its
// select arms (streaming sites that also need a ping-ticker arm).
package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"otto-gateway/internal/canonical"
)

// ErrStreamIdleTimeout is the sentinel returned (wrapped) by
// RangeChunksWithIdleTimeout when the idle timer fires before the next
// chunk or stream close. Callers errors.Is-check this sentinel to map
// the error to a per-surface terminal error frame (and to a 504 on the
// non-streaming surfaces).
var ErrStreamIdleTimeout = errors.New("stream idle timeout")

// RangeChunksWithIdleTimeout ranges stream.Chunks(), invoking onChunk
// for each chunk delivered. Semantics:
//
//   - idle == 0  → no timer is allocated; the loop becomes a bare
//     `for chunk := range stream.Chunks()` with a ctx.Done short-circuit.
//     This is the explicit opt-out path (legacy hang-forever behavior).
//   - idle > 0   → time.NewTimer(idle) is created and selected on
//     alongside ctx.Done and chunks. Each chunk drains+resets the timer
//     (drain-safe Stop pattern). Timer fire returns
//     fmt.Errorf("engine: stream idle timeout after %s: %w", idle, ErrStreamIdleTimeout).
//   - On chunks channel close (clean end), returns nil. Callers still
//     invoke stream.Result() afterward.
//   - On ctx.Done, returns fmt.Errorf("engine: idle range ctx: %w", ctx.Err()).
//   - On onChunk-returned error, the error is propagated verbatim (NOT
//     wrapped, NOT swallowed) so callers can route specific error types
//     (e.g., io.ErrClosedPipe) without unwrapping.
//
// We use time.NewTimer + drain-safe Stop (not time.After) because
// time.After leaks until the duration elapses; time.NewTimer is the
// established Go pattern for cancellable idle watchdogs.
func RangeChunksWithIdleTimeout(
	ctx context.Context,
	stream Stream,
	idle time.Duration,
	onChunk func(canonical.Chunk) error,
) error {
	chunks := stream.Chunks()

	// Zero-cost disabled path: no timer is allocated when idle == 0.
	if idle <= 0 {
		for {
			select {
			case <-ctx.Done():
				return fmt.Errorf("engine: idle range ctx: %w", ctx.Err())
			case c, ok := <-chunks:
				if !ok {
					return nil
				}
				if err := onChunk(c); err != nil {
					return err
				}
			}
		}
	}

	timer := time.NewTimer(idle)
	defer func() {
		if !timer.Stop() {
			// Drain any pending tick so the channel does not leak a
			// stray value into a future caller. Non-blocking — the
			// select on default protects against a racy already-drained
			// channel (e.g., timer fired but we are exiting via ctx).
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("engine: idle range ctx: %w", ctx.Err())
		case <-timer.C:
			return fmt.Errorf("engine: stream idle timeout after %s: %w", idle, ErrStreamIdleTimeout)
		case c, ok := <-chunks:
			if !ok {
				return nil
			}
			if err := onChunk(c); err != nil {
				return err
			}
			// Drain-safe reset (standard Go idiom).
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		}
	}
}
