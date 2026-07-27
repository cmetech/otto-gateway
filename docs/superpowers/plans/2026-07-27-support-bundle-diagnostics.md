# Support Bundle Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a redacted support bundle with separate Gateway, Kiro, and Co-worker logs plus timestamped Gateway metrics and enabled ACP-capture snapshots.

**Architecture:** Add a JSON-preserving redacted mode to capture GET, pass the tray's detected Co-worker home to the wrapper, and implement one explicit diagnostic manifest in both shell wrappers.

**Tech Stack:** Go 1.24, Bash 3.2, PowerShell 5.1+, curl/Invoke-WebRequest, existing archive/redaction tools.

## Global Constraints

- Layout: `logs/gateway`, `logs/kiro`, `logs/co-worker`.
- Co-worker remains bundle-only.
- Include approved Hermes current/numeric-rotation/profile logs; exclude curator.
- Regular files only; never follow symlinks/reparse points.
- One UTC `YYYYMMDD-HHMMSSZ` snapshot timestamp.
- Always attempt metrics; include capture only when enabled.
- Snapshot requests never mutate runtime state.
- Preserve valid JSON during capture redaction and warn about sensitive user content.
- Partial collection succeeds with manifest statuses.
- Drop oldest rotations before current logs/snapshots.
- POSIX and PowerShell remain equivalent.
- Use TDD and commit after each task.

---

## File Structure

- `internal/admin/capture.go` and new `capture_redact.go` — redacted support export.
- `cmd/otto-tray/openfolder.go`, `runner*.go`, `tray.go` — Co-worker-home plumbing.
- `scripts/gw` and `scripts/gw.ps1` — organized collectors and snapshots.
- `scripts/lib/redact.*` — remote-write token defense.
- `tests/scripts/test-support-*` — integration/redaction coverage.
- `docs/operating.md` — user documentation.

### Task 1: JSON-preserving capture support export

**Files:**
- Modify: `internal/admin/capture.go`
- Create: `internal/admin/capture_redact.go`
- Modify: `internal/admin/capture_test.go`

**Interfaces:**
- GET: `/admin/api/acp-capture?support=redacted`.
- Helpers: `redactCaptureFrames([]CaptureFrame) []CaptureFrame` and `redactCapturedParams(string) string`.

- [ ] **Step 1: Write failing endpoint tests**

Build an enabled frame containing nested authorization/API-key/token/password keys, bearer/header strings, and `safe-value`. Request support mode, decode with `json.Unmarshal`, assert secrets are absent, safe data remains, and source frames are unchanged. Add malformed params and ordinary GET cases.

~~~go
rec := doGet(t, h, "/api/acp-capture?support=redacted")
if rec.Code != http.StatusOK { t.Fatalf("status=%d", rec.Code) }
var body struct { Enabled bool; Frames []admin.CaptureFrame }
if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil { t.Fatalf("invalid JSON: %v", err) }
for _, secret := range []string{"glc_secret", "Bearer abc.def", "Basic dXNlcjp0b2tlbg=="} {
	if strings.Contains(rec.Body.String(), secret) { t.Fatalf("secret leaked: %q", secret) }
}
~~~

Run: `go test ./internal/admin -run AcpCaptureSupport -count=1`  
Expected: FAIL.

- [ ] **Step 2: Implement recursive redaction**

Compile case-insensitive patterns for authorization, API key, token, key, secret, password, passphrase, bearer, and header values. Implement copy-on-redact:

~~~go
func redactCaptureFrames(in []CaptureFrame) []CaptureFrame {
	out := make([]CaptureFrame, len(in))
	copy(out, in)
	for i := range out { out[i].Params = redactCapturedParams(out[i].Params) }
	return out
}

func redactCapturedParams(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil { return redactCaptureString(raw) }
	value = redactCaptureValue("", value)
	body, err := json.Marshal(value)
	if err != nil { return "[REDACTED: invalid captured JSON]" }
	return string(body)
}
~~~

`redactCaptureValue` recursively handles maps, arrays, and strings; secret-named values become `[REDACTED]`. Work on decoded strings, never serialized outer JSON.

- [ ] **Step 3: Select support mode without mutation**

After one ordinary snapshot:

~~~go
if req.Method == http.MethodGet && req.URL.Query().Get("support") == "redacted" {
	resp.Frames = redactCaptureFrames(resp.Frames)
}
~~~

No controller mutator is called.

- [ ] **Step 4: Verify and commit**

~~~bash
gofmt -w internal/admin/capture.go internal/admin/capture_redact.go internal/admin/capture_test.go
go test -race ./internal/admin -run AcpCapture -count=1
git add internal/admin/capture.go internal/admin/capture_redact.go internal/admin/capture_test.go
git commit -m "feat: add redacted ACP capture support export"
~~~

### Task 2: Pass detected Co-worker home from tray

**Files:**
- Modify: `cmd/otto-tray/openfolder.go` and tests
- Modify: `cmd/otto-tray/runner.go`, both platform runners, and tests
- Modify: `cmd/otto-tray/tray.go`

**Interfaces:**
- `detectedHermesHome(...) (string, bool)` uses `desktopOutput.Candidate`.
- `runWrapper(installDir, gwHome, verb string, extraArgs ...string) runResult`.
- POSIX `--co-worker-home PATH`; Windows `-CoworkerHome PATH`.

- [ ] **Step 1: Write failing pure tests**

Assert a detected candidate resolves the same home as existing `resolveHermesHome` even when stopped; nil candidate returns false. Extend runner tests so support extras follow the verb.

Run: `go test ./cmd/otto-tray -run 'DetectedHermesHome|WrapperPath' -count=1`  
Expected: compile failures.

- [ ] **Step 2: Implement detection and native args**

The helper requires a candidate but does not call `runningDesktopCandidate`.

~~~go
func supportCoworkerArgs(goos, home string) []string {
	if strings.TrimSpace(home) == "" { return nil }
	if goos == "windows" { return []string{"-CoworkerHome", home} }
	return []string{"--co-worker-home", home}
}
~~~

Make runner functions variadic and append extras. In `handleSupportBundle` derive from `s.desktopCurrent.Load()` and pass extras. No candidate means scripts use `HERMES_HOME` fallback.

- [ ] **Step 3: Verify and commit**

~~~bash
gofmt -w cmd/otto-tray/openfolder.go cmd/otto-tray/openfolder_test.go cmd/otto-tray/runner.go cmd/otto-tray/runner_darwin.go cmd/otto-tray/runner_windows.go cmd/otto-tray/runner_test.go cmd/otto-tray/tray.go
go test ./cmd/otto-tray -count=1
git add cmd/otto-tray
git commit -m "feat: pass Co-worker home to support collection"
~~~

### Task 3: POSIX bundle organization and snapshots

**Files:**
- Modify: `scripts/gw`, `scripts/lib/redact.sh`
- Modify: `tests/scripts/test-support-bundle.sh`, `test-support-redact.sh`

**Interfaces:**
- `--co-worker-home` then `HERMES_HOME` fallback.
- Kiro source: `KIRO_CHAT_LOG_FILE` then `GW_HOME/logs/kiro-chat.log`.
- Snapshot GETs: `GW_ADDR/metrics` and redacted capture.

- [ ] **Step 1: Expand the failing fixture**

Create Gateway/Kiro current and rotations. Create a Co-worker home with the six core logs, four ancillary logs, numeric rotations, `profiles/work/logs/errors.log`, unrelated/curator files, and a symlink to an external secret. Serve deterministic metrics and enabled capture JSON locally. Assert exact folders, timestamped snapshots, profile preservation, exclusions, and no secrets.

Run: `bash tests/scripts/test-support-bundle.sh`  
Expected: FAIL on flat layout.

- [ ] **Step 2: Implement safe explicit collection**

Parse `--co-worker-home`. Create all standard sections plus the three app directories. Resolve `coworker_home`, `kiro_log`, and `snapshot_ts` once. Add a helper requiring regular non-symlink source, creating the destination parent, and piping through `redact_stream`.

Move Gateway logs to `logs/gateway`. Put Kiro current/numeric rotations in `logs/kiro`. In Co-worker root and immediate `profiles/*/logs`, allow only:

~~~text
agent.log
errors.log
gateway.log
gui.log
desktop.log
mcp-stderr.log
gateway-shutdown-watchdog.log
dashboard-auth.log
container-boot.log
tool_calls.log
~~~

Allow numeric suffixes, preserve profile paths, and exclude curator by construction. Keep Gateway compressed rotations only; do not copy compressed Kiro/Co-worker logs without decompression plus redaction.

- [ ] **Step 3: Add snapshots and manifest status**

Write successful metrics to `logs/gateway/metrics-snapshot-YYYYMMDD-HHMMSSZ.prom`. Fetch redacted capture to a temporary file: enabled true moves to the timestamped JSON; enabled false produces no file and `disabled`; invalid/unreachable is `unavailable`. Record metrics/capture states and the review-before-sharing warning. Never POST.

- [ ] **Step 4: Generalize cap and secret handling**

Add `GW_METRICS_REMOTE_WRITE_TOKEN` to explicit assignment redaction and secret lists. Include remote-write URL/user/token/interval in effective env with masked token. Drop oldest rotations across all app trees before current logs/snapshots and record omissions.

- [ ] **Step 5: Verify and commit**

~~~bash
bash tests/scripts/test-support-redact.sh
bash tests/scripts/test-support-bundle.sh
shellcheck scripts/gw scripts/lib/redact.sh tests/scripts/test-support-bundle.sh tests/scripts/test-support-redact.sh
git diff --check
git add scripts/gw scripts/lib/redact.sh tests/scripts/test-support-bundle.sh tests/scripts/test-support-redact.sh
git commit -m "feat: organize POSIX support diagnostics"
~~~

### Task 4: PowerShell parity

**Files:**
- Modify: `scripts/gw.ps1`, `scripts/lib/redact.ps1`
- Modify: `tests/scripts/test-support-bundle.ps1`, `test-support-redact.ps1`

**Interfaces:**
- `-CoworkerHome` then `HERMES_HOME` fallback.
- Same relative paths/status meanings as Task 3.

- [ ] **Step 1: Expand failing Windows fixture**

Mirror POSIX data and assertions, including a reparse-point case when permitted and deterministic HTTP snapshots.

Run: `pwsh -NoProfile -File tests/scripts/test-support-bundle.ps1`  
Expected: FAIL.

- [ ] **Step 2: Implement equivalent collection**

Add `[string]$CoworkerHome`. Resolve detected/fallback Co-worker home, Kiro log, and UTC timestamp. Create app subdirectories. The helper uses `Get-Item -LiteralPath`, rejects `FileAttributes.ReparsePoint`, creates parents, and streams through `Invoke-RedactStream`. Apply the identical basename/rotation/profile rules.

- [ ] **Step 3: Add validated snapshots, cap, and redaction**

Use `Invoke-WebRequest` and `ConvertFrom-Json`. Write only enabled capture and the same manifest states/warning. Drop oldest rotations first. Extend explicit remote-write token redaction and effective env exactly as POSIX.

- [ ] **Step 4: Verify and commit**

~~~powershell
pwsh -NoProfile -File tests/scripts/test-support-redact.ps1
pwsh -NoProfile -File tests/scripts/test-support-bundle.ps1
~~~

~~~bash
git diff --check
git add scripts/gw.ps1 scripts/lib/redact.ps1 tests/scripts/test-support-bundle.ps1 tests/scripts/test-support-redact.ps1
git commit -m "feat: organize Windows support diagnostics"
~~~

### Task 5: Document and verify end to end

**Files:**
- Modify: `docs/operating.md`

- [ ] **Step 1: Document exact behavior**

Document both Co-worker-home flags, the three subtrees, selected Hermes logs, metrics/capture conditions, statuses, symlink exclusion, cap priority, and sensitive-content warning.

- [ ] **Step 2: Run final verification**

~~~bash
go test -race ./internal/admin ./cmd/otto-tray -count=1
bash tests/scripts/test-support-redact.sh
bash tests/scripts/test-support-bundle.sh
pwsh -NoProfile -File tests/scripts/test-support-redact.ps1
pwsh -NoProfile -File tests/scripts/test-support-bundle.ps1
go vet ./internal/admin ./cmd/otto-tray
git diff --check
~~~

Expected: all available commands PASS. If `pwsh` is absent, record the exact limitation and defer those commands to Windows CI.

- [ ] **Step 3: Commit**

~~~bash
git add docs/operating.md
git commit -m "docs: explain support diagnostic contents"
~~~

