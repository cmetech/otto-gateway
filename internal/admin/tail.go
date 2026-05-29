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
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
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
// through bufio.Reader, and broadcasts each completed line to all
// subscribers and into the ring buffer.
//
// Partial-line handling (WR-02): bufio.Reader.ReadString('\n') returns
// io.EOF with the partial trailing bytes that have no newline yet. We
// carry those bytes in `partialLine` across ticks; the NEXT tick reads
// the rest and prepends `partialLine` so the line is emitted exactly
// once when the terminator finally arrives. The previous bufio.Scanner
// implementation discarded its internal buffer when recreated each tick,
// silently dropping partial lines.
//
// On rotation (rename+recreate detected via os.Stat + os.SameFile),
// the file is closed and re-opened at the new file's EOF. Any in-flight
// `partialLine` is discarded because it belongs to the old file inode.
// On missing file or read error, the goroutine logs once per tick at
// DEBUG level and retries on the next tick — it never crashes.
func (t *Tailer) run(ctx context.Context) {
	var (
		f           *os.File
		reader      *bufio.Reader
		lastSize    int64
		partialLine string // carry-over bytes with no terminator yet (WR-02)
	)

	// reopen closes any existing file handle and opens t.path at EOF.
	// On error, f is set to nil and the tailer retries on the next tick.
	// Any in-flight partialLine is discarded — it belongs to the prior
	// file inode and cannot be meaningfully concatenated to the new one.
	reopen := func() {
		if f != nil {
			_ = f.Close()
			f = nil
		}
		reader = nil
		partialLine = ""
		nf, err := os.Open(t.path)
		if err != nil {
			// File does not exist yet or path is wrong — log at DEBUG
			// and retry on the next tick. This is the expected startup
			// state when scripts/otto-gw hasn't written anything yet.
			t.logger.Debug("admin: tailer cannot open log", "path", t.path, "err", err)
			return
		}
		// Seek to EOF: D-10 invariant — NEVER backfill historical content.
		sz, err := nf.Seek(0, io.SeekEnd)
		if err != nil {
			_ = nf.Close()
			return
		}
		f = nf
		lastSize = sz
		// 64KB read buffer matches the prior scanner sizing. The
		// TailerMaxLineBytes cap is enforced separately in readLines.
		reader = bufio.NewReaderSize(f, 64*1024)
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

			// Read new bytes line by line, carrying partial trailing
			// bytes (no terminator) into the next tick (WR-02).
			newPartial, readErr := t.readLines(reader, partialLine)
			partialLine = newPartial
			if readErr != nil {
				// Read error (other than EOF, which readLines swallows).
				// Reopen to recover; partialLine has been reset above.
				t.logger.Debug("admin: tailer read error", "err", readErr)
				reopen()
				continue
			}
			lastSize = st.Size()
		}
	}
}

// readLines reads from r until io.EOF, broadcasting each \n-terminated
// line. The returned string is any trailing bytes with no terminator —
// the caller must carry it back into the next call so a partial line
// is emitted exactly once when its terminator finally arrives (WR-02).
//
// `carry` is the partialLine accumulated from the previous tick; it is
// prepended to the first line read so the terminator from the current
// tick completes the prior tick's partial bytes.
//
// Lines exceeding TailerMaxLineBytes are truncated to the cap and a
// DEBUG log is emitted; this matches the prior bufio.Scanner behavior
// where bufio.ErrTooLong triggered a reopen. We keep the file open here
// so a single oversized record does not lose subsequent lines.
//
// Returns the new partial-line carry (possibly "") plus any non-EOF
// read error.
func (t *Tailer) readLines(r *bufio.Reader, carry string) (string, error) {
	current := carry
	for {
		chunk, err := r.ReadString('\n')
		if len(chunk) > 0 {
			current += chunk
			// Enforce the per-line size cap to bound memory growth in
			// case a log producer never emits a newline. If the carry
			// exceeds TailerMaxLineBytes, truncate it and emit a marker.
			if len(current) > TailerMaxLineBytes && !strings.HasSuffix(current, "\n") {
				t.logger.Debug("admin: tailer line exceeds max",
					"bytes", len(current), "max", TailerMaxLineBytes)
				t.broadcast(current[:TailerMaxLineBytes])
				current = ""
				continue
			}
			if strings.HasSuffix(current, "\n") {
				// Strip the trailing \n (and an optional preceding \r
				// from CRLF-terminated logs) to match the prior
				// bufio.Scanner.Text() semantics.
				line := strings.TrimSuffix(current, "\n")
				line = strings.TrimSuffix(line, "\r")
				t.broadcast(line)
				current = ""
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Carry any trailing partial bytes into the next tick.
				return current, nil
			}
			return current, err
		}
	}
}

// ---------------------------------------------------------------------------
// TailerRegistry — quick 260529-ll2 multi-source extension
// ---------------------------------------------------------------------------

// TailerRegistry is a lazy name→*Tailer cache (quick 260529-ll2).
//
// Each (name, path) pair maps to one *Tailer instance. Get is the only
// constructor: the first call for a given name builds a new *Tailer via
// NewTailer(path, logger) and caches it; subsequent calls with the same
// name return the cached pointer (the path argument on subsequent calls
// is IGNORED — read-only registry, D-10 lifetime). This means an
// operator who reconfigures CHAT_TRACE_FILE mid-process still streams
// from the original path; the gateway restart is the lifecycle for path
// changes.
//
// Lazy construction matters because we should never spin up a tailer
// goroutine for a source no SSE client has subscribed to (e.g., the
// chat-trace source when CHAT_TRACE=true but no operator opens the
// admin UI on that channel).
//
// Concurrency: mu.Lock spans the map check + insert so concurrent
// Get(name, _) calls from racing SSE handlers see the same cached
// pointer. The pattern is identical to sync.Once-per-key but avoids
// the extra map[string]*sync.Once allocation since admin Get traffic
// is shaped by SSE-handler frequency (sparse, not hot).
type TailerRegistry struct {
	mu     sync.Mutex
	byName map[string]*Tailer
	logger *slog.Logger
}

// NewTailerRegistry constructs an empty registry rooted at logger. The
// logger is forwarded to every per-source *Tailer constructed via Get
// so all tailers share one structured-log destination.
//
// A nil logger is permitted (defensive); each underlying *Tailer.run
// will dereference logger on read/rotation paths, so callers SHOULD
// pass a real logger. admin.Handler already substitutes slog.Default
// for nil at the Deps layer.
func NewTailerRegistry(logger *slog.Logger) *TailerRegistry {
	return &TailerRegistry{
		byName: make(map[string]*Tailer),
		logger: logger,
	}
}

// Get returns the *Tailer associated with name, constructing one
// lazily on first call via NewTailer(path, registry.logger).
// Subsequent calls with the same name return the cached pointer; the
// path argument is consulted ONLY on the first call (see TailerRegistry
// docstring for the rationale).
//
// Empty name is permitted but discouraged — it creates a single
// shared cached entry under the "" key, which is rarely what callers
// want.
func (r *TailerRegistry) Get(name, path string) *Tailer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.byName[name]; ok {
		return t
	}
	t := NewTailer(path, r.logger)
	r.byName[name] = t
	return t
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
