package acp

import (
	"encoding/json"
	"sync"
)

// rpcFrame is the envelope for every incoming JSON-RPC 2.0 message from kiro-cli.
// A frame with no ID (per hasID below) is a notification; the dispatcher routes
// it to onNotif without consulting the pending map.
//
// Phase 1.1 CR-01: ID is json.RawMessage rather than *uint64 so the dispatcher
// can survive any JSON-RPC 2.0 §4 id shape — string, number (negative or
// positive), or null. The Phase 1 *uint64 form silently dropped string-id
// frames, which would reintroduce the D-20 session/request_permission deadlock
// the moment kiro-cli (or a future ACP agent) used string ids. The outbound
// rpcResponse path now echoes ID verbatim, so the body of the JSON id is
// preserved byte-for-byte from request to response. For our outbound requests
// we still mint numeric ids via c.nextID.Add(1); the dispatcher parses those
// back into uint64 for the pending-map lookup inside route().
type rpcFrame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// hasID reports whether the frame carries a usable JSON-RPC id. JSON-RPC 2.0
// allows id to be a string, number, or null; absence and explicit `null` are
// treated the same way (the frame is a notification). Empty RawMessage covers
// the "field absent" case; the explicit "null" check covers `"id": null`.
func (f rpcFrame) hasID() bool {
	return len(f.ID) > 0 && string(f.ID) != "null"
}

// rpcError is the JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// dispatcher correlates JSON-RPC response frames with waiting callers.
// It routes:
//   - nil-ID frames (notifications) → onNotif callback
//   - ID-bearing frames (responses)  → the registered buffered channel
//
// sync.Mutex is used instead of sync.Map because the pending map has high churn
// (register + delete on every request/response cycle), where sync.Mutex outperforms
// sync.Map's optimistic-read path.
type dispatcher struct {
	mu      sync.Mutex
	pending map[uint64]chan<- rpcFrame
	onNotif func(rpcFrame) // called for every notification (nil-ID frame)
}

// register creates a buffered-1 channel for request id and stores it in pending.
// The channel is buffered-1 so that route() never blocks even if the caller has
// already context-cancelled and walked away.
func (d *dispatcher) register(id uint64) <-chan rpcFrame {
	ch := make(chan rpcFrame, 1)
	d.mu.Lock()
	d.pending[id] = ch
	d.mu.Unlock()
	return ch
}

// cancel removes a pending entry without sending to it.
// Called when a caller's context is cancelled before a response arrives.
func (d *dispatcher) cancel(id uint64) {
	d.mu.Lock()
	delete(d.pending, id)
	d.mu.Unlock()
}

// route dispatches frame to the appropriate handler per JSON-RPC 2.0:
//
//   - Notification: no `id`, has `method`. Route to onNotif.
//   - Server-to-client request (Phase 1.1 D-20): has both `id` AND `method`.
//     This is kiro-cli initiating a request that needs a response (e.g.,
//     session/request_permission). Route to onNotif so handleNotification
//     can build an rpcResponse on the same frame id.
//   - Response: has `id`, no `method` (carries `result` or `error` instead).
//     Look up the originating request in the pending map.
//
// Why the method check matters: Phase 1 routed every frame-with-id to the
// pending map, which would silently drop kiro-cli's session/request_permission
// frame (id is set; nothing is pending) and deadlock the subprocess. D-20
// fixes this by recognising request frames structurally.
func (d *dispatcher) route(frame rpcFrame) {
	// Server-to-client traffic: any frame with a method field. Includes both
	// notifications (no id) AND server-initiated requests (id + method).
	if frame.Method != "" {
		d.onNotif(frame)
		return
	}
	// Otherwise it's a response to one of our requests — must carry an id.
	if !frame.hasID() {
		// Malformed frame (no method AND no id). Drop silently — surfaced via
		// the read loop's malformed-frame Warn upstream.
		return
	}
	// Phase 1.1 CR-01: pending-map keys are uint64 because we control the
	// outbound id space (c.nextID.Add(1)). A response frame from kiro-cli
	// MUST echo one of those ids verbatim, so a non-numeric id here means
	// the response is unsolicited — drop it. parse here (where we control
	// the type), NOT at the wire boundary; this preserves the ability of
	// the dispatcher's notification path to forward string ids unchanged.
	var num uint64
	if err := json.Unmarshal(frame.ID, &num); err != nil {
		// Response id is not a uint64 we ever issued — drop silently.
		return
	}
	// Lookup and delete must be atomic under the same lock (Pitfall 3).
	d.mu.Lock()
	ch, ok := d.pending[num]
	if ok {
		delete(d.pending, num)
	}
	d.mu.Unlock()
	if ok {
		ch <- frame // non-blocking: channel is buffered-1
	}
	// Not found: stale response (caller context-cancelled). Drop silently.
}

// drainAll sends a sentinel error frame to every pending caller and empties the map.
// Called by failPending on Close() and on readLoop EOF so no caller hangs.
//
// CR-01 fix: the send uses a non-blocking select. A buffered-1 pending channel
// can already be full if route() raced in a response just before drainAll took
// the lock; a blocking send would then deadlock while still holding d.mu, which
// would block every subsequent register/cancel/route call. The non-blocking
// select drops the duplicate sentinel safely — the caller already has its frame
// (or has walked away).
func (d *dispatcher) drainAll(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, ch := range d.pending {
		select {
		case ch <- rpcFrame{Error: &rpcError{Code: -32099, Message: err.Error()}}:
		default:
			// Channel already has a frame (route() beat us) or caller is gone — drop safely.
		}
		delete(d.pending, id)
	}
}
