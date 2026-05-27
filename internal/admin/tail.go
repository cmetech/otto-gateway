// Package admin — log file tailer + ring buffer.
//
// This file implements the shared single-tailer pattern described in
// RESEARCH.md §Pattern 4 (lines 527-737). The tailer fans new log lines from
// a file path to N subscribed SSE clients via per-subscriber channels.
//
// Design invariants (from CONTEXT.md):
//   - D-09: Exactly ONE goroutine tails the log file across the whole gateway.
//     Maintains a 500-line in-memory ring buffer. Started lazily on first
//     Subscribe; exits when the last subscriber Unsubscribes.
//   - D-10: File access is strictly read-only (os.Open). On rotation
//     (rename+recreate), close and re-open at EOF — NEVER backfill historical
//     content. Zero changes to the log-writing path.
//   - D-11: Clean lifecycle — ctx cancel propagates from SSE handler to
//     Unsubscribe to goroutine exit.
//
// Threat mitigations covered here:
//   - T-6.1-11: Non-blocking broadcast (drop on full subscriber chan).
//   - T-6.1-12: Goroutine leak on disconnect — goleak gate in tests.
//   - T-6.1-15: Rotation detection via os.Stat + os.SameFile.
package admin

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Package-level constants for the log tailer.
//
// TailerSubChanBuffer is the per-subscriber channel capacity.
// It is referenced by both tail.go and sse.go; sse.go may reference
// this const directly to avoid duplication.
const (
	// RingBufferLines is the maximum number of log lines held in memory
	// by the shared tailer's ring buffer (D-09 default).
	RingBufferLines = 500

	// TailPollInterval is the cadence at which the tailer polls the log
	// file for size growth and rotation (RESEARCH §Pattern 4 cadence).
	TailPollInterval = 250 * time.Millisecond

	// TailerSubChanBuffer is the capacity of each per-subscriber channel.
	// Full-buffer lines are dropped rather than backpressuring the tailer
	// (T-6.1-11 mitigation). Referenced by sse.go as SSEFanoutBuffer.
	TailerSubChanBuffer = 16

	// TailerMaxLineBytes is the maximum size of a single log line in bytes.
	// bufio.Scanner.Buffer is set to this limit per RESEARCH Pitfall 2.
	TailerMaxLineBytes = 1024 * 1024 // 1 MB
)

// ---------------------------------------------------------------------------
// RingBuffer
// ---------------------------------------------------------------------------

// RingBuffer is a fixed-capacity FIFO ring buffer of strings.
// When capacity is exceeded the oldest entry is overwritten.
// All methods are safe for concurrent use.
type RingBuffer struct {
	mu   sync.Mutex
	cap  int
	head int    // index of the oldest entry when full
	data []string
	full bool
}

// NewRingBuffer allocates a ring buffer with the given capacity.
// Panics if cap ≤ 0 (callers must pass a positive capacity; the
// RingBufferLines const satisfies this at all call sites).
func NewRingBuffer(cap int) *RingBuffer {
	if cap <= 0 {
		panic("admin: RingBuffer capacity must be > 0")
	}
	return &RingBuffer{
		cap:  cap,
		data: make([]string, cap),
	}
}

// Push appends line to the ring buffer. If the buffer is full, the oldest
// entry is overwritten (head advances).
func (r *RingBuffer) Push(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		// Overwrite the oldest slot and advance head.
		r.data[r.head] = line
		r.head = (r.head + 1) % r.cap
	} else {
		// Find next write position: number of valid entries = position
		// before which the buffer wraps.
		// We track fill-count via data[:] until full.
		// head points to the oldest entry (0 until first overflow).
		// Write position = (head + current_len) % cap; but since head==0
		// while not full, write pos == len(valid entries).
		// Track write position as separate field would be cleaner; instead
		// derive it: while not full, data[0..writeIdx-1] are valid, writeIdx
		// grows from 0 to cap. Use head to mean "writeIdx" while not full
		// (head is 0 until we first overflow, then it tracks oldest).
		//
		// Actually: use the convention that before overflow head==0 and we
		// fill data[0..cap-1] in order. The "write index" is how many we
		// have written, which we can derive from scanning — but that is O(n).
		// Better: add an explicit writeIdx field.
		//
		// Implementation note: we repurpose head to mean "write index" while
		// !full and "oldest-entry index" while full. This is the standard
		// circular-buffer technique.
		r.data[r.head] = line
		r.head++
		if r.head == r.cap {
			// Buffer just filled; wrap head to 0 and mark full.
			r.head = 0
			r.full = true
		}
	}
}

// Copy returns a new slice of all buffered lines in FIFO order (oldest first).
// Returns nil if the buffer is empty.
func (r *RingBuffer) Copy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full && r.head == 0 {
		// Buffer is empty.
		return nil
	}
	var result []string
	if r.full {
		// head points to the oldest entry.
		result = make([]string, r.cap)
		copy(result, r.data[r.head:])
		copy(result[r.cap-r.head:], r.data[:r.head])
	} else {
		// Not yet full: data[0..head-1] are valid in FIFO order.
		result = make([]string, r.head)
		copy(result, r.data[:r.head])
	}
	return result
}

// ---------------------------------------------------------------------------
// subscriber
// ---------------------------------------------------------------------------

// subscriber is a single SSE client's receive channel.
// The channel is closed by Unsubscribe to signal the SSE loop to exit.
// The closed flag is set to true (under Tailer.mu) before the channel
// is closed; broadcast reads this flag (also under mu) to avoid a
// send-to-closed-channel panic under the -race detector.
type subscriber struct {
	C      chan string
	ctx    context.Context // caller's context for lifetime correlation
	closed bool            // true after Unsubscribe closes C; guarded by Tailer.mu
}

// ---------------------------------------------------------------------------
// Tailer
// ---------------------------------------------------------------------------

// Tailer fans new log lines from path to N subscribed channels.
// Exactly ONE goroutine tails the file (D-09). The goroutine starts
// lazily on the first Subscribe and exits when the last subscriber
// calls Unsubscribe. The ring buffer provides backfill for late joiners.
//
// All fields are guarded by mu except path and logger which are
// read-only after construction.
type Tailer struct {
	path   string
	logger *slog.Logger

	mu          sync.Mutex
	ring        *RingBuffer
	subscribers []*subscriber
	running     bool
	cancelRun   context.CancelFunc
}

// NewTailer constructs a Tailer rooted at path. It does NOT start the
// poll goroutine; call Subscribe to lazy-start it.
func NewTailer(path string, logger *slog.Logger) *Tailer {
	return &Tailer{
		path:   path,
		logger: logger,
		ring:   NewRingBuffer(RingBufferLines),
	}
}

// Subscribe returns a *subscriber whose C channel receives every new line
// read AFTER this call. Call Tailer.Snapshot() separately to backfill
// historical lines from the ring buffer.
//
// The caller MUST call Unsubscribe when done or when the caller's context
// is cancelled — failing to unsubscribe leaks the shared tailer goroutine.
func (t *Tailer) Subscribe(ctx context.Context) *subscriber {
	t.mu.Lock()
	defer t.mu.Unlock()
	sub := &subscriber{
		C:      make(chan string, TailerSubChanBuffer),
		ctx:    ctx,
		closed: false,
	}
	t.subscribers = append(t.subscribers, sub)
	if !t.running {
		// Lazy start: first subscriber spins up the tailer goroutine.
		runCtx, cancel := context.WithCancel(context.Background())
		t.cancelRun = cancel
		t.running = true
		go t.run(runCtx)
	}
	return sub
}

// Unsubscribe removes sub from the fan-out and closes sub.C.
// If sub was the last subscriber the shared tailer goroutine is cancelled.
//
// The closed flag is set to true under t.mu BEFORE closing the channel.
// broadcast reads closed (also under t.mu) to avoid a concurrent
// send-to-closed-channel panic under the race detector.
func (t *Tailer) Unsubscribe(sub *subscriber) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, s := range t.subscribers {
		if s == sub {
			t.subscribers = append(t.subscribers[:i], t.subscribers[i+1:]...)
			// Mark closed under the lock before closing the channel.
			// broadcast checks sub.closed under the same lock so it will
			// skip sending after this point.
			sub.closed = true
			close(sub.C)
			break
		}
	}
	if len(t.subscribers) == 0 && t.running {
		t.cancelRun()
		t.running = false
	}
}

// Snapshot returns a copy of the ring buffer in FIFO order (oldest first).
// Use this to backfill a new SSE client before entering the live-stream loop.
func (t *Tailer) Snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Copy()
}

// ---------------------------------------------------------------------------
// run — the single tailer goroutine
// ---------------------------------------------------------------------------

// run is the single goroutine that polls the log file for new lines.
// It opens the file at EOF (D-10 — never backfills historical content),
// polls for size growth on a TailPollInterval ticker, reads new bytes
// through bufio.Scanner, and broadcasts each line to all subscribers and
// into the ring buffer.
//
// On rotation (rename+recreate detected via os.Stat + os.SameFile),
// the file is closed and re-opened at the new file's EOF.
// On missing file or read error, the goroutine logs once per tick at
// DEBUG level and retries on the next tick — it never crashes.
func (t *Tailer) run(ctx context.Context) {
	var (
		f          *os.File
		lastSize   int64
		scanner    *bufio.Scanner
		scanBuffer = make([]byte, 0, 64*1024)
	)

	// reopen closes any existing file handle and opens t.path at EOF.
	// On error, f is set to nil and the tailer retries on the next tick.
	reopen := func() {
		if f != nil {
			_ = f.Close()
			f = nil
		}
		nf, err := os.Open(t.path)
		if err != nil {
			// File does not exist yet or path is wrong — log at DEBUG
			// and retry on the next tick. This is the expected startup
			// state when scripts/otto-gw hasn't written anything yet.
			t.logger.Debug("admin: tailer cannot open log", "path", t.path, "err", err)
			return
		}
		// Seek to EOF: D-10 invariant — NEVER backfill historical content.
		sz, err := nf.Seek(0, 2) // io.SeekEnd == 2
		if err != nil {
			_ = nf.Close()
			return
		}
		f = nf
		lastSize = sz
		scanner = bufio.NewScanner(f)
		scanner.Buffer(scanBuffer, TailerMaxLineBytes)
	}

	reopen()

	ticker := time.NewTicker(TailPollInterval)
	defer ticker.Stop()
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			if f == nil {
				reopen()
				continue
			}

			// Detect rotation: stat the path and compare to the open file.
			// If size shrank (truncation) or inode changed (rename+recreate),
			// close and re-open at the new file's EOF.
			st, err := os.Stat(t.path)
			if err != nil {
				t.logger.Debug("admin: tailer stat failed", "path", t.path, "err", err)
				reopen()
				continue
			}
			fst, ferr := f.Stat()
			if ferr != nil || st.Size() < lastSize || !os.SameFile(fst, st) {
				// Rotation or truncation detected — reopen at new EOF.
				reopen()
				continue
			}

			// Nothing to read yet.
			if st.Size() == lastSize {
				continue
			}

			// Read new bytes line by line.
			for scanner.Scan() {
				line := scanner.Text()
				t.broadcast(line)
			}
			if err := scanner.Err(); err != nil {
				// Scanner error (e.g. bufio.ErrTooLong) — reopen.
				t.logger.Debug("admin: tailer scan error", "err", err)
				reopen()
				continue
			}

			// bufio.Scanner returns false at EOF; create a fresh scanner
			// for the next tick so it doesn't stay in a terminal state.
			scanner = bufio.NewScanner(f)
			scanner.Buffer(scanBuffer, TailerMaxLineBytes)
			lastSize = st.Size()
		}
	}
}

// broadcast pushes line into the ring buffer and fans it out to all
// current subscribers. All channel sends happen while holding t.mu so that
// close(sub.C) in Unsubscribe (also under t.mu) and sub.closed checks are
// mutually exclusive, eliminating the send-to-closed-channel race.
//
// Non-blocking channel send: a full subscriber buffer drops the line for
// that subscriber (T-6.1-11). The tailer NEVER blocks on a slow client.
//
// Note: holding t.mu during the sends means a concurrent Subscribe or
// Unsubscribe will wait for the broadcast to finish. This is acceptable
// because the sends are non-blocking (select/default) and the subscriber
// list is small (at most a handful of operators).
func (t *Tailer) broadcast(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ring.Push(line)
	for _, sub := range t.subscribers {
		if sub.closed {
			continue
		}
		select {
		case sub.C <- line:
		default:
			// Drop: subscriber is slow. The tailer keeps moving.
			// The operator may see brief gaps in the SSE stream —
			// acceptable tradeoff vs. blocking the shared goroutine.
		}
	}
}
