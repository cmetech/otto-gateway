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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loop24-gateway/internal/acp"
	"loop24-gateway/internal/canonical"
	"loop24-gateway/internal/engine"
	"loop24-gateway/internal/pool"
	"loop24-gateway/internal/testutil"
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

	// scripted model catalog returned by AvailableModels.
	models []canonical.ModelInfo

	mu               sync.Mutex
	initializeCalls  int
	newSessionCalls  int
	cancelCalls      []string
	closeCalls       int
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
	return f.models
}

// snapshot helpers — read counters/calls under mu.
func (f *fakeClient) newSessionCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.newSessionCalls }
func (f *fakeClient) cancelCallList() []string {
	f.mu.Lock(); defer f.mu.Unlock()
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
// the LOOP24_INTEGRATION=1 gate from Phase 1 (D-17).
func hasKiroBinary() bool {
	if os.Getenv("LOOP24_INTEGRATION") != "1" {
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
		KiroCmd: "/nonexistent/binary-xyz-loop24-test",
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
		t.Skip("LOOP24_INTEGRATION=1 + kiro-cli on PATH required for real-kiro warmup")
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
