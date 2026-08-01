package privacy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

type lifecycleTicker struct {
	ch       chan time.Time
	stopOnce sync.Once
	stopped  chan struct{}
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
