package pool

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// ErrPoolExhausted is returned by NewSession when all slots are busy past
// the AcquireTimeout deadline. Callers (adapters) should map this to HTTP 503
// with a Retry-After: 5 header and a surface-native error body (D-07).
// Re-exported from canonical for TRST-04 compliance (per D-17-01); new code
// should reference canonical.ErrPoolExhausted directly.
var ErrPoolExhausted = canonical.ErrPoolExhausted

// D-18-07 REL-HTTP-07 test-only panic-injection seams. The relevant
// goroutine invokes its probe once near the top of its body; tests
// install `func() { panic(...) }` to drive the defer-recover branch.
// Default nil → no-op in production.
//
// Reads + writes go through panicProbeMu so the race detector observes
// the happens-before relationship between a test installing a probe and
// the goroutine reading it (the goroutine started in a prior subtest
// might still be in scheduler limbo when the next subtest installs a
// new probe).
//
//nolint:gochecknoglobals // package-private test seams, leave nil in production
var (
	panicProbeMu          sync.Mutex
	ctxWatcherPanicProbe  func()
	exitWatcherPanicProbe func()
)

// firePanicProbe atomically reads and invokes a probe. Used by the
// goroutines in pool.go and exit_watcher.go.
func firePanicProbe(p *func()) {
	panicProbeMu.Lock()
	probe := *p
	panicProbeMu.Unlock()
	if probe != nil {
		probe()
	}
}

// setPanicProbe atomically installs a probe. Used by tests via package-
// public helper SetPanicProbeForTest. Returns the previous value so
// callers can restore on cleanup.
func setPanicProbe(p *func(), v func()) func() {
	panicProbeMu.Lock()
	prev := *p
	*p = v
	panicProbeMu.Unlock()
	return prev
}

// SetCtxWatcherPanicProbeForTest installs the ctx-watcher panic probe.
// Returns a function that restores the previous value (use with
// t.Cleanup). Test-only.
func SetCtxWatcherPanicProbeForTest(v func()) func() {
	prev := setPanicProbe(&ctxWatcherPanicProbe, v)
	return func() { setPanicProbe(&ctxWatcherPanicProbe, prev) }
}

// SetExitWatcherPanicProbeForTest installs the exit-watcher panic probe.
// Returns a function that restores the previous value (use with
// t.Cleanup). Test-only.
func SetExitWatcherPanicProbeForTest(v func()) func() {
	prev := setPanicProbe(&exitWatcherPanicProbe, v)
	return func() { setPanicProbe(&exitWatcherPanicProbe, prev) }
}

// respawnCause classifies why respawnSlot is being invoked so the two
// call sites (lazy dequeue-time recovery, and the future scheduled
// recycler from Task 3) can diverge on error classification, success
// log reason, and counters while sharing one implementation.
type respawnCause uint8

const (
	// respawnCauseLazy is the existing dequeue-time recovery path
	// (Pool.NewSession's dead-slot branch). WR-07 ctx-cancel suppression
	// applies only to this cause — a caller-owned ctx cancelling mid-spawn
	// is expected traffic, not a pool incident.
	respawnCauseLazy respawnCause = iota
	// respawnCauseRecycle is the Task 3 background scheduled-recycle path.
	// It does not carry a caller-owned ctx, so ctx-cancellation is treated
	// as a genuine spawn error rather than suppressed.
	respawnCauseRecycle
)

// successReason returns the byte-exact log "reason" field for a
// successful respawn. lazy-respawn-success is locked per CONTEXT.md
// §D-18-05 (TestRegression_REL_OBSV_02 asserts it verbatim) and MUST
// NOT change for respawnCauseLazy.
func (c respawnCause) successReason() string {
	if c == respawnCauseRecycle {
		return "recycle-respawn-success"
	}
	return "lazy-respawn-success"
}

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

	// catalogRetry is the backoff schedule for the warmup catalog-capture
	// retry (Track 1 resilient discovery). Defaulted in New; overridable in
	// tests. len(schedule) retries → up to len+1 attempts.
	catalogRetry []time.Duration

	// catalogProbing is the singleflight guard for the lazy self-heal
	// re-probe fired from Models() when the catalog is empty. CAS false→true
	// launches at most one background probe at a time.
	catalogProbing atomic.Bool
	// probeWG tracks in-flight self-heal probe goroutines so Close waits for
	// them (goleak-clean; probes are otherwise untracked bare goroutines).
	probeWG sync.WaitGroup

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

	// warnOnce throttles the O-1 (REL-CFG-04) "pool: waiting for free
	// slot" Warn so it fires AT MOST ONCE per saturation episode. The
	// non-blocking try-then-park pattern in NewSession checks
	// warnOnce.Do before parking; subsequent parks during the same
	// saturation episode do NOT re-emit the Warn (avoids log spam
	// during sustained load). Reset to a fresh sync.Once in Pool.Close
	// so a post-restart saturation episode emits the Warn again.
	warnOnce sync.Once

	// lastProgressAt is the D-05a observability field — atomic.Int64
	// holding time.Now().UnixNano() of the most recent forward-progress
	// event. Advanced on: every stream chunk push (advanceProgress
	// called from acp.Stream.push when used through the pool wrapper),
	// every ping ack, and every slot release. NOT advanced on slot
	// acquire — acquire alone does not prove forward progress.
	//
	// Read by Plan 16-02's healthHandler via LastProgressAt() to
	// compute the D-05a degraded status: when Busy == Alive == Size
	// AND now.Sub(LastProgressAt()) > 30s the pool is considered
	// "degraded" (alive but stalled).
	lastProgressAt atomic.Int64

	// Track 4b monotonic event counters, surfaced via the Prometheus
	// pull-collector. respawns bumps on each successful lazy respawn;
	// recycles bumps on each successful scheduled-recycle respawn (Task 3);
	// pingEscalations / pingSuspendSkips are incremented by the per-slot
	// acp.Client via the OnPing* hooks wired in initSlot (aggregated here so
	// the counts survive slot respawns).
	respawns         atomic.Uint64
	recycles         atomic.Uint64
	pingEscalations  atomic.Uint64
	pingSuspendSkips atomic.Uint64
}

// Respawns returns the total successful lazy slot respawns since start.
func (p *Pool) Respawns() uint64 { return p.respawns.Load() }

// Recycles returns the total successful scheduled-recycle slot respawns
// since start (Task 3's background recycler). Distinct from Respawns,
// which counts only the dequeue-time lazy recovery path.
func (p *Pool) Recycles() uint64 { return p.recycles.Load() }

// PingEscalations returns the total liveness-ping escalations (worker teardowns).
func (p *Pool) PingEscalations() uint64 { return p.pingEscalations.Load() }

// PingSuspendSkips returns the total ping cycles skipped after a suspend/resume.
func (p *Pool) PingSuspendSkips() uint64 { return p.pingSuspendSkips.Load() }

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
		catalogRetry: defaultCatalogRetry,
	}
}

// defaultCatalogRetry is the warmup catalog-capture backoff schedule (Track 1
// resilient discovery): 3 retries at 250ms → 500ms → 1s absorb a transiently
// cold kiro-cli at boot before the pool degrades to a self-healing empty
// catalog.
var defaultCatalogRetry = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}

// catalogProbeTimeout bounds a single lazy self-heal re-probe.
const catalogProbeTimeout = 10 * time.Second

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
			//
			// Track 1 (resilient discovery, Node 6bbd0c2): a transiently-cold
			// kiro is absorbed by captureCatalogWithRetry (retry+backoff). If
			// the catalog is still empty after retries, DEGRADE rather than
			// abort boot — a gateway serving "auto"-only and self-healing on
			// demand (see selfHealCatalog) beats one that won't start. Slot
			// spawn/Initialize failures (initSlot, above) keep their fail-fast
			// semantics; only an empty/uncooperative catalog degrades here.
			if models := p.captureCatalogWithRetry(ctx, slot.Client); len(models) > 0 {
				p.mu.Lock()
				p.models = models
				p.mu.Unlock()
			} else if p.cfg.Logger != nil {
				p.cfg.Logger.Warn("pool: model catalog empty after warmup retries; serving degraded (auto-only), will self-heal on demand")
			}
		}
		p.mu.Lock()
		p.all = append(p.all, slot)
		p.mu.Unlock()
		// Send into the buffered slots channel. Cannot block — channel
		// capacity equals cfg.Size and we have at most cfg.Size slots.
		p.slots <- slot
	}
	// D-05a (REL-CFG-04): seed lastProgressAt at warmup completion so
	// the post-Warmup steady state is "fresh" rather than the Unix
	// epoch — without this, healthHandler would briefly classify the
	// pool as degraded between Warmup and the first slot release.
	p.advanceProgress()
	return nil
}

// probeCatalogOnce runs one session/new on the given client and captures the
// model catalog it exposes, cancelling the throwaway probe session afterward
// (D-05 slot-stateless: engine.Run makes fresh sessions per request). Shared by
// Warmup (retry loop) and the lazy self-heal path so the two cannot drift.
func (p *Pool) probeCatalogOnce(ctx context.Context, client PoolClient) ([]canonical.ModelInfo, error) {
	sid, err := client.NewSession(ctx, p.cfg.KiroCWD)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller decides whether to retry/degrade; error is transient
	}
	models := client.AvailableModels()
	client.Cancel(sid)
	return models, nil
}

// captureCatalogWithRetry probes the catalog with bounded retry+backoff (Track 1
// resilient discovery). A NewSession error OR an empty catalog triggers a retry;
// after the schedule is exhausted (or ctx is cancelled) it returns nil so the
// caller degrades to self-heal. len(catalogRetry) retries → up to len+1 attempts.
func (p *Pool) captureCatalogWithRetry(ctx context.Context, client PoolClient) []canonical.ModelInfo {
	for attempt := 0; ; attempt++ {
		if models, err := p.probeCatalogOnce(ctx, client); err == nil && len(models) > 0 {
			return models
		}
		if attempt >= len(p.catalogRetry) {
			return nil
		}
		select {
		case <-time.After(p.catalogRetry[attempt]):
		case <-ctx.Done():
			return nil
		}
	}
}

// selfHealCatalog fires at most one background re-probe (singleflight via
// catalogProbing) to recover an empty model catalog once kiro is warm — the
// lazy half of Track 1 resilient discovery. It is non-blocking: it takes a slot
// only if one is immediately free (never contends with request traffic) and
// never blocks the Models() caller. probeWG lets Close wait it out (goleak).
func (p *Pool) selfHealCatalog() {
	if !p.catalogProbing.CompareAndSwap(false, true) {
		return // a probe is already in flight
	}
	// Don't start a probe on a closing pool (avoids a probe outliving Close).
	select {
	case <-p.closing:
		p.catalogProbing.Store(false)
		return
	default:
	}
	p.probeWG.Add(1)
	go func() {
		defer p.probeWG.Done()
		defer p.catalogProbing.Store(false)

		var slot *Slot
		select {
		case slot = <-p.slots:
		case <-p.closing:
			return
		default:
			return // no free slot right now; a later read retries
		}
		defer func() { p.slots <- slot }()
		if !p.slotAlive(slot) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), catalogProbeTimeout)
		defer cancel()
		if models, err := p.probeCatalogOnce(ctx, slot.Client); err == nil && len(models) > 0 {
			p.mu.Lock()
			p.models = models
			p.mu.Unlock()
		}
	}()
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
	client, err := p.cfg.Factory.Spawn(ctx, p.acpSlotConfig())
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
// to re-queue the slot via the same release path as the ctx-cancel
// branch (D-03; the removeSlot path was removed as dead code in Phase 17).
//
// cause distinguishes the lazy dequeue-time recovery path (respawnCauseLazy,
// Pool.NewSession's dead-slot branch) from the Task 3 scheduled-recycle path
// (respawnCauseRecycle): WR-07 ctx-cancellation suppression applies only to
// respawnCauseLazy (a caller-owned ctx cancelling mid-spawn is expected
// traffic there, not a pool incident); the success log reason and the
// counter incremented (respawns vs recycles) also depend on cause.
func (p *Pool) respawnSlot(ctx context.Context, slot *Slot, cause respawnCause) error {
	// D-18-05 REL-OBSV-02: capture OLD pid BEFORE Close so the
	// lazy-respawn-success INFO log (emitted after step 4) can report
	// the previous-pid → new-pid pair for operator correlation against
	// the prior "pool: slot died" record (exit_watcher.go:42). After
	// Close + Wait the OS reaps the pid so reading c.cmd.Process.Pid
	// post-Close would still work today (the field is set at Start),
	// but capturing here is robust against future Close() refactors
	// that null the field.
	var previousPid int
	if slot.Client != nil {
		previousPid = slot.Client.Pid()
	}
	// Step 1: close OLD client first so OLD exit-watcher exits cleanly
	// via its <-slot.Client.Done() branch (Pitfall 2).
	if slot.Client != nil {
		_ = slot.Client.Close()
	}
	// Step 2: spawn NEW client. ctx is honored — caller cancellation
	// during a slow kiro-cli spawn aborts the respawn promptly (D-02).
	newClient, err := p.cfg.Factory.Spawn(ctx, p.acpSlotConfig())
	if err != nil {
		// WR-07: distinguish ctx-cancellation (caller disconnect, the
		// normal D-02 abort path) from genuine spawn failures so
		// operator logs do not surface every cancelled request as a
		// "respawn failed" incident. This suppression is lazy-only: the
		// scheduled recycler (respawnCauseRecycle) has no caller-owned ctx
		// to abort on, so a ctx error there is a genuine spawn failure.
		if cause == respawnCauseLazy &&
			(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return fmt.Errorf("pool: respawn slot %s aborted: %w", slot.Label, err)
		}
		p.recordSpawnErr(err)
		return fmt.Errorf("pool: respawn slot %s: spawn: %w", slot.Label, err)
	}
	// Step 3: initialise the NEW client. On failure close it to avoid
	// orphaning a subprocess + writer/reader goroutine trio.
	if err := newClient.Initialize(ctx); err != nil {
		_ = newClient.Close()
		// WR-07: same ctx-cancellation distinction as Spawn above (lazy-only).
		if cause == respawnCauseLazy &&
			(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return fmt.Errorf("pool: respawn slot %s aborted: %w", slot.Label, err)
		}
		p.recordSpawnErr(err)
		return fmt.Errorf("pool: respawn slot %s: initialize: %w", slot.Label, err)
	}
	// Step 4: replace under p.mu. Short critical section — no client
	// method calls under the lock. WR-01: capture the NEW client's
	// Done() channel under p.mu so the fresh watcher binds to the
	// NEW client deterministically (no scheduler-timing dependency).
	//
	// WR-03 fix (phase 15 review): spawn the exit-watcher UNDER p.mu so
	// a death between the slot.Client swap and watcher install is
	// observable. Previously a death in this window flipped slot.dead
	// only on the NEXT NewSession (via the dead-slot detection path),
	// but a NewSession arriving INSIDE the gap saw slot.dead=false and
	// handed out a slot whose Client.NewSession would return
	// ErrClientClosed. startExitWatcher's `go func() { ... }` is a
	// non-blocking goroutine spawn; the spawned goroutine takes p.mu
	// itself before flipping slot.dead, so calling it under our locked
	// p.mu is safe — the spawned goroutine simply parks on the lock
	// until we unlock.
	p.mu.Lock()
	slot.Client = newClient
	slot.dead = false
	newDone := newClient.Done()
	// Step 5: spawn a fresh exit-watcher for the NEW client (under p.mu).
	p.startExitWatcher(slot, newDone)
	newPid := newClient.Pid()
	label := slot.Label
	p.mu.Unlock()

	// D-18-05 REL-OBSV-02: log the success record AFTER unlock so the
	// critical section stays narrow. Reason is byte-exact
	// "lazy-respawn-success" for respawnCauseLazy per CONTEXT.md §D-18-05
	// (TestRegression_REL_OBSV_02 asserts it verbatim); respawnCauseRecycle
	// uses the distinct "recycle-respawn-success" reason (Task 3). Field key
	// for slot label is "label" mirroring the death log at
	// exit_watcher.go:42 (RESEARCH.md Pattern 3 / Pitfall 5).
	//
	// WR-07: previous_pid=0 indicates a non-spawned (NewWithConn / test
	// fake) client whose Pid() returns 0. In production every slot is
	// spawned via cfg.Factory.Spawn so previous_pid is always > 0 in
	// real deployments; the field is emitted unconditionally to keep
	// the log shape stable for downstream parsers.
	if p.cfg.Logger != nil {
		p.cfg.Logger.Info(
			"pool: slot recovered",
			"label", label,
			"worker_pid", newPid,
			"previous_pid", previousPid,
			"reason", cause.successReason(),
		)
	}
	// Track 4b: exactly one counter increments per successful respawn,
	// selected by cause — respawns stays lazy-only (gw_pool_slot_respawns_total
	// semantics are preserved byte-exact); recycles is new (Task 3/4).
	if cause == respawnCauseRecycle {
		p.recycles.Add(1)
	} else {
		p.respawns.Add(1)
	}
	return nil
}

// acpSlotConfig builds the acp.Config shared by initSlot + respawnSlot,
// including the Track 4b ping-event hooks that aggregate per-worker events into
// pool-level counters (so counts survive slot respawns).
func (p *Pool) acpSlotConfig() acp.Config {
	cfg := acp.Config{
		Logger:            p.cfg.Logger,
		Command:           p.cfg.KiroCmd,
		Args:              p.cfg.KiroArgs,
		Cwd:               p.cfg.KiroCWD,
		PingInterval:      p.cfg.PingInterval,
		OnPingEscalate:    func() { p.pingEscalations.Add(1) },
		OnPingSuspendSkip: func() { p.pingSuspendSkips.Add(1) },
	}
	// Kiro usage-metrics parity: forward each slot's per-turn usage to the
	// shared recorder. Pool slots are stateless, so (unlike the session
	// registry) there is no per-slot context-recycle and no OnContextPct — the
	// ctx histogram is observed once per turn from OnTurnMeter's end-of-turn
	// ctx. Left unset when no recorder is wired.
	if rec := p.cfg.Metrics; rec != nil {
		cfg.OnTurnMeter = rec.RecordTurnMeter
		cfg.OnMCPInit = rec.RecordMCPInit
	}
	// Track 0 capture: forward raw frames to the ring when enabled.
	if p.cfg.Capture != nil {
		cfg.OnRawFrame = p.cfg.Capture
	}
	// Track 3a circuit breaker: forward the max-tool-denials threshold to each slot.
	cfg.MaxToolDenials = p.cfg.MaxToolDenials
	return cfg
}

// Models returns a defensive copy of the captured model catalog.
// Called by Plan 06's /api/tags handler. Returns nil if Warmup
// failed before slot 0's NewSession captured the catalog.
func (p *Pool) Models() []canonical.ModelInfo {
	p.mu.Lock()
	n := len(p.models)
	var out []canonical.ModelInfo
	if n > 0 {
		out = make([]canonical.ModelInfo, n)
		copy(out, p.models)
	}
	p.mu.Unlock()
	// Track 1 lazy self-heal: an empty catalog (cold-boot degrade) triggers a
	// background re-probe so the list recovers without a restart. The read
	// returns the current (possibly still-empty) snapshot immediately.
	if n == 0 {
		p.selfHealCatalog()
	}
	return out
}

// LastProgressAt returns the wall-clock timestamp of the most recent
// forward-progress event (chunk pushed, ping acked, slot released).
// D-05a (REL-CFG-04 — Plan 16-02 consumer): healthHandler computes
// the "degraded" pool status from this — when Busy == Alive == Size
// AND now.Sub(LastProgressAt()) > 30s the pool is alive but stalled.
//
// Returns the Unix epoch (1970-01-01 UTC) when no progress has been
// recorded yet (atomic value zero); callers should compare via
// time.Since() rather than checking IsZero, since Unix(0, 0) is NOT
// time.Time{}.
func (p *Pool) LastProgressAt() time.Time {
	return time.Unix(0, p.lastProgressAt.Load())
}

// IsExhausted reports whether the pool has no slots available — i.e.
// every spawned slot is currently checked out. D-05b (REL-CFG-04 —
// Plan 16-02 consumer): healthHandler returns the "exhausted" status
// when this is true.
//
// Held under p.mu for a short critical section (read-only); never
// crosses a slot.Client method call.
func (p *Pool) IsExhausted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.slots) == 0
}

// advanceProgress is the internal helper that stamps the D-05a
// lastProgressAt field. Called on every forward-progress event.
// Wrapped as a method (rather than inline atomic.Store) so callers
// document the intent at the call site and future refactors can
// add instrumentation in one place.
func (p *Pool) advanceProgress() {
	p.lastProgressAt.Store(time.Now().UnixNano())
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
	// SpawnFailing is a CURRENT-health signal: true only when a genuine spawn
	// error was recorded within the recency window (2x PingInterval). Unlike
	// the sticky LastSpawnError (never cleared on success), it self-clears once
	// the failure ages out, so the dashboard can reserve red for a real current
	// fault instead of pinning a slot red forever after one historical failure.
	SpawnFailing bool
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
	// SpawnFailing is recency-bounded: a genuine spawn error within roughly the
	// last liveness cycle (2x PingInterval). Computed here under p.mu alongside
	// the fields it reads so the flag is consistent with the error it derives
	// from. The sticky LastSpawnError is deliberately NOT used on its own.
	spawnFailing := p.lastSpawnErr != "" &&
		!p.lastSpawnErrAt.IsZero() &&
		time.Since(p.lastSpawnErrAt) < 2*p.cfg.PingInterval
	return HealthSummary{
		Size:           p.cfg.Size,
		Alive:          alive,
		Busy:           busy,
		Healthy:        p.cfg.Size == 0 || alive > 0,
		LastSpawnError: p.lastSpawnErr,
		LastSpawnErrAt: p.lastSpawnErrAt,
		SpawnFailing:   spawnFailing,
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
		// Track 1: wait out any in-flight self-heal probe goroutine so it
		// cannot outlive Close (goleak-clean). closing is already closed, so
		// no new probe will start (selfHealCatalog's closing-check).
		p.probeWG.Wait()
		// WR-01 (phase 16 review): the prior `p.warnOnce = sync.Once{}`
		// reassignment was a data race against in-flight NewSession
		// callers that had passed the non-blocking `<-p.slots` try and
		// were mid-`p.warnOnce.Do(...)`. Overwriting a sync.Once while
		// another goroutine is calling Do on it violates the sync.Once
		// contract (its internal `done atomic.Uint32` and `m sync.Mutex`
		// are not safe to overwrite under concurrent access). The reset
		// existed to re-emit the "pool: waiting for free slot" Warn after
		// a hypothetical post-Close restart — but the pool is not
		// restartable in place: closeAll nils p.all and sets p.closed,
		// so callers must construct a fresh *Pool, which gets a fresh
		// sync.Once already. The reset was dead-code prevention against
		// a use case that does not exist. Deleted.
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
	// Snapshot immutable {label, client} pairs UNDER p.mu rather than the
	// []*Slot pointers themselves. *Slot.Client is mutated by respawnSlot
	// under this same lock (Step 4/lazy respawn, and Task 3's scheduled
	// recycler); reading slot.Client AFTER unlocking would race an
	// in-flight respawnSlot's locked write to slot.Client — an
	// unsynchronized interface-value read. Capturing the label+client pair
	// here means the close loop below never dereferences slot.Client again.
	type closeTarget struct {
		label  string
		client PoolClient
	}
	targets := make([]closeTarget, 0, len(p.all))
	for _, slot := range p.all {
		if slot != nil && slot.Client != nil {
			targets = append(targets, closeTarget{label: slot.Label, client: slot.Client})
		}
	}
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
	//
	// WR-01 fix (phase 15 review): emit an INFO log with the inflight
	// count so operators can correlate "cancels attempted at shutdown" vs
	// what the kiro-cli side actually received in audit logs. The
	// best-effort nature of sendNotification means some cancels may be
	// dropped (writeCh full, clientCtx already cancelled by a parallel
	// path); the count below is the upper bound. The REL-POOL-02 test
	// asserts the per-client cancel parity that this log helps diagnose.
	for _, e := range inflight {
		e.client.Cancel(e.sid)
	}
	if len(inflight) > 0 && p.cfg.Logger != nil {
		p.cfg.Logger.Info("pool.close.cancel_inflight",
			"inflight_count", len(inflight))
	}

	var firstErr error
	// Reverse allocation order so the most-recently-spawned (and
	// therefore likely most-recently-touched) subprocess shuts down
	// first — matches the typical "close last-opened first" stdlib
	// idiom. Closes the captured client values directly — never
	// dereferences slot.Client here (that would reopen the race this
	// snapshot exists to close).
	for i := len(targets) - 1; i >= 0; i-- {
		t := targets[i]
		if err := t.client.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("pool: close %s: %w", t.label, err)
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

	// O-1 fix (REL-CFG-04): non-blocking try first. If a slot is
	// immediately available, take the fast path with no Warn. If all
	// slots are busy, emit Warn("pool: waiting for free slot", ...)
	// AT MOST ONCE per saturation episode via p.warnOnce, then fall
	// through to the blocking select below.
	var slot *Slot
	select {
	case slot = <-p.slots:
		// Fast path: slot was immediately available — no saturation,
		// no Warn, no parking.
	default:
		// All slots busy — emit ONE Warn per saturation episode then
		// park. The warnOnce is reset to a fresh sync.Once in Close()
		// so a saturation episode after a restart re-emits the Warn.
		p.warnOnce.Do(func() {
			if p.cfg.Logger == nil {
				return
			}
			p.mu.Lock()
			busy := len(p.all) - len(p.slots)
			if busy < 0 {
				busy = 0
			}
			size := p.cfg.Size
			p.mu.Unlock()
			p.cfg.Logger.Warn("pool: waiting for free slot",
				"busy", busy,
				"size", size)
		})
		// Blocking acquire with the full set of arms.
		select {
		case slot = <-p.slots:
			// acquired after parking
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
	}
	p.debugLog("pool.acquire", "slot", slot.Label)

	// Phase 5 D-01/D-02/D-03: dead-slot detection + lazy synchronous
	// re-spawn. The per-slot exit-watcher (exit_watcher.go) flips
	// slot.dead to true when slot.Client.Done() fires; this branch
	// observes the flag, respawns synchronously, and on failure drops
	// the slot from p.all so the pool effective size shrinks (D-03).
	if !p.slotAlive(slot) {
		if err := p.respawnSlot(ctx, slot, respawnCauseLazy); err != nil {
			// WR-07 / audit pool-respawn-ctx-cancel-shrinks-pool-permanently:
			// distinguish caller-disconnect ctx cancellation from genuine
			// spawn failures. A laptop reconnecting after sleep with
			// multiple cached client tabs hitting dead slots used to walk
			// the pool 4→3→2→1→0 because every disconnect-during-respawn
			// landed in the dead-slot drop path (removed in Phase 17). The
			// slot is still dead (we haven't swapped a new client in) so
			// re-queue it; the next acquirer
			// retries the respawn rather than starving on a shrunken pool.
			// recordSpawnErr was intentionally skipped on ctx-cancel inside
			// respawnSlot so /health/pool LastSpawnError stays clean.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// CR-01 fix (phase 15 review): re-queue UNDER p.mu with an
				// explicit p.closed check rather than racing close(p.closing)
				// against the channel send. The previous select-on-p.closing
				// arm had a runtime-window between case-selection and the send
				// where Close() could fire and the slot would be re-queued
				// into p.slots after p.all was already nilled and the
				// underlying Client closed — a subsequent NewSession would
				// then dequeue a dead slot. Locking p.mu serialises with
				// closeAll's p.mu critical section; the explicit p.closed
				// check makes the shutdown-induced drop deterministic.
				p.mu.Lock()
				if p.closed {
					p.mu.Unlock()
					return "", fmt.Errorf("pool: closed during respawn: %w", err)
				}
				// WR-03 fix (phase 16 review): mark dead BEFORE re-queue
				// so the next acquirer's slotAlive() check trips the
				// respawn path deterministically. respawnSlot's step 1
				// already called slot.Client.Close() on the OLD client;
				// that close fires Done() which the OLD exit-watcher
				// observes and (asynchronously) takes p.mu to flip
				// slot.dead=true. But there is a window between our
				// p.slots <- slot send below and the exit-watcher
				// acquiring p.mu: a fast NewSession can dequeue the
				// slot, see slot.dead==false in slotAlive, and call
				// NewSession against the already-closed Client — which
				// surfaces a confusing `pool: new-session: acp: client
				// closed` to the handler. Setting dead under p.mu here
				// closes that window: the dequeuer's slotAlive sees
				// dead==true and triggers a fresh respawn.
				slot.dead = true
				select {
				case p.slots <- slot:
					p.debugLog("pool.respawn.deferred", "slot", slot.Label, "err", err.Error())
				default:
					// p.slots is buffered with cap == cfg.Size and p.all
					// length is bounded by the same cap, so this default
					// arm is unreachable in steady state; keep it so a
					// future change that makes the buffer asymmetric
					// (or a duplicate-requeue bug) drops the slot rather
					// than deadlocking under p.mu.
				}
				p.mu.Unlock()
				return "", fmt.Errorf("pool: respawn slot %s deferred: %w", slot.Label, err)
			}
			// WR-07 transient respawn failure: re-queue the slot instead of
			// dropping it from p.all (the dead-slot drop path was removed
			// in Phase 17). This preserves the pool's effective size so
			// the next acquirer can retry the respawn (REL-POOL-01 D-08).
			// A caller-disconnect (ctx-cancel) landed in the re-queue branch
			// above; this arm is reached only for genuine (non-ctx) transient
			// failures (disk full, fd exhaustion, etc.) — slot stays in p.all.
			//
			// CR-01 fix (phase 15 review): same shutdown-race mitigation as
			// the ctx-cancel arm above — re-queue under p.mu + p.closed check.
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				return "", fmt.Errorf("pool: closed during respawn: %w", err)
			}
			// WR-03 fix (phase 16 review): mark dead BEFORE re-queue so
			// the next acquirer's slotAlive() check trips a fresh
			// respawn. Same race as the ctx-cancel arm above — the OLD
			// exit-watcher would set dead asynchronously, but the next
			// NewSession can dequeue first and call NewSession on the
			// already-closed Client. See the ctx-cancel arm for the
			// full rationale.
			slot.dead = true
			select {
			case p.slots <- slot:
				p.debugLog("pool.respawn.transient_requeue", "slot", slot.Label, "err", err.Error())
			default:
				// Unreachable in steady state — see ctx-cancel arm above.
			}
			p.mu.Unlock()
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
		// D-05a (REL-CFG-04): slot release is one of the
		// forward-progress signals consumed by Plan 16-02's
		// healthHandler degraded-status rule.
		p.advanceProgress()
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
	// D-18-07 REL-HTTP-07: capture logger BEFORE goroutine launch so
	// the closure binds to a stable reference (and to avoid a
	// data-race between the running goroutine reading p.cfg.Logger and
	// any later Config mutation — defense in depth, the Config is
	// immutable today).
	ctxWatcherLogger := p.cfg.Logger
	go func() {
		// D-18-07 REL-HTTP-07: defense-in-depth panic recovery. An
		// unrecovered panic in this background goroutine would crash
		// the gateway. Site name "pool-ctx-watcher" is byte-exact per
		// CONTEXT.md §D-18-07. Recovers, logs once, exits cleanly —
		// the slot release path is owned by w.releaseOnce so a panic
		// before that runs results in a leaked slot (caught by goleak
		// in tests; operator sees the panic-recovered log in prod).
		defer func() {
			if r := recover(); r != nil && ctxWatcherLogger != nil {
				ctxWatcherLogger.Error(
					"goroutine panic recovered",
					"site", "pool-ctx-watcher",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
			}
		}()
		// Test-only seam: tests install via SetCtxWatcherPanicProbeForTest
		// to drive the defer-recover branch. Default nil → no-op in
		// production. Goes through firePanicProbe so the race detector
		// sees the happens-before relationship between cross-test
		// probe writes and goroutine reads.
		firePanicProbe(&ctxWatcherPanicProbe)
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
		// D-05a (REL-CFG-04): forward-progress signal.
		p.advanceProgress()
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
