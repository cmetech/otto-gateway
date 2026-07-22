//go:build darwin || windows

package main

import "otto-gateway/cmd/otto-tray/icon"

func gatewayIconForState(State) []byte { return icon.Gateway }
