# Kiro Log Runtime and Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route every Gateway-managed Kiro ACP process to a native log under `GW_HOME/logs` and let users select and live-tail it from the Gateway dashboard.

**Architecture:** Resolve `Config.KiroChatLogFile` once, prepare its directory before warmup, and pass `KIRO_CHAT_LOG_FILE` through pool and stateful-session configs into `acp.Config.Env`. Extend the allowlisted log registry with a Kiro source and a separate ID-to-label map.

**Tech Stack:** Go 1.24, Chi admin handlers, embedded vanilla JavaScript, Go tests.

## Global Constraints

- Default `<GW_HOME>/logs/kiro-chat.log`; explicit `KIRO_CHAT_LOG_FILE` wins.
- Do not set `KIRO_LOG_LEVEL` by default. Debug uses `KIRO_LOG_LEVEL=debug` in `overrides.env` plus restart.
- Do not use process-global `os.Setenv`.
- Dashboard exposes Gateway and Kiro, never Co-worker.
- Preserve source IDs and their allowlist.
- Follow red-green-refactor and commit after each task.

---

## File Structure

- `internal/config/config.go` and tests resolve the native path.
- `internal/pool` and `internal/session` configs forward child env.
- `cmd/otto-gateway/main.go` prepares the directory and wires sources.
- `internal/admin` publishes source labels.
- `internal/admin/static/js/admin.js` renders labels while submitting IDs.
- `docs/operating.md` documents normal/debug operation.

### Task 1: Resolve and prepare the Kiro destination

**Files:**
- Modify: `internal/config/config.go:90-125,390-430,930-975`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/otto-gateway/main.go:72-96`
- Modify: `cmd/otto-gateway/main_test.go:23-118`

**Interfaces:**
- Produces: `config.Config.KiroChatLogFile string`.
- Produces: `prepareKiroLaunch(config.Config, *slog.Logger) error` guarantees the parent exists.

- [ ] **Step 1: Write failing config tests**

~~~go
func TestLoad_KiroChatLogFile(t *testing.T) {
	gwHome := t.TempDir()
	t.Setenv("GW_HOME", gwHome)
	t.Setenv("KIRO_CHAT_LOG_FILE", "")
	cfg, err := config.Load()
	if err != nil { t.Fatal(err) }
	want := filepath.Join(gwHome, "logs", "kiro-chat.log")
	if cfg.KiroChatLogFile != want { t.Fatalf("got %q want %q", cfg.KiroChatLogFile, want) }

	explicit := filepath.Join(t.TempDir(), "native", "kiro.log")
	t.Setenv("KIRO_CHAT_LOG_FILE", explicit)
	cfg, err = config.Load()
	if err != nil { t.Fatal(err) }
	if cfg.KiroChatLogFile != explicit { t.Fatalf("got %q want %q", cfg.KiroChatLogFile, explicit) }
}
~~~

Run: `go test ./internal/config -run TestLoad_KiroChatLogFile -count=1`  
Expected: compile failure because the field is absent.

- [ ] **Step 2: Implement config resolution**

Add `KiroChatLogFile string` to `Config`. Resolve and assign:

~~~go
kiroChatLogFile := strings.TrimSpace(os.Getenv("KIRO_CHAT_LOG_FILE"))
if kiroChatLogFile == "" {
	gatewayHome, homeErr := gatewayembed.GatewayDir()
	if homeErr != nil {
		errs = append(errs, fmt.Errorf("config: KIRO_CHAT_LOG_FILE default: %w", homeErr))
	} else {
		kiroChatLogFile = filepath.Join(gatewayHome, "logs", "kiro-chat.log")
	}
}
~~~

Run: `go test ./internal/config -run 'TestLoad_KiroChatLogFile|TestLoad_Defaults' -count=1`  
Expected: PASS.

- [ ] **Step 3: Write failing launch tests**

~~~go
func TestPrepareKiroLaunchPreparesNativeLogDir(t *testing.T) {
	root := t.TempDir()
	logFile := filepath.Join(root, "logs", "kiro-chat.log")
	cfg := config.Config{KiroCmd: "kiro-cli", KiroCWD: root, KiroChatLogFile: logFile}
	if err := prepareKiroLaunch(cfg, testutil.Logger(t)); err != nil { t.Fatal(err) }
	if info, err := os.Stat(filepath.Dir(logFile)); err != nil || !info.IsDir() {
		t.Fatalf("info=%v err=%v", info, err)
	}
}
~~~

Add the explicit failure case:

~~~go
func TestPrepareKiroLaunchReportsNativeLogDirFailure(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil { t.Fatal(err) }
	cfg := config.Config{KiroCmd: "kiro-cli", KiroCWD: root, KiroChatLogFile: filepath.Join(blocker, "kiro.log")}
	err := prepareKiroLaunch(cfg, testutil.Logger(t))
	if err == nil || !strings.Contains(err.Error(), "Kiro log directory") {
		t.Fatalf("error=%v", err)
	}
}
~~~

Run: `go test ./cmd/otto-gateway -run TestPrepareKiroLaunchPreparesNativeLogDir -count=1`  
Expected: FAIL.

- [ ] **Step 4: Prepare and log the path**

At the beginning of `prepareKiroLaunch`:

~~~go
if cfg.KiroChatLogFile == "" {
	return errors.New("prepare Kiro launch: Kiro log path is empty")
}
logDir := filepath.Dir(cfg.KiroChatLogFile)
if err := os.MkdirAll(logDir, 0o750); err != nil {
	return fmt.Errorf("prepare Kiro log directory %q: %w", logDir, err)
}
~~~

Add `chat_log_file` and the safe path value to the existing startup record. Kiro creates the file.

Update every existing `cmd/otto-gateway` test config literal with a non-empty
`KiroCmd` to provide a test-owned `KiroChatLogFile`, for example
`filepath.Join(t.TempDir(), "logs", "kiro-chat.log")`. Config literals in the
intentional degraded `KiroCmd: ""` posture do not need the field because they
never call launch preparation. This keeps the new fail-fast empty-path contract
from obscuring the behavior each existing test is meant to exercise.

- [ ] **Step 5: Verify and commit**

~~~bash
gofmt -w internal/config/config.go internal/config/config_test.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
go test ./internal/config ./cmd/otto-gateway -count=1
git add internal/config/config.go internal/config/config_test.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
git commit -m "feat: resolve Kiro native log destination"
~~~

### Task 2: Propagate child-only environment

**Files:**
- Modify: `internal/pool/config.go`, `internal/pool/pool.go`, `internal/pool/capture_test.go`
- Modify: `internal/session/config.go`, `internal/session/registry.go`, `internal/session/capture_test.go`
- Modify: `cmd/otto-gateway/main.go:526-590`

**Interfaces:**
- Produces: `pool.Config.KiroEnv []string` and `session.Config.KiroEnv []string`.
- Consumes: `acp.Config.Env []string`, already appended after `os.Environ()`.

- [ ] **Step 1: Write failing propagation tests**

Add this white-box case to `internal/pool/capture_test.go`:

~~~go
func TestAcpSlotConfig_ForwardsKiroEnv(t *testing.T) {
	want := []string{"KIRO_CHAT_LOG_FILE=/tmp/kiro-chat.log"}
	cfg := New(Config{KiroEnv: want}).acpSlotConfig()
	if diff := cmp.Diff(want, cfg.Env); diff != "" {
		t.Fatalf("acp env mismatch (-want +got):\n%s", diff)
	}
}
~~~

Add this case to `internal/session/capture_test.go`, reusing its existing
`capturingFactory` and `newFake` helpers:

~~~go
func TestCreateEntry_ForwardsKiroEnv(t *testing.T) {
	var captured acp.Config
	want := []string{"KIRO_CHAT_LOG_FILE=/tmp/kiro-chat.log"}
	r := session.New(session.Config{
		Factory: &capturingFactory{cfgSink: &captured, client: newFake("kiro-env")},
		KiroCWD: t.TempDir(), KiroEnv: want,
	})
	t.Cleanup(func() { _ = r.Close() })
	if _, err := r.Get(context.Background(), "session-env", ""); err != nil { t.Fatal(err) }
	if diff := cmp.Diff(want, captured.Env); diff != "" {
		t.Fatalf("acp env mismatch (-want +got):\n%s", diff)
	}
}
~~~

Run: `go test ./internal/pool ./internal/session -run ForwardsKiroEnv -count=1`  
Expected: compile failure because `KiroEnv` is absent.

- [ ] **Step 2: Implement propagation**

Add to both lifecycle configs:

~~~go
// KiroEnv is appended to the parent environment for each Kiro subprocess.
KiroEnv []string
~~~

Add to `Pool.acpSlotConfig` and `Registry.createEntry` respectively:

~~~go
Env: append([]string(nil), p.cfg.KiroEnv...),
Env: append([]string(nil), r.cfg.KiroEnv...),
~~~

After `prepareKiroLaunch` succeeds, build:

~~~go
kiroEnv := []string{"KIRO_CHAT_LOG_FILE=" + cfg.KiroChatLogFile}
~~~

Pass `KiroEnv: kiroEnv` to both lifecycle configs. Do not add `KIRO_LOG_LEVEL`.

- [ ] **Step 3: Verify and commit**

~~~bash
gofmt -w internal/pool/config.go internal/pool/pool.go internal/pool/capture_test.go internal/session/config.go internal/session/registry.go internal/session/capture_test.go cmd/otto-gateway/main.go
go test ./internal/pool ./internal/session ./cmd/otto-gateway -count=1
git add internal/pool internal/session cmd/otto-gateway/main.go
git commit -m "feat: propagate Kiro log environment"
~~~

### Task 3: Register Kiro and render friendly labels

**Files:**
- Modify: `cmd/otto-gateway/main.go:850-907` and `main_test.go`
- Modify: `internal/admin/admin.go`, `snapshot.go`, `snapshot_test.go`
- Modify: `internal/admin/static/js/admin.js:614-670,1119-1152`

**Interfaces:**
- Produces: `admin.Deps.LogPathLabels map[string]string`.
- Produces JSON field `log_source_labels` while retaining `log_sources` as string IDs.

- [ ] **Step 1: Write failing tests**

Test a helper named `buildAdminLogSources`:

~~~go
paths, order, labels := buildAdminLogSources("gateway.log", "boot.log", "kiro.log", "trace.log", true)
if diff := cmp.Diff([]string{"main", "boot-err", "kiro", "chat-trace"}, order); diff != "" { t.Fatal(diff) }
if paths["kiro"] != "kiro.log" || labels["kiro"] != "Kiro" { t.Fatalf("%v %v", paths, labels) }
if _, ok := paths["co-worker"]; ok { t.Fatal("Co-worker must not be exposed") }
~~~

In `snapshot_test.go`, supply labels, decode `log_source_labels`, assert values, mutate the decoded map, and assert the dependency map is unchanged.

Run: `go test ./internal/admin ./cmd/otto-gateway -run 'LogSourceLabels|BuildAdminLogSources' -count=1`  
Expected: compile failures.

- [ ] **Step 2: Publish labels defensively**

Add `LogPathLabels map[string]string` to `admin.Deps` and a `LogSourceLabels map[string]string` snapshot field named `log_source_labels`. Initialize it non-nil and copy only ordered IDs:

~~~go
snap.LogSourceLabels = make(map[string]string, len(h.deps.LogPathOrder))
for _, id := range h.deps.LogPathOrder {
	if label := h.deps.LogPathLabels[id]; label != "" {
		snap.LogSourceLabels[id] = label
	}
}
~~~

- [ ] **Step 3: Build and wire sources**

~~~go
func buildAdminLogSources(mainPath, bootPath, kiroPath, chatTracePath string, chatTrace bool) (map[string]string, []string, map[string]string) {
	paths := map[string]string{"main": mainPath, "boot-err": bootPath, "kiro": kiroPath}
	order := []string{"main", "boot-err", "kiro"}
	labels := map[string]string{"main": "Gateway", "boot-err": "Gateway boot/errors", "kiro": "Kiro"}
	if chatTrace {
		paths["chat-trace"] = chatTracePath
		order = append(order, "chat-trace")
		labels["chat-trace"] = "Gateway chat trace"
	}
	return paths, order, labels
}
~~~

Use `cfg.KiroChatLogFile` and pass all outputs into admin deps. Keep SSE IDs unchanged.

- [ ] **Step 4: Render labels**

Pass both snapshot fields to `populateLogSources` and render:

~~~javascript
opt.value = sources[i];
opt.textContent = labels[sources[i]] || sources[i];
~~~

Include both sources and labels in the function's JSON cache. Retain `main` as default and the current reconnect flow. Existing `TestAdmin_TailerMissingFileGracefulRetry` covers a missing file appearing later.

- [ ] **Step 5: Verify and commit**

~~~bash
gofmt -w cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go internal/admin/admin.go internal/admin/snapshot.go internal/admin/snapshot_test.go
go test ./internal/admin ./cmd/otto-gateway -count=1
git diff --check
git add cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go internal/admin/admin.go internal/admin/snapshot.go internal/admin/snapshot_test.go internal/admin/static/js/admin.js
git commit -m "feat: tail Kiro logs in Gateway dashboard"
~~~

### Task 4: Document and verify

**Files:**
- Modify: `docs/operating.md:206-245`

**Interfaces:**
- Documents: `KIRO_CHAT_LOG_FILE`, `KIRO_LOG_LEVEL`, and the dashboard boundary.

- [ ] **Step 1: Add operator guidance**

Add rows documenting the default native path and the exact debug override/restart flow. State that Gateway shows Gateway and Kiro logs only; Co-worker keeps its own viewer.

- [ ] **Step 2: Run final verification**

~~~bash
go test ./internal/config ./internal/pool ./internal/session ./internal/admin ./cmd/otto-gateway -count=1
go test -race ./internal/pool ./internal/session ./internal/admin -count=1
go vet ./internal/config ./internal/pool ./internal/session ./internal/admin ./cmd/otto-gateway
git diff --check
~~~

Expected: PASS.

- [ ] **Step 3: Commit**

~~~bash
git add docs/operating.md
git commit -m "docs: explain Kiro native logging"
~~~
