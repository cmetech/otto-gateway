// Whitebox unit tests for the dispatcher type (package acp).
// D-18: whitebox package gives access to unexported types.
package acp

import (
	"fmt"
	"sync"
	"testing"
)

// TestDispatcherRoute verifies that a notification (no id, has method) routes
// to onNotif.
func TestDispatcherRoute(t *testing.T) {
	t.Parallel()
	notified := make(chan rpcFrame, 1)
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(f rpcFrame) { notified <- f },
	}

	frame := rpcFrame{Method: "session/update"}
	d.route(frame)

	select {
	case got := <-notified:
		if got.Method != "session/update" {
			t.Errorf("got method %q, want session/update", got.Method)
		}
	default:
		t.Error("onNotif was not called for nil-ID frame")
	}
}

// TestDispatcherRoutes_ServerRequest_ToOnNotif verifies the Plan 04 D-20
// surface: an inbound frame with BOTH id AND method (a server-to-client
// request like session/request_permission) routes to onNotif rather than the
// pending map. Phase 1 incorrectly routed any frame-with-id to pending,
// silently dropping permission requests and deadlocking kiro-cli.
func TestDispatcherRoutes_ServerRequest_ToOnNotif(t *testing.T) {
	t.Parallel()
	notified := make(chan rpcFrame, 1)
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(f rpcFrame) { notified <- f },
	}
	id42 := uint64(42)
	d.route(rpcFrame{ID: &id42, Method: "session/request_permission"})

	select {
	case got := <-notified:
		if got.Method != "session/request_permission" {
			t.Errorf("method: got %q, want session/request_permission", got.Method)
		}
		if got.ID == nil || *got.ID != 42 {
			t.Errorf("ID: got %v, want pointer-to-42 (must be preserved for response echo)", got.ID)
		}
	default:
		t.Error("onNotif was not called for server-initiated request (id + method)")
	}
}

// TestDispatcherPending verifies register → route delivers a response frame
// (id set, method empty per JSON-RPC 2.0) to the channel.
func TestDispatcherPending(t *testing.T) {
	t.Parallel()
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(rpcFrame) { t.Error("onNotif called unexpectedly") },
	}

	id := uint64(42)
	respCh := d.register(id)

	id42 := id
	// JSON-RPC 2.0 response: id set, NO method, result carries the body.
	d.route(rpcFrame{ID: &id42, Result: []byte(`{}`)})

	select {
	case got := <-respCh:
		if got.ID == nil || *got.ID != 42 {
			t.Errorf("got id %v, want pointer-to-42", got.ID)
		}
		if string(got.Result) != "{}" {
			t.Errorf("got result %q, want {}", string(got.Result))
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

// TestDispatcherCancel verifies cancel removes the pending entry so a routed
// response frame is silently dropped (not delivered and not a panic).
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
	// Response shape: id set, no method.
	d.route(rpcFrame{ID: &id99, Result: []byte(`{}`)})
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

	// Phase 2: route responses from n goroutines (no method — JSON-RPC 2.0 response).
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			//nolint:gosec // G115: idx is bounded by n=10, never overflows uint64
			id := uint64(idx + 1)
			d.route(rpcFrame{ID: &id, Result: []byte(`{}`)})
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
		if r.ID == nil || *r.ID != uint64(i+1) {
			t.Errorf("goroutine %d: got id %v, want pointer-to-%d", i, r.ID, i+1)
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
