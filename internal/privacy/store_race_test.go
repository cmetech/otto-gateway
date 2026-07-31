package privacy

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopeStore_CoordinateRotationDifferentStoresDoNotSerialize(t *testing.T) {
	storeA := newCoordinateRotationTestStore(t, 1)
	leaseA := acquireTestScope(t, storeA, "coordinate-store-a")
	defer leaseA.Release()
	storeB := newCoordinateRotationTestStore(t, 1)
	leaseB := acquireTestScope(t, storeB, "coordinate-store-b")
	defer leaseB.Release()

	candidateStarted := make(chan struct{})
	releaseCandidate := make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		_, err := storeA.coordinateRotationIndex("coordinate-store-a", func(uint32) (uint16, error) {
			close(candidateStarted)
			<-releaseCandidate
			return 17, nil
		})
		aDone <- err
	}()
	<-candidateStarted

	bDone := make(chan error, 1)
	go func() {
		rotation, err := storeB.coordinateRotationIndex("coordinate-store-b", fixedRotationCandidate(17))
		if err == nil && rotation != 17 {
			err = fmt.Errorf("store B rotation=%d, want 17", rotation)
		}
		bDone <- err
	}()

	select {
	case err := <-bDone:
		if err != nil {
			close(releaseCandidate)
			<-aDone
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseCandidate)
		<-aDone
		<-bDone
		t.Fatal("store B coordinate reservation serialized behind store A candidate")
	}

	close(releaseCandidate)
	if err := <-aDone; err != nil {
		t.Fatalf("store A reservation failed: %v", err)
	}
}

func TestScopeStore_NoGlobalConvoy_AcquireSameScopeWaiter(t *testing.T) {
	store := newParallelTestStore(t)
	blocked := blockScopeCandidate(t, store, "scope-a")
	defer blocked.finish(t)

	type acquireResult struct {
		lease *ScopeLease
		err   error
	}
	acquireDone := make(chan acquireResult, 1)
	go func() {
		lease, err := store.Acquire("scope-a", ProfileStandard)
		acquireDone <- acquireResult{lease: lease, err: err}
	}()
	waitForScopeWaiter(t, blocked.scope)

	bDone := insertIntoScopeAsync(store, "scope-b")
	convoyed, bErr := operationConvoyed(bDone)
	blocked.finish(t)
	second := <-acquireDone
	if second.err != nil {
		t.Fatalf("second Acquire(scope-a) failed: %v", second.err)
	}
	second.lease.Release()
	if convoyed {
		bErr = <-bDone
	}
	if bErr != nil {
		t.Fatalf("scope B insertion failed: %v", bErr)
	}
	if convoyed {
		t.Fatal("same-scope Acquire waiter held the global store lock")
	}
}

func TestScopeStore_NoGlobalConvoy_ReapExpired(t *testing.T) {
	store := newParallelTestStore(t)
	blocked := blockScopeCandidate(t, store, "scope-a")
	defer blocked.finish(t)

	reapDone := make(chan int, 1)
	go func() {
		reapDone <- store.ReapExpired()
	}()
	reapCompleted, reaped := waitForScopeWaiterOrReap(t, blocked.scope, reapDone)

	bDone := insertIntoScopeAsync(store, "scope-b")
	convoyed, bErr := operationConvoyed(bDone)
	blocked.finish(t)
	if !reapCompleted {
		reaped = <-reapDone
	}
	if reaped != 0 {
		t.Fatalf("ReapExpired()=%d with in-flight scope, want 0", reaped)
	}
	if convoyed {
		bErr = <-bDone
	}
	if bErr != nil {
		t.Fatalf("scope B insertion failed: %v", bErr)
	}
	if convoyed {
		t.Fatal("ReapExpired held the global store lock while waiting on scope A")
	}
}

func TestScopeStore_NoGlobalConvoy_List(t *testing.T) {
	store := newParallelTestStore(t)
	blocked := blockScopeCandidate(t, store, "scope-a")
	defer blocked.finish(t)

	listDone := make(chan []ScopeInfo, 1)
	go func() {
		listDone <- store.List()
	}()
	waitForScopeWaiter(t, blocked.scope)

	bDone := insertIntoScopeAsync(store, "scope-b")
	convoyed, bErr := operationConvoyed(bDone)
	blocked.finish(t)
	infos := <-listDone
	if len(infos) == 0 {
		t.Fatal("List() returned no scope metadata")
	}
	if convoyed {
		bErr = <-bDone
	}
	if bErr != nil {
		t.Fatalf("scope B insertion failed: %v", bErr)
	}
	if convoyed {
		t.Fatal("List held the global store lock while waiting on scope A")
	}
}

func TestScopeStore_ParallelScopesDoNotSerialize(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          4,
		MaxEntriesPerScope: 2,
		MaxTotalEntries:    8,
	})
	leaseA := acquireTestScope(t, store, "scope-a")
	defer leaseA.Release()

	candidateStarted := make(chan struct{})
	releaseCandidate := make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		_, _, err := leaseA.GetOrCreate("HOST", "source-a", ProvenanceInput, func(uint32) (string, error) {
			close(candidateStarted)
			<-releaseCandidate
			return "synthetic-a", nil
		})
		aDone <- err
	}()
	<-candidateStarted

	bDone := make(chan error, 1)
	go func() {
		leaseB, err := store.Acquire("scope-b", ProfileStandard)
		if err != nil {
			bDone <- err
			return
		}
		defer leaseB.Release()
		_, _, err = leaseB.GetOrCreate("HOST", "source-b", ProvenanceInput, fixedCandidate("synthetic-b"))
		bDone <- err
	}()

	select {
	case err := <-bDone:
		if err != nil {
			close(releaseCandidate)
			<-aDone
			t.Fatalf("scope B insertion failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseCandidate)
		<-aDone
		<-bDone
		t.Fatal("scope B serialized behind scope A candidate")
	}

	close(releaseCandidate)
	if err := <-aDone; err != nil {
		t.Fatalf("scope A insertion failed: %v", err)
	}
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 2 || snapshot.Entries != 2 {
		t.Fatalf("parallel snapshot=%+v", snapshot)
	}
}

type blockedScopeCandidate struct {
	scope   *scopeState
	lease   *ScopeLease
	unblock chan struct{}
	done    chan error
	once    sync.Once
}

func newParallelTestStore(t *testing.T) *ScopeStore {
	t.Helper()

	return newTestScopeStore(t, newFakeClock(), StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          4,
		MaxEntriesPerScope: 2,
		MaxTotalEntries:    8,
	})
}

func blockScopeCandidate(t *testing.T, store *ScopeStore, scopeID string) *blockedScopeCandidate {
	t.Helper()

	lease := acquireTestScope(t, store, scopeID)
	blocked := &blockedScopeCandidate{
		scope:   lease.scope,
		lease:   lease,
		unblock: make(chan struct{}),
		done:    make(chan error, 1),
	}
	started := make(chan struct{})
	go func() {
		_, _, err := lease.GetOrCreate("HOST", "source-"+scopeID, ProvenanceInput, func(uint32) (string, error) {
			close(started)
			<-blocked.unblock
			return "synthetic-" + scopeID, nil
		})
		blocked.done <- err
	}()
	<-started
	return blocked
}

func (b *blockedScopeCandidate) finish(t *testing.T) {
	t.Helper()

	b.once.Do(func() {
		close(b.unblock)
		if err := <-b.done; err != nil {
			t.Errorf("blocked candidate failed: %v", err)
		}
		b.lease.Release()
	})
}

func waitForScopeWaiter(t *testing.T, scope *scopeState) {
	t.Helper()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for scope.lockWaiters.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("operation did not reach the contested scope lock")
		default:
			runtime.Gosched()
		}
	}
}

func waitForScopeWaiterOrReap(t *testing.T, scope *scopeState, done <-chan int) (bool, int) {
	t.Helper()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case reaped := <-done:
			return true, reaped
		case <-deadline.C:
			t.Fatal("ReapExpired neither completed nor reached the contested scope lock")
		default:
			if scope.lockWaiters.Load() > 0 {
				return false, 0
			}
			runtime.Gosched()
		}
	}
}

func insertIntoScopeAsync(store *ScopeStore, scopeID string) <-chan error {
	done := make(chan error, 1)
	go func() {
		lease, err := store.Acquire(scopeID, ProfileStandard)
		if err != nil {
			done <- err
			return
		}
		defer lease.Release()
		_, _, err = lease.GetOrCreate("HOST", "source-"+scopeID, ProvenanceInput, fixedCandidate("synthetic-"+scopeID))
		done <- err
	}()
	return done
}

func operationConvoyed(done <-chan error) (bool, error) {
	select {
	case err := <-done:
		return false, err
	case <-time.After(500 * time.Millisecond):
		return true, nil
	}
}

func TestScopeStore_ParallelSameScopeCreatesExactlyOneEntry(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          1,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    1,
	})

	type result struct {
		entry   MappingEntry
		created bool
		err     error
	}
	results := make(chan result, 100)
	var candidateCalls atomic.Int64
	var workers sync.WaitGroup
	workers.Add(100)
	for range 100 {
		go func() {
			defer workers.Done()
			lease, err := store.Acquire("shared", ProfileStandard)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer lease.Release()
			entry, created, err := lease.GetOrCreate("HOST", "one-source", ProvenanceInput, func(uint32) (string, error) {
				candidateCalls.Add(1)
				return "one-synthetic", nil
			})
			results <- result{entry: entry, created: created, err: err}
		}()
	}
	workers.Wait()
	close(results)

	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("same-scope operation failed: %v", result.err)
		}
		if result.entry.Entity != "HOST" || result.entry.Original != "one-source" || result.entry.Synthetic != "one-synthetic" {
			t.Fatalf("same-scope entry=%+v", result.entry)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 || candidateCalls.Load() != 1 {
		t.Fatalf("created=%d candidate calls=%d, want 1 and 1", createdCount, candidateCalls.Load())
	}
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 1 || snapshot.RequestsInFlight != 0 || snapshot.Entries != 1 {
		t.Fatalf("same-scope snapshot=%+v", snapshot)
	}
}

func TestScopeStore_ParallelDistinctScopesHonorExactGlobalCapacity(t *testing.T) {
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          100,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    50,
	})

	type result struct {
		created bool
		err     error
	}
	results := make(chan result, 100)
	var workers sync.WaitGroup
	workers.Add(100)
	for i := range 100 {
		go func() {
			defer workers.Done()
			scopeID := fmt.Sprintf("scope-%03d", i)
			lease, err := store.Acquire(scopeID, ProfileStandard)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer lease.Release()
			_, created, err := lease.GetOrCreate(
				"HOST",
				"source-"+scopeID,
				ProvenanceGenerated,
				fixedCandidate("synthetic-"+scopeID),
			)
			results <- result{created: created, err: err}
		}()
	}
	workers.Wait()
	close(results)

	createdCount := 0
	rejectedCount := 0
	for result := range results {
		switch {
		case result.err != nil:
			rejectedCount++
		case result.created:
			createdCount++
		default:
			t.Fatal("distinct first insertion returned created=false without error")
		}
	}
	if createdCount != 50 || rejectedCount != 50 {
		t.Fatalf("created=%d rejected=%d, want 50 and 50", createdCount, rejectedCount)
	}
	if snapshot := store.Snapshot(); snapshot.ScopesActive != 100 || snapshot.RequestsInFlight != 0 || snapshot.Entries != 50 {
		t.Fatalf("distinct-scope snapshot=%+v", snapshot)
	}
	if _, err := store.Acquire("scope-overflow", ProfileStandard); err == nil {
		t.Fatal("Acquire(scope-overflow) succeeded beyond exact scope capacity")
	}
}
