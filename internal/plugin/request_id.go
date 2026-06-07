// Phase 8 Plan 08-01 — request_id.go (Task 4 full implementation).
//
// RequestIDHook is the first PreHook in the day-one chain
// (08-CONTEXT.md PLUG-04). It generates a per-request ULID (or honors an
// inbound id) and emits a slog correlation record so every downstream
// span — Pre, engine, ACP, Post — can read the SAME id through
// RequestIDFromContext(ctx) and tag its slog records with
// `request_id=<ulid>` (OBSV-03 correlation seam).
//
// Architectural note (08-RESEARCH.md Open Question 3): RequestIDHook is
// Pre-ONLY in v1. A Post-side hook to emit a final correlation record is
// trivially added later by LoggingHook (slice 3); duplicating Post logic
// here would muddy the seam.
//
// Engine ctx-propagation seam: the engine's PreHook contract is
// (ctx, req) → (resp, err) per internal/engine/hooks.go:30-32; the
// engine's Run loop does NOT thread the ctx return back through
// subsequent Pre hooks (see internal/engine/engine.go:152-162). The
// SUPPORTED way to make a request id visible to all downstream code is
// for the adapter HTTP handler to call WithRequestID(ctx, id) BEFORE
// invoking engine.Run. Slice 5 (main.go wiring) will add the
// per-adapter middleware; this slice ships the primitives.
//
// Until the adapter wires WithRequestID, RequestIDHook still fulfills
// the OBSV-03 promise via the slog correlation log line it emits — the
// id is observable in process logs even when the inner-loop ctx doesn't
// carry it forward.
//
// References:
//   - 08-CONTEXT.md PLUG-04, OBSV-03, D-04 (RequestID first in chain order)
//   - 08-PATTERNS.md Pattern 3 (typed-key ctx + accessor pair)
//   - 08-RESEARCH.md Pattern 3, Code Example 7, Open Question 3
//   - T-8-RID-1 mitigation: ctxKey is an unexported struct type — Go
//     type-identity rules prevent cross-package key collision.
//   - T-8-RID-2 mitigation: ulid.Make() uses a process-global monotonic
//     entropy source seeded from crypto/rand; no hand-rolled fallback.

package plugin

import (
	"context"
	"log/slog"

	"otto-gateway/internal/canonical"

	"github.com/oklog/ulid/v2"
)

// ctxKey is the unexported, struct-typed context key used to stamp the
// request id onto ctx. T-8-RID-1 mitigation — Go type-identity rules
// (a struct type is identified by its package path + type name) prevent
// any other package from constructing a key that compares equal to this
// one, so a malicious adapter / hook cannot inject a spoofed id.
//
// The single `name` field is for debugging (visible in ctx String()
// output via reflect); the value of the field is irrelevant to
// equality (each value of ctxKey with name="request-id" compares equal
// only within this package).
type ctxKey struct{ name string }

// requestIDKey is the single canonical key used by WithRequestID +
// RequestIDFromContext. Declared as a package-level var (not a const —
// const-of-struct is not a Go thing) so all call sites share the
// same value-typed key.
var requestIDKey = ctxKey{name: "request-id"}

// RequestIDHook implements engine.PreHook. It generates a fresh ULID
// when no inbound id is present on the ctx, honors the inbound id when
// present, and emits a slog correlation record either way. The hook
// never short-circuits and never returns an error (returns (nil, nil)
// unconditionally).
//
// Logger is the optional slog target; nil falls back to slog.Default()
// so unit tests that construct a bare &RequestIDHook{} don't NPE.
type RequestIDHook struct {
	Logger *slog.Logger
}

// Name reports the hook's filter-discovery name for chain.Filter
// (08-PATTERNS Pattern A — explicit Name() over reflect for
// caller-stable API).
func (h *RequestIDHook) Name() string { return "RequestIDHook" }

// Describe publishes the hook's safe-to-publish config for
// /health/hooks (OBSV-04). Kind is "Pre" per 08-RESEARCH.md
// Open Question 3 (RequestIDHook is Pre-only in v1). Config exposes
// the id format so operators can confirm the generator at a glance;
// the actual generator (oklog/ulid/v2.Make) is an implementation
// detail not on the wire.
func (h *RequestIDHook) Describe() (kind string, config map[string]any) {
	return "Pre", map[string]any{"format": "ulid"}
}

// Before is the PreHook entry. Algorithm:
//  1. Read any existing id from ctx (an upstream adapter may have
//     already stamped X-Request-Id via WithRequestID).
//  2. If empty, generate a fresh ULID via ulid.Make() (process-global
//     monotonic entropy source seeded from crypto/rand — T-8-RID-2
//     mitigation; never roll our own crypto/rand fallback).
//  3. Emit a slog correlation record carrying request_id so the id is
//     observable in process logs even if downstream code doesn't read
//     it from ctx (the load-bearing OBSV-03 promise — see file
//     docstring on the ctx-propagation seam).
//
// Returns (nil, nil) — no short-circuit, no error path. The hook is
// pure-observational beyond the slog emission.
func (h *RequestIDHook) Before(ctx context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	id := RequestIDFromContext(ctx)
	if id == "" {
		id = NewRequestID()
	}

	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.With("request_id", id).Info("plugin.request_id.generated")

	return nil, nil
}

// WithRequestID returns a child ctx carrying id under the unexported
// requestIDKey. Adapters / middleware call this BEFORE invoking
// engine.Run so every downstream span can read the id via
// RequestIDFromContext.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the id stamped on ctx by WithRequestID,
// or "" when no id is present. Safe to call from any layer
// (adapter, engine, ACP, hook) — empty-string-on-absent means callers
// can do `logger.With("request_id", RequestIDFromContext(ctx))`
// unconditionally without nil-checking.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// NewRequestID returns a fresh ULID string. Exported for the rare
// callers that need a new id outside the hook (e.g., the adapter
// HTTP handler stamping X-Request-Id onto the outbound response
// when the inbound request didn't carry one).
//
// ulid.Make() is the post-v2.1 recommended path — it uses a
// process-global monotonic entropy source seeded once from
// crypto/rand. Calling it from many goroutines is safe (the
// underlying entropy source is sync.Mutex-guarded internally).
func NewRequestID() string {
	return ulid.Make().String()
}
