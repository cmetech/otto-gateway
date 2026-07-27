# Metrics Remote-Write Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Grafana remote write require only the API key in `overrides.env` while keeping metrics sending disabled until the user enables the tray checkbox.

**Architecture:** Ship the shared non-secret Grafana endpoint, user, and 30-second interval as active generated `.env` defaults. Keep `GW_METRICS_REMOTE_WRITE_TOKEN` absent from defaults and in operator-owned overrides, then provide immediate tray feedback when enabling without a complete credential.

**Tech Stack:** dotenv template, Go tray configuration, systray notification seam, Bash configuration test, operator Markdown.

## Global Constraints

- Active defaults are exactly:
  - `GW_METRICS_REMOTE_WRITE_URL=https://prometheus-prod-66-prod-us-east-3.grafana.net/api/prom/push`
  - `GW_METRICS_REMOTE_WRITE_USER=3370048`
  - `GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=30`
- Generated defaults never contain an active `GW_METRICS_REMOTE_WRITE_TOKEN`.
- The only user-added override is `GW_METRICS_REMOTE_WRITE_TOKEN=<Grafana API key>`.
- Preserve the existing variable name for compatibility.
- Remote sending remains disabled by default and controlled by the persisted tray checkbox.
- Never store URL, user, or token in `tray.json`; only the enabled boolean belongs there.
- Never print or log the token.
- Missing-key notification happens on enable, not every writer interval.
- Follow red-green-refactor and commit after each task.

---

## File Structure

- `scripts/.env.example` — active shared defaults and secret instructions.
- Create `tests/scripts/test-metrics-remote-write-defaults.sh` — exact template contract.
- `cmd/otto-tray/remotewrite_config.go` — report missing required values.
- `cmd/otto-tray/remotewrite_test.go` — ready/missing behavior and dotenv precedence.
- `cmd/otto-tray/tray.go` — notify after an incomplete enable action.
- `docs/operator-quickstart.md` — packaged end-user setup.
- `docs/operating.md` — complete operator reference.

### Task 1: Activate shared non-secret defaults

**Files:**
- Modify: `scripts/.env.example:182-199`
- Create: `tests/scripts/test-metrics-remote-write-defaults.sh`

**Interfaces:**
- Produces the three exact active dotenv assignments.
- Keeps token and `GW_METRICS_REMOTE_WRITE_ENABLED` inactive.

- [ ] **Step 1: Write the failing template contract**

Create an executable Bash test that counts exact active lines:

~~~bash
#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd -P "$(dirname "$0")/../.." && pwd)"
template="$repo_root/scripts/.env.example"

assert_once() {
    local line="$1"
    [[ "$(grep -Fxc "$line" "$template")" -eq 1 ]]
}

assert_once 'GW_METRICS_REMOTE_WRITE_URL=https://prometheus-prod-66-prod-us-east-3.grafana.net/api/prom/push'
assert_once 'GW_METRICS_REMOTE_WRITE_USER=3370048'
assert_once 'GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=30'

if grep -Eq '^GW_METRICS_REMOTE_WRITE_TOKEN=' "$template"; then
    echo 'active remote-write token must not ship in defaults' >&2
    exit 1
fi
if grep -Eq '^GW_METRICS_REMOTE_WRITE_ENABLED=true$' "$template"; then
    echo 'remote write must remain opt-in' >&2
    exit 1
fi
~~~

Run: `bash tests/scripts/test-metrics-remote-write-defaults.sh`  
Expected: FAIL because URL/user/interval are commented placeholders.

- [ ] **Step 2: Update the template**

Replace placeholder/commented endpoint lines with the exact active assignments from Global Constraints. Remove the token assignment example and retain only a comment showing that `GW_METRICS_REMOTE_WRITE_TOKEN=<Grafana API key>` belongs in `overrides.env`. Keep `GW_METRICS_REMOTE_WRITE_ENABLED=false` commented so tray state remains the opt-in control.

Update the prose to say endpoint/user/interval are shared defaults and the user supplies only the API key.

- [ ] **Step 3: Verify and commit**

~~~bash
chmod +x tests/scripts/test-metrics-remote-write-defaults.sh
bash tests/scripts/test-metrics-remote-write-defaults.sh
shellcheck tests/scripts/test-metrics-remote-write-defaults.sh
git diff --check
git add scripts/.env.example tests/scripts/test-metrics-remote-write-defaults.sh
git commit -m "feat: default Grafana remote-write endpoint"
~~~

### Task 2: Notify when enabling without an API key

**Files:**
- Modify: `cmd/otto-tray/remotewrite_config.go`
- Modify: `cmd/otto-tray/remotewrite_test.go`
- Modify: `cmd/otto-tray/tray.go:606-633`

**Interfaces:**
- Produces: `remoteWriteConfig.missingRequiredFields() []string`.
- Produces: `remoteWriteEnableWarning(remoteWriteConfig) string`; empty means ready.
- Consumes: existing `notify("Gateway", body)` seam.

- [ ] **Step 1: Write failing config/warning tests**

Create a temporary `.env` with the three shared defaults and no token. Assert load returns exact URL/user/30 seconds, `ready()` is false, missing fields equals `[]string{"API key"}`, and the warning names `GW_METRICS_REMOTE_WRITE_TOKEN` without containing any token value.

Add a ready case with token in `overrides.env` and assert the warning is empty. Marshal `TrayConfig{MetricsRemoteWriteEnabled: boolPtr(true)}` and assert JSON contains only the boolean preference, not URL/user/token strings.

Run: `go test ./cmd/otto-tray -run 'RemoteWriteMissing|RemoteWriteEnableWarning|TrayConfig.*Remote' -count=1`  
Expected: compile failures for the helpers.

- [ ] **Step 2: Implement deterministic missing-field reporting**

~~~go
func (c remoteWriteConfig) missingRequiredFields() []string {
	var missing []string
	if c.URL == "" { missing = append(missing, "endpoint") }
	if c.User == "" { missing = append(missing, "account user") }
	if c.Token == "" { missing = append(missing, "API key") }
	return missing
}

func remoteWriteEnableWarning(c remoteWriteConfig) string {
	missing := c.missingRequiredFields()
	if len(missing) == 0 { return "" }
	return "Metrics sending is enabled but missing " + strings.Join(missing, ", ") +
		". Add GW_METRICS_REMOTE_WRITE_TOKEN to overrides.env."
}
~~~

Keep `ready()` as the gate used by the writer. The warning contains a variable name only, never a value.

- [ ] **Step 3: Notify only from the enable action**

After persisting the newly enabled boolean in `toggleMetricsRemoteWrite`:

~~~go
if newVal {
	if warning := remoteWriteEnableWarning(loadRemoteWriteConfig(s.gwHome)); warning != "" {
		notify("Gateway", warning)
	}
}
~~~

Do not notify from `tickOnce` or the polling loop; this prevents recurring spam. Disabling remains silent and immediately stops writes through the existing atomic flag.

- [ ] **Step 4: Verify and commit**

~~~bash
gofmt -w cmd/otto-tray/remotewrite_config.go cmd/otto-tray/remotewrite_test.go cmd/otto-tray/tray.go
go test ./cmd/otto-tray -run 'RemoteWrite|MetricsRW' -count=1
go test -race ./cmd/otto-tray -run 'RemoteWrite|MetricsRW' -count=1
git add cmd/otto-tray/remotewrite_config.go cmd/otto-tray/remotewrite_test.go cmd/otto-tray/tray.go
git commit -m "feat: explain missing metrics API key"
~~~

### Task 3: Document and verify the simplified setup

**Files:**
- Modify: `docs/operator-quickstart.md`
- Modify: `docs/operating.md`

**Interfaces:**
- Produces: one-step secret setup followed by tray opt-in instructions.

- [ ] **Step 1: Add exact user instructions**

Add this snippet to both documents:

~~~dotenv
# ~/.gw/overrides.env
GW_METRICS_REMOTE_WRITE_TOKEN=<Grafana API key>
~~~

Explain that URL `https://prometheus-prod-66-prod-us-east-3.grafana.net/api/prom/push`, user `3370048`, and interval `30` come from generated `.env`; enable “Send metrics to Grafana Cloud” in the tray. State that `gw upgrade-env` refreshes shared defaults without touching `overrides.env`, and the tray stores only the boolean preference.

- [ ] **Step 2: Verify all onboarding contracts**

~~~bash
bash tests/scripts/test-metrics-remote-write-defaults.sh
go test -race ./cmd/otto-tray -count=1
go vet ./cmd/otto-tray
rg -n '^GW_METRICS_REMOTE_WRITE_(URL|USER|INTERVAL_SEC)=' scripts/.env.example
rg -n '^GW_METRICS_REMOTE_WRITE_TOKEN=' scripts/.env.example
git diff --check
~~~

Expected: first `rg` prints exactly three active defaults; second prints nothing and therefore exits 1. Treat that second exit as the expected secret-absence result.

- [ ] **Step 3: Commit documentation**

~~~bash
git add docs/operator-quickstart.md docs/operating.md
git commit -m "docs: simplify Grafana metrics setup"
~~~

### Task 4: Cross-plan release verification

**Files:**
- No production edits expected.

**Interfaces:**
- Verifies the Kiro log, support diagnostics, and metrics onboarding plans together.

- [ ] **Step 1: Run repository and script gates**

~~~bash
go test ./... -count=1
go test -race ./internal/config ./internal/pool ./internal/session ./internal/admin ./cmd/otto-gateway ./cmd/otto-tray -count=1
go vet ./...
bash tests/scripts/test-support-redact.sh
bash tests/scripts/test-support-bundle.sh
bash tests/scripts/test-metrics-remote-write-defaults.sh
pwsh -NoProfile -File tests/scripts/test-support-redact.ps1
pwsh -NoProfile -File tests/scripts/test-support-bundle.ps1
git diff --check
git status --short
~~~

Expected: all available gates PASS and the worktree is clean. If PowerShell is unavailable, report only those unrun commands.

- [ ] **Step 2: Manual smoke checklist**

Start Gateway with normal defaults and confirm `GW_HOME/logs/kiro-chat.log` appears. Select Kiro in the dashboard and observe live tailing. Add `KIRO_LOG_LEVEL=debug`, restart, confirm debug detail, then remove/restart. Enable capture, create traffic, create a support bundle, and inspect all three log trees plus timestamped metrics/capture files. Add only the remote-write token to overrides, enable tray sending, and confirm a successful push; disable and confirm pushes stop.

