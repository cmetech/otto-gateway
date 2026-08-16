# Admin Copy Messages and Gateway PID Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one-click copying of the full ACP capture response and show the gateway process PID in a dedicated dashboard overview header.

**Architecture:** Extend the existing admin snapshot with an additive `gateway_pid` field sourced from `os.Getpid()`, then hydrate a new overview-header hook through the existing snapshot renderer. Add a client-only ACP copy control that fetches the existing pretty capture endpoint as text and writes it unchanged through the Clipboard API, with bounded inline feedback.

**Tech Stack:** Go `net/http` and `os`, Go `html/template`, vanilla JavaScript and Clipboard API, embedded CSS, Go tests, Node.js built-in test runner.

## Global Constraints

- Copy the full response from `/admin/api/acp-capture?pretty=1` unchanged; do not parse, re-serialize, redact, or select only `frames`.
- Button labels are exactly `Copy Messages`, `Copying…`, `Copied`, and `Copy failed`.
- Success and failure feedback reset to `Copy Messages` after 2,000 milliseconds.
- The copy button is disabled only while the fetch/clipboard operation is pending.
- Do not add a legacy clipboard fallback or a new server endpoint.
- Publish the process ID as the additive integer snapshot field `gateway_pid`, sourced from `os.Getpid()`.
- Put the gateway PID in its own full-width overview header above the unchanged seven-widget summary grid.
- Do not modify or commit the pre-existing untracked `.superpowers/` directory.

---

### Task 1: Gateway PID snapshot and overview header

**Files:**
- Modify: `internal/admin/snapshot.go:8-45,190-210`
- Modify: `internal/admin/templates/dashboard.html.tmpl:4-12`
- Modify: `internal/admin/static/js/admin.js:588-600`
- Modify: `internal/admin/static/css/admin.css:153-175`
- Test: `internal/admin/snapshot_test.go:90-165`
- Test: `internal/admin/handlers_test.go:68-153`
- Test: `internal/admin/admin_js_test.js:11-115,195-245,400-445`

**Interfaces:**
- Produces: `Snapshot.GatewayPID int` serialized as `gateway_pid`.
- Produces: dashboard hook `[data-gateway-pid]` hydrated by `renderSummary(snap)`.
- Consumes: the existing `/admin/api/snapshot` polling and `setText(attribute, value)` helper.

- [ ] **Step 1: Write failing snapshot and page-scaffold tests**

Add `os` to `internal/admin/snapshot_test.go` imports and assert the live process ID after decoding the snapshot:

```go
if snap.GatewayPID != os.Getpid() {
	t.Errorf("gateway_pid: want %d, got %d", os.Getpid(), snap.GatewayPID)
}
```

Extend `TestAdmin_PageHandler`'s required dashboard hooks in
`internal/admin/handlers_test.go`:

```go
for _, attr := range []string{
	"data-gateway-pid",
	"data-pill",
	"data-uptime",
	"data-last-updated",
} {
```

Also assert the dedicated header copy and class:

```go
for _, want := range []string{
	`class="gw-summary-header"`,
	"Gateway PID",
} {
	if !strings.Contains(body, want) {
		t.Errorf("body missing gateway PID header scaffold %q", want)
	}
}
```

- [ ] **Step 2: Run the Go tests and verify the new assertions fail**

Run:

```bash
go test ./internal/admin -run 'TestAdmin_(SnapshotHandler|PageHandler)$' -count=1
```

Expected: FAIL because `Snapshot.GatewayPID` and the overview PID markup do not
exist.

- [ ] **Step 3: Write a failing JavaScript hydration test**

Add `gateway_pid: 4242` to the `snapshot(labels)` fixture in
`internal/admin/admin_js_test.js`, add a `[data-gateway-pid]` `Element` to the
harness selectors, and add:

```javascript
test('gateway PID hydrates the dedicated overview header', async () => {
  const harness = createHarness([snapshot({ main: 'Gateway', kiro: 'Kiro' })]);
  harness.start();
  await settleSnapshot();

  assert.equal(harness.selectors['[data-gateway-pid]'].textContent, '4242');
});
```

- [ ] **Step 4: Run the JavaScript test and verify it fails for the missing render behavior**

Run:

```bash
node --test --test-name-pattern='gateway PID hydrates' internal/admin/admin_js_test.js
```

Expected: FAIL because `renderSummary` leaves the new hook empty.

- [ ] **Step 5: Add the snapshot field and populate it**

In `internal/admin/snapshot.go`, import `os`, add the field near uptime, and set
it in the snapshot literal:

```go
GatewayPID int `json:"gateway_pid"`
```

```go
GatewayPID:     os.Getpid(),
UptimeSeconds: time.Since(h.deps.Start).Seconds(),
```

Update the wire-shape comment to show `"gateway_pid": 12345` beside the other
top-level process metadata.

- [ ] **Step 6: Add and hydrate the overview header**

In `internal/admin/templates/dashboard.html.tmpl`, immediately after the
screen-reader-only heading add:

```html
<div class="gw-summary-header" aria-label="Gateway process">
  <span class="gw-summary-header-label">Gateway PID</span>
  <code class="gw-summary-header-value" data-gateway-pid>—</code>
</div>
```

At the start of `renderSummary(snap)` in `internal/admin/static/js/admin.js`,
add:

```javascript
setText('data-gateway-pid', snap.gateway_pid > 0 ? String(snap.gateway_pid) : '—');
```

In `internal/admin/static/css/admin.css`, before `.gw-summary-items`, add:

```css
.gw-summary-header {
  flex: 0 0 100%;
  display: flex;
  align-items: baseline;
  gap: var(--gw-space-sm);
  padding-bottom: var(--gw-space-sm);
  border-bottom: 1px solid var(--gw-border);
}

.gw-summary-header-label {
  color: var(--gw-fg-muted);
  font-size: var(--gw-text-xs);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}

.gw-summary-header-value {
  color: var(--gw-fg);
  font-family: var(--gw-font-mono);
  font-size: var(--gw-text-base);
}
```

- [ ] **Step 7: Run focused and full admin tests**

Run:

```bash
go test ./internal/admin -run 'TestAdmin_(SnapshotHandler|PageHandler)$' -count=1
node --test --test-name-pattern='gateway PID hydrates' internal/admin/admin_js_test.js
go test ./internal/admin/...
```

Expected: all PASS.

- [ ] **Step 8: Commit the gateway PID task**

```bash
git add internal/admin/snapshot.go internal/admin/snapshot_test.go \
  internal/admin/templates/dashboard.html.tmpl internal/admin/static/js/admin.js \
  internal/admin/static/css/admin.css internal/admin/handlers_test.go \
  internal/admin/admin_js_test.js
git commit -m "feat: show gateway PID in dashboard overview"
```

---

### Task 2: ACP Copy Messages control and feedback

**Files:**
- Modify: `internal/admin/templates/dashboard.html.tmpl:233-268`
- Modify: `internal/admin/static/js/admin.js:1749-1840`
- Test: `internal/admin/handlers_test.go`
- Test: `internal/admin/admin_js_test.js:11-385`

**Interfaces:**
- Produces: button hook `[data-acp-capture-copy]` and label hook `[data-acp-capture-copy-label]`.
- Produces: `copyAcpCapture(button)` behavior using the existing `acpCaptureUrl()`.
- Consumes: `GET /admin/api/acp-capture?pretty=1` as raw text and `navigator.clipboard.writeText(text) -> Promise<void>`.

- [ ] **Step 1: Write the failing template scaffold test**

Add a focused handler test that renders `/` and checks all copy-control
contracts:

```go
func TestAdmin_PageHandler_ACPCopyMessagesScaffold(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := Handler(Deps{Logger: testutil.Logger(t)})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d", rec.Code)
	}
	for _, want := range []string{
		"data-acp-capture-copy",
		"data-acp-capture-copy-label",
		"Copy Messages",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("page HTML missing ACP copy control %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the scaffold test and verify it fails**

Run:

```bash
go test ./internal/admin -run TestAdmin_PageHandler_ACPCopyMessagesScaffold -count=1
```

Expected: FAIL because the copy button hooks and label do not exist.

- [ ] **Step 3: Extend the JavaScript harness and write failing copy tests**

In `createHarness`, when `options.acpCapture` is true, build an ACP section and
map its descendant `querySelector` calls to pill, count, controls, note, toggle,
toggle label, clear, copy, and copy-label elements. Because the lightweight
`Element.querySelector` only understands class selectors, explicitly override
`section.querySelector`, `toggle.querySelector`, and `copy.querySelector` with
their data-hook maps. Add every element referenced directly by a test to the
root `selectors` map, and add the section under `[data-acp-capture]` so
`initAcpCapture()` wires it normally.

Add an `acpResponses` queue. Route both the initial
`/admin/api/acp-capture` fetch and the copy
`/admin/api/acp-capture?pretty=1` fetch through it. Each fake response must
support both existing JSON consumption and new text consumption:

```javascript
return Promise.resolve({
  ok: status >= 200 && status < 300,
  status,
  json: () => Promise.resolve(entry.body),
  text: () => Promise.resolve(entry.text),
});
```

Expose `clipboardWrites` from the harness and add a configurable clipboard
implementation to the VM context:

```javascript
navigator: {
  clipboard: options.clipboardUnavailable ? undefined : {
    writeText(text) {
      if (options.clipboardError) return Promise.reject(new Error(options.clipboardError));
      clipboardWrites.push(text);
      return Promise.resolve();
    },
  },
},
```

Use this ready-state response as the first ACP queue entry in each test:

```javascript
const captureState = {
  enabled: true,
  allowRuntimeToggle: true,
  count: 1,
  size: 100,
  frames: [],
};
```

Add the success test:

```javascript
test('ACP Copy Messages copies the full pretty response unchanged and resets feedback', async () => {
  const pretty = '{\n  "enabled": true,\n  "frames": []\n}\n';
  const harness = createHarness(
    [snapshot({ main: 'Gateway', kiro: 'Kiro' })],
    { acpCapture: true, acpResponses: [{ body: captureState }, { text: pretty }] },
  );
  harness.start();
  await settleAsyncWork();

  const button = harness.selectors['[data-acp-capture-copy]'];
  const label = harness.selectors['[data-acp-capture-copy-label]'];
  button.dispatchEvent({ type: 'click' });
  assert.equal(button.disabled, true);
  assert.equal(label.textContent, 'Copying…');
  await settleAsyncWork();

  assert.deepEqual(harness.clipboardWrites, [pretty]);
  assert.equal(button.disabled, false);
  assert.equal(label.textContent, 'Copied');
  const copyCall = harness.fetchCalls.find((call) => call.url.endsWith('?pretty=1'));
  assert.equal(copyCall.options.headers.Accept, 'application/json');

  harness.runTimeout(2000);
  assert.equal(label.textContent, 'Copy Messages');
});
```

Add table-driven failure coverage for a non-OK response, clipboard rejection,
and unavailable Clipboard API:

```javascript
test('ACP Copy Messages reports bounded fetch and clipboard failures', async () => {
  const cases = [
    {
      name: 'HTTP failure',
      options: { acpResponses: [{ body: captureState }, { httpStatus: 503, text: 'unavailable' }] },
    },
    {
      name: 'clipboard rejection',
      options: {
        acpResponses: [{ body: captureState }, { text: '{\n  "frames": []\n}\n' }],
        clipboardError: 'permission denied',
      },
    },
    {
      name: 'Clipboard API unavailable',
      options: { acpResponses: [{ body: captureState }], clipboardUnavailable: true },
    },
  ];

  for (const tc of cases) {
    const harness = createHarness(
      [snapshot({ main: 'Gateway', kiro: 'Kiro' })],
      { acpCapture: true, ...tc.options },
    );
    harness.start();
    await settleAsyncWork();

    const button = harness.selectors['[data-acp-capture-copy]'];
    const label = harness.selectors['[data-acp-capture-copy-label]'];
    button.dispatchEvent({ type: 'click' });
    await settleAsyncWork();

    assert.equal(label.textContent, 'Copy failed', tc.name);
    assert.equal(button.disabled, false, tc.name);
    assert.deepEqual(harness.clipboardWrites, [], tc.name);
    harness.runTimeout(2000);
    assert.equal(label.textContent, 'Copy Messages', tc.name);
  }
});
```

- [ ] **Step 4: Run the JavaScript tests and verify they fail for missing behavior**

Run:

```bash
node --test --test-name-pattern='ACP Copy Messages' internal/admin/admin_js_test.js
```

Expected: FAIL because the copy button is not present and no copy listener is
wired.

- [ ] **Step 5: Add the Copy Messages button**

In `internal/admin/templates/dashboard.html.tmpl`, place this button immediately
before **Show Messages**:

```html
<button type="button" class="gw-btn gw-btn--icon" data-acp-capture-copy>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" d="M15.75 17.25v3.375c0 .621-.504 1.125-1.125 1.125h-9.75a1.125 1.125 0 0 1-1.125-1.125V10.875c0-.621.504-1.125 1.125-1.125H8.25m7.5 7.5h3.375c.621 0 1.125-.504 1.125-1.125V6.108c0-.298-.119-.585-.33-.796l-4.232-4.232a1.125 1.125 0 0 0-.796-.33H9.375c-.621 0-1.125.504-1.125 1.125v6.75"/><path stroke-linecap="round" stroke-linejoin="round" d="M15 0.75v4.5a1.125 1.125 0 0 0 1.125 1.125h4.125"/></svg>
  <span data-acp-capture-copy-label aria-live="polite">Copy Messages</span>
</button>
```

The existing `.gw-btn--icon` rules already size and align the SVG; no new copy
button CSS is required.

- [ ] **Step 6: Implement fetch-to-clipboard behavior and bounded feedback**

In the ACP capture section of `internal/admin/static/js/admin.js`, add one timer
and focused helpers:

```javascript
var acpCopyResetTimer = null;

function setAcpCopyLabel(button, text) {
  var label = button.querySelector('[data-acp-capture-copy-label]');
  if (label) label.textContent = text;
}

function finishAcpCopy(button, text) {
  button.disabled = false;
  setAcpCopyLabel(button, text);
  if (acpCopyResetTimer) clearTimeout(acpCopyResetTimer);
  acpCopyResetTimer = setTimeout(function () {
    setAcpCopyLabel(button, 'Copy Messages');
    acpCopyResetTimer = null;
  }, 2000);
}

function copyAcpCapture(button) {
  if (acpCopyResetTimer) {
    clearTimeout(acpCopyResetTimer);
    acpCopyResetTimer = null;
  }
  button.disabled = true;
  setAcpCopyLabel(button, 'Copying…');

  if (!navigator.clipboard || typeof navigator.clipboard.writeText !== 'function') {
    finishAcpCopy(button, 'Copy failed');
    return;
  }

  fetch(acpCaptureUrl() + '?pretty=1', {
    headers: { 'Accept': 'application/json' }
  })
    .then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.text();
    })
    .then(function (body) { return navigator.clipboard.writeText(body); })
    .then(
      function () { finishAcpCopy(button, 'Copied'); },
      function () { finishAcpCopy(button, 'Copy failed'); }
    );
}
```

In `initAcpCapture()`, select and wire the new button:

```javascript
var copy = section.querySelector('[data-acp-capture-copy]');
if (copy) copy.addEventListener('click', function () { copyAcpCapture(copy); });
```

- [ ] **Step 7: Run focused and full admin tests**

Run:

```bash
go test ./internal/admin -run TestAdmin_PageHandler_ACPCopyMessagesScaffold -count=1
node --test --test-name-pattern='ACP Copy Messages' internal/admin/admin_js_test.js
node --test internal/admin/admin_js_test.js
go test ./internal/admin/...
```

Expected: all PASS with no warning or unhandled-rejection output.

- [ ] **Step 8: Commit the ACP copy task**

```bash
git add internal/admin/templates/dashboard.html.tmpl internal/admin/static/js/admin.js \
  internal/admin/handlers_test.go internal/admin/admin_js_test.js
git commit -m "feat: copy ACP diagnostic messages"
```

---

### Task 3: Final verification

**Files:**
- Verify only; do not modify files unless a gate exposes a defect in Tasks 1-2.

**Interfaces:**
- Consumes: the additive `gateway_pid` snapshot contract and ACP copy UI from Tasks 1-2.
- Produces: current-session evidence that the complete repository remains buildable and formatted.

- [ ] **Step 1: Run formatting checks**

```bash
gofmt -w internal/admin/snapshot.go internal/admin/snapshot_test.go internal/admin/handlers_test.go
git diff --check
```

Expected: no output from `git diff --check`.

- [ ] **Step 2: Run the complete JavaScript and Go verification gates**

```bash
node --test internal/admin/admin_js_test.js
go test ./internal/admin/...
go build ./...
```

Expected: all tests PASS and the build exits 0.

- [ ] **Step 3: Inspect the final scoped diff**

```bash
git status --short
git diff HEAD~2 -- internal/admin
```

Expected: only the approved snapshot, overview-header, ACP copy control, and
their tests are changed; `.superpowers/` remains untracked and uncommitted.

- [ ] **Step 4: Record final verification without an empty commit**

If Step 1 formatting changed tracked files, amend the task commit that owns
those files after re-running Step 2. If no files changed, do not create an empty
verification commit; report the exact successful commands and commit hashes in
the handoff.
