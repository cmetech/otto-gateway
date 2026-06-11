---
finding: T-6
severity: M
rel_id: REL-TRAY-06
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# T-6: Windows Bundle-Path Stdout Pollution (REL-TRAY-06)

## Review Citation

> **[Medium] T-6: Windows bundle-path parsing breaks — the tray treats the *entire* wrapper stdout as the archive path, and `Initialize-Config`'s `Write-Host` lines land on redirected stdout**
>
> Files: `cmd/otto-tray/tray.go:296-299`, `scripts/otto-gw.ps1:321,330` (`Write-Host "loaded env file: …"`), `scripts/otto-gw.ps1:1644` (`Write-Output $outPath`).

## Current-Source Check

Verified against current source (worktree at commit `3a72d03`):

- **`cmd/otto-tray/tray.go:296-299` (path parsing):** Lines 296–299 show:
  ```go
  path := strings.TrimSpace(res.Stdout)
  if path == "" {
      path = filepath.Join(s.installRoot, "support", "latest"+bundleExt())
  }
  ```
  `res.Stdout` is the entire captured stdout from the wrapper. `strings.TrimSpace` collapses the full multi-line stdout to a single trimmed string. If stdout contains multiple lines (`loaded env file: ...\n<bundle-path>`), the result includes ALL lines joined, making `path` a multi-line string that is NOT a valid filesystem path. **Failure path intact.**

- **`scripts/otto-gw.ps1:321` (first Write-Host chatter):** Line 321 shows:
  ```powershell
  Write-Host "loaded env file: $envFilePath" -ForegroundColor DarkGray
  ```
  `Write-Host` sends output to the console host. When stdout is redirected (as `runWrapper` does in runner.go via `cmd.Stdout = &stdout`), PowerShell's `Write-Host` on PS 5.x sends to the redirected stdout — not the Information stream. **Failure path intact.**

- **`scripts/otto-gw.ps1:330` (second Write-Host chatter):** Line 330 shows:
  ```powershell
  Write-Host "loaded overrides:  $overridesPath" -ForegroundColor DarkGray
  ```
  Same issue — a second chatter line appears on stdout before the archive path. **Failure path intact.**

- **`scripts/otto-gw.ps1:1644` (actual path output):** Line 1644 shows:
  ```powershell
  Write-Output $outPath
  ```
  `Write-Output` correctly sends the archive path on stdout — but only as the LAST line, after any `Write-Host` chatter from `Initialize-Config`.

The fix (`tray.go:296` should take the LAST non-empty line of stdout, not the whole trimmed blob) is not yet present.

## Evidence

Manual reproducer: `tests/reliability/manual/REL-TRAY-06-repro.ps1`

The script invokes `scripts/otto-gw.ps1 support`, captures all stdout lines, and shows the first non-empty line vs. the last non-empty line. It then computes what `tray.go:296`'s `strings.TrimSpace(res.Stdout)` would produce and verifies whether the first line is chatter (pre-fix) or the archive path (post-fix).

Discoverability stub: `cmd/otto-tray/regression_rel_tray_06_test.go` — `TestRegression_REL_TRAY_06_WindowsBundlePathPollution` skips with pointer to the manual script.

## Verdict

**confirmed** — `tray.go:296` uses `strings.TrimSpace(res.Stdout)` which collapses all wrapper stdout (including `Write-Host` chatter lines from `Initialize-Config`) into a non-path string. The `Write-Host "loaded env file: ..."` and `Write-Host "loaded overrides: ..."` lines at ps1:321/330 appear BEFORE the archive path `Write-Output $outPath` at ps1:1644. Phase 16 scope: change `tray.go:296` to take the last non-empty line of stdout rather than the full TrimSpace'd blob.
