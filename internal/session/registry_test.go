// Package session_test — blackbox test file (D-18 pattern).
// Tests drive the registry through its exported surface PLUS the
// test-only accessors in export_test.go.
package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// ---------------------------------------------------------------------------
// Fake harness — drives registry behaviour without real kiro-cli.
// ---------------------------------------------------------------------------

// fakeClient implements session.PoolClient. All hooks default to no-op
// success so a test sets only the fields it cares about. Call counts
// and last-args are recorded under mu so concurrent assertions work.
type fakeClient struct {
	// scripted hooks (nil = default behaviour)
	initializeFn func(ctx context.Context) error
	newSessionFn func(ctx context.Context, cwd string) (string, error)
	setModelFn   func(ctx context.Context, sid, m string) error
	promptFn     func(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error)
	closeFn      func() error

	mu              sync.Mutex
	initializeCalls int
	newSessionCalls int
	setModelCalls   int
	cancelCalls     []string
	closeCalls      int

	doneMu sync.Mutex
	doneCh chan struct{}
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
	f.mu.Lock()
	f.setModelCalls++
	f.mu.Unlock()
	if f.setModelFn != nil {
		return f.setModelFn(ctx, sid, m)
	}
	return nil
}

func (f *fakeClient) Prompt(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error) {
	if f.promptFn != nil {
		return f.promptFn(ctx, sid, blocks)
	}
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
	closeFn := f.closeFn
	f.mu.Unlock()
	if closeFn != nil {
		return closeFn()
	}
	return nil
}

func (f *fakeClient) AvailableModels() []canonical.ModelInfo { return nil }

func (f *fakeClient) Done() <-chan struct{} {
	f.doneMu.Lock()
	defer f.doneMu.Unlock()
	if f.doneCh == nil {
		f.doneCh = make(chan struct{})
	}
	return f.doneCh
}

func (f *fakeClient) newSessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newSessionCalls
}

func (f *fakeClient) setModelCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setModelCalls
}

func (f *fakeClient) cancelCallList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.cancelCalls))
	copy(out, f.cancelCalls)
	return out
}

func (f *fakeClient) closeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls
}

// fakeClientFactory hands out pre-scripted fakeClients in order. Spawn
// errors once the script is exhausted, unless spawnErr is set.
type fakeClientFactory struct {
	clients  []session.PoolClient
	mu       sync.Mutex
	idx      int
	spawnErr error
	// spawnHook fires AFTER mu is released, BEFORE returning. Used by
	// the Pitfall 4 racing-same-sid test to synchronise concurrent
	// callers around a single Spawn observation.
	spawnHook func()
}

func (ff *fakeClientFactory) Spawn(_ context.Context, _ acp.Config) (session.PoolClient, error) {
	if ff.spawnErr != nil {
		return nil, ff.spawnErr
	}
	ff.mu.Lock()
	if ff.idx >= len(ff.clients) {
		ff.mu.Unlock()
		return nil, errors.New("fakeClientFactory: no more clients in script")
	}
	c := ff.clients[ff.idx]
	ff.idx++
	hook := ff.spawnHook
	ff.mu.Unlock()
	if hook != nil {
		hook()
	}
	return c, nil
}

func (ff *fakeClientFactory) spawnCount() int {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.idx
}

// ---------------------------------------------------------------------------
// Tests — Task 1
// ---------------------------------------------------------------------------

// TestRegistry_Get_LazyCreate — SESS-01 + D-05. First Get spawns the
// subprocess; second Get returns the cached entry without re-spawning.
func TestRegistry_Get_LazyCreate(t *testing.T) {
	fc := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "kiro-sess-1", nil
		},
	}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: ff,
	})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	e1, err := r.Get(ctx, "sid-1", "/tmp")
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	if e1 == nil {
		t.Fatal("Get #1 returned nil entry")
	}
	if e1.SessionID != "kiro-sess-1" {
		t.Errorf("SessionID = %q; want kiro-sess-1", e1.SessionID)
	}
	if fc.newSessionCount() != 1 {
		t.Errorf("NewSession calls after first Get = %d; want 1", fc.newSessionCount())
	}
	if ff.spawnCount() != 1 {
		t.Errorf("Spawn calls after first Get = %d; want 1", ff.spawnCount())
	}

	e2, err := r.Get(ctx, "sid-1", "/tmp")
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if e2 != e1 {
		t.Errorf("Get #2 returned different *Entry; lazy-cache broken")
	}
	if fc.newSessionCount() != 1 {
		t.Errorf("NewSession calls after second Get = %d; want 1 (cached)", fc.newSessionCount())
	}
	if ff.spawnCount() != 1 {
		t.Errorf("Spawn calls after second Get = %d; want 1 (cached)", ff.spawnCount())
	}
}

// TestRegistry_Get_RacingSameSid_NoDoubleSpawn — Pitfall 4. Two
// concurrent same-sid Get calls observe a single Spawn and both
// receive the SAME *Entry pointer.
func TestRegistry_Get_RacingSameSid_NoDoubleSpawn(t *testing.T) {
	// Gate so both goroutines are confirmed inside Get before the
	// Spawn completes.
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	fc := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "kiro-sess-race", nil
		},
		initializeFn: func(_ context.Context) error {
			// Hold inside Initialize until both goroutines have
			// signalled they entered Get and the test releases.
			<-release
			return nil
		},
	}
	ff := &fakeClientFactory{
		clients: []session.PoolClient{fc},
		spawnHook: func() {
			ready <- struct{}{}
		},
	}
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: ff,
	})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	type result struct {
		e   *session.Entry
		err error
	}
	resCh := make(chan result, 2)
	go func() {
		e, err := r.Get(ctx, "sid-race", "/tmp")
		resCh <- result{e, err}
	}()
	// Tiny delay so the second goroutine is more likely to enter Get
	// after the first installs the placeholder. The Pitfall 4 logic
	// works regardless, but this exercises the waiter-path explicitly.
	time.Sleep(20 * time.Millisecond)
	go func() {
		e, err := r.Get(ctx, "sid-race", "/tmp")
		resCh <- result{e, err}
	}()

	// Wait for the spawnHook to fire (proves Spawn was called at least once).
	select {
	case <-ready:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Spawn was not called within 500ms")
	}
	// Release Initialize so createEntry can finish.
	close(release)

	r1 := <-resCh
	r2 := <-resCh
	if r1.err != nil {
		t.Fatalf("racing Get #1 err: %v", r1.err)
	}
	if r2.err != nil {
		t.Fatalf("racing Get #2 err: %v", r2.err)
	}
	if r1.e == nil || r2.e == nil {
		t.Fatal("racing Get returned nil entry")
	}
	if r1.e != r2.e {
		t.Errorf("racing Get returned different entries — Pitfall 4 broken (r1=%p r2=%p)", r1.e, r2.e)
	}
	if got := ff.spawnCount(); got != 1 {
		t.Errorf("Spawn calls = %d; want exactly 1 (Pitfall 4)", got)
	}
	if got := fc.newSessionCount(); got != 1 {
		t.Errorf("NewSession calls = %d; want exactly 1", got)
	}
}

// TestRegistry_Get_SessionMaxExceeded — D-06. With MaxSessions=2, the
// third Get with a NEW sid returns ErrSessionMaxExceeded. Existing
// entries are not affected; a subsequent Get with an EXISTING sid
// still works (read-through cache).
func TestRegistry_Get_SessionMaxExceeded(t *testing.T) {
	fc1 := &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return "kiro-1", nil }}
	fc2 := &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return "kiro-2", nil }}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc1, fc2}}
	r := session.New(session.Config{
		Logger:      testutil.Logger(t),
		Factory:     ff,
		MaxSessions: 2,
	})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if _, err := r.Get(ctx, "sid-A", "/tmp"); err != nil {
		t.Fatalf("Get sid-A: %v", err)
	}
	if _, err := r.Get(ctx, "sid-B", "/tmp"); err != nil {
		t.Fatalf("Get sid-B: %v", err)
	}
	// Third Get with a NEW sid: must return ErrSessionMaxExceeded.
	_, err := r.Get(ctx, "sid-C", "/tmp")
	if err == nil {
		t.Fatalf("Get sid-C: expected ErrSessionMaxExceeded, got nil")
	}
	if !errors.Is(err, session.ErrSessionMaxExceeded) {
		t.Errorf("Get sid-C: errors.Is(err, ErrSessionMaxExceeded) = false; err=%v", err)
	}
	if got := ff.spawnCount(); got != 2 {
		t.Errorf("Spawn count = %d; want 2 (third Get must not spawn)", got)
	}
	// Existing sid still returns cached entry.
	if _, err := r.Get(ctx, "sid-A", "/tmp"); err != nil {
		t.Errorf("Get sid-A (cached): %v", err)
	}
}

// TestRegistry_Delete_KnownSid — D-08 happy path. Delete returns nil;
// fakeClient observes one Cancel + one Close; a subsequent Get
// lazy-creates a NEW entry (sid no longer in map).
func TestRegistry_Delete_KnownSid(t *testing.T) {
	fc1 := &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return "kiro-1", nil }}
	fc2 := &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return "kiro-2", nil }}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc1, fc2}}
	r := session.New(session.Config{Logger: testutil.Logger(t), Factory: ff})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	e1, err := r.Get(ctx, "sid-1", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = e1

	if err := r.Delete("sid-1"); err != nil {
		t.Fatalf("Delete sid-1: %v", err)
	}
	if got := fc1.cancelCallList(); len(got) != 1 || got[0] != "kiro-1" {
		t.Errorf("Cancel calls after Delete = %v; want [kiro-1]", got)
	}
	if got := fc1.closeCallCount(); got != 1 {
		t.Errorf("Close calls after Delete = %d; want 1", got)
	}
	// Subsequent Get for same sid lazy-creates new entry (uses fc2).
	e2, err := r.Get(ctx, "sid-1", "/tmp")
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if e2 == e1 {
		t.Errorf("Get after Delete returned the old entry — map-delete-first broken")
	}
	if e2.SessionID != "kiro-2" {
		t.Errorf("SessionID after re-create = %q; want kiro-2", e2.SessionID)
	}
}

// TestRegistry_Delete_UnknownSid_ReturnsErrSessionNotFound — D-08 404
// path. Delete on a sid not in the map returns ErrSessionNotFound;
// errors.Is matches.
func TestRegistry_Delete_UnknownSid_ReturnsErrSessionNotFound(t *testing.T) {
	r := session.New(session.Config{Logger: testutil.Logger(t)})
	defer func() { _ = r.Close() }()

	err := r.Delete("nonexistent")
	if err == nil {
		t.Fatal("Delete(nonexistent): expected ErrSessionNotFound, got nil")
	}
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("errors.Is(err, ErrSessionNotFound) = false; err=%v", err)
	}
}

// TestRegistry_Delete_CancelsInFlight — Pitfall 1 / D-08. Delete does
// NOT wait on the entry's Mu (map-delete-first); it proceeds to
// Cancel + Close even while a "stream" holds the Mu in another
// goroutine. Subsequent Get returns a NEW entry.
func TestRegistry_Delete_CancelsInFlight(t *testing.T) {
	fc1 := &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return "kiro-A", nil }}
	fc2 := &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return "kiro-B", nil }}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc1, fc2}}
	r := session.New(session.Config{Logger: testutil.Logger(t), Factory: ff})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-busy", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Simulate in-flight Prompt: hold the entry's Mu in a goroutine.
	muHeld := make(chan struct{})
	muRelease := make(chan struct{})
	go func() {
		e.Mu.Lock()
		close(muHeld)
		<-muRelease
		e.Mu.Unlock()
	}()
	<-muHeld

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- r.Delete("sid-busy")
	}()

	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("Delete returned err: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Delete did not return within 100ms — map-delete-first broken (waited on Mu)")
	}

	// Cancel was called (defensive cleanup) even though Mu was held.
	if got := fc1.cancelCallList(); len(got) != 1 || got[0] != "kiro-A" {
		t.Errorf("Cancel after concurrent Delete = %v; want [kiro-A]", got)
	}
	// Subsequent Get lazy-creates a new entry (different fc).
	e2, err := r.Get(ctx, "sid-busy", "/tmp")
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if e2 == e {
		t.Errorf("Get after Delete returned old entry")
	}
	if e2.SessionID != "kiro-B" {
		t.Errorf("new entry SessionID = %q; want kiro-B", e2.SessionID)
	}

	// Release the simulated in-flight goroutine so it exits before
	// the goleak gate runs.
	close(muRelease)
}

// TestEntry_SetModel_SkipsWhenUnchanged — D-09. SetModel("X") spawns
// one RPC; SetModel("X") again is a no-op; SetModel("Y") spawns again.
func TestEntry_SetModel_SkipsWhenUnchanged(t *testing.T) {
	fc := &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return "kiro-1", nil }}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
	r := session.New(session.Config{Logger: testutil.Logger(t), Factory: ff})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-set-model", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := e.SetModel(ctx, e.SessionID, "model-X"); err != nil {
		t.Fatalf("SetModel(X) #1: %v", err)
	}
	if got := fc.setModelCount(); got != 1 {
		t.Errorf("setModelCount after first SetModel = %d; want 1", got)
	}
	if e.LastModel != "model-X" {
		t.Errorf("LastModel = %q; want model-X", e.LastModel)
	}

	if err := e.SetModel(ctx, e.SessionID, "model-X"); err != nil {
		t.Fatalf("SetModel(X) #2: %v", err)
	}
	if got := fc.setModelCount(); got != 1 {
		t.Errorf("setModelCount after second SetModel(X) = %d; want 1 (skipped)", got)
	}

	if err := e.SetModel(ctx, e.SessionID, "model-Y"); err != nil {
		t.Fatalf("SetModel(Y): %v", err)
	}
	if got := fc.setModelCount(); got != 2 {
		t.Errorf("setModelCount after SetModel(Y) = %d; want 2", got)
	}
	if e.LastModel != "model-Y" {
		t.Errorf("LastModel after SetModel(Y) = %q; want model-Y", e.LastModel)
	}
}

// TestEntry_MarkUsed_UpdatesLastUsed — D-11. MarkUsed sets LastUsed
// to ~time.Now(); two consecutive calls produce non-decreasing
// timestamps.
func TestEntry_MarkUsed_UpdatesLastUsed(t *testing.T) {
	e := session.NewEntryForTest(nil, "sid-mu")
	// P-5 fix: LastUsed is now an accessor method (atomic.Int64 backing).
	t0 := e.LastUsed()

	// Tiny sleep so the second MarkUsed produces a measurably later
	// timestamp on platforms with coarse clock resolution.
	time.Sleep(2 * time.Millisecond)
	e.MarkUsed()
	if !e.LastUsed().After(t0) {
		t.Errorf("MarkUsed did not advance LastUsed: t0=%v t1=%v", t0, e.LastUsed())
	}

	t1 := e.LastUsed()
	time.Sleep(2 * time.Millisecond)
	e.MarkUsed()
	if e.LastUsed().Before(t1) {
		t.Errorf("MarkUsed produced regressing timestamp: t1=%v t2=%v", t1, e.LastUsed())
	}
}

// TestRegistry_Stats_ReturnsActiveCount — Stats.Active reflects the
// current number of entries; Delete decrements it.
func TestRegistry_Stats_ReturnsActiveCount(t *testing.T) {
	clients := []session.PoolClient{}
	for i := 0; i < 4; i++ {
		clients = append(clients, &fakeClient{
			newSessionFn: func(_ context.Context, _ string) (string, error) {
				return "kiro", nil
			},
		})
	}
	ff := &fakeClientFactory{clients: clients}
	r := session.New(session.Config{Logger: testutil.Logger(t), Factory: ff})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if got := r.Stats().Active; got != 0 {
		t.Errorf("initial Stats.Active = %d; want 0", got)
	}
	for i, sid := range []string{"sid-1", "sid-2", "sid-3"} {
		if _, err := r.Get(ctx, sid, "/tmp"); err != nil {
			t.Fatalf("Get %s: %v", sid, err)
		}
		want := i + 1
		if got := r.Stats().Active; got != want {
			t.Errorf("Stats.Active after Get %s = %d; want %d", sid, got, want)
		}
	}
	if err := r.Delete("sid-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := r.Stats().Active; got != 2 {
		t.Errorf("Stats.Active after Delete = %d; want 2", got)
	}
}

// TestRegistry_Detail_RowShape — D-16. Detail rows carry the expected
// fields: ID = sid, Alive = !Dead, Busy reflects Mu lock state,
// LastUsed = Entry.LastUsed, Model = nil when LastModel empty else
// *LastModel.
func TestRegistry_Detail_RowShape(t *testing.T) {
	fc := &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return "kiro-1", nil }}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
	r := session.New(session.Config{Logger: testutil.Logger(t), Factory: ff})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-detail", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	rows := r.Detail()
	if len(rows) != 1 {
		t.Fatalf("Detail() len = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.ID != "sid-detail" {
		t.Errorf("row.ID = %q; want sid-detail", row.ID)
	}
	if !row.Alive {
		t.Errorf("row.Alive = false; want true (Dead=%v)", e.Dead)
	}
	if row.Busy {
		t.Errorf("row.Busy = true; want false (Mu is unlocked)")
	}
	if row.Model != nil {
		t.Errorf("row.Model = %v; want nil (no SetModel yet)", *row.Model)
	}
	if !row.LastUsed.Equal(e.LastUsed()) {
		t.Errorf("row.LastUsed = %v; want %v", row.LastUsed, e.LastUsed())
	}

	// SetModel then re-check Model is a non-nil pointer.
	if err := e.SetModel(ctx, e.SessionID, "model-Z"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	rows = r.Detail()
	if rows[0].Model == nil {
		t.Errorf("row.Model after SetModel = nil; want pointer to model-Z")
	} else if *rows[0].Model != "model-Z" {
		t.Errorf("*row.Model = %q; want model-Z", *rows[0].Model)
	}

	// Lock entry.Mu and re-check Busy=true.
	e.Mu.Lock()
	rows = r.Detail()
	if !rows[0].Busy {
		t.Errorf("row.Busy with Mu locked = false; want true")
	}
	e.Mu.Unlock()
}

// TestRegistry_Get_FactorySpawnFails — T-05-CTX-LEAK. On Spawn error,
// the placeholder is removed and a subsequent Get retries cleanly.
func TestRegistry_Get_FactorySpawnFails(t *testing.T) {
	failErr := errors.New("kiro busted")
	ff := &fakeClientFactory{spawnErr: failErr}
	r := session.New(session.Config{Logger: testutil.Logger(t), Factory: ff})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := r.Get(ctx, "sid-spawn-fails", "/tmp")
	if err == nil {
		t.Fatal("Get with spawnErr: expected error, got nil")
	}
	if !errors.Is(err, failErr) {
		t.Errorf("errors.Is(err, failErr) = false; err=%v", err)
	}
	// Stats reflects that the placeholder was cleaned up: the failed
	// spawn must NOT leave a "creating" entry behind that would block
	// future Get retries.
	if got := r.Stats().Active; got != 0 {
		t.Errorf("Stats.Active after failed Spawn = %d; want 0 (placeholder cleaned up)", got)
	}
	// Subsequent Get with the SAME sid retries through the factory
	// (whose spawnErr is sticky, so it also fails) — this confirms the
	// placeholder is gone (otherwise we'd hit the cached-creating path
	// and deadlock on the never-closed ready chan).
	if _, retryErr := r.Get(ctx, "sid-spawn-fails", "/tmp"); retryErr == nil {
		t.Error("retry Get with sticky spawnErr: expected error, got nil")
	}
}

// TestRegistry_Get_AfterClose_ReturnsErrRegistryClosed — Close-then-Get
// must fail fast with ErrRegistryClosed.
func TestRegistry_Get_AfterClose_ReturnsErrRegistryClosed(t *testing.T) {
	r := session.New(session.Config{Logger: testutil.Logger(t)})
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := r.Get(context.Background(), "sid", "/tmp")
	if err == nil {
		t.Fatal("Get after Close: expected ErrRegistryClosed, got nil")
	}
	if !errors.Is(err, session.ErrRegistryClosed) {
		t.Errorf("errors.Is(err, ErrRegistryClosed) = false; err=%v", err)
	}
}
