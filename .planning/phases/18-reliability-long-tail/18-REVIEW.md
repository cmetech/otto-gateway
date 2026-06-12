---
phase: 18-reliability-long-tail
reviewed: 2026-06-11T00:00:00Z
depth: standard
files_reviewed: 34
files_reviewed_list:
  - cmd/otto-gateway/main.go
  - cmd/otto-gateway/testmain_test.go
  - cmd/otto-tray/fsm.go
  - cmd/otto-tray/poller.go
  - cmd/otto-tray/regression_rel_tray_08_test.go
  - cmd/otto-tray/regression_rel_tray_09_test.go
  - internal/acp/client.go
  - internal/acp/regression_rel_obsv_03_test.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/ollama/regression_rel_http_06_test.go
  - internal/admin/regression_rel_http_07_test.go
  - internal/admin/regression_rel_obsv_04_test.go
  - internal/admin/tail.go
  - internal/config/config.go
  - internal/config/config_test.go
  - internal/config/regression_rel_cfg_05_test.go
  - internal/config/regression_rel_cfg_06_test.go
  - internal/config/regression_rel_cfg_07_test.go
  - internal/config/regression_rel_obsv_04_test.go
  - internal/config/testmain_test.go
  - internal/engine/engine.go
  - internal/engine/regression_rel_http_07_test.go
  - internal/pool/config.go
  - internal/pool/exit_watcher.go
  - internal/pool/exit_watcher_test.go
  - internal/pool/pool.go
  - internal/pool/pool_test.go
  - internal/pool/regression_rel_http_07_test.go
  - internal/pool/regression_rel_obsv_02_test.go
  - scripts/otto-gw
  - scripts/otto-gw.ps1
  - tests/scripts/test-support-bundle-rel-tray-09.sh
  - tests/scripts/test-support-bundle.sh
findings:
  critical: 3
  warning: 7
  info: 4
  total: 14
status: issues_found
---

# Phase 18: Code Review Report

**Reviewed:** 2026-06-11T00:00:00Z
**Depth:** standard
**Files Reviewed:** 34
**Status:** issues_found

## Summary

Phase 18 closes 10 deferred Low-severity reliability findings across config hardening, observability, panic recovery, and tray honesty. Overall the Go-side changes are tight: the `internal/acp/client.go` stderr drain goroutine is correctly wired, the four panic-recovery sites match the documented template, and the regression tests use clean probe seams with mutex-protected fakes. Config validation grows the right named errors and the AdminTailPath single-source-of-truth is correctly threaded.

However, three BLOCKER-tier defects surfaced:

- **CR-01 (Tailer lazy-restart broken on panic recovery)** — the docstring contract that "a subsequent Subscribe will lazy-start a fresh tailer goroutine" is broken because `t.running` is never reset on panic-recover exit. Subscribers added after a panic see `running=true` and never get lines.
- **CR-02 (PowerShell `firstError` semantics inverted vs. bash)** — `Import-DotEnv` reads local `$firstError` but writes `$script:firstError`, so the "first malformed line wins" contract is silently inverted to "last wins" (or worse, every subsequent line clobbers because the local variable is never refreshed).
- **CR-03 (PowerShell `$script:firstError` leaks across Import-DotEnv calls)** — never reset at function entry, so a clean second file inherits the prior file's error state and never reaches the `Clear-ConfigErrorSentinel` branch.

The seven WARNINGs cover memory-DoS surface on the unbounded ReadString accumulation, KIRO_CWD `~/..` path-escape via tilde expansion + filepath.Join, mid-rune byte slicing in the stderr cap, and an HTTP_ADDR bind-probe TOCTOU acknowledged in code but not bounded against listener leak on the success path (the `_ = ln.Close()` return value is discarded — a Close() error would silently leave a listener open). Plus minor wrapper hygiene and structural-finding notes.

## Critical Issues

### CR-01: Tailer panic-recovery breaks lazy-restart contract

**File:** `internal/admin/tail.go:319-345` (and `tail.go:212-220` for the Subscribe contract)
**Issue:** The panic-recovery deferred at the top of `Tailer.run` correctly logs and exits the goroutine, but it does NOT reset `t.running = false` under `t.mu`. The docstring at `tail.go:325-326` explicitly promises:

> "The recover logs the panic and returns; a subsequent Subscribe will lazy-start a fresh tailer goroutine. No restart / spin loop."

This contract is broken. After a panic, `Tailer.run` returns. `t.running` remains `true` from `Subscribe` (line 217). Any later `Subscribe` call hits the `if !t.running { ... go t.run(...) }` guard, sees `running=true`, and falls through WITHOUT spawning a new goroutine. The new subscriber is appended to `t.subscribers` but no goroutine is broadcasting to it. The admin Log Tail panel goes permanently dark until the gateway is restarted.

This is the same kind of "silent dark UI" failure mode REL-OBSV-04 was supposed to close out — only one layer deeper.

**Fix:** Reset `t.running` (and `t.cancelRun`) inside the deferred recover, under `t.mu`. Must happen before the goroutine returns:

```go
defer func() {
    if r := recover(); r != nil && t.logger != nil {
        t.logger.Error(
            "goroutine panic recovered",
            "site", "admin-tailer",
            "panic", fmt.Sprintf("%v", r),
            "stack", string(debug.Stack()),
        )
        // Reset running flag so the next Subscribe lazy-restarts the goroutine.
        // Without this, t.running stays true forever and Subscribe never
        // respawns the broadcaster — violating the docstring contract.
        t.mu.Lock()
        t.running = false
        t.cancelRun = nil
        t.mu.Unlock()
    }
}()
```

Also add a regression case: install the panic probe, Subscribe, wait for the panic-recovered record, Unsubscribe, Subscribe again, write a line to the file, assert the second subscriber receives the line.

---

### CR-02: PowerShell `Import-DotEnv` first-error contract inverted by `$script:` scope mismatch

**File:** `scripts/otto-gw.ps1:274-290`
**Issue:** `Import-DotEnv` initializes `$firstError` as a function-local variable at line 274:

```powershell
$firstError = $null
```

Inside the `ForEach-Object` block (line 285) it tests the LOCAL `$firstError`:

```powershell
if (-not $firstError) {
    ...
    $script:firstError = "..."   # ← writes to SCRIPT scope
}
```

The write target is `$script:firstError` — a different variable. The local `$firstError` is never assigned. On the SECOND malformed line, `-not $firstError` is still `true` (local is still `$null`), so the code overwrites `$script:firstError` AGAIN. Result: **every subsequent malformed line clobbers the prior one**, so `$script:firstError` holds the LAST malformed line, not the first.

This silently inverts the documented contract. The bash counterpart at `scripts/otto-gw:264` correctly checks `[[ -z "$first_error" ]]` against the same variable it later writes, so the first malformed line sticks. The PowerShell wrapper does the opposite.

Operator impact: if `.otto-gw.env` has parse errors on lines 5, 12, and 18, the tray surfaces line 18's error — confusing because the operator typically fixes top-down and sees the same sentinel re-appear with a different message after each fix.

**Fix:** Use a single scope consistently. Either drop the `$script:` qualifier on the write, or hoist `$firstError` to script scope:

```powershell
function Import-DotEnv {
    param([string]$Path)
    if (-not (Test-Path $Path)) { return }
    $script:firstError = $null   # ← reset at entry (also fixes CR-03)
    $lineno = 0
    Get-Content $Path | ForEach-Object {
        $lineno++
        ...
        if ($line -notmatch '=') {
            if (-not $script:firstError) {   # ← read AND write same scope
                $snippet = if ($line.Length -gt 80) { $line.Substring(0, 80) } else { $line }
                $script:firstError = "$(Split-Path -Leaf $Path):${lineno}: missing '=' (got: $snippet)"
            }
            return
        }
        ...
    }
    ...
}
```

Add a PowerShell test (Pester or a bash-side harness invoking pwsh) feeding a fixture with three malformed lines and asserting the sentinel matches the FIRST one.

---

### CR-03: PowerShell `$script:firstError` leaks across consecutive `Import-DotEnv` calls

**File:** `scripts/otto-gw.ps1:268-307`
**Issue:** `$script:firstError` is never reset at the top of `Import-DotEnv`. The function is called twice in `Load-Config` (once for `.otto-gw.env`, once for `.otto-gw.overrides.env`). If the FIRST call sets `$script:firstError` to a parse error, the SECOND call (even if the overrides file parses cleanly) will hit line 300 with `$script:firstError` still truthy, re-emit the WARN line with the FIRST file's error, write the sentinel AGAIN with that error, and never reach the `Clear-ConfigErrorSentinel` branch.

Conversely, if the FIRST file is clean and the SECOND is broken, the contract holds — but on a subsequent invocation of the wrapper (e.g., `otto-gw status` after `otto-gw start`), if the operator has fixed the file, `$script:firstError` from the prior wrapper exec is dead (process exit), so the next exec starts clean. Cross-call leak is bounded by process lifetime.

The intra-process leak between the two `Import-DotEnv` calls in a single `Load-Config` is the real defect: a broken .otto-gw.env + a clean overrides.env produces double-warning + sentinel-not-cleared-after-fix UX.

**Fix:** Reset `$script:firstError = $null` as the first statement inside `Import-DotEnv`. The fix is the same line shown in CR-02's snippet (`$script:firstError = $null` at function entry). This must land alongside CR-02 — they share the same root cause (scope confusion) and the same call site.

Regression: in the same Pester/wrapper-harness test as CR-02, feed two files — broken first, clean second — and assert the sentinel is REMOVED after Load-Config, not present with the first file's error.

## Warnings

### WR-01: `stderrDrainLoop` ReadString accumulates unbounded before per-line cap is applied

**File:** `internal/acp/client.go:391-422`
**Issue:** The 1MB cap is applied AFTER `bufio.Reader.ReadString('\n')` returns. ReadString accumulates internally until a `\n` arrives or EOF — there is no upstream byte limit. A compromised or wedged kiro-cli that emits 50MB of UTF-8 bytes with no newline forces the bufio internal buffer to grow to 50MB before the 1MB trim fires. The acp.Client per slot is N=POOL_SIZE so the worst-case transient memory under attack is N × incoming-stderr-volume, not 1MB × N.

The REL-OBSV-03 regression at `regression_rel_obsv_03_test.go:154-178` actually demonstrates this: the test emits 2MB of `X` characters without a newline and asserts the eventual record is capped — confirming that 2MB is buffered in-process before the cap activates. Scale that to a malicious agent emitting much more.

This is acknowledged as out-of-scope (performance) by review-scope but it's a memory-DoS surface on a high-risk subprocess channel.

**Fix:** Use `io.LimitReader` to wrap the pipe with a hard byte budget, OR switch to a manual loop over fixed-size reads (`reader.Read(buf[:N])`) that bails when a logical line exceeds `maxLineBytes` without consuming more bytes than necessary. Simplest:

```go
const maxLineBytes = 1024 * 1024
reader := bufio.NewReaderSize(pipe, 64*1024)
var line []byte
for {
    b, err := reader.ReadByte()
    if err != nil { /* flush partial, return */ }
    if b == '\n' { /* flush line, reset */ }
    else if len(line) >= maxLineBytes { /* truncate-flush, then drain-to-newline without buffering */ }
    else { line = append(line, b) }
}
```

Or accept the design and document the bound: "stderr memory cost per slot is unbounded until the kiro-cli writer emits a newline; operators should treat a hung kiro-cli emitting >1MB of stderr without a newline as a DoS vector."

---

### WR-02: KIRO_CWD tilde expansion permits `~/..` path escape above $HOME

**File:** `internal/config/config.go:327-344`
**Issue:** Tilde expansion at line 328-329 calls `filepath.Join(home, kiroCWD[2:])` — `filepath.Join` cleans `..` segments, so `KIRO_CWD=~/../../etc` resolves to `/etc` (or whatever path the `..` chain reaches). The subsequent `os.Stat` then validates the resolved path is a directory, which is true for `/etc`, and stores it on `cfg.KiroCWD`. The kiro-cli subprocess is then chdir'd into `/etc` (or any other resolved location).

KIRO_CWD is operator-controlled boot-time env, so this is "intentional misconfiguration" rather than a request-time vulnerability. But the project context calls this out explicitly in the focus areas as "Verify tilde-expansion logic for KIRO_CWD doesn't allow path traversal beyond $HOME." It currently does.

**Fix:** After `filepath.Join`, verify the cleaned path has `home` as a prefix, OR reject the value if `..` appears in the post-`~/` portion:

```go
if strings.HasPrefix(kiroCWD, "~/") {
    if home, herr := os.UserHomeDir(); herr == nil {
        rest := kiroCWD[2:]
        if strings.Contains(rest, "..") {
            errs = append(errs, fmt.Errorf("config: KIRO_CWD (%q): '..' segments not permitted after '~/'", kiroCWD))
        } else {
            kiroCWD = filepath.Join(home, rest)
        }
    }
}
```

If this is intentional (operators using `~/..` as shorthand), document the policy decision in the field comment.

---

### WR-03: HTTP_ADDR bind probe discards `Close()` error — listener may leak on failed close

**File:** `internal/config/config.go:667-671`
**Issue:**

```go
if ln, lerr := net.Listen("tcp", httpAddr); lerr != nil {
    errs = append(errs, fmt.Errorf(...))
} else {
    _ = ln.Close()
}
```

If `Close()` returns an error (rare: EBADF, EINTR on macOS, or kernel pressure), the listener is silently leaked — and worse, the real `server.ListenAndServe` later will fail with EADDRINUSE on the same address. The probe's purpose is to prevent that exact 5–10s-deferred error, so a leak here is self-defeating.

The TOCTOU window between this Close and the real bind is acknowledged in the code comment (line 663-665), but the bind-leak-on-Close-error is not.

**Fix:** Surface the Close error into `errs` so the operator sees it, even though it's unlikely:

```go
if cerr := ln.Close(); cerr != nil {
    errs = append(errs, fmt.Errorf("config: HTTP_ADDR (%q): probe close failed: %w", httpAddr, cerr))
}
```

This is defense-in-depth — if Close fails the probe has already done its job (the bind succeeded), but the next real ListenAndServe will see EADDRINUSE caused by THIS process's leaked socket. Better to surface the rare Close error than to silently break startup.

---

### WR-04: `stderrDrainLoop` byte-slicing mid-rune corrupts UTF-8 in log "line" field

**File:** `internal/acp/client.go:405-407`
**Issue:** `trimmed[:maxLineBytes]` is a raw byte slice. If kiro-cli's stderr is UTF-8 (very likely, given Go's default), the slice may split a multi-byte rune at the 1MB boundary. slog's JSON handler will then emit either invalid UTF-8 bytes (depending on encoder strictness) or `�` replacement characters. Downstream log parsers may reject the record or display garbled text precisely at the most-important diagnostic line.

Per project rule (PII bracket shape, kiro-cli/Claude hangs on `<...>` markers), structured log corruption is not the worst-case for this surface — but invalid UTF-8 in JSON values is undefined behavior across slog handlers and Splunk/ELK pipelines.

**Fix:** Slice on rune boundary. Walk back from the cap until you find a valid UTF-8 sequence start:

```go
if len(trimmed) > maxLineBytes {
    n := maxLineBytes
    for n > 0 && !utf8.RuneStart(trimmed[n]) {
        n--
    }
    trimmed = trimmed[:n]
}
```

(Add `unicode/utf8` import.) Cost is at most 3 byte-walks. Documented in the code that the cap is "≤1MB on UTF-8-safe boundary, not strict byte-cap."

---

### WR-05: PowerShell sentinel write is not atomic — tray may observe partial content during write

**File:** `scripts/otto-gw.ps1:250-266`
**Issue:** `Set-Content -Path $sentinel -Value $flat -NoNewline` truncates then writes. The Go tray reads via `os.ReadFile` (poller.go:36) which reads the whole file in one syscall — but during the Set-Content write window (open, truncate, write 200 bytes, close) a poller tick can fire and observe the file mid-truncation as zero-bytes. The tray then sees `ConfigError = ""` and falls through to the normal FSM path, showing "stopped" or "running" instead of the actual parse error for one 3–6s tick.

The bash counterpart at `scripts/otto-gw:231` has the same issue (`printf ... > "$sentinel"` truncates then writes).

**Fix:** Write to a temp file in the same directory and atomically rename:

PowerShell:
```powershell
$tmp = "$sentinel.tmp"
Set-Content -Path $tmp -Value $flat -Encoding UTF8 -NoNewline -ErrorAction Stop
Move-Item -Force -Path $tmp -Destination $sentinel -ErrorAction Stop
```

Bash:
```bash
printf '%s' "$msg" | tr '\n' ' ' | head -c 200 > "${sentinel}.tmp" 2>/dev/null
mv "${sentinel}.tmp" "$sentinel" 2>/dev/null || true
```

Rename within the same filesystem is atomic on POSIX/NTFS so the tray either sees the old content or the new content — never a zero-byte intermediate.

---

### WR-06: `config_error_sentinel_path` falls back to world-writable `/tmp` when $HOME unset

**File:** `scripts/otto-gw:205-207`
**Issue:**

```bash
config_error_sentinel_path() {
    printf '%s/.otto-gw/.config-error' "${HOME:-/tmp}"
}
```

When `$HOME` is unset (degraded shell, sandboxed runner), the sentinel lands at `/tmp/.otto-gw/.config-error`. `/tmp` is world-writable; another local user could pre-create a symlink at that path pointing at any file they want overwritten with operator-visible content. The mode 0600 + 0750 chmod helps but the *initial* `mkdir -p` followed by `> "$sentinel"` is not atomic — a TOCTOU symlink in the parent could redirect the write.

More practically: the sentinel content is a parse-error message that may contain a partial `KEY=VALUE` from the malformed line, per the docstring (`docstring at config.go's adjacent` and the bash comment at line 222 says sentinel may contain "a partial KEY=VALUE from the malformed line"). If KEY is `AUTH_TOKEN` or `PII_HASH_KEY` with a partial value, writing this to `/tmp/.otto-gw/.config-error` exposes secret fragments to other local users.

The Go tray-side `readConfigErrorSentinel` at `cmd/otto-tray/poller.go:31-34` correctly returns "" when $HOME is empty, so the tray-read side is safe. The wrapper-write side is not.

**Fix:** Refuse to write the sentinel when $HOME is unset rather than falling back to /tmp:

```bash
config_error_sentinel_path() {
    if [[ -z "${HOME:-}" ]]; then
        return 1  # caller treats as "no sentinel possible"
    fi
    printf '%s/.otto-gw/.config-error' "$HOME"
}
```

Then in `write_config_error_sentinel`, check the return:

```bash
sentinel="$(config_error_sentinel_path)" || return 0
```

A degraded shell that doesn't have $HOME is already unable to wire `.otto-gw.env` properly; failing the sentinel silently is the safer posture.

---

### WR-07: `Pool.respawnSlot` lazy-respawn-success log captures NEW pid BEFORE p.mu unlock — fine, but `label` capture is redundant

**File:** `internal/pool/pool.go:400-422`
**Issue:** Defense-in-depth observation: the comment at lines 410-413 says "log the lazy-respawn-success AFTER unlock so the critical section stays narrow." This is correct. But the `label := slot.Label` capture at line 407 is unnecessary — `slot.Label` is immutable for the slot's lifetime (set in `initSlot` at line 298 and never written elsewhere). The capture suggests defensive caution against future mutation, which is fine, but it's also dead-pattern noise that may confuse future readers ("why is label captured but pid not? oh wait, pid IS captured").

Bigger concern: `previousPid` is captured at lines 344-347 BEFORE `slot.Client.Close()`. On the OLD `acp.Client`, calling `Pid()` returns `cmd.Process.Pid` which is set at `cmd.Start()` time and survives `Close()`. The comment correctly notes this. But the `slot.Client != nil` guard at line 345 doesn't catch the case where slot.Client is non-nil but `slot.Client.Pid()` returns 0 (NewWithConn path or test fake) — `previousPid` is then 0 and the log shows `previous_pid: 0`, which is indistinguishable from "no previous pid recorded." Cosmetic only in production (NewWithConn isn't used there), but the test surface uses fakeClient with pid=1001/1002 which is fine.

**Fix:** Add a brief comment clarifying that `previous_pid: 0` indicates a non-spawned (NewWithConn / test) client, OR explicitly elide the field when previousPid is 0:

```go
logArgs := []any{
    "label", label,
    "worker_pid", newPid,
    "reason", "lazy-respawn-success",
}
if previousPid > 0 {
    logArgs = append(logArgs, "previous_pid", previousPid)
}
p.cfg.Logger.Info("pool: slot recovered", logArgs...)
```

This keeps the production log shape unchanged (production always has pid > 0) and stops the test fake path from emitting `previous_pid: 0` which the regression test specifically asserts as 1001 — actually the regression test uses fake pid 1001/1002 so this would not break it. Either approach is defensible; current is acceptable.

## Info

### IN-01: `acp.Client.stderrDrainLoop` second `_ = pipe.Close()` defer is benign-redundant

**File:** `internal/acp/client.go:391-393`
**Issue:** The deferred `_ = pipe.Close()` is documented as "belt-and-braces in case ReadString returns a wrapped error before EOF." In practice, when `cmd.Wait()` runs at Close (client.go:1272), exec.Cmd internally closes its StdinPipe/StdoutPipe/StderrPipe — so the deferred `pipe.Close()` is a double-close on the happy path. `*os.File.Close()` after Close returns `os.ErrClosed`, which is discarded by the `_ =` assignment. Benign.

**Fix:** None required. The defensive close is correct on the rare-error path; the docstring already explains it.

---

### IN-02: `internal/config/config.go` validation order — KIRO_CMD LookPath happens before degenerate-env Warn

**File:** `internal/config/config.go:317-319` vs `374-381`
**Issue:** `exec.LookPath` runs at line 317 (well before AUTH_TOKEN/ALLOWED_IPS degenerate-check at 374-398). If KIRO_CMD fails LookPath, `errs` accumulates. The slog.Default Warn for AUTH_TOKEN/ALLOWED_IPS degeneracy still fires regardless. End result is the same single `errors.Join` surface, which is the documented contract. No issue, just noting the ordering for future maintainers.

**Fix:** None.

---

### IN-03: `internal/admin/tail.go` `TailerRegistry.Get` ignores path argument on cached hit (documented)

**File:** `internal/admin/tail.go:572-581`
**Issue:** Documented behavior — path mismatch on cached name silently returns the cached *Tailer. If a future operator reconfigures `CHAT_TRACE_FILE` mid-run and the gateway re-invokes `Get("chat-trace", newPath)`, the new path is dropped on the floor. Phase 18's D-18-08 (Config.AdminTailPath single source of truth) makes this irrelevant in practice — main.go reads the resolved path once at boot — but worth flagging that the registry is a one-time cache, not a settings store.

**Fix:** None. Behavior matches docstring (lines 521-535).

---

### IN-04: PowerShell `Get-ConfigErrorSentinelPath` uses backslash literal — fine on Windows, awkward on Pwsh-on-Unix

**File:** `scripts/otto-gw.ps1:233`
**Issue:** `Join-Path $home_ '.otto-gw\.config-error'` uses a literal backslash in the second argument. PowerShell's Join-Path normalizes path separators for the platform — on Windows this becomes `\`, on Linux/macOS PowerShell Core it becomes `/`. The result is correct on Windows (the only supported tray surface for this wrapper) but the literal `\.` reads ambiguously as if it could be an escape sequence. The tray Go-side reads via `filepath.Join(home, ".otto-gw", ".config-error")` which always uses platform separator.

**Fix:** None functionally required. Cosmetic: use a forward slash for cross-platform readability — `Join-Path $home_ '.otto-gw/.config-error'` — PowerShell normalizes either way.

## Structural Findings (fallow)

No `<structural_findings>` block was supplied in the prompt; this section is empty by design. Cross-module / dead-code / circular-dep findings are deferred to the structural pre-pass.

---

_Reviewed: 2026-06-11T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
