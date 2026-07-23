package pool_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

type acquireObservation struct {
	duration time.Duration
	result   string
}

type acquireRecorder struct {
	mu           sync.Mutex
	observations []acquireObservation
}

func (*acquireRecorder) RecordTurnMeter(float64, int64, float64, bool) {}
func (*acquireRecorder) RecordMCPInit(string, bool)                    {}

func (r *acquireRecorder) RecordPoolAcquire(duration time.Duration, result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations = append(r.observations, acquireObservation{duration: duration, result: result})
}

func (r *acquireRecorder) results() []acquireObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]acquireObservation, len(r.observations))
	copy(out, r.observations)
	return out
}

func newAcquireTestPool(t *testing.T, recorder *acquireRecorder, timeout time.Duration) *pool.Pool {
	t.Helper()
	var sessions atomic.Int64
	client := &fakeClient{
		models: []canonical.ModelInfo{{ID: "auto"}},
		newSessionFn: func(context.Context, string) (string, error) {
			return fmt.Sprintf("session-%d", sessions.Add(1)), nil
		},
	}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		AcquireTimeout: timeout,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{client}},
		Metrics:        recorder,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	return p
}

func assertSingleAcquireResult(t *testing.T, recorder *acquireRecorder, want string) {
	t.Helper()
	got := recorder.results()
	if len(got) != 1 {
		t.Fatalf("acquire observations = %+v, want exactly one", got)
	}
	if got[0].result != want {
		t.Errorf("acquire result = %q, want %q", got[0].result, want)
	}
	if got[0].duration < 0 {
		t.Errorf("acquire duration = %v, want non-negative", got[0].duration)
	}
}

func TestNewSession_RecordsAcquireImmediate(t *testing.T) {
	recorder := &acquireRecorder{}
	p := newAcquireTestPool(t, recorder, time.Second)
	defer func() { _ = p.Close() }()

	if _, err := p.NewSession(context.Background(), ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	assertSingleAcquireResult(t, recorder, "immediate")
}

func TestNewSession_RecordsAcquireWaited(t *testing.T) {
	recorder := &acquireRecorder{}
	p := newAcquireTestPool(t, recorder, time.Second)
	defer func() { _ = p.Close() }()

	slot, ok := p.WaitForSlotRelease(100 * time.Millisecond)
	if !ok {
		t.Fatal("warm slot unavailable")
	}
	result := make(chan error, 1)
	go func() {
		_, err := p.NewSession(context.Background(), "")
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	p.PutSlotBack(slot)
	if err := <-result; err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	assertSingleAcquireResult(t, recorder, "waited")
}

func TestNewSession_RecordsAcquireTimeout(t *testing.T) {
	recorder := &acquireRecorder{}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		AcquireTimeout: 5 * time.Millisecond,
		Metrics:        recorder,
	})
	defer func() { _ = p.Close() }()

	_, err := p.NewSession(context.Background(), "")
	if !errors.Is(err, pool.ErrPoolExhausted) {
		t.Fatalf("NewSession error = %v, want ErrPoolExhausted", err)
	}
	assertSingleAcquireResult(t, recorder, "timeout")
}

func TestNewSession_RecordsAcquireCancelled(t *testing.T) {
	recorder := &acquireRecorder{}
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Metrics: recorder})
	defer func() { _ = p.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.NewSession(ctx, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("NewSession error = %v, want context.Canceled", err)
	}
	assertSingleAcquireResult(t, recorder, "cancelled")
}

func TestNewSession_RecordsAcquireClosed(t *testing.T) {
	recorder := &acquireRecorder{}
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Metrics: recorder})
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := p.NewSession(context.Background(), "")
	if err == nil || err.Error() != "pool: closed" {
		t.Fatalf("NewSession error = %v, want pool: closed", err)
	}
	assertSingleAcquireResult(t, recorder, "closed")
}

var _ pool.MetricsRecorder = (*acquireRecorder)(nil)
