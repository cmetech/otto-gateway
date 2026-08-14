'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

class Element {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.listeners = {};
    this.textContent = '';
    this.value = '';
    this.hidden = false;
	this.disabled = false;
	this.colSpan = 1;
	this.scope = '';
	this.className = '';
    this.dataset = {};
    this.style = { display: '' };
    this.scrollHeight = 0;
    this.scrollTop = 0;
    this.classList = {
      add: (...classes) => {
        const names = new Set(this.className.split(/\s+/).filter(Boolean));
        for (const name of classes) names.add(name);
        this.className = [...names].join(' ');
      },
      remove: (...classes) => {
        const removed = new Set(classes);
        this.className = this.className
          .split(/\s+/)
          .filter((name) => name && !removed.has(name))
          .join(' ');
      },
	  toggle: (name, force) => {
		const names = new Set(this.className.split(/\s+/).filter(Boolean));
		const enabled = force === undefined ? !names.has(name) : !!force;
		if (enabled) names.add(name);
		else names.delete(name);
		this.className = [...names].join(' ');
		return enabled;
	  },
    };
  }

  get firstChild() {
    return this.children[0] || null;
  }

  appendChild(child) {
    this.children.push(child);
    return child;
  }

  append(...children) {
    this.children.push(...children);
  }

  replaceChildren(...children) {
    this.children = children;
  }

  setAttribute(name, value) {
    this[name] = value;
  }

  removeChild(child) {
    const index = this.children.indexOf(child);
    if (index >= 0) this.children.splice(index, 1);
    return child;
  }

  addEventListener(type, listener) {
    this.listeners[type] = listener;
  }

  dispatchEvent(event) {
    this.listeners[event.type](event);
  }

  querySelectorAll(selector) {
    const wantedClasses = selector
      .split(',')
      .map((part) => part.trim())
      .filter((part) => part.startsWith('.'))
      .map((part) => part.slice(1));
    const matches = [];
    const visit = (element) => {
      const classes = new Set(element.className.split(/\s+/).filter(Boolean));
      if (wantedClasses.some((name) => classes.has(name))) matches.push(element);
      for (const child of element.children) visit(child);
    };
    for (const child of this.children) visit(child);
    return matches;
  }

  querySelector(selector) {
    return this.querySelectorAll(selector)[0] || null;
  }
}

function snapshot(labels) {
  return {
    status: 'ok',
    uptime_seconds: 1,
    generated_at: '2026-07-27T12:00:00Z',
    process_stat_ok: false,
    pool: { size: 0, slots: [], max_turns: 0 },
    sessions: [],
    log_sources: ['main', 'kiro'],
    log_source_labels: labels,
	privacy: {
	  default_profile: 'strict',
	  strict_available: true,
	  triage_enabled: false,
	  requests_protected: 144,
	  requests_blocked: 7,
	  scopes_active: 3,
	  requests_in_flight: 2,
	  entries: 41,
	  max_scopes: 128,
	  max_entries_per_scope: 4096,
	  max_total_entries: 32768,
	  scope_ttl_seconds: 3600,
	  oldest_scope_age_seconds: 91,
	  last_error_code: 'privacy_output_blocked',
	},
  };
}

function slotSnapshot(overrides = {}) {
  return {
    ...snapshot({ main: 'Gateway', kiro: 'Kiro' }),
    generated_at: '2026-08-04T12:00:00Z',
    pool: {
      size: 1,
      max_turns: 20,
      idle_recycle_ms: 900000,
      idle_recycle_memory_bytes: 500 * 1024 * 1024,
      idle_recycle_supported: true,
      slots: [{
        label: 'slot-0',
        pid: 4101,
        alive: true,
        busy: false,
        checked_out: false,
        stat_ok: true,
        rss_bytes: 800 * 1024 * 1024,
        turns: 2,
        user_requests_since_spawn: 1,
        last_user_release_at: '2026-08-04T11:44:00Z',
        ...overrides,
      }],
    },
  };
}

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

function elementText(element) {
  return [element.textContent, ...element.children.map(elementText)].join(' ');
}

function fakeHTTPResponse(body, httpStatus = 200) {
  return {
	ok: httpStatus >= 200 && httpStatus < 300,
	status: httpStatus,
	json: () => Promise.resolve(body),
  };
}

function deferredFetch() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
	resolve = resolvePromise;
	reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function createHarness(responses, options = {}) {
  const sourceSelect = new Element('select');
  const logStatus = new Element('span');
  const logViewport = new Element('div');
  const logEmpty = new Element('div');
  const logActivity = new Element('span');
  const logLevel = new Element('select');
  const logGrep = new Element('input');
  const logGrepHint = new Element('span');
  const logPause = new Element('button');
  const logNewest = new Element('span');
  const slotGrid = new Element('div');
  const slotGridEmpty = new Element('p');
  logEmpty.textContent = 'Waiting for log activity…';
  logLevel.value = 'all';
  logGrepHint.hidden = true;
  logNewest.hidden = true;
  logViewport.appendChild(logEmpty);
  const selectors = {
    '[data-log-source]': sourceSelect,
    '[data-log-status]': logStatus,
    '[data-log-viewport]': logViewport,
    '[data-log-empty]': logEmpty,
    '[data-log-activity]': logActivity,
    '[data-log-level]': logLevel,
    '[data-log-grep]': logGrep,
    '[data-log-grep-hint]': logGrepHint,
    '[data-log-pause]': logPause,
    '[data-log-newest]': logNewest,
    '[data-slot-grid]': slotGrid,
    '[data-slot-grid-empty]': slotGridEmpty,
  };
	if (options.modelCatalog) {
	  selectors['[data-model-catalog-state]'] = new Element('span');
	  selectors['[data-model-catalog-count]'] = new Element('span');
	  selectors['[data-model-catalog-last-success]'] = new Element('span');
	  selectors['[data-model-catalog-next]'] = new Element('span');
	  selectors['[data-model-catalog-interval]'] = new Element('span');
	  selectors['[data-model-catalog-refresh]'] = new Element('button');
	  selectors['[data-model-catalog-body]'] = new Element('tbody');
	  selectors['[data-model-catalog-pending]'] = new Element('p');
	  selectors['[data-model-catalog-message]'] = new Element('p');
	  selectors['[data-model-catalog-live]'] = new Element('p');
	  selectors['[data-model-catalog-refresh]'].textContent = 'Refresh now';
	  selectors['[data-model-catalog-pending]'].hidden = true;
	  selectors['[data-model-catalog-message]'].hidden = true;
	}
	for (const name of [
	  'default-profile', 'strict', 'protected', 'blocked', 'scopes', 'in-flight',
	  'entries', 'per-scope', 'ttl', 'oldest', 'triage', 'last-error',
	]) {
	  selectors[`[data-privacy-${name}]`] = new Element('span');
	}
  const documentListeners = {};
  const intervals = [];
  const timeouts = [];
  const eventSources = [];
	const fetchCalls = [];
	const catalogResponses = (options.catalogResponses || []).slice();
	const refreshResponses = (options.refreshResponses || []).slice();

  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.closed = false;
      this.listeners = {};
      eventSources.push(this);
    }

    addEventListener(type, listener) {
      this.listeners[type] = listener;
    }

    emit(type, data = '') {
      if (this.listeners[type]) this.listeners[type]({ data });
      if (type === 'open' && this.onopen) this.onopen();
      if (type === 'error' && this.onerror) this.onerror();
    }

    close() {
      this.closed = true;
    }
  }

  const document = {
    addEventListener(type, listener) {
      documentListeners[type] = listener;
    },
    createElement(tagName) {
      return new Element(tagName);
    },
    createElementNS(_namespace, tagName) {
      return new Element(tagName);
    },
    querySelector(selector) {
      return selectors[selector] || null;
    },
  };

  const context = {
    Date,
    Error,
    EventSource: FakeEventSource,
    JSON,
    Math,
    Promise,
    RegExp,
    Set,
    clearTimeout(id) {
      const timeout = timeouts.find((entry) => entry.id === id);
      if (timeout) timeout.active = false;
    },
    document,
    encodeURIComponent,
    fetch(url, requestOptions = {}) {
	  fetchCalls.push({ url, options: requestOptions });
	  let queue;
	  let label;
	  if (url === '/admin/api/snapshot') {
		queue = responses;
		label = 'snapshot';
	  } else if (url === '/admin/api/model-catalog') {
		queue = catalogResponses;
		label = 'model catalog';
	  } else if (url === '/admin/api/model-catalog/refresh') {
		queue = refreshResponses;
		label = 'model catalog refresh';
	  } else {
		throw new Error(`unexpected fetch URL ${url}`);
	  }
	  const entry = queue.shift();
	  assert.ok(entry, `unexpected ${label} fetch`);
	  if (entry.fetchPromise) return entry.fetchPromise;
	  if (entry.fetchError) return Promise.reject(new Error(entry.fetchError));
	  const status = entry.httpStatus || 200;
	  return Promise.resolve({
		ok: status >= 200 && status < 300,
		status,
		json: () => entry.jsonError
		  ? Promise.reject(new Error('invalid JSON'))
		  : Promise.resolve(Object.hasOwn(entry, 'body') ? entry.body : entry),
	  });
    },
    setInterval(callback, delay) {
      intervals.push({ callback, delay });
      return intervals.length;
    },
    setTimeout(callback, delay) {
      const timeout = { id: timeouts.length + 1, callback, delay, active: true };
      timeouts.push(timeout);
      return timeout.id;
    },
    window: { GW_ADMIN_CONFIG: { pollMs: 4321 } },
  };

  const script = fs.readFileSync(path.join(__dirname, 'static', 'js', 'admin.js'), 'utf8');
  vm.runInNewContext(script, context, { filename: 'admin.js' });

  return {
    eventSources,
	fetchCalls,
	intervals,
    logActivity,
    logEmpty,
    logGrep,
    logLevel,
    logViewport,
    sourceSelect,
    selectors,
    start() {
      documentListeners.DOMContentLoaded();
    },
    poll() {
      intervals.find((entry) => entry.delay === 4321).callback();
    },
	pollCatalog() {
	  const interval = intervals.find((entry) => entry.callback.name === 'fetchModelCatalog');
	  assert.ok(interval, 'no model catalog polling interval');
	  interval.callback();
	},
    runTimeout(delay) {
      const timeout = timeouts.find((entry) => entry.delay === delay && entry.active);
      assert.ok(timeout, `no active timeout with delay ${delay}`);
      timeout.active = false;
      timeout.callback();
    },
  };
}

async function settleSnapshot() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

async function settleAsyncWork() {
  for (let i = 0; i < 24; i++) await Promise.resolve();
}

function catalogModel(id, name, capabilities = {}) {
  return {
	id,
	name,
	selection_mode: id === 'auto' ? 'automatic' : 'explicit',
	capabilities: {
	  completion: 'supported',
	  tools: 'supported',
	  vision: 'unsupported',
	  reasoning: 'unknown',
	  ...capabilities,
	},
  };
}

function modelCatalog(models, overrides = {}) {
  return {
	state: 'ready',
	count: models.length,
	generation: 4,
	models,
	refresh: {
	  enabled: true,
	  interval_seconds: 900,
	  in_progress: false,
	  last_success_at: '2026-08-13T14:05:06Z',
	  next_attempt_at: '2026-08-13T14:20:06Z',
	  last_outcome: 'unchanged',
	  pending_removals: 0,
	},
	...overrides,
  };
}

test('log source labels cache and selection drive the real EventSource flow', async () => {
  const harness = createHarness([
    snapshot({ main: 'Gateway', kiro: 'Kiro' }),
    snapshot({ kiro: 'Kiro', main: 'Gateway' }),
    snapshot({ main: 'Gateway', kiro: 'Kiro assistant' }),
  ]);

  harness.start();
  await settleSnapshot();

  assert.deepEqual(
    harness.sourceSelect.children.map((option) => [option.value, option.textContent]),
    [['main', 'Gateway'], ['kiro', 'Kiro']],
  );
  assert.equal(harness.sourceSelect.value, 'main', 'main is the initial default');
  assert.equal(harness.eventSources[0].url, '/admin/logs/stream?source=main');

  const initialOptions = harness.sourceSelect.children.slice();
  harness.poll();
  await settleSnapshot();
  assert.equal(harness.sourceSelect.children[0], initialOptions[0], 'equivalent labels must not rebuild options');
  assert.equal(harness.sourceSelect.children[1], initialOptions[1], 'equivalent labels must not rebuild options');

  harness.sourceSelect.value = 'kiro';
  harness.sourceSelect.dispatchEvent({ type: 'change' });
  assert.equal(harness.eventSources[0].closed, true, 'selecting Kiro closes the prior stream');
  assert.equal(harness.eventSources[1].url, '/admin/logs/stream?source=kiro');

  harness.poll();
  await settleSnapshot();
  assert.deepEqual(
    harness.sourceSelect.children.map((option) => [option.value, option.textContent]),
    [['main', 'Gateway'], ['kiro', 'Kiro assistant']],
  );
  assert.equal(harness.sourceSelect.value, 'kiro', 'label-only rerender preserves the selected ID');
  assert.equal(harness.eventSources.length, 2, 'label-only rerender does not reconnect');
});

test('a friendly label arriving after SSE open refreshes transport text', async () => {
  const harness = createHarness([snapshot({ main: 'Gateway', kiro: 'Kiro' })]);
  harness.start();
  harness.eventSources[0].emit('open');
  await settleSnapshot();
  assert.equal(harness.selectors['[data-log-status]'].textContent, 'Connected — Gateway');
});

test('log source status events render precise file-health messages', async () => {
  const harness = createHarness([snapshot({ main: 'Gateway', kiro: 'Kiro' })]);
  harness.start();
  await settleSnapshot();
  const source = harness.eventSources[0];
  const cases = [
    [{ state: 'opening' }, 'Checking log source…'],
    [{ state: 'missing' }, 'Log file has not been created yet. Watching for it…'],
    [{ state: 'unreadable' }, 'Log file cannot be read. Check its permissions.'],
    [{ state: 'empty', size_bytes: 0 }, 'Log file is empty. Waiting for the first entry.'],
    [{ state: 'watching', size_bytes: 18 }, 'Connected and watching for new complete log entries.'],
  ];
  for (const [status, want] of cases) {
    source.emit('status', JSON.stringify(status));
    assert.equal(harness.logEmpty.textContent, want, status.state);
    assert.equal(harness.logEmpty.hidden, false, `${status.state} placeholder visibility`);
  }

  source.emit('status', '{malformed');
  assert.equal(
    harness.logEmpty.textContent,
    'Connected and watching for new complete log entries.',
    'malformed status must preserve the last valid state',
  );
});

test('Kiro transport and empty states use its friendly label and effective level', async () => {
  const harness = createHarness([snapshot({ main: 'Gateway', kiro: 'Kiro' })]);
  harness.start();
  await settleSnapshot();
  harness.sourceSelect.value = 'kiro';
  harness.sourceSelect.dispatchEvent({ type: 'change' });
  assert.equal(harness.selectors['[data-log-status]'].textContent, 'Connecting to Kiro…');

  const source = harness.eventSources[1];
  source.emit('open');
  assert.equal(harness.selectors['[data-log-status]'].textContent, 'Connected — Kiro');
  source.emit('status', JSON.stringify({ state: 'empty', size_bytes: 0, level: 'DEBUG' }));
  assert.equal(
    harness.logEmpty.textContent,
    'Kiro log is empty. Logging is configured at DEBUG; waiting for the first entry.',
  );

  source.emit('error');
  assert.equal(
    harness.selectors['[data-log-status]'].textContent,
    'Log stream disconnected — reconnecting…',
  );
});

test('level filters distinguish received-but-hidden rows from an empty file', async () => {
  const harness = createHarness([snapshot({ main: 'Gateway', kiro: 'Kiro' })]);
  harness.start();
  await settleSnapshot();
  const source = harness.eventSources[0];
  source.emit('log', JSON.stringify({
    level: 'INFO',
    time: '2026-08-05T12:00:00Z',
    msg: 'request complete',
    logger: 'gateway',
  }));
  const row = harness.logViewport.querySelector('.gw-log-row');
  assert.ok(row, 'real log event should render a row');
  assert.equal(harness.logEmpty.hidden, true);

  harness.logLevel.value = 'error';
  harness.logLevel.dispatchEvent({ type: 'change' });
  assert.equal(row.style.display, 'none');
  assert.equal(harness.logEmpty.hidden, false);
  assert.equal(
    harness.logEmpty.textContent,
    'Log entries were received, but none match the current filters.',
  );

  harness.logLevel.value = 'all';
  harness.logLevel.dispatchEvent({ type: 'change' });
  assert.equal(row.style.display, '');
  assert.equal(harness.logEmpty.hidden, true);
});

test('regex filters recompute the received-but-hidden state after debounce', async () => {
  const harness = createHarness([snapshot({ main: 'Gateway', kiro: 'Kiro' })]);
  harness.start();
  await settleSnapshot();
  const source = harness.eventSources[0];
  source.emit('log', JSON.stringify({
    level: 'WARN',
    time: '2026-08-05T12:00:00Z',
    msg: 'retry scheduled',
    logger: 'gateway',
  }));
  const row = harness.logViewport.querySelector('.gw-log-row');

  harness.logGrep.value = 'does-not-match';
  harness.logGrep.dispatchEvent({ type: 'input' });
  harness.runTimeout(150);
  assert.equal(row.style.display, 'none');
  assert.equal(
    harness.logEmpty.textContent,
    'Log entries were received, but none match the current filters.',
  );
  assert.equal(harness.logEmpty.hidden, false);

  harness.logGrep.value = 'retry';
  harness.logGrep.dispatchEvent({ type: 'input' });
  harness.runTimeout(150);
  assert.equal(row.style.display, '');
  assert.equal(harness.logEmpty.hidden, true);
});

test('privacy snapshot hydrates read-only dashboard status and current/max limits', async () => {
  const harness = createHarness([snapshot({ main: 'Gateway', kiro: 'Kiro' })]);
  harness.start();
  await settleSnapshot();

  const text = (name) => harness.selectors[`[data-privacy-${name}]`].textContent;
  assert.equal(text('default-profile'), 'strict');
  assert.equal(text('strict'), 'AVAILABLE');
  assert.equal(text('protected'), '144');
  assert.equal(text('blocked'), '7');
  assert.equal(text('scopes'), '3 / 128');
  assert.equal(text('in-flight'), '2');
  assert.equal(text('entries'), '41 / 32768');
  assert.equal(text('per-scope'), '4096');
  assert.equal(text('ttl'), '1h');
  assert.equal(text('oldest'), '1m 31s');
  assert.equal(text('triage'), 'DISABLED');
  assert.equal(text('last-error'), 'privacy_output_blocked');
  assert.match(harness.selectors['[data-privacy-strict]'].className, /is-ok/);
  assert.match(harness.selectors['[data-privacy-triage]'].className, /is-muted/);
});

test('slot cards render idle-memory recycling policy across worker lifecycle states', async () => {
  const unused = slotSnapshot({
    pid: 4102,
    user_requests_since_spawn: 0,
    last_user_release_at: '',
  });
  const busy = slotSnapshot({
    pid: 4103,
    busy: true,
    last_user_release_at: '2026-08-04T11:20:00Z',
  });
  const checkedOut = slotSnapshot({
    pid: 4105,
    checked_out: true,
    last_user_release_at: '2026-08-04T11:20:00Z',
  });
  const unsupported = slotSnapshot({
    pid: 4104,
    stat_ok: false,
  });
  unsupported.pool.idle_recycle_supported = false;
  const replacement = slotSnapshot({
    pid: 5101,
    rss_bytes: 300 * 1024 * 1024,
    turns: 0,
    user_requests_since_spawn: 0,
    last_user_release_at: '',
  });
  const harness = createHarness([
    slotSnapshot(),
    unused,
    busy,
    checkedOut,
    unsupported,
    replacement,
  ]);

  harness.start();
  await settleSnapshot();
  let text = elementText(harness.selectors['[data-slot-grid]'].children[0]);
  assert.match(text, /USER REQS\s+1/);
  assert.match(text, /IDLE\s+16m 0s \/ 15m 0s/);
  assert.match(text, /Mem\s+800 MiB \/ 500 MiB/);

  harness.poll();
  await settleSnapshot();
  text = elementText(harness.selectors['[data-slot-grid]'].children[0]);
  assert.match(text, /USER REQS\s+0/);
  assert.match(text, /IDLE\s+—/);

  harness.poll();
  await settleSnapshot();
  text = elementText(harness.selectors['[data-slot-grid]'].children[0]);
  assert.match(text, /IDLE\s+active/);

  harness.poll();
  await settleSnapshot();
  text = elementText(harness.selectors['[data-slot-grid]'].children[0]);
  assert.match(text, /Active/);
  assert.match(text, /IDLE\s+active/);

  harness.poll();
  await settleSnapshot();
  text = elementText(harness.selectors['[data-slot-grid]'].children[0]);
  assert.match(text, /perf n\/a/);
  assert.match(text, /IDLE\s+n\/a/);
  assert.doesNotMatch(text, /500 MiB/);

  harness.poll();
  await settleSnapshot();
  text = elementText(harness.selectors['[data-slot-grid]'].children[0]);
  assert.match(text, /pid 5101/);
  assert.match(text, /USER REQS\s+0/);
  assert.match(text, /IDLE\s+—/);
});

test('slot cards reject a release timestamp later than the snapshot timestamp', async () => {
  const harness = createHarness([slotSnapshot({
    last_user_release_at: '2026-08-04T12:00:01Z',
  })]);

  harness.start();
  await settleSnapshot();
  const text = elementText(harness.selectors['[data-slot-grid]'].children[0]);
  assert.match(text, /IDLE\s+—/);
  assert.doesNotMatch(text, /IDLE\s+0s/);
});

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

test('slot grid preserves source snapshots and renders unexpected workers above the cap', async () => {
  const paddedSnapshot = slotSnapshotWithCount(2);
  const unexpectedSnapshot = slotSnapshotWithCount(7);
  const paddedSlots = paddedSnapshot.pool.slots;
  const unexpectedSlots = unexpectedSnapshot.pool.slots;
  const paddedSlotsBefore = structuredClone(paddedSlots);
  const unexpectedSlotsBefore = structuredClone(unexpectedSlots);
  const harness = createHarness([paddedSnapshot, unexpectedSnapshot]);

  harness.start();
  await settleSnapshot();
  assert.strictEqual(paddedSnapshot.pool.slots, paddedSlots);
  assert.equal(paddedSlots.length, 2, 'padding must not add vacant cards to the server snapshot');
  assert.deepEqual(paddedSlots, paddedSlotsBefore, 'padding must preserve real-slot order and content');

  harness.poll();
  await settleSnapshot();
  assert.equal(harness.selectors['[data-slot-grid]'].children.length, 7);
  assert.equal(
    harness.selectors['[data-slot-grid]'].children.filter((child) => child.className.includes('is-vacant')).length,
    0,
  );
  assert.strictEqual(unexpectedSnapshot.pool.slots, unexpectedSlots);
  assert.equal(unexpectedSlots.length, 7, 'rendering must not truncate the server snapshot');
  assert.deepEqual(unexpectedSlots, unexpectedSlotsBefore, 'rendering must preserve real-slot order and content');
});

test('model catalog groups shuffled rows and applies numeric-aware name and exact-ID sorting', async () => {
  const models = [
	catalogModel('mistral-large', 'Mistral Large'),
	catalogModel('gpt-zeta', 'GPT Same'),
	catalogModel('qwen3-coder-next', 'Qwen 3 Coder Next', {
	  completion: 'unknown', tools: 'unknown', vision: 'unknown', reasoning: 'unknown',
	}),
	catalogModel('gpt-5.10-preview', 'GPT 5.10 Preview'),
	catalogModel('auto', 'Automatic', {
	  completion: 'unknown', tools: 'unknown', vision: 'unknown', reasoning: 'unknown',
	}),
	catalogModel('gpt-5.6-sol', 'GPT 5.6 Sol'),
	catalogModel('claude-sonnet-5', 'Claude Sonnet 5'),
	catalogModel('gpt-alpha', 'GPT Same'),
	catalogModel('gpt-5.6-luna', 'GPT 5.6 Luna'),
	catalogModel('gpt-a', 'GPT Code Unit Tie'),
	catalogModel('gpt-Z', 'GPT Code Unit Tie'),
  ];
  const originalOrder = models.map((model) => model.id);
  const harness = createHarness(
	[snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	{ modelCatalog: true, catalogResponses: [modelCatalog(models)] },
  );

  harness.start();
  await settleAsyncWork();

  const body = harness.selectors['[data-model-catalog-body]'];
  const groupRows = body.children.filter((row) => row.className === 'gw-model-catalog-group');
  assert.deepEqual(
	groupRows.map((row) => row.children[0].textContent),
	['Automatic', 'Anthropic / Claude', 'OpenAI / GPT', 'Qwen', 'Other models'],
  );
  for (const row of groupRows) {
	assert.equal(row.children[0].scope, 'rowgroup');
	assert.equal(row.children[0].colSpan, 6);
  }

  const dataRows = body.children.filter((row) => row.className === 'gw-model-catalog-row');
  const gptIDs = dataRows
	.filter((row) => row.children[1].textContent.startsWith('gpt-'))
	.map((row) => row.children[1].textContent);
  assert.deepEqual(gptIDs, [
	'gpt-5.6-luna',
	'gpt-5.6-sol',
	'gpt-5.10-preview',
	'gpt-Z',
	'gpt-a',
	'gpt-alpha',
	'gpt-zeta',
  ]);
  const qwenRow = dataRows.find((row) => row.children[1].textContent === 'qwen3-coder-next');
  assert.ok(qwenRow, 'Qwen row should render');
  assert.deepEqual(
	qwenRow.children.slice(2).map((cell) => elementText(cell).trim()),
	['Unknown', 'Unknown', 'Unknown', 'Unknown'],
  );
  assert.doesNotMatch(qwenRow.children.slice(2).map(elementText).join(' '), /Unsupported/);
  assert.deepEqual(models.map((model) => model.id), originalOrder, 'rendering must not mutate the response');
});

test('model catalog renders a distinct persistent pluralized pending-removals warning with count only', async () => {
	const refreshWithPending = (count) => ({
	  enabled: true,
	  interval_seconds: 900,
	  in_progress: false,
	  last_success_at: '2026-08-13T14:05:06Z',
	  next_attempt_at: '2026-08-13T14:20:06Z',
	  last_outcome: 'pending_shrink',
	  pending_removals: count,
	});
	const harness = createHarness(
	  [snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	  {
		modelCatalog: true,
		catalogResponses: [
		  modelCatalog([catalogModel('gpt-published', 'GPT Published')], {
			state: 'pending_shrink',
			refresh: refreshWithPending(1),
		  }),
		  modelCatalog([catalogModel('gpt-published', 'GPT Published')], {
			state: 'pending_shrink',
			refresh: refreshWithPending(2),
		  }),
		],
	  },
	);

	harness.start();
	await settleAsyncWork();
	const pending = harness.selectors['[data-model-catalog-pending]'];
	const transient = harness.selectors['[data-model-catalog-message]'];
	assert.equal(pending.hidden, false);
	assert.equal(
	  pending.textContent,
	  '1 model removal awaits confirmation. The current catalog remains in use.',
	);
	assert.doesNotMatch(pending.textContent, /gpt-published/);
	assert.notStrictEqual(pending, transient, 'pending state must not share the transient action message node');

	harness.pollCatalog();
	await settleAsyncWork();
	assert.equal(
	  pending.textContent,
	  '2 model removals await confirmation. The current catalog remains in use.',
	);
});

test('model catalog performs an isolated initial GET and poll at the configured cadence', async () => {
  const first = modelCatalog([catalogModel('auto', 'Automatic')]);
  const second = modelCatalog([
	catalogModel('auto', 'Automatic'),
	catalogModel('claude-sonnet-5', 'Claude Sonnet 5'),
  ]);
  const harness = createHarness(
	[
	  snapshot({ main: 'Gateway', kiro: 'Kiro' }),
	  snapshot({ main: 'Gateway', kiro: 'Kiro' }),
	],
	{ modelCatalog: true, catalogResponses: [first, second] },
  );

  harness.start();
  await settleAsyncWork();
  let snapshotCalls = harness.fetchCalls.filter((call) => call.url === '/admin/api/snapshot');
  let catalogCalls = harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog');
  assert.equal(snapshotCalls.length, 1);
  assert.equal(catalogCalls.length, 1);
  assert.equal(catalogCalls[0].options.cache, 'no-store');
  assert.equal(catalogCalls[0].options.headers.Accept, 'application/json');
  assert.equal(
	harness.intervals.filter((entry) => entry.delay === 4321 && entry.callback.name === 'fetchModelCatalog').length,
	1,
	'catalog must own one distinct polling interval',
  );

  harness.pollCatalog();
  await settleAsyncWork();
  snapshotCalls = harness.fetchCalls.filter((call) => call.url === '/admin/api/snapshot');
  catalogCalls = harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog');
  assert.equal(snapshotCalls.length, 1, 'catalog poll must not invoke snapshot polling');
  assert.equal(catalogCalls.length, 2);

  harness.poll();
  await settleAsyncWork();
  assert.equal(
	harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog').length,
	2,
	'snapshot poll must not invoke catalog polling',
  );
});

test('model catalog refresh POST is empty, busy while pending, and immediately GETs on success', async () => {
  const harness = createHarness(
	[snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	{
	  modelCatalog: true,
	  catalogResponses: [
		modelCatalog([catalogModel('auto', 'Automatic')]),
		modelCatalog([
		  catalogModel('auto', 'Automatic'),
		  catalogModel('gpt-5.6-sol', 'GPT 5.6 Sol'),
		]),
	  ],
	  refreshResponses: [{ body: { outcome: 'expanded', message: 'Model catalog refresh completed.' } }],
	},
  );
  harness.start();
  await settleAsyncWork();

  const button = harness.selectors['[data-model-catalog-refresh]'];
  button.dispatchEvent({ type: 'click' });
  assert.equal(button.disabled, true);
  assert.equal(button['aria-busy'], 'true');
  assert.equal(button.textContent, 'Refreshing…');

  await settleAsyncWork();
  const refreshCalls = harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog/refresh');
  assert.equal(refreshCalls.length, 1);
  assert.equal(refreshCalls[0].options.method, 'POST');
  assert.equal(refreshCalls[0].options.headers.Accept, 'application/json');
  assert.equal(Object.hasOwn(refreshCalls[0].options, 'body'), false, 'refresh POST must have no body');
  assert.equal(
	harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog').length,
	2,
	'success must immediately fetch the resulting catalog',
  );
  assert.equal(button.disabled, false);
  assert.equal(button['aria-busy'], 'false');
  assert.equal(button.textContent, 'Refresh now');
  assert.equal(
	harness.selectors['[data-model-catalog-live]'].textContent,
	'Model catalog refresh completed.',
  );
  assert.equal(harness.selectors['[data-model-catalog-count]'].textContent, '2');
});

test('model catalog refresh success applies the authoritative retry cooldown', async () => {
  const harness = createHarness(
	[snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	{
	  modelCatalog: true,
	  catalogResponses: [
		modelCatalog([catalogModel('auto', 'Automatic')]),
		modelCatalog([
		  catalogModel('auto', 'Automatic'),
		  catalogModel('gpt-5.6-sol', 'GPT 5.6 Sol'),
		]),
	  ],
	  refreshResponses: [{
		body: {
		  outcome: 'expanded',
		  message: 'Model catalog refresh completed.',
		  retry_after_seconds: 30,
		},
	  }],
	},
  );
  const button = harness.selectors['[data-model-catalog-refresh]'];
  harness.start();
  await settleAsyncWork();
  button.dispatchEvent({ type: 'click' });
  await settleAsyncWork();

  assert.equal(button.disabled, true);
  assert.equal(button['aria-busy'], 'false');
  assert.equal(button.textContent, 'Retry in 30s');
  assert.equal(
	harness.selectors['[data-model-catalog-message]'].textContent,
	'Model catalog refresh completed.',
  );
  assert.equal(harness.selectors['[data-model-catalog-count]'].textContent, '2');

  harness.runTimeout(30000);
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, 'Refresh now');
});

test('model catalog refresh errors use fixed local copy and retry cooldowns', async () => {
  const cases = [
	{
	  status: 409,
	  code: 'catalog_refresh_in_progress',
	  retry: 2,
	  want: 'A model catalog refresh is already in progress.',
	},
	{
	  status: 429,
	  code: 'catalog_refresh_cooldown',
	  retry: 4,
	  want: 'Model catalog refresh is temporarily rate limited.',
	},
	{
	  status: 503,
	  code: 'catalog_refresh_busy',
	  retry: 30,
	  want: 'No idle gateway worker is available for a model catalog refresh. The current catalog remains in use.',
	},
	{
	  status: 503,
	  code: 'catalog_refresh_unavailable',
	  retry: 0,
	  want: 'Model catalog refresh is unavailable.',
	},
	{
	  status: 502,
	  code: 'catalog_refresh_failed',
	  retry: 30,
	  want: 'Model catalog refresh failed. The current catalog remains in use.',
	},
	{
	  status: 502,
	  code: 'not-a-bounded-code',
	  retry: 0,
	  want: 'Model catalog refresh failed. The current catalog remains in use.',
	},
  ];

  for (const tc of cases) {
	const harness = createHarness(
	  [snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	  {
		modelCatalog: true,
		catalogResponses: [modelCatalog([catalogModel('auto', 'Automatic')])],
		refreshResponses: [{
		  httpStatus: tc.status,
		  body: {
			code: tc.code,
			message: 'UPSTREAM PRIVATE ERROR MUST NOT RENDER',
			retry_after_seconds: tc.retry,
		  },
		}],
	  },
	);
	const button = harness.selectors['[data-model-catalog-refresh]'];
	harness.start();
	await settleAsyncWork();
	button.dispatchEvent({ type: 'click' });
	await settleAsyncWork();

	const message = harness.selectors['[data-model-catalog-message]'];
	assert.equal(message.textContent, tc.want, `${tc.status} ${tc.code}`);
	assert.doesNotMatch(message.textContent, /PRIVATE ERROR/);
	assert.equal(button['aria-busy'], 'false');
	if (tc.retry > 0) {
	  assert.equal(button.disabled, true, `${tc.code} cooldown should disable refresh`);
	  assert.equal(button.textContent, `Retry in ${tc.retry}s`);
	  harness.runTimeout(tc.retry * 1000);
	  assert.equal(button.disabled, false, `${tc.code} cooldown should expire`);
	  assert.equal(button.textContent, 'Refresh now');
	} else {
	  assert.equal(button.disabled, false, `${tc.code} without retry should restore refresh`);
	}
  }
});

test('model catalog failed actions invalidate older GETs and later polls preserve the action state', async () => {
	const cases = [
	  { status: 409, code: 'catalog_refresh_in_progress', retry: 2, want: 'A model catalog refresh is already in progress.' },
	  { status: 429, code: 'catalog_refresh_cooldown', retry: 4, want: 'Model catalog refresh is temporarily rate limited.' },
	  {
		status: 503,
		code: 'catalog_refresh_busy',
		retry: 30,
		want: 'No idle gateway worker is available for a model catalog refresh. The current catalog remains in use.',
	  },
	  {
		status: 502,
		code: 'catalog_refresh_failed',
		retry: 0,
		want: 'Model catalog refresh failed. The current catalog remains in use.',
	  },
	];

	for (const tc of cases) {
	  for (const staleOutcome of ['resolve', 'reject']) {
		const stalePoll = deferredFetch();
		const initial = modelCatalog([
		  catalogModel('auto', 'Automatic'),
		  catalogModel('gpt-current', 'GPT Current'),
		]);
		const stale = modelCatalog([
		  catalogModel('auto', 'Automatic'),
		  catalogModel('gpt-stale', 'GPT Stale'),
		], {
		  state: 'refreshing',
		  generation: 2,
		  refresh: {
			enabled: true,
			interval_seconds: 900,
			in_progress: true,
			last_success_at: '2026-08-13T13:00:00Z',
			next_attempt_at: '2026-08-13T13:15:00Z',
			last_outcome: 'unchanged',
			pending_removals: 0,
		  },
		});
		const laterPoll = modelCatalog([
		  catalogModel('auto', 'Automatic'),
		  catalogModel('gpt-later', 'GPT Later'),
		], { generation: 3 });
		const harness = createHarness(
		  [snapshot({ main: 'Gateway', kiro: 'Kiro' })],
		  {
			modelCatalog: true,
			catalogResponses: [initial, { fetchPromise: stalePoll.promise }, laterPoll],
			refreshResponses: [{
			  httpStatus: tc.status,
			  body: { code: tc.code, retry_after_seconds: tc.retry, message: 'private server detail' },
			}],
		  },
		);

		harness.start();
		await settleAsyncWork();
		harness.pollCatalog();
		harness.selectors['[data-model-catalog-refresh]'].dispatchEvent({ type: 'click' });
		await settleAsyncWork();
		const message = harness.selectors['[data-model-catalog-message]'];
		const live = harness.selectors['[data-model-catalog-live]'];
		const body = harness.selectors['[data-model-catalog-body]'];
		assert.equal(message.textContent, tc.want, `${tc.status} ${staleOutcome}: terminal action`);

		if (staleOutcome === 'resolve') stalePoll.resolve(fakeHTTPResponse(stale));
		else stalePoll.reject(new Error('late stale poll failure'));
		await settleAsyncWork();
		assert.equal(message.textContent, tc.want, `${tc.status} ${staleOutcome}: stale GET must not erase action`);
		assert.equal(live.textContent, tc.want, `${tc.status} ${staleOutcome}: stale GET must not replace announcement`);
		assert.doesNotMatch(elementText(body), /gpt-stale/);

		harness.pollCatalog();
		await settleAsyncWork();
		assert.match(elementText(body), /gpt-later/, `${tc.status} ${staleOutcome}: a later poll may refresh the view`);
		assert.equal(message.textContent, tc.want, `${tc.status} ${staleOutcome}: later poll must preserve failed action`);
		assert.equal(live.textContent, tc.want, `${tc.status} ${staleOutcome}: later poll must preserve action announcement`);
	  }
	}
});

test('model catalog disabled scheduling leaves manual refresh available', async () => {
  const disabled = modelCatalog(
	[catalogModel('auto', 'Automatic')],
	{
	  state: 'disabled',
	  refresh: {
		enabled: false,
		interval_seconds: 0,
		in_progress: false,
		last_success_at: '',
		next_attempt_at: '',
		last_outcome: 'startup',
		pending_removals: 0,
	  },
	},
  );
  const harness = createHarness(
	[snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	{
	  modelCatalog: true,
	  catalogResponses: [disabled],
	  refreshResponses: [{
		httpStatus: 503,
		body: { code: 'catalog_refresh_unavailable', message: 'server message' },
	  }],
	},
  );

  harness.start();
  await settleAsyncWork();
  const button = harness.selectors['[data-model-catalog-refresh]'];
  assert.equal(harness.selectors['[data-model-catalog-state]'].textContent, 'Disabled');
  assert.equal(harness.selectors['[data-model-catalog-next]'].textContent, 'Disabled');
  assert.equal(harness.selectors['[data-model-catalog-interval]'].textContent, 'Manual only');
  assert.equal(button.disabled, false, 'disabled scheduling must not disable manual refresh');
  assert.equal(button.textContent, 'Refresh now');

  button.dispatchEvent({ type: 'click' });
  await settleAsyncWork();
  assert.equal(
	harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog/refresh').length,
	1,
	'manual refresh should still reach the endpoint',
  );
  assert.equal(
	harness.selectors['[data-model-catalog-message]'].textContent,
	'Model catalog refresh is unavailable.',
	'endpoint availability remains a server-owned decision',
  );
});

test('model catalog ignores stale overlapping GET resolution and failure after refresh', async () => {
  for (const staleOutcome of ['resolve', 'reject']) {
	const stalePoll = deferredFetch();
	const initial = modelCatalog([
	  catalogModel('auto', 'Automatic'),
	  catalogModel('gpt-old', 'GPT Old'),
	]);
	const refreshed = modelCatalog([
	  catalogModel('auto', 'Automatic'),
	  catalogModel('gpt-new', 'GPT New'),
	], { generation: 9 });
	const stale = modelCatalog([
	  catalogModel('auto', 'Automatic'),
	  catalogModel('gpt-stale', 'GPT Stale'),
	], {
	  state: 'refreshing',
	  generation: 4,
	  refresh: {
		enabled: true,
		interval_seconds: 900,
		in_progress: true,
		last_success_at: '2026-08-13T13:00:00Z',
		next_attempt_at: '2026-08-13T13:15:00Z',
		last_outcome: 'unchanged',
		pending_removals: 0,
	  },
	});
	const harness = createHarness(
	  [snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	  {
		modelCatalog: true,
		catalogResponses: [initial, { fetchPromise: stalePoll.promise }, refreshed],
		refreshResponses: [{
		  body: { outcome: 'expanded', message: 'Model catalog refresh completed.' },
		}],
	  },
	);

	harness.start();
	await settleAsyncWork();
	harness.pollCatalog();
	harness.selectors['[data-model-catalog-refresh]'].dispatchEvent({ type: 'click' });
	await settleAsyncWork();

	const body = harness.selectors['[data-model-catalog-body]'];
	const button = harness.selectors['[data-model-catalog-refresh]'];
	assert.match(elementText(body), /gpt-new/, `${staleOutcome}: immediate GET should render fresh view`);
	assert.doesNotMatch(elementText(body), /gpt-old|gpt-stale/);
	assert.equal(
	  harness.selectors['[data-model-catalog-message]'].textContent,
	  'Model catalog refresh completed.',
	);
	assert.equal(button.disabled, false);
	assert.equal(button['aria-busy'], 'false');
	assert.equal(button.textContent, 'Refresh now');

	if (staleOutcome === 'resolve') {
	  stalePoll.resolve(fakeHTTPResponse(stale));
	} else {
	  stalePoll.reject(new Error('late stale poll failure'));
	}
	await settleAsyncWork();

	assert.match(elementText(body), /gpt-new/, `${staleOutcome}: stale completion must not replace fresh view`);
	assert.doesNotMatch(elementText(body), /gpt-stale/);
	assert.equal(
	  harness.selectors['[data-model-catalog-message]'].textContent,
	  'Model catalog refresh completed.',
	  `${staleOutcome}: stale completion must not replace the fresh action message`,
	);
	assert.equal(
	  harness.selectors['[data-model-catalog-live]'].textContent,
	  'Model catalog refresh completed.',
	  `${staleOutcome}: stale completion must not replace the fresh live announcement`,
	);
	assert.equal(button.disabled, false, `${staleOutcome}: stale completion must not disable the control`);
	assert.equal(button['aria-busy'], 'false');
	assert.equal(button.textContent, 'Refresh now');
  }
});

test('model catalog privileged success GET wins while pending interval polls do not claim freshness', async () => {
  for (const staleOutcome of ['resolve', 'reject']) {
	for (const completionOrder of ['stale-first', 'fresh-first']) {
	  const stalePoll = deferredFetch();
	  const freshPoll = deferredFetch();
	  const initial = modelCatalog([
		catalogModel('auto', 'Automatic'),
		catalogModel('gpt-old', 'GPT Old'),
	  ]);
	  const stale = modelCatalog([
		catalogModel('auto', 'Automatic'),
		catalogModel('gpt-stale', 'GPT Stale'),
	  ], { generation: 4 });
	  const refreshed = modelCatalog([
		catalogModel('auto', 'Automatic'),
		catalogModel('gpt-new', 'GPT New'),
	  ], { generation: 9 });
	  const harness = createHarness(
		[snapshot({ main: 'Gateway', kiro: 'Kiro' })],
		{
		  modelCatalog: true,
		  catalogResponses: [
			initial,
			{ fetchPromise: stalePoll.promise },
			{ fetchPromise: freshPoll.promise },
		  ],
		  refreshResponses: [{
			body: { outcome: 'expanded', message: 'Model catalog refresh completed.' },
		  }],
		},
	  );

	  harness.start();
	  await settleAsyncWork();
	  harness.pollCatalog();
	  harness.selectors['[data-model-catalog-refresh]'].dispatchEvent({ type: 'click' });
	  await settleAsyncWork();

	  const button = harness.selectors['[data-model-catalog-refresh]'];
	  assert.equal(button.disabled, true, `${staleOutcome}/${completionOrder}: POST remains pending through success GET`);
	  assert.equal(button['aria-busy'], 'true');
	  assert.equal(button.textContent, 'Refreshing…');
	  assert.equal(
		harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog').length,
		3,
		`${staleOutcome}/${completionOrder}: initial, stale, and privileged GET should be started`,
	  );

	  harness.pollCatalog();
	  assert.equal(
		harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog').length,
		3,
		`${staleOutcome}/${completionOrder}: pending interval must no-op before fetching or claiming freshness`,
	  );

	  const completeStale = () => {
		if (staleOutcome === 'resolve') stalePoll.resolve(fakeHTTPResponse(stale));
		else stalePoll.reject(new Error('late stale poll failure'));
	  };
	  if (completionOrder === 'stale-first') {
		completeStale();
		await settleAsyncWork();
		freshPoll.resolve(fakeHTTPResponse(refreshed));
	  } else {
		freshPoll.resolve(fakeHTTPResponse(refreshed));
		await settleAsyncWork();
		completeStale();
	  }
	  await settleAsyncWork();

	  const body = harness.selectors['[data-model-catalog-body]'];
	  assert.match(elementText(body), /gpt-new/, `${staleOutcome}/${completionOrder}: privileged view must render`);
	  assert.doesNotMatch(elementText(body), /gpt-old|gpt-stale/);
	  assert.equal(
		harness.selectors['[data-model-catalog-message]'].textContent,
		'Model catalog refresh completed.',
	  );
	  assert.equal(
		harness.selectors['[data-model-catalog-live]'].textContent,
		'Model catalog refresh completed.',
	  );
	  assert.equal(button.disabled, false);
	  assert.equal(button['aria-busy'], 'false');
	  assert.equal(button.textContent, 'Refresh now');
	}
  }
});

test('model catalog initial GET failure preserves manual POST recovery', async () => {
  const cases = [
	{
	  name: 'success',
	  catalogResponses: [
		{ fetchError: 'initial GET offline' },
		modelCatalog([
		  catalogModel('auto', 'Automatic'),
		  catalogModel('claude-sonnet-5', 'Claude Sonnet 5'),
		]),
	  ],
	  refreshResponse: {
		body: { outcome: 'expanded', message: 'Model catalog refresh completed.' },
	  },
	  wantMessage: 'Model catalog refresh completed.',
	},
	{
	  name: 'error',
	  catalogResponses: [{ fetchError: 'initial GET offline' }],
	  refreshResponse: {
		httpStatus: 502,
		body: { code: 'catalog_refresh_failed', message: 'private upstream detail' },
	  },
	  wantMessage: 'Model catalog refresh failed. The current catalog remains in use.',
	},
  ];

  for (const tc of cases) {
	const harness = createHarness(
	  [snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	  {
		modelCatalog: true,
		catalogResponses: tc.catalogResponses,
		refreshResponses: [tc.refreshResponse],
	  },
	);
	const button = harness.selectors['[data-model-catalog-refresh]'];
	harness.start();
	await settleAsyncWork();

	assert.equal(button.disabled, false, `${tc.name}: initial GET failure must restore manual action`);
	assert.equal(button['aria-busy'], 'false');
	assert.equal(button.textContent, 'Refresh now');
	button.dispatchEvent({ type: 'click' });
	assert.equal(button.disabled, true, `${tc.name}: POST pending state`);
	assert.equal(button['aria-busy'], 'true');
	assert.equal(button.textContent, 'Refreshing…');

	await settleAsyncWork();
	const refreshCalls = harness.fetchCalls.filter(
	  (call) => call.url === '/admin/api/model-catalog/refresh',
	);
	assert.equal(refreshCalls.length, 1, `${tc.name}: manual click must reach POST`);
	assert.equal(refreshCalls[0].options.method, 'POST');
	assert.equal(Object.hasOwn(refreshCalls[0].options, 'body'), false, 'recovery POST must remain empty');
	assert.equal(button.disabled, false, `${tc.name}: completion must restore manual action`);
	assert.equal(button['aria-busy'], 'false');
	assert.equal(button.textContent, 'Refresh now');
	assert.equal(harness.selectors['[data-model-catalog-message]'].textContent, tc.wantMessage);
  }
});

test('model catalog GET failure retains the last good grouped table', async () => {
  const harness = createHarness(
	[snapshot({ main: 'Gateway', kiro: 'Kiro' })],
	{
	  modelCatalog: true,
	  catalogResponses: [
		modelCatalog([
		  catalogModel('auto', 'Automatic'),
		  catalogModel('claude-sonnet-5', 'Claude Sonnet 5'),
		]),
		{ httpStatus: 500, body: { message: 'private server detail' } },
	  ],
	},
  );
  harness.start();
  await settleAsyncWork();
  const body = harness.selectors['[data-model-catalog-body]'];
  const before = elementText(body);
  const beforeChildren = body.children.slice();

  harness.pollCatalog();
  await settleAsyncWork();

  assert.equal(elementText(body), before);
  assert.deepEqual(body.children, beforeChildren, 'failed GET must not replace the last good rows');
  assert.equal(
	harness.selectors['[data-model-catalog-message]'].textContent,
	'Model catalog status could not be updated. Showing the last known catalog.',
  );
  assert.equal(
	harness.selectors['[data-model-catalog-live]'].textContent,
	'Model catalog status could not be updated. Showing the last known catalog.',
	'catalog polling failure should be announced through the polite live region',
  );
  assert.doesNotMatch(harness.selectors['[data-model-catalog-message]'].textContent, /private server detail/);
});

test('model catalog initialization does not duplicate its fetch, listener, or timer', async () => {
  const harness = createHarness(
	[
	  snapshot({ main: 'Gateway', kiro: 'Kiro' }),
	  snapshot({ main: 'Gateway', kiro: 'Kiro' }),
	],
	{
	  modelCatalog: true,
	  catalogResponses: [modelCatalog([catalogModel('auto', 'Automatic')])],
	  refreshResponses: [{ body: { outcome: 'unchanged', message: 'Model catalog refresh completed.' } }],
	},
  );

  harness.start();
  await settleAsyncWork();
  harness.start();
  await settleAsyncWork();

  assert.equal(
	harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog').length,
	1,
	'repeated initialization must not issue another initial catalog GET',
  );
  assert.equal(
	harness.intervals.filter((entry) => entry.callback.name === 'fetchModelCatalog').length,
	1,
	'repeated initialization must not register another catalog interval',
  );
  harness.selectors['[data-model-catalog-refresh]'].dispatchEvent({ type: 'click' });
  await settleAsyncWork();
  assert.equal(
	harness.fetchCalls.filter((call) => call.url === '/admin/api/model-catalog/refresh').length,
	1,
	'repeated initialization must retain one refresh listener',
  );
});
