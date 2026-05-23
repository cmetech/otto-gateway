# Phase 1: Foundations - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in `01-CONTEXT.md` — this log preserves the alternatives considered.

**Date:** 2026-05-23
**Phase:** 1-Foundations
**Areas discussed:** ACP client layering, Trust-gate plumbing scope, HTTP scaffold scope + /health shape, ACP integration test gating, Operational lifecycle (raised by user mid-discussion)

---

## ACP client layering

### Q1 — Package layering

| Option | Description | Selected |
|--------|-------------|----------|
| Single package, file-scoped layers | `internal/acp/{framer.go, dispatcher.go, client.go}`. Unexported framer + dispatcher types. Idiomatic Go for single-consumer single-transport client. | ✓ |
| Sub-packages: `internal/acp/jsonrpc` + `internal/acp` | Bifrost-style clean split. Justifiable only with a second consumer (we have none). | |
| Flat package, no internal layers | One tightly-coupled struct. Hard to test framer/dispatcher in isolation. | |

**User's choice:** Single package, file-scoped layers (Recommended).
**Notes:** Framing emphasized Go community lean toward "one package until you have a real reason to split"; user is new to Go, wanted explicit best-practice rationale.

### Q2 — Cancellation flow

| Option | Description | Selected |
|--------|-------------|----------|
| `ctx`-first, `select`-on-Done in `Prompt` | Every method takes `ctx`; `Prompt` selects on `ctx.Done()` and sends best-effort `session/cancel`. Matches stdlib idiom. | ✓ |
| Explicit `Cancel(sessionID)` method | Handler imperatively calls `Cancel`. Less idiomatic. | |
| Kill-the-subprocess on cancel | Cancel `ctx` → kill subprocess. Kills warm pool slot. Wrong. | |

**User's choice:** `ctx`-first, `select`-on-Done.
**Notes:** Cited `context.Context` package docs and the AbortSignal → context.Context analogy from the Node reference.

### Q3 — Streaming API shape

| Option | Description | Selected |
|--------|-------------|----------|
| Streaming channel from day 1 | `*Stream` with `Chunks <-chan canonical.Chunk` + `Result()`. Phase 4 just wires it. | ✓ |
| Buffer in Phase 1, refactor in Phase 4 | Return `([]Chunk, *Result, error)`. Creates known rework. | |
| Visitor / sink callback | `func(chunk) error` callback. Less idiomatic in modern Go. | |

**User's choice:** Streaming channel from day 1.
**Notes:** Reader goroutine already produces chunks one-at-a-time per ACP-02; buffering would throw away streaming we want back in Phase 4.

### Q4 — Translation location

| Option | Description | Selected |
|--------|-------------|----------|
| Inside `internal/acp` | `acp.Client.Prompt` returns `canonical.Chunk`. Mirrors Bifrost provider-package pattern. | ✓ |
| Inside `internal/engine` | Engine orchestrates AND translates. Mixes concerns. | |
| Separate `internal/canonicalize` package | Dedicated translator pkg. Overkill for one direction × one source. | |

**User's choice:** Inside `internal/acp`.
**Notes:** Allowed because `canonical` is the leaf — `acp` importing `canonical` doesn't break the architectural boundary check.

### Q5 — Constructor signature

| Option | Description | Selected |
|--------|-------------|----------|
| Config struct | `acp.New(acp.Config{...})`. Matches stdlib (`http.Server`, `net.Dialer`, `exec.Cmd`). | ✓ |
| Functional options | `acp.New(acp.WithLogger(l), ...)`. More compositional, more code. | |
| Positional args | Brittle to new options. | |

**User's choice:** Config struct.
**Notes:** Pendulum has swung back to Config structs in modern Go for simple cases (Mills, Cox).

### Q6 — Subprocess ownership

| Option | Description | Selected |
|--------|-------------|----------|
| Both: convenience + bring-your-own-conn | `New(cfg)` spawns; `NewWithConn(rwc, cfg)` takes `io.ReadWriteCloser` for Phase 5's pool. | ✓ |
| Only `NewWithConn` | Cleanest separation; Phase 1 test gets boilerplate. | |
| Only `New` | Forces Phase 5 rework. | |

**User's choice:** Both: convenience + bring-your-own-conn.
**Notes:** Stdlib pattern (`http.NewRequest` vs `NewRequestWithContext`).

---

## Trust-gate plumbing scope

### Q7 — CI host

| Option | Description | Selected |
|--------|-------------|----------|
| Defer — local trust gates only | Pre-commit + Makefile target only; no `.gitlab-ci.yml` / `.github/workflows/` yet. | ✓ |
| GitLab CI (matches Node reference) | Ship `.gitlab-ci.yml`. | |
| GitHub Actions | Ship `.github/workflows/ci.yml`. | |
| Both | Two CI configs to keep in sync. | |

**User's choice:** Defer — local trust gates only.
**Notes:** Matches "local-only at boot" stance in PROJECT.md; CI host decision deferred until bare module name resolved.

### Q8 — Pre-commit framework

| Option | Description | Selected |
|--------|-------------|----------|
| `pre-commit` (Python) | Author already runs Python; official golangci-lint + gitleaks + go-mod-tidy hooks exist. | ✓ |
| `lefthook` | Go-native single binary; smaller ecosystem. | |
| Plain `.githooks/` | Zero deps; you write all the glue. | |

**User's choice:** `pre-commit` (Python).

### Q9 — Trust-gate scope for Phase 1

| Option | Description | Selected |
|--------|-------------|----------|
| Brief-spirit mid-path | Lint, vuln, race, goleak, pre-commit + `go-arch-lint` scaffold (empty rules). Defer property tests + Example_ funcs. | ✓ |
| Spec scope per REQUIREMENTS.md | Just TRST-01/02/03/08 in Phase 1; rest in Phase 9. | |
| Full brief §3.12 now | All 8 TRST items immediately. | |

**User's choice:** Brief-spirit mid-path.
**Notes:** Captures the brief's "day-one" intent without writing dead lint config for things that don't exist yet.

---

## HTTP scaffold scope + /health shape

### Q10 — Routes in Phase 1

| Option | Description | Selected |
|--------|-------------|----------|
| `/health` only | Strict success-criteria minimum. | |
| `/health` + `/api/version` | BLD-03 requires version embedding anyway; useful smoke endpoint. Phase 2 moves `/api/version` into Ollama adapter. | ✓ |
| `+ /healthz` alias | k8s-style probe. Not relevant to laptop deployment model. | |

**User's choice:** `/health` + `/api/version`.

### Q11 — `/health` JSON shape

| Option | Description | Selected |
|--------|-------------|----------|
| Define Go-idiomatic shape now | Lock contract; additive-only across phases. | ✓ |
| Defer to researcher — match Node verbatim | Requires locating Node source (not locally checked out). | |
| Minimal shape now, expand per phase | Schema churn across phases. | |

**User's choice:** Define Go-idiomatic shape now.
**Notes:** Surfaced the finding that `acp_server_node_reference.md` doesn't specify exact shape AND that the Node source repo isn't locally checked out at the path PROJECT.md references.

### Q12 — Env var loading approach

| Option | Description | Selected |
|--------|-------------|----------|
| stdlib `os.Getenv` + `internal/config` package | Refines Bifrost pattern (which uses os.Getenv inline in main.go) by housing parsing in a package — our env surface is ~15+ vars across phases. | ✓ |
| Inline in main.go (Bifrost mirror) | Faster at Phase 1; bloats by Phase 8. | |
| `envconfig` (struct tags) | Less LOC; struct-tag debugging cost. | |

**User's choice:** stdlib + `internal/config` package.
**Notes:** User asked specifically what Bifrost does — investigated `cli/internal/config/config.go` (on-disk JSON config) and `transports/bifrost-http/main.go` (env loading inline via `os.Getenv`). Decision diverges from Bifrost intentionally because our env surface is larger.

### Q13 — Middleware chain

| Option | Description | Selected |
|--------|-------------|----------|
| `RequestID` + `Recoverer` + custom access log | chi built-ins + per-request slog line. HTTP-layer; complements Phase 8 PreHook chain. | ✓ |
| Skip all middleware in Phase 1 | No request-id correlation; panics crash binary. | |
| `RequestID` + `Recoverer` only | No structured per-request log line. | |

**User's choice:** `RequestID` + `Recoverer` + access log.

### Q14 — `slog.Logger` propagation

| Option | Description | Selected |
|--------|-------------|----------|
| Explicit injection + per-request child logger | Constructor takes `*slog.Logger`; access-log derives `logger.With("request_id", id)` and stashes in `r.Context()`. | ✓ |
| Global `slog.SetDefault` + `slog.With` per request | Global state; testability hit. | |
| Context-only (`slogctx`) | Lookup friction at every callsite. | |

**User's choice:** Explicit injection + per-request child logger.

---

## ACP integration test gating

### Q15 — Gating approach

| Option | Description | Selected |
|--------|-------------|----------|
| Auto-skip via PATH detection + env override | `exec.LookPath("kiro-cli")` + `LOOP24_KIRO_BIN`; loud SKIP when missing; auto-runs on dev box. | ✓ |
| Build tag `//go:build acp_integration` | Test file only compiles with explicit tag. Easy to forget. | |
| Env var only (must opt in) | Friction on dev box where kiro-cli is on PATH. | |

**User's choice:** Auto-skip via PATH detection + env override.
**Notes:** Verified `kiro-cli 2.4.1` is at `/Users/coreyellis/.local/bin/kiro-cli` on dev box during discussion.

### Q16 — Test-logger convention

| Option | Description | Selected |
|--------|-------------|----------|
| Hand-rolled `testLogger(t)` helper | `internal/testutil.Logger(t)`; pure stdlib slog routing to `t.Log`. | ✓ |
| `neilotoole/slogt` dep | Tiny dep; another thing to vet. | |
| `io.Discard` everywhere | Lose log output on test failures. | |

**User's choice:** Hand-rolled `testLogger(t)` helper.

### Q17 — `Client.Close()` semantics

| Option | Description | Selected |
|--------|-------------|----------|
| Cancel + drain + Wait | Idempotent (`sync.Once`); cancel ctx; close stdin; `wg.Wait()`; `cmd.Wait()`. | ✓ |
| Kill subprocess immediately | Faster but in-flight callers see `io.ErrUnexpectedEOF` instead of `context.Canceled`. | |
| Close with deadline | Configurable but every caller now thinks about deadlines. | |

**User's choice:** Cancel + drain + Wait.

---

## Operational lifecycle (start/stop/status/restart)

User raised this mid-discussion as an open question. Reframed after user clarified: "this will run on developers laptops — no Docker/k8s; macOS needs a script wrapper; Windows needs a PowerShell script; service mode is 'at some point' not now."

### Q18 — Wrapper script shape

| Option | Description | Selected |
|--------|-------------|----------|
| Ship both wrappers as proposed | `scripts/loop24` (POSIX shell, macOS/Linux) + `scripts/loop24.ps1` (PowerShell). Subcommands: start, stop, status, restart, logs, run. PID/log in OS-temp. Env-var overrides. Makefile `make start/stop/status` delegates to host script. | ✓ |
| macOS only in Phase 1, Windows in Phase 9 | Blocks Windows dev consumption of Phase 1–8 builds. | |
| Just Makefile targets, no scripts | Awkward syntax; Windows devs without make are stuck. | |
| Different subcommand naming | start/stop/status matches systemctl; changing is purely aesthetic. | |

**User's choice:** Ship both wrappers as proposed.
**Notes:** User's original phrasing was "for mac we will need a script wrapper to control it, and on windows we will have an exe executable binary - at some point I would like to run this as a service but in the short term maybe some powershell script to manage it" — service-mode-deferred but immediate wrappers required. Saved as project memory (`project_deployment_model.md`).

---

## Claude's Discretion

Items where the user deferred to planner judgment or where Go best practice was unambiguous (called out in `01-CONTEXT.md` D-23 onwards):

- Pending-request map sync primitive (`sync.Mutex` over `map[id]chan` recommended; `sync.Map` reserved for read-mostly cases)
- Exact sentinel error values exposed by `internal/acp`
- Log line keys (`request_id` vs `req_id`, `error` vs `err`)
- `internal/testutil` as new package vs sub-package
- Shutdown deadline value (10–60s; default 30s suggested)
- Whether `/health` returns 503 when ACP is dead in Phase 1 (probably 200 — pool doesn't exist yet)
- Specific `golangci-lint` config file structure (copy brief §3.12 linter list)

## Deferred Ideas

Captured in `01-CONTEXT.md` `<deferred>` section. Highlights:

- Service mode (systemd / Windows Service) — "at some point", not Phase 1
- Hosted CI (GitLab vs GitHub Actions) — until bare module name resolved
- `go-arch-lint` rules — activate in Phase 2 when first adapter↔canonical↔engine flow exists
- Property tests — Phase 6 (`coerceToolCall`)
- `Example_` doc functions — added per phase as public funcs land
- JSON encoder swap (`sonic`/`jsoniter`) — Phase 4 if throughput becomes a concern
- `/health` returning 503 — Phase 5 when pool exists
- `Dockerfile` — out of scope (laptop tool, not container service)
