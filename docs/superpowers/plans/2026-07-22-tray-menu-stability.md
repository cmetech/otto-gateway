# Tray Menu Stability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent unchanged three-second tray poll results from mutating and dismissing the active Windows tray menu.

**Architecture:** Project gateway and Co-Worker outputs into comparable menu models and pass them through a small generic render cache. The cache invokes native systray operations only for the first model or a changed model; state snapshots and transition notifications continue updating independently.

**Tech Stack:** Go 1.25, `github.com/energye/systray`, Go generics, standard `testing`, Windows cross-compilation.

## Global Constraints

- Keep both polling cadences at exactly three seconds.
- Keep manual Co-Worker Refresh emitting Detecting followed by the fresh result.
- Preserve all existing labels, visibility rules, enabled states, icons, tooltips, notifications, click handlers, discovery, paths, and process control.
- The first gateway and Co-Worker models must always render.
- An identical projected model must execute zero native tray operations.
- A genuine model change must render exactly once.
- `desktopCurrent` must receive every fresh immutable output snapshot even when its projected menu model is unchanged.
- Do not fork or replace `github.com/energye/systray` and do not intercept native menu-open lifecycle.

---

### Task 1: Deduplicate Gateway and Co-Worker Menu Rendering

**Files:**
- Create: `cmd/otto-tray/menurender.go`
- Create: `cmd/otto-tray/menurender_test.go`
- Modify: `cmd/otto-tray/tray.go`
- Modify: `cmd/otto-tray/desktoptray.go`
- Modify: `cmd/otto-tray/desktoptray_test.go`

**Interfaces:**
- Produces: `menuRenderCache[T comparable]` with `Apply(next T, render func(T)) bool`.
- Produces: `gatewayMenuModel` and `gatewayMenuForOutput(stateOutput, string) gatewayMenuModel`.
- Consumes: existing comparable `desktopMenuModel` and `desktopMenuForOutput`.

- [ ] **Step 1: Write the failing generic render-cache test**

Create `cmd/otto-tray/menurender_test.go` with a test that applies model `A`, applies identical model `A`, then applies model `B`. Capture callback values and require exactly `[]string{"A", "B"}`. Also require `Apply` to return `true`, `false`, `true` respectively.

```go
func TestMenuRenderCacheSkipsIdenticalModels(t *testing.T) {
	var cache menuRenderCache[string]
	var rendered []string
	render := func(model string) { rendered = append(rendered, model) }

	if !cache.Apply("A", render) {
		t.Fatal("first model was not rendered")
	}
	if cache.Apply("A", render) {
		t.Fatal("identical model rendered again")
	}
	if !cache.Apply("B", render) {
		t.Fatal("changed model was not rendered")
	}
	if !reflect.DeepEqual(rendered, []string{"A", "B"}) {
		t.Fatalf("rendered = %v", rendered)
	}
}
```

- [ ] **Step 2: Run the cache test and verify RED**

Run:

```bash
go test ./cmd/otto-tray -run TestMenuRenderCacheSkipsIdenticalModels -count=1
```

Expected: compile failure because `menuRenderCache` does not exist.

- [ ] **Step 3: Implement the minimal generic render cache**

Create `cmd/otto-tray/menurender.go`:

```go
//go:build darwin || windows

package main

type menuRenderCache[T comparable] struct {
	last T
	set  bool
}

func (c *menuRenderCache[T]) Apply(next T, render func(T)) bool {
	if c.set && c.last == next {
		return false
	}
	render(next)
	c.last = next
	c.set = true
	return true
}
```

The cache records a model only after the render callback returns.

- [ ] **Step 4: Run the cache test and verify GREEN**

Run:

```bash
go test ./cmd/otto-tray -run TestMenuRenderCacheSkipsIdenticalModels -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing gateway-model and desktop-snapshot tests**

In `menurender_test.go`, add a table test for `gatewayMenuForOutput` covering Running and Stopped/Error. Assert the exact tooltip/header/subheader and Start/Stop/Restart enabled booleans.

Use this production shape:

```go
type gatewayMenuModel struct {
	State          State
	Tooltip        string
	Header         string
	Subheader      string
	StartEnabled   bool
	StopEnabled    bool
	RestartEnabled bool
}
```

In `desktoptray_test.go`, add a cache-level integration test that projects two `DesktopRunning` outputs with the same LOOP24 display name but different `ExecutablePath` values. Require only one menu-render callback while separately storing and verifying the second immutable output snapshot. This proves menu equality does not make action state stale.

- [ ] **Step 6: Run the new focused tests and verify RED**

Run:

```bash
go test ./cmd/otto-tray -run 'TestGatewayMenuForOutput|TestDesktopUnchangedMenuStillStoresLatestSnapshot' -count=1
```

Expected: compile failures because the gateway projection and cache-integrated snapshot helper do not exist.

- [ ] **Step 7: Add the gateway model and cache fields**

In `tray.go`, add `gatewayMenuModel`, the pure `gatewayMenuForOutput` projection, and these fields to `trayState`:

```go
gatewayMenuCache menuRenderCache[gatewayMenuModel]
desktopMenuCache menuRenderCache[desktopMenuModel]
```

`gatewayMenuForOutput` must preserve the current formatting:

```go
func gatewayMenuForOutput(out stateOutput, dashboardURL string) gatewayMenuModel {
	header := fmt.Sprintf("Gateway · %s", out.State)
	if out.Detail != "" {
		header += " (" + out.Detail + ")"
	}
	canStart := out.State == StateStopped || out.State == StateError
	return gatewayMenuModel{
		State:          out.State,
		Tooltip:        tooltipForState(out.State, out.Detail),
		Header:         header,
		Subheader:      dashboardURL,
		StartEnabled:   canStart,
		StopEnabled:    !canStart,
		RestartEnabled: !canStart,
	}
}
```

- [ ] **Step 8: Route gateway rendering through the cache**

Keep the existing `prev/current` update at the start of `applyState`. Project the model, then call `gatewayMenuCache.Apply(model, s.renderGatewayMenu)`. Move only the icon, tooltip, SetTitle, and Enable/Disable calls into:

```go
func (s *trayState) renderGatewayMenu(model gatewayMenuModel) {
	setIconForState(model.State)
	systray.SetTooltip(model.Tooltip)
	s.miHeader.SetTitle(model.Header)
	s.miSubheader.SetTitle(model.Subheader)
	setMenuItemEnabled(s.miStart, model.StartEnabled)
	setMenuItemEnabled(s.miStop, model.StopEnabled)
	setMenuItemEnabled(s.miRestart, model.RestartEnabled)
}
```

Add a local helper that calls `Enable` only for `true` and `Disable` only for `false`. Call `notifyTransition(prev, out.State)` after the cache call on every output, preserving notification behavior independently of rendering.

- [ ] **Step 9: Route Co-Worker rendering through the cache without stale snapshots**

In `applyDesktopOutput`, continue cloning the candidate and storing
`desktopCurrent` before projecting the menu. Then call:

```go
model := desktopMenuForOutput(snapshot)
s.desktopMenuCache.Apply(model, s.renderDesktopMenu)
```

Move all existing Co-Worker SetTitle, Enable/Disable, and Show/Hide calls verbatim into `renderDesktopMenu(model desktopMenuModel)`. Do not change their labels or state rules.

For the cache-level snapshot test, extract only this pure helper if needed:

```go
func immutableDesktopOutput(out desktopOutput) desktopOutput
```

It must copy the pointed-to candidate exactly as the current implementation does. `applyDesktopOutput` stores that snapshot before render deduplication.

- [ ] **Step 10: Run focused tests and verify GREEN**

Run:

```bash
go test ./cmd/otto-tray -run 'TestMenuRenderCache|TestGatewayMenuForOutput|TestDesktopMenuForOutput|TestDesktopUnchangedMenuStillStoresLatestSnapshot' -count=1
```

Expected: PASS.

- [ ] **Step 11: Verify poller/manual-refresh contracts remain unchanged**

Run:

```bash
go test ./cmd/otto-tray -run 'TestRunPoller|TestRunDesktopPoller|TestRequestDesktopRefresh' -count=1
```

Expected: PASS without modifying either three-second ticker construction or poller behavior.

- [ ] **Step 12: Run full verification**

Run:

```bash
git diff --check
go run mvdan.cc/gofumpt@latest -d .
go test ./... -count=1
go test -race ./cmd/otto-tray -count=1
go vet ./...
go build -o /tmp/otto-gateway-menu-stability ./cmd/otto-gateway
go build -o /tmp/gateway-tray-menu-stability ./cmd/otto-tray
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c -o /tmp/gateway-tray-menu-stability-tests.exe ./cmd/otto-tray
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags='-H windowsgui' -o /tmp/gateway-tray-menu-stability.exe ./cmd/otto-tray
```

Expected: every command exits 0 and `gofumpt -d` prints nothing.

- [ ] **Step 13: Record Windows interactive acceptance honestly**

Verify on Windows when available:

```text
[ ] Open the tray menu and navigate top-level items for more than 6 seconds.
[ ] Open Advanced and move between its items for more than 6 seconds.
[ ] Confirm neither popup disappears while Gateway and Co-Worker states remain stable.
[ ] Trigger Refresh Co-Worker Detection and confirm Detecting/resolved labels update.
[ ] Start or stop the Gateway and confirm the real transition updates the menu once.
```

If Windows is unavailable, record these as unverified rather than passed.

- [ ] **Step 14: Commit Task 1**

```bash
git add cmd/otto-tray/menurender.go cmd/otto-tray/menurender_test.go \
  cmd/otto-tray/tray.go cmd/otto-tray/desktoptray.go cmd/otto-tray/desktoptray_test.go
git commit -m "fix(tray): skip unchanged menu renders"
```
