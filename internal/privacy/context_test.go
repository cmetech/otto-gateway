package privacy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestContextStampHTTPContextUsesTrustedSurfaceAndBoundedHeaders(t *testing.T) {
	headers := make(http.Header)
	headers.Set("X-GW-Privacy-Profile", "strict")
	headers.Set("X-GW-Privacy-Scope", "run-7f29b4d4")
	headers.Set("X-GW-Skill", strings.Repeat("x", 80))
	headers.Set("X-Flow-Name", "ignored-fallback")
	headers.Set("X-GW-Surface", "caller-spoof")

	ctx, state := StampHTTPContext(context.Background(), headers, "openai")
	fromContext, ok := StateFromContext(ctx)
	if !ok || fromContext != state {
		t.Fatalf("StateFromContext: got %p, %v; want %p, true", fromContext, ok, state)
	}
	got := state.Metadata()
	if got.RequestedProfile != "strict" || got.ScopeID != "run-7f29b4d4" {
		t.Fatalf("privacy metadata: %#v", got)
	}
	if got.Surface != "openai" {
		t.Fatalf("surface trusted caller header: got %q", got.Surface)
	}
	if got.Workload != strings.Repeat("x", 64) {
		t.Fatalf("workload: length=%d value=%q", len(got.Workload), got.Workload)
	}
}

func TestContextStampHTTPContextFallsBackToFlowName(t *testing.T) {
	headers := make(http.Header)
	headers.Set("X-Flow-Name", "network-hardening")
	_, state := StampHTTPContext(context.Background(), headers, "anthropic")
	if got := state.Metadata().Workload; got != "network-hardening" {
		t.Fatalf("workload: got %q", got)
	}
}

func TestContextRequestStateCountersAreBoundedAndConcurrentSafe(t *testing.T) {
	state := NewRequestState(RequestMetadata{Surface: strings.Repeat("s", 200), Workload: strings.Repeat("w", 200)})
	meta := state.Metadata()
	if len(meta.Surface) > maxSurfaceLength || len(meta.Workload) > maxWorkloadLength {
		t.Fatalf("unbounded metadata: %#v", meta)
	}

	for range 10 {
		state.addTransformed(1)
		state.addRestored(1)
		state.addBlocked(1)
	}
	transformed, restored, blocked := state.counts()
	if transformed != 10 || restored != 10 || blocked != 10 {
		t.Fatalf("counts: got (%d,%d,%d)", transformed, restored, blocked)
	}
}

func TestContextRequestStateRejectsOversizedAuthorizedToken(t *testing.T) {
	state := NewRequestState(RequestMetadata{})
	if state.authorizeToken(strings.Repeat("x", 513)) {
		t.Fatal("oversized authorized token was retained")
	}
}

func TestContextAbsentOrNilStateIsSafe(t *testing.T) {
	if state, ok := StateFromContext(context.Background()); ok || state != nil {
		t.Fatalf("unstamped state: got %p, %v", state, ok)
	}
	ctx := WithRequestState(context.Background(), nil)
	if state, ok := StateFromContext(ctx); ok || state != nil {
		t.Fatalf("nil stamped state: got %p, %v", state, ok)
	}
}

func TestErrorContainsOnlyStableCodeAndStage(t *testing.T) {
	cause := errors.New("raw corey@example.com and [PII:Email:secret]")
	err := &Error{Code: CodeInputBlocked, Stage: "inbound", Cause: cause}
	if got, want := err.Error(), "privacy_input_blocked: inbound"; got != want {
		t.Fatalf("Error: got %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatal("Error does not unwrap cause")
	}
}

func TestErrorInfoIsExactClosedMap(t *testing.T) {
	cases := []struct {
		code   string
		status int
	}{
		{code: CodeRequestInvalid, status: 400},
		{code: CodeProfileUnavailable, status: 400},
		{code: CodeScopeClosed, status: 409},
		{code: CodeInputBlocked, status: 422},
		{code: CodeOutputBlocked, status: 502},
		{code: CodeCapacityExceeded, status: 503},
		{code: CodeInternalError, status: 503},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			status, code, ok := ErrorInfo(&Error{Code: tc.code, Stage: "test", Cause: errors.New("raw")})
			if !ok || status != tc.status || code != tc.code {
				t.Fatalf("ErrorInfo: got (%d,%q,%v)", status, code, ok)
			}
		})
	}

	status, code, ok := ErrorInfo(&Error{Code: "privacy_unknown", Stage: "test"})
	if ok || status != 0 || code != "" {
		t.Fatalf("unknown ErrorInfo: got (%d,%q,%v)", status, code, ok)
	}
	if _, _, ok := ErrorInfo(errors.New("raw")); ok {
		t.Fatal("ordinary error unexpectedly classified")
	}
}
