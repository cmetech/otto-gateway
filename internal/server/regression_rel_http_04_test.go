package server_test

// Regression test for REL-HTTP-04 (H-4): the HTTP server has no ReadTimeout and
// no per-request body-read deadline. A client that sends request headers but
// then stalls mid-body (Wi-Fi drop, sleep/wake) parks the handler goroutine
// indefinitely inside decodeJSONBody — until kernel TCP keepalive (~2h default)
// reaps the connection.
//
// Pre-fix observable: the handler does NOT return within a bounded deadline when
// the request body reader stalls. The test uses a 3s watchdog: if the handler
// has not returned within 3s, the pre-fix condition is confirmed. The stall is
// then released to let the test exit cleanly.
//
// Post-fix: a per-request read deadline via
// http.ResponseController.SetReadDeadline() (or equivalent) causes the handler
// to return within the configured deadline. Unskip in Phase 16 fix commit and
// flip the assertion.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// stalledReader blocks all Read calls until its gate channel is closed, then
// returns io.EOF. It simulates a client that sent the request header but stalls
// mid-body (e.g. Wi-Fi drop, machine sleep mid-upload from LangFlow).
type stalledReader struct {
	gate <-chan struct{} // close to unblock
}

func (s *stalledReader) Read(p []byte) (int, error) {
	<-s.gate // block until released
	return 0, io.EOF
}

// TestRegression_REL_HTTP_04_BodyReadDeadlineMissing demonstrates that the HTTP
// server has no per-request body-read deadline. A POST with a stalled body
// reader parks the handler goroutine indefinitely.
//
// The test drives srv.ServeHTTP directly (no real TCP; no server startup needed)
// via httptest.NewRecorder + httptest.NewRequestWithContext so the handler runs
// inline in a goroutine. A 3s watchdog confirms the handler has NOT returned
// (pre-fix observable). After the watchdog fires, the gate is closed to let the
// handler exit and prevent a goroutine leak.
func TestRegression_REL_HTTP_04_BodyReadDeadlineMissing(t *testing.T) {
	t.Skip("REL-HTTP-04 (H-4): regression test — unskip in Phase 16 fix commit")
	defer goleak.VerifyNone(t)

	srv := newTestServer(t)

	gate := make(chan struct{})
	body := &stalledReader{gate: gate}

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	req := httptest.NewRequestWithContext(reqCtx, http.MethodPost,
		"/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	// Content-Length unknown (no length header) — simulates a streaming upload
	// body that stalls before sending the JSON payload.
	rec := httptest.NewRecorder()

	// Run the handler in a goroutine so the test can observe its liveness.
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		srv.ServeHTTP(rec, req)
	}()

	// Give the handler a brief moment to reach the body-read inside decodeJSONBody.
	time.Sleep(100 * time.Millisecond)

	// Pre-fix observable: the handler has NOT returned after 3s because the body
	// read is unbounded. The watchdog confirms the stall.
	select {
	case <-handlerDone:
		// Handler returned without reading the full body — bug may already be fixed.
		t.Errorf("pre-fix reproducer: handler returned unexpectedly within 3s of body stall; " +
			"if a body-read deadline was added, this test should be unskipped in Phase 16")
	case <-time.After(3 * time.Second):
		// Handler is still parked on the stalled body read — pre-fix confirmed.
		t.Log("pre-fix observable confirmed: handler parked on stalled body read for 3s")
	}

	// Release the stall and cancel the request to allow the handler to exit
	// cleanly (prevents goroutine leak caught by goleak.VerifyNone).
	close(gate)
	reqCancel()
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Error("handler did not exit within 5s after stall released")
	}
}
