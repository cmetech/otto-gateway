---
phase: quick-260524-pee
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - tests/e2e/e2e_test.go
  - tests/e2e/sdk/package.json
  - tests/e2e/sdk/sdk_roundtrip.mjs
  - tests/e2e/sdk/README.md
  - tests/e2e/cmd/report/main.go
  - Makefile
  - .gitignore
autonomous: true
requirements: []
must_haves:
  truths:
    - "make test / go test ./... -race stays green with no node, no OTTO_E2E, and never compiles the e2e file"
    - "go test -tags e2e ./tests/e2e/ with OTTO_E2E unset skips all subtests cleanly (no kiro, no binary boot)"
    - "make e2e boots the real otto-gateway binary against real kiro and always renders a markdown report regardless of test pass/fail"
    - "The Node SDK harness skips cleanly when @anthropic-ai/sdk is not installed; make e2e-sdk-setup enables it"
  artifacts:
    - path: "tests/e2e/e2e_test.go"
      provides: "e2e-tagged, OTTO_E2E-gated black-box HTTP suite booting a real temp-built binary"
      contains: "//go:build e2e"
    - path: "tests/e2e/cmd/report/main.go"
      provides: "stdlib go-test-json -> markdown report renderer (no build tag)"
      contains: "package main"
    - path: "tests/e2e/sdk/sdk_roundtrip.mjs"
      provides: "opt-in @anthropic-ai/sdk round-trip (non-stream + stream)"
    - path: "tests/e2e/sdk/package.json"
      provides: "private ESM package pinning @anthropic-ai/sdk ^0.90.0"
    - path: "Makefile"
      provides: "e2e + e2e-sdk-setup targets; e2e never added to all/ci"
      contains: "e2e:"
  key_links:
    - from: "Makefile e2e target"
      to: "go run ./tests/e2e/cmd/report"
      via: "go test -json piped to the renderer with test exit code preserved"
      pattern: "go test -tags e2e -json"
    - from: "tests/e2e/e2e_test.go bootGateway"
      to: "real otto-gateway binary subprocess"
      via: "exec.Command(builtBinary) + HTTP_ADDR free-port + /health poll"
      pattern: "exec.Command"
---

<objective>
Build an automated end-to-end test suite under `tests/e2e/` that boots the
REAL otto-gateway binary against REAL kiro-cli, drives it over HTTP, and emits a
markdown report. This automates HUMAN-UAT steps 1, 2, 3, 6 (health, auth,
non-streaming + streaming Anthropic round-trips, surface gating / fail-fast) and
provides an opt-in Node `@anthropic-ai/sdk` harness for steps 4-5.

Purpose: replace manual UAT toil with a one-command `make e2e` that produces a
reviewable artifact, while keeping the default `go test ./...` / `make test` /
`make ci` paths untouched (no new go.mod deps, e2e behind a build tag + env gate,
Node harness opt-in).

Output: 7 files — one e2e Go test, a stdlib report renderer, a Node SDK harness
(package.json + .mjs + README), and Makefile + .gitignore edits.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@CLAUDE.md

# Mirror these patterns — the e2e test reuses the kiro-gating + strict-SSE
# state machine from the existing Anthropic integration test, but swaps
# httptest for a real binary subprocess and OTTO_INTEGRATION for OTTO_E2E.
@internal/adapter/anthropic/integration_test.go
@cmd/otto-gateway/main.go
@internal/config/config.go
@Makefile
@.gitignore

<interfaces>
<!-- Contracts the executor needs. Do NOT import internal/* from tests/e2e —
     this is a black-box HTTP suite (package e2e_test, external). All assertions
     go through HTTP + JSON, not Go types. -->

Binary boot (from cmd/otto-gateway/main.go):
- `config.LoadArgs(os.Args[1:])` -> `newApp` -> `app.srv.RunUntilSignal(ctx)`. Foreground process.
- Warmup is BLOCKING before the HTTP listener accepts. Warmup failure => process exits NON-ZERO and logs to stderr ("startup failed" / "pool warmup: ...").
- Graceful shutdown on SIGINT/SIGTERM (os.Interrupt).

Config env vars consumed at boot (from internal/config/config.go):
- HTTP_ADDR (default "127.0.0.1:11435")
- AUTH_TOKEN (comma-split; empty => auth disabled). Set it to enable 401 path.
- KIRO_CMD (default "kiro-cli"); KIRO_ARGS (whitespace-split, default "acp")
- ENABLED_SURFACES (comma-split, default "ollama,anthropic"). Unknown name => Load() error => boot exits non-zero, stderr names the offending surface (e.g. "anthrpic").

Routes:
- GET /health -> 200 JSON (exempt route, NO auth; alive even in degraded mode). Use for warmup poll.
- POST /v1/messages -> Anthropic surface (mounted only when "anthropic" in ENABLED_SURFACES). 404 when surface disabled.
- POST /api/chat -> Ollama surface (mounted only when "ollama" in ENABLED_SURFACES).
- Auth applies to protected routes: missing/invalid token -> 401.

Anthropic non-streaming response shape (assert via JSON, not Go types):
- top-level: type=="message", role=="assistant", stop_reason present (non-null), content[] non-empty, content[0].type=="text", content[0].text non-empty.

Anthropic SSE framing (stream:true; Content-Type text/event-stream):
- Strict frame: line "event: <name>" then "data: <json>" then blank line. Single space after "event:" and "data:".
- Ordered presence on happy path: message_start (first), content_block_start, >=1 content_block_delta, content_block_stop, message_delta, message_stop (last). No "error" event.
- Mirror the bufio.Scanner + frameState state machine and scanner.Buffer(64KiB,1MiB) from integration_test.go.

kiro resolution (mirror resolveKiroCLI):
- OTTO_KIRO_BIN env wins; else exec.LookPath("kiro-cli"); else t.Skip.
- Top gate: skip unless os.Getenv("OTTO_E2E")=="1".
</interfaces>
</context>

<tasks>

<task type="auto">
  <name>Task 1: E2E Go suite — gate, temp-binary build, bootGateway helper, HTTP subtests</name>
  <files>tests/e2e/e2e_test.go</files>
  <action>
Create `tests/e2e/e2e_test.go` with first line `//go:build e2e` (blank line, then
`package e2e_test`). External test package, black-box: import ONLY stdlib
(bufio, bytes, context, encoding/json, errors, fmt, net, net/http, os, os/exec,
path/filepath, strings, testing, time). NEVER import otto-gateway/internal/* —
all assertions go over HTTP. Stdlib only, no new go.mod deps.

Package var: `var builtBinary string`.

TestMain(m *testing.M): if os.Getenv("OTTO_E2E") != "1", run m.Run() immediately
and exit (so the gate-skip path is cheap and never builds). Otherwise create a
temp dir (os.MkdirTemp), build once: `exec.Command("go","build","-o",
filepath.Join(tmp,"otto-gateway"),"./cmd/otto-gateway")` with cmd.Dir set so the
build runs from the repo root (the test runs in tests/e2e/, so use a path that
resolves to the module root — set cmd.Dir to "../.." relative to the test file's
runtime dir; resolve via a robust approach: run the build with cmd.Dir = the
result of locating go.mod by walking up from os.Getwd(), or simply set
cmd.Dir="../.." which is the module root from tests/e2e/). On build failure:
print stderr and os.Exit(1). Store the absolute binary path in builtBinary,
defer os.RemoveAll(tmp), then run m.Run() and os.Exit(code).

resolveKiro(t): mirror resolveKiroCLI from integration_test.go — OTTO_KIRO_BIN
env wins; else exec.LookPath("kiro-cli") (t.Skip if not found). The OTTO_E2E top
gate is checked separately (see gateOrSkip below) so all subtests skip uniformly.

gateOrSkip(t): t.Helper(); if os.Getenv("OTTO_E2E") != "1" { t.Skip("set
OTTO_E2E=1 to run the E2E suite") }.

freePort(t) string: net.Listen("tcp","127.0.0.1:0"), capture ln.Addr().String(),
close the listener, return the addr. (Accept the small race window — standard
test pattern.)

bootGateway(t, extraEnv map[string]string) (baseURL string, cleanup func()):
t.Helper(); kiro := resolveKiro(t); addr := freePort(t); baseURL :=
"http://"+addr. Build env from os.Environ() plus HTTP_ADDR=addr,
AUTH_TOKEN=e2e-token, KIRO_CMD=kiro, then overlay extraEnv (extraEnv wins).
cmd := exec.Command(builtBinary); cmd.Env = the assembled slice; capture stderr
to a *bytes.Buffer (cmd.Stderr = &buf). cmd.Start(). Poll
GET baseURL+"/health" every ~250ms for up to ~15s: on 200 break. BEFORE each poll
check whether the process already exited (use a done channel fed by a goroutine
running cmd.Wait(), or check cmd.ProcessState); if it exited early OR the 15s
warmup deadline elapses, call cleanup-ish (kill if still running) and
t.Skipf("gateway warmup failed (likely kiro-cli auth-not-refreshed); stderr:\n%s",
buf.String()) — mirror the kiroSetup skip-on-warmup-failure policy. cleanup func:
send cmd.Process.Signal(os.Interrupt); wait on a channel for cmd.Wait() with a
~5s timeout; on timeout cmd.Process.Kill(). Ignore the non-zero exit from
interrupt (log via t.Logf, do not fail).

postMessages helper (t, baseURL, body []byte, headers map[string]string)
*http.Response: build POST baseURL+"/v1/messages", set Content-Type
application/json + anthropic-version 2023-06-01 + the supplied headers, Do via
http.DefaultClient (or a client with a ~60s timeout). Return the response.

TopLevel test funcs (each begins with gateOrSkip(t)):

TestE2E_SharedGateway — boot ONE gateway (no extraEnv) and run the auth + shape +
stream cases as t.Run subtests sharing that boot (speed). defer cleanup. Subtests:
  - "Health": GET baseURL+"/health" -> expect 200; json.Unmarshal the body into
    map[string]any (must succeed).
  - "Unauthorized": POST /v1/messages with NO auth header (still send
    anthropic-version) and a minimal valid body -> expect 401.
  - "NonStreaming_XApiKey": body
    {"model":"auto","max_tokens":256,"stream":false,"messages":[{"role":"user","content":"say hi"}]},
    header x-api-key:e2e-token -> expect 200; decode JSON and assert type=="message",
    role=="assistant", content[0].type=="text" && content[0].text != "",
    stop_reason present (non-null).
  - "NonStreaming_Bearer": same body, header Authorization:Bearer e2e-token ->
    expect 200 + same shape assertions.
  - "Streaming_SSE": same body but stream:true, header Authorization:Bearer
    e2e-token -> expect 200 and Content-Type prefix text/event-stream. Reuse the
    strict frameState state machine + scanner.Buffer(make([]byte,0,64*1024),1<<20)
    from integration_test.go: validate exact framing ("event: " / "data: " single
    space, blank-line terminator, JSON-valid data payloads, end state ==
    expectingEvent). Collect the ordered event-name list; assert events[0]==
    message_start, last==message_stop, and presence of content_block_start, >=1
    content_block_delta, content_block_stop, message_delta. Fail if any "error"
    event appears.

TestE2E_SurfaceGating_OllamaOnly — gateOrSkip; bootGateway with extraEnv
{"ENABLED_SURFACES":"ollama"}; defer cleanup. POST /v1/messages (with bearer
auth) -> expect 404 (surface not mounted). POST /api/chat with a minimal valid
Ollama body (e.g. {"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false})
and bearer auth -> assert status != 404 (route exists; any other code incl 200
or 503 is acceptable — we only prove the route is mounted, not the full Ollama
round-trip).

TestE2E_SurfaceGating_TypoFailFast — gateOrSkip; resolveKiro(t) (so it skips
without kiro env like the rest). Exec the built binary DIRECTLY (no /health poll)
with env including ENABLED_SURFACES=anthrpic (deliberate typo) and a free
HTTP_ADDR. Run cmd.Start(); wait on a goroutine+channel for cmd.Wait() with a
~5s timeout (kill on timeout and t.Fatal). Assert the process exited NON-ZERO AND
the captured stderr contains the substring "anthrpic" (fail-fast names the
offending surface).

TestE2E_SDK_RoundTrip — gateOrSkip; opt-in: if exec.LookPath("node") fails ->
t.Skip("node not installed — run: make e2e-sdk-setup"). Determine harness
readiness: os.Stat("sdk/node_modules") OK (relative to tests/e2e working dir) OR
os.Getenv("OTTO_E2E_SDK")=="1"; if neither -> t.Skip("SDK harness not installed —
run: make e2e-sdk-setup"). Otherwise bootGateway (no extraEnv); defer cleanup;
exec.Command("node","tests/e2e/sdk/sdk_roundtrip.mjs") run from the MODULE ROOT
(set cmd.Dir to "../.." so the relative script path resolves), with env
os.Environ() + ANTHROPIC_BASE_URL=baseURL + ANTHROPIC_API_KEY=e2e-token; capture
combined stdout+stderr; t.Logf them; assert exit code 0.

NOTE on relative paths: the test process CWD is tests/e2e/. Module root is "..".
Reconcile: `go build ./cmd/otto-gateway` and `node tests/e2e/sdk/...` both need
the module root as CWD, so set cmd.Dir="../.." for the go build (TestMain) and
the node exec; for os.Stat("sdk/node_modules") the CWD-relative path is correct.
Be consistent and comment the chosen base. (Module root is two levels up from
tests/e2e/cmd? No — the test package lives in tests/e2e/, so module root is
"../.." . Verify by computing: tests/e2e/ -> e2e -> tests -> root = "../.." .)

Do NOT place fenced code blocks here — this prose IS the spec; write idiomatic Go
mirroring integration_test.go style (t.Helper, t.Fatalf with context, defer
cleanup).
  </action>
  <verify>
With OTTO_E2E unset, the e2e binary must compile and every subtest must skip:
`go vet -tags e2e ./tests/e2e/...` is clean AND
`go test -tags e2e ./tests/e2e/` (OTTO_E2E unset) reports ok with all skips
(no kiro, no binary boot, no node). Also confirm the default path is untouched:
`go build ./...` clean, `go test ./... -race -count=1` green and does NOT compile
e2e_test.go (it is behind the `e2e` tag).
  </verify>
  <done>
tests/e2e/e2e_test.go exists with //go:build e2e + package e2e_test, imports only
stdlib, TestMain builds a temp binary only when OTTO_E2E=1, bootGateway boots the
real binary on a free loopback port and skips on warmup failure, and all subtests
gate on OTTO_E2E. `go vet -tags e2e ./tests/e2e/...` and
`go test -tags e2e ./tests/e2e/` (gate off) both pass with skips; plain
`go test ./... -race` stays green and never compiles the e2e file.
  </done>
</task>

<task type="auto">
  <name>Task 2: Markdown report renderer — go test -json -> stdout markdown</name>
  <files>tests/e2e/cmd/report/main.go</files>
  <action>
Create `tests/e2e/cmd/report/main.go` — `package main`, NO build tag (it must
compile and run under the default toolchain so `go run ./tests/e2e/cmd/report`
works without the e2e tag). Stdlib only: bufio, encoding/json, flag, fmt, os,
os/exec, sort, strings, time.

Define an event struct matching `go test -json` lines:
`type event struct { Action string; Test string; Elapsed float64; Output string }`
(json tags: Action, Test, Elapsed, Output — these are the capitalized field
names go test emits; with Go's default case-insensitive JSON matching the struct
field names already match, but add explicit json:"action" / "test" /
"elapsed" / "output" tags to be safe).

Read NDJSON from os.Stdin line-by-line with bufio.Scanner (bump
scanner.Buffer to ~1MiB for large Output lines). For each line json.Unmarshal
into event; ignore lines that fail to parse (go test can emit non-JSON noise) and
events with empty Test (package-level events). Accumulate per Test name:
  - final result = last seen terminal Action in {"pass","fail","skip"}
  - elapsed = the Elapsed from the terminal event (pass/fail/skip carry it)
  - output = concatenation of all Output strings for that test (kept for the
    Failures section).

A `-version` flag (string, default ""): if empty, attempt
`git describe --tags --always --dirty` via exec.Command (best-effort; on error
fall back to "unknown"). Trim the output.

Write markdown to os.Stdout (emoji-free):
  - `# OTTO Gateway E2E Report`
  - a line with generated timestamp (time.Now().UTC().Format(time.RFC3339)) and
    version.
  - a summary line with counts: pass / fail / skip / total.
  - a results table: `| Test | Result | Duration |` header + separator, one row
    per test (sort test names for stable output), Duration formatted like
    "1.23s" (fmt %.2f on Elapsed). Use plain words PASS / FAIL / SKIP for Result.
  - a `## Failures` section: for each failed test, a `### <name>` heading and a
    fenced ```text block with the captured output (only emit the section if there
    is at least one failure).

The renderer must never exit non-zero on its own for normal input — its exit code
is irrelevant; the Makefile preserves go test's exit code separately.

Do NOT place fenced code blocks in this action; write the Go directly in the file.
  </action>
  <verify>
`go build ./tests/e2e/cmd/report` is clean and `go vet ./tests/e2e/cmd/report`
is clean. Smoke test the renderer on a synthetic stream (executor: pipe a few
hand-written go-test-json lines — at least one pass, one fail with Output, one
skip — into `go run ./tests/e2e/cmd/report` and confirm stdout is valid markdown
containing the table header `| Test | Result | Duration |` and a `## Failures`
section for the failing test).
  </verify>
  <done>
tests/e2e/cmd/report/main.go compiles with the default toolchain (no build tag),
reads go-test-json from stdin, and writes emoji-free markdown with a header
(timestamp + version + counts), a | Test | Result | Duration | table, and a
Failures section carrying captured output for failed tests.
  </done>
</task>

<task type="auto">
  <name>Task 3: Opt-in Node SDK harness (package.json + sdk_roundtrip.mjs + README)</name>
  <files>tests/e2e/sdk/package.json, tests/e2e/sdk/sdk_roundtrip.mjs, tests/e2e/sdk/README.md</files>
  <action>
Create `tests/e2e/sdk/package.json`: JSON object with
"name":"otto-e2e-sdk", "private":true, "version":"0.0.0", "type":"module",
"scripts":{"test":"node sdk_roundtrip.mjs"},
"dependencies":{"@anthropic-ai/sdk":"^0.90.0"}.

Create `tests/e2e/sdk/sdk_roundtrip.mjs` (ESM, no assertion libs):
  - `import Anthropic from "@anthropic-ai/sdk";`
  - Read baseURL and apiKey from process.env.ANTHROPIC_BASE_URL and
    process.env.ANTHROPIC_API_KEY; construct
    `const client = new Anthropic({ baseURL, apiKey });`.
  - Wrap everything in an async main() inside try/catch.
  - (a) Non-streaming: `const msg = await client.messages.create({ model:"auto",
    max_tokens:256, messages:[{role:"user",content:"say hi"}] });` — throw new
    Error if !msg.content?.[0] || msg.content[0].type!=="text" ||
    !msg.content[0].text. console.log a concise PASS line with a text preview.
  - (b) Streaming: `const stream = client.messages.stream({ model:"auto",
    max_tokens:256, messages:[{role:"user",content:"say hi"}] });` collect event
    types via `stream.on("streamEvent", (e)=> seen.add(e.type))` (or iterate
    `for await (const ev of stream)` pushing ev.type) ; then
    `const final = await stream.finalMessage();` — throw if the final message has
    no non-empty text OR if "message_start"/"message_stop" were not among the
    seen event types. console.log a concise PASS line.
  - On success: console.log("E2E SDK: PASS") and process.exit(0).
  - In catch: console.error the error (including SDK Zod parse errors — print
    err.message and, if present, err.stack) and process.exit(1).
  - Call main() at module top level (it returns a promise; let unhandled
    rejection -> nonzero, but the explicit catch already handles it).

Create `tests/e2e/sdk/README.md`: short doc — explain this is the opt-in Node
harness that enables HUMAN-UAT steps 4-5; one-time setup is `make e2e-sdk-setup`
(or `cd tests/e2e/sdk && npm install`); it is invoked automatically by the Go
TestE2E_SDK_RoundTrip subtest when node + node_modules (or OTTO_E2E_SDK=1) are
present, otherwise that subtest skips. Emoji-free.

Note: this task installs NO node modules (node_modules is gitignored in Task 4
and created on demand by make e2e-sdk-setup). package.json and the .mjs and the
README stay tracked.
  </action>
  <verify>
`node --check tests/e2e/sdk/sdk_roundtrip.mjs` reports no syntax errors (if node
is available in the executor env; if node is absent, skip this check and rely on
manual review). `python3 -c "import json,sys; json.load(open('tests/e2e/sdk/package.json'))"`
confirms package.json is valid JSON. Confirm no node_modules/ is committed.
  </verify>
  <done>
tests/e2e/sdk/ contains a valid private ESM package.json pinning
@anthropic-ai/sdk ^0.90.0, a syntactically valid sdk_roundtrip.mjs that does a
non-streaming + streaming round-trip and exits 0/1 on success/failure, and a
README documenting the opt-in setup. No node_modules committed.
  </done>
</task>

<task type="auto">
  <name>Task 4: Makefile e2e + e2e-sdk-setup targets and .gitignore entries</name>
  <files>Makefile, .gitignore</files>
  <action>
Edit `Makefile`:
  1. Add `e2e` and `e2e-sdk-setup` to the `.PHONY` line (line 11).
  2. Add an `e2e` target that depends on `build`, ensures the reports dir exists,
     runs the e2e suite with OTTO_E2E=1 and the `e2e` tag, captures go test's
     EXIT CODE portably (do NOT rely on bash PIPESTATUS — the project Makefile has
     no `SHELL := bash` override and must stay POSIX-portable), and ALWAYS renders
     the report regardless of pass/fail. Use the tmpfile exit-code approach in a
     single shell recipe line (continued with backslashes):
        - `@mkdir -p tests/e2e/reports`
        - in one `@TS=...; ...` block: compute TS via `date +%Y%m%d-%H%M%S`; run
          `OTTO_E2E=1 go test -tags e2e -json -v ./tests/e2e/` writing JSON to
          `tests/e2e/reports/raw.jsonl` AND capturing its exit code to a tmp rc
          file. Concretely run the test in a subshell that records `$?`:
          `( OTTO_E2E=1 go test -tags e2e -json -v ./tests/e2e/ > tests/e2e/reports/raw.jsonl; echo $$? > tests/e2e/reports/rc )` ;
          then render: `go run ./tests/e2e/cmd/report < tests/e2e/reports/raw.jsonl > tests/e2e/reports/REPORT-$$TS.md` ;
          then `cp tests/e2e/reports/REPORT-$$TS.md tests/e2e/reports/LATEST.md` ;
          then echo the report path ; then `exit $$(cat tests/e2e/reports/rc)`.
       Give the target the help comment:
       `## Run E2E suite (real binary + kiro) and write a markdown report`
       (Remember: in a Make recipe, `$?` and `$#` must be written `$$?` so Make
       passes them to the shell; `$$TS` references the shell var.)
  3. Add an `e2e-sdk-setup` target with help comment
     `## Install the opt-in Node SDK harness (enables E2E steps 4-5)` whose recipe
     is `cd tests/e2e/sdk && (pnpm install || npm install)`.
  4. Do NOT add e2e or e2e-sdk-setup to `all` (line 13) or `ci` (line 58).
  5. The help target's awk already auto-lists any `name: ... ## comment` target,
     so both new targets list automatically — confirm by running `make help` and
     seeing `e2e` and `e2e-sdk-setup`.

Edit `.gitignore`: append two lines —
  `tests/e2e/reports/`
  `tests/e2e/sdk/node_modules/`
Leave package.json + sdk_roundtrip.mjs + README.md tracked (do NOT ignore the
sdk/ dir itself).
  </action>
  <verify>
`make help` lists `e2e` and `e2e-sdk-setup` with their descriptions.
`make -n e2e` dry-run prints a recipe that builds, mkdir -p tests/e2e/reports,
runs `OTTO_E2E=1 go test -tags e2e -json` and pipes/redirects into
`go run ./tests/e2e/cmd/report`, and exits with the captured test rc.
Confirm `make test` recipe is unchanged and does NOT reference e2e
(grep the Makefile: the `test:` recipe is still `go test ./...`).
`git check-ignore tests/e2e/reports/x tests/e2e/sdk/node_modules/x` matches both,
and `git check-ignore tests/e2e/sdk/package.json` does NOT match (still tracked).
  </verify>
  <done>
Makefile has `e2e` (depends on build, always renders a report, preserves test
exit code via a portable tmpfile rc capture — no bash PIPESTATUS) and
`e2e-sdk-setup` targets, both auto-listed by `make help`, and neither is wired
into `all` or `ci`. .gitignore ignores tests/e2e/reports/ and
tests/e2e/sdk/node_modules/ while keeping the SDK source files tracked.
  </done>
</task>

</tasks>

<verification>
Acceptance gates (run from the module root, all must pass before SUMMARY):
1. `go build ./...` — clean (no e2e file compiled; it is behind the `e2e` tag).
2. `go vet ./...` — clean.
3. `go test ./... -race -count=1` — green AND does not compile tests/e2e/e2e_test.go.
4. `go vet -tags e2e ./tests/e2e/...` — clean.
5. `go test -tags e2e ./tests/e2e/` with OTTO_E2E UNSET — all subtests skip, exit 0,
   no binary build, no kiro, no node.
6. Report renderer: `go build ./tests/e2e/cmd/report` clean; a sample
   `go test -json` stream piped into `go run ./tests/e2e/cmd/report` yields valid
   markdown with the `| Test | Result | Duration |` table.
7. `make test` does NOT run e2e (recipe still `go test ./...`); `make help` lists
   both new targets; `make -n e2e` shows the build + render + rc-preserving recipe.
8. Running the full `make e2e` against real kiro is the orchestrator's job after
   this plan — NOT required inside the executor.
</verification>

<success_criteria>
- All 7 files exist with the exact behaviors above.
- Default Go workflow (build / test / test-race / ci / vet) stays green with no
  node and no OTTO_E2E, and never compiles the e2e file.
- e2e suite compiles under `-tags e2e` and gate-skips cleanly when OTTO_E2E is unset.
- Report renderer compiles under the default toolchain and produces valid markdown.
- Node harness is opt-in and skips cleanly when absent.
- No new go.mod dependencies; no edits to internal/ or cmd/ source.
</success_criteria>

<output>
Create `.planning/quick/260524-pee-e2e-suite/260524-pee-SUMMARY.md` when done.
</output>
