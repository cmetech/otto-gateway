// Package canonical — shared sentinel errors that cross the
// engine/adapter boundary (quick 260531-ruv).
//
// TRST-04 prohibits adapter_* → engine imports, so any error
// type/sentinel that both the engine AND the adapter handlers need to
// errors.Is-check lives in canonical (which both layers may depend on).
package canonical

import "errors"

// ErrStreamIdleTimeout is the sentinel returned (wrapped) by
// engine.RangeChunksWithIdleTimeout and by each adapter's per-
// streaming-surface idle watchdog when the chunk-receive loop sits
// idle longer than the configured timeout. Handlers errors.Is-check
// it to render a 504 on non-streaming paths and to log the WARN
// marker stream.idle_timeout uniformly across surfaces.
var ErrStreamIdleTimeout = errors.New("stream idle timeout")

// ErrPoolExhausted is the sentinel returned by pool.NewSession when
// all warm kiro-cli slots are busy past the AcquireTimeout deadline.
// Lives here (not in internal/pool) so the three adapter packages
// (anthropic, ollama, openai) can errors.Is-check it without
// importing internal/pool — TRST-04 prohibits adapter_* → pool
// imports. Handlers map this to HTTP 503 with `Retry-After: 5` and a
// surface-native error body (D-07).
//
// The internal/pool package re-exports this sentinel as
// `var ErrPoolExhausted = canonical.ErrPoolExhausted` for backward
// compatibility with existing pool-internal callers and tests; the
// errors.Is identity is preserved because it is literally the same
// *errorString value.
var ErrPoolExhausted = errors.New("pool: all workers busy; retry in 5s")
