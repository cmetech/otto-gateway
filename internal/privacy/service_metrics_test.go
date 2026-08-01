package privacy

import (
	"context"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

type privacyObserverEvents struct {
	mu       sync.Mutex
	requests []string
	blocks   int
	residual int
	receipts []string
	internal int
	capacity []string
	scopes   []string
	mapping  []string
	duration map[string]int
}

func (e *privacyObserverEvents) observers() Observers {
	return Observers{
		Request: func(_ Profile, _, _, result string) {
			e.mu.Lock()
			e.requests = append(e.requests, result)
			e.mu.Unlock()
		},
		Block: func(Profile, string, string) {
			e.mu.Lock()
			e.blocks++
			e.mu.Unlock()
		},
		Residual: func(Profile, string, string) {
			e.mu.Lock()
			e.residual++
			e.mu.Unlock()
		},
		Receipt: func(_ Profile, result string) {
			e.mu.Lock()
			e.receipts = append(e.receipts, result)
			e.mu.Unlock()
		},
		InternalError: func(string, string) {
			e.mu.Lock()
			e.internal++
			e.mu.Unlock()
		},
		CapacityRejection: func(resource string) {
			e.mu.Lock()
			e.capacity = append(e.capacity, resource)
			e.mu.Unlock()
		},
		ScopeEvent: func(event string) {
			e.mu.Lock()
			e.scopes = append(e.scopes, event)
			e.mu.Unlock()
		},
		MappingOperation: func(operation, result string) {
			e.mu.Lock()
			e.mapping = append(e.mapping, operation+":"+result)
			e.mu.Unlock()
		},
		Duration: func(_ Profile, stage string, _ time.Duration) {
			e.mu.Lock()
			if e.duration == nil {
				e.duration = make(map[string]int)
			}
			e.duration[stage]++
			e.mu.Unlock()
		},
	}
}

func TestServicePrivacyObservers_ExactlyOnceForPassAndMappingLifecycle(t *testing.T) {
	events := &privacyObserverEvents{}
	service := newStrictTestService(t, strictTestConfig{
		classifier: outboundFixtureClassifier{},
		observers:  events.observers(),
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-pass"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "10.20.30.40"}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	alias := req.System
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
		Kind: canonical.ContentKindText,
		Text: alias + " 10.20.30.41",
	}}}}
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	service.Close()

	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.requests) != 1 || events.requests[0] != "pass" {
		t.Fatalf("request events=%v, want [pass]", events.requests)
	}
	if len(events.receipts) != 1 || events.receipts[0] != "pass" {
		t.Fatalf("receipt events=%v, want [pass]", events.receipts)
	}
	if got := events.scopes; len(got) != 2 || got[0] != "created" || got[1] != "closed" {
		t.Fatalf("scope events=%v, want [created closed]", got)
	}
	wantMapping := []string{
		"lookup:miss", "insert:pass",
		"lookup:miss", "insert:pass",
		"restore:pass",
	}
	if len(events.mapping) != len(wantMapping) {
		t.Fatalf("mapping events=%v, want %v", events.mapping, wantMapping)
	}
	for index := range wantMapping {
		if events.mapping[index] != wantMapping[index] {
			t.Fatalf("mapping events=%v, want %v", events.mapping, wantMapping)
		}
	}
	for _, stage := range []string{"transform", "verify", "restore"} {
		if events.duration[stage] == 0 {
			t.Errorf("missing %s duration event: %v", stage, events.duration)
		}
	}
}

func TestServicePrivacyObservers_ExactlyOnceForBlockErrorAndCapacity(t *testing.T) {
	t.Run("block", func(t *testing.T) {
		events := &privacyObserverEvents{}
		classifier := &residualPassClassifier{
			first: func(string) []Finding { return nil },
			residual: func(value string) []Finding {
				return exactFinding(value, "raw-protected", "PERSON", CategoryPersonal, MatchNER)
			},
		}
		service := newStrictTestService(t, strictTestConfig{classifier: classifier, observers: events.observers()})
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-block"})
		_, err := service.Before(WithRequestState(context.Background(), state), &canonical.ChatRequest{System: "raw-protected"})
		assertPrivacyError(t, err, CodeInputBlocked, "input")

		events.mu.Lock()
		defer events.mu.Unlock()
		if len(events.requests) != 1 || events.requests[0] != "block" || events.blocks != 1 || events.residual != 1 {
			t.Fatalf("block observer events=%+v", events)
		}
		if len(events.receipts) != 1 || events.receipts[0] != "block" {
			t.Fatalf("block receipt events=%v", events.receipts)
		}
	})

	t.Run("internal error", func(t *testing.T) {
		events := &privacyObserverEvents{}
		service := newStrictTestService(t, strictTestConfig{
			classifier: panickingPrivacyClassifier{payload: "metrics-error-canary"},
			observers:  events.observers(),
		})
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-error"})
		_, err := service.Before(WithRequestState(context.Background(), state), &canonical.ChatRequest{System: "panic"})
		assertPrivacyError(t, err, CodeInternalError, "input")

		events.mu.Lock()
		defer events.mu.Unlock()
		if len(events.requests) != 1 || events.requests[0] != "error" || events.internal != 1 {
			t.Fatalf("internal observer events=%+v", events)
		}
		if len(events.receipts) != 1 || events.receipts[0] != "error" {
			t.Fatalf("internal receipt events=%v", events.receipts)
		}
	})

	t.Run("scope capacity", func(t *testing.T) {
		events := &privacyObserverEvents{}
		service := newStrictTestService(t, strictTestConfig{maxScopes: 1, observers: events.observers()})
		first := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-capacity-first"})
		if _, err := service.Before(WithRequestState(context.Background(), first), &canonical.ChatRequest{}); err != nil {
			t.Fatalf("first Before: %v", err)
		}
		if err := service.After(WithRequestState(context.Background(), first), &canonical.ChatRequest{}, &canonical.ChatResponse{}); err != nil {
			t.Fatalf("first After: %v", err)
		}
		second := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-capacity-second"})
		_, err := service.Before(WithRequestState(context.Background(), second), &canonical.ChatRequest{})
		assertPrivacyError(t, err, CodeCapacityExceeded, "scope")

		events.mu.Lock()
		defer events.mu.Unlock()
		if len(events.capacity) != 1 || events.capacity[0] != "scope" {
			t.Fatalf("capacity events=%v, want [scope]", events.capacity)
		}
		if len(events.requests) != 2 || events.requests[0] != "pass" || events.requests[1] != "error" {
			t.Fatalf("capacity request outcomes=%v, want [pass error]", events.requests)
		}
	})
}

func TestServicePrivacyObservers_ConcurrentScopeCreatedExactlyOnce(t *testing.T) {
	events := &privacyObserverEvents{}
	service := newStrictTestService(t, strictTestConfig{observers: events.observers()})
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-shared"})
			ctx := WithRequestState(context.Background(), state)
			if _, err := service.Before(ctx, &canonical.ChatRequest{}); err != nil {
				t.Errorf("Before: %v", err)
				return
			}
			if err := service.After(ctx, &canonical.ChatRequest{}, &canonical.ChatResponse{}); err != nil {
				t.Errorf("After: %v", err)
			}
		}()
	}
	close(start)
	workers.Wait()

	events.mu.Lock()
	defer events.mu.Unlock()
	created := 0
	for _, event := range events.scopes {
		if event == "created" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("concurrent created events=%d (%v), want exactly 1", created, events.scopes)
	}
}
