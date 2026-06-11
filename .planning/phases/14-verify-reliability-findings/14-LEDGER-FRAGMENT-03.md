# Phase 14 Ledger Fragment 03 — Tray / Wrapper (T-1..T-7)

| Finding ID | Sev | REL-* ID | Status | File:line | Evidence | Target phase |
|---|---|---|---|---|---|---|
| T-1 | H | REL-TRAY-01 | confirmed | cmd/otto-tray/tray.go:144-145, scripts/otto-gw:782-784, scripts/otto-gw.ps1:562-564 | 14-FINDING-T-1.md | 15 |
| T-2 | H | REL-TRAY-02 | confirmed | scripts/otto-gw.ps1:581-593 (Get-GatewayStatus exit 1), scripts/otto-gw.ps1:1464 (Invoke-Support) | 14-FINDING-T-2.md | 15 |
| T-3 | H | REL-TRAY-03 | confirmed | cmd/otto-tray/tray.go:199-201, cmd/otto-tray/tray.go:74-75, cmd/otto-tray/uihelpers_darwin.go:43-58 | 14-FINDING-T-3.md | 15 |
| T-4 | M | REL-TRAY-04 | confirmed | cmd/otto-tray/tray.go:199-201 (applyState), cmd/otto-tray/uihelpers_windows.go:50-68 | 14-FINDING-T-4.md | 16 |
| T-5 | M | REL-TRAY-05 | confirmed | cmd/otto-tray/tray.go:153 (snapshot error swallowed), cmd/otto-tray/fsm.go:52-54 | 14-FINDING-T-5.md | 16 |
| T-6 | M | REL-TRAY-06 | confirmed | cmd/otto-tray/tray.go:296-299, scripts/otto-gw.ps1:321,330,1644 | 14-FINDING-T-6.md | 16 |
| T-7 | M | REL-TRAY-07 | confirmed | scripts/otto-gw:1864-1873 (live logs uncapped), scripts/otto-gw:1957-1989 (gz-only cap), cmd/otto-tray/runner.go:29-33 | 14-FINDING-T-7.md | 16 |
