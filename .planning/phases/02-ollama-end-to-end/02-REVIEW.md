---
phase: 02-ollama-end-to-end
reviewed: 2026-05-24T02:13:21Z
depth: standard
files_reviewed: 48
files_reviewed_list:
  - cmd/loop24-gateway/main.go
  - cmd/loop24-gateway/main_test.go
  - internal/acp/stream_testhelpers.go
  - internal/adapter/ollama/adapter.go
  - internal/adapter/ollama/decode.go
  - internal/adapter/ollama/decode_test.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/ollama/handlers_test.go
  - internal/adapter/ollama/integration_test.go
  - internal/adapter/ollama/render.go
  - internal/adapter/ollama/stub.go
  - internal/adapter/ollama/stub_test.go
  - internal/adapter/ollama/wire.go
  - internal/adapter/ollama/wire_test.go
  - internal/auth/auth.go
  - internal/auth/auth_internal_test.go
  - internal/auth/auth_test.go
  - internal/auth/bearer.go
  - internal/auth/ipallowlist.go
  - internal/canonical/chat.go
  - internal/canonical/chat_test.go
  - internal/canonical/chunk.go
  - internal/canonical/chunk_image_test.go
  - internal/config/config.go
  - internal/config/config_internal_test.go
  - internal/config/config_test.go
  - internal/engine/acp_adapter.go
  - internal/engine/acp_adapter_test.go
  - internal/engine/build_acp.go
  - internal/engine/build_acp_test.go
  - internal/engine/collect.go
  - internal/engine/collect_test.go
  - internal/engine/engine.go
  - internal/engine/engine_test.go
  - internal/engine/hooks.go
  - internal/engine/pickcwd.go
  - internal/engine/pickcwd_test.go
  - internal/engine/preflight_phase11_test.go
  - internal/engine/testmain_test.go
  - internal/pool/config.go
  - internal/pool/export_test.go
  - internal/pool/pool.go
  - internal/pool/pool_test.go
  - internal/pool/stats.go
  - internal/pool/testmain_test.go
  - internal/server/health.go
  - internal/server/server.go
  - internal/server/server_test.go
  - scripts/loop24
  - scripts/loop24.ps1
findings:
  critical: 0
  warning: 5
  info: 9
  total: 14
status: issues_found
---

# Phase 2: Code Review Report

**Reviewed:** 2026-05-24T02:13:21Z
**Depth:** standard
**Files Reviewed:** 48
**Status:** issues_found

## Summary

This adversarial review of the Phase 2 Ollama end-to-end slice traced
Codex H-1 through M-6 fixes through the implementation and inspected
the load-bearing surfaces (XFF trust, body caps, router auth boundary,
pool slot release coordination, subprocess spawn, goroutine
lifecycles).

**Codex review-fix verification (all confirmed correctly implemented):**

- **H-7 (XFF trust default-OFF):** `auth.IPAllowlist` only consults
  `X-Forwarded-For` when `cfg.TrustXForwardedFor` is true; default is
  false. Threaded from `config.AuthTrustXFF` (`AUTH_TRUST_XFF` env) all
  the way through `server.NewFromConfig` → `auth.Bearer`/`IPAllowlist`.
  Tests cover both spoofed-XFF and opt-in paths
  (`auth_test.go:158-216`, `server_test.go:345-377`).
- **M-5 (body size caps):** `decodeJSONBody` is uniformly applied to
  every body-reading handler: chat (4 MiB), generate (4 MiB), show
  (1 MiB), copy/delete/pull/push/create (64 KiB stub cap). The
  `*http.MaxBytesError` → 413 mapping fires correctly; tests confirm
  (`decode_test.go:41-60`, `handlers_test.go:368-377`).
- **M-4 (router auth boundary):** `/api/version` is registered exactly
  once on the OUTER router via `adapter.HandleVersion()` accessor; the
  protected `chi` sub-router built by `New` does NOT register `/version`.
  Tests assert non-registration (`handlers_test.go:356-364`).
- **M-3 (pool slot release):** `poolStreamWrapper` uses `sync.Once`
  for `releaseOnce`; release closure uses map-delete-first guard so
  whichever of three terminal paths (Result drained, ctx cancelled,
  engine-initiated Cancel) wins, exactly one channel send occurs.
  Comprehensive tests cover each path
  (`pool_test.go:446-814`).
- **Subprocess spawn (CLAUDE.md gosec G204):** No untrusted input
  reaches `exec.CommandContext`; the only call site is
  `internal/acp/client.go:284` (outside this phase's scope), driven by
  operator-controlled env vars (`KIRO_CMD`, `KIRO_ARGS`).
- **Goroutine leak gate:** `goleak.VerifyTestMain` is wired in both
  `internal/engine/testmain_test.go` and `internal/pool/testmain_test.go`.
  Pool's ctx-watcher goroutine has both `<-watchCtx.Done()` and
  `<-w.doneCh` exit paths so it cannot leak when `releaseOnce` fires.

**Findings:** 0 Critical, 5 Warning, 9 Info. No security defects, data
loss risks, or shipping blockers. Most findings are documentation
accuracy / hygiene / minor footguns.

## Warnings

### WR-01: `chatResponseToWire` PromptEvalCount is always zero — misleading "best effort" comment

**File:** `internal/adapter/ollama/render.go:42-49`

**Issue:** The block

```go
promptTokens := 0
if resp != nil {
    promptTokens = estimateTokens("")
}
```

always sets `promptTokens` to `(0+3)/4 == 0`. The accompanying comment
("Best-effort prompt token estimate from the system + any user turns we
can see") promises a real estimate that the code does not deliver. The
result is `PromptEvalCount` always renders as `0` in `/api/chat`
responses, with no behavior difference from just writing `0` directly.
This is dead code dressed up as a calculation, and the misleading
comment makes it easy to "fix" by adding text that breaks the
zero-by-design invariant Phase 2 actually relies on.

**Fix:**

```go
// Phase 2 does not retain the prompt at render time, so the prompt-
// token estimate is left at 0 (Ollama clients tolerate zero). Phase 3+
// can populate this from kiro-cli's Usage.InputTokens when available.
out := &ollamaChatResponse{
    ...
    PromptEvalCount: 0,
    ...
}
```

Either delete the dead expression and zero-init the field, or actually
estimate from `req.System + joined user turns` if the caller propagates
the request (it does not today — `chatResponseToWire` only receives
`requestedModel`).

### WR-02: `handleChat` / `handleGenerate` use redundant `if` to clear `Stream` field

**File:** `internal/adapter/ollama/handlers.go:42-45, 82-84`

**Issue:**

```go
if wire.Stream {
    wire.Stream = false
}
```

The branch test gates a no-op assignment. The conditional is
indistinguishable from an unconditional `wire.Stream = false` —
reviewers will pause to look for a hidden side effect that does not
exist. Worse, if anyone later adds logic inside the `if`, it will be
silently scoped to "previously was true", which is rarely the intended
semantic for a silent-downgrade pattern.

**Fix:**

```go
// Phase 2 only honors non-streaming; silent downgrade matches Node parity.
wire.Stream = false
```

Apply to both `handleChat:42-45` and `handleGenerate:82-84`.

### WR-03: Engine error string echoed to client via `err.Error()` may leak request content (T-02-33 mitigation is partial)

**File:** `internal/adapter/ollama/handlers.go:51-54, 91-94`

**Issue:** The handler calls
`writeError(w, http.StatusInternalServerError, err.Error())` on engine
failures. `handlers_test.go:149-160` only asserts the substring `"hi"`
is absent from the response — but kiro-cli error wrappings can easily
include request fragments, file paths from `WorkingDirOverride`, model
names, or partial prompt text. The T-02-33 mitigation comment in
`handlers.go:280-283` claims engine errors "DO NOT echo the request
body" but the assertion is enforced only at the engine boundary
(`engine.Run` wraps as `engine: collect: %w`). Any inner error message
from `*acp.Client` (e.g. a JSON-RPC error response that quotes the
request) will surface verbatim to the client.

**Fix:** Map engine errors to a fixed, structured response rather than
`err.Error()`. Log the full error server-side; surface only a
categorical message client-side.

```go
if err != nil {
    a.cfg.Logger.Error("chat: engine collect failed",
        "err", err,
        "request_id", middleware.GetReqID(r.Context()))
    writeError(w, http.StatusInternalServerError, "internal error")
    return
}
```

Or, if richer client-side surface is required, switch to a
`map[string]any` shape with a stable `code` field and a sanitized
`message` field.

### WR-04: `Transfer-Encoding: chunked` set manually in `writeNDJSON` — Go's stdlib manages this automatically

**File:** `internal/adapter/ollama/stub.go:69`

**Issue:** `w.Header().Set("Transfer-Encoding", "chunked")` is set
explicitly before `WriteHeader(http.StatusOK)`. Go's
`net/http.ResponseWriter` automatically sets
`Transfer-Encoding: chunked` for responses without a known
`Content-Length` (which NDJSON is). Per `net/http` docs, explicit
`Transfer-Encoding` headers are scrutinized and may be silently
stripped by the stdlib (and have been the subject of CVEs in adjacent
languages). The line is dead in the best case and could cause
double-encoding under future Go versions / non-stdlib middleware.

**Fix:**

```go
w.Header().Set("Content-Type", "application/x-ndjson")
w.Header().Set("Cache-Control", "no-cache")
w.Header().Set("X-Accel-Buffering", "no")
w.WriteHeader(http.StatusOK)
```

Drop the `Transfer-Encoding` line entirely; Go will chunk-encode the
streaming response correctly without it.

### WR-05: `scripts/loop24.ps1` crashes uncleanly when PID file is empty or corrupted

**File:** `scripts/loop24.ps1:20, 54, 72`

**Issue:** Three sites cast a PID file's contents to `[int]`:

```powershell
$existingPid = [int](Get-Content $PidFile -Raw)
```

If the file is empty (zero bytes), contains only whitespace, or
contains non-numeric content (e.g. corrupted from a disk-full event),
`[int]` cast throws an exception. Combined with
`$ErrorActionPreference = 'Stop'` and `Set-StrictMode -Version Latest`
at lines 8-9, the script aborts with an unhandled PowerShell exception
— surfacing as a confusing red error message rather than the clean
"stopped (stale PID)" path the equivalent POSIX script provides
(`scripts/loop24:14-21`).

**Fix:** Wrap the read+cast in a `try/catch`, treating any failure as
"stale PID":

```powershell
function Read-PidOrStale {
    param([string]$Path)
    if (-not (Test-Path $Path)) { return $null }
    try {
        $raw = (Get-Content $Path -Raw).Trim()
        if (-not $raw) { return $null }
        return [int]$raw
    } catch {
        return $null
    }
}

# Usage:
$existingPid = Read-PidOrStale $PidFile
if ($null -eq $existingPid) {
    Remove-Item $PidFile -ErrorAction SilentlyContinue
    # treat as not running
}
```

## Info

### IN-01: `extractCommit` silently truncates to 7 chars without bounds check on len==7 edge

**File:** `internal/adapter/ollama/handlers.go:251-260`

**Issue:** `s.Value[:7]` is gated by `len(s.Value) >= 7`, so the slice
itself is safe. However the function silently truncates revision
strings (typically 40-char SHAs), and on a hypothetical
`vcs.revision = "abc"` (3 chars), it returns `"unknown"`. The behaviour
is correct but undocumented for the short-revision edge case. Minor
documentation gap.

**Fix:** Add a doc-comment line:
`// Returns "unknown" when build info is unavailable OR the revision is shorter than 7 chars.`

### IN-02: `wireToChatRequest` extracts only the FIRST system message; subsequent system text is silently dropped

**File:** `internal/adapter/ollama/wire.go:262-267`

**Issue:** The system-message extraction loop breaks after the first
match. Subsequent `role: "system"` messages are added to `req.Messages`
as `RoleSystem` but `buildBlocks` (`internal/engine/build_acp.go:73`)
explicitly skips RoleSystem text in the transcript — so any text in a
SECOND `system` message is silently dropped. The behaviour is plausible
(LangFlow rarely sends multi-system arrays) but is a footgun for
clients that legitimately do.

**Fix:** Either concatenate multiple system messages with `\n\n`
separators, or log a warning when a request contains >1 system message
so operators can detect drift.

### IN-03: `handleChat` / `handleGenerate` errors use 500 even for client-side malformed payloads after `decodeJSONBody`

**File:** `internal/adapter/ollama/handlers.go:51-54, 91-94`

**Issue:** All engine errors map to `http.StatusInternalServerError`,
even for cases where the engine rejects an obviously client-side issue
(e.g. an unparseable image, an invalid model id). The Ollama Node
reference uses 400/500 split based on error category. Phase 2 is
documented as "Phase 2 only honors non-streaming; silent downgrade
matches Node parity" but the error-status-code parity is not preserved.

**Fix:** Introduce a small error-classification helper in the engine
that returns a `(category, message)` pair, and let the adapter map
category → HTTP status. Out of scope for v1 fixes; document as a
Phase 3+ deferral.

### IN-04: `getEnvStrSlice` whitespace split vs. `getEnvStrSliceComma` comma split — two near-identical helpers create drift risk

**File:** `internal/config/config.go:136-166`

**Issue:** Two split helpers exist with subtly different semantics —
whitespace-split for `KIRO_ARGS` (so `acp --verbose` parses as
`[acp, --verbose]`), comma-split for `AUTH_TOKEN` and `ALLOWED_IPS`.
The naming differs only by `Comma` suffix. A future contributor adding
a new list-shaped env var has a 50% chance of picking the wrong helper.

**Fix:** Either consolidate into one helper that takes a separator,
or rename the whitespace one to `getEnvStrSliceFields` (matching
`strings.Fields`) to make the semantic split self-documenting.

### IN-05: `extractCommit` is duplicated logic in adapter + server packages

**File:** `internal/adapter/ollama/handlers.go:251-260` and
`internal/server/health.go:80-93` (handler reads `s.commit` set at
construction time)

**Issue:** `extractCommit` exists as a fallback in the ollama adapter
even though the construction-time wiring in `cmd/loop24-gateway/main.go`
already supplies a non-empty `Commit` via `version.Commit()`. The
fallback path is dead in production. Adapter and server packages have
parallel commit-handling code. Minor duplication.

**Fix:** Delete the `extractCommit` fallback in
`internal/adapter/ollama/handlers.go` and rely on the construction-time
value being authoritative. Or, if defensive default is desired, set
`a.cfg.Commit = "unknown"` once in `New` when empty.

### IN-06: `handleVersion` ignores `runtime/debug` import after `extractCommit` removal would be needed

**File:** `internal/adapter/ollama/handlers.go:6, 251-260`

**Issue:** The `runtime/debug` import is solely for `extractCommit`. If
IN-05 is acted on, this import becomes orphaned. Document so the
cleanup doesn't get stranded.

**Fix:** When applying IN-05, also remove the `"runtime/debug"` import.

### IN-07: `assembleChatResponse` ID generator uses `time.Now().UnixNano()` — potential collision under burst

**File:** `internal/engine/collect.go:96`

**Issue:** `fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())` — under
high concurrent load on systems with coarse clock resolution (Windows
~15ms historically; modern systems are typically monotonic with
nanosecond precision), two concurrent `Collect` calls could produce
identical IDs. Phase 2 is single-pool-slot (POOL_SIZE=1) so concurrency
is bounded, but Phase 5's pool-size bump to 4 could expose this. Use a
counter or `crypto/rand` for IDs.

**Fix:**

```go
import "crypto/rand"
import "encoding/hex"

func newRequestID() string {
    var b [8]byte
    _, _ = rand.Read(b[:])
    return "chatcmpl-" + hex.EncodeToString(b[:])
}
```

### IN-08: `scripts/loop24` start function has TOCTOU between `nohup ... &` and `echo $!`

**File:** `scripts/loop24:32-33`

**Issue:** Classic shell pattern: between line 32 (`nohup "$LOOP24_BIN"
... &`) and line 33 (`echo $! > "$LOOP24_PID"`), the backgrounded
process can exit (e.g. config error → immediate exit). The PID file
gets written with what is now a stale PID. `status` later reports
"stopped (stale PID)" — acceptable but obscures the actual failure
cause. Consider a brief sleep + `kill -0` check, or have the gateway
binary write its own PID file via `--pid-file` flag (D-22 forbids
adding subcommands; PID-file flag would be an exception worth
discussing).

**Fix:** Add a post-spawn sanity check:

```bash
nohup "$LOOP24_BIN" >> "$LOOP24_LOG" 2>&1 &
pid=$!
echo "$pid" > "$LOOP24_PID"
sleep 0.2
if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$LOOP24_PID"
    echo "loop24-gateway failed to stay up — check $LOOP24_LOG" >&2
    exit 1
fi
echo "loop24-gateway started (PID $pid)"
```

### IN-09: `scripts/loop24:73-75` silently swallows JSON formatting failure when `python3` is unavailable

**File:** `scripts/loop24:73-75`

**Issue:**
`curl -sf "${LOOP24_ADDR}/health" 2>/dev/null | python3 -m json.tool 2>/dev/null || true`
silently drops the health response when `python3` is missing. A user
running `loop24 status` on a Python-free system gets no health JSON at
all, which looks like the gateway is unresponsive.

**Fix:** Fall back to raw curl output when `python3` is unavailable:

```bash
if command -v curl >/dev/null 2>&1; then
    local json
    json=$(curl -sf "${LOOP24_ADDR}/health" 2>/dev/null) || return
    if command -v python3 >/dev/null 2>&1; then
        echo "$json" | python3 -m json.tool
    elif command -v jq >/dev/null 2>&1; then
        echo "$json" | jq .
    else
        echo "$json"
    fi
fi
```

---

_Reviewed: 2026-05-24T02:13:21Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
