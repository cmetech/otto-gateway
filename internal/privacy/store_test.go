package privacy

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) NewTicker(time.Duration) Ticker {
	return &fakeTicker{ch: make(chan time.Time)}
}

func (c *fakeClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(delta)
}

type fakeTicker struct {
	ch chan time.Time
}

func (t *fakeTicker) C() <-chan time.Time {
	return t.ch
}

func (t *fakeTicker) Stop() {}

func TestScopeStore_AcquisitionStabilityAndExpiryRefresh(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                10 * time.Minute,
		MaxScopes:          4,
		MaxEntriesPerScope: 8,
		MaxTotalEntries:    16,
	})

	leaseA := acquireTestScope(t, store, "scope-a")
	defer leaseA.Release()

	callbackCalls := 0
	first, created, err := leaseA.GetOrCreate("IP", "192.0.2.1", ProvenanceInput, func(attempt uint32) (string, error) {
		callbackCalls++
		return fmt.Sprintf("198.51.100.%d", attempt+1), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first GetOrCreate created=false, want true")
	}

	clock.Advance(6 * time.Minute)
	second, created, err := leaseA.GetOrCreate("IP", "192.0.2.1", ProvenanceInput, func(uint32) (string, error) {
		callbackCalls++
		return "must-not-run", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second GetOrCreate created=true, want false")
	}
	if second != first {
		t.Fatalf("stable entry=%+v, want %+v", second, first)
	}
	if callbackCalls != 1 {
		t.Fatalf("candidate calls=%d, want 1", callbackCalls)
	}

	resolved, ok := leaseA.ResolveSynthetic("IP", first.Synthetic)
	if !ok || resolved != first {
		t.Fatalf("ResolveSynthetic()=(%+v, %t), want (%+v, true)", resolved, ok, first)
	}

	info := findScopeInfo(t, store.List(), "scope-a")
	if info.Profile != ProfileStandard || info.State != "active" || info.Entries != 1 || info.InFlight != 1 {
		t.Fatalf("scope info=%+v", info)
	}
	if want := clock.Now().Add(10 * time.Minute); !info.ExpiresAt.Equal(want) {
		t.Fatalf("expiry=%v, want %v", info.ExpiresAt, want)
	}
	if !info.LastUsedAt.Equal(clock.Now()) {
		t.Fatalf("last used=%v, want %v", info.LastUsedAt, clock.Now())
	}

	leaseB := acquireTestScope(t, store, "scope-b")
	defer leaseB.Release()
	entryB, created, err := leaseB.GetOrCreate("IP", "192.0.2.1", ProvenanceInput, func(attempt uint32) (string, error) {
		return fmt.Sprintf("203.0.113.%d", attempt+1), nil
	})
	if err != nil || !created {
		t.Fatalf("cross-scope GetOrCreate() created=%t err=%v", created, err)
	}
	if entryB.Synthetic == first.Synthetic {
		t.Fatalf("cross-scope synthetic=%q, want independent from %q", entryB.Synthetic, first.Synthetic)
	}
}

func TestScopeStore_CollisionRetryAndCandidateFailureRollback(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          2,
		MaxEntriesPerScope: 3,
		MaxTotalEntries:    3,
	})
	lease := acquireTestScope(t, store, "collisions")
	defer lease.Release()

	_, _, err := lease.GetOrCreate("HOST", "source-a", ProvenanceGenerated, func(uint32) (string, error) {
		return "synthetic-a", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var attempts []uint32
	entry, created, err := lease.GetOrCreate("HOST", "source-b", ProvenanceGenerated, func(attempt uint32) (string, error) {
		attempts = append(attempts, attempt)
		if attempt == 0 {
			return "synthetic-a", nil
		}
		return "synthetic-b", nil
	})
	if err != nil || !created {
		t.Fatalf("collision GetOrCreate() created=%t err=%v", created, err)
	}
	if entry.Synthetic != "synthetic-b" || !slices.Equal(attempts, []uint32{0, 1}) {
		t.Fatalf("entry=%+v attempts=%v", entry, attempts)
	}

	candidateErr := errors.New("candidate failed")
	_, created, err = lease.GetOrCreate("HOST", "source-c", ProvenanceInput, func(uint32) (string, error) {
		return "", candidateErr
	})
	if !errors.Is(err, candidateErr) || created {
		t.Fatalf("candidate failure created=%t err=%v", created, err)
	}
	if got := store.Snapshot().Entries; got != 2 {
		t.Fatalf("entries after candidate failure=%d, want 2", got)
	}

	_, created, err = lease.GetOrCreate("HOST", "source-c", Provenance("invalid"), func(uint32) (string, error) {
		return "synthetic-c", nil
	})
	if err == nil || created {
		t.Fatalf("invalid provenance created=%t err=%v", created, err)
	}
	if got := store.Snapshot().Entries; got != 2 {
		t.Fatalf("entries after invalid provenance=%d, want 2", got)
	}
}

func TestScopeStore_ProvenancePromotesGeneratedToInput(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          1,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    1,
	})
	lease := acquireTestScope(t, store, "promotion")
	defer lease.Release()

	candidateCalls := 0
	generated, created, err := lease.GetOrCreate("IMSI", "310150123456789", ProvenanceGenerated, func(uint32) (string, error) {
		candidateCalls++
		return "001010987654321", nil
	})
	if err != nil || !created || generated.Provenance != ProvenanceGenerated {
		t.Fatalf("generated GetOrCreate()=(%+v, %t, %v)", generated, created, err)
	}
	clock.Advance(10 * time.Minute)

	promoted, created, err := lease.GetOrCreate("IMSI", "310150123456789", ProvenanceInput, func(uint32) (string, error) {
		candidateCalls++
		return "must-not-replace-alias", nil
	})
	if err != nil || created {
		t.Fatalf("input repeat GetOrCreate()=(%+v, %t, %v)", promoted, created, err)
	}
	if promoted.Provenance != ProvenanceInput {
		t.Fatalf("promoted provenance=%q, want %q", promoted.Provenance, ProvenanceInput)
	}
	if promoted.Synthetic != generated.Synthetic || !promoted.CreatedAt.Equal(generated.CreatedAt) {
		t.Fatalf("promotion changed stable entry: generated=%+v promoted=%+v", generated, promoted)
	}
	if candidateCalls != 1 {
		t.Fatalf("candidate calls=%d, want 1", candidateCalls)
	}
	if snapshot := store.Snapshot(); snapshot.Entries != 1 {
		t.Fatalf("entries after promotion=%d, want 1", snapshot.Entries)
	}

	reversed, ok := lease.ResolveSynthetic("IMSI", generated.Synthetic)
	if !ok || reversed != promoted {
		t.Fatalf("reverse after promotion=(%+v, %t), want (%+v, true)", reversed, ok, promoted)
	}
	inspected := mustInspect(t, store, "promotion")
	if len(inspected) != 1 || inspected[0] != promoted {
		t.Fatalf("forward after promotion=%+v, want [%+v]", inspected, promoted)
	}
}

func TestScopeStore_ProvenanceNeverDowngradesInput(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          1,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    1,
	})
	lease := acquireTestScope(t, store, "no-downgrade")
	defer lease.Release()

	candidateCalls := 0
	input, created, err := lease.GetOrCreate("IMEI", "490154203237518", ProvenanceInput, func(uint32) (string, error) {
		candidateCalls++
		return "100000000000009", nil
	})
	if err != nil || !created || input.Provenance != ProvenanceInput {
		t.Fatalf("input GetOrCreate()=(%+v, %t, %v)", input, created, err)
	}

	repeated, created, err := lease.GetOrCreate("IMEI", "490154203237518", ProvenanceGenerated, func(uint32) (string, error) {
		candidateCalls++
		return "must-not-replace-alias", nil
	})
	if err != nil || created {
		t.Fatalf("generated repeat GetOrCreate()=(%+v, %t, %v)", repeated, created, err)
	}
	if repeated != input || repeated.Provenance != ProvenanceInput {
		t.Fatalf("input provenance downgraded: first=%+v repeated=%+v", input, repeated)
	}
	if candidateCalls != 1 || store.Snapshot().Entries != 1 {
		t.Fatalf("candidate calls=%d entries=%d, want 1 and 1", candidateCalls, store.Snapshot().Entries)
	}
	if reversed, ok := lease.ResolveSynthetic("IMEI", input.Synthetic); !ok || reversed != input {
		t.Fatalf("reverse after generated repeat=(%+v, %t), want (%+v, true)", reversed, ok, input)
	}
}

func TestScopeStore_ProvenanceConcurrentMixedCallsConvergeToInput(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          1,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    1,
	})
	lease := acquireTestScope(t, store, "concurrent-provenance")
	defer lease.Release()

	seed, created, err := lease.GetOrCreate("SITE", "RAN-ABC123", ProvenanceGenerated, fixedCandidate("SITE-SYN-ABCDE12345"))
	if err != nil || !created {
		t.Fatalf("seed GetOrCreate()=(%+v, %t, %v)", seed, created, err)
	}

	type result struct {
		entry   MappingEntry
		created bool
		err     error
	}
	results := make(chan result, 100)
	var candidateCalls atomic.Int64
	var workers sync.WaitGroup
	workers.Add(100)
	for i := range 100 {
		go func() {
			defer workers.Done()
			provenance := ProvenanceGenerated
			if i%2 == 0 {
				provenance = ProvenanceInput
			}
			entry, created, err := lease.GetOrCreate("SITE", "RAN-ABC123", provenance, func(uint32) (string, error) {
				candidateCalls.Add(1)
				return "must-not-replace-alias", nil
			})
			results <- result{entry: entry, created: created, err: err}
		}()
	}
	workers.Wait()
	close(results)

	for result := range results {
		if result.err != nil || result.created {
			t.Fatalf("concurrent repeat=(%+v, %t, %v)", result.entry, result.created, result.err)
		}
		if result.entry.Synthetic != seed.Synthetic || !result.entry.CreatedAt.Equal(seed.CreatedAt) {
			t.Fatalf("concurrent repeat changed stable entry: seed=%+v repeat=%+v", seed, result.entry)
		}
	}
	if candidateCalls.Load() != 0 {
		t.Fatalf("repeat candidate calls=%d, want 0", candidateCalls.Load())
	}
	if snapshot := store.Snapshot(); snapshot.Entries != 1 {
		t.Fatalf("concurrent entries=%d, want 1", snapshot.Entries)
	}

	final, ok := lease.ResolveSynthetic("SITE", seed.Synthetic)
	if !ok || final.Provenance != ProvenanceInput || final.Synthetic != seed.Synthetic || !final.CreatedAt.Equal(seed.CreatedAt) {
		t.Fatalf("final reverse entry=(%+v, %t), want promoted stable input entry", final, ok)
	}
	inspected := mustInspect(t, store, "concurrent-provenance")
	if len(inspected) != 1 || inspected[0] != final {
		t.Fatalf("final forward entries=%+v, want [%+v]", inspected, final)
	}
}

func TestScopeStore_CoordinateRotationStableUniqueAndCollisionProbes(t *testing.T) {
	store := newCoordinateRotationTestStore(t, 2)
	firstLease := acquireTestScope(t, store, "coordinate-a")
	defer firstLease.Release()
	secondLease := acquireTestScope(t, store, "coordinate-b")
	defer secondLease.Release()

	firstCalls := 0
	first, err := store.coordinateRotationIndex("coordinate-a", func(attempt uint32) (uint16, error) {
		firstCalls++
		if attempt != 0 {
			t.Fatalf("first candidate attempt=%d, want 0", attempt)
		}
		return 7, nil
	})
	if err != nil || first != 7 {
		t.Fatalf("first rotation=(%d, %v), want (7, nil)", first, err)
	}
	repeated, err := store.coordinateRotationIndex("coordinate-a", func(uint32) (uint16, error) {
		firstCalls++
		return 99, nil
	})
	if err != nil || repeated != first || firstCalls != 1 {
		t.Fatalf("stable rotation=(%d, %v), calls=%d, want (%d, nil, 1)", repeated, err, firstCalls, first)
	}

	var secondAttempts []uint32
	second, err := store.coordinateRotationIndex("coordinate-b", func(attempt uint32) (uint16, error) {
		secondAttempts = append(secondAttempts, attempt)
		if attempt == 0 {
			return 7, nil
		}
		return 9, nil
	})
	if err != nil || second != 9 || !slices.Equal(secondAttempts, []uint32{0, 1}) {
		t.Fatalf("collision-probed rotation=(%d, %v), attempts=%v", second, err, secondAttempts)
	}
	if len(store.coordinateByScope) != 2 || len(store.coordinateByIndex) != 2 {
		t.Fatalf("registry sizes=(%d scopes, %d indices), want (2, 2)", len(store.coordinateByScope), len(store.coordinateByIndex))
	}
	if store.coordinateByScope["coordinate-a"] != 7 || store.coordinateByScope["coordinate-b"] != 9 ||
		store.coordinateByIndex[7] != "coordinate-a" || store.coordinateByIndex[9] != "coordinate-b" {
		t.Fatalf("registry forward=%v reverse=%v", store.coordinateByScope, store.coordinateByIndex)
	}
}

func TestScopeStore_CoordinateRotationPrunesLifecycleAndRejectsAbsentScope(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Minute,
		MaxScopes:          3,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    3,
	})

	cleared := acquireTestScope(t, store, "coordinate-cleared")
	if _, err := store.coordinateRotationIndex("coordinate-cleared", fixedRotationCandidate(1)); err != nil {
		t.Fatal(err)
	}
	cleared.Release()
	if result, err := store.Clear("coordinate-cleared"); err != nil || result != ClearCompleted {
		t.Fatalf("Clear(coordinate-cleared)=(%q, %v)", result, err)
	}

	expired := acquireTestScope(t, store, "coordinate-expired")
	if _, err := store.coordinateRotationIndex("coordinate-expired", fixedRotationCandidate(2)); err != nil {
		t.Fatal(err)
	}
	expired.Release()
	clock.Advance(2 * time.Minute)
	if got := store.ReapExpired(); got != 1 {
		t.Fatalf("ReapExpired()=%d, want 1", got)
	}

	retained := acquireTestScope(t, store, "coordinate-retained")
	defer retained.Release()
	if _, err := store.coordinateRotationIndex("coordinate-retained", fixedRotationCandidate(3)); err != nil {
		t.Fatal(err)
	}
	if len(store.coordinateByScope) != 1 || len(store.coordinateByIndex) != 1 ||
		store.coordinateByScope["coordinate-retained"] != 3 || store.coordinateByIndex[3] != "coordinate-retained" {
		t.Fatalf("pruned registry forward=%v reverse=%v", store.coordinateByScope, store.coordinateByIndex)
	}

	candidateCalls := 0
	for _, scopeID := range []string{"coordinate-cleared", "coordinate-expired", "never-retained"} {
		if _, err := store.coordinateRotationIndex(scopeID, func(uint32) (uint16, error) {
			candidateCalls++
			return 4, nil
		}); err == nil {
			t.Fatalf("coordinateRotationIndex(%q) succeeded for absent scope", scopeID)
		}
	}
	if candidateCalls != 0 {
		t.Fatalf("absent-scope candidate calls=%d, want 0", candidateCalls)
	}
}

func TestScopeStore_CoordinateRotationCandidateErrorStoresNoReservation(t *testing.T) {
	store := newCoordinateRotationTestStore(t, 2)
	firstLease := acquireTestScope(t, store, "coordinate-reserved")
	defer firstLease.Release()
	targetLease := acquireTestScope(t, store, "coordinate-target")
	defer targetLease.Release()

	if _, err := store.coordinateRotationIndex("coordinate-reserved", fixedRotationCandidate(12)); err != nil {
		t.Fatal(err)
	}
	wantErr := &TechnicalCapacityError{Entity: "COORDINATES"}
	rotation, err := store.coordinateRotationIndex("coordinate-target", func(attempt uint32) (uint16, error) {
		if attempt == 0 {
			return 12, nil
		}
		return 0, wantErr
	})
	var capacityErr *TechnicalCapacityError
	if rotation != 0 || !errors.As(err, &capacityErr) || capacityErr != wantErr {
		t.Fatalf("exhausted rotation=(%d, %v), want (0, typed candidate error)", rotation, err)
	}
	if _, reserved := store.coordinateByScope["coordinate-target"]; reserved {
		t.Fatal("candidate error stored target reservation")
	}
	if len(store.coordinateByScope) != 1 || len(store.coordinateByIndex) != 1 {
		t.Fatalf("registry after candidate error forward=%v reverse=%v", store.coordinateByScope, store.coordinateByIndex)
	}
}

func TestScopeStore_CoordinateRotationConcurrentAllocationsAreUnique(t *testing.T) {
	const scopeCount = 100

	store := newCoordinateRotationTestStore(t, scopeCount)
	leases := make([]*ScopeLease, 0, scopeCount)
	for index := range scopeCount {
		leases = append(leases, acquireTestScope(t, store, fmt.Sprintf("coordinate-concurrent-%03d", index)))
	}
	defer func() {
		for _, lease := range leases {
			lease.Release()
		}
	}()

	type result struct {
		scopeID string
		index   uint16
		err     error
	}
	results := make(chan result, scopeCount)
	var workers sync.WaitGroup
	workers.Add(scopeCount)
	for scopeNumber, lease := range leases {
		go func() {
			defer workers.Done()
			index, err := store.coordinateRotationIndex(lease.scope.id, func(attempt uint32) (uint16, error) {
				if attempt >= scopeCount {
					return 0, &TechnicalCapacityError{Entity: "COORDINATES"}
				}
				return uint16((scopeNumber + int(attempt)) % scopeCount), nil
			})
			results <- result{scopeID: lease.scope.id, index: index, err: err}
		}()
	}
	workers.Wait()
	close(results)

	indices := make(map[uint16]string, scopeCount)
	for result := range results {
		if result.err != nil {
			t.Fatalf("scope %q allocation failed: %v", result.scopeID, result.err)
		}
		if prior, exists := indices[result.index]; exists {
			t.Fatalf("scopes %q and %q share rotation %d", prior, result.scopeID, result.index)
		}
		indices[result.index] = result.scopeID
	}
	if len(indices) != scopeCount || len(store.coordinateByScope) != scopeCount || len(store.coordinateByIndex) != scopeCount {
		t.Fatalf("unique=%d forward=%d reverse=%d, want %d", len(indices), len(store.coordinateByScope), len(store.coordinateByIndex), scopeCount)
	}
	for _, lease := range leases {
		want := store.coordinateByScope[lease.scope.id]
		got, err := store.coordinateRotationIndex(lease.scope.id, func(uint32) (uint16, error) {
			t.Fatal("stable lookup called candidate")
			return 0, nil
		})
		if err != nil || got != want {
			t.Fatalf("stable scope %q=(%d, %v), want %d", lease.scope.id, got, err, want)
		}
	}
}

func TestScopeStore_CoordinateRotationMultiStoreOwnershipIsIndependent(t *testing.T) {
	const storeCount = 256

	stores := make([]*ScopeStore, 0, storeCount)
	candidateCalls := 0
	for index := range storeCount {
		store := newCoordinateRotationTestStore(t, 1)
		scopeID := fmt.Sprintf("store-owned-%03d", index)
		lease := acquireTestScope(t, store, scopeID)
		rotation, err := store.coordinateRotationIndex(scopeID, func(attempt uint32) (uint16, error) {
			candidateCalls++
			if attempt != 0 {
				t.Fatalf("store %d candidate attempt=%d, want 0", index, attempt)
			}
			return 41, nil
		})
		if err != nil || rotation != 41 {
			t.Fatalf("store %d rotation=(%d, %v), want (41, nil)", index, rotation, err)
		}
		lease.Release()
		if len(store.coordinateByScope) != 1 || len(store.coordinateByIndex) != 1 ||
			store.coordinateByScope[scopeID] != 41 || store.coordinateByIndex[41] != scopeID {
			t.Fatalf("store %d registry forward=%v reverse=%v", index, store.coordinateByScope, store.coordinateByIndex)
		}
		stores = append(stores, store)
	}
	if candidateCalls != storeCount {
		t.Fatalf("candidate calls across %d stores=%d, want %d", storeCount, candidateCalls, storeCount)
	}
	for index, store := range stores {
		if len(store.coordinateByScope) != 1 || len(store.coordinateByIndex) != 1 {
			t.Fatalf("historical store %d registry sizes=(%d, %d), want (1, 1)", index, len(store.coordinateByScope), len(store.coordinateByIndex))
		}
	}
}

func fixedRotationCandidate(index uint16) func(uint32) (uint16, error) {
	return func(uint32) (uint16, error) {
		return index, nil
	}
}

func newCoordinateRotationTestStore(t *testing.T, maxScopes int) *ScopeStore {
	t.Helper()

	return newTestScopeStore(t, newFakeClock(), StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          maxScopes,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    maxScopes,
	})
}

func TestScopeStore_CapacityIsAtomicAndSharedWithRelations(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          2,
		MaxEntriesPerScope: 2,
		MaxTotalEntries:    3,
	})

	leaseA := acquireTestScope(t, store, "scope-a")
	defer leaseA.Release()
	_, _, err := leaseA.GetOrCreate("IP", "192.0.2.1", ProvenanceInput, fixedCandidate("198.51.100.1"))
	if err != nil {
		t.Fatal(err)
	}
	relation, created, err := leaseA.GetOrCreateRelation("192.0.2.0/24", fixedCandidate("198.51.100.0/24"))
	if err != nil || !created || relation != "198.51.100.0/24" {
		t.Fatalf("relation=%q created=%t err=%v", relation, created, err)
	}
	stableRelation, created, err := leaseA.GetOrCreateRelation("192.0.2.0/24", fixedCandidate("must-not-run"))
	if err != nil || created || stableRelation != relation {
		t.Fatalf("stable relation=%q created=%t err=%v", stableRelation, created, err)
	}

	_, created, err = leaseA.GetOrCreate("IP", "192.0.2.2", ProvenanceInput, fixedCandidate("198.51.100.2"))
	if err == nil || created {
		t.Fatalf("per-scope overflow created=%t err=%v", created, err)
	}
	if got := store.Snapshot().Entries; got != 2 {
		t.Fatalf("entries after per-scope rejection=%d, want 2", got)
	}
	if got := len(mustInspect(t, store, "scope-a")); got != 1 {
		t.Fatalf("reversible entries after rejection=%d, want 1", got)
	}

	leaseB := acquireTestScope(t, store, "scope-b")
	defer leaseB.Release()
	_, _, err = leaseB.GetOrCreate("IP", "203.0.113.1", ProvenanceInput, fixedCandidate("198.51.100.3"))
	if err != nil {
		t.Fatal(err)
	}
	_, created, err = leaseB.GetOrCreateRelation("203.0.113.0/24", fixedCandidate("198.51.101.0/24"))
	if err == nil || created {
		t.Fatalf("global overflow created=%t err=%v", created, err)
	}
	if snapshot := store.Snapshot(); snapshot.Entries != 3 || snapshot.ScopesActive != 2 {
		t.Fatalf("snapshot after global rejection=%+v", snapshot)
	}
	if _, err := store.Acquire("scope-c", ProfileStandard); err == nil {
		t.Fatal("Acquire(scope-c) succeeded at active scope capacity")
	}
	if got := len(store.List()); got != 2 {
		t.Fatalf("active scopes after rejection=%d, want 2", got)
	}
}

func TestScopeStore_CapacityReclaimsExpiredEntriesBeforeRejecting(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Minute,
		MaxScopes:          2,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    1,
	})

	expired := acquireTestScope(t, store, "expired")
	_, _, err := expired.GetOrCreate("HOST", "old", ProvenanceInput, fixedCandidate("old-synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	expired.Release()
	clock.Advance(2 * time.Minute)

	target := acquireTestScope(t, store, "target")
	defer target.Release()
	entry, created, err := target.GetOrCreate("HOST", "new", ProvenanceInput, fixedCandidate("new-synthetic"))
	if err != nil || !created || entry.Synthetic != "new-synthetic" {
		t.Fatalf("GetOrCreate() after expired global entry=(%+v, %t, %v)", entry, created, err)
	}
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 1 || snapshot.Entries != 1 {
		t.Fatalf("snapshot after global reclamation=%+v", snapshot)
	}
	if _, err := store.Inspect("expired"); err == nil {
		t.Fatal("expired entry-owning scope was retained")
	}
}

func TestScopeStore_CapacityNeverReclaimsExpiredInFlightEntries(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Minute,
		MaxScopes:          2,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    1,
	})

	active := acquireTestScope(t, store, "active")
	defer active.Release()
	_, _, err := active.GetOrCreate("HOST", "old", ProvenanceInput, fixedCandidate("old-synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)

	target := acquireTestScope(t, store, "target")
	defer target.Release()
	_, created, err := target.GetOrCreate("HOST", "new", ProvenanceInput, fixedCandidate("new-synthetic"))
	if err == nil || created {
		t.Fatalf("GetOrCreate() evicted expired in-flight entry: created=%t err=%v", created, err)
	}
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 2 || snapshot.Entries != 1 {
		t.Fatalf("snapshot after protected active capacity=%+v", snapshot)
	}
}

func TestScopeStore_ReclaimsClosedAndExpiredButNeverInFlightScopes(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Minute,
		MaxScopes:          2,
		MaxEntriesPerScope: 2,
		MaxTotalEntries:    4,
	})

	closed := acquireTestScope(t, store, "closed")
	closed.Release()
	if result, err := store.Clear("closed"); err != nil || result != ClearCompleted {
		t.Fatalf("Clear(closed)=(%q, %v), want (%q, nil)", result, err, ClearCompleted)
	}

	inFlight := acquireTestScope(t, store, "in-flight")
	defer inFlight.Release()
	clock.Advance(2 * time.Minute)

	replacement := acquireTestScope(t, store, "replacement")
	defer replacement.Release()
	if _, err := store.Acquire("third", ProfileStandard); err == nil {
		t.Fatal("Acquire(third) evicted or bypassed capacity with two in-flight scopes")
	}
	if _, ok := scopeInfo(store.List(), "in-flight"); !ok {
		t.Fatal("expired in-flight scope was evicted")
	}

	replacement.Release()
	clock.Advance(2 * time.Minute)
	third := acquireTestScope(t, store, "third")
	third.Release()
	if _, ok := scopeInfo(store.List(), "replacement"); ok {
		t.Fatal("expired idle scope was not reclaimed")
	}
}

func TestScopeStore_ClearDrainsActiveLeaseAndRetainsTombstone(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          2,
		MaxEntriesPerScope: 4,
		MaxTotalEntries:    8,
	})

	lease := acquireTestScope(t, store, "workflow")
	retiredState := lease.scope
	entry, _, err := lease.GetOrCreate("IP", "192.0.2.10", ProvenanceInput, fixedCandidate("198.51.100.10"))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := store.Clear("workflow"); err != nil || result != ClearClosing {
		t.Fatalf("Clear(active)=(%q, %v), want (%q, nil)", result, err, ClearClosing)
	}
	if result, err := store.Clear("workflow"); err != nil || result != ClearClosing {
		t.Fatalf("repeated Clear(active)=(%q, %v), want (%q, nil)", result, err, ClearClosing)
	}
	info := findScopeInfo(t, store.List(), "workflow")
	if info.State != "closing" || info.InFlight != 1 || info.Entries != 1 {
		t.Fatalf("closing scope info=%+v", info)
	}
	if _, err := store.Acquire("workflow", ProfileStandard); err == nil {
		t.Fatal("Acquire(closing scope) succeeded")
	}
	if resolved, ok := lease.ResolveSynthetic("IP", entry.Synthetic); !ok || resolved != entry {
		t.Fatalf("active lease could not finish against closing scope: (%+v, %t)", resolved, ok)
	}

	lease.Release()
	lease.Release()
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 0 || snapshot.RequestsInFlight != 0 || snapshot.Entries != 0 {
		t.Fatalf("snapshot after final release=%+v", snapshot)
	}
	retiredState.mu.Lock()
	if retiredState.forward != nil || retiredState.reverse != nil || retiredState.relations != nil {
		t.Fatalf("retired ledger retained values: forward=%v reverse=%v relations=%v", retiredState.forward, retiredState.reverse, retiredState.relations)
	}
	retiredState.mu.Unlock()
	if _, err := store.Inspect("workflow"); err == nil {
		t.Fatal("Inspect(cleared scope) succeeded")
	}
	if result, err := store.Clear("workflow"); err != nil || result != ClearCompleted {
		t.Fatalf("Clear(tombstone)=(%q, %v), want (%q, nil)", result, err, ClearCompleted)
	}
	if _, err := store.Acquire("workflow", ProfileStandard); err == nil {
		t.Fatal("Acquire(tombstoned scope) succeeded")
	}

	clock.Advance(time.Hour)
	reused := acquireTestScope(t, store, "workflow")
	reused.Release()
}

func TestScopeStore_ClearAllReportsCompletedAndClosing(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          3,
		MaxEntriesPerScope: 2,
		MaxTotalEntries:    6,
	})

	idle := acquireTestScope(t, store, "idle")
	_, _, err := idle.GetOrCreate("HOST", "source", ProvenanceInput, fixedCandidate("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	idle.Release()
	active := acquireTestScope(t, store, "active")

	if summary := store.ClearAll(); summary != (ClearSummary{Completed: 1, Closing: 1}) {
		t.Fatalf("ClearAll()=%+v, want {Completed:1 Closing:1}", summary)
	}
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 1 || snapshot.RequestsInFlight != 1 || snapshot.Entries != 0 {
		t.Fatalf("snapshot while clear-all drains=%+v", snapshot)
	}
	if _, err := store.Acquire("idle", ProfileStandard); err == nil {
		t.Fatal("Acquire(clear-all tombstone) succeeded")
	}
	if _, _, err := active.GetOrCreate("HOST", "late", ProvenanceGenerated, fixedCandidate("late-synthetic")); err != nil {
		t.Fatalf("existing lease could not finish after ClearAll: %v", err)
	}
	active.Release()
	if summary := store.ClearAll(); summary != (ClearSummary{}) {
		t.Fatalf("idempotent ClearAll()=%+v, want zero summary", summary)
	}
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 0 || snapshot.Entries != 0 {
		t.Fatalf("snapshot after clear-all drain=%+v", snapshot)
	}
}

func TestScopeStore_ExpiryNeverReapsInFlightAndTombstoneExpires(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Minute,
		MaxScopes:          2,
		MaxEntriesPerScope: 2,
		MaxTotalEntries:    4,
	})

	lease := acquireTestScope(t, store, "expiring")
	_, _, err := lease.GetOrCreate("IP", "192.0.2.20", ProvenanceInput, fixedCandidate("198.51.100.20"))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if got := store.ReapExpired(); got != 0 {
		t.Fatalf("ReapExpired() with active lease=%d, want 0", got)
	}
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 1 || snapshot.RequestsInFlight != 1 || snapshot.Entries != 1 {
		t.Fatalf("snapshot after active expiry=%+v", snapshot)
	}

	lease.Release()
	if got := store.ReapExpired(); got != 1 {
		t.Fatalf("ReapExpired() after release=%d, want 1", got)
	}
	if _, err := store.Acquire("expiring", ProfileStandard); err == nil {
		t.Fatal("Acquire(expired tombstone) succeeded")
	}
	clock.Advance(time.Minute)
	if got := store.ReapExpired(); got != 0 {
		t.Fatalf("ReapExpired() counted tombstone expiry=%d, want 0", got)
	}
	reused := acquireTestScope(t, store, "expiring")
	reused.Release()
}

func TestScopeStore_ExpiryTombstonesUseBoundedFIFO(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          1,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    1,
	})

	for i := range 129 {
		scopeID := fmt.Sprintf("scope-%03d", i)
		lease := acquireTestScope(t, store, scopeID)
		_, _, err := lease.GetOrCreate("HOST", scopeID, ProvenanceInput, fixedCandidate("alias-"+scopeID))
		if err != nil {
			t.Fatal(err)
		}
		lease.Release()
		if result, err := store.Clear(scopeID); err != nil || result != ClearCompleted {
			t.Fatalf("Clear(%q)=(%q, %v)", scopeID, result, err)
		}
		if snapshot := store.Snapshot(); snapshot.ScopesActive != 0 || snapshot.Entries != 0 {
			t.Fatalf("snapshot after churn %d=%+v", i, snapshot)
		}
	}

	oldest := acquireTestScope(t, store, "scope-000")
	oldest.Release()
	if result, err := store.Clear("scope-000"); err != nil || result != ClearCompleted {
		t.Fatalf("Clear(reused oldest)=(%q, %v)", result, err)
	}
	if _, err := store.Acquire("scope-128", ProfileStandard); err == nil {
		t.Fatal("newest tombstone was not retained after FIFO churn")
	}

	clock.Advance(time.Hour)
	newest := acquireTestScope(t, store, "scope-128")
	newest.Release()
}

func TestScopeStore_InspectReturnsSortedIndependentCopy(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          1,
		MaxEntriesPerScope: 16,
		MaxTotalEntries:    16,
	})
	lease := acquireTestScope(t, store, "inspect")
	defer lease.Release()

	want := make([]MappingEntry, 0, 12)
	for i := 11; i >= 0; i-- {
		entity := fmt.Sprintf("ENTITY-%02d", i/3)
		synthetic := fmt.Sprintf("synthetic-%02d", i)
		entry, _, err := lease.GetOrCreate(entity, fmt.Sprintf("original-%02d", i), ProvenanceInput, fixedCandidate(synthetic))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, entry)
	}
	slices.SortFunc(want, func(a, b MappingEntry) int {
		if a.Entity != b.Entity {
			if a.Entity < b.Entity {
				return -1
			}
			return 1
		}
		if a.Synthetic < b.Synthetic {
			return -1
		}
		if a.Synthetic > b.Synthetic {
			return 1
		}
		return 0
	})

	got := mustInspect(t, store, "inspect")
	if !slices.Equal(got, want) {
		t.Fatalf("Inspect() order=%+v, want %+v", got, want)
	}
	got[0].Original = "mutated-copy"
	got[0].Synthetic = "mutated-copy"
	if second := mustInspect(t, store, "inspect"); !slices.Equal(second, want) {
		t.Fatalf("Inspect() retained caller mutation: %+v", second)
	}
}

func newTestScopeStore(t *testing.T, clock Clock, config StoreConfig) *ScopeStore {
	t.Helper()

	store, err := NewScopeStore(config, clock)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func acquireTestScope(t *testing.T, store *ScopeStore, scopeID string) *ScopeLease {
	t.Helper()

	lease, err := store.Acquire(scopeID, ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func fixedCandidate(value string) func(uint32) (string, error) {
	return func(uint32) (string, error) {
		return value, nil
	}
}

func findScopeInfo(t *testing.T, infos []ScopeInfo, scopeID string) ScopeInfo {
	t.Helper()

	info, ok := scopeInfo(infos, scopeID)
	if !ok {
		t.Fatalf("scope %q not found in %+v", scopeID, infos)
	}
	return info
}

func scopeInfo(infos []ScopeInfo, scopeID string) (ScopeInfo, bool) {
	for _, info := range infos {
		if info.ID == scopeID {
			return info, true
		}
	}
	return ScopeInfo{}, false
}

func mustInspect(t *testing.T, store *ScopeStore, scopeID string) []MappingEntry {
	t.Helper()

	entries, err := store.Inspect(scopeID)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
