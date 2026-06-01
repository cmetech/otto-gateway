---
phase: quick-260601-aix
plan: 01
type: execute
wave: 1
depends_on: [quick-260601-98c, quick-260601-9je, quick-260601-a3z]
files_modified:
  - internal/admin/admin.go
  - cmd/otto-gateway/main.go
  - internal/admin/templates/docs.html.tmpl
  - internal/admin/static/css/admin.css
autonomous: true
requirements: []
tags: [admin-ui, templates, docs-page, env-reference]

must_haves:
  truths:
    - "GET /admin/docs returns 200 HTML with operator reference content (env vars table, files & paths, CLI flags, endpoints, basic usage, troubleshooting, footer)."
    - "The env vars table renders one row per documented cfg env var with Variable, Default, Description, and Current value columns."
    - "AUTH_TOKEN current value is rendered as '(set)' or '(unset)' — NEVER the plaintext token characters."
    - "At least one numeric current value from a real env var is visible (e.g. default pool size '4' or default addr '127.0.0.1:18080' or '30s' for stream-idle)."
    - "Docs tab in header nav is marked active (otto-tab is-active + aria-current=page) on GET /admin/docs."
    - "Deps struct gains two new fields: ChatTraceFile (string) and ChatTraceMaxAgeDays (int); cmd/otto-gateway/main.go wires both from cfg."
    - "go build ./... and go test ./internal/admin/... remain clean."
    - "Admin package import list unchanged outside the allowed set (stdlib + chi + internal/version) — TRST-04 preserved."
  artifacts:
    - path: internal/admin/admin.go
      provides: "Deps + 2 new fields (ChatTraceFile, ChatTraceMaxAgeDays); docsData view-model + docsHandler builds env-var table + CLI-flag table from in-handler seed lists and renders via WR-05 buffer-then-write."
      contains: "docsData"
    - path: cmd/otto-gateway/main.go
      provides: "admin.Handler(admin.Deps{...}) call wires ChatTraceFile + ChatTraceMaxAgeDays from cfg."
      contains: "ChatTraceFile:"
    - path: internal/admin/templates/docs.html.tmpl
      provides: "Operator-reference page — intro, env vars table inside .otto-docs-table-scroll, files & paths card, CLI flags / startup card, endpoints reference card, basic usage card, troubleshooting card, footer link row."
      contains: "Environment variables"
    - path: internal/admin/static/css/admin.css
      provides: ".otto-docs-table-scroll (scrollable table container) + .otto-code-block (monospace block) + .otto-docs-env-table optional column treatment, appended under a Quick 260601-aix banner."
      contains: "otto-docs-table-scroll"
  key_links:
    - from: cmd/otto-gateway/main.go
      to: internal/admin/admin.go
      via: "admin.Handler(admin.Deps{...}) call passes ChatTraceFile/ChatTraceMaxAgeDays from cfg.*"
      pattern: "ChatTraceFile:\\s+cfg\\.ChatTraceFile"
    - from: internal/admin/templates/docs.html.tmpl
      to: internal/admin/admin.go
      via: "template ranges over .EnvVars and .CliFlags slices built by docsHandler"
      pattern: "range \\.EnvVars|range \\.CliFlags"
    - from: internal/admin/admin.go
      to: internal/admin/templates/docs.html.tmpl
      via: "docsHandler buffer-then-write of docsTemplate (defined in assets.go) — WR-05 pattern"
      pattern: "docsTemplate\\.ExecuteTemplate"
---

<objective>
Step 4 of the admin UI redesign: replace the /admin/docs "Coming soon" placeholder with a self-contained operator reference page (env-var table with live current values, files & paths, CLI flag → env mapping, endpoints reference, basic admin usage, troubleshooting). Mirrors step 3's About-page handler pattern — same WR-05 buffer-then-write, same view-model-in-handler / template-stays-presentational discipline, same TRST-04 import boundary (admin package only imports stdlib + chi + internal/version).

Purpose: Give operators a single bookmarkable page that answers "what knobs are there, what are they set to right now, where do logs go, what URL paths does this binary expose, what does the admin UI mean" without grepping environment variables or reading source code. Closes the only remaining "Coming soon" stub from quick-260601-98c on the Docs route.

Output: Six-section operator reference page driven by docsData (env vars + CLI flags + path prefixes + file locations) populated from cfg via two new Deps fields plus the ten cfg fields already added by step 3. Two atomic commits split along the Go / UI boundary (mirrors step 3's split).
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@CLAUDE.md
@.planning/quick/260601-a3z-admin-ui-redesign-step-3-about-page-cont/260601-a3z-PLAN.md
@.planning/quick/260601-a3z-admin-ui-redesign-step-3-about-page-cont/260601-a3z-SUMMARY.md
@internal/admin/admin.go
@internal/admin/templates/base.html.tmpl
@internal/admin/templates/about.html.tmpl
@internal/admin/templates/docs.html.tmpl
@internal/admin/static/css/admin.css
@internal/admin/assets.go
@internal/config/config.go
@cmd/otto-gateway/main.go
</context>

<tasks>

<task type="auto" tdd="false">
  <name>Task 1: Extend admin.Deps (2 fields) + docsData + docsHandler + main.go wiring (Go side)</name>
  <files>internal/admin/admin.go, cmd/otto-gateway/main.go</files>
  <behavior>
    - After this task: admin.Deps gains exactly two new fields — ChatTraceFile (string) and ChatTraceMaxAgeDays (int) — appended to the existing 22-field struct (the original 10 + 12 from step 3).
    - A new unexported package-level type docsData is declared, holding: TabActive, PageTitle, Version, Commit, EnvVars []envVarRow, CliFlags []cliFlagRow, ChatTraceEnabled bool, ChatTraceFile string, ChatTraceMaxAgeDays int, OllamaPathPrefix, OpenAIPathPrefix, AnthropicPathPrefix string.
    - Two new unexported package-level types: envVarRow{Name, Default, Description, CurrentValue string} and cliFlagRow{Flag, EnvMapping, Notes string}.
    - docsHandler replaces the existing placeholder body. It (a) builds an []envVarRow seed list inline (see env var seed list below — verbatim values), filling CurrentValue from h.deps.* using the AUTH_TOKEN safety rule (never plaintext); (b) builds a []cliFlagRow seed list inline (see CLI flag seed list below — verbatim); (c) constructs docsData; (d) renders via the SAME WR-05 buffer-then-write pattern as aboutHandler (use docsTemplate.ExecuteTemplate(&buf, "base", data) — docsTemplate is already declared in assets.go).
    - cmd/otto-gateway/main.go admin.Handler(admin.Deps{...}) call gains exactly two new field assignments: ChatTraceFile: cfg.ChatTraceFile, ChatTraceMaxAgeDays: cfg.ChatTraceMaxAgeDays (placed adjacent to the existing 12 step-3 fields, alphabetic or grouped — executor's call).
    - go build ./... passes.
    - go test ./internal/admin/... passes.
    - TRST-04 preserved: no new imports beyond what step 3 already added (stdlib fmt, runtime, strings are already imported; this task adds NO new imports). Note: do NOT import internal/config — the seed-list strings are hand-mirrored from config.go on purpose to keep the boundary clean.
    - No git stash. No git --no-verify. No attempts to fix pre-existing go vet errors in tail_test.go / tail_timberjack_test.go.
  </behavior>
  <action>
    Step 1 — Extend internal/admin/admin.go Deps struct:

    (a) Append these two fields to the Deps struct block AFTER the existing AnthropicPathPrefix field (last field added in step 3). Add a brief doc-comment above the pair: "// Chat-trace file location and retention surfaced on /admin/docs (quick 260601-aix, step 4 of admin UI redesign). Read-only snapshots of cfg.ChatTraceFile / cfg.ChatTraceMaxAgeDays."

        ChatTraceFile       string
        ChatTraceMaxAgeDays int

    Step 2 — Add three unexported package-level types (place them near the existing aboutData type, before docsHandler):

        // envVarRow is one row in the /admin/docs environment-variables
        // reference table (quick 260601-aix). CurrentValue is computed by
        // docsHandler at request time from the Deps snapshot (taken at
        // wire-up). AUTH_TOKEN row's CurrentValue is "(set)"/"(unset)" —
        // never the plaintext token characters.
        type envVarRow struct {
            Name         string
            Default      string
            Description  string
            CurrentValue string
        }

        // cliFlagRow is one row in the /admin/docs CLI-flag / env-var
        // mapping table (quick 260601-aix). The flag/env mapping is
        // hand-mirrored from internal/config/config.go LoadArgs() so the
        // admin package does NOT import internal/config (TRST-04 boundary).
        type cliFlagRow struct {
            Flag       string
            EnvMapping string
            Notes      string
        }

        // docsData is the render-time view-model for the /admin/docs page
        // (quick 260601-aix). The two table slices are populated in
        // docsHandler from in-handler seed lists; path prefixes and the
        // chat-trace block are copied from h.deps.*.
        type docsData struct {
            TabActive            string
            PageTitle            string
            Version              string
            Commit               string
            EnvVars              []envVarRow
            CliFlags             []cliFlagRow
            ChatTraceEnabled     bool
            ChatTraceFile        string
            ChatTraceMaxAgeDays  int
            OllamaPathPrefix     string
            OpenAIPathPrefix     string
            AnthropicPathPrefix  string
        }

    Step 3 — Rewrite docsHandler (replace the existing placeholder body in internal/admin/admin.go around lines 318-337). Preserve the function signature and the WR-05 buffer-then-write pattern. The function MUST:

    (a) Compute small helpers locally (do not extract to package-level helpers — keep churn minimal):
        - boolOnOff(b bool) string returning "on" / "off" — declare inline as a local closure OR a tiny package-level helper if preferred; executor's call.
        - truncate(s string, n int) string returning s if len(s)<=n else s[:n]+"…". For long current values like KIRO_ARGS or AnthropicPathPrefix combinations. Used in CurrentValue cells only.
        - authCurrent computed as "(set)" when h.deps.AuthEnabled, else "(unset)".
        - allowedIPsCurrent computed as "(set)" when h.deps.IPAllowlistEnabled, else "(unset)". (Treat ALLOWED_IPS like AUTH_TOKEN for display compactness — never list the actual CIDR list, just on/off. Note: the IP allowlist is NOT secret, but rendering CIDR lists in a docs table is noisy and breaks the at-a-glance "is this knob on?" affordance. Documented in the row's Description.)
        - chatTraceFileCurrent: when h.deps.ChatTrace is true, h.deps.ChatTraceFile; when false, "(disabled — CHAT_TRACE=false)".

    (b) Build envVars as an []envVarRow literal in the order shown in the env var seed list below. Use the EXACT Name / Default / Description strings — operators read these alongside docs/INSTALL.md so consistency matters.

    (c) Build cliFlags as a []cliFlagRow literal in the order shown in the CLI flag seed list below.

    (d) Construct docsData with TabActive="docs", PageTitle="Documentation", Version/Commit from h.deps, and the path prefixes + chat-trace fields from h.deps.

    (e) Render via WR-05 (mirror aboutHandler verbatim):

        var buf bytes.Buffer
        if err := docsTemplate.ExecuteTemplate(&buf, "base", data); err != nil {
            h.deps.Logger.Error("admin: docs render", "err", err)
            http.Error(w, "admin docs render failed", http.StatusInternalServerError)
            return
        }
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        w.WriteHeader(http.StatusOK)
        if _, err := w.Write(buf.Bytes()); err != nil {
            h.deps.Logger.Debug("admin: docs write", "err", err)
        }

    ---

    ## Env var seed list (verbatim — Name, Default, Description, then computed CurrentValue source)

    Use this exact order. Defaults are taken from internal/config/config.go Load() defaults; Descriptions are operator-facing one-liners. CurrentValue is the right-hand expression to fill in code.

    | # | Name | Default | Description | CurrentValue from |
    |---|------|---------|-------------|-------------------|
    |  1 | HTTP_ADDR | 127.0.0.1:18080 | HTTP listen address. Set to :18080 to bind all interfaces. | h.deps.HTTPAddr |
    |  2 | KIRO_CMD | kiro-cli | kiro-cli binary name or path resolved on PATH. Empty value puts the gateway in degraded mode. | h.deps.KiroCmd (empty → "(unset — degraded mode)") |
    |  3 | KIRO_ARGS | acp | Whitespace-split argv passed to KIRO_CMD. | strings.Join(h.deps.KiroArgs, " ") (empty → "(none)"), truncate(40) |
    |  4 | KIRO_CWD | (empty) | Working directory for the kiro-cli subprocess. Empty = inherit gateway cwd. | h.deps.KiroCwd (empty → "(empty)") |
    |  5 | POOL_SIZE | 4 | Number of warm kiro-cli subprocesses kept in the pool. | strconv.Itoa(h.deps.PoolSize) |
    |  6 | SESSION_TTL_MS | 1800000 (30m) | Idle stateful-session reap threshold. Accepts ms-integer (Node parity) or Go duration string. | h.deps.SessionTTL.String() |
    |  7 | STREAM_IDLE_TIMEOUT_SEC | 30 | Server-side idle-stream watchdog (0 disables, negative = boot error). | streamIdleCurrent: "disabled" when 0 else fmt.Sprintf("%ds", h.deps.StreamIdleTimeoutSec) |
    |  8 | AUTH_TOKEN | (unset) | Comma-split bearer-token allowlist. Empty = auth disabled (Node parity). Rendered as on/off — never the plaintext value. | authCurrent ("(set)"/"(unset)") |
    |  9 | ALLOWED_IPS | (unset) | Comma-split CIDR/IP allowlist. Empty = allow-all (Node parity). Rendered as on/off for compactness. | allowedIPsCurrent ("(set)"/"(unset)") |
    | 10 | AUTH_TRUST_XFF | false | Trust X-Forwarded-For in the IP allowlist check. Enable ONLY behind a known reverse proxy. | boolOnOff(false) — wired via existing cfg.AuthTrustXFF if exposed in Deps; current step 3 Deps does NOT carry AuthTrustXFF. Render literal "see startup log" if absent (acceptable short-term). |
    | 11 | DEBUG | false | Enables debug-level structured logging (slog JSON). | boolOnOff(h.deps.Debug) |
    | 12 | CHAT_TRACE | false | SENSITIVE — when true, writes raw user prompts to CHAT_TRACE_FILE. | boolOnOff(h.deps.ChatTrace) |
    | 13 | CHAT_TRACE_FILE | ./logs/otto-gateway-chat-trace.log (or sibling of LOG_FILE) | On-disk path of the chat-trace NDJSON log. Only opened when CHAT_TRACE=true. | chatTraceFileCurrent |
    | 14 | CHAT_TRACE_MAX_AGE_DAYS | 3 | timberjack MaxAge in days for chat-trace.log rotation pruning. | strconv.Itoa(h.deps.ChatTraceMaxAgeDays) |
    | 15 | OLLAMA_PATH_PREFIX | /api | Route prefix mounting the Ollama surface. | h.deps.OllamaPathPrefix |
    | 16 | OPENAI_PATH_PREFIX | /v1 | Route prefix mounting the OpenAI surface. | h.deps.OpenAIPathPrefix |
    | 17 | ANTHROPIC_PATH_PREFIX | /v1 | Route prefix mounting the Anthropic surface (shared with OpenAI; endpoint-level disambiguation). | h.deps.AnthropicPathPrefix |
    | 18 | ENABLED_SURFACES | ollama,anthropic,openai | Comma-split list of HTTP surfaces constructed at boot. | (NOT in Deps — render "(see startup log)") |
    | 19 | ENABLED_HOOKS | (empty = all) | Comma-split allowlist of plugin hook names. Empty = all hooks in the chain enabled (permissive default). | "(see startup log)" |
    | 20 | PII_REDACTION_ENABLED | false | Whether PIIRedactionHook does work when invoked. | "(see startup log)" |
    | 21 | PII_REDACTION_MODE | replace | One of replace / mask / hash / drop. mode=hash REQUIRES PII_HASH_KEY (boot error otherwise). | "(see startup log)" |
    | 22 | PII_ENABLED_ENTITIES | (empty = all six) | Comma-split allowlist: Email, IPv4, IPv6, SSN, CreditCard, USPhone. | "(see startup log)" |
    | 23 | PII_HASH_KEY | (unset) | HMAC-SHA256 key required when PII_REDACTION_MODE=hash. Rotating invalidates prior correlation tokens. | "(set)" or "(unset)" — but NOT in Deps; render "(see startup log)" |
    | 24 | SESSION_MAX | 32 | Cap on concurrent stateful sessions. Lazy-create over the cap returns 503. | "(see startup log)" — NOT in Deps |
    | 25 | SESSION_TICK_INTERVAL_MS | 60000 (60s) | Cadence of the registry reaper goroutine. Test injection seam. | "(see startup log)" |
    | 26 | PING_INTERVAL | 60s | kiro-cli heartbeat interval. Accepts ms-integer or Go duration string. | "(see startup log)" |
    | 27 | LOG_FILE | (unset) | When set, slog JSON also writes to this rotated file. Empty = stdout/stderr only. | "(see startup log)" |

    For rows whose CurrentValue is "(see startup log)" because the field is not in Deps, that string is the literal value to put in the cell. (Out-of-scope to extend Deps for those right now — operators can still grep startup logs; extending Deps further is a future step.)

    Add strconv to the import list if not already present (it likely is not — verify and add). strconv is stdlib, TRST-04 safe.

    ---

    ## CLI flag seed list (verbatim — Flag, EnvMapping, Notes)

    Use this exact order. Mirrored from internal/config/config.go LoadArgs() fs.* declarations.

    | # | Flag | EnvMapping | Notes |
    |---|------|------------|-------|
    |  1 | --http-addr | HTTP_ADDR | HTTP listen address. |
    |  2 | --kiro-cmd | KIRO_CMD | kiro-cli binary name or path. |
    |  3 | --kiro-args | KIRO_ARGS | Whitespace-split argv. |
    |  4 | --kiro-cwd | KIRO_CWD | Working directory for kiro-cli. |
    |  5 | --debug | DEBUG | Enable debug-level slog output. |
    |  6 | --ping-interval | PING_INTERVAL | Go duration string. |
    |  7 | --pool-size | POOL_SIZE | Warm subprocess count. |
    |  8 | --session-ttl | SESSION_TTL_MS | Go duration; env also accepts ms-integer. |
    |  9 | --session-max | SESSION_MAX | Concurrent stateful-session cap. |
    | 10 | --enabled-surfaces | ENABLED_SURFACES | Comma-split list. |
    | 11 | --ollama-path-prefix | OLLAMA_PATH_PREFIX | Ollama surface route prefix. |
    | 12 | --openai-path-prefix | OPENAI_PATH_PREFIX | OpenAI surface route prefix. |
    | 13 | --anthropic-path-prefix | ANTHROPIC_PATH_PREFIX | Anthropic surface route prefix. |
    | 14 | --allowed-ips | ALLOWED_IPS | Comma-split CIDR/IP allowlist. |
    | 15 | --auth-trust-xff | AUTH_TRUST_XFF | Trust X-Forwarded-For in allowlist check. |
    | 16 | --version | (n/a) | Print version and exit. |
    | 17 | (env-only — no flag) | AUTH_TOKEN | Secret; intentionally env-only (never argv). |

    Step 4 — Wire cmd/otto-gateway/main.go:

    Locate the existing admin.Handler(admin.Deps{...}) call (lines 578-606 in the current source). Add the two new field assignments inside the Deps literal, adjacent to ChatTrace: cfg.ChatTrace (line 588). Suggested placement immediately after ChatTrace:

        ChatTrace:           cfg.ChatTrace,
        ChatTraceFile:       cfg.ChatTraceFile,
        ChatTraceMaxAgeDays: cfg.ChatTraceMaxAgeDays,

    (Field names match — cfg.ChatTraceFile and cfg.ChatTraceMaxAgeDays are confirmed to exist in internal/config/config.go Config struct at lines 176 and 183.)

    Step 5 — Commit as a single atomic commit on the Go-side change with subject:

        refactor(quick-260601-aix): extend admin.Deps with chat-trace surfacing + docsData (step 4 part 1)

    No git stash. No --no-verify. NO attempts to fix pre-existing tail_test.go / tail_timberjack_test.go go vet errors.

    Use absolute paths rooted at /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/ for ALL Read/Edit calls. Do NOT cd out of the worktree mid-task. (Step 3 had a cwd-drift incident — edits briefly landed in the main repo.)
  </action>
  <verify>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go build ./...</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go test ./internal/admin/... -count=1</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -c 'ChatTraceFile\s*string' internal/admin/admin.go | awk '$1 >= 1 { exit 0 } { exit 1 }'</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -c 'ChatTraceMaxAgeDays\s*int' internal/admin/admin.go | awk '$1 >= 1 { exit 0 } { exit 1 }'</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E 'ChatTraceFile:\s+cfg\.ChatTraceFile' cmd/otto-gateway/main.go</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E 'ChatTraceMaxAgeDays:\s+cfg\.ChatTraceMaxAgeDays' cmd/otto-gateway/main.go</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E 'type docsData struct' internal/admin/admin.go</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E 'type envVarRow struct' internal/admin/admin.go</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E 'type cliFlagRow struct' internal/admin/admin.go</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E 'docsTemplate\.ExecuteTemplate\(&buf, "base", data\)' internal/admin/admin.go</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -v '^//' internal/admin/admin.go | grep -E '^\s*import\b|"otto-gateway/internal/(pool|session|server|engine|adapter|config|plugin|canonical)"' | grep -E '"otto-gateway/internal/(pool|session|server|engine|adapter|config|plugin|canonical)"' && exit 1 || exit 0</automated>
  </verify>
  <done>
    - Deps has the 2 new fields with correct types (ChatTraceFile string, ChatTraceMaxAgeDays int).
    - docsData, envVarRow, cliFlagRow types declared.
    - docsHandler builds envVars (27 rows, AUTH_TOKEN row uses "(set)"/"(unset)") and cliFlags (17 rows) seed slices and renders docsTemplate via WR-05 buffer-then-write.
    - cmd/otto-gateway/main.go admin.Handler call wires ChatTraceFile and ChatTraceMaxAgeDays from cfg.*.
    - go build + go test (./internal/admin/...) clean.
    - TRST-04 preserved (no new imports outside stdlib + chi + internal/version; strconv is stdlib).
    - Single atomic commit landed.
  </done>
</task>

<task type="auto" tdd="false">
  <name>Task 2: Docs page template + CSS (UI side)</name>
  <files>internal/admin/templates/docs.html.tmpl, internal/admin/static/css/admin.css</files>
  <behavior>
    - GET /admin/docs renders an operator reference page with sections: intro paragraph; Environment variables (scrollable table); Files & paths (card); CLI flags / startup (card with code block + flag mapping table); Endpoints reference (card with Admin / Public API / Internal subsections); Basic admin usage (card); Troubleshooting (card); footer link row matching About page.
    - AUTH_TOKEN row Current value cell contains "(set)" or "(unset)" — NEVER the plaintext token.
    - Pool size default 4 visible in the Default column (text "4"); HTTP_ADDR default 127.0.0.1:18080 visible.
    - Docs tab in header nav rendered with otto-tab is-active class (driven by TabActive="docs" set in handler).
    - go build ./... passes (template parses at startup via embed.FS).
    - go test ./internal/admin/... passes.
  </behavior>
  <action>
    Step 1 — Replace internal/admin/templates/docs.html.tmpl entirely. Discard the current "Coming soon" placeholder. Write a new template that {{define "content"}} ... {{end}} extending base, with these sections IN THIS ORDER inside a <main class="otto-page">. Use the existing .otto-card class for each section card.

    Section 1 — INTRO (no card, just a leading paragraph or wrapped in a single .otto-card.otto-docs-intro):

        <section class="otto-card otto-docs-intro">
          <h1 class="otto-h2">Documentation</h1>
          <p>Operator reference for the OTTO Gateway binary. For live status see <a href="/admin/">Dashboard</a>; for build metadata see <a href="/admin/about">About</a>. This page is a server-rendered snapshot of the running configuration — it does NOT auto-refresh.</p>
        </section>

    Section 2 — ENVIRONMENT VARIABLES (scrollable table):

        <section class="otto-card">
          <h2 class="otto-h2">Environment variables</h2>
          <p>Current values reflect the configuration in effect at server start. Sensitive values (bearer token, allowlist) are summarised as <code>(set)</code> / <code>(unset)</code>. Rows whose current value is <code>(see startup log)</code> are not yet surfaced in the admin runtime snapshot.</p>
          <div class="otto-docs-table-scroll">
            <table class="otto-table otto-docs-env-table">
              <thead>
                <tr><th scope="col">Variable</th><th scope="col">Default</th><th scope="col">Description</th><th scope="col">Current value</th></tr>
              </thead>
              <tbody>
                {{range .EnvVars}}
                <tr>
                  <td><code>{{.Name}}</code></td>
                  <td>{{.Default}}</td>
                  <td>{{.Description}}</td>
                  <td class="current"><code>{{.CurrentValue}}</code></td>
                </tr>
                {{end}}
              </tbody>
            </table>
          </div>
        </section>

    Section 3 — FILES & PATHS:

        <section class="otto-card">
          <h2 class="otto-h2">Files &amp; paths</h2>
          <dl class="otto-about-dl">
            <dt>Chat trace file</dt><dd><code>{{.ChatTraceFile}}</code>{{if not .ChatTraceEnabled}} <span class="otto-badge">disabled</span>{{end}}</dd>
            <dt>Chat trace retention</dt><dd>{{.ChatTraceMaxAgeDays}} days (timberjack MaxAge)</dd>
            <dt>Admin static assets</dt><dd>Embedded in the binary via <code>embed.FS</code> (no on-disk lookup at runtime).</dd>
            <dt>Wrapper env files</dt><dd><code>.otto-gw.env</code> (generated template; load-first) then <code>.otto-gw.overrides.env</code> (operator-owned secrets/customisations; load-second wins). See <code>scripts/otto-gw upgrade-env</code>.</dd>
            <dt>Log destination</dt><dd>Structured slog JSON to stdout/stderr by default. Set <code>LOG_FILE</code> to also write a rotated file.</dd>
          </dl>
        </section>

    Section 4 — CLI FLAGS / STARTUP:

        <section class="otto-card">
          <h2 class="otto-h2">CLI flags &amp; startup</h2>
          <p>Basic launch:</p>
          <pre class="otto-code-block">otto-gateway --http-addr 127.0.0.1:18080 --pool-size 4 --kiro-cmd kiro-cli</pre>
          <p>Wrapper (recommended — loads <code>.otto-gw.env</code> and applies <code>--trace</code> mode shortcuts):</p>
          <pre class="otto-code-block">./scripts/otto-gw run
./scripts/otto-gw run --trace   # DEBUG=true + CHAT_TRACE=true</pre>
          <p>Flag &rarr; environment variable mapping (CLI flag wins over env when both are passed):</p>
          <table class="otto-table otto-docs-flags-table">
            <thead>
              <tr><th scope="col">Flag</th><th scope="col">Env var</th><th scope="col">Notes</th></tr>
            </thead>
            <tbody>
              {{range .CliFlags}}
              <tr>
                <td><code>{{.Flag}}</code></td>
                <td><code>{{.EnvMapping}}</code></td>
                <td>{{.Notes}}</td>
              </tr>
              {{end}}
            </tbody>
          </table>
        </section>

    Section 5 — ENDPOINTS:

        <section class="otto-card">
          <h2 class="otto-h2">Endpoints reference</h2>
          <h3 class="otto-h3">Admin (auth-exempt)</h3>
          <ul class="otto-docs-endpoint-list">
            <li><code>GET /admin/</code> — Dashboard HTML</li>
            <li><code>GET /admin/about</code> — About page</li>
            <li><code>GET /admin/docs</code> — This page</li>
            <li><code>GET /admin/api/snapshot</code> — Unified pool + session JSON</li>
            <li><code>GET /admin/logs/stream?source=&lt;name&gt;</code> — SSE log tail</li>
            <li><code>GET /admin/static/*</code> — Embedded CSS / JS / icons</li>
          </ul>
          <h3 class="otto-h3">Public API surfaces</h3>
          <ul class="otto-docs-endpoint-list">
            <li>Ollama: prefix <code>{{.OllamaPathPrefix}}</code> &mdash; <code>/version</code>, <code>/tags</code>, <code>/chat</code>, <code>/generate</code></li>
            <li>OpenAI: prefix <code>{{.OpenAIPathPrefix}}</code> &mdash; <code>/chat/completions</code></li>
            <li>Anthropic: prefix <code>{{.AnthropicPathPrefix}}</code> &mdash; <code>/messages</code></li>
          </ul>
          <h3 class="otto-h3">Internal</h3>
          <ul class="otto-docs-endpoint-list">
            <li><code>GET /health</code> — D-12 locked health probe (do not consume from wrappers; use <code>/admin/api/snapshot</code> instead).</li>
          </ul>
        </section>

    Section 6 — BASIC ADMIN USAGE:

        <section class="otto-card">
          <h2 class="otto-h2">Basic admin usage</h2>
          <ul class="otto-docs-bullets">
            <li><strong>Status pill</strong> — green = healthy (pool ready + at least one slot idle), amber = degraded (KIRO_CMD missing or pool starving), red = down (no slots, no recovery in flight).</li>
            <li><strong>Pool slots</strong> — one card per warm kiro-cli subprocess; idle (yellow), busy (purple), or terminated (red) with last-error context.</li>
            <li><strong>Active sessions</strong> — stateful conversation sessions per the session registry; idle &gt; <code>SESSION_TTL_MS</code> are reaped on the next tick.</li>
            <li><strong>Log tail</strong> — pick a source from the dropdown, stream SSE-delivered lines into the table. Use the search box for client-side <code>dataset.raw</code> grep.</li>
          </ul>
        </section>

    Section 7 — TROUBLESHOOTING:

        <section class="otto-card">
          <h2 class="otto-h2">Troubleshooting</h2>
          <ul class="otto-docs-bullets">
            <li><strong>Status shows degraded</strong> — usually <code>KIRO_CMD</code> is unset or not on <code>$PATH</code>. Check the Upstream Worker card on <a href="/admin/about">About</a>.</li>
            <li><strong>Status shows down</strong> — all pool slots terminated. Inspect the Log tail filtered on <code>pool.acquire</code> / <code>pool.recover</code> for the underlying spawn or ACP error.</li>
            <li><strong>Requests hang for ~120s then time out</strong> — server-side stream-idle watchdog is disabled (<code>STREAM_IDLE_TIMEOUT_SEC=0</code>) and the upstream stalled silently. Re-enable the watchdog (default 30s) and retry.</li>
            <li><strong>"chat trace enabled" warning at startup</strong> — <code>CHAT_TRACE=true</code> writes raw prompts to disk at the path shown in <em>Files &amp; paths</em>. Turn off in production unless intentionally debugging.</li>
            <li><strong>Where do logs go?</strong> — structured slog JSON to stdout / stderr by default; set <code>LOG_FILE</code> for a rotated on-disk copy.</li>
          </ul>
        </section>

    Section 8 — FOOTER:

        <section class="otto-card otto-docs-footer">
          <p>Repository: <a href="https://github.com/cmetech/otto_app">github.com/cmetech/otto_app</a></p>
        </section>

    End the file with:

        {{end}}
        {{template "base" .}}

    (Match the existing about.html.tmpl structure.)

    Step 2 — Append to internal/admin/static/css/admin.css (do NOT touch existing rules; append under a banner-commented block at the end of the file). Reuse existing tokens (--otto-border, --otto-fg, --otto-fg-muted, --otto-card-hover, --otto-text-sm, --otto-space-*).

        /* ====== Quick 260601-aix — admin UI redesign step 4 (Docs page) ====== */

        .otto-docs-intro h1 {
          margin-top: 0;
        }

        .otto-docs-table-scroll {
          max-height: 600px;
          overflow: auto;
          border: 1px solid var(--otto-border);
          border-radius: 6px;
        }

        .otto-docs-env-table {
          width: 100%;
          border-collapse: collapse;
          font-size: var(--otto-text-sm);
        }

        .otto-docs-env-table th,
        .otto-docs-env-table td {
          padding: var(--otto-space-sm) var(--otto-space-md);
          border-bottom: 1px solid var(--otto-border);
          text-align: left;
          vertical-align: top;
        }

        .otto-docs-env-table thead th {
          position: sticky;
          top: 0;
          background: var(--otto-card-hover);
          z-index: 1;
        }

        .otto-docs-env-table td code {
          font-size: var(--otto-text-sm);
        }

        .otto-docs-env-table td.current code {
          color: var(--otto-fg);
        }

        .otto-docs-flags-table {
          width: 100%;
          border-collapse: collapse;
          font-size: var(--otto-text-sm);
          margin-top: var(--otto-space-base);
        }

        .otto-docs-flags-table th,
        .otto-docs-flags-table td {
          padding: var(--otto-space-sm) var(--otto-space-md);
          border-bottom: 1px solid var(--otto-border);
          text-align: left;
          vertical-align: top;
        }

        .otto-docs-flags-table thead th {
          background: var(--otto-card-hover);
        }

        .otto-code-block {
          font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
          background: var(--otto-card-hover);
          color: var(--otto-fg);
          padding: var(--otto-space-md);
          border-radius: 6px;
          font-size: var(--otto-text-sm);
          overflow-x: auto;
          white-space: pre;
          margin: var(--otto-space-sm) 0 var(--otto-space-base) 0;
        }

        .otto-h3 {
          font-size: var(--otto-text-base);
          font-weight: 600;
          letter-spacing: 0.02em;
          margin: var(--otto-space-base) 0 var(--otto-space-sm) 0;
          color: var(--otto-fg);
        }

        .otto-docs-endpoint-list,
        .otto-docs-bullets {
          margin: 0 0 var(--otto-space-sm) 0;
          padding-left: var(--otto-space-lg);
          font-size: var(--otto-text-sm);
        }

        .otto-docs-endpoint-list li,
        .otto-docs-bullets li {
          margin-bottom: var(--otto-space-xs);
          color: var(--otto-fg);
        }

        .otto-docs-bullets li strong {
          color: var(--otto-fg);
        }

        .otto-docs-footer {
          text-align: center;
          font-size: var(--otto-text-sm);
        }

        .otto-docs-footer a {
          color: var(--otto-link);
          text-decoration: none;
        }

        .otto-docs-footer a:hover {
          text-decoration: underline;
        }

    Step 3 — Commit as a single atomic commit on the UI-side change with subject:

        feat(quick-260601-aix): Docs page real content — env vars + flags + endpoints + troubleshooting (step 4 part 2)

    No git stash. No --no-verify. Stay in the worktree — use absolute paths rooted at /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/ for all Edit calls.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go build ./...</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go test ./internal/admin/... -count=1</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -v '^[[:space:]]*#' internal/admin/templates/docs.html.tmpl | grep -q 'Environment variables'</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E '\{\{range \.EnvVars\}\}' internal/admin/templates/docs.html.tmpl</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E '\{\{range \.CliFlags\}\}' internal/admin/templates/docs.html.tmpl</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep 'Files &amp; paths' internal/admin/templates/docs.html.tmpl</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep 'Troubleshooting' internal/admin/templates/docs.html.tmpl</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E 'github\.com/cmetech/otto_app' internal/admin/templates/docs.html.tmpl</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && ! grep 'Coming soon' internal/admin/templates/docs.html.tmpl</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E '\.otto-docs-table-scroll' internal/admin/static/css/admin.css</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && grep -E '\.otto-code-block' internal/admin/static/css/admin.css</automated>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && AUTH_TOKEN='sentinel-aix-do-not-render-12345' go run ./cmd/otto-gateway --version >/dev/null 2>&1 && echo build-ok || echo build-fail</automated>
    <human-check>Boot the gateway (./scripts/otto-gw run or go run ./cmd/otto-gateway), navigate to http://127.0.0.1:18080/admin/docs — confirm the page renders all 7 sections (intro, env vars table, files &amp; paths, CLI flags + code block, endpoints, basic usage, troubleshooting, footer); env-vars table scrolls inside the .otto-docs-table-scroll container with sticky header; Docs tab is highlighted as active in the header nav; AUTH_TOKEN row shows "(set)" or "(unset)" — NEVER any plaintext token characters.</human-check>
  </verify>
  <done>
    - docs.html.tmpl rewritten with 7 sections; ranges over .EnvVars + .CliFlags; uses existing .otto-card + new .otto-docs-* classes.
    - admin.css has .otto-docs-table-scroll, .otto-code-block, .otto-docs-env-table, .otto-docs-flags-table, .otto-h3, .otto-docs-endpoint-list, .otto-docs-bullets, .otto-docs-footer rules appended under a banner.
    - go build + go test (./internal/admin/...) clean.
    - "Coming soon" string removed from docs.html.tmpl.
    - Single atomic commit landed.
    - Human verification confirms visual correctness (7 sections render, table scrolls, Docs tab is active, AUTH_TOKEN is on/off only).
  </done>
</task>

</tasks>

<verification>
End-to-end after both tasks (run from the worktree):

1. `cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go build ./... && go test ./internal/admin/... -count=1` — both clean.

2. Boot the gateway with a sentinel AUTH_TOKEN to prove the safety rule end-to-end:

       cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway
       AUTH_TOKEN='sentinel-aix-do-not-render-12345' POOL_SIZE=4 HTTP_ADDR=127.0.0.1:18080 go run ./cmd/otto-gateway &
       SERVER_PID=$!
       sleep 2

3. Curl the docs page and grep the acceptance signals:

       BODY=$(curl -s http://127.0.0.1:18080/admin/docs)

       # Section presence
       echo "$BODY" | grep -q 'Environment variables' || { echo FAIL env-section; exit 1; }
       echo "$BODY" | grep -q 'HTTP_ADDR'              || { echo FAIL HTTP_ADDR row; exit 1; }
       echo "$BODY" | grep -q 'KIRO_CMD'               || { echo FAIL KIRO_CMD row; exit 1; }
       echo "$BODY" | grep -q 'POOL_SIZE'              || { echo FAIL POOL_SIZE row; exit 1; }
       echo "$BODY" | grep -q 'CHAT_TRACE'             || { echo FAIL CHAT_TRACE row; exit 1; }
       echo "$BODY" | grep -q 'Files &amp; paths'      || { echo FAIL Files section; exit 1; }
       echo "$BODY" | grep -q 'Endpoints reference'    || { echo FAIL Endpoints section; exit 1; }
       echo "$BODY" | grep -q 'Troubleshooting'        || { echo FAIL Troubleshooting section; exit 1; }
       echo "$BODY" | grep -q 'github.com/cmetech/otto_app' || { echo FAIL footer link; exit 1; }

       # AUTH_TOKEN safety — must show (set), must NOT show sentinel plaintext
       echo "$BODY" | grep -qE '\(set\)|\(unset\)' || { echo FAIL auth on/off; exit 1; }
       echo "$BODY" | grep -q 'sentinel-aix-do-not-render-12345' && { echo FAIL plaintext token leak; exit 1; }

       # Live current values — at least one numeric value visible
       echo "$BODY" | grep -qE '127\.0\.0\.1:18080|:18080|>4<|30s' || { echo FAIL no live current value; exit 1; }

       # Docs tab active
       echo "$BODY" | grep -qE 'otto-tab[^"]*is-active[^>]*aria-current[^>]*Docs|>Docs<' || { echo FAIL Docs tab not in nav; exit 1; }

       kill $SERVER_PID

4. Constraints recap (all must hold):
   - NO git stash anywhere in the task history.
   - NO new imports in internal/admin/admin.go outside stdlib + chi + internal/version. (strconv is stdlib — allowed.)
   - NO go vet fix attempts for the pre-existing tail_test.go / tail_timberjack_test.go errors.
   - AUTH_TOKEN plaintext never appears in the rendered HTML.
   - Two atomic commits preferred (Go side / UI side); single combined commit acceptable.
   - All Edit / Write paths used absolute paths rooted at /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/ — no cwd-drift incidents.
</verification>

<success_criteria>
1. GET /admin/docs returns 200 with HTML containing: "Environment variables", "HTTP_ADDR", "KIRO_CMD", "POOL_SIZE", "CHAT_TRACE", "Files &amp; paths", "Endpoints reference", "Troubleshooting".
2. AUTH_TOKEN row renders "(set)" or "(unset)" — never the plaintext token characters. Verified with sentinel env var at test time.
3. At least one numeric / literal current value from a real env var is visible (default ":18080", "4", or "30s").
4. Docs tab in header nav rendered with .otto-tab.is-active driven by TabActive="docs".
5. admin.Deps has ChatTraceFile (string) and ChatTraceMaxAgeDays (int) fields.
6. cmd/otto-gateway/main.go wires both new fields from cfg.ChatTraceFile and cfg.ChatTraceMaxAgeDays.
7. TRST-04 preserved — no forbidden imports added to internal/admin.
8. go build ./... and go test ./internal/admin/... both clean.
9. .otto-docs-table-scroll and .otto-code-block CSS rules present.
10. No "Coming soon" remaining in docs.html.tmpl.
</success_criteria>

<output>
Create `.planning/quick/260601-aix-admin-ui-redesign-step-4-docs-page-conte/260601-aix-SUMMARY.md` when done.
</output>
