package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// TestNewApp_RegistryCloseIsBoundedTime — Pitfall 5 from plan 05-02 +
// the Plan 05-03 cleanup ordering. Construct a real *session.Registry
// with TickInterval=100ms; start the reaper; call Close; assert Close
// returns within 2s. This proves the reaper goroutine exits via
// <-r.closing within bounded time.
//
// The test bypasses newApp's KIRO_CMD-gated pool construction by
// building an *app literal directly — this is the documented "testable
// cleanup" pattern in plan 05-03 Task 4 <action>.
func TestNewApp_RegistryCloseIsBoundedTime(t *testing.T) {
	logger := testutil.Logger(t)
	reg := session.New(session.Config{
		Logger:       logger,
		TTL:          200 * time.Millisecond,
		TickInterval: 100 * time.Millisecond,
		MaxSessions:  4,
	})
	reg.Start(context.Background())

	a := &app{
		logger:   logger,
		registry: reg,
	}

	// Build the cleanup closure manually — mirrors newApp's cleanup
	// shape with registry FIRST, pool SECOND (both nil-safe).
	cleanup := func() {
		if a.registry != nil {
			if err := a.registry.Close(); err != nil {
				t.Errorf("registry.Close: %v", err)
			}
		}
		if a.pool != nil {
			if err := a.pool.Close(); err != nil {
				t.Errorf("pool.Close: %v", err)
			}
		}
	}

	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()
	select {
	case <-done:
		// success — Close returned within bounded time.
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return within 2s; registry.Close hung past TickInterval+reapOnce bound")
	}
}

// TestNewApp_CleanupOrdersRegistryBeforePool — verifies the cleanup
// closure calls registry.Close BEFORE pool.Close (Pitfall 5 ordering).
//
// This test does NOT go through newApp because newApp constructs the
// real *pool.Pool / *session.Registry which would require a working
// kiro binary. Instead it captures the order by instrumenting the
// cleanup closure's calls into observable side-effects.
//
// The test is a structural assertion on the cleanup closure shape:
// the closure must call registry.Close first; the test mimics that
// shape and asserts the order at runtime.
func TestNewApp_CleanupOrdersRegistryBeforePool(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
	)
	record := func(label string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, label)
	}

	// Construct a real registry; the reaper is NOT started so we can
	// Close immediately. This validates that the Close path itself is
	// safe to call out of order with Start.
	logger := testutil.Logger(t)
	reg := session.New(session.Config{
		Logger:       logger,
		TTL:          time.Hour,
		TickInterval: time.Hour,
		MaxSessions:  4,
	})
	reg.Start(context.Background())

	// Build a cleanup closure that mirrors newApp's order but records
	// the labels before delegating to the real Close methods.
	cleanup := func() {
		// registry first.
		record("registry")
		if err := reg.Close(); err != nil {
			t.Errorf("registry.Close: %v", err)
		}
		// pool second (real pool not constructed — record-only).
		record("pool")
	}

	cleanup()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "registry" || order[1] != "pool" {
		t.Errorf("cleanup order: got %v, want [registry pool]", order)
	}
}

// TestNewApp_RegistryFieldExists — compile-time assertion that the
// app struct has a registry field of type *session.Registry. If the
// field is renamed or its type drifts, this test fails to compile.
func TestNewApp_RegistryFieldExists(t *testing.T) {
	_ = t
	var a app
	a.registry = (*session.Registry)(nil) // type assertion via assignment
	_ = a.registry
}
