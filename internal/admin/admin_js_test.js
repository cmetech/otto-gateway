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

function elementText(element) {
  return [element.textContent, ...element.children.map(elementText)].join(' ');
}

function createHarness(responses) {
  const sourceSelect = new Element('select');
  const logStatus = new Element('span');
  const slotGrid = new Element('div');
  const slotGridEmpty = new Element('p');
  const selectors = {
    '[data-log-source]': sourceSelect,
    '[data-log-status]': logStatus,
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
  const eventSources = [];

  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.closed = false;
      eventSources.push(this);
    }

    addEventListener() {}

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
    clearTimeout() {},
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
    setTimeout() {
      return 1;
    },
    window: { GW_ADMIN_CONFIG: { pollMs: 4321 } },
  };

  const script = fs.readFileSync(path.join(__dirname, 'static', 'js', 'admin.js'), 'utf8');
  vm.runInNewContext(script, context, { filename: 'admin.js' });

  return {
    eventSources,
    sourceSelect,
	selectors,
    start() {
      documentListeners.DOMContentLoaded();
    },
    poll() {
      intervals.find((entry) => entry.delay === 4321).callback();
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
