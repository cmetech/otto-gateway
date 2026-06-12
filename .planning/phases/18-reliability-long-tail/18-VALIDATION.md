---
phase: 18
slug: reliability-long-tail
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-06-11
---

# Phase 18 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) + goleak for D-18-04 goroutine assertion |
| **Config file** | none — repo-level `go.mod` covers all packages |
| **Quick run command** | `go test ./internal/config/... ./internal/acp/... ./internal/pool/... ./internal/admin/... ./internal/engine/... ./internal/adapter/ollama/... ./cmd/otto-tray/...` |
| **Full suite command** | `go test -race ./...` |
| **Estimated runtime** | ~30s quick / ~90s full |

---

## Sampling Rate

- **After every task commit:** Run package-scoped quick test for the touched package
- **After every plan wave:** Run `go test -race ./...`
- **Before `/gsd-verify-work`:** Full `make ci` exit 0 + `grep -rn "cmd.Stderr = os.Stderr"` returns no production hits
- **Max feedback latency:** ~30s (package-scoped)

---

## Per-Task Verification Map

> Populated by gsd-planner. Each REQ-ID gets at least one regression test asserting new behavior; tests in `t.Skip` state get the Skip removed in the same commit as the fix (v1.9 D-02 pattern).

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 18-01-XX | 01 | 1 | REL-CFG-05 | — | Warn + treat-as-unset on degenerate ALLOWED_IPS/AUTH_TOKEN | unit | `go test ./internal/config/ -run REL_CFG_05` | ❌ W0 | ⬜ pending |
| 18-01-XX | 01 | 1 | REL-CFG-06 | — | Config-named errors for KIRO_CMD/KIRO_CWD with ~ expansion | unit | `go test ./internal/config/ -run REL_CFG_06` | ❌ W0 | ⬜ pending |
| 18-01-XX | 01 | 1 | REL-CFG-07 | — | Bind-then-close port probe in config.Load() | unit | `go test ./internal/config/ -run REL_CFG_07` | ❌ W0 | ⬜ pending |
| 18-02-XX | 02 | 1 | REL-OBSV-03 | — | kiro-cli stderr → structured slog.Warn (goleak clean) | unit | `go test -race ./internal/acp/ -run REL_OBSV_03` | ❌ W0 | ⬜ pending |
| 18-02-XX | 02 | 1 | REL-OBSV-02 | — | Worker recovery slog.Info at lazy-respawn success site | unit | `go test ./internal/pool/ -run REL_OBSV_02` | ❌ W0 | ⬜ pending |
| 18-02-XX | 02 | 1 | REL-HTTP-06 | — | Ollama streaming eng.Run failure → slog.Warn mirroring REL-HTTP-03 fields | unit | `go test ./internal/adapter/ollama/ -run REL_HTTP_06` | ❌ W0 | ⬜ pending |
| 18-02-XX | 02 | 1 | REL-HTTP-07 | — | Panic recovery at 3 goroutine sites | unit | `go test ./internal/admin/ ./internal/pool/ ./internal/engine/ -run REL_HTTP_07` | ❌ W0 | ⬜ pending |
| 18-02-XX | 02 | 1 | REL-OBSV-04 | — | Config.AdminTailPath single source of truth, WARN on missing | unit | `go test ./internal/admin/ -run REL_OBSV_04` | ❌ W0 | ⬜ pending |
| 18-03-XX | 03 | 1 | REL-TRAY-08 | — | Tray StateError surfaces config error via wrapper-written sentinel | unit | `go test ./cmd/otto-tray/ -run REL_TRAY_08` | ❌ W0 | ⬜ pending |
| 18-03-XX | 03 | 1 | REL-TRAY-09 | — | Bundle macOS rows: autostart probe + tray-state.txt removed; existing repro test updated | unit + manual | `go test ./cmd/otto-tray/ -run REL_TRAY_09` + `tests/reliability/manual/REL-TRAY-02-repro.ps1` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

Planner-resolved; each plan declares the test files it creates or updates. Locked items so far:

- [ ] `internal/config/regression_rel_cfg_05_test.go` — Warn + treat-as-unset behavior for degenerate ALLOWED_IPS/AUTH_TOKEN
- [ ] `internal/config/regression_rel_cfg_06_test.go` — Named KIRO_CMD/KIRO_CWD errors with ~ expansion
- [ ] `internal/config/regression_rel_cfg_07_test.go` — Bind-then-close port probe
- [ ] `internal/acp/regression_rel_obsv_03_test.go` — stderr scanner + slog assertion + `goleak.VerifyNone`
- [ ] `internal/pool/regression_rel_obsv_02_test.go` — Worker recovery log at success site
- [ ] `internal/adapter/ollama/regression_rel_http_06_test.go` — eng.Run failure WARN with mirrored fields
- [ ] `internal/admin/regression_rel_http_07_test.go` — admin tailer panic recover
- [ ] `internal/pool/regression_rel_http_07_test.go` — pool ctx-watcher panic recover
- [ ] `internal/engine/regression_rel_http_07_test.go` — engine watchdog panic recover (planner to confirm watchdog site per research open question)
- [ ] `internal/admin/regression_rel_obsv_04_test.go` — Config.AdminTailPath used by writer + tailer; WARN on missing
- [ ] `cmd/otto-tray/regression_rel_tray_08_test.go` — makeProbe sentinel read → StateError with config-error Detail
- [ ] `cmd/otto-tray/regression_rel_tray_09_test.go` — bundle macOS row set excludes autostart + tray-state.txt
- [ ] Update `tests/reliability/manual/REL-TRAY-02-repro.ps1` — assert macOS-side row removals in the same commit (D-18-10 side-effect)

*Where existing tests are `t.Skip`'d for these REQ-IDs, the Skip is removed in the same commit as the fix.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Tray icon shows StateError with config-error tooltip on bad dotenv | REL-TRAY-08 | Tray UI is OS-native; automated screenshot harness not in v1.10.3 scope | 1) Write malformed `.env` line; 2) Launch `scripts/otto-gw` from terminal; 3) Confirm wrapper writes `~/.otto-gw/.config-error` sentinel; 4) Tray icon flips to error state within one poll (~3–6s); 5) Hover tooltip shows `config error: <first sentinel line>`. |
| macOS support-bundle row set (operator visual check) | REL-TRAY-09 | Bundle is operator-only diagnostic; visual confirmation appropriate | On macOS: run support-bundle command; confirm autostart probe row absent and `tray-state.txt` row absent from the bundle output. |
| Boot-time auth-state line stays INFO | REL-CFG-05 (side-effect) | One-line log-level assertion across boot output | Set degenerate `ALLOWED_IPS=","`; boot gateway; confirm `cmd/otto-gateway/main.go:115-120` line emits at INFO with `enabled=false ip_allowlist=false` AND a separate WARN line fires from config.Load(). |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s (package-scoped)
- [ ] `go test -race ./...` clean tree-wide
- [ ] `grep -rn "cmd.Stderr = os.Stderr"` returns no production-code hits
- [ ] `make ci` exit 0 (v1.10.2 baseline preserved)
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
