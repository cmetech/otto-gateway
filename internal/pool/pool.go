package pool

import (
	"context"
	"fmt"
	"sync"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// Slot is one warm kiro-cli connection owned by the pool. Client is
// typed as the PoolClient interface (Codex M-2) so tests can inject
// fake clients; production uses *acp.Client via the default factory.
type Slot struct {
	// Label is a stable human-readable identifier (e.g., "slot-0").
	Label string
	// Client is the PoolClient that this slot wraps. Interface-typed
	// per Codex M-2 so fakeClient can be injected in tests.
	Client PoolClient
	// dead is set to true by the per-slot exit-watcher (exit_watcher.go,
	// added in Phase 5 Task 2) when slot.Client.Done() fires. Read by
	// Pool.NewSession's dead-slot branch (Task 2) which then synchronously
	// re-spawns the slot before handing it to the caller. Guarded by p.mu —
	// the watcher acquires p.mu only for the assignment, never holds it
	// across slot.Client method calls.
	dead bool
}

// Pool is a fixed-size warm pool of kiro-cli slots that satisfies
// engine.ACPClient (assertion in Task 2). POOL-01/POOL-02/POOL-03 +
// OBSV-01 + the D-13 model-catalog capture path (Codex H-6 fix:
// capture happens via slot 0's NewSession response, not post-Initialize).
//
// Acquire is a channel receive (<-p.slots); Release is a channel send
// (p.slots <- slot). No sync.Cond, no hand-rolled WaitGroup pool.
//
// Slot release happens on every terminal path (Codex M-3): Result()
// drained, ctx cancelled via the Prompt-spawned watch goroutine, or
// Pool.Cancel called explicitly. sync.Once + map-delete-first pattern
// coordinate the race between Cancel-driven release and Result-driven
// release so the slot returns to p.slots exactly once.
type Pool struct {
	cfg Config

	// slots is the channel of free slots. Capacity equals cfg.Size.
	// Acquire = <-p.slots; Release = p.slots <- slot.
	slots chan *Slot

	// all retains every spawned slot for Stats(), Close(), and Models().
	// Guarded by mu.
	all []*Slot

	// models is the catalog captured from slot 0's NewSession during
	// Warmup. Defensive copy returned by Models().
	models []canonical.ModelInfo

	// sessionSlots maps active session ids to their owning slot. Populated
	// by NewSession (Task 2), consumed by SetModel / Prompt / Cancel.
	// Guarded by mu.
	sessionSlots map[string]*Slot

	// mu guards all, models, sessionSlots, and the closed flag. Held
	// only for short critical sections — never across slot.Client
	// method calls.
	mu sync.Mutex

	// closed flips to true on first Close call to guard subsequent ops.
	closed bool
	// closeOnce ensures the shutdown sequence runs exactly once.
	closeOnce sync.Once

	// closing is closed by Pool.Close BEFORE closeAll so per-slot
	// exit-watcher goroutines (exit_watcher.go, added in Phase 5 Task 2)
	// select-exit deterministically via <-p.closing. Closing this channel
	// BEFORE closeAll's slot.Client.Close calls means the watcher always
	// observes the close-signal first and never flips slot.dead on
	// shutdown teardown. goleak gate in testmain_test.go enforces clean
	// watcher exit on Close.
	closing chan struct{}
}

// New constructs a Pool with the given Config. The slots channel is
// allocated with capacity = cfg.Size (after applyDefaults). No subprocess
// is spawned until Warmup runs.
func New(cfg Config) *Pool {
	cfg.applyDefaults()
	return &Pool{
		cfg:          cfg,
		slots:        make(chan *Slot, cfg.Size),
		sessionSlots: make(map[string]*Slot),
		closing:      make(chan struct{}),
	}
}

// Warmup sequentially (D-07a) spawns + initialises cfg.Size slots. On
// any failure it tears down partial state via closeAll and returns a
// wrapped error — fail-fast per D-07a so main.go aborts startup loudly
// rather than serving traffic with a degraded pool.
//
// On the FIRST slot only (i == 0), Warmup additionally calls
// slot.Client.NewSession(ctx, cfg.KiroCWD) so the model catalog populates
// (Phase 1.1 D-12: AvailableModels returns nil until NewSession has run
// at least once). After capturing the catalog under p.mu, the warmup
// session is cancelled best-effort via slot.Client.Cancel(sid) — engine.Run
// creates a fresh session per request per D-05 / slot-stateless semantics.
//
// Codex H-6 fix: previously the plan called AvailableModels right after
// Initialize, which returned nil because the wire only populates models
// on session/new responses. The NewSession-during-warmup path honors
// D-13 literally.
func (p *Pool) Warmup(ctx context.Context) error {
	for i := 0; i < p.cfg.Size; i++ {
		label := fmt.Sprintf("slot-%d", i)
		slot, err := p.initSlot(ctx, label)
		if err != nil {
			// closeAll's error is intentionally discarded — the
			// warmup error is the primary cause; partial-cleanup
			// errors during the unwind are logged best-effort by
			// the underlying clients (subprocess teardown).
			_ = p.closeAll()
			return fmt.Errorf("pool: warmup slot %d: %w", i, err)
		}
		if i == 0 {
			// Codex H-6 / D-13: capture model catalog from slot 0's
			// session/new response. Phase 1.1 D-12 populates client.models
			// inside NewSession; AvailableModels returns nil until then.
			// Use cfg.KiroCWD as the warmup cwd; engine.Run uses pickCwd
			// per request.
			sid, err := slot.Client.NewSession(ctx, p.cfg.KiroCWD)
			if err != nil {
				_ = slot.Client.Close()
				// closeAll's error intentionally discarded — see
				// comment in the initSlot-error branch above.
				_ = p.closeAll()
				return fmt.Errorf("pool: warmup new-session: %w", err)
			}
			p.mu.Lock()
			p.models = slot.Client.AvailableModels()
			p.mu.Unlock()
			// Best-effort warmup-session cleanup. Phase 2 has no
			// dedicated session-close RPC; Cancel is the closest
			// cleanup primitive and matches D-05 slot-stateless
			// semantics. engine.Run will create fresh sessions per
			// request.
			slot.Client.Cancel(sid)
		}
		p.mu.Lock()
		p.all = append(p.all, slot)
		p.mu.Unlock()
		// Send into the buffered slots channel. Cannot block — channel
		// capacity equals cfg.Size and we have at most cfg.Size slots.
		p.slots <- slot
	}
	return nil
}

// initSlot spawns + initialises a single slot via cfg.Factory. On
// Initialize failure it closes the freshly-spawned client and returns
// a wrapped error.
func (p *Pool) initSlot(ctx context.Context, label string) (*Slot, error) {
	client, err := p.cfg.Factory.Spawn(ctx, acp.Config{
		Logger:       p.cfg.Logger,
		Command:      p.cfg.KiroCmd,
		Args:         p.cfg.KiroArgs,
		Cwd:          p.cfg.KiroCWD,
		PingInterval: p.cfg.PingInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("pool: spawn %s: %w", label, err)
	}
	if err := client.Initialize(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("pool: initialize %s: %w", label, err)
	}
	return &Slot{Label: label, Client: client}, nil
}

// Models returns a defensive copy of the captured model catalog.
// Called by Plan 06's /api/tags handler. Returns nil if Warmup
// failed before slot 0's NewSession captured the catalog.
func (p *Pool) Models() []canonical.ModelInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.models == nil {
		return nil
	}
	out := make([]canonical.ModelInfo, len(p.models))
	copy(out, p.models)
	return out
}

// Stats returns a point-in-time snapshot of pool occupancy. Size is
// the configured pool size; Alive counts non-nil clients; Busy is
// len(all) - len(slots) — the count of checked-out slots.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	alive := 0
	for _, s := range p.all {
		if s != nil && s.Client != nil {
			alive++
		}
	}
	busy := len(p.all) - len(p.slots)
	if busy < 0 {
		busy = 0
	}
	return Stats{
		Size:  p.cfg.Size,
		Alive: alive,
		Busy:  busy,
	}
}

// Close shuts the pool down idempotently. Subsequent calls return nil
// after the first invocation finishes. Slots are closed in reverse
// allocation order; the first error encountered is returned.
//
// Phase 5 D-01: close(p.closing) is the FIRST line of the shutdown body,
// BEFORE closeAll. Per-slot exit-watcher goroutines (exit_watcher.go,
// added in Task 2) select on <-p.closing as their clean-exit branch;
// closing first means the watcher always wins the select against
// <-slot.Client.Done() that would otherwise fire from closeAll's
// slot.Client.Close() calls. This is the goleak-clean ordering.
func (p *Pool) Close() error {
	var firstErr error
	p.closeOnce.Do(func() {
		close(p.closing)
		firstErr = p.closeAll()
	})
	return firstErr
}

// closeAll closes every slot's Client and marks the pool closed. Safe
// to call once (the public Close wraps it in sync.Once). On partial
// failures during Warmup, Warmup invokes closeAll directly to tear
// down whatever has been built so far — closeAll therefore tolerates
// being called on a partially-populated p.all.
func (p *Pool) closeAll() error {
	p.mu.Lock()
	slots := p.all
	p.all = nil
	p.closed = true
	p.mu.Unlock()

	var firstErr error
	// Reverse allocation order so the most-recently-spawned (and
	// therefore likely most-recently-touched) subprocess shuts down
	// first — matches the typical "close last-opened first" stdlib
	// idiom.
	for i := len(slots) - 1; i >= 0; i-- {
		s := slots[i]
		if s == nil || s.Client == nil {
			continue
		}
		if err := s.Client.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("pool: close %s: %w", s.Label, err)
		}
	}
	return firstErr
}

// ----------------------------------------------------------------------------
// engine.ACPClient surface (Task 2)
// ----------------------------------------------------------------------------
//
// NewSession / SetModel / Prompt / Cancel route through an acquired slot
// per D-06. The same slot is held for a full Run lifecycle via the
// sessionSlots map; the slot is released back to p.slots on EVERY
// terminal path (Codex M-3):
//
//  1. Result() drained (happy path) — releases via poolStreamWrapper
//  2. ctx cancelled before Result drains — releases via the ctx-watcher
//     goroutine spawned inside Prompt
//  3. engine-initiated Cancel(sid) — releases via Pool.Cancel
//
// The wrapper's sync.Once-guarded releaseOnce plus the map-delete-first
// pattern (lookup, delete, then send-to-channel-only-if-found) together
// guarantee exactly-one-release across all three races.

// NewSession acquires a slot and creates a kiro-cli session on it. The
// caller's ctx is observed on the acquire path so an aborted request
// does not park forever on a fully-busy pool. On NewSession error the
// slot is returned to p.slots before the error is surfaced (Codex M-3
// error-path leak prevention).
func (p *Pool) NewSession(ctx context.Context, cwd string) (string, error) {
	var slot *Slot
	select {
	case slot = <-p.slots:
		// acquired
	case <-ctx.Done():
		return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
	}

	sid, err := slot.Client.NewSession(ctx, cwd)
	if err != nil {
		// Release the slot synchronously — no sessionSlots entry was
		// recorded yet so we can put it back directly.
		p.slots <- slot
		return "", fmt.Errorf("pool: new-session: %w", err)
	}

	p.mu.Lock()
	p.sessionSlots[sid] = slot
	p.mu.Unlock()
	return sid, nil
}

// SetModel looks up the slot for sid and forwards to slot.Client.SetModel.
// On unknown session it returns a typed error. p.mu is released BEFORE
// the slot.Client call so a slow SetModel never blocks other pool ops.
func (p *Pool) SetModel(ctx context.Context, sid, modelID string) error {
	p.mu.Lock()
	slot, ok := p.sessionSlots[sid]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("pool: unknown session %q", sid)
	}
	if err := slot.Client.SetModel(ctx, sid, modelID); err != nil {
		return fmt.Errorf("pool: set-model: %w", err)
	}
	return nil
}

// Prompt looks up the slot for sid, forwards to slot.Client.Prompt, and
// wraps the returned *acp.Stream in a poolStreamWrapper that releases
// the slot on ANY of three terminal paths (Codex M-3 — Result drained,
// ctx cancelled via the spawned watch goroutine, or engine.Cancel
// called explicitly).
//
// On slot.Client.Prompt error the slot is released synchronously
// (Codex M-3 error-path leak prevention).
func (p *Pool) Prompt(ctx context.Context, sid string, blocks []canonical.Block) (engine.Stream, error) {
	p.mu.Lock()
	slot, ok := p.sessionSlots[sid]
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("pool: unknown session %q", sid)
	}

	raw, err := slot.Client.Prompt(ctx, sid, blocks)
	if err != nil {
		// Codex M-3: release the slot on Prompt error so a size-1 pool
		// is not leaked by a kiro-cli protocol failure.
		p.releaseSlotForSession(sid)
		return nil, fmt.Errorf("pool: prompt: %w", err)
	}

	// release closure — used by all three terminal paths. The
	// map-delete-first pattern means whichever terminal path fires
	// SECOND finds the sessionSlots entry already gone and skips the
	// channel send, so the slot returns to p.slots exactly once.
	release := func() {
		p.mu.Lock()
		s, stillOwned := p.sessionSlots[sid]
		if stillOwned {
			delete(p.sessionSlots, sid)
		}
		p.mu.Unlock()
		if !stillOwned {
			// Another terminal path (Cancel) won the race and already
			// deleted the entry — that path will (or already did) send
			// to p.slots. Skip the send to avoid double-release.
			return
		}
		p.slots <- s
	}

	// ctx-watcher goroutine — Codex M-3. If ctx cancels BEFORE Result()
	// is called, this goroutine releases the slot. If Result() runs
	// first, releaseOnce closes doneCh which cleanly exits this
	// goroutine. goleak (testmain) catches any leaked watchers.
	watchCtx, cancelWatch := context.WithCancel(ctx)
	w := &poolStreamWrapper{
		underlying:  raw,
		release:     release,
		doneCh:      make(chan struct{}),
		cancelWatch: cancelWatch,
	}
	go func() {
		select {
		case <-watchCtx.Done():
			// ctx cancelled (or Result-driven releaseOnce called
			// cancelWatch — that path's doneCh-close branch wins
			// the race, but both branches are safe).
			w.releaseOnce()
		case <-w.doneCh:
			// Result() / Release() already fired — exit cleanly.
		}
	}()
	return w, nil
}

// Cancel forwards to slot.Client.Cancel AND releases the slot back to
// the pool (Codex M-3 fix — the previous design only forwarded the
// cancel, leaking the slot on a size-1 pool whenever an engine-initiated
// cancel did NOT drain the stream).
//
// Missing session is a silent no-op — matches the "best-effort cancel"
// semantics of acp.Client.Cancel (which sends a notification with no
// response expected).
func (p *Pool) Cancel(sid string) {
	p.mu.Lock()
	slot, ok := p.sessionSlots[sid]
	p.mu.Unlock()
	if !ok {
		return
	}
	slot.Client.Cancel(sid)
	// Codex M-3: also release the slot. The wrapper's sync.Once
	// coordinates so a subsequent Result() / ctx-cancel does not
	// double-release — see release closure in Prompt for the
	// map-delete-first race resolution.
	p.releaseSlotForSession(sid)
}

// releaseSlotForSession is the shared release helper used by the
// Prompt-error path and by Cancel. It performs the lookup-delete-then-
// send pattern under mu so it interleaves safely with the wrapper's
// release closure.
func (p *Pool) releaseSlotForSession(sid string) {
	p.mu.Lock()
	slot, ok := p.sessionSlots[sid]
	if ok {
		delete(p.sessionSlots, sid)
	}
	p.mu.Unlock()
	if ok {
		p.slots <- slot
	}
}

// poolStreamWrapper adapts *acp.Stream (Chunks is a FIELD; Result
// returns *acp.FinalResult) to engine.Stream (Chunks is a METHOD;
// Result returns *canonical.FinalResult) AND owns the slot-release
// lifecycle (Codex M-3 — release happens exactly once on Result drained,
// ctx cancelled, or Release called from Pool.Cancel).
type poolStreamWrapper struct {
	underlying  *acp.Stream
	release     func()        // closure: re-add slot + delete sessionSlots[sid]
	released    sync.Once     // ensures release runs exactly once
	doneCh      chan struct{} // closed by releaseOnce so ctx-watcher exits
	cancelWatch context.CancelFunc
}

// Chunks returns the underlying *acp.Stream.Chunks field via a
// method-call shim so engine.Stream is satisfied. Pointer-equality of
// the channel is preserved (no copy / no buffering) so the readLoop's
// pushes flow directly through.
func (w *poolStreamWrapper) Chunks() <-chan canonical.Chunk { return w.underlying.Chunks }

// Result delegates to *acp.Stream.Result and translates the returned
// *acp.FinalResult into a *canonical.FinalResult. After Result returns
// (success or error), releaseOnce fires so the slot returns to the
// pool's free queue.
func (w *poolStreamWrapper) Result() (*canonical.FinalResult, error) {
	fr, err := w.underlying.Result()
	w.releaseOnce()
	if fr == nil {
		return nil, err //nolint:wrapcheck // pure delegation
	}
	return &canonical.FinalResult{
		SessionID:  fr.SessionID,
		ChunkCount: fr.ChunkCount,
		StopReason: fr.StopReason,
	}, err //nolint:wrapcheck // pure delegation
}

// Release is the package-private (per-test, not part of engine.Stream)
// hook that Pool.Cancel uses to force early release without waiting on
// Result. Codex M-3.
func (w *poolStreamWrapper) Release() { w.releaseOnce() }

// releaseOnce coordinates the three terminal paths via sync.Once. It
// cancels the ctx-watcher (so the watcher goroutine exits via its
// ctx.Done() branch), closes doneCh (the watcher's alternate exit
// branch — exactly one of the two branches will win), then invokes
// the release closure exactly once.
func (w *poolStreamWrapper) releaseOnce() {
	w.released.Do(func() {
		if w.cancelWatch != nil {
			w.cancelWatch()
		}
		close(w.doneCh)
		w.release()
	})
}

// Production-path compile-time interface satisfaction check. Build
// failure here means Pool no longer implements engine.ACPClient —
// surface the missing method to the executor.
var (
	_ engine.ACPClient = (*Pool)(nil)
	_ engine.Stream    = (*poolStreamWrapper)(nil)
)
