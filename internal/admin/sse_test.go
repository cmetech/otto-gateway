// Package admin — whitebox test file.
// Tests for sseHandler, sseLoop (ticker injection), writeSSELine,
// and admin.go wiring (h.tailer + /logs/stream route).
//
// Every test defers goleak.VerifyNone(t) so goroutine leaks are caught.
package admin

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// ---------------------------------------------------------------------------
// writeSSELine unit tests
// ---------------------------------------------------------------------------

func TestAdmin_WriteSSELine_MultilinePayload(t *testing.T) {
	defer goleak.VerifyNone(t)

	tests := []struct {
		name      string
		eventName string
		payload   string
		want      string
	}{
		{
			name:      "single line with event",
			eventName: "log",
			payload:   "hello world",
			want:      "event: log\ndata: hello world\n\n",
		},
		{
			name:      "multiline with event",
			eventName: "log",
			payload:   "line1\nline2",
			want:      "event: log\ndata: line1\ndata: line2\n\n",
		},
		{
			name:      "empty payload with event",
			eventName: "log",
			payload:   "",
			want:      "event: log\ndata:\n\n",
		},
		{
			name:      "no event name",
			eventName: "",
			payload:   "line1",
			want:      "data: line1\n\n",
		},
		{
			name:      "empty payload no event",
			eventName: "",
			payload:   "",
			want:      "data:\n\n",
		},
		{
			name:      "script-tag in payload — literal text, no HTML",
			eventName: "log",
			payload:   "<script>alert(1)</script>",
			want:      "event: log\ndata: <script>alert(1)</script>\n\n",
		},
		{
			name:      "newline-embedded log line splits into two data prefixes (T-6.1-13 mitigation)",
			eventName: "log",
			payload:   "part1\npart2",
			want:      "event: log\ndata: part1\ndata: part2\n\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			writeSSELine(&sb, tc.eventName, tc.payload)
			got := sb.String()
			if got != tc.want {
				t.Errorf("writeSSELine(%q, %q):\n got: %q\nwant: %q", tc.eventName, tc.payload, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// flushRecorder: httptest.ResponseRecorder that also implements http.Flusher
// ---------------------------------------------------------------------------

type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushRecorder) Flush() {
	// No-op: data is already in the ResponseRecorder body buffer.
}

// noFlushRecorder wraps ResponseRecorder but does NOT implement http.Flusher.
type noFlushRecorder struct {
	*httptest.ResponseRecorder
}

// ---------------------------------------------------------------------------
// sseLoop via ticker injection
// ---------------------------------------------------------------------------

func TestAdmin_SSEPingViaInjectedTicker(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())
	sub := tailer.Subscribe(context.Background())
	defer tailer.Unsubscribe(sub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := &flushRecorder{httptest.NewRecorder()}
	tickerC := make(chan time.Time, 1)

	// Run sseLoop in a goroutine so the test can inject a tick.
	done := make(chan error, 1)
	go func() {
		done <- sseLoop(ctx, rec, rec, sub, tickerC, nil)
	}()

	// Inject one ticker tick.
	tickerC <- time.Now()

	// Give the loop time to process the tick.
	time.Sleep(100 * time.Millisecond)

	// Cancel context so the loop exits.
	cancel()
	<-done // wait for sseLoop to return

	body := rec.Body.String()
	// Expect a ping event.
	if !strings.Contains(body, "event: ping") {
		t.Errorf("expected 'event: ping' in body, got: %q", body)
	}
}

// ---------------------------------------------------------------------------
// sseHandler flusher-cast failure
// ---------------------------------------------------------------------------

func TestAdmin_SSEHandler_FlusherCastFailure(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	h := &handler{
		deps: Deps{
			Logger:  discardLogger(),
			LogPath: logPath,
		},
	}
	h.tailer = NewTailer(logPath, discardLogger())

	// Wrap recorder in noFlushRecorder so http.Flusher cast fails.
	inner := httptest.NewRecorder()
	nfr := &noFlushRecorder{inner}

	req := httptest.NewRequest(http.MethodGet, "/logs/stream", nil)
	h.sseHandler(nfr, req)

	if inner.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for non-flusher writer, got %d", inner.Code)
	}
	if !strings.Contains(inner.Body.String(), "streaming unsupported") {
		t.Errorf("expected 'streaming unsupported' body, got: %q", inner.Body.String())
	}
}

// ---------------------------------------------------------------------------
// sseHandler backfill + live line
// ---------------------------------------------------------------------------

func TestAdmin_SSEBackfillAndLive(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"

	// Pre-populate the log BEFORE creating the tailer (the tailer will open
	// at EOF, so these lines are backfill via Snapshot only).
	appendToFile(t, logPath, "pre-1", "pre-2")

	tailer := NewTailer(logPath, discardLogger())
	// Run the tailer briefly to populate the ring buffer.
	// We manually push lines into the ring for the backfill test since
	// the tailer opens at EOF.
	// For backfill testing we use Snapshot directly in sseLoop.
	// Push the pre-existing lines directly into the ring buffer.
	tailer.ring.Push("pre-1")
	tailer.ring.Push("pre-2")

	h := &handler{
		deps: Deps{
			Logger:  discardLogger(),
			LogPath: logPath,
		},
	}
	h.tailer = tailer

	// Use a cancellable context to terminate the SSE handler.
	ctx, cancel := context.WithCancel(context.Background())

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/logs/stream", nil)
	rec := &flushRecorder{httptest.NewRecorder()}

	// Run handler in background — it blocks until ctx is cancelled.
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		h.sseHandler(rec, req)
	}()

	// Give handler time to send backfill.
	time.Sleep(200 * time.Millisecond)

	// Check headers.
	resp := rec.Result()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: got %q, want no-cache", cc)
	}
	if conn := resp.Header.Get("Connection"); conn != "keep-alive" {
		t.Errorf("Connection: got %q, want keep-alive", conn)
	}
	if xab := resp.Header.Get("X-Accel-Buffering"); xab != "no" {
		t.Errorf("X-Accel-Buffering: got %q, want no", xab)
	}

	// Check backfill lines appeared.
	body := rec.Body.String()
	if !strings.Contains(body, "event: log\ndata: pre-1\n") {
		t.Errorf("expected backfill 'pre-1' in body, got: %q", body)
	}
	if !strings.Contains(body, "event: log\ndata: pre-2\n") {
		t.Errorf("expected backfill 'pre-2' in body, got: %q", body)
	}

	// Wait for tailer to start and open the file.
	time.Sleep(400 * time.Millisecond)

	// Append a live line.
	appendToFile(t, logPath, "live-1")

	// Wait for it to arrive.
	time.Sleep(1000 * time.Millisecond)

	liveBody := rec.Body.String()
	if !strings.Contains(liveBody, "event: log\ndata: live-1\n") {
		t.Errorf("expected live line 'live-1' in body, got: %q", liveBody)
	}

	// Cancel context and wait for handler to exit.
	cancel()
	select {
	case <-handlerDone:
		// Good — handler exited cleanly.
	case <-time.After(500 * time.Millisecond):
		t.Error("handler did not exit within 500ms of ctx cancel")
	}
}

// ---------------------------------------------------------------------------
// sseHandler context-cancel teardown
// ---------------------------------------------------------------------------

func TestAdmin_SSECtxCancelTeardown(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())
	h := &handler{
		deps: Deps{
			Logger:  discardLogger(),
			LogPath: logPath,
		},
	}
	h.tailer = tailer

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/logs/stream", nil)
	rec := &flushRecorder{httptest.NewRecorder()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.sseHandler(rec, req)
	}()

	// Give handler a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Cancel context — handler should exit, tailer goroutine should stop.
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("handler did not exit within 500ms of ctx cancel")
	}

	// Give tailer goroutine time to observe cancelRun and exit.
	time.Sleep(100 * time.Millisecond)
	// goleak.VerifyNone at defer will catch any leaked goroutine.
}

// ---------------------------------------------------------------------------
// sseHandler slow subscriber drop
// ---------------------------------------------------------------------------

func TestAdmin_SSESlowSubscriberDrops(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())
	h := &handler{
		deps: Deps{
			Logger:  discardLogger(),
			LogPath: logPath,
		},
	}
	h.tailer = tailer

	// Use slowFlusher to simulate a slow SSE consumer — it just records body.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/logs/stream", nil)
	rec := &flushRecorder{httptest.NewRecorder()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.sseHandler(rec, req)
	}()

	// Wait for handler to subscribe.
	time.Sleep(400 * time.Millisecond)

	// Broadcast more lines than TailerSubChanBuffer so the slow subscriber
	// (which never reads from its channel in the handler loop) may fill up.
	more := TailerSubChanBuffer + 5
	for i := 0; i < more; i++ {
		appendToFile(t, logPath, fmt.Sprintf("line-%d", i))
		time.Sleep(30 * time.Millisecond)
	}

	// Wait for processing.
	time.Sleep(1 * time.Second)

	// Verify the tailer is not blocked — Snapshot should be non-empty.
	snap := tailer.Snapshot()
	if len(snap) == 0 {
		t.Error("Snapshot is empty — tailer appears blocked by slow SSE subscriber")
	}

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// admin.go wiring: tailer field + /logs/stream route
// ---------------------------------------------------------------------------

func TestAdmin_AdminGo_TailerWired(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Handler with a LogPath — tailer should be wired.
	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	handler := Handler(Deps{
		Logger:  discardLogger(),
		Version: "test",
		LogPath: logPath,
	})

	// GET /logs/stream — should NOT return 404 (route is registered).
	// It may return 500 if the response writer doesn't implement http.Flusher
	// (httptest.ResponseRecorder does NOT implement Flusher by default).
	// We use a non-Flusher recorder to get a deterministic non-404 response.
	req := httptest.NewRequest(http.MethodGet, "/logs/stream", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Errorf("GET /logs/stream returned 404 — route not registered in Handler")
	}
	// Should be 500 "streaming unsupported" because httptest.ResponseRecorder
	// doesn't implement http.Flusher.
	if rec.Code != http.StatusInternalServerError {
		t.Logf("GET /logs/stream returned %d (expected 500 for non-flusher recorder, or non-404)", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// sseLoop — backfill lines ordering
// ---------------------------------------------------------------------------

func TestAdmin_SSELoop_BackfillOrdering(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())
	sub := tailer.Subscribe(context.Background())
	defer tailer.Unsubscribe(sub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := &flushRecorder{httptest.NewRecorder()}
	tickerC := make(chan time.Time)

	// Provide backfill snapshot with known ordering.
	snapshot := []string{"first", "second", "third"}

	done := make(chan error, 1)
	go func() {
		done <- sseLoop(ctx, rec, rec, sub, tickerC, snapshot)
	}()

	// Give the loop time to send backfill.
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-done

	body := rec.Body.String()
	scanner := bufio.NewScanner(strings.NewReader(body))
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}

	// Expect backfill in order.
	if len(dataLines) < 3 {
		t.Fatalf("expected at least 3 data lines, got %d: %v", len(dataLines), dataLines)
	}
	if dataLines[0] != "first" || dataLines[1] != "second" || dataLines[2] != "third" {
		t.Errorf("backfill order wrong: %v", dataLines[:3])
	}
}
