package pool_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

type recycleMetricRecord struct {
	reason   string
	rssBytes uint64
	idle     time.Duration
}

type fakeRecycleMetrics struct {
	mu      sync.Mutex
	records []recycleMetricRecord
}

func (m *fakeRecycleMetrics) RecordWorkerRecycle(reason string, rssBytes uint64, idle time.Duration) {
	m.mu.Lock()
	m.records = append(m.records, recycleMetricRecord{reason: reason, rssBytes: rssBytes, idle: idle})
	m.mu.Unlock()
}

func (m *fakeRecycleMetrics) snapshot() []recycleMetricRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recycleMetricRecord(nil), m.records...)
}

func assertSlotActivity(t *testing.T, p *pool.Pool, requests uint64, releasedAt *time.Time) {
	t.Helper()
	row := p.Detail()[0]
	if row.UserRequestsSinceSpawn != requests {
		t.Fatalf("user requests = %d, want %d", row.UserRequestsSinceSpawn, requests)
	}
	if diff := cmp.Diff(releasedAt, row.LastUserReleaseAt); diff != "" {
		t.Fatalf("last release mismatch (-want +got):\n%s", diff)
	}
}

func TestPool_IdleMemoryRecycle_UsedIdleAboveThreshold(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2101}
	newClient := &fakeClient{pid: 2102}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       15 * time.Minute,
		IdleRecycleMemoryBytes: 500 << 20,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(pid int) (uint64, bool) {
			if pid != 2101 {
				t.Fatalf("memory reader pid = %d, want 2101", pid)
			}
			return 800 << 20, true
		},
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(15 * time.Minute)
	p.SweepIdleWorkersForTesting()
	p.WaitForRecyclesForTesting()

	if p.Recycles() != 1 || oldClient.closeCallCount() == 0 {
		t.Fatalf("recycles=%d closes=%d, want 1 and >=1", p.Recycles(), oldClient.closeCallCount())
	}
	row := p.Detail()[0]
	if row.Pid != 2102 || row.UserRequestsSinceSpawn != 0 || row.LastUserReleaseAt != nil {
		t.Fatalf("replacement state = %+v", row)
	}
}

func TestPool_IdleMemoryRecycle_UnusedWorkerNotSampled(t *testing.T) {
	var reads atomic.Int32
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2201}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		IdleRecycleAfter:       15 * time.Minute,
		IdleRecycleMemoryBytes: 500 << 20,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			reads.Add(1)
			return 800 << 20, true
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.SweepIdleWorkersForTesting()
	if got := reads.Load(); got != 0 {
		t.Fatalf("memory reads = %d, want 0 for unused worker", got)
	}
	if slot, ok := p.TakeSlotIfAvailable(); !ok {
		t.Fatal("unused worker was not returned to the free queue")
	} else {
		p.PutSlotBack(slot)
	}
}

func TestPool_IdleMemoryRecycle_CatalogProbeNotUserUse(t *testing.T) {
	var reads atomic.Int32
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2301}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		IdleRecycleAfter:       time.Nanosecond,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			reads.Add(1)
			return 2, true
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if turns, ok := p.SlotTurns("slot-0"); !ok || turns != 1 {
		t.Fatalf("warmup turns = (%d,%v), want (1,true)", turns, ok)
	}
	p.SweepIdleWorkersForTesting()
	if got := reads.Load(); got != 0 {
		t.Fatalf("memory reads = %d, want 0 for catalog-only worker", got)
	}
}

func TestPool_IdleMemoryRecycle_BelowIdleNotSampled(t *testing.T) {
	now := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
	var reads atomic.Int32
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2401}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       15 * time.Minute,
		IdleRecycleMemoryBytes: 500 << 20,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			reads.Add(1)
			return 800 << 20, true
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(15*time.Minute - time.Nanosecond)
	p.SweepIdleWorkersForTesting()
	if got := reads.Load(); got != 0 {
		t.Fatalf("memory reads = %d, want 0 below idle boundary", got)
	}
}

func TestPool_IdleMemoryRecycle_ExactlyThresholdDoesNotRecycle(t *testing.T) {
	now := time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC)
	const threshold = uint64(500 << 20)
	var reads atomic.Int32
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2501}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       15 * time.Minute,
		IdleRecycleMemoryBytes: threshold,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			reads.Add(1)
			return threshold, true
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(15 * time.Minute)
	p.SweepIdleWorkersForTesting()
	p.WaitForRecyclesForTesting()
	if reads.Load() != 1 || p.Recycles() != 0 {
		t.Fatalf("reads=%d recycles=%d, want 1 and 0", reads.Load(), p.Recycles())
	}
}

func TestPool_IdleMemoryRecycle_OneByteOverRecycles(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	const threshold = uint64(500 << 20)
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2601}
	newClient := &fakeClient{pid: 2602}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       15 * time.Minute,
		IdleRecycleMemoryBytes: threshold,
		WorkerMemorySupported:  true,
		ReadWorkerMemory:       func(int) (uint64, bool) { return threshold + 1, true },
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(15 * time.Minute)
	p.SweepIdleWorkersForTesting()
	p.WaitForRecyclesForTesting()
	if got := p.Recycles(); got != 1 {
		t.Fatalf("recycles = %d, want 1", got)
	}
}

func TestPool_IdleMemoryRecycle_UnavailableSampleRequeues(t *testing.T) {
	now := time.Date(2026, 8, 4, 16, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2701}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory:       func(int) (uint64, bool) { return 0, false },
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(time.Minute)
	p.SweepIdleWorkersForTesting()
	slot, ok := p.TakeSlotIfAvailable()
	if !ok {
		t.Fatal("unreadable worker was not requeued")
	}
	if extra, duplicate := p.TakeSlotIfAvailable(); duplicate {
		p.PutSlotBack(extra)
		t.Fatal("unreadable worker was requeued more than once")
	}
	p.PutSlotBack(slot)
}

func TestPool_IdleMemoryRecycle_InvalidPIDNotSampled(t *testing.T) {
	now := time.Date(2026, 8, 4, 17, 0, 0, 0, time.UTC)
	var reads atomic.Int32
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 0}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			reads.Add(1)
			return 2, true
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(time.Minute)
	p.SweepIdleWorkersForTesting()
	if got := reads.Load(); got != 0 {
		t.Fatalf("memory reads = %d, want 0 for invalid pid", got)
	}
}

func TestPool_IdleMemoryRecycle_BusyWorkerNotClaimed(t *testing.T) {
	now := time.Date(2026, 8, 4, 18, 0, 0, 0, time.UTC)
	var reads atomic.Int32
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2801}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			reads.Add(1)
			return 2, true
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	p.SweepIdleWorkersForTesting()
	if got := reads.Load(); got != 0 {
		t.Fatalf("memory reads = %d, want 0 for busy worker", got)
	}
	stream, err := p.Prompt(context.Background(), sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	drainChunks(stream.Chunks())
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
}

func TestPool_IdleMemoryRecycle_DeferredBehindTurnRecycle(t *testing.T) {
	now := time.Date(2026, 8, 4, 19, 0, 0, 0, time.UTC)
	metrics := &fakeRecycleMetrics{}
	var memoryReads atomic.Int32
	w0 := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2901}
	w1 := &fakeClient{pid: 2902}
	r0 := &fakeClient{pid: 2903}
	r1 := &fakeClient{pid: 2904}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   2,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{w0, w1, r0, r1}},
		Now:                    func() time.Time { return now },
		MaxWorkerTurns:         3,
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			memoryReads.Add(1)
			return 2, true
		},
		RecycleMetrics: metrics,
	})
	gate := make(chan struct{})
	launchEntered := make(chan struct{})
	var first sync.Once
	p.SetRecycleLaunchHookForTesting(func() {
		first.Do(func() {
			close(launchEntered)
			<-gate
		})
	})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
		_ = p.Close()
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Slot 0 starts at one catalog-probe turn. Give slot 0 and slot 1 one
	// request each so FIFO returns slot 0 to the front at turns=2 and leaves
	// slot 1 used-but-below-budget at turns=1.
	runOneRequest(t, p)
	runOneRequest(t, p)

	firstRequestDone := make(chan struct{})
	go func() {
		runOneRequest(t, p)
		close(firstRequestDone)
	}()
	<-launchEntered

	// The only free worker is slot 1. It is idle-memory eligible but still
	// below the turn budget, so this sweep genuinely requests idle_memory;
	// the in-flight max-turn recycle must defer it through the shared gate.
	now = now.Add(time.Minute)
	p.SweepIdleWorkersForTesting()
	if got := memoryReads.Load(); got != 1 {
		t.Fatalf("memory reads while max-turn recycle parked = %d, want 1", got)
	}
	if got := p.Recycles(); got != 0 {
		t.Fatalf("recycles while max-turn replacement parked = %d, want 0", got)
	}
	if records := metrics.snapshot(); len(records) != 0 {
		t.Fatalf("metrics while max-turn replacement parked = %+v, want none", records)
	}

	// Push slot 1 over its turn budget while slot 0 is still parked. Both
	// releases defer. Once slot 0 completes, the next sweep sees an
	// idle-memory candidate that is also over budget; max_turns must win.
	runOneRequest(t, p)
	runOneRequest(t, p)
	close(gate)
	<-firstRequestDone
	p.WaitForRecyclesForTesting()

	p.SweepIdleWorkersForTesting()
	p.WaitForRecyclesForTesting()
	records := metrics.snapshot()
	if len(records) != 2 || records[0].reason != "max_turns" || records[1].reason != "max_turns" {
		t.Fatalf("recycle records = %+v, want two max_turns records", records)
	}
}

func TestPool_IdleMemoryRecycle_LaterRequestResetsEligibility(t *testing.T) {
	now := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	var reads atomic.Int32
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 3001}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       15 * time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			reads.Add(1)
			return 2, true
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(14 * time.Minute)
	runOneRequest(t, p)
	now = now.Add(time.Minute)
	p.SweepIdleWorkersForTesting()
	if got := reads.Load(); got != 0 {
		t.Fatalf("memory reads = %d, want 0 one minute after later release", got)
	}
}

func TestPool_IdleMemoryRecycle_UnsupportedDoesNotStartLoop(t *testing.T) {
	now := time.Date(2026, 8, 4, 20, 30, 0, 0, time.UTC)
	logBuf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))
	ticks := make(chan time.Time, 3)
	ticks <- time.Time{}
	ticks <- time.Time{}
	ticks <- time.Time{}
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 3101}
	p := pool.New(pool.Config{
		Logger:                 logger,
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  false,
		ReadWorkerMemory: func(int) (uint64, bool) {
			t.Fatal("memory reader called on unsupported platform")
			return 0, false
		},
	})
	p.SetIdleSweepTicksForTesting(ticks)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(time.Minute)
	p.SweepIdleWorkersForTesting()
	slot, ok := p.TakeSlotIfAvailable()
	if !ok {
		t.Fatal("unsupported worker was not requeued")
	}
	if extra, duplicate := p.TakeSlotIfAvailable(); duplicate {
		p.PutSlotBack(extra)
		t.Fatal("unsupported worker was requeued more than once")
	}
	p.PutSlotBack(slot)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(logBuf.String(), "pool: idle-memory recycling unavailable on this platform"); got != 1 {
		t.Fatalf("unavailable warning count = %d, want 1", got)
	}
	if got := len(ticks); got != 3 {
		t.Fatalf("manual ticks remaining = %d, want 3 (loop must not start)", got)
	}
}

func TestPool_IdleMemoryRecycle_CloseJoinsLoop(t *testing.T) {
	now := time.Date(2026, 8, 4, 21, 0, 0, 0, time.UTC)
	ticks := make(chan time.Time, 1)
	readerEntered := make(chan struct{})
	readerGate := make(chan struct{})
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 3201}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			close(readerEntered)
			<-readerGate
			return 0, false
		},
	})
	p.SetIdleSweepTicksForTesting(ticks)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(time.Minute)
	ticks <- now
	<-readerEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()
	<-p.ClosingChan()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before idle sweep exited: %v", err)
	default:
	}
	if got := client.closeCallCount(); got != 0 {
		t.Fatalf("client close calls before sweep exit = %d, want 0", got)
	}
	close(readerGate)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestPool_IdleMemoryRecycle_CloseWinsFinalWarmupStart(t *testing.T) {
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 3251}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory:       func(int) (uint64, bool) { return 0, false },
	})
	boundaryEntered := make(chan struct{})
	boundaryRelease := make(chan struct{})
	var admitted atomic.Int32
	p.SetIdleSweepLifecycleHookForTesting(func(stage string) {
		switch stage {
		case "before_admission":
			close(boundaryEntered)
			<-boundaryRelease
		case "admitted":
			admitted.Add(1)
		default:
			t.Errorf("unexpected idle sweep lifecycle stage %q", stage)
		}
	})

	warmupDone := make(chan error, 1)
	go func() { warmupDone <- p.Warmup(context.Background()) }()
	<-boundaryEntered
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	close(boundaryRelease)
	if err := <-warmupDone; err != nil {
		t.Fatal(err)
	}
	// The broken ordering admits here, after Close already observed an empty
	// WaitGroup and returned. Join it explicitly so the RED test stays clean.
	p.WaitForIdleSweepForTesting()
	if got := admitted.Load(); got != 0 {
		t.Fatalf("idle sweep admissions after Close = %d, want 0", got)
	}
}

func TestPool_IdleMemoryRecycle_CloseDuringSweepPreservesOwnership(t *testing.T) {
	now := time.Date(2026, 8, 4, 22, 0, 0, 0, time.UTC)
	ticks := make(chan time.Time, 1)
	readerEntered := make(chan struct{})
	readerGate := make(chan struct{})
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 3301}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(int) (uint64, bool) {
			close(readerEntered)
			<-readerGate
			return 0, false
		},
	})
	p.SetIdleSweepTicksForTesting(ticks)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(time.Minute)
	ticks <- now
	<-readerEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()
	<-p.ClosingChan()
	close(readerGate)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	slot, ok := p.TakeSlotIfAvailable()
	if !ok {
		t.Fatal("unreadable slot was lost during close/sweep race")
	}
	if extra, duplicate := p.TakeSlotIfAvailable(); duplicate {
		p.PutSlotBack(extra)
		t.Fatal("slot was returned more than once during close/sweep race")
	}
	_ = slot
}

func TestPool_IdleMemoryRecycle_ConcurrentAcquireReleaseSweep(t *testing.T) {
	base := time.Date(2026, 8, 4, 23, 0, 0, 0, time.UTC)
	var nanos atomic.Int64
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 3401}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:                    func() time.Time { return base.Add(time.Duration(nanos.Add(1))) },
		AcquireTimeout:         time.Second,
		IdleRecycleAfter:       time.Nanosecond,
		IdleRecycleMemoryBytes: ^uint64(0),
		WorkerMemorySupported:  true,
		ReadWorkerMemory:       func(int) (uint64, bool) { return 1, true },
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 50; i++ {
			runOneRequest(t, p)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 50; i++ {
			p.SweepIdleWorkersForTesting()
		}
	}()
	close(start)
	wg.Wait()
	slot, ok := p.TakeSlotIfAvailable()
	if !ok {
		t.Fatal("slot lost after concurrent acquire/release/sweep")
	}
	if extra, duplicate := p.TakeSlotIfAvailable(); duplicate {
		p.PutSlotBack(extra)
		t.Fatal("slot duplicated after concurrent acquire/release/sweep")
	}
	p.PutSlotBack(slot)
}

func TestPool_RecycleMetrics_RecordedOnceAfterSuccess(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	metrics := &fakeRecycleMetrics{}
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 3501}
	newClient := &fakeClient{pid: 3502}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       15 * time.Minute,
		IdleRecycleMemoryBytes: 500 << 20,
		WorkerMemorySupported:  true,
		ReadWorkerMemory:       func(int) (uint64, bool) { return 800 << 20, true },
		RecycleMetrics:         metrics,
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(15 * time.Minute)
	p.SweepIdleWorkersForTesting()
	p.WaitForRecyclesForTesting()
	want := []recycleMetricRecord{{reason: "idle_memory", rssBytes: 800 << 20, idle: 15 * time.Minute}}
	if diff := cmp.Diff(want, metrics.snapshot(), cmp.AllowUnexported(recycleMetricRecord{})); diff != "" {
		t.Fatalf("recycle metrics mismatch (-want +got):\n%s", diff)
	}
}

func TestPool_RecycleMetrics_NotRecordedAfterFailure(t *testing.T) {
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	metrics := &fakeRecycleMetrics{}
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 3601}
	ff := &stepFactory{steps: []stepResult{
		{client: oldClient},
		{err: errors.New("recycle spawn failed")},
	}}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                ff,
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       time.Minute,
		IdleRecycleMemoryBytes: 1,
		WorkerMemorySupported:  true,
		ReadWorkerMemory:       func(int) (uint64, bool) { return 2, true },
		RecycleMetrics:         metrics,
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(time.Minute)
	p.SweepIdleWorkersForTesting()
	p.WaitForRecyclesForTesting()
	if records := metrics.snapshot(); len(records) != 0 {
		t.Fatalf("recycle metrics after failure = %+v, want none", records)
	}
}

func TestIdleSweepCadence_Clamps(t *testing.T) {
	tests := []struct {
		name  string
		after time.Duration
		want  time.Duration
	}{
		{name: "lower clamp", after: 2 * time.Second, want: time.Second},
		{name: "quarter cadence", after: 8 * time.Second, want: 2 * time.Second},
		{name: "upper boundary", after: 2 * time.Minute, want: 30 * time.Second},
		{name: "upper clamp", after: 15 * time.Minute, want: 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pool.IdleSweepCadenceForTesting(tt.after); got != tt.want {
				t.Fatalf("idle sweep cadence(%v) = %v, want %v", tt.after, got, tt.want)
			}
		})
	}
}

func TestPool_UserActivityExcludesCatalogAndStartsAtRelease(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1101}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := p.Detail()[0]
	if before.UserRequestsSinceSpawn != 0 || before.LastUserReleaseAt != nil {
		t.Fatalf("warmup activity = %+v, want unused", before)
	}

	now = now.Add(5 * time.Minute)
	runOneRequest(t, p)
	after := p.Detail()[0]
	if after.UserRequestsSinceSpawn != 1 || after.LastUserReleaseAt == nil || !after.LastUserReleaseAt.Equal(now) {
		t.Fatalf("request activity = %+v, want count=1 release=%v", after, now)
	}
}

func TestPool_UserActivityTerminalPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "normal result drainage", path: "result"},
		{name: "request context cancellation", path: "context-cancel"},
		{name: "explicit Pool.Cancel", path: "pool-cancel"},
		{name: "Prompt error", path: "prompt-error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
			releasedAt := now.Add(7 * time.Minute)
			client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1201}
			var raw *acp.Stream
			if tt.path == "context-cancel" || tt.path == "pool-cancel" {
				client.promptFn = func(_ context.Context, sid string, _ []canonical.Block) (*acp.Stream, error) {
					raw = acp.NewStreamForTest(sid)
					return raw, nil
				}
			}
			if tt.path == "prompt-error" {
				client.promptFn = func(_ context.Context, _ string, _ []canonical.Block) (*acp.Stream, error) {
					return nil, errors.New("prompt failed")
				}
			}
			p := pool.New(pool.Config{
				Logger:  testutil.Logger(t),
				Size:    1,
				Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
				Now:     func() time.Time { return now },
			})
			defer func() { _ = p.Close() }()
			if err := p.Warmup(context.Background()); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sid, err := p.NewSession(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			now = releasedAt
			var stream interface {
				Chunks() <-chan canonical.Chunk
				Result() (*canonical.FinalResult, error)
			}
			switch tt.path {
			case "result":
				stream, err = p.Prompt(ctx, sid, nil)
				if err == nil {
					drainChunks(stream.Chunks())
					_, err = stream.Result()
				}
			case "context-cancel":
				stream, err = p.Prompt(ctx, sid, nil)
				cancel()
			case "pool-cancel":
				stream, err = p.Prompt(ctx, sid, nil)
				p.Cancel(sid)
			case "prompt-error":
				_, err = p.Prompt(ctx, sid, nil)
				if err == nil {
					t.Fatal("Prompt() error = nil, want failure")
				}
				err = nil
			default:
				t.Fatalf("unknown path %q", tt.path)
			}
			if err != nil {
				t.Fatal(err)
			}

			slot, ok := p.WaitForSlotRelease(200 * time.Millisecond)
			if !ok {
				t.Fatal("terminal path did not return a free slot")
			}
			if got := p.SessionSlotsLen(); got != 0 {
				t.Fatalf("session map length = %d, want 0", got)
			}

			// Fire a later terminal loser while the expected slot remains out of
			// the queue. It must neither overwrite the winning timestamp nor
			// return the slot again.
			now = releasedAt.Add(3 * time.Minute)
			switch tt.path {
			case "result", "prompt-error":
				p.Cancel(sid)
			case "context-cancel", "pool-cancel":
				raw.CloseForTest(&acp.FinalResult{StopReason: canonical.StopCancelled}, nil)
				_, _ = stream.Result()
			}
			if extra, ok := p.TakeSlotIfAvailable(); ok {
				p.PutSlotBack(extra)
				t.Fatal("terminal loser returned the slot a second time")
			}
			assertSlotActivity(t, p, 1, &releasedAt)
			p.PutSlotBack(slot)
		})
	}
}

func TestPool_UserActivityLaterRequestResetsIdle(t *testing.T) {
	now := time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1301}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}

	firstRelease := now.Add(2 * time.Minute)
	now = firstRelease
	runOneRequest(t, p)
	assertSlotActivity(t, p, 1, &firstRelease)

	secondRelease := firstRelease.Add(11 * time.Minute)
	now = secondRelease
	runOneRequest(t, p)
	assertSlotActivity(t, p, 2, &secondRelease)
}

func TestPool_WorkerProcsActivitySemantics(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1401}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}

	procs := p.WorkerProcs()
	if len(procs) != 1 || procs[0].UserRequestsSinceSpawn != 0 || procs[0].IdleSeconds != 0 {
		t.Fatalf("unused WorkerProcs() = %+v, want count=0 idle=0", procs)
	}

	now = now.Add(time.Minute)
	runOneRequest(t, p)
	now = now.Add(4 * time.Minute)
	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	procs = p.WorkerProcs()
	if len(procs) != 1 || procs[0].UserRequestsSinceSpawn != 2 || procs[0].IdleSeconds != 0 {
		t.Fatalf("busy WorkerProcs() = %+v, want count=2 idle=0", procs)
	}

	now = now.Add(time.Minute)
	stream, err := p.Prompt(context.Background(), sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	drainChunks(stream.Chunks())
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(150 * time.Second)
	procs = p.WorkerProcs()
	if len(procs) != 1 || procs[0].UserRequestsSinceSpawn != 2 || procs[0].IdleSeconds != 150 {
		t.Fatalf("idle WorkerProcs() = %+v, want count=2 idle=150", procs)
	}
	now = time.Date(2026, 8, 4, 15, 5, 30, 0, time.UTC)
	if got := p.WorkerProcs()[0].IdleSeconds; got != 0 {
		t.Fatalf("negative-clock idle seconds = %v, want 0", got)
	}
}

func TestPool_UserActivityConfiguredClockInitialSpawn(t *testing.T) {
	now := time.Date(2026, 8, 4, 16, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1501}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	spawnedAt := p.Detail()[0].SpawnedAt
	if spawnedAt == nil || !spawnedAt.Equal(now) {
		t.Fatalf("initial spawned at = %v, want %v", spawnedAt, now)
	}
}

func TestPool_UserActivityConfiguredClockReplacementSpawn(t *testing.T) {
	now := time.Date(2026, 8, 4, 17, 0, 0, 0, time.UTC)
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1601}
	newClient := &fakeClient{pid: 1602}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
		MaxWorkerTurns: 2,
		Now:            func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}

	now = now.Add(20 * time.Minute)
	runOneRequest(t, p)
	slot, ok := p.WaitForSlotRelease(200 * time.Millisecond)
	if !ok {
		t.Fatal("replacement slot was not returned")
	}
	defer p.PutSlotBack(slot)

	row := p.Detail()[0]
	if row.SpawnedAt == nil || !row.SpawnedAt.Equal(now) {
		t.Fatalf("replacement spawned at = %v, want %v", row.SpawnedAt, now)
	}
	assertSlotActivity(t, p, 0, nil)
}
