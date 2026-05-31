---
phase: quick-260531-ebi
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - internal/admin/admin.go
  - internal/admin/snapshot.go
  - internal/admin/templates/index.html.tmpl
  - internal/admin/handlers_test.go
  - internal/admin/snapshot_test.go
  - cmd/otto-gateway/main.go
  - scripts/otto-gw
  - scripts/otto-gw.ps1
autonomous: true
requirements: [QUICK-260531-EBI]
must_haves:
  truths:
    - "GET /admin/api/snapshot returns debug and chat_trace booleans reflecting cfg.Debug / cfg.ChatTrace"
    - "GET /admin/ HTML page visibly shows Debug and Chat-trace enablement state"
    - "otto-gw status (POSIX) prints debug: on/off and chat-trace: on/off sourced from the admin snapshot"
    - "otto-gw.ps1 status (PowerShell) prints debug and chat-trace state sourced from the admin snapshot"
    - "GET /health response is unchanged (no new fields)"
    - "Existing admin tests still pass; new fields are covered by tests"
  artifacts:
    - path: "internal/admin/snapshot.go"
      provides: "Debug and ChatTrace fields on AdminSnapshot, populated from Deps"
      contains: "ChatTrace"
    - path: "internal/admin/admin.go"
      provides: "Debug and ChatTrace fields on Deps; page render struct carries them"
      contains: "ChatTrace"
    - path: "internal/admin/templates/index.html.tmpl"
      provides: "Visible Debug and Chat-trace summary items"
      contains: ".ChatTrace"
    - path: "cmd/otto-gateway/main.go"
      provides: "Wiring cfg.Debug and cfg.ChatTrace into admin.Deps"
      contains: "ChatTrace: cfg.ChatTrace"
    - path: "scripts/otto-gw"
      provides: "status() curls /admin/api/snapshot and prints debug/chat-trace"
      contains: "chat-trace"
    - path: "scripts/otto-gw.ps1"
      provides: "Get-GatewayStatus queries /admin/api/snapshot and prints debug/chat-trace"
      contains: "chat-trace"
  key_links:
    - from: "cmd/otto-gateway/main.go"
      to: "admin.Deps"
      via: "Debug/ChatTrace field assignment from cfg"
      pattern: "ChatTrace:\\s*cfg\\.ChatTrace"
    - from: "internal/admin/snapshot.go"
      to: "AdminSnapshot JSON"
      via: "snake_case debug / chat_trace tags"
      pattern: "json:\"chat_trace\""
    - from: "scripts/otto-gw"
      to: "/admin/api/snapshot"
      via: "curl + sed parse"
      pattern: "admin/api/snapshot"
---

<objective>
Surface DEBUG-logging and chat-trace enablement state in two operator-facing surfaces: the admin web UI (HTML page + JSON snapshot) and the `otto-gw status` command (POSIX + PowerShell wrappers).

Purpose: An operator should tell at a glance whether `DEBUG` logging and `CHAT_TRACE` (a SENSITIVE feature that writes raw prompts to disk) are on, without grepping env or logs. Chat-trace visibility in particular is a safety affordance.

Output: Two new booleans (`debug`, `chat_trace`) flow from `config.Config` → `admin.Deps` → both the JSON snapshot and the rendered HTML page, and both wrapper `status` commands read them from `/admin/api/snapshot` (NOT `/health`, which is D-12-locked).
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@./CLAUDE.md

# Verified source files (read during planning — facts below are confirmed):
# - internal/admin/admin.go: Deps struct (line 63), pageHandler render struct (line 148-155, currently {Version, Commit})
# - internal/admin/snapshot.go: AdminSnapshot struct (line 20), snapshotHandler populates it (line 80)
# - internal/admin/templates/index.html.tmpl: summary-strip section (line 12-57), uses {{.Version}} at line 31
# - cmd/otto-gateway/main.go: admin.Handler(admin.Deps{...}) wiring (line 564-573); cfg.Debug + cfg.ChatTrace in scope
# - internal/config/config.go: ChatTrace bool (line 160), Debug parsed (line 197)
# - scripts/otto-gw: status() (line 429-461) sed-parses ${OTTO_ADDR}/health; OTTO_ADDR base URL at line 56
# - scripts/otto-gw.ps1: Get-GatewayStatus (line 315-343) IRM $HealthUrl/health; $HealthUrl base at line 89
# - admin routes are auth-exempt (mounted on outer router per D-01/D-07 — see admin.go package doc lines 1-3, 82)
@internal/admin/admin.go
@internal/admin/snapshot.go
@internal/admin/templates/index.html.tmpl
@cmd/otto-gateway/main.go
@scripts/otto-gw
@scripts/otto-gw.ps1
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Add debug + chat_trace to admin Deps, snapshot JSON, and HTML page</name>
  <files>internal/admin/admin.go, internal/admin/snapshot.go, internal/admin/templates/index.html.tmpl, cmd/otto-gateway/main.go, internal/admin/snapshot_test.go, internal/admin/handlers_test.go</files>
  <behavior>
    - GET /admin/api/snapshot returns JSON containing "debug": true and "chat_trace": true when Deps.Debug and Deps.ChatTrace are true.
    - GET /admin/api/snapshot returns "debug": false and "chat_trace": false when those Deps fields are false (zero-value default — no regression for callers that don't set them).
    - GET /admin/ HTML body contains visible "Debug" and "Chat-trace" labels plus their on/off state rendered from the template fields.
    - Existing snapshot/page/computeStatus/log-sources tests continue to pass unchanged.
  </behavior>
  <action>
    Add two exported bool fields `Debug` and `ChatTrace` to `admin.Deps` (internal/admin/admin.go, the struct at ~line 63). Document them in the field-doc comment block in the same style as the existing fields (one short paragraph each: Debug mirrors cfg.Debug DEBUG logging; ChatTrace mirrors cfg.ChatTrace, note it is the SENSITIVE raw-prompt tracer).

    Add `Debug bool \`json:"debug"\`` and `ChatTrace bool \`json:"chat_trace"\`` to the `AdminSnapshot` struct in internal/admin/snapshot.go (snake_case tags are the load-bearing wire contract — match the existing convention). Place them after `Commit` so the wire ordering stays readable. In `snapshotHandler` (~line 81), populate them from `h.deps.Debug` and `h.deps.ChatTrace` alongside the existing Version/Commit assignments. Update the wire-shape doc comment above the handler to list the two new keys.

    In `pageHandler` (internal/admin/admin.go ~line 148), extend the anonymous render struct to add `Debug bool` and `ChatTrace bool` and assign them from `h.deps.Debug` / `h.deps.ChatTrace`.

    In internal/admin/templates/index.html.tmpl, add two new `.otto-summary-item` blocks inside the summary strip `<section>` (after the Version item at ~line 32, before Pool). These are baked-in at render time (like Version), NOT JS-hydrated. Render label + state, e.g. `<span class="otto-summary-label">Debug</span><span class="otto-summary-value">{{if .Debug}}on{{else}}off{{end}}</span>`, and the same for Chat-trace using `{{.ChatTrace}}`. Use literal text "Debug" and "Chat-trace" for the labels so the wrapper-independent HTML check and operators both find them.

    In cmd/otto-gateway/main.go, in the `admin.Handler(admin.Deps{...})` literal (~line 564), add `Debug: cfg.Debug,` and `ChatTrace: cfg.ChatTrace,` to the struct (cfg is already in scope — same scope that reads cfg.ChatTrace at line 560).

    Tests: In internal/admin/snapshot_test.go, extend `TestAdmin_SnapshotHandler` to set `Debug: true, ChatTrace: true` in deps and assert `snap.Debug == true` and `snap.ChatTrace == true` after decode. Add `snap.Debug == false`/`snap.ChatTrace == false` assertions to `TestAdmin_SnapshotNilSafe` (deps leaves them unset → must be false, proving zero-value default). In internal/admin/handlers_test.go, extend `TestAdmin_PageHandler` to set `Debug: true` in deps and assert the body contains the literal labels "Debug" and "Chat-trace" and the rendered "on" state.

    Do NOT touch internal/server/health.go — the /health response is D-12-locked. The snapshot is the data source for these flags.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go test ./internal/admin/... && go vet ./internal/admin/... ./cmd/otto-gateway/... && go build ./... && golangci-lint run ./internal/admin/... ./cmd/otto-gateway/... 2>/dev/null; gosec -quiet ./internal/admin/... 2>/dev/null || true</automated>
  </verify>
  <done>
    `go test ./internal/admin/...` passes with new assertions; `go build ./...` succeeds; `go vet` clean. GET /admin/api/snapshot JSON includes `debug` and `chat_trace` keys; GET /admin/ HTML shows visible Debug and Chat-trace state. /health is untouched.
  </done>
</task>

<task type="auto">
  <name>Task 2: Surface debug + chat-trace in otto-gw status (POSIX + PowerShell wrappers)</name>
  <files>scripts/otto-gw, scripts/otto-gw.ps1</files>
  <action>
    POSIX (scripts/otto-gw, `status()` at ~line 429): after the existing `/health` parse block (which prints status/version/uptime/pool/sessions and ends ~line 460), add a second fetch against the admin snapshot. Use the same base URL variable `${OTTO_ADDR}` — the endpoint is `${OTTO_ADDR}/admin/api/snapshot` (auth-exempt, no token needed). Guard it the same way the health fetch is guarded: `command -v curl` is already checked earlier in the function, so reuse it. Fetch into a local var with `curl -sf "${OTTO_ADDR}/admin/api/snapshot" 2>/dev/null || true`; if empty, skip silently (do not fail status — the snapshot is best-effort like health). Parse the two booleans with `sed` matching the JSON-boolean form (no quotes around true/false), e.g. `sed -n 's/.*"debug"[[:space:]]*:[[:space:]]*\(true\|false\).*/\1/p'` and the same for `"chat_trace"`. Map the parsed value to a friendly on/off via a tiny helper or inline `[[ "$dbg" == "true" ]] && echo on || echo off`. Print two aligned lines matching the existing two-space-indent style: `  debug:    on/off` and `  chat-trace: on/off`. Keep column alignment visually consistent with the existing lines (status:/version:/uptime:/pool:/sessions:). Must remain POSIX-sh-compatible bash and shellcheck-clean — quote all expansions, no jq/python.

    PowerShell (scripts/otto-gw.ps1, `Get-GatewayStatus` at ~line 315): inside the existing `try` block, after the `sessions:` line (~line 339), add a second `Invoke-RestMethod` against `"$HealthUrl/admin/api/snapshot"` (same base URL var, admin endpoint). Invoke-RestMethod returns a parsed object, so read `$snap.debug` and `$snap.chat_trace` directly. Wrap this admin fetch in its own try/catch (or guard) so an unreachable admin endpoint does not blank out the already-printed health lines. Map the booleans to on/off (e.g. `if ($snap.debug) { 'on' } else { 'off' }`) and `Write-Host` two lines matching the existing `("  label:   {0}" -f ...)` format: `debug` and `chat-trace`.

    Verification limitation (call out in SUMMARY): scripts/otto-gw.ps1 cannot be executed or pwsh-linted on this macOS dev box (no pwsh). It is reviewed by reading only; correctness of the PowerShell path is asserted by code review, not runtime test.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && shellcheck scripts/otto-gw && grep -q "admin/api/snapshot" scripts/otto-gw && grep -q "chat-trace" scripts/otto-gw && grep -q "admin/api/snapshot" scripts/otto-gw.ps1 && grep -q "chat_trace" scripts/otto-gw.ps1</automated>
  </verify>
  <done>
    `shellcheck scripts/otto-gw` passes clean. POSIX `status()` and PowerShell `Get-GatewayStatus` both fetch `/admin/api/snapshot` from the existing base-URL var and print `debug` and `chat-trace` on/off lines after the existing health output. Neither breaks if the admin endpoint is unreachable. PowerShell path is review-only (no pwsh on this box — noted as a verification limitation).
  </done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| operator → admin UI/CLI | Operator reads enablement state; no new input crosses into the gateway |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-ebi-01 | Information Disclosure | /admin/api/snapshot debug/chat_trace booleans | accept | Admin endpoint is already auth-exempt and already exposes pool/session detail (D-01/D-07). Two booleans reveal feature-flag state only, no secrets or prompt contents. No change to exposure posture. |
| T-ebi-02 | Information Disclosure | chat-trace visibility | mitigate | Surfacing chat_trace=on is a SAFETY affordance — it warns operators that raw prompts are being written to disk. Net reduction in accidental-exposure risk. Label uses literal "Chat-trace" so it is unmissable. |
| T-ebi-03 | Tampering | wrapper sed/IRM parse of snapshot JSON | accept | Parsing is read-only display; malformed/absent JSON degrades to skipped lines (best-effort), never alters gateway behavior. No package installs in this plan. |
</threat_model>

<verification>
- `go test ./internal/admin/...` passes (existing + new assertions for debug/chat_trace).
- `go build ./...` and `go vet ./...` clean (admin + cmd packages).
- `golangci-lint run` and `gosec` clean on touched Go packages (project trust gates).
- `shellcheck scripts/otto-gw` clean.
- Manual/inspection: GET /admin/ HTML renders visible Debug + Chat-trace state; GET /admin/api/snapshot JSON has `debug` and `chat_trace` keys; `/health` response byte-shape unchanged.
- PowerShell wrapper reviewed by reading only (no pwsh runtime on this dev box — documented limitation).
</verification>

<success_criteria>
- An operator viewing GET /admin/ sees at a glance whether Debug logging and Chat-trace are on or off.
- GET /admin/api/snapshot exposes `debug` and `chat_trace` booleans matching cfg.Debug / cfg.ChatTrace.
- `otto-gw status` (both POSIX and PowerShell) prints `debug` and `chat-trace` on/off, sourced from the admin snapshot, without requiring an auth token.
- The D-12-locked /health response gains no new fields.
- All project trust gates (go test, go vet, golangci-lint, gosec, shellcheck) pass on touched files.
</success_criteria>

<output>
Create `.planning/quick/260531-ebi-surface-debug-and-chat-trace-enablement-/260531-ebi-SUMMARY.md` when done.
</output>
