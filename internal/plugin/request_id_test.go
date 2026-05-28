// Package plugin — Wave 0 request_id tests (Phase 8 Plan 08-01 Task 2).
//
// These tests scaffold the expectations for RequestIDHook + the
// ctx-propagation primitive BEFORE the implementation lands in Task 4.
// All tests in this file are expected to FAIL with `undefined: RequestIDHook`
// / `undefined: WithRequestID` until request_id.go is written.
//
// The ULID shape regex (^[0-9A-HJKMNP-TV-Z]{26}$) is Crockford Base32
// per RESEARCH.md §State of the Art / Code Example 7. If Task 1 redirected
// to google/uuid, swap the regex below for the UUIDv4 pattern.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"testing"

	"otto-gateway/internal/canonical"
)

// ulidShape matches a Crockford Base32 ULID (26 chars, uppercase, no
// I/L/O/U). RESEARCH.md §Code Example 7 — `ulid.Make().String()` produces
// exactly this shape.
var ulidShape = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// TestRequestID_GeneratesULID_WhenAbsent proves the hook generates a
// fresh ULID when the inbound ctx has no request id stamped. The id is
// observed via the helper RequestIDHook exposes when it stamps the ctx
// before returning (the hook's only-side-effect; the engine seam
// requires the stamping to happen at ctx-creation time, not per-Pre-loop
// — see request_id.go package comment for the rationale).
func TestRequestID_GeneratesULID_WhenAbsent(t *testing.T) {
	hook := &RequestIDHook{}

	// The hook's Before signature is (ctx, req) → (resp, err). To observe
	// the id the hook produced, we wrap an outer "stamp + read" helper
	// since the engine does not thread ctx mutations through the Pre
	// loop. The PRODUCTION path for stamping ctx is the adapter HTTP
	// handler (slice 5); for Wave 0 we call the hook directly and
	// inspect the slog record it emits.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	hook.Logger = logger

	ctx := context.Background()
	resp, err := hook.Before(ctx, &canonical.ChatRequest{})
	if err != nil {
		t.Fatalf("Before: unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("Before: want nil resp (no short-circuit), got %+v", resp)
	}

	// Hook should have emitted a log line carrying the generated id.
	// Decode the JSON record and assert the request_id is a ULID.
	var rec map[string]any
	if err := json.NewDecoder(&buf).Decode(&rec); err != nil {
		t.Fatalf("decode log record: %v (raw=%q)", err, buf.String())
	}
	id, ok := rec["request_id"].(string)
	if !ok {
		t.Fatalf("log record missing 'request_id' string field; record=%v", rec)
	}
	if !ulidShape.MatchString(id) {
		t.Errorf("generated id: want ULID shape %s, got %q", ulidShape, id)
	}
}

// TestRequestID_HonorsInboundID proves the hook honors an inbound ctx
// id (no regeneration). This is the load-bearing case for end-to-end
// correlation when the client passes its own X-Request-Id (e.g., a
// front-end tracer or an upstream gateway).
func TestRequestID_HonorsInboundID(t *testing.T) {
	const inbound = "01HZQABCDEFGHJKMNPQRSTVWXY" // 26-char ULID-shape

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	hook := &RequestIDHook{Logger: logger}

	ctx := WithRequestID(context.Background(), inbound)
	resp, err := hook.Before(ctx, &canonical.ChatRequest{})
	if err != nil {
		t.Fatalf("Before: unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("Before: want nil resp, got %+v", resp)
	}

	// Hook must NOT regenerate when an inbound id is present.
	got := RequestIDFromContext(ctx)
	if got != inbound {
		t.Errorf("RequestIDFromContext: want %q, got %q", inbound, got)
	}

	// If the hook logs anything in the inbound-honored path, it should
	// carry the SAME inbound id (proves correlation, not regeneration).
	if buf.Len() > 0 {
		var rec map[string]any
		if err := json.NewDecoder(&buf).Decode(&rec); err == nil {
			if id, ok := rec["request_id"].(string); ok && id != inbound {
				t.Errorf("log record request_id: want %q, got %q (regenerated?)", inbound, id)
			}
		}
	}
}

// TestRequestIDFromContext_AbsentReturnsEmpty proves the accessor is
// safe to call from any code path even when no id has been stamped
// (e.g., a unit test that constructs a bare context.Background or a
// handler running before any middleware). Empty string is the
// documented absent-value.
func TestRequestIDFromContext_AbsentReturnsEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RequestIDFromContext panicked on bare ctx: %v", r)
		}
	}()

	got := RequestIDFromContext(context.Background())
	if got != "" {
		t.Errorf("absent: want empty string, got %q", got)
	}
}

// TestRequestID_SlogCorrelation proves the ctx-propagation primitive
// works end-to-end with slog: a record emitted inside a ctx descended
// from WithRequestID carries the request_id when the producer reads it
// via RequestIDFromContext + slog.With (the documented OBSV-03 idiom).
func TestRequestID_SlogCorrelation(t *testing.T) {
	const fixedID = "TEST-ID-XYZ"

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	ctx := WithRequestID(context.Background(), fixedID)
	logger.With("request_id", RequestIDFromContext(ctx)).Info("downstream.span")

	var rec map[string]any
	if err := json.NewDecoder(&buf).Decode(&rec); err != nil {
		t.Fatalf("decode log record: %v (raw=%q)", err, buf.String())
	}
	id, ok := rec["request_id"].(string)
	if !ok {
		t.Fatalf("log record missing 'request_id' field; record=%v", rec)
	}
	if id != fixedID {
		t.Errorf("slog correlation: want %q, got %q", fixedID, id)
	}
}
