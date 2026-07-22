# Tool-less Kiro ACP Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the gateway launch Kiro ACP under an embedded, workspace-scoped, tool-less `acp_proxy` agent by default while preserving every existing environment and flag override.

**Architecture:** A focused `internal/embed` package owns the compiled agent JSON, derives the existing gateway-home root, and materializes the workspace agent without overwriting existing files. `internal/config` selects that root and `acp --agent acp_proxy` only as defaults, while `cmd/otto-gateway` materializes and logs the effective launch immediately before pool warmup.

**Tech Stack:** Go 1.23+, standard library `embed`, `os`, `path/filepath`, `log/slog`, existing Go tests and gateway ACP integration harness.

## Global Constraints

- Keep `identityGuardClause`, ACP deny-permission handling, and `execute→run_shell` aliasing unchanged.
- Preserve `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `--kiro-args`, and `--kiro-cwd` precedence.
- Never overwrite an existing `.kiro/agents/acp_proxy.json`.
- Do not seed the global `~/.kiro/agents` directory.
- Do not gate behavior on brand.
- Keep the gateway statically distributable and cross-compilable without cgo.
- Do not claim the live behavior is verified unless the installed Kiro and three-surface checks have actually run.

## File structure

| File | Responsibility |
|---|---|
| `internal/embed/acp_proxy.json` | Canonical tool-less Kiro custom-agent configuration embedded into the binary. |
| `internal/embed/assets.go` | Gateway-home derivation, asset path construction, and exclusive write-if-absent materialization. |
| `internal/embed/assets_test.go` | JSON contract, path precedence, creation, preservation, and error tests. |
| `internal/config/config.go` | New Kiro defaults, default-vs-override tracking, and missing-default-directory validation exception. |
| `internal/config/config_test.go` | Default and environment-override regression coverage. |
| `internal/config/loadargs_test.go` | CLI override regression coverage, including default ownership tracking. |
| `cmd/otto-gateway/main.go` | Pre-warmup materialization and structured launch diagnostic. |
| `cmd/otto-gateway/main_test.go` | Materialization, preservation, custom-workspace, error, and logging coverage. |
| `internal/admin/admin.go` | Accurate operator-facing Kiro defaults. |
| `internal/admin/handlers_test.go` | Rendered documentation regression test. |

---

### Task 1: Embed and safely materialize `acp_proxy`

**Files:**
- Create: `internal/embed/acp_proxy.json`
- Create: `internal/embed/assets.go`
- Create: `internal/embed/assets_test.go`
- Delete: `internal/embed/.gitkeep`

**Interfaces:**
- Produces: `func GatewayDir() (string, error)`
- Produces: `func ACPProxyPath(root string) string`
- Produces: `func EnsureACPProxy(root string) (path string, created bool, err error)`
- Depends on: `$GW_HOME` and `os.UserConfigDir()` only.

- [ ] **Step 1: Write failing asset tests**

Create `internal/embed/assets_test.go` with tests that specify the public API and preservation contract:

```go
package gatewayembed_test

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"

    gatewayembed "otto-gateway/internal/embed"
)

func TestGatewayDirPrefersGWHome(t *testing.T) {
    root := filepath.Join(t.TempDir(), "gateway-home")
    t.Setenv("GW_HOME", "  "+root+"  ")
    got, err := gatewayembed.GatewayDir()
    if err != nil { t.Fatalf("GatewayDir: %v", err) }
    if got != root { t.Fatalf("GatewayDir = %q, want %q", got, root) }
}

func TestEnsureACPProxyCreatesToolLessAgent(t *testing.T) {
    root := filepath.Join(t.TempDir(), "gateway")
    path, created, err := gatewayembed.EnsureACPProxy(root)
    if err != nil { t.Fatalf("EnsureACPProxy: %v", err) }
    if !created { t.Fatal("created = false, want true") }
    if path != filepath.Join(root, ".kiro", "agents", "acp_proxy.json") {
        t.Fatalf("path = %q", path)
    }
    body, err := os.ReadFile(path)
    if err != nil { t.Fatalf("ReadFile: %v", err) }
    var cfg struct {
        Name           string         `json:"name"`
        Prompt         any            `json:"prompt"`
        MCPServers     map[string]any `json:"mcpServers"`
        Tools          []string       `json:"tools"`
        AllowedTools   []string       `json:"allowedTools"`
        IncludeMCPJSON bool           `json:"includeMcpJson"`
    }
    if err := json.Unmarshal(body, &cfg); err != nil { t.Fatalf("Unmarshal: %v", err) }
    if cfg.Name != "acp_proxy" || cfg.Prompt != nil || len(cfg.MCPServers) != 0 ||
        cfg.Tools == nil || len(cfg.Tools) != 0 || cfg.AllowedTools == nil ||
        len(cfg.AllowedTools) != 0 || cfg.IncludeMCPJSON {
        t.Fatalf("agent is not explicitly tool-less: %+v", cfg)
    }
}

func TestEnsureACPProxyPreservesExistingFile(t *testing.T) {
    root := t.TempDir()
    path := gatewayembed.ACPProxyPath(root)
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { t.Fatal(err) }
    const custom = `{"name":"user-customized"}`
    if err := os.WriteFile(path, []byte(custom), 0o600); err != nil { t.Fatal(err) }
    gotPath, created, err := gatewayembed.EnsureACPProxy(root)
    if err != nil { t.Fatalf("EnsureACPProxy: %v", err) }
    if created { t.Fatal("created = true, want false") }
    body, _ := os.ReadFile(gotPath)
    if string(body) != custom { t.Fatalf("existing file was changed: %q", body) }
}

func TestEnsureACPProxyRejectsNonRegularTarget(t *testing.T) {
    root := t.TempDir()
    path := gatewayembed.ACPProxyPath(root)
    if err := os.MkdirAll(path, 0o755); err != nil { t.Fatal(err) }
    if _, _, err := gatewayembed.EnsureACPProxy(root); err == nil {
        t.Fatal("EnsureACPProxy returned nil error for directory target")
    }
}
```

- [ ] **Step 2: Run the asset tests and verify RED**

Run: `go test ./internal/embed -run 'Test(GatewayDir|EnsureACPProxy)' -count=1`

Expected: FAIL because `internal/embed` has no Go package and the three functions do not exist.

- [ ] **Step 3: Add the exact embedded agent asset**

Create `internal/embed/acp_proxy.json` with the exact approved payload from the design, including explicit empty `tools`, `allowedTools`, `mcpServers`, resources, hooks, aliases, and settings.

- [ ] **Step 4: Implement the minimal materializer**

Create `internal/embed/assets.go`:

```go
// Package gatewayembed owns binary-embedded runtime assets materialized into
// the gateway-controlled workspace.
package gatewayembed

import (
    _ "embed"
    "errors"
    "fmt"
    "io/fs"
    "os"
    "path/filepath"
    "strings"
)

//go:embed acp_proxy.json
var acpProxyJSON []byte

func GatewayDir() (string, error) {
    if root := strings.TrimSpace(os.Getenv("GW_HOME")); root != "" {
        return filepath.Clean(root), nil
    }
    configDir, err := os.UserConfigDir()
    if err != nil {
        return "", fmt.Errorf("derive gateway home: %w (set GW_HOME or KIRO_CWD)", err)
    }
    return filepath.Join(configDir, "gateway"), nil
}

func ACPProxyPath(root string) string {
    return filepath.Join(root, ".kiro", "agents", "acp_proxy.json")
}

func EnsureACPProxy(root string) (string, bool, error) {
    path := ACPProxyPath(root)
    if exists, err := regularFileExists(path); err != nil {
        return path, false, err
    } else if exists {
        return path, false, nil
    }
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return path, false, fmt.Errorf("create acp_proxy agent directory: %w", err)
    }
    file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
    if errors.Is(err, fs.ErrExist) {
        exists, statErr := regularFileExists(path)
        if statErr != nil { return path, false, statErr }
        if exists { return path, false, nil }
    }
    if err != nil { return path, false, fmt.Errorf("create acp_proxy agent: %w", err) }
    if _, err := file.Write(acpProxyJSON); err != nil {
        _ = file.Close()
        _ = os.Remove(path)
        return path, false, fmt.Errorf("write acp_proxy agent: %w", err)
    }
    if err := file.Close(); err != nil {
        _ = os.Remove(path)
        return path, false, fmt.Errorf("close acp_proxy agent: %w", err)
    }
    return path, true, nil
}

func regularFileExists(path string) (bool, error) {
    info, err := os.Lstat(path)
    if errors.Is(err, fs.ErrNotExist) { return false, nil }
    if err != nil { return false, fmt.Errorf("inspect acp_proxy agent: %w", err) }
    if !info.Mode().IsRegular() {
        return false, fmt.Errorf("acp_proxy agent path %q exists but is not a regular file", path)
    }
    return true, nil
}
```

Remove `internal/embed/.gitkeep` once real package files exist.

- [ ] **Step 5: Run the asset tests and verify GREEN**

Run: `go test ./internal/embed -count=1`

Expected: PASS.

- [ ] **Step 6: Commit Task 1**

```bash
git add internal/embed
git commit -m "feat: embed tool-less Kiro ACP agent"
```

---

### Task 2: Change defaults while preserving override ownership

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/loadargs_test.go`
- Modify: `internal/config/regression_rel_cfg_06_test.go`

**Interfaces:**
- Consumes: `gatewayembed.GatewayDir() (string, error)`
- Produces: `Config.KiroCWDIsDefault bool`, true only when neither environment nor CLI explicitly selected the Kiro working directory.
- Produces default: `KiroArgs == []string{"acp", "--agent", "acp_proxy"}`.

- [ ] **Step 1: Write failing configuration-default tests**

Update `TestLoadDefaults` to derive the expected gateway directory and assert:

```go
wantCWD, err := gatewayembed.GatewayDir()
if err != nil { t.Fatalf("GatewayDir: %v", err) }
if !reflect.DeepEqual(cfg.KiroArgs, []string{"acp", "--agent", "acp_proxy"}) {
    t.Errorf("KiroArgs: got %v", cfg.KiroArgs)
}
if cfg.KiroCWD != wantCWD || !cfg.KiroCWDIsDefault {
    t.Errorf("KiroCWD = %q default=%v, want %q true", cfg.KiroCWD, cfg.KiroCWDIsDefault, wantCWD)
}
```

Extend environment and flag tests so explicit `KIRO_CWD` and `--kiro-cwd`
set `KiroCWDIsDefault` to false, and explicit Kiro arguments remain unchanged.
Update REL-CFG-06 case F to expect the derived default rather than an empty cwd.

- [ ] **Step 2: Run focused config tests and verify RED**

Run: `go test ./internal/config -run 'TestLoad(Default|EnvOverrides)|TestLoadArgs_FlagWins_WhitespaceSlice_KiroArgs|TestRegression_REL_CFG_06' -count=1`

Expected: FAIL because defaults remain bare `acp`, cwd remains empty, and `KiroCWDIsDefault` is undefined.

- [ ] **Step 3: Implement default selection and ownership tracking**

Import `gatewayembed "otto-gateway/internal/embed"`, add the documented field:

```go
// KiroCWDIsDefault reports that KiroCWD came from the gateway-owned default,
// not an explicit KIRO_CWD or --kiro-cwd override.
KiroCWDIsDefault bool
```

Replace the current defaults in `Load` with:

```go
kiroArgs := getEnvStrSlice("KIRO_ARGS", []string{"acp", "--agent", "acp_proxy"})
rawKiroCWD := strings.TrimSpace(os.Getenv("KIRO_CWD"))
kiroCWDIsDefault := rawKiroCWD == ""
kiroCWD := rawKiroCWD
if kiroCWDIsDefault {
    var cwdErr error
    kiroCWD, cwdErr = gatewayembed.GatewayDir()
    if cwdErr != nil { errs = append(errs, fmt.Errorf("config: KIRO_CWD default: %w", cwdErr)) }
}
```

In cwd validation, allow only `fs.ErrNotExist` for `kiroCWDIsDefault`; retain
all explicit-path errors. Populate `KiroCWDIsDefault` in the returned Config.
When `--kiro-cwd` is visited, set `cfg.KiroCWDIsDefault = false`.

- [ ] **Step 4: Run config tests and verify GREEN**

Run: `go test ./internal/config -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

```bash
git add internal/config
git commit -m "feat: default Kiro ACP to acp_proxy"
```

---

### Task 3: Materialize and log the effective launch before warmup

**Files:**
- Modify: `cmd/otto-gateway/main.go`
- Modify: `cmd/otto-gateway/main_test.go`

**Interfaces:**
- Consumes: `Config.KiroCWDIsDefault` and `gatewayembed.EnsureACPProxy`.
- Produces: `prepareKiroLaunch(config.Config, *slog.Logger) error`.
- Produces log message: `kiro launch configured` with `command`, `args`, `cwd`, `agent_config`, and `agent_config_status`.

- [ ] **Step 1: Write failing launch-preparation tests**

Add focused tests using a JSON slog handler backed by a buffer:

```go
func TestPrepareKiroLaunchMaterializesAndLogsDefaultAgent(t *testing.T) {
    root := filepath.Join(t.TempDir(), "gateway")
    var logs bytes.Buffer
    logger := slog.New(slog.NewJSONHandler(&logs, nil))
    cfg := config.Config{
        KiroCmd: "kiro-cli", KiroArgs: []string{"acp", "--agent", "acp_proxy"},
        KiroCWD: root, KiroCWDIsDefault: true,
    }
    if err := prepareKiroLaunch(cfg, logger); err != nil { t.Fatalf("prepareKiroLaunch: %v", err) }
    if _, err := os.Stat(gatewayembed.ACPProxyPath(root)); err != nil { t.Fatalf("agent file: %v", err) }
    text := logs.String()
    for _, want := range []string{"kiro launch configured", "acp_proxy", root, "created"} {
        if !strings.Contains(text, want) { t.Errorf("log missing %q: %s", want, text) }
    }
}

func TestPrepareKiroLaunchPreservesDefaultAgent(t *testing.T) {
    root := t.TempDir()
    path := gatewayembed.ACPProxyPath(root)
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(path, []byte(`{"name":"custom"}`), 0o600); err != nil { t.Fatal(err) }
    cfg := config.Config{KiroCmd: "kiro-cli", KiroCWD: root, KiroCWDIsDefault: true}
    if err := prepareKiroLaunch(cfg, testutil.Logger(t)); err != nil { t.Fatal(err) }
    body, _ := os.ReadFile(path)
    if string(body) != `{"name":"custom"}` { t.Fatal("existing agent was overwritten") }
}

func TestPrepareKiroLaunchDoesNotModifyCustomCWD(t *testing.T) {
    root := t.TempDir()
    cfg := config.Config{KiroCmd: "kiro-cli", KiroCWD: root, KiroCWDIsDefault: false}
    if err := prepareKiroLaunch(cfg, testutil.Logger(t)); err != nil { t.Fatal(err) }
    if _, err := os.Stat(filepath.Join(root, ".kiro")); !errors.Is(err, fs.ErrNotExist) {
        t.Fatalf("custom cwd was modified: %v", err)
    }
}
```

Also add an error test with a regular file blocking the default root, expecting
`prepareKiroLaunch` to return a materialization error.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./cmd/otto-gateway -run 'TestPrepareKiroLaunch' -count=1`

Expected: FAIL because `prepareKiroLaunch` does not exist.

- [ ] **Step 3: Implement launch preparation**

Import `gatewayembed "otto-gateway/internal/embed"` and add:

```go
func prepareKiroLaunch(cfg config.Config, logger *slog.Logger) error {
    agentPath := "(custom workspace — not managed)"
    status := "not-managed"
    if cfg.KiroCWDIsDefault {
        path, created, err := gatewayembed.EnsureACPProxy(cfg.KiroCWD)
        if err != nil { return fmt.Errorf("prepare acp_proxy agent: %w", err) }
        agentPath = path
        status = "preserved"
        if created { status = "created" }
    }
    logger.Info("kiro launch configured",
        "command", cfg.KiroCmd,
        "args", cfg.KiroArgs,
        "cwd", cfg.KiroCWD,
        "agent_config", agentPath,
        "agent_config_status", status,
    )
    return nil
}
```

At the start of the existing `if cfg.KiroCmd != ""` pool-construction block,
call the helper. On error, invoke `cleanup()` and return the wrapped startup
error before `pool.New` or `Warmup`.

- [ ] **Step 4: Run launch tests and verify GREEN**

Run: `go test ./cmd/otto-gateway -run 'TestPrepareKiroLaunch|TestApp_WarmupBeforeListen' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

```bash
git add cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go
git commit -m "feat: prepare tool-less Kiro launch workspace"
```

---

### Task 4: Update operator documentation and run verification

**Files:**
- Modify: `internal/admin/admin.go`
- Modify: `internal/admin/handlers_test.go`
- Modify: `scripts/.env.example` if its Kiro examples still imply bare ACP defaults.

**Interfaces:**
- Produces rendered defaults `acp --agent acp_proxy` and gateway-managed cwd.

- [ ] **Step 1: Write the failing admin documentation test**

Add `TestAdmin_DocsEnvTable_KiroACPProxyDefaults` that GETs `/docs` and asserts
the rendered page contains `acp --agent acp_proxy`, `gateway-managed`, and
`.kiro/agents/acp_proxy.json`.

- [ ] **Step 2: Run the admin test and verify RED**

Run: `go test ./internal/admin -run TestAdmin_DocsEnvTable_KiroACPProxyDefaults -count=1`

Expected: FAIL because the page still says `KIRO_ARGS=acp` and empty cwd.

- [ ] **Step 3: Update operator-facing defaults**

Change only the Kiro rows:

```go
{Name: "KIRO_ARGS", Default: "acp --agent acp_proxy", Description: "Whitespace-split argv passed to KIRO_CMD. The default selects the embedded tool-less ACP proxy agent.", CurrentValue: kiroArgsCurrent},
{Name: "KIRO_CWD", Default: "<gateway-home>", Description: "Working directory for kiro-cli. The default is the gateway-managed workspace containing .kiro/agents/acp_proxy.json; explicit overrides are not modified.", CurrentValue: kiroCwdCurrent},
```

Update stale comments/examples without changing override semantics.

- [ ] **Step 4: Run focused and full automated verification**

Run:

```bash
gofmt -w internal/embed/assets.go internal/embed/assets_test.go internal/config/config.go internal/config/config_test.go internal/config/loadargs_test.go internal/config/regression_rel_cfg_06_test.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go internal/admin/admin.go internal/admin/handlers_test.go
go test ./internal/embed ./internal/config ./cmd/otto-gateway ./internal/admin -count=1
go test ./... -count=1
go vet ./...
go build ./cmd/otto-gateway
```

Expected: every command exits 0 with no test failures or vet diagnostics.

- [ ] **Step 5: Run live Kiro and gateway verification**

Record:

```bash
kiro-cli --version
kiro-cli acp --help
```

Launch a temporary gateway instance with an isolated `GW_HOME`, default
`KIRO_ARGS`/`KIRO_CWD`, `POOL_SIZE=1`, and temporary listen address. Confirm the
startup log contains `acp --agent acp_proxy`, the cwd contains the agent JSON,
and ACP capture contains no built-in tool permission request.

Run the repository's available identity/tool-call/tool-execution parity checks
and normal chat plus streaming checks for Ollama, OpenAI, and Anthropic. Record
exact passes, failures, skipped checks, authentication blockers, and any absent
`gateway-toolcall-parity` skill rather than inferring success.

- [ ] **Step 6: Commit Task 4**

```bash
git add internal/admin/admin.go internal/admin/handlers_test.go scripts/.env.example
git commit -m "docs: describe tool-less Kiro ACP defaults"
```

---

### Task 5: Final requirements audit

**Files:**
- Review: `docs/superpowers/specs/2026-07-21-tool-less-kiro-acp-agent-design.md`
- Review: all files changed by Tasks 1–4.

**Interfaces:** None; this is the completion gate.

- [ ] **Step 1: Confirm protected workarounds are unchanged**

Run:

```bash
git diff HEAD~4..HEAD -- internal/engine/build_acp.go internal/acp/context.go internal/acp/client.go internal/engine/toolcall_resolve.go
```

Expected: no diff.

- [ ] **Step 2: Confirm overrides and no-clobber behavior from tests**

Run:

```bash
go test ./internal/embed ./internal/config ./cmd/otto-gateway -run 'ACPProxy|Kiro|KIRO|PrepareKiroLaunch' -count=1
```

Expected: PASS.

- [ ] **Step 3: Inspect the final diff and repository state**

Run:

```bash
git diff --check
git status --short
git log -5 --oneline
```

Expected: no whitespace errors; only intentional planning/GSD artifacts may
remain for their final documentation commit.
