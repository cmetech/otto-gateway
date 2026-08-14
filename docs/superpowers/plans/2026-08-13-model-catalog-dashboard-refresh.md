# Model Catalog Dashboard and Live Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Display the live Kiro model catalog on the main dashboard and refresh it safely, automatically or on demand, without restarting the Gateway or interrupting client traffic.

**Architecture:** A pool-owned catalog manager is the sole writer and snapshot source. It normalizes candidates, applies expansions immediately, confirms removals only after two identical observations, and performs runtime probes through nonblocking idle-slot acquisition. Consumer-owned admin interfaces expose sanitized GET/POST contracts; OpenAI, capability, Ollama, and dashboard surfaces continue sharing one atomic pool catalog.

**Tech Stack:** Go 1.24, `net/http`, `sync`, `context`, chi, embedded Go templates/assets, vanilla ES2018 JavaScript, Node's built-in test runner, and Go's race detector.

## Global Constraints

- Follow test-first RED → GREEN → REFACTOR order in every implementation task.
- Default `MODEL_CATALOG_REFRESH_INTERVAL_SEC` to `900`; `0` disables scheduled refresh; valid nonzero range is `60`–`86400`; every other value fails startup.
- Never queue catalog work behind, interrupt, or preempt client traffic; runtime probes may use only an immediately idle pool slot.
- Run at most one lazy, scheduled, or manual runtime catalog probe at a time.
- Apply valid additions immediately; remove models only after two consecutive valid candidates with the exact same reduced ID set.
- Empty, malformed, timed-out, cancelled, or failed probes retain the last known-good catalog and never confirm removals.
- Preserve size-one pool safety, worker-turn accounting, cancellation, shutdown, race safety, and exactly-once return/recycle behavior.
- Keep `/v1/models`, `/v1/model-capabilities`, `/api/tags`, Ollama model validation, and the dashboard on the same published catalog.
- Do not expose raw ACP responses, upstream errors, model evidence, environment values, paths, credentials, schemas, session details, prompts, completions, tool arguments, or raw frames in the admin API.
- Keep `internal/admin` free of imports from `internal/pool`, `internal/session`, `internal/engine`, and `internal/registry`.
- Do not change model selection, aliases, tool protocol, request routing, or existing client wire contracts.
- Add no dependencies, release changes, tags, pushes, or unrelated refactors.
- Make no unmeasured latency, throughput, or scheduler-overhead claims.
- Preserve unrelated user changes and stage only files owned by the current task.

## File Map

| File | Responsibility |
|---|---|
| `internal/config/config.go` | Parse and validate the refresh interval. |
| `internal/pool/catalog.go` | Snapshot types, normalization, and reconciliation. |
| `internal/pool/catalog_refresh.go` | Idle-slot probe, scheduler, cooldown, and lifecycle. |
| `internal/pool/pool.go` | Warmup, `Models`, and `Close` integration. |
| `internal/admin/model_catalog.go` | Consumer-owned admin types and GET/POST handlers. |
| `cmd/otto-gateway/model_catalog.go` | Pool/registry-to-admin composition adapter. |
| `internal/admin/templates/dashboard.html.tmpl` | Grouped table and refresh controls. |
| `internal/admin/static/js/admin.js` | Fetch, group, sort, render, and action state. |
| `internal/admin/static/css/admin.css` | Responsive catalog presentation. |
| About, Docs, env example, and operator guides | Operator configuration and behavior reference. |

---

### Task 1: Configuration Contract

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: existing `getEnvInt` and joined validation-error pattern.
- Produces: `config.Config.ModelCatalogRefreshInterval time.Duration`, always zero or between one minute and 24 hours after successful load.

- [ ] **Step 1: Write the failing table test**

Add alongside the pool and timeout configuration tests:

```go
func TestLoad_ModelCatalogRefreshInterval(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr string
	}{
		{name: "default", value: "", want: 15 * time.Minute},
		{name: "disabled", value: "0", want: 0},
		{name: "minimum", value: "60", want: time.Minute},
		{name: "maximum", value: "86400", want: 24 * time.Hour},
		{name: "below minimum", value: "59", wantErr: "must be 0 or in [60,86400], got 59"},
		{name: "above maximum", value: "86401", wantErr: "must be 0 or in [60,86400], got 86401"},
		{name: "negative", value: "-1", wantErr: "must be 0 or in [60,86400], got -1"},
		{name: "not integer", value: "later", wantErr: "MODEL_CATALOG_REFRESH_INTERVAL_SEC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MODEL_CATALOG_REFRESH_INTERVAL_SEC", tc.value)
			cfg, err := config.Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load error = %v; want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil { t.Fatalf("Load: %v", err) }
			if cfg.ModelCatalogRefreshInterval != tc.want {
				t.Fatalf("interval = %v; want %v", cfg.ModelCatalogRefreshInterval, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/config -run '^TestLoad_ModelCatalogRefreshInterval$' -count=1
```

Expected: compile failure because the field does not exist.

- [ ] **Step 3: Implement the validated field**

Add to `Config`:

```go
// ModelCatalogRefreshInterval is the pool-owned rediscovery cadence. Zero
// disables only scheduling; warmup, lazy healing, and manual refresh remain.
ModelCatalogRefreshInterval time.Duration
```

In `Load`, next to pool lifecycle settings:

```go
modelCatalogRefreshSec, err := getEnvInt("MODEL_CATALOG_REFRESH_INTERVAL_SEC", 900)
if err != nil {
	errs = append(errs, err)
}
if modelCatalogRefreshSec != 0 && (modelCatalogRefreshSec < 60 || modelCatalogRefreshSec > 86400) {
	errs = append(errs, fmt.Errorf(
		"MODEL_CATALOG_REFRESH_INTERVAL_SEC: must be 0 or in [60,86400], got %d",
		modelCatalogRefreshSec,
	))
}
```

Populate the returned literal:

```go
ModelCatalogRefreshInterval: time.Duration(modelCatalogRefreshSec) * time.Second,
```

- [ ] **Step 4: Verify GREEN and commit**

```bash
go test ./internal/config -run 'ModelCatalogRefreshInterval|PoolSize' -count=1
go test ./internal/config -count=1
git diff --check
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: configure model catalog refresh interval"
```

---

### Task 2: Catalog Normalization and Reconciliation

**Files:**
- Create: `internal/pool/catalog.go`
- Create: `internal/pool/catalog_test.go`

**Interfaces:**
- Consumes: `canonical.ModelInfo`.
- Produces: `CatalogOutcome`, `ModelCatalogSnapshot`, `CatalogRefreshResult`, and an unexported locked `catalogStore` used by Task 3.

- [ ] **Step 1: Write normalization tests**

Create the test in package `pool`:

```go
func TestNormalizeCatalog(t *testing.T) {
	got, err := normalizeCatalog([]canonical.ModelInfo{
		{ID: " auto ", Name: "Auto"},
		{ID: " gpt-5.6-sol ", Name: " GPT 5.6 Sol "},
		{ID: "gpt-5.6-sol", Name: "duplicate loses"},
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
	})
	if err != nil { t.Fatal(err) }
	want := []canonical.ModelInfo{
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
	}
	if !reflect.DeepEqual(got, want) { t.Fatalf("got %#v; want %#v", got, want) }
	if _, err := normalizeCatalog([]canonical.ModelInfo{{ID: "   "}}); !errors.Is(err, errInvalidCatalog) {
		t.Fatalf("blank ID error = %v", err)
	}
	if _, err := normalizeCatalog([]canonical.ModelInfo{{ID: "auto"}}); !errors.Is(err, errEmptyCatalog) {
		t.Fatalf("auto-only error = %v", err)
	}
}
```

- [ ] **Step 2: Write state-machine tests**

Use a fixed clock and cover equal membership, name-only update, pure expansion,
first shrink, identical second shrink, different second candidate, full
recovery, empty/failure while pending, and mixed add/remove. The load-bearing
shrink assertion is:

```go
s := newCatalogStore(15 * time.Minute)
s.initialize([]canonical.ModelInfo{{ID: "claude-sonnet-5"}, {ID: "gpt-5.6-sol"}}, time.Unix(100, 0))
candidate := []canonical.ModelInfo{{ID: "claude-sonnet-5"}}
first, err := s.reconcile(candidate, time.Unix(200, 0))
if err != nil || first.Outcome != CatalogPendingShrink || len(s.snapshot().Models) != 2 {
	t.Fatalf("first=%+v err=%v snapshot=%+v", first, err, s.snapshot())
}
second, err := s.reconcile(candidate, time.Unix(300, 0))
if err != nil || second.Outcome != CatalogShrinkConfirmed || len(s.snapshot().Models) != 1 {
	t.Fatalf("second=%+v err=%v snapshot=%+v", second, err, s.snapshot())
}
```

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/pool -run 'TestNormalizeCatalog|TestCatalogStore' -count=1
```

Expected: compile failure for missing catalog types.

- [ ] **Step 4: Implement the bounded store**

Define these exact public shapes:

```go
type CatalogOutcome string

const (
	CatalogStartup CatalogOutcome = "startup"
	CatalogUnchanged CatalogOutcome = "unchanged"
	CatalogExpanded CatalogOutcome = "expanded"
	CatalogMetadataUpdated CatalogOutcome = "metadata_updated"
	CatalogPendingShrink CatalogOutcome = "pending_shrink"
	CatalogShrinkConfirmed CatalogOutcome = "shrink_confirmed"
	CatalogSkippedBusy CatalogOutcome = "skipped_busy"
	CatalogFailed CatalogOutcome = "failed"
	CatalogCancelled CatalogOutcome = "cancelled"
)

type ModelCatalogSnapshot struct {
	Models []canonical.ModelInfo
	Generation uint64
	RefreshInterval time.Duration
	InProgress bool
	LastAttemptAt, LastSuccessAt, LastUpdatedAt, NextAttemptAt time.Time
	LastOutcome CatalogOutcome
	PendingRemovals int
}

type CatalogRefreshResult struct {
	Outcome CatalogOutcome
	PreviousCount, CandidateCount, PublishedCount, PendingRemovals int
}
```

The private `catalogStore` owns a `sync.RWMutex`, published slice, generation,
interval/timestamps/outcome, and pending candidate/fingerprint/count. Normalize
by trimming, excluding `auto`, rejecting blank IDs, and de-duplicating first
occurrence. Fingerprint sorted exact IDs joined by `"\x00"`. Clone all inputs
and outputs. For mixed add/remove, publish new additions while retaining missing
published rows, then stage the exact normalized candidate.

- [ ] **Step 5: Verify GREEN and commit**

```bash
go test ./internal/pool -run 'TestNormalizeCatalog|TestCatalogStore' -count=1
go test -race ./internal/pool -run 'TestCatalogStore' -count=1
git diff --check
git add internal/pool/catalog.go internal/pool/catalog_test.go
git commit -m "feat: reconcile live model catalog updates"
```

---

### Task 3: Idle-Slot Refresh Lifecycle and Scheduler

**Files:**
- Create: `internal/pool/catalog_refresh.go`
- Create: `internal/pool/catalog_refresh_test.go`
- Modify: `internal/pool/pool.go`
- Modify: `internal/pool/config.go`
- Modify: `internal/pool/export_test.go`
- Modify: `internal/pool/model_discovery_test.go`

**Interfaces:**
- Consumes: Task 2 store; existing `probeCatalogOnce`, `releaseOrRecycle`, `p.slots`, `p.closing`, and probe wait-group ordering.
- Produces: `Pool.CatalogSnapshot()` and `Pool.RefreshModelCatalog(context.Context)` plus typed busy/in-progress/cooldown/unavailable errors carrying bounded retry timing.

- [ ] **Step 1: Write RED manual and terminal tests**

Using the existing fake client, warm with two models, switch
`availableModelsFn` to three, call manual refresh, and assert expansion, one
extra worker turn, one throwaway-session cancel, and one slot return. Add tests
for no idle slot, blocked-probe single-flight, 30-second cooldown, caller
cancellation, size-one pool, `Close` racing a blocked probe, two real shrink
probes, failed second probe, and refresh-triggered worker recycle.

The core assertion is:

```go
result, err := p.RefreshModelCatalog(context.Background())
if err != nil || result.Outcome != pool.CatalogExpanded {
	t.Fatalf("refresh = %+v, %v", result, err)
}
if turns, ok := p.SlotTurns("slot-0"); !ok || turns != baselineTurns+1 {
	t.Fatalf("turns = %d,%v; want %d,true", turns, ok, baselineTurns+1)
}
```

- [ ] **Step 2: Write RED deterministic scheduler tests**

Expose only test-time seams in `export_test.go`:

```go
func (p *Pool) SetCatalogRefreshTicksForTesting(ticks <-chan time.Time) {
	p.catalogRefreshTicks = ticks
}

func (p *Pool) SetCatalogNowForTesting(now func() time.Time) {
	p.catalogNow = now
}
```

Prove a tick expands, a busy tick records `CatalogSkippedBusy`, interval zero
starts no loop, the first refresh is not immediate, and `Close` joins the loop.
Use channel barriers rather than sleeps.

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/pool -run 'CatalogRefresh|CatalogScheduler' -count=1
```

Expected: compile failure for the new pool API.

- [ ] **Step 4: Add pool config and public runtime API**

Add `ModelCatalogRefreshInterval time.Duration` to `pool.Config`. Do not default
zero in the pool package because zero means disabled. Create:

```go
var (
	ErrCatalogRefreshInProgress = errors.New("model catalog refresh already in progress")
	ErrCatalogRefreshBusy = errors.New("model catalog refresh requires an idle pool slot")
	ErrCatalogRefreshCooldown = errors.New("model catalog manual refresh cooldown")
	ErrCatalogRefreshUnavailable = errors.New("model catalog refresh unavailable")
)

type CatalogRefreshError struct {
	Kind error
	RetryAfter time.Duration
}

func (e *CatalogRefreshError) Error() string { return e.Kind.Error() }
func (e *CatalogRefreshError) Unwrap() error { return e.Kind }

func (p *Pool) CatalogSnapshot() ModelCatalogSnapshot
func (p *Pool) RefreshModelCatalog(ctx context.Context) (CatalogRefreshResult, error)
func (p *Pool) refreshModelCatalog(ctx context.Context, source catalogRefreshSource) (CatalogRefreshResult, error)
```

Sources are the fixed strings `lazy`, `scheduled`, and `manual`.

- [ ] **Step 5: Implement safe probe admission**

Order admission as: manual cooldown check, atomic single-flight claim, then one
`p.mu` critical section that either rejects closed state or admits
`probeWG.Add(1)`, followed by nonblocking receive from `p.slots`. Every rejected
path releases the single-flight claim; every admitted path owns one matching
`probeWG.Done`.
After acquisition, call `markSlotCheckedOut` and immediately install exactly
one deferred `releaseOrRecycle(slot)`. Derive a 10-second context from the
caller and cancel it from the pool catalog lifecycle during `Close`.

A scheduled busy result records `CatalogSkippedBusy`; manual busy returns
`ErrCatalogRefreshBusy`. Log only fixed source/outcome, counts, pending count,
and duration. Never perform ACP, channel, recycle, or logging work under the
catalog mutex.

- [ ] **Step 6: Integrate warmup, lazy healing, scheduler, and shutdown**

Replace direct `p.models` access with the catalog store. `Models()` returns the
defensive snapshot and preserves lazy healing when empty. Start the scheduler
after warmup only when interval is positive; wait one interval before its first
probe and use ordinary intervals after busy skips.

During `Close`, close admission, cancel catalog work, join scheduler/probes,
then preserve the established ordering that drains any recycle work registered
by a probe's terminal `releaseOrRecycle` before client teardown completes.

- [ ] **Step 7: Verify GREEN, race safety, and commit**

```bash
go test ./internal/pool -run 'Catalog|ModelDiscovery' -count=1
go test ./internal/pool -count=1
go test -race ./internal/pool -run 'CatalogRefresh|ModelDiscovery|Release|Recycle|Close' -count=1
git diff --check
git add internal/pool/catalog_refresh.go internal/pool/catalog_refresh_test.go internal/pool/pool.go internal/pool/config.go internal/pool/export_test.go internal/pool/model_discovery_test.go
git commit -m "feat: refresh model catalog from idle pool slots"
```

---

### Task 4: Sanitized Admin API

**Files:**
- Create: `internal/admin/model_catalog.go`
- Create: `internal/admin/model_catalog_test.go`
- Modify: `internal/admin/admin.go`

**Interfaces:**
- Consumes: only admin-owned values supplied through `Deps`.
- Produces: `ModelCatalogSource`, `GET /admin/api/model-catalog`, and `POST /admin/api/model-catalog/refresh`.

- [ ] **Step 1: Write fake-driven RED GET tests**

Define a fake implementing:

```go
type ModelCatalogSource interface {
	Snapshot() ModelCatalogView
	Refresh(context.Context) ModelCatalogActionResult
}
```

Assert models is always an array, count includes exactly one `auto`, timestamps
are UTC RFC3339 or omitted, capabilities use only supported/unsupported/unknown,
and evidence/upstream fields never appear.

- [ ] **Step 2: Write RED POST mapping and origin tests**

Table-test these codes:

```go
cases := []struct{ code string; want int }{
	{code: "", want: http.StatusOK},
	{code: "catalog_refresh_in_progress", want: http.StatusConflict},
	{code: "catalog_refresh_cooldown", want: http.StatusTooManyRequests},
	{code: "catalog_refresh_busy", want: http.StatusServiceUnavailable},
	{code: "catalog_refresh_failed", want: http.StatusBadGateway},
	{code: "catalog_refresh_unavailable", want: http.StatusServiceUnavailable},
}
```

Also test nil source, matching/nonmatching `Origin`, `Sec-Fetch-Site:
cross-site`, operator clients without browser headers, bounded `Retry-After`,
and fixed messages that cannot contain a fake raw error.

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/admin -run '^TestModelCatalogAPI_' -count=1
```

- [ ] **Step 4: Implement consumer-owned wire types and routes**

Create:

```go
type ModelCatalogModel struct {
	ID string `json:"id"`
	Name string `json:"name"`
	SelectionMode string `json:"selection_mode"`
	Capabilities map[string]string `json:"capabilities"`
}

type ModelCatalogRefreshView struct {
	Enabled bool `json:"enabled"`
	IntervalSeconds int64 `json:"interval_seconds"`
	InProgress bool `json:"in_progress"`
	LastAttemptAt string `json:"last_attempt_at,omitempty"`
	LastSuccessAt string `json:"last_success_at,omitempty"`
	LastUpdatedAt string `json:"last_updated_at,omitempty"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	LastOutcome string `json:"last_outcome"`
	PendingRemovals int `json:"pending_removals"`
}

type ModelCatalogView struct {
	State string `json:"state"`
	Count int `json:"count"`
	Generation uint64 `json:"generation"`
	Models []ModelCatalogModel `json:"models"`
	Refresh ModelCatalogRefreshView `json:"refresh"`
}

type ModelCatalogActionResult struct {
	Outcome string `json:"outcome,omitempty"`
	Code string `json:"code,omitempty"`
	Message string `json:"message"`
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}
```

Add the source to `Deps` and register both routes. Reject explicit cross-origin browser
requests using Origin/Fetch Metadata, while keeping headerless operator clients
usable. Encode no wrapped errors.

- [ ] **Step 5: Verify GREEN, boundary safety, and commit**

```bash
go test ./internal/admin -run 'ModelCatalogAPI|Admin_PageHandler' -count=1
go test ./internal/admin -count=1
if rg -n 'internal/(pool|registry|engine|session)' internal/admin/model_catalog.go; then exit 1; fi
git diff --check
git add internal/admin/admin.go internal/admin/model_catalog.go internal/admin/model_catalog_test.go
git commit -m "feat: add model catalog admin API"
```

---

### Task 5: Composition-Root Wiring and Shared Catalog

**Files:**
- Create: `cmd/otto-gateway/model_catalog.go`
- Create: `cmd/otto-gateway/model_catalog_test.go`
- Modify: `cmd/otto-gateway/main.go`
- Modify: `cmd/otto-gateway/main_test.go`

**Interfaces:**
- Consumes: Task 1 config, Task 3 pool API/errors, Task 4 admin source, and `registry.Registry.Enrich`.
- Produces: `adminModelCatalogAdapter` and one capability-registry instance shared by OpenAI and admin wiring.

- [ ] **Step 1: Write RED adapter tests**

Define the private runtime boundary:

```go
type modelCatalogRuntime interface {
	CatalogSnapshot() pool.ModelCatalogSnapshot
	RefreshModelCatalog(context.Context) (pool.CatalogRefreshResult, error)
}
```

Using a fake runtime containing Claude, GPT, and Qwen, assert the admin adapter:

- prepends exactly one `auto`;
- sets `count == len(models)`;
- enriches completion through the registry;
- preserves unknown tools/vision/reasoning;
- formats UTC timestamps; and
- applies state precedence `refreshing > pending_shrink > degraded > disabled > ready`.

Table-test all four pool sentinel errors plus an unknown raw error. The raw
error text must not appear in the fixed admin result.

- [ ] **Step 2: Verify RED**

```bash
go test ./cmd/otto-gateway -run '^TestAdminModelCatalogAdapter_' -count=1
```

- [ ] **Step 3: Implement the adapter**

Create:

```go
type adminModelCatalogAdapter struct {
	source modelCatalogRuntime
	reg *registry.Registry
	now func() time.Time
}

func (a adminModelCatalogAdapter) Snapshot() admin.ModelCatalogView
func (a adminModelCatalogAdapter) Refresh(ctx context.Context) admin.ModelCatalogActionResult
```

`Snapshot` calls the pool once and enriches that exact copied slice. Map only
the four required capabilities. `Refresh` calls the pool once and maps errors
with `errors.Is`; use `errors.As` to read `CatalogRefreshError.RetryAfter` and
round it up to whole seconds for the admin result. An unknown error becomes
`catalog_refresh_failed`; never pass `err.Error()` to the admin type.

- [ ] **Step 4: Wire one registry and the configured pool**

Move capability-registry loading out of the OpenAI-only conditional so one
validated `*registry.Registry` serves OpenAI and the dashboard. A registry load
failure remains fail-fast even when OpenAI is disabled.

Pass into `pool.Config`:

```go
ModelCatalogRefreshInterval: cfg.ModelCatalogRefreshInterval,
```

When the pool exists, pass
`adminModelCatalogAdapter{source: a.pool, reg: capReg, now: time.Now}` as
`admin.Deps.ModelCatalog`. Reuse `capReg` in `modelCapabilityCatalog`.

- [ ] **Step 5: Add wiring regressions**

In `main_test.go`, prove the interval reaches
`a.pool.CatalogSnapshot().RefreshInterval`, no-pool construction is nil-safe,
registry failure aborts startup for every enabled-surface combination, and an
expanded fake snapshot yields the same explicit ID set in admin/capability
views plus one `auto`.

In `model_catalog_test.go`, add
`TestLiveCatalogRefresh_ConvergesEveryModelSurface`. Use one mutable fake that
implements `Models`, `CatalogSnapshot`, and `RefreshModelCatalog`; mount
`admin.Handler`, `openai.New`, and `ollama.New` with that same fake. After the
fake refresh adds an ID, GET `/api/model-catalog`, `/v1/models`,
`/v1/model-capabilities`, and `/api/tags`, normalize away each protocol's
wrapper fields, and assert the exact same explicit ID set plus exactly one
synthetic `auto` on every applicable response.

- [ ] **Step 6: Verify GREEN and commit**

```bash
go test ./cmd/otto-gateway -run 'AdminModelCatalogAdapter|ModelCatalog|RegistryLoader' -count=1
go test ./cmd/otto-gateway -count=1
go test ./internal/adapter/openai ./internal/adapter/ollama -run 'Model|Tags' -count=1
git diff --check
git add cmd/otto-gateway/model_catalog.go cmd/otto-gateway/model_catalog_test.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
git commit -m "feat: wire live catalog across gateway surfaces"
```

---

### Task 6: Grouped Dashboard Table and Refresh Interaction

**Files:**
- Modify: `internal/admin/templates/dashboard.html.tmpl`
- Modify: `internal/admin/static/js/admin.js`
- Modify: `internal/admin/admin_js_test.js`
- Modify: `internal/admin/static/css/admin.css`
- Modify: `internal/admin/handlers_test.go`

**Interfaces:**
- Consumes: Task 4 GET/POST JSON.
- Produces: `#model-catalog`, grouped model rows, deterministic numeric-aware sorting, capability badges, refresh status, responsive layout, and accessible action behavior.

- [ ] **Step 1: Write RED template tests**

Require these exact hooks:

```go
for _, want := range []string{
	`id="model-catalog"`,
	`data-model-catalog-state`,
	`data-model-catalog-count`,
	`data-model-catalog-last-success`,
	`data-model-catalog-next`,
	`data-model-catalog-interval`,
	`data-model-catalog-refresh`,
	`data-model-catalog-body`,
	`data-model-catalog-message`,
	`aria-live="polite"`,
} {
	if !strings.Contains(body, want) { t.Errorf("dashboard missing %q", want) }
}
```

Assert the table is between Active Sessions and Privacy Boundary and headings
are Model, Model ID, Completion, Tools, Vision, and Reasoning.

- [ ] **Step 2: Write RED JavaScript grouping/sorting tests**

Extend the DOM shim only for primitives production uses (`disabled`,
`classList.toggle`, `colSpan`, and `scope`). Feed shuffled rows and assert group
order:

```text
Automatic, Anthropic / Claude, OpenAI / GPT, Qwen, Other models
```

Prove numeric-aware ordering with `GPT 5.6 Luna`, `GPT 5.6 Sol`, and
`GPT 5.10 Preview`, with exact ID as tie-breaker. Unknown capabilities must
render literal `Unknown`, not `Unsupported`.

- [ ] **Step 3: Write RED request-state tests**

Mock `fetch` and fake timers. Assert page load and 30-second polling GET the
catalog independently of snapshot polling; click POSTs the refresh endpoint;
button becomes disabled with `aria-busy=true` and `Refreshing…`; success
immediately GETs again; 409/429/503/502 show fixed messages; retry seconds drive
cooldown; and GET failure retains the last good table.

- [ ] **Step 4: Verify RED**

```bash
go test ./internal/admin -run 'ModelCatalog|Admin_PageHandler' -count=1
node --test --test-name-pattern='model catalog' internal/admin/admin_js_test.js
```

- [ ] **Step 5: Add semantic markup**

Add a `gw-card` between Active Sessions and Privacy Boundary. Its wrapping
header contains status, count, last success, next attempt/disabled, interval,
and `<button type="button">Refresh now</button>`. Use a real table in a labeled
horizontal-scroll container and one empty `<tbody data-model-catalog-body>`.
Include a visible warning/error banner plus a separate polite live region.

- [ ] **Step 6: Implement safe grouping and rendering**

Add the deterministic presentation helpers inside the existing IIFE:

```javascript
function modelCatalogURL() { return '/admin/api/model-catalog'; }
function modelCatalogRefreshURL() { return '/admin/api/model-catalog/refresh'; }

var modelNameCollator = new Intl.Collator(undefined, {
  numeric: true,
  sensitivity: 'base'
});

function modelGroup(model) {
  var id = String((model && model.id) || '');
  if (id === 'auto') return { key: 'auto', label: 'Automatic', order: 0 };
  if (id.indexOf('claude-') === 0) return { key: 'claude', label: 'Anthropic / Claude', order: 1 };
  if (id.indexOf('gpt-') === 0) return { key: 'gpt', label: 'OpenAI / GPT', order: 2 };
  if (id.indexOf('qwen') === 0) return { key: 'qwen', label: 'Qwen', order: 3 };
  return { key: 'other', label: 'Other models', order: 4 };
}

function compareModelRows(a, b) {
  var byName = modelNameCollator.compare(a.name || a.id, b.name || b.id);
  return byName || String(a.id).localeCompare(String(b.id));
}
```

Implement `renderModelCatalog(view)` by clearing only the catalog `<tbody>`,
partitioning a defensive copy of `view.models`, sorting groups by the returned
`order`, sorting each group's rows with `compareModelRows`, then creating one
group heading `<tr>` with
`<th scope="rowgroup" colspan="6">`; never use `innerHTML`.

Implement `fetchModelCatalog()` as a GET with `cache: 'no-store'` and
`Accept: application/json`; call `renderModelCatalog` only after an `ok`
response parses successfully so failure retains the last good table.

Implement `refreshModelCatalog()` as an empty-body POST with
`Accept: application/json`. Set the button's disabled/`aria-busy`/label state
before the request. On success, announce the returned fixed message and await
`fetchModelCatalog()`. On non-OK, render only the server's bounded action code
through a local fixed-message switch, apply `retry_after_seconds`, and never
display an arbitrary response field. Restore button state when the cooldown
expires.

Implement `initModelCatalog()` by attaching one click handler, issuing the
initial GET, and registering a distinct `setInterval(fetchModelCatalog,
pollMs)`. It must not share failure counters or promises with the health
snapshot request.

Initialize on `DOMContentLoaded` and poll on `pollMs`, but do not share failure
counters or promises with the health snapshot request.

- [ ] **Step 7: Add polished responsive CSS**

Use existing color, spacing, typography, and button tokens. Add a wrapping
metadata/action header; high-contrast button hover/focus-visible/disabled/busy
states; text-bearing status and capability badges; restrained group headings;
monospace IDs; pending/failure banners; and `overflow-x:auto` with a sensible
table `min-width`. Preserve both themes and reduced-motion behavior.

- [ ] **Step 8: Verify GREEN and commit**

```bash
go test ./internal/admin -count=1
node --test internal/admin/admin_js_test.js
git diff --check
git add internal/admin/templates/dashboard.html.tmpl internal/admin/static/js/admin.js internal/admin/admin_js_test.js internal/admin/static/css/admin.css internal/admin/handlers_test.go
git commit -m "feat: add grouped model catalog dashboard"
```

---

### Task 7: About, Docs, and Packaged Configuration

**Files:**
- Modify: `internal/admin/admin.go`
- Modify: `internal/admin/templates/about.html.tmpl`
- Modify: `internal/admin/templates/docs.html.tmpl`
- Modify: `internal/admin/handlers_test.go`
- Modify: `cmd/otto-gateway/main.go`
- Modify: `scripts/.env.example`
- Modify: `docs/operating.md`
- Modify: `docs/operator-quickstart.md`

**Interfaces:**
- Consumes: approved interval/API/reconciliation behavior and Task 6 dashboard anchor.
- Produces: read-only About status, endpoint reference, packaged env default, and operator guidance.

- [ ] **Step 1: Write RED About/Docs tests**

Require `/about` and `/docs` to include the environment name, 900-second
default, zero-disable semantics, two matching refreshes, and both admin API
paths. Assert About links to `/admin/#model-catalog` and renders the effective
interval from `Deps`, including `disabled` for zero.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/admin -run 'About|Docs|ModelCatalog' -count=1
```

- [ ] **Step 3: Add read-only About wiring**

Add `ModelCatalogRefreshInterval time.Duration` to `admin.Deps` and
`aboutData`, render `15m0s` or `disabled`, and pass
`cfg.ModelCatalogRefreshInterval` from `main.go`. The About page links to the
main table rather than duplicating it.

- [ ] **Step 4: Update Docs and env example**

Document valid range, zero semantics, restart requirement for interval
changes, nonblocking busy skip, 10-second probe bound, 30-second manual
cooldown, and two-observation removal safeguard. Add near pool settings:

```text
# Model catalog rediscovery cadence in seconds (0 disables scheduled refresh).
MODEL_CATALOG_REFRESH_INTERVAL_SEC=900
```

Do not add wrapper flags or runtime interval mutation.

- [ ] **Step 5: Update operator guides**

Add the environment table row and POSIX/PowerShell GET/POST examples to
`docs/operating.md`. In the quickstart, explain the main-dashboard table,
manual button, and that busy/failure preserves the current catalog. State only
proved properties; do not claim performance improvements.

- [ ] **Step 6: Verify GREEN and commit**

```bash
go test ./internal/admin ./cmd/otto-gateway -run 'About|Docs|ModelCatalog' -count=1
bash -n scripts/gw
git diff --check
git add internal/admin/admin.go internal/admin/templates/about.html.tmpl internal/admin/templates/docs.html.tmpl internal/admin/handlers_test.go cmd/otto-gateway/main.go scripts/.env.example docs/operating.md docs/operator-quickstart.md
git commit -m "docs: explain live model catalog refresh"
```

---

### Task 8: Acceptance and Full Verification

**Files:**
- Modify only when a failing gate proves a defect in an owned file.

**Interfaces:**
- Consumes: all prior committed behavior.
- Produces: evidence that refresh is race-safe, contract-safe, accessible, and cross-platform buildable.

- [ ] **Step 1: Format and inspect tracked scope**

```bash
make fmt
git diff --check
git status --short
```

Expected: no unrelated change; `.superpowers/` remains untracked and unstaged.

- [ ] **Step 2: Run focused feature suites uncached**

```bash
go test ./internal/config -run 'ModelCatalogRefreshInterval' -count=1
go test ./internal/pool -run 'Catalog|ModelDiscovery' -count=1
go test ./internal/admin -run 'ModelCatalog|About|Docs|Admin_PageHandler' -count=1
go test ./cmd/otto-gateway -run 'ModelCatalog|RegistryLoader' -count=1
node --test --test-name-pattern='model catalog' internal/admin/admin_js_test.js
```

- [ ] **Step 3: Run race-critical suites**

```bash
go test -race ./internal/pool -run 'CatalogRefresh|ModelDiscovery|Release|Recycle|Close' -count=1
go test -race ./internal/admin ./cmd/otto-gateway -run 'ModelCatalog|Admin_PageHandler|RegistryLoader' -count=1
```

- [ ] **Step 4: Run complete test and JavaScript gates**

```bash
go test ./... -count=1
node --test internal/admin/admin_js_test.js
```

- [ ] **Step 5: Run static, architecture, and build gates**

```bash
go vet ./...
golangci-lint run ./...
make arch-lint
make build
make cross-windows-amd64
```

- [ ] **Step 6: Audit acceptance properties**

Confirm partial non-empty catalogs expand without restart; invalid results fail
closed; removal needs two matches; probes never wait for slots; size-one,
cancellation, shutdown, races, and exactly-once release pass; all model
surfaces share one explicit set; UI grouping/sorting/accessibility pass; no
forbidden details appear; and no dependency/release/unrelated change exists.

```bash
git log --oneline --decorate -8
git diff --stat HEAD~7..HEAD
git diff --check HEAD~7..HEAD
git status --short
```

- [ ] **Step 7: Commit only a demonstrated correction**

If verification makes no changes, do not create an empty commit. If an owned
defect is corrected, rerun the failing command plus Steps 2–5, stage only exact
fix/test files, and commit:

```bash
git commit -m "fix: close model catalog refresh verification gaps"
```

Record final commands and outcomes in the completion handoff.
