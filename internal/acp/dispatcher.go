package acp

import (
	"encoding/json"
	"sync"
)

// rpcFrame is the envelope for every incoming JSON-RPC 2.0 message from kiro-cli.
// A nil ID field means the frame is a notification (session/update,
// session/request_permission, etc.) — it never enters the pending map.
type rpcFrame struct {
	ID     *uint64         `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
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

// route dispatches frame to the appropriate handler.
// CRITICAL: check ID == nil FIRST. Notifications (session/request_permission,
// session/update) have no id field. If routed to the pending map they are silently
// dropped, causing kiro-cli to block forever waiting for session/grant_permission.
func (d *dispatcher) route(frame rpcFrame) {
	if frame.ID == nil {
		d.onNotif(frame)
		return
	}
	// Lookup and delete must be atomic under the same lock (Pitfall 3).
	d.mu.Lock()
	ch, ok := d.pending[*frame.ID]
	if ok {
		delete(d.pending, *frame.ID)
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
