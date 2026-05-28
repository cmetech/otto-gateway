---
phase: quick
plan: 260528-qe9
status: complete
subsystem: ops-consistency
tags: [wrappers, defaults, kiro-cli, otto-gw]
dependency_graph:
  requires: []
  provides:
    - "Bash wrapper OTTO_ADDR probe target aligned with PowerShell wrapper (127.0.0.1)"
    - "Gateway KIRO_CMD compiled-in default aligned with installed binary name (kiro)"
  affects:
    - "scripts/otto-gw"
    - "internal/config/config.go"
    - "internal/acp/client.go"
    - "internal/config/config_test.go"
tech_stack:
  added: []
  patterns:
    - "Wrapper/binary naming-default alignment (operator-error-message coherence)"
key_files:
  created:
    - ".planning/quick/260528-qe9-wrapper-naming-probe-consistency-cleanup/260528-qe9-SUMMARY.md"
  modified:
    - "scripts/otto-gw (line 39 — probe default)"
    - "internal/config/config.go (line 44 doc + line 166 default)"
    - "internal/acp/client.go (line 37 doc + line 52 applyDefaults fallback)"
    - "internal/config/config_test.go (TestLoadDefaults expectation)"
decisions:
  - "Kept the descriptive phrase \"kiro-cli binary\" in doc comments — only the parenthetical default value flipped to \"kiro\". The subprocess role description is unchanged; only the compiled-in default string is."
  - "Committed bash wrapper change separately from Go changes (two atomic commits per plan constraint A/B split). Wrapper change has no Go test interaction; isolating it preserves bisectability."
metrics:
  duration_minutes: 7
  completed_date: "2026-05-28"
  tasks_total: 1
  tasks_completed: 1
  commits: 2
---

# Quick Plan 260528-qe9: Wrapper Naming + Probe Consistency Cleanup Summary

**One-liner:** Aligned bash wrapper's `OTTO_ADDR` probe default to `127.0.0.1` (mirrors PowerShell `.ps1`) and flipped the gateway's compiled-in `KIRO_CMD` default from `"kiro-cli"` to `"kiro"` (the binary name both wrappers actually auto-detect).

## What Changed

### Commit A — `5f55717` — bash wrapper probe default

`scripts/otto-gw` line 39:

```diff
-OTTO_ADDR="${OTTO_ADDR:-http://localhost:18080}"
+OTTO_ADDR="${OTTO_ADDR:-http://127.0.0.1:18080}"
```

Brings the bash wrapper in line with `scripts/otto-gw.ps1` line 67 (already `127.0.0.1` post-commit `7185293`). Harmless on stock Linux/macOS today, but removes a latent footgun on WSL2 mirrored-mode and dual-stack hosts where `localhost` may resolve to `::1` while the gateway only binds `127.0.0.1`.

### Commit B — `8aacc0c` — gateway `KIRO_CMD` default

Four Go-file edits in one commit. Two doc-comment alignments, the actual `Load()` default flip, the `applyDefaults` zero-value fallback flip, and the matching test expectation update:

```diff
 // internal/config/config.go:44
-// KiroCmd is the kiro-cli binary name or path (default "kiro-cli").
+// KiroCmd is the kiro-cli binary name or path (default "kiro").

 // internal/config/config.go:166
-kiroCmd := getEnvStr("KIRO_CMD", "kiro-cli")
+kiroCmd := getEnvStr("KIRO_CMD", "kiro")

 // internal/acp/client.go:37
-// Command is the kiro-cli binary path or name (default "kiro-cli").
+// Command is the kiro-cli binary path or name (default "kiro").

 // internal/acp/client.go:52
-c.Command = "kiro-cli"
+c.Command = "kiro"

 // internal/config/config_test.go:24-25
-if cfg.KiroCmd != "kiro-cli" {
-    t.Errorf("KiroCmd: got %q, want %q", cfg.KiroCmd, "kiro-cli")
+if cfg.KiroCmd != "kiro" {
+    t.Errorf("KiroCmd: got %q, want %q", cfg.KiroCmd, "kiro")
 }
```

The operator-facing impact: a bare-binary run (`./bin/otto-gateway` with `KIRO_CMD` unset) now spawns/errors against `"kiro"`, matching what both wrappers' preflight already searches for via `command -v kiro` / `Get-Command kiro`. Previously the wrapper would say "kiro not found" while the gateway error would say "kiro-cli not found" — eliminating that mismatch was the point.

## Verification Performed

| Check | Result |
| --- | --- |
| `go build ./...` | clean exit |
| `go test -race -count=1 ./cmd/otto-gateway/... ./internal/engine/... ./internal/acp/... ./internal/config/...` | all four packages PASS |
| `grep -n 'OTTO_ADDR:-http://127.0.0.1:18080' scripts/otto-gw` | matches line 39 |
| `! grep -n 'OTTO_ADDR:-http://localhost:18080' scripts/otto-gw` | confirmed absent |
| `grep -n 'getEnvStr("KIRO_CMD", "kiro")' internal/config/config.go` | matches line 166 |
| `grep -n 'c.Command = "kiro"' internal/acp/client.go` | matches line 52 |
| `git log --oneline -2 \| grep -E "fix\((otto-gw\|config)\):"` | matches both commits |
| `git diff --stat HEAD~2..HEAD` scope | exactly 4 files (scripts/otto-gw + 3 Go files), 7+/7-, no drift |

## Out-of-Scope Sites Confirmed Untouched

Per the planner's identification, the following `"kiro-cli"` occurrences are deliberate test inputs or real-binary discovery targets, NOT default-value sites — they remained unchanged:

- `internal/acp/cancel_test.go:40` — `Command: "kiro-cli"` test input
- `internal/acp/client_test.go:76, 338` — `Command: "kiro-cli"` test inputs
- `internal/acp/integration_test.go:36, 79` — `LookPath("kiro-cli")` + test input
- `internal/adapter/anthropic/integration_test.go:42` — `LookPath("kiro-cli")`
- `internal/adapter/ollama/integration_test.go:64` — `LookPath("kiro-cli")`
- `internal/adapter/openai/integration_test.go:39` — `LookPath("kiro-cli")`
- `internal/engine/acp_adapter_test.go:126` — `Command: "kiro-cli"` test input
- `internal/pool/pool_test.go:204, 269` — `LookPath("kiro-cli")` + `KiroCmd: "kiro-cli"` test input
- `internal/config/config_test.go:45, 58` — `TestLoadEnvOverrides` sets env to `/usr/local/bin/kiro-cli` (testing override path, not default)
- `scripts/otto-gw` lines referencing `kiro-cli` in env-var documentation prose (e.g. line 241 "KIRO_CMD, KIRO_ARGS, KIRO_CWD — kiro-cli subprocess wiring") — descriptive role-of-subprocess prose, not binary name

All targeted tests pass without modification, confirming these sites correctly exercise the Command/KiroCmd-is-honored path independent of the default string.

## Deviations from Plan

None — plan executed exactly as written. No fifth default-value site discovered; the planner's site map was complete.

## Deferred Follow-up (Noted, Not Fixed)

`cmd/otto-gateway/main_test.go:106` carries a stale documentation comment that mentions the historical `"kiro-cli"` default by name:

```go
// Force degraded mode AFTER config.Load: getEnvStr("KIRO_CMD",
// "kiro-cli") falls back to the default when the env value
// is empty, so we cannot disable pool construction via env
// alone. Overriding the resolved Config field is the
// supported degraded-mode entrypoint (mirrors how
// TestApp_NoKiroCmd_StartsHealthOnly builds its cfg literal).
cfg.KiroCmd = ""
```

The test itself is correct (it overrides `cfg.KiroCmd = ""` after `Load()`, which is independent of the default value). The comment's quoted `"kiro-cli"` is now mildly stale — should read `"kiro"`. This was NOT modified because:

1. The plan explicitly enumerated the files in scope and `cmd/otto-gateway/main_test.go` was not among them.
2. The context note instructed: "if you find a fifth `"kiro-cli"` default-value site the planner missed, STOP and flag it … rather than silently expanding scope." This is a comment, not a code default site, but the same scope-discipline applies.
3. A trivial doc-only follow-up can flip the comment in a future cleanup without affecting any behaviour or test outcome.

Recommend: include in a future tidy pass or hand-fix when next touching that file.

## Commits

| Hash | Subject | Files |
| --- | --- | --- |
| `5f55717` | `fix(otto-gw): default OTTO_ADDR probe to 127.0.0.1 (Linux/macOS consistency with .ps1 #7185293)` | `scripts/otto-gw` |
| `8aacc0c` | `fix(config): default KIRO_CMD to "kiro" (matches what wrappers auto-detect)` | `internal/config/config.go`, `internal/acp/client.go`, `internal/config/config_test.go` |

## Self-Check: PASSED

- `scripts/otto-gw` line 39 verified at `OTTO_ADDR="${OTTO_ADDR:-http://127.0.0.1:18080}"` (grep match)
- `internal/config/config.go` line 166 verified at `kiroCmd := getEnvStr("KIRO_CMD", "kiro")` (grep match)
- `internal/acp/client.go` line 52 verified at `c.Command = "kiro"` (grep match)
- `internal/config/config_test.go` `TestLoadDefaults` verified asserting `"kiro"` (read-after-edit, test pass)
- Commit `5f55717` present in `git log` for `fix(otto-gw): …`
- Commit `8aacc0c` present in `git log` for `fix(config): …`
- `git diff --stat HEAD~2..HEAD` shows exactly the 4 planned files (no drift)
- `go build ./...` clean
- `go test -race -count=1 ./cmd/otto-gateway/... ./internal/engine/... ./internal/acp/... ./internal/config/...` all 4 packages PASS
