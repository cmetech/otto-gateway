// Track 1 (legacy-parity): resilient model discovery — retry+backoff on the
// warmup catalog capture, degrade instead of fail-fast on a cold kiro, and
// lazy self-heal on a catalog read. Mirrors Node 6bbd0c2.
package pool_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

// fastCatalogRetry is a near-zero backoff schedule so the retry tests don't
// sleep real time.
func fastCatalogRetry(n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = time.Millisecond
	}
	return out
}

// TestPool_ModelDiscovery_RetriesThenSucceeds: a transiently-cold kiro
// (NewSession errors on the first two attempts) is absorbed by retry+backoff,
// and Warmup ends with the catalog captured.
func TestPool_ModelDiscovery_RetriesThenSucceeds(t *testing.T) {
	want := []canonical.ModelInfo{{ID: "kiro-3.5"}, {ID: "kiro-lite"}}
	var attempts int32
	fc := &fakeClient{
		models: want,
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			if atomic.AddInt32(&attempts, 1) <= 2 {
				return "", errors.New("kiro cold: session/new failed")
			}
			return "sess-ok", nil
		},
	}
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 1, Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}}})
	p.SetCatalogRetryForTesting(fastCatalogRetry(2)) // 2 retries → up to 3 attempts
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup should succeed after retrying a cold kiro: %v", err)
	}
	if got := p.Models(); len(got) != 2 {
		t.Fatalf("Models() = %v; want the 2-model catalog after retry recovery", got)
	}
	if got := fc.newSessionCount(); got != 3 {
		t.Fatalf("NewSession attempts = %d; want 3 (2 failures + 1 success)", got)
	}
}

// TestPool_ModelDiscovery_PersistentColdDegradesNotAbort: when NewSession
// keeps failing, Warmup must NOT abort boot (the pre-change behavior) — it
// degrades to an empty catalog (adapters render "auto"-only) and self-heals
// later.
func TestPool_ModelDiscovery_PersistentColdDegradesNotAbort(t *testing.T) {
	fc := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("kiro still cold")
		},
	}
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 1, Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}}})
	p.SetCatalogRetryForTesting(fastCatalogRetry(2))
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup must degrade (boot) on a persistently cold kiro, not abort: %v", err)
	}
	if got := p.Models(); len(got) != 0 {
		t.Fatalf("Models() = %v; want empty (degraded) catalog", got)
	}
}

// TestPool_ModelDiscovery_LazySelfHealOnRead: a pool that booted degraded
// (empty catalog) heals on a later Models() read once kiro is warm — no
// restart. The re-probe is singleflight-guarded: a burst of concurrent reads
// triggers exactly ONE probe.
func TestPool_ModelDiscovery_LazySelfHealOnRead(t *testing.T) {
	var warm atomic.Bool
	fc := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) { return "sess", nil },
		availableModelsFn: func() []canonical.ModelInfo {
			if warm.Load() {
				return []canonical.ModelInfo{{ID: "kiro-3.5"}, {ID: "kiro-lite"}}
			}
			return nil // cold at boot
		},
	}
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 1, Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}}})
	p.SetCatalogRetryForTesting(nil) // no warmup retries → exactly 1 warmup NewSession
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	if got := p.Models(); len(got) != 0 {
		t.Fatalf("Models() = %v; want empty at cold boot", got)
	}
	baseline := fc.newSessionCount() // warmup consumed exactly 1

	// kiro is now warm. A burst of concurrent reads must trigger exactly ONE
	// background re-probe (singleflight), and the catalog heals.
	warm.Store(true)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Models() }()
	}
	wg.Wait()

	// Poll for the heal (the probe runs in the background).
	healed := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if len(p.Models()) == 2 {
			healed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !healed {
		t.Fatal("catalog did not self-heal on read after kiro warmed up")
	}
	if got := fc.newSessionCount() - baseline; got != 1 {
		t.Fatalf("self-heal probes = %d; want exactly 1 (singleflight)", got)
	}
}

// TestPool_ModelDiscovery_NoReprobeWhenPopulated: once the catalog is
// populated, catalog reads NEVER trigger a probe.
func TestPool_ModelDiscovery_NoReprobeWhenPopulated(t *testing.T) {
	fc := &fakeClient{
		models:       []canonical.ModelInfo{{ID: "kiro-3.5"}},
		newSessionFn: func(_ context.Context, _ string) (string, error) { return "sess", nil },
	}
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 1, Factory: &fakeClientFactory{clients: []pool.PoolClient{fc}}})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	baseline := fc.newSessionCount()
	for i := 0; i < 8; i++ {
		_ = p.Models()
	}
	time.Sleep(20 * time.Millisecond) // give any (erroneous) background probe a chance to run
	if got := fc.newSessionCount(); got != baseline {
		t.Fatalf("populated catalog triggered %d extra probes; want 0", got-baseline)
	}
}

// TestPool_ModelDiscovery_SpawnFailureStillFailFast: the degrade-on-cold change
// must NOT loosen genuine spawn failures — a slot that cannot spawn still aborts
// Warmup.
func TestPool_ModelDiscovery_SpawnFailureStillFailFast(t *testing.T) {
	ff := &fakeClientFactory{spawnErr: errors.New("spawn: fork/exec kiro-cli: no such file")}
	p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 1, Factory: ff})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Warmup(ctx); err == nil {
		t.Fatal("Warmup must still fail-fast on a genuine spawn failure")
	}
}
