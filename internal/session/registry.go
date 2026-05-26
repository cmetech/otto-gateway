package session

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrSessionNotFound is returned by Registry.Delete when the sid is not
// in the entries map. Surface adapters translate to HTTP 404.
var ErrSessionNotFound = errors.New("session: not found")

// ErrSessionMaxExceeded is returned by Registry.Get when a lazy-create
// would push len(entries) past Config.MaxSessions (D-06). Surface
// adapters translate to HTTP 503 service-unavailable.
var ErrSessionMaxExceeded = errors.New("session: max sessions exceeded")

// ErrRegistryClosed is returned by Registry.Get when called after
// Registry.Close has run. Surface adapters translate to HTTP 503.
var ErrRegistryClosed = errors.New("session: registry closed")

// Entry is one dedicated kiro-cli session owned by the registry, keyed
// by the client-supplied X-Session-Id (D-04, D-05, D-07).
//
// Field visibility:
//   - Mu/Client/SessionID/LastUsed/LastModel/Dead are exported because
//     surface handlers (plan 05-03) read them: Lock/Unlock Mu around
//     Prompt, call MarkUsed in a defer to update LastUsed (D-11), inspect
//     LastModel for the D-09 diff-skip path, and read Dead during error
//     surfacing.
//   - creating/ready are lowercase: package-internal Pitfall 4
//     creation-sentinel discipline.
type Entry struct {
	// Mu serializes per-session Prompt calls (D-07). Surface handlers
	// Lock before Prompt and Unlock after stream completes. The reaper
	// uses TryLock — never blocks (D-12).
	Mu sync.Mutex
	// Client is the dedicated *acp.Client for this session (typed as
	// PoolClient so tests can inject fakes via fakeClientFactory).
	Client PoolClient
	// SessionID is the ACP session id returned by NewSession during
	// createEntry. Cached so Entry.NewSession can return it without
	// another RPC (the engine.ACPClient.NewSession contract).
	SessionID string
	// LastUsed is the wall-clock timestamp the reaper compares against
	// the TTL cutoff (D-10, D-12). Updated by MarkUsed at response
	// complete, NEVER at request start (D-11).
	LastUsed time.Time
	// LastModel caches the most-recently-set model id for the D-09
	// diff-skip path: SetModel returns early when modelID == LastModel.
	LastModel string
	// Dead is set by error paths to signal "do not reuse"; the next Get
	// for this sid treats Dead==true as not-present and lazy-creates a
	// replacement entry.
	Dead bool

	// creating is the Pitfall 4 sentinel: true while createEntry is
	// running the slow Spawn+Initialize+NewSession sequence under
	// Registry.mu released. Subsequent same-sid Get callers observe
	// creating==true, drop the registry lock, and wait on <-ready
	// before re-checking the map. Guarded by Registry.mu.
	creating bool
	// ready is closed by createEntry on success OR error after the
	// entry has either been fully populated or removed from the map.
	// Same-sid waiters select on this channel to learn when the
	// creation attempt has resolved.
	ready chan struct{}
}

// Registry owns the map of dedicated kiro-cli sessions keyed by sid.
// All ownership semantics for D-04..D-13 live here. The reaper is
// spawned by Start and torn down by Close (Pitfall 5 — bounded
// shutdown via close(closing) + wg.Wait()).
type Registry struct {
	cfg Config

	// mu guards entries + closed. RWMutex because the reaper's
	// snapshot-then-iterate pattern (D-12) only needs a read lock to
	// copy the entries map; Get/Delete/createEntry take write locks
	// for brief critical sections that NEVER cross slow client calls.
	mu sync.RWMutex
	// entries maps sid → *Entry. Plain map + RWMutex (NOT sync.Map —
	// the reaper iterates the whole map every tick, which is the wrong
	// shape for sync.Map's read-mostly assumption).
	entries map[string]*Entry
	// closed flips to true on first Close call to fail subsequent Gets.
	closed bool

	// closing is closed by Close to signal the reaper goroutine to
	// exit (Pitfall 5). wg.Wait() in Close blocks until the reaper
	// observes the signal, bounded by TickInterval + worst-case
	// reapOnce iteration time.
	closing chan struct{}
	// wg tracks the reaper goroutine spawned by Start. wg.Wait() in
	// Close ensures clean teardown (goleak gate).
	wg sync.WaitGroup
	// closeOnce ensures the shutdown sequence runs exactly once.
	closeOnce sync.Once
}

// New constructs a Registry with the given Config. The reaper is NOT
// started until Start is called — this lets callers (cmd/otto-gateway
// main.go) wire the registry into the server.Config before background
// goroutines spin up.
func New(cfg Config) *Registry {
	cfg.applyDefaults()
	return &Registry{
		cfg:     cfg,
		entries: make(map[string]*Entry),
		closing: make(chan struct{}),
	}
}

// Start spawns the reaper goroutine. ctx is currently unused at the
// Registry level — the reaper exits via <-r.closing from Close(). The
// ctx parameter is kept in the signature for API stability so plan
// 05-03 can pass the gateway's lifetime ctx if cross-cutting
// cancellation becomes needed.
//
// Task 0 STUB: spawns the reaper but reaperLoop body is itself a stub.
// Full implementation arrives in Task 2.
func (r *Registry) Start(_ context.Context) {
	r.wg.Add(1)
	go r.reaperLoop()
}

// Get returns the existing entry for sid, or lazy-creates one and
// caches it under r.entries[sid]. Task 0 STUB: full implementation
// arrives in Task 1.
func (r *Registry) Get(_ context.Context, _, _ string) (*Entry, error) {
	panic("session.Registry.Get: not yet implemented (Task 1)")
}

// Delete removes the entry for sid from the registry. Task 0 STUB:
// full implementation arrives in Task 1.
func (r *Registry) Delete(_ string) error {
	panic("session.Registry.Delete: not yet implemented (Task 1)")
}

// Close drains the reaper and tears down all entries. Task 0 STUB:
// the closeOnce + close(closing) + wg.Wait() shape is implemented so
// tests that spawn Start can clean up cleanly; entry teardown lands
// in Task 1.
func (r *Registry) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		close(r.closing)
		r.wg.Wait()
	})
	return nil
}
