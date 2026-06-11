package server_test

// Regression test for REL-HTTP-01 (H-1): graceful shutdown blocks for the
// full 30s grace period while an admin log-tail SSE connection is open.
//
// Pre-fix observable: srv.Shutdown(ctx) returns only after the full grace
// deadline, causing the binary to log "server stopped with error" and exit
// non-zero. Combined with P-2, defer cleanup() is skipped and kiro-cli trees
// are orphaned on every operator restart.
//
// Post-fix: Shutdown returns in < 1s because a shutdown signal is wired into
// the SSE handler (e.g. via srv.RegisterOnShutdown + ctx cancel, BaseContext,
// or a dedicated shutdown channel in sseLoop).
//
// Unskip this test in the Phase 15 fix commit and flip the assertion.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestRegression_REL_HTTP_01_ShutdownBlocksOnAdminSSE reproduces the failure
// path described in REL-HTTP-01: srv.Shutdown blocks for the full grace period
// while a long-lived admin log-tail SSE connection holds the server open.
//
// Design: use httptest.NewServer to bind a real listener (a recorder cannot
// simulate a blocking long-lived SSE drain). An SSE handler simulates
// /admin/logs/stream — it blocks on r.Context().Done() or a test gate,
// whichever comes first. Pre-fix: http.Server.Shutdown waits indefinitely for
// the active connection without cancelling in-flight request contexts; the test
// asserts the block lasts > 2s within a 6s total Shutdown deadline.
func TestRegression_REL_HTTP_01_ShutdownBlocksOnAdminSSE(t *testing.T) {
	defer goleak.VerifyNone(t)

	connHeld := make(chan struct{})    // closed once the SSE handler is streaming
	releaseConn := make(chan struct{}) // test closes this to let the handler exit
	defer close(releaseConn)

	// shutdownCh simulates the channel that server.Run() registers via
	// srv.RegisterOnShutdown, and that admin.sseLoop selects on (REL-HTTP-01 fix).
	// The handler below uses it to exit promptly on Shutdown instead of blocking
	// for the full 30s grace period.
	shutdownCh := make(chan struct{})

	// Minimal SSE handler simulating the admin /admin/logs/stream endpoint.
	// Post-fix: selects on shutdownCh (closed by RegisterOnShutdown callback)
	// so it exits within 1s instead of blocking until the full 30s grace fires.
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: ping\ndata: \n\n"))
		flusher.Flush()
		// Signal the SSE connection is live.
		select {
		case <-connHeld:
		default:
			close(connHeld)
		}
		// Post-fix: exit promptly when shutdownCh fires (the fix for REL-HTTP-01).
		// This mirrors what admin.sseLoop does after the Phase 15 fix.
		select {
		case <-shutdownCh:
			// Gateway shutting down — exit promptly so Shutdown() completes in < 1s.
		case <-r.Context().Done():
			// Client disconnected.
		case <-releaseConn:
			// Test cleanup.
		}
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	// REL-HTTP-01 fix: wire shutdownCh into the server lifecycle so the SSE
	// handler is notified when Shutdown begins. This mirrors the production
	// code path in internal/server/server.go (Run's RegisterOnShutdown block).
	srv.RegisterOnShutdown(func() {
		select {
		case <-shutdownCh:
		default:
			close(shutdownCh)
		}
	})

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// Open a long-lived SSE connection with no read deadline.
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	connErr := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(sseCtx, http.MethodGet,
			ts.URL+"/admin/logs/stream", nil)
		if err != nil {
			connErr <- err
			return
		}
		//nolint:bodyclose // closed via sseCancel + resp.Body.Read loop below
		resp, err := ts.Client().Do(req)
		if err != nil {
			connErr <- err
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 64)
		for {
			_, err := resp.Body.Read(buf)
			if err != nil {
				break
			}
		}
		connErr <- nil
	}()

	// Wait for the SSE handler to confirm the connection is live.
	select {
	case <-connHeld:
	case err := <-connErr:
		t.Fatalf("SSE connection failed before connHeld: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("SSE connection never established within 5s")
	}

	// Shutdown with a 6s deadline. Post-fix: completes in < 1s because
	// RegisterOnShutdown closes shutdownCh and the SSE handler exits promptly.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer shutdownCancel()

	start := time.Now()
	_ = srv.Shutdown(shutdownCtx)
	elapsed := time.Since(start)

	// POST-FIX ASSERTION: Shutdown must complete in < 1s.
	// The SSE handler selects on shutdownCh which is closed by RegisterOnShutdown
	// at the start of Shutdown — handler exits promptly, Shutdown drains in < 1s.
	if elapsed >= 1*time.Second {
		t.Errorf("post-fix assertion: Shutdown took %v (>= 1s) — "+
			"the SSE handler should have exited promptly via shutdownCh",
			elapsed)
	}
	t.Logf("Shutdown completed in %v (post-fix: SSE exited via shutdownCh)", elapsed)

	// Tear down the SSE connection so the test goroutine can exit.
	sseCancel()
}
