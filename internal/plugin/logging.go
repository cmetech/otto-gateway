// Phase 8 Plan 08-03 — logging.go (Task 4 full implementation).
//
// LoggingHook is the LAST Pre hook AND the only Post hook in v1
// (08-CONTEXT.md D-04 chain order: RequestID → Auth → PIIRedaction →
// Logging). Because it sits AFTER PIIRedactionHook on Pre, the req it
// observes has already been mutated in place — logs see REDACTED
// content; raw PII never enters slog records.
//
// LoggingHook owns the canonical-layer observation of every request that
// survives auth. The records it emits are:
//
//   - plugin.before  (Pre)   : request_id, model, message_count
//   - plugin.after   (Post)  : request_id, duration_ms, stop_reason,
//                              optional redacted={Email:2, SSN:1}
//
// The redacted attr is sourced from pii.SummaryFromContext (D-04 API
// seam, populated by slice 4's PIIRedactionHook). When no Summary is on
// ctx (slice 4 not wired, or PII disabled), the attr is OMITTED — this
// is graceful degradation so slice 3 is fully runnable before slice 4
// lands.
//
// Timing seam: Pre records start = time.Now() into a sync.Map keyed by
// request_id; Post LoadAndDeletes the entry and computes
// time.Since(start). Rationale for sync.Map vs ctx-stamping the start
// time: engine.Run does NOT propagate the ctx returned from PreHook to
// subsequent hooks (see 08-RESEARCH OQ-1 / engine.go:152-162 finding
// from slice 1), so a ctx.WithValue stash in Before would NOT be visible
// to After. The sync.Map is keyed by request_id (which IS propagated by
// the adapter's WithRequestID stamp BEFORE engine entry) so the bridge
// is correlation-safe. LoadAndDelete in After also prevents map growth
// across long-lived processes (Pitfall 10-style leak protection without
// a goroutine).
//
// Threat-model mappings:
//   - T-8-PII (raw PII in logs): mitigated by D-04 chain order + the
//     attribute layout below — only structural fields (model,
//     message_count, request_id, duration_ms, stop_reason) hit slog,
//     never the request body. Source-audit test
//     (TestLoggingHook_SourceAudit_NoRawContent) regression-guards this.
//   - T-8-PII-2 (empty redaction summary leaks "no PII this request"):
//     accepted; emit only when SummaryFromContext returns ok=true so the
//     non-PII-hook-running case omits the attr entirely.
//   - T-8-LEAK-3 (slog.SetDefault leaks Logger across handlers): never
//     called. Nil Logger falls back to slog.Default() per-call only.
//   - T-8-GO-LEAK (goroutine leak from async logging): v1 is synchronous
//     — no goroutine spawned in Before or After. goleak gate enforces.
//
// References:
//   - 08-CONTEXT.md D-04 (chain order + redaction-summary contract)
//   - 08-PATTERNS.md LoggingHook block (lines 240-287)
//   - 08-RESEARCH.md Pattern 3, Pitfall 5, Pitfall 8, Pitfall 10
//   - internal/server/middleware.go:22-46 (accessLog timing blueprint)

package plugin

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin/pii"
)

// LoggingHook implements both engine.PreHook and engine.PostHook. It is
// the canonical-layer observation seam for every request that survives
// auth. See package docstring for the threat-model context and the
// chain-order invariant (D-04).
//
// Logger is the slog target; nil falls back to slog.Default() per-call so
// tests constructing a bare &LoggingHook{} don't NPE and so production
// deployments that forget to wire a Logger still get audit-grade output.
//
// startTimes bridges Pre→Post duration measurement. Keyed by request_id
// (which travels via ctx independently of the PreHook ctx-propagation
// quirk; see package docstring). LoadAndDelete in After prevents
// unbounded growth.
type LoggingHook struct {
	Logger     *slog.Logger
	startTimes sync.Map // map[string]time.Time, request_id → Before timestamp
}

// Name reports the filter-discovery name for chain.Filter
// (08-PATTERNS Pattern A — explicit Name() over reflect for stable API).
func (h *LoggingHook) Name() string { return "LoggingHook" }

// Describe publishes the hook's safe-to-publish config for
// /health/hooks (OBSV-04). Kind is "Pre,Post" because LoggingHook
// uniquely implements both interfaces and appears in both slices —
// reporting the combined kind in Describe lets the introspection
// consumer de-duplicate the row at presentation time.
//
// Config exposes only the active log level (e.g., "INFO"). Patterns,
// keys, request bodies, and the sync.Map state are NEVER published
// (Pitfall 9: describe whitelist, not blacklist).
func (h *LoggingHook) Describe() (kind string, config map[string]any) {
	return "Pre,Post", map[string]any{"level": logLevelString(h.logger())}
}

// logger returns h.Logger if set, otherwise slog.Default(). Per-call
// fallback (never cached) so changes to slog.Default at boot are seen.
func (h *LoggingHook) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// logLevelString probes a logger for its active level by checking
// Enabled() at each well-known level (descending priority). Returns
// "INFO" as a conservative default when probing fails. This is a
// cheap query — slog.Logger.Enabled is O(1).
func logLevelString(l *slog.Logger) string {
	if l == nil {
		return "INFO"
	}
	ctx := context.Background()
	switch {
	case l.Enabled(ctx, slog.LevelDebug):
		return "DEBUG"
	case l.Enabled(ctx, slog.LevelInfo):
		return "INFO"
	case l.Enabled(ctx, slog.LevelWarn):
		return "WARN"
	case l.Enabled(ctx, slog.LevelError):
		return "ERROR"
	default:
		return "INFO"
	}
}

// Before emits "plugin.before" carrying request_id + structural request
// shape (model, message_count). It then records the start timestamp into
// the sync.Map keyed by request_id so After can compute duration.
//
// Returns (nil, nil) — LoggingHook never short-circuits and never errors.
//
// Attributes shipped (load-bearing for slice 5's e2e expectations):
//
//	request_id     string  — from RequestIDFromContext(ctx)
//	model          string  — req.Model (may be empty for "auto")
//	message_count  int     — len(req.Messages)
//
// NOTE on T-8-PII: req.Messages itself is NEVER passed to slog. Only
// the count is published. The source-audit test enforces this.
func (h *LoggingHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	rid := RequestIDFromContext(ctx)
	model := ""
	mcount := 0
	if req != nil {
		model = req.Model
		mcount = len(req.Messages)
	}

	h.logger().LogAttrs(ctx, slog.LevelInfo, "plugin.before",
		slog.String("request_id", rid),
		slog.String("model", model),
		slog.Int("message_count", mcount),
	)

	// Stash start time keyed by request_id. The sync.Map is the safe
	// cross-Pre/Post bridge — see package docstring rationale.
	h.startTimes.Store(rid, time.Now())
	return nil, nil
}

// After emits "plugin.after" carrying request_id, duration_ms, stop_reason
// (when resp is non-nil), and the optional redacted attr from
// pii.SummaryFromContext.
//
// Attributes shipped (load-bearing for slice 5's e2e expectations):
//
//	request_id   string                — from RequestIDFromContext(ctx)
//	duration_ms  int64                 — time.Since(Before-stamp).ms
//	stop_reason  int (slog.Any wrap)   — resp.StopReason (only when resp != nil)
//	redacted     map[string]int        — pii.Summary.Counts() (only when present)
//
// NOTE: stop_reason is rendered via slog.Any on the canonical.StopReason
// int — slog's default encoder will produce the underlying integer
// (e.g., 1 for StopEndTurn). Slice 5's e2e tests should assert on the
// integer form, not a string name. See 08-03-SUMMARY's assumption note.
//
// LoadAndDelete (not just Load) prevents the sync.Map from growing
// unbounded across long-lived processes — every Before-stored entry is
// reclaimed by its matching After call. Defensive fallback: if no start
// time is recorded (shouldn't happen in practice; would only occur if
// After ran without a paired Before), duration_ms = 0.
func (h *LoggingHook) After(ctx context.Context, _ *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	rid := RequestIDFromContext(ctx)

	var start time.Time
	if v, ok := h.startTimes.LoadAndDelete(rid); ok {
		if t, tok := v.(time.Time); tok {
			start = t
		}
	}
	var durationMS int64
	if !start.IsZero() {
		durationMS = time.Since(start).Milliseconds()
	}

	attrs := make([]slog.Attr, 0, 4)
	attrs = append(attrs,
		slog.String("request_id", rid),
		slog.Int64("duration_ms", durationMS),
	)
	if resp != nil {
		// slog.Any on the typed StopReason; consumer-side decode is
		// integer (per canonical/stop_reason.go iota positions).
		attrs = append(attrs, slog.Any("stop_reason", resp.StopReason))
	}

	// D-04 seam: emit redaction summary when present. slog.Any on the
	// Counts() map specifically — NEVER on the Summary struct itself
	// (would leak internal mu/counts field shape and bypass the
	// MarshalJSON nil-safe path).
	if s, ok := pii.SummaryFromContext(ctx); ok && s != nil {
		attrs = append(attrs, slog.Any("redacted", s.Counts()))
	}

	h.logger().LogAttrs(ctx, slog.LevelInfo, "plugin.after", attrs...)
	return nil
}

// Compile-time interface assertions — both PreHook and PostHook must be
// satisfied (LoggingHook is the only Pre+Post entity in v1 chain).
var (
	_ engine.PreHook  = (*LoggingHook)(nil)
	_ engine.PostHook = (*LoggingHook)(nil)
)
