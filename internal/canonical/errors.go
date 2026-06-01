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
