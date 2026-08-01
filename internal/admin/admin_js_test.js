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

function createHarness(responses) {
  const sourceSelect = new Element('select');
  const logStatus = new Element('span');
  const selectors = {
    '[data-log-source]': sourceSelect,
    '[data-log-status]': logStatus,
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
