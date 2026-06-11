// Package admin — SSE log-tail handler.
//
// This file implements the SSE handler that streams log lines from the shared
// Tailer to N connected browser clients. The design mirrors
// internal/adapter/anthropic/sse.go's D-05 single-goroutine invariant:
// exactly ONE goroutine per HTTP request writes to the response writer,
// pulling lines from the Tailer's per-subscriber channel.
//
// Design invariants:
//   - D-05: Single goroutine per request owns the http.ResponseWriter.
//   - D-08: Live log tail at GET /admin/logs/stream.
//   - D-09: The shared Tailer goroutine is started/stopped lazily via
//     Subscribe/Unsubscribe — this handler never starts its own poll loop.
//   - D-11: SSE handler exits cleanly on r.Context() cancellation.
//
// Threat mitigations:
//   - T-6.1-11: Non-blocking fan-out (Tailer.broadcast — slow subscriber drops).
//   - T-6.1-12: defer Unsubscribe ensures cleanup on any exit path.
//   - T-6.1-13: writeSSELine splits multi-line payloads into multiple data:
//     prefixes so a log line containing \n cannot close a frame.
//   - T-6.1-14: X-Accel-Buffering: no prevents nginx from buffering the stream.
//   - T-6.1-16: JS side uses textContent (not innerHTML) for DOM injection.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// SSEKeepaliveInterval is the cadence at which the SSE emitter sends
// `event: ping` keepalive frames during idle stretches. Matches the
// cadence in internal/adapter/anthropic/sse.go.
const SSEKeepaliveInterval = 15 * time.Second

// SSEFanoutBuffer is the per-subscriber channel buffer capacity.
// Referenced from tail.go's TailerSubChanBuffer; declared here as an
// alias so callers that import sse.go get the same constant without
// importing tail.go explicitly.
// Value: TailerSubChanBuffer (16). Both consts must stay in sync.
// Rationale for duplication (vs. one source of truth): Go const iota
// across files in the same package is fine; a simple numeric literal keeps
// the dep graph flat (tail.go and sse.go are in the same package, so this
// is not a real dependency). If you change one, grep for the other.
// At 16-line buffer, a subscriber 4 seconds behind the tailer at 250ms poll
// cadence will start dropping — acceptable for admin observers.
const SSEFanoutBuffer = TailerSubChanBuffer // 16

// ---------------------------------------------------------------------------
// sseHandler
// ---------------------------------------------------------------------------

// sseHandler handles GET /admin/logs/stream.
// It opens a Server-Sent Events connection, sends the last ≤500 lines
// from the Tailer ring buffer as backfill, then forwards live lines as
// they arrive from the file tailer.
//
// The handler exits when r.Context() is cancelled (client disconnect).
// The shared Tailer goroutine is stopped lazily when this is the last
// subscriber.
func (h *handler) sseHandler(w http.ResponseWriter, r *http.Request) {
	// Pitfall 4: Flusher cast MUST be checked at handler entry. Some
	// reverse-proxy middleware (gzip, buffering) wraps the response writer
	// and removes the Flusher interface.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Quick 260529-ll2 — resolve the source query param BEFORE writing
	// SSE headers so an unknown source returns a clean 400 JSON envelope
	// instead of opening a benign empty event-stream connection.
	// Empty / absent source defaults to "main" (the first entry in
	// LogPathOrder per the conventional UI contract). The allowlist check
	// uses slices.Contains on LogPathOrder so the strict order in Deps
	// IS the authoritative valid set — keeping LogPaths and LogPathOrder
	// in sync at construction time is the caller's responsibility.
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "main"
	}
	if !slices.Contains(h.deps.LogPathOrder, source) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		// G705 mitigation: json.Encoder quote-escapes the untrusted source
		// value so it cannot inject structural JSON or HTML.
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "unknown source: " + source,
		})
		return
	}
	path := h.deps.LogPaths[source]
	// Audit admin-sse-log-source-key-mismatch-silent-broken-tailer:
	// LogPathOrder listing a source not present in LogPaths previously
	// constructed a Tailer with path="" that quietly DEBUG-logged once
	// per 250ms tick and served no data. Surface the misconfiguration
	// as a 400 with a clear envelope so operators do not chase a dead
	// log stream.
	if path == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		// G705 mitigation: json.Encoder quote-escapes the untrusted source value.
		// Preserve the original %q-quoted appearance by formatting the source via
		// strconv.Quote before embedding in the human-readable message.
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("source %q listed in LogPathOrder but missing from LogPaths", source),
		})
		return
	}
	tailer := h.tailers.Get(source, path)

	// Set SSE headers BEFORE writing any body (Pitfall 3 — nginx buffering).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)

	// Subscribe to the resolved tailer. defer Unsubscribe guarantees
	// cleanup on any exit path (ctx cancel, write error, subscriber
	// channel closed).
	sub := tailer.Subscribe(r.Context())
	defer tailer.Unsubscribe(sub)

	// Snapshot the ring buffer for backfill AFTER subscribing so we don't
	// miss any lines that arrive between subscribe and snapshot.
	snapshot := tailer.Snapshot()

	// Start the keepalive ticker.
	ticker := time.NewTicker(SSEKeepaliveInterval)
	defer ticker.Stop()

	// Run the SSE loop (factored for ticker injection in tests).
	// Pass h.deps.ShutdownCh so the loop exits promptly on gateway shutdown
	// (REL-HTTP-01). A nil channel is safe — nil select arms are never selected.
	if err := sseLoop(r.Context(), w, flusher, sub, ticker.C, snapshot, h.deps.ShutdownCh); err != nil {
		// ctx.Canceled is the expected normal exit — don't log it as an error.
		if !errors.Is(err, context.Canceled) {
			h.deps.Logger.Debug("admin: sse loop exit", "err", err)
		}
	}
}

// ---------------------------------------------------------------------------
// sseLoop (factored for ticker injection in tests)
// ---------------------------------------------------------------------------

// sseLoop runs the per-request SSE event loop. It is factored out from
// sseHandler so tests can inject a manual ticker channel for deterministic
// ping-frame testing (mirrors the anthropic/sse.go runSSEEmitterLoop pattern).
//
// Parameters:
//   - ctx: the request context; cancellation exits the loop.
//   - w: the http.ResponseWriter (also accepts io.Writer for test injection).
//   - flusher: the http.Flusher extracted from w.
//   - sub: the Tailer subscriber; sub.C receives live log lines.
//   - tickerC: receives ticks for keepalive ping frames.
//   - snapshot: backfill lines sent before entering the live loop.
//   - shutdownCh: closed when the gateway initiates graceful shutdown (REL-HTTP-01).
//     sseLoop exits within one poll interval instead of blocking the full 30s grace.
//
// Returns the exit error (ctx.Err() on normal client disconnect,
// errors.New("...") if the subscriber channel was closed by Unsubscribe,
// or errors.New("admin: gateway shutting down") on shutdownCh close).
func sseLoop(
	ctx context.Context,
	w io.Writer,
	flusher http.Flusher,
	sub *subscriber,
	tickerC <-chan time.Time,
	snapshot []string,
	shutdownCh <-chan struct{},
) error {
	// Send backfill: the current ring buffer contents (oldest first).
	for _, line := range snapshot {
		writeSSELine(w, "log", line)
	}
	if len(snapshot) > 0 {
		flusher.Flush()
	}

	// Live-stream loop (D-05: only this goroutine writes to w).
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("admin.sse: ctx done: %w", ctx.Err())

		case <-shutdownCh:
			// Gateway is shutting down — exit promptly so Shutdown() can
			// complete without blocking for the full 30s grace (REL-HTTP-01).
			return errors.New("admin: gateway shutting down")

		case <-tickerC:
			// Keepalive ping — prevents idle TCP connections from timing out.
			writeSSELine(w, "ping", "")
			flusher.Flush()

		case line, ok := <-sub.C:
			if !ok {
				// Channel closed by Unsubscribe — exit cleanly.
				return errors.New("admin: tailer closed subscriber channel")
			}
			writeSSELine(w, "log", line)
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// writeSSELine
// ---------------------------------------------------------------------------

// writeSSELine writes a single SSE event frame to w.
//
// Format (per https://html.spec.whatwg.org/multipage/server-sent-events.html):
//
//	event: <eventName>\n   (omitted if eventName == "")
//	data: <segment1>\n
//	data: <segment2>\n     (only if payload contains \n)
//	\n                     (empty line = end of event frame)
//
// Multi-line payloads are split into multiple data: lines within ONE event
// (T-6.1-13 mitigation: a log line containing \n cannot close a frame and
// become a new event boundary on the client). This is required by the SSE
// spec for multi-line payloads anyway.
//
// EventSource line-terminator handling (HTML spec §parsing-an-event-stream):
// the client treats \r, \n, AND \r\n as line terminators. A log line
// containing a bare \r (progress-bar overwrites, raw subprocess stdout,
// Windows-formatted logs missing the \n half) would split mid-data on
// the client and produce truncated visible text plus spurious fields.
// Normalize \r\n → \n and lone \r → \n BEFORE the strings.Split below
// so all three terminator forms route through the same multi-line
// splitter and never reach the client as inline terminators.
//
// Empty payload emits `data:\n\n` (an empty data field with a terminator).
//
// The function accepts io.Writer so it can be called with both
// http.ResponseWriter and strings.Builder (in tests).
func writeSSELine(w io.Writer, eventName, payload string) {
	if eventName != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", eventName)
	}
	if payload == "" {
		_, _ = fmt.Fprint(w, "data:\n\n")
		return
	}
	// Normalize CRLF and lone CR to LF so the split below handles all
	// three EventSource line terminator forms (\r, \n, \r\n).
	// Order matters: collapse \r\n first so we don't double-split a CRLF
	// into two segments.
	payload = strings.ReplaceAll(payload, "\r\n", "\n")
	payload = strings.ReplaceAll(payload, "\r", "\n")
	// Split on \n so embedded newlines produce separate data: lines within
	// the same event frame (T-6.1-13: operator log line containing \n
	// cannot inject a new SSE frame boundary).
	segments := strings.Split(payload, "\n")
	for _, seg := range segments {
		_, _ = fmt.Fprintf(w, "data: %s\n", seg)
	}
	_, _ = fmt.Fprint(w, "\n")
}
