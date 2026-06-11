package server_test

// Regression test for REL-HTTP-04 (H-4): the HTTP server has no ReadTimeout and
// no per-request body-read deadline. A client that sends request headers but
// then stalls mid-body (Wi-Fi drop, sleep/wake) parks the handler goroutine
// indefinitely inside decodeJSONBody — until kernel TCP keepalive (~2h default)
// reaps the connection.
//
// Post-fix (Plan 16-02): a per-request body-read deadline is applied via
// time.AfterFunc + r.Body.Close() to all chat-body POST handlers. When the
// deadline fires, r.Body.Close() unblocks the handler's io.ReadAll / json.Decode
// call. The deadline applies ONLY to the body-read phase — long SSE response
// writes are unaffected because the timer is stopped after the body has been
// fully read (or its lifetime is scoped to the wrapper).
//
// This file contains two regression tests:
//   - TestRegression_REL_HTTP_04_BodyReadDeadline — the headline H-4 fix:
//     a stalled chat-body POST returns within the configured deadline.
//   - TestRegression_REL_HTTP_04_SSEWriteUnaffected — must_haves bullet 2:
//     after the body is read, the handler can write SSE-style chunks for
//     much longer than the body-read deadline without being interrupted.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/goleak"

	"otto-gateway/internal/server"
	"otto-gateway/internal/testutil"
)

// stalledReader blocks all Read calls until its gate channel is closed or
// the body is closed by the server-side deadline wrapper (which causes
// the next Read to return io.ErrClosedPipe-style error).
type stalledReader struct {
	gate   <-chan struct{} // close to unblock
	closed chan struct{}   // closed by Close() — used to wake parked Reads
}

func newStalledReader(gate <-chan struct{}) *stalledReader {
	return &stalledReader{gate: gate, closed: make(chan struct{})}
}

func (s *stalledReader) Read(p []byte) (int, error) {
	select {
	case <-s.gate:
		return 0, io.EOF
	case <-s.closed:
		// Body was closed mid-read by the deadline wrapper.
		return 0, io.ErrUnexpectedEOF
	}
}

func (s *stalledReader) Close() error {
	select {
	case <-s.closed:
		// already closed
	default:
		close(s.closed)
	}
	return nil
}

// readingChatRegistrar registers a stub /chat/completions POST handler that
// attempts to read its entire request body via io.ReadAll, then writes a
// 200 response. With the H-4 fix in place, r.Body.Close() fired by the
// deadline wrapper unblocks the io.ReadAll call.
func readingChatRegistrar(observed chan<- error) server.RouteRegistrar {
	return stubRouteRegistrar{fn: func(r chi.Router) {
		r.Post("/chat/completions", func(w http.ResponseWriter, req *http.Request) {
			_, err := io.ReadAll(req.Body)
			select {
			case observed <- err:
			default:
			}
			if err != nil {
				// Body-read failed (deadline fired or client cancelled).
				w.WriteHeader(http.StatusRequestTimeout)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	}}
}

// sseWriteRegistrar registers a stub /chat/completions POST handler that
// reads its body fully, then writes 10 SSE-style chunks across 1 second.
// Proves the body-read deadline does NOT bound response writes.
func sseWriteRegistrar() server.RouteRegistrar {
	return stubRouteRegistrar{fn: func(r chi.Router) {
		r.Post("/chat/completions", func(w http.ResponseWriter, req *http.Request) {
			// Drain the body (fast — small body).
			_, _ = io.Copy(io.Discard, req.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			// Total write phase = 1 second, far longer than BodyReadTimeout=200ms below.
			for i := 0; i < 10; i++ {
				_, _ = w.Write([]byte("data: chunk\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
	}}
}

// TestRegression_REL_HTTP_04_BodyReadDeadline asserts that a POST handler
// reading the request body returns within the configured BodyReadTimeout
// when the client stalls mid-body. Pre-fix the handler would park
// indefinitely; post-fix r.Body.Close() fired by time.AfterFunc wakes
// the io.ReadAll call within the deadline.
func TestRegression_REL_HTTP_04_BodyReadDeadline(t *testing.T) {
	defer goleak.VerifyNone(t)

	deadline := 200 * time.Millisecond
	observed := make(chan error, 1)
	srv := server.NewFromConfig(server.Config{
		Logger:          testutil.Logger(t),
		BodyReadTimeout: deadline,
		Surfaces: []server.SurfaceMount{
			{Prefix: "/v1", Router: readingChatRegistrar(observed)},
		},
	})

	gate := make(chan struct{})
	defer close(gate) // safety: release if handler never gets there
	body := newStalledReader(gate)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	req := httptest.NewRequestWithContext(reqCtx, http.MethodPost,
		"/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(handlerDone)
		srv.ServeHTTP(rec, req)
	}()

	// Post-fix: handler must return within ~deadline + small slack.
	// Choose 2s as a generous ceiling for CI noise; deadline itself is 200ms.
	select {
	case <-handlerDone:
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Errorf("handler took %v to return; want < 2s after %v BodyReadTimeout", elapsed, deadline)
		}
		// Confirm the body-read path actually fired (not a 404 short-circuit).
		select {
		case err := <-observed:
			if err == nil {
				t.Errorf("handler observed nil body-read error; want non-nil (deadline should have closed body)")
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("handler returned but never observed a body-read attempt")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("post-fix observable failed: handler still parked 3s after start (BodyReadTimeout=%v not enforced)", deadline)
	}
}

// TestRegression_REL_HTTP_04_SSEWriteUnaffected asserts must_haves bullet 2:
// after the body is read, long SSE response writes are unaffected by the
// BodyReadTimeout. The handler writes 10 SSE chunks across 1 second; the
// body-read deadline is 200ms. The deadline is body-phase-only.
func TestRegression_REL_HTTP_04_SSEWriteUnaffected(t *testing.T) {
	defer goleak.VerifyNone(t)

	deadline := 200 * time.Millisecond
	srv := server.NewFromConfig(server.Config{
		Logger:          testutil.Logger(t),
		BodyReadTimeout: deadline,
		Surfaces: []server.SurfaceMount{
			{Prefix: "/v1", Router: sseWriteRegistrar()},
		},
	})

	// Small in-memory body — readable immediately, fully consumed before
	// the timer fires.
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	start := time.Now()
	srv.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("response status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Total SSE-write phase is 10 * 100ms = 1s. If the deadline incorrectly
	// fired during writes, the response would be truncated and elapsed << 1s.
	if elapsed < 900*time.Millisecond {
		t.Errorf("SSE write phase took %v; want >= 900ms (deadline %v must NOT bound response writes)", elapsed, deadline)
	}
	// And the body must contain all 10 chunks.
	got := rec.Body.String()
	if strings.Count(got, "data: chunk") != 10 {
		t.Errorf("SSE chunks count: got %d, want 10; body=%q", strings.Count(got, "data: chunk"), got)
	}
}
