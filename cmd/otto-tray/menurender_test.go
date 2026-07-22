//go:build darwin || windows

package main

import (
	"reflect"
	"testing"
)

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
