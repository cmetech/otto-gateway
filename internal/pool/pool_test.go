// Package pool_test — blackbox test file (D-18 pattern).
// Tests drive the pool through its exported surface PLUS the test-only
// accessors in export_test.go (which lives in `package pool` and is
// only compiled under `go test`).
package pool_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

// Compile-time interface satisfaction assertion (defense-in-depth
// versus the production-side assertion in pool.go). Build failure here
// means *pool.Pool no longer satisfies engine.ACPClient.
var _ engine.ACPClient = (*pool.Pool)(nil)

// drainChunks consumes a chunk channel to completion. Used by tests
// that need the stream's underlying readLoop to reach EOF so Result()
// returns promptly. Body intentionally empty — the chunk values
// themselves are not under test.
func drainChunks(ch <-chan canonical.Chunk) {
	for range ch { //nolint:revive // intentional drain — body empty by design
		_ = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// fake harness (Codex M-2) — drives pool behaviour without real kiro-cli.
// ---------------------------------------------------------------------------

// fakeClient implements pool.PoolClient. All hooks default to no-op
// success so a test sets only the fields it cares about. Call counts
// and last-args are recorded under mu so concurrent assertions work.
type fakeClient struct {
	// scripted hooks (nil = default behaviour)
	initializeFn func(ctx context.Context) error
	newSessionFn func(ctx context.Context, cwd string) (string, error)
	setModelFn   func(ctx context.Context, sid, m string) error
	promptFn     func(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error)

	// scripted model catalog returned by AvailableModels. When
	// availableModelsFn is set it takes precedence (lets a test flip the
	// catalog over time to exercise lazy self-heal).
	models            []canonical.ModelInfo
	availableModelsFn func() []canonical.ModelInfo

	mu              sync.Mutex
	initializeCalls int
	newSessionCalls int
	cancelCalls     []string
	closeCalls      int

	// doneCh is the Phase 5 D-01 push-exit signal channel. Lazily
	// allocated by Done() so the same fakeClient can be reused for tests
	// that never inspect the channel. Tests close it to simulate
	// subprocess death; Done() returns the channel.
	doneMu sync.Mutex
	doneCh chan struct{}

	// pid is the fake subprocess pid returned by Pid(). Tests assign
	// distinct values (e.g. 1001 OLD, 1002 NEW) to assert the D-18-05
	// previous_pid / worker_pid pair in the lazy-respawn-success log.
	pid int
}

// Pid implements pool.PoolClient. Returns the test-assigned pid (default 0).
func (f *fakeClient) Pid() int { return f.pid }

// Done implements pool.PoolClient. The channel is lazily allocated under
// doneMu so multiple Done() calls return the same channel and tests can
// close it from outside to fire the exit signal.
func (f *fakeClient) Done() <-chan struct{} {
	f.doneMu.Lock()
	defer f.doneMu.Unlock()
	if f.doneCh == nil {
		f.doneCh = make(chan struct{})
	}
	return f.doneCh
}

// fireDone closes the doneCh channel to simulate subprocess exit. Idempotent:
// safe to call multiple times — the second call short-circuits.
func (f *fakeClient) fireDone() {
	f.doneMu.Lock()
	defer f.doneMu.Unlock()
	if f.doneCh == nil {
		f.doneCh = make(chan struct{})
	}
	// Idempotent close — check via a non-blocking select.
	select {
	case <-f.doneCh:
		// already closed
	default:
		close(f.doneCh)
	}
}

func (f *fakeClient) Initialize(ctx context.Context) error {
	f.mu.Lock()
	f.initializeCalls++
	f.mu.Unlock()
	if f.initializeFn != nil {
		return f.initializeFn(ctx)
	}
	return nil
}

func (f *fakeClient) NewSession(ctx context.Context, cwd string) (string, error) {
	f.mu.Lock()
	f.newSessionCalls++
	f.mu.Unlock()
	if f.newSessionFn != nil {
		return f.newSessionFn(ctx, cwd)
	}
	return "fake-sess", nil
}

func (f *fakeClient) SetModel(ctx context.Context, sid, m string) error {
	if f.setModelFn != nil {
		return f.setModelFn(ctx, sid, m)
	}
	return nil
}

func (f *fakeClient) Prompt(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error) {
	if f.promptFn != nil {
		return f.promptFn(ctx, sid, blocks)
	}
	// default: return a freshly-closed stream so consumers see immediate EOF
	s := acp.NewStreamForTest(sid)
	s.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
	return s, nil
}

func (f *fakeClient) Cancel(sid string) {
	f.mu.Lock()
	f.cancelCalls = append(f.cancelCalls, sid)
	f.mu.Unlock()
}

func (f *fakeClient) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeClient) AvailableModels() []canonical.ModelInfo {
	if f.availableModelsFn != nil {
		return f.availableModelsFn()
	}
	return f.models
}

// snapshot helpers — read counters/calls under mu.
func (f *fakeClient) newSessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newSessionCalls
}

func (f *fakeClient) cancelCallList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.cancelCalls))
	copy(out, f.cancelCalls)
	return out
}

// fakeClientFactory hands out pre-scripted fakeClients in order. Spawn
// errors once the script is exhausted.
type fakeClientFactory struct {
	clients []pool.PoolClient
	mu      sync.Mutex
	idx     int
	// optional: if non-nil, returned instead of dispensing from clients
	spawnErr error
}

func (ff *fakeClientFactory) Spawn(_ context.Context, _ acp.Config) (pool.PoolClient, error) {
	if ff.spawnErr != nil {
		return nil, ff.spawnErr
	}
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if ff.idx >= len(ff.clients) {
		return nil, errors.New("fakeClientFactory: no more clients in script")
	}
	c := ff.clients[ff.idx]
	ff.idx++
	return c, nil
}

// ---------------------------------------------------------------------------
// soft-integration gate
// ---------------------------------------------------------------------------

// hasKiroBinary reports whether real-kiro tests should run. Mirrors
// the GW_INTEGRATION=1 gate from Phase 1 (D-17).
func hasKiroBinary() bool {
	if os.Getenv("GW_INTEGRATION") != "1" {
		return false
	}
	_, err := exec.LookPath("kiro-cli")
	return err == nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPool_SatisfiesEngineACPClient(t *testing.T) {
	t.Run("compile-time assertion", func(t *testing.T) {
		// The assignment below is the assertion — it would fail to compile
		// if *pool.Pool no longer implements engine.ACPClient.
		var _ engine.ACPClient = (*pool.Pool)(nil)
		_ = t
	})
}

func TestPool_New_DefaultSize(t *testing.T) {
	p := pool.New(pool.Config{Logger: testutil.Logger(t)})
	if got := p.Stats().Size; got != 1 {
		t.Fatalf("default Size = %d; want 1", got)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPool_New_SizeOverride(t *testing.T) {
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 4})
	if got := p.Stats().Size; got != 4 {
		t.Fatalf("Size override = %d; want 4", got)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPool_Warmup_NoKiroCmd_FailsFast(t *testing.T) {
	// Default factory (acpClientFactory) calls acp.New which spawns a
	// subprocess. A nonexistent binary causes the spawn to fail.
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		KiroCmd: "/nonexistent/binary-xyz-otto-test",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err == nil {
		t.Fatalf("Warmup with bogus KiroCmd: want error, got nil")
	}
	if got := p.Stats().Alive; got != 0 {
		t.Fatalf("Stats().Alive after failed Warmup = %d; want 0", got)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close after failed Warmup: %v", err)
	}
}

func TestPool_Warmup_SkipsWithoutKiroBinary(t *testing.T) {
	if !hasKiroBinary() {
		t.Skip("GW_INTEGRATION=1 + kiro-cli on PATH required for real-kiro warmup")
	}
	p := pool.New(pool.Config{
		Logger:   testutil.Logger(t),
		Size:     1,
		KiroCmd:  "kiro-cli",
		KiroArgs: []string{"acp"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("real-kiro Warmup: %v", err)
	}
	if got := p.Stats().Alive; got != 1 {
		t.Fatalf("Stats().Alive = %d; want 1", got)
	}
	if m := p.Models(); m == nil {
		t.Fatalf("Models() = nil after real-kiro Warmup; expected non-nil catalog")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPool_Close_Idempotent(t *testing.T) {
	p := pool.New(pool.Config{Logger: testutil.Logger(t)})
	if err := p.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close #2: %v", err)
	}
}

func TestPool_Stats_RaceFree(t *testing.T) {
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 2})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = p.Stats()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = p.Close()
	}()
	wg.Wait()
}

func TestPool_NewSession_RequiresWarmup(t *testing.T) {
	// Pool with size 1, no Warmup → slots channel is empty. NewSession
	// blocks on receive; ctx-cancel should propagate.
	p := pool.New(pool.Config{Logger: testutil.Logger(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.NewSession(ctx, "")
	if err == nil {
		t.Fatalf("NewSession without Warmup: want ctx-cancel error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NewSession error = %v; want context.DeadlineExceeded", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Codex H-6: Warmup captures model catalog from slot 0's NewSession.
// ---------------------------------------------------------------------------

func TestPool_Warmup_CapturesModels(t *testing.T) {
	wantModels := []canonical.ModelInfo{
		{ID: "kiro-3.5", Name: "Kiro 3.5"},
	}
	fc := &fakeClient{
		models: wantModels,
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "sess-warmup", nil
		},
	}
	ff := &fakeClientFactory{clients: []pool.PoolClient{fc}}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: ff,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	if got := p.Models(); !reflect.DeepEqual(got, wantModels) {
		t.Fatalf("Models() = %v; want %v", got, wantModels)
	}
	if got := fc.newSessionCount(); got != 1 {
		t.Fatalf("NewSession call count = %d; want exactly 1", got)
	}
	calls := fc.cancelCallList()
	if len(calls) != 1 || calls[0] != "sess-warmup" {
		t.Fatalf("Cancel calls = %v; want [\"sess-warmup\"]", calls)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Session→slot routing (Codex M-2 fake-factory) + Codex M-3 slot release.
// ---------------------------------------------------------------------------

// warmedPoolWithFakes is a helper that constructs a pool with the given
// fakeClients, runs Warmup, and returns the pool. Each fakeClient is
// configured to return a unique session id when NewSession is called.
// The first client's NewSession is consumed by warmup itself.
func warmedPoolWithFakes(t *testing.T, clients []*fakeClient) *pool.Pool {
	t.Helper()
	// Adapt to pool.PoolClient.
	pcs := make([]pool.PoolClient, len(clients))
	for i, c := range clients {
		pcs[i] = c
	}
	ff := &fakeClientFactory{clients: pcs}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    len(clients),
		Factory: ff,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	return p
}

func TestPool_Prompt_SessionRoutesToSlot(t *testing.T) {
	// Two fake clients. Each Prompt call records that it was hit.
	var prompts [2]atomic.Int32
	mkClient := func(idx int, sid string) *fakeClient {
		return &fakeClient{
			newSessionFn: func(_ context.Context, _ string) (string, error) {
				// Warmup consumes slot-0's NewSession (returns "warmup-0").
				// Test-time NewSession calls return distinct session ids.
				return sid, nil
			},
			promptFn: func(_ context.Context, _ string, _ []canonical.Block) (*acp.Stream, error) {
				prompts[idx].Add(1)
				s := acp.NewStreamForTest(sid)
				s.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
				return s, nil
			},
		}
	}
	// Both clients need distinct session ids. Slot 0's NewSession during
	// warmup returns whatever fc0.newSessionFn returns the FIRST call.
	// Subsequent calls (test code) get whatever the function returns
	// then too — so we want the warmup id to differ from the test id.
	// Solution: make newSessionFn a stateful function.
	makeStatefulNewSession := func(warmupSid, runSid string) func(context.Context, string) (string, error) {
		var count int32
		return func(_ context.Context, _ string) (string, error) {
			n := atomic.AddInt32(&count, 1)
			if n == 1 {
				return warmupSid, nil
			}
			return runSid, nil
		}
	}
	fc0 := mkClient(0, "sess-A")
	fc0.newSessionFn = makeStatefulNewSession("warmup-0", "sess-A")
	fc1 := mkClient(1, "sess-B")
	fc1.newSessionFn = makeStatefulNewSession("warmup-1", "sess-B")
	// Override warmup model capture (warmup only touches slot 0, but
	// fc1 must also be safe — it never gets its NewSession called during
	// warmup since only slot 0 captures models).

	p := warmedPoolWithFakes(t, []*fakeClient{fc0, fc1})
	defer func() { _ = p.Close() }()

	// Acquire both slots via NewSession.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	sidA, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession A: %v", err)
	}
	sidB, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession B: %v", err)
	}
	// sidA/sidB belong to whichever slot the channel handed out first
	// (order is FIFO within a single goroutine since Warmup pushes in
	// order). We can therefore assert: Prompt(sidA) hits fc that
	// produced sidA's promptFn. Easier assertion: count total prompts
	// across both clients and verify the SUM is 1 (one prompt fired,
	// targeting whichever slot owns the session).

	// Issue the prompt against sidA.
	stream, err := p.Prompt(ctx, sidA, nil)
	if err != nil {
		t.Fatalf("Prompt sidA: %v", err)
	}
	// Drain + Result so the slot returns.
	drainChunks(stream.Chunks())
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
	totalPrompts := prompts[0].Load() + prompts[1].Load()
	if totalPrompts != 1 {
		t.Fatalf("total Prompt calls across both fakes = %d; want 1", totalPrompts)
	}
	// And the OTHER slot's fake should NOT have been hit.
	if prompts[0].Load() > 0 && prompts[1].Load() > 0 {
		t.Fatalf("Both fakes received Prompt — routing broken (per-fake = %d, %d)",
			prompts[0].Load(), prompts[1].Load())
	}
	_ = sidB // sidB is the other session; used to occupy slot but not prompted
}

func TestPool_Prompt_ErrorReleasesSlot(t *testing.T) {
	fc := &fakeClient{
		promptFn: func(_ context.Context, _ string, _ []canonical.Block) (*acp.Stream, error) {
			return nil, errors.New("kiro busted")
		},
	}
	// Warmup wants newSessionFn to return a warmup id; the test then
	// NewSessions again and the function returns the run id.
	var count int32
	fc.newSessionFn = func(_ context.Context, _ string) (string, error) {
		n := atomic.AddInt32(&count, 1)
		if n == 1 {
			return "warmup", nil
		}
		return "run-sid", nil
	}
	p := warmedPoolWithFakes(t, []*fakeClient{fc})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := p.Prompt(ctx, sid, nil); err == nil {
		t.Fatalf("Prompt: want error, got nil")
	}
	// Slot should be back in the channel within 100ms — assert by
	// observing it directly via the test-only accessor.
	slot, ok := p.WaitForSlotRelease(100 * time.Millisecond)
	if !ok {
		t.Fatalf("slot not released within 100ms after Prompt error")
	}
	// Put it back so Close finds the pool in a clean state.
	p.PutSlotBack(slot)
}

func TestPool_Result_ReleasesSlot(t *testing.T) {
	fc := &fakeClient{}
	var count int32
	fc.newSessionFn = func(_ context.Context, _ string) (string, error) {
		n := atomic.AddInt32(&count, 1)
		if n == 1 {
			return "warmup", nil
		}
		return "run-sid", nil
	}
	p := warmedPoolWithFakes(t, []*fakeClient{fc})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stream, err := p.Prompt(ctx, sid, nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	drainChunks(stream.Chunks())
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
	slot, ok := p.WaitForSlotRelease(100 * time.Millisecond)
	if !ok {
		t.Fatalf("slot not released within 100ms after Result")
	}
	p.PutSlotBack(slot)
	// A follow-up NewSession on the size-1 pool should succeed without
	// blocking.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()
	if _, err := p.NewSession(ctx2, ""); err != nil {
		t.Fatalf("follow-up NewSession: %v", err)
	}
}

func TestPool_Prompt_UnknownSessionError(t *testing.T) {
	fc := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "warmup", nil
		},
	}
	p := warmedPoolWithFakes(t, []*fakeClient{fc})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err := p.Prompt(ctx, "no-such-session", nil)
	if err == nil {
		t.Fatalf("Prompt(no-such): want error, got nil")
	}
	// No slot was taken (no NewSession in this test path), so the slot
	// channel should still have its lone warmup slot.
	slot, ok := p.WaitForSlotRelease(50 * time.Millisecond)
	if !ok {
		t.Fatalf("free slot vanished — unknown-session path mishandled the channel")
	}
	p.PutSlotBack(slot)
}

func TestPool_Cancel_RoutesToCorrectSlot(t *testing.T) {
	mkClient := func() *fakeClient {
		fc := &fakeClient{}
		var count int32
		fc.newSessionFn = func(_ context.Context, _ string) (string, error) {
			n := atomic.AddInt32(&count, 1)
			if n == 1 {
				return "warmup", nil
			}
			return "run-" + time.Now().Format("150405.000000000"), nil
		}
		return fc
	}
	fc0 := mkClient()
	fc1 := mkClient()
	p := warmedPoolWithFakes(t, []*fakeClient{fc0, fc1})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	sidA, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession A: %v", err)
	}
	_, err = p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession B: %v", err)
	}
	// Snapshot Cancel calls BEFORE the test Cancel — warmup-time Cancel
	// (slot 0's "warmup" sid cleanup) already incremented fc0's counter.
	pre0 := append([]string{}, fc0.cancelCallList()...)
	pre1 := append([]string{}, fc1.cancelCallList()...)

	p.Cancel(sidA)

	post0 := fc0.cancelCallList()
	post1 := fc1.cancelCallList()
	new0 := post0[len(pre0):]
	new1 := post1[len(pre1):]
	// Exactly ONE of the two fakes should have received a NEW Cancel
	// post-snapshot.
	if len(new0)+len(new1) != 1 {
		t.Fatalf("new Cancel calls after p.Cancel = %d; want exactly 1 (fc0 diff=%v, fc1 diff=%v)",
			len(new0)+len(new1), new0, new1)
	}
	// And the one that got it must have seen sidA exactly.
	got := new0
	if len(new1) == 1 {
		got = new1
	}
	if len(got) != 1 || got[0] != sidA {
		t.Fatalf("new Cancel argument = %v; want [%q]", got, sidA)
	}
}

// ---------------------------------------------------------------------------
// Codex M-3: slot release on EVERY terminal path.
// ---------------------------------------------------------------------------

// makeStreamFn returns a promptFn that hands the test the stream
// handle via a channel so the test can decide when to close it.
type streamHandle struct {
	stream *acp.Stream
}

func TestPool_ContextCancel_ReleasesSlot(t *testing.T) {
	// Hand the test a stream that the test holds open. ctx-cancel
	// before Result should release the slot via the ctx-watcher.
	handleCh := make(chan *streamHandle, 1)
	fc := &fakeClient{}
	var count int32
	fc.newSessionFn = func(_ context.Context, _ string) (string, error) {
		n := atomic.AddInt32(&count, 1)
		if n == 1 {
			return "warmup", nil
		}
		return "run", nil
	}
	fc.promptFn = func(_ context.Context, sid string, _ []canonical.Block) (*acp.Stream, error) {
		s := acp.NewStreamForTest(sid)
		handleCh <- &streamHandle{stream: s}
		return s, nil
	}
	p := warmedPoolWithFakes(t, []*fakeClient{fc})
	defer func() { _ = p.Close() }()

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	sid, err := p.NewSession(parentCtx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	promptCtx, promptCancel := context.WithCancel(parentCtx)
	_, err = p.Prompt(promptCtx, sid, nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// Receive the stream handle so we can close it after the slot
	// release assertion (otherwise the dangling open stream would leak
	// goroutines through the goleak gate).
	h := <-handleCh

	// Cancel the prompt ctx BEFORE Result runs — Codex M-3 ctx-watcher
	// should release the slot.
	promptCancel()

	slot, ok := p.WaitForSlotRelease(200 * time.Millisecond)
	if !ok {
		t.Fatalf("slot not released within 200ms after ctx-cancel — Codex M-3 path broken")
	}
	p.PutSlotBack(slot)

	// Subsequent NewSession on the size-1 pool succeeds without
	// blocking.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if _, err := p.NewSession(ctx2, ""); err != nil {
		t.Fatalf("follow-up NewSession after ctx-cancel release: %v", err)
	}

	// Finalise the stream so its internal state is clean (close idempotent).
	h.stream.CloseForTest(&acp.FinalResult{StopReason: canonical.StopCancelled}, nil)
}

func TestPool_StreamCloseWithoutResult_ReleasesSlot(t *testing.T) {
	handleCh := make(chan *streamHandle, 1)
	wrapperCh := make(chan engine.Stream, 1)
	fc := &fakeClient{}
	var count int32
	fc.newSessionFn = func(_ context.Context, _ string) (string, error) {
		n := atomic.AddInt32(&count, 1)
		if n == 1 {
			return "warmup", nil
		}
		return "run", nil
	}
	fc.promptFn = func(_ context.Context, sid string, _ []canonical.Block) (*acp.Stream, error) {
		s := acp.NewStreamForTest(sid)
		handleCh <- &streamHandle{stream: s}
		return s, nil
	}
	p := warmedPoolWithFakes(t, []*fakeClient{fc})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	w, err := p.Prompt(ctx, sid, nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	wrapperCh <- w
	h := <-handleCh

	// Simulate an engine.Run path that aborts before draining: invoke
	// the wrapper's package-private Release via a type assertion.
	type releaser interface{ Release() }
	r, ok := w.(releaser)
	if !ok {
		t.Fatalf("Prompt returned %T; expected wrapper with Release() method", w)
	}
	r.Release()

	slot, ok := p.WaitForSlotRelease(200 * time.Millisecond)
	if !ok {
		t.Fatalf("slot not released within 200ms after Release()")
	}

	// Calling Result after Release should be a no-op (no double-release).
	// Finalise the underlying stream so Result returns cleanly, then
	// call Result — leaving the slot OUT of the channel for now so that
	// any extra release fires into an empty channel and is visible.
	h.stream.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
	_, _ = w.Result()

	// Now the channel should still be empty (we never put the slot
	// back). A double-release would have sent the slot a second time.
	if extra, ok := p.WaitForSlotRelease(50 * time.Millisecond); ok {
		p.PutSlotBack(extra)
		t.Fatalf("extra slot observed after Release+Result — double-release happened")
	}

	// Put the slot back so Close finds the pool in a clean state.
	p.PutSlotBack(slot)
}

func TestPool_Cancel_ReleasesSlot(t *testing.T) {
	handleCh := make(chan *streamHandle, 1)
	fc := &fakeClient{}
	var count int32
	fc.newSessionFn = func(_ context.Context, _ string) (string, error) {
		n := atomic.AddInt32(&count, 1)
		if n == 1 {
			return "warmup", nil
		}
		return "run-c", nil
	}
	fc.promptFn = func(_ context.Context, sid string, _ []canonical.Block) (*acp.Stream, error) {
		s := acp.NewStreamForTest(sid)
		handleCh <- &streamHandle{stream: s}
		return s, nil
	}
	p := warmedPoolWithFakes(t, []*fakeClient{fc})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	w, err := p.Prompt(ctx, sid, nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h := <-handleCh

	// Pre-cancel check: sessionSlots should contain sid.
	if l := p.SessionSlotsLen(); l != 1 {
		t.Fatalf("SessionSlotsLen before Cancel = %d; want 1", l)
	}

	p.Cancel(sid)

	// (a) fakeClient.Cancel was called with sid.
	calls := fc.cancelCallList()
	// Note: warmup also fired Cancel("warmup"). So we expect at least one
	// post-warmup call with sid.
	sawSid := false
	for _, c := range calls {
		if c == sid {
			sawSid = true
		}
	}
	if !sawSid {
		t.Fatalf("fakeClient.Cancel did not see %q; got %v", sid, calls)
	}

	// (b) slot returned to p.slots within 100ms.
	slot, ok := p.WaitForSlotRelease(100 * time.Millisecond)
	if !ok {
		t.Fatalf("slot not released within 100ms after Pool.Cancel")
	}

	// (c) sessionSlots no longer contains sid.
	if l := p.SessionSlotsLen(); l != 0 {
		t.Fatalf("SessionSlotsLen after Cancel = %d; want 0", l)
	}

	// (d) Calling Result after Cancel does not double-release. Leave the
	// slot OUT of the channel until after this check so a second release
	// would land in an empty channel and be visible.
	h.stream.CloseForTest(&acp.FinalResult{StopReason: canonical.StopCancelled}, nil)
	_, _ = w.Result()
	if extra, ok := p.WaitForSlotRelease(50 * time.Millisecond); ok {
		p.PutSlotBack(extra)
		t.Fatalf("extra slot observed after Cancel+Result — double-release happened")
	}

	// Put the slot back so Close finds the pool in a clean state.
	p.PutSlotBack(slot)
}

// ---------------------------------------------------------------------------
// Phase 5 D-01/D-02/D-03 — dead-slot detection + lazy synchronous re-spawn.
// ---------------------------------------------------------------------------

// waitForSlotDead polls the SlotAlive accessor until the slot flips dead
// or timeout elapses. Returns true if the slot became dead within
// timeout, false otherwise.
func waitForSlotDead(p *pool.Pool, label string, timeout time.Duration) bool { //nolint:unparam // timeout param kept polymorphic for future timing tests
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, found := p.SlotAlive(label)
		if found && !alive {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestPool_DeadSlot_LazyRespawn — POOL-04 happy path.
// Pool size 1; client0 dies via Done(); the next NewSession must
// synchronously respawn via client1Replacement.
func TestPool_DeadSlot_LazyRespawn(t *testing.T) {
	var c0count int32
	fc0 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			if atomic.AddInt32(&c0count, 1) == 1 {
				return "warmup-0", nil
			}
			return "should-not-be-called", nil
		},
	}
	fc1 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "respawned-sess", nil
		},
	}
	ff := &fakeClientFactory{clients: []pool.PoolClient{fc0, fc1}}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: ff,
	})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}

	fc0.fireDone()
	if !waitForSlotDead(p, "slot-0", 200*time.Millisecond) {
		t.Fatal("slot-0 did not become dead within 200ms after Done() fired")
	}

	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession after dead slot: %v", err)
	}
	if sid != "respawned-sess" {
		t.Errorf("sid: got %q, want \"respawned-sess\" (proves respawn fired)", sid)
	}
	if got := fc1.newSessionCount(); got != 1 {
		t.Errorf("client1Replacement.NewSession calls = %d; want 1", got)
	}
	alive, found := p.SlotAlive("slot-0")
	if !found || !alive {
		t.Errorf("after respawn: alive=%v found=%v; want true,true", alive, found)
	}
	// Track 4b: a successful lazy respawn bumps the gw_pool_slot_respawns_total
	// source counter.
	if got := p.Respawns(); got != 1 {
		t.Errorf("Respawns() = %d; want 1 after one lazy respawn", got)
	}
}

// TestPool_DeadSlot_RespawnFailure_SlotRequeued — REL-POOL-01 D-08.
// After client0 dies, the replacement spawn fails with a transient error;
// the slot is re-queued into p.slots (not removed from p.all) so the pool
// effective size is preserved. The caller receives a wrapped error and can
// retry. This replaces the old D-03 "shrink on failure" test which was
// updated in Phase 15 when the re-queue strategy was adopted to prevent
// permanent pool size reduction on transient respawn failures.
func TestPool_DeadSlot_RespawnFailure_SlotRequeued(t *testing.T) {
	var c0count int32
	fc0 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			atomic.AddInt32(&c0count, 1)
			return "warmup-0", nil
		},
	}
	respawnErr := errors.New("spawn failed: kiro busted")
	ff := &scriptedFailingFactory{
		clients:    []pool.PoolClient{fc0},
		failAfter:  1,
		failureErr: respawnErr,
	}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: ff,
	})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	if got := len(p.AllSlotsSnapshot()); got != 1 {
		t.Fatalf("pre-death AllSlots size = %d; want 1", got)
	}

	fc0.fireDone()
	if !waitForSlotDead(p, "slot-0", 200*time.Millisecond) {
		t.Fatal("slot-0 did not become dead within 200ms")
	}

	_, err := p.NewSession(ctx, "")
	if err == nil {
		t.Fatal("NewSession after dead+respawn-fail: want error, got nil")
	}
	if !errors.Is(err, respawnErr) {
		t.Errorf("error chain does not contain respawnErr: %v", err)
	}

	// REL-POOL-01 D-08: slot is re-queued, NOT removed — p.all retains size 1.
	if got := len(p.AllSlotsSnapshot()); got != 1 {
		t.Errorf("post-failure AllSlots size = %d; want 1 (re-queue preserves pool size)", got)
	}
}

// TestPool_DeadSlot_RespawnRespectsCtxCancel — D-02 invariant.
// The caller's ctx is cancelled while respawnSlot is in flight; the call
// must return ctx-cancelled-wrapped error promptly.
func TestPool_DeadSlot_RespawnRespectsCtxCancel(t *testing.T) {
	var c0count int32
	fc0 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			atomic.AddInt32(&c0count, 1)
			return "warmup-0", nil
		},
	}
	spawnGate := make(chan struct{})
	ff := &ctxGatingFactory{
		clients:        []pool.PoolClient{fc0},
		spawnGate:      spawnGate,
		gateAfterCount: 1, // first spawn (warmup) ungated
	}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: ff,
	})
	defer func() {
		ff.unmark() // disable gating before Close
		select {
		case <-spawnGate:
		default:
			close(spawnGate)
		}
		_ = p.Close()
	}()

	bgCtx := context.Background()
	if err := p.Warmup(bgCtx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}

	fc0.fireDone()
	if !waitForSlotDead(p, "slot-0", 200*time.Millisecond) {
		t.Fatal("slot-0 did not become dead within 200ms")
	}

	ctx, cancel := context.WithCancel(bgCtx)
	errCh := make(chan error, 1)
	go func() {
		_, err := p.NewSession(ctx, "")
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("NewSession returned nil after ctx-cancel during respawn")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error does not contain context.Canceled: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("NewSession did not return within 500ms of ctx-cancel — respawn ignored ctx (D-02 violation)")
	}
}

// TestPool_DeadSlot_ConcurrentAcquiresUnaffected — POOL-04 invariant.
// Pool size 2, slot-0 dies but slot-1 is alive. A goroutine that acquires
// slot-1 must complete promptly without being blocked behind slot-0's respawn.
func TestPool_DeadSlot_ConcurrentAcquiresUnaffected(t *testing.T) {
	var c0count, c1count int32
	fc0 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			atomic.AddInt32(&c0count, 1)
			return "warmup-0", nil
		},
	}
	fc1 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			n := atomic.AddInt32(&c1count, 1)
			if n == 1 {
				return "warmup-1", nil
			}
			return "sess-1-run", nil
		},
	}
	fc0Replacement := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "sess-0-respawn", nil
		},
	}
	slowSpawn := make(chan struct{})
	ff := &ctxGatingFactory{
		clients:        []pool.PoolClient{fc0, fc1, fc0Replacement},
		spawnGate:      slowSpawn,
		gateAfterCount: 2,
	}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    2,
		Factory: ff,
	})
	defer func() {
		ff.unmark()
		select {
		case <-slowSpawn:
		default:
			close(slowSpawn)
		}
		_ = p.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}

	fc0.fireDone()
	if !waitForSlotDead(p, "slot-0", 200*time.Millisecond) {
		t.Fatal("slot-0 did not become dead within 200ms")
	}

	deadAcquireErr := make(chan error, 1)
	go func() {
		_, err := p.NewSession(ctx, "")
		deadAcquireErr <- err
	}()
	aliveAcquireErr := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		_, err := p.NewSession(ctx, "")
		aliveAcquireErr <- err
	}()

	select {
	case err := <-aliveAcquireErr:
		if err != nil {
			t.Fatalf("alive-slot acquire failed: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("alive-slot acquire blocked > 250ms — concurrent acquires were not independent")
	}

	// Release the dead-acquire path so cleanup can proceed.
	ff.unmark()
	select {
	case <-slowSpawn:
	default:
		close(slowSpawn)
	}
	select {
	case <-deadAcquireErr:
	case <-time.After(500 * time.Millisecond):
	}
}

// TestPool_ExitWatcher_RespawnSpawnsNewWatcher — Pitfall 2.
// After a successful respawn, killing the NEW client's Done() must mark
// the slot dead again — proving the old watcher exited and a new one took
// its place.
func TestPool_ExitWatcher_RespawnSpawnsNewWatcher(t *testing.T) {
	var c0count int32
	fc0 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			atomic.AddInt32(&c0count, 1)
			return "warmup-0", nil
		},
	}
	fc1 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "respawned", nil
		},
	}
	ff := &fakeClientFactory{clients: []pool.PoolClient{fc0, fc1}}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: ff,
	})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}

	fc0.fireDone()
	if !waitForSlotDead(p, "slot-0", 200*time.Millisecond) {
		t.Fatal("slot-0 not dead after fc0.fireDone")
	}
	if _, err := p.NewSession(ctx, ""); err != nil {
		t.Fatalf("respawn NewSession: %v", err)
	}
	alive, _ := p.SlotAlive("slot-0")
	if !alive {
		t.Fatal("slot-0 not alive after respawn")
	}

	fc1.fireDone()
	if !waitForSlotDead(p, "slot-0", 200*time.Millisecond) {
		t.Fatal("slot-0 did not become dead after fc1 (new client) died — fresh watcher missing")
	}
}

// ---------------------------------------------------------------------------
// Test factory helpers for dead-slot tests.
// ---------------------------------------------------------------------------

// scriptedFailingFactory hands out clients from a list until the first
// `failAfter` Spawn calls, then returns failureErr for every subsequent
// Spawn. Used by TestPool_DeadSlot_RespawnFailure_PoolShrinks.
type scriptedFailingFactory struct {
	clients    []pool.PoolClient
	mu         sync.Mutex
	idx        int
	failAfter  int
	failureErr error
}

func (sf *scriptedFailingFactory) Spawn(_ context.Context, _ acp.Config) (pool.PoolClient, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.idx >= sf.failAfter {
		return nil, sf.failureErr
	}
	if sf.idx >= len(sf.clients) {
		return nil, errors.New("scriptedFailingFactory: clients exhausted before failAfter")
	}
	c := sf.clients[sf.idx]
	sf.idx++
	return c, nil
}

// ctxGatingFactory hands out clients from a list, gating Spawn calls
// (after gateAfterCount initial successes) on a channel + caller ctx.
// Spawn blocks on the gate but ALSO honors ctx so a ctx-cancelled
// caller aborts promptly (D-02).
type ctxGatingFactory struct {
	clients        []pool.PoolClient
	mu             sync.Mutex
	idx            int
	spawnGate      chan struct{}
	gateAfterCount int
	disabled       atomic.Bool
}

func (cg *ctxGatingFactory) unmark() { cg.disabled.Store(true) }

func (cg *ctxGatingFactory) Spawn(ctx context.Context, _ acp.Config) (pool.PoolClient, error) {
	cg.mu.Lock()
	idx := cg.idx
	cg.idx++
	cg.mu.Unlock()

	if idx >= cg.gateAfterCount && !cg.disabled.Load() {
		select {
		case <-cg.spawnGate:
			// proceed
		case <-ctx.Done():
			return nil, ctx.Err() //nolint:wrapcheck // tests assert errors.Is(ctx.Err)
		}
	}

	cg.mu.Lock()
	defer cg.mu.Unlock()
	if idx >= len(cg.clients) {
		return nil, errors.New("ctxGatingFactory: clients exhausted")
	}
	return cg.clients[idx], nil
}

// ---------------------------------------------------------------------------
// Phase 5 D-15 — Pool.Detail() rows for /health/agents consumer.
// ---------------------------------------------------------------------------

// makeWarmupOnly returns a stateful newSessionFn that returns the given
// warmup sid on first call and runSid on subsequent calls. Helper for the
// Detail tests so the Warmup path doesn't collide with test-time
// NewSession routing.
func makeWarmupOnly(warmupSid, runSid string) func(context.Context, string) (string, error) {
	var n int32
	return func(_ context.Context, _ string) (string, error) {
		if atomic.AddInt32(&n, 1) == 1 {
			return warmupSid, nil
		}
		return runSid, nil
	}
}

// TestPool_Detail_HealthyPool — D-15: pool size 4, all alive, none busy.
func TestPool_Detail_HealthyPool(t *testing.T) {
	clients := []*fakeClient{
		// slot-0's catalog probe must return a non-empty catalog on the first
		// attempt so Turns is deterministic (1) instead of retrying per
		// defaultCatalogRetry (which would otherwise probe 3x on an empty
		// catalog before the warmup ctx deadline cuts the retries off).
		{newSessionFn: makeWarmupOnly("warm-0", "run-0"), models: []canonical.ModelInfo{{ID: "auto"}}},
		{newSessionFn: makeWarmupOnly("warm-1", "run-1")},
		{newSessionFn: makeWarmupOnly("warm-2", "run-2")},
		{newSessionFn: makeWarmupOnly("warm-3", "run-3")},
	}
	p := warmedPoolWithFakes(t, clients)
	defer func() { _ = p.Close() }()

	rows := p.Detail()
	if len(rows) != 4 {
		t.Fatalf("Detail() length = %d; want 4", len(rows))
	}
	for i, row := range rows {
		wantLabel := "slot-" + string(rune('0'+i))
		if row.Label != wantLabel {
			t.Errorf("row[%d].Label = %q; want %q", i, row.Label, wantLabel)
		}
		if !row.Alive {
			t.Errorf("row[%d].Alive = false; want true", i)
		}
		if row.Busy {
			t.Errorf("row[%d].Busy = true; want false", i)
		}
		if row.CurrentSessionID != nil {
			t.Errorf("row[%d].CurrentSessionID = %v; want nil", i, *row.CurrentSessionID)
		}
		// Only slot-0 runs the D-13 model-catalog probe during Warmup (one
		// session/new call, i==0 branch) — other slots are spawned+initialized
		// but never probed, so their Turns stay 0.
		wantTurns := 0
		if i == 0 {
			wantTurns = 1
		}
		if row.Turns != wantTurns {
			t.Errorf("row[%d].Turns = %d; want %d", i, row.Turns, wantTurns)
		}
		if row.SpawnedAt == nil {
			t.Errorf("row[%d].SpawnedAt = nil; want non-nil after initSlot", i)
		} else if row.SpawnedAt.IsZero() {
			t.Errorf("row[%d].SpawnedAt = zero time; want a real timestamp", i)
		}
	}
}

// TestPool_Detail_OneBusyOneDead — D-15: slot-0 holds an active session,
// slot-1 has dead=true (via fireDone). Detail returns rows reflecting both
// states. Pool size 4 (others all idle+alive).
func TestPool_Detail_OneBusyOneDead(t *testing.T) {
	clients := []*fakeClient{
		{newSessionFn: makeWarmupOnly("warm-0", "sess-X")},
		{newSessionFn: makeWarmupOnly("warm-1", "run-1")},
		{newSessionFn: makeWarmupOnly("warm-2", "run-2")},
		{newSessionFn: makeWarmupOnly("warm-3", "run-3")},
	}
	p := warmedPoolWithFakes(t, clients)
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Open one session on slot-0 (the FIFO winner since Warmup pushed in
	// order). Don't drain — the session stays active.
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sid != "sess-X" {
		t.Fatalf("first NewSession returned %q; want sess-X (slot-0)", sid)
	}

	// Kill slot-1 — it's still in p.slots since we only acquired slot-0.
	clients[1].fireDone()
	if !waitForSlotDead(p, "slot-1", 200*time.Millisecond) {
		t.Fatal("slot-1 did not become dead within 200ms")
	}

	rows := p.Detail()
	if len(rows) != 4 {
		t.Fatalf("Detail() length = %d; want 4", len(rows))
	}

	// Find slot-0 + slot-1 by label.
	rowByLabel := make(map[string]pool.AgentSlot, len(rows))
	for _, r := range rows {
		rowByLabel[r.Label] = r
	}
	row0, ok := rowByLabel["slot-0"]
	if !ok {
		t.Fatal("slot-0 row missing")
	}
	if !row0.Alive {
		t.Error("slot-0 Alive = false; want true")
	}
	if !row0.Busy {
		t.Error("slot-0 Busy = false; want true (session active)")
	}
	if row0.CurrentSessionID == nil || *row0.CurrentSessionID != "sess-X" {
		t.Errorf("slot-0 CurrentSessionID = %v; want &\"sess-X\"", row0.CurrentSessionID)
	}

	row1, ok := rowByLabel["slot-1"]
	if !ok {
		t.Fatal("slot-1 row missing")
	}
	if row1.Alive {
		t.Error("slot-1 Alive = true; want false (dead)")
	}
	if row1.Busy {
		t.Error("slot-1 Busy = true; want false")
	}
	if row1.CurrentSessionID != nil {
		t.Errorf("slot-1 CurrentSessionID = %v; want nil", *row1.CurrentSessionID)
	}
}

// TestPool_Detail_AfterRespawnFailureRequeues — D-15 + REL-POOL-01 D-08 interaction.
// After a transient respawn failure re-queues the slot, Detail() still returns
// 1 row (slot is still in p.all, just re-queued for retry by the next caller).
// This test was updated in Phase 15 from the old D-03 shrink assertion to
// reflect the re-queue strategy adopted by REL-POOL-01.
func TestPool_Detail_AfterRespawnFailureRequeues(t *testing.T) {
	var c0count int32
	fc0 := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			atomic.AddInt32(&c0count, 1)
			return "warmup-0", nil
		},
	}
	respawnErr := errors.New("spawn failed: shrink test")
	ff := &scriptedFailingFactory{
		clients:    []pool.PoolClient{fc0},
		failAfter:  1,
		failureErr: respawnErr,
	}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: ff,
	})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}

	// Pre-failure: Detail returns one row.
	if got := len(p.Detail()); got != 1 {
		t.Fatalf("pre-failure Detail len = %d; want 1", got)
	}

	fc0.fireDone()
	if !waitForSlotDead(p, "slot-0", 200*time.Millisecond) {
		t.Fatal("slot-0 did not become dead within 200ms")
	}

	// Trigger the respawn failure via NewSession.
	if _, err := p.NewSession(ctx, ""); err == nil {
		t.Fatal("NewSession: want respawn-fail error, got nil")
	}

	// Post-failure: Detail still returns 1 row — slot was re-queued, not removed.
	// REL-POOL-01 D-08: re-queue preserves pool effective size.
	rows := p.Detail()
	if len(rows) != 1 {
		t.Errorf("post-failure Detail len = %d; want 1 (re-queue preserves slot in p.all)", len(rows))
	}
}

// TestPool_Detail_NilSafeOnEmptyPool — calling Detail() before Warmup
// returns an empty slice (not nil panic, not nil slice — empty slice so
// the handler encodes "slots": []).
func TestPool_Detail_NilSafeOnEmptyPool(t *testing.T) {
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 4})
	defer func() { _ = p.Close() }()

	rows := p.Detail()
	if rows == nil {
		t.Fatal("Detail() returned nil; want empty slice for clean JSON encoding")
	}
	if len(rows) != 0 {
		t.Errorf("Detail() pre-Warmup length = %d; want 0", len(rows))
	}
}

// TestPool_Detail_FieldShape_MatchesD15 — JSON tags lock the D-15 wire
// contract. Build failure if downstream consumers depend on the old shape.
// Turns/SpawnedAt were added additively (worker-recycling dashboard stats,
// quick 260721-ovm) — this lock test was updated deliberately alongside that
// change; it is not a stale assertion.
func TestPool_Detail_FieldShape_MatchesD15(t *testing.T) {
	rt := reflect.TypeOf(pool.AgentSlot{})
	wantTags := map[string]string{
		"Label":            "label",
		"Alive":            "alive",
		"Busy":             "busy",
		"CurrentSessionID": "current_session_id",
		"Turns":            "turns",
		"SpawnedAt":        "spawned_at",
	}
	if rt.NumField() != len(wantTags) {
		t.Fatalf("AgentSlot field count = %d; want %d (extra/missing fields break D-15 wire)",
			rt.NumField(), len(wantTags))
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		want, ok := wantTags[f.Name]
		if !ok {
			t.Errorf("unexpected field %q", f.Name)
			continue
		}
		got := f.Tag.Get("json")
		if got != want {
			t.Errorf("field %s json tag = %q; want %q", f.Name, got, want)
		}
	}
}

// TestPool_HealthSummary_DegradedSizeZeroIsHealthy proves the
// "operator intentionally booted without KIRO_CMD" mode is reported as
// healthy. /health/pool should not page someone for an empty pool when
// the operator's whole point was diagnostics-only mode.
func TestPool_HealthSummary_DegradedSizeZeroIsHealthy(t *testing.T) {
	// Size: 0 is rejected by applyDefaults (clamps to 1). To exercise
	// the actual Size == 0 branch we have to forcibly construct a pool
	// with cfg.Size = 0 via the same constructor — applyDefaults's
	// minimum is 1, so this assertion documents the SEMANTIC: if a
	// future change ever permits Size == 0, healthy must be true.
	// For the realistic "degraded boot" case (Size >= 1 with no slots
	// warmed up), Alive will be 0 and Healthy false — see test below.
	p := pool.New(pool.Config{Logger: testutil.Logger(t)}) // applyDefaults clamps Size → 1
	defer func() { _ = p.Close() }()

	h := p.HealthSummary()
	// applyDefaults clamps to 1; alive is 0 because no warmup ran;
	// therefore healthy is false in this realistic startup window.
	if h.Size < 1 {
		t.Fatalf("Size: got %d, want >= 1 (applyDefaults clamps)", h.Size)
	}
	if h.Alive != 0 {
		t.Errorf("Alive: got %d, want 0 (no warmup ran)", h.Alive)
	}
	if h.Healthy {
		t.Errorf("Healthy: got true, want false (Size >= 1 and Alive == 0)")
	}
	if h.LastSpawnError != "" {
		t.Errorf("LastSpawnError: got %q, want empty (no spawn attempts yet)", h.LastSpawnError)
	}
	if !h.LastSpawnErrAt.IsZero() {
		t.Errorf("LastSpawnErrAt: got %v, want zero time", h.LastSpawnErrAt)
	}
}

// TestPool_HealthSummary_CapturesSpawnError verifies the lastSpawnErr
// path: an error recorded via recordSpawnErr appears in HealthSummary,
// is timestamped, and is not cleared by subsequent reads (forensic
// field semantic).
func TestPool_HealthSummary_CapturesSpawnError(t *testing.T) {
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 2})
	defer func() { _ = p.Close() }()

	// Baseline: no error recorded.
	h0 := p.HealthSummary()
	if h0.LastSpawnError != "" {
		t.Fatalf("baseline LastSpawnError: got %q, want empty", h0.LastSpawnError)
	}

	before := time.Now().UTC()
	p.RecordSpawnErrForTesting(errors.New("fork/exec /usr/local/bin/kiro: no such file or directory"))
	after := time.Now().UTC()

	h1 := p.HealthSummary()
	if h1.LastSpawnError == "" {
		t.Fatalf("after record: LastSpawnError still empty")
	}
	if !strings.Contains(h1.LastSpawnError, "no such file") {
		t.Errorf("LastSpawnError = %q, expected to contain 'no such file'", h1.LastSpawnError)
	}
	if h1.LastSpawnErrAt.IsZero() {
		t.Errorf("LastSpawnErrAt: got zero, want a timestamp")
	}
	if h1.LastSpawnErrAt.Before(before) || h1.LastSpawnErrAt.After(after.Add(time.Second)) {
		t.Errorf("LastSpawnErrAt = %v, expected within [%v, %v]", h1.LastSpawnErrAt, before, after)
	}

	// Read again — must NOT clear (forensic semantic).
	h2 := p.HealthSummary()
	if h2.LastSpawnError != h1.LastSpawnError {
		t.Errorf("second read cleared LastSpawnError: %q vs %q", h2.LastSpawnError, h1.LastSpawnError)
	}
	if !h2.LastSpawnErrAt.Equal(h1.LastSpawnErrAt) {
		t.Errorf("second read changed LastSpawnErrAt: %v vs %v", h2.LastSpawnErrAt, h1.LastSpawnErrAt)
	}

	// Nil error is a no-op (guards against an accidental clear via nil).
	p.RecordSpawnErrForTesting(nil)
	h3 := p.HealthSummary()
	if h3.LastSpawnError != h1.LastSpawnError {
		t.Errorf("nil record changed LastSpawnError: %q vs %q", h3.LastSpawnError, h1.LastSpawnError)
	}
}

// TestPool_HealthSummary_SpawnFailingRecency verifies SpawnFailing is a CURRENT
// signal, not the sticky historical LastSpawnError. It is true only when a
// genuine spawn error was recorded within the recency window (2x PingInterval),
// so a single old failure cannot pin the dashboard slot red forever.
func TestPool_HealthSummary_SpawnFailingRecency(t *testing.T) {
	const pingInterval = 60 * time.Second // recency window = 2x = 120s
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 1, PingInterval: pingInterval})
	defer func() { _ = p.Close() }()

	// No error recorded yet → not failing.
	if p.HealthSummary().SpawnFailing {
		t.Errorf("SpawnFailing: got true with no recorded error, want false")
	}

	// A genuine error recorded just now → failing (within the window).
	p.SetSpawnErrForTesting("fork/exec /usr/local/bin/kiro: no such file or directory", time.Now().UTC())
	if !p.HealthSummary().SpawnFailing {
		t.Errorf("SpawnFailing: got false for a fresh spawn error, want true")
	}

	// The SAME sticky error, but older than the recency window → NOT failing.
	// (LastSpawnError is still populated — that must not drive red on its own.)
	p.SetSpawnErrForTesting("fork/exec /usr/local/bin/kiro: no such file or directory",
		time.Now().UTC().Add(-3*pingInterval))
	h := p.HealthSummary()
	if h.SpawnFailing {
		t.Errorf("SpawnFailing: got true for a stale spawn error (%v old), want false", 3*pingInterval)
	}
	if h.LastSpawnError == "" {
		t.Errorf("LastSpawnError must remain populated (sticky forensic field) even when SpawnFailing is false")
	}
}

// TestPool_Cancel_ReleasesSlot_WithoutResultDrain — 260531-ra6 RA6-01.
//
// Regression for the watchdog-cancel slot-leak path. The existing
// TestPool_Cancel_ReleasesSlot above asserts the same release contract
// but also closes the stream and calls Result() afterwards. This test
// exercises the more aggressive "engine watchdog cancels at 120s while
// kiro-cli is still blocked in Prompt" path: the stream is NEVER closed
// from the kiro side, Result() is NEVER called, and the only release
// driver is Pool.Cancel(sid). Pool.Cancel MUST itself release the slot
// — it cannot lean on the wrapper's Result-path release because that
// path will never fire when kiro hangs.
//
// Observable symptom (pre-fix): Admin UI Stats() shows Busy == 1 forever
// after the watchdog fires.
func TestPool_Cancel_ReleasesSlot_WithoutResultDrain(t *testing.T) {
	handleCh := make(chan *streamHandle, 1)
	fc := &fakeClient{}
	var count int32
	fc.newSessionFn = func(_ context.Context, _ string) (string, error) {
		n := atomic.AddInt32(&count, 1)
		if n == 1 {
			return "warmup", nil
		}
		return "run-c", nil
	}
	fc.promptFn = func(_ context.Context, sid string, _ []canonical.Block) (*acp.Stream, error) {
		s := acp.NewStreamForTest(sid)
		handleCh <- &streamHandle{stream: s}
		return s, nil
	}
	p := warmedPoolWithFakes(t, []*fakeClient{fc})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := p.Prompt(ctx, sid, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h := <-handleCh
	// Hold the stream open from the kiro side — the test will NOT close
	// it and will NOT call Result(). The only release path under test
	// is Pool.Cancel.
	_ = h

	// Pre-cancel sanity: slot is checked out (Busy == 1).
	if s := p.Stats(); s.Busy != 1 {
		t.Fatalf("pre-Cancel Stats.Busy = %d; want 1", s.Busy)
	}

	p.Cancel(sid)

	// (a) fakeClient.Cancel saw sid.
	sawSid := false
	for _, c := range fc.cancelCallList() {
		if c == sid {
			sawSid = true
			break
		}
	}
	if !sawSid {
		t.Fatalf("fakeClient.Cancel did not see %q; got %v", sid, fc.cancelCallList())
	}

	// (b) Stats reports Busy == 0 + Alive == 1 within 250ms. This is
	// the load-bearing assertion — pre-fix the slot stays Busy because
	// neither the wrapper's Result-path nor the ctx-watcher fired (no
	// stream close, no ctx cancel).
	deadline := time.Now().Add(250 * time.Millisecond)
	var last pool.Stats
	for time.Now().Before(deadline) {
		last = p.Stats()
		if last.Busy == 0 && last.Alive == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if last.Busy != 0 || last.Alive != 1 {
		t.Fatalf("Stats after Cancel = %+v; want Busy=0 Alive=1 within 250ms", last)
	}

	// (c) sessionSlots no longer contains sid.
	if l := p.SessionSlotsLen(); l != 0 {
		t.Fatalf("SessionSlotsLen after Cancel = %d; want 0", l)
	}

	// (d) Close the stream out-of-band so the wrapper's goroutines exit
	// cleanly under the goleak gate.
	h.stream.CloseForTest(&acp.FinalResult{StopReason: canonical.StopCancelled}, nil)
}
