package pool_test

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

const catalogTestTimeout = 2 * time.Second

type mutableCatalog struct {
	mu     sync.Mutex
	models []canonical.ModelInfo
}

func (c *mutableCatalog) get() []canonical.ModelInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]canonical.ModelInfo, len(c.models))
	copy(out, c.models)
	return out
}

func (c *mutableCatalog) set(models ...canonical.ModelInfo) {
	c.mu.Lock()
	c.models = append([]canonical.ModelInfo(nil), models...)
	c.mu.Unlock()
}

func twoCatalogModels() []canonical.ModelInfo {
	return []canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
	}
}

func threeCatalogModels() []canonical.ModelInfo {
	return []canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
		{ID: "qwen3-coder-next", Name: "Qwen 3 Coder Next"},
	}
}

func warmCatalogRefreshPool(t *testing.T, cfg pool.Config, clients ...pool.PoolClient) *pool.Pool {
	t.Helper()
	if cfg.Size == 0 {
		cfg.Size = len(clients)
	}
	cfg.Logger = testutil.Logger(t)
	cfg.Factory = &fakeClientFactory{clients: clients}
	p := pool.New(cfg)
	p.SetCatalogRetryForTesting(nil)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func sendCatalogTick(t *testing.T, ticks chan<- time.Time) {
	t.Helper()
	select {
	case ticks <- time.Unix(500, 0):
	case <-time.After(catalogTestTimeout):
		t.Fatal("catalog scheduler did not receive a test tick")
	}
}

func waitForCatalogOutcome(t *testing.T, p *pool.Pool, outcome pool.CatalogOutcome) pool.ModelCatalogSnapshot {
	t.Helper()
	deadline := time.NewTimer(catalogTestTimeout)
	defer deadline.Stop()
	for {
		snapshot := p.CatalogSnapshot()
		if snapshot.LastOutcome == outcome {
			return snapshot
		}
		select {
		case <-deadline.C:
			t.Fatalf("catalog outcome = %q; want %q", snapshot.LastOutcome, outcome)
		default:
			runtime.Gosched()
		}
	}
}

func TestPool_CatalogRefresh_ManualExpansionUsesOneIdleSlot(t *testing.T) {
	state := &mutableCatalog{models: twoCatalogModels()}
	fc := &fakeClient{availableModelsFn: state.get}
	p := warmCatalogRefreshPool(t, pool.Config{Size: 1}, fc)
	baselineTurns, ok := p.SlotTurns("slot-0")
	if !ok {
		t.Fatal("slot-0 missing after warmup")
	}
	baselineSessions := fc.newSessionCount()
	baselineCancels := len(fc.cancelCallList())
	state.set(threeCatalogModels()...)

	result, err := p.RefreshModelCatalog(context.Background())
	if err != nil || result.Outcome != pool.CatalogExpanded {
		t.Fatalf("refresh = %+v, %v; want expanded", result, err)
	}
	if turns, found := p.SlotTurns("slot-0"); !found || turns != baselineTurns+1 {
		t.Fatalf("turns = %d,%v; want %d,true", turns, found, baselineTurns+1)
	}
	if got := fc.newSessionCount(); got != baselineSessions+1 {
		t.Fatalf("session/new calls = %d; want %d", got, baselineSessions+1)
	}
	if got := len(fc.cancelCallList()); got != baselineCancels+1 {
		t.Fatalf("throwaway session cancels = %d; want %d", got, baselineCancels+1)
	}
	returned, ok := p.TakeSlotIfAvailable()
	if !ok {
		t.Fatal("manual refresh did not return its idle slot")
	}
	if duplicate, found := p.TakeSlotIfAvailable(); found {
		p.PutSlotBack(duplicate)
		t.Fatal("manual refresh returned its slot more than once")
	}
	p.PutSlotBack(returned)
	if got := p.Models(); !reflect.DeepEqual(got, threeCatalogModels()) {
		t.Fatalf("Models() = %#v; want expanded catalog", got)
	}
}

func TestPool_CatalogRefresh_BusyDoesNotWaitForSlot(t *testing.T) {
	fc := &fakeClient{models: twoCatalogModels()}
	p := warmCatalogRefreshPool(t, pool.Config{Size: 1}, fc)
	held, ok := p.TakeSlotIfAvailable()
	if !ok {
		t.Fatal("failed to hold the only pool slot")
	}
	defer p.PutSlotBack(held)
	baseline := fc.newSessionCount()

	result, err := p.RefreshModelCatalog(context.Background())
	if !errors.Is(err, pool.ErrCatalogRefreshBusy) {
		t.Fatalf("refresh error = %v; want ErrCatalogRefreshBusy", err)
	}
	var refreshErr *pool.CatalogRefreshError
	if !errors.As(err, &refreshErr) || refreshErr.RetryAfter <= 0 || refreshErr.RetryAfter > 30*time.Second {
		t.Fatalf("refresh error = %#v; want bounded retry timing", err)
	}
	if result.Outcome != "" || fc.newSessionCount() != baseline {
		t.Fatalf("refresh = %+v, session/new=%d; want no probe", result, fc.newSessionCount())
	}
}

func TestPool_CatalogRefresh_SingleFlightIncludesLazyProbe(t *testing.T) {
	var warmed atomic.Bool
	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	fc := &fakeClient{
		availableModelsFn: func() []canonical.ModelInfo {
			if warmed.Load() {
				return twoCatalogModels()
			}
			return nil
		},
		newSessionFn: func(ctx context.Context, _ string) (string, error) {
			if warmed.Load() {
				close(probeEntered)
				select {
				case <-releaseProbe:
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return "catalog-probe", nil
		},
	}
	p := warmCatalogRefreshPool(t, pool.Config{Size: 1}, fc)
	warmed.Store(true)
	_ = p.Models()
	select {
	case <-probeEntered:
	case <-time.After(catalogTestTimeout):
		t.Fatal("lazy catalog probe did not start")
	}

	_, err := p.RefreshModelCatalog(context.Background())
	if !errors.Is(err, pool.ErrCatalogRefreshInProgress) {
		t.Fatalf("manual refresh error = %v; want ErrCatalogRefreshInProgress", err)
	}
	close(releaseProbe)
	returned, ok := p.WaitForSlotRelease(catalogTestTimeout)
	if !ok {
		t.Fatal("lazy catalog probe did not return its slot")
	}
	p.PutSlotBack(returned)
}

func TestPool_CatalogRefresh_ManualCooldownIsThirtySeconds(t *testing.T) {
	now := time.Unix(1_000, 0)
	fc := &fakeClient{models: twoCatalogModels()}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	p.SetCatalogRetryForTesting(nil)
	p.SetCatalogNowForTesting(func() time.Time { return now })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	if _, err := p.RefreshModelCatalog(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	baseline := fc.newSessionCount()
	_, err := p.RefreshModelCatalog(context.Background())
	var refreshErr *pool.CatalogRefreshError
	if !errors.Is(err, pool.ErrCatalogRefreshCooldown) || !errors.As(err, &refreshErr) || refreshErr.RetryAfter != 30*time.Second {
		t.Fatalf("immediate refresh error = %#v; want 30-second cooldown", err)
	}
	now = now.Add(29 * time.Second)
	_, err = p.RefreshModelCatalog(context.Background())
	if !errors.As(err, &refreshErr) || refreshErr.RetryAfter != time.Second {
		t.Fatalf("29-second refresh error = %#v; want 1-second retry", err)
	}
	if got := fc.newSessionCount(); got != baseline {
		t.Fatalf("cooldown admitted %d extra probes; want 0", got-baseline)
	}
	now = now.Add(time.Second)
	if _, err := p.RefreshModelCatalog(context.Background()); err != nil {
		t.Fatalf("refresh at cooldown boundary: %v", err)
	}
}

func TestPool_CatalogRefresh_CallerCancellationRetainsCatalogAndSlot(t *testing.T) {
	probeEntered := make(chan struct{})
	var runtimeProbe atomic.Bool
	fc := &fakeClient{
		models: twoCatalogModels(),
		newSessionFn: func(ctx context.Context, _ string) (string, error) {
			if runtimeProbe.Load() {
				close(probeEntered)
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "warmup", nil
		},
	}
	p := warmCatalogRefreshPool(t, pool.Config{Size: 1}, fc)
	runtimeProbe.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan pool.CatalogRefreshResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := p.RefreshModelCatalog(ctx)
		resultCh <- result
		errCh <- err
	}()
	select {
	case <-probeEntered:
	case <-time.After(catalogTestTimeout):
		t.Fatal("manual catalog probe did not start")
	}
	cancel()
	result := <-resultCh
	err := <-errCh
	if !errors.Is(err, context.Canceled) || result.Outcome != pool.CatalogCancelled {
		t.Fatalf("refresh = %+v, %v; want cancelled", result, err)
	}
	if snapshot := p.CatalogSnapshot(); len(snapshot.Models) != 2 || snapshot.LastOutcome != pool.CatalogCancelled {
		t.Fatalf("snapshot = %+v; want retained two-model catalog", snapshot)
	}
	returned, ok := p.TakeSlotIfAvailable()
	if !ok {
		t.Fatal("cancelled refresh did not return its slot")
	}
	p.PutSlotBack(returned)
}

func TestPool_CatalogRefresh_SizeOneRemainsUsable(t *testing.T) {
	fc := &fakeClient{models: twoCatalogModels()}
	p := warmCatalogRefreshPool(t, pool.Config{Size: 1}, fc)
	if _, err := p.RefreshModelCatalog(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatalf("NewSession after refresh: %v", err)
	}
	p.Cancel(sid)
	returned, ok := p.WaitForSlotRelease(catalogTestTimeout)
	if !ok {
		t.Fatal("size-one slot was not released after client request")
	}
	p.PutSlotBack(returned)
}

func TestPool_CatalogRefresh_CloseCancelsAndJoinsBlockedProbe(t *testing.T) {
	probeEntered := make(chan struct{})
	probeCancelled := make(chan struct{})
	allowProbeReturn := make(chan struct{})
	var runtimeProbe atomic.Bool
	fc := &fakeClient{
		models: twoCatalogModels(),
		newSessionFn: func(ctx context.Context, _ string) (string, error) {
			if runtimeProbe.Load() {
				close(probeEntered)
				<-ctx.Done()
				close(probeCancelled)
				<-allowProbeReturn
				return "", ctx.Err()
			}
			return "warmup", nil
		},
	}
	p := warmCatalogRefreshPool(t, pool.Config{Size: 1}, fc)
	runtimeProbe.Store(true)
	refreshErr := make(chan error, 1)
	go func() {
		_, err := p.RefreshModelCatalog(context.Background())
		refreshErr <- err
	}()
	select {
	case <-probeEntered:
	case <-time.After(catalogTestTimeout):
		t.Fatal("manual catalog probe did not start")
	}
	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close() }()
	select {
	case <-probeCancelled:
	case <-time.After(catalogTestTimeout):
		t.Fatal("Close did not cancel the catalog probe context")
	}
	select {
	case err := <-closeErr:
		t.Fatalf("Close returned before the admitted probe exited: %v", err)
	default:
	}
	close(allowProbeReturn)
	if err := <-refreshErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh error = %v; want context.Canceled", err)
	}
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPool_CatalogRefresh_AfterCloseIsUnavailable(t *testing.T) {
	p := pool.New(pool.Config{Logger: testutil.Logger(t)})
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := p.RefreshModelCatalog(context.Background())
	if !errors.Is(err, pool.ErrCatalogRefreshUnavailable) {
		t.Fatalf("refresh error = %v; want ErrCatalogRefreshUnavailable", err)
	}
}

func TestPool_CatalogRefresh_TwoRealProbesConfirmShrink(t *testing.T) {
	now := time.Unix(2_000, 0)
	state := &mutableCatalog{models: twoCatalogModels()}
	fc := &fakeClient{availableModelsFn: state.get}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	p.SetCatalogRetryForTesting(nil)
	p.SetCatalogNowForTesting(func() time.Time { return now })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	baseline := fc.newSessionCount()
	state.set(canonical.ModelInfo{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"})

	first, err := p.RefreshModelCatalog(context.Background())
	if err != nil || first.Outcome != pool.CatalogPendingShrink || len(p.Models()) != 2 {
		t.Fatalf("first refresh = %+v, %v models=%v; want pending shrink", first, err, p.Models())
	}
	now = now.Add(30 * time.Second)
	second, err := p.RefreshModelCatalog(context.Background())
	if err != nil || second.Outcome != pool.CatalogShrinkConfirmed || len(p.Models()) != 1 {
		t.Fatalf("second refresh = %+v, %v models=%v; want confirmed shrink", second, err, p.Models())
	}
	if got := fc.newSessionCount() - baseline; got != 2 {
		t.Fatalf("runtime probes = %d; want 2 real probes", got)
	}
}

func TestPool_CatalogRefresh_FailedSecondProbeDoesNotConfirmShrink(t *testing.T) {
	now := time.Unix(3_000, 0)
	state := &mutableCatalog{models: twoCatalogModels()}
	var failProbe atomic.Bool
	fc := &fakeClient{
		availableModelsFn: state.get,
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			if failProbe.Load() {
				return "", errors.New("upstream probe failed")
			}
			return "catalog-probe", nil
		},
	}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	p.SetCatalogRetryForTesting(nil)
	p.SetCatalogNowForTesting(func() time.Time { return now })
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	state.set(canonical.ModelInfo{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"})
	first, err := p.RefreshModelCatalog(context.Background())
	if err != nil || first.Outcome != pool.CatalogPendingShrink {
		t.Fatalf("first refresh = %+v, %v; want pending shrink", first, err)
	}
	now = now.Add(30 * time.Second)
	failProbe.Store(true)
	second, err := p.RefreshModelCatalog(context.Background())
	if err == nil || second.Outcome != pool.CatalogFailed {
		t.Fatalf("second refresh = %+v, %v; want failed observation", second, err)
	}
	snapshot := p.CatalogSnapshot()
	if len(snapshot.Models) != 2 || snapshot.PendingRemovals != 1 || snapshot.LastOutcome != pool.CatalogFailed {
		t.Fatalf("snapshot = %+v; want retained catalog and pending evidence", snapshot)
	}
}

func TestPool_CatalogRefresh_ReleaseCanTriggerWorkerRecycle(t *testing.T) {
	state := &mutableCatalog{models: twoCatalogModels()}
	oldClient := &fakeClient{availableModelsFn: state.get}
	newClient := &fakeClient{models: threeCatalogModels()}
	p := warmCatalogRefreshPool(t, pool.Config{Size: 1, MaxWorkerTurns: 2}, oldClient, newClient)
	state.set(threeCatalogModels()...)
	result, err := p.RefreshModelCatalog(context.Background())
	if err != nil || result.Outcome != pool.CatalogExpanded {
		t.Fatalf("refresh = %+v, %v; want expanded", result, err)
	}
	p.WaitForRecyclesForTesting()
	if got := p.Recycles(); got != 1 {
		t.Fatalf("Recycles() = %d; want 1 after probe reached max turns", got)
	}
	if turns, ok := p.SlotTurns("slot-0"); !ok || turns != 0 {
		t.Fatalf("replacement turns = %d,%v; want 0,true", turns, ok)
	}
}

func TestPool_CatalogScheduler_TickExpandsCatalog(t *testing.T) {
	state := &mutableCatalog{models: twoCatalogModels()}
	var runtimeProbe atomic.Bool
	probeCancelled := make(chan struct{}, 1)
	fc := &fakeClient{
		availableModelsFn: state.get,
		cancelFn: func(string) {
			if runtimeProbe.Load() {
				probeCancelled <- struct{}{}
			}
		},
	}
	ticks := make(chan time.Time)
	p := pool.New(pool.Config{
		Logger:                      testutil.Logger(t),
		Size:                        1,
		Factory:                     &fakeClientFactory{clients: []pool.PoolClient{fc}},
		ModelCatalogRefreshInterval: time.Minute,
	})
	p.SetCatalogRetryForTesting(nil)
	p.SetCatalogRefreshTicksForTesting(ticks)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	state.set(threeCatalogModels()...)
	runtimeProbe.Store(true)
	sendCatalogTick(t, ticks)
	select {
	case <-probeCancelled:
	case <-time.After(catalogTestTimeout):
		t.Fatal("scheduled probe did not cancel its throwaway session")
	}
	if snapshot := waitForCatalogOutcome(t, p, pool.CatalogExpanded); len(snapshot.Models) != 3 {
		t.Fatalf("snapshot = %+v; want scheduled expansion", snapshot)
	}
}

func TestPool_CatalogScheduler_BusyTickRecordsAndWaitsForNextTick(t *testing.T) {
	state := &mutableCatalog{models: twoCatalogModels()}
	var runtimeProbe atomic.Bool
	probeCancelled := make(chan struct{}, 1)
	fc := &fakeClient{
		availableModelsFn: state.get,
		cancelFn: func(string) {
			if runtimeProbe.Load() {
				probeCancelled <- struct{}{}
			}
		},
	}
	ticks := make(chan time.Time)
	p := pool.New(pool.Config{
		Logger:                      testutil.Logger(t),
		Size:                        1,
		Factory:                     &fakeClientFactory{clients: []pool.PoolClient{fc}},
		ModelCatalogRefreshInterval: time.Minute,
	})
	p.SetCatalogRetryForTesting(nil)
	p.SetCatalogRefreshTicksForTesting(ticks)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	held, ok := p.TakeSlotIfAvailable()
	if !ok {
		t.Fatal("failed to hold the only pool slot")
	}
	sendCatalogTick(t, ticks)
	waitForCatalogOutcome(t, p, pool.CatalogSkippedBusy)
	p.PutSlotBack(held)

	state.set(threeCatalogModels()...)
	runtimeProbe.Store(true)
	sendCatalogTick(t, ticks)
	select {
	case <-probeCancelled:
	case <-time.After(catalogTestTimeout):
		t.Fatal("scheduler did not wait for and process the next ordinary tick")
	}
	if snapshot := waitForCatalogOutcome(t, p, pool.CatalogExpanded); len(snapshot.Models) != 3 {
		t.Fatalf("snapshot = %+v; want expansion on the next tick", snapshot)
	}
}

func TestPool_CatalogScheduler_ZeroIntervalStartsNoLoop(t *testing.T) {
	fc := &fakeClient{models: twoCatalogModels()}
	ticks := make(chan time.Time, 1)
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}},
	})
	p.SetCatalogRetryForTesting(nil)
	p.SetCatalogRefreshTicksForTesting(ticks)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	baseline := fc.newSessionCount()
	ticks <- time.Unix(500, 0)
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(ticks); got != 1 {
		t.Fatalf("disabled scheduler consumed %d ticks; want 0", 1-got)
	}
	if got := fc.newSessionCount(); got != baseline {
		t.Fatalf("disabled scheduler ran %d probes; want 0", got-baseline)
	}
}

func TestPool_CatalogScheduler_FirstRefreshIsNotImmediate(t *testing.T) {
	fc := &fakeClient{models: twoCatalogModels()}
	ticks := make(chan time.Time)
	p := pool.New(pool.Config{
		Logger:                      testutil.Logger(t),
		Size:                        1,
		Factory:                     &fakeClientFactory{clients: []pool.PoolClient{fc}},
		ModelCatalogRefreshInterval: time.Minute,
	})
	p.SetCatalogRetryForTesting(nil)
	p.SetCatalogRefreshTicksForTesting(ticks)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	baseline := fc.newSessionCount()
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fc.newSessionCount(); got != baseline {
		t.Fatalf("scheduler ran %d probes before its first tick; want 0", got-baseline)
	}
}

func TestPool_CatalogScheduler_CloseJoinsBlockedTick(t *testing.T) {
	var runtimeProbe atomic.Bool
	probeEntered := make(chan struct{})
	probeCancelled := make(chan struct{})
	allowProbeReturn := make(chan struct{})
	fc := &fakeClient{
		models: twoCatalogModels(),
		newSessionFn: func(ctx context.Context, _ string) (string, error) {
			if runtimeProbe.Load() {
				close(probeEntered)
				<-ctx.Done()
				close(probeCancelled)
				<-allowProbeReturn
				return "", ctx.Err()
			}
			return "warmup", nil
		},
	}
	ticks := make(chan time.Time)
	p := pool.New(pool.Config{
		Logger:                      testutil.Logger(t),
		Size:                        1,
		Factory:                     &fakeClientFactory{clients: []pool.PoolClient{fc}},
		ModelCatalogRefreshInterval: time.Minute,
	})
	p.SetCatalogRetryForTesting(nil)
	p.SetCatalogRefreshTicksForTesting(ticks)
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	runtimeProbe.Store(true)
	sendCatalogTick(t, ticks)
	select {
	case <-probeEntered:
	case <-time.After(catalogTestTimeout):
		t.Fatal("scheduled probe did not start")
	}
	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close() }()
	select {
	case <-probeCancelled:
	case <-time.After(catalogTestTimeout):
		t.Fatal("Close did not cancel the scheduled probe")
	}
	select {
	case err := <-closeErr:
		t.Fatalf("Close returned before the scheduler probe exited: %v", err)
	default:
	}
	close(allowProbeReturn)
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if snapshot := p.CatalogSnapshot(); snapshot.LastOutcome != pool.CatalogCancelled {
		t.Fatalf("snapshot outcome = %q; want cancelled", snapshot.LastOutcome)
	}
}
