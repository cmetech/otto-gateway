// OTTO Gateway Admin UI — Client Script
// Phase 6.1 Plan 01: Summary-strip polling + live-dot logic.
// Plan 03 will amend this file to add the SSE log-tail EventSource.
//
// Constraints:
// - Vanilla ES2018+ IIFE (no modules, no transpile, no external deps).
// - Config island: window.OTTO_ADMIN_CONFIG = { version, commit, pollMs }.
// - DOM queries use document.querySelector('[data-X]') attribute hooks.

(function () {
  'use strict';

  var cfg = window.OTTO_ADMIN_CONFIG || {};
  var pollMs = cfg.pollMs || 30000;

  var lastSnapshotTime = null;   // Date of last successful snapshot
  var consecutiveFailures = 0;   // Failure counter for live-dot red logic

  // ---------------------------------------------------------------------------
  // DOM helpers
  // ---------------------------------------------------------------------------

  function qs(attr) {
    return document.querySelector('[' + attr + ']');
  }

  function setText(attr, value) {
    var el = qs(attr);
    if (el) el.textContent = value;
  }

  // ---------------------------------------------------------------------------
  // Status pill update
  // ---------------------------------------------------------------------------

  function updatePill(status) {
    var el = qs('data-pill');
    if (!el) return;
    // Remove all modifier classes first.
    el.classList.remove('is-healthy', 'is-degraded', 'is-down');
    if (status === 'ok') {
      el.textContent = 'HEALTHY';
      el.classList.add('is-healthy');
    } else if (status === 'degraded') {
      el.textContent = 'DEGRADED';
      el.classList.add('is-degraded');
    } else {
      el.textContent = 'DOWN';
      el.classList.add('is-down');
    }
  }

  // ---------------------------------------------------------------------------
  // Live dot update
  // ---------------------------------------------------------------------------

  function updateDot(cls) {
    var el = qs('data-live-dot');
    if (!el) return;
    el.classList.remove('is-yellow', 'is-orange', 'is-red');
    if (cls) el.classList.add(cls);
  }

  // ---------------------------------------------------------------------------
  // Uptime humanization
  // ---------------------------------------------------------------------------

  function humanizeUptime(seconds) {
    seconds = Math.floor(seconds);
    if (seconds < 60) return seconds + 's';
    var mins = Math.floor(seconds / 60);
    var secs = seconds % 60;
    if (mins < 60) return mins + 'm ' + secs + 's';
    var hrs = Math.floor(mins / 60);
    mins = mins % 60;
    if (hrs < 24) return hrs + 'h ' + mins + 'm';
    var days = Math.floor(hrs / 24);
    hrs = hrs % 24;
    return days + 'd ' + hrs + 'h';
  }

  // ---------------------------------------------------------------------------
  // shortId: truncate a long id to "XXXX…XXXX" for display density
  // ---------------------------------------------------------------------------

  function shortId(id) {
    return id && id.length > 10 ? id.slice(0, 4) + '…' + id.slice(-4) : (id || '—');
  }

  // ---------------------------------------------------------------------------
  // renderSlots: DOM-patch the pool-slot grid from snapshot pool.slots[]
  // ---------------------------------------------------------------------------

  function buildSlotLabel(slot) {
    var el = document.createElement('div');
    el.className = 'otto-slot-label';
    el.textContent = slot.label || ('Slot ' + slot.id);
    return el;
  }

  function buildSlotBadges(slot) {
    var el = document.createElement('div');
    el.className = 'otto-slot-badges';
    if (!slot.alive) {
      var dead = document.createElement('span');
      dead.className = 'otto-badge is-dead';
      dead.textContent = 'DEAD';
      el.append(dead);
    } else {
      var alive = document.createElement('span');
      alive.className = 'otto-badge is-alive';
      alive.textContent = 'ALIVE';
      el.append(alive);
      if (slot.current_session_id) {
        var busy = document.createElement('span');
        busy.className = 'otto-badge is-busy';
        busy.textContent = 'BUSY';
        el.append(busy);
      }
    }
    return el;
  }

  function buildSlotMeta(slot) {
    var el = document.createElement('div');
    el.className = 'otto-slot-meta';
    if (!slot.alive) {
      el.classList.add('is-dead');
      el.textContent = 'Dead — respawning…';
    } else if (slot.current_session_id) {
      el.classList.add('is-busy');
      el.textContent = 'Busy — session ' + shortId(slot.current_session_id);
    } else {
      el.textContent = 'Idle';
    }
    return el;
  }

  function buildSlotCard(slot) {
    var article = document.createElement('article');
    article.className = 'otto-slot-card';
    if (!slot.alive) {
      article.classList.add('is-dead');
    }
    article.append(buildSlotLabel(slot), buildSlotBadges(slot), buildSlotMeta(slot));
    return article;
  }

  function updateSlotCard(article, slot) {
    if (!slot.alive) {
      article.classList.add('is-dead');
    } else {
      article.classList.remove('is-dead');
    }
    // Replace children in place.
    article.replaceChildren(buildSlotLabel(slot), buildSlotBadges(slot), buildSlotMeta(slot));
  }

  function renderSlots(slots) {
    var grid = document.querySelector('[data-slot-grid]');
    var empty = document.querySelector('[data-slot-grid-empty]');
    if (!grid) return;

    if (!slots || slots.length === 0) {
      if (empty) empty.hidden = false;
      grid.replaceChildren();
      return;
    }

    if (empty) empty.hidden = true;

    if (grid.children.length !== slots.length) {
      // Array length changed — full rebuild.
      grid.replaceChildren.apply(grid, slots.map(buildSlotCard));
    } else {
      // Same count — update in place.
      for (var i = 0; i < slots.length; i++) {
        updateSlotCard(grid.children[i], slots[i]);
      }
    }
  }

  // ---------------------------------------------------------------------------
  // renderSummary: update DOM from snapshot data
  // ---------------------------------------------------------------------------

  function renderSummary(snap) {
    // Status pill.
    updatePill(snap.status);

    // Uptime.
    setText('data-uptime', humanizeUptime(snap.uptime_seconds || 0));

    // Pool summary: "<alive>/<size> alive · <busy> busy"
    var pool = snap.pool || {};
    setText('data-pool-summary',
      (pool.alive || 0) + '/' + (pool.size || 0) + ' alive · ' + (pool.busy || 0) + ' busy');

    // Sessions count.
    var sessions = snap.sessions || [];
    setText('data-sessions-count', sessions.length);

    // Live dot based on status.
    if (snap.status === 'ok') {
      updateDot('is-yellow'); // yellow pulse = healthy
    } else if (snap.status === 'degraded') {
      updateDot('is-orange');
    } else {
      updateDot('is-red');
    }

    // Store successful fetch time for the 1s ticker.
    lastSnapshotTime = new Date();
    consecutiveFailures = 0;
  }

  // ---------------------------------------------------------------------------
  // fetchSnapshot: poll /admin/api/snapshot
  // ---------------------------------------------------------------------------

  function fetchSnapshot() {
    fetch('/admin/api/snapshot', { cache: 'no-store' })
      .then(function (resp) {
        if (!resp.ok) {
          throw new Error('HTTP ' + resp.status);
        }
        return resp.json();
      })
      .then(function (snap) {
        renderSummary(snap);
        renderSlots(snap.pool ? snap.pool.slots : []);
      })
      .catch(function () {
        consecutiveFailures++;
        var now = new Date();
        var hh = now.getHours().toString().padStart(2, '0');
        var mm = now.getMinutes().toString().padStart(2, '0');
        var ss = now.getSeconds().toString().padStart(2, '0');
        setText('data-last-updated', 'Poll failed at ' + hh + ':' + mm + ':' + ss + ' — retrying');

        if (consecutiveFailures >= 3) {
          updateDot('is-red');
        }
      });
  }

  // ---------------------------------------------------------------------------
  // tick: update "Last updated Xs ago" every 1s independently of the poll
  // ---------------------------------------------------------------------------

  function tick() {
    if (!lastSnapshotTime) return;
    var elapsed = Math.floor((Date.now() - lastSnapshotTime.getTime()) / 1000);
    setText('data-last-updated', 'Last updated ' + elapsed + 's ago');
  }

  // ---------------------------------------------------------------------------
  // Initialisation on DOMContentLoaded
  // ---------------------------------------------------------------------------

  document.addEventListener('DOMContentLoaded', function () {
    // 1s ticker for "Xs ago" counter.
    setInterval(tick, 1000);

    // Immediate first fetch on page load.
    fetchSnapshot();

    // Repeating poll at the configured interval (30s default).
    setInterval(fetchSnapshot, pollMs);
  });

})();
