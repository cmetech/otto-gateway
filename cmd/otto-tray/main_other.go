//go:build !darwin && !windows

// This stub exists so `go build ./...` from a linux CI host (which
// runs the project's standard test traversal) does not fail with
// "function main is undeclared in the main package". The tray app
// only exists on darwin/windows; on every other GOOS we produce a
// trivial binary that explains why and exits non-zero.

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "otto-tray: not supported on this platform")
	os.Exit(2)
}
