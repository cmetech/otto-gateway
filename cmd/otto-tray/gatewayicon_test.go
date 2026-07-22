//go:build darwin || windows

package main

import (
	"bytes"
	"testing"

	"otto-gateway/cmd/otto-tray/icon"
)

func TestGatewayIconForEveryState(t *testing.T) {
	states := []State{StateUnknown, StateStopped, StateStarting, StateRunning, StateDegraded, StateError}
	for _, state := range states {
		if got := gatewayIconForState(state); !bytes.Equal(got, icon.Gateway) {
			t.Errorf("state %s did not use Gateway icon", state)
		}
	}
}
