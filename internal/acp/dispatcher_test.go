// Whitebox unit tests for the dispatcher type (package acp).
// D-18: whitebox package gives access to unexported types.
package acp

import (
	"fmt"
	"sync"
	"testing"
)

// TestDispatcherRoute verifies that a frame with a nil ID goes to onNotif.
func TestDispatcherRoute(t *testing.T) {
	t.Parallel()
	notified := make(chan rpcFrame, 1)
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(f rpcFrame) { notified <- f },
	}

	frame := rpcFrame{Method: "session/request_permission"}
	d.route(frame)

	select {
	case got := <-notified:
		if got.Method != "session/request_permission" {
			t.Errorf("got method %q, want session/request_permission", got.Method)
		}
	default:
		t.Error("onNotif was not called for nil-ID frame")
	}
}

// TestDispatcherPending verifies register → route delivers the frame to the channel.
func TestDispatcherPending(t *testing.T) {
	t.Parallel()
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(rpcFrame) { t.Error("onNotif called unexpectedly") },
	}

	id := uint64(42)
	respCh := d.register(id)

	id42 := id
	d.route(rpcFrame{ID: &id42, Method: "initialize", Result: []byte(`{}`)})

	select {
	case got := <-respCh:
		if got.Method != "initialize" {
			t.Errorf("got method %q, want initialize", got.Method)
		}
	default:
		t.Error("response was not delivered to the registered channel")
	}

	// Verify the entry was removed from pending after route.
	d.mu.Lock()
	_, exists := d.pending[id]
	d.mu.Unlock()
	if exists {
		t.Error("pending entry not removed after route")
	}
}

// TestDispatcherCancel verifies cancel removes the pending entry so a routed frame
// is silently dropped (not delivered and not a panic).
func TestDispatcherCancel(t *testing.T) {
	t.Parallel()
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(rpcFrame) { t.Error("onNotif called unexpectedly") },
	}

	id := uint64(99)
	_ = d.register(id)
	d.cancel(id) // remove before response arrives

	// Routing after cancel must be a no-op (no panic, no delivery).
	id99 := id
	d.route(rpcFrame{ID: &id99, Method: "ping"})
	// If we get here without panic, the test passes.

	d.mu.Lock()
	_, exists := d.pending[id]
	d.mu.Unlock()
	if exists {
		t.Error("pending entry still present after cancel")
	}
}

// TestDispatcherConcurrent runs 10 goroutines each registering an ID and routes
// responses concurrently. go test -race must not flag any data race.
func TestDispatcherConcurrent(t *testing.T) {
	t.Parallel()
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(rpcFrame) {},
	}

	const n = 10
	var wg sync.WaitGroup
	results := make([]rpcFrame, n)

	// Phase 1: register all IDs.
	chans := make([]<-chan rpcFrame, n)
	for i := 0; i < n; i++ {
		chans[i] = d.register(uint64(i + 1))
	}

	// Phase 2: route responses from n goroutines.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			//nolint:gosec // G115: idx is bounded by n=10, never overflows uint64
			id := uint64(idx + 1)
			d.route(rpcFrame{ID: &id, Method: "resp"})
		}(i)
	}

	// Phase 3: collect results.
	var collectWg sync.WaitGroup
	for i := 0; i < n; i++ {
		collectWg.Add(1)
		go func(idx int) {
			defer collectWg.Done()
			results[idx] = <-chans[idx]
		}(i)
	}

	wg.Wait()
	collectWg.Wait()

	for i, r := range results {
		if r.Method != "resp" {
			t.Errorf("goroutine %d: got method %q, want resp", i, r.Method)
		}
	}
}

// TestDispatcherDrainAll verifies drainAll sends an error frame to every pending caller
// and clears the pending map.
func TestDispatcherDrainAll(t *testing.T) {
	t.Parallel()
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(rpcFrame) {},
	}

	ch1 := d.register(1)
	ch2 := d.register(2)

	// Use a plain error for the drain test — ErrClientClosed is defined in client.go.
	testErr := fmt.Errorf("acp: client closed")
	d.drainAll(testErr)

	for _, ch := range []<-chan rpcFrame{ch1, ch2} {
		select {
		case f := <-ch:
			if f.Error == nil || f.Error.Code != -32099 {
				t.Errorf("expected sentinel error frame, got %+v", f)
			}
		default:
			t.Error("pending channel did not receive sentinel on drainAll")
		}
	}

	d.mu.Lock()
	l := len(d.pending)
	d.mu.Unlock()
	if l != 0 {
		t.Errorf("pending map not empty after drainAll: %d entries", l)
	}
}
