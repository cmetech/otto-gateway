# Three-Column Worker Slot Grid Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Default the Gateway to two warm workers, reject pool sizes above six, and render one or two complete three-card dashboard rows with wider desktop cards.

**Architecture:** Configuration validation remains the enforcement boundary: both environment loading and explicit CLI overlays validate an inclusive 0–6 range, while the internal pool package retains its existing zero-value default. The admin snapshot remains unchanged; the browser pads a copied real-slot list to three or six and CSS renders three equal desktop columns with the existing responsive fallbacks.

**Tech Stack:** Go 1.23 configuration and admin handlers, embedded vanilla JavaScript/CSS, Node's built-in test runner, Go test/race tooling.

## Global Constraints

- `POOL_SIZE` defaults to 2 at the Gateway application layer and accepts only 0 through 6.
- `POOL_SIZE=0` preserves the existing no-warm-pool and admin empty-state behavior.
- `--pool-size` must enforce the same inclusive range and must never clamp an invalid value.
- `internal/pool.Config{}` keeps its existing defensive size default; do not change pool package zero-value behavior.
- The server snapshot carries only real slots; vacant cards remain browser-only and never create metrics or performance samples.
- Desktop uses three equal columns; tablet remains two columns and mobile remains one column.
- Do not truncate an unexpected snapshot containing more than six real slots.
- Follow strict red-green-refactor for Go and JavaScript behavior. Do not add a source-text assertion for CSS; verify layout through computed browser behavior instead.

---

### Task 1: Enforce the two-worker default and six-worker maximum

**Files:**
- Modify: `internal/config/config.go:143-146, 578-598, 1210-1325`
- Modify: `internal/config/config_test.go:340-380`
- Modify: `internal/config/loadargs_test.go:39-57, 245-270`

**Interfaces:**
- Consumes: `getEnvInt(name string, fallback int) (int, error)` and the existing `LoadArgs` explicit-flag overlay.
- Produces: `validatePoolSize(source string, value int) error`, used by both `Load` and `LoadArgs`; resolved `Config.PoolSize` is always in `[0, 6]`.

- [ ] **Step 1: Write failing environment-default and boundary tests**

Replace the default assertion and extend the `POOL_SIZE` section with literal boundary cases:

```go
func TestLoad_PoolSize_Default(t *testing.T) {
	t.Setenv("POOL_SIZE", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.PoolSize != 2 {
		t.Errorf("PoolSize: got %d, want 2", cfg.PoolSize)
	}
}

func TestLoad_PoolSize_Boundaries(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr string
	}{
		{name: "disabled", value: "0", want: 0},
		{name: "maximum", value: "6", want: 6},
		{name: "above maximum", value: "7", wantErr: "POOL_SIZE: sanity cap exceeded (max 6), got 7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POOL_SIZE", tc.value)
			cfg, err := config.Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v; want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() returned unexpected error: %v", err)
			}
			if cfg.PoolSize != tc.want {
				t.Fatalf("PoolSize = %d; want %d", cfg.PoolSize, tc.want)
			}
		})
	}
}
```

Keep the existing malformed-value test and existing negative-value regression coverage.

- [ ] **Step 2: Write failing CLI boundary tests**

Add a focused table to `loadargs_test.go`:

```go
func TestLoadArgs_PoolSizeBoundaries(t *testing.T) {
	t.Setenv("POOL_SIZE", "2")
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr string
	}{
		{name: "maximum", value: "6", want: 6},
		{name: "above maximum", value: "7", wantErr: "--pool-size: sanity cap exceeded (max 6), got 7"},
		{name: "negative", value: "-1", wantErr: "--pool-size: must be >= 0, got -1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.LoadArgs([]string{"--pool-size", tc.value})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("LoadArgs() error = %v; want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadArgs() returned unexpected error: %v", err)
			}
			if cfg.PoolSize != tc.want {
				t.Fatalf("PoolSize = %d; want %d", cfg.PoolSize, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/config -run 'PoolSize' -count=1
```

Expected failures:

- the default test reports 4 instead of 2;
- environment value 7 is accepted instead of rejected;
- CLI values 7 and -1 are accepted instead of rejected.

- [ ] **Step 4: Add one shared validator and apply it at both resolution boundaries**

Add near the config parsing helpers:

```go
const maxPoolSize = 6

func validatePoolSize(source string, value int) error {
	if value < 0 {
		return fmt.Errorf("%s: must be >= 0, got %d", source, value)
	}
	if value > maxPoolSize {
		return fmt.Errorf("%s: sanity cap exceeded (max %d), got %d", source, maxPoolSize, value)
	}
	return nil
}
```

Change the application default and environment validation:

```go
poolSize, err := getEnvInt("POOL_SIZE", 2)
if err != nil {
	errs = append(errs, err)
}
if verr := validatePoolSize("POOL_SIZE", poolSize); verr != nil {
	errs = append(errs, verr)
}
```

Update the `Config.PoolSize` and parsing comments to say the application default
is 2 and the supported range is 0–6. Preserve the comment explaining why
`internal/pool.Config{}` keeps its own package-level default.

In the `LoadArgs` `fs.Visit` switch, validate only an explicitly supplied flag:

```go
case "pool-size":
	cfg.PoolSize = *poolSize
	if verr := validatePoolSize("--pool-size", cfg.PoolSize); verr != nil {
		errs = append(errs, verr)
	}
```

- [ ] **Step 5: Run focused and package tests and verify GREEN**

Run:

```bash
gofumpt -w internal/config/config.go internal/config/config_test.go internal/config/loadargs_test.go
go test ./internal/config -run 'PoolSize' -count=1
go test -race ./internal/config -count=1
```

Expected: all commands pass.

- [ ] **Step 6: Commit the configuration contract**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/loadargs_test.go
git commit -m "feat(config): default pool to two workers and cap at six"
```

---

### Task 2: Render adaptive complete rows of three cards

**Files:**
- Modify: `internal/admin/admin_js_test.js:85-112, 210-370`
- Modify: `internal/admin/static/js/admin.js:460-505`

**Interfaces:**
- Consumes: `pool.slots`, the real server-projected slot array, and `pool.size` for vacant-card copy.
- Produces: browser-only `displaySlots`; lengths 1–3 pad to 3 and lengths 4–6 pad to 6, while 0 keeps the empty state and values above 6 remain untruncated.

- [ ] **Step 1: Add a complete-snapshot fixture helper**

Add this test-only helper after `slotSnapshot` so every generated real slot
retains the full production snapshot shape:

```js
function slotSnapshotWithCount(count) {
  const snap = slotSnapshot();
  const prototype = snap.pool.slots[0];
  snap.pool.size = count;
  snap.pool.slots = Array.from({ length: count }, (_unused, index) => ({
    ...prototype,
    label: `slot-${index}`,
    pid: 4101 + index,
  }));
  return snap;
}
```

- [ ] **Step 2: Write a failing rendered-card table test**

The mutation this catches is returning to fixed-four padding or omitting the
second padded row.

```js
test('slot grid pads real workers to complete three-card rows through the six-worker cap', async () => {
  const cases = [
    { real: 0, cards: 0, vacant: 0 },
    { real: 1, cards: 3, vacant: 2 },
    { real: 2, cards: 3, vacant: 1 },
    { real: 3, cards: 3, vacant: 0 },
    { real: 4, cards: 6, vacant: 2 },
    { real: 5, cards: 6, vacant: 1 },
    { real: 6, cards: 6, vacant: 0 },
  ];

  for (const tc of cases) {
    const harness = createHarness([slotSnapshotWithCount(tc.real)]);
    harness.start();
    await settleSnapshot();
    const children = harness.selectors['[data-slot-grid]'].children;
    assert.equal(children.length, tc.cards, `POOL_SIZE=${tc.real} card count`);
    assert.equal(
      children.filter((child) => child.className.includes('is-vacant')).length,
      tc.vacant,
      `POOL_SIZE=${tc.real} vacant count`,
    );
  }
});
```

- [ ] **Step 3: Write a failing in-place vacancy transition test**

```js
test('slot grid clears vacant styling at the three- and six-worker boundaries', async () => {
  const harness = createHarness([
    slotSnapshotWithCount(2),
    slotSnapshotWithCount(3),
    slotSnapshotWithCount(5),
    slotSnapshotWithCount(6),
  ]);

  harness.start();
  await settleSnapshot();
  assert.match(harness.selectors['[data-slot-grid]'].children[2].className, /is-vacant/);

  harness.poll();
  await settleSnapshot();
  assert.doesNotMatch(harness.selectors['[data-slot-grid]'].children[2].className, /is-vacant/);

  harness.poll();
  await settleSnapshot();
  assert.match(harness.selectors['[data-slot-grid]'].children[5].className, /is-vacant/);

  harness.poll();
  await settleSnapshot();
  assert.doesNotMatch(harness.selectors['[data-slot-grid]'].children[5].className, /is-vacant/);
});
```

- [ ] **Step 4: Run the Node test and verify RED**

Run:

```bash
node --test --test-name-pattern='slot grid' internal/admin/admin_js_test.js
```

Expected: the fixed-four implementation reports 4 cards for real counts 1 and
2, and 4 or 5 cards instead of 6 for counts 4 and 5.

- [ ] **Step 5: Implement adaptive padding without truncation**

Replace the fixed-four comment and loop in `renderSlots` with:

```js
  // Real slots stay first. The browser adds only enough vacant positions to
  // complete one or two three-card desktop rows; the server snapshot remains
  // untouched, so placeholders never enter perf ingestion or metrics.
  var displaySlots = slots.slice();
  var displayCount = displaySlots.length <= 3 ? 3 : 6;
  for (var i = displaySlots.length; i < displayCount; i++) {
    displaySlots.push({ vacant: true, label: 'slot-' + i, pool_size: pool.size || 0 });
  }
```

Because `displaySlots` begins as the full copied array, an unexpected length
above six remains above six; `displayCount` does not slice or hide it.

- [ ] **Step 6: Run the focused and complete admin JavaScript suites**

Run:

```bash
node --test --test-name-pattern='slot grid' internal/admin/admin_js_test.js
node --test internal/admin/admin_js_test.js
```

Expected: all tests pass, including existing idle-memory, checked-out,
unsupported-platform, and future-timestamp cases.

- [ ] **Step 7: Commit adaptive card rendering**

```bash
git add internal/admin/admin_js_test.js internal/admin/static/js/admin.js
git commit -m "feat(admin): pad worker cards to rows of three"
```

---

### Task 3: Widen desktop cards, align operator documentation, and verify

**Files:**
- Modify: `internal/admin/static/css/admin.css:385-435`
- Modify: `internal/admin/admin.go:840-860`
- Modify: `README.md:35-45`
- Modify: `docs/operating.md:410-420`
- Modify: `docs/architecture/otto_architecture_infographic_prompt.md:30-38`

**Interfaces:**
- Consumes: the Task 2 three-or-six-card display list.
- Produces: three equal desktop columns, unchanged two/one responsive columns, and operator-facing documentation of default 2 and range 0–6.

- [ ] **Step 1: Establish the pre-style behavioral baseline**

Run:

```bash
node --test internal/admin/admin_js_test.js
go test ./internal/config ./internal/admin -count=1
```

Expected: PASS. This establishes that subsequent styling/documentation edits
do not regress functional behavior. Do not add a test that greps CSS source;
such a test would assert implementation text instead of rendered behavior.

- [ ] **Step 2: Change the desktop grid to three equal columns**

Update the desktop rule and its rationale:

```css
/* minmax(0, 1fr) (not bare 1fr): a bare 1fr track cannot shrink below its
 * content's min-width, so a card with wide content would steal width from
 * its siblings. Three equal desktop columns give CPU/memory threshold values
 * enough room while preserving balanced rows. */
.gw-slot-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: var(--gw-space-base);
}
```

Update the vacant-card comment from `POOL_SIZE < 4` to describe padding an
incomplete three-card row. Do not change the existing tablet rule
`repeat(2, minmax(0, 1fr))`, mobile rule `1fr`, `.gw-perf-value`
`white-space: nowrap`, or the 1280px page maximum.

- [ ] **Step 3: Update live admin configuration documentation**

Change the `POOL_SIZE` row in `internal/admin/admin.go` to:

```go
{
	Name:         "POOL_SIZE",
	Default:      "2",
	Description:  "Number of warm kiro-cli subprocesses kept in the pool. Accepts 0–6.",
	CurrentValue: strconv.Itoa(h.deps.PoolSize),
},
```

- [ ] **Step 4: Update maintained operator and architecture documentation**

Make these exact semantic updates:

- `README.md`: session pool text says `default 2`.
- `docs/operating.md`: `POOL_SIZE` says binary and laptop-template default 2,
  accepts 0–6, and shared hosts may raise it only through 6.
- `docs/architecture/otto_architecture_infographic_prompt.md`: session pool says
  `default 2`; replace the 2×2 all-worker illustration with one three-position
  row containing two live workers and one dashed `VACANT` position.
- Leave `docs/reference/acp_server_node_reference.md`, port briefs, and previous
  dated design/plan documents unchanged because they record historical Node or
  implementation context.

- [ ] **Step 5: Format and run focused verification**

Run:

```bash
gofumpt -w internal/admin/admin.go
git diff --check
go test -race ./internal/config ./internal/admin -count=1
node --test internal/admin/admin_js_test.js
```

Expected: all commands pass.

- [ ] **Step 6: Verify rendered layout behavior at responsive widths**

Run the Gateway in degraded mode using an intentionally missing Kiro binary on
an unused loopback port, then open `/admin` in a browser:

```bash
HTTP_ADDR=127.0.0.1:18081 KIRO_CMD=/definitely/missing/kiro POOL_SIZE=2 \
  go run ./cmd/otto-gateway
```

Verify with browser computed layout and bounding boxes:

- at 1280px viewport, `.gw-slot-grid` has three equal columns and exactly three
  cards for two real workers;
- the third card is `slot-2`, dashed, and `VACANT`;
- the current `Mem N MiB / 500 MiB` value remains one line and its value
  bounding box stays inside the containing card;
- below 1024px the grid has two columns;
- below 768px the grid has one column.

If the local platform reports worker memory as unavailable, use browser
devtools to change one existing `.gw-perf-value` element's `textContent` to
`800 MiB / 500 MiB`, then compare that element's `getBoundingClientRect().right`
with its closest `.gw-slot-card` right edge. This changes only the live DOM;
do not alter production code or commit a fixture-only route.

- [ ] **Step 7: Run the full repository gate**

Use a fresh lint cache to avoid stale paths from removed worktrees:

```bash
export PATH="/Users/coreyellis/go/bin:$PATH"
slot_grid_lint_cache=$(mktemp -d /tmp/otto-slot-grid-lint.XXXXXX)
GOLANGCI_LINT_CACHE="$slot_grid_lint_cache" make ci
git status --short
```

Expected: `make ci` exits 0 and the worktree contains only the intended
documentation/styling changes for this task before commit.

- [ ] **Step 8: Commit styling and documentation**

```bash
git add internal/admin/static/css/admin.css internal/admin/admin.go README.md \
  docs/operating.md docs/architecture/otto_architecture_infographic_prompt.md
git commit -m "feat(admin): widen worker cards and document pool limits"
```

- [ ] **Step 9: Final scope and history check**

Run:

```bash
git diff --check HEAD~3..HEAD
git status --short
git log -3 --oneline
```

Expected: three atomic implementation commits, no uncommitted changes, and no
changes to pool lifecycle, metrics, snapshots, wrappers, or historical docs.
