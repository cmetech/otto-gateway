package pool_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

func assertSlotActivity(t *testing.T, p *pool.Pool, requests uint64, releasedAt *time.Time) {
	t.Helper()
	row := p.Detail()[0]
	if row.UserRequestsSinceSpawn != requests {
		t.Fatalf("user requests = %d, want %d", row.UserRequestsSinceSpawn, requests)
	}
	if diff := cmp.Diff(releasedAt, row.LastUserReleaseAt); diff != "" {
		t.Fatalf("last release mismatch (-want +got):\n%s", diff)
	}
}

func TestPool_UserActivityExcludesCatalogAndStartsAtRelease(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1101}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := p.Detail()[0]
	if before.UserRequestsSinceSpawn != 0 || before.LastUserReleaseAt != nil {
		t.Fatalf("warmup activity = %+v, want unused", before)
	}

	now = now.Add(5 * time.Minute)
	runOneRequest(t, p)
	after := p.Detail()[0]
	if after.UserRequestsSinceSpawn != 1 || after.LastUserReleaseAt == nil || !after.LastUserReleaseAt.Equal(now) {
		t.Fatalf("request activity = %+v, want count=1 release=%v", after, now)
	}
}

func TestPool_UserActivityTerminalPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "normal result drainage", path: "result"},
		{name: "request context cancellation", path: "context-cancel"},
		{name: "explicit Pool.Cancel", path: "pool-cancel"},
		{name: "Prompt error", path: "prompt-error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
			releasedAt := now.Add(7 * time.Minute)
			client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1201}
			var raw *acp.Stream
			if tt.path == "context-cancel" || tt.path == "pool-cancel" {
				client.promptFn = func(_ context.Context, sid string, _ []canonical.Block) (*acp.Stream, error) {
					raw = acp.NewStreamForTest(sid)
					return raw, nil
				}
			}
			if tt.path == "prompt-error" {
				client.promptFn = func(_ context.Context, _ string, _ []canonical.Block) (*acp.Stream, error) {
					return nil, errors.New("prompt failed")
				}
			}
			p := pool.New(pool.Config{
				Logger:  testutil.Logger(t),
				Size:    1,
				Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
				Now:     func() time.Time { return now },
			})
			defer func() { _ = p.Close() }()
			if err := p.Warmup(context.Background()); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sid, err := p.NewSession(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			now = releasedAt
			var stream interface {
				Chunks() <-chan canonical.Chunk
				Result() (*canonical.FinalResult, error)
			}
			switch tt.path {
			case "result":
				stream, err = p.Prompt(ctx, sid, nil)
				if err == nil {
					drainChunks(stream.Chunks())
					_, err = stream.Result()
				}
			case "context-cancel":
				stream, err = p.Prompt(ctx, sid, nil)
				cancel()
			case "pool-cancel":
				stream, err = p.Prompt(ctx, sid, nil)
				p.Cancel(sid)
			case "prompt-error":
				_, err = p.Prompt(ctx, sid, nil)
				if err == nil {
					t.Fatal("Prompt() error = nil, want failure")
				}
				err = nil
			default:
				t.Fatalf("unknown path %q", tt.path)
			}
			if err != nil {
				t.Fatal(err)
			}

			slot, ok := p.WaitForSlotRelease(200 * time.Millisecond)
			if !ok {
				t.Fatal("terminal path did not return a free slot")
			}
			if got := p.SessionSlotsLen(); got != 0 {
				t.Fatalf("session map length = %d, want 0", got)
			}

			// Fire a later terminal loser while the expected slot remains out of
			// the queue. It must neither overwrite the winning timestamp nor
			// return the slot again.
			now = releasedAt.Add(3 * time.Minute)
			switch tt.path {
			case "result", "prompt-error":
				p.Cancel(sid)
			case "context-cancel", "pool-cancel":
				raw.CloseForTest(&acp.FinalResult{StopReason: canonical.StopCancelled}, nil)
				_, _ = stream.Result()
			}
			if extra, ok := p.TakeSlotIfAvailable(); ok {
				p.PutSlotBack(extra)
				t.Fatal("terminal loser returned the slot a second time")
			}
			assertSlotActivity(t, p, 1, &releasedAt)
			p.PutSlotBack(slot)
		})
	}
}

func TestPool_UserActivityLaterRequestResetsIdle(t *testing.T) {
	now := time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1301}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}

	firstRelease := now.Add(2 * time.Minute)
	now = firstRelease
	runOneRequest(t, p)
	assertSlotActivity(t, p, 1, &firstRelease)

	secondRelease := firstRelease.Add(11 * time.Minute)
	now = secondRelease
	runOneRequest(t, p)
	assertSlotActivity(t, p, 2, &secondRelease)
}

func TestPool_WorkerProcsActivitySemantics(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1401}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}

	procs := p.WorkerProcs()
	if len(procs) != 1 || procs[0].UserRequestsSinceSpawn != 0 || procs[0].IdleSeconds != 0 {
		t.Fatalf("unused WorkerProcs() = %+v, want count=0 idle=0", procs)
	}

	now = now.Add(time.Minute)
	runOneRequest(t, p)
	now = now.Add(4 * time.Minute)
	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	procs = p.WorkerProcs()
	if len(procs) != 1 || procs[0].UserRequestsSinceSpawn != 2 || procs[0].IdleSeconds != 0 {
		t.Fatalf("busy WorkerProcs() = %+v, want count=2 idle=0", procs)
	}

	now = now.Add(time.Minute)
	stream, err := p.Prompt(context.Background(), sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	drainChunks(stream.Chunks())
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(150 * time.Second)
	procs = p.WorkerProcs()
	if len(procs) != 1 || procs[0].UserRequestsSinceSpawn != 2 || procs[0].IdleSeconds != 150 {
		t.Fatalf("idle WorkerProcs() = %+v, want count=2 idle=150", procs)
	}
	now = time.Date(2026, 8, 4, 15, 5, 30, 0, time.UTC)
	if got := p.WorkerProcs()[0].IdleSeconds; got != 0 {
		t.Fatalf("negative-clock idle seconds = %v, want 0", got)
	}
}

func TestPool_UserActivityConfiguredClockInitialSpawn(t *testing.T) {
	now := time.Date(2026, 8, 4, 16, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1501}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	spawnedAt := p.Detail()[0].SpawnedAt
	if spawnedAt == nil || !spawnedAt.Equal(now) {
		t.Fatalf("initial spawned at = %v, want %v", spawnedAt, now)
	}
}

func TestPool_UserActivityConfiguredClockReplacementSpawn(t *testing.T) {
	now := time.Date(2026, 8, 4, 17, 0, 0, 0, time.UTC)
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1601}
	newClient := &fakeClient{pid: 1602}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
		MaxWorkerTurns: 2,
		Now:            func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}

	now = now.Add(20 * time.Minute)
	runOneRequest(t, p)
	slot, ok := p.WaitForSlotRelease(200 * time.Millisecond)
	if !ok {
		t.Fatal("replacement slot was not returned")
	}
	defer p.PutSlotBack(slot)

	row := p.Detail()[0]
	if row.SpawnedAt == nil || !row.SpawnedAt.Equal(now) {
		t.Fatalf("replacement spawned at = %v, want %v", row.SpawnedAt, now)
	}
	assertSlotActivity(t, p, 0, nil)
}
