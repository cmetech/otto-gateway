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
	t.Skip("REL-HTTP-01 (H-1): regression test — unskip in Phase 15 fix commit")
	defer goleak.VerifyNone(t)

	connHeld := make(chan struct{})    // closed once the SSE handler is streaming
	releaseConn := make(chan struct{}) // test closes this to let the handler exit
	defer close(releaseConn)

	// Minimal SSE handler simulating the admin /admin/logs/stream endpoint.
	// It streams "event: ping\ndata: \n\n" once, then blocks until the request
	// context is cancelled (post-fix) or releaseConn is closed (test cleanup).
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
		// Pre-fix: r.Context() is never cancelled by Shutdown — this blocks
		// forever until releaseConn or Shutdown deadline fires.
		select {
		case <-r.Context().Done():
			// Post-fix: server wires shutdown cancellation into request contexts.
		case <-releaseConn:
			// Test cleanup.
		}
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

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

	// Shutdown with a 6s deadline. Pre-fix: blocks until the full deadline
	// because no shutdown signal is wired into the SSE handler's context.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer shutdownCancel()

	start := time.Now()
	shutdownErr := srv.Shutdown(shutdownCtx)
	elapsed := time.Since(start)

	// Pre-fix observable: Shutdown returns with context.DeadlineExceeded after
	// the full 6s because the SSE handler is still open.
	// Post-fix assertion (unskip in Phase 15): elapsed < 1s (handler exits
	// immediately when shutdown cancels its context).
	//
	// Assert the pre-fix condition: Shutdown should have blocked > 2s because
	// the SSE connection is still open.
	const minBlockDuration = 2 * time.Second
	if elapsed < minBlockDuration {
		t.Errorf("pre-fix reproducer: Shutdown returned in %v (< %v) — "+
			"the SSE connection should have held it open; "+
			"if this assertion fails, the bug may already be fixed",
			elapsed, minBlockDuration)
	}
	t.Logf("Shutdown blocked for %v (pre-fix observable: SSE connection held Shutdown open)", elapsed)
	if shutdownErr != nil {
		t.Logf("Shutdown error (expected pre-fix DeadlineExceeded): %v", shutdownErr)
	}

	// Tear down the SSE connection so the test goroutine can exit.
	sseCancel()
}
