// Gateway Admin UI — Client Script
// Phase 6.1 Plan 01: Summary-strip polling + live-dot logic.
// Plan 03 will amend this file to add the SSE log-tail EventSource.
//
// Constraints:
// - Vanilla ES2018+ IIFE (no modules, no transpile, no external deps).
// - Config island: window.GW_ADMIN_CONFIG = { version, commit, pollMs }.
// - DOM queries use document.querySelector('[data-X]') attribute hooks.

(function () {
  'use strict';

  var cfg = window.GW_ADMIN_CONFIG || {};
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
  // Perf: gateway + per-worker CPU/RSS tiles and rolling-window sparklines.
  //
  // The snapshot carries CPU as a cumulative counter (process_cpu_seconds /
  // slot cpu_seconds), so a live percent is derived client-side by diffing two
  // successive polls: Δcpu / Δwall × 100. Because the poll cadence is a fixed
  // 30s (D-06), the first CPU% for any series appears on the SECOND poll and the
  // sparkline is a ~15-minute rolling window (PERF_MAX points). RSS is
  // instantaneous and shows on the first poll. History lives here in JS (the
  // server keeps none for the UI — that is Prometheus/Grafana's job), so the
  // DOM can be freely rebuilt each poll from these buffers.
  // ---------------------------------------------------------------------------

  var SVG_NS = 'http://www.w3.org/2000/svg';
  var PERF_MAX = 30;          // rolling-window length (points)
  var perfPrev = {};          // key -> { t: wallMs, cpu: cumulativeSeconds }
  var perfHist = {};          // key -> [ { cpuPct: number|null, rss: number } ]
  var GW_KEY = '__gateway__';

  function slotKey(label) { return 'slot:' + label; }

  // pushSample folds one reading into a series' ring buffer, deriving cpuPct
  // from the previous sample. A counter that went backwards (a respawned worker
  // with a fresh pid resets cumulative CPU) is treated as 0% rather than a
  // negative spike.
  function pushSample(key, cpuSeconds, rssBytes, wallMs) {
    var prev = perfPrev[key];
    var pct = null;
    if (prev) {
      var dt = (wallMs - prev.t) / 1000;
      if (dt > 0) {
        pct = cpuSeconds >= prev.cpu ? ((cpuSeconds - prev.cpu) / dt) * 100 : 0;
      }
    }
    perfPrev[key] = { t: wallMs, cpu: cpuSeconds };
    var hist = perfHist[key] || (perfHist[key] = []);
    hist.push({ cpuPct: pct === null ? null : Math.max(0, pct), rss: rssBytes });
    if (hist.length > PERF_MAX) hist.shift();
  }

  // ingestPerf updates every series' buffer from a fresh snapshot. Unreadable
  // series (stat_ok false — a dead slot, or the darwin dev box) are simply not
  // sampled; their tiles render "n/a" and any existing sparkline freezes.
  function ingestPerf(snap) {
    var wallMs = Date.parse(snap.generated_at) || Date.now();
    if (snap.process_stat_ok) {
      pushSample(GW_KEY, snap.process_cpu_seconds || 0, snap.process_rss_bytes || 0, wallMs);
    }
    var slots = (snap.pool && snap.pool.slots) || [];
    slots.forEach(function (s) {
      if (s.stat_ok) pushSample(slotKey(s.label), s.cpu_seconds || 0, s.rss_bytes || 0, wallMs);
    });
  }

  function latestCPUPct(key) {
    var hist = perfHist[key];
    if (!hist || !hist.length) return null;
    return hist[hist.length - 1].cpuPct;
  }

  // sparkValues returns the drawable series for a field: cpu% (null → 0 so the
  // pre-first-derivative gap draws flat) or rss bytes.
  function sparkValues(key, field) {
    var hist = perfHist[key] || [];
    return hist.map(function (h) {
      return field === 'cpu' ? (h.cpuPct === null ? 0 : h.cpuPct) : h.rss;
    });
  }

  function fmtPct(pct) {
    if (pct === null || pct === undefined) return '…';
    return (pct >= 10 ? Math.round(pct) : pct.toFixed(1)) + '%';
  }

  function formatBytes(n) {
    if (n === null || n === undefined) return '—';
    var units = ['B', 'KB', 'MB', 'GB', 'TB'];
    var v = n, i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v : v.toFixed(1)) + ' ' + units[i];
  }

  // buildSparkline draws a normalized polyline into a fixed 80×20 viewBox. The
  // series is scaled to its own window max so a mostly-flat line still shows
  // shape. Fewer than two points → an empty (blank) SVG placeholder.
  function buildSparkline(values, modifier) {
    var W = 80, H = 20;
    var svg = document.createElementNS(SVG_NS, 'svg');
    svg.setAttribute('class', 'gw-spark' + (modifier ? ' ' + modifier : ''));
    svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
    svg.setAttribute('preserveAspectRatio', 'none');
    svg.setAttribute('aria-hidden', 'true');
    if (!values || values.length < 2) return svg;
    var max = 0;
    for (var k = 0; k < values.length; k++) { if (values[k] > max) max = values[k]; }
    if (max <= 0) max = 1;
    var n = values.length;
    var pts = values.map(function (v, i) {
      var x = (i / (n - 1)) * W;
      var y = H - (v / max) * (H - 2) - 1; // 1px top/bottom padding
      return x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
    var poly = document.createElementNS(SVG_NS, 'polyline');
    poly.setAttribute('class', 'gw-spark-line');
    poly.setAttribute('points', pts);
    poly.setAttribute('fill', 'none');
    svg.append(poly);
    return svg;
  }

  // perfStat builds one "LABEL value + sparkline" cell for a slot card.
  function perfStat(label, valueText, values, modifier) {
    var cell = document.createElement('div');
    cell.className = 'gw-perf-stat';
    var lab = document.createElement('span');
    lab.className = 'gw-perf-label';
    lab.textContent = label;
    var val = document.createElement('span');
    val.className = 'gw-perf-value';
    val.textContent = valueText;
    cell.append(lab, val, buildSparkline(values, modifier));
    return cell;
  }

  // buildSlotPerf is the per-worker CPU/Mem block appended to each slot card.
  function buildSlotPerf(slot) {
    var el = document.createElement('div');
    el.className = 'gw-slot-perf';
    if (slot.stat_ok) {
      var key = slotKey(slot.label);
      el.append(perfStat('CPU', fmtPct(latestCPUPct(key)), sparkValues(key, 'cpu'), 'is-cpu'));
      el.append(perfStat('Mem', formatBytes(slot.rss_bytes), sparkValues(key, 'rss'), 'is-mem'));
    } else {
      var na = document.createElement('span');
      na.className = 'gw-perf-na';
      na.textContent = 'perf n/a';
      el.append(na);
    }
    return el;
  }

  // renderGatewayPerf updates the two summary-strip perf tiles + sparklines.
  function renderGatewayPerf(snap) {
    var cpuEl = qs('data-gw-cpu');
    var memEl = qs('data-gw-mem');
    if (snap.process_stat_ok) {
      if (cpuEl) cpuEl.textContent = fmtPct(latestCPUPct(GW_KEY));
      if (memEl) memEl.textContent = formatBytes(snap.process_rss_bytes);
    } else {
      if (cpuEl) cpuEl.textContent = 'n/a';
      if (memEl) memEl.textContent = 'n/a';
    }
    replaceSpark('data-gw-cpu-spark', sparkValues(GW_KEY, 'cpu'), 'is-cpu');
    replaceSpark('data-gw-mem-spark', sparkValues(GW_KEY, 'rss'), 'is-mem');
  }

  function replaceSpark(attr, values, modifier) {
    var host = qs(attr);
    if (host) host.replaceChildren(buildSparkline(values, modifier));
  }

  // ---------------------------------------------------------------------------
  // renderSlots: DOM-patch the pool-slot grid from snapshot pool.slots[]
  // ---------------------------------------------------------------------------

  function buildSlotLabel(slot) {
    var el = document.createElement('div');
    el.className = 'gw-slot-label';
    el.textContent = slot.label || ('Slot ' + slot.id);
    return el;
  }

  // poolFailed distinguishes a genuine current fault (red "Failed") from a
  // transient recycle (yellow "Recovering…") for a not-alive slot. It is
  // derived once per snapshot from pool-level signals (status "down" OR
  // spawn_failing) and threaded down to the per-slot renderers.
  function buildSlotBadges(slot, poolFailed) {
    var el = document.createElement('div');
    el.className = 'gw-slot-badges';
    if (!slot.alive) {
      var notAlive = document.createElement('span');
      if (poolFailed) {
        notAlive.className = 'gw-badge is-dead';
        notAlive.textContent = 'FAILED';
      } else {
        notAlive.className = 'gw-badge is-recovering';
        notAlive.textContent = 'RECOVERING';
      }
      el.append(notAlive);
    } else {
      var alive = document.createElement('span');
      alive.className = 'gw-badge is-alive';
      alive.textContent = 'ALIVE';
      el.append(alive);
      if (slot.current_session_id) {
        var busy = document.createElement('span');
        busy.className = 'gw-badge is-busy';
        busy.textContent = 'BUSY';
        el.append(busy);
      }
    }
    return el;
  }

  function buildSlotMeta(slot, poolFailed) {
    var el = document.createElement('div');
    el.className = 'gw-slot-meta';
    if (!slot.alive) {
      if (poolFailed) {
        el.classList.add('is-dead');
        el.textContent = 'Failed — check logs';
      } else {
        el.classList.add('is-recovering');
        el.textContent = 'Recovering…';
      }
    } else if (slot.current_session_id) {
      el.classList.add('is-busy');
      // "ACP session" disambiguates from the operator-level X-Session-Id
      // counter at the top: this id is kiro-cli's internal ACP session for
      // the subprocess on this slot, surfaces for every in-flight request
      // (stateless or stateful), and is NOT the same as the "Stateful
      // sessions" counter (which only tracks X-Session-Id registry entries).
      el.textContent = 'Busy — ACP session ' + shortId(slot.current_session_id);
    } else {
      el.textContent = 'Idle';
    }
    return el;
  }

  function buildSlotCard(slot, poolFailed) {
    var article = document.createElement('article');
    article.className = 'gw-slot-card';
    if (!slot.alive) {
      article.classList.add(poolFailed ? 'is-dead' : 'is-recovering');
    }
    article.append(buildSlotLabel(slot), buildSlotBadges(slot, poolFailed), buildSlotMeta(slot, poolFailed), buildSlotPerf(slot));
    return article;
  }

  function updateSlotCard(article, slot, poolFailed) {
    article.classList.remove('is-dead', 'is-recovering');
    if (!slot.alive) {
      article.classList.add(poolFailed ? 'is-dead' : 'is-recovering');
    }
    // Replace children in place.
    article.replaceChildren(buildSlotLabel(slot), buildSlotBadges(slot, poolFailed), buildSlotMeta(slot, poolFailed), buildSlotPerf(slot));
  }

  function renderSlots(slots, poolFailed) {
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
      grid.replaceChildren.apply(grid, slots.map(function (slot) {
        return buildSlotCard(slot, poolFailed);
      }));
    } else {
      // Same count — update in place.
      for (var i = 0; i < slots.length; i++) {
        updateSlotCard(grid.children[i], slots[i], poolFailed);
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
    span.className = 'gw-badge';
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

    // Gateway process CPU/RSS tiles + sparklines (buffers already updated by
    // ingestPerf earlier in the poll).
    renderGatewayPerf(snap);

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
        // Fold CPU/RSS readings into the rolling buffers BEFORE any render, so
        // the summary tiles and slot cards read a current window this poll.
        ingestPerf(snap);
        renderSummary(snap);
        // A not-alive slot is a genuine failure (red) only when the pool cannot
        // serve right now (status "down") or a current spawn failure is flagged;
        // otherwise it is a transient recycle rendered yellow "Recovering…".
        var poolFailed = snap.status === 'down' || !!(snap.pool && snap.pool.spawn_failing);
        renderSlots(snap.pool ? snap.pool.slots : [], poolFailed);
        renderSessions(snap.sessions || []);
        // Quick 260529-ll2 — populate the source dropdown from
        // snap.log_sources. populateLogSources no-ops when the list is
        // unchanged so operator selection survives across polls.
        populateLogSources(snap.log_sources || []);
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

  // Quick 260529-ll2 — multi-source Log Tail state.
  // currentLogSource is the name passed in /admin/logs/stream?source=.
  // logSourceLastJSON is a stringified-array cache used by
  // populateLogSources to no-op when the snapshot's log_sources field
  // hasn't changed (so operator selection survives across polls).
  // logEventSource holds the active EventSource so the source switcher
  // can close it before opening a new one.
  var currentLogSource = 'main';
  var logSourceLastJSON = '';
  var logEventSource = null;

  // Deduplication strategy (WR-03 + WR-04):
  // ---------------------------------------------------------------
  // The server replays up to RingBufferLines (500) lines as backfill
  // on every EventSource reconnect. Without dedup the operator sees
  // up to 480 duplicate lines per reconnect. The prior 20-entry ring
  // was both (a) far too small to catch a full 500-line backfill, and
  // (b) silently dropping legitimate duplicate log lines in the
  // steady state (heartbeats, pings, identical access logs).
  //
  // Fix: dedup is ENABLED ONLY during the backfill window after each
  // SSE open/reconnect. Once the window closes, every incoming line
  // is delivered as-is — steady-state duplicates pass through.
  //
  // The backfill set is sized for the full 500-line server window and
  // uses a Set + FIFO queue for O(1) lookup vs. the prior O(n) indexOf.
  // The window closes when LOG_BACKFILL_WINDOW_MS elapses since SSE open.
  // 2s is generous for a server-replay-then-live transition; if duplicates
  // somehow arrive after that bound the operator sees them, which is
  // strictly better than the prior 480-duplicates-per-reconnect failure.
  var LOG_BACKFILL_MAX = 500;          // match server RingBufferLines
  var LOG_BACKFILL_WINDOW_MS = 2000;   // tail bound on dedup activity
  var logBackfillActive = false;
  var logBackfillSet = null;           // Set of line strings seen in backfill
  var logBackfillQueue = null;         // FIFO array bounded by LOG_BACKFILL_MAX
  var logBackfillTimer = null;

  // logBackfillStart is called on SSE open/reconnect. It resets dedup
  // state and arms the time-bound window. Called from onSSEOpen.
  function logBackfillStart() {
    logBackfillActive = true;
    logBackfillSet = new Set();
    logBackfillQueue = [];
    if (logBackfillTimer) clearTimeout(logBackfillTimer);
    logBackfillTimer = setTimeout(logBackfillEnd, LOG_BACKFILL_WINDOW_MS);
  }

  // logBackfillEnd releases dedup state so steady-state lines pass
  // through (WR-04). Idempotent — safe to call when already inactive.
  function logBackfillEnd() {
    logBackfillActive = false;
    logBackfillSet = null;
    logBackfillQueue = null;
    if (logBackfillTimer) {
      clearTimeout(logBackfillTimer);
      logBackfillTimer = null;
    }
  }

  // logIsDupe returns true ONLY when we are inside the post-open
  // backfill window AND the line was already replayed. Outside the
  // window every line is unique by definition (WR-04: steady-state
  // duplicates pass through and reach the operator).
  function logIsDupe(line) {
    if (!logBackfillActive) return false;
    return logBackfillSet.has(line);
  }

  // logTrackLine records a line in the backfill set during the window.
  // The window is closed by the time-bound timer set in logBackfillStart;
  // steady-state lines (after the window closes) bypass this function
  // entirely via the !logBackfillActive guard.
  //
  // The Set is bounded by LOG_BACKFILL_MAX (500 — matches server
  // RingBufferLines). If the server's backfill ever grows past 500 in
  // a future change, the oldest entries are evicted FIFO and would
  // become eligible for steady-state replay, but the time window will
  // close before that matters in practice.
  function logTrackLine(line) {
    if (!logBackfillActive) return;
    if (logBackfillSet.has(line)) return;
    logBackfillSet.add(line);
    logBackfillQueue.push(line);
    while (logBackfillQueue.length > LOG_BACKFILL_MAX) {
      var evicted = logBackfillQueue.shift();
      logBackfillSet.delete(evicted);
    }
  }

  // Parse a slog log line into {level, time, msg, source, raw}. Supports both
  // slog text format ("time=… level=INFO msg=… logger=…") and JSON
  // ({"level":"INFO","time":"…","msg":"…","logger":"…"}). The `raw` field is
  // always the original line argument — load-bearing for grep re-derive
  // (filters match against raw, not concatenated cell text).
  //
  // source: prefer `logger`, fall back to `source` for both branches.
  //
  // 260530 enrichment: the JSON branch also extracts the common slog
  // access-log fields (method, path, status, duration_ms, request_id)
  // so appendLine can render `msg METHOD PATH STATUS DURATIONms` instead
  // of just `msg`. For an HTTP access line where msg="request", showing
  // only the msg field is useless because every line shares that value;
  // surfacing the structural fields makes the Log Tail actually scannable.
  function parseLogLine(line) {
    // Try JSON first (slog JSON handler output).
    if (line.charAt(0) === '{') {
      try {
        var obj = JSON.parse(line);
        var lv = obj.level;
        if (typeof lv === 'string') {
          return {
            level: lv.toLowerCase(),
            time: typeof obj.time === 'string' ? obj.time : '',
            msg: typeof obj.msg === 'string' ? obj.msg : '',
            source: typeof obj.logger === 'string'
              ? obj.logger
              : (typeof obj.source === 'string' ? obj.source : ''),
            method: typeof obj.method === 'string' ? obj.method : '',
            path: typeof obj.path === 'string' ? obj.path : '',
            status: (typeof obj.status === 'number') ? obj.status : null,
            duration_ms: (typeof obj.duration_ms === 'number') ? obj.duration_ms : null,
            request_id: typeof obj.request_id === 'string' ? obj.request_id : '',
            raw: line
          };
        }
      } catch (e) {
        // fall through to text parsing
      }
    }
    // slog text handler: "time=… level=INFO msg=…"
    var match = /\blevel=([A-Za-z]+)/.exec(line);
    if (match) {
      // Best-effort field extraction. The value group accepts either a
      // double-quoted span (which may contain spaces) or a bare \S+ token.
      function unquote(s) {
        if (typeof s !== 'string' || s.length < 2) return s || '';
        if (s.charAt(0) === '"' && s.charAt(s.length - 1) === '"') {
          return s.slice(1, -1);
        }
        return s;
      }
      function bareNum(s) {
        if (typeof s !== 'string') return null;
        var n = parseInt(s, 10);
        return isNaN(n) ? null : n;
      }
      var tm = /\btime=("[^"]*"|\S+)/.exec(line);
      var mg = /\bmsg=("[^"]*"|\S+)/.exec(line);
      var src = /\blogger=("[^"]*"|\S+)/.exec(line);
      if (!src) src = /\bsource=("[^"]*"|\S+)/.exec(line);
      var meth = /\bmethod=("[^"]*"|\S+)/.exec(line);
      var pth = /\bpath=("[^"]*"|\S+)/.exec(line);
      var stat = /\bstatus=(\S+)/.exec(line);
      var dur = /\bduration_ms=(\S+)/.exec(line);
      var rid = /\brequest_id=("[^"]*"|\S+)/.exec(line);
      return {
        level: match[1].toLowerCase(),
        time: tm ? unquote(tm[1]) : '',
        msg: mg ? unquote(mg[1]) : '',
        source: src ? unquote(src[1]) : '',
        method: meth ? unquote(meth[1]) : '',
        path: pth ? unquote(pth[1]) : '',
        status: stat ? bareNum(stat[1]) : null,
        duration_ms: dur ? bareNum(dur[1]) : null,
        request_id: rid ? unquote(rid[1]) : '',
        raw: line
      };
    }
    return {
      level: null, time: '', msg: '', source: '',
      method: '', path: '', status: null, duration_ms: null, request_id: '',
      raw: line
    };
  }

  // enrichedMessage returns the string that should appear in the Message
  // cell for a parsed line. When access-log fields are present (method +
  // path), the structural shape is appended so HTTP request lines are
  // self-describing. Otherwise the raw msg is returned unchanged.
  //
  // The order is deliberate: msg first (preserves grep-by-message muscle
  // memory), then method/path (the most useful identifiers), then status
  // (success/error at a glance), then duration. Examples:
  //   "request GET /health 200 0ms"
  //   "request POST /v1/messages 401 2ms"
  function enrichedMessage(parsed) {
    var out = parsed.msg || '';
    if (parsed.method && parsed.path) {
      out += ' ' + parsed.method + ' ' + parsed.path;
    }
    if (parsed.status !== null && parsed.status !== undefined) {
      out += ' ' + String(parsed.status);
    }
    if (parsed.duration_ms !== null && parsed.duration_ms !== undefined) {
      out += ' ' + String(parsed.duration_ms) + 'ms';
    }
    return out;
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

  // appendLine adds a log entry to the viewport as a 4-cell grid row
  // (Time | Level | Source | Message), or as a single full-width fallback
  // row when the line cannot be parsed at all (T-6.1-16: textContent only,
  // never raw-HTML APIs).
  //
  // WR-06: persist parsed.level (including the null case) via dataset.level
  // so the level-filter re-derive paths read the same source of truth as
  // the initial render. dataset.raw mirrors the original SSE line so the
  // grep filter matches what it was matching BEFORE the grid refactor
  // (not the concatenated cell text).
  function appendLine(line, parsed) {
    var vp = document.querySelector('[data-log-viewport]');
    if (!vp) return;

    // Remove "empty" placeholder on first line.
    var empty = document.querySelector('[data-log-empty]');
    if (empty) empty.hidden = true;

    var row;
    // Fallback: parser found nothing usable — render the raw line as one
    // full-width cell spanning all 4 columns so nothing is silently dropped.
    if (parsed.level === null && parsed.time === '' && parsed.msg === '') {
      row = document.createElement('div');
      row.className = 'gw-log-row-fallback';
      row.dataset.level = 'unknown';
      row.dataset.raw = parsed.raw;
      var fallbackCell = document.createElement('div');
      fallbackCell.className = 'gw-log-cell gw-log-cell-fallback';
      fallbackCell.textContent = parsed.raw;  // textContent only (T-6.1-16)
      row.appendChild(fallbackCell);
    } else {
      // Standard 4-cell grid row.
      row = document.createElement('div');
      row.className = 'gw-log-row';
      row.dataset.level = parsed.level || 'unknown';
      row.dataset.raw = parsed.raw;

      var timeCell = document.createElement('div');
      timeCell.className = 'gw-log-cell gw-log-cell-time';
      timeCell.textContent = parsed.time || '';

      var levelCell = document.createElement('div');
      levelCell.className = 'gw-log-cell gw-log-cell-level';
      var chip = document.createElement('span');
      var levelKey = parsed.level || 'unknown';
      chip.className = 'gw-log-level-chip is-' + levelKey;
      // Chip text: uppercase level name, or "?" for unknown.
      chip.textContent = parsed.level ? parsed.level.toUpperCase() : '?';
      levelCell.appendChild(chip);

      var sourceCell = document.createElement('div');
      sourceCell.className = 'gw-log-cell gw-log-cell-source';
      sourceCell.textContent = parsed.source || '';

      var msgCell = document.createElement('div');
      msgCell.className = 'gw-log-cell gw-log-cell-message';
      // 260530 enrichment: render `msg METHOD PATH STATUS DURATIONms` when
      // the access-log structural fields are present; otherwise just msg.
      // Fall back to the raw line when both msg and the enriched form would
      // be empty (line had a level but no msg or structured fields).
      var display = enrichedMessage(parsed);
      msgCell.textContent = display || parsed.raw;

      row.appendChild(timeCell);
      row.appendChild(levelCell);
      row.appendChild(sourceCell);
      row.appendChild(msgCell);
    }

    // Filter visibility — pass parsed.raw so the grep regex still matches
    // the original SSE line (not the concatenated cell text). With
    // display: contents on .gw-log-row{,-fallback}, setting style.display
    // = 'none' on the row hides all of its grid-cell children.
    if (!matchesFilters(parsed, parsed.raw)) {
      row.style.display = 'none';
    }
    vp.appendChild(row);

    // Trim DOM to last 1000 LOG rows (the header row + empty placeholder
    // must never be evicted). Count via the row classes so the header stays
    // pinned at the top of the viewport regardless of how many lines arrive.
    while (vp.querySelectorAll('.gw-log-row, .gw-log-row-fallback').length > 1000) {
      var victim = vp.querySelector('.gw-log-row, .gw-log-row-fallback');
      if (!victim) break;
      vp.removeChild(victim);
    }
  }

  // levelFromElement reads the persisted parsed level (WR-06) from an
  // already-rendered log-line element. Returns null when the parser could
  // not determine the level (dataset.level === 'unknown'), or the level
  // string otherwise. Both level-filter and grep-filter re-derive paths
  // call this so they agree with the initial-render decision in
  // matchesFilters.
  function levelFromElement(el) {
    var v = el.dataset.level;
    if (!v || v === 'unknown') return null;
    return v;
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
  // Activates the backfill-dedup window (WR-03 + WR-04) so duplicate
  // backfill lines from the upcoming replay are suppressed; the window
  // closes after LOG_BACKFILL_WINDOW_MS so steady-state duplicates pass
  // through unfiltered.
  function onSSEOpen() {
    var statusEl = document.querySelector('[data-log-status]');
    if (statusEl) statusEl.textContent = 'Connected — ' + currentLogSource;
    var dotEl = document.querySelector('[data-log-activity]');
    if (dotEl) dotEl.classList.remove('is-disconnected');
    logConsecutiveReconnects = 0;
    logBackfillStart();
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
  // WR-06: read the persisted level from dataset.level (set by appendLine)
  // so this path agrees with the initial-render filter decision even when
  // the parser could not determine the level (null).
  function initLevelFilter() {
    var sel = document.querySelector('[data-log-level]');
    if (!sel) return;
    sel.addEventListener('change', function () {
      logLevelFilter = sel.value;
      // Walk existing DOM nodes and set display accordingly. Read dataset.raw
      // (set by appendLine) instead of el.textContent — grep regex must see
      // the original SSE line, not the concatenated cell text.
      var vp = document.querySelector('[data-log-viewport]');
      if (!vp) return;
      var lines = vp.querySelectorAll('.gw-log-row, .gw-log-row-fallback');
      for (var i = 0; i < lines.length; i++) {
        var el = lines[i];
        var parsed = { level: levelFromElement(el) };
        var line = el.dataset.raw || '';
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
        // WR-06: read persisted level via levelFromElement instead of
        // inferring from classList (which loses the null case). Read
        // dataset.raw (set by appendLine) so the grep regex matches the
        // original SSE line, NOT the concatenated cell text — this is the
        // load-bearing correctness invariant for the grid refactor.
        var vp = document.querySelector('[data-log-viewport]');
        if (!vp) return;
        var lines = vp.querySelectorAll('.gw-log-row, .gw-log-row-fallback');
        for (var i = 0; i < lines.length; i++) {
          var el = lines[i];
          var parsed = { level: levelFromElement(el) };
          var line = el.dataset.raw || '';
          el.style.display = matchesFilters(parsed, line) ? '' : 'none';
        }
      }, 150);
    });
  }

  // Quick 260529-ll2 — populate the source dropdown from the
  // snapshot's log_sources array. No-op when the list is unchanged so
  // operator mid-session selection is preserved across snapshot polls.
  // T-6.1-16: builds <option> elements via document.createElement +
  // textContent/value assignment — NEVER innerHTML.
  function populateLogSources(sources) {
    var serialized = JSON.stringify(sources || []);
    if (serialized === logSourceLastJSON) {
      return;
    }
    logSourceLastJSON = serialized;
    var sel = document.querySelector('[data-log-source]');
    if (!sel) return;
    // Clear existing options without using innerHTML.
    while (sel.firstChild) sel.removeChild(sel.firstChild);
    if (!sources || sources.length === 0) {
      return;
    }
    for (var i = 0; i < sources.length; i++) {
      var opt = document.createElement('option');
      opt.value = sources[i];
      opt.textContent = sources[i];
      sel.appendChild(opt);
    }
    // Default selection: "main" if present, else first entry.
    var defaultSource = sources.indexOf('main') >= 0 ? 'main' : sources[0];
    // Honor an existing currentLogSource if it's still in the list
    // (operator selected a source that survived the snapshot refresh).
    if (sources.indexOf(currentLogSource) < 0) {
      currentLogSource = defaultSource;
    }
    sel.value = currentLogSource;
  }

  // Quick 260529-ll2 — remove all .gw-log-row / .gw-log-row-fallback
  // children from the viewport while PRESERVING the sticky header row and
  // re-showing the empty placeholder. Used when switching source — the
  // operator should see a clean viewport before the new SSE backfill
  // arrives. NodeList-to-array conversion avoids live-collection
  // mutation issues during removal.
  function clearLogViewport() {
    var vp = document.querySelector('[data-log-viewport]');
    if (!vp) return;
    var rows = Array.prototype.slice.call(
      vp.querySelectorAll('.gw-log-row, .gw-log-row-fallback'));
    for (var i = 0; i < rows.length; i++) {
      vp.removeChild(rows[i]);
    }
    var empty = document.querySelector('[data-log-empty]');
    if (empty) empty.hidden = false;
  }

  // Quick 260529-ll2 — open (or re-open) the SSE stream against the
  // current source. Idempotent: closing the existing logEventSource
  // before assignment is safe if the prior value was null.
  function openLogStream() {
    if (logEventSource) {
      try { logEventSource.close(); } catch (e) {
        // ignore — best-effort close
      }
      logEventSource = null;
    }
    logEventSource = new EventSource(
      '/admin/logs/stream?source=' + encodeURIComponent(currentLogSource));
    logEventSource.addEventListener('log', onLogEvent);
    logEventSource.addEventListener('ping', function () { /* keepalive */ });
    logEventSource.onopen = onSSEOpen;
    logEventSource.onerror = onSSEError;
  }

  // Quick 260529-ll2 — source dropdown change handler.
  // (1) update currentLogSource; (2) clear the viewport; (3) reset
  // backfill dedup + newest-buffer + badge; (4) update status text;
  // (5) open a new EventSource against the new source. All operations
  // are best-effort and idempotent — failing any one of them should
  // not leave the UI wedged.
  function initLogSourceSelector() {
    var sel = document.querySelector('[data-log-source]');
    if (!sel) return;
    sel.addEventListener('change', function () {
      currentLogSource = sel.value;
      clearLogViewport();
      logBackfillEnd();
      logNewestBuffer = [];
      updateNewestBadge();
      var statusEl = document.querySelector('[data-log-status]');
      if (statusEl) statusEl.textContent = 'Connecting to ' + currentLogSource + '…';
      openLogStream();
    });
  }

  // Initialise EventSource and wire up controls on DOMContentLoaded.
  // This block runs after the snapshot polling init so the setInterval
  // registrations are not disturbed.
  function initLogTail() {
    initPauseButton();
    initLevelFilter();
    initGrepFilter();
    initLogSourceSelector();
    openLogStream();
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
