package privacy

import (
	"context"
	"fmt"
	"strings"
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
	restores []string
	duration map[string]int
}

type gatedMappingObserver struct {
	ready   sync.WaitGroup
	release chan struct{}
}

func newGatedMappingObserver(workers int) *gatedMappingObserver {
	mapper := &gatedMappingObserver{release: make(chan struct{})}
	mapper.ready.Add(workers)
	return mapper
}

func (m *gatedMappingObserver) Map(lease *ScopeLease, entity, original string, provenance Provenance) (string, error) {
	m.ready.Done()
	<-m.release
	entry, _, err := lease.GetOrCreate(entity, original, provenance, fixedCandidate("198.18.0.1"))
	return entry.Synthetic, err
}

func (m *gatedMappingObserver) mapObserved(lease *ScopeLease, entity, original string, provenance Provenance) (string, mappingOutcome, error) {
	m.ready.Done()
	<-m.release
	entry, created, err := lease.GetOrCreate(entity, original, provenance, fixedCandidate("198.18.0.1"))
	return entry.Synthetic, mappingOutcome{created: created}, err
}

type collidingMappingObserver struct{}

type exactTechnicalObserverClassifier struct {
	entity string
	value  string
}

func (c exactTechnicalObserverClassifier) Classify(_ string, value string) []Finding {
	return exactFinding(value, c.value, c.entity, CategoryTechnical, MatchValidatedRegex)
}

func (collidingMappingObserver) Map(lease *ScopeLease, entity, original string, provenance Provenance) (string, error) {
	entry, _, err := lease.GetOrCreate(entity, original, provenance, func(attempt uint32) (string, error) {
		if attempt == 0 {
			return "reserved-alias", nil
		}
		return "accepted-alias", nil
	})
	return entry.Synthetic, err
}

func (collidingMappingObserver) mapObserved(lease *ScopeLease, entity, original string, provenance Provenance) (string, mappingOutcome, error) {
	entry, created, err := lease.GetOrCreate(entity, original, provenance, func(attempt uint32) (string, error) {
		if attempt == 0 {
			return "reserved-alias", nil
		}
		return "accepted-alias", nil
	})
	return entry.Synthetic, mappingOutcome{created: created, collisions: 1}, err
}

func TestServicePrivacyObservers_MappingOutcomeIsAtomicUnderConcurrency(t *testing.T) {
	const workers = 64
	events := &privacyObserverEvents{}
	service := newStrictTestService(t, strictTestConfig{observers: events.observers()})
	mapper := newGatedMappingObserver(workers)
	service.mapper = mapper

	leases := make([]*ScopeLease, workers)
	for index := range leases {
		lease, err := service.store.Acquire("metrics-mapping-atomic", ProfileStrict)
		if err != nil {
			t.Fatalf("Acquire(%d): %v", index, err)
		}
		leases[index] = lease
		defer lease.Release()
	}

	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range workers {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			_, _, _, err := service.strictReplacement(
				NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-mapping-atomic"}),
				leases[index],
				Finding{Entity: "IPv4", Category: CategoryTechnical, Kind: MatchValidatedRegex},
				"10.20.30.40",
				map[string]int{},
				map[string]int{},
			)
			if err != nil {
				t.Errorf("strictReplacement(%d): %v", index, err)
			}
		}(index)
	}
	close(start)
	mapper.ready.Wait()
	close(mapper.release)
	group.Wait()

	events.mu.Lock()
	defer events.mu.Unlock()
	counts := map[string]int{}
	for _, event := range events.mapping {
		counts[event]++
	}
	if counts["lookup:miss"] != 1 || counts["insert:pass"] != 1 || counts["lookup:hit"] != 63 {
		t.Fatalf("mapping event counts=%v, want lookup:miss=1 insert:pass=1 lookup:hit=63", counts)
	}
}

func TestServicePrivacyObservers_MappingCollisionIsReportedBeforeInsert(t *testing.T) {
	events := &privacyObserverEvents{}
	service := newStrictTestService(t, strictTestConfig{observers: events.observers()})
	lease, err := service.store.Acquire("metrics-mapping-collision", ProfileStrict)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, _, err := lease.GetOrCreate("IPv4", "seed", ProvenanceInput, fixedCandidate("reserved-alias")); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	service.mapper = collidingMappingObserver{}

	got, _, _, err := service.strictReplacement(
		NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-mapping-collision"}),
		lease,
		Finding{Entity: "IPv4", Category: CategoryTechnical, Kind: MatchValidatedRegex},
		"10.20.30.40",
		map[string]int{},
		map[string]int{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "accepted-alias" {
		t.Fatalf("mapping=%q, want accepted-alias", got)
	}

	events.mu.Lock()
	defer events.mu.Unlock()
	want := []string{"lookup:miss", "insert:collision", "insert:pass"}
	if fmt.Sprint(events.mapping) != fmt.Sprint(want) {
		t.Fatalf("mapping events=%v, want %v", events.mapping, want)
	}
}

func TestServicePrivacyObservers_CoordinateRegistryCollisionIsReported(t *testing.T) {
	events := &privacyObserverEvents{}
	service := newStrictTestService(t, strictTestConfig{observers: events.observers()})
	seed, err := service.store.Acquire("metrics-coordinate-seed", ProfileStrict)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Release()
	target, err := service.store.Acquire("metrics-coordinate-target", ProfileStrict)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Release()
	mapper := service.mapper.(*TechnicalMapper)
	collisionIndex := uint16(mapper.coordinateRotationCandidate(target.scope.id, 0))
	if _, err := service.store.coordinateRotationIndex(seed.scope.id, fixedRotationCandidate(collisionIndex)); err != nil {
		t.Fatalf("seed rotation: %v", err)
	}

	if _, _, _, err := service.strictReplacement(
		NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: target.scope.id}),
		target,
		Finding{Entity: "COORDINATES", Category: CategoryTechnical, Kind: MatchValidatedRegex},
		"40.7128N, 74.0060W",
		map[string]int{},
		map[string]int{},
	); err != nil {
		t.Fatal(err)
	}

	events.mu.Lock()
	defer events.mu.Unlock()
	want := []string{"lookup:miss", "insert:collision", "insert:pass"}
	if fmt.Sprint(events.mapping) != fmt.Sprint(want) {
		t.Fatalf("mapping events=%v, want %v", events.mapping, want)
	}
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
		Restoration: func(profile Profile, entity, result string) {
			e.mu.Lock()
			e.restores = append(e.restores, string(profile)+":"+entity+":"+result)
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

func TestServicePrivacyObservers_StandardRestorationAttemptOutcomes(t *testing.T) {
	key := mustEncryptionKey(t, "metrics-standard-restoration")
	events := &privacyObserverEvents{}
	service := newStandardTestService(t, ActionEncrypt, key)
	service.config.Observers = events.observers()
	wrapper, err := EncryptValue(key, "Email", "corey@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, payload, ok := ParseEncryptedToken(wrapper)
	if !ok {
		t.Fatalf("ParseEncryptedToken(%q)", wrapper)
	}
	invalid, err := EncryptValue(mustEncryptionKey(t, "metrics-standard-restoration-wrong-key"), "Email", "corey@example.com")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "wrapped pass", value: wrapper, want: "corey@example.com"},
		{name: "bare pass", value: payload, want: "corey@example.com"},
		{name: "valid miss", value: invalid, want: invalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events.mu.Lock()
			events.restores = nil
			events.mu.Unlock()
			if got := service.restoreStandardValue(nil, []string{"Email"}, test.value); got != test.want {
				t.Fatalf("restoreStandardValue()=%q, want %q", got, test.want)
			}
			events.mu.Lock()
			defer events.mu.Unlock()
			if len(events.restores) != 1 {
				t.Fatalf("restoration events=%v, want exactly one", events.restores)
			}
			wantResult := ":pass"
			if test.name == "valid miss" {
				wantResult = ":miss"
			}
			if !strings.HasSuffix(events.restores[0], wantResult) {
				t.Fatalf("restoration event=%q, want suffix %q", events.restores[0], wantResult)
			}
			if strings.Contains(events.restores[0], "corey@example.com") || strings.Contains(events.restores[0], payload) {
				t.Fatalf("restoration event leaked protected value: %q", events.restores[0])
			}
		})
	}
}

func TestServicePrivacyObservers_StandardMixedBareAndWrappedRestoreInSourceOrder(t *testing.T) {
	key := mustEncryptionKey(t, "metrics-standard-mixed-restoration")
	events := &privacyObserverEvents{}
	service := newStandardTestService(t, ActionEncrypt, key)
	service.config.Observers = events.observers()
	bareWrapper, err := EncryptValue(key, "Email", "first@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, bare, ok := ParseEncryptedToken(bareWrapper)
	if !ok {
		t.Fatal("failed to parse bare fixture")
	}
	wrapped, err := EncryptValue(key, "Email", "second@example.com")
	if err != nil {
		t.Fatal(err)
	}
	value := bare + " then " + wrapped
	if got := service.restoreStandardValue(nil, []string{"Email"}, value); got != "first@example.com then second@example.com" {
		t.Fatalf("mixed restoration=%q", got)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.restores) != 2 {
		t.Fatalf("restoration events=%v, want two attempts", events.restores)
	}
}

func TestServicePrivacyObservers_StrictRestorationPassAndRejectedAttempts(t *testing.T) {
	tests := []struct {
		name      string
		response  func(t *testing.T, service *Service, requestToken string) string
		wantError bool
		want      string
	}{
		{name: "authorized pass", response: func(_ *testing.T, _ *Service, token string) string { return token }, want: ":pass"},
		{name: "invented", response: func(_ *testing.T, _ *Service, _ string) string {
			return "[PII:Email:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA]"
		}, wantError: true, want: ":rejected"},
		{name: "malformed", response: func(_ *testing.T, _ *Service, _ string) string {
			return "[PII:Email:not-valid?]"
		}, wantError: true, want: ":rejected"},
		{name: "unauthorized", response: func(t *testing.T, service *Service, _ string) string {
			token, err := EncryptValue(service.config.PIIEncryptKey, "Email", "outsider@example.com")
			if err != nil {
				t.Fatal(err)
			}
			return token
		}, wantError: true, want: ":rejected"},
		{name: "unauthorized bare", response: func(t *testing.T, service *Service, _ string) string {
			token, err := EncryptValue(service.config.PIIEncryptKey, "Email", "outsider@example.com")
			if err != nil {
				t.Fatal(err)
			}
			_, payload, ok := ParseEncryptedToken(token)
			if !ok {
				t.Fatal("failed to parse unauthorized bare fixture")
			}
			return payload
		}, wantError: true, want: ":rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := &privacyObserverEvents{}
			service := newStrictTestService(t, strictTestConfig{
				classifier:  outboundFixtureClassifier{},
				piiMode:     ActionEncrypt,
				recognizers: []string{"Email"},
				observers:   events.observers(),
			})
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-restore-" + strings.ReplaceAll(test.name, " ", "-")})
			ctx := WithRequestState(context.Background(), state)
			req := &canonical.ChatRequest{System: "corey@example.com"}
			if _, err := service.Before(ctx, req); err != nil {
				t.Fatalf("Before: %v", err)
			}
			output := test.response(t, service, req.System)
			resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: output}}}}
			err := service.After(ctx, req, resp)
			if test.wantError {
				assertPrivacyError(t, err, CodeOutputBlocked, "output")
			} else if err != nil {
				t.Fatalf("After: %v", err)
			}
			events.mu.Lock()
			defer events.mu.Unlock()
			if len(events.restores) != 1 || !strings.HasSuffix(events.restores[0], test.want) {
				t.Fatalf("restoration events=%v, want exactly one *%s", events.restores, test.want)
			}
			if strings.Contains(events.restores[0], "corey@example.com") || strings.Contains(events.restores[0], output) {
				t.Fatalf("restoration event leaked protected value: %q", events.restores[0])
			}
		})
	}
}

func TestServicePrivacyObservers_InventedReservedTechnicalAliasIsRejected(t *testing.T) {
	tests := []struct {
		name   string
		entity string
		alias  string
	}{
		{name: "IPv4", entity: "IPv4", alias: "198.18.1.10"},
		{name: "SIP", entity: "SIP_URI", alias: "sip:u-inventedalias@gw.invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := &privacyObserverEvents{}
			scopeCanary := "reserved-alias-scope-canary-" + strings.ToLower(test.name)
			service := newStrictTestService(t, strictTestConfig{
				classifier: exactTechnicalObserverClassifier{entity: test.entity, value: test.alias},
				observers:  events.observers(),
			})
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: scopeCanary})
			context := WithRequestState(context.Background(), state)
			request := &canonical.ChatRequest{}
			if _, err := service.Before(context, request); err != nil {
				t.Fatal(err)
			}
			response := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText, Text: test.alias,
			}}}}
			err := service.After(context, request, response)
			assertPrivacyError(t, err, CodeOutputBlocked, "output")

			events.mu.Lock()
			defer events.mu.Unlock()
			want := "strict:" + test.entity + ":rejected"
			if len(events.restores) != 1 || events.restores[0] != want {
				t.Fatalf("restoration events=%v, want [%s]", events.restores, want)
			}
			for _, event := range events.restores {
				if strings.Contains(event, ":pass") || strings.Contains(event, ":miss") ||
					strings.Contains(event, test.alias) || strings.Contains(event, scopeCanary) {
					t.Fatalf("restoration event leaked or used wrong outcome: %q", event)
				}
			}
		})
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

func TestServicePrivacyObservers_MappingCapacityReportsExactResource(t *testing.T) {
	tests := []struct {
		name       string
		maxEntries int
		maxTotal   int
		want       string
	}{
		{name: "per scope", maxEntries: 1, maxTotal: 8, want: "per_scope"},
		{name: "global", maxEntries: 8, maxTotal: 1, want: "global"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := &privacyObserverEvents{}
			service := newStrictTestService(t, strictTestConfig{
				classifier:      outboundFixtureClassifier{},
				observers:       events.observers(),
				maxEntries:      test.maxEntries,
				maxTotalEntries: test.maxTotal,
			})
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "metrics-capacity-" + test.want})
			_, err := service.Before(
				WithRequestState(context.Background(), state),
				&canonical.ChatRequest{System: "10.20.30.40"},
			)
			assertPrivacyError(t, err, CodeCapacityExceeded, "mapping")

			events.mu.Lock()
			defer events.mu.Unlock()
			if len(events.capacity) != 1 || events.capacity[0] != test.want {
				t.Fatalf("capacity events=%v, want [%s]", events.capacity, test.want)
			}
			if len(events.requests) != 1 || events.requests[0] != "error" {
				t.Fatalf("request events=%v, want [error]", events.requests)
			}
		})
	}
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
