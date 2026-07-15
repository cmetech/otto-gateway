// Package capture holds a bounded ring buffer of raw ACP notification frames
// for the Track 0 tool-call wire-capture harness. It is a leaf package (stdlib
// only) so the acp/pool/session layers feed it through a plain func hook
// without importing it, and the admin endpoint reads it through a
// consumer-defined interface — the same boundary discipline the metrics
// recorder uses.
package capture

import (
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"
)

// Frame is one captured inbound kiro frame. Params is the raw params JSON,
// truncated to the ring's per-frame byte cap on a UTF-8 rune boundary; Bytes is
// the pre-truncation length so an operator can tell when a frame was clipped.
type Frame struct {
	Seq    uint64    `json:"seq"`
	Ts     time.Time `json:"ts"`
	Method string    `json:"method"`
	Params string    `json:"params"`
	Bytes  int       `json:"bytes"`
}

// Ring is a fixed-size, mutex-guarded circular buffer of Frames. Safe for
// concurrent Record from multiple readLoop goroutines (one per slot/session).
type Ring struct {
	mu       sync.Mutex
	buf      []Frame
	capBytes int
	next     int    // index to write the next frame
	count    int    // number of valid frames (<= len(buf))
	seq      uint64 // monotonic frame counter
}

// NewRing returns a ring holding up to size frames, each with params truncated
// to capBytes. size <= 0 floors to 1; capBytes <= 0 floors to 1.
func NewRing(size, capBytes int) *Ring {
	if size <= 0 {
		size = 1
	}
	if capBytes <= 0 {
		capBytes = 1
	}
	return &Ring{buf: make([]Frame, size), capBytes: capBytes}
}

// Record appends one frame, overwriting the oldest when full.
func (r *Ring) Record(method string, params json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.buf[r.next] = Frame{
		Seq:    r.seq,
		Ts:     time.Now(),
		Method: method,
		Params: truncateUTF8(string(params), r.capBytes),
		Bytes:  len(params),
	}
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// Snapshot returns a copy of the buffered frames, oldest first.
func (r *Ring) Snapshot() []Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Frame, 0, r.count)
	start := 0
	if r.count == len(r.buf) {
		start = r.next // full: oldest sits at next
	}
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

// truncateUTF8 clips s to at most maxLen bytes without splitting a rune. Walks
// back from the cap to the previous rune-start byte (cost <= utf8.UTFMax-1);
// mirrors the stderrDrainLoop truncation in internal/acp/client.go.
func truncateUTF8(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	n := maxLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
