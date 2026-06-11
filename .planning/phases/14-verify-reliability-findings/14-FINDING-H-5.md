---
finding: H-5
severity: M
rel_id: REL-HTTP-05
status: confirmed
target_phase: 16
verified_at: "2026-06-11"
---

# H-5: Admin Tailer Per-Line Cap Bypassed by Newline-Terminated Lines

## Review Citation

**Source:** `docs/reviews/2026-06-11-reliability-review.md` §H-5 (section "2. HTTP surface reliability")

> `readLines` truncates only when `len(current) > TailerMaxLineBytes && !strings.HasSuffix(current, "\n")`. `bufio.Reader.ReadString('\n')` returns the entire line regardless of length, so any complete line bypasses the cap. With `CHAT_TRACE=true` (chat-trace NDJSON embeds full prompts; bodies allowed up to 4 MiB) and the chat-trace tail open in the admin UI, the 500-line ring can hold 500 × multi-MB strings (GB-scale RSS) and each line ships whole to the browser over SSE.

Cited source file:line: `internal/admin/tail.go:393-427` (cap check at :402).

## Current-Source Check

**`internal/admin/tail.go:393-427`** — `readLines` function verified:

```go
func (t *Tailer) readLines(r *bufio.Reader, carry string) (string, error) {
    current := carry
    for {
        chunk, err := r.ReadString('\n')
        if len(chunk) > 0 {
            current += chunk
            // Enforce the per-line size cap to bound memory growth in
            // case a log producer never emits a newline. If the carry
            // exceeds TailerMaxLineBytes, truncate it and emit a marker.
            if len(current) > TailerMaxLineBytes && !strings.HasSuffix(current, "\n") {
                // ← line 402: cap is ONLY enforced for unterminated lines
                t.logger.Debug("admin: tailer line exceeds max", ...)
                t.broadcast(current[:TailerMaxLineBytes])
                current = ""
                continue
            }
            if strings.HasSuffix(current, "\n") {
                line := strings.TrimSuffix(current, "\n")
                line = strings.TrimSuffix(line, "\r")
                t.broadcast(line) // ← broadcasts FULL multi-MB line if it has \n
                current = ""
            }
        }
        // ... EOF / error handling
    }
}
```

Confirmed: the boolean guard `&& !strings.HasSuffix(current, "\n")` at line 402 makes the truncation check a no-op for any line that is newline-terminated. `bufio.Reader.ReadString('\n')` returns the full line contents including the `\n` byte regardless of length — it has no internal buffer cap in this usage (we call `ReadString` not via `bufio.Scanner` which does enforce a token size limit). Therefore:

1. A 5 MB line terminated by `\n` arrives as one call to `ReadString('\n')`.
2. `current += chunk` → `len(current) == 5_242_880 + 1` (including the `\n`).
3. Cap check: `len(current) > 1_048_576 && !strings.HasSuffix(current, "\n")` → `true && false` → **false**. Cap not enforced.
4. `strings.HasSuffix(current, "\n")` → true → `broadcast(line)` where `len(line) == 5_242_880`.

The comment on the cap block says it "bounds memory growth in case a log producer never emits a newline" — this correctly identifies the partial-line case, but the full-line case is missed entirely.

`TailerMaxLineBytes = 1_048_576` (1 MB) is defined at `tail.go:57`.

## Evidence

Regression test file: `internal/admin/regression_rel_http_05_test.go`
Function: `TestRegression_REL_HTTP_05_AdminTailerLineCapBypass`

The test creates a `Tailer` on a temp log file, subscribes, appends a single line of `5×TailerMaxLineBytes` (5 MB) terminated by `\n` via `appendToFile`, and waits for the subscriber channel to deliver it via `waitLines`. Pre-fix observable: `len(received[0]) > TailerMaxLineBytes` — the full 5 MB line was delivered, bypassing the 1 MB cap. The test is currently `t.Skip`'d per D-12 and will be unskipped in the Phase 16 fix commit.

## Verdict

**CONFIRMED** — The per-line cap in `readLines` at `tail.go:402` is gated by `!strings.HasSuffix(current, "\n")`, which exempts all newline-terminated lines from truncation. The doc comment claiming the cap "bounds memory growth" is accurate only for the partial-line (no-newline) case. Any complete log line of arbitrary size flows into `t.broadcast()` and thus into the ring buffer and SSE stream unbounded.

With `CHAT_TRACE=true`, the chat-trace NDJSON log writes full 4 MiB prompts as single newline-terminated lines. The admin UI's log-tail panel for the chat-trace source will receive these lines at full size, and the ring buffer can accumulate up to `RingBufferLines` (500) such lines.

Assigned to Phase 16 for fix. Fix per review: remove the `&& !strings.HasSuffix(current, "\n")` guard so the cap is enforced unconditionally — truncate `current` to `TailerMaxLineBytes` and broadcast the truncated form whenever `len(current) > TailerMaxLineBytes`, regardless of terminator presence.
