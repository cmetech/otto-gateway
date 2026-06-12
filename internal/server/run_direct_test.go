package server

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"otto-gateway/internal/config"
)

// TestServer_Run_DirectShutdown is the QUAL-03 guard rail: it asserts that a
// Server constructed via the public New* path has a nil forceCloseCh (the
// allocation has been relocated to RunUntilSignal — D-20-04), and that direct
// callers of Run can shut down cleanly via ctx.Done() despite that nil
// channel. The nil-channel select arm in Run() is intentionally never taken
// for direct Run callers; this test guards against accidental re-introduction
// of the allocation in any constructor.
func TestServer_Run_DirectShutdown(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:     "127.0.0.1:0",
		PingInterval: 60 * time.Second,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	s := New(cfg, logger, "test")

	// Sentinel: forceCloseCh must be nil immediately after construction so
	// the nil-channel select-never invariant in Run() holds for direct
	// callers. RunUntilSignal allocates it before calling Run.
	if s.forceCloseCh != nil {
		t.Fatalf("forceCloseCh must be nil immediately after New(); got non-nil channel — D-20-04 constructor invariant violated")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx)
	}()

	// Give the server a moment to bind and enter the serve loop, then trigger
	// graceful shutdown via context cancellation. This exercises Run's select
	// arm — both the shutdownErrCh arm AND (implicitly) the nil forceCloseCh
	// arm which must never fire.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Shutdown on a server with no in-flight requests returns nil.
		if err != nil {
			t.Fatalf("Run returned error on ctx.Done(): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s after context cancel — nil forceCloseCh arm may have fired incorrectly, or goroutine leaked")
	}
}

// TestServer_NewWithCommit_NilForceClose mirrors the direct-shutdown test but
// constructs via NewWithCommit (the underlying constructor New delegates to)
// so a future refactor that introduces a divergent allocation path is caught
// independently.
func TestServer_NewWithCommit_NilForceClose(t *testing.T) {
	cfg := config.Config{HTTPAddr: "127.0.0.1:0"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewWithCommit(cfg, logger, "test", "abc1234")
	if s.forceCloseCh != nil {
		t.Fatalf("forceCloseCh must be nil after NewWithCommit; got non-nil — D-20-04 violation")
	}
}
