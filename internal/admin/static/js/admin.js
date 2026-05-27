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
  // relativeTime: format a last_used ISO timestamp as "Xs/Xm/Xh/Xd ago"
  // ---------------------------------------------------------------------------

  function relativeTime(iso) {
    var t = new Date(iso);
    var s = Math.max(0, Math.floor((Date.now() - t.getTime()) / 1000));
    if (s < 60) return s + 's ago';
    if (s < 3600) return Math.floor(s / 60) + 'm ago';
    if (s < 86400) return Math.floor(s / 3600) + 'h ago';
    return Math.floor(s / 86400) + 'd ago';
  }

  // ---------------------------------------------------------------------------
  // renderSessions: DOM-patch the active-sessions table from snapshot sessions[]
  // ---------------------------------------------------------------------------

  function renderStatusBadge(sess) {
    var span = document.createElement('span');
    span.className = 'otto-badge';
    if (!sess.alive) {
      span.classList.add('is-dead');
      span.textContent = 'Dead';
    } else if (sess.busy) {
      span.classList.add('is-busy');
      span.textContent = 'Busy';
    } else {
      span.classList.add('is-alive');
      span.textContent = 'Idle';
    }
    return span;
  }

  function td(cls, text, title) {
    var cell = document.createElement('td');
    if (cls) cell.className = cls;
    if (typeof text === 'string' || typeof text === 'number') {
      cell.textContent = text;
    } else if (text && typeof text === 'object') {
      // DOM node
      cell.append(text);
    }
    if (title) cell.title = title;
    return cell;
  }

  function buildSessionRow(sess) {
    var tr = document.createElement('tr');
    tr.append(
      td('is-session-id', shortId(sess.id)),
      td('', renderStatusBadge(sess)),
      td('', relativeTime(sess.last_used), sess.last_used),
      td(sess.model ? '' : 'is-model-null', sess.model || '—')
    );
    return tr;
  }

  function renderSessions(sessions) {
    var tbody = document.querySelector('[data-sessions-tbody]');
    var empty = document.querySelector('[data-sessions-empty]');
    var table = document.querySelector('[data-sessions-table]');
    if (!tbody) return;

    if (!sessions || sessions.length === 0) {
      if (empty) empty.hidden = false;
      if (table) table.hidden = true;
      tbody.replaceChildren();
      return;
    }

    if (empty) empty.hidden = true;
    if (table) table.hidden = false;

    if (tbody.children.length !== sessions.length) {
      // Array length changed — full rebuild.
      tbody.replaceChildren.apply(tbody, sessions.map(buildSessionRow));
    } else {
      // Same count — update in place.
      for (var i = 0; i < sessions.length; i++) {
        var newRow = buildSessionRow(sessions[i]);
        tbody.replaceChild(newRow, tbody.children[i]);
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
        renderSessions(snap.sessions || []);
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

  // ---------------------------------------------------------------------------
  // Log Tail SSE — Phase 6.1 Plan 03
  // EventSource consumer with level filter, regex grep (150ms debounce),
  // pause/resume + "N new" badge, reconnect line deduplication.
  // All DOM mutations use textContent (not innerHTML) — T-6.1-16 XSS.
  // ---------------------------------------------------------------------------

  var logPaused = false;           // true when operator clicks Pause
  var logNewestBuffer = [];        // lines buffered while paused
  var logGrepRe = null;            // current compiled RegExp or null (no filter)
  var logLevelFilter = 'all';      // 'all' | 'debug' | 'info' | 'warn' | 'error'
  var logConsecutiveReconnects = 0;
  var logGrepDebounceTimer = null;

  // Deduplication ring: last 20 lines received. Prevents duplicate backfill
  // lines from appearing when EventSource reconnects (UI-SPEC §auto-reconnect).
  var logRecentLines = [];
  var LOG_DEDUP_MAX = 20;

  function logIsDupe(line) {
    return logRecentLines.indexOf(line) !== -1;
  }

  function logTrackLine(line) {
    logRecentLines.push(line);
    if (logRecentLines.length > LOG_DEDUP_MAX) {
      logRecentLines.shift();
    }
  }

  // Parse a slog log line to extract the level field.
  // Supports both slog text format ("level=INFO ...") and JSON
  // ({"level":"INFO",...}). Returns { level: 'info'|'debug'|'warn'|'error'|null }.
  function parseLogLine(line) {
    // Try JSON first (slog JSON handler output).
    if (line.charAt(0) === '{') {
      try {
        var obj = JSON.parse(line);
        var lv = obj.level;
        if (typeof lv === 'string') {
          return { level: lv.toLowerCase(), msg: line };
        }
      } catch (e) {
        // fall through to text parsing
      }
    }
    // slog text handler: "time=… level=INFO msg=…"
    var match = /\blevel=([A-Za-z]+)/.exec(line);
    if (match) {
      return { level: match[1].toLowerCase(), msg: line };
    }
    return { level: null, msg: line };
  }

  // matchesFilters returns true if the line passes both level and grep filters.
  function matchesFilters(parsed, line) {
    if (logLevelFilter !== 'all' && parsed.level !== logLevelFilter) {
      return false;
    }
    if (logGrepRe && !logGrepRe.test(line)) {
      return false;
    }
    return true;
  }

  // appendLine adds a log line to the viewport (T-6.1-16: textContent only).
  function appendLine(line, parsed) {
    var vp = document.querySelector('[data-log-viewport]');
    if (!vp) return;

    // Remove "empty" placeholder on first line.
    var empty = document.querySelector('[data-log-empty]');
    if (empty) empty.hidden = true;

    var div = document.createElement('div');
    div.className = 'otto-log-line';
    if (parsed.level && parsed.level !== 'info') {
      div.classList.add('is-' + parsed.level);
    }
    div.textContent = line;  // textContent — never innerHTML (T-6.1-16)
    if (!matchesFilters(parsed, line)) {
      div.style.display = 'none';
    }
    vp.appendChild(div);

    // Trim DOM to last 1000 nodes to prevent unbounded memory growth (T-6.1-17).
    while (vp.children.length > 1000) {
      vp.removeChild(vp.firstChild);
    }
  }

  // autoScroll scrolls the viewport to the bottom.
  function autoScroll() {
    var vp = document.querySelector('[data-log-viewport]');
    if (vp) vp.scrollTop = vp.scrollHeight;
  }

  // updateNewestBadge updates the "N new" badge visibility and count.
  function updateNewestBadge() {
    var badge = document.querySelector('[data-log-newest]');
    if (!badge) return;
    if (logNewestBuffer.length === 0) {
      badge.hidden = true;
      return;
    }
    badge.hidden = false;
    badge.textContent = logNewestBuffer.length + ' new';
  }

  // onLogEvent handles an SSE "log" event.
  function onLogEvent(ev) {
    var line = ev.data;

    // Deduplicate reconnect backfill.
    if (logIsDupe(line)) return;
    logTrackLine(line);

    var parsed = parseLogLine(line);
    if (logPaused) {
      logNewestBuffer.push({ line: line, parsed: parsed });
      updateNewestBadge();
      return;
    }
    appendLine(line, parsed);
    autoScroll();
  }

  // onSSEOpen handles EventSource open.
  function onSSEOpen() {
    var statusEl = document.querySelector('[data-log-status]');
    if (statusEl) statusEl.textContent = 'Connected';
    var dotEl = document.querySelector('[data-log-activity]');
    if (dotEl) dotEl.classList.remove('is-disconnected');
    logConsecutiveReconnects = 0;
  }

  // onSSEError handles EventSource error (browser auto-reconnects every ~3s).
  function onSSEError() {
    var statusEl = document.querySelector('[data-log-status]');
    if (statusEl) statusEl.textContent = 'Log stream disconnected — reconnecting…';
    var dotEl = document.querySelector('[data-log-activity]');
    if (dotEl) dotEl.classList.add('is-disconnected');
    logConsecutiveReconnects++;
  }

  // Pause button handler: toggles paused state.
  function initPauseButton() {
    var btn = document.querySelector('[data-log-pause]');
    if (!btn) return;
    btn.addEventListener('click', function () {
      logPaused = !logPaused;
      if (logPaused) {
        btn.textContent = 'Resume';
        btn.classList.add('is-paused');
      } else {
        btn.textContent = 'Pause';
        btn.classList.remove('is-paused');
        // Flush buffered lines.
        for (var i = 0; i < logNewestBuffer.length; i++) {
          var entry = logNewestBuffer[i];
          appendLine(entry.line, entry.parsed);
        }
        logNewestBuffer = [];
        updateNewestBadge();
        autoScroll();
      }
    });
  }

  // Level dropdown: re-evaluate all existing log lines on change.
  function initLevelFilter() {
    var sel = document.querySelector('[data-log-level]');
    if (!sel) return;
    sel.addEventListener('change', function () {
      logLevelFilter = sel.value;
      // Walk existing DOM nodes and set display accordingly.
      var vp = document.querySelector('[data-log-viewport]');
      if (!vp) return;
      var lines = vp.querySelectorAll('.otto-log-line');
      for (var i = 0; i < lines.length; i++) {
        var el = lines[i];
        // Re-derive level from class list.
        var level = null;
        if (el.classList.contains('is-debug')) level = 'debug';
        else if (el.classList.contains('is-warn')) level = 'warn';
        else if (el.classList.contains('is-error')) level = 'error';
        else level = 'info';
        var parsed = { level: level };
        var line = el.textContent;
        el.style.display = matchesFilters(parsed, line) ? '' : 'none';
      }
    });
  }

  // Grep input: 150ms debounce + invalid-regex hint.
  function initGrepFilter() {
    var input = document.querySelector('[data-log-grep]');
    var hint = document.querySelector('[data-log-grep-hint]');
    if (!input) return;
    input.addEventListener('input', function () {
      clearTimeout(logGrepDebounceTimer);
      logGrepDebounceTimer = setTimeout(function () {
        var val = input.value;
        if (val === '') {
          logGrepRe = null;
          if (hint) hint.hidden = true;
        } else {
          try {
            logGrepRe = new RegExp(val, 'i');
            if (hint) hint.hidden = true;
          } catch (e) {
            // Invalid regex: show hint, leave last valid filter active.
            if (hint) hint.hidden = false;
            return; // don't re-evaluate DOM
          }
        }
        // Re-evaluate existing DOM lines with the new filter.
        var vp = document.querySelector('[data-log-viewport]');
        if (!vp) return;
        var lines = vp.querySelectorAll('.otto-log-line');
        for (var i = 0; i < lines.length; i++) {
          var el = lines[i];
          var level = null;
          if (el.classList.contains('is-debug')) level = 'debug';
          else if (el.classList.contains('is-warn')) level = 'warn';
          else if (el.classList.contains('is-error')) level = 'error';
          else level = 'info';
          var parsed = { level: level };
          var line = el.textContent;
          el.style.display = matchesFilters(parsed, line) ? '' : 'none';
        }
      }, 150);
    });
  }

  // Initialise EventSource and wire up controls on DOMContentLoaded.
  // This block runs after the snapshot polling init so the setInterval
  // registrations are not disturbed.
  function initLogTail() {
    initPauseButton();
    initLevelFilter();
    initGrepFilter();

    var es = new EventSource('/admin/logs/stream');
    es.addEventListener('log', onLogEvent);
    es.addEventListener('ping', function () { /* keepalive — no-op */ });
    es.onopen = onSSEOpen;
    es.onerror = onSSEError;
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

    // Initialise SSE log tail (Plan 03).
    initLogTail();
  });

})();
