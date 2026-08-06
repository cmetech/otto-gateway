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

function createHarness(responses) {
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
    fetch() {
      const body = responses.shift();
      assert.ok(body, 'unexpected snapshot fetch');
      return Promise.resolve({ ok: true, json: () => Promise.resolve(body) });
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
