---
phase: 260601-def
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - internal/admin/templates/docs.html.tmpl
  - internal/admin/static/css/admin.css
  - internal/admin/admin.go
autonomous: true
requirements:
  - DOCS-FLAG-WRAP
  - DOCS-HOOKS-CARD
  - DOCS-ENV-ALPHA
tags: [admin, ui, docs, hooks, pii]

must_haves:
  truths:
    - "Flag/Switch columns on /admin/docs render --env-file PATH and -EnvFile PATH on a single line (no mid-token wrap)."
    - "/admin/docs renders a Hooks card naming all 4 default hooks (RequestIDHook, AuthHook, PIIRedactionHook, LoggingHook) with their Kind."
    - "Hooks card surfaces PII_REDACTION_ENABLED, PII_REDACTION_MODE, PII_ENABLED_ENTITIES, PII_HASH_KEY env vars with defaults + one-line descriptions."
    - "Hooks card shows the bracketed sentinel example [EMAIL_1] (or equivalent) and the SENSITIVE CHAT_TRACE pre-redaction callout."
    - "/admin/docs Environment variables table rows render in alphabetical order by Name (ALLOWED_IPS before AUTH_TOKEN before CHAT_TRACE…)."
    - "go build ./... and go test ./internal/admin/... pass clean."
  artifacts:
    - path: "internal/admin/templates/docs.html.tmpl"
      provides: "Hooks card markup + flag-table class hook + unchanged env-vars iteration over .EnvVars"
      contains: "otto-flag-table"
    - path: "internal/admin/static/css/admin.css"
      provides: ".otto-flag-table rules with table-layout: fixed + nowrap on first two columns + wrap on description"
      contains: ".otto-flag-table"
    - path: "internal/admin/admin.go"
      provides: "docsHandler sorts envVars alphabetically by Name before passing to template"
      contains: "sort.Slice(envVars"
  key_links:
    - from: "internal/admin/templates/docs.html.tmpl flag table"
      to: "internal/admin/static/css/admin.css .otto-flag-table"
      via: "class attribute on <table>"
      pattern: "otto-flag-table"
    - from: "internal/admin/admin.go docsHandler envVars slice"
      to: "internal/admin/templates/docs.html.tmpl {{range .EnvVars}}"
      via: "alphabetically sorted slice passed as docsData.EnvVars"
      pattern: "sort\\.Slice\\(envVars"
---

<objective>
Round-2 admin UI feedback on /admin/docs: (a) fix mid-token wrap in the Flag/Switch reference table so `--env-file PATH` / `-EnvFile PATH` render on one line, (b) add a Hooks documentation card naming the default 4-hook chain + PII redaction env vars + sentinel format + the SENSITIVE CHAT_TRACE pre-redaction callout, and (c) sort the Environment variables table alphabetically by Name.

Purpose: Operator clarity on the docs page. Round-1 (260601-cx3) restructured the Docs page around the otto-gw wrapper; round-2 fills the remaining gaps operators flagged after using the page in anger — column wrap, missing hooks documentation, hard-to-scan env var list.

Output: Single atomic commit modifying docs.html.tmpl, admin.css, admin.go. No new files, no Deps changes, no aboutHandler changes.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@CLAUDE.md
@.planning/quick/260601-cx3-admin-ui-feedback-round-1-trim-about-res/260601-cx3-SUMMARY.md
@internal/admin/templates/docs.html.tmpl
@internal/admin/admin.go
@internal/admin/static/css/admin.css

# Reference-only (do NOT modify):
@internal/plugin/pii/recognizers.go
@internal/plugin/pii/modes.go
@internal/server/hooks_handler_test.go
@cmd/otto-gateway/main.go
@internal/config/config.go

# Confirmed grounding (from researcher reads, do not re-derive):
# - PII env var names: PII_REDACTION_ENABLED, PII_REDACTION_MODE,
#   PII_ENABLED_ENTITIES, PII_HASH_KEY (internal/config/config.go).
# - Canonical entity names: Email, IPv4, IPv6, SSN, CreditCard, USPhone
#   (internal/plugin/pii/recognizers.go, the 6-row Recognizers slice).
# - Sentinel formats: replace="[EMAIL_1]", mask="[EMAIL]",
#   hash="[EMAIL:h-<tag>]" (internal/plugin/pii/modes.go fmt.Sprintf
#   calls — bracketed, NOT angle-bracketed; mirrors round-1
#   260531-pt8 fix that kept kiro-cli from treating sentinels as
#   opening XML tags).
# - Default hook chain (cmd/otto-gateway/main.go lines 186-215 + the
#   four-hook conformance test in internal/server/hooks_handler_test.go):
#       Pre:  RequestIDHook → AuthHook → PIIRedactionHook → LoggingHook
#       Post: LoggingHook
#   LoggingHook is the SAME instance for Pre (last) and Post (only),
#   bridging Pre→Post timings via its per-instance sync.Map keyed by
#   request_id. The conformance test reports it as Kind="Pre,Post".
# - All hooks default to enabled; the operator narrows the chain via
#   ENABLED_HOOKS (comma-split allowlist; empty = all hooks active).
</context>

<tasks>

<task type="auto">
  <name>Task 1: Flag-table wrap fix + Hooks doc card + alphabetical env var sort</name>
  <files>
    internal/admin/templates/docs.html.tmpl,
    internal/admin/static/css/admin.css,
    internal/admin/admin.go
  </files>
  <action>
    Three coordinated edits in a single commit. Use REPO-RELATIVE paths only (no /Users/... prefixes — those resolve to the main repo, not this worktree). Do NOT use `git stash`. Do NOT touch about.html.tmpl, admin.js, the Deps struct, aboutHandler, or any non-admin files. Do NOT fix pre-existing `go vet` errors in tail_test.go / tail_timberjack_test.go.

    --- 1A. Flag-table CSS fix (internal/admin/static/css/admin.css) ---

    Append a new rule block AT END OF FILE (after the existing `260601-aix admin UI redesign step 4 (Docs page)` block that contains `.otto-docs-flags-table`). Header the block `/* ====== Quick 260601-def — round-2 docs polish: flag-table column wrap fix ====== */`.

    Add a NEW CSS class `.otto-flag-table` (do NOT modify the existing `.otto-docs-flags-table` rule — operator wanted no disturbance to env vars table styling, and the existing flags-table rule is the safe baseline). The new class is ADDITIVE — Task 1B adds it as a second class on the <table> alongside the existing `.otto-docs-flags-table`.

    Rules to add:
      .otto-flag-table { table-layout: fixed; width: 100%; }
      .otto-flag-table th:nth-child(1),
      .otto-flag-table td:nth-child(1),
      .otto-flag-table th:nth-child(2),
      .otto-flag-table td:nth-child(2) {
        width: 25%;
        white-space: nowrap;
      }
      .otto-flag-table th:nth-child(3),
      .otto-flag-table td:nth-child(3) {
        width: 50%;
        word-wrap: break-word;
        overflow-wrap: break-word;
      }

    Rationale: `table-layout: fixed` forces the browser to honor the column widths instead of auto-sizing around content. `white-space: nowrap` on cols 1-2 keeps the placeholder tokens (--env-file PATH, -EnvFile PATH) on a single line. `word-wrap`/`overflow-wrap: break-word` on col 3 lets the long descriptions wrap normally. The 25/25/50 split matches the visual weight of the existing layout (description is the verbose column).

    Do NOT add this class to `.otto-docs-env-table` — env-vars table is out of scope for the wrap fix, and operator explicitly said "DO NOT disturb the env vars table styling".

    --- 1B. Hooks card + flag-table class + env-vars iteration unchanged (internal/admin/templates/docs.html.tmpl) ---

    Edit 1B-i (class addition): On the existing `<table class="otto-table otto-docs-flags-table">` line inside the "CLI & startup (otto-gw wrapper)" card (currently around line 85), change the class attribute to `<table class="otto-table otto-docs-flags-table otto-flag-table">`. This is the ONLY change to that table — leave headers, rows, and tbody untouched.

    Edit 1B-ii (Hooks card insertion): Insert a NEW `<section class="otto-card">` BETWEEN the existing "Endpoints reference" card (currently ~line 120) and the "Basic admin usage" card (currently ~line 143). Match the visual idiom of surrounding cards (h2.otto-h2 title, intro paragraph, h3.otto-h3 sub-headings, .otto-table for tables, .otto-about-dl for definition lists, .otto-code-block for code samples).

    Card structure (verbatim copy + paste pattern from existing cards):

    ```
    <section class="otto-card">
      <h2 class="otto-h2">Hooks</h2>
      <p>OTTO Gateway processes every request through a configurable hook chain. The default chain is enabled out-of-the-box; narrow it via the <code>ENABLED_HOOKS</code> env var (comma-separated allowlist; empty value = all hooks active).</p>

      <h3 class="otto-h3">Default hook chain</h3>
      <div class="otto-docs-table-scroll">
        <table class="otto-table otto-docs-flags-table">
          <thead>
            <tr><th scope="col">Hook</th><th scope="col">Kind</th><th scope="col">Default</th><th scope="col">Purpose</th></tr>
          </thead>
          <tbody>
            <tr><td><code>RequestIDHook</code></td><td>Pre</td><td>enabled</td><td>Assigns a unique request ID propagated through structured logs and downstream worker headers.</td></tr>
            <tr><td><code>AuthHook</code></td><td>Pre</td><td>enabled</td><td>Enforces bearer-token (<code>AUTH_TOKEN</code>) + IP-allowlist (<code>ALLOWED_IPS</code>) when configured. No-op when both are empty (Node parity).</td></tr>
            <tr><td><code>PIIRedactionHook</code></td><td>Pre</td><td>enabled</td><td>Detects and redacts PII in inbound prompts before they reach the upstream worker. No-op when <code>PII_REDACTION_ENABLED=false</code> (the default).</td></tr>
            <tr><td><code>LoggingHook</code></td><td>Pre,Post</td><td>enabled</td><td>Emits structured request/response log entries (slog JSON). The same instance runs as the last Pre hook and the only Post hook, bridging timings via an internal request-ID map.</td></tr>
          </tbody>
        </table>
      </div>

      <h3 class="otto-h3">PII redaction</h3>
      <p>PIIRedactionHook sits third in the Pre chain (after RequestID and Auth) so redacted prompts never reach the upstream worker. It is a cheap pass-through unless <code>PII_REDACTION_ENABLED=true</code>. Six built-in recognizers detect <code>Email</code>, <code>IPv4</code>, <code>IPv6</code>, <code>SSN</code>, <code>CreditCard</code>, and <code>USPhone</code>. Detected matches are replaced with bracketed sentinels.</p>

      <dl class="otto-about-dl">
        <dt><code>PII_REDACTION_ENABLED</code></dt><dd>default <code>false</code> — master switch. When <code>false</code> the hook stays in the chain but does no work.</dd>
        <dt><code>PII_REDACTION_MODE</code></dt><dd>default <code>replace</code> — one of <code>replace</code> (numbered sentinel per match), <code>mask</code> (unnumbered category sentinel), <code>hash</code> (HMAC-keyed correlation token), <code>drop</code> (matched substring removed entirely).</dd>
        <dt><code>PII_ENABLED_ENTITIES</code></dt><dd>default empty (all six recognizers active) — comma-separated allowlist from <code>Email,IPv4,IPv6,SSN,CreditCard,USPhone</code>.</dd>
        <dt><code>PII_HASH_KEY</code></dt><dd>no default — REQUIRED when <code>PII_REDACTION_MODE=hash</code> (boot error otherwise). HMAC-SHA256 key. Rotating it invalidates prior session correlation tokens.</dd>
      </dl>

      <p>Detected PII is replaced with bracketed sentinels like <code>[EMAIL_1]</code>, <code>[PHONE_2]</code>, or <code>[EMAIL:h-abc12345]</code> (hash mode). Square brackets are deliberate — angle-bracketed sentinels like <code>&lt;EMAIL_1&gt;</code> caused kiro-cli to treat them as opening XML tags (see fix 260531-pt8).</p>

      <h3 class="otto-h3">Examples</h3>
      <pre class="otto-code-block">PII_REDACTION_ENABLED=true
PII_REDACTION_MODE=replace
# Default — numbered sentinels per match per entity:
#   "Email me at a@b.com or c@d.com"
#   → "Email me at [EMAIL_1] or [EMAIL_2]"</pre>
      <pre class="otto-code-block">PII_REDACTION_ENABLED=true
PII_REDACTION_MODE=hash
PII_HASH_KEY=&lt;32+ random bytes, hex or base64&gt;
PII_ENABLED_ENTITIES=Email,SSN
# Same value → same hash sentinel within a key lifetime (correlation
# across requests without leaking the underlying PII):
#   "a@b.com … a@b.com" → "[EMAIL:h-9f3a2c1d] … [EMAIL:h-9f3a2c1d]"</pre>

      <p><strong>SENSITIVE:</strong> <code>CHAT_TRACE=true</code> writes raw prompts to <code>CHAT_TRACE_FILE</code> <em>before</em> PIIRedactionHook runs. When chat-trace is on, the on-disk file contains unredacted PII — this is why the About page surfaces a SENSITIVE badge whenever <code>CHAT_TRACE=true</code>. Turn off in production unless intentionally debugging a request shape.</p>
    </section>
    ```

    Placement: After the closing `</section>` of the Endpoints reference card and before the opening `<section class="otto-card">` of the Basic admin usage card. Leave a single blank line above and below the new section to match the page's existing inter-section whitespace.

    Edit 1B-iii: The `{{range .EnvVars}}` block (currently ~line 16) is unchanged — Task 1C does the sort handler-side, so the template iteration is naturally alphabetical at render time.

    --- 1C. Sort env vars alphabetically (internal/admin/admin.go) ---

    In docsHandler (currently starting ~line 369), AFTER the `envVars := []envVarRow{ ... }` literal closes (currently ~line 441) and BEFORE the `cliFlags := []cliFlagRow{ ... }` literal opens (currently ~line 443), insert:

      sort.Slice(envVars, func(i, j int) bool {
          return envVars[i].Name < envVars[j].Name
      })

    Add `"sort"` to the import block at the top of the file (currently has bytes, fmt, log/slog, net/http, runtime, strconv, strings, time, github.com/go-chi/chi/v5 — `sort` belongs alphabetically between `runtime` and `strconv`). The `goimports`/`gofmt` ordering is: stdlib group (sorted), blank line, third-party group. Match the existing pattern.

    Do NOT sort cliFlags — that table has its own deliberate operator-facing grouping (per-subcommand) preserved by round-1 and out of scope here.

    --- Implementation notes ---

    - Round-1 SUMMARY (260601-cx3) confirms the round-1 pattern: handler-side fallbacks + presentation-only templates. This plan extends that pattern (sort is handler-side; template stays presentation-only).
    - WR-05 buffer-then-write in docsHandler is preserved — only the slice ordering changes; the bytes.Buffer flow stays intact.
    - TRST-04 preserved — no new imports outside stdlib + go-chi/chi/v5. `sort` is stdlib.
    - All assets embedded via embed.FS — no new files, just edits to already-embedded ones (docs.html.tmpl + admin.css are already in the embed tree).
    - Single atomic commit preferred: `feat(260601-def): admin UI round-2 feedback — flag table wrap fix, Hooks doc card, alphabetical env vars`.
  </action>
  <verify>
    <automated>
      cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway &&
      # 1. Build + admin tests pass clean
      go build ./... &&
      go test ./internal/admin/... &&
      # 2. CSS rule exists with the load-bearing properties (skip header comment via grep -v)
      grep -v '^[[:space:]]*\*\|^[[:space:]]*//\|^[[:space:]]*/\*' internal/admin/static/css/admin.css | grep -q 'otto-flag-table' &&
      grep -q 'table-layout:[[:space:]]*fixed' internal/admin/static/css/admin.css &&
      grep -q 'white-space:[[:space:]]*nowrap' internal/admin/static/css/admin.css &&
      # 3. Template: flag-table class wired AND env-vars iteration unchanged
      grep -q 'otto-docs-flags-table otto-flag-table' internal/admin/templates/docs.html.tmpl &&
      grep -q '{{range .EnvVars}}' internal/admin/templates/docs.html.tmpl &&
      # 4. Hooks card present with all 4 hook names + PII env vars + bracketed sentinel
      grep -q '>Hooks<' internal/admin/templates/docs.html.tmpl &&
      grep -q 'RequestIDHook' internal/admin/templates/docs.html.tmpl &&
      grep -q 'AuthHook' internal/admin/templates/docs.html.tmpl &&
      grep -q 'PIIRedactionHook' internal/admin/templates/docs.html.tmpl &&
      grep -q 'LoggingHook' internal/admin/templates/docs.html.tmpl &&
      grep -q 'PII_REDACTION_ENABLED' internal/admin/templates/docs.html.tmpl &&
      grep -q 'PII_REDACTION_MODE' internal/admin/templates/docs.html.tmpl &&
      grep -q 'PII_ENABLED_ENTITIES' internal/admin/templates/docs.html.tmpl &&
      grep -q 'PII_HASH_KEY' internal/admin/templates/docs.html.tmpl &&
      grep -q 'ENABLED_HOOKS' internal/admin/templates/docs.html.tmpl &&
      grep -q '\[EMAIL_1\]' internal/admin/templates/docs.html.tmpl &&
      grep -q 'CHAT_TRACE' internal/admin/templates/docs.html.tmpl &&
      # 5. Handler: sort.Slice on envVars present, sort imported
      grep -q 'sort.Slice(envVars' internal/admin/admin.go &&
      grep -q '"sort"' internal/admin/admin.go &&
      # 6. Render-order check: with sort.Slice in place, an in-process render
      # MUST emit ALLOWED_IPS before AUTH_TOKEN before CHAT_TRACE in the HTML.
      # Use go test with httptest against docsHandler to confirm. Existing
      # admin_test.go suite already does this kind of render check — if a
      # test for env-var order does not exist, the build + grep gates above
      # are sufficient; a render-order test is OPTIONAL polish (not blocking)
      # and the manual /admin/docs check below catches it.
      echo "OK: all grep gates pass"
    </automated>
    <human-check>
      Run the gateway locally, visit http://127.0.0.1:18080/admin/docs (or whichever HTTP_ADDR is configured), and confirm:
      1. Flag/Switch table rows for --env-file / --overrides-file / --template / --dest / --overrides-dest / --auth-token / --kiro / --addr do NOT wrap mid-token (the PATH / TOK / TOKEN / ADDR placeholders stay on the same line as the flag name).
      2. A new "Hooks" card appears between "Endpoints reference" and "Basic admin usage" with the 4-hook chain table + PII subsection + two code-block examples + SENSITIVE CHAT_TRACE callout.
      3. The Environment variables table is alphabetical — ALLOWED_IPS appears before AUTH_TOKEN, CHAT_TRACE before DEBUG, etc.
      4. About page (/admin/about) is visually unchanged — round-2 must not regress round-1.
    </human-check>
  </verify>
  <done>
    docs.html.tmpl, admin.css, and admin.go modified per the action; all grep gates listed in verify.automated pass; go build ./... and go test ./internal/admin/... are clean; visual checks above confirm the three behavior changes shipped without disturbing the rest of the Docs or About pages. Committed as a single atomic commit.
  </done>
</task>

</tasks>

<verification>
- `go build ./...` — clean.
- `go test ./internal/admin/...` — passes (existing admin suite covers docsHandler render).
- All automated grep gates in Task 1 verify block pass.
- Manual visual check on /admin/docs confirms the three operator-facing changes.
</verification>

<success_criteria>
- /admin/docs Flag/Switch reference table: no mid-token wrap on placeholder rows.
- /admin/docs Hooks card present with default chain (RequestIDHook / AuthHook / PIIRedactionHook / LoggingHook), PII env vars (PII_REDACTION_ENABLED / PII_REDACTION_MODE / PII_ENABLED_ENTITIES / PII_HASH_KEY), bracketed sentinel example, and SENSITIVE CHAT_TRACE callout.
- /admin/docs Environment variables table rendered alphabetically (ALLOWED_IPS < AUTH_TOKEN < … in HTML order).
- `go build ./...` and `go test ./internal/admin/...` pass.
- TRST-04 boundary preserved (no new internal/* imports in internal/admin).
- About page and Dashboard unchanged.
- Single atomic commit.
</success_criteria>

<output>
Create `.planning/quick/260601-def-admin-ui-feedback-round-2-flag-table-wid/260601-def-SUMMARY.md` when done.
</output>
