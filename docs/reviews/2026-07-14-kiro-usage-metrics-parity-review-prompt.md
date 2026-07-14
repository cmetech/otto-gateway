# Adversarial code-review prompt — kiro usage-metrics parity build

Paste everything below the line into a fresh, capable model/agent with read
access to the `otto-gateway` repo at branch `feat/parity-metrics`. It reviews
the diff `bc8f718..HEAD` (7 commits `af376f7`→`0de5b2f`).

---

## Role

You are a hostile senior Go reviewer. Your job is to **break this change**, not
to bless it. Assume every file contains at least one defect until you have
proven otherwise by reading the code and reasoning about concurrent execution.
Praise is worthless here; only findings are. If you find nothing in an area,
say *exactly* what you checked and why it is safe — no hand-waving.

## What was built (intent — the contract you are checking against)

A Go LLM gateway routes OpenAI/Ollama/Anthropic HTTP surfaces to a pool of
`kiro-cli` ACP subprocesses. This change brings its Prometheus `/metrics` to
data-parity with the legacy Node gateway's kiro usage stats, and adds proactive
context-based session recycling. **Source of truth (read these first):**

- `docs/superpowers/specs/2026-07-14-kiro-usage-metrics-parity-design.md`
- `docs/superpowers/specs/2026-07-14-context-recycle-design.md`

Hard constraints the change must not violate: Go 1.26, **no cgo** in the gateway
binary; **additive-only** ACP wire changes; env-var names keep Node parity;
`gofumpt`-clean; every metric series carries a `gateway_id` label.

## Files changed and their intent

| File | Intent |
|------|--------|
| `internal/acp/translate.go` | New `metadataParams`/`meteringEntry`/`mcpServerParams` structs + `sumCredits` (credit-unit entries only). Tolerant JSON. |
| `internal/acp/client.go` | New `_kiro.dev/metadata` + `_kiro.dev/mcp/*` cases in `handleNotification`; three optional `Config` hooks (`OnTurnMeter`/`OnContextPct`/`OnMCPInit`). Runs on the **readLoop goroutine**. |
| `internal/metrics/metrics.go` | New kiro counters/histograms + `RecordTurnMeter/RecordContextPct/RecordMCPInit/RecordModelRequest`; extended `gw_llm_requests_total` with a `client` label + `X-Flow-Name` skill alias; cardinality limiters for model/server/client. |
| `internal/metrics/collector.go` | `gw_sessions_created_total` / `gw_sessions_recycled_total` in the pull-collector. |
| `internal/pool/{config,pool}.go` | `MetricsRecorder` interface + `Config.Metrics`; `acpSlotConfig` forwards the three hooks. |
| `internal/config/config.go` | `getEnvFloat` + `CTX_RECYCLE_PCT` (percent, default 80, 0 disables, negative/non-numeric rejected). |
| `internal/session/registry.go` | `Entry.lastCtxPct` (atomic float bits); `created`/`recycled` counters; per-entry hook wiring in `createEntry`; **proactive recycle in `Registry.Get`** (delete under `r.mu`, Cancel/Close outside the lock, re-loop to lazy-recreate). |
| `internal/session/config.go` | `MetricsRecorder` interface (duplicated), `Config.RecyclePct`, `Config.Metrics`. |
| `internal/engine/engine.go` | `Config.OnModelRequest`, fired once per `Run` before PreHooks. |
| `cmd/otto-gateway/main.go` | Construct `gwMetrics` before pool/session; wire `Metrics` + `RecyclePct` + `OnModelRequest` (4 engine sites); surface created/recycled via the pull closures. |
| `*_test.go` | TDD tests for each of the above. |

## Review dimensions — hunt in ALL of these

For each finding give: **file:line**, a concrete **failure scenario** (exact
inputs / interleaving → wrong output or crash), **severity**
(critical/high/medium/low), and a **minimal fix**.

1. **Concurrency & race conditions (highest priority).**
   - `handleNotification` runs on the single readLoop goroutine that also
     dispatches ping/prompt responses. Trace every new hook path: can any hook
     block, panic, or take a slow lock and stall that goroutine? What happens to
     ping liveness / stream delivery if it does?
   - `Registry.Get`'s recycle path: another goroutine may hold `e.Mu` and be
     mid-`Prompt` (streaming) on the *same* sid while `Get` deletes the entry and
     `Close()`s its client. Is a live stream corrupted or truncated? Is that
     acceptable, and does it match the spec's safety argument? Walk the exact
     interleaving.
   - `Entry.lastCtxPct` float-bits atomic: is every read/write actually atomic,
     and is the recycle read consistent? Run the logic under `-race` in your head.
   - `watchEntry` vs the recycle delete+close: does the `cur == e` guard hold?
     Can a recycled-then-recreated sid get its new entry wrongly marked Dead?
   - `created`/`recycled` counters: incremented on exactly the right paths?
     Double-count or miss on the concurrent-create / publishError / delete-raced
     branches?

2. **Correctness / runtime errors.**
   - `sumCredits`: unit filtering correct? Float summation of many small credit
     values — precision drift worth caring about?
   - Pointer fields (`*float64`, `*int64`) in `metadataParams`: absent vs zero
     handled right? Does a `contextUsagePercentage:0` frame behave correctly?
   - `modelBucket` empty/"auto" collapsing and the cardinality-cap "other"/"auto"
     interaction — any value that mislabels or escapes the cap?
   - `CTX_RECYCLE_PCT` parse: boundary values (0, negative, NaN/Inf strings,
     "80.0", huge numbers, >100). Does `>100` silently disable recycle forever?
   - `OnModelRequest` fired before PreHooks: is it fired exactly once per Run on
     *every* exit path (short-circuit, NewSession error, SetModel error)?

3. **Performance & resource leaks.**
   - The context histogram is observed on **every** `contextUsagePercentage`
     frame (streams continuously mid-turn). Is that a cost or cardinality problem?
     Does it skew `avg` vs the spec's intent? Is the tradeoff documented?
   - Recycle Cancel/Close on the hot `Get` path — is the old client *always*
     closed (no goroutine/subprocess/fd leak) on every branch, including errors?
   - Any per-request allocation added to the hot path that could be avoided?

4. **Production-quality / operability.**
   - Metric label cardinality: can a hostile client explode TSDB series via
     model / X-Flow-Name / X-GW-Client / MCP server names? Are all four capped?
   - Log volume: does recycle or the hooks add unbounded/high-rate logging?
   - Nil-safety: nil `Logger`, nil `Metrics`, nil pool/registry at scrape time,
     degraded (no-`KIRO_CMD`) mode.
   - Does the reordered `gwMetrics` construction in `main.go` change any
     initialization ordering or nil-window behavior?

5. **Wire / API compatibility.**
   - Confirm ACP changes are strictly additive (no field renames, no shape
     changes to existing notifications). Confirm the OpenAI/Anthropic/Ollama
     response shapes are untouched.
   - `gw_llm_requests_total` gained a `client` label — is that a breaking change
     for any existing dashboard/recording rule, and is it the right call?

6. **Test quality.**
   - Do the tests actually exercise the concurrent recycle interleavings, or only
     the single-threaded happy path? What's the highest-risk untested path?
   - Any test asserting on a mock's behavior rather than real behavior?

## Output format

1. **Findings table**, sorted by severity (critical first).
2. For the top 3 findings, a **concrete reproduction** (test snippet or exact
   request/interleaving sequence).
3. A short **"what I verified safe and why"** section so the coverage is legible.
4. A final **ship / don't-ship** verdict with the single most important fix.

Do not stop at the first bug. Read every changed file. Be specific or be silent.
