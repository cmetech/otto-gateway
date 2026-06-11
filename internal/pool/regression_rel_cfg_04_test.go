// Phase 14 Plan 14-04 Task 5 — Regression test for REL-CFG-04 (O-1 Medium).
//
// Finding O-1: Pool exhaustion is completely silent at default log level.
// When all slots are busy and a new request parks waiting for one to become
// free, no log line is emitted at Warn level (or above) — operators see
// "the gateway silently stopped answering" with no diagnostic signal.
//
// Pre-fix observable: a size-1 pool with a blocked Prompt, plus a second
// concurrent acquire waiting, emits ZERO Warn records mentioning
// "pool: waiting for free slot" — the waiting goroutine is invisible.
//
// Post-fix (Phase 16): on first park at the slot-acquire select in
// pool.go:490-505, the pool emits Warn("pool: waiting for free slot",
// "busy", ..., "size", ...) so operators know why requests are stalling.
package pool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
)

// syncBuffer wraps bytes.Buffer with a sync.Mutex so the test goroutine
// reading via String() does not race the slog handler goroutine writing
// via Write(). Without this, `go test -race` flags a write/read race on
// bytes.Buffer's internal buf slice (the slog Warn fires from the parking
// goroutine; the assertion reads from the main test goroutine).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p) //nolint:wrapcheck // pure delegation
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Compile-time assertion: syncBuffer implements io.Writer.
var _ io.Writer = (*syncBuffer)(nil)

// captureSlogForPool returns a JSON-handler-backed *slog.Logger and its
// mutex-protected buffer. Used by regression tests that need to assert
// on the pool's own log output. The syncBuffer wrapper is required for
// `go test -race` cleanliness: slog handlers Write from whichever
// goroutine emitted the record, and the test asserts via String() from
// the main goroutine — without sync these collide on bytes.Buffer's
// internal slice.
func captureSlogForPool(_ *testing.T) (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// decodePoolRecords splits the buffer into one JSON record per line.
func decodePoolRecords(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode slog record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// TestRegression_REL_CFG_04_PoolExhaustionSilent drives a size-1 pool to
// exhaustion and asserts that NO Warn-level "pool: waiting for free slot"
// record is emitted (pre-fix state).
//
// Setup:
//   - size-1 pool with a fakeClient whose Prompt blocks until the test ends
//   - first goroutine acquires the slot and parks in Prompt (holds it)
//   - second goroutine tries to acquire the same slot → blocks on p.slots
//
// Pre-fix: second goroutine blocks silently — no Warn in the log buffer.
// Post-fix (Phase 16): pool emits Warn("pool: waiting for free slot", ...)
// when the immediate acquire fails.
func TestRegression_REL_CFG_04_PoolExhaustionSilent(t *testing.T) {
	// O-1 (REL-CFG-04) fix shipped in Phase 16-01:
	//   internal/pool/pool.go — p.warnOnce sync.Once + non-blocking
	//     try-then-park acquire pattern in NewSession; reset in Close.
	//     Emits Warn("pool: waiting for free slot", "busy", ..., "size", ...)
	//     AT MOST ONCE per saturation episode.
	logger, buf := captureSlogForPool(t)

	// Gate controls when the first Prompt returns. Close it at test end to
	// unblock the first goroutine so the pool releases the slot cleanly
	// (goleak / testmain_test.go require all goroutines to exit).
	promptGate := make(chan struct{})

	// fakeClient whose Prompt blocks until promptGate is closed.
	blockingClient := &fakeClient{
		promptFn: func(ctx context.Context, _ string, _ []canonical.Block) (*acp.Stream, error) {
			select {
			case <-promptGate:
			case <-ctx.Done():
			}
			s := acp.NewStreamForTest("blocked")
			s.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
			return s, nil
		},
	}
	ff := &fakeClientFactory{clients: []pool.PoolClient{blockingClient}}

	p := pool.New(pool.Config{
		Logger:  logger,
		Size:    1,
		Factory: ff,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		close(promptGate)
		t.Fatalf("Warmup: %v", err)
	}
	defer func() {
		_ = p.Close()
	}()

	// Goroutine 1: acquire the sole slot and hold it in Prompt.
	g1Ready := make(chan struct{})
	g1Done := make(chan struct{})
	go func() {
		defer close(g1Done)
		sid, err := p.NewSession(ctx, "")
		if err != nil {
			return
		}
		close(g1Ready)
		// Block in Prompt until promptGate is closed.
		stream, err := p.Prompt(ctx, sid, []canonical.Block{{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "ping"}}})
		if err != nil {
			return
		}
		// Drain stream to completion.
		drainChunks(stream.Chunks())
		_, _ = stream.Result()
	}()

	// Wait for goroutine 1 to acquire the slot.
	select {
	case <-g1Ready:
	case <-time.After(2 * time.Second):
		close(promptGate)
		t.Fatal("goroutine 1 did not acquire the slot in time")
	}

	// Goroutine 2: try to acquire the same slot — this will park because the
	// pool is exhausted. Give it 100ms to park then assert the log.
	g2Ctx, g2Cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer g2Cancel()
	go func() {
		// Expected to time out (g2Ctx) since the slot is held.
		_, _ = p.NewSession(g2Ctx, "")
	}()

	// Wait for g2 to park and time out.
	time.Sleep(150 * time.Millisecond)

	// Assert: no Warn record mentioning "pool: waiting for free slot" was
	// emitted (pre-fix state). Post-fix: at least one such record exists.
	recs := decodePoolRecords(t, buf)
	for _, r := range recs {
		level, _ := r["level"].(string)
		msg, _ := r["msg"].(string)
		if strings.EqualFold(level, "warn") &&
			strings.Contains(msg, "pool: waiting for free slot") {
			// Post-fix state.
			t.Logf("post-fix: found Warn record: %+v", r)
			close(promptGate)
			<-g1Done
			return
		}
	}

	// Pre-fix state confirmed: no Warn emitted.
	t.Log("pre-fix confirmed: pool exhaustion is silent (no Warn record)")
	t.Error("expected Warn 'pool: waiting for free slot', got none (pre-fix state)")

	// Clean up: unblock g1.
	close(promptGate)
	<-g1Done
}
