package privacy

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
