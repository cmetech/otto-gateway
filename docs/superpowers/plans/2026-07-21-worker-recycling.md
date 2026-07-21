# Pool Worker Recycling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bound stateless pool-worker memory by recycling each worker after a configurable number of successful ACP `session/new` calls, ship laptop-oriented pool defaults, and render vacant dashboard slots.

**Architecture:** Count successful session creation on every pool-worker path under `Pool.mu`. Release paths atomically decide whether to return the exclusively owned slot or register a WaitGroup-tracked background respawn; cause-aware respawn keeps crash recovery and scheduled recycling observability distinct. Configuration remains opt-in in the binary, while the wrapper template enables the laptop posture and the dashboard derives vacant cards without changing the snapshot schema.

**Tech Stack:** Go 1.23+, `sync`/`context`/`sync/atomic`, Prometheus Go client, embedded vanilla JavaScript/CSS, Bash, PowerShell, Markdown.

## Global Constraints

- `KIRO_WORKER_MAX_TURNS` is an integer in `[0, 10000]`; binary default `0` disables scheduled recycling.
- The binary `POOL_SIZE` default remains `4`; `scripts/.env.example` actively sets `POOL_SIZE=2` and `KIRO_WORKER_MAX_TURNS=20` for script-based laptop installs.
- `overrides.env` remains the supported shared-host escape hatch and must continue to win over regenerated `.env`.
- `Slot.turns`, `Slot.dead`, `Slot.Client` replacement, and recycle admission are guarded by `Pool.mu`.
- Positive `recycleWG.Add` and `probeWG.Add` calls must be ordered before their corresponding `Wait` calls through the same `Pool.mu`/`p.closed` discipline.
- Never call `PoolClient.Close`, `Spawn`, `Initialize`, `NewSession`, or `Pid` while holding `Pool.mu`.
- Scheduled respawns use a 30-second background timeout and never add spawn latency directly to the releasing request.
- Stateful registry clients and `CTX_RECYCLE_PCT` behavior are unchanged.
- `/admin/api/snapshot` has no wire-format change; vacant cards are client-side projections.
- Do not add Node, jsdom, or another JavaScript test dependency; use the documented manual dashboard matrix.
- Pool tests run with `-race` and the existing package-level goleak gate.
- Preserve the exact lazy success reason `lazy-respawn-success` and the existing `gw_pool_slot_respawns_total` meaning.

---

### Task 1: Configuration, wiring, and wrapper defaults

**Files:**
- Modify: `internal/config/config.go:89`
- Modify: `internal/config/config.go:499`
- Modify: `internal/config/config.go:907`
- Modify: `internal/config/config_test.go:269`
- Modify: `internal/pool/config.go:113`
- Modify: `cmd/otto-gateway/main.go:487`
- Modify: `scripts/.env.example:38`
- Modify: `scripts/gw:1795`
- Modify: `scripts/gw:1898`
- Modify: `scripts/gw.ps1:1547`
- Modify: `scripts/gw.ps1:1614`
- Modify: `tests/scripts/test-support-bundle.sh:97`
- Modify: `tests/scripts/test-support-bundle.ps1:140`

**Interfaces:**
- Produces: `config.Config.KiroWorkerMaxTurns int`.
- Produces: `pool.Config.MaxWorkerTurns int`.
- Consumed by Task 3: `Pool.cfg.MaxWorkerTurns` controls recycle admission.

- [x] **Step 1: Add failing environment-configuration tests**

Add table-driven coverage beside the `POOL_SIZE` tests:

```go
func TestLoad_KiroWorkerMaxTurns(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr string
	}{
		{name: "default disabled", value: "", want: 0},
		{name: "enabled", value: "20", want: 20},
		{name: "negative", value: "-1", wantErr: "KIRO_WORKER_MAX_TURNS: must be >= 0"},
		{name: "above cap", value: "10001", wantErr: "KIRO_WORKER_MAX_TURNS: sanity cap exceeded (max 10000)"},
		{name: "malformed", value: "twenty", wantErr: "KIRO_WORKER_MAX_TURNS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KIRO_WORKER_MAX_TURNS", tc.value)
			cfg, err := config.Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v; want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.KiroWorkerMaxTurns != tc.want {
				t.Fatalf("KiroWorkerMaxTurns = %d; want %d", cfg.KiroWorkerMaxTurns, tc.want)
			}
		})
	}
}
```

- [x] **Step 2: Run the focused test and confirm the red state**

Run: `go test ./internal/config -run TestLoad_KiroWorkerMaxTurns -count=1`

Expected: build failure because `config.Config` has no `KiroWorkerMaxTurns` field.

- [x] **Step 3: Parse, validate, return, and wire the new setting**

Add the public config field and parse it immediately after `POOL_SIZE`:

```go
// KiroWorkerMaxTurns is the number of successful session/new calls a warm
// pool worker may serve before scheduled process recycling. Zero disables.
KiroWorkerMaxTurns int
```

```go
maxWorkerTurns, err := getEnvInt("KIRO_WORKER_MAX_TURNS", 0)
if err != nil {
	errs = append(errs, err)
}
if maxWorkerTurns < 0 {
	errs = append(errs, fmt.Errorf("KIRO_WORKER_MAX_TURNS: must be >= 0, got %d", maxWorkerTurns))
}
if maxWorkerTurns > 10_000 {
	errs = append(errs, fmt.Errorf("KIRO_WORKER_MAX_TURNS: sanity cap exceeded (max 10000), got %d", maxWorkerTurns))
}
```

Set `KiroWorkerMaxTurns: maxWorkerTurns` in the returned `config.Config`, add this field to `pool.Config`, and wire it in `main.go`:

```go
// MaxWorkerTurns schedules a worker respawn after this many successful
// session/new calls. Zero disables scheduled recycling.
MaxWorkerTurns int
```

```go
MaxWorkerTurns: cfg.KiroWorkerMaxTurns,
```

- [x] **Step 4: Run configuration tests**

Run: `go test ./internal/config ./cmd/otto-gateway -count=1`

Expected: PASS.

- [x] **Step 5: Activate the script-distribution defaults and diagnostics**

Replace the commented pool suggestion with:

```dotenv
# Sized for single-user laptops. Shared hosts should set larger values in
# overrides.env; gw upgrade-env never touches that file and overrides win.
POOL_SIZE=2
KIRO_WORKER_MAX_TURNS=20
```

Add `KIRO_WORKER_MAX_TURNS` immediately after `POOL_SIZE` in both `print_env`/`Show-Env` lists and both support-bundle effective-environment lists.

Set `KIRO_WORKER_MAX_TURNS=20` in each support-bundle test invocation and assert the extracted `env/effective.env` contains the literal non-secret line:

```bash
if grep -q '^KIRO_WORKER_MAX_TURNS=20$' "$BUNDLE_ROOT/env/effective.env"; then
    ok "env/effective.env captured KIRO_WORKER_MAX_TURNS"
else
    fail_with "env/effective.env missing KIRO_WORKER_MAX_TURNS"
fi
```

Use the PowerShell equivalent `Select-String -Quiet -Pattern '^KIRO_WORKER_MAX_TURNS=20$'` in `test-support-bundle.ps1`.

- [x] **Step 6: Run wrapper tests**

Run: `bash tests/scripts/test-support-bundle.sh`

Expected: summary reports `failed: 0`.

Run when PowerShell is available: `pwsh -NoProfile -File tests/scripts/test-support-bundle.ps1`

Expected: all assertions pass.

- [x] **Step 7: Commit configuration and rollout changes**

```bash
git add internal/config/config.go internal/config/config_test.go internal/pool/config.go cmd/otto-gateway/main.go scripts/.env.example scripts/gw scripts/gw.ps1 tests/scripts/test-support-bundle.sh tests/scripts/test-support-bundle.ps1
git commit -m "feat(pool): configure worker turn recycling"
```

---

### Task 2: Cause-aware respawn and race-free shutdown snapshots

**Files:**
- Modify: `internal/pool/pool.go:80`
- Modify: `internal/pool/pool.go:423`
- Modify: `internal/pool/pool.go:727`
- Modify: `internal/pool/pool.go:775`
- Modify: `internal/pool/regression_rel_obsv_02_test.go:36`

**Interfaces:**
- Produces: `type respawnCause uint8` with `respawnCauseLazy` and `respawnCauseRecycle`.
- Produces: `func (p *Pool) respawnSlot(context.Context, *Slot, respawnCause) error`.
- Produces: `func (p *Pool) Recycles() uint64`.
- Consumed by Task 3: the background recycler calls `respawnSlot` with `respawnCauseRecycle`.
- Consumed by Task 4: metrics wiring reads `Pool.Recycles()`.

- [x] **Step 1: Lock the lazy-respawn contract with failing assertions**

Extend the existing observability regression test so a lazy respawn still asserts:

```go
if got, want := recovered["reason"], "lazy-respawn-success"; got != want {
	t.Fatalf("reason = %v; want %q", got, want)
}
if got := p.Respawns(); got != 1 {
	t.Fatalf("Respawns() = %d; want 1", got)
}
if got := p.Recycles(); got != 0 {
	t.Fatalf("Recycles() = %d; want 0", got)
}
```

- [x] **Step 2: Confirm the new accessor is red**

Run: `go test -race ./internal/pool -run TestRegression_REL_OBSV_02 -count=1`

Expected: build failure because `Recycles` and the race-safe snapshot implementation do not exist.

- [x] **Step 3: Add respawn causes and distinct success accounting**

Add:

```go
type respawnCause uint8

const (
	respawnCauseLazy respawnCause = iota
	respawnCauseRecycle
)

func (c respawnCause) successReason() string {
	if c == respawnCauseRecycle {
		return "recycle-respawn-success"
	}
	return "lazy-respawn-success"
}
```

Add `recycles atomic.Uint64` beside `respawns`, expose `Recycles`, change every existing call to `respawnSlot(ctx, slot, respawnCauseLazy)`, and change the method signature to accept the cause.

For both `Spawn` and `Initialize` errors, suppress context cancellation only for lazy request-owned respawns:

```go
if cause == respawnCauseLazy &&
	(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
	return fmt.Errorf("pool: respawn slot %s aborted: %w", slot.Label, err)
}
p.recordSpawnErr(err)
```

Use `cause.successReason()` in the success log and increment exactly one counter:

```go
if cause == respawnCauseRecycle {
	p.recycles.Add(1)
} else {
	p.respawns.Add(1)
}
```

- [x] **Step 4: Snapshot immutable clients under `Pool.mu`**

Inside `closeAll`, replace the `[]*Slot` post-unlock traversal with values captured under the lock:

```go
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
```

After unlocking, close `targets` in reverse order and report errors with the captured label. Never dereference `slot.Client` in that loop.

- [x] **Step 5: Run pool race and regression tests**

Run: `go test -race ./internal/pool -run 'TestRegression_REL_OBSV_02|TestPool_DeadSlot' -count=20`

Expected: PASS with no race report; lazy reason remains byte-exact and only `Respawns()` increments. Task 3 adds the direct concurrent Close/respawn regression once the background respawn path exists.

- [x] **Step 6: Commit the respawn foundation**

```bash
git add internal/pool/pool.go internal/pool/regression_rel_obsv_02_test.go
git commit -m "refactor(pool): distinguish recycle respawns"
```

---

### Task 3: Turn accounting, background recycling, and shutdown ordering

**Files:**
- Modify: `internal/pool/pool.go:80`
- Modify: `internal/pool/pool.go:111`
- Modify: `internal/pool/pool.go:312`
- Modify: `internal/pool/pool.go:326`
- Modify: `internal/pool/pool.go:346`
- Modify: `internal/pool/pool.go:486`
- Modify: `internal/pool/pool.go:727`
- Modify: `internal/pool/pool.go:1019`
- Modify: `internal/pool/pool.go:1077`
- Modify: `internal/pool/pool.go:1191`
- Modify: `internal/pool/export_test.go:11`
- Modify: `internal/pool/worker_recycle_test.go`
- Modify: `internal/pool/model_discovery_test.go:20`

**Interfaces:**
- Consumes: `pool.Config.MaxWorkerTurns` from Task 1.
- Consumes: `respawnCauseRecycle` and cause-aware `respawnSlot` from Task 2.
- Produces: `func (p *Pool) releaseOrRecycle(*Slot)` used by prompt, cancel/error, and self-heal releases.
- Produces: `Slot.turns int`, guarded by `Pool.mu`.

- [x] **Step 1: Add failing turn-count and threshold tests**

Use the existing `fakeClient`/`fakeClientFactory` harness in `worker_recycle_test.go`. Add test-only accessors in `export_test.go`:

```go
func (p *Pool) SlotTurns(label string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, slot := range p.all {
		if slot != nil && slot.Label == label {
			return slot.turns, true
		}
	}
	return 0, false
}

func (p *Pool) SetRecycleLaunchHookForTesting(hook func()) {
	p.mu.Lock()
	p.recycleLaunchHook = hook
	p.mu.Unlock()
}
```

Cover these observable cases:

- Warmup catalog `session/new` counts once.
- A successful request increments; a failed `session/new` does not.
- `MaxWorkerTurns=2` recycles on the first completed request after warmup.
- `MaxWorkerTurns=0` returns the original client without spawning.
- Happy `Result`, `Pool.Cancel`, prompt error, and self-heal return all reach `releaseOrRecycle`.
- Recycle failure requeues the slot dead; the next acquire performs lazy recovery.
- A recycle-cause `context.DeadlineExceeded` records `LastSpawnError`; lazy cancellation remains suppressed.

The primary happy-path test should use the existing package-wide fake harness:

```go
func TestPool_WorkerRecycleAtThreshold(t *testing.T) {
	oldClient := &fakeClient{
		models: []canonical.ModelInfo{{ID: "auto"}},
		pid:    1001,
	}
	newClient := &fakeClient{pid: 1002}
	p := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		MaxWorkerTurns: 2,
		Factory:        &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup(): %v", err)
	}

	sid, err := p.NewSession(context.Background(), "")
	if err != nil {
		t.Fatalf("NewSession(): %v", err)
	}
	stream, err := p.Prompt(context.Background(), sid, nil)
	if err != nil {
		t.Fatalf("Prompt(): %v", err)
	}
	drainChunks(stream.Chunks())
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result(): %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for p.Recycles() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := p.Recycles(); got != 1 {
		t.Fatalf("Recycles() = %d; want 1", got)
	}
	if got := p.Respawns(); got != 0 {
		t.Fatalf("Respawns() = %d; want 0", got)
	}
	if turns, ok := p.SlotTurns("slot-0"); !ok || turns != 0 {
		t.Fatalf("SlotTurns(slot-0) = (%d, %v); want (0, true)", turns, ok)
	}
}
```

- [x] **Step 2: Run the focused suite and confirm the red state**

Run: `go test -race ./internal/pool -run 'TestPool_WorkerTurns|TestPool_WorkerRecycle|TestPool_CatalogProbeCounts' -count=1`

Expected: build failures for missing `turns`, `releaseOrRecycle`, and the test accessors.

- [x] **Step 3: Count every successful pool-worker session**

Add `turns int` to `Slot`. Change catalog helpers to accept the slot:

```go
func (p *Pool) probeCatalogOnce(ctx context.Context, slot *Slot) ([]canonical.ModelInfo, error) {
	sid, err := slot.Client.NewSession(ctx, p.cfg.KiroCWD)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	slot.turns++
	p.mu.Unlock()
	models := slot.Client.AvailableModels()
	slot.Client.Cancel(sid)
	return models, nil
}
```

Change `captureCatalogWithRetry` to take `*Slot`, update warmup and self-heal callers, and increment `slot.turns` in the existing `Pool.NewSession` map critical section:

```go
p.mu.Lock()
p.sessionSlots[sid] = slot
slot.turns++
p.mu.Unlock()
```

Reset `slot.turns = 0` in the same respawn swap critical section that assigns the fresh client and clears `slot.dead`.

- [x] **Step 4: Implement atomic recycle admission and the worker goroutine**

Add `recycleWG sync.WaitGroup` and the test hook to `Pool`. Implement `releaseOrRecycle` so the threshold decision and positive `Add` happen in one lock acquisition:

```go
func (p *Pool) releaseOrRecycle(slot *Slot) {
	p.mu.Lock()
	turns := slot.turns
	if p.cfg.MaxWorkerTurns == 0 || turns < p.cfg.MaxWorkerTurns || p.closed {
		p.slots <- slot
		p.mu.Unlock()
		return
	}
	p.recycleWG.Add(1)
	hook := p.recycleLaunchHook
	p.mu.Unlock()

	if hook != nil {
		hook()
	}
	go p.recycleSlot(slot, turns)
}
```

Implement the owned-slot lifecycle with a 30-second timeout:

```go
const recycleRespawnTimeout = 30 * time.Second

func (p *Pool) recycleSlot(slot *Slot, turns int) {
	defer p.recycleWG.Done()
	select {
	case <-p.closing:
		return
	default:
	}
	if p.cfg.Logger != nil {
		p.cfg.Logger.Info("pool: slot recycling", "label", slot.Label, "turns", turns)
	}
	ctx, cancel := context.WithTimeout(context.Background(), recycleRespawnTimeout)
	defer cancel()
	err := p.respawnSlot(ctx, slot, respawnCauseRecycle)

	p.mu.Lock()
	if p.closed {
		client := slot.Client
		p.mu.Unlock()
		if err == nil && client != nil {
			_ = client.Close()
		}
		return
	}
	if err != nil {
		slot.dead = true
	}
	select {
	case p.slots <- slot:
	default:
		if p.cfg.Logger != nil {
			p.cfg.Logger.Error("pool: recycle requeue failed", "label", slot.Label)
		}
	}
	p.mu.Unlock()
}
```

Replace the two serving releases and the self-heal defer with `releaseOrRecycle`. Preserve existing release debug logs and `advanceProgress` calls.

- [x] **Step 5: Fix `probeWG` admission and Close wait order**

After the `catalogProbing` CAS, admit a self-heal probe under `p.mu`:

```go
p.mu.Lock()
if p.closed {
	p.mu.Unlock()
	p.catalogProbing.Store(false)
	return
}
p.probeWG.Add(1)
p.mu.Unlock()
```

Keep the goroutine’s early `p.closing` selection. In `Pool.Close`, wait in this order after `closeAll`:

```go
p.probeWG.Wait()
p.recycleWG.Wait()
```

The order is required because a finishing self-heal probe can call `releaseOrRecycle` and register recycle work before `probeWG.Done`.

- [x] **Step 6: Add deterministic shutdown interleaving tests**

Use `SetRecycleLaunchHookForTesting` with entered/release channels to prove `Close` waits after recycle admission but before goroutine launch. Add a gated factory to cover Close during spawn and Close after client swap. Each case must assert:

```go
select {
case err := <-closeDone:
	if err != nil {
		t.Fatalf("Close(): %v", err)
	}
case <-time.After(time.Second):
	t.Fatal("Close did not finish after recycle work was released")
}
```

Also assert the replacement fake’s `closeCalls == 1` or greater, no slot is pushed by the recycle goroutine after shutdown, and the package goleak gate remains clean.

- [x] **Step 7: Run the pool suite repeatedly under the race detector**

Run: `go test -race ./internal/pool -count=20`

Expected: PASS with no race or goleak report.

- [x] **Step 8: Commit worker lifecycle behavior**

```bash
git add internal/pool/pool.go internal/pool/export_test.go internal/pool/worker_recycle_test.go internal/pool/model_discovery_test.go
git commit -m "feat(pool): recycle workers after bounded turns"
```

---

### Task 4: Metrics and operator documentation

**Files:**
- Modify: `internal/metrics/metrics.go:57`
- Modify: `internal/metrics/collector.go:8`
- Modify: `internal/metrics/metrics_test.go:72`
- Modify: `cmd/otto-gateway/main.go:422`
- Modify: `internal/admin/admin.go:80`
- Modify: `internal/admin/admin.go:625`
- Modify: `internal/admin/handlers_test.go:170`
- Modify: `cmd/otto-gateway/main.go:840`
- Modify: `docs/operating.md:224`
- Modify: `scripts/gen_grafana_dashboard.py:278`
- Regenerate: `docs/grafana/otto-gateway-dashboard.json`
- Modify: `docs/grafana/README.md:25`
- Modify: `CLAUDE.md:37`

**Interfaces:**
- Consumes: `Pool.Recycles()` from Task 2.
- Produces: `metrics.PoolStats.SlotRecycles uint64`.
- Produces: Prometheus counter `gw_pool_slot_recycles_total`.
- Produces: `admin.Deps.KiroWorkerMaxTurns int` for the docs current-value column.

- [x] **Step 1: Add failing metrics and admin-doc assertions**

Extend `TestMetrics_EventCounters`:

```go
m := testMetrics(
	metrics.PoolStats{SlotRespawns: 5, SlotRecycles: 3, PingEscalations: 2, PingSuspendSkips: 7},
	metrics.SessionStats{Reaped: 3},
)
```

Require:

```go
`gw_pool_slot_recycles_total{gateway_id="gw-test-123"} 3`,
```

Add an admin docs test using `Deps{KiroWorkerMaxTurns: 20}` and require both `KIRO_WORKER_MAX_TURNS` and `20` in `/docs`.

- [x] **Step 2: Run focused tests and confirm the red state**

Run: `go test ./internal/metrics ./internal/admin -run 'TestMetrics_EventCounters|TestAdmin_DocsEnvTable_WorkerRecycle' -count=1`

Expected: build failures for missing `SlotRecycles` and `KiroWorkerMaxTurns`.

- [x] **Step 3: Expose the scheduled-recycle counter**

Add `SlotRecycles uint64` to `metrics.PoolStats`. Add a `slotRecycles *prometheus.Desc` field to `poolCollector`, initialize it with:

```go
prometheus.NewDesc(
	"gw_pool_slot_recycles_total",
	"Total scheduled worker recycles (KIRO_WORKER_MAX_TURNS).",
	nil,
	nil,
)
```

Include it in `Describe` and emit it as a counter in `Collect`. Wire the pull snapshot in `main.go`:

```go
SlotRecycles: a.pool.Recycles(),
```

- [x] **Step 4: Document the environment knob with its live value**

Add `KiroWorkerMaxTurns int` to `admin.Deps`, wire `cfg.KiroWorkerMaxTurns`, and add the sorted environment row:

```go
{
	Name:         "KIRO_WORKER_MAX_TURNS",
	Default:      "0 (disabled)",
	Description:  "Successful pool-worker session/new calls before scheduled process recycling. The gw laptop template sets 20; shared hosts override in overrides.env.",
	CurrentValue: strconv.Itoa(h.deps.KiroWorkerMaxTurns),
},
```

Add `POOL_SIZE` and `KIRO_WORKER_MAX_TURNS` rows to `docs/operating.md`, explicitly distinguishing binary defaults from wrapper-template values. Add the env name to the compatibility list in `CLAUDE.md`.

- [x] **Step 5: Add scheduled recycles to the generated Grafana dashboard**

Add this target between respawns and ping escalations:

```python
(f"sum(rate(gw_pool_slot_recycles_total{SEL}[$__rate_interval]))", "scheduled recycles/s"),
```

Rename the panel to `Slot Respawns, Recycles & Ping Escalations`, update its description to distinguish crash recovery from turn-budget maintenance, then run:

Run: `python3 scripts/gen_grafana_dashboard.py`

Expected: `docs/grafana/otto-gateway-dashboard.json` is regenerated and contains `gw_pool_slot_recycles_total`.

Update the dashboard README’s Pool & Session Health row to mention scheduled worker recycles.

- [x] **Step 6: Run observability and docs tests**

Run: `go test ./internal/metrics ./internal/admin ./cmd/otto-gateway -count=1`

Expected: PASS.

Run: `rg -n 'KIRO_WORKER_MAX_TURNS|gw_pool_slot_recycles_total' internal docs scripts CLAUDE.md`

Expected: matches in configuration docs, metrics collector/tests, wrapper diagnostics, Grafana generator, and generated dashboard.

- [x] **Step 7: Commit observability and documentation**

```bash
git add internal/metrics/metrics.go internal/metrics/collector.go internal/metrics/metrics_test.go internal/admin/admin.go internal/admin/handlers_test.go cmd/otto-gateway/main.go docs/operating.md scripts/gen_grafana_dashboard.py docs/grafana/otto-gateway-dashboard.json docs/grafana/README.md CLAUDE.md
git commit -m "feat(metrics): expose scheduled worker recycles"
```

---

### Task 5: Vacant dashboard slot cards

**Files:**
- Modify: `internal/admin/static/js/admin.js:252`
- Modify: `internal/admin/static/js/admin.js:499`
- Modify: `internal/admin/static/css/admin.css:317`
- Modify: `internal/admin/static/css/admin.css:364`

**Interfaces:**
- Consumes: existing snapshot fields `snap.pool.size` and `snap.pool.slots`.
- Produces: client-only placeholder objects `{vacant: true, label: string, pool_size: number}`.
- No server type or JSON field changes.

- [x] **Step 1: Capture a pre-change dashboard baseline**

Run: `go test ./internal/admin -count=1`

Expected: PASS before asset changes.

- [x] **Step 2: Compute card classes wholesale**

Add:

```javascript
function slotCardClass(slot, poolFailed) {
  var classes = ['gw-slot-card'];
  if (slot.vacant) {
    classes.push('is-vacant');
  } else if (!slot.alive) {
    classes.push(poolFailed ? 'is-dead' : 'is-recovering');
  }
  return classes.join(' ');
}
```

Make both `buildSlotCard` and `updateSlotCard` assign `article.className = slotCardClass(slot, poolFailed)`; remove incremental class-list mutation.

- [x] **Step 3: Render vacant content without performance elements**

Handle `slot.vacant` before alive/dead branches in badge and meta builders:

```javascript
var vacant = document.createElement('span');
vacant.className = 'gw-badge is-vacant';
vacant.textContent = 'VACANT';
```

```javascript
el.textContent = 'Not provisioned (POOL_SIZE=' + slot.pool_size + ')';
```

Use one child builder so build and update cannot drift:

```javascript
function slotCardChildren(slot, poolFailed) {
  var children = [
    buildSlotLabel(slot),
    buildSlotBadges(slot, poolFailed),
    buildSlotMeta(slot, poolFailed)
  ];
  if (!slot.vacant) children.push(buildSlotPerf(slot));
  return children;
}
```

Use `article.append.apply(article, children)` for initial build and `article.replaceChildren.apply(article, children)` for update.

- [x] **Step 4: Pad the logical list and pass pool size**

Change `renderSlots` to accept `poolSize`, preserve the size-zero empty state, and pad a copy:

```javascript
var displaySlots = slots.slice();
for (var i = displaySlots.length; i < 4; i++) {
  displaySlots.push({ vacant: true, label: 'slot-' + i, pool_size: poolSize });
}
```

Use `displaySlots` for the child-count comparison, rebuild, and in-place update. Call it with:

```javascript
renderSlots(
  snap.pool ? snap.pool.slots : [],
  poolFailed,
  snap.pool ? snap.pool.size : 0
);
```

`ingestPerf` continues to read the unpadded snapshot array, so vacant cards never create samples.

- [x] **Step 5: Style vacant cards and badges**

Add:

```css
.gw-slot-card.is-vacant {
  border-style: dashed;
  opacity: 0.58;
}

.gw-badge.is-vacant {
  background: rgba(128, 128, 128, 0.1);
  color: var(--gw-fg-muted);
}
```

Verify existing responsive grid rules remain unchanged.

- [x] **Step 6: Run static/admin regression tests**

Run: `go test ./internal/admin -count=1`

Expected: PASS; embedded JS and CSS endpoints still serve successfully.

- [x] **Step 7: Perform the documented manual matrix**

With the browser dashboard left open across restarts, verify:

1. `POOL_SIZE=2`: two live cards and two dashed `VACANT` cards.
2. `POOL_SIZE=4`: four live cards and no placeholders.
3. `KIRO_CMD=` degraded mode: existing empty-state text and no placeholders.
4. Restart from `POOL_SIZE=2` to `POOL_SIZE=3`: `slot-2` changes from vacant to real through the in-place path and its class is exactly `gw-slot-card` plus its current live state, never `is-vacant`.
5. Vacant cards contain no `.gw-slot-perf` or `.gw-spark` descendants in browser developer tools.

- [x] **Step 8: Commit dashboard changes**

```bash
git add internal/admin/static/js/admin.js internal/admin/static/css/admin.css
git commit -m "feat(admin): render vacant pool slots"
```

---

### Task 6: Full verification and release evidence

**Files:**
- Verify only; do not modify source unless a preceding task failed a gate.

**Interfaces:**
- Verifies all interfaces produced by Tasks 1–5 compose in the gateway binary.

- [x] **Step 1: Format all changed Go files**

Run: `go run mvdan.cc/gofumpt@latest -w internal/config internal/pool internal/metrics internal/admin cmd/otto-gateway`

Expected: command exits zero.

- [x] **Step 2: Run the full race-enabled test suite**

Run: `go test -race ./...`

Expected: PASS with no race or goleak report.

- [x] **Step 3: Run vet, build, and pinned lint**

Run: `go vet ./...`

Expected: exit zero.

Run: `go build ./...`

Expected: exit zero.

Run: `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`

Expected: exit zero with no findings.

- [x] **Step 4: Verify generated and rollout artifacts**

Run: `git diff --check`

Expected: no output.

Run: `rg -n '^POOL_SIZE=2$|^KIRO_WORKER_MAX_TURNS=20$' scripts/.env.example`

Expected: exactly one match for each active template setting.

Run: `rg -n 'gw_pool_slot_recycles_total' internal/metrics scripts/gen_grafana_dashboard.py docs/grafana/otto-gateway-dashboard.json`

Expected: collector, test, generator, and generated-dashboard matches.

- [x] **Step 5: Inspect the final commit series and worktree**

Run: `git status --short`

Expected: clean worktree.

Run: `git log --oneline -5`

Expected: separate commits for configuration, respawn foundation, worker lifecycle, observability/docs, and dashboard UI.
