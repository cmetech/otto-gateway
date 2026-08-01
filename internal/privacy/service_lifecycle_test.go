package privacy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

type lifecycleClock struct {
	mu       sync.Mutex
	now      time.Time
	interval time.Duration
	ticker   *lifecycleTicker
}

func newLifecycleClock() *lifecycleClock {
	return &lifecycleClock{now: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}
}

func (c *lifecycleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *lifecycleClock) NewTicker(interval time.Duration) Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interval = interval
	c.ticker = &lifecycleTicker{ch: make(chan time.Time, 1), stopped: make(chan struct{})}
	return c.ticker
}

func (c *lifecycleClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	now := c.now
	ticker := c.ticker
	c.mu.Unlock()
	if ticker != nil {
		ticker.ch <- now
	}
}

func (c *lifecycleClock) AdvanceSilently(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func (c *lifecycleClock) Tick() {
	c.mu.Lock()
	now := c.now
	ticker := c.ticker
	c.mu.Unlock()
	if ticker != nil {
		ticker.ch <- now
	}
}

type lifecycleTicker struct {
	ch       chan time.Time
	stopOnce sync.Once
	stopped  chan struct{}
}

type acquireGateClock struct {
	now     time.Time
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func newAcquireGateClock() *acquireGateClock {
	return &acquireGateClock{
		now:     time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		entered: make(chan struct{}),
		resume:  make(chan struct{}),
	}
}

func (c *acquireGateClock) Now() time.Time {
	c.once.Do(func() {
		close(c.entered)
		<-c.resume
	})
	return c.now
}

func (c *acquireGateClock) NewTicker(time.Duration) Ticker {
	return &lifecycleTicker{ch: make(chan time.Time), stopped: make(chan struct{})}
}

func (t *lifecycleTicker) C() <-chan time.Time { return t.ch }

func (t *lifecycleTicker) Stop() {
	t.stopOnce.Do(func() { close(t.stopped) })
}

func TestServiceStrict_CleanupNilWorkerErrorPostErrorRepeatedAfterAndTrace(t *testing.T) {
	newRequest := func(t *testing.T, service *Service, scope string) (context.Context, *RequestState, *canonical.ChatRequest) {
		t.Helper()
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scope})
		ctx := WithRequestState(context.Background(), state)
		req := &canonical.ChatRequest{}
		if _, err := service.Before(ctx, req); err != nil {
			t.Fatalf("Before: %v", err)
		}
		return ctx, state, req
	}

	t.Run("nil response and worker error cleanup", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		ctx, state, req := newRequest(t, service, "cleanup-nil")
		if err := service.After(ctx, req, nil); err != nil {
			t.Fatalf("After(nil): %v", err)
		}
		if state.scopeLease() != nil || service.store.Snapshot().RequestsInFlight != 0 {
			t.Fatal("nil worker response retained lease")
		}
		receipt, _ := decodeStateReceipt(t, state)
		if receipt.Result != "error" || receipt.Coverage != "full" {
			t.Fatalf("worker-error receipt=%+v", receipt)
		}
	})

	t.Run("post hook failure and repeated After release once", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{secret: NewSecretClassifier()})
		ctx, _, req := newRequest(t, service, "cleanup-post-error")
		resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText, Text: "api_key=sk-abcdefghijklmnopqrstuvwxyz123456",
		}}}}
		assertPrivacyError(t, service.After(ctx, req, resp), CodeOutputBlocked, "output")
		if got := service.store.Snapshot().RequestsInFlight; got != 0 {
			t.Fatalf("post-hook error retained %d leases", got)
		}
		if err := service.After(ctx, req, resp); err != nil {
			t.Fatalf("repeated After: %v", err)
		}
		if got := service.store.Snapshot().RequestsInFlight; got != 0 {
			t.Fatalf("repeated After changed in-flight to %d", got)
		}
	})

	t.Run("strict trace is bounded metadata only", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		ctx, _, req := newRequest(t, service, "cleanup-trace")
		if service.AllowSensitiveTrace(ctx) {
			t.Fatal("strict trace allowed sensitive content")
		}
		summary := service.TraceSummary(ctx)
		joined := ""
		for key, value := range summary {
			joined += key + "=" + fmt.Sprint(value) + ";"
		}
		for _, forbidden := range []string{"original", "synthetic", "mapping", "token", "secret"} {
			if strings.Contains(strings.ToLower(joined), forbidden) {
				t.Fatalf("trace summary leaked forbidden metadata: %q", joined)
			}
		}
		if err := service.After(ctx, req, &canonical.ChatResponse{}); err != nil {
			t.Fatalf("After: %v", err)
		}
	})

	t.Run("concurrent repeated After has one terminal owner", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		ctx, _, req := newRequest(t, service, "cleanup-concurrent-after")
		resp := &canonical.ChatResponse{}
		start := make(chan struct{})
		errs := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				errs <- service.After(ctx, req, resp)
			}()
		}
		close(start)
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent After: %v", err)
			}
		}
		if snapshot := service.store.Snapshot(); snapshot.RequestsInFlight != 0 {
			t.Fatalf("concurrent After retained %d leases", snapshot.RequestsInFlight)
		}
	})
}

func TestServiceStrict_CleanupReaperIntervalShutdownAndJoin(t *testing.T) {
	tests := []struct {
		ttl  time.Duration
		want time.Duration
	}{
		{ttl: 50 * time.Millisecond, want: 100 * time.Millisecond},
		{ttl: 10 * time.Second, want: 5 * time.Second},
		{ttl: 4 * time.Hour, want: time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.ttl.String(), func(t *testing.T) {
			clock := newLifecycleClock()
			service := newLifecycleTestService(t, clock, tc.ttl)
			if clock.interval != tc.want {
				t.Fatalf("reaper interval=%s, want %s", clock.interval, tc.want)
			}
			service.Close()
			service.Close()
			select {
			case <-clock.ticker.stopped:
			default:
				t.Fatal("Close did not stop ticker")
			}
			select {
			case <-service.reaperDone:
			default:
				t.Fatal("Close did not join reaper")
			}
		})
	}
}

func TestServicePrivacyObservers_ReentrantClosedObserverDoesNotDeadlock(t *testing.T) {
	clock := newLifecycleClock()
	var service *Service
	var closedEvents atomic.Int32
	observer := func(event string) {
		if event != "closed" {
			return
		}
		closedEvents.Add(1)
		service.Close()
	}
	var err error
	service, err = NewService(Config{
		DefaultProfile: ProfileStandard, RequestProfiles: []Profile{ProfileStandard, ProfileStrict},
		AliasKey: []byte("reentrant-close-key"), SecretAction: ActionReplace, TechnicalAction: ActionPseudonymize,
		ScopeTTL: time.Hour, MaxScopes: 2, MaxEntriesPerScope: 8, MaxTotalEntries: 16,
		PIIEnabled: true, PIIMode: ActionReplace, PIIEntityActions: map[string]Action{},
		Clock: clock, Observers: Observers{ScopeEvent: observer},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "reentrant-close"})
	ctx := WithRequestState(context.Background(), state)
	if _, err := service.Before(ctx, &canonical.ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := service.After(ctx, &canonical.ChatRequest{}, &canonical.ChatResponse{}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		service.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close deadlocked when the closed observer reentered Close")
	}
	service.Close()
	if got := closedEvents.Load(); got != 1 {
		t.Fatalf("closed observer events=%d, want exactly 1", got)
	}
}

func TestServiceStrict_ClearRaceActiveFinishesClosedRejectsAndFinalReleaseWipes(t *testing.T) {
	clock := newLifecycleClock()
	service := newLifecycleTestService(t, clock, time.Hour)
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "clear-race"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "10.20.30.40"}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	alias := req.System
	if result, err := service.store.Clear("clear-race"); err != nil || result != ClearClosing {
		t.Fatalf("Clear=(%q,%v), want closing", result, err)
	}
	newState := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "clear-race"})
	_, err := service.Before(WithRequestState(context.Background(), newState), &canonical.ChatRequest{})
	assertPrivacyError(t, err, CodeScopeClosed, "scope")
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: alias}}}}
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("active After: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != "10.20.30.40" {
		t.Fatalf("active response=%q, want restored original", got)
	}
	if _, err := service.store.Inspect("clear-race"); !errors.Is(err, errScopeNotFound) {
		t.Fatalf("final release Inspect error=%v, want not found", err)
	}
}

func TestServiceStrict_ExpiryRaceActiveSurvivesThenIdleReaperWipes(t *testing.T) {
	clock := newLifecycleClock()
	service := newLifecycleTestService(t, clock, time.Second)
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "expiry-race"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "10.20.30.40"}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	clock.Advance(2 * time.Second)
	waitForCondition(t, func() bool { return service.store.Snapshot().ScopesActive == 1 })
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: req.System}}}}
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("After active expiry: %v", err)
	}
	if got := service.store.Snapshot().RequestsInFlight; got != 0 {
		t.Fatalf("after active expiry in-flight=%d", got)
	}
	clock.Advance(2 * time.Second)
	waitForCondition(t, func() bool { return service.store.Snapshot().ScopesActive == 0 })
}

func TestServicePrivacyObservers_OpportunisticExpiryOutcomesAreExactlyOnce(t *testing.T) {
	newService := func(t *testing.T, clock Clock, observer func(string)) *Service {
		t.Helper()
		service, err := NewService(Config{
			DefaultProfile: ProfileStandard, RequestProfiles: []Profile{ProfileStandard, ProfileStrict},
			AliasKey: []byte("expiry-observer-key"), SecretAction: ActionReplace, TechnicalAction: ActionPseudonymize,
			ScopeTTL: time.Second, MaxScopes: 1, MaxEntriesPerScope: 8, MaxTotalEntries: 16,
			PIIEnabled: true, PIIMode: ActionReplace, PIIEntityActions: map[string]Action{},
			Clock: clock, Observers: Observers{ScopeEvent: observer},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(service.Close)
		return service
	}
	runRequest := func(t *testing.T, service *Service, scopeID string) error {
		t.Helper()
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scopeID})
		ctx := WithRequestState(context.Background(), state)
		req := &canonical.ChatRequest{}
		if _, err := service.Before(ctx, req); err != nil {
			return err
		}
		return service.After(ctx, req, &canonical.ChatResponse{})
	}

	t.Run("capacity reclaim", func(t *testing.T) {
		clock := newLifecycleClock()
		var mu sync.Mutex
		var events []string
		service := newService(t, clock, func(event string) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		})
		if err := runRequest(t, service, "expiry-capacity-old"); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		events = nil
		mu.Unlock()
		clock.AdvanceSilently(2 * time.Second)
		if err := runRequest(t, service, "expiry-capacity-new"); err != nil {
			t.Fatal(err)
		}
		clock.Tick()
		waitForCondition(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(events) >= 2
		})
		mu.Lock()
		defer mu.Unlock()
		if fmt.Sprint(events) != fmt.Sprint([]string{"expired", "created"}) {
			t.Fatalf("scope events=%v, want [expired created]", events)
		}
		if len(service.observedScopes) != 1 {
			t.Fatalf("observed scope identities=%d, want 1 retained identity", len(service.observedScopes))
		}
	})

	t.Run("same ID reacquire", func(t *testing.T) {
		clock := newLifecycleClock()
		var mu sync.Mutex
		var events []string
		service := newService(t, clock, func(event string) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		})
		if err := runRequest(t, service, "expiry-same-id"); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		events = nil
		mu.Unlock()
		clock.AdvanceSilently(2 * time.Second)
		err := runRequest(t, service, "expiry-same-id")
		assertPrivacyError(t, err, CodeScopeClosed, "scope")
		clock.Tick()
		mu.Lock()
		defer mu.Unlock()
		if fmt.Sprint(events) != fmt.Sprint([]string{"expired"}) {
			t.Fatalf("scope events=%v, want [expired]", events)
		}
		if len(service.observedScopes) != 0 {
			t.Fatalf("observed scope identities=%d, want 0", len(service.observedScopes))
		}
	})

	t.Run("nil observer retains no identity bookkeeping", func(t *testing.T) {
		service := newService(t, newLifecycleClock(), nil)
		if err := runRequest(t, service, "expiry-no-observer"); err != nil {
			t.Fatal(err)
		}
		if service.observedScopes != nil {
			t.Fatalf("observedScopes=%v, want nil when ScopeEvent is nil", service.observedScopes)
		}
	})
}

func TestServiceStrict_CleanupShutdownActiveAllowsFinishAndRejectsNewAcquire(t *testing.T) {
	clock := newLifecycleClock()
	service := newLifecycleTestService(t, clock, time.Hour)
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "shutdown-active"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "10.20.30.40"}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	alias := req.System
	service.Close()
	newState := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "shutdown-new"})
	_, err := service.Before(WithRequestState(context.Background(), newState), &canonical.ChatRequest{})
	assertPrivacyError(t, err, CodeScopeClosed, "scope")
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: alias}}}}
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("active After during shutdown: %v", err)
	}
	if snapshot := service.store.Snapshot(); snapshot.ScopesActive != 0 || snapshot.RequestsInFlight != 0 || snapshot.Entries != 0 {
		t.Fatalf("shutdown snapshot=%+v", snapshot)
	}
}

func TestServiceStrict_CleanupCloseAcquireRaceRejectsPausedAcquisition(t *testing.T) {
	clock := newAcquireGateClock()
	service := newLifecycleTestService(t, clock, time.Hour)
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "shutdown-paused-acquire"})
	ctx := WithRequestState(context.Background(), state)
	beforeErr := make(chan error, 1)
	go func() {
		_, err := service.Before(ctx, &canonical.ChatRequest{})
		beforeErr <- err
	}()

	<-clock.entered
	service.Close()
	close(clock.resume)
	err := <-beforeErr
	assertPrivacyError(t, err, CodeScopeClosed, "scope")
	if snapshot := service.store.Snapshot(); snapshot.ScopesActive != 0 || snapshot.RequestsInFlight != 0 || snapshot.Entries != 0 {
		t.Fatalf("post-close acquire retained state: %+v", snapshot)
	}
}

func TestServiceStrict_AllowSensitiveTraceDeniesUnresolvedAndEarlyStrictErrors(t *testing.T) {
	t.Run("before Before and missing state", func(t *testing.T) {
		standard := newStrictTestService(t, strictTestConfig{})
		strictRequested := NewRequestState(RequestMetadata{RequestedProfile: "strict"})
		if standard.AllowSensitiveTrace(WithRequestState(context.Background(), strictRequested)) {
			t.Fatal("requested strict was allowed before Before")
		}
		if standard.AllowSensitiveTrace(context.Background()) {
			t.Fatal("missing request state was allowed")
		}

		strictDefault := newStrictTestService(t, strictTestConfig{defaultProfile: ProfileStrict})
		for _, requested := range []string{"", "standard"} {
			state := NewRequestState(RequestMetadata{RequestedProfile: requested})
			if strictDefault.AllowSensitiveTrace(WithRequestState(context.Background(), state)) {
				t.Fatalf("strict default allowed requested profile %q before Before", requested)
			}
		}
	})

	t.Run("unknown and invalid strict metadata", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		unknown := NewRequestState(RequestMetadata{RequestedProfile: "maximum"})
		unknownCtx := WithRequestState(context.Background(), unknown)
		_, err := service.Before(unknownCtx, &canonical.ChatRequest{})
		assertPrivacyError(t, err, CodeProfileUnavailable, "profile")
		if service.AllowSensitiveTrace(unknownCtx) {
			t.Fatal("unknown profile error was allowed")
		}

		invalid := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "invalid scope"})
		invalidCtx := WithRequestState(context.Background(), invalid)
		_, err = service.Before(invalidCtx, &canonical.ChatRequest{})
		assertPrivacyError(t, err, CodeRequestInvalid, "scope")
		if service.AllowSensitiveTrace(invalidCtx) {
			t.Fatal("invalid strict scope was allowed")
		}
	})

	t.Run("closed and capacity strict scope errors", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		closed := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "trace-closed"})
		closedCtx := WithRequestState(context.Background(), closed)
		if _, err := service.Before(closedCtx, &canonical.ChatRequest{}); err != nil {
			t.Fatalf("seed closed scope: %v", err)
		}
		closed.releaseLease()
		if _, err := service.store.Clear("trace-closed"); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		reuse := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "trace-closed"})
		reuseCtx := WithRequestState(context.Background(), reuse)
		_, err := service.Before(reuseCtx, &canonical.ChatRequest{})
		assertPrivacyError(t, err, CodeScopeClosed, "scope")
		if service.AllowSensitiveTrace(reuseCtx) {
			t.Fatal("closed strict scope was allowed")
		}

		var held []*RequestState
		for index := range 32 {
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: fmt.Sprintf("trace-capacity-%02d", index)})
			if _, err := service.Before(WithRequestState(context.Background(), state), &canonical.ChatRequest{}); err != nil {
				t.Fatalf("fill capacity %d: %v", index, err)
			}
			held = append(held, state)
		}
		defer func() {
			for _, state := range held {
				state.releaseLease()
			}
		}()
		capacity := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "trace-capacity-rejected"})
		capacityCtx := WithRequestState(context.Background(), capacity)
		_, err = service.Before(capacityCtx, &canonical.ChatRequest{})
		assertPrivacyError(t, err, CodeCapacityExceeded, "scope")
		if service.AllowSensitiveTrace(capacityCtx) {
			t.Fatal("capacity-rejected strict scope was allowed")
		}
	})

	t.Run("trusted standard compatibility", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		defaultState := NewRequestState(RequestMetadata{})
		defaultCtx := WithRequestState(context.Background(), defaultState)
		if !service.AllowSensitiveTrace(defaultCtx) {
			t.Fatal("default standard was denied before Before")
		}
		if _, err := service.Before(defaultCtx, &canonical.ChatRequest{}); err != nil {
			t.Fatalf("default standard Before: %v", err)
		}
		if !service.AllowSensitiveTrace(defaultCtx) {
			t.Fatal("resolved default standard was denied")
		}

		state := NewRequestState(RequestMetadata{RequestedProfile: "standard"})
		ctx := WithRequestState(context.Background(), state)
		if !service.AllowSensitiveTrace(ctx) {
			t.Fatal("explicit standard was denied before Before")
		}
		if _, err := service.Before(ctx, &canonical.ChatRequest{}); err != nil {
			t.Fatalf("standard Before: %v", err)
		}
		if !service.AllowSensitiveTrace(ctx) {
			t.Fatal("resolved standard was denied")
		}
	})
}

func newLifecycleTestService(t *testing.T, clock Clock, ttl time.Duration) *Service {
	t.Helper()
	service, err := NewService(Config{
		DefaultProfile: ProfileStandard, RequestProfiles: []Profile{ProfileStandard, ProfileStrict},
		AliasKey: []byte("lifecycle-alias-key"), SecretAction: ActionReplace, TechnicalAction: ActionPseudonymize,
		ScopeTTL: ttl, MaxScopes: 8, MaxEntriesPerScope: 32, MaxTotalEntries: 128,
		PIIEnabled: true, PIIMode: ActionReplace, PIIEntityActions: map[string]Action{},
		Classifier: outboundFixtureClassifier{}, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
