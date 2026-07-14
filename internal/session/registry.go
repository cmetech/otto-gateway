package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"otto-gateway/internal/acp"
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
	// lastUsedNs is the wall-clock timestamp (time.Now().UnixNano()) the
	// reaper compares against the TTL cutoff (D-10, D-12). Updated by
	// MarkUsed at response complete, NEVER at request start (D-11).
	//
	// P-5 fix (REL-POOL-05): previously typed `LastUsed time.Time` and
	// written without a consistent mutex — Registry.Get wrote under r.mu
	// at registry.go:206, Entry.MarkUsed wrote without any lock at
	// entry_acp.go:78, and watchEntry/stats read without any lock.
	// `go test -race` flagged the multi-word time.Time write/write race.
	// Converted to atomic.Int64 (UnixNano) with the public LastUsed()
	// accessor below; all read sites switched from `e.LastUsed` to
	// `e.LastUsed()`. The unexported lowercase field name prevents
	// callers from accidentally reverting to direct field access.
	lastUsedNs atomic.Int64
	// lastCtxPct holds the most-recent kiro context-usage percent (0–100)
	// observed for this session via the acp OnContextPct hook (kiro
	// usage-metrics parity Track 2). Stored as float64 bits in an
	// atomic.Uint64 so the continuously-streaming OnContextPct writes race
	// safely against Registry.Get's recycle read under -race. Accessed via
	// setCtxPct/ctxPct.
	lastCtxPct atomic.Uint64
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
	// readyOnce guards close(ready) so concurrent paths
	// (createEntry success, createEntry publishError, createEntry
	// concurrent-removal, Delete mid-creation branch, Close
	// mid-creation branch) can call closeReady idempotently without
	// panicking on a double close (CR-02). The previous
	// select-default idiom was racy: between case-evaluation and
	// default-close, another goroutine could close the channel and
	// the second close panicked the process.
	readyOnce sync.Once
}

// setCtxPct stores the latest context-usage percent (0–100) for this session.
// Called from the acp OnContextPct hook wired in createEntry, which fires on
// every kiro contextUsagePercentage frame.
func (e *Entry) setCtxPct(pct float64) {
	e.lastCtxPct.Store(math.Float64bits(pct))
}

// ctxPct returns the latest context-usage percent observed for this session,
// or 0 if none has been reported yet. Read by Registry.Get's recycle guard.
func (e *Entry) ctxPct() float64 {
	return math.Float64frombits(e.lastCtxPct.Load())
}

// closeReady idempotently closes e.ready. Safe to call from any
// goroutine and any number of times (CR-02 fix).
func (e *Entry) closeReady() {
	e.readyOnce.Do(func() {
		if e.ready != nil {
			close(e.ready)
		}
	})
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

	// reaped is the Track 4b monotonic counter of sessions reaped for
	// idleness, surfaced via the Prometheus pull-collector (gw_sessions_reaped_total).
	reaped atomic.Uint64
	// created / recycled are the kiro usage-metrics parity monotonic counters,
	// surfaced via the pull-collector (gw_sessions_created_total /
	// gw_sessions_recycled_total). created bumps on each successful
	// createEntry publish; recycled bumps when Get recycles an entry that
	// crossed CTX_RECYCLE_PCT.
	created  atomic.Uint64
	recycled atomic.Uint64
}

// Reaped returns the total stateful sessions reaped for idleness since start.
func (r *Registry) Reaped() uint64 { return r.reaped.Load() }

// Created returns the total stateful sessions created since start.
func (r *Registry) Created() uint64 { return r.created.Load() }

// Recycled returns the total sessions recycled at the context-usage threshold.
func (r *Registry) Recycled() uint64 { return r.recycled.Load() }

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
func (r *Registry) Start(_ context.Context) {
	r.wg.Add(1)
	go r.reaperLoop()
}

// Get returns the existing entry for sid, or lazy-creates one and
// caches it under r.entries[sid]. (D-05, D-06, Pitfall 4)
//
// Lazy-create race resolution (Pitfall 4) is via the creating sentinel
// + ready chan:
//
//  1. First caller observing no entry installs a placeholder Entry with
//     creating=true under r.mu, drops the lock, runs the slow
//     Spawn+Initialize+NewSession sequence, then re-acquires the lock to
//     publish the fully-populated entry (or removes it on error) and
//     close(ready).
//  2. Concurrent same-sid callers observe creating==true, release r.mu,
//     wait on <-e.ready, then re-acquire r.mu and re-read entries[sid].
//     This deterministically gives a single Spawn per sid.
//
// SESSION_MAX gate (D-06) is enforced at the placeholder-install step:
// if len(entries) >= cfg.MaxSessions and sid is not already present,
// return ErrSessionMaxExceeded without installing a placeholder.
//
// Dead entries are treated as not-present: deleted from the map and
// the lazy-create path runs to produce a replacement entry. This
// matches the "Get returns alive entry OR creates new one" contract.
func (r *Registry) Get(ctx context.Context, sid, cwd string) (*Entry, error) {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, ErrRegistryClosed
		}
		if e, ok := r.entries[sid]; ok {
			if e.Dead {
				// Dead entry: delete and fall through to lazy-create.
				delete(r.entries, sid)
			} else if e.creating {
				// Pitfall 4: another caller is mid-create for this
				// sid. Drop r.mu, wait for completion, then retry the
				// lookup.
				ready := e.ready
				r.mu.Unlock()
				select {
				case <-ready:
					continue // re-loop to re-read entries[sid]
				case <-ctx.Done():
					return nil, fmt.Errorf("session: get cancelled while waiting for racing create: %w", ctx.Err())
				case <-r.closing:
					return nil, ErrRegistryClosed
				}
			} else if r.cfg.RecyclePct > 0 && e.ctxPct() >= r.cfg.RecyclePct && e.Mu.TryLock() {
				// Track 2 proactive recycle: this session crossed
				// CTX_RECYCLE_PCT, so drop it and lazy-recreate on this
				// same Get (the client re-sends the full transcript, so the
				// fresh session loses nothing — see the context-recycle
				// design).
				//
				// Recycle is guarded by e.Mu.TryLock (mirroring the reaper's
				// D-12 TryLock): we recycle ONLY when we can take the entry's
				// Mu, i.e. no request is streaming on it. If TryLock fails the
				// entry is mid-request — closing its client would truncate the
				// live stream — so we fall through to serve it normally and
				// recycle on a later idle Get. A continuously-busy session
				// therefore recycles at its first gap, exactly as a
				// continuously-streaming session is never reaped.
				//
				// Holding e.Mu across the delete + Close makes the teardown
				// safe: no stream is active, and once deleted from the map a
				// concurrent Get lazy-creates a fresh entry. Delete under r.mu;
				// Close OUTSIDE r.mu (never hold r.mu across a slow Close). The
				// fresh entry starts at ctxPct 0, so the guard is one-shot.
				trippingPct := e.ctxPct()
				old := e
				delete(r.entries, sid)
				r.recycled.Add(1)
				r.mu.Unlock()
				if r.cfg.Logger != nil {
					r.cfg.Logger.Info("session.recycled",
						"sid", shortSid(sid), "ctx_pct", trippingPct,
						"threshold", r.cfg.RecyclePct)
				}
				if old.Client != nil {
					old.Client.Cancel(old.SessionID)
					_ = old.Client.Close()
				}
				old.Mu.Unlock()
				continue // re-loop → lazy-create a replacement entry
			} else {
				// Alive + ready: refresh LastUsed under r.mu before
				// returning so the reaper's snapshot-then-iterate cannot
				// reap this entry between our handoff and the handler's
				// entry.Mu.Lock. Closes audit
				// session-reap-vs-get-handoff-race: the previous code
				// returned without touching LastUsed (per D-11 "advance
				// only at response complete"), but D-11 is about
				// stream-continuity reaping which D-12's TryLock already
				// covers. The handoff-window race is a separate boundary
				// that requires a fresh timestamp.
				// P-5 fix (REL-POOL-05): atomic write replaces the
				// previous unguarded `e.LastUsed = time.Now()` (multi-word
				// time.Time wrote raced MarkUsed at entry_acp.go).
				e.lastUsedNs.Store(time.Now().UnixNano())
				r.mu.Unlock()
				return e, nil
			}
		}
		// SESSION_MAX gate (D-06).
		if len(r.entries) >= r.cfg.MaxSessions {
			r.mu.Unlock()
			return nil, ErrSessionMaxExceeded
		}
		// Install creation sentinel + drop the lock so Spawn does not
		// block other registry ops.
		placeholder := &Entry{
			creating: true,
			ready:    make(chan struct{}),
		}
		r.entries[sid] = placeholder
		r.mu.Unlock()
		return r.createEntry(ctx, sid, cwd, placeholder)
	}
}

// recorderTurnMeter / recorderMCPInit return the acp hook forwarders for the
// shared metrics recorder, or nil when no recorder is wired (so
// handleNotification no-ops). OnContextPct is handled inline in createEntry
// because it also drives per-session recycle (independent of the recorder).
func (r *Registry) recorderTurnMeter() func(float64, int64, float64, bool) {
	rec := r.cfg.Metrics
	if rec == nil {
		return nil
	}
	return rec.RecordTurnMeter
}

func (r *Registry) recorderMCPInit() func(string, bool) {
	rec := r.cfg.Metrics
	if rec == nil {
		return nil
	}
	return rec.RecordMCPInit
}

// shortSid truncates a client-supplied X-Session-Id to its first 8 bytes for
// low-noise log correlation (the recycle INFO line). Empty stays empty.
func shortSid(sid string) string {
	if len(sid) <= 8 {
		return sid
	}
	return sid[:8]
}

// createEntry runs the slow Spawn + Initialize + NewSession sequence
// outside r.mu. On success it publishes the fully-populated entry; on
// error it removes the placeholder so subsequent Get calls retry. In
// both cases close(placeholder.ready) signals any same-sid waiters.
//
// Plan 05-04 SC3 fix (H-B root cause — see
// .planning/phases/05-pool-stateful-sessions/05-04-WIRE-DIFF.md):
// kiro-cli's session/prompt returns rpc error -32603 "Improperly formed
// request" against every prompt issued on a session whose session/new
// was called with an empty cwd. The pool path is unaffected because
// engine.Run resolves cwd via engine.pickCwd which falls back to
// os.Getwd(). The registry must do the same; the simplest cwd guarantee
// is local — if the caller supplies "" (the default when KIRO_CWD env
// var is unset), substitute os.Getwd() before calling client.NewSession.
// Importing internal/engine to reuse pickCwd is forbidden by
// .go-arch-lint.yml (the session→engine→session cycle would surface
// once engine grows registry-aware helpers), so the fallback is inlined
// here.
func (r *Registry) createEntry(ctx context.Context, sid, cwd string, e *Entry) (*Entry, error) {
	// Best-effort cleanup on error: remove placeholder + close ready +
	// optionally close the freshly-spawned client.
	publishError := func(client PoolClient, err error) (*Entry, error) {
		r.mu.Lock()
		// Only delete if the placeholder is still ours — another
		// concurrent path (e.g., Delete) may have removed it.
		if cur, ok := r.entries[sid]; ok && cur == e {
			delete(r.entries, sid)
		}
		r.mu.Unlock()
		e.closeReady()
		if client != nil {
			_ = client.Close()
		}
		return nil, err
	}

	// Plan 05-04 SC3 fix: ensure cwd is non-empty before calling
	// client.NewSession. kiro-cli rejects subsequent session/prompt
	// against an empty-cwd session with rpc error -32603 (see WIRE-DIFF).
	if cwd == "" {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return publishError(nil, fmt.Errorf("session: resolve cwd %q: %w", sid, wdErr))
		}
		cwd = wd
	}

	client, err := r.cfg.Factory.Spawn(ctx, acp.Config{
		Logger:       r.cfg.Logger,
		Command:      r.cfg.KiroCmd,
		Args:         r.cfg.KiroArgs,
		Cwd:          cwd,
		PingInterval: r.cfg.PingInterval,
		// Kiro usage-metrics parity: OnContextPct drives ONLY the per-session
		// recycle signal (e.lastCtxPct) — it fires on every mid-turn frame, so
		// it does no Prometheus work. The ctx histogram is observed once per turn
		// from OnTurnMeter's end-of-turn ctx. OnTurnMeter / OnMCPInit forward to
		// the shared recorder. Wired per-entry because OnContextPct closes over
		// this specific *Entry.
		OnContextPct: func(pct float64) { e.setCtxPct(pct) },
		OnTurnMeter:  r.recorderTurnMeter(),
		OnMCPInit:    r.recorderMCPInit(),
	})
	if err != nil {
		return publishError(nil, fmt.Errorf("session: spawn %q: %w", sid, err))
	}
	if err := client.Initialize(ctx); err != nil {
		return publishError(client, fmt.Errorf("session: initialize %q: %w", sid, err))
	}
	sessionID, err := client.NewSession(ctx, cwd)
	if err != nil {
		return publishError(client, fmt.Errorf("session: new-session %q: %w", sid, err))
	}

	// Publish: under r.mu fill the entry fields, clear creating, then
	// close(ready) so waiters observe the populated entry on retry.
	r.mu.Lock()
	// Defensive: if the placeholder was removed (e.g., DELETE raced),
	// abandon the work and best-effort close the spawned client.
	cur, stillOurs := r.entries[sid]
	if !stillOurs || cur != e {
		r.mu.Unlock()
		e.closeReady()
		_ = client.Close()
		return nil, fmt.Errorf("session: create %q: entry removed concurrently", sid)
	}
	e.Client = client
	e.SessionID = sessionID
	// P-5 fix (REL-POOL-05): atomic write replaces the previous
	// `e.LastUsed = time.Now()` for the createEntry publish path.
	e.lastUsedNs.Store(time.Now().UnixNano())
	e.creating = false
	r.mu.Unlock()
	e.closeReady()
	// Kiro usage-metrics parity: count each successful session creation
	// (gw_sessions_created_total). Incremented after publish so a failed
	// create (publishError path) is not counted.
	r.created.Add(1)

	// Spawn a per-Entry watcher that observes client.Done() and flips
	// the entry to Dead on unexpected subprocess exit (OOM, segfault,
	// SIGTERM from outside, broken pipe after laptop sleep/wake). Without
	// this, a crashed subprocess leaves the entry returning 500 to every
	// retry of its sid for up to the full TTL window (default 30 min)
	// until the reaper notices LastUsed.Before(cutoff). r.wg tracks the
	// watcher so Registry.Close cleanly drains it.
	r.wg.Add(1)
	go r.watchEntry(sid, e)
	return e, nil
}

// watchEntry waits for the entry's client to fire Done() and marks the
// entry Dead. Exits cleanly on registry shutdown. Idempotent against
// races with Delete and the reaper: both paths already use the
// "if entries[sid] == e then delete + Dead = true" guard.
func (r *Registry) watchEntry(sid string, e *Entry) {
	defer r.wg.Done()
	if e == nil || e.Client == nil {
		return
	}
	select {
	case <-e.Client.Done():
	case <-r.closing:
		// Registry is shutting down; Close will handle teardown for
		// every entry in its snapshot. No work for the watcher.
		return
	}
	// Subprocess exited. Take r.mu and, only if the map still points to
	// our entry, delete + flip Dead. Concurrent Delete / reap may have
	// already cleared it — that's fine, the guard absorbs the race.
	r.mu.Lock()
	if cur, ok := r.entries[sid]; ok && cur == e {
		delete(r.entries, sid)
		e.Dead = true
	}
	r.mu.Unlock()
	// Best-effort client close — Done() typically fires because Close
	// already ran (Delete, reaper, registry shutdown), so this is a
	// no-op via PoolClient.Close's idempotency. We still call it to
	// cover the readLoop-EOF path (subprocess crash) where the client
	// fired Done() via defer c.cancel() but Close was never invoked.
	_ = e.Client.Close()
	if r.cfg.Logger != nil {
		r.cfg.Logger.Warn("session: subprocess exited unexpectedly",
			"sid", sid,
			"idle_for", time.Since(e.LastUsed()))
	}
}

// Delete tears down a session synchronously. D-08 semantics:
//
//  1. Lock r.mu; look up entries[sid]. If absent, return
//     ErrSessionNotFound (→ 404).
//  2. delete(r.entries, sid) BEFORE dropping r.mu — Codex M-3
//     map-delete-first ensures concurrent Get sees "not present" and a
//     subsequent same-sid Get can lazy-create cleanly.
//  3. Drop r.mu (critical: never hold across slow Close()).
//  4. Best-effort Cancel(e.SessionID) — interrupts in-flight Prompt
//     so any open stream's readLoop sees EOF promptly.
//  5. e.Client.Close() — wraps the close error with sid context.
//
// Map-delete-first means Delete does NOT block on the entry's Mu.
// An in-flight Prompt under Mu sees its readLoop crash via EOF (Pitfall 1)
// — Phase 4's D-06 watchdog covers the response side; the surface
// handler observes the truncated stream and renders an error.
func (r *Registry) Delete(sid string) error {
	r.mu.Lock()
	e, ok := r.entries[sid]
	if !ok {
		r.mu.Unlock()
		return ErrSessionNotFound
	}
	delete(r.entries, sid)
	r.mu.Unlock()

	// If the entry was still mid-creation, it has no Client yet — skip
	// Cancel/Close, but still close the ready chan so waiters unblock
	// and see "not present" on retry.
	if e.Client != nil {
		e.Client.Cancel(e.SessionID)
		if err := e.Client.Close(); err != nil {
			return fmt.Errorf("session: delete %q: close: %w", sid, err)
		}
	}
	if e.creating {
		// CR-02 fix: use idempotent closeReady() instead of the racy
		// select-default close idiom. A concurrent createEntry path
		// could close the channel between the select case-evaluation
		// and the default-close, panicking the process.
		e.closeReady()
	}
	return nil
}

// Close drains the reaper and tears down all entries. Pitfall 5:
//
//  1. closeOnce guards re-entry.
//  2. Mark r.closed=true under r.mu so subsequent Get returns
//     ErrRegistryClosed without racing the entry teardown.
//  3. Snapshot + nil the entries map under r.mu, drop the lock —
//     entries are closed WITHOUT holding r.mu (anti-pattern from
//     pool.closeAll: never hold r.mu across slow Client.Close()).
//  4. close(r.closing) signals reaper exit; r.wg.Wait() blocks
//     bounded by TickInterval + worst-case reapOnce iteration.
//  5. Iterate snapshot, calling Cancel + Close on each. First error
//     wins the return.
func (r *Registry) Close() error {
	var firstErr error
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		entries := r.entries
		r.entries = nil
		r.mu.Unlock()

		close(r.closing)
		r.wg.Wait()

		for sid, e := range entries {
			if e == nil {
				continue
			}
			if e.Client == nil {
				// Mid-creation entry — best-effort close ready
				// so any same-sid waiters unblock. CR-02 fix:
				// use idempotent closeReady() (sync.Once) instead
				// of the racy select-default close idiom.
				e.closeReady()
				continue
			}
			e.Client.Cancel(e.SessionID)
			if err := e.Client.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("session: close %q: %w", sid, err)
			}
		}
	})
	return firstErr
}
