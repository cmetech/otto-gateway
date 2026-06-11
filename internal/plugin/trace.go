// Quick 260529-ll2 — trace.go: ChatTraceHook (Pre+Post NDJSON tracer).
//
// Purpose: write one NDJSON line per chat-shaped request to a dedicated
// chat-trace.log on engine entry (stage="pre_chain_in") and one more line
// on engine exit (stage="post_chain_out"). The pre line captures the
// post-adapter canonical request — full messages slice, system prompt,
// tools shape — and the post line captures the aggregated canonical
// response plus a duration_ms field bridging the two via request_id.
//
// KEY CORRECTNESS INVARIANT (load-bearing, do NOT reorder):
//
//	ChatTraceHook MUST be first in the Pre chain (index 0) so it
//	observes the canonical request BEFORE PIIRedactionHook mutates
//	req.Messages in place. The whole product value of chat-trace.log is
//	recording what the client *actually said* — pre-redaction. main.go's
//	wiring documents this invariant verbatim. The
//	TestChatTraceHook_RecordsPreRedactionContent regression test
//	guards the ordering by composing the relevant chain prefix and
//	asserting the NDJSON line emitted by ChatTraceHook.Before contains
//	the raw, non-redacted string. A future refactor that "tidies" the
//	Pre slice order would break this contract silently otherwise.
//
// STREAMING SCOPE (v1):
//
//	After fires once after engine.Run completes, observing the
//	AGGREGATED canonical response. Per-chunk streaming records are a
//	future enhancement (would multiply the privacy surface area —
//	threat T-ll2-06 accepted out of scope).
//
// DESCRIBE WHITELIST POLICY:
//
//	Describe() returns ONLY the keys {"enabled", "output_path"}. It
//	MUST NEVER include any key whose value is or contains raw request
//	content — messages, system, tools, content. TestChatTraceHook_DescribeNoSecrets
//	walks the returned map and fails on any forbidden key substring.
//	This is Pitfall 9 (describe-whitelist, never blacklist) applied to
//	the new hook.
//
// THREAT MAPPINGS:
//
//	T-ll2-01 (file leak): the writer is a timberjack rotator opened
//	with FileMode=0o600 in main.go — the hook itself is writer-agnostic
//	and only enforces atomic line writes via h.mu.
//	T-ll2-04 (Describe leak): see DESCRIBE WHITELIST POLICY above.
//	T-ll2-05 (unbounded growth): caller (main.go) supplies the rotator;
//	hook itself never opens files.
//	T-ll2-07 (chain reorder): see KEY CORRECTNESS INVARIANT.
//	T-ll2-08 (startTimes leak): LoadAndDelete in After prevents the
//	sync.Map from growing unbounded across long-lived processes —
//	mirrors LoggingHook.startTimes (logging.go pattern).

package plugin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// ChatTraceHook implements both engine.PreHook and engine.PostHook. See
// the package docstring for the chain-order invariant and the streaming
// scope rationale.
//
// Writer is the NDJSON sink — typically a *timberjack.Logger opened by
// main.go when cfg.ChatTrace=true. nil Writer + Enabled=true short-
// circuits the write path (no panic; the record is silently dropped —
// caller should always wire a writer when enabling).
//
// Enabled is the work-doing toggle; Enabled=false makes Before and
// After total no-ops (D-02 two-knob model carried forward — main.go's
// "construct only when cfg.ChatTrace=true" pattern means this flag is
// effectively always true at runtime, but the toggle keeps unit tests
// cheap).
//
// Logger is optional; nil falls back to slog.Default() per-call so
// tests constructing a bare &ChatTraceHook{} don't NPE on internal
// debug emissions (currently none, but reserved).
//
// mu serializes json.Encoder writes so concurrent Before / After calls
// across goroutines don't interleave bytes within a single NDJSON line.
//
// startTimes bridges Pre→Post duration measurement. Keyed by
// request_id. LoadAndDelete in After prevents unbounded growth across
// long-lived processes (T-ll2-08 mitigation — same pattern as
// LoggingHook.startTimes).
type ChatTraceHook struct {
	Writer     io.Writer
	Enabled    bool
	Logger     *slog.Logger
	mu         sync.Mutex
	startTimes sync.Map // map[string]time.Time, request_id → Before timestamp
}

// Name reports the filter-discovery name for chain.Filter. The exact
// string "ChatTraceHook" is part of the operator-facing
// ENABLED_HOOKS allowlist contract.
func (h *ChatTraceHook) Name() string { return "ChatTraceHook" }

// Describe publishes the hook's safe-to-publish config for /health/hooks
// (OBSV-04 carried forward). Kind is "Pre,Post" because ChatTraceHook
// uniquely implements both interfaces and appears in both slices —
// reporting the combined kind keeps the de-dup hint in the wire shape.
//
// Config exposes ONLY:
//
//	enabled       bool   — operator can confirm work-doing state
//	output_path   string — empty when Writer is not a known-named sink;
//	                       main.go wires this to cfg.ChatTraceFile via
//	                       a thin shim if a future operator-facing
//	                       observability surface needs it. For v1 we
//	                       report "" to keep the hook writer-agnostic
//	                       and the keyset minimal.
//
// NEVER returns keys whose value contains request content — messages,
// system, tools, or content. Test TestChatTraceHook_DescribeNoSecrets
// walks the map and fails on forbidden substrings.
func (h *ChatTraceHook) Describe() (kind string, config map[string]any) {
	return "Pre,Post", map[string]any{
		"enabled":     h.Enabled,
		"output_path": "",
	}
}

// logger returns h.Logger if set, else slog.Default(). Per-call
// fallback (never cached) so changes to slog.Default at boot are
// observed. Currently only used by internal debug emissions on write
// failures (rare).
func (h *ChatTraceHook) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// preRecord is the NDJSON shape emitted by Before. Field order is
// deliberate: ts and stage are the lexicographic identifiers an
// operator scans first; request_id is the correlation key; the
// payload-heavy fields trail.
type preRecord struct {
	TS           string               `json:"ts"`
	Stage        string               `json:"stage"`
	RequestID    string               `json:"request_id"`
	Surface      string               `json:"surface"`
	Model        string               `json:"model"`
	MessageCount int                  `json:"message_count"`
	Messages     []canonical.Message  `json:"messages"`
	System       string               `json:"system,omitempty"`
	Tools        []canonical.ToolSpec `json:"tools,omitempty"`
}

// postRecord is the NDJSON shape emitted by After. duration_ms is the
// time.Since(Before-stamp) bridge in milliseconds; 0 when Before was
// never paired (defensive — should not happen in practice).
type postRecord struct {
	TS         string                  `json:"ts"`
	Stage      string                  `json:"stage"`
	RequestID  string                  `json:"request_id"`
	Surface    string                  `json:"surface"`
	StopReason canonical.StopReason    `json:"stop_reason"`
	Content    []canonical.ContentPart `json:"content,omitempty"`
	DurationMS int64                   `json:"duration_ms"`
}

// Before emits the pre_chain_in NDJSON line and stashes the start
// timestamp in startTimes for After to consume.
//
// Algorithm:
//  1. Enabled=false → return (nil, nil) immediately (total no-op).
//  2. Mint a fresh request_id via NewRequestID when ctx has no id.
//     We do NOT call WithRequestID on a returned ctx because engine.Run
//     does not propagate the PreHook ctx return value (08-RESEARCH
//     OQ-1); the locally-minted id is written into the NDJSON line and
//     RequestIDHook downstream honors any inbound id from the
//     adapter-stamped ctx. Net result: when the adapter stamps an id
//     (production path), ChatTraceHook and RequestIDHook see the same
//     id; when nothing stamps (unit tests / direct invocation),
//     ChatTraceHook's locally-minted id is in the NDJSON line but is
//     NOT propagated to downstream hooks — acceptable because the
//     load-bearing case is operator-side trace correlation, which the
//     adapter stamp covers.
//  3. Compose preRecord with ts (RFC3339Nano, monotonic stripped),
//     surface from SurfaceFromContext, and the full canonical req
//     fields (Messages, System, Tools).
//  4. Encode under h.mu so concurrent After calls cannot interleave.
//  5. Stash startTimes[rid] = time.Now() for After.
//  6. Return (nil, nil) — never short-circuits, never errors.
//
// Nil req → record minimal fields and skip the messages payload (defensive
// — would only occur if the engine contract drifts).
func (h *ChatTraceHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if !h.Enabled {
		return nil, nil
	}

	rid := RequestIDFromContext(ctx)
	if rid == "" {
		rid = NewRequestID()
	}
	surface, _ := SurfaceFromContext(ctx)

	rec := preRecord{
		TS:        nowRFC3339Nano(),
		Stage:     "pre_chain_in",
		RequestID: rid,
		Surface:   surface,
	}
	if req != nil {
		rec.Model = req.Model
		rec.MessageCount = len(req.Messages)
		rec.Messages = req.Messages
		rec.System = req.System
		rec.Tools = req.Tools
	}

	h.emit(rec)

	// WR-02 (phase 16 review) — mirror the logging.go empty-rid guard.
	// Two bugs collapsed into one fix:
	//
	//  1. The local `rid` above may be freshly minted via NewRequestID()
	//     when ctx had no id. After's path looks up startTimes keyed on
	//     RequestIDFromContext(ctx) — if ctx was empty, that lookup
	//     returns "" and never finds the minted-rid entry, so every
	//     empty-ctx-rid request leaks an entry in the sync.Map.
	//
	//  2. When RequestIDHook is filtered out of ENABLED_HOOKS and
	//     ChatTraceHook is wired (the CHAT_TRACE=true auto-prepend path
	//     in config.go:615), two concurrent requests would both Store
	//     under "" — second Before overwrites first; both Afters race
	//     for the single LoadAndDelete; one After observes the OTHER
	//     request's stamp and emits a duration_ms from the wrong start.
	//
	// Fix: only Store when ctx has a real request_id. Empty-rid Afters
	// will LoadAndDelete("") which returns nothing and duration_ms = 0,
	// matching LoggingHook's empty-rid path. emptyRequestIDWarnOnce is
	// shared with LoggingHook (logging.go:70) so the warn is once per
	// process across both hooks.
	ctxRID := RequestIDFromContext(ctx)
	if ctxRID == "" {
		emptyRequestIDWarnOnce.Do(func() {
			h.logger().Warn("plugin.chat_trace.empty_request_id",
				"note", "duration_ms will be 0; ensure RequestIDHook is enabled. Logging once per process.")
		})
		return nil, nil
	}
	h.startTimes.Store(ctxRID, time.Now())
	return nil, nil
}

// After emits the post_chain_out NDJSON line and reclaims the
// startTimes entry minted by Before.
//
// Algorithm:
//  1. Enabled=false → return nil (total no-op).
//  2. Read request_id from ctx; LoadAndDelete the matching
//     startTimes entry (prevents unbounded sync.Map growth).
//  3. Compute duration_ms from time.Since(start).
//  4. Compose postRecord with ts, surface, stop_reason, full
//     response content, duration_ms.
//  5. Encode under h.mu.
//  6. Return nil — never errors.
//
// Nil resp → record minimal fields (duration_ms still emitted so
// the trace correlation stays useful even on engine error paths).
func (h *ChatTraceHook) After(ctx context.Context, _ *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	if !h.Enabled {
		return nil
	}

	rid := RequestIDFromContext(ctx)
	// G-1 (REL-HOOKS-01) — LoadAndDelete unconditionally so the
	// startTimes sync.Map entry is reclaimed on every code path,
	// including the non-streaming error paths in engine.Collect /
	// anthropic.CollectAnthropicChat which now call After with a
	// nil resp.
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
	surface, _ := SurfaceFromContext(ctx)

	rec := postRecord{
		TS:         nowRFC3339Nano(),
		Stage:      "post_chain_out",
		RequestID:  rid,
		Surface:    surface,
		DurationMS: durationMS,
	}
	if resp == nil {
		// G-1 error path — entry was already reclaimed above; emit
		// the post_chain_out record with the duration_ms bridge but
		// no stop_reason / content, so chat-trace.log is complete
		// for failed requests.
		h.emit(rec)
		return nil
	}
	rec.StopReason = resp.StopReason
	rec.Content = resp.Message.Content

	h.emit(rec)
	return nil
}

// emit writes one NDJSON record. json.NewEncoder appends \n
// automatically, satisfying the NDJSON contract. Writes are guarded by
// h.mu so concurrent Before / After calls across goroutines cannot
// interleave bytes within a single line.
//
// Writer=nil short-circuits silently (caller misconfigured the hook).
// Encoder errors are logged at DEBUG via h.logger() and otherwise
// dropped — chat-trace is observational; a write failure should not
// fail the request.
func (h *ChatTraceHook) emit(v any) {
	if h.Writer == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := json.NewEncoder(h.Writer).Encode(v); err != nil {
		h.logger().Debug("plugin.chat_trace.emit_error", "err", err)
	}
}

// nowRFC3339Nano returns the current wall-clock time in RFC3339Nano
// with the monotonic-clock reading stripped (so the timestamp is
// directly comparable to JSON-decoded times in tests).
func nowRFC3339Nano() string {
	return time.Now().Round(0).Format(time.RFC3339Nano)
}

// Compile-time interface assertions — both PreHook and PostHook must be
// satisfied (ChatTraceHook is the second Pre+Post entity in the chain
// after LoggingHook; main.go wires it as a single instance referenced
// in both slices).
var (
	_ engine.PreHook  = (*ChatTraceHook)(nil)
	_ engine.PostHook = (*ChatTraceHook)(nil)
)
