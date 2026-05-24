package pool

import (
	"context"
	"fmt"
	"sync"

	"loop24-gateway/internal/acp"
	"loop24-gateway/internal/canonical"
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
func (p *Pool) Close() error {
	var firstErr error
	p.closeOnce.Do(func() {
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

// var _ engine.ACPClient = (*Pool)(nil)  // enabled after Task 2 implements the ACPClient surface
