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
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"runtime/debug"
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
	head int // index of the oldest entry when full
	data []string
	full bool
}

// NewRingBuffer allocates a ring buffer with the given capacity.
// Panics if capacity ≤ 0 (callers must pass a positive capacity; the
// RingBufferLines const satisfies this at all call sites).
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		panic("admin: RingBuffer capacity must be > 0")
	}
	return &RingBuffer{
		cap:  capacity,
		data: make([]string, capacity),
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
	C         chan string
	BackfillC chan []string
	StatusC   chan TailStatus
	ctx       context.Context // caller's context for lifetime correlation
	closed    bool            // true after Unsubscribe closes channels; guarded by Tailer.mu
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
	level  string
	logger *slog.Logger

	mu          sync.Mutex
	ring        *RingBuffer
	subscribers []*subscriber
	running     bool
	cancelRun   context.CancelFunc
	status      TailStatus
	// initialLoaded is scoped to the Tailer instance rather than one run
	// goroutine, so a last-subscriber stop/start does not replay file history.
	initialLoaded bool
}

// NewTailer constructs a Tailer rooted at path. It does NOT start the
// poll goroutine; call Subscribe to lazy-start it.
func NewTailer(path string, logger *slog.Logger) *Tailer {
	return newTailer(path, "", logger)
}

func newTailer(path, level string, logger *slog.Logger) *Tailer {
	return &Tailer{
		path:   path,
		level:  level,
		logger: logger,
		ring:   NewRingBuffer(RingBufferLines),
		status: TailStatus{State: TailStateOpening, Level: level},
	}
}

// Subscribe returns a *subscriber whose C channel receives every new line
// read AFTER this call. Call Tailer.Snapshot() separately to backfill
// historical lines from the ring buffer.
//
// The caller MUST call Unsubscribe when done or when the caller's context
// is cancelled — failing to unsubscribe leaks the shared tailer goroutine.
func (t *Tailer) Subscribe(ctx context.Context) *subscriber { //nolint:revive // *subscriber is package-private by design; returned opaquely as a handle for Unsubscribe, no callers outside internal/admin/
	sub, _, _ := t.Attach(ctx)
	return sub
}

// Attach atomically registers a subscriber and captures the current ring and
// source status. The shared lock prevents a line from falling between the
// snapshot and live-delivery paths.
func (t *Tailer) Attach(ctx context.Context) (*subscriber, []string, TailStatus) { //nolint:revive // package-private subscriber is an opaque lifecycle handle
	t.mu.Lock()
	defer t.mu.Unlock()
	sub := &subscriber{
		C:         make(chan string, TailerSubChanBuffer),
		BackfillC: make(chan []string, 1),
		StatusC:   make(chan TailStatus, 1),
		ctx:       ctx,
		closed:    false,
	}
	t.subscribers = append(t.subscribers, sub)
	snapshot := t.ring.Copy()
	status := t.status
	if !t.running {
		// Lazy start: first subscriber spins up the tailer goroutine.
		runCtx, cancel := context.WithCancel(context.Background())
		t.cancelRun = cancel
		t.running = true
		go t.run(runCtx)
	}
	return sub, snapshot, status
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
			close(sub.BackfillC)
			close(sub.StatusC)
			break
		}
	}
	if len(t.subscribers) == 0 && t.running {
		t.cancelRun()
		t.running = false
	}
}

// publishStatus stores and fans out only meaningful source-state changes.
// Each subscriber keeps a size-one latest-value mailbox, so status delivery
// never backpressures file polling while the newest state remains observable.
func (t *Tailer) publishStatus(status TailStatus) {
	status.Level = t.level
	t.mu.Lock()
	defer t.mu.Unlock()
	if tailStatusesEqual(t.status, status) {
		return
	}
	t.status = status
	for _, sub := range t.subscribers {
		if sub.closed {
			continue
		}
		select {
		case <-sub.StatusC:
		default:
		}
		select {
		case sub.StatusC <- status:
		default:
		}
	}
}

func (t *Tailer) publishFileStatus(info fs.FileInfo) {
	size := info.Size()
	state := TailStateWatching
	if size == 0 {
		state = TailStateEmpty
	}
	t.publishStatus(TailStatus{
		State:      state,
		SizeBytes:  &size,
		ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
	})
}

func (t *Tailer) publishFileError(err error) {
	state := TailStateUnreadable
	if errors.Is(err, fs.ErrNotExist) {
		state = TailStateMissing
	}
	t.publishStatus(TailStatus{State: state})
}

func (t *Tailer) needsInitialBackfill() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.initialLoaded
}

// publishInitialBackfill atomically chooses the history delivery path.
// Subscribers already attached receive one batch; later subscribers see the
// same records in Attach's ring snapshot. The size-one batch mailbox avoids
// truncating 500 records through the 16-record live channel.
func (t *Tailer) publishInitialBackfill(lines []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.initialLoaded {
		return
	}
	for _, line := range lines {
		t.ring.Push(line)
	}
	t.initialLoaded = true
	if len(lines) == 0 {
		return
	}
	for _, sub := range t.subscribers {
		if sub.closed {
			continue
		}
		batch := append([]string(nil), lines...)
		select {
		case sub.BackfillC <- batch:
		default:
		}
	}
}

// Snapshot returns a copy of the ring buffer in FIFO order (oldest first).
// Use this to backfill a new SSE client before entering the live-stream loop.
func (t *Tailer) Snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Copy()
}

// adminTailerPanicProbe is a test-only seam (D-18-07 REL-HTTP-07). The
// run() goroutine invokes it once at the top of its body; tests install
// `func() { panic(...) }` to drive the defer-recover branch. Default
// nil → no-op in production.
//
// Reads + writes go through adminTailerPanicProbeMu so the race
// detector observes the happens-before relationship between a test
// installing a probe and the goroutine reading it.
//
//nolint:gochecknoglobals // package-private test seam, leave nil in production
var (
	adminTailerPanicProbeMu sync.Mutex
	adminTailerPanicProbe   func()
)

// fireAdminTailerPanicProbe atomically reads and invokes the probe.
func fireAdminTailerPanicProbe() {
	adminTailerPanicProbeMu.Lock()
	probe := adminTailerPanicProbe
	adminTailerPanicProbeMu.Unlock()
	if probe != nil {
		probe()
	}
}

// SetAdminTailerPanicProbeForTest installs the probe and returns a
// restore function (use with t.Cleanup). Test-only.
func SetAdminTailerPanicProbeForTest(v func()) func() {
	adminTailerPanicProbeMu.Lock()
	prev := adminTailerPanicProbe
	adminTailerPanicProbe = v
	adminTailerPanicProbeMu.Unlock()
	return func() {
		adminTailerPanicProbeMu.Lock()
		adminTailerPanicProbe = prev
		adminTailerPanicProbeMu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// run — the single tailer goroutine
// ---------------------------------------------------------------------------

// run is the single goroutine that polls the log file for new lines. Its first
// successful open loads bounded recent history; later run restarts, rotations,
// and truncations open at EOF. It then polls for growth and broadcasts each
// completed live line to all subscribers and into the ring buffer.
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
	// D-18-07 REL-HTTP-07: defense-in-depth panic recovery. The tailer
	// goroutine runs in the background; an unrecovered panic (e.g., a
	// future bug in broadcast or in subscriber-list iteration) would
	// take down the gateway because net/http's per-handler recover does
	// NOT cover background goroutines. The recover logs the panic and
	// returns; a subsequent Subscribe will lazy-start a fresh tailer
	// goroutine. No restart / spin loop.
	//
	// Mirrors the engine.callPreHookSafe template at engine.go:317-329.
	// Site name "admin-tailer" is byte-exact per CONTEXT.md §D-18-07.
	defer func() {
		if r := recover(); r != nil {
			if t.logger != nil {
				t.logger.Error(
					"goroutine panic recovered",
					"site", "admin-tailer",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
			}
			// CR-01: Reset running flag so the next Subscribe lazy-restarts the
			// goroutine. Without this, t.running stays true forever and
			// Subscribe never respawns the broadcaster — violating the
			// docstring contract above ("a subsequent Subscribe will lazy-start
			// a fresh tailer goroutine. No restart / spin loop.").
			t.mu.Lock()
			t.running = false
			t.cancelRun = nil
			t.mu.Unlock()
		}
	}()
	// Test-only seam: tests install via SetAdminTailerPanicProbeForTest
	// to drive the defer-recover branch. Default nil → no-op in
	// production. Goes through fireAdminTailerPanicProbe so the race
	// detector sees the happens-before relationship between cross-test
	// probe writes and goroutine reads.
	fireAdminTailerPanicProbe()
	var (
		f           *os.File
		reader      *bufio.Reader
		lastSize    int64
		partialLine string // carry-over bytes with no terminator yet (WR-02)
	)

	// reopen closes any existing file handle and opens t.path. The first
	// successful open reads bounded recent history; every later open seeks EOF.
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
			// D-18-08 REL-OBSV-04: promote from DEBUG to WARN so an
			// operator who sees an empty Log Tail panel gets a visible
			// diagnostic at the default INFO+ logger level. The retry
			// loop runs on every tick; production noise is bounded by
			// the early-return below (the tailer reopens once per tick,
			// not per-line). Path is included so the operator sees
			// exactly which path missed.
			t.logger.Warn("admin: tailer cannot open log", "path", t.path, "err", err)
			t.publishFileError(err)
			return
		}
		info, err := nf.Stat()
		if err != nil {
			_ = nf.Close()
			t.publishStatus(TailStatus{State: TailStateUnreadable})
			return
		}
		if t.needsInitialBackfill() {
			lines, carry, size, backfillErr := readInitialBackfill(
				nf, RingBufferLines, TailerInitialBackfillMaxBytes,
			)
			if backfillErr != nil {
				_ = nf.Close()
				if t.logger != nil {
					t.logger.Warn(
						"admin: tailer cannot read initial log history",
						"path", t.path,
						"err", backfillErr,
					)
				}
				t.publishStatus(TailStatus{State: TailStateUnreadable})
				return
			}
			if _, seekErr := nf.Seek(size, io.SeekStart); seekErr != nil {
				_ = nf.Close()
				if t.logger != nil {
					t.logger.Warn(
						"admin: tailer cannot seek after initial log history",
						"path", t.path,
						"err", seekErr,
					)
				}
				t.publishStatus(TailStatus{State: TailStateUnreadable})
				return
			}
			f = nf
			lastSize = size
			partialLine = carry
			reader = bufio.NewReaderSize(f, 64*1024)
			t.publishInitialBackfill(lines)
			t.publishFileStatus(info)
			return
		}

		// Run restarts, rotations, and truncations never replay history.
		sz, err := nf.Seek(0, io.SeekEnd)
		if err != nil {
			_ = nf.Close()
			t.publishStatus(TailStatus{State: TailStateUnreadable})
			return
		}
		f = nf
		lastSize = sz
		// 64KB read buffer matches the prior scanner sizing. The
		// TailerMaxLineBytes cap is enforced separately in readLines.
		reader = bufio.NewReaderSize(f, 64*1024)
		t.publishFileStatus(info)
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
				t.publishFileError(err)
				reopen()
				continue
			}
			fst, ferr := f.Stat()
			if ferr != nil {
				t.publishStatus(TailStatus{State: TailStateUnreadable})
				reopen()
				continue
			}
			if st.Size() < lastSize || !os.SameFile(fst, st) {
				// Rotation or truncation detected — reopen at new EOF.
				reopen()
				continue
			}

			// Nothing to read yet.
			if st.Size() == lastSize {
				t.publishFileStatus(st)
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
				t.publishStatus(TailStatus{State: TailStateUnreadable})
				_ = f.Close()
				f = nil
				reader = nil
				partialLine = ""
				continue
			}
			lastSize = st.Size()
			t.publishFileStatus(st)
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
			// case a log producer never emits a newline OR emits a
			// multi-MB newline-terminated line (H-5 / REL-HTTP-05).
			// This first check handles the unterminated-fragment path:
			// a chunk that grew current past the cap without a '\n'
			// gets truncated and broadcast immediately so we do not
			// hold the whole payload in memory waiting for a
			// terminator that may never arrive.
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
				// H-5 fix: cap newline-terminated lines too. The prior
				// check above only fires when !HasSuffix("\n"), so a
				// multi-MB line that arrives with a trailing '\n' in
				// a single ReadString call bypassed truncation and
				// flowed unbounded through the ring buffer and SSE
				// stream. Truncate here before broadcast so the
				// memory cost is bounded by TailerMaxLineBytes for
				// ALL paths.
				if len(line) > TailerMaxLineBytes {
					t.logger.Debug("admin: tailer line truncated at cap",
						"bytes", len(line), "max", TailerMaxLineBytes)
					line = line[:TailerMaxLineBytes]
				}
				t.broadcast(line)
				current = ""
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Carry any trailing partial bytes into the next tick.
				return current, nil
			}
			return current, fmt.Errorf("admin.tail: read line: %w", err)
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
func (r *TailerRegistry) Get(name, path, level string) *Tailer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.byName[name]; ok {
		return t
	}
	t := newTailer(path, level, r.logger)
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
