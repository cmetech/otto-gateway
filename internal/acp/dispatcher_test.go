// Whitebox unit tests for the dispatcher type (package acp).
// D-18: whitebox package gives access to unexported types.
package acp

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// rawIDNum builds a JSON-RPC id as a json.RawMessage holding a JSON number.
// Phase 1.1 CR-01: rpcFrame.ID is json.RawMessage so all tests construct ids
// via this helper rather than via &uint64.
func rawIDNum(n uint64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("%d", n))
}

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
//
// Phase 1.1 CR-01: ID is json.RawMessage so the dispatcher echoes whatever
// shape arrived (number, string, or null). The numeric-id case lives here;
// TestDispatcherRoutes_ServerRequest_StringID_PreservedForEcho covers the
// string-id wire shape that the *uint64 form silently dropped.
func TestDispatcherRoutes_ServerRequest_ToOnNotif(t *testing.T) {
	t.Parallel()
	notified := make(chan rpcFrame, 1)
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(f rpcFrame) { notified <- f },
	}
	d.route(rpcFrame{ID: rawIDNum(42), Method: "session/request_permission"})

	select {
	case got := <-notified:
		if got.Method != "session/request_permission" {
			t.Errorf("method: got %q, want session/request_permission", got.Method)
		}
		if !got.hasID() || string(got.ID) != "42" {
			t.Errorf("ID: got %q, want raw bytes \"42\" (must be preserved for response echo)", string(got.ID))
		}
	default:
		t.Error("onNotif was not called for server-initiated request (id + method)")
	}
}

// TestDispatcherRoutes_ServerRequest_StringID_PreservedForEcho is the CR-01
// regression test. JSON-RPC 2.0 §4 allows id to be a string OR number; the
// pre-fix *uint64 field silently dropped string ids, which would make the
// readLoop see "id: nil" on a session/request_permission frame and take the
// "permission request without id — dropped" branch in handleNotification —
// reintroducing the D-20 deadlock.
//
// Contract under test: the dispatcher forwards the inbound string-id frame
// to onNotif with frame.ID preserved byte-for-byte ("req-1" stays the bytes
// `"req-1"`, including quotes). handleNotification (in client.go) then
// marshals an rpcResponse whose id field is the same json.RawMessage —
// echoing the inbound string id verbatim per JSON-RPC 2.0 response rules.
func TestDispatcherRoutes_ServerRequest_StringID_PreservedForEcho(t *testing.T) {
	t.Parallel()
	notified := make(chan rpcFrame, 1)
	d := &dispatcher{
		pending: make(map[uint64]chan<- rpcFrame),
		onNotif: func(f rpcFrame) { notified <- f },
	}
	// Build the raw JSON id `"req-1"` (a JSON string).
	stringID := json.RawMessage(`"req-1"`)
	d.route(rpcFrame{ID: stringID, Method: "session/request_permission"})

	select {
	case got := <-notified:
		if got.Method != "session/request_permission" {
			t.Errorf("method: got %q, want session/request_permission", got.Method)
		}
		if !got.hasID() {
			t.Fatal("hasID() returned false for a frame carrying a string id")
		}
		if string(got.ID) != `"req-1"` {
			t.Errorf("ID raw bytes: got %q, want %q (must survive byte-for-byte for echo)",
				string(got.ID), `"req-1"`)
		}
		// Confirm round-trip through json.Marshal preserves the string id.
		// This is the load-bearing assertion: the outbound rpcResponse
		// must include `"id":"req-1"` so kiro-cli's pending map matches.
		marshalled, err := json.Marshal(struct {
			ID json.RawMessage `json:"id"`
		}{ID: got.ID})
		if err != nil {
			t.Fatalf("json.Marshal of preserved id: %v", err)
		}
		if string(marshalled) != `{"id":"req-1"}` {
			t.Errorf("round-tripped frame: got %q, want %q", string(marshalled), `{"id":"req-1"}`)
		}
	default:
		t.Error("onNotif was not called for server-initiated request with string id")
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

	// JSON-RPC 2.0 response: id set, NO method, result carries the body.
	d.route(rpcFrame{ID: rawIDNum(id), Result: []byte(`{}`)})

	select {
	case got := <-respCh:
		if !got.hasID() || string(got.ID) != "42" {
			t.Errorf("got id %q, want raw bytes \"42\"", string(got.ID))
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
	// Response shape: id set, no method.
	d.route(rpcFrame{ID: rawIDNum(id), Result: []byte(`{}`)})
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
			d.route(rpcFrame{ID: rawIDNum(id), Result: []byte(`{}`)})
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
		want := fmt.Sprintf("%d", i+1)
		if !r.hasID() || string(r.ID) != want {
			t.Errorf("goroutine %d: got id %q, want %q", i, string(r.ID), want)
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
			if f.Error == nil || f.Error.Code != closeSentinelCode {
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
