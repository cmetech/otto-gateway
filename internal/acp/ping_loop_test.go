// Whitebox unit tests for the ping suspend-guard in pingLoop / pingTick.
// D-18: package acp (not acp_test) for access to unexported pingTick + fields.
package acp

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// syncBuffer is a goroutine-safe log sink. The client's readLoop/writerLoop can
// emit teardown warnings concurrently with the test goroutine's assertions, so
// the buffer that captures ping-loop logs must be mutex-guarded.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p) //nolint:wrapcheck
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// fakeClock is a mutex-guarded injectable wall clock for the Config.Now seam.
// The idle pingLoop goroutine reads it concurrently, hence the lock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// newPingTestClient builds a client whose real pingLoop stays idle (10-minute
// interval) so the test can drive pingTick directly with a controlled clock and
// a capturing logger.
func newPingTestClient(t *testing.T, clock func() time.Time, logs *syncBuffer) (*mockRWC, *Client) {
	t.Helper()
	mock := newMockRWC()
	cfg := Config{
		Logger:       slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Command:      "kiro-cli",
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute, // keep the real ping loop idle; we drive pingTick
		Now:          clock,
	}
	return mock, NewWithConn(mock, cfg)
}

// expiredCtx returns a context whose deadline is already in the past, so any
// Ping run against it returns context.DeadlineExceeded near-instantly instead
// of blocking on the real 10s ping timeout.
func expiredCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	t.Cleanup(cancel)
	return ctx
}

// TestPingTick_NormalGapFailingPingEscalates preserves the existing crash-and-
// replace behavior: on a normal-gap tick a failing ping tears the worker down
// (Done() fires) and logs acp.ping.escalated_to_close.
func TestPingTick_NormalGapFailingPingEscalates(t *testing.T) {
	logs := &syncBuffer{}
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	mock, c := newPingTestClient(t, clk.now, logs)

	lastTick := c.cfg.Now() // normal gap: no clock jump before the tick

	_, stop := c.pingTick(expiredCtx(t), lastTick)

	if !stop {
		t.Fatal("pingTick must signal stop after a failing ping on a normal-gap tick")
	}
	select {
	case <-c.Done():
		// expected — escalation called c.cancel()
	case <-time.After(time.Second):
		t.Fatal("Done() did not fire after a failing ping on a normal-gap tick")
	}
	if !strings.Contains(logs.String(), "acp.ping.escalated_to_close") {
		t.Errorf("expected acp.ping.escalated_to_close log; got:\n%s", logs.String())
	}

	mock.serverClose()
	_ = c.Close()
	goleak.VerifyNone(t)
}

// TestPingTick_SuspendGapSkipsLivenessCheck is the core fix: when the wall clock
// jumped by more than 2x the ping interval (machine suspended), the tick skips
// the liveness check entirely — the healthy worker survives (Done() does NOT
// fire) and acp.ping.skipped_after_resume is logged. The already-expired ctx
// proves the ping is never consulted.
func TestPingTick_SuspendGapSkipsLivenessCheck(t *testing.T) {
	logs := &syncBuffer{}
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	mock, c := newPingTestClient(t, clk.now, logs)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	lastTick := c.cfg.Now()
	clk.advance(3 * c.cfg.PingInterval) // wall-clock jump past 2x interval → suspend

	_, stop := c.pingTick(expiredCtx(t), lastTick)

	if stop {
		t.Fatal("pingTick must not stop the loop on a suspend-skip cycle")
	}
	select {
	case <-c.Done():
		t.Fatal("Done() fired on a suspend-skip cycle — a healthy worker was killed")
	default:
		// expected — worker untouched
	}
	if !strings.Contains(logs.String(), "acp.ping.skipped_after_resume") {
		t.Errorf("expected acp.ping.skipped_after_resume log; got:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "acp.ping.escalated_to_close") {
		t.Errorf("must not escalate on a suspend-skip cycle; got:\n%s", logs.String())
	}
}

// TestPingTick_SuspendGuardIsOneShot confirms the guard is one-shot, not sticky:
// after a single skipped (suspend) cycle, the very next normal-gap tick with a
// failing ping still escalates. A genuinely-hung worker is caught within one
// interval.
func TestPingTick_SuspendGuardIsOneShot(t *testing.T) {
	logs := &syncBuffer{}
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	mock, c := newPingTestClient(t, clk.now, logs)

	lastTick := c.cfg.Now()
	clk.advance(3 * c.cfg.PingInterval)

	// First tick: suspend gap → skipped, loop continues, worker survives.
	newLastTick, stop := c.pingTick(context.Background(), lastTick)
	if stop {
		t.Fatal("first (suspend) tick must not stop the loop")
	}
	select {
	case <-c.Done():
		t.Fatal("Done() fired on the suspend-skip cycle")
	default:
	}

	// Second tick: normal gap (no further clock jump), failing ping → escalates.
	_, stop = c.pingTick(expiredCtx(t), newLastTick)
	if !stop {
		t.Fatal("second (normal) tick with a failing ping must escalate")
	}
	select {
	case <-c.Done():
		// expected — guard was one-shot, not sticky
	case <-time.After(time.Second):
		t.Fatal("guard was sticky: Done() did not fire on the normal tick after a suspend cycle")
	}
	if !strings.Contains(logs.String(), "acp.ping.escalated_to_close") {
		t.Errorf("expected acp.ping.escalated_to_close after the one-shot guard; got:\n%s", logs.String())
	}

	mock.serverClose()
	_ = c.Close()
	goleak.VerifyNone(t)
}
