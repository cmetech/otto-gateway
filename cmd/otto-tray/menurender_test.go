//go:build darwin || windows

package main

import (
	"fmt"
	"reflect"
	"testing"
)

func recordingMenuItemRenderOps(name string, operations *[]string) menuItemRenderOps {
	return menuItemRenderOps{
		setTitle: func(title string) {
			*operations = append(*operations, fmt.Sprintf("%s.SetTitle(%s)", name, title))
		},
		setEnabled: func(enabled bool) {
			*operations = append(*operations, fmt.Sprintf("%s.SetEnabled(%t)", name, enabled))
		},
		setVisible: func(visible bool) {
			*operations = append(*operations, fmt.Sprintf("%s.SetVisible(%t)", name, visible))
		},
	}
}

func recordingGatewayMenuRenderOps(operations *[]string) gatewayMenuRenderOps {
	return gatewayMenuRenderOps{
		setIcon: func(state State) {
			*operations = append(*operations, fmt.Sprintf("icon(%s)", state))
		},
		setTooltip: func(tooltip string) {
			*operations = append(*operations, fmt.Sprintf("tooltip(%s)", tooltip))
		},
		header:    recordingMenuItemRenderOps("gateway.header", operations),
		subheader: recordingMenuItemRenderOps("gateway.subheader", operations),
		start:     recordingMenuItemRenderOps("gateway.start", operations),
		stop:      recordingMenuItemRenderOps("gateway.stop", operations),
		restart:   recordingMenuItemRenderOps("gateway.restart", operations),
	}
}

func recordingDesktopMenuRenderOps(operations *[]string) desktopMenuRenderOps {
	return desktopMenuRenderOps{
		header:     recordingMenuItemRenderOps("desktop.header", operations),
		appFolder:  recordingMenuItemRenderOps("desktop.app-folder", operations),
		dataFolder: recordingMenuItemRenderOps("desktop.data-folder", operations),
		install:    recordingMenuItemRenderOps("desktop.install", operations),
		start:      recordingMenuItemRenderOps("desktop.start", operations),
		stop:       recordingMenuItemRenderOps("desktop.stop", operations),
	}
}

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

func TestMenuRenderCacheDoesNotCachePanickedRender(t *testing.T) {
	var cache menuRenderCache[string]
	calls := 0
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		cache.Apply("A", func(string) {
			calls++
			panic("render failed")
		})
	}()

	if recovered == nil {
		t.Fatal("render panic was not observed")
	}
	if !cache.Apply("A", func(string) { calls++ }) {
		t.Fatal("model was cached despite render panic")
	}
	if calls != 2 {
		t.Fatalf("render callback invoked %d times, want 2", calls)
	}
}

func TestGatewayNativeRenderOperationsAreCached(t *testing.T) {
	var cache menuRenderCache[gatewayMenuModel]
	var operations []string
	ops := recordingGatewayMenuRenderOps(&operations)
	running := gatewayMenuForOutput(stateOutput{State: StateRunning}, "http://127.0.0.1:8080")
	errored := gatewayMenuForOutput(stateOutput{State: StateError, Detail: "/health unreachable"}, "http://127.0.0.1:8080")

	if !applyGatewayMenuModel(&cache, running, ops) {
		t.Fatal("first gateway model was not rendered")
	}
	firstOperationCount := len(operations)
	if applyGatewayMenuModel(&cache, running, ops) {
		t.Fatal("identical gateway model rendered again")
	}
	if len(operations) != firstOperationCount {
		t.Fatalf("identical gateway model executed operations: %v", operations[firstOperationCount:])
	}
	if !applyGatewayMenuModel(&cache, errored, ops) {
		t.Fatal("changed gateway model was not rendered")
	}

	want := []string{
		"icon(running)",
		"tooltip(Gateway · running)",
		"gateway.header.SetTitle(Gateway · running)",
		"gateway.subheader.SetTitle(http://127.0.0.1:8080)",
		"gateway.start.SetEnabled(false)",
		"gateway.stop.SetEnabled(true)",
		"gateway.restart.SetEnabled(true)",
		"icon(error)",
		"tooltip(Gateway · error (/health unreachable))",
		"gateway.header.SetTitle(Gateway · error (/health unreachable))",
		"gateway.subheader.SetTitle(http://127.0.0.1:8080)",
		"gateway.start.SetEnabled(true)",
		"gateway.stop.SetEnabled(false)",
		"gateway.restart.SetEnabled(false)",
	}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("gateway operations = %v, want %v", operations, want)
	}
}

func TestDesktopNativeRenderOperationsAreCached(t *testing.T) {
	var cache menuRenderCache[desktopMenuModel]
	var operations []string
	ops := recordingDesktopMenuRenderOps(&operations)
	detecting := desktopMenuForOutput(desktopOutput{State: DesktopDetecting})
	running := desktopMenuForOutput(desktopOutput{
		State: DesktopRunning,
		Candidate: &desktopCandidate{
			Identity: identityFromDisplayName("LOOP24"),
		},
	})

	if !applyDesktopMenuModel(&cache, detecting, ops) {
		t.Fatal("first desktop model was not rendered")
	}
	firstOperationCount := len(operations)
	if applyDesktopMenuModel(&cache, detecting, ops) {
		t.Fatal("identical desktop model rendered again")
	}
	if len(operations) != firstOperationCount {
		t.Fatalf("identical desktop model executed operations: %v", operations[firstOperationCount:])
	}
	if !applyDesktopMenuModel(&cache, running, ops) {
		t.Fatal("changed desktop model was not rendered")
	}

	want := []string{
		"desktop.header.SetTitle(Co-Worker · detecting…)",
		"desktop.app-folder.SetTitle(Open Co-Worker App Folder)",
		"desktop.data-folder.SetTitle(Open Co-Worker Data Folder)",
		"desktop.app-folder.SetEnabled(false)",
		"desktop.data-folder.SetEnabled(false)",
		"desktop.install.SetVisible(false)",
		"desktop.install.SetEnabled(false)",
		"desktop.start.SetVisible(false)",
		"desktop.start.SetEnabled(false)",
		"desktop.stop.SetVisible(false)",
		"desktop.stop.SetEnabled(false)",
		"desktop.header.SetTitle(Co-Worker · LOOP24 running)",
		"desktop.app-folder.SetTitle(Open LOOP24 App Folder)",
		"desktop.data-folder.SetTitle(Open LOOP24 Data Folder)",
		"desktop.app-folder.SetEnabled(true)",
		"desktop.data-folder.SetEnabled(true)",
		"desktop.install.SetVisible(false)",
		"desktop.install.SetEnabled(false)",
		"desktop.start.SetVisible(false)",
		"desktop.start.SetEnabled(false)",
		"desktop.stop.SetVisible(true)",
		"desktop.stop.SetEnabled(true)",
	}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("desktop operations = %v, want %v", operations, want)
	}
}

func TestGatewayMenuForOutput(t *testing.T) {
	tests := []struct {
		name string
		out  stateOutput
		want gatewayMenuModel
	}{
		{
			name: "running",
			out:  stateOutput{State: StateRunning},
			want: gatewayMenuModel{
				State:          StateRunning,
				Tooltip:        "Gateway · running",
				Header:         "Gateway · running",
				Subheader:      "http://127.0.0.1:8080",
				StopEnabled:    true,
				RestartEnabled: true,
			},
		},
		{
			name: "stopped",
			out:  stateOutput{State: StateStopped},
			want: gatewayMenuModel{
				State:        StateStopped,
				Tooltip:      "Gateway · stopped",
				Header:       "Gateway · stopped",
				Subheader:    "http://127.0.0.1:8080",
				StartEnabled: true,
			},
		},
		{
			name: "error with detail",
			out:  stateOutput{State: StateError, Detail: "/health unreachable"},
			want: gatewayMenuModel{
				State:        StateError,
				Tooltip:      "Gateway · error (/health unreachable)",
				Header:       "Gateway · error (/health unreachable)",
				Subheader:    "http://127.0.0.1:8080",
				StartEnabled: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayMenuForOutput(tc.out, "http://127.0.0.1:8080"); got != tc.want {
				t.Fatalf("gatewayMenuForOutput(%+v) = %+v, want %+v", tc.out, got, tc.want)
			}
		})
	}
}
