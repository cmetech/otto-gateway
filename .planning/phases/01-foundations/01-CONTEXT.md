# Phase 1: Foundations - Context

**Gathered:** 2026-05-23
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 1 stands up the Go scaffold with strict trust-gate tooling and a working ACP JSON-RPC client over `kiro-cli` stdio — the runnable skeleton every later phase builds on.

**Deliverables (per ROADMAP.md success criteria):**
1. `make build` produces `bin/loop24-gateway` that serves `GET /health` returning empty pool/registry/embedding stats on `:11434`.
2. `make lint` runs strict `golangci-lint` config with zero findings on the scaffold.
3. `make test-race` passes; `govulncheck` runs clean.
4. Standalone integration test spawns `kiro-cli acp`, completes `initialize` + `session/new` + `ping` + auto-grant `session/request_permission` + translate `session/update` into a typed canonical chunk — no goroutine leaks, no hung subprocesses.
5. Pre-commit hooks installed and blocking bad commits locally.

**Plus (added during discuss):**
6. Wrapper scripts (`scripts/loop24` POSIX shell + `scripts/loop24.ps1` PowerShell) provide `start | stop | status | restart | logs` lifecycle control for developer laptops on macOS, Linux, and Windows.

**Requirements covered:** ACP-01, ACP-02, ACP-03, ACP-04, ACP-05, ACP-06, BLD-01, TRST-01, TRST-02, TRST-03, TRST-08.

**Explicitly NOT in Phase 1:** any adapter code (Phase 2/3), HTTP surfaces beyond `/health` + `/api/version` (Phase 2+), pool / sessions (Phase 5), tool-call coercion (Phase 6), embeddings (Phase 7), plugin hooks (Phase 8), cross-compile CI (Phase 9).

</domain>

<decisions>
## Implementation Decisions

### ACP client architecture (`internal/acp`)

- **D-01:** Single package, file-scoped layers. `internal/acp/{framer.go, dispatcher.go, client.go}` with unexported types for the framer (stdio NDJSON framing) and dispatcher (id correlation + pending-response map). Exported surface: `Client` plus `Initialize`, `NewSession`, `Prompt`, `Cancel`, `Ping`, `Close`. Rationale: Go community lean toward "one package until you have a real reason to split"; single consumer + single transport doesn't justify sub-packages.

- **D-02:** `context.Context`-first cancellation. Every exported `Client` method takes `ctx` as first arg. Inside `Prompt`, a `select { case <-ctx.Done(): ... ; case resp := <-respCh: ... }` handles HTTP-client-disconnect by sending a best-effort `session/cancel` JSON-RPC notification, removing the id from the pending map, and returning `ctx.Err()`. Lifetime context for reader/writer goroutines is owned by the `Client` (set in `New`, cancelled in `Close`).

- **D-03:** Streaming channel from day 1. `Client.Prompt(ctx, sid, p)` returns a `*Stream` handle exposing `Chunks <-chan canonical.Chunk` and a `Result() (*FinalResult, error)` method valid after `Chunks` closes. No buffer-and-return in Phase 1; Phase 4 wires the channel straight into NDJSON/SSE encoders.

- **D-04:** Translation lives inside `internal/acp`. Raw JSON-RPC `session/update` (and `_kiro.dev/session/update`) payloads are converted to typed `canonical.Chunk` values (text / thought / tool_call / plan) before anything leaves the package. `internal/acp` imports `internal/canonical` (allowed — `canonical` is the leaf with no internal imports). Mirrors the Bifrost provider-package pattern.

- **D-05:** Config-struct constructor, not functional options. `acp.New(acp.Config{Logger, Command, Args, Cwd, Env, PingInterval})`. Matches stdlib (`http.Server`, `net.Dialer`, `exec.Cmd`). Required field: `Logger`. All others have zero-value defaults; defaults are grep-able in one place.

- **D-06:** Dual constructors for subprocess ownership. `acp.New(cfg)` spawns the subprocess (Phase 1 convenience + the integration test). `acp.NewWithConn(rwc io.ReadWriteCloser, cfg)` accepts a pre-built connection so Phase 5's pool can own `*exec.Cmd` lifecycle. Shared internals via an unexported `newClient(rwc, cfg)` helper. Stdlib pattern: `http.NewRequest` vs `NewRequestWithContext`, `database/sql.Open` vs `sql.OpenDB`.

- **D-07:** `Close()` semantics — Cancel + drain + Wait. Idempotent (via `sync.Once`). Cancels client-lifetime context, closes subprocess stdin (signals EOF), waits for reader/writer goroutines via `sync.WaitGroup`, calls `cmd.Wait()` if subprocess-owned. Returns first error encountered. In-flight `Prompt()` callers see `context.Canceled` from their `select`. `goleak` passes after `Close` returns.

### Trust-gate plumbing

- **D-08:** No hosted CI in Phase 1. Local-only trust gates via pre-commit hooks + Makefile target. CI host (GitLab vs GitHub Actions) decided when the bare module name is resolved before first remote push.

- **D-09:** Pre-commit framework = `pre-commit` (Python). Ships `.pre-commit-config.yaml` pinning `golangci-lint`, `gitleaks`, `go-mod-tidy-repo`, `go-vet-repo-mod` from their respective upstream hook repos. Rationale: author already runs Python; golangci-lint and gitleaks docs both use this framework; widest ecosystem.

- **D-10:** Brief-spirit mid-path trust-gate scope. Phase 1 ships:
  - TRST-01 `golangci-lint` strict (errcheck, errorlint, gosec, staticcheck, revive, wrapcheck, ineffassign, unused, unparam, nilerr, noctx, bodyclose) — zero findings on the scaffold
  - TRST-02 `govulncheck` (`make ci` target)
  - TRST-03 `go test -race ./...` (`make test-race` is the CI default)
  - TRST-04 `go-arch-lint` scaffolded with empty ruleset; rules activate in Phase 2 once first adapter↔canonical↔engine flow exists
  - TRST-05 `goleak.VerifyTestMain` at top of `internal/acp` test package (catches reader/writer goroutine leaks)
  - TRST-08 pre-commit hooks installed locally
  Deferred (no current consumer): TRST-06 property tests → Phase 6 (`coerceToolCall`); TRST-07 `Example_` functions → added per phase as public funcs land.

### HTTP scaffold (`internal/server`, `internal/config`)

- **D-11:** Phase 1 exposes `GET /health` + `GET /api/version`. `/api/version` lives in `internal/server` for now; Phase 2 moves it into `internal/adapter/ollama` (trivial 5-line refactor — Ollama-shaped response).

- **D-12:** `/health` JSON shape (locked contract; additive-only across phases):
  ```json
  {
    "status": "ok",
    "version": "<embedded via -ldflags>",
    "uptime_seconds": 0,
    "pool": { "size": 0, "alive": 0, "busy": 0 },
    "sessions": { "active": 0 },
    "embeddings": { "models_loaded": 0 }
  }
  ```
  Go types live in `internal/server/health.go`: `HealthResponse`, `PoolStats`, `SessionStats`, `EmbeddingStats`. Later phases populate the sub-stats as their subsystems come online.

- **D-13:** Middleware chain (chi router). `middleware.RequestID` + `middleware.Recoverer` + custom `accessLog(logger)` that emits one `slog` line per request with `request_id`, `method`, `path`, `status`, `duration`. PLUG-04's `RequestIDHook` (Phase 8) is a different layer (canonical-engine plugin chain) and complements, not replaces, the HTTP middleware.

- **D-14:** Config loading via `internal/config` package with stdlib `os.Getenv`. Typed `Config` struct, single `Load() (Config, error)` entry point, small per-type helpers (`getEnvStr`, `getEnvStrSlice`, `getEnvInt`, `getEnvDuration`, `getEnvBool`). No third-party deps. Refines Bifrost's pattern (which inlines env reads in `main.go`) because our env surface is ~15+ vars across phases — too many for `main.go`.

- **D-15:** `slog.Logger` propagation — explicit dependency injection. `main.go` constructs one `*slog.Logger` (`slog.NewJSONHandler` over `os.Stdout`, level from `cfg.LogLevel()`). Logger passed via `Config` structs into `internal/server`, `internal/acp`. Access-log middleware derives a per-request child via `logger.With("request_id", id)` and stashes it in `r.Context()`; handlers retrieve via a small `LoggerFromCtx` helper. No `slog.SetDefault` — no global state.

- **D-16:** Graceful shutdown via `http.Server.Shutdown(ctx)` on `SIGINT`/`SIGTERM`. Standard Go idiom. 30s shutdown deadline; ACP `Client.Close()` invoked during shutdown to drain reader/writer goroutines.

### ACP integration test gating

- **D-17:** Auto-skip via PATH detection + `LOOP24_KIRO_BIN` env override. Integration test file always compiles. At test start: resolve binary path (env var first, else `exec.LookPath("kiro-cli")`); `t.Skip(...)` if neither found. Loud SKIP on machines without `kiro-cli`; auto-runs on dev box (where `kiro-cli 2.4.1` is already on PATH at `~/.local/bin/kiro-cli`). `make test` stays hermetic.

- **D-18:** Test package layout — hybrid whitebox + blackbox:
  - `internal/acp/framer_test.go`, `dispatcher_test.go`, `client_test.go` — `package acp` (whitebox; unit-tests unexported types via `bytes.Buffer` / `io.Pipe`)
  - `internal/acp/integration_test.go` — `package acp_test` (blackbox; exercises only public API; gated per D-17)
  - `internal/acp/testmain_test.go` — `goleak.VerifyTestMain` per TRST-05

- **D-19:** Test logger convention — hand-rolled `internal/testutil.Logger(t)` helper. Returns a `*slog.Logger` that routes output to `t.Log` via a small `testWriter`. Zero deps. Used by every test in `internal/`.

### Operational lifecycle (laptop deployment)

- **D-20:** Phase 1 ships two wrapper scripts for developer-laptop control:
  - `scripts/loop24` — POSIX shell, works on macOS + Linux. Subcommands: `start | stop | status | restart | logs [-f] | run`. Background-launches the binary, writes PID to `/tmp/loop24-gateway.pid`, redirects logs to `/tmp/loop24-gateway.log`. `status` combines PID-alive check with HTTP `GET /health` (prints version + uptime + pool/session counts).
  - `scripts/loop24.ps1` — PowerShell, works on Windows. Same subcommands. Uses `Start-Process` with redirected stdout/stderr; PID/log files in `$env:TEMP`. `status` uses `Get-Process` + `Invoke-RestMethod`.
  Both scripts accept env-var overrides (`LOOP24_BIN`, `LOOP24_PID`, `LOOP24_LOG`, `LOOP24_ADDR`) and pass through gateway env vars (`KIRO_CMD`, `KIRO_ARGS`, `DEBUG`, `HTTP_ADDR`, etc.).

- **D-21:** Makefile adds `make start | stop | status` targets that delegate to the host-appropriate wrapper. `make run` keeps the existing foreground behavior.

- **D-22:** The Go binary stays a single foreground process. **Never** add `start`/`stop`/`status` subcommands to the binary itself — the wrappers own process supervision, the binary owns the gateway logic. This keeps a future systemd / Windows Service path clean (deferred to a later milestone — "at some point" per the user, not Phase 1).

- **D-23:** Docs additions: README gets a "Running" section showing both wrappers; `docs/operating.md` (new) covers PID/log locations, env-var overrides, and how status is computed.

### Claude's Discretion

The planner has explicit latitude on (these were either implied by Go best practices or marked as planner-judgment during discussion):

- Pending-request map sync primitive (`sync.Mutex` over `map[id]chan` is the conventional choice over `sync.Map`; planner confirms)
- Exact sentinel error values exposed by `internal/acp` (e.g., `ErrSessionClosed`, `ErrSubprocessExited`) — driven by what tests need to assert on
- Exact wording of `t.Skip` messages, log line keys (`request_id` vs `req_id`, `error` vs `err` — pick once, lint will enforce consistency)
- Whether `internal/testutil` is a new package or a sub-package of `internal/server` (recommendation: new package, used by multiple tests)
- The exact 30s shutdown deadline value (any 10–60s default is fine)
- Whether `/health` returns 503 when ACP subprocess is dead in Phase 1 (probably 200 in Phase 1 since pool doesn't exist yet; reassessed in Phase 5)
- Specific `golangci-lint` config file structure (planner copies brief §3.12 linter list verbatim)

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Spec of record (must-read)
- `docs/briefs/go_port_brief.md` — full ~1000-line Go-port design brief. Sections especially relevant to Phase 1:
  - §2: Why Go over Rust (cross-compile + first-Go-project rationale)
  - §3.4: Embeddings backend (sidecar provisional — not Phase 1 but informs `internal/embed` scaffold)
  - §3.8: Architectural layer invariants (drives TRST-04 ruleset)
  - §3.9: Cross-compile (Phase 9 detail, but already wired in current Makefile)
  - §3.12: Trust gates — non-negotiable from day-one (D-08 through D-10)
  - §3.13: Adapter-over-canonical layout (`internal/adapter/{ollama,openai}` ↔ `internal/canonical` ↔ `internal/engine`)
  - §5: M0–M9 milestone plan (Phase 1 = M0+M1 collapsed)
- `docs/reference/acp_server_node_reference.md` — Node implementation behavioral reference. Sections especially relevant to Phase 1:
  - "Things that must survive the port" — ACP behaviors (auto-grant permissions, pool warmup, client-disconnect cancellation, longest-common-parent `cwd`, 60s ping)
  - JSON-RPC message shapes for `initialize`, `session/new`, `session/prompt`, `session/cancel`, `ping`, `session/request_permission`, `session/update`, `_kiro.dev/session/update`
  - `/health` endpoint description (exact JSON shape NOT specified — we define a Go-idiomatic shape in D-12)

### Planning context (must-read)
- `.planning/PROJECT.md` — Loop24 Gateway project overview, constraints, decisions table, evolution rules
- `.planning/REQUIREMENTS.md` — v1 requirements list (Phase 1 = ACP-01..06, BLD-01, TRST-01..03, TRST-08)
- `.planning/ROADMAP.md` §"Phase 1: Foundations" — phase goal, mode (mvp), depends-on, success criteria
- `.planning/STATE.md` — current project state; will be updated post-Phase-1

### Reference architecture (read as needed)
- `../bifrost/transports/bifrost-http/main.go` — env-var loading pattern (`os.Getenv` + `flag`); validates D-14
- `../bifrost/transports/bifrost-http/server/server.go` — HTTP transport composition pattern
- `../bifrost/cli/internal/config/config.go` — Bifrost's on-disk config (not env config; different concern; useful only as a style reference)
- `../bifrost/` (root) — overall layout; Bifrost is our "shape donor" per PROJECT.md

### External (look up via researcher, not local files)
- chi router docs: middleware composition (`r.Use`), built-in middleware (`RequestID`, `Recoverer`, `Logger`)
- `log/slog` package docs: handler construction, attribute composition via `With`
- `go.uber.org/goleak` docs: `VerifyTestMain` setup and known-leak suppression
- `github.com/golangci/golangci-lint` docs: strict config patterns; v1.62+ linter list
- `github.com/gitleaks/gitleaks` pre-commit hook docs
- `golang.org/x/vuln/cmd/govulncheck` docs
- `pre-commit` framework docs (`.pre-commit-config.yaml` schema)
- `go-arch-lint` (or equivalent) — layered-architecture lint rule format
- Zed ACP protocol reference — needed for `session/*` RPC method shapes (also documented in `docs/reference/acp_server_node_reference.md` but the Zed source is authoritative)

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- `cmd/loop24-gateway/main.go` — current 30-line stub already wires `version` via `-ldflags` and reads `vcs.revision` from `debug.ReadBuildInfo()`. Phase 1 expands this to construct config, logger, server, and ACP client.
- `Makefile` — `build`, `run`, `test`, `test-race`, `lint`, `fmt`, `tidy`, `clean`, `cross` all already exist with correct shape. Phase 1 ADDS `start`, `stop`, `status`, `ci`. The existing `cross` targets (linux/amd64, windows/amd64) are correct but cross-compile validation is Phase 9.
- `internal/{acp,adapter/ollama,adapter/openai,canonical,config,embed,engine,plugin,pool,server,version}/.gitkeep` — package directories already scaffolded matching brief §3.13. Phase 1 fills in `acp`, `config`, `server`, `version`; later phases fill the rest.
- `bin/loop24-gateway*` — three binaries currently exist (host, linux-amd64, windows-amd64) from a previous `make all && make cross` run. Phase 1 work will overwrite these; not load-bearing.

### Established Patterns

- **Adapter-over-canonical layout** (PROJECT.md + brief §3.13) — Phase 1 doesn't touch adapters, but the layout constraints inform `internal/acp` and `internal/canonical` placement. `internal/canonical` imports nothing under `internal/`; `internal/acp` imports `internal/canonical` (per D-04).
- **Env-var contract from Node version** — All env vars use Node names (`KIRO_CMD`, `KIRO_ARGS`, `POOL_SIZE`, `SESSION_TTL_MS`, `AUTH_TOKEN`, `ALLOWED_IPS`, `DEBUG`, `EMBEDDING_MODEL_DEFAULT`). Phase 1 reads: `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `DEBUG`, `PING_INTERVAL`, `HTTP_ADDR`. Other env vars come online in later phases.
- **`log/slog` over zerolog/zap** (PROJECT.md Constraints + author preference) — locked. All packages take `*slog.Logger` via Config.
- **`chi` over stdlib mux or fasthttp** (REQUIREMENTS + PROJECT.md Constraints) — locked. `internal/server` constructs the `chi.Router`.

### Integration Points

- `cmd/loop24-gateway/main.go` is the dependency-wiring root. Phase 1 turns it into roughly:
  ```go
  cfg := mustLoadConfig()
  logger := buildLogger(cfg)
  acpClient := mustNewACP(cfg, logger)          // for the Phase 1 integration test; not yet used by handlers
  srv := mustNewServer(cfg, logger, version)    // /health, /api/version, middleware
  srv.RunUntilSignal(ctx)                        // SIGINT/SIGTERM → graceful shutdown → acpClient.Close()
  ```
- `internal/server.New(...)` constructs the chi router, registers `/health` + `/api/version`, wires middleware. `Server.RunUntilSignal(ctx)` owns the `http.Server.Shutdown` dance.
- Wrapper scripts in `scripts/` operate outside the binary entirely — they spawn `./bin/loop24-gateway`, manage PID file, and tail logs. The binary doesn't know they exist.

</code_context>

<specifics>
## Specific Ideas

- **`kiro-cli` is at `/Users/coreyellis/.local/bin/kiro-cli` (v2.4.1)** — confirmed during discuss. Symlinked from `/Applications/Kiro CLI.app/Contents/MacOS/kiro-cli`. The integration test's `exec.LookPath` auto-detect works against this immediately on the dev box.
- **`docs/reference/acp_server_node_reference.md` does NOT specify the exact `/health` JSON shape** — only describes it as "pool + registry + embeddings stats". D-12 defines a Go-idiomatic shape as the contract; later phases extend it additively.
- **The Node reference repo (`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`) is NOT checked out locally** at the path PROJECT.md references. Only the documentation doc is available. If the researcher needs the original Node source for ACP wire-format details beyond what `acp_server_node_reference.md` covers, they'll need to clone it or get a copy from the user.
- **Bifrost's `transports/bifrost-http/main.go` uses `os.Getenv` + `flag` directly in `init()`** — confirmed by reading. We deliberately diverge (`internal/config` package) because our env surface is bigger.
- **`docs/architecture/architecture-overview.png`** is referenced in README but its presence/state was not verified during discuss — likely fine but the planner should confirm before producing Phase 1 docs that reference it.

</specifics>

<deferred>
## Deferred Ideas

- **Service mode (systemd unit + Windows Service)** — User wants this "at some point" but not Phase 1. The wrapper scripts (D-20) explicitly preserve the option: never add `start`/`stop` subcommands to the binary, so a future systemd / NSSM wrapping stays clean. Candidate phases: Phase 9 (Distribution) or a follow-up packaging milestone.
- **Hosted CI (GitLab CI vs GitHub Actions)** — Deferred per D-08 until bare module name (`loop24-gateway`) is resolved before first remote push.
- **Architectural boundary rules** (`go-arch-lint` ruleset) — Scaffolded in Phase 1 (D-10) but rules activate in Phase 2 once the first `adapter↔canonical↔engine` flow exists.
- **Property tests** (TRST-06) — Phase 6 when `coerceToolCall` exists. Nothing in Phase 1 is interesting to property-test.
- **`Example_` documentation functions** (TRST-07) — Added per phase as public functions worth documenting land (`coerceToolCall`, `pickCwd`, `buildAcpBlocks` etc., none of which exist in Phase 1).
- **JSON encoding choice** (`encoding/json` vs `sonic` / `jsoniter`) — Phase 4 (Streaming) when throughput matters. Phase 1 uses stdlib `encoding/json` everywhere.
- **`/health` returning 503 when ACP is dead** — Phase 5 (Pool) when there's actually something to be down. Phase 1 always returns 200 from `/health`.
- **Health probe path aliases** (`/healthz` for k8s, etc.) — Not needed for laptop deployment model; revisit if/when container deployment becomes a target.
- **`docker/Dockerfile`** — Out of scope per project deployment model (laptop tool, not container service).
- **Pre-commit hook for `gofumpt`** — Brief mentions `gofumpt -d` in CI matrix (TRST-09 spirit); add to pre-commit when CI lands.

</deferred>

---

*Phase: 1-Foundations*
*Context gathered: 2026-05-23*
