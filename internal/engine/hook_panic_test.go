// Regression tests for engine-hook-panic-no-recover
// (.planning/audit/PRODUCTION-RELIABILITY-AUDIT.md). Pre-fix, a
// panicking PreHook or PostHook unwound through the HTTP handler with
// no engine-layer recover. net/http's per-handler recover kept the
// process alive but for streaming surfaces that had already written
// headers the connection was torn down with no terminal frame.
//
// Post-fix, the engine wraps every hook invocation in a defer-recover
// guard that converts panics into normal "engine: hook panic: ..."
// errors. The existing error-wrapping in Run / Collect / RunPostHooks
// then handles teardown symmetrically with a returned-error hook.

package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/testutil"
)

type panickingPreHook struct{}

func (panickingPreHook) Before(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	panic("synthetic prehook panic — engine must recover")
}

type panickingPostHook struct{}

func (panickingPostHook) After(_ context.Context, _ *canonical.ChatRequest, _ *canonical.ChatResponse) error {
	panic("synthetic posthook panic — engine must recover")
}

// TestEngine_PreHookPanic_RecoveredAsError verifies a panicking PreHook
// is converted to a normal engine error rather than crashing the
// goroutine. Before the fix this test would panic.
func TestEngine_PreHookPanic_RecoveredAsError(t *testing.T) {
	t.Parallel()
	eng := engine.New(engine.Config{
		Logger:   testutil.Logger(t),
		ACP:      nil, // PreHook panic fires before ACP is touched
		PreHooks: []engine.PreHook{panickingPreHook{}},
	})
	_, err := eng.Run(context.Background(), &canonical.ChatRequest{})
	if err == nil {
		t.Fatal("expected engine error from panicking PreHook, got nil")
	}
	if !strings.Contains(err.Error(), "engine: prehook") {
		t.Errorf("error = %v; want wrapped as engine: prehook: ...", err)
	}
	if !strings.Contains(err.Error(), "hook panic") {
		t.Errorf("error = %v; want to include 'hook panic'", err)
	}
}

// TestEngine_PostHookPanic_RecoveredAsError verifies RunPostHooks
// catches a panicking PostHook and returns it as an error.
func TestEngine_PostHookPanic_RecoveredAsError(t *testing.T) {
	t.Parallel()
	eng := engine.New(engine.Config{
		Logger:    testutil.Logger(t),
		PostHooks: []engine.PostHook{panickingPostHook{}},
	})
	err := eng.RunPostHooks(context.Background(), &canonical.ChatRequest{}, &canonical.ChatResponse{})
	if err == nil {
		t.Fatal("expected engine error from panicking PostHook, got nil")
	}
	if !strings.Contains(err.Error(), "engine: posthook") {
		t.Errorf("error = %v; want wrapped as engine: posthook: ...", err)
	}
	if !strings.Contains(err.Error(), "hook panic") {
		t.Errorf("error = %v; want to include 'hook panic'", err)
	}
	// Also: the recovered panic must NOT be wrapped as a context error
	// or any other sentinel — sanity check that error chain stays clean.
	if errors.Is(err, context.Canceled) {
		t.Errorf("error wrongly chains context.Canceled: %v", err)
	}
}
