// Package pool_test — Task 3 worker-recycling behaviour.
//
// Covers turn accounting (request path AND catalog probes), the atomic
// recycle-vs-release decision, the background recycle goroutine, recycle
// failure recovery, cause-aware error classification, and the load-bearing
// shutdown-ordering property (Close waits after recycle admission but before /
// during the recycle goroutine's respawn). Blackbox package plus the
// export_test.go accessors (SlotTurns / SetRecycleLaunchHookForTesting).
package pool_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

// closeCallCount reads fakeClient.closeCalls under its mutex. Defined here (same
// package pool_test as the harness) for the shutdown tests that assert the
// replacement worker was closed on the p.closed branch.
func (f *fakeClient) closeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls
}

// stepFactory dispenses a scripted sequence of Spawn results — each step is
// either a client (success) or an error — so a test can make the warmup Spawn
// succeed, the recycle Spawn fail, and a later lazy-recovery Spawn succeed.
type stepFactory struct {
	mu    sync.Mutex
	steps []stepResult
	idx   int
}

type stepResult struct {
	client pool.PoolClient
	err    error
}

func (ff *stepFactory) Spawn(_ context.Context, _ acp.Config) (pool.PoolClient, error) {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if ff.idx >= len(ff.steps) {
		return nil, errors.New("stepFactory: script exhausted")
	}
	s := ff.steps[ff.idx]
	ff.idx++
	return s.client, s.err
}

// gatedRecycleFactory dispenses the warmup client immediately, then makes every
// subsequent (recycle / lazy) Spawn signal spawnEntered and block on gate. The
// spawnEntered send is the deterministic sync point the "Close during spawn"
// test uses to prove the recycle goroutine is parked inside respawnSlot's Spawn
// when Close runs.
type gatedRecycleFactory struct {
	clients      []pool.PoolClient
	warmupDone   atomic.Bool
	spawnEntered chan struct{}
	gate         chan struct{}

	mu  sync.Mutex
	idx int
}

func (ff *gatedRecycleFactory) Spawn(ctx context.Context, _ acp.Config) (pool.PoolClient, error) {
	if ff.warmupDone.CompareAndSwap(false, true) {
		return ff.next()
	}
	ff.spawnEntered <- struct{}{} // blocking: strictly after the closing-check
	select {
	case <-ff.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return ff.next()
}

func (ff *gatedRecycleFactory) next() (pool.PoolClient, error) {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if ff.idx >= len(ff.clients) {
		return nil, errors.New("gatedRecycleFactory: script exhausted")
	}
	c := ff.clients[ff.idx]
	ff.idx++
	return c, nil
}

// pollUntil polls fn every millisecond up to timeout, returning true once fn is
// true. Used instead of a busy-loop so the background recycle goroutine has a
// chance to run.
func pollUntil(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return fn()
}

// runOneRequest drives NewSession → Prompt → drain → Result for a slot with the
// default (immediately-closed) fake stream. It is invoked from a non-test
// goroutine by the shutdown-interleaving tests, so it uses t.Errorf + early
// return (t.Fatalf may only be called from the goroutine running the test).
func runOneRequest(t *testing.T, p *pool.Pool) {
	t.Helper()
	ctx := context.Background()
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Errorf("NewSession(): %v", err)
		return
	}
	stream, err := p.Prompt(ctx, sid, nil)
	if err != nil {
		t.Errorf("Prompt(): %v", err)
		return
	}
	drainChunks(stream.Chunks())
	if _, err := stream.Result(); err != nil {
		t.Errorf("Result(): %v", err)
		return
	}
}

// TestPool_CatalogProbeCounts: the warmup catalog session/new counts as exactly
// one turn on slot 0 (review finding M-4 — probes accumulate memory too).
func TestPool_CatalogProbeCounts(t *testing.T) {
	fc := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	if turns, ok := p.SlotTurns("slot-0"); !ok || turns != 1 {
		t.Fatalf("SlotTurns(slot-0) after warmup = (%d, %v); want (1, true)", turns, ok)
	}
}

// TestPool_WorkerTurns_SuccessIncrementsFailureDoesNot: a successful
// session/new increments the counter; a failed one does not.
func TestPool_WorkerTurns_SuccessIncrementsFailureDoesNot(t *testing.T) {
	var n int32
	fc := &fakeClient{
		models: []canonical.ModelInfo{{ID: "auto"}},
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			// warmup catalog (1) + one successful request (2) succeed; the
			// next request's session/new fails.
			if atomic.AddInt32(&n, 1) <= 2 {
				return "sess", nil
			}
			return "", errors.New("kiro: session/new boom")
		},
	}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
		// MaxWorkerTurns unset (0) → no recycling; turns just accumulate.
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	// warmup catalog probe → turns == 1.
	if turns, _ := p.SlotTurns("slot-0"); turns != 1 {
		t.Fatalf("SlotTurns after warmup = %d; want 1", turns)
	}
	// one successful request → turns == 2.
	runOneRequest(t, p)
	if turns, _ := p.SlotTurns("slot-0"); turns != 2 {
		t.Fatalf("SlotTurns after 1 request = %d; want 2", turns)
	}
	// a failed session/new must NOT increment.
	if _, err := p.NewSession(context.Background(), ""); err == nil {
		t.Fatal("NewSession() = nil err; want failure")
	}
	if turns, _ := p.SlotTurns("slot-0"); turns != 2 {
		t.Fatalf("SlotTurns after failed NewSession = %d; want 2 (failure must not count)", turns)
	}
}

// TestPool_WorkerRecycleAtThreshold is the brief's primary happy-path test:
// with MaxWorkerTurns=2, the first completed request after warmup recycles the
// worker via a background respawn (recycles counter, not respawns), and the
// fresh slot re-enters the pool with turns reset to 0.
func TestPool_WorkerRecycleAtThreshold(t *testing.T) {
	oldClient := &fakeClient{
		models: []canonical.ModelInfo{{ID: "auto"}},
		pid:    1001,
	}
	newClient := &fakeClient{pid: 1002}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	// Capture the pre-recycle SpawnedAt + Pid so we can assert both change
	// once the recycle respawn completes (dashboard "UP" cell resets, and
	// the pid visibly confirms the worker was actually replaced — the label
	// stays "slot-0" by design).
	preRows := p.Detail()
	var preSpawnedAt time.Time
	var prePid int
	for _, r := range preRows {
		if r.Label == "slot-0" {
			if r.SpawnedAt == nil {
				t.Fatal("pre-recycle Detail(): slot-0 SpawnedAt is nil")
			}
			preSpawnedAt = *r.SpawnedAt
			prePid = r.Pid
		}
	}
	if preSpawnedAt.IsZero() {
		t.Fatal("pre-recycle SpawnedAt not captured for slot-0")
	}
	if prePid != 1001 {
		t.Fatalf("pre-recycle Detail(): slot-0 Pid = %d; want 1001 (oldClient)", prePid)
	}

	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatalf("NewSession(): %v", err)
	}
	stream, err := p.Prompt(context.Background(), sid, nil)
	if err != nil {
		t.Fatalf("Prompt(): %v", err)
	}
	drainChunks(stream.Chunks())
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result(): %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for p.Recycles() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := p.Recycles(); got != 1 {
		t.Fatalf("Recycles() = %d; want 1", got)
	}
	if got := p.Respawns(); got != 0 {
		t.Fatalf("Respawns() = %d; want 0", got)
	}
	if turns, ok := p.SlotTurns("slot-0"); !ok || turns != 0 {
		t.Fatalf("SlotTurns(slot-0) = (%d, %v); want (0, true)", turns, ok)
	}
	// The old worker was torn down as part of the recycle respawn.
	if got := oldClient.closeCallCount(); got < 1 {
		t.Fatalf("old client closeCalls = %d; want >= 1", got)
	}

	// Detail() must reflect the completed recycle: turns reset to 0 and
	// spawned_at advanced past the pre-recycle worker's spawn time (dashboard
	// operators watch both reset together when a recycle completes).
	postRows := p.Detail()
	for _, r := range postRows {
		if r.Label != "slot-0" {
			continue
		}
		if r.Turns != 0 {
			t.Errorf("post-recycle Detail(): slot-0 Turns = %d; want 0", r.Turns)
		}
		if r.SpawnedAt == nil {
			t.Fatal("post-recycle Detail(): slot-0 SpawnedAt is nil")
		}
		if !r.SpawnedAt.After(preSpawnedAt) {
			t.Errorf("post-recycle Detail(): slot-0 SpawnedAt = %v; want strictly after pre-recycle %v",
				*r.SpawnedAt, preSpawnedAt)
		}
		// The pid is the operator-visible confirmation that a recycle
		// actually happened — the label is stable by design, so the pid
		// changing (1001 → 1002, newClient) is the only thing that moves.
		if r.Pid != 1002 {
			t.Errorf("post-recycle Detail(): slot-0 Pid = %d; want 1002 (newClient)", r.Pid)
		}
		if r.Pid == prePid {
			t.Errorf("post-recycle Detail(): slot-0 Pid unchanged at %d; want it to differ from pre-recycle", r.Pid)
		}
	}
}

// TestPool_WorkerRecycleDisabled: MaxWorkerTurns=0 never recycles, even well
// past what would be a threshold — the slot returns to the free queue and no
// replacement is spawned.
func TestPool_WorkerRecycleDisabled(t *testing.T) {
	// Only one client is scripted; a recycle attempt would exhaust the factory.
	fc := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	for i := 0; i < 3; i++ {
		runOneRequest(t, p)
	}
	// give any (erroneous) recycle goroutine a chance to run.
	time.Sleep(20 * time.Millisecond)
	if got := p.Recycles(); got != 0 {
		t.Fatalf("Recycles() = %d; want 0 (recycling disabled)", got)
	}
	// warmup(1) + 3 requests(4) = 4 turns, none reset by a recycle.
	if turns, _ := p.SlotTurns("slot-0"); turns != 4 {
		t.Fatalf("SlotTurns = %d; want 4", turns)
	}
}

// TestPool_WorkerRecycleViaCancel: the release path through Pool.Cancel /
// releaseSlotForSession also reaches releaseOrRecycle.
func TestPool_WorkerRecycleViaCancel(t *testing.T) {
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	newClient := &fakeClient{}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	sid, err := p.NewSession(context.Background(), "") // turns -> 2
	if err != nil {
		t.Fatalf("NewSession(): %v", err)
	}
	p.Cancel(sid) // releaseSlotForSession -> releaseOrRecycle -> recycle
	if !pollUntil(time.Second, func() bool { return p.Recycles() == 1 }) {
		t.Fatalf("Recycles() = %d; want 1 (recycle via Cancel)", p.Recycles())
	}
}

// TestPool_WorkerRecycleViaPromptError: the Prompt-error release path reaches
// releaseOrRecycle.
func TestPool_WorkerRecycleViaPromptError(t *testing.T) {
	oldClient := &fakeClient{
		models: []canonical.ModelInfo{{ID: "auto"}},
		promptFn: func(_ context.Context, _ string, _ []canonical.Block) (*acp.Stream, error) {
			return nil, errors.New("prompt boom")
		},
	}
	newClient := &fakeClient{}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	sid, err := p.NewSession(context.Background(), "") // turns -> 2
	if err != nil {
		t.Fatalf("NewSession(): %v", err)
	}
	if _, err := p.Prompt(context.Background(), sid, nil); err == nil {
		t.Fatal("Prompt() = nil err; want prompt failure")
	}
	if !pollUntil(time.Second, func() bool { return p.Recycles() == 1 }) {
		t.Fatalf("Recycles() = %d; want 1 (recycle via Prompt error)", p.Recycles())
	}
}

// TestPool_WorkerRecycleViaSelfHeal: a catalog self-heal probe increments the
// turn counter and returns its slot through releaseOrRecycle, so a
// probe-bloated worker recycles rather than being exempt (review M-4).
func TestPool_WorkerRecycleViaSelfHeal(t *testing.T) {
	var warm atomic.Bool
	oldClient := &fakeClient{
		availableModelsFn: func() []canonical.ModelInfo {
			if warm.Load() {
				return []canonical.ModelInfo{{ID: "auto"}}
			}
			return nil // cold at boot → degraded catalog
		},
	}
	newClient := &fakeClient{}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
	})
	p.SetCatalogRetryForTesting(nil) // exactly one warmup probe → turns == 1
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	if turns, _ := p.SlotTurns("slot-0"); turns != 1 {
		t.Fatalf("SlotTurns after degraded warmup = %d; want 1", turns)
	}
	// kiro warms up; a Models() read triggers one self-heal probe → turns 2 →
	// its releaseOrRecycle return recycles the worker.
	warm.Store(true)
	_ = p.Models()
	if !pollUntil(2*time.Second, func() bool { return p.Recycles() == 1 }) {
		t.Fatalf("Recycles() = %d; want 1 (recycle via self-heal probe)", p.Recycles())
	}
}

// TestPool_WorkerRecycleFailureRequeuesDead: when the recycle respawn fails, the
// slot re-enters the free queue marked dead and the next acquire performs lazy
// recovery. The recycles counter stays 0; the lazy respawn bumps respawns.
func TestPool_WorkerRecycleFailureRequeuesDead(t *testing.T) {
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	goodClient := &fakeClient{}
	ff := &stepFactory{steps: []stepResult{
		{client: oldClient},                        // warmup
		{err: errors.New("recycle spawn: no fds")}, // recycle respawn FAILS
		{client: goodClient},                       // lazy recovery
	}}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        ff,
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	runOneRequest(t, p) // turns -> 2 -> recycle respawn fails -> slot requeued dead

	if !pollUntil(time.Second, func() bool {
		alive, ok := p.SlotAlive("slot-0")
		return ok && !alive
	}) {
		t.Fatal("slot did not become dead after failed recycle respawn")
	}
	if got := p.Recycles(); got != 0 {
		t.Fatalf("Recycles() = %d; want 0 (recycle respawn failed)", got)
	}
	// Next acquire drives the lazy respawn to success.
	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatalf("lazy recovery NewSession(): %v", err)
	}
	if sid == "" {
		t.Fatal("lazy recovery NewSession returned empty sid")
	}
	if got := p.Respawns(); got != 1 {
		t.Fatalf("Respawns() = %d; want 1 (lazy recovery)", got)
	}
}

// TestPool_WorkerRecycleDeadlineRecordsSpawnError: a context.DeadlineExceeded on
// the recycle cause is a genuine failure (no caller to disconnect) and is
// recorded via recordSpawnErr → HealthSummary().LastSpawnError (review M-3).
func TestPool_WorkerRecycleDeadlineRecordsSpawnError(t *testing.T) {
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	ff := &stepFactory{steps: []stepResult{
		{client: oldClient},             // warmup
		{err: context.DeadlineExceeded}, // recycle respawn hits the 30s budget
	}}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        ff,
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	runOneRequest(t, p) // turns -> 2 -> recycle respawn deadline-exceeds

	if !pollUntil(time.Second, func() bool { return p.HealthSummary().LastSpawnError != "" }) {
		t.Fatal("recycle-cause DeadlineExceeded was not recorded in LastSpawnError")
	}
	if got := p.Recycles(); got != 0 {
		t.Fatalf("Recycles() = %d; want 0 (recycle respawn failed)", got)
	}
}

// TestPool_WorkerRecycleLazyDeadlineSuppressed is the WR-07 regression guard:
// a context.DeadlineExceeded on the LAZY cause (caller disconnect during a slow
// dequeue-time respawn) stays suppressed — LastSpawnError remains clean.
func TestPool_WorkerRecycleLazyDeadlineSuppressed(t *testing.T) {
	oldClient := &fakeClient{}
	ff := &stepFactory{steps: []stepResult{
		{client: oldClient},             // warmup
		{err: context.DeadlineExceeded}, // lazy respawn: benign caller-disconnect
	}}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: ff, // MaxWorkerTurns 0 → no recycling; only lazy path exercised
	})
	p.SetCatalogRetryForTesting(nil) // empty catalog: one warmup probe, no backoff sleeps
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	// Kill the warmup worker so the next acquire takes the lazy respawn path.
	oldClient.fireDone()
	if !pollUntil(time.Second, func() bool { return p.Stats().Alive == 0 }) {
		t.Fatal("slot did not flip dead after Done() fire")
	}
	// Lazy respawn fails with DeadlineExceeded → suppressed (aborted, not recorded).
	if _, err := p.NewSession(context.Background(), ""); err == nil {
		t.Fatal("NewSession() = nil err; want deferred respawn failure")
	}
	if got := p.HealthSummary().LastSpawnError; got != "" {
		t.Fatalf("LastSpawnError = %q; want empty (lazy ctx-deadline suppressed)", got)
	}
}

// TestPool_WorkerRecycleCloseAfterCommitBeforeLaunch proves THE shutdown
// property: once releaseOrRecycle has committed (recycleWG.Add under p.mu),
// Close's recycleWG.Wait blocks until the recycle work is released — even
// before the recycle goroutine has launched. The launch hook holds execution
// between commit and goroutine start.
func TestPool_WorkerRecycleCloseAfterCommitBeforeLaunch(t *testing.T) {
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1001}
	newClient := &fakeClient{pid: 1002}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	p.SetRecycleLaunchHookForTesting(func() {
		close(entered) // admission (recycleWG.Add) already happened
		<-release      // hold between commit-to-recycle and goroutine launch
	})

	driverDone := make(chan struct{})
	go func() {
		defer close(driverDone)
		runOneRequest(t, p) // Result() blocks inside releaseOrRecycle's hook
	}()

	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()

	// Close must NOT finish while the recycle work is committed-but-unreleased.
	select {
	case <-closeDone:
		t.Fatal("Close finished before recycle work was released")
	case <-time.After(150 * time.Millisecond):
	}

	close(release) // hook returns → goroutine launches → sees p.closing → drops slot
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close(): %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after recycle work was released")
	}
	<-driverDone

	// The recycle goroutine dropped the slot on shutdown — nothing pushed back.
	if _, ok := p.WaitForSlotRelease(100 * time.Millisecond); ok {
		t.Fatal("recycle goroutine pushed a slot after shutdown")
	}
}

// TestPool_WorkerRecycleCloseDuringSpawn: Close runs while the recycle goroutine
// is parked inside respawnSlot's Spawn. After the spawn completes, the
// goroutine's p.closed branch closes the freshly-spawned replacement client and
// drops the slot; Close then returns; goleak stays clean.
func TestPool_WorkerRecycleCloseDuringSpawn(t *testing.T) {
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1001}
	newClient := &fakeClient{pid: 1002}
	gf := &gatedRecycleFactory{
		clients:      []pool.PoolClient{oldClient, newClient},
		spawnEntered: make(chan struct{}),
		gate:         make(chan struct{}),
	}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        gf,
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	// Drive the threshold request; Result launches the recycle goroutine, which
	// passes the closing-check and parks inside Spawn.
	runOneRequest(t, p)
	<-gf.spawnEntered

	closeDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()

	// closeAll has set p.closed and closed the old client; Close now blocks on
	// recycleWG.Wait while the goroutine is inside Spawn.
	select {
	case <-closeDone:
		t.Fatal("Close finished while a recycle spawn was in flight")
	case <-time.After(150 * time.Millisecond):
	}

	close(gf.gate) // Spawn returns newClient → swap → p.closed branch closes it
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close(): %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after the recycle spawn completed")
	}

	if got := newClient.closeCallCount(); got < 1 {
		t.Fatalf("replacement client closeCalls = %d; want >= 1 (closed on p.closed branch)", got)
	}
	if _, ok := p.WaitForSlotRelease(100 * time.Millisecond); ok {
		t.Fatal("recycle goroutine pushed a slot after shutdown")
	}
}

// TestPool_WorkerRecycleConcurrentCloseNoRaceNoLeak stresses concurrent request
// releases (each recycling at MaxWorkerTurns=1) against Close. Run under -race
// it guards the closeAll {label, client} snapshot fix (review H-2); under the
// package goleak gate it guards against recycle goroutines outliving Close.
func TestPool_WorkerRecycleConcurrentCloseNoRaceNoLeak(t *testing.T) {
	const size = 4
	clients := make([]pool.PoolClient, 0, size*6)
	for i := 0; i < size; i++ {
		clients = append(clients, &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}})
	}
	for i := 0; i < size*5; i++ { // replacements for the recycles
		clients = append(clients, &fakeClient{})
	}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           size,
		MaxWorkerTurns: 1, // every request recycles its worker
		Factory:        &fakeClientFactory{clients: clients},
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			sid, err := p.NewSession(ctx, "")
			if err != nil {
				return // pool closed or transient respawn failure — tolerated
			}
			stream, err := p.Prompt(ctx, sid, nil)
			if err != nil {
				return
			}
			drainChunks(stream.Chunks())
			_, _ = stream.Result()
		}()
	}

	time.Sleep(5 * time.Millisecond) // let some recycles get in flight
	if err := p.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	wg.Wait()
}

// syncBuf is a goroutine-safe io.Writer over a bytes.Buffer so the exit-watcher
// goroutine's slog output can be inspected from the test goroutine without a
// data race (the -race gate would otherwise flag concurrent Write/String).
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPool_WorkerRecycle_StaleWatcherDoesNotDeadMark is the regression test for
// the recycle exit-watcher race (final-review IMPORTANT-1/2): a stale OLD
// exit-watcher, waking AFTER respawnSlot has swapped in a fresh healthy worker,
// must NOT dead-mark that worker nor emit the operator-facing "pool: slot died"
// crash signal.
//
// Determinism (no sleeps-as-sync): the exit-watcher panic probe seam runs at
// every watcher-goroutine start, BEFORE its select. We install a probe that
// parks every watcher on a gate channel, so the OLD watcher cannot observe its
// (already-fired) Done() until we open the gate — which we do only AFTER the
// recycle swap has completed (Recycles()==1). This places the OLD watcher's
// wakeup deterministically in the post-swap window that the identity guard
// covers. Red-check: reverting the guard in exit_watcher.go's <-done branch
// (unconditional dead-mark + "pool: slot died") makes this test fail on both
// the SlotAlive assertion and the "pool: slot died" log assertion.
func TestPool_WorkerRecycle_StaleWatcherDoesNotDeadMark(t *testing.T) {
	logBuf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Park every exit-watcher at goroutine start, before its select.
	gate := make(chan struct{})
	restore := pool.SetExitWatcherPanicProbeForTest(func() { <-gate })
	t.Cleanup(restore)
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }

	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1001}
	newClient := &fakeClient{pid: 1002}
	p := pool.New(pool.Config{
		Logger: logger,
		Size:   1,
		// Threshold 2 so the warmup catalog turn (turns=1) stays below it and
		// does NOT trigger the Finding-2 warmup-time recycle; the one completed
		// request (turns=2) is then the sole recycle this test exercises.
		MaxWorkerTurns: 2,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
	})
	// Always release the gate (so parked watchers can exit) and Close the pool,
	// even on an early t.Fatalf, so the goleak gate stays clean.
	t.Cleanup(func() {
		openGate()
		_ = p.Close()
	})

	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	// One request drives turns to 2 (warmup probe consumed turn 1) → background
	// recycle: respawnSlot closes the
	// OLD client, swaps in the NEW client, and resets dead=false /
	// respawning=false.
	runOneRequest(t, p)
	if !pollUntil(2*time.Second, func() bool { return p.Recycles() == 1 }) {
		t.Fatalf("Recycles() = %d; want 1 (recycle did not complete)", p.Recycles())
	}
	if alive, ok := p.SlotAlive("slot-0"); !ok || !alive {
		t.Fatalf("slot-0 alive=(%v, ok=%v) before gate open; want alive,true", alive, ok)
	}

	// Simulate the OLD client's Done() firing as a result of the recycle's
	// Close. The real acp.Client fires Done() from Close(); the fakeClient does
	// not, so we fire it explicitly here — AFTER the swap — to model the stale
	// OLD watcher waking in the post-swap window.
	oldClient.fireDone()

	// Open the gate: the OLD watcher now reaches its select and observes the
	// OLD client's now-fired Done(). With the guard this is a planned teardown
	// (slot.Client is the NEW client → identity mismatch) and must be skipped.
	openGate()

	// Deterministic sync point: wait until the OLD watcher has processed its
	// <-done branch, observable via the log line it emits — the planned-teardown
	// Debug on the fixed path, or "pool: slot died" on the buggy path.
	if !pollUntil(2*time.Second, func() bool {
		s := logBuf.String()
		return strings.Contains(s, "planned teardown") || strings.Contains(s, "pool: slot died")
	}) {
		t.Fatal("OLD watcher never processed its Done() branch after gate open")
	}

	// The freshly recycled worker must still be alive and un-respawned, and no
	// false crash signal may have been logged.
	if alive, ok := p.SlotAlive("slot-0"); !ok || !alive {
		t.Errorf("slot-0 alive=(%v, ok=%v) after stale watcher fired; want alive,true — recycle race dead-marked a healthy worker", alive, ok)
	}
	if got := p.Respawns(); got != 0 {
		t.Errorf("Respawns() = %d; want 0 (recycled worker must not be torn down via the lazy path)", got)
	}
	if s := logBuf.String(); strings.Contains(s, "pool: slot died") {
		t.Errorf(`log contains "pool: slot died" — stale OLD watcher emitted a false crash signal for a planned recycle`)
	}
}

// TestPool_WorkerRecycle_RecyclingSlotReportsUnavailable is the Finding-1
// regression guard: while a worker is mid-recycle (respawning==true,
// dead==false — respawnSlot has closed the OLD worker and is spawning the
// replacement) every health surface must report it unavailable, and every
// surface must recover once the respawn completes.
//
// Determinism (no sleeps as sync): the gatedRecycleFactory blocks the recycle
// Spawn and signals spawnEntered from inside it. respawnSlot sets
// respawning=true UNDER p.mu (step 0) before calling Spawn, so receiving
// spawnEntered places us deterministically in the mid-recycle window.
// Red-check: reverting the `!s.respawning` predicates (Stats/HealthSummary),
// detail.go's `Alive` term, and WorkerProcs' skip makes the four during-recycle
// assertions fail (Alive counts 1, Detail row Alive==true, WorkerProcs returns
// the terminated OLD pid).
func TestPool_WorkerRecycle_RecyclingSlotReportsUnavailable(t *testing.T) {
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1001}
	newClient := &fakeClient{pid: 1002}
	gf := &gatedRecycleFactory{
		clients:      []pool.PoolClient{oldClient, newClient},
		spawnEntered: make(chan struct{}),
		gate:         make(chan struct{}),
	}
	p := pool.New(pool.Config{
		Logger: testutil.Logger(t),
		Size:   1,
		// Threshold 2: warmup catalog turn (=1) stays below it; the one request
		// drives turns to 2 and triggers the background recycle we gate on.
		MaxWorkerTurns: 2,
		Factory:        gf,
	})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gf.gate) }) }
	// Always release the gate (so the parked recycle Spawn can finish) and Close,
	// even on early t.Fatalf, so recycleWG.Wait cannot hang and goleak stays clean.
	t.Cleanup(func() {
		openGate()
		_ = p.Close()
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	// One request drives turns to 2 → background recycle. The recycle goroutine
	// marks the slot respawning, closes the OLD worker, and parks inside the
	// gated Spawn — the deterministic mid-recycle window.
	runOneRequest(t, p)
	<-gf.spawnEntered

	// Every health surface must report the recycling slot as unavailable.
	if got := p.Stats().Alive; got != 0 {
		t.Errorf("Stats().Alive during recycle = %d; want 0", got)
	}
	hs := p.HealthSummary()
	if hs.Alive != 0 {
		t.Errorf("HealthSummary().Alive during recycle = %d; want 0", hs.Alive)
	}
	if hs.Healthy {
		t.Error("HealthSummary().Healthy during recycle = true; want false (size-1 pool, only worker recycling)")
	}
	detail := p.Detail()
	if len(detail) != 1 {
		t.Fatalf("Detail() len = %d; want 1", len(detail))
	}
	if detail[0].Alive {
		t.Error("Detail()[0].Alive during recycle = true; want false")
	}
	if procs := p.WorkerProcs(); len(procs) != 0 {
		t.Errorf("WorkerProcs() during recycle = %v; want empty (respawning slot excluded — stale/terminated pid)", procs)
	}

	// Release the gate; the recycle completes and every surface recovers.
	openGate()
	if !pollUntil(2*time.Second, func() bool { return p.Recycles() == 1 }) {
		t.Fatalf("Recycles() = %d; want 1 (recycle did not complete)", p.Recycles())
	}
	if !pollUntil(time.Second, func() bool { return p.Stats().Alive == 1 }) {
		t.Fatalf("Stats().Alive after recovery = %d; want 1", p.Stats().Alive)
	}
	if hs := p.HealthSummary(); hs.Alive != 1 || !hs.Healthy {
		t.Errorf("HealthSummary() after recovery = {Alive:%d Healthy:%v}; want {1 true}", hs.Alive, hs.Healthy)
	}
	if detail := p.Detail(); len(detail) != 1 || !detail[0].Alive {
		t.Errorf("Detail() after recovery = %+v; want a single alive row", detail)
	}
	if !pollUntil(time.Second, func() bool {
		procs := p.WorkerProcs()
		return len(procs) == 1 && procs[0].Pid == 1002
	}) {
		t.Errorf("WorkerProcs() after recovery = %v; want single row with the NEW pid 1002", p.WorkerProcs())
	}
}

// TestPool_WarmupRecyclesSlotAtThreshold is the Finding-2 (a) guard: with
// MaxWorkerTurns=1 the single warmup catalog probe already puts slot-0 at the
// threshold, so Warmup must recycle it SYNCHRONOUSLY before publishing —
// otherwise the worn worker would serve one request. After Warmup returns the
// recycle counter is 1, the published slot is the SECOND client with turns
// reset, and the first user request lands on that fresh client.
func TestPool_WarmupRecyclesSlotAtThreshold(t *testing.T) {
	firstClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1001}
	secondClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1002}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 1,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{firstClient, secondClient}},
	})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	if got := p.Recycles(); got != 1 {
		t.Fatalf("Recycles() after Warmup = %d; want 1 (synchronous warmup recycle at threshold)", got)
	}
	if turns, ok := p.SlotTurns("slot-0"); !ok || turns != 0 {
		t.Fatalf("SlotTurns(slot-0) after Warmup = (%d, %v); want (0, true)", turns, ok)
	}
	if got := firstClient.closeCallCount(); got < 1 {
		t.Errorf("first client closeCalls = %d; want >= 1 (torn down by the warmup recycle)", got)
	}

	// The first user request must be served by the fresh (second) client.
	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatalf("NewSession(): %v", err)
	}
	if sid == "" {
		t.Fatal("NewSession returned empty sid")
	}
	if got := secondClient.newSessionCount(); got < 1 {
		t.Errorf("second client newSessionCount = %d; want >= 1 (serves the first request)", got)
	}
	// The first client saw only its single warmup catalog session/new.
	if got := firstClient.newSessionCount(); got != 1 {
		t.Errorf("first client newSessionCount = %d; want 1 (warmup catalog probe only)", got)
	}
}

// TestPool_WarmupRecyclesOnEmptyCatalogOvershoot is the Finding-2 (b) guard: an
// empty-catalog warmup runs the full retry schedule, and every probe is a
// counted turn, so the accumulated count can OVERSHOOT the threshold. Warmup
// must still recycle exactly once at publish and reset the fresh worker's
// counter. Four probe attempts (three zero-delay retries) → turns=4 with
// MaxWorkerTurns=2.
func TestPool_WarmupRecyclesOnEmptyCatalogOvershoot(t *testing.T) {
	firstClient := &fakeClient{
		// session/new succeeds but the model list stays empty, so
		// captureCatalogWithRetry runs every attempt — each a counted turn.
		availableModelsFn: func() []canonical.ModelInfo { return nil },
	}
	secondClient := &fakeClient{}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{firstClient, secondClient}},
	})
	// Three zero-delay retries → four probe attempts → turns == 4, no real sleeps.
	p.SetCatalogRetryForTesting([]time.Duration{0, 0, 0})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	if got := firstClient.newSessionCount(); got != 4 {
		t.Fatalf("first client newSessionCount = %d; want 4 (four empty-catalog probe attempts)", got)
	}
	if got := p.Recycles(); got != 1 {
		t.Fatalf("Recycles() after Warmup = %d; want 1 (single synchronous recycle despite overshoot)", got)
	}
	if turns, ok := p.SlotTurns("slot-0"); !ok || turns != 0 {
		t.Fatalf("SlotTurns(slot-0) after Warmup = (%d, %v); want (0, true) (fresh worker counter reset)", turns, ok)
	}
}

// TestPool_ReleaseAfterCloseDropsSlot is the Finding-3 hardening guard: a
// release that completes AFTER Close must DROP the slot, not requeue it — a
// post-close push would let a racing fast-path acquire dequeue a closed client
// and surface a confusing 500 instead of "pool: closed". Deterministic: Cancel
// runs the release path synchronously, so after it returns the drop has
// definitely happened.
//
// Red-check: reverting the `if p.closed { return }` restructure in
// releaseOrRecycle (back to the pre-existing `|| p.closed` push branch) makes
// this test fail — the slot is requeued and WaitForSlotRelease observes it.
func TestPool_ReleaseAfterCloseDropsSlot(t *testing.T) {
	fc := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	t.Cleanup(func() { _ = p.Close() }) // idempotent — safe after the explicit Close below
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	// Check out the only slot (dequeued into sessionSlots).
	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatalf("NewSession(): %v", err)
	}

	// Close the pool while the slot is checked out; closeAll owns teardown via
	// its p.all snapshot.
	if err := p.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	// Complete the release path AFTER Close. releaseOrRecycle must observe
	// p.closed and drop rather than requeue.
	p.Cancel(sid) // releaseSlotForSession → releaseOrRecycle (p.closed → drop)

	if s, ok := p.WaitForSlotRelease(100 * time.Millisecond); ok {
		t.Fatalf("slot %q was requeued after Close; want dropped (closeAll owns cleanup)", s.Label)
	}
}

// ---------------------------------------------------------------------------
// Finding 1 (round-3 review): NewSession rejects closed slots post-Close.
// ---------------------------------------------------------------------------

// gatedNewSessionClient builds a fakeClient whose FIRST NewSession (the warmup
// catalog probe) returns immediately, but every subsequent NewSession signals
// `entered` and blocks on `gate` before returning `result()`. The Finding 1
// race tests use it to park a request-path NewSession INSIDE the client call
// while Close runs, then release it to drive the closed-aware requeue (error
// variant) and the closed-aware register-suppression (success variant). The
// gate is released strictly after Close returns, so the acquire + fast-path
// p.closed check already passed while the pool was live — no sleep-as-sync.
func gatedNewSessionClient(result func() (string, error)) (fc *fakeClient, entered <-chan struct{}, gate chan struct{}) {
	ent := make(chan struct{}, 1)
	g := make(chan struct{})
	var n atomic.Int32
	fc = &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	fc.newSessionFn = func(_ context.Context, _ string) (string, error) {
		if n.Add(1) == 1 {
			return "sess-warmup", nil // warmup catalog probe: return immediately
		}
		ent <- struct{}{}
		<-g
		return result()
	}
	return fc, ent, g
}

// TestPool_NewSession_AfterClose_FastPathDropsClosedSlot pins Finding 1(a): an
// idle slot left buffered in p.slots after Close (closeAll does not drain it)
// must NOT be handed to a client call. The fast-path acquire dequeues it, the
// new p.closed check drops it, and NewSession returns exactly "pool: closed"
// with no client method invoked and no requeue.
//
// Red-check (production reverted): the fast-path has no closed arm, slotAlive
// passes (the watcher exited via <-p.closing without dead-marking), and the
// default fakeClient.NewSession succeeds → NewSession returns a sid + nil error,
// failing the `want error` assertion (and newSessionCount would advance).
func TestPool_NewSession_AfterClose_FastPathDropsClosedSlot(t *testing.T) {
	fc := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	baseline := fc.newSessionCount() // warmup catalog probe consumed exactly 1
	if err := p.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	_, err := p.NewSession(context.Background(), "")
	if err == nil {
		t.Fatal("NewSession after Close: want error, got nil")
	}
	if err.Error() != "pool: closed" {
		t.Fatalf("NewSession after Close error = %q; want exactly \"pool: closed\"", err.Error())
	}
	if got := fc.newSessionCount(); got != baseline {
		t.Fatalf("newSessionCount = %d; want %d (no client method invoked on the dequeued closed slot)", got, baseline)
	}
	if s, ok := p.WaitForSlotRelease(100 * time.Millisecond); ok {
		t.Fatalf("closed slot %q was requeued to p.slots; want dropped", s.Label)
	}
}

// TestPool_NewSession_RacesClose_NoRequeueOnError pins Finding 1(b) error
// variant: a NewSession that acquired a live slot, then had its in-flight
// client NewSession fail BECAUSE Close raced it, must DROP the slot (closed-
// aware requeue) rather than push a closed client back for a fast-path acquire.
//
// Red-check (production reverted): the error-path requeue is the unconditional
// `p.slots <- slot`, so the closed slot lands back in p.slots and
// WaitForSlotRelease observes it → the `want dropped` assertion fails.
func TestPool_NewSession_RacesClose_NoRequeueOnError(t *testing.T) {
	fc, entered, gate := gatedNewSessionClient(func() (string, error) {
		return "", errors.New("newsession boom")
	})
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	type result struct {
		sid string
		err error
	}
	resC := make(chan result, 1)
	go func() {
		sid, err := p.NewSession(context.Background(), "")
		resC <- result{sid, err}
	}()
	<-entered // NewSession acquired the slot (fast-path closed check passed) and is parked in the client call
	if err := p.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	close(gate) // client NewSession now returns its error, under p.closed

	res := <-resC
	if res.err == nil {
		t.Fatal("NewSession: want error, got nil")
	}
	if !strings.Contains(res.err.Error(), "new-session") {
		t.Fatalf("NewSession error = %q; want the wrapped new-session error", res.err.Error())
	}
	if s, ok := p.WaitForSlotRelease(100 * time.Millisecond); ok {
		t.Fatalf("failed-NewSession slot %q was requeued after Close; want dropped", s.Label)
	}
}

// TestPool_NewSession_RacesClose_SuccessReturnsClosed pins Finding 1(b) success
// variant: a NewSession whose in-flight client call SUCCEEDS after Close must
// not register the orphan session — it releases the lock, best-effort Cancels
// the just-created session, drops the slot, and returns "pool: closed".
//
// Red-check (production reverted): the register critical section has no
// p.closed guard, so the session is registered and (sid, nil) returned → both
// the `want "pool: closed"` and `SessionSlotsLen == 0` assertions fail, and no
// Cancel is issued for the orphan session.
func TestPool_NewSession_RacesClose_SuccessReturnsClosed(t *testing.T) {
	fc, entered, gate := gatedNewSessionClient(func() (string, error) {
		return "sess-late", nil
	})
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	type result struct {
		sid string
		err error
	}
	resC := make(chan result, 1)
	go func() {
		sid, err := p.NewSession(context.Background(), "")
		resC <- result{sid, err}
	}()
	<-entered
	if err := p.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	close(gate) // client NewSession returns success AFTER Close

	res := <-resC
	if res.err == nil {
		t.Fatalf("NewSession: want \"pool: closed\", got sid=%q nil err", res.sid)
	}
	if res.err.Error() != "pool: closed" {
		t.Fatalf("NewSession error = %q; want exactly \"pool: closed\"", res.err.Error())
	}
	if res.sid != "" {
		t.Fatalf("NewSession sid = %q; want empty on closed pool", res.sid)
	}
	if got := p.SessionSlotsLen(); got != 0 {
		t.Fatalf("SessionSlotsLen() = %d; want 0 (orphan session not registered on closed pool)", got)
	}
	var cancelledLate bool
	for _, c := range fc.cancelCallList() {
		if c == "sess-late" {
			cancelledLate = true
		}
	}
	if !cancelledLate {
		t.Fatalf("Cancel calls = %v; want to include \"sess-late\" (orphan session cancelled)", fc.cancelCallList())
	}
}

// TestPool_WorkerRecycleFailure_AtomicDeadNotRespawning is the Finding 2
// (round-3) guard. A failed BACKGROUND recycle used to clear respawning in
// respawnSlot's error section but mark dead only later in recycleSlot; between
// the two a health snapshot saw !dead && !respawning and counted the already-
// closed old worker as alive (stale PID in WorkerProcs). respawnSlot now sets
// dead=true in the SAME critical section that clears respawning on the recycle
// cause, so the transition is atomic.
//
// What this pins: the TERMINAL invariant (dead=true ∧ respawning=false) after a
// failed recycle. The transient no-window property (never !dead && !respawning
// mid-recycle) is not directly wedgeable without a new seam between respawnSlot
// and recycleSlot; it is enforced structurally by the same-critical-section
// code and this end-state assertion together. The old code also reached this
// same terminal state, so this test does not red-check — it locks the invariant
// against regressions (e.g. moving the dead-mark back out of the atomic section).
func TestPool_WorkerRecycleFailure_AtomicDeadNotRespawning(t *testing.T) {
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}}
	ff := &stepFactory{steps: []stepResult{
		{client: oldClient},                        // warmup
		{err: errors.New("recycle spawn: no fds")}, // background recycle respawn FAILS
	}}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        ff,
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}
	runOneRequest(t, p) // turns -> 2 -> background recycle respawn fails

	if !pollUntil(time.Second, func() bool {
		alive, ok := p.SlotAlive("slot-0")
		return ok && !alive
	}) {
		t.Fatal("slot did not become dead after failed recycle respawn")
	}
	respawning, ok := p.SlotRespawning("slot-0")
	if !ok {
		t.Fatal("slot-0 not found")
	}
	if respawning {
		t.Fatal("slot still respawning after failed recycle; want (dead=true, respawning=false) — atomic transition regressed")
	}
}

// TestPool_RespawnCloseRace_ClosesReplacement is the round-4 review guard: a
// SUCCESSFUL lazy respawn that finishes spawning AFTER Pool.Close ran to
// completion must close the fresh replacement client itself (its swap-under-mu
// sees p.closed and aborts) instead of leaking the fresh kiro-cli process.
// Lazy respawns are NOT recycleWG-tracked, so Close does not wait for the
// parked Spawn — the only thing that reaps the replacement is respawnSlot's own
// p.closed branch at the step-4 swap point.
//
// Determinism (no sleeps as sync): the gatedRecycleFactory dispenses the warmup
// client immediately, then blocks the SECOND Spawn (the lazy respawn) and
// signals spawnEntered from inside it — so receiving spawnEntered places us
// deterministically with respawnSlot parked in Spawn (the OLD client already
// closed) while Close runs. The gate is released only after Close returns.
//
// Red-check (production reverted): without the p.closed guard in respawnSlot's
// step-4 critical section, the swap installs newClient after closeAll's
// snapshot, so nothing closes it — newClient.closeCallCount stays 0 and this
// test fails on the closeCalls assertion.
func TestPool_RespawnCloseRace_ClosesReplacement(t *testing.T) {
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1001}
	newClient := &fakeClient{pid: 1002}
	gf := &gatedRecycleFactory{
		clients:      []pool.PoolClient{oldClient, newClient},
		spawnEntered: make(chan struct{}),
		gate:         make(chan struct{}),
	}
	p := pool.New(pool.Config{
		Logger: testutil.Logger(t),
		Size:   1,
		// MaxWorkerTurns unset (0): no recycling — exercise the LAZY respawn path.
		Factory: gf,
	})
	p.SetCatalogRetryForTesting(nil) // exactly one warmup catalog probe, no backoff
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	// Kill the warmup worker so the next acquire takes the lazy respawn path.
	oldClient.fireDone()
	if !pollUntil(time.Second, func() bool { return p.Stats().Alive == 0 }) {
		t.Fatal("slot did not flip dead after Done() fire")
	}

	// NewSession acquires the dead slot and enters the synchronous lazy respawn;
	// respawnSlot closes the OLD client (step 1) then parks in the gated Spawn.
	type result struct {
		sid string
		err error
	}
	resC := make(chan result, 1)
	go func() {
		sid, err := p.NewSession(context.Background(), "")
		resC <- result{sid, err}
	}()
	<-gf.spawnEntered // respawnSlot is parked inside the replacement Spawn

	// Close runs to completion while the respawn's Spawn is parked. Lazy
	// respawns are not recycleWG-tracked, so Close must NOT block on it.
	closeDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close(): %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish while the lazy respawn Spawn was parked (lazy respawns must not be waited on)")
	}

	// Release the gate: Spawn returns newClient → Initialize → the step-4 swap
	// observes p.closed and must close the fresh client rather than leak it.
	close(gf.gate)

	res := <-resC
	if res.err == nil {
		t.Fatalf("NewSession: want a shutdown error, got sid=%q nil err", res.sid)
	}
	if !strings.Contains(res.err.Error(), "closed") {
		t.Fatalf("NewSession error = %q; want a pool-closed shutdown error", res.err.Error())
	}
	if res.sid != "" {
		t.Fatalf("NewSession sid = %q; want empty on the shutdown race", res.sid)
	}

	// The replacement client must have been closed exactly once by respawnSlot's
	// p.closed branch — closeAll never saw it (it was never swapped into p.all).
	if !pollUntil(time.Second, func() bool { return newClient.closeCallCount() == 1 }) {
		t.Fatalf("replacement client closeCalls = %d; want 1 (respawnSlot closed it on the shutdown-race branch — otherwise the fresh kiro-cli process leaks past Close)", newClient.closeCallCount())
	}

	// No slot was requeued — the dead-slot branch saw p.closed and dropped.
	if s, ok := p.WaitForSlotRelease(100 * time.Millisecond); ok {
		t.Fatalf("slot %q was requeued after Close; want no requeue on the shutdown race (closeAll owns cleanup)", s.Label)
	}
}
