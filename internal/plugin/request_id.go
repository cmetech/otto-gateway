// Phase 8 Plan 08-01 — request_id.go stub (Task 3 compile-unblock).
//
// This file is intentionally MINIMAL in Task 3: it declares the symbols
// request_id_test.go references so the plugin package compiles, but the
// production behavior lands in Task 4 (Implement RequestIDHook with ULID
// generation + ctx accessor).
//
// Task 4 will replace this stub with the full implementation per the
// 08-PLAN <action> block + 08-PATTERNS Pattern 3 (typed-key ctx + ULID
// generator + Describer + Name).
//
// The stub returns the natural empty values:
//   - RequestIDHook.Before is a no-op returning (nil, nil).
//   - WithRequestID stamps the id verbatim with no key collision protection.
//   - RequestIDFromContext returns "" until Task 4 wires the typed key.
//
// Wave 0 tests 6-9 are EXPECTED TO FAIL after Task 3 (RED for the
// request_id subset); Task 4 turns them GREEN.

package plugin

import (
	"context"
	"log/slog"

	"otto-gateway/internal/canonical"
)

// RequestIDHook is the first PreHook in the day-one chain — it
// generates a per-request ULID (or honors an inbound X-Request-Id) and
// stamps it onto the ctx so every downstream span (engine, ACP,
// post-hooks) reads the same id via RequestIDFromContext (OBSV-03
// correlation seam).
//
// Task 3 STUB — Task 4 implements the real ULID generation and ctx
// stamping. The Logger field is the optional slog target; nil falls
// back to slog.Default() at use-time in Task 4.
type RequestIDHook struct {
	Logger *slog.Logger
}

// Before — Task 3 STUB. Task 4 implements the ULID generation +
// inbound-id honoring + slog correlation log line.
func (h *RequestIDHook) Before(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

// WithRequestID stamps id onto ctx — Task 3 STUB returns ctx unchanged.
// Task 4 wires the unexported ctxKey for collision-safe storage.
func WithRequestID(ctx context.Context, _ string) context.Context {
	return ctx
}

// RequestIDFromContext returns the stamped id — Task 3 STUB returns "".
// Task 4 reads from the unexported ctxKey set by WithRequestID.
func RequestIDFromContext(_ context.Context) string {
	return ""
}
