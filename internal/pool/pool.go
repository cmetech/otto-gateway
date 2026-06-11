package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// ErrPoolExhausted is returned by NewSession when all slots are busy past
// the AcquireTimeout deadline. Callers (adapters) should map this to HTTP 503
// with a Retry-After: 5 header and a surface-native error body (D-07).
var ErrPoolExhausted = errors.New("pool: all workers busy; retry in 5s")

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

	// lastSpawnErr / lastSpawnErrAt record the most recent GENUINE
	// respawn failure (ctx-cancellation paths are deliberately excluded
	// so caller-disconnect during a slow spawn does not surface as a
	// pool incident). Surfaced via HealthSummary() → GET /health/pool so
	// operators see WHY the pool shrank without grepping logs. Guarded
	// by mu; never cleared on subsequent success — keep as forensic
	// historical context (operators read lastSpawnErrAt for recency).
	lastSpawnErr   string
	lastSpawnErrAt time.Time
}

// debugLog is the nil-safe DEBUG emitter for pool observability markers
// (pool.acquire / pool.release). cfg.Logger is documented as optional
// (see Config.Logger doc); calling a method on a nil *slog.Logger would
// panic, so guard the call here. Production callers (main.go) always
// wire a real logger; tests may construct a bare Config{}.
func (p *Pool) debugLog(msg string, attrs ...any) {
	if p.cfg.Logger == nil {
		return
	}
	p.cfg.Logger.Debug(msg, attrs...)
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
//
// Phase 5 D-01: spawns a per-slot exit-watcher goroutine (exit_watcher.go)
// AFTER successful Initialize so the watcher observes the slot's Done()
// channel for the rest of the slot's life (until the slot is respawned
// or the pool closes).
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
	slot := &Slot{Label: label, Client: client}
	// WR-01: capture Done() at the spawn site so the watcher
	// goroutine cannot lazily re-evaluate slot.Client.Done() against a
	// later-swapped client. Safe here without p.mu — the slot has
	// just been allocated and no other goroutine holds a reference.
	p.startExitWatcher(slot, client.Done())
	return slot, nil
}

// slotAlive reports whether the slot is alive (not dead). Held under p.mu
// for a short critical section — no slot.Client calls under the lock.
func (p *Pool) slotAlive(slot *Slot) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !slot.dead
}

// respawnSlot synchronously re-spawns a dead slot in place (Phase 5
// POOL-04 + D-02). The caller (Pool.NewSession's dead-slot branch) has
// exclusive ownership of slot — it was just received from p.slots — so
// in-place client replacement is race-free with concurrent acquires.
//
// Ordering is load-bearing (05-RESEARCH.md Pitfall 2 + 05-PATTERNS.md
// §"Insertion 2"):
//  1. Close the OLD client first. This fires the OLD client's Done(),
//     so the OLD exit-watcher's <-slot.Client.Done() branch wins and
//     the OLD watcher exits cleanly via slot.dead = true (which is
//     about to be reset anyway).
//  2. Spawn the NEW client (honors ctx — D-02: ctx-canceled caller
//     does not block on a slow kiro-cli spawn).
//  3. Initialize the NEW client (mirrors initSlot).
//  4. Under p.mu: replace slot.Client and reset slot.dead = false.
//  5. Spawn a fresh exit-watcher for the NEW client.
//
// On any failure the wrapped error is returned; the caller is expected
// to call removeSlot to drop the dead slot from p.all (D-03).
func (p *Pool) respawnSlot(ctx context.Context, slot *Slot) error {
	// Step 1: close OLD client first so OLD exit-watcher exits cleanly
	// via its <-slot.Client.Done() branch (Pitfall 2).
	if slot.Client != nil {
		_ = slot.Client.Close()
	}
	// Step 2: spawn NEW client. ctx is honored — caller cancellation
	// during a slow kiro-cli spawn aborts the respawn promptly (D-02).
	newClient, err := p.cfg.Factory.Spawn(ctx, acp.Config{
		Logger:       p.cfg.Logger,
		Command:      p.cfg.KiroCmd,
		Args:         p.cfg.KiroArgs,
		Cwd:          p.cfg.KiroCWD,
		PingInterval: p.cfg.PingInterval,
	})
	if err != nil {
		// WR-07: distinguish ctx-cancellation (caller disconnect, the
		// normal D-02 abort path) from genuine spawn failures so
		// operator logs do not surface every cancelled request as a
		// "respawn failed" incident.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("pool: respawn slot %s aborted: %w", slot.Label, err)
		}
		p.recordSpawnErr(err)
		return fmt.Errorf("pool: respawn slot %s: spawn: %w", slot.Label, err)
	}
	// Step 3: initialise the NEW client. On failure close it to avoid
	// orphaning a subprocess + writer/reader goroutine trio.
	if err := newClient.Initialize(ctx); err != nil {
		_ = newClient.Close()
		// WR-07: same ctx-cancellation distinction as Spawn above.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("pool: respawn slot %s aborted: %w", slot.Label, err)
		}
		p.recordSpawnErr(err)
		return fmt.Errorf("pool: respawn slot %s: initialize: %w", slot.Label, err)
	}
	// Step 4: replace under p.mu. Short critical section — no client
	// method calls under the lock. WR-01: capture the NEW client's
	// Done() channel under p.mu so the fresh watcher binds to the
	// NEW client deterministically (no scheduler-timing dependency).
	p.mu.Lock()
	slot.Client = newClient
	slot.dead = false
	newDone := newClient.Done()
	p.mu.Unlock()
	// Step 5: spawn a fresh exit-watcher for the NEW client.
	p.startExitWatcher(slot, newDone)
	return nil
}

// removeSlot drops slot from p.all so the pool effective size shrinks
// (Phase 5 D-03 — respawn failure path). Held under p.mu briefly.
// The slot is NOT returned to p.slots; subsequent NewSession callers
// will compete for the remaining alive slots.
func (p *Pool) removeSlot(slot *Slot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.all {
		if s == slot {
			p.all = append(p.all[:i], p.all[i+1:]...)
			return
		}
	}
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
// the configured pool size; Alive counts slots with a live client
// (non-nil Client AND !dead — matches Detail's per-slot view, see
// CR-03); Busy is len(all) - len(slots) — the count of checked-out
// slots.
//
// CR-03 fix: previously Alive counted any slot with `s.Client != nil`.
// Phase 5's dead-slot detection (exit_watcher) sets `slot.dead = true`
// WITHOUT clearing s.Client (the OLD client field stays populated
// until respawnSlot swaps it). That meant /health reported all slots
// alive even when N were dead awaiting lazy respawn — disagreeing
// with /health/agents (which already uses !slot.dead). Use the same
// !dead test here so the two endpoints stay consistent.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	alive := 0
	for _, s := range p.all {
		if s != nil && s.Client != nil && !s.dead {
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

// HealthSummary is the richer pool snapshot returned by HealthSummary()
// and surfaced via GET /health/pool. Adds Healthy + LastSpawnError* to
// the Stats fields so monitors get a single-field "is the pool serving?"
// signal alongside the diagnostic context for why it isn't.
type HealthSummary struct {
	Size           int       // configured cfg.Size
	Alive          int       // !dead && Client != nil count
	Busy           int       // checked-out count
	Healthy        bool      // Size == 0 (degraded by design) OR Alive > 0
	LastSpawnError string    // most recent genuine respawn failure; empty if none
	LastSpawnErrAt time.Time // when the error above was recorded; zero if none
}

// HealthSummary returns the pool snapshot used by GET /health/pool.
// Single mu acquisition for consistency between Alive/Busy/Healthy and
// the lastSpawnErr fields. Healthy semantics deliberately treat the
// "no KIRO_CMD configured" startup mode (Size == 0) as healthy — that
// is the operator's intentional degraded-mode choice, not a failure.
func (p *Pool) HealthSummary() HealthSummary {
	p.mu.Lock()
	defer p.mu.Unlock()
	alive := 0
	for _, s := range p.all {
		if s != nil && s.Client != nil && !s.dead {
			alive++
		}
	}
	busy := len(p.all) - len(p.slots)
	if busy < 0 {
		busy = 0
	}
	return HealthSummary{
		Size:           p.cfg.Size,
		Alive:          alive,
		Busy:           busy,
		Healthy:        p.cfg.Size == 0 || alive > 0,
		LastSpawnError: p.lastSpawnErr,
		LastSpawnErrAt: p.lastSpawnErrAt,
	}
}

// recordSpawnErr captures the most recent GENUINE respawn failure
// (ctx-cancellation paths skip this — they are not pool incidents,
// just normal caller-disconnect during a slow spawn). Called from
// respawnSlot. NEVER cleared on a subsequent success: keep as a
// forensic field so operators can see what went wrong and when, even
// after a self-recovery. Recency is communicated via LastSpawnErrAt.
func (p *Pool) recordSpawnErr(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	p.lastSpawnErr = err.Error()
	p.lastSpawnErrAt = time.Now().UTC()
	p.mu.Unlock()
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

// closeAll closes every slot's Client and marks the pool closed.
//
// Idempotency contract (WR-06): closeAll IS internally idempotent for
// repeated calls because the first invocation nils p.all under p.mu,
// so a second call's `slots := p.all` reads nil and the iteration
// is a no-op. The public Close wraps closeAll in sync.Once so callers
// see firstErr from the original invocation; Warmup's partial-failure
// path calls closeAll directly without going through closeOnce, which
// is why the closeAll body must tolerate being followed by Pool.Close
// (cleanup runs after a Warmup-failure return — both paths fire).
// Do NOT remove the `p.all = nil` line; the no-op-on-second-call
// behaviour rests on it.
func (p *Pool) closeAll() error {
	p.mu.Lock()
	slots := p.all
	p.all = nil
	p.closed = true
	// Drain in-flight sessions: collect (sid, client) pairs to cancel
	// BEFORE calling client.Close(). This ensures kiro-cli processes
	// receive the cancel signal for any sessions that were mid-generation
	// when Close was called (REL-POOL-02). best-effort — errors are
	// intentionally swallowed; subprocess teardown via Close below is the
	// hard kill.
	type inflightEntry struct {
		sid    string
		client PoolClient
	}
	var inflight []inflightEntry
	for sid, slot := range p.sessionSlots {
		if slot != nil && slot.Client != nil {
			inflight = append(inflight, inflightEntry{sid: sid, client: slot.Client})
		}
	}
	p.mu.Unlock()

	// Cancel in-flight sessions BEFORE hard-closing clients so kiro-cli
	// has a chance to clean up any mid-generation state (REL-POOL-02).
	for _, e := range inflight {
		e.client.Cancel(e.sid)
	}

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
//
// When AcquireTimeout is set and all slots are busy past the deadline,
// NewSession returns ErrPoolExhausted instead of blocking indefinitely
// (D-04 / REL-POOL-01). Adapters map this to HTTP 503 + Retry-After: 5.
func (p *Pool) NewSession(ctx context.Context, cwd string) (string, error) {
	// D-04: bounded acquire — timeoutC fires after AcquireTimeout and
	// causes NewSession to return ErrPoolExhausted. A nil channel (when
	// AcquireTimeout == 0) is never selected, preserving the pre-D-04
	// infinite-wait behaviour for tests that explicitly set zero.
	var timeoutC <-chan time.Time
	if p.cfg.AcquireTimeout > 0 {
		timer := time.NewTimer(p.cfg.AcquireTimeout)
		defer timer.Stop()
		timeoutC = timer.C
	}

	var slot *Slot
	select {
	case slot = <-p.slots:
		// acquired
	case <-ctx.Done():
		return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
	// Audit pool-newsession-blocks-on-closed-pool: post-Close NewSession
	// used to race between caller-ctx and a slot-release that returned a
	// closed client (wasted round-trip through respawn → ErrClientClosed
	// during shutdown). Selecting on p.closing returns a clean
	// "pool: closed" error so operators can distinguish shutdown-induced
	// failures from real ones in slog.
	case <-p.closing:
		return "", errors.New("pool: closed")
	case <-timeoutC:
		return "", ErrPoolExhausted
	}
	p.debugLog("pool.acquire", "slot", slot.Label)

	// Phase 5 D-01/D-02/D-03: dead-slot detection + lazy synchronous
	// re-spawn. The per-slot exit-watcher (exit_watcher.go) flips
	// slot.dead to true when slot.Client.Done() fires; this branch
	// observes the flag, respawns synchronously, and on failure drops
	// the slot from p.all so the pool effective size shrinks (D-03).
	if !p.slotAlive(slot) {
		if err := p.respawnSlot(ctx, slot); err != nil {
			// WR-07 / audit pool-respawn-ctx-cancel-shrinks-pool-permanently:
			// distinguish caller-disconnect ctx cancellation from genuine
			// spawn failures. A laptop reconnecting after sleep with
			// multiple cached client tabs hitting dead slots used to walk
			// the pool 4→3→2→1→0 because every disconnect-during-respawn
			// landed in removeSlot. The slot is still dead (we haven't
			// swapped a new client in) so re-queue it; the next acquirer
			// retries the respawn rather than starving on a shrunken pool.
			// recordSpawnErr was intentionally skipped on ctx-cancel inside
			// respawnSlot so /health/pool LastSpawnError stays clean.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				select {
				case p.slots <- slot:
					p.debugLog("pool.respawn.deferred", "slot", slot.Label, "err", err.Error())
				case <-p.closing:
					// Pool shutting down — Close drains p.all itself.
				}
				return "", fmt.Errorf("pool: respawn slot %s deferred: %w", slot.Label, err)
			}
			// WR-07 transient respawn failure: re-queue the slot instead of
			// calling removeSlot. This preserves the pool's effective size so
			// the next acquirer can retry the respawn (REL-POOL-01 D-08).
			// A caller-disconnect (ctx-cancel) landed in the re-queue branch
			// above; this arm is reached only for genuine (non-ctx) transient
			// failures (disk full, fd exhaustion, etc.) — slot stays in p.all.
			select {
			case p.slots <- slot:
				p.debugLog("pool.respawn.transient_requeue", "slot", slot.Label, "err", err.Error())
			case <-p.closing:
				// Pool shutting down — Close drains p.all itself.
			}
			return "", fmt.Errorf("pool: respawn slot %s: %w", slot.Label, err)
		}
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
		p.debugLog("pool.release", "slot", s.Label, "session_id", sid)
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
//
// WR-04 fix: capture the Client pointer UNDER p.mu before releasing the
// lock so a concurrent respawn (e.g., a future refactor that respawns
// a session-bound slot) cannot swap slot.Client between our read and
// the Cancel call. Today the "only NewSession respawns from the free
// queue" invariant makes the unguarded read benign, but capturing
// while we already hold the mutex is free insurance against the
// invariant breaking.
func (p *Pool) Cancel(sid string) {
	p.mu.Lock()
	slot, ok := p.sessionSlots[sid]
	var client PoolClient
	if ok {
		client = slot.Client
	}
	p.mu.Unlock()
	if !ok || client == nil {
		return
	}
	client.Cancel(sid)
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
		p.debugLog("pool.release", "slot", slot.Label, "session_id", sid)
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
